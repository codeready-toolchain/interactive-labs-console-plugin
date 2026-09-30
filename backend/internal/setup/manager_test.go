package setup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"helm.sh/helm/v3/pkg/release"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

// Embed unused Helm methods: each test implements only operations its path needs.
type managerTestHelm struct {
	HelmManager
	releases     []LabRelease
	status       string
	statusErr    error
	statusNames  []string
	steps        *[]string
	uninstallErr error
	uninstall    func(context.Context, LabRelease, []SpaceRequestResult) error
	install      func() error
	installOpts  func(context.Context, HelmInstallOpts) error
	list         func(context.Context, []SpaceRequestResult) ([]LabRelease, error)
}

func (h *managerTestHelm) ListLabReleases(ctx context.Context, namespace, token string, srs []SpaceRequestResult) ([]LabRelease, error) {
	if namespace != "alice-dev" || token != "user-token" {
		return nil, errors.New("Helm called without user namespace/token")
	}
	if h.list != nil {
		return h.list(ctx, srs)
	}
	return h.releases, nil
}

func (h *managerTestHelm) ReleaseStatus(_ context.Context, name, namespace, token string) (string, error) {
	if namespace != "alice-dev" || token != "user-token" {
		return "", errors.New("Helm called without user namespace/token")
	}
	h.statusNames = append(h.statusNames, name)
	return h.status, h.statusErr
}

func (h *managerTestHelm) UninstallLabRelease(ctx context.Context, _, _ string, target LabRelease, srs []SpaceRequestResult) error {
	if h.uninstall != nil {
		return h.uninstall(ctx, target, srs)
	}
	*h.steps = append(*h.steps, "uninstall:"+target.Name)
	return h.uninstallErr
}

func (h *managerTestHelm) Install(ctx context.Context, opts HelmInstallOpts) error {
	if h.installOpts != nil {
		return h.installOpts(ctx, opts)
	}
	if h.install != nil {
		return h.install()
	}
	if h.steps != nil {
		*h.steps = append(*h.steps, "install")
	}
	return errors.New("unexpected Helm install")
}

func TestManagerInventoryLifecycle(t *testing.T) {
	ctx := context.Background()
	ref := strings.Repeat("ab", 32)
	label, err := CatalogRefLabel(ref)
	require.NoError(t, err)
	bindings := SchemaBindings{Namespaces: []NamespaceBinding{{Key: "gitea", Path: "namespaces.gitea"}}}
	srJSON := func(failed bool) string {
		condition := `{"type":"Ready","status":"True"}`
		if failed {
			condition = `{"type":"Ready","status":"False","reason":"UnableToCreateSpace"}`
		}
		return fmt.Sprintf(`{"metadata":{"name":"gitea","namespace":"alice-dev","uid":"sr-uid","labels":{%q:%q},"annotations":{%q:"gitea"}},"status":{"namespaceAccess":[{"name":"alice-gitea"}],"conditions":[%s]}}`, CatalogRefLabelKey, label, BindingKeyAnnotation, condition)
	}
	inventory := func(t *testing.T, manager *Manager) map[string]any {
		t.Helper()
		items, err := manager.List(ctx, "alice", "user-token")
		require.NoError(t, err)
		require.Len(t, items, 1)
		payload, err := json.Marshal(items[0])
		require.NoError(t, err)
		assert.NotContains(t, string(payload), "user-token")
		var item map[string]any
		require.NoError(t, json.Unmarshal(payload, &item))
		return item
	}
	for _, tc := range []struct {
		name, releaseStatus, wantStatus                                          string
		held, unknown, missingCoordinator, cleanupBlocked, failedSR, wantBlocked bool
	}{
		{name: "deployed opens", releaseStatus: "deployed", wantStatus: "deployed"},
		{name: "cleanup-blocked deployment still opens", releaseStatus: "deployed", cleanupBlocked: true, wantStatus: "deployed", wantBlocked: true},
		{name: "failed release allows cleanup", releaseStatus: "failed", wantStatus: "failed"},
		{name: "failed release with keep policy blocks cleanup", releaseStatus: "failed", cleanupBlocked: true, wantStatus: "failed", wantBlocked: true},
		{name: "unheld pending install allows explicit cleanup", releaseStatus: "pending-install", wantStatus: "failed"},
		{name: "held pending install blocks cleanup", releaseStatus: "pending-install", held: true, wantStatus: "pending", wantBlocked: true},
		{name: "pending upgrade blocks cleanup", releaseStatus: "pending-upgrade", wantStatus: "pending", wantBlocked: true},
		{name: "SR-only abandoned setup allows cleanup", wantStatus: "failed"},
		{name: "SR-only live or crashed reservation blocks cleanup", held: true, wantStatus: "pending", wantBlocked: true},
		{name: "failed SR with held reservation blocks cleanup", held: true, failedSR: true, wantStatus: "failed", wantBlocked: true},
		{name: "failed SR without reservation allows cleanup", failedSR: true, wantStatus: "failed"},
		{name: "failed release with retained reservation blocks cleanup", releaseStatus: "failed", held: true, wantStatus: "failed", wantBlocked: true},
		{name: "unknown reservation fails closed", unknown: true, wantStatus: "pending", wantBlocked: true},
		{name: "unknown reservation blocks deployed cleanup", releaseStatus: "deployed", unknown: true, wantStatus: "deployed", wantBlocked: true},
		{name: "unknown reservation blocks failed cleanup", releaseStatus: "failed", unknown: true, wantStatus: "failed", wantBlocked: true},
		{name: "missing coordination blocks cleanup", missingCoordinator: true, wantStatus: "pending", wantBlocked: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			steps := []string{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "Bearer user-token", r.Header.Get("Authorization"))
				if r.Method != http.MethodGet {
					steps = append(steps, r.Method)
				}
				if strings.HasSuffix(r.URL.Path, "/spacerequests") {
					_, _ = fmt.Fprintf(w, `{"items":[%s]}`, srJSON(tc.failedSR))
				} else {
					_, _ = fmt.Fprint(w, srJSON(tc.failedSR))
				}
			}))
			defer server.Close()
			client := fake.NewSimpleClientset()
			coord := NewOperationCoordinator(client, "plugin-ns")
			_, releaseOther, err := coord.Acquire(ctx, "alice", strings.Repeat("cd", 32))
			require.NoError(t, err)
			defer releaseOther()
			if tc.missingCoordinator {
				coord = nil
			}
			if tc.held {
				_, release, err := coord.Acquire(ctx, "alice", ref)
				require.NoError(t, err)
				defer release()
			}
			if tc.unknown {
				client.PrependReactor("get", "leases", func(ktesting.Action) (bool, runtime.Object, error) {
					return true, nil, errors.New("reservation read unavailable user-token")
				})
			}
			helm := &managerTestHelm{steps: &steps}
			if tc.releaseStatus != "" {
				helm.releases = []LabRelease{{Name: "il-first", Namespace: "alice-dev", UID: "helm-uid", CatalogRef: ref, Status: tc.releaseStatus, CleanupBlocked: tc.cleanupBlocked}}
			}
			manager := NewManager(ManagerConfig{Helm: helm, SpaceRequests: &SpaceRequestClient{HTTPClient: server.Client(), APIHost: server.URL}, Coordinator: coord})
			item := inventory(t, manager)
			assert.Equal(t, tc.wantStatus, item["status"])
			assert.Equal(t, tc.wantBlocked, item["blocked"] == true)
			wantReason := ""
			switch {
			case tc.held || tc.unknown || tc.missingCoordinator:
				wantReason = "operation"
			case tc.releaseStatus != "" && tc.releaseStatus != "deployed" && tc.releaseStatus != "failed" && tc.releaseStatus != "pending-install":
				wantReason = "helm-state"
			case tc.cleanupBlocked:
				wantReason = "unknown"
			}
			if wantReason == "" {
				assert.NotContains(t, item, "blockedReason")
			} else {
				assert.Equal(t, wantReason, item["blockedReason"])
			}
			if tc.wantBlocked {
				assert.Error(t, awaitTeardown(t, manager, ctx, ref, item["generation"].(string)))
				assert.Empty(t, steps)
			} else {
				assert.NoError(t, awaitTeardown(t, manager, ctx, ref, item["generation"].(string)))
			}
		})
	}

	t.Run("denied SpaceRequest create leaves no setup to tear down", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "Bearer user-token", r.Header.Get("Authorization"))
			switch {
			case r.Method == http.MethodPost:
				w.WriteHeader(http.StatusForbidden)
			case strings.HasSuffix(r.URL.Path, "/spacerequests"):
				_, _ = fmt.Fprint(w, `{"items":[]}`)
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		defer server.Close()
		coord := NewOperationCoordinator(fake.NewSimpleClientset(), "plugin-ns")
		manager := NewManager(ManagerConfig{Helm: &managerTestHelm{}, SpaceRequests: &SpaceRequestClient{HTTPClient: server.Client(), APIHost: server.URL}, Coordinator: coord})

		_, ready, err := manager.Start(ctx, "alice", "user-token", ref, "chart", bindings)
		require.Error(t, err)
		assert.False(t, ready)
		assert.NotContains(t, err.Error(), "user-token")
		items, err := manager.List(ctx, "alice", "user-token")
		require.NoError(t, err)
		assert.Empty(t, items, "permission denial before resource creation must not leave a teardown target")
		assert.False(t, coord.IsHeld(ctx, "alice", ref))
	})

	for _, tc := range []struct{ retain, noSR bool }{{}, {retain: true}, {noSR: true}, {retain: true, noSR: true}} {
		t.Run(fmt.Sprintf("worker failure retains reservation=%t noSR=%t", tc.retain, tc.noSR), func(t *testing.T) {
			retain := tc.retain
			var removed atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodDelete {
					removed.Store(true)
					return
				}
				if strings.HasSuffix(r.URL.Path, "/spacerequests") {
					if tc.noSR || removed.Load() {
						_, _ = fmt.Fprint(w, `{"items":[]}`)
					} else {
						_, _ = fmt.Fprintf(w, `{"items":[%s]}`, srJSON(false))
					}
				} else if removed.Load() {
					w.WriteHeader(http.StatusNotFound)
				} else {
					_, _ = fmt.Fprint(w, srJSON(false))
				}
			}))
			defer server.Close()
			client := fake.NewSimpleClientset()
			if retain {
				client.PrependReactor("update", "leases", func(ktesting.Action) (bool, runtime.Object, error) {
					return true, nil, errors.New("release failed user-token")
				})
			}
			started, finish := make(chan struct{}), make(chan struct{})
			defer close(finish)
			helm := &managerTestHelm{install: func() error {
				close(started)
				<-finish
				return errors.New("install failed user-token")
			}}
			coord := NewOperationCoordinator(client, "plugin-ns")
			manager := NewManager(ManagerConfig{Helm: helm, SpaceRequests: &SpaceRequestClient{HTTPClient: server.Client(), APIHost: server.URL}, Coordinator: coord})
			workerBindings := bindings
			if tc.noSR {
				workerBindings = SchemaBindings{}
			}
			_, ready, err := manager.Start(ctx, "alice", "user-token", ref, "chart", workerBindings)
			require.NoError(t, err)
			assert.False(t, ready)
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("worker did not start")
			}
			item := inventory(t, manager)
			assert.Equal(t, "pending", item["status"])
			assert.Equal(t, true, item["blocked"])
			assert.Error(t, awaitTeardown(t, manager, ctx, ref, item["generation"].(string)))
			finish <- struct{}{}
			require.Eventually(t, func() bool {
				status, err := manager.Status(ctx, "alice", "user-token", ref, workerBindings)
				return err == nil && status.Helm.Status == "failed"
			}, time.Second, time.Millisecond)
			require.Eventually(t, func() bool {
				items, err := manager.List(ctx, "alice", "user-token")
				return err == nil && len(items) == 1 && items[0].Blocked == retain
			}, time.Second, time.Millisecond)
			item = inventory(t, manager)
			assert.Equal(t, "failed", item["status"])
			assert.Equal(t, retain, item["blocked"] == true)
			// Ready SRs do not hide a worker's failure; state survives manager restart
			// through the held reservation when release failed.
			if retain {
				assert.Error(t, awaitTeardown(t, manager, ctx, ref, item["generation"].(string)))
				if !tc.noSR {
					restarted := NewManager(manager.config)
					assert.Equal(t, true, inventory(t, restarted)["blocked"])
				}
			} else {
				require.NoError(t, awaitTeardown(t, manager, ctx, ref, item["generation"].(string)))
				items, err := manager.List(ctx, "alice", "user-token")
				require.NoError(t, err)
				assert.Empty(t, items, "teardown clears terminal worker inventory")
			}
		})
	}
}

