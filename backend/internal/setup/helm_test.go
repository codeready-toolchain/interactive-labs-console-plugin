package setup

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"helm.sh/helm/v3/pkg/release"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestBuildValues(t *testing.T) {
	t.Run("nested keys", func(t *testing.T) {
		flat := map[string]string{
			"namespaces.gitea":      "ns-abc",
			"namespaces.datasphere": "ns-xyz",
		}
		vals, err := BuildValues(flat)
		require.NoError(t, err)

		namespaces, ok := vals["namespaces"].(map[string]interface{})
		require.True(t, ok)
		assert.Equal(t, "ns-abc", namespaces["gitea"])
		assert.Equal(t, "ns-xyz", namespaces["datasphere"])
	})

	t.Run("top level key", func(t *testing.T) {
		flat := map[string]string{
			"myNamespace": "ns-abc",
		}
		vals, err := BuildValues(flat)
		require.NoError(t, err)
		assert.Equal(t, "ns-abc", vals["myNamespace"])
	})

	t.Run("empty map", func(t *testing.T) {
		vals, err := BuildValues(map[string]string{})
		require.NoError(t, err)
		assert.Empty(t, vals)
	})
}

func TestHelmStatusLabel(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"deployed", "deployed"},
		{"failed", "failed"},
		{"pending-install", "installing"},
		{"pending-upgrade", "installing"},
		{"pending-rollback", "installing"},
		{"unknown", "pending"},
		{"", "pending"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.expected, HelmStatusLabel(tt.input))
		})
	}
}

func TestVerifiedNamespaceBinding(t *testing.T) {
	ref := "abababababababababababababababababababababababababababababababab"
	label, err := CatalogRefLabel(ref)
	require.NoError(t, err)
	for _, tc := range []struct {
		name, uid, label string
		wantErr          bool
	}{
		{"verified ready SR", "expected-uid", label, false},
		{"replacement UID rejected", "replacement", label, true},
		{"foreign lab rejected", "expected-uid", "other", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = fmt.Fprintf(w, `{"metadata":{"name":"gitea","namespace":"alice-dev","uid":%q,"labels":{%q:%q},"annotations":{%q:"gitea"}},"status":{"namespaceAccess":[{"name":"ns-ready"}],"conditions":[{"type":"Ready","status":"True"}]}}`, tc.uid, CatalogRefLabelKey, tc.label, BindingKeyAnnotation)
			}))
			defer server.Close()
			client := &SpaceRequestClient{HTTPClient: server.Client(), APIHost: server.URL}
			values, err := mapVerifiedBindingsToValues(context.Background(), client, "token", "alice-dev", ref, []NamespaceBinding{{Key: "gitea", Path: "namespace"}}, map[string]string{"gitea": "expected-uid"})
			if tc.wantErr {
				require.Error(t, err)
				assert.Empty(t, values)
			} else {
				require.NoError(t, err)
				assert.Equal(t, "ns-ready", values["namespace"])
			}
		})
	}
}

func TestExtractNestedValue(t *testing.T) {
	m := map[string]interface{}{
		"passwords": map[string]interface{}{
			"postgres":   "secret123",
			"giteaAdmin": "admin456",
		},
		"topLevel": "flat",
	}

	val, ok := extractNestedValue(m, "passwords.postgres")
	assert.True(t, ok)
	assert.Equal(t, "secret123", val)

	val, ok = extractNestedValue(m, "passwords.giteaAdmin")
	assert.True(t, ok)
	assert.Equal(t, "admin456", val)

	val, ok = extractNestedValue(m, "topLevel")
	assert.True(t, ok)
	assert.Equal(t, "flat", val)

	_, ok = extractNestedValue(m, "missing")
	assert.False(t, ok)

	_, ok = extractNestedValue(m, "passwords.missing")
	assert.False(t, ok)

	_, ok = extractNestedValue(m, "passwords.postgres.deep")
	assert.False(t, ok)
}

