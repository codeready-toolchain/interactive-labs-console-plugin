package handler_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"

	"k8s.io/client-go/kubernetes/fake"

	"github.com/codeready-toolchain/interactive-labs-console-plugin/backend/internal/handler"
	"github.com/codeready-toolchain/interactive-labs-console-plugin/backend/internal/setup"
)

type setupGuardHelmManager struct {
	*stubHelmManager
	releases []setup.LabRelease
}

func (h *setupGuardHelmManager) ListLabReleases(context.Context, string, string, []setup.SpaceRequestResult) ([]setup.LabRelease, error) {
	return h.releases, nil
}

func TestSetupGuardConflictHTTPContract(t *testing.T) {
	t.Run("terminating old binding with no Helm release", func(t *testing.T) {
		ref := goodCatalogRef()
		label, err := setup.CatalogRefLabel(ref)
		require.NoError(t, err)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "Bearer user1", r.Header.Get("Authorization"))
			assert.Equal(t, setup.CatalogRefLabelKey+"="+label, r.URL.Query().Get("labelSelector"))
			if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/spacerequests") {
				t.Error("terminating inventory must block named SR requests and mutations")
				w.WriteHeader(http.StatusConflict)
				return
			}
			_, _ = fmt.Fprintf(w, `{"items":[{"metadata":{"name":"old-binding","namespace":"user1-dev","uid":"sr-uid","deletionTimestamp":"2026-10-01T00:00:00Z","labels":{%q:%q},"annotations":{%q:"old-binding"}},"status":{"namespaceAccess":[{"name":"old-namespace"}],"conditions":[{"type":"Ready","status":"True"}]}}]}`, setup.CatalogRefLabelKey, label, setup.BindingKeyAnnotation)
		}))
		defer server.Close()
		helm := &stubHelmManager{}
		router := handler.NewRouter(handler.Config{
			CatalogSource: goodCatalogCM,
			Authenticate:  func(_ context.Context, _, token string) (string, error) { return token, nil },
			SetupManager:  newTestManager(t, &setup.SpaceRequestClient{HTTPClient: server.Client(), APIHost: server.URL}, helm),
		})
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1alpha1/labs/"+ref+"/setup", nil)
		req.Header.Set("Authorization", "Bearer user1")
		rw := httptest.NewRecorder()
		router.ServeHTTP(rw, req)
		assert.Equal(t, http.StatusConflict, rw.Code, rw.Body.String())
		assert.False(t, helm.installed)
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("duplicate release must block SpaceRequest access")
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	ref := goodCatalogRef()
	helm := &setupGuardHelmManager{stubHelmManager: &stubHelmManager{}, releases: []setup.LabRelease{
		{CatalogRef: ref, Name: "il-first", Namespace: "user1-dev", UID: "first", Status: "deployed"},
		{CatalogRef: ref, Name: "il-second", Namespace: "user1-dev", UID: "second", Status: "deployed"},
	}}
	router := handler.NewRouter(handler.Config{
		CatalogSource: goodCatalogCM,
		Authenticate:  func(_ context.Context, _, token string) (string, error) { return token, nil },
		SetupManager:  newTestManager(t, &setup.SpaceRequestClient{HTTPClient: server.Client(), APIHost: server.URL}, helm),
	})
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1alpha1/labs/"+ref+"/setup", nil)
	req.Header.Set("Authorization", "Bearer user1")
	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)
	require.Equal(t, http.StatusConflict, rw.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rw.Body.Bytes(), &body))
	assert.Equal(t, "configuration_error", body["type"])
	assert.Equal(t, "Multiple releases found for this lab. Contact an administrator.", body["message"])
	assert.False(t, helm.installed)
}

type teardownHelm struct {
	stubHelmManager
	releases     []setup.LabRelease
	steps        *[]string
	uninstallErr error
	listErr      error
	mu           sync.Mutex
}

func (h *teardownHelm) ListLabReleases(context.Context, string, string, []setup.SpaceRequestResult) ([]setup.LabRelease, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]setup.LabRelease(nil), h.releases...), h.listErr
}