func awaitTeardown(t *testing.T, manager *Manager, ctx context.Context, ref, generation string) error {
	t.Helper()
	started, err := manager.BeginTeardown(ctx, "alice", "user-token", ref, generation)
	if err != nil {
		return err
	}
	var outcome string
	require.Eventually(t, func() bool {
		progress, err := manager.TeardownStatus(context.Background(), "alice", ref, started.OperationID)
		if err != nil {
			return false
		}
		outcome = progress.Status
		return outcome == "succeeded" || outcome == "failed"
	}, 3*time.Second, time.Millisecond)
	if outcome == "failed" {
		return errors.New("teardown worker failed")
	}
	return nil
}

func TestManagerTerminatingSpaceRequests(t *testing.T) {
	ctx := context.Background()
	ref := strings.Repeat("ab", 32)
	label, err := CatalogRefLabel(ref)
	require.NoError(t, err)
	bindings := SchemaBindings{Namespaces: []NamespaceBinding{{Key: "gitea", Path: "namespaces.gitea"}}}
	t.Run("unrelated terminating lab does not block healthy deployment", func(t *testing.T) {
		otherLabel, err := CatalogRefLabel(strings.Repeat("cd", 32))
		require.NoError(t, err)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, http.MethodGet, r.Method)
			assert.Equal(t, CatalogRefLabelKey+"="+label, r.URL.Query().Get("labelSelector"))
			selectedLabel, deletion := label, ""
			if r.URL.Query().Get("labelSelector") != CatalogRefLabelKey+"="+label {
				selectedLabel, deletion = otherLabel, `,"deletionTimestamp":"2026-10-01T00:00:00Z"`
			}
			_, _ = fmt.Fprintf(w, `{"items":[{"metadata":{"name":"gitea","namespace":"alice-dev","uid":"sr-uid","labels":{%q:%q},"annotations":{%q:"gitea"}%s},"status":{"namespaceAccess":[{"name":"alice-gitea"}],"conditions":[{"type":"Ready","status":"True"}]}}]}`, CatalogRefLabelKey, selectedLabel, BindingKeyAnnotation, deletion)
		}))
		defer server.Close()
		helm := &managerTestHelm{releases: []LabRelease{{Name: "il-first", Namespace: "alice-dev", UID: "helm-uid", CatalogRef: ref, Status: "deployed"}}}
		manager := NewManager(ManagerConfig{Helm: helm, SpaceRequests: &SpaceRequestClient{HTTPClient: server.Client(), APIHost: server.URL}, Coordinator: NewOperationCoordinator(fake.NewSimpleClientset(), "plugin-ns")})
		status, ready, err := manager.Start(ctx, "alice", "user-token", ref, "chart", bindings)
		require.NoError(t, err)
		assert.True(t, ready)
		assert.Equal(t, "ready", status.SpaceRequests.Status)
		assert.Equal(t, "deployed", status.Helm.Status)
	})
	for _, tc := range []struct {
		name          string
		afterTeardown bool
		deployed      bool
		oldBinding    bool
		deleteAtGet   int32
	}{
		{name: "accepted DELETE blocks relaunch without Helm", afterTeardown: true},
		{name: "schema drift cannot hide terminating old binding", afterTeardown: true, oldBinding: true},
		{name: "existing deployed release cannot reuse terminating SR", deployed: true},
		{name: "external deletion after initial list", deleteAtGet: 1},
		{name: "external deletion after Ensure", deleteAtGet: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var terminating atomic.Bool
			terminating.Store(!tc.afterTeardown && tc.deleteAtGet == 0)
			var namedGets, mutations, installs atomic.Int32
			coord := NewOperationCoordinator(fake.NewSimpleClientset(), "plugin-ns")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "Bearer user-token", r.Header.Get("Authorization"))
				if r.Method == http.MethodDelete {
					mutations.Add(1)
					terminating.Store(true)
					w.WriteHeader(http.StatusAccepted)
					return
				}
				if r.Method != http.MethodGet {
					mutations.Add(1)
					w.WriteHeader(http.StatusConflict)
					return
				}
				list := strings.HasSuffix(r.URL.Path, "/spacerequests")
				if !list {
					count := namedGets.Add(1)
					if tc.deleteAtGet > 0 && count >= tc.deleteAtGet {
						terminating.Store(true)
					}
				}
				deletion := ""
				if terminating.Load() {
					deletion = `,"deletionTimestamp":"2026-10-01T00:00:00Z"`
				}
				sr := fmt.Sprintf(`{"metadata":{"name":"gitea","namespace":"alice-dev","uid":"sr-uid","labels":{%q:%q},"annotations":{%q:"gitea"}%s},"status":{"namespaceAccess":[{"name":"alice-gitea"}],"conditions":[{"type":"Ready","status":"True"}]}}`, CatalogRefLabelKey, label, BindingKeyAnnotation, deletion)
				if list {
					_, _ = fmt.Fprintf(w, `{"items":[%s]}`, sr)
				} else {
					_, _ = fmt.Fprint(w, sr)
				}
			}))
			defer server.Close()
			helm := &managerTestHelm{install: func() error { installs.Add(1); return nil }}
			if tc.deployed {
				helm.releases = []LabRelease{{Name: "il-first", Namespace: "alice-dev", UID: "helm-uid", CatalogRef: ref, Status: "deployed"}}
				helm.status = "deployed"
			}
			manager := NewManager(ManagerConfig{Helm: helm, SpaceRequests: &SpaceRequestClient{HTTPClient: server.Client(), APIHost: server.URL}, Coordinator: coord})
			if tc.afterTeardown {
				items, err := manager.List(ctx, "alice", "user-token")
				require.NoError(t, err)
				require.Len(t, items, 1)
				require.NoError(t, awaitTeardown(t, manager, ctx, ref, items[0].Generation))
				assert.True(t, terminating.Load(), "DELETE accepted while finalizers retain the SR")
				assert.False(t, coord.IsHeld(ctx, "alice", ref), "successful teardown releases the reservation")
				manager = NewManager(manager.config) // Admission must work across pods, without worker memory.
				for _, held := range []bool{false, true} {
					release := func() {}
					if held {
						_, release, err = coord.Acquire(ctx, "alice", ref)
						require.NoError(t, err)
					}
					items, err = manager.List(ctx, "alice", "user-token")
					release()
					require.NoError(t, err)
					require.Len(t, items, 1)
					assert.Equal(t, "pending", items[0].Status, "finalizers still cleaning up, not setup failure")
					assert.True(t, items[0].Blocked)
					assert.EqualValues(t, "cleanup", items[0].BlockedReason, "terminating SR cleanup wins over held=%t", held)
				}
			}
			before := mutations.Load()
			namedGets.Store(0)
			startBindings := bindings
			if tc.oldBinding {
				startBindings = SchemaBindings{Namespaces: []NamespaceBinding{{Key: "new-binding", Path: "namespaces.new"}}}
			}
			_, ready, err := manager.Start(ctx, "alice", "user-token", ref, "chart", startBindings)
			if err == nil && !ready {
				require.Eventually(t, func() bool { return !coord.IsHeld(ctx, "alice", ref) }, time.Second, time.Millisecond)
			}
			var managerErr *ManagerError
			require.ErrorAs(t, err, &managerErr)
			assert.Equal(t, ManagerConflict, managerErr.Kind)
			assert.False(t, ready)
			assert.Equal(t, before, mutations.Load())
			if tc.deleteAtGet == 0 {
				assert.Zero(t, namedGets.Load(), "preflight rejects before SR adoption or worker handoff")
			}
			assert.Zero(t, installs.Load())
			assert.False(t, coord.IsHeld(ctx, "alice", ref), "no background worker owns the reservation")
			status, err := manager.Status(ctx, "alice", "user-token", ref, bindings)
			require.NoError(t, err)
			assert.Equal(t, "provisioning", status.SpaceRequests.Status)
			assert.Equal(t, "pending", status.SpaceRequests.Namespaces["gitea"].Status)
			assert.Empty(t, status.Attributes)
			if tc.afterTeardown {
				items, err := manager.List(ctx, "alice", "user-token")
				require.NoError(t, err)
				require.Len(t, items, 1)
				_, err = manager.BeginTeardown(ctx, "alice", "user-token", ref, items[0].Generation)
				var repeatErr *ManagerError
				require.ErrorAs(t, err, &repeatErr)
				assert.Equal(t, ManagerConflict, repeatErr.Kind)
			}
		})
	}
}

