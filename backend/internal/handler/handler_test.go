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
	"sync"
	"sync/atomic"
	"testing"
	"time"

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
		Authenticate: func(_ context.Context, _, token string) (string, error) {
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

func newSetupRouter(t *testing.T) (http.Handler, <-chan struct{}) {
	t.Helper()
	client := prepareSetupClient(t)
	installCh := make(chan struct{})
	return handler.NewRouter(handler.Config{
		CatalogSource: goodCatalogCM,
		Authenticate: func(_ context.Context, _, token string) (string, error) {
			return token, nil
		},
		BundleLoader: func(_ context.Context, _, _, _ string) (*bundle.Result, error) {
			return &bundle.Result{
				Manifest:           &manifest.LabManifest{Metadata: manifest.Metadata{Name: "lab", DisplayName: "Manifest Lab"}},
				Pages:              []string{"= Intro"},
				HelmChartAvailable: true,
				HelmChartDir:       t.TempDir(),
				ValuesSchema:       testValuesSchema,
			}, nil
		},
		SetupClient: client,
		HelmManager: &stubHelmManager{statusErr: errors.New("not found"), installCh: installCh},
		HelmTimeout: 5 * time.Second,
	}), installCh
}

// --- GET /api/v1alpha1/catalog ---

func TestCatalogHandler(t *testing.T) {
	t.Run("returns 200", func(t *testing.T) {
		rw := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1alpha1/catalog", nil)
		newRouter(t).ServeHTTP(rw, req)

		require.Equal(t, http.StatusOK, rw.Code)
		var body map[string]any
		require.NoError(t, json.Unmarshal(rw.Body.Bytes(), &body))
		assert.Equal(t, "CatalogList", body["kind"])
		assert.Equal(t, "v1alpha1", body["apiVersion"])
		items := body["items"].([]any)
		assert.Len(t, items, 1)
	})

	t.Run("returns items in declared order", func(t *testing.T) {
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
		require.NoError(t, json.Unmarshal(rw.Body.Bytes(), &body))
		items := body["items"].([]any)
		require.Len(t, items, 2)
		assert.Equal(t, "Lab Z", items[0].(map[string]any)["displayName"])
		assert.Equal(t, "Lab A", items[1].(map[string]any)["displayName"])
		// catalogRef must be present, id must not
		assert.Contains(t, items[0].(map[string]any), "catalogRef")
		assert.NotContains(t, items[0].(map[string]any), "id")
	})

	t.Run("exposes normalized lab path", func(t *testing.T) {
		rw := httptest.NewRecorder()
		newRouter(t).ServeHTTP(rw, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1alpha1/catalog", nil))

		require.Equal(t, http.StatusOK, rw.Code)
		var body map[string]any
		require.NoError(t, json.Unmarshal(rw.Body.Bytes(), &body))
		item := body["items"].([]any)[0].(map[string]any)
		assert.Equal(t, "lab", item["labPath"])
		assert.Equal(t, "Manifest description", item["description"])
	})

	t.Run("exposes catalog ref", func(t *testing.T) {
		rw := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1alpha1/catalog", nil)
		newRouter(t).ServeHTTP(rw, req)
		require.Equal(t, http.StatusOK, rw.Code)
		var body map[string]any
		require.NoError(t, json.Unmarshal(rw.Body.Bytes(), &body))
		items := body["items"].([]any)
		require.Len(t, items, 1)
		item := items[0].(map[string]any)
		assert.Contains(t, item, "catalogRef", "catalogRef must be present")
	})

	t.Run("uses manifest metadata and omits only classified failures", func(t *testing.T) {
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
	})

	t.Run("unexpected loader failure is not omitted", func(t *testing.T) {
		router := handler.NewRouter(handler.Config{
			CatalogSource: goodCatalogCM,
			BundleLoader: func(context.Context, string, string, string) (*bundle.Result, error) {
				return nil, errors.New("unexpected")
			},
		})
		rw := httptest.NewRecorder()
		router.ServeHTTP(rw, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1alpha1/catalog", nil))
		assert.Equal(t, http.StatusBadGateway, rw.Code)
	})
}

// --- GET /api/v1alpha1/labs/{id}/bundle (stub — no real loader) ---

func TestBundleHandler(t *testing.T) {
	t.Run("unknown lab returns 404", func(t *testing.T) {
		// Non-existent catalogRef (valid hex but not in catalog) returns 404 after auth.
		unknownRef := fmt.Sprintf("%064x", 0)
		rw := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1alpha1/labs/"+unknownRef+"/bundle", nil)
		req.Header.Set("Authorization", "Bearer user1")
		newRouter(t).ServeHTTP(rw, req)

		require.Equal(t, http.StatusNotFound, rw.Code)
		var body map[string]any
		require.NoError(t, json.Unmarshal(rw.Body.Bytes(), &body))
		assert.Equal(t, "lab_not_found", body["type"])
	})

	t.Run("returns sorted navigable page descriptors", func(t *testing.T) {
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
	})

	t.Run("requires auth", func(t *testing.T) {
		ref := goodCatalogRef()
		rw := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1alpha1/labs/"+ref+"/bundle", nil)
		newRouter(t).ServeHTTP(rw, req)
		require.Equal(t, http.StatusUnauthorized, rw.Code)
		var body map[string]any
		require.NoError(t, json.Unmarshal(rw.Body.Bytes(), &body))
		assert.Equal(t, "unauthorized", body["type"])
	})

	t.Run("runtime context uses catalog ref", func(t *testing.T) {
		ref := goodCatalogRef()
		rw := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1alpha1/labs/"+ref+"/bundle", nil)
		req.Header.Set("Authorization", "Bearer user1")
		newRouter(t).ServeHTTP(rw, req)
		require.Equal(t, http.StatusOK, rw.Code)
		var body map[string]any
		require.NoError(t, json.Unmarshal(rw.Body.Bytes(), &body))
		rc := body["runtimeContext"].(map[string]any)
		assert.Equal(t, ref, rc["catalogRef"], "runtimeContext must use catalogRef")
		assert.NotContains(t, rc, "labId", "runtimeContext must not use labId")
	})

	t.Run("attribute merge order", func(t *testing.T) {
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
			Authenticate: func(_ context.Context, _, token string) (string, error) { return token, nil },
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
		require.NoError(t, json.Unmarshal(rw.Body.Bytes(), &body))
		m := body["manifest"].(map[string]any)
		spec := m["spec"].(map[string]any)
		attrs := spec["attributes"].(map[string]any)
		assert.Equal(t, "runtime-val", attrs["user"], "runtime must win over catalog and manifest")
		assert.Equal(t, "yes", attrs["manifest-only"], "manifest-only key must be present")
		assert.Equal(t, "yes", attrs["catalog-only"], "catalog-only key must be present")
		assert.Equal(t, "https://api.example.test", attrs["openshift_api_url"], "runtime values must be present")
	})

	t.Run("runtime attributes are all or safe failure", func(t *testing.T) {
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
					Authenticate:  func(_ context.Context, _, token string) (string, error) { return token, nil },
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
	})
}

// --- POST /api/v1alpha1/labs/{id}/setup ---

var testValuesSchema = []byte(`{
	"properties": {
		"gitea.namespace": {
			"type": "string",
			"title": "gitea namespace",
			"description": "Namespace name for Gitea deployment",
			"x-interactive-labs": {
				"kind": "namespace-name"
			}
		},
		"datasphere.namespace": {
			"type": "string",
			"title": "datasphere namespace",
			"description": "Namespace name for Datasphere deployment",
			"x-interactive-labs": {
				"kind": "namespace-name"
			}
		}
	}
}`)

func prepareSetupClient(t *testing.T) *setup.SpaceRequestClient {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			name := filepath.Base(r.URL.Path)
			fmt.Fprintf(w, `{"metadata":{"name":%q},"status":{"namespaceAccess":[{"name":"ns-%s","secretRef":"s"}],"conditions":[{"type":"Ready","status":"True"}]}}`, name, name) //nolint:gosec
			return
		}
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(server.Close)
	return &setup.SpaceRequestClient{HTTPClient: server.Client(), APIHost: server.URL}
}

// goodCatalogRef returns the expected catalogRef for goodCatalogCM's single entry.
func goodCatalogRef() string {
	return catalog.DeriveRef("https://github.com/redhat-scholars/lab1", "main", "lab/", nil)
}

func TestSetupHandler(t *testing.T) {
	t.Run("rejects missing authorization", func(t *testing.T) {
		rw := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1alpha1/labs/"+goodCatalogRef()+"/setup", nil)
		router, _ := newSetupRouter(t)
		router.ServeHTTP(rw, req)

		require.Equal(t, http.StatusUnauthorized, rw.Code)
		var body map[string]string
		require.NoError(t, json.Unmarshal(rw.Body.Bytes(), &body))
		require.Equal(t, "unauthorized", body["type"])
	})

	t.Run("no helm chart returns 422", func(t *testing.T) {
		ref := goodCatalogRef()
		rw := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1alpha1/labs/"+ref+"/setup", nil)
		req.Header.Set("Authorization", "Bearer user1")
		newRouter(t).ServeHTTP(rw, req)

		require.Equal(t, http.StatusUnprocessableEntity, rw.Code)
		var body map[string]any
		require.NoError(t, json.Unmarshal(rw.Body.Bytes(), &body))
		assert.Equal(t, "chart_not_found", body["type"])
	})

	t.Run("valid schema returns 202", func(t *testing.T) {
		ref := goodCatalogRef()
		rw := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1alpha1/labs/"+ref+"/setup", nil)
		req.Header.Set("Authorization", "Bearer "+makeTestJWT("user1"))
		router, installCh := newSetupRouter(t)
		router.ServeHTTP(rw, req)
		<-installCh

		require.Equal(t, http.StatusAccepted, rw.Code)
		var body map[string]any
		err := json.Unmarshal(rw.Body.Bytes(), &body)
		require.NoError(t, err)
		assert.Equal(t, "SetupStatus", body["kind"])
		require.IsType(t, map[string]any{}, body["spaceRequests"])
		sr := body["spaceRequests"].(map[string]any)
		assert.Equal(t, "provisioning", sr["status"])
		require.IsType(t, map[string]any{}, body["helm"])
		helm := body["helm"].(map[string]any)
		assert.Equal(t, "pending", helm["status"])
		assert.NotEmpty(t, helm["releaseName"])
	})

	t.Run("no authorization echoed", func(t *testing.T) {
		rw := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1alpha1/labs/"+goodCatalogRef()+"/setup", nil)
		req.Header.Set("Authorization", "Bearer eyJtest.token.value")
		router, installCh := newSetupRouter(t)
		router.ServeHTTP(rw, req)
		<-installCh

		assertNoTokenInBody(t, rw.Body.String())
	})

	t.Run("token not logged", func(t *testing.T) {
		rw := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1alpha1/labs/"+goodCatalogRef()+"/setup", nil)
		req.Header.Set("Authorization", "Bearer eyJtest.token.value")
		router, installCh := newSetupRouter(t)
		router.ServeHTTP(rw, req)
		<-installCh
		assertNoTokenInBody(t, rw.Body.String())
	})

	t.Run("already deployed returns 200", func(t *testing.T) {
		client := prepareSetupClient(t)
		helm := &stubHelmManager{status: "deployed"}
		router := handler.NewRouter(handler.Config{
			CatalogSource: goodCatalogCM,
			Authenticate:  func(_ context.Context, _, token string) (string, error) { return token, nil },
			BundleLoader: func(_ context.Context, _, _, _ string) (*bundle.Result, error) {
				return &bundle.Result{
					Manifest:           &manifest.LabManifest{Metadata: manifest.Metadata{Name: "lab", DisplayName: "Lab"}},
					Pages:              []string{"= Intro"},
					HelmChartAvailable: true,
					ValuesSchema:       testValuesSchema,
				}, nil
			},
			SetupClient: client,
			HelmManager: helm,
		})

		ref := goodCatalogRef()
		rw := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1alpha1/labs/"+ref+"/setup", nil)
		req.Header.Set("Authorization", "Bearer "+makeTestJWT("user1"))
		router.ServeHTTP(rw, req)

		require.Equal(t, http.StatusOK, rw.Code)
		var body map[string]any
		require.NoError(t, json.Unmarshal(rw.Body.Bytes(), &body))
		assert.Equal(t, "SetupStatus", body["kind"])
		helmSection := body["helm"].(map[string]any)
		assert.Equal(t, "deployed", helmSection["status"])
	})

	t.Run("concurrent requests start only one worker", func(t *testing.T) {
		var installCount atomic.Int32
		blockInstall := make(chan struct{})
		helm := &stubHelmManager{
			statusErr: errors.New("not found"),
			installErr: func() error {
				// not actually called here — we intercept via wrapper below
				return nil
			}(),
		}
		originalInstall := helm.Install
		_ = originalInstall

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if r.Method == http.MethodGet {
				fmt.Fprintf(w, `{"metadata":{"name":"test"},"status":{"namespaceAccess":[{"name":"ns-abc","secretRef":"s"}],"conditions":[{"type":"Ready","status":"True"}]}}`)
				return
			}
			w.WriteHeader(http.StatusCreated)
		}))
		defer server.Close()

		countingHelm := &countingHelmManager{
			inner:        helm,
			installCount: &installCount,
			blockInstall: blockInstall,
		}

		router := handler.NewRouter(handler.Config{
			CatalogSource: goodCatalogCM,
			Authenticate:  func(_ context.Context, _, token string) (string, error) { return token, nil },
			BundleLoader: func(_ context.Context, _, _, _ string) (*bundle.Result, error) {
				return &bundle.Result{
					Manifest:           &manifest.LabManifest{Metadata: manifest.Metadata{Name: "lab", DisplayName: "Lab"}},
					Pages:              []string{"intro.adoc"},
					HelmChartAvailable: true,
					HelmChartDir:       t.TempDir(),
					ValuesSchema:       testValuesSchema,
				}, nil
			},
			SetupClient: &setup.SpaceRequestClient{HTTPClient: server.Client(), APIHost: server.URL},
			HelmManager: countingHelm,
			HelmTimeout: 5 * time.Second,
		})

		ref := goodCatalogRef()
		const concurrency = 5
		var wg sync.WaitGroup
		wg.Add(concurrency)
		for i := 0; i < concurrency; i++ {
			go func() {
				defer wg.Done()
				rw := httptest.NewRecorder()
				req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1alpha1/labs/"+ref+"/setup", nil)
				req.Header.Set("Authorization", "Bearer "+makeTestJWT("concurrent-test-user"))
				router.ServeHTTP(rw, req)
				assert.Equal(t, http.StatusAccepted, rw.Code)
			}()
		}
		wg.Wait()

		close(blockInstall)

		assert.Eventually(t, func() bool {
			return installCount.Load() >= 1
		}, 5*time.Second, 50*time.Millisecond)

		assert.Equal(t, int32(1), installCount.Load(), "expected exactly one Helm install across concurrent requests")
	})
}