func (h *teardownHelm) UninstallLabRelease(_ context.Context, _, _ string, target setup.LabRelease, _ []setup.SpaceRequestResult) error {
	*h.steps = append(*h.steps, "uninstall:"+target.Name)
	if h.uninstallErr != nil {
		return h.uninstallErr
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for i, r := range h.releases {
		if r.Name == target.Name {
			h.releases = append(h.releases[:i], h.releases[i+1:]...)
			break
		}
	}
	return nil
}

func TestSetupOwnershipPreflight(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  string
		listErr error
		want    int
	}{
		{"deployed release with missing namespaces must not report ready", "deployed", nil, http.StatusConflict},
		{"pending Helm blocks new setup", "pending-install", nil, http.StatusConflict},
		{"failed Helm requires explicit cleanup", "failed", nil, http.StatusConflict},
		{"list permission failure never creates SRs", "", fmt.Errorf("forbidden"), http.StatusBadGateway},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ref := goodCatalogRef()
			var mutations int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					mutations++
				}
				w.WriteHeader(http.StatusNotFound)
			}))
			defer server.Close()
			helm := &teardownHelm{listErr: tc.listErr, releases: []setup.LabRelease{{Name: "il-original-keys", CatalogRef: ref, Namespace: "alice-dev", UID: "release-1", Revision: 1, Status: tc.status}}}
			router := handler.NewRouter(handler.Config{
				Authenticate:  func(context.Context, string, string) (string, error) { return "alice", nil },
				CatalogSource: goodCatalogCM,
				SetupManager:  newTestManager(t, &setup.SpaceRequestClient{HTTPClient: server.Client(), APIHost: server.URL}, helm),
			})
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1alpha1/labs/"+ref+"/setup", nil)
			req.Header.Set("Authorization", "Bearer token")
			rw := httptest.NewRecorder()
			router.ServeHTTP(rw, req)
			assert.Equal(t, tc.want, rw.Code, rw.Body.String())
			assert.Zero(t, mutations)
		})
	}
}