func TestManager(t *testing.T) {
	ctx := context.Background()
	ref := strings.Repeat("ab", 32)
	label, err := CatalogRefLabel(ref)
	require.NoError(t, err)
	bindings := SchemaBindings{Namespaces: []NamespaceBinding{{Key: "gitea", Path: "namespaces.gitea"}}}
	newManager := func(server *httptest.Server, helm HelmManager) *Manager {
		return NewManager(ManagerConfig{
			Helm: helm, SpaceRequests: &SpaceRequestClient{HTTPClient: server.Client(), APIHost: server.URL},
			Coordinator: NewOperationCoordinator(fake.NewSimpleClientset(), "plugin-ns"), TierName: "base1ns",
		})
	}

	t.Run("duplicate releases block SR mutations", func(t *testing.T) {
		mutations := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				mutations++
			}
			w.WriteHeader(http.StatusNotFound)
		}))
		defer server.Close()
		helm := &managerTestHelm{releases: []LabRelease{
			{Name: "il-first", Namespace: "alice-dev", CatalogRef: ref, Status: "deployed", UID: "uid-1", Revision: 1},
			{Name: "il-second", Namespace: "alice-dev", CatalogRef: ref, Status: "deployed", UID: "uid-2", Revision: 1},
		}}
		_, _, err := newManager(server, helm).Start(ctx, "alice", "user-token", ref, "chart", bindings)
		require.Error(t, err)
		assert.Zero(t, mutations)
	})

	t.Run("cleanup-blocked deployed release remains openable", func(t *testing.T) {
		steps := []string{}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "Bearer user-token", r.Header.Get("Authorization"))
			if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/spacerequests") {
				steps = append(steps, "unexpected SR request: "+r.Method+" "+r.URL.Path)
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = fmt.Fprintf(w, `{"items":[{"metadata":{"name":"gitea","namespace":"alice-dev","uid":"sr-uid","labels":{%q:%q},"annotations":{%q:"gitea"}},"status":{"namespaceAccess":[{"name":"alice-gitea"}],"conditions":[{"type":"Ready","status":"True"}]}}]}`, CatalogRefLabelKey, label, BindingKeyAnnotation)
		}))
		defer server.Close()
		helm := &managerTestHelm{steps: &steps, releases: []LabRelease{{Name: "il-first", Namespace: "alice-dev", UID: "helm-uid", CatalogRef: ref, Status: "deployed", CleanupBlocked: true}}}
		manager := newManager(server, helm)
		status, ready, err := manager.Start(ctx, "alice", "user-token", ref, "chart", bindings)
		require.NoError(t, err)
		assert.True(t, ready)
		assert.Equal(t, "ready", status.SpaceRequests.Status)
		assert.Equal(t, "deployed", status.Helm.Status)
		assert.Equal(t, "il-first", status.Helm.ReleaseName)
		assert.Empty(t, steps, "opening a deployed release must not install Helm or mutate SpaceRequests")
	})

	t.Run("new SR must have verified UID before worker handoff", func(t *testing.T) {
		created := false
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "Bearer user-token", r.Header.Get("Authorization"))
			switch r.Method {
			case http.MethodGet:
				if strings.HasSuffix(r.URL.Path, "/spacerequests") {
					_, _ = fmt.Fprint(w, `{"items":[]}`)
					return
				}
				if !created {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				_, _ = fmt.Fprintf(w, `{"metadata":{"name":"gitea","namespace":"alice-dev","labels":{%q:%q},"annotations":{%q:"gitea"}}}`, CatalogRefLabelKey, label, BindingKeyAnnotation)
			case http.MethodPost:
				var body struct {
					Metadata struct {
						Name        string            `json:"name"`
						Namespace   string            `json:"namespace"`
						Labels      map[string]string `json:"labels"`
						Annotations map[string]string `json:"annotations"`
					} `json:"metadata"`
				}
				require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
				assert.Equal(t, "gitea", body.Metadata.Name)
				assert.Equal(t, "alice-dev", body.Metadata.Namespace)
				assert.Equal(t, label, body.Metadata.Labels[CatalogRefLabelKey])
				assert.Equal(t, "gitea", body.Metadata.Annotations[BindingKeyAnnotation])
				created = true
				w.WriteHeader(http.StatusCreated)
			default:
				t.Errorf("unexpected SR request %s", r.Method)
			}
		}))
		defer server.Close()
		_, _, err := newManager(server, &managerTestHelm{}).Start(ctx, "alice", "user-token", ref, "chart", bindings)
		require.Error(t, err, "new SR without UID cannot be handed to background worker")
		assert.True(t, created)
	})

	t.Run("stale generation blocks cleanup; valid generation uninstalls Helm before deleting SR", func(t *testing.T) {
		steps := []string{}
		sr := fmt.Sprintf(`{"metadata":{"name":"gitea","namespace":"alice-dev","uid":"sr-uid","labels":{%q:%q},"annotations":{%q:"gitea"}}}`, CatalogRefLabelKey, label, BindingKeyAnnotation)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "Bearer user-token", r.Header.Get("Authorization"))
			switch r.Method {
			case http.MethodGet:
				if strings.HasSuffix(r.URL.Path, "/spacerequests") {
					_, _ = fmt.Fprintf(w, `{"items":[%s]}`, sr)
				} else {
					_, _ = fmt.Fprint(w, sr)
				}
			case http.MethodDelete:
				var options struct {
					Preconditions struct {
						UID string `json:"uid"`
					} `json:"preconditions"`
				}
				require.NoError(t, json.NewDecoder(r.Body).Decode(&options))
				assert.Equal(t, "sr-uid", options.Preconditions.UID)
				steps = append(steps, "delete:gitea")
			default:
				t.Errorf("unexpected SR request %s", r.Method)
			}
		}))
		defer server.Close()
		helm := &managerTestHelm{steps: &steps, releases: []LabRelease{{Name: "il-first", Namespace: "alice-dev", CatalogRef: ref, Status: "deployed", UID: "helm-uid", Revision: 1}}}
		manager := newManager(server, helm)
		items, err := manager.List(ctx, "alice", "user-token")
		require.NoError(t, err)
		require.Len(t, items, 1)
		listed, err := json.Marshal(items[0])
		require.NoError(t, err)
		var confirmation struct {
			CatalogRef string `json:"catalogRef"`
			Generation string `json:"generation"`
		}
		require.NoError(t, json.Unmarshal(listed, &confirmation))
		assert.Equal(t, ref, confirmation.CatalogRef)
		require.NotEmpty(t, confirmation.Generation)
		require.Error(t, awaitTeardown(t, manager, ctx, ref, "stale"))
		assert.Empty(t, steps)
		require.NoError(t, awaitTeardown(t, manager, ctx, ref, confirmation.Generation))
		assert.Equal(t, []string{"uninstall:il-first", "delete:gitea"}, steps)
	})

	t.Run("status exposes ready namespaces and deployed Helm with attributes", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "Bearer user-token", r.Header.Get("Authorization"))
			_, _ = fmt.Fprintf(w, `{"metadata":{"name":"gitea","namespace":"alice-dev","uid":"sr-uid","labels":{%q:%q},"annotations":{%q:"gitea"}},"status":{"namespaceAccess":[{"name":"alice-gitea"}],"conditions":[{"type":"Ready","status":"True"}]}}`, CatalogRefLabelKey, label, BindingKeyAnnotation)
		}))
		defer server.Close()
		helm := &managerTestHelm{status: "deployed", releases: []LabRelease{{Name: HelmReleaseName("alice", ref, bindings.Namespaces), Namespace: "alice-dev", CatalogRef: ref, UID: "helm-uid", Status: "deployed"}}}
		status, err := newManager(server, helm).Status(ctx, "alice", "user-token", ref, bindings)
		require.NoError(t, err)
		payload, err := json.Marshal(status)
		require.NoError(t, err)
		var actual struct {
			SpaceRequests struct {
				Status     string `json:"status"`
				Namespaces map[string]struct {
					Status string `json:"status"`
					Name   string `json:"name"`
				} `json:"namespaces"`
			} `json:"spaceRequests"`
			Helm struct {
				Status      string `json:"status"`
				ReleaseName string `json:"releaseName"`
			} `json:"helm"`
			Attributes map[string]string `json:"attributes"`
		}
		require.NoError(t, json.Unmarshal(payload, &actual))
		assert.Equal(t, "ready", actual.SpaceRequests.Status)
		assert.Equal(t, "ready", actual.SpaceRequests.Namespaces["gitea"].Status)
		assert.Equal(t, "alice-gitea", actual.SpaceRequests.Namespaces["gitea"].Name)
		assert.Equal(t, "deployed", actual.Helm.Status)
		assert.Equal(t, HelmReleaseName("alice", ref, bindings.Namespaces), actual.Helm.ReleaseName)
		assert.Equal(t, "alice-gitea", actual.Attributes["namespaces-gitea"])
	})

	t.Run("status read errors", func(t *testing.T) {
		for _, tc := range []struct {
			name      string
			srStatus  int
			listErr   error
			wantError bool
		}{
			{name: "SR NotFound remains pending", srStatus: http.StatusNotFound},
			{name: "SR forbidden surfaces error", srStatus: http.StatusForbidden, wantError: true},
			{name: "Helm snapshot discovery failure surfaces error", srStatus: http.StatusOK, listErr: errors.New("snapshot read failed user-token"), wantError: true},
		} {
			t.Run(tc.name, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					assert.Equal(t, http.MethodGet, r.Method)
					assert.Equal(t, "/apis/toolchain.dev.openshift.com/v1alpha1/namespaces/alice-dev/spacerequests/gitea", r.URL.Path)
					assert.Equal(t, "Bearer user-token", r.Header.Get("Authorization"))
					w.WriteHeader(tc.srStatus)
					if tc.srStatus == http.StatusOK {
						_, _ = fmt.Fprintf(w, `{"metadata":{"name":"gitea","namespace":"alice-dev","uid":"sr-uid","labels":{%q:%q},"annotations":{%q:"gitea"}},"status":{"namespaceAccess":[{"name":"alice-gitea"}],"conditions":[{"type":"Ready","status":"True"}]}}`, CatalogRefLabelKey, label, BindingKeyAnnotation)
					}
				}))
				defer server.Close()
				helm := &managerTestHelm{list: func(context.Context, []SpaceRequestResult) ([]LabRelease, error) {
					return nil, tc.listErr
				}}
				statusBindings := bindings
				statusBindings.Passwords = []ValueBinding{{Key: "gitea.password", Path: "gitea.adminPassword", Kind: KindGeneratedPassword}}
				status, err := newManager(server, helm).Status(ctx, "alice", "user-token", ref, statusBindings)
				if tc.wantError {
					require.Error(t, err, "read failure must not be reported as pending or as a usable deployment")
					var managerErr *ManagerError
					require.ErrorAs(t, err, &managerErr)
					assert.Equal(t, ManagerBadGateway, managerErr.Kind)
					assert.NotContains(t, err.Error(), "user-token")
					assert.Empty(t, status.Attributes, "failed reads must not expose incomplete launch attributes")
					assert.Empty(t, status.Helm.GeneratedValues)
					return
				}
				require.NoError(t, err)
				assert.Equal(t, "provisioning", status.SpaceRequests.Status)
				assert.Equal(t, NamespaceStatus{Status: "pending", Name: "gitea"}, status.SpaceRequests.Namespaces["gitea"])
				assert.Equal(t, "pending", status.Helm.Status)
				assert.Empty(t, status.Attributes)
			})
		}
	})

	t.Run("status works without Helm manager", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "Bearer user-token", r.Header.Get("Authorization"))
			_, _ = fmt.Fprintf(w, `{"metadata":{"name":"gitea","namespace":"alice-dev","uid":"sr-uid","labels":{%q:%q},"annotations":{%q:"gitea"}},"status":{"namespaceAccess":[{"name":"alice-gitea"}],"conditions":[{"type":"Ready","status":"True"}]}}`, CatalogRefLabelKey, label, BindingKeyAnnotation)
		}))
		defer server.Close()
		status, err := newManager(server, nil).Status(ctx, "alice", "user-token", ref, bindings)
		require.NoError(t, err)
		assert.Equal(t, "ready", status.SpaceRequests.Status)
		assert.Equal(t, NamespaceStatus{Status: "ready", Name: "alice-gitea"}, status.SpaceRequests.Namespaces["gitea"])
		assert.Equal(t, "pending", status.Helm.Status)
		assert.Equal(t, HelmReleaseName("alice", ref, bindings.Namespaces), status.Helm.ReleaseName)
	})
}

type slowTeardownHelm struct {
	HelmManager
	release LabRelease
	started chan struct{}
	proceed chan struct{}
	fail    bool
}

func (h *slowTeardownHelm) ListLabReleases(_ context.Context, _, token string, _ []SpaceRequestResult) ([]LabRelease, error) {
	if token != "user-token" {
		return nil, fmt.Errorf("missing user token")
	}
	return []LabRelease{h.release}, nil
}

func (h *slowTeardownHelm) UninstallLabRelease(_ context.Context, _, _ string, _ LabRelease, _ []SpaceRequestResult) error {
	close(h.started)
	<-h.proceed
	if h.fail {
		return fmt.Errorf("uninstall failed")
	}
	return nil
}

func TestManagerAsyncTeardown(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		fail       bool
	}{
		{name: "completed", want: "succeeded"},
		{name: "failed uninstall", want: "failed", fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ref := strings.Repeat("ab", 32)
			label, err := CatalogRefLabel(ref)
			require.NoError(t, err)
			sr := fmt.Sprintf(`{"metadata":{"name":"gitea","namespace":"alice-dev","uid":"sr-uid","labels":{%q:%q},"annotations":{%q:"gitea"}}}`, CatalogRefLabelKey, label, BindingKeyAnnotation)
			deleted := make(chan struct{}, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "Bearer user-token", r.Header.Get("Authorization"))
				switch r.Method {
				case http.MethodGet:
					if strings.HasSuffix(r.URL.Path, "/spacerequests") {
						_, _ = fmt.Fprintf(w, `{"items":[%s]}`, sr)
					} else {
						_, _ = fmt.Fprint(w, sr)
					}
				case http.MethodDelete:
					deleted <- struct{}{}
				default:
					t.Errorf("unexpected SR request %s", r.Method)
				}
			}))
			t.Cleanup(server.Close)
			helm := &slowTeardownHelm{
				release: LabRelease{Name: "il-lab", Namespace: "alice-dev", CatalogRef: ref, Status: "deployed", UID: "helm-uid", Revision: 1},
				started: make(chan struct{}), proceed: make(chan struct{}), fail: tc.fail,
			}
			coord := NewOperationCoordinator(fake.NewSimpleClientset(), "plugin-ns")
			manager := NewManager(ManagerConfig{Helm: helm, SpaceRequests: &SpaceRequestClient{HTTPClient: server.Client(), APIHost: server.URL}, Coordinator: coord})
			items, err := manager.List(context.Background(), "alice", "user-token")
			require.NoError(t, err)
			require.Len(t, items, 1)
			_, err = manager.BeginTeardown(context.Background(), "alice", "user-token", ref, "stale")
			require.Error(t, err)
			assert.Empty(t, deleted)
			requestCtx, cancel := context.WithCancel(context.Background())
			status, err := manager.BeginTeardown(requestCtx, "alice", "user-token", ref, items[0].Generation)
			require.NoError(t, err)
			cancel()
			assert.Equal(t, "running", status.Status)
			require.Len(t, status.OperationID, 32)
			select {
			case <-helm.started:
			case <-time.After(2 * time.Second):
				t.Fatal("background uninstall did not start")
			}
			otherPod := NewManager(ManagerConfig{Coordinator: NewOperationCoordinator(coord.client, "plugin-ns")})
			observed, err := otherPod.TeardownStatus(context.Background(), "alice", ref, status.OperationID)
			require.NoError(t, err)
			assert.Equal(t, "running", observed.Status)
			discovered, err := otherPod.TeardownStatus(context.Background(), "alice", ref, "")
			require.NoError(t, err)
			assert.Equal(t, status.OperationID, discovered.OperationID)
			assert.Equal(t, "running", discovered.Status)
			assert.Empty(t, deleted, "SRs must not be deleted while Helm is running")
			_, err = manager.BeginTeardown(context.Background(), "alice", "user-token", ref, items[0].Generation)
			require.Error(t, err, "same lab may not overlap")
			close(helm.proceed)
			require.Eventually(t, func() bool {
				observed, err = otherPod.TeardownStatus(context.Background(), "alice", ref, status.OperationID)
				return err == nil && observed.Status == tc.want
			}, 3*time.Second, 10*time.Millisecond)
			if tc.fail {
				assert.Empty(t, deleted)
			} else {
				assert.Len(t, deleted, 1)
			}
			_, err = manager.BeginTeardown(context.Background(), "alice", "user-token", ref, "stale")
			require.Error(t, err)
			observed, err = otherPod.TeardownStatus(context.Background(), "alice", ref, status.OperationID)
			require.NoError(t, err, "rejected request must not replace previous receipt")
			assert.Equal(t, tc.want, observed.Status)
		})
	}
}