func cleanupTestSpaceRequest(t *testing.T, ref string) SpaceRequestResult {
	t.Helper()
	label, err := CatalogRefLabel(ref)
	require.NoError(t, err)
	return SpaceRequestResult{
		Name: "gitea", Namespace: "alice-dev", UID: "sr-uid",
		Labels:          map[string]string{CatalogRefLabelKey: label},
		Annotations:     map[string]string{BindingKeyAnnotation: "gitea"},
		Conditions:      []SpaceRequestCondition{{Type: "Ready", Status: "True"}},
		NamespaceAccess: []SpaceRequestNamespaceAccess{{Name: "alice-gitea"}},
	}
}

// Exercise the public Helm API against local storage only. No uninstall may run.
func cleanupTestHelm(t *testing.T, rel *release.Release, uid string) (*HelmClient, *[]string) {
	t.Helper()
	payload, err := json.Marshal(rel)
	require.NoError(t, err)
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	_, err = writer.Write(payload)
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	secret := corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("sh.helm.release.v1.%s.v%d", rel.Name, rel.Version),
			Namespace: rel.Namespace, UID: types.UID(uid),
			Labels: map[string]string{"owner": "helm", "name": rel.Name, "status": string(rel.Info.Status), CatalogRefLabelKey: rel.Labels[CatalogRefLabelKey]},
		},
		Data: map[string][]byte{"release": []byte(base64.StdEncoding.EncodeToString(compressed.Bytes()))},
	}
	mutations := []string{}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer user-token", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodGet {
			mutations = append(mutations, r.Method+" "+r.URL.Path)
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if r.URL.Path == "/version" {
			_, _ = fmt.Fprint(w, `{"major":"1","minor":"30","gitVersion":"v1.30.0"}`)
			return
		}
		if r.URL.Path == "/api/v1/namespaces/alice-dev/secrets" {
			assert.NoError(t, json.NewEncoder(w).Encode(corev1.SecretList{Items: []corev1.Secret{secret}}))
			return
		}
		if r.URL.Path == "/api/v1/namespaces/alice-dev/secrets/"+secret.Name {
			assert.NoError(t, json.NewEncoder(w).Encode(secret))
			return
		}
		assert.Fail(t, "unexpected Helm API request", "%s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(server.Close)
	caFile := filepath.Join(t.TempDir(), "ca.crt")
	require.NoError(t, os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600))
	return &HelmClient{APIHost: server.URL, CAFile: caFile}, &mutations
}

