package handler_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
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
	"github.com/codeready-toolchain/interactive-labs-console-plugin/backend/internal/setup"
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
				Manifest: &manifest.LabManifest{Metadata: manifest.Metadata{Name: "lab", DisplayName: "Manifest Lab", Description: "Manifest description"}},
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
		BundleLoader: func(_ context.Context, repository, _, _ string) (*bundle.Result, error) {
			name := "Lab A"
			if strings.Contains(repository, "lab-z") {
				name = "Lab Z"
			}
			return &bundle.Result{Manifest: &manifest.LabManifest{Metadata: manifest.Metadata{Name: name, DisplayName: name}}, Pages: map[string]string{"intro.adoc": "= Intro"}}, nil
		},
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

var testValuesSchema = []byte(`{
	"properties": {
		"namespaces": {
			"type": "object",
			"properties": {
				"gitea": {
					"type": "string",
					"x-interactive-labs": { "kind": "namespace-name" }
				},
				"datasphere": {
					"type": "string",
					"x-interactive-labs": { "kind": "namespace-name" }
				}
			}
		}
	}
}`)

func stubSetupClient(t *testing.T) (*setup.SpaceRequestClient, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(server.Close)
	return &setup.SpaceRequestClient{HTTPClient: server.Client(), APIHost: server.URL}, server
}

func newSetupRouter(t *testing.T) http.Handler {
	t.Helper()
	client, _ := stubSetupClient(t)
	return handler.NewRouter(handler.Config{
		CatalogSource: goodCatalogCM,
		Authenticate: func(_ context.Context, token string) (string, error) {
			return token, nil
		},
		BundleLoader: func(_ context.Context, _, _, _ string) (*bundle.Result, error) {
			return &bundle.Result{
				Manifest:           &manifest.LabManifest{Metadata: manifest.Metadata{Name: "lab", DisplayName: "Manifest Lab"}},
				Pages:              map[string]string{"intro.adoc": "= Intro"},
				HelmChartAvailable: true,
				ValuesSchema:       testValuesSchema,
			}, nil
		},
		SetupClient: client,
	})
}

func TestSetupHandler_NoHelmChart_Returns422(t *testing.T) {
	ref := goodCatalogRef()
	rw := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1alpha1/labs/"+ref+"/setup", nil)
	req.Header.Set("Authorization", "Bearer user1")
	newRouter(t).ServeHTTP(rw, req)

	require.Equal(t, http.StatusUnprocessableEntity, rw.Code)
	var body map[string]any
	json.Unmarshal(rw.Body.Bytes(), &body) //nolint:errcheck
	assert.Equal(t, "chart_not_found", body["type"])
}

func TestSetupHandler_ValidSchema_Returns202(t *testing.T) {
	ref := goodCatalogRef()
	rw := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1alpha1/labs/"+ref+"/setup", nil)
	req.Header.Set("Authorization", "Bearer "+makeTestJWT("user1"))
	newSetupRouter(t).ServeHTTP(rw, req)

	require.Equal(t, http.StatusAccepted, rw.Code)
	var body map[string]any
	json.Unmarshal(rw.Body.Bytes(), &body) //nolint:errcheck
	assert.Equal(t, "SetupResult", body["kind"])
	assert.Equal(t, "accepted", body["outcome"])
}

func TestSetupHandler_NoAuthorizationEchoed(t *testing.T) {
	rw := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1alpha1/labs/"+goodCatalogRef()+"/setup", nil)
	req.Header.Set("Authorization", "Bearer eyJtest.token.value")
	newSetupRouter(t).ServeHTTP(rw, req)

	assertNoTokenInBody(t, rw.Body.String())
}