func TestManagerHookInventory(t *testing.T) {
	ref := strings.Repeat("ab", 32)
	label, err := CatalogRefLabel(ref)
	require.NoError(t, err)
	for _, tc := range []struct{ name, namespace, reason string }{
		{"dedicated Job allowed", "alice-gitea", ""},
		{"home Job blocked", "alice-dev", "helm-hook"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, http.MethodGet, r.Method)
				assert.Equal(t, "Bearer user-token", r.Header.Get("Authorization"))
				_, _ = fmt.Fprintf(w, `{"items":[{"metadata":{"name":"gitea","namespace":"alice-dev","uid":"sr-uid","labels":{%q:%q},"annotations":{%q:"gitea"}},"status":{"conditions":[{"type":"Ready","status":"True"}],"namespaceAccess":[{"name":"alice-gitea"}]}}]}`, CatalogRefLabelKey, label, BindingKeyAnnotation)
			}))
			t.Cleanup(srs.Close)
			helm, mutations := cleanupTestHelm(t, &release.Release{
				Name: "il-lab", Namespace: "alice-dev", Version: 1,
				Labels: map[string]string{CatalogRefLabelKey: label}, Info: &release.Info{Status: release.StatusDeployed},
				Hooks: []*release.Hook{{Kind: "Job", Manifest: fmt.Sprintf("apiVersion: batch/v1\nkind: Job\nmetadata:\n  name: hook\n  namespace: %s\n", tc.namespace)}},
			}, "helm-uid")
			manager := NewManager(ManagerConfig{Helm: helm, SpaceRequests: &SpaceRequestClient{APIHost: srs.URL, HTTPClient: srs.Client()}, Coordinator: NewOperationCoordinator(fake.NewSimpleClientset(), "plugin-ns")})
			items, err := manager.List(context.Background(), "alice", "user-token")
			require.NoError(t, err)
			require.Len(t, items, 1)
			assert.Equal(t, tc.reason != "", items[0].Blocked)
			assert.EqualValues(t, tc.reason, items[0].BlockedReason)
			payload, err := json.Marshal(items[0])
			require.NoError(t, err)
			assert.NotContains(t, string(payload), "user-token")
			assert.NotContains(t, string(payload), "apiVersion")
			assert.NotContains(t, string(payload), "alice-gitea")
			assert.Empty(t, *mutations)
		})
	}
}

func TestManagerHookTeardownRechecksSpaceRequests(t *testing.T) {
	ctx := context.Background()
	ref := strings.Repeat("ab", 32)
	label, err := CatalogRefLabel(ref)
	require.NoError(t, err)
	for _, tc := range []struct{ name, ready, target string }{
		{"readiness lost before uninstall", "False", "alice-gitea"},
		{"target changed before uninstall", "True", "replacement"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srJSON := func(ready, target string) string {
				return fmt.Sprintf(`{"metadata":{"name":"gitea","namespace":"alice-dev","uid":"sr-uid","labels":{%q:%q},"annotations":{%q:"gitea"}},"status":{"conditions":[{"type":"Ready","status":%q}],"namespaceAccess":[{"name":%q}]}}`, CatalogRefLabelKey, label, BindingKeyAnnotation, ready, target)
			}
			var srLists atomic.Int32
			srs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, http.MethodGet, r.Method, "blocked Helm must not delete SRs")
				assert.Equal(t, "Bearer user-token", r.Header.Get("Authorization"))
				if strings.HasSuffix(r.URL.Path, "/spacerequests") {
					current := srJSON("True", "alice-gitea")
					if srLists.Add(1) > 3 {
						current = srJSON(tc.ready, tc.target)
					}
					_, _ = fmt.Fprintf(w, `{"items":[%s]}`, current)
				} else {
					_, _ = fmt.Fprint(w, srJSON(tc.ready, tc.target))
				}
			}))
			t.Cleanup(srs.Close)
			helm, mutations := cleanupTestHelm(t, &release.Release{
				Name: "il-lab", Namespace: "alice-dev", Version: 1,
				Labels: map[string]string{CatalogRefLabelKey: label}, Info: &release.Info{Status: release.StatusDeployed},
				Hooks: []*release.Hook{{Kind: "Job", Manifest: "apiVersion: batch/v1\nkind: Job\nmetadata:\n  name: hook\n  namespace: alice-gitea\n"}},
			}, "helm-uid")
			manager := NewManager(ManagerConfig{Helm: helm, SpaceRequests: &SpaceRequestClient{APIHost: srs.URL, HTTPClient: srs.Client()}, Coordinator: NewOperationCoordinator(fake.NewSimpleClientset(), "plugin-ns")})
			items, err := manager.List(ctx, "alice", "user-token")
			require.NoError(t, err)
			require.Len(t, items, 1)
			require.False(t, items[0].Blocked)
			err = awaitTeardown(t, manager, ctx, ref, items[0].Generation)
			require.Error(t, err)
			assert.NotContains(t, err.Error(), "user-token")
			assert.Empty(t, *mutations)
		})
	}
}

func TestManagerCleanupReasonCodes(t *testing.T) {
	ctx := context.Background()
	ref := strings.Repeat("ab", 32)
	for _, tc := range []struct {
		name, status  string
		reason        BlockedReason
		blocked, held bool
		want          BlockedReason
	}{
		{name: "eligible omits reason", status: "deployed"},
		{name: "hook", status: "deployed", blocked: true, reason: BlockedHelmHook, want: BlockedHelmHook},
		{name: "retained", status: "failed", blocked: true, reason: BlockedHelmRetained, want: BlockedHelmRetained},
		{name: "manifest", status: "deployed", blocked: true, reason: BlockedHelmManifest, want: BlockedHelmManifest},
		{name: "unknown", status: "deployed", blocked: true, want: BlockedUnknown},
		{name: "raw reason suppressed", status: "deployed", blocked: true, reason: "raw Helm user-token", want: BlockedUnknown},
		{name: "unheld pending install permits confirmed cleanup", status: "pending-install"},
		{name: "pending install with unsafe hook blocked", status: "pending-install", blocked: true, reason: BlockedHelmHook, want: BlockedHelmState},
		{name: "pending upgrade", status: "pending-upgrade", want: BlockedHelmState},
		{name: "pending rollback", status: "pending-rollback", want: BlockedHelmState},
		{name: "unknown Helm state", status: "unknown", want: BlockedHelmState},
		{name: "operation overrides hook", status: "deployed", blocked: true, reason: BlockedHelmHook, held: true, want: BlockedOperation},
		{name: "operation overrides Helm state", status: "pending-install", held: true, want: BlockedOperation},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, http.MethodGet, r.Method)
				_, _ = fmt.Fprint(w, `{"items":[]}`)
			}))
			t.Cleanup(server.Close)
			coord := NewOperationCoordinator(fake.NewSimpleClientset(), "plugin-ns")
			if tc.held {
				_, release, err := coord.Acquire(ctx, "alice", ref)
				require.NoError(t, err)
				t.Cleanup(release)
			}
			helm := &managerTestHelm{}
			if tc.status != "" {
				helm.releases = []LabRelease{{Name: "il-lab", Namespace: "alice-dev", UID: "helm-uid", CatalogRef: ref, Status: tc.status, CleanupBlocked: tc.blocked, BlockedReason: tc.reason}}
			}
			manager := NewManager(ManagerConfig{Helm: helm, SpaceRequests: &SpaceRequestClient{APIHost: server.URL, HTTPClient: server.Client()}, Coordinator: coord})
			items, err := manager.List(ctx, "alice", "user-token")
			require.NoError(t, err)
			require.Len(t, items, 1)
			assert.Equal(t, tc.want != "", items[0].Blocked)
			assert.Equal(t, tc.want, items[0].BlockedReason)
			payload, err := json.Marshal(items[0])
			require.NoError(t, err)
			assert.NotContains(t, string(payload), "user-token")
			if tc.want == "" {
				assert.NotContains(t, string(payload), "blockedReason")
			}
		})
	}
}

// Recovery must use the new request's token for discovery and worker handoff.
type managerRecoveryHelm struct {
	HelmManager
	t        *testing.T
	status   string
	releases []LabRelease
	install  func(HelmInstallOpts) error
}

func (h *managerRecoveryHelm) ListLabReleases(_ context.Context, namespace, token string, _ []SpaceRequestResult) ([]LabRelease, error) {
	assert.Equal(h.t, "alice-dev", namespace)
	assert.Equal(h.t, "fresh-token", token)
	return h.releases, nil
}

func (h *managerRecoveryHelm) ReleaseStatus(_ context.Context, _, namespace, token string) (string, error) {
	assert.Equal(h.t, "alice-dev", namespace)
	assert.Equal(h.t, "fresh-token", token)
	return h.status, nil
}

func (h *managerRecoveryHelm) Install(_ context.Context, opts HelmInstallOpts) error {
	assert.Equal(h.t, "fresh-token", opts.Token)
	if h.install == nil {
		assert.Fail(h.t, "unexpected Helm install")
		return fmt.Errorf("unexpected Helm install")
	}
	return h.install(opts)
}

type managerRecoveryCleanupHelm struct {
	HelmManager
	t       *testing.T
	release LabRelease
	steps   *[]string
}

func (h *managerRecoveryCleanupHelm) ListLabReleases(_ context.Context, _, token string, _ []SpaceRequestResult) ([]LabRelease, error) {
	assert.Equal(h.t, "fresh-token", token)
	return []LabRelease{h.release}, nil
}

func (h *managerRecoveryCleanupHelm) UninstallLabRelease(_ context.Context, _, token string, target LabRelease, _ []SpaceRequestResult) error {
	assert.Equal(h.t, "fresh-token", token)
	*h.steps = append(*h.steps, "uninstall:"+target.Name)
	return nil
}

type managerRecoveryLossHelm struct {
	HelmManager
	coordinator *OperationCoordinator
	cancelled   chan bool
}

func (h *managerRecoveryLossHelm) ListLabReleases(ctx context.Context, _, _ string, _ []SpaceRequestResult) ([]LabRelease, error) {
	leases := h.coordinator.client.CoordinationV1().Leases("plugin-ns")
	current, err := leases.Get(ctx, operationLeaseName("alice", strings.Repeat("ab", 32)), metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	successor := strings.Repeat("f", 32)
	current.Spec.HolderIdentity = &successor
	if _, err = leases.Update(ctx, current, metav1.UpdateOptions{}); err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		h.cancelled <- true
		return nil, ctx.Err()
	case <-time.After(22 * time.Second):
		h.cancelled <- false
		return nil, fmt.Errorf("preflight ignored lost ownership")
	}
}

