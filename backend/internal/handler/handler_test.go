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
	"os"
	"path/filepath"
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
				Manifest: &manifest.LabManifest{Metadata: manifest.Metadata{Name: "lab", DisplayName: "Manifest Lab", Description: "Manifest description"}},
				Pages:    []string{"intro.adoc"},
			}, nil
		},
		RuntimeAttributes: func(_ *http.Request, subject string) (map[string]string, error) {
			return map[string]string{
				"user": subject, "openshift_api_url": "https://api.example.test", "openshift_console_url": "https://console.example.test", "openshift_cluster_ingress_domain": "apps.example.test",
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
			return &bundle.Result{Manifest: &manifest.LabManifest{Metadata: manifest.Metadata{Name: name, DisplayName: name}}, Pages: []string{"intro.adoc"}}, nil
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

func TestCatalogHandler_ExposesNormalizedLabPath(t *testing.T) {
	rw := httptest.NewRecorder()
	newRouter(t).ServeHTTP(rw, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1alpha1/catalog", nil))

	require.Equal(t, http.StatusOK, rw.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rw.Body.Bytes(), &body))
	item := body["items"].([]any)[0].(map[string]any)
	assert.Equal(t, "lab", item["labPath"])
	assert.Equal(t, "Manifest description", item["description"])
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
}

func TestBundleHandler_ReturnsSortedNavigablePageDescriptors(t *testing.T) {
	ref := goodCatalogRef()
	rw := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1alpha1/labs/"+ref+"/bundle", nil)
	req.Header.Set("Authorization", "Bearer user1")
	newRouter(t).ServeHTTP(rw, req)

	require.Equal(t, http.StatusOK, rw.Code)
	var body struct {
		Pages []struct {
			Path string `json:"path"`
			URL  string `json:"url"`
		} `json:"pages"`
	}
	require.NoError(t, json.Unmarshal(rw.Body.Bytes(), &body))
	assert.Equal(t, []struct {
		Path string `json:"path"`
		URL  string `json:"url"`
	}{
		{Path: "intro.adoc", URL: "api/v1alpha1/labs/" + ref + "/resources/lab/content/intro.adoc"},
	}, body.Pages)
	assert.NotContains(t, rw.Body.String(), "= Intro")
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
	// Build a catalog entry with attributeOverrides.
	overrides := map[string]string{"user": "catalog-val", "catalog-only": "yes"}
	ref := catalog.DeriveRef("https://github.com/redhat/lab", "main", ".", overrides)
	router := handler.NewRouter(handler.Config{
		CatalogSource: stubCatalogCM(map[string]string{
			"catalog.yaml": `- displayName: "Lab"
  repository: "https://github.com/redhat/lab"
  branch: "main"
  labPath: "."
  attributeOverrides:
    user: catalog-val
    catalog-only: yes`,
		}),
		Authenticate: func(_ context.Context, token string) (string, error) { return token, nil },
		BundleLoader: func(_ context.Context, _, _, _ string) (*bundle.Result, error) {
			return &bundle.Result{
				Manifest: &manifest.LabManifest{
					APIVersion: "v1alpha1",
					Kind:       "Lab",
					Metadata:   manifest.Metadata{Name: "lab", DisplayName: "Lab"},
					Spec:       manifest.Spec{Attributes: map[string]string{"user": "manifest-val", "manifest-only": "yes"}},
				},
				Pages: []string{"intro.adoc"},
			}, nil
		},
		RuntimeAttributes: func(_ *http.Request, _ string) (map[string]string, error) {
			return map[string]string{
				"user": "runtime-val", "openshift_api_url": "https://api.example.test", "openshift_console_url": "https://console.example.test", "openshift_cluster_ingress_domain": "apps.example.test",
			}, nil
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
	assert.Equal(t, "runtime-val", attrs["user"], "runtime must win over catalog and manifest")
	assert.Equal(t, "yes", attrs["manifest-only"], "manifest-only key must be present")
	assert.Equal(t, "yes", attrs["catalog-only"], "catalog-only key must be present")
	assert.Equal(t, "https://api.example.test", attrs["openshift_api_url"], "runtime values must be present")
}

func TestBundleHandler_RuntimeAttributesAreAllOrSafeFailure(t *testing.T) {
	ref := goodCatalogRef()
	for _, tc := range []struct {
		name  string
		attrs map[string]string
		err   error
	}{
		{name: "callback failure", err: errors.New("cluster config unavailable")},
		{name: "incomplete values", attrs: map[string]string{
			"user": "user1", "openshift_api_url": "https://api.example.test", "openshift_console_url": "https://console.example.test",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router := handler.NewRouter(handler.Config{
				CatalogSource: goodCatalogCM,
				Authenticate:  func(_ context.Context, token string) (string, error) { return token, nil },
				BundleLoader: func(_ context.Context, _, _, _ string) (*bundle.Result, error) {
					return &bundle.Result{Manifest: &manifest.LabManifest{}, Pages: []string{"intro.adoc"}}, nil
				},
				RuntimeAttributes: func(_ *http.Request, _ string) (map[string]string, error) {
					return tc.attrs, tc.err
				},
			})
			rw := httptest.NewRecorder()
			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1alpha1/labs/"+ref+"/bundle", nil)
			req.Header.Set("Authorization", "Bearer user1")
			router.ServeHTTP(rw, req)

			require.Equal(t, http.StatusServiceUnavailable, rw.Code)
			var body map[string]any
			require.NoError(t, json.Unmarshal(rw.Body.Bytes(), &body))
			assert.Equal(t, "runtime_attributes_unavailable", body["type"])
		})
	}
}

func TestSetupHandler_ReleaseNameExcludesRuntimeAttrs(t *testing.T) {
	makeRouter := func(runtimeAttrs map[string]string) http.Handler {
		return handler.NewRouter(handler.Config{
			CatalogSource: goodCatalogCM,
			Authenticate:  func(_ context.Context, token string) (string, error) { return token, nil },
			BundleLoader: func(_ context.Context, _, _, _ string) (*bundle.Result, error) {
				return &bundle.Result{Manifest: &manifest.LabManifest{}}, nil
			},
			RuntimeAttributes: func(_ *http.Request, _ string) (map[string]string, error) {
				return runtimeAttrs, nil
			},
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
			return &bundle.Result{Manifest: &manifest.LabManifest{Metadata: manifest.Metadata{Name: "good", DisplayName: "Manifest name", Description: "Manifest description"}}, Pages: []string{"intro.adoc"}}, nil
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

// --- GET /api/v1alpha1/labs/{catalogRef}/resources/{repositoryRelativePath...} ---

func TestResourceHandler_ServesApprovedResourcesWithSafeHeaders(t *testing.T) {
	root := resourceFixture(t)
	ref := goodCatalogRef()
	router := resourceRouter(t, goodCatalogCM, root, "abc123")

	for _, tc := range []struct {
		name        string
		resource    string
		wantBody    []byte
		contentType string
	}{
		{name: "page", resource: "lab/content/intro.adoc", wantBody: []byte("= Intro\n"), contentType: "text/plain; charset=utf-8"},
		{name: "partial", resource: "lab/partials/_shared.adoc", wantBody: []byte("Shared\n"), contentType: "text/plain; charset=utf-8"},
		{name: "raster", resource: "lab/images/pixel.png", wantBody: pngBytes(), contentType: "image/png"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rw := resourceRequest(t, router, ref, tc.resource)
			require.Equal(t, http.StatusOK, rw.Code)
			assert.Equal(t, tc.wantBody, rw.Body.Bytes())
			assert.Equal(t, tc.contentType, rw.Header().Get("Content-Type"))
			assert.Equal(t, "nosniff", rw.Header().Get("X-Content-Type-Options"))
			assert.Equal(t, "private, no-cache", rw.Header().Get("Cache-Control"))
			assert.Equal(t, `"abc123"`, rw.Header().Get("ETag"))
		})
	}
}

func TestResourceHandler_SniffsContentTypeAfterLoading(t *testing.T) {
	ref := goodCatalogRef()
	router := handler.NewRouter(handler.Config{
		CatalogSource: goodCatalogCM,
		Authenticate:  func(_ context.Context, token string) (string, error) { return token, nil },
		SnapshotCommitHash: func(context.Context, string, string, string) (string, error) {
			return "abc123", nil
		},
		ResourceLoader: func(context.Context, string, string, string, string) (*bundle.Resource, error) {
			return &bundle.Resource{Bytes: []byte("= Intro\n")}, nil
		},
	})

	rw := resourceRequest(t, router, ref, "lab/content/intro.adoc")

	require.Equal(t, http.StatusOK, rw.Code)
	assert.Equal(t, "text/plain; charset=utf-8", rw.Header().Get("Content-Type"))
}

func TestResourceHandler_ReturnsNotModifiedBeforeLoadingResource(t *testing.T) {
	loads := 0
	ref := goodCatalogRef()
	router := handler.NewRouter(handler.Config{
		CatalogSource: goodCatalogCM,
		Authenticate:  func(_ context.Context, token string) (string, error) { return token, nil },
		SnapshotCommitHash: func(context.Context, string, string, string) (string, error) {
			return "abc123", nil
		},
		ResourceLoader: func(context.Context, string, string, string, string) (*bundle.Resource, error) {
			loads++
			return nil, errors.New("resource loader must not run")
		},
	})
	request := httptest.NewRequest(http.MethodGet, "/api/v1alpha1/labs/"+ref+"/resources/lab/content/intro.adoc", nil)
	request.Header.Set("Authorization", "Bearer user1")
	request.Header.Set("If-None-Match", `"abc123"`)
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, request)

	assert.Equal(t, http.StatusNotModified, recorder.Code)
	assert.Zero(t, loads)
	assert.Equal(t, `"abc123"`, recorder.Header().Get("ETag"))
	assert.Equal(t, "private, no-cache", recorder.Header().Get("Cache-Control"))
}

func TestResourceHandler_HonorsQuotedETagForConditionalGET(t *testing.T) {
	root := resourceFixture(t)
	ref := goodCatalogRef()
	router := resourceRouter(t, goodCatalogCM, root, "abc123")

	for _, tc := range []struct {
		name        string
		ifNoneMatch string
		wantStatus  int
		wantBody    []byte
	}{
		{name: "mismatch", ifNoneMatch: `"other"`, wantStatus: http.StatusOK, wantBody: []byte("= Intro\n")},
		{name: "quoted exact match", ifNoneMatch: `"abc123"`, wantStatus: http.StatusNotModified},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rw := httptest.NewRecorder()
			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet,
				"/api/v1alpha1/labs/"+ref+"/resources/lab/content/intro.adoc", nil)
			req.Header.Set("Authorization", "Bearer user1")
			req.Header.Set("If-None-Match", tc.ifNoneMatch)
			router.ServeHTTP(rw, req)

			assert.Equal(t, tc.wantStatus, rw.Code)
			assert.Equal(t, `"abc123"`, rw.Header().Get("ETag"))
			assert.Equal(t, tc.wantBody, rw.Body.Bytes())
		})
	}
}

func TestResourceHandler_RejectsInvalidResources(t *testing.T) {
	root := resourceFixture(t)
	outside := filepath.Join(t.TempDir(), "outside.adoc")
	require.NoError(t, os.WriteFile(outside, []byte("outside"), 0o600))
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "lab", "content", "escape.adoc")))
	require.NoError(t, os.Symlink("intro.adoc", filepath.Join(root, "lab", "content", "link.adoc")))

	ref := goodCatalogRef()
	router := resourceRouter(t, goodCatalogCM, root, "abc123")
	for _, tc := range []struct {
		name         string
		resource     string
		wantStatus   int
		wantType     string
		wantRedirect string
	}{
		{name: "missing", resource: "lab/content/missing.adoc", wantStatus: http.StatusNotFound, wantType: "content_error"},
		// these cases makes sure that even if the caller wants to be clever and pass in the path as one url segment, we handle the path on the backend properly and don't allow an escape from the repo root
		{name: "missing encoded", resource: "lab%2Fcontent%2Fmissing.adoc", wantStatus: http.StatusNotFound, wantType: "content_error"},
		{name: "remote", resource: "https://example.test/outside.adoc", wantStatus: http.StatusTemporaryRedirect, wantRedirect: "https:/example.test/outside.adoc"},
		{name: "remote redirected", resource: "https:/example.test/outside.adoc", wantStatus: http.StatusUnprocessableEntity, wantType: "path_traversal"},
		{name: "remote encoded", resource: "https:%2F%2Fexample.test%2Foutside.adoc", wantStatus: http.StatusUnprocessableEntity, wantType: "path_traversal"},
		{name: "absolute", resource: "/etc/passwd", wantStatus: http.StatusTemporaryRedirect, wantRedirect: "etc/passwd"},
		{name: "absolute encoded", resource: "%2Fetc%2Fpasswd", wantStatus: http.StatusUnprocessableEntity, wantType: "path_traversal"},
		// here we just test we cannot obtain a path that would point outside of our repository - we actually obtain a URL that points to a different enpoint completely, giving a 404 after the redirect, so that's correct behavior
		{name: "repository escape", resource: "lab/content/../../../../outside.adoc", wantStatus: http.StatusTemporaryRedirect, wantRedirect: "labs/outside.adoc"},
		{name: "repository escape encoded", resource: "lab%2Fcontent%2F..%2F..%2F..%2Foutside.adoc", wantStatus: http.StatusUnprocessableEntity, wantType: "path_traversal"},
		{name: "directory", resource: "lab/content", wantStatus: http.StatusUnprocessableEntity, wantType: "content_error"},
		{name: "directory encoded", resource: "lab%2Fcontent", wantStatus: http.StatusUnprocessableEntity, wantType: "content_error"},
		{name: "symlink escape", resource: "lab/content/escape.adoc", wantStatus: http.StatusUnprocessableEntity, wantType: "path_traversal"},
		{name: "symlink escape encoded", resource: "lab%2Fcontent%2Fescape.adoc", wantStatus: http.StatusUnprocessableEntity, wantType: "path_traversal"},
		{name: "symlink", resource: "lab/content/link.adoc", wantStatus: http.StatusUnprocessableEntity, wantType: "path_traversal"},
		{name: "symlink encoded", resource: "lab%2Fcontent%2Flink.adoc", wantStatus: http.StatusUnprocessableEntity, wantType: "path_traversal"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rw := resourceRequest(t, router, ref, tc.resource)
			require.Equal(t, tc.wantStatus, rw.Code)
			var body map[string]any
			// redirects don't have a body we can influence but have a target that we should check
			if rw.Code < 300 || rw.Code > 399 {
				require.NoError(t, json.Unmarshal(rw.Body.Bytes(), &body))
				assert.Equal(t, tc.wantType, body["type"])
			} else {
				loc := rw.Header().Get("Location")
				assert.True(t, strings.HasSuffix(loc, tc.wantRedirect), "expected redirect %s but got %s", tc.wantRedirect, loc)
			}
		})
	}
}