// --- GET /api/v1alpha1/labs/{id}/setup/status ---

func TestSetupStatusHandler(t *testing.T) {
	t.Run("all ready", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"metadata":{"name":"test"},"status":{"namespaceAccess":[{"name":"ns-abc","secretRef":"s"}],"conditions":[{"type":"Ready","status":"True"}]}}`)
		}))
		defer server.Close()

		router := handler.NewRouter(handler.Config{
			CatalogSource: goodCatalogCM,
			Authenticate:  func(_ context.Context, _, token string) (string, error) { return token, nil },
			BundleLoader: func(_ context.Context, _, _, _ string) (*bundle.Result, error) {
				return &bundle.Result{
					Manifest:           &manifest.LabManifest{Metadata: manifest.Metadata{Name: "lab", DisplayName: "Lab"}},
					Pages:              []string{"intro.adoc"},
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
		require.NoError(t, json.Unmarshal(rw.Body.Bytes(), &body))
		assert.Equal(t, "SetupStatus", body["kind"])
		sr := body["spaceRequests"].(map[string]any)
		assert.Equal(t, "ready", sr["status"])
		require.IsType(t, map[string]any{}, body["helm"])
		helm := body["helm"].(map[string]any)
		assert.Equal(t, "pending", helm["status"])
		assert.NotEmpty(t, helm["releaseName"])
	})

	t.Run("provisioning", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"metadata":{"name":"test"},"status":{"conditions":[{"type":"Ready","status":"False","reason":"Provisioning"}]}}`)
		}))
		defer server.Close()

		router := handler.NewRouter(handler.Config{
			CatalogSource: goodCatalogCM,
			Authenticate:  func(_ context.Context, _, token string) (string, error) { return token, nil },
			BundleLoader: func(_ context.Context, _, _, _ string) (*bundle.Result, error) {
				return &bundle.Result{
					Manifest:           &manifest.LabManifest{Metadata: manifest.Metadata{Name: "lab", DisplayName: "Lab"}},
					Pages:              []string{"intro.adoc"},
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
		require.NoError(t, json.Unmarshal(rw.Body.Bytes(), &body))
		sr := body["spaceRequests"].(map[string]any)
		assert.Equal(t, "provisioning", sr["status"])
		helm := body["helm"].(map[string]any)
		assert.Equal(t, "pending", helm["status"])
		assert.NotEmpty(t, helm["releaseName"])
	})

	t.Run("ready without namespace access stays provisioning", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"metadata":{"name":"test"},"status":{"conditions":[{"type":"Ready","status":"True"}]}}`)
		}))
		defer server.Close()

		router := handler.NewRouter(handler.Config{
			CatalogSource: goodCatalogCM,
			Authenticate:  func(_ context.Context, _, token string) (string, error) { return token, nil },
			BundleLoader: func(_ context.Context, _, _, _ string) (*bundle.Result, error) {
				return &bundle.Result{
					Manifest:           &manifest.LabManifest{Metadata: manifest.Metadata{Name: "lab", DisplayName: "Lab"}},
					Pages:              []string{"intro.adoc"},
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
		require.NoError(t, json.Unmarshal(rw.Body.Bytes(), &body))
		require.IsType(t, map[string]any{}, body["spaceRequests"])
		spaceRequests := body["spaceRequests"].(map[string]any)
		assert.Equal(t, "provisioning", spaceRequests["status"])
		require.IsType(t, map[string]any{}, body["helm"])
		helm := body["helm"].(map[string]any)
		assert.Equal(t, "pending", helm["status"])
		assert.NotEmpty(t, helm["releaseName"])
	})

	t.Run("terminal failure", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"metadata":{"name":"test"},"status":{"conditions":[{"type":"Ready","status":"False","reason":"UnableToCreateSpace"}]}}`)
		}))
		defer server.Close()

		router := handler.NewRouter(handler.Config{
			CatalogSource: goodCatalogCM,
			Authenticate:  func(_ context.Context, _, token string) (string, error) { return token, nil },
			BundleLoader: func(_ context.Context, _, _, _ string) (*bundle.Result, error) {
				return &bundle.Result{
					Manifest:           &manifest.LabManifest{Metadata: manifest.Metadata{Name: "lab", DisplayName: "Lab"}},
					Pages:              []string{"intro.adoc"},
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
		require.NoError(t, json.Unmarshal(rw.Body.Bytes(), &body))
		require.IsType(t, map[string]any{}, body["spaceRequests"])
		spaceRequests := body["spaceRequests"].(map[string]any)
		assert.Equal(t, "failed", spaceRequests["status"])
		require.IsType(t, map[string]any{}, body["helm"])
		helm := body["helm"].(map[string]any)
		assert.Equal(t, "pending", helm["status"])
		assert.NotEmpty(t, helm["releaseName"])
	})

	t.Run("requires auth", func(t *testing.T) {
		ref := goodCatalogRef()
		rw := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1alpha1/labs/"+ref+"/setup/status", nil)
		router, _ := newSetupRouter(t)
		router.ServeHTTP(rw, req)

		require.Equal(t, http.StatusUnauthorized, rw.Code)
	})

	t.Run("with helm deployed", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"metadata":{"name":"test"},"status":{"namespaceAccess":[{"name":"ns-abc","secretRef":"s"}],"conditions":[{"type":"Ready","status":"True"}]}}`)
		}))
		defer server.Close()

		helm := &stubHelmManager{status: "deployed"}
		router := handler.NewRouter(handler.Config{
			CatalogSource: goodCatalogCM,
			Authenticate:  func(_ context.Context, _, token string) (string, error) { return token, nil },
			BundleLoader: func(_ context.Context, _, _, _ string) (*bundle.Result, error) {
				return &bundle.Result{
					Manifest:           &manifest.LabManifest{Metadata: manifest.Metadata{Name: "lab", DisplayName: "Lab"}},
					Pages:              []string{"= Intro"},
					HelmChartAvailable: true,
					ValuesSchema:       testValuesSchema,
				}, nil
			},
			SetupClient: &setup.SpaceRequestClient{HTTPClient: server.Client(), APIHost: server.URL},
			HelmManager: helm,
		})

		ref := goodCatalogRef()
		rw := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1alpha1/labs/"+ref+"/setup/status", nil)
		req.Header.Set("Authorization", "Bearer user1")
		router.ServeHTTP(rw, req)

		require.Equal(t, http.StatusOK, rw.Code)
		var body map[string]any
		require.NoError(t, json.Unmarshal(rw.Body.Bytes(), &body))
		sr := body["spaceRequests"].(map[string]any)
		assert.Equal(t, "ready", sr["status"])
		helmSection := body["helm"].(map[string]any)
		assert.Equal(t, "deployed", helmSection["status"])
	})

	t.Run("with helm installing", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"metadata":{"name":"test"},"status":{"namespaceAccess":[{"name":"ns-abc","secretRef":"s"}],"conditions":[{"type":"Ready","status":"True"}]}}`)
		}))
		defer server.Close()

		helm := &stubHelmManager{status: "pending-install"}
		router := handler.NewRouter(handler.Config{
			CatalogSource: goodCatalogCM,
			Authenticate:  func(_ context.Context, _, token string) (string, error) { return token, nil },
			BundleLoader: func(_ context.Context, _, _, _ string) (*bundle.Result, error) {
				return &bundle.Result{
					Manifest:           &manifest.LabManifest{Metadata: manifest.Metadata{Name: "lab", DisplayName: "Lab"}},
					Pages:              []string{"= Intro"},
					HelmChartAvailable: true,
					ValuesSchema:       testValuesSchema,
				}, nil
			},
			SetupClient: &setup.SpaceRequestClient{HTTPClient: server.Client(), APIHost: server.URL},
			HelmManager: helm,
		})

		ref := goodCatalogRef()
		rw := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1alpha1/labs/"+ref+"/setup/status", nil)
		req.Header.Set("Authorization", "Bearer user1")
		router.ServeHTTP(rw, req)

		require.Equal(t, http.StatusOK, rw.Code)
		var body map[string]any
		require.NoError(t, json.Unmarshal(rw.Body.Bytes(), &body))
		helmSection := body["helm"].(map[string]any)
		assert.Equal(t, "installing", helmSection["status"])
	})

	t.Run("with generated values", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"metadata":{"name":"test"},"status":{"namespaceAccess":[{"name":"ns-abc","secretRef":"s"}],"conditions":[{"type":"Ready","status":"True"}]}}`)
		}))
		defer server.Close()

		helm := &stubHelmManager{
			status:        "deployed",
			releaseValues: map[string]string{"passwords.postgres": "secret123"},
		}
		router := handler.NewRouter(handler.Config{
			CatalogSource: goodCatalogCM,
			Authenticate:  func(_ context.Context, _, token string) (string, error) { return token, nil },
			BundleLoader: func(_ context.Context, _, _, _ string) (*bundle.Result, error) {
				return &bundle.Result{
					Manifest:           &manifest.LabManifest{Metadata: manifest.Metadata{Name: "lab", DisplayName: "Lab"}},
					Pages:              []string{"= Intro"},
					HelmChartAvailable: true,
					ValuesSchema:       testValuesSchemaWithPasswords,
				}, nil
			},
			SetupClient: &setup.SpaceRequestClient{HTTPClient: server.Client(), APIHost: server.URL},
			HelmManager: helm,
		})

		ref := goodCatalogRef()
		rw := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1alpha1/labs/"+ref+"/setup/status", nil)
		req.Header.Set("Authorization", "Bearer user1")
		router.ServeHTTP(rw, req)

		require.Equal(t, http.StatusOK, rw.Code)
		var body map[string]any
		require.NoError(t, json.Unmarshal(rw.Body.Bytes(), &body))
		helmSection := body["helm"].(map[string]any)
		assert.Equal(t, "deployed", helmSection["status"])
		genVals := helmSection["generatedValues"].(map[string]any)
		assert.Equal(t, "secret123", genVals["passwords.postgres"])
	})

	t.Run("no generated values when no passwords", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"metadata":{"name":"test"},"status":{"namespaceAccess":[{"name":"ns-abc","secretRef":"s"}],"conditions":[{"type":"Ready","status":"True"}]}}`)
		}))
		defer server.Close()

		helm := &stubHelmManager{status: "deployed"}
		router := handler.NewRouter(handler.Config{
			CatalogSource: goodCatalogCM,
			Authenticate:  func(_ context.Context, _, token string) (string, error) { return token, nil },
			BundleLoader: func(_ context.Context, _, _, _ string) (*bundle.Result, error) {
				return &bundle.Result{
					Manifest:           &manifest.LabManifest{Metadata: manifest.Metadata{Name: "lab", DisplayName: "Lab"}},
					Pages:              []string{"= Intro"},
					HelmChartAvailable: true,
					ValuesSchema:       testValuesSchema, // no password bindings
				}, nil
			},
			SetupClient: &setup.SpaceRequestClient{HTTPClient: server.Client(), APIHost: server.URL},
			HelmManager: helm,
		})

		ref := goodCatalogRef()
		rw := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1alpha1/labs/"+ref+"/setup/status", nil)
		req.Header.Set("Authorization", "Bearer user1")
		router.ServeHTTP(rw, req)

		require.Equal(t, http.StatusOK, rw.Code)
		var body map[string]any
		require.NoError(t, json.Unmarshal(rw.Body.Bytes(), &body))
		helmSection := body["helm"].(map[string]any)
		assert.Equal(t, "deployed", helmSection["status"])
		assert.Nil(t, helmSection["generatedValues"])
	})
}