func TestManagerRestartRecovery(t *testing.T) {
	ctx := context.Background()
	ref := strings.Repeat("ab", 32)
	label, err := CatalogRefLabel(ref)
	require.NoError(t, err)
	bindings := SchemaBindings{Namespaces: []NamespaceBinding{{Key: "gitea", Path: "namespaces.gitea"}}}
	stale := metav1.NewMicroTime(time.Now().Add(-201 * time.Second))
	fresh := metav1.NewMicroTime(time.Now())
	newManager := func(t *testing.T, helm HelmManager, renewed *metav1.MicroTime) *Manager {
		t.Helper()
		client := coordinatorTestClient(coordinatorHeldLease(t, ref, renewed))
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "Bearer fresh-token", r.Header.Get("Authorization"))
			if !assert.Equal(t, http.MethodGet, r.Method, "recovery must reuse existing SRs without mutation") {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			sr := fmt.Sprintf(`{"metadata":{"name":"gitea","namespace":"alice-dev","uid":"sr-uid","labels":{%q:%q},"annotations":{%q:"gitea"}},"status":{"namespaceAccess":[{"name":"alice-gitea"}],"conditions":[{"type":"Ready","status":"True"}]}}`, CatalogRefLabelKey, label, BindingKeyAnnotation)
			if strings.HasSuffix(r.URL.Path, "/spacerequests") {
				assert.Equal(t, CatalogRefLabelKey+"="+label, r.URL.Query().Get("labelSelector"))
				_, _ = fmt.Fprintf(w, `{"items":[%s]}`, sr)
			} else {
				assert.True(t, strings.HasSuffix(r.URL.Path, "/spacerequests/gitea"))
				_, _ = fmt.Fprint(w, sr)
			}
		}))
		t.Cleanup(server.Close)
		return NewManager(ManagerConfig{
			Helm: helm, SpaceRequests: &SpaceRequestClient{HTTPClient: server.Client(), APIHost: server.URL},
			Coordinator: NewOperationCoordinator(client, "plugin-ns"),
		})
	}

	t.Run("status offers retry only for expired reservation without Helm release", func(t *testing.T) {
		for _, tc := range []struct {
			name       string
			renewed    *metav1.MicroTime
			helmStatus string
			wantRetry  bool
		}{
			{"stale without release", &stale, "", true},
			{"live without release", &fresh, "", false},
			{"deployed release", &stale, "deployed", false},
			{"pending release", &stale, "pending-install", false},
			{"failed release", &stale, "failed", false},
		} {
			helm := &managerRecoveryHelm{t: t, status: tc.helmStatus}
			if tc.helmStatus != "" {
				helm.releases = []LabRelease{{Name: "il-recovered", Namespace: "alice-dev", CatalogRef: ref, UID: "helm-uid", Status: tc.helmStatus}}
			}
			manager := newManager(t, helm, tc.renewed)
			status, err := manager.Status(ctx, "alice", "fresh-token", ref, bindings)
			require.NoError(t, err, tc.name)
			assert.Equal(t, "ready", status.SpaceRequests.Status, tc.name)
			// Specify RetrySetup's json:"retrySetup,omitempty" contract while
			// keeping the red test runnable before the field is implemented.
			payload, err := json.Marshal(status)
			require.NoError(t, err)
			var wire map[string]any
			require.NoError(t, json.Unmarshal(payload, &wire))
			if tc.wantRetry {
				assert.Equal(t, true, wire["retrySetup"], tc.name)
			} else {
				assert.NotContains(t, wire, "retrySetup", tc.name)
			}
			if tc.helmStatus == "pending-install" {
				assert.Equal(t, "failed", status.Helm.Status, "an orphaned Helm install must not spin forever")
				assert.NotEmpty(t, status.Helm.Message)
			}
			assert.NotContains(t, string(payload), "fresh-token")
		}
	})

	t.Run("interrupted pending install can be explicitly torn down without administrator", func(t *testing.T) {
		steps := []string{}
		release := LabRelease{Name: "il-lab", Namespace: "alice-dev", UID: "helm-uid", CatalogRef: ref, Status: "pending-install", Revision: 1}
		helm := &managerRecoveryCleanupHelm{t: t, release: release, steps: &steps}
		sr := fmt.Sprintf(`{"metadata":{"name":"gitea","namespace":"alice-dev","uid":"sr-uid","labels":{%q:%q},"annotations":{%q:"gitea"}},"status":{"namespaceAccess":[{"name":"alice-gitea"}],"conditions":[{"type":"Ready","status":"True"}]}}`, CatalogRefLabelKey, label, BindingKeyAnnotation)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "Bearer fresh-token", r.Header.Get("Authorization"))
			switch r.Method {
			case http.MethodGet:
				if strings.HasSuffix(r.URL.Path, "/spacerequests") {
					_, _ = fmt.Fprintf(w, `{"items":[%s]}`, sr)
				} else {
					_, _ = fmt.Fprint(w, sr)
				}
			case http.MethodDelete:
				steps = append(steps, "delete:gitea")
			default:
				t.Errorf("unexpected SpaceRequest call %s", r.Method)
			}
		}))
		t.Cleanup(server.Close)
		manager := NewManager(ManagerConfig{Helm: helm, SpaceRequests: &SpaceRequestClient{HTTPClient: server.Client(), APIHost: server.URL}, Coordinator: NewOperationCoordinator(coordinatorTestClient(coordinatorHeldLease(t, ref, &stale)), "plugin-ns")})
		items, err := manager.List(ctx, "alice", "fresh-token")
		require.NoError(t, err)
		require.Len(t, items, 1)
		assert.Equal(t, "failed", items[0].Status)
		assert.False(t, items[0].Blocked)
		started, err := manager.BeginTeardown(ctx, "alice", "fresh-token", ref, items[0].Generation)
		require.NoError(t, err)
		require.Eventually(t, func() bool {
			status, err := manager.TeardownStatus(ctx, "alice", ref, started.OperationID)
			return err == nil && status.Status == "succeeded"
		}, time.Second, time.Millisecond)
		assert.Equal(t, []string{"uninstall:il-lab", "delete:gitea"}, steps)
	})

	t.Run("teardown receipt stops old setup polling from recreating lab", func(t *testing.T) {
		manager := newManager(t, &managerRecoveryHelm{t: t}, &stale)
		leases := manager.config.Coordinator.client.CoordinationV1().Leases("plugin-ns")
		lease, err := leases.Get(ctx, operationLeaseName("alice", ref), metav1.GetOptions{})
		require.NoError(t, err)
		empty := ""
		lease.Spec.HolderIdentity = &empty
		lease.Annotations[teardownIDAnnotation] = strings.Repeat("f", 32)
		lease.Annotations[teardownStatusAnnotation] = "succeeded"
		_, err = leases.Update(ctx, lease, metav1.UpdateOptions{})
		require.NoError(t, err)
		status, err := manager.Status(ctx, "alice", "fresh-token", ref, bindings)
		require.NoError(t, err)
		assert.False(t, status.RetrySetup)
		assert.Equal(t, "failed", status.Helm.Status, "old polling tab must stop after teardown")
		assert.Contains(t, status.Helm.Message, "torn down")
	})

	t.Run("lost Lease cancels preflight before further SR mutations", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			coord := NewOperationCoordinator(coordinatorTestClient(), "plugin-ns")
			helm := &managerRecoveryLossHelm{coordinator: coord, cancelled: make(chan bool, 1)}
			manager := NewManager(ManagerConfig{Helm: helm, SpaceRequests: &SpaceRequestClient{}, Coordinator: coord})
			_, _, err := manager.Start(ctx, "alice", "fresh-token", ref, "chart", bindings)
			require.Error(t, err)
			assert.True(t, <-helm.cancelled, "preflight must inherit operation cancellation")
		})
	})

	t.Run("stale takeover uses fresh token and verified SRs but refuses pending Helm", func(t *testing.T) {
		started, finish := make(chan HelmInstallOpts, 1), make(chan struct{})
		helm := &managerRecoveryHelm{t: t, install: func(opts HelmInstallOpts) error {
			started <- opts
			<-finish
			return nil
		}}
		manager := newManager(t, helm, &stale)
		defer func() {
			close(finish)
			require.Eventually(t, func() bool { return !manager.config.Coordinator.IsHeld(ctx, "alice", ref) }, time.Second, time.Millisecond)
		}()
		status, ready, err := manager.Start(ctx, "alice", "fresh-token", ref, "chart", bindings)
		require.NoError(t, err)
		assert.False(t, ready)
		assert.Equal(t, "provisioning", status.SpaceRequests.Status)
		var opts HelmInstallOpts
		select {
		case opts = <-started:
		case <-time.After(time.Second):
			require.FailNow(t, "recovery did not start Helm worker")
		}
		assert.Equal(t, "fresh-token", opts.Token)
		assert.Equal(t, "chart", opts.ChartDir)
		assert.Equal(t, "alice-dev", opts.Namespace)
		assert.Equal(t, HelmReleaseName("alice", ref, bindings.Namespaces), opts.ReleaseName)
		assert.Equal(t, map[string]string{"namespaces.gitea": "alice-gitea"}, opts.Values)
		assert.Equal(t, map[string]string{CatalogRefLabelKey: label}, opts.Labels)
		assert.True(t, manager.config.Coordinator.IsHeld(ctx, "alice", ref))

		// A restarted manager must inspect Helm even when inventory misses
		// its pending release; an expired reservation alone permits no install.
		pending := newManager(t, &managerRecoveryHelm{t: t, status: "pending-install"}, &stale)
		_, ready, err = pending.Start(ctx, "alice", "fresh-token", ref, "chart", bindings)
		var managerErr *ManagerError
		require.ErrorAs(t, err, &managerErr)
		assert.Equal(t, ManagerConflict, managerErr.Kind)
		assert.False(t, ready)
		assert.False(t, pending.config.Coordinator.IsHeld(ctx, "alice", ref))
	})
}

type managerRoundTripFunc func(*http.Request) (*http.Response, error)