func TestResourceHandler_DerivesSourceOnlyFromCatalogRef(t *testing.T) {
	const (
		repository = "https://github.com/redhat/selected"
		branch     = "stable"
		labPath    = "selected-lab"
	)
	ref := catalog.DeriveRef(repository, branch, labPath, nil)
	var gotRepository, gotBranch, gotLabPath string
	router := handler.NewRouter(handler.Config{
		CatalogSource: stubCatalogCM(map[string]string{"catalog.yaml": `- repository: https://github.com/redhat/other
  branch: main
  labPath: other-lab
- repository: https://github.com/redhat/selected
  branch: stable
  labPath: selected-lab`}),
		Authenticate: func(_ context.Context, token string) (string, error) { return token, nil },
		SnapshotCommitHash: func(context.Context, string, string, string) (string, error) {
			return "abc123", nil
		},
		ResourceLoader: func(_ context.Context, repository, branch, labPath, resourcePath string) (*bundle.Resource, error) {
			gotRepository, gotBranch, gotLabPath = repository, branch, labPath
			assert.Equal(t, "selected-lab/content/intro.adoc", resourcePath)
			return &bundle.Resource{Bytes: []byte("= Intro\n")}, nil
		},
	})

	rw := resourceRequest(t, router, ref, "selected-lab/content/intro.adoc")
	require.Equal(t, http.StatusOK, rw.Code)
	assert.Equal(t, repository, gotRepository)
	assert.Equal(t, branch, gotBranch)
	assert.Equal(t, labPath, gotLabPath)
}