func TestTeardown(t *testing.T) {
	t.Run("empty setup inventory is a JSON array", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = fmt.Fprint(w, `{"items":[]}`)
		}))
		t.Cleanup(server.Close)
		router := handler.NewRouter(handler.Config{
			Authenticate: func(context.Context, string, string) (string, error) { return "alice", nil },
			SetupManager: newTestManager(t, &setup.SpaceRequestClient{HTTPClient: server.Client(), APIHost: server.URL}, &teardownHelm{}),
		})
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1alpha1/setups", nil)
		req.Header.Set("Authorization", "Bearer user-token")
		rw := httptest.NewRecorder()
		router.ServeHTTP(rw, req)
		require.Equal(t, http.StatusOK, rw.Code)
		var body map[string]any
		require.NoError(t, json.Unmarshal(rw.Body.Bytes(), &body))
		assert.Equal(t, []any{}, body["items"])
	})

	for _, tc := range []struct {
		name          string
		failUninstall bool
		noRelease     bool
		pending       bool
	}{
		{name: "removed catalog setup can be cleaned"},
		{name: "uninstall failure never deletes SR", failUninstall: true},
		{name: "failed provisioning without Helm can be cleaned", noRelease: true},
		{name: "unheld pending Helm allows confirmed cleanup", pending: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ref, other := strings.Repeat("ab", 32), strings.Repeat("cd", 32)
			label, err := setup.CatalogRefLabel(ref)
			require.NoError(t, err)
			steps := []string{}
			removed := false
			uid := "uid-1"
			srJSON := func() string {
				return fmt.Sprintf(`{"metadata":{"name":"gitea","namespace":"alice-dev","uid":%q,"labels":{%q:%q},"annotations":{%q:"gitea"}}}`, uid, setup.CatalogRefLabelKey, label, setup.BindingKeyAnnotation)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "Bearer user-token", r.Header.Get("Authorization"))
				assert.Contains(t, r.URL.Path, "/namespaces/alice-dev/")
				switch r.Method {
				case http.MethodGet:
					if strings.HasSuffix(r.URL.Path, "/spacerequests") {
						if removed {
							_, _ = fmt.Fprint(w, `{"items":[]}`)
						} else {
							_, _ = fmt.Fprint(w, `{"items":[`+srJSON()+`]}`)
						}
					} else if removed {
						w.WriteHeader(http.StatusNotFound)
					} else {
						_, _ = fmt.Fprint(w, srJSON())
					}
				case http.MethodDelete:
					var opts struct {
						Preconditions struct {
							UID string `json:"uid"`
						} `json:"preconditions"`
					}
					if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&opts)) {
						http.Error(w, "invalid delete options JSON", http.StatusBadRequest)
						return
					}
					assert.Equal(t, "uid-1", opts.Preconditions.UID)
					steps = append(steps, "delete:gitea")
					removed = true
				default:
					t.Errorf("unexpected mutation %s", r.Method)
				}
			}))
			defer server.Close()
			helm := &teardownHelm{steps: &steps, releases: []setup.LabRelease{
				{Name: "il-alice", Namespace: "alice-dev", CatalogRef: ref, Status: "deployed", Revision: 1, UID: "helm-uid"},
				{Name: "il-other", Namespace: "alice-dev", CatalogRef: other, Status: "deployed", Revision: 1, UID: "other-uid"},
			}}
			if tc.noRelease {
				helm.releases = helm.releases[1:]
			}
			if tc.pending {
				helm.releases[0].Status = "pending-install"
			}
			if tc.failUninstall {
				helm.uninstallErr = fmt.Errorf("uninstall failed")
			}
			coord := setup.NewOperationCoordinator(fake.NewSimpleClientset(), "plugin-ns")
			router := handler.NewRouter(handler.Config{
				Authenticate: func(context.Context, string, string) (string, error) { return "alice", nil },
				CatalogSource: func(context.Context) (*corev1.ConfigMap, error) {
					t.Error("teardown must not read current catalog")
					return nil, fmt.Errorf("removed")
				},
				SetupManager: newTestManager(t, &setup.SpaceRequestClient{HTTPClient: server.Client(), APIHost: server.URL}, helm, func(c *setup.ManagerConfig) {
					c.Coordinator = coord
					c.WithSnapshot = func(string, string, func(string) error) error {
						t.Error("inventory and teardown must not read snapshots")
						return fmt.Errorf("repository unavailable")
					}
				}),
			})
			request := func(method, url, body string, authorize bool) *httptest.ResponseRecorder {
				req := httptest.NewRequestWithContext(t.Context(), method, url, strings.NewReader(body))
				if authorize {
					req.Header.Set("Authorization", "Bearer user-token")
				}
				rw := httptest.NewRecorder()
				router.ServeHTTP(rw, req)
				return rw
			}
			rw := request(http.MethodGet, "/api/v1alpha1/setups", "", true)
			require.Equal(t, http.StatusOK, rw.Code, rw.Body.String())
			var inventory struct {
				Items []struct {
					CatalogRef string `json:"catalogRef"`
					Generation string `json:"generation"`
				} `json:"items"`
			}
			require.NoError(t, json.Unmarshal(rw.Body.Bytes(), &inventory))
			require.Len(t, inventory.Items, 2)
			var generation string
			for _, item := range inventory.Items {
				if item.CatalogRef == ref {
					generation = item.Generation
				}
			}
			require.NotEmpty(t, generation)
			assert.Empty(t, steps, "inventory never mutates")
			endpoint := "/api/v1alpha1/labs/" + ref + "/teardown"
			assert.Equal(t, http.StatusUnauthorized, request(http.MethodPost, endpoint, "{}", false).Code)
			assert.Equal(t, http.StatusBadRequest, request(http.MethodPost, endpoint, `{"generation":"`+generation+`","namespace":"evil"}`, true).Code)
			assert.Equal(t, http.StatusConflict, request(http.MethodPost, endpoint, `{"generation":"stale"}`, true).Code)
			assert.Empty(t, steps)
			_, unlock, err := coord.Acquire(context.Background(), "alice", ref)
			require.NoError(t, err)
			assert.Equal(t, http.StatusConflict, request(http.MethodPost, endpoint, `{"generation":"`+generation+`"}`, true).Code)
			unlock()
			uid = "replacement-before-confirmation"
			assert.Equal(t, http.StatusConflict, request(http.MethodPost, endpoint, `{"generation":"`+generation+`"}`, true).Code)
			uid = "uid-1"
			assert.Empty(t, steps)
			rw = request(http.MethodPost, endpoint, `{"generation":"`+generation+`"}`, true)
			require.Equal(t, http.StatusAccepted, rw.Code, rw.Body.String())
			var progress struct {
				OperationID string `json:"operationId"`
				Status      string `json:"status"`
			}
			require.NoError(t, json.Unmarshal(rw.Body.Bytes(), &progress))
			assert.Equal(t, "running", progress.Status)
			want := "succeeded"
			if tc.failUninstall {
				want = "failed"
			}
			require.Eventually(t, func() bool {
				status := request(http.MethodGet, endpoint+"/status?operationId="+progress.OperationID, "", true)
				if status.Code != http.StatusOK || json.Unmarshal(status.Body.Bytes(), &progress) != nil {
					return false
				}
				return progress.Status == want
			}, 3*time.Second, 10*time.Millisecond)
			switch {
			case tc.noRelease:
				assert.Equal(t, []string{"delete:gitea"}, steps)
			case tc.failUninstall:
				assert.Equal(t, []string{"uninstall:il-alice"}, steps)
			default:
				assert.Equal(t, []string{"uninstall:il-alice", "delete:gitea"}, steps)
				assert.NotContains(t, steps, "uninstall:il-other")
			}
		})
	}
}

type blockingHandlerHelm struct {
	*teardownHelm
	started chan struct{}
	resume  chan struct{}
}