func (f managerRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestManagerWorkerValues(t *testing.T) {
	ref := strings.Repeat("ab", 32)
	label, err := CatalogRefLabel(ref)
	require.NoError(t, err)
	sr := fmt.Sprintf(`{"metadata":{"name":"gitea","namespace":"alice-dev","uid":"sr-uid","labels":{%q:%q},"annotations":{%q:"gitea"}},"status":{"conditions":[{"type":"Ready","status":"True"}],"namespaceAccess":[{"name":"alice-gitea"}]}}`, CatalogRefLabelKey, label, BindingKeyAnnotation)
	for _, tc := range []struct {
		name, ingress, rootRef, message string
		workerBody                      string
		workerCode                      int
	}{
		{name: "ready namespace and root URL map to Helm values", ingress: "apps.example.com", rootRef: "gitea"},
		{name: "root URL without ingress fails safely", rootRef: "gitea", message: "cluster ingress domain not configured"},
		{name: "root URL with unknown namespace fails safely", ingress: "apps.example.com", rootRef: "unknown", message: "root-url references unknown namespace binding"},
		{name: "SR not ready after handoff", workerBody: strings.ReplaceAll(sr, `"status":"True"`, `"status":"False"`)},
		{name: "SR missing after handoff", workerCode: http.StatusNotFound},
		{name: "SR read denied after handoff", workerCode: http.StatusForbidden},
		{name: "SR UID replaced after handoff", workerBody: strings.ReplaceAll(sr, "sr-uid", "replacement")},
		{name: "SR UID missing after handoff", workerBody: strings.ReplaceAll(sr, "sr-uid", "")},
		{name: "SR foreign label after handoff", workerBody: strings.ReplaceAll(sr, label, "foreign")},
		{name: "SR binding mark changed after handoff", workerBody: strings.ReplaceAll(sr, `:"gitea"}`, `:"other"}`)},
		{name: "SR terminating after handoff", workerBody: strings.ReplaceAll(sr, `"uid":"sr-uid"`, `"uid":"sr-uid","deletionTimestamp":"2026-10-01T00:00:00Z"`)},
		{name: "SR ready without target after handoff", workerBody: strings.ReplaceAll(sr, "alice-gitea", "")},
	} {
		t.Run("given "+tc.name+" when worker runs then only verified values install", func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx := context.Background()
				namedGets := 0
				workerFinished := false
				client := &http.Client{Transport: managerRoundTripFunc(func(r *http.Request) (*http.Response, error) {
					assert.Equal(t, http.MethodGet, r.Method)
					assert.Equal(t, "Bearer user-token", r.Header.Get("Authorization"))
					body, code := sr, http.StatusOK
					if strings.HasSuffix(r.URL.Path, "/spacerequests") {
						body = `{"items":[` + sr + `]}`
					} else {
						namedGets++
						if namedGets > 2 && !workerFinished {
							if tc.workerBody != "" {
								body = tc.workerBody
							}
							if tc.workerCode != 0 {
								code = tc.workerCode
							}
						}
					}
					return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
				})}
				var installed []HelmInstallOpts
				helm := &managerTestHelm{installOpts: func(_ context.Context, opts HelmInstallOpts) error {
					installed = append(installed, opts)
					return nil
				}}
				coord := NewOperationCoordinator(coordinatorTestClient(), "plugin-ns")
				manager := NewManager(ManagerConfig{Helm: helm, SpaceRequests: &SpaceRequestClient{APIHost: "https://cluster.test", HTTPClient: client}, Coordinator: coord, ClusterIngressDomain: tc.ingress})
				bindings := SchemaBindings{Namespaces: []NamespaceBinding{{Key: "gitea", Path: "namespaces.gitea"}}}
				if tc.rootRef != "" {
					bindings.RootURLs = []RootURLBinding{{Path: "gitea.rootUrl", RouteName: "gitea", NamespaceRef: tc.rootRef}}
				}
				_, ready, err := manager.Start(ctx, "alice", "user-token", ref, "chart", bindings)
				require.NoError(t, err)
				assert.False(t, ready)
				synctest.Wait()
				if tc.workerBody != "" || tc.workerCode != 0 {
					assert.Empty(t, installed, "post-handoff read must not install stale preflight values")
					time.Sleep(31 * time.Minute) // Virtual polling timeout, not wall-clock delay.
					synctest.Wait()
				}
				workerFinished = true
				assert.False(t, coord.IsHeld(ctx, "alice", ref))
				if tc.message == "" && tc.workerBody == "" && tc.workerCode == 0 {
					require.Len(t, installed, 1)
					assert.Equal(t, "alice-gitea", installed[0].Values["namespaces.gitea"])
					assert.Equal(t, "gitea-alice-gitea.apps.example.com", installed[0].Values["gitea.rootUrl"])
					assert.Equal(t, map[string]string{CatalogRefLabelKey: label}, installed[0].Labels)
				} else {
					assert.Empty(t, installed)
					status, err := manager.Status(ctx, "alice", "user-token", ref, bindings)
					require.NoError(t, err)
					assert.Equal(t, "failed", status.Helm.Status)
					message := tc.message
					if message == "" {
						message = "timed out waiting for SpaceRequests"
					}
					assert.Equal(t, message, status.Helm.Message)
					assert.NotContains(t, status.Helm.Message, "user-token")
				}
			})
		})
	}
}

func TestManagerTeardownHandoff(t *testing.T) {
	ctx := context.Background()
	ref := strings.Repeat("ab", 32)
	label, err := CatalogRefLabel(ref)
	require.NoError(t, err)
	foreignLabel, err := CatalogRefLabel(strings.Repeat("cd", 32))
	require.NoError(t, err)
	sr := fmt.Sprintf(`{"metadata":{"name":"gitea","namespace":"alice-dev","uid":"sr-uid","labels":{%q:%q},"annotations":{%q:"gitea"}}}`, CatalogRefLabelKey, label, BindingKeyAnnotation)
	for _, tc := range []struct {
		name, current string
		wantSuccess   bool
	}{
		{"confirmed release avoids worker Helm inventory", sr, true},
		{"SR missing after reservation", "", false},
		{"foreign label after reservation", strings.ReplaceAll(sr, label, foreignLabel), false},
		{"UID replaced after reservation", strings.ReplaceAll(sr, "sr-uid", "replacement"), false},
		{"UID missing after reservation", strings.ReplaceAll(sr, "sr-uid", ""), false},
		{"identity mark removed after reservation", strings.ReplaceAll(sr, label, ""), false},
		{"binding changed after reservation", strings.ReplaceAll(sr, `:"gitea"}`, `:"other"}`), false},
	} {
		t.Run("given "+tc.name+" when teardown starts then mutation rechecks identity", func(t *testing.T) {
			var srLists, helmLists atomic.Int32
			steps := []string{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "Bearer user-token", r.Header.Get("Authorization"))
				switch r.Method {
				case http.MethodGet:
					if strings.HasSuffix(r.URL.Path, "/spacerequests") {
						current := sr
						if srLists.Add(1) > 3 {
							current = tc.current
						}
						_, _ = fmt.Fprintf(w, `{"items":[%s]}`, current)
					} else if tc.current == "" {
						w.WriteHeader(http.StatusNotFound)
					} else {
						_, _ = fmt.Fprint(w, tc.current)
					}
				case http.MethodDelete:
					steps = append(steps, "delete:gitea")
				default:
					t.Errorf("unexpected request %s", r.Method)
				}
			}))
			t.Cleanup(server.Close)
			helm := &managerTestHelm{steps: &steps, list: func(_ context.Context, _ []SpaceRequestResult) ([]LabRelease, error) {
				helmLists.Add(1)
				return []LabRelease{{Name: "il-lab", Namespace: "alice-dev", CatalogRef: ref, UID: "helm-uid", Revision: 1, Status: "deployed"}}, nil
			}}
			manager := NewManager(ManagerConfig{Helm: helm, SpaceRequests: &SpaceRequestClient{APIHost: server.URL, HTTPClient: server.Client()}, Coordinator: NewOperationCoordinator(coordinatorTestClient(), "plugin-ns")})
			items, err := manager.List(ctx, "alice", "user-token")
			require.NoError(t, err)
			require.Len(t, items, 1)
			err = awaitTeardown(t, manager, ctx, ref, items[0].Generation)
			if tc.wantSuccess {
				require.NoError(t, err)
				assert.Equal(t, []string{"uninstall:il-lab", "delete:gitea"}, steps)
			} else {
				require.Error(t, err)
				assert.Empty(t, steps)
			}
			assert.EqualValues(t, 4, srLists.Load(), "list, pre-acquire confirmation, post-acquire confirmation, fresh worker SR-only inventory")
			assert.EqualValues(t, 3, helmLists.Load())
		})
	}

	for _, created := range []bool{false, true} {
		t.Run(fmt.Sprintf("given another lab shares hook namespace after reservation created=%t then real Helm rejects uninstall", created), func(t *testing.T) {
			otherLabel, err := CatalogRefLabel(strings.Repeat("cd", 32))
			require.NoError(t, err)
			srJSON := func(name, identity, target string) string {
				return fmt.Sprintf(`{"metadata":{"name":%q,"namespace":"alice-dev","uid":%q,"labels":{%q:%q},"annotations":{%q:%q}},"status":{"conditions":[{"type":"Ready","status":"True"}],"namespaceAccess":[{"name":%q}]}}`, name, name+"-uid", CatalogRefLabelKey, identity, BindingKeyAnnotation, name, target)
			}
			var srLists atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, http.MethodGet, r.Method, "shared hooks must not mutate SRs")
				assert.Equal(t, "Bearer user-token", r.Header.Get("Authorization"))
				if strings.HasSuffix(r.URL.Path, "/spacerequests") {
					count := srLists.Add(1)
					other := ""
					if !created || count > 3 {
						target := "alice-other"
						if count > 3 {
							target = "alice-gitea"
						}
						other = "," + srJSON("other", otherLabel, target)
					}
					_, _ = fmt.Fprintf(w, `{"items":[%s%s]}`, srJSON("gitea", label, "alice-gitea"), other)
				} else if strings.HasSuffix(r.URL.Path, "/other") {
					_, _ = fmt.Fprint(w, srJSON("other", otherLabel, "alice-gitea"))
				} else {
					_, _ = fmt.Fprint(w, srJSON("gitea", label, "alice-gitea"))
				}
			}))
			t.Cleanup(server.Close)
			helm, mutations := cleanupTestHelm(t, &release.Release{
				Name: "il-lab", Namespace: "alice-dev", Version: 1,
				Labels: map[string]string{CatalogRefLabelKey: label}, Info: &release.Info{Status: release.StatusDeployed},
				Hooks: []*release.Hook{{Manifest: "apiVersion: batch/v1\nkind: Job\nmetadata:\n  name: hook\n  namespace: alice-gitea\n"}},
			}, "helm-uid")
			manager := NewManager(ManagerConfig{Helm: helm, SpaceRequests: &SpaceRequestClient{APIHost: server.URL, HTTPClient: server.Client()}, Coordinator: NewOperationCoordinator(coordinatorTestClient(), "plugin-ns")})
			items, err := manager.List(ctx, "alice", "user-token")
			require.NoError(t, err)
			if created {
				require.Len(t, items, 1)
			} else {
				require.Len(t, items, 2)
			}
			require.False(t, items[0].Blocked)
			require.Error(t, awaitTeardown(t, manager, ctx, ref, items[0].Generation))
			assert.Empty(t, *mutations, "worker must discover current cross-ref SR namespace sharing")
		})
	}

	for _, withRelease := range []bool{false, true} {
		for _, replaced := range []bool{false, true} {
			t.Run(fmt.Sprintf("given unrelated SR changes after reservation release=%t replaced=%t when teardown runs then own setup is cleaned", withRelease, replaced), func(t *testing.T) {
				otherLabel, err := CatalogRefLabel(strings.Repeat("cd", 32))
				require.NoError(t, err)
				other := strings.ReplaceAll(strings.ReplaceAll(sr, "gitea", "other"), label, otherLabel)
				var srLists atomic.Int32
				steps := []string{}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					assert.Equal(t, "Bearer user-token", r.Header.Get("Authorization"))
					switch r.Method {
					case http.MethodGet:
						if strings.HasSuffix(r.URL.Path, "/spacerequests") {
							currentOther := other
							if srLists.Add(1) > 3 {
								currentOther = ""
								if replaced {
									currentOther = strings.ReplaceAll(other, "sr-uid", "replacement")
								}
							}
							if currentOther != "" {
								currentOther = "," + currentOther
							}
							_, _ = fmt.Fprintf(w, `{"items":[%s%s]}`, sr, currentOther)
						} else if strings.HasSuffix(r.URL.Path, "/other") {
							if replaced {
								_, _ = fmt.Fprint(w, strings.ReplaceAll(other, "sr-uid", "replacement"))
							} else {
								w.WriteHeader(http.StatusNotFound)
							}
						} else {
							_, _ = fmt.Fprint(w, sr)
						}
					case http.MethodDelete:
						assert.True(t, strings.HasSuffix(r.URL.Path, "/gitea"))
						steps = append(steps, "delete:gitea")
					default:
						t.Errorf("unexpected request %s", r.Method)
					}
				}))
				t.Cleanup(server.Close)
				helm := &managerTestHelm{steps: &steps}
				wantSteps := []string{"delete:gitea"}
				if withRelease {
					helm.releases = []LabRelease{{Name: "il-lab", Namespace: "alice-dev", CatalogRef: ref, UID: "helm-uid", Revision: 1, Status: "deployed"}}
					wantSteps = []string{"uninstall:il-lab", "delete:gitea"}
				}
				manager := NewManager(ManagerConfig{Helm: helm, SpaceRequests: &SpaceRequestClient{APIHost: server.URL, HTTPClient: server.Client()}, Coordinator: NewOperationCoordinator(coordinatorTestClient(), "plugin-ns")})
				items, err := manager.List(ctx, "alice", "user-token")
				require.NoError(t, err)
				require.Len(t, items, 2)
				require.NoError(t, awaitTeardown(t, manager, ctx, ref, items[0].Generation))
				assert.Equal(t, wantSteps, steps)
			})
		}
	}
}

// Name-based reads must never participate in a setup status snapshot.
type managerSnapshotOnlyHelm struct {
	HelmManager
	lists, statusReads int
}

func (h *managerSnapshotOnlyHelm) ListLabReleases(ctx context.Context, namespace, token string, srs []SpaceRequestResult) ([]LabRelease, error) {
	h.lists++
	return h.HelmManager.ListLabReleases(ctx, namespace, token, srs)
}

func (h *managerSnapshotOnlyHelm) ReleaseStatus(context.Context, string, string, string) (string, error) {
	h.statusReads++
	return "", errors.New("unexpected name-based status read")
}

