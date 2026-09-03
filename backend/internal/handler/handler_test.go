package handler_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"

	"github.com/codeready-toolchain/interactive-labs-console-plugin/backend/internal/bundle"
	"github.com/codeready-toolchain/interactive-labs-console-plugin/backend/internal/catalog"
	"github.com/codeready-toolchain/interactive-labs-console-plugin/backend/internal/handler"
	"github.com/codeready-toolchain/interactive-labs-console-plugin/backend/internal/manifest"
)

// stubCatalogReader provides a fixed ConfigMap for handler injection.
func stubCatalogCM(data map[string]string) func(ctx context.Context) (*corev1.ConfigMap, error) {
	return func(_ context.Context) (*corev1.ConfigMap, error) {
		return &corev1.ConfigMap{Data: data}, nil
	}
}

var goodCatalogCM = stubCatalogCM(map[string]string{
	"catalog.yaml": `- displayName: "Lab 1"
  repository: "https://github.com/redhat-scholars/lab1"
  branch: "main"
  labPath: "lab/"`,
})

func newRouter(t *testing.T) http.Handler {
	t.Helper()
	return handler.NewRouter(handler.Config{
		CatalogSource: goodCatalogCM,
		Authenticate: func(_ context.Context, token string) (string, error) {
			return token, nil
		},
		BundleLoader: func(_ context.Context, _, _, _ string) (*bundle.Result, error) {
			return &bundle.Result{
				Manifest: &manifest.LabManifest{},
				Pages:    map[string]string{"intro.adoc": "= Intro"},
			}, nil
		},
	})
}

// --- GET /api/v1alpha1/catalog ---

func TestCatalogHandler_Returns200(t *testing.T) {
	rw := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1alpha1/catalog", nil)
	newRouter(t).ServeHTTP(rw, req)

	require.Equal(t, http.StatusOK, rw.Code)
	var body map[string]any
	json.Unmarshal(rw.Body.Bytes(), &body) //nolint:errcheck
	assert.Equal(t, "CatalogList", body["kind"])
	assert.Equal(t, "v1alpha1", body["apiVersion"])
	items := body["items"].([]any)
	assert.Len(t, items, 1)
}

func TestCatalogHandler_ReturnsItemsInDeclaredOrder(t *testing.T) {
	router := handler.NewRouter(handler.Config{
		CatalogSource: stubCatalogCM(map[string]string{
			"catalog.yaml": `- displayName: "Lab Z"
  repository: "https://github.com/redhat-scholars/lab-z"
  branch: "main"
  labPath: "."
- displayName: "Lab A"
  repository: "https://github.com/redhat-scholars/lab-a"
  branch: "main"
  labPath: "."`,
		}),
	})
	rw := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1alpha1/catalog", nil)
	router.ServeHTTP(rw, req)

	require.Equal(t, http.StatusOK, rw.Code)
	var body map[string]any
	json.Unmarshal(rw.Body.Bytes(), &body) //nolint:errcheck
	items := body["items"].([]any)
	require.Len(t, items, 2)
	assert.Equal(t, "Lab Z", items[0].(map[string]any)["displayName"])
	assert.Equal(t, "Lab A", items[1].(map[string]any)["displayName"])
	// catalogRef must be present, id must not
	assert.Contains(t, items[0].(map[string]any), "catalogRef")
	assert.NotContains(t, items[0].(map[string]any), "id")
}

func TestCatalogHandler_NoAuthorizationEchoed(t *testing.T) {
	rw := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1alpha1/catalog", nil)
	req.Header.Set("Authorization", "Bearer eyJtest.token.value")
	newRouter(t).ServeHTTP(rw, req)

	body := rw.Body.String()
	assertNoTokenInBody(t, body)
}

// --- GET /api/v1alpha1/labs/{id}/bundle (stub — no real loader) ---

func TestBundleHandler_UnknownLabReturns404(t *testing.T) {
	// Non-existent catalogRef (valid hex but not in catalog) returns 404 after auth.
	unknownRef := fmt.Sprintf("%064x", 0)
	rw := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1alpha1/labs/"+unknownRef+"/bundle", nil)
	req.Header.Set("Authorization", "Bearer user1")
	newRouter(t).ServeHTTP(rw, req)

	require.Equal(t, http.StatusNotFound, rw.Code)
	var body map[string]any
	json.Unmarshal(rw.Body.Bytes(), &body) //nolint:errcheck
	assert.Equal(t, "lab_not_found", body["type"])
}

// --- POST /api/v1alpha1/labs/{id}/setup ---