func TestSetupHandler_TokenNotLogged(t *testing.T) {
	rw := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1alpha1/labs/"+goodCatalogRef()+"/setup", nil)
	req.Header.Set("Authorization", "Bearer eyJtest.token.value")
	newSetupRouter(t).ServeHTTP(rw, req)
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

// --- GET /api/v1alpha1/labs/{id}/setup/status ---

func TestSetupStatusHandler_AllReady(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"metadata":{"name":"test"},"status":{"namespaceAccess":[{"name":"ns-abc","secretRef":"s"}],"conditions":[{"type":"Ready","status":"True"}]}}`)
	}))
	defer server.Close()

	router := handler.NewRouter(handler.Config{
		CatalogSource: goodCatalogCM,
		Authenticate:  func(_ context.Context, token string) (string, error) { return token, nil },
		BundleLoader: func(_ context.Context, _, _, _ string) (*bundle.Result, error) {
			return &bundle.Result{
				Manifest:           &manifest.LabManifest{Metadata: manifest.Metadata{Name: "lab", DisplayName: "Lab"}},
				Pages:              map[string]string{"intro.adoc": "= Intro"},
				HelmChartAvailable: true,
				ValuesSchema:       testValuesSchema,
			}, nil
		},
		SetupClient: &setup.SpaceRequestClient{HTTPClient: server.Client(), APIHost: server.URL},
	})

	ref := goodCatalogRef()
	rw := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1alpha1/labs/"+ref+"/setup/status", nil)
	req.Header.Set("Authorization", "Bearer user1")
	router.ServeHTTP(rw, req)

	require.Equal(t, http.StatusOK, rw.Code)
	var body map[string]any
	json.Unmarshal(rw.Body.Bytes(), &body) //nolint:errcheck
	assert.Equal(t, "SetupStatus", body["kind"])
	assert.Equal(t, "ready", body["status"])
}

func TestSetupStatusHandler_Provisioning(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"metadata":{"name":"test"},"status":{"conditions":[{"type":"Ready","status":"False","reason":"Provisioning"}]}}`)
	}))
	defer server.Close()

	router := handler.NewRouter(handler.Config{
		CatalogSource: goodCatalogCM,
		Authenticate:  func(_ context.Context, token string) (string, error) { return token, nil },
		BundleLoader: func(_ context.Context, _, _, _ string) (*bundle.Result, error) {
			return &bundle.Result{
				Manifest:           &manifest.LabManifest{Metadata: manifest.Metadata{Name: "lab", DisplayName: "Lab"}},
				Pages:              map[string]string{"intro.adoc": "= Intro"},
				HelmChartAvailable: true,
				ValuesSchema:       testValuesSchema,
			}, nil
		},
		SetupClient: &setup.SpaceRequestClient{HTTPClient: server.Client(), APIHost: server.URL},
	})

	ref := goodCatalogRef()
	rw := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1alpha1/labs/"+ref+"/setup/status", nil)
	req.Header.Set("Authorization", "Bearer user1")
	router.ServeHTTP(rw, req)

	require.Equal(t, http.StatusOK, rw.Code)
	var body map[string]any
	json.Unmarshal(rw.Body.Bytes(), &body) //nolint:errcheck
	assert.Equal(t, "provisioning", body["status"])
}

func TestSetupStatusHandler_RequiresAuth(t *testing.T) {
	ref := goodCatalogRef()
	rw := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1alpha1/labs/"+ref+"/setup/status", nil)
	newSetupRouter(t).ServeHTTP(rw, req)

	require.Equal(t, http.StatusUnauthorized, rw.Code)
}

func TestCatalogHandler_UsesManifestMetadataAndOmitsOnlyClassifiedFailures(t *testing.T) {
	var logs bytes.Buffer
	var warnings []string
	validRef := catalog.DeriveRef("https://github.com/redhat/good", "main", "lab", nil)
	router := handler.NewRouter(handler.Config{
		CatalogSource: stubCatalogCM(map[string]string{"catalog.yaml": `- displayName: Legacy
  description: Legacy
  repository: https://github.com/redhat/good
  branch: main
  labPath: lab
- repository: https://github.com/redhat/missing
  branch: main
  labPath: lab`}),
		BundleLoader: func(_ context.Context, repository, _, _ string) (*bundle.Result, error) {
			if strings.Contains(repository, "missing") {
				return nil, fmt.Errorf("%w: private details", bundle.ErrManifestMissing)
			}
			return &bundle.Result{Manifest: &manifest.LabManifest{Metadata: manifest.Metadata{Name: "good", DisplayName: "Manifest name", Description: "Manifest description"}}, Pages: map[string]string{"intro.adoc": "= Intro"}}, nil
		},
		Logger:            slog.New(slog.NewJSONHandler(&logs, nil)),
		CatalogLabOmitted: func(_ context.Context, ref, reason string) { warnings = append(warnings, ref+":"+reason) },
	})

	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1alpha1/catalog", nil))
	require.Equal(t, http.StatusOK, rw.Code)
	var body struct {
		Items []struct {
			CatalogRef  string `json:"catalogRef"`
			DisplayName string `json:"displayName"`
			Description string `json:"description"`
		} `json:"items"`
	}
	require.NoError(t, json.Unmarshal(rw.Body.Bytes(), &body))
	require.Equal(t, []struct {
		CatalogRef  string `json:"catalogRef"`
		DisplayName string `json:"displayName"`
		Description string `json:"description"`
	}{{validRef, "Manifest name", "Manifest description"}}, body.Items)
	assert.Equal(t, []string{catalog.DeriveRef("https://github.com/redhat/missing", "main", "lab", nil) + ":ManifestMissing"}, warnings)
	assert.Contains(t, logs.String(), "catalog lab omitted")
	assert.NotContains(t, logs.String(), "private details")
	assert.NotContains(t, logs.String(), "https://github.com/redhat/missing")
}

func TestCatalogHandler_UnexpectedLoaderFailureIsNotOmitted(t *testing.T) {
	router := handler.NewRouter(handler.Config{
		CatalogSource: goodCatalogCM,
		BundleLoader: func(context.Context, string, string, string) (*bundle.Result, error) {
			return nil, errors.New("unexpected")
		},
	})
	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1alpha1/catalog", nil))
	assert.Equal(t, http.StatusBadGateway, rw.Code)
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
	payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"sub":%q}`, sub))) //nolint:fmtappendf
	return header + "." + payload + "."
}