func TestManagerVerifiedRelease(t *testing.T) {
	ctx := context.Background()
	ref := strings.Repeat("ab", 32)
	bindings := SchemaBindings{Passwords: []ValueBinding{{Path: "admin.password", Kind: KindGeneratedPassword}}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		_, _ = fmt.Fprint(w, `{"items":[]}`)
	}))
	t.Cleanup(server.Close)
	newManager := func(helm HelmManager) *Manager {
		return NewManager(ManagerConfig{Helm: helm, SpaceRequests: &SpaceRequestClient{APIHost: server.URL, HTTPClient: server.Client()}, Coordinator: NewOperationCoordinator(coordinatorTestClient(), "plugin-ns")})
	}
	verified := LabRelease{Name: "il-original-schema", Namespace: "alice-dev", UID: "helm-uid", CatalogRef: ref, Status: "deployed", Revision: 1, CleanupBlocked: true}

	t.Run("given a marked snapshot when polling then no name-based rereads supply status or values", func(t *testing.T) {
		label, err := CatalogRefLabel(ref)
		require.NoError(t, err)
		client, mutations := cleanupTestHelm(t, &release.Release{
			Name: verified.Name, Namespace: "alice-dev", Version: 1,
			Labels: map[string]string{CatalogRefLabelKey: label}, Info: &release.Info{Status: release.StatusDeployed},
			Config: map[string]interface{}{
				"admin": map[string]interface{}{"password": "snapshot-password", "unrequested": "private-secret"},
				"token": "private-token",
			},
		}, "helm-uid")
		helm := &managerSnapshotOnlyHelm{HelmManager: client}
		status, err := newManager(helm).Status(ctx, "alice", "user-token", ref, bindings)
		require.NoError(t, err)
		assert.Equal(t, 1, helm.lists)
		assert.Zero(t, helm.statusReads)
		assert.Equal(t, verified.Name, status.Helm.ReleaseName, "schema-derived name must not replace discovered name")
		assert.Equal(t, "deployed", status.Helm.Status)
		assert.Empty(t, status.Helm.Message)
		assert.Equal(t, map[string]string{"admin.password": "snapshot-password"}, status.Helm.GeneratedValues)
		assert.Equal(t, map[string]string{"admin-password": "snapshot-password"}, status.Attributes)
		payload, err := json.Marshal(status)
		require.NoError(t, err)
		assert.NotContains(t, string(payload), "private-secret")
		assert.NotContains(t, string(payload), "private-token")
		items, err := newManager(helm).List(ctx, "alice", "user-token")
		require.NoError(t, err)
		require.Len(t, items, 1)
		require.NotNil(t, items[0].Release)
		assert.Nil(t, items[0].Release.Config, "inventory must not retain full Config, even internally")
		payload, err = json.Marshal(items)
		require.NoError(t, err)
		assert.NotContains(t, string(payload), "snapshot-password")
		assert.NotContains(t, string(payload), "private-secret")
		assert.NotContains(t, string(payload), "private-token")
		assert.Empty(t, *mutations)
	})

	t.Run("given same revision lifecycle transition when polling then no release-change conflict", func(t *testing.T) {
		reads := 0
		helm := &managerTestHelm{status: "deployed", list: func(context.Context, []SpaceRequestResult) ([]LabRelease, error) {
			reads++
			snapshot := verified
			password := "deployed-password"
			if reads == 1 {
				snapshot.Status = "pending-install"
				password = "not-yet-ready-password"
			}
			snapshot.Config = map[string]interface{}{"admin": map[string]interface{}{"password": password}}
			return []LabRelease{snapshot}, nil
		}}
		manager := newManager(helm)
		_, finish, err := manager.config.Coordinator.Acquire(ctx, "alice", ref)
		require.NoError(t, err)
		t.Cleanup(finish)
		pending, err := manager.Status(ctx, "alice", "user-token", ref, bindings)
		require.NoError(t, err)
		assert.Equal(t, "installing", pending.Helm.Status)
		assert.Empty(t, pending.Helm.Message)
		assert.Empty(t, pending.Helm.GeneratedValues)
		assert.Empty(t, pending.Attributes)
		deployed, err := manager.Status(ctx, "alice", "user-token", ref, bindings)
		require.NoError(t, err)
		assert.Equal(t, "deployed", deployed.Helm.Status)
		assert.Empty(t, deployed.Helm.Message)
		assert.Equal(t, map[string]string{"admin.password": "deployed-password"}, deployed.Helm.GeneratedValues)
		assert.Equal(t, "deployed-password", deployed.Attributes["admin-password"])
		assert.Equal(t, 2, reads, "one discovery per poll")
		assert.Empty(t, helm.statusNames)
	})

	t.Run("given verified failed release when polling then actionable teardown guidance without values", func(t *testing.T) {
		failed := verified
		failed.Status = "failed"
		helm := &managerTestHelm{releases: []LabRelease{failed}, status: "failed"}
		status, err := newManager(helm).Status(ctx, "alice", "user-token", ref, bindings)
		require.NoError(t, err)
		assert.Equal(t, "failed", status.Helm.Status)
		assert.Equal(t, "Lab setup failed. Tear down this lab before launching again. If teardown is unavailable, contact an administrator.", status.Helm.Message)
		assert.Empty(t, status.Helm.GeneratedValues)
		assert.Empty(t, status.Attributes)
		assert.False(t, status.RetrySetup)
	})

	t.Run("given schema drift when polling then discovered name serves status and values", func(t *testing.T) {
		helm := &managerTestHelm{releases: []LabRelease{verified}, status: "deployed"}
		manager := newManager(helm)
		opened, ready, err := manager.Start(ctx, "alice", "user-token", ref, "chart", bindings)
		require.NoError(t, err)
		assert.True(t, ready, "cleanup eligibility must not restrict opening")
		status, err := manager.Status(ctx, "alice", "user-token", ref, bindings)
		require.NoError(t, err)
		assert.Equal(t, opened.Helm.ReleaseName, status.Helm.ReleaseName)
		assert.Equal(t, verified.Name, status.Helm.ReleaseName)
		assert.Empty(t, helm.statusNames, "status comes from discovered snapshot")
	})

	for _, tc := range []struct {
		name   string
		change func(*managerTestHelm)
	}{
		{"missing release name", func(h *managerTestHelm) { h.releases[0].Name = "" }},
		{"missing storage UID", func(h *managerTestHelm) { h.releases[0].UID = "" }},
		{"wrong namespace", func(h *managerTestHelm) { h.releases[0].Namespace = "bob-dev" }},
		{"duplicate marked releases", func(h *managerTestHelm) { h.releases = append(h.releases, h.releases[0]) }},
	} {
		t.Run("given "+tc.name+" when polling then no launch attributes escape", func(t *testing.T) {
			snapshot := verified
			snapshot.Config = map[string]interface{}{"admin": map[string]interface{}{"password": "unverified-secret"}}
			helm := &managerTestHelm{releases: []LabRelease{snapshot}, status: "deployed"}
			tc.change(helm)
			status, err := newManager(helm).Status(ctx, "alice", "user-token", ref, bindings)
			var managerErr *ManagerError
			require.ErrorAs(t, err, &managerErr)
			assert.Equal(t, ManagerConflict, managerErr.Kind)
			assert.Empty(t, status.Attributes)
			assert.Empty(t, status.Helm.GeneratedValues)
			assert.NotContains(t, err.Error(), "user-token")
		})
	}

	for _, tc := range []struct {
		name string
		ref  string
	}{
		{"unmarked same-name release", ""},
		{"foreign same-name release", strings.Repeat("cd", 32)},
	} {
		t.Run("given "+tc.name+" when polling then no name-based values escape", func(t *testing.T) {
			helm := &managerTestHelm{status: "deployed"}
			if tc.ref != "" {
				other := verified
				other.Name = HelmReleaseName("alice", ref, bindings.Namespaces)
				other.CatalogRef = tc.ref
				other.Config = map[string]interface{}{"admin": map[string]interface{}{"password": "foreign-secret"}}
				helm.releases = []LabRelease{other}
			}
			status, err := newManager(helm).Status(ctx, "alice", "user-token", ref, bindings)
			require.NoError(t, err, "only marked releases for this catalogRef participate in status")
			assert.Equal(t, "pending", status.Helm.Status)
			assert.Empty(t, status.Attributes)
			assert.Empty(t, status.Helm.GeneratedValues)
			assert.Empty(t, helm.statusNames, "never inspect unmarked/foreign same-name releases")
		})
	}

	t.Run("given release replacement during namespace preflight when opening then reject stale deployment", func(t *testing.T) {
		label, err := CatalogRefLabel(ref)
		require.NoError(t, err)
		helm := &managerTestHelm{releases: []LabRelease{verified}}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, http.MethodGet, r.Method)
			helm.releases[0].UID = "replacement"
			_, _ = fmt.Fprintf(w, `{"items":[{"metadata":{"name":"gitea","namespace":"alice-dev","uid":"sr-uid","labels":{%q:%q},"annotations":{%q:"gitea"}},"status":{"conditions":[{"type":"Ready","status":"True"}],"namespaceAccess":[{"name":"alice-gitea"}]}}]}`, CatalogRefLabelKey, label, BindingKeyAnnotation)
		}))
		t.Cleanup(server.Close)
		manager := NewManager(ManagerConfig{Helm: helm, SpaceRequests: &SpaceRequestClient{APIHost: server.URL, HTTPClient: server.Client()}, Coordinator: NewOperationCoordinator(coordinatorTestClient(), "plugin-ns")})
		_, ready, err := manager.Start(ctx, "alice", "user-token", ref, "chart", SchemaBindings{Namespaces: []NamespaceBinding{{Key: "gitea", Path: "namespaces.gitea"}}})
		var managerErr *ManagerError
		require.ErrorAs(t, err, &managerErr)
		assert.Equal(t, ManagerConflict, managerErr.Kind)
		assert.False(t, ready)
	})

	t.Run("given real Helm marked unsafe deployment when polling then discovered release stays openable", func(t *testing.T) {
		label, err := CatalogRefLabel(ref)
		require.NoError(t, err)
		helm, mutations := cleanupTestHelm(t, &release.Release{
			Name: verified.Name, Namespace: "alice-dev", Version: 1,
			Labels: map[string]string{CatalogRefLabelKey: label}, Info: &release.Info{Status: release.StatusDeployed},
			Config: map[string]interface{}{"admin": map[string]interface{}{"password": "generated-value"}},
			Hooks:  []*release.Hook{{Manifest: "apiVersion: batch/v1\nkind: Job\nmetadata:\n  name: unsafe\n  namespace: alice-dev\n"}},
		}, "helm-uid")
		manager := newManager(helm)
		opened, ready, err := manager.Start(ctx, "alice", "user-token", ref, "chart", bindings)
		require.NoError(t, err, "separate SDK Config maps must not break Start's identity guard")
		assert.True(t, ready)
		assert.Equal(t, verified.Name, opened.Helm.ReleaseName)
		status, err := manager.Status(ctx, "alice", "user-token", ref, bindings)
		require.NoError(t, err)
		assert.Equal(t, verified.Name, status.Helm.ReleaseName)
		assert.Equal(t, "deployed", status.Helm.Status)
		assert.Equal(t, "generated-value", status.Attributes["admin-password"])
		assert.Empty(t, *mutations)
	})
}