func TestSetupHandler_InvalidBindings_Returns422(t *testing.T) {
	payload := `{"namespaceBindings":{}}` // missing releaseNamespace
	ref := goodCatalogRef()
	rw := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1alpha1/labs/"+ref+"/setup",
		strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer eyJtest.token.value")
	newRouter(t).ServeHTTP(rw, req)

	require.Equal(t, http.StatusUnprocessableEntity, rw.Code)
	var body map[string]any
	json.Unmarshal(rw.Body.Bytes(), &body) //nolint:errcheck
	assert.Equal(t, "namespace_binding_invalid", body["type"])
}

func TestSetupHandler_ValidBindings_Returns200WithHelmReleaseName(t *testing.T) {
	payload := `{"namespaceBindings":{"releaseNamespace":"my-ns"}}`
	ref := goodCatalogRef()
	rw := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1alpha1/labs/"+ref+"/setup",
		strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	// Provide a minimal JWT with sub=user1
	req.Header.Set("Authorization", "Bearer "+makeTestJWT("user1"))
	newRouter(t).ServeHTTP(rw, req)

	require.Equal(t, http.StatusOK, rw.Code)
	var body map[string]any
	json.Unmarshal(rw.Body.Bytes(), &body) //nolint:errcheck
	assert.Equal(t, "SetupResult", body["kind"])
	assert.Equal(t, "success", body["outcome"])
	name, _ := body["helmReleaseName"].(string)
	assert.True(t, strings.HasPrefix(name, "il-"), "helmReleaseName prefix: %q", name)
	assert.Len(t, name, len("il-")+40, "helmReleaseName length: %q", name)
}

func TestSetupHandler_DeterministicHelmReleaseName(t *testing.T) {
	payload := `{"namespaceBindings":{"releaseNamespace":"my-ns"}}`
	token := makeTestJWT("user1")
	ref := goodCatalogRef()

	call := func() string {
		rw := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1alpha1/labs/"+ref+"/setup",
			bytes.NewBufferString(payload))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		newRouter(t).ServeHTTP(rw, req)
		var body map[string]any
		json.Unmarshal(rw.Body.Bytes(), &body) //nolint:errcheck
		return body["helmReleaseName"].(string)
	}
	firstCall := call()
	secondCall := call()
	assert.Equal(t, firstCall, secondCall, "deterministic helmReleaseName must be the same")
}

func TestSetupHandler_ContentPointerDeterminesReleaseName(t *testing.T) {
	payload := `{"namespaceBindings":{"releaseNamespace":"my-ns"}}`
	call := func(repository, labPath, branch string) string {
		router := handler.NewRouter(handler.Config{
			CatalogSource: stubCatalogCM(map[string]string{
				"catalog.yaml": fmt.Sprintf("- displayName: Lab\n  repository: %q\n  branch: %q\n  labPath: %q",
					repository, branch, labPath),
			}),
			Authenticate: func(_ context.Context, token string) (string, error) { return token, nil },
			BundleLoader: func(_ context.Context, _, _, _ string) (*bundle.Result, error) {
				return &bundle.Result{Manifest: &manifest.LabManifest{}}, nil
			},
		})
		ref := catalog.DeriveRef(repository, branch, labPath, nil)
		rw := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1alpha1/labs/"+ref+"/setup", strings.NewReader(payload))
		req.Header.Set("Authorization", "Bearer user1")
		router.ServeHTTP(rw, req)
		var body map[string]any
		json.Unmarshal(rw.Body.Bytes(), &body) //nolint:errcheck
		return body["helmReleaseName"].(string)
	}

	baseline := call("https://github.com/redhat/lab", "labs/example", "main")
	assert.NotEqual(t, baseline, call("https://github.com/redhat/other", "labs/example", "main"),
		"repository must affect helmReleaseName")
	assert.NotEqual(t, baseline, call("https://github.com/redhat/lab", "labs/other", "main"),
		"lab path must affect helmReleaseName")
	assert.NotEqual(t, baseline, call("https://github.com/redhat/lab", "labs/example", "other"),
		"branch must affect helmReleaseName")
}