func TestHelmCleanupEligibility(t *testing.T) {
	ref := strings.Repeat("ab", 32)
	label, err := CatalogRefLabel(ref)
	require.NoError(t, err)
	job := "apiVersion: batch/v1\nkind: Job\nmetadata:\n  name: hook\n  namespace: alice-gitea\n"
	for _, tc := range []struct {
		name, manifest, hook, reason string
		srs                          func() []SpaceRequestResult
		extraHook                    *release.Hook
	}{
		{name: "no hooks"},
		{name: "regular resource", manifest: "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: normal\n"},
		{name: "dedicated Job", hook: job},
		{name: "multiple dedicated Jobs", hook: job + "---\n" + strings.ReplaceAll(job, "name: hook", "name: hook-two")},
		{name: "empty separator documents", hook: "---\n" + job + "---\n"},
		{name: "JSON Job", hook: `{"apiVersion":"batch/v1","kind":"Job","metadata":{"name":"hook","namespace":"alice-gitea"}}`},
		{name: "duplicate hook keys", hook: job + "kind: ClusterRole\n", reason: "helm-manifest"},
		{name: "invalid annotation type", hook: job + "  annotations: [keep]\n", reason: "helm-manifest"},
		{name: "unknown hook list", hook: "apiVersion: v1\nkind: List\nitems: []\n", reason: "helm-hook"},
		{name: "invalid main resource", manifest: "{}", hook: job, reason: "helm-manifest"},
		{name: "home Job", hook: strings.ReplaceAll(job, "alice-gitea", "alice-dev"), reason: "helm-hook"},
		{name: "unverified namespace", hook: strings.ReplaceAll(job, "alice-gitea", "foreign"), reason: "helm-hook"},
		{name: "missing explicit namespace", hook: strings.ReplaceAll(job, "  namespace: alice-gitea\n", ""), reason: "helm-hook"},
		{name: "cluster scoped hook", hook: strings.ReplaceAll(job, "kind: Job", "kind: ClusterRole"), reason: "helm-hook"},
		{name: "other namespaced kind", hook: strings.ReplaceAll(job, "kind: Job", "kind: ConfigMap"), reason: "helm-hook"},
		{name: "old Job API", hook: strings.ReplaceAll(job, "batch/v1", "batch/v1beta1"), reason: "helm-hook"},
		{name: "unnamed Job", hook: strings.ReplaceAll(job, "  name: hook\n", ""), reason: "helm-hook"},
		{name: "unknown second document", hook: job + "---\napiVersion: v1\nkind: Unknown\n", reason: "helm-hook"},
		{name: "invalid hook YAML", hook: job + "spec: [", reason: "helm-manifest"},
		{name: "invalid second document", hook: job + "---\ninvalid: [", reason: "helm-manifest"},
		{name: "empty hook manifest", extraHook: &release.Hook{Kind: "Job"}, reason: "helm-hook"},
		{name: "safe and unsafe hooks", hook: job, extraHook: &release.Hook{Kind: "Job", Manifest: strings.ReplaceAll(job, "alice-gitea", "alice-dev")}, reason: "helm-hook"},
		{name: "retained hook Job", hook: job + "  annotations:\n    helm.sh/resource-policy: keep\n", reason: "helm-retained"},
		{name: "normalized retained hook policy", hook: job + "  annotations:\n    helm.sh/resource-policy: ' KEEP '\n", reason: "helm-retained"},
		{name: "retained regular resource", manifest: "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: retained\n  annotations:\n    helm.sh/resource-policy: keep\n", hook: job, reason: "helm-retained"},
		{name: "invalid main YAML", manifest: "metadata: [", hook: job, reason: "helm-manifest"},
		{name: "no SR", hook: job, srs: func() []SpaceRequestResult { return nil }, reason: "helm-hook"},
		{name: "not Ready SR", hook: job, srs: func() []SpaceRequestResult {
			sr := cleanupTestSpaceRequest(t, ref)
			sr.Conditions = nil
			return []SpaceRequestResult{sr}
		}, reason: "helm-hook"},
		{name: "given ready SR without target when listing then hook cleanup stays blocked", hook: job, srs: func() []SpaceRequestResult {
			sr := cleanupTestSpaceRequest(t, ref)
			sr.NamespaceAccess = nil
			return []SpaceRequestResult{sr}
		}, reason: "helm-hook"},
		{name: "given terminating ready SR when listing then hook cleanup stays blocked", hook: job, srs: func() []SpaceRequestResult {
			sr := cleanupTestSpaceRequest(t, ref)
			sr.DeletionTimestamp = "2026-10-01T00:00:00Z"
			return []SpaceRequestResult{sr}
		}, reason: "helm-hook"},
		{name: "given contradictory ready and failed SR when listing then hook cleanup stays blocked", hook: job, srs: func() []SpaceRequestResult {
			sr := cleanupTestSpaceRequest(t, ref)
			sr.Conditions = append(sr.Conditions, SpaceRequestCondition{Type: "Ready", Status: "False", Reason: "UnableToCreateSpace"})
			return []SpaceRequestResult{sr}
		}, reason: "helm-hook"},
		{name: "unmarked SR", hook: job, srs: func() []SpaceRequestResult {
			sr := cleanupTestSpaceRequest(t, ref)
			sr.Labels = nil
			return []SpaceRequestResult{sr}
		}, reason: "helm-hook"},
		{name: "foreign catalog SR", hook: job, srs: func() []SpaceRequestResult {
			return []SpaceRequestResult{cleanupTestSpaceRequest(t, strings.Repeat("cd", 32))}
		}, reason: "helm-hook"},
		{name: "shared target namespace", hook: job, srs: func() []SpaceRequestResult {
			return []SpaceRequestResult{cleanupTestSpaceRequest(t, ref), cleanupTestSpaceRequest(t, strings.Repeat("cd", 32))}
		}, reason: "helm-hook"},
		{name: "missing UID SR", hook: job, srs: func() []SpaceRequestResult {
			sr := cleanupTestSpaceRequest(t, ref)
			sr.UID = ""
			return []SpaceRequestResult{sr}
		}, reason: "helm-hook"},
		{name: "missing binding marker SR", hook: job, srs: func() []SpaceRequestResult {
			sr := cleanupTestSpaceRequest(t, ref)
			sr.Annotations = nil
			return []SpaceRequestResult{sr}
		}, reason: "helm-hook"},
		{name: "foreign home SR", hook: job, srs: func() []SpaceRequestResult {
			sr := cleanupTestSpaceRequest(t, ref)
			sr.Namespace = "bob-dev"
			return []SpaceRequestResult{sr}
		}, reason: "helm-hook"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rel := &release.Release{Name: "il-lab", Namespace: "alice-dev", Version: 1, Info: &release.Info{Status: release.StatusDeployed}, Labels: map[string]string{CatalogRefLabelKey: label}, Manifest: tc.manifest}
			if tc.hook != "" {
				rel.Hooks = append(rel.Hooks, &release.Hook{Kind: "Job", Manifest: tc.hook})
			}
			if tc.extraHook != nil {
				rel.Hooks = append(rel.Hooks, tc.extraHook)
			}
			srs := []SpaceRequestResult{cleanupTestSpaceRequest(t, ref)}
			if tc.srs != nil {
				srs = tc.srs()
			}
			client, mutations := cleanupTestHelm(t, rel, "helm-uid")
			items, err := client.ListLabReleases(context.Background(), "alice-dev", "user-token", srs)
			require.NoError(t, err)
			require.Len(t, items, 1)
			assert.Equal(t, tc.reason != "", items[0].CleanupBlocked)
			assert.EqualValues(t, tc.reason, items[0].BlockedReason)
			assert.Empty(t, *mutations)
		})
	}
}