func TestManagerSnapshotValues(t *testing.T) {
	ctx := context.Background()
	ref := strings.Repeat("ab", 32)
	label, err := CatalogRefLabel(ref)
	require.NoError(t, err)
	for _, tc := range []struct {
		name, srReady, srReason string
		status                  release.Status
		passwords, wantValues   bool
	}{
		{name: "deployed only requested string paths", status: release.StatusDeployed, srReady: "True", passwords: true, wantValues: true},
		{name: "deployed without requested passwords", status: release.StatusDeployed, srReady: "True"},
		{name: "failed Helm never reveals values", status: release.StatusFailed, srReady: "True", passwords: true},
		{name: "pending install never reveals values", status: release.StatusPendingInstall, srReady: "True", passwords: true},
		{name: "failed SpaceRequest never reveals values", status: release.StatusDeployed, srReady: "False", srReason: "UnableToCreateSpace", passwords: true},
		{name: "pending SpaceRequest never reveals values", status: release.StatusDeployed, srReady: "False", passwords: true},
	} {
		t.Run("given "+tc.name+" when polling then snapshot values stay filtered", func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, http.MethodGet, r.Method)
				assert.Equal(t, "Bearer user-token", r.Header.Get("Authorization"))
				_, _ = fmt.Fprintf(w, `{"metadata":{"name":"gitea","namespace":"alice-dev","uid":"sr-uid","labels":{%q:%q},"annotations":{%q:"gitea"}},"status":{"namespaceAccess":[{"name":"alice-gitea"}],"conditions":[{"type":"Ready","status":%q,"reason":%q}]}}`, CatalogRefLabelKey, label, BindingKeyAnnotation, tc.srReady, tc.srReason)
			}))
			t.Cleanup(server.Close)
			client, mutations := cleanupTestHelm(t, &release.Release{
				Name: "il-original-schema", Namespace: "alice-dev", Version: 1,
				Labels: map[string]string{CatalogRefLabelKey: label}, Info: &release.Info{Status: tc.status},
				Config: map[string]interface{}{
					"admin":  map[string]interface{}{"password": "snapshot-password", "other": "unrequested-secret"},
					"number": 42, "list": []string{"private-item"}, "token": "private-token",
				},
			}, "helm-uid")
			helm := &managerSnapshotOnlyHelm{HelmManager: client}
			coord := NewOperationCoordinator(coordinatorTestClient(), "plugin-ns")
			_, finish, err := coord.Acquire(ctx, "alice", ref)
			require.NoError(t, err)
			t.Cleanup(finish)
			manager := NewManager(ManagerConfig{Helm: helm, SpaceRequests: &SpaceRequestClient{APIHost: server.URL, HTTPClient: server.Client()}, Coordinator: coord})
			bindings := SchemaBindings{Namespaces: []NamespaceBinding{{Key: "gitea", Path: "namespaces.gitea"}}}
			if tc.passwords {
				for _, path := range []string{"admin.password", "admin.missing", "number", "list", "admin.password.child"} {
					bindings.Passwords = append(bindings.Passwords, ValueBinding{Path: path, Kind: KindGeneratedPassword})
				}
			}
			status, err := manager.Status(ctx, "alice", "user-token", ref, bindings)
			require.NoError(t, err)
			if tc.wantValues {
				assert.Equal(t, map[string]string{"admin.password": "snapshot-password"}, status.Helm.GeneratedValues)
				assert.Equal(t, "snapshot-password", status.Attributes["admin-password"])
			} else {
				assert.Empty(t, status.Helm.GeneratedValues)
				assert.NotContains(t, status.Attributes, "admin-password")
				if tc.status != release.StatusDeployed || tc.srReady != "True" {
					assert.Empty(t, status.Attributes)
				}
			}
			payload, err := json.Marshal(status)
			require.NoError(t, err)
			for _, secret := range []string{"unrequested-secret", "private-item", "private-token", "user-token"} {
				assert.NotContains(t, string(payload), secret)
			}
			if !tc.wantValues {
				assert.NotContains(t, string(payload), "snapshot-password")
			}
			assert.Equal(t, 1, helm.lists)
			assert.Zero(t, helm.statusReads)
			assert.Empty(t, *mutations)
		})
	}
}

func TestManagerWorkerOwnership(t *testing.T) {
	ctx := context.Background()
	ref := strings.Repeat("ab", 32)
	t.Run("given old local failure when another pod deploys then cluster outcome wins", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = fmt.Fprint(w, `{"items":[]}`)
		}))
		t.Cleanup(server.Close)
		var deployed atomic.Bool
		helm := &managerTestHelm{list: func(context.Context, []SpaceRequestResult) ([]LabRelease, error) {
			if deployed.Load() {
				return []LabRelease{{Name: "il-recovered", Namespace: "alice-dev", UID: "new-uid", CatalogRef: ref, Status: "deployed"}}, nil
			}
			return nil, nil
		}}
		coord := NewOperationCoordinator(coordinatorTestClient(), "plugin-ns")
		manager := NewManager(ManagerConfig{Helm: helm, SpaceRequests: &SpaceRequestClient{APIHost: server.URL, HTTPClient: server.Client()}, Coordinator: coord})
		_, _, err := manager.Start(ctx, "alice", "user-token", ref, "chart", SchemaBindings{})
		require.NoError(t, err)
		require.Eventually(t, func() bool { return !coord.IsHeld(ctx, "alice", ref) }, time.Second, time.Millisecond)
		status, err := manager.Status(ctx, "alice", "user-token", ref, SchemaBindings{})
		require.NoError(t, err)
		assert.Equal(t, "failed", status.Helm.Status)
		assert.Equal(t, "Helm install failed", status.Helm.Message, "retain safe failure detail when cluster has no outcome")
		assert.False(t, status.RetrySetup, "actual local failures need explicit relaunch, not automatic retry")
		// Another pod's live reservation is authoritative even before Helm exists.
		started, finish := make(chan struct{}), make(chan struct{})
		otherHelm := &managerTestHelm{install: func() error {
			close(started)
			<-finish
			return nil
		}}
		otherPod := NewManager(ManagerConfig{Helm: otherHelm, SpaceRequests: manager.config.SpaceRequests, Coordinator: NewOperationCoordinator(coord.client, "plugin-ns")})
		_, _, err = otherPod.Start(ctx, "alice", "user-token", ref, "chart", SchemaBindings{})
		require.NoError(t, err)
		<-started
		status, err = manager.Status(ctx, "alice", "user-token", ref, SchemaBindings{})
		require.NoError(t, err)
		assert.Equal(t, "pending", status.Helm.Status, "old failure must not override another pod's live operation")
		assert.False(t, status.RetrySetup)
		items, err := manager.List(ctx, "alice", "user-token")
		require.NoError(t, err)
		require.Len(t, items, 1)
		assert.Equal(t, "pending", items[0].Status)
		assert.Equal(t, BlockedOperation, items[0].BlockedReason)
		close(finish)
		require.Eventually(t, func() bool { return !coord.IsHeld(ctx, "alice", ref) }, time.Second, time.Millisecond)
		deployed.Store(true)
		items, err = manager.List(ctx, "alice", "user-token")
		require.NoError(t, err)
		require.Len(t, items, 1)
		assert.Equal(t, "deployed", items[0].Status)
		assert.False(t, items[0].Blocked)
	})

	t.Run("given expired teardown when successor starts then old cleanup cannot remove successor record", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			client := &http.Client{Transport: managerRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"items":[]}`)), Header: make(http.Header)}, nil
			})}
			oldStarted := make(chan context.Context, 1)
			oldFinish, successorFinish := make(chan struct{}), make(chan struct{})
			successorStarted := make(chan struct{})
			deployed := true
			helm := &managerTestHelm{
				list: func(context.Context, []SpaceRequestResult) ([]LabRelease, error) {
					if deployed {
						return []LabRelease{{Name: "il-old", Namespace: "alice-dev", UID: "helm-uid", CatalogRef: ref, Status: "deployed"}}, nil
					}
					return nil, nil
				},
				uninstall: func(ctx context.Context, _ LabRelease, _ []SpaceRequestResult) error {
					oldStarted <- ctx
					<-oldFinish
					return nil
				},
				installOpts: func(context.Context, HelmInstallOpts) error {
					close(successorStarted)
					<-successorFinish
					return errors.New("successor failure")
				},
			}
			coord := NewOperationCoordinator(coordinatorTestClient(), "plugin-ns")
			manager := NewManager(ManagerConfig{Helm: helm, SpaceRequests: &SpaceRequestClient{APIHost: "https://cluster.test", HTTPClient: client}, Coordinator: coord})
			items, err := manager.List(ctx, "alice", "user-token")
			require.NoError(t, err)
			require.Len(t, items, 1)
			oldOperation, err := manager.BeginTeardown(ctx, "alice", "user-token", ref, items[0].Generation)
			require.NoError(t, err)
			oldCtx := <-oldStarted
			leases := coord.client.CoordinationV1().Leases("plugin-ns")
			lease, err := leases.Get(ctx, operationLeaseName("alice", ref), metav1.GetOptions{})
			require.NoError(t, err)
			stale := metav1.NewMicroTime(time.Now().Add(-201 * time.Second))
			lease.Spec.RenewTime = &stale
			_, err = leases.Update(ctx, lease, metav1.UpdateOptions{})
			require.NoError(t, err)
			deployed = false // Old in-flight Helm can outlive the reservation.
			_, _, err = manager.Start(ctx, "alice", "user-token", ref, "chart", SchemaBindings{})
			require.NoError(t, err)
			<-successorStarted
			close(oldFinish)
			synctest.Wait()
			assert.ErrorIs(t, oldCtx.Err(), context.Canceled)
			status, err := manager.Status(ctx, "alice", "user-token", ref, SchemaBindings{})
			require.NoError(t, err)
			assert.Equal(t, "installing", status.Helm.Status)
			assert.True(t, coord.IsHeld(ctx, "alice", ref))
			_, err = manager.TeardownStatus(ctx, "alice", ref, oldOperation.OperationID)
			var managerErr *ManagerError
			require.ErrorAs(t, err, &managerErr)
			assert.Equal(t, ManagerNotFound, managerErr.Kind, "old completion must not resurrect superseded receipt")
			close(successorFinish)
			synctest.Wait()
			assert.False(t, coord.IsHeld(ctx, "alice", ref))
		})
	})

	for _, oldFails := range []bool{true, false} {
		t.Run(fmt.Sprintf("given expired installer when successor starts then old completion cannot overwrite it failure=%t", oldFails), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = fmt.Fprint(w, `{"items":[]}`)
			}))
			t.Cleanup(server.Close)
			started := make(chan context.Context, 2)
			oldFinish, successorFinish := make(chan struct{}), make(chan struct{})
			var installs atomic.Int32
			helm := &managerTestHelm{installOpts: func(ctx context.Context, _ HelmInstallOpts) error {
				call := installs.Add(1)
				started <- ctx
				if call == 1 {
					<-oldFinish
					if oldFails {
						return errors.New("old install failed user-token")
					}
					return nil
				}
				<-successorFinish
				return errors.New("successor install failed user-token")
			}}
			coord := NewOperationCoordinator(coordinatorTestClient(), "plugin-ns")
			manager := NewManager(ManagerConfig{Helm: helm, SpaceRequests: &SpaceRequestClient{APIHost: server.URL, HTTPClient: server.Client()}, Coordinator: coord})
			_, _, err := manager.Start(ctx, "alice", "user-token", ref, "chart", SchemaBindings{})
			require.NoError(t, err)
			oldCtx := <-started
			t.Cleanup(func() {
				close(successorFinish)
				require.Eventually(t, func() bool { return !coord.IsHeld(ctx, "alice", ref) }, time.Second, time.Millisecond)
			})
			leases := coord.client.CoordinationV1().Leases("plugin-ns")
			lease, err := leases.Get(ctx, operationLeaseName("alice", ref), metav1.GetOptions{})
			require.NoError(t, err)
			stale := metav1.NewMicroTime(time.Now().Add(-201 * time.Second))
			lease.Spec.RenewTime = &stale
			_, err = leases.Update(ctx, lease, metav1.UpdateOptions{})
			require.NoError(t, err)
			items, err := manager.List(ctx, "alice", "user-token")
			require.NoError(t, err)
			require.Len(t, items, 1)
			assert.Equal(t, "failed", items[0].Status, "expired local installer must not permanently block recovery")
			assert.False(t, items[0].Blocked)
			status, err := manager.Status(ctx, "alice", "user-token", ref, SchemaBindings{})
			require.NoError(t, err)
			assert.True(t, status.RetrySetup)
			_, _, err = manager.Start(ctx, "alice", "user-token", ref, "chart", SchemaBindings{})
			require.NoError(t, err)
			<-started
			close(oldFinish)
			require.Eventually(t, func() bool { return oldCtx.Err() != nil }, time.Second, time.Millisecond)
			status, err = manager.Status(ctx, "alice", "user-token", ref, SchemaBindings{})
			require.NoError(t, err)
			assert.Equal(t, "installing", status.Helm.Status, "old success/failure must not replace successor's record")
			assert.True(t, coord.IsHeld(ctx, "alice", ref), "old completion must not release successor")
		})
	}
}