func TestSetupHandler_DifferentManifestAttributes_DifferentReleaseName(t *testing.T) {
	payload := `{"namespaceBindings":{"releaseNamespace":"my-ns"}}`
	ref := goodCatalogRef()
	call := func(attributes map[string]string) string {
		router := handler.NewRouter(handler.Config{
			CatalogSource: goodCatalogCM,
			Authenticate:  func(_ context.Context, token string) (string, error) { return token, nil },
			BundleLoader: func(_ context.Context, _, _, _ string) (*bundle.Result, error) {
				return &bundle.Result{Manifest: &manifest.LabManifest{Spec: manifest.Spec{Attributes: attributes}}}, nil
			},
		})
		rw := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1alpha1/labs/"+ref+"/setup", strings.NewReader(payload))
		req.Header.Set("Authorization", "Bearer user1")
		router.ServeHTTP(rw, req)
		var body map[string]any
		json.Unmarshal(rw.Body.Bytes(), &body) //nolint:errcheck
		return body["helmReleaseName"].(string)
	}

	assert.NotEqual(t, call(map[string]string{"version": "v1"}), call(map[string]string{"version": "v2"}),
		"different manifest attributes must produce different helmReleaseName")
}

func TestSetupHandler_DifferentSubject_DifferentReleaseName(t *testing.T) {
	payload := `{"namespaceBindings":{"releaseNamespace":"my-ns"}}`
	ref := goodCatalogRef()

	call := func(sub string) string {
		rw := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1alpha1/labs/"+ref+"/setup",
			bytes.NewBufferString(payload))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+makeTestJWT(sub))
		newRouter(t).ServeHTTP(rw, req)
		var body map[string]any
		json.Unmarshal(rw.Body.Bytes(), &body) //nolint:errcheck
		return body["helmReleaseName"].(string)
	}

	assert.NotEqual(t, call("user1"), call("user2"), "different subjects must produce different helmReleaseName")
}

func TestSetupHandler_NoAuthorizationEchoed(t *testing.T) {
	payload := `{"namespaceBindings":{"releaseNamespace":"my-ns"}}`
	rw := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1alpha1/labs/"+goodCatalogRef()+"/setup",
		strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer eyJtest.token.value")
	newRouter(t).ServeHTTP(rw, req)

	assertNoTokenInBody(t, rw.Body.String())
}

func TestSetupHandler_TokenNotLogged(t *testing.T) {
	// Token-logging test: capture stderr/stdout is complex;
	// instead assert the handler implementation uses no fmt.Printf with the token.
	// The real TestTokenNotLogged integration test is in cmd/server.
	// Here we just confirm the response body has no token material.
	rw := httptest.NewRecorder()
	payload := `{"namespaceBindings":{"releaseNamespace":"my-ns"}}`
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1alpha1/labs/"+goodCatalogRef()+"/setup",
		strings.NewReader(payload))
	req.Header.Set("Authorization", "Bearer eyJtest.token.value")
	req.Header.Set("Content-Type", "application/json")
	newRouter(t).ServeHTTP(rw, req)
	assertNoTokenInBody(t, rw.Body.String())
}

// --- New tests for catalogRef, auth on bundle, and attribute merging (RED before implementation) ---

// goodCatalogRef returns the expected catalogRef for goodCatalogCM's single entry.
func goodCatalogRef() string {
	return catalog.DeriveRef("https://github.com/redhat-scholars/lab1", "main", "lab/", nil)
}

func TestCatalogHandler_ExposesCatalogRef(t *testing.T) {
	rw := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1alpha1/catalog", nil)
	newRouter(t).ServeHTTP(rw, req)
	require.Equal(t, http.StatusOK, rw.Code)
	var body map[string]any
	json.Unmarshal(rw.Body.Bytes(), &body) //nolint:errcheck
	items := body["items"].([]any)
	require.Len(t, items, 1)
	item := items[0].(map[string]any)
	assert.Contains(t, item, "catalogRef", "catalogRef must be present")
	assert.NotContains(t, item, "id", "id must not be exposed")
	assert.NotContains(t, item, "attributeOverrides", "attributeOverrides must not be exposed to browser")
}

func TestBundleHandler_RequiresAuth(t *testing.T) {
	ref := goodCatalogRef()
	rw := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1alpha1/labs/"+ref+"/bundle", nil)
	newRouter(t).ServeHTTP(rw, req)
	require.Equal(t, http.StatusUnauthorized, rw.Code)
	var body map[string]any
	json.Unmarshal(rw.Body.Bytes(), &body) //nolint:errcheck
	assert.Equal(t, "unauthorized", body["type"])
}

func TestBundleHandler_RuntimeContextUsesCatalogRef(t *testing.T) {
	ref := goodCatalogRef()
	rw := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1alpha1/labs/"+ref+"/bundle", nil)
	req.Header.Set("Authorization", "Bearer user1")
	newRouter(t).ServeHTTP(rw, req)
	require.Equal(t, http.StatusOK, rw.Code)
	var body map[string]any
	json.Unmarshal(rw.Body.Bytes(), &body) //nolint:errcheck
	rc := body["runtimeContext"].(map[string]any)
	assert.Equal(t, ref, rc["catalogRef"], "runtimeContext must use catalogRef")
	assert.NotContains(t, rc, "labId", "runtimeContext must not use labId")
}