func resourceRouter(t *testing.T, catalogSource func(context.Context) (*corev1.ConfigMap, error), root, etag string) http.Handler {
	t.Helper()
	return handler.NewRouter(handler.Config{
		CatalogSource: catalogSource,
		Authenticate:  func(_ context.Context, token string) (string, error) { return token, nil },
		SnapshotCommitHash: func(context.Context, string, string, string) (string, error) {
			return etag, nil
		},
		ResourceLoader: func(_ context.Context, _, _, _, resourcePath string) (*bundle.Resource, error) {
			return bundle.ReadResource(root, resourcePath)
		},
	})
}

func resourceRequest(t *testing.T, router http.Handler, catalogRef, resourcePath string) *httptest.ResponseRecorder {
	t.Helper()
	rw := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet,
		"/api/v1alpha1/labs/"+catalogRef+"/resources/"+resourcePath, nil)
	req.Header.Set("Authorization", "Bearer user1")
	router.ServeHTTP(rw, req)
	return rw
}

func resourceFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, dir := range []string{"lab/content", "lab/partials", "lab/images"} {
		require.NoError(t, os.MkdirAll(filepath.Join(root, dir), 0o755))
	}
	require.NoError(t, os.WriteFile(filepath.Join(root, "lab", "content", "intro.adoc"), []byte("= Intro\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "lab", "partials", "_shared.adoc"), []byte("Shared\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "lab", "images", "pixel.png"), pngBytes(), 0o600))
	return root
}

func pngBytes() []byte {
	return []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}
}

// helpers

// makeTestJWT builds a minimal unsigned JWT with the given sub.
// No signature verification is expected in MVP; cluster validates the token.
func makeTestJWT(sub string) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"sub":%q}`, sub))) //nolint:fmtappendf
	return header + "." + payload + "."
}