// --- GET /api/v1alpha1/labs/{catalogRef}/resources/{repositoryRelativePath...} ---

func TestResourceHandler(t *testing.T) {
	t.Run("serves approved resources with safe headers", func(t *testing.T) {
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
	})

	t.Run("sniffs content type after loading", func(t *testing.T) {
		ref := goodCatalogRef()
		router := handler.NewRouter(handler.Config{
			CatalogSource: goodCatalogCM,
			Authenticate:  func(_ context.Context, _ string, token string) (string, error) { return token, nil },
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
	})

	t.Run("returns not modified before loading resource", func(t *testing.T) {
		loads := 0
		ref := goodCatalogRef()
		router := handler.NewRouter(handler.Config{
			CatalogSource: goodCatalogCM,
			Authenticate:  func(_ context.Context, _ string, token string) (string, error) { return token, nil },
			SnapshotCommitHash: func(context.Context, string, string, string) (string, error) {
				return "abc123", nil
			},
			ResourceLoader: func(context.Context, string, string, string, string) (*bundle.Resource, error) {
				loads++
				return nil, errors.New("resource loader must not run")
			},
		})
		request := httptest.NewRequestWithContext(context.TODO(), http.MethodGet, "/api/v1alpha1/labs/"+ref+"/resources/lab/content/intro.adoc", nil)
		request.Header.Set("Authorization", "Bearer user1")
		request.Header.Set("If-None-Match", `"abc123"`)
		recorder := httptest.NewRecorder()

		router.ServeHTTP(recorder, request)

		assert.Equal(t, http.StatusNotModified, recorder.Code)
		assert.Zero(t, loads)
		assert.Equal(t, `"abc123"`, recorder.Header().Get("ETag"))
		assert.Equal(t, "private, no-cache", recorder.Header().Get("Cache-Control"))
	})

	t.Run("honors quoted ETag for conditional GET", func(t *testing.T) {
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
	})

	t.Run("rejects invalid resources", func(t *testing.T) {
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
	})

	t.Run("derives source only from catalog ref", func(t *testing.T) {
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
			Authenticate: func(_ context.Context, _, token string) (string, error) { return token, nil },
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
	})
}

// --- Root-URL resolution in setup worker ---

var testValuesSchemaWithRootURL = []byte(`{
	"properties": {
		"namespaces": {
			"type": "object",
			"properties": {
				"gitea": {
					"type": "string",
					"x-interactive-labs": { "kind": "namespace-name" }
				}
			}
		},
		"gitea": {
			"type": "object",
			"properties": {
				"rootUrl": {
					"type": "string",
					"x-interactive-labs": { "kind": "root-url", "routeName": "gitea", "namespaceRef": "gitea" }
				}
			}
		}
	}
}`)

var testValuesSchemaWithRootURLBadRef = []byte(`{
	"properties": {
		"namespaces": {
			"type": "object",
			"properties": {
				"gitea": {
					"type": "string",
					"x-interactive-labs": { "kind": "namespace-name" }
				}
			}
		},
		"gitea": {
			"type": "object",
			"properties": {
				"rootUrl": {
					"type": "string",
					"x-interactive-labs": { "kind": "root-url", "routeName": "gitea", "namespaceRef": "nonexistent" }
				}
			}
		}
	}
}`)

func readySpaceRequestServer(t *testing.T, nsName string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"metadata":{"name":"gitea"},"status":{"namespaceAccess":[{"name":%q,"secretRef":"s"}],"conditions":[{"type":"Ready","status":"True"}]}}`, nsName)
	}))
	t.Cleanup(server.Close)
	return server
}

func TestSetupWorker(t *testing.T) {
	t.Run("root URL resolves namespace", func(t *testing.T) {
		token := makeTestJWT("rooturl-resolve")
		srServer := readySpaceRequestServer(t, "ns-gitea-xyz")

		helm := &stubHelmManager{
			statusErr: errors.New("not found"),
			installCh: make(chan struct{}),
		}
		router := handler.NewRouter(handler.Config{
			CatalogSource: goodCatalogCM,
			Authenticate:  func(_ context.Context, _, tok string) (string, error) { return tok, nil },
			BundleLoader: func(_ context.Context, _, _, _ string) (*bundle.Result, error) {
				return &bundle.Result{
					Manifest:           &manifest.LabManifest{Metadata: manifest.Metadata{Name: "lab", DisplayName: "Lab"}},
					Pages:              []string{"= Intro"},
					HelmChartAvailable: true,
					ValuesSchema:       testValuesSchemaWithRootURL,
				}, nil
			},
			SetupClient:          &setup.SpaceRequestClient{HTTPClient: srServer.Client(), APIHost: srServer.URL},
			HelmManager:          helm,
			ClusterIngressDomain: "apps.example.com",
		})

		ref := goodCatalogRef()
		rw := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1alpha1/labs/"+ref+"/setup", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		router.ServeHTTP(rw, req)
		require.Equal(t, http.StatusAccepted, rw.Code)

		select {
		case <-helm.installCh:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for Helm install")
		}

		require.NotNil(t, helm.installOpts)
		assert.Equal(t, "gitea-ns-gitea-xyz.apps.example.com", helm.installOpts.Values["gitea.rootUrl"])
		assert.Equal(t, "ns-gitea-xyz", helm.installOpts.Values["namespaces.gitea"])
	})

	t.Run("root URL missing ingress domain", func(t *testing.T) {
		token := makeTestJWT("rooturl-no-domain")
		srServer := readySpaceRequestServer(t, "ns-gitea-xyz")

		helm := &stubHelmManager{statusErr: errors.New("not found")}
		router := handler.NewRouter(handler.Config{
			CatalogSource: goodCatalogCM,
			Authenticate:  func(_ context.Context, _, tok string) (string, error) { return tok, nil },
			BundleLoader: func(_ context.Context, _, _, _ string) (*bundle.Result, error) {
				return &bundle.Result{
					Manifest:           &manifest.LabManifest{Metadata: manifest.Metadata{Name: "lab", DisplayName: "Lab"}},
					Pages:              []string{"= Intro"},
					HelmChartAvailable: true,
					ValuesSchema:       testValuesSchemaWithRootURL,
				}, nil
			},
			SetupClient:          &setup.SpaceRequestClient{HTTPClient: srServer.Client(), APIHost: srServer.URL},
			HelmManager:          helm,
			ClusterIngressDomain: "",
		})

		ref := goodCatalogRef()
		rw := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1alpha1/labs/"+ref+"/setup", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		router.ServeHTTP(rw, req)
		require.Equal(t, http.StatusAccepted, rw.Code)

		var helmMessage string
		require.Eventually(t, func() bool {
			rw := httptest.NewRecorder()
			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1alpha1/labs/"+ref+"/setup/status", nil)
			req.Header.Set("Authorization", "Bearer "+token)
			router.ServeHTTP(rw, req)
			var body map[string]any
			if err := json.Unmarshal(rw.Body.Bytes(), &body); err != nil {
				return false
			}
			helmSection := body["helm"].(map[string]any)
			if helmSection["status"] == "failed" {
				helmMessage, _ = helmSection["message"].(string)
				return true
			}
			return false
		}, 5*time.Second, 50*time.Millisecond)

		assert.Contains(t, helmMessage, "cluster ingress domain not configured")
		assert.False(t, helm.installed)
	})

	t.Run("root URL unknown namespace ref", func(t *testing.T) {
		token := makeTestJWT("rooturl-bad-ref")
		srServer := readySpaceRequestServer(t, "ns-gitea-xyz")

		helm := &stubHelmManager{statusErr: errors.New("not found")}
		router := handler.NewRouter(handler.Config{
			CatalogSource: goodCatalogCM,
			Authenticate:  func(_ context.Context, _, token string) (string, error) { return token, nil },
			BundleLoader: func(_ context.Context, _, _, _ string) (*bundle.Result, error) {
				return &bundle.Result{
					Manifest:           &manifest.LabManifest{Metadata: manifest.Metadata{Name: "lab", DisplayName: "Lab"}},
					Pages:              []string{"= Intro"},
					HelmChartAvailable: true,
					ValuesSchema:       testValuesSchemaWithRootURLBadRef,
				}, nil
			},
			SetupClient:          &setup.SpaceRequestClient{HTTPClient: srServer.Client(), APIHost: srServer.URL},
			HelmManager:          helm,
			ClusterIngressDomain: "apps.example.com",
		})

		ref := goodCatalogRef()
		rw := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1alpha1/labs/"+ref+"/setup", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		router.ServeHTTP(rw, req)
		require.Equal(t, http.StatusAccepted, rw.Code)

		var helmMessage string
		require.Eventually(t, func() bool {
			rw := httptest.NewRecorder()
			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1alpha1/labs/"+ref+"/setup/status", nil)
			req.Header.Set("Authorization", "Bearer "+token)
			router.ServeHTTP(rw, req)
			var body map[string]any
			if err := json.Unmarshal(rw.Body.Bytes(), &body); err != nil {
				return false
			}
			helmSection := body["helm"].(map[string]any)
			if helmSection["status"] == "failed" {
				helmMessage, _ = helmSection["message"].(string)
				return true
			}
			return false
		}, 5*time.Second, 50*time.Millisecond)

		assert.Contains(t, helmMessage, "root-url references unknown namespace binding")
		assert.Contains(t, helmMessage, "nonexistent")
		assert.False(t, helm.installed)
	})
}

// --- Helm integration stubs ---

var testValuesSchemaWithPasswords = []byte(`{
	"properties": {
		"namespaces": {
			"type": "object",
			"properties": {
				"gitea": {
					"type": "string",
					"x-interactive-labs": { "kind": "namespace-name" }
				}
			}
		},
		"passwords": {
			"type": "object",
			"properties": {
				"postgres": {
					"type": "string",
					"x-interactive-labs": { "kind": "generated-password" }
				}
			}
		}
	}
}`)

type stubHelmManager struct {
	status        string
	statusErr     error
	installErr    error
	installed     bool
	installOpts   *setup.HelmInstallOpts
	releaseValues map[string]string
	releaseValErr error
	installCh     chan struct{}
}

func (s *stubHelmManager) ReleaseStatus(_ context.Context, _, _, _ string) (string, error) {
	return s.status, s.statusErr
}

func (s *stubHelmManager) Install(_ context.Context, opts setup.HelmInstallOpts) error {
	s.installed = true
	s.installOpts = &opts
	if s.installCh != nil {
		close(s.installCh)
	}
	return s.installErr
}

func (s *stubHelmManager) ReleaseValues(_ context.Context, _, _, _ string, _ []string) (map[string]string, error) {
	return s.releaseValues, s.releaseValErr
}

func (s *stubHelmManager) ListPods(_ context.Context, _, _ string) ([]setup.PodSummary, error) {
	return nil, nil
}

type countingHelmManager struct {
	inner        *stubHelmManager
	installCount *atomic.Int32
	blockInstall chan struct{}
}

func (c *countingHelmManager) ReleaseStatus(ctx context.Context, release, ns, token string) (string, error) {
	return c.inner.ReleaseStatus(ctx, release, ns, token)
}

func (c *countingHelmManager) Install(_ context.Context, _ setup.HelmInstallOpts) error {
	<-c.blockInstall
	c.installCount.Add(1)
	return c.inner.installErr
}

func (c *countingHelmManager) ReleaseValues(ctx context.Context, release, ns, token string, paths []string) (map[string]string, error) {
	return c.inner.ReleaseValues(ctx, release, ns, token, paths)
}

func (c *countingHelmManager) ListPods(ctx context.Context, token, ns string) ([]setup.PodSummary, error) {
	return c.inner.ListPods(ctx, token, ns)
}

// --- helpers ---

func resourceRouter(t *testing.T, catalogSource func(context.Context) (*corev1.ConfigMap, error), root, etag string) http.Handler {
	t.Helper()
	return handler.NewRouter(handler.Config{
		CatalogSource: catalogSource,
		Authenticate:  func(_ context.Context, _, token string) (string, error) { return token, nil },
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