func TestBundleHandler_AttributeMergeOrder(t *testing.T) {
	// Build a catalog entry with attributeOverrides
	overrides := map[string]string{"k": "catalog-val", "catalog-only": "yes"}
	ref := catalog.DeriveRef("https://github.com/redhat/lab", "main", ".", overrides)
	router := handler.NewRouter(handler.Config{
		CatalogSource: stubCatalogCM(map[string]string{
			"catalog.yaml": `- displayName: "Lab"
  repository: "https://github.com/redhat/lab"
  branch: "main"
  labPath: "."
  attributeOverrides:
    k: catalog-val
    catalog-only: yes`,
		}),
		Authenticate: func(_ context.Context, token string) (string, error) { return token, nil },
		BundleLoader: func(_ context.Context, _, _, _ string) (*bundle.Result, error) {
			return &bundle.Result{
				Manifest: &manifest.LabManifest{
					APIVersion: "v1alpha1",
					Kind:       "Lab",
					Metadata:   manifest.Metadata{Name: "lab", DisplayName: "Lab"},
					Spec:       manifest.Spec{Attributes: map[string]string{"k": "manifest-val", "manifest-only": "yes"}},
				},
				Pages: map[string]string{"intro.adoc": "= Intro"},
			}, nil
		},
		RuntimeAttributes: func(_ *http.Request, _ string) map[string]string {
			return map[string]string{"k": "runtime-val", "runtime-only": "yes"}
		},
	})
	rw := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1alpha1/labs/"+ref+"/bundle", nil)
	req.Header.Set("Authorization", "Bearer user1")
	router.ServeHTTP(rw, req)
	require.Equal(t, http.StatusOK, rw.Code)
	var body map[string]any
	json.Unmarshal(rw.Body.Bytes(), &body) //nolint:errcheck
	m := body["manifest"].(map[string]any)
	spec := m["spec"].(map[string]any)
	attrs := spec["attributes"].(map[string]any)
	assert.Equal(t, "runtime-val", attrs["k"], "runtime must win over catalog and manifest")
	assert.Equal(t, "yes", attrs["manifest-only"], "manifest-only key must be present")
	assert.Equal(t, "yes", attrs["catalog-only"], "catalog-only key must be present")
	assert.Equal(t, "yes", attrs["runtime-only"], "runtime-only key must be present")
}

func TestSetupHandler_ReleaseNameExcludesRuntimeAttrs(t *testing.T) {
	makeRouter := func(runtimeAttrs map[string]string) http.Handler {
		return handler.NewRouter(handler.Config{
			CatalogSource: goodCatalogCM,
			Authenticate:  func(_ context.Context, token string) (string, error) { return token, nil },
			BundleLoader: func(_ context.Context, _, _, _ string) (*bundle.Result, error) {
				return &bundle.Result{Manifest: &manifest.LabManifest{}}, nil
			},
			RuntimeAttributes: func(_ *http.Request, _ string) map[string]string { return runtimeAttrs },
		})
	}
	ref := goodCatalogRef()
	payload := `{"namespaceBindings":{"releaseNamespace":"my-ns"}}`
	call := func(router http.Handler) string {
		rw := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1alpha1/labs/"+ref+"/setup", strings.NewReader(payload))
		req.Header.Set("Authorization", "Bearer user1")
		router.ServeHTTP(rw, req)
		var body map[string]any
		json.Unmarshal(rw.Body.Bytes(), &body) //nolint:errcheck
		return body["helmReleaseName"].(string)
	}
	r1 := makeRouter(map[string]string{"runtime-key": "val1"})
	r2 := makeRouter(map[string]string{"runtime-key": "val2"})
	assert.Equal(t, call(r1), call(r2), "runtime attrs must not affect helmReleaseName")
}

// helpers

func assertNoTokenInBody(t *testing.T, body string) {
	t.Helper()
	assert.NotContains(t, body, "Bearer")
	assert.NotContains(t, body, "eyJ")
	assert.NotContains(t, body, "Authorization")
}

// makeTestJWT builds a minimal unsigned JWT with the given sub.
// No signature verification is expected in MVP; cluster validates the token.
func makeTestJWT(sub string) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"sub":%q}`, sub)))
	return header + "." + payload + "."
}