func (h *blockingHandlerHelm) UninstallLabRelease(ctx context.Context, namespace, token string, target setup.LabRelease, srs []setup.SpaceRequestResult) error {
	close(h.started)
	<-h.resume
	return h.teardownHelm.UninstallLabRelease(ctx, namespace, token, target, srs)
}

func TestAsyncTeardownHTTPContract(t *testing.T) {
	ref := strings.Repeat("ab", 32)
	label, err := setup.CatalogRefLabel(ref)
	require.NoError(t, err)
	sr := fmt.Sprintf(`{"metadata":{"name":"gitea","namespace":"alice-dev","uid":"sr-uid","labels":{%q:%q},"annotations":{%q:"gitea"}}}`, setup.CatalogRefLabelKey, label, setup.BindingKeyAnnotation)
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
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected SpaceRequest request: %s", r.Method)
		}
	}))
	t.Cleanup(server.Close)
	steps := []string{}
	helm := &blockingHandlerHelm{teardownHelm: &teardownHelm{steps: &steps, releases: []setup.LabRelease{{Name: "il-lab", Namespace: "alice-dev", CatalogRef: ref, Status: "deployed", UID: "helm-uid", Revision: 1}}}, started: make(chan struct{}), resume: make(chan struct{})}
	t.Cleanup(func() {
		select {
		case <-helm.resume:
		default:
			close(helm.resume)
		}
	})
	manager := newTestManager(t, &setup.SpaceRequestClient{HTTPClient: server.Client(), APIHost: server.URL}, helm)
	router := handler.NewRouter(handler.Config{Authenticate: func(context.Context, string, string) (string, error) { return "alice", nil }, SetupManager: manager})
	request := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer user-token")
		rw := httptest.NewRecorder()
		router.ServeHTTP(rw, req)
		return rw
	}
	inventory := request(http.MethodGet, "/api/v1alpha1/setups", "")
	require.Equal(t, http.StatusOK, inventory.Code)
	var listed struct {
		Items []struct {
			Generation string `json:"generation"`
		} `json:"items"`
	}
	require.NoError(t, json.Unmarshal(inventory.Body.Bytes(), &listed))
	require.Len(t, listed.Items, 1)
	endpoint := "/api/v1alpha1/labs/" + ref + "/teardown"
	started := request(http.MethodPost, endpoint, `{"generation":"`+listed.Items[0].Generation+`"}`)
	require.Equal(t, http.StatusAccepted, started.Code, started.Body.String())
	assert.Equal(t, "no-store", started.Header().Get("Cache-Control"))
	var operation struct {
		Kind        string `json:"kind"`
		CatalogRef  string `json:"catalogRef"`
		OperationID string `json:"operationId"`
		Status      string `json:"status"`
	}
	require.NoError(t, json.Unmarshal(started.Body.Bytes(), &operation))
	assert.Equal(t, "TeardownStatus", operation.Kind)
	assert.Equal(t, "running", operation.Status)
	statusURL := endpoint + "/status?operationId=" + operation.OperationID
	select {
	case <-helm.started:
	case <-time.After(2 * time.Second):
		t.Fatal("background uninstall did not start")
	}
	active := request(http.MethodGet, endpoint+"/status", "")
	require.Equal(t, http.StatusOK, active.Code)
	var discovered struct {
		OperationID string `json:"operationId"`
		Status      string `json:"status"`
	}
	require.NoError(t, json.Unmarshal(active.Body.Bytes(), &discovered))
	assert.Equal(t, operation.OperationID, discovered.OperationID)
	assert.Equal(t, "running", discovered.Status)
	assert.Equal(t, http.StatusNotFound, request(http.MethodGet, endpoint+"/status?operationId="+strings.Repeat("f", 32), "").Code)
	close(helm.resume)
	require.Eventually(t, func() bool {
		got := request(http.MethodGet, statusURL, "")
		if got.Code != http.StatusOK {
			return false
		}
		if !assert.NoError(t, json.Unmarshal(got.Body.Bytes(), &operation)) {
			return false
		}
		return operation.Status == "succeeded"
	}, 3*time.Second, 10*time.Millisecond)
	assert.Equal(t, "no-store", request(http.MethodGet, statusURL, "").Header().Get("Cache-Control"))
	oldEndpoint := "/api/v1alpha1/setups/" + ref + "/teardown"
	assert.Equal(t, http.StatusNotFound, request(http.MethodPost, oldEndpoint, `{"generation":"`+listed.Items[0].Generation+`"}`).Code)
	assert.Equal(t, http.StatusNotFound, request(http.MethodGet, oldEndpoint+"/status?operationId="+operation.OperationID, "").Code)
}