func TestHelmUninstallInterruptedPendingInstall(t *testing.T) {
	ref := strings.Repeat("ab", 32)
	label, err := CatalogRefLabel(ref)
	require.NoError(t, err)
	rel := &release.Release{Name: "il-lab", Namespace: "alice-dev", Version: 1,
		Info: &release.Info{Status: release.StatusPendingInstall}, Labels: map[string]string{CatalogRefLabelKey: label}}
	client, mutations := cleanupTestHelm(t, rel, "helm-uid")
	target := LabRelease{Name: rel.Name, Namespace: rel.Namespace, CatalogRef: ref, Status: "pending-install", Revision: 1, UID: "helm-uid"}
	uninstallErr := client.UninstallLabRelease(context.Background(), "alice-dev", "user-token", target, []SpaceRequestResult{cleanupTestSpaceRequest(t, ref)})
	require.Error(t, uninstallErr, "fixture forbids mutation but eligible pending install must attempt managed cleanup")
	assert.NotEqual(t, "lab release changed or requires manual recovery", uninstallErr.Error())
	assert.NotEmpty(t, *mutations)
}

func TestHelmUninstallRechecksEligibility(t *testing.T) {
	ref := strings.Repeat("ab", 32)
	label, err := CatalogRefLabel(ref)
	require.NoError(t, err)
	for _, tc := range []struct {
		name, namespace string
		status          release.Status
		uid             string
	}{
		{"home hook introduced", "alice-dev", release.StatusDeployed, "helm-uid"},
		{"unverified hook introduced", "other", release.StatusDeployed, "helm-uid"},
		{"pending release", "alice-gitea", release.StatusPendingUpgrade, "helm-uid"},
		{"storage replaced", "alice-gitea", release.StatusDeployed, "replacement"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rel := &release.Release{Name: "il-lab", Namespace: "alice-dev", Version: 1, Info: &release.Info{Status: tc.status}, Labels: map[string]string{CatalogRefLabelKey: label}, Hooks: []*release.Hook{{Kind: "Job", Manifest: fmt.Sprintf("apiVersion: batch/v1\nkind: Job\nmetadata:\n  name: hook\n  namespace: %s\n", tc.namespace)}}}
			client, mutations := cleanupTestHelm(t, rel, tc.uid)
			target := LabRelease{Name: "il-lab", Namespace: "alice-dev", CatalogRef: ref, Status: "deployed", Revision: 1, UID: "helm-uid"}
			err := client.UninstallLabRelease(context.Background(), "alice-dev", "user-token", target, []SpaceRequestResult{cleanupTestSpaceRequest(t, ref)})
			require.EqualError(t, err, "lab release changed or requires manual recovery")
			assert.Empty(t, *mutations)
		})
	}
}
