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
	"k8s.io/client-go/kubernetes/fake"

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
				Manifest: &manifest.LabManifest{Metadata: manifest.Metadata{Title: "Manifest Lab", Description: "Manifest description"}},
				Modules:  []bundle.Module{{ID: "010-intro", Pages: []string{"intro.adoc"}}},
			}, nil
		},
		RuntimeAttributes: func(_ *http.Request, subject string) (map[string]string, error) {
			return map[string]string{
				"user": subject, "openshift-api-url": "https://api.example.test", "openshift-console-url": "https://console.example.test", "openshift-cluster-ingress-domain": "apps.example.test",
			}, nil
		},
	})
}

func newTestManager(t *testing.T, client *setup.SpaceRequestClient, helm setup.HelmManager, configure ...func(*setup.ManagerConfig)) *setup.Manager {
	t.Helper()
	config := setup.ManagerConfig{SpaceRequests: client, Helm: helm, Coordinator: setup.NewOperationCoordinator(fake.NewSimpleClientset(), "plugin-ns")}
	for _, update := range configure {
		update(&config)
	}
	return setup.NewManager(config)
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
				Manifest:           &manifest.LabManifest{Metadata: manifest.Metadata{Title: "Manifest Lab"}},
				Modules:            []bundle.Module{{ID: "010-intro", Pages: []string{"intro.adoc"}}},
				HelmChartAvailable: true,
				HelmChartDir:       t.TempDir(),
				ValuesSchema:       testValuesSchema,
			}, nil
		},
		SetupManager: newTestManager(t, client, &stubHelmManager{installCh: installCh}, func(c *setup.ManagerConfig) { c.HelmTimeout = 5 * time.Second }),
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
				return &bundle.Result{Manifest: &manifest.LabManifest{Metadata: manifest.Metadata{Title: name}}, Modules: []bundle.Module{{ID: "010-intro", Pages: []string{"intro.adoc"}}}}, nil
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
		assert.Equal(t, "Lab Z", items[0].(map[string]any)["title"])
		assert.Equal(t, "Lab A", items[1].(map[string]any)["title"])
		assert.NotContains(t, items[0].(map[string]any), "displayName")
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
				return &bundle.Result{Manifest: &manifest.LabManifest{Metadata: manifest.Metadata{Title: "Manifest name", Description: "Manifest description"}}, Modules: []bundle.Module{{ID: "010-intro", Pages: []string{"intro.adoc"}}}}, nil
			},
			Logger: slog.New(slog.NewJSONHandler(&logs, nil)),
			CatalogLabOmitted: func(_ context.Context, entry catalog.Entry, reason string) {
				warnings = append(warnings, entry.CatalogRef+":"+reason)
			},
		})

		rw := httptest.NewRecorder()
		router.ServeHTTP(rw, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1alpha1/catalog", nil))
		require.Equal(t, http.StatusOK, rw.Code)
		var body struct {
			Items []struct {
				CatalogRef  string `json:"catalogRef"`
				Title       string `json:"title"`
				Description string `json:"description"`
			} `json:"items"`
		}
		require.NoError(t, json.Unmarshal(rw.Body.Bytes(), &body))
		require.Equal(t, []struct {
			CatalogRef  string `json:"catalogRef"`
			Title       string `json:"title"`
			Description string `json:"description"`
		}{{validRef, "Manifest name", "Manifest description"}}, body.Items)
		assert.Equal(t, []string{catalog.DeriveRef("https://github.com/redhat/missing", "main", "lab", nil) + ":ManifestMissing"}, warnings)
		assert.Contains(t, logs.String(), "catalog lab omitted")
		assert.NotContains(t, logs.String(), "private details")
		assert.Contains(t, logs.String(), "https://github.com/redhat/missing")
		assert.Contains(t, logs.String(), "branch")
		assert.Contains(t, logs.String(), "labPath")
	})

	t.Run("passes validated entry to omission observer", func(t *testing.T) {
		var observed catalog.Entry
		router := handler.NewRouter(handler.Config{
			CatalogSource: stubCatalogCM(map[string]string{"catalog.yaml": `- repository: https://github.com/redhat/missing
  branch: main
  labPath: lab`}),
			BundleLoader: func(context.Context, string, string, string) (*bundle.Result, error) {
				return nil, bundle.ErrManifestMissing
			},
			CatalogLabOmitted: func(_ context.Context, entry catalog.Entry, reason string) {
				observed = entry
				assert.Equal(t, "ManifestMissing", reason)
			},
		})

		router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1alpha1/catalog", nil))
		assert.Equal(t, "https://github.com/redhat/missing", observed.Repository)
		assert.Equal(t, "main", observed.Branch)
		assert.Equal(t, "lab", observed.LabPath)
		assert.NotEmpty(t, observed.CatalogRef)
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

// STORY-14: module metadata belongs to the bundle, but setup remains lab-wide.
func TestModuleWireContract(t *testing.T) {
	root := t.TempDir()
	for name, contents := range map[string]string{
		"lab/lab.yaml": "apiVersion: v1alpha1\nkind: Lab\nmetadata:\n  title: Manifest title\n  description: Catalog only\nspec:\n  attributes:\n    shared: manifest\n    user: manifest\n    manifest-only: yes\n",
		"lab/modules/010-intro/content/010-start.adoc":        "= Start\n",
		"lab/modules/010-intro/content/_partials/shared.adoc": "Shared\n",
		"lab/modules/010-intro/module.yaml":                   "checkpoints:\n  - id: checkpoint\n    type: manual\n    pagePath: 010-start.adoc\n    label: Done\n",
		"lab/modules/020-apply/content/pages/020-apply.adoc":  "= Apply\n",
		"invalid/lab.yaml": "apiVersion: v1alpha1\nkind: Lab\nmetadata:\n  title: Invalid lab\n",
		"invalid/modules/010-broken/content/_only.adoc": "= Include only\n",
		"setup/helm/Chart.yaml":                         "apiVersion: v2\nname: test\nversion: 1.0.0\n",
		"setup/helm/values.schema.json":                 string(testValuesSchema),
	} {
		file := filepath.Join(root, filepath.FromSlash(name))
		require.NoError(t, os.MkdirAll(filepath.Dir(file), 0o755))
		require.NoError(t, os.WriteFile(file, []byte(contents), 0o600))
	}
	const repository = "https://github.com/redhat/modules"
	overrides := map[string]string{"shared": "catalog", "user": "catalog", "catalog-only": "yes"}
	ref := catalog.DeriveRef(repository, "main", "lab", overrides)
	var omitted []string
	router := handler.NewRouter(handler.Config{
		CatalogSource: stubCatalogCM(map[string]string{"catalog.yaml": `- repository: https://github.com/redhat/modules
  branch: main
  labPath: lab
  attributeOverrides:
    shared: catalog
    user: catalog
    catalog-only: yes
- repository: https://github.com/redhat/invalid
  branch: main
  labPath: invalid`}),
		BundleLoader: func(_ context.Context, _, _, labPath string) (*bundle.Result, error) {
			return bundle.LoadFromSnapshot(root, labPath)
		},
		CatalogLabOmitted: func(_ context.Context, entry catalog.Entry, reason string) {
			omitted = append(omitted, entry.CatalogRef+":"+reason)
		},
		Authenticate: func(_ context.Context, _, token string) (string, error) { return token, nil },
		RuntimeAttributes: func(_ *http.Request, subject string) (map[string]string, error) {
			return map[string]string{"user": subject, "openshift-api-url": "https://api.example.test", "openshift-console-url": "https://console.example.test", "openshift-cluster-ingress-domain": "apps.example.test"}, nil
		},
		ResourceLoader: func(_ context.Context, _, _, resourcePath string) (*bundle.Resource, error) {
			res, err := bundle.ReadResource(root, resourcePath)
			if err != nil {
				return nil, err
			}
			res.Revision = "abc123"
			return res, nil
		},
		SetupManager: newTestManager(t, prepareSetupClient(t, ref), &stubHelmManager{status: "deployed", catalogRef: ref}),
	})

	t.Run("catalog exposes manifest title and safely omits invalid lab", func(t *testing.T) {
		rw := httptest.NewRecorder()
		router.ServeHTTP(rw, httptest.NewRequestWithContext(context.TODO(), http.MethodGet, "/api/v1alpha1/catalog", nil))
		require.Equal(t, http.StatusOK, rw.Code)
		var body struct {
			Items []map[string]any `json:"items"`
		}
		require.NoError(t, json.Unmarshal(rw.Body.Bytes(), &body))
		require.Len(t, body.Items, 1)
		assert.Equal(t, ref, body.Items[0]["catalogRef"])
		assert.Equal(t, "Manifest title", body.Items[0]["title"])
		assert.Equal(t, "Catalog only", body.Items[0]["description"])
		assert.Equal(t, "lab", body.Items[0]["labPath"])
		assert.NotContains(t, body.Items[0], "displayName")
		assert.Equal(t, []string{catalog.DeriveRef("https://github.com/redhat/invalid", "main", "invalid", nil) + ":LabInvalid"}, omitted)
	})

	t.Run("bundle returns only module-qualified paths and effective attributes", func(t *testing.T) {
		rw := resourceRequest(t, router, ref, "lab/modules/010-intro/content/010-start.adoc")
		require.Equal(t, http.StatusOK, rw.Code)
		assert.Equal(t, "= Start\n", rw.Body.String())
		req := httptest.NewRequestWithContext(context.TODO(), http.MethodGet, "/api/v1alpha1/labs/"+ref+"/bundle", nil)
		req.Header.Set("Authorization", "Bearer user1")
		rw = httptest.NewRecorder()
		router.ServeHTTP(rw, req)
		require.Equal(t, http.StatusOK, rw.Code)
		var body map[string]any
		require.NoError(t, json.Unmarshal(rw.Body.Bytes(), &body))
		assert.Equal(t, "LabBundle", body["kind"])
		assert.NotContains(t, body, "pages")
		modules, ok := body["modules"].([]any)
		require.True(t, ok, "bundle must have modules: %s", rw.Body.String())
		require.Len(t, modules, 2)
		for i, expected := range []struct{ id, page string }{{"010-intro", "010-start.adoc"}, {"020-apply", "pages/020-apply.adoc"}} {
			module := modules[i].(map[string]any)
			assert.Equal(t, expected.id, module["id"])
			assert.Len(t, module, 2, "module contains only id and pages")
			pagePath := "modules/" + expected.id + "/content/" + expected.page
			assert.Equal(t, []any{map[string]any{"path": pagePath, "url": "api/v1alpha1/labs/" + ref + "/resources/lab/" + pagePath}}, module["pages"])
		}
		m := body["manifest"].(map[string]any)
		assert.Equal(t, map[string]any{"title": "Manifest title", "description": "Catalog only"}, m["metadata"])
		assert.NotContains(t, m["spec"].(map[string]any), "checkpoints")
		attrs := m["spec"].(map[string]any)["attributes"].(map[string]any)
		assert.Equal(t, "catalog", attrs["shared"])
		assert.Equal(t, "yes", attrs["manifest-only"])
		assert.Equal(t, "yes", attrs["catalog-only"])
		assert.Equal(t, "user1", attrs["user"])
		assert.Equal(t, ref, body["runtimeContext"].(map[string]any)["catalogRef"])
		assert.Equal(t, true, body["runtimeContext"].(map[string]any)["helmChartAvailable"])
	})

	t.Run("cross-module partial and page remain approved resources with ETag", func(t *testing.T) {
		for _, target := range []struct{ path, body string }{
			{"lab/modules/010-intro/content/_partials/shared.adoc", "Shared\n"},
			{"lab/modules/020-apply/content/pages/020-apply.adoc", "= Apply\n"},
		} {
			rw := resourceRequest(t, router, ref, target.path)
			require.Equal(t, http.StatusOK, rw.Code)
			assert.Equal(t, target.body, rw.Body.String())
			assert.Equal(t, `"abc123"`, rw.Header().Get("ETag"))
			assert.Equal(t, "private, no-cache", rw.Header().Get("Cache-Control"))
		}
	})

	t.Run("setup still uses one lab-wide chart and catalog identity", func(t *testing.T) {
		req := httptest.NewRequestWithContext(context.TODO(), http.MethodPost, "/api/v1alpha1/labs/"+ref+"/setup", nil)
		req.Header.Set("Authorization", "Bearer user1")
		rw := httptest.NewRecorder()
		router.ServeHTTP(rw, req)
		require.Equal(t, http.StatusOK, rw.Code)
		var body map[string]any
		require.NoError(t, json.Unmarshal(rw.Body.Bytes(), &body))
		assert.Equal(t, "deployed", body["helm"].(map[string]any)["status"])
		assert.NotEmpty(t, body["helm"].(map[string]any)["releaseName"])
		assert.NotContains(t, rw.Body.String(), "modules/")
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

	t.Run("returns grouped navigable page descriptors", func(t *testing.T) {
		ref := goodCatalogRef()
		rw := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1alpha1/labs/"+ref+"/bundle", nil)
		req.Header.Set("Authorization", "Bearer user1")
		newRouter(t).ServeHTTP(rw, req)

		require.Equal(t, http.StatusOK, rw.Code)
		var body struct {
			Modules []struct {
				ID    string `json:"id"`
				Pages []struct {
					Path string `json:"path"`
					URL  string `json:"url"`
				} `json:"pages"`
			} `json:"modules"`
		}
		require.NoError(t, json.Unmarshal(rw.Body.Bytes(), &body))
		require.Len(t, body.Modules, 1)
		assert.Equal(t, "010-intro", body.Modules[0].ID)
		assert.Len(t, body.Modules[0].Pages, 1)
		assert.Equal(t, "modules/010-intro/content/intro.adoc", body.Modules[0].Pages[0].Path)
		assert.Equal(t, "api/v1alpha1/labs/"+ref+"/resources/lab/modules/010-intro/content/intro.adoc", body.Modules[0].Pages[0].URL)
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
						Metadata:   manifest.Metadata{Title: "Lab"},
						Spec:       manifest.Spec{Attributes: map[string]string{"user": "manifest-val", "manifest-only": "yes"}},
					},
					Modules: []bundle.Module{{ID: "010-intro", Pages: []string{"intro.adoc"}}},
				}, nil
			},
			RuntimeAttributes: func(_ *http.Request, _ string) (map[string]string, error) {
				return map[string]string{
					"user": "runtime-val", "openshift-api-url": "https://api.example.test", "openshift-console-url": "https://console.example.test", "openshift-cluster-ingress-domain": "apps.example.test",
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
		assert.Equal(t, "https://api.example.test", attrs["openshift-api-url"], "runtime values must be present")
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
				"user": "user1", "openshift-api-url": "https://api.example.test", "openshift-console-url": "https://console.example.test",
			}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				router := handler.NewRouter(handler.Config{
					CatalogSource: goodCatalogCM,
					Authenticate:  func(_ context.Context, _, token string) (string, error) { return token, nil },
					BundleLoader: func(_ context.Context, _, _, _ string) (*bundle.Result, error) {
						return &bundle.Result{Manifest: &manifest.LabManifest{}, Modules: []bundle.Module{{ID: "010-intro", Pages: []string{"intro.adoc"}}}}, nil
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
		"gitea": {
			"type": "object",
			"properties": {
				"namespace": {
					"type": "string",
					"title": "gitea namespace",
					"description": "Namespace name for Gitea deployment",
					"x-interactive-labs": {
						"kind": "namespace-name"
					}
				}
			}
		},
		"datasphere": {
			"type": "object",
			"properties": {
				"namespace": {
					"type": "string",
					"title": "datasphere namespace",
					"description": "Namespace name for Datasphere deployment",
					"x-interactive-labs": {
						"kind": "namespace-name"
					}
				}
			}
		}
	}
}`)

func prepareSetupClient(t *testing.T, catalogRefs ...string) *setup.SpaceRequestClient {
	t.Helper()
	ref := goodCatalogRef()
	if len(catalogRefs) > 0 {
		ref = catalogRefs[0]
	}
	label, err := setup.CatalogRefLabel(ref)
	require.NoError(t, err)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			name := filepath.Base(r.URL.Path)
			ns := filepath.Base(filepath.Dir(filepath.Dir(r.URL.Path)))
			if name == "spacerequests" {
				ns = filepath.Base(filepath.Dir(r.URL.Path))
				var items []json.RawMessage
				for _, key := range []string{"gitea", "datasphere"} {
					items = append(items, json.RawMessage(fmt.Sprintf(`{"metadata":{"name":%q,"namespace":%q,"uid":"uid-%s","labels":{%q:%q},"annotations":{%q:%q}},"status":{"namespaceAccess":[{"name":"ns-%s"}],"conditions":[{"type":"Ready","status":"True"}]}}`, key, ns, key, setup.CatalogRefLabelKey, label, setup.BindingKeyAnnotation, key, key)))
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"items": items})
				return
			}
			assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{
				"metadata": map[string]any{
					"name": name, "namespace": ns, "uid": "uid-1",
					"labels": map[string]string{setup.CatalogRefLabelKey: label}, "annotations": map[string]string{setup.BindingKeyAnnotation: name},
				},
				"status": map[string]any{
					"namespaceAccess": []map[string]string{{"name": "ns-" + name, "secretRef": "s"}},
					"conditions":      []map[string]string{{"type": "Ready", "status": "True"}},
				},
			}))
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
	t.Run("given only non-namespace bindings when setup or status is requested then schema is rejected", func(t *testing.T) {
		router := handler.NewRouter(handler.Config{
			CatalogSource: goodCatalogCM,
			Authenticate:  func(_ context.Context, _, token string) (string, error) { return token, nil },
			BundleLoader: func(context.Context, string, string, string) (*bundle.Result, error) {
				return &bundle.Result{ValuesSchema: []byte(`{"properties":{"password":{"x-interactive-labs":{"kind":"generated-password"}},"url":{"x-interactive-labs":{"kind":"root-url"}}}}`)}, nil
			},
			SetupManager: newTestManager(t, nil, nil),
		})
		for _, target := range []struct{ method, suffix string }{{http.MethodPost, "/setup"}, {http.MethodGet, "/setup/status"}} {
			req := httptest.NewRequestWithContext(t.Context(), target.method, "/api/v1alpha1/labs/"+goodCatalogRef()+target.suffix, nil)
			req.Header.Set("Authorization", "Bearer user1")
			rw := httptest.NewRecorder()
			router.ServeHTTP(rw, req)
			assert.Equal(t, http.StatusUnprocessableEntity, rw.Code)
			assert.Contains(t, rw.Body.String(), "values schema declares no namespace bindings")
		}
	})

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

	t.Run("new setup tags its SpaceRequest before Helm starts", func(t *testing.T) {
		ref := goodCatalogRef()
		label, err := setup.CatalogRefLabel(ref)
		require.NoError(t, err)
		var created sync.Map
		var metadata map[string]any
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				_ = json.NewDecoder(r.Body).Decode(&metadata)
				created.Store(metadata["metadata"].(map[string]any)["name"].(string), true)
				w.WriteHeader(http.StatusCreated)
				return
			}
			name := filepath.Base(r.URL.Path)
			if name == "spacerequests" {
				_, _ = fmt.Fprint(w, `{"items":[]}`)
				return
			}
			if _, exists := created.Load(name); !exists {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{
				"metadata": map[string]any{
					"name": name, "namespace": "user1-dev", "uid": "uid-1",
					"labels": map[string]string{setup.CatalogRefLabelKey: label}, "annotations": map[string]string{setup.BindingKeyAnnotation: name},
				},
				"status": map[string]any{
					"namespaceAccess": []map[string]string{{"name": "ns-gitea"}},
					"conditions":      []map[string]string{{"type": "Ready", "status": "True"}},
				},
			}))
		}))
		defer server.Close()
		installCh := make(chan struct{})
		helm := &stubHelmManager{installCh: installCh}
		router := handler.NewRouter(handler.Config{
			CatalogSource: goodCatalogCM,
			Authenticate:  func(_ context.Context, _, token string) (string, error) { return token, nil },
			BundleLoader: func(_ context.Context, _, _, _ string) (*bundle.Result, error) {
				return &bundle.Result{Manifest: &manifest.LabManifest{Metadata: manifest.Metadata{Title: "Lab"}}, HelmChartAvailable: true, ValuesSchema: testValuesSchema}, nil
			},
			SetupManager: newTestManager(t, &setup.SpaceRequestClient{HTTPClient: server.Client(), APIHost: server.URL}, helm),
		})
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1alpha1/labs/"+ref+"/setup", nil)
		req.Header.Set("Authorization", "Bearer user1")
		rw := httptest.NewRecorder()
		router.ServeHTTP(rw, req)
		require.Equal(t, http.StatusAccepted, rw.Code)
		<-installCh
		require.NotNil(t, metadata)
		meta := metadata["metadata"].(map[string]any)
		require.IsType(t, map[string]any{}, meta["labels"], "SpaceRequest identity label missing")
		assert.Equal(t, label, meta["labels"].(map[string]any)[setup.CatalogRefLabelKey])
		require.NotNil(t, helm.installOpts)
		assert.Equal(t, label, helm.installOpts.Labels[setup.CatalogRefLabelKey])
	})

	t.Run("running setup blocks same lab on another pod before creating SRs", func(t *testing.T) {
		client := fake.NewSimpleClientset()
		first := setup.NewOperationCoordinator(client, "plugin-ns")
		second := setup.NewOperationCoordinator(client, "plugin-ns")
		_, release, err := first.Acquire(context.Background(), "user1", goodCatalogRef())
		require.NoError(t, err)
		defer release()
		var calls atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			calls.Add(1)
			w.WriteHeader(http.StatusNotFound)
		}))
		defer server.Close()
		router := handler.NewRouter(handler.Config{
			CatalogSource: goodCatalogCM,
			Authenticate:  func(_ context.Context, _, token string) (string, error) { return token, nil },
			BundleLoader: func(_ context.Context, _, _, _ string) (*bundle.Result, error) {
				return &bundle.Result{Manifest: &manifest.LabManifest{Metadata: manifest.Metadata{Title: "Lab"}}, HelmChartAvailable: true, ValuesSchema: testValuesSchema}, nil
			},
			SetupManager: newTestManager(t, &setup.SpaceRequestClient{HTTPClient: server.Client(), APIHost: server.URL}, &stubHelmManager{}, func(c *setup.ManagerConfig) { c.Coordinator = second }),
		})
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1alpha1/labs/"+goodCatalogRef()+"/setup", nil)
		req.Header.Set("Authorization", "Bearer user1")
		rw := httptest.NewRecorder()
		router.ServeHTTP(rw, req)
		assert.Equal(t, http.StatusConflict, rw.Code)
		assert.Zero(t, calls.Load())
	})

	t.Run("missing coordinator fails closed", func(t *testing.T) {
		router := handler.NewRouter(handler.Config{
			CatalogSource: goodCatalogCM,
			Authenticate:  func(_ context.Context, _, token string) (string, error) { return token, nil },
			BundleLoader: func(context.Context, string, string, string) (*bundle.Result, error) {
				return &bundle.Result{ValuesSchema: testValuesSchema}, nil
			},
			SetupManager: newTestManager(t, prepareSetupClient(t), &stubHelmManager{}, func(c *setup.ManagerConfig) { c.Coordinator = nil }),
		})
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1alpha1/labs/"+goodCatalogRef()+"/setup", nil)
		req.Header.Set("Authorization", "Bearer user1")
		rw := httptest.NewRecorder()
		router.ServeHTTP(rw, req)
		require.Equal(t, http.StatusServiceUnavailable, rw.Code)
		var body map[string]any
		require.NoError(t, json.Unmarshal(rw.Body.Bytes(), &body))
		assert.Equal(t, "configuration_error", body["type"])
		assert.Equal(t, "lab setup unavailable", body["message"])
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
					Manifest:           &manifest.LabManifest{Metadata: manifest.Metadata{Title: "Lab"}},
					Modules:            []bundle.Module{{ID: "010-intro", Pages: []string{"intro.adoc"}}},
					HelmChartAvailable: true,
					ValuesSchema:       testValuesSchema,
				}, nil
			},
			SetupManager: newTestManager(t, client, helm),
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
			installErr: func() error {
				// not actually called here — we intercept via wrapper below
				return nil
			}(),
		}
		originalInstall := helm.Install
		_ = originalInstall

		label, err := setup.CatalogRefLabel(goodCatalogRef())
		require.NoError(t, err)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if r.Method == http.MethodGet {
				name := filepath.Base(r.URL.Path)
				ns := filepath.Base(filepath.Dir(filepath.Dir(r.URL.Path)))
				assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{
					"metadata": map[string]any{
						"name": name, "namespace": ns, "uid": "uid-1",
						"labels": map[string]string{setup.CatalogRefLabelKey: label}, "annotations": map[string]string{setup.BindingKeyAnnotation: name},
					},
					"status": map[string]any{
						"namespaceAccess": []map[string]string{{"name": "ns-abc", "secretRef": "s"}},
						"conditions":      []map[string]string{{"type": "Ready", "status": "True"}},
					},
				}))
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
					Manifest:           &manifest.LabManifest{Metadata: manifest.Metadata{Title: "Lab"}},
					Modules:            []bundle.Module{{ID: "010-intro", Pages: []string{"intro.adoc"}}},
					HelmChartAvailable: true,
					HelmChartDir:       t.TempDir(),
					ValuesSchema:       testValuesSchema,
				}, nil
			},
			SetupManager: newTestManager(t, &setup.SpaceRequestClient{HTTPClient: server.Client(), APIHost: server.URL}, countingHelm, func(c *setup.ManagerConfig) { c.HelmTimeout = 5 * time.Second }),
		})

		ref := goodCatalogRef()
		const concurrency = 5
		var wg sync.WaitGroup
		var accepted, conflicted atomic.Int32
		wg.Add(concurrency)
		for i := 0; i < concurrency; i++ {
			go func() {
				defer wg.Done()
				rw := httptest.NewRecorder()
				req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1alpha1/labs/"+ref+"/setup", nil)
				req.Header.Set("Authorization", "Bearer "+makeTestJWT("concurrent-test-user"))
				router.ServeHTTP(rw, req)
				switch rw.Code {
				case http.StatusAccepted:
					accepted.Add(1)
				case http.StatusConflict:
					conflicted.Add(1)
				default:
					t.Errorf("unexpected response %d: %s", rw.Code, rw.Body.String())
				}
			}()
		}
		wg.Wait()
		assert.Equal(t, int32(1), accepted.Load())
		assert.Equal(t, int32(concurrency-1), conflicted.Load())

		close(blockInstall)

		assert.Eventually(t, func() bool {
			return installCount.Load() >= 1
		}, 5*time.Second, 50*time.Millisecond)

		assert.Equal(t, int32(1), installCount.Load(), "expected exactly one Helm install across concurrent requests")
	})
}

// --- GET /api/v1alpha1/labs/{id}/setup/status ---

func TestSetupStatusHandler(t *testing.T) {
	t.Run("rejects mismatched SpaceRequest identity", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			name := filepath.Base(r.URL.Path)
			assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{
				"metadata": map[string]any{
					"name": name, "namespace": "user1-dev", "uid": "uid-1",
					"labels": map[string]string{setup.CatalogRefLabelKey: "foreign"}, "annotations": map[string]string{setup.BindingKeyAnnotation: name},
				},
				"status": map[string]any{
					"namespaceAccess": []map[string]string{{"name": "ns-abc"}},
					"conditions":      []map[string]string{{"type": "Ready", "status": "True"}},
				},
			}))
		}))
		t.Cleanup(server.Close)
		router := handler.NewRouter(handler.Config{
			CatalogSource: goodCatalogCM,
			Authenticate:  func(_ context.Context, _, token string) (string, error) { return token, nil },
			BundleLoader: func(context.Context, string, string, string) (*bundle.Result, error) {
				return &bundle.Result{ValuesSchema: testValuesSchema}, nil
			},
			SetupManager: newTestManager(t, &setup.SpaceRequestClient{HTTPClient: server.Client(), APIHost: server.URL}, nil),
		})
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1alpha1/labs/"+goodCatalogRef()+"/setup/status", nil)
		req.Header.Set("Authorization", "Bearer user1")
		rw := httptest.NewRecorder()
		router.ServeHTTP(rw, req)
		require.Equal(t, http.StatusConflict, rw.Code)
		var body map[string]any
		require.NoError(t, json.Unmarshal(rw.Body.Bytes(), &body))
		assert.Equal(t, "configuration_error", body["type"])
		assert.Equal(t, "SpaceRequest identity changed; refresh before retrying.", body["message"])
	})

	t.Run("all ready", func(t *testing.T) {
		server := statusSpaceRequestServer(t, "True", "", "ns-abc")

		router := handler.NewRouter(handler.Config{
			CatalogSource: goodCatalogCM,
			Authenticate:  func(_ context.Context, _, token string) (string, error) { return token, nil },
			BundleLoader: func(_ context.Context, _, _, _ string) (*bundle.Result, error) {
				return &bundle.Result{
					Manifest:           &manifest.LabManifest{Metadata: manifest.Metadata{Title: "Lab"}},
					Modules:            []bundle.Module{{ID: "010-intro", Pages: []string{"intro.adoc"}}},
					HelmChartAvailable: true,
					ValuesSchema:       testValuesSchema,
				}, nil
			},
			SetupManager: newTestManager(t, &setup.SpaceRequestClient{HTTPClient: server.Client(), APIHost: server.URL}, nil),
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
		server := statusSpaceRequestServer(t, "False", "Provisioning", "")

		router := handler.NewRouter(handler.Config{
			CatalogSource: goodCatalogCM,
			Authenticate:  func(_ context.Context, _, token string) (string, error) { return token, nil },
			BundleLoader: func(_ context.Context, _, _, _ string) (*bundle.Result, error) {
				return &bundle.Result{
					Manifest:           &manifest.LabManifest{Metadata: manifest.Metadata{Title: "Lab"}},
					Modules:            []bundle.Module{{ID: "010-intro", Pages: []string{"intro.adoc"}}},
					HelmChartAvailable: true,
					ValuesSchema:       testValuesSchema,
				}, nil
			},
			SetupManager: newTestManager(t, &setup.SpaceRequestClient{HTTPClient: server.Client(), APIHost: server.URL}, nil),
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
		server := statusSpaceRequestServer(t, "True", "", "")

		router := handler.NewRouter(handler.Config{
			CatalogSource: goodCatalogCM,
			Authenticate:  func(_ context.Context, _, token string) (string, error) { return token, nil },
			BundleLoader: func(_ context.Context, _, _, _ string) (*bundle.Result, error) {
				return &bundle.Result{
					Manifest:           &manifest.LabManifest{Metadata: manifest.Metadata{Title: "Lab"}},
					Modules:            []bundle.Module{{ID: "010-intro", Pages: []string{"intro.adoc"}}},
					HelmChartAvailable: true,
					ValuesSchema:       testValuesSchema,
				}, nil
			},
			SetupManager: newTestManager(t, &setup.SpaceRequestClient{HTTPClient: server.Client(), APIHost: server.URL}, nil),
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
		server := statusSpaceRequestServer(t, "False", "UnableToCreateSpace", "")

		router := handler.NewRouter(handler.Config{
			CatalogSource: goodCatalogCM,
			Authenticate:  func(_ context.Context, _, token string) (string, error) { return token, nil },
			BundleLoader: func(_ context.Context, _, _, _ string) (*bundle.Result, error) {
				return &bundle.Result{
					Manifest:           &manifest.LabManifest{Metadata: manifest.Metadata{Title: "Lab"}},
					Modules:            []bundle.Module{{ID: "010-intro", Pages: []string{"intro.adoc"}}},
					HelmChartAvailable: true,
					ValuesSchema:       testValuesSchema,
				}, nil
			},
			SetupManager: newTestManager(t, &setup.SpaceRequestClient{HTTPClient: server.Client(), APIHost: server.URL}, nil),
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
		server := statusSpaceRequestServer(t, "True", "", "ns-abc")

		helm := &stubHelmManager{status: "deployed"}
		router := handler.NewRouter(handler.Config{
			CatalogSource: goodCatalogCM,
			Authenticate:  func(_ context.Context, _, token string) (string, error) { return token, nil },
			BundleLoader: func(_ context.Context, _, _, _ string) (*bundle.Result, error) {
				return &bundle.Result{
					Manifest:           &manifest.LabManifest{Metadata: manifest.Metadata{Title: "Lab"}},
					Modules:            []bundle.Module{{ID: "010-intro", Pages: []string{"intro.adoc"}}},
					HelmChartAvailable: true,
					ValuesSchema:       testValuesSchema,
				}, nil
			},
			SetupManager: newTestManager(t, &setup.SpaceRequestClient{HTTPClient: server.Client(), APIHost: server.URL}, helm),
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

	t.Run("unheld Helm install reports interruption", func(t *testing.T) {
		server := statusSpaceRequestServer(t, "True", "", "ns-abc")

		helm := &stubHelmManager{status: "pending-install"}
		router := handler.NewRouter(handler.Config{
			CatalogSource: goodCatalogCM,
			Authenticate:  func(_ context.Context, _, token string) (string, error) { return token, nil },
			BundleLoader: func(_ context.Context, _, _, _ string) (*bundle.Result, error) {
				return &bundle.Result{
					Manifest:           &manifest.LabManifest{Metadata: manifest.Metadata{Title: "Lab"}},
					Modules:            []bundle.Module{{ID: "010-intro", Pages: []string{"intro.adoc"}}},
					HelmChartAvailable: true,
					ValuesSchema:       testValuesSchema,
				}, nil
			},
			SetupManager: newTestManager(t, &setup.SpaceRequestClient{HTTPClient: server.Client(), APIHost: server.URL}, helm),
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
		assert.Equal(t, "failed", helmSection["status"])
		assert.Contains(t, helmSection["message"], "interrupted")
	})

	t.Run("password values in attributes not in generatedValues", func(t *testing.T) {
		server := statusSpaceRequestServer(t, "True", "", "ns-abc")

		helm := &stubHelmManager{
			status:        "deployed",
			releaseValues: map[string]string{"passwords.postgres": "secret123"},
		}
		router := handler.NewRouter(handler.Config{
			CatalogSource: goodCatalogCM,
			Authenticate:  func(_ context.Context, _, token string) (string, error) { return token, nil },
			BundleLoader: func(_ context.Context, _, _, _ string) (*bundle.Result, error) {
				return &bundle.Result{
					Manifest:           &manifest.LabManifest{Metadata: manifest.Metadata{Title: "Lab"}},
					Modules:            []bundle.Module{{ID: "010-intro", Pages: []string{"intro.adoc"}}},
					HelmChartAvailable: true,
					ValuesSchema:       testValuesSchemaWithPasswords,
				}, nil
			},
			SetupManager: newTestManager(t, &setup.SpaceRequestClient{HTTPClient: server.Client(), APIHost: server.URL}, helm),
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
		assert.NotContains(t, helmSection, "generatedValues")
		attrs := body["attributes"].(map[string]any)
		assert.Equal(t, "secret123", attrs["passwords-postgres"])
	})

	t.Run("attributes with namespace names when deployed", func(t *testing.T) {
		server := statusSpaceRequestServer(t, "True", "", "ns-abc")

		helm := &stubHelmManager{status: "deployed"}
		router := handler.NewRouter(handler.Config{
			CatalogSource: goodCatalogCM,
			Authenticate:  func(_ context.Context, _, token string) (string, error) { return token, nil },
			BundleLoader: func(_ context.Context, _, _, _ string) (*bundle.Result, error) {
				return &bundle.Result{
					Manifest:           &manifest.LabManifest{Metadata: manifest.Metadata{Title: "Lab"}},
					Modules:            []bundle.Module{{Pages: []string{"= Intro"}}},
					HelmChartAvailable: true,
					ValuesSchema:       testValuesSchema,
				}, nil
			},
			SetupManager: newTestManager(t, &setup.SpaceRequestClient{HTTPClient: server.Client(), APIHost: server.URL}, helm),
		})

		ref := goodCatalogRef()
		rw := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1alpha1/labs/"+ref+"/setup/status", nil)
		req.Header.Set("Authorization", "Bearer user1")
		router.ServeHTTP(rw, req)

		require.Equal(t, http.StatusOK, rw.Code)
		var body map[string]any
		require.NoError(t, json.Unmarshal(rw.Body.Bytes(), &body))
		require.IsType(t, map[string]any{}, body["attributes"])
		attrs := body["attributes"].(map[string]any)
		assert.Equal(t, "ns-abc", attrs["gitea-namespace"])
		assert.Equal(t, "ns-abc", attrs["datasphere-namespace"])
	})

	t.Run("attributes absent during provisioning", func(t *testing.T) {
		server := statusSpaceRequestServer(t, "False", "Provisioning", "")

		router := handler.NewRouter(handler.Config{
			CatalogSource: goodCatalogCM,
			Authenticate:  func(_ context.Context, _, token string) (string, error) { return token, nil },
			BundleLoader: func(_ context.Context, _, _, _ string) (*bundle.Result, error) {
				return &bundle.Result{
					Manifest: &manifest.LabManifest{Metadata: manifest.Metadata{Title: "Lab"}},
					Modules: []bundle.Module{{
						Pages: []string{"intro.adoc"},
					}},
					HelmChartAvailable: true,
					ValuesSchema:       testValuesSchema,
				}, nil
			},
			SetupManager: newTestManager(t, &setup.SpaceRequestClient{HTTPClient: server.Client(), APIHost: server.URL}, nil),
		})

		ref := goodCatalogRef()
		rw := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1alpha1/labs/"+ref+"/setup/status", nil)
		req.Header.Set("Authorization", "Bearer user1")
		router.ServeHTTP(rw, req)

		require.Equal(t, http.StatusOK, rw.Code)
		var body map[string]any
		require.NoError(t, json.Unmarshal(rw.Body.Bytes(), &body))
		assert.Nil(t, body["attributes"])
	})

	t.Run("attributes with passwords when deployed", func(t *testing.T) {
		server := statusSpaceRequestServer(t, "True", "", "ns-abc")

		helm := &stubHelmManager{
			status:        "deployed",
			releaseValues: map[string]string{"passwords.postgres": "secret123"},
		}
		router := handler.NewRouter(handler.Config{
			CatalogSource: goodCatalogCM,
			Authenticate:  func(_ context.Context, _, token string) (string, error) { return token, nil },
			BundleLoader: func(_ context.Context, _, _, _ string) (*bundle.Result, error) {
				return &bundle.Result{
					Manifest: &manifest.LabManifest{Metadata: manifest.Metadata{Title: "Lab"}},
					Modules: []bundle.Module{{
						Pages: []string{"= Intro"},
					}},
					HelmChartAvailable: true,
					ValuesSchema:       testValuesSchemaWithPasswords,
				}, nil
			},
			SetupManager: newTestManager(t, &setup.SpaceRequestClient{HTTPClient: server.Client(), APIHost: server.URL}, helm),
		})

		ref := goodCatalogRef()
		rw := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1alpha1/labs/"+ref+"/setup/status", nil)
		req.Header.Set("Authorization", "Bearer user1")
		router.ServeHTTP(rw, req)

		require.Equal(t, http.StatusOK, rw.Code)
		var body map[string]any
		require.NoError(t, json.Unmarshal(rw.Body.Bytes(), &body))
		require.IsType(t, map[string]any{}, body["attributes"])
		attrs := body["attributes"].(map[string]any)
		assert.Equal(t, "ns-abc", attrs["namespaces-gitea"])
		assert.Equal(t, "secret123", attrs["passwords-postgres"])
	})

	t.Run("attributes with root URLs when deployed", func(t *testing.T) {
		server := readySpaceRequestServer(t, "ns-gitea-xyz")

		helm := &stubHelmManager{status: "deployed"}
		router := handler.NewRouter(handler.Config{
			CatalogSource: goodCatalogCM,
			Authenticate:  func(_ context.Context, _, token string) (string, error) { return token, nil },
			BundleLoader: func(_ context.Context, _, _, _ string) (*bundle.Result, error) {
				return &bundle.Result{
					Manifest: &manifest.LabManifest{Metadata: manifest.Metadata{Title: "Lab"}},
					Modules: []bundle.Module{{
						Pages: []string{"= Intro"},
					}},
					HelmChartAvailable: true,
					ValuesSchema:       testValuesSchemaWithRootURL,
				}, nil
			},
			SetupManager: newTestManager(t, &setup.SpaceRequestClient{HTTPClient: server.Client(), APIHost: server.URL}, helm, func(c *setup.ManagerConfig) { c.ClusterIngressDomain = "apps.example.com" }),
		})

		ref := goodCatalogRef()
		rw := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1alpha1/labs/"+ref+"/setup/status", nil)
		req.Header.Set("Authorization", "Bearer user1")
		router.ServeHTTP(rw, req)

		require.Equal(t, http.StatusOK, rw.Code)
		var body map[string]any
		require.NoError(t, json.Unmarshal(rw.Body.Bytes(), &body))
		require.IsType(t, map[string]any{}, body["attributes"])
		attrs := body["attributes"].(map[string]any)
		assert.Equal(t, "ns-gitea-xyz", attrs["namespaces-gitea"])
		assert.Equal(t, "gitea-ns-gitea-xyz.apps.example.com", attrs["gitea-rootUrl"])
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
			{name: "page", resource: "lab/modules/010-intro/content/intro.adoc", wantBody: []byte("= Intro\n"), contentType: "text/plain; charset=utf-8"},
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
			ResourceLoader: func(context.Context, string, string, string) (*bundle.Resource, error) {
				return &bundle.Resource{Bytes: []byte("= Intro\n"), Revision: "abc123"}, nil
			},
		})

		rw := resourceRequest(t, router, ref, "lab/modules/010-intro/content/intro.adoc")

		require.Equal(t, http.StatusOK, rw.Code)
		assert.Equal(t, "text/plain; charset=utf-8", rw.Header().Get("Content-Type"))
	})

	t.Run("returns not modified from paired resource revision", func(t *testing.T) {
		loads := 0
		ref := goodCatalogRef()
		router := handler.NewRouter(handler.Config{
			CatalogSource: goodCatalogCM,
			Authenticate:  func(_ context.Context, _ string, token string) (string, error) { return token, nil },
			ResourceLoader: func(context.Context, string, string, string) (*bundle.Resource, error) {
				loads++
				return &bundle.Resource{Bytes: []byte("= Intro\n"), Revision: "abc123"}, nil
			},
		})
		request := httptest.NewRequestWithContext(context.TODO(), http.MethodGet, "/api/v1alpha1/labs/"+ref+"/resources/lab/modules/010-intro/content/intro.adoc", nil)
		request.Header.Set("Authorization", "Bearer user1")
		request.Header.Set("If-None-Match", `"abc123"`)
		recorder := httptest.NewRecorder()

		router.ServeHTTP(recorder, request)

		assert.Equal(t, http.StatusNotModified, recorder.Code)
		assert.Equal(t, 1, loads)
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
					"/api/v1alpha1/labs/"+ref+"/resources/lab/modules/010-intro/content/intro.adoc", nil)
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
		require.NoError(t, os.Symlink(outside, filepath.Join(root, "lab", "modules", "010-intro", "content", "escape.adoc")))
		require.NoError(t, os.Symlink("intro.adoc", filepath.Join(root, "lab", "modules", "010-intro", "content", "link.adoc")))

		ref := goodCatalogRef()
		router := resourceRouter(t, goodCatalogCM, root, "abc123")
		for _, tc := range []struct {
			name         string
			resource     string
			wantStatus   int
			wantType     string
			wantRedirect string
		}{
			{name: "missing", resource: "lab/modules/010-intro/content/missing.adoc", wantStatus: http.StatusNotFound, wantType: "content_error"},
			// these cases makes sure that even if the caller wants to be clever and pass in the path as one url segment, we handle the path on the backend properly and don't allow an escape from the repo root
			{name: "missing encoded", resource: "lab%2Fmodules%2F010-intro%2Fcontent%2Fmissing.adoc", wantStatus: http.StatusNotFound, wantType: "content_error"},
			{name: "remote", resource: "https://example.test/outside.adoc", wantStatus: http.StatusTemporaryRedirect, wantRedirect: "https:/example.test/outside.adoc"},
			{name: "remote redirected", resource: "https:/example.test/outside.adoc", wantStatus: http.StatusUnprocessableEntity, wantType: "path_traversal"},
			{name: "remote encoded", resource: "https:%2F%2Fexample.test%2Foutside.adoc", wantStatus: http.StatusUnprocessableEntity, wantType: "path_traversal"},
			{name: "absolute", resource: "/etc/passwd", wantStatus: http.StatusTemporaryRedirect, wantRedirect: "etc/passwd"},
			{name: "absolute encoded", resource: "%2Fetc%2Fpasswd", wantStatus: http.StatusUnprocessableEntity, wantType: "path_traversal"},
			// here we just test we cannot obtain a path that would point outside of our repository - we actually obtain a URL that points to a different enpoint completely, giving a 404 after the redirect, so that's correct behavior
			{name: "repository escape", resource: "lab/modules/010-intro/content/../../../../../../outside.adoc", wantStatus: http.StatusTemporaryRedirect, wantRedirect: "labs/outside.adoc"},
			{name: "repository escape encoded", resource: "lab%2Fmodules%2F010-intro%2Fcontent%2F..%2F..%2F..%2F..%2F..%2F..%2Foutside.adoc", wantStatus: http.StatusUnprocessableEntity, wantType: "path_traversal"},
			{name: "directory", resource: "lab/modules/010-intro/content", wantStatus: http.StatusUnprocessableEntity, wantType: "content_error"},
			{name: "directory encoded", resource: "lab%2Fmodules%2F010-intro%2Fcontent", wantStatus: http.StatusUnprocessableEntity, wantType: "content_error"},
			{name: "symlink escape", resource: "lab/modules/010-intro/content/escape.adoc", wantStatus: http.StatusUnprocessableEntity, wantType: "path_traversal"},
			{name: "symlink escape encoded", resource: "lab%2Fmodules%2F010-intro%2Fcontent%2Fescape.adoc", wantStatus: http.StatusUnprocessableEntity, wantType: "path_traversal"},
			{name: "symlink", resource: "lab/modules/010-intro/content/link.adoc", wantStatus: http.StatusUnprocessableEntity, wantType: "path_traversal"},
			{name: "symlink encoded", resource: "lab%2Fmodules%2F010-intro%2Fcontent%2Flink.adoc", wantStatus: http.StatusUnprocessableEntity, wantType: "path_traversal"},
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
		var gotRepository, gotBranch string
		router := handler.NewRouter(handler.Config{
			CatalogSource: stubCatalogCM(map[string]string{"catalog.yaml": `- repository: https://github.com/redhat/other
  branch: main
  labPath: other-lab
- repository: https://github.com/redhat/selected
  branch: stable
  labPath: selected-lab`}),
			Authenticate: func(_ context.Context, _, token string) (string, error) { return token, nil },
			ResourceLoader: func(_ context.Context, repository, branch, resourcePath string) (*bundle.Resource, error) {
				gotRepository, gotBranch = repository, branch
				assert.Equal(t, "selected-lab/modules/010-intro/content/intro.adoc", resourcePath)
				return &bundle.Resource{Bytes: []byte("= Intro\n"), Revision: "abc123"}, nil
			},
		})

		rw := resourceRequest(t, router, ref, "selected-lab/modules/010-intro/content/intro.adoc")
		require.Equal(t, http.StatusOK, rw.Code)
		assert.Equal(t, repository, gotRepository)
		assert.Equal(t, branch, gotBranch)
	})

	t.Run("uses revision paired with resource bytes", func(t *testing.T) {
		ref := goodCatalogRef()
		router := handler.NewRouter(handler.Config{
			CatalogSource: goodCatalogCM,
			Authenticate:  func(_ context.Context, _, token string) (string, error) { return token, nil },
			ResourceLoader: func(context.Context, string, string, string) (*bundle.Resource, error) {
				return &bundle.Resource{Bytes: []byte("= New\n"), Revision: "new"}, nil
			},
		})

		rw := resourceRequest(t, router, ref, "lab/content/intro.adoc")
		require.Equal(t, http.StatusOK, rw.Code)
		assert.Equal(t, []byte("= New\n"), rw.Body.Bytes())
		assert.Equal(t, `"new"`, rw.Header().Get("ETag"))
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

func statusSpaceRequestServer(t *testing.T, conditionStatus, reason, targetNamespace string) *httptest.Server {
	t.Helper()
	label, err := setup.CatalogRefLabel(goodCatalogRef())
	require.NoError(t, err)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		name := filepath.Base(r.URL.Path)
		namespace := filepath.Base(filepath.Dir(filepath.Dir(r.URL.Path)))
		assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"metadata": map[string]any{
				"name": name, "namespace": namespace, "uid": "uid-1",
				"labels": map[string]string{setup.CatalogRefLabelKey: label}, "annotations": map[string]string{setup.BindingKeyAnnotation: name},
			},
			"status": map[string]any{
				"namespaceAccess": []map[string]string{{"name": targetNamespace, "secretRef": "s"}},
				"conditions":      []map[string]string{{"type": "Ready", "status": conditionStatus, "reason": reason}},
			},
		}))
	}))
	t.Cleanup(server.Close)
	return server
}

func readySpaceRequestServer(t *testing.T, nsName string) *httptest.Server {
	t.Helper()
	label, err := setup.CatalogRefLabel(goodCatalogRef())
	require.NoError(t, err)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		userNamespace := filepath.Base(filepath.Dir(filepath.Dir(r.URL.Path)))
		assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"metadata": map[string]any{
				"name": "gitea", "namespace": userNamespace, "uid": "uid-1",
				"labels": map[string]string{setup.CatalogRefLabelKey: label}, "annotations": map[string]string{setup.BindingKeyAnnotation: "gitea"},
			},
			"status": map[string]any{
				"namespaceAccess": []map[string]string{{"name": nsName, "secretRef": "s"}},
				"conditions":      []map[string]string{{"type": "Ready", "status": "True"}},
			},
		}))
	}))
	t.Cleanup(server.Close)
	return server
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
	catalogRef    string
	statusErr     error
	installErr    error
	installed     bool
	installOpts   *setup.HelmInstallOpts
	releaseValues map[string]string
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

func (s *stubHelmManager) ListLabReleases(_ context.Context, namespace, _ string, _ []setup.SpaceRequestResult) ([]setup.LabRelease, error) {
	if s.status == "deployed" || s.status == "pending-install" {
		ref := s.catalogRef
		if ref == "" {
			ref = goodCatalogRef()
		}
		config, err := setup.BuildValues(s.releaseValues)
		if err != nil {
			return nil, err
		}
		return []setup.LabRelease{{Name: "il-existing", Namespace: namespace, CatalogRef: ref, Status: s.status, UID: "release-uid", Revision: 1, Config: config}}, nil
	}
	return nil, nil
}

func (s *stubHelmManager) UninstallLabRelease(context.Context, string, string, setup.LabRelease, []setup.SpaceRequestResult) error {
	return errors.New("unexpected uninstall")
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

func (c *countingHelmManager) ListLabReleases(ctx context.Context, namespace, token string, srs []setup.SpaceRequestResult) ([]setup.LabRelease, error) {
	return c.inner.ListLabReleases(ctx, namespace, token, srs)
}

func (c *countingHelmManager) UninstallLabRelease(ctx context.Context, namespace, token string, target setup.LabRelease, srs []setup.SpaceRequestResult) error {
	return c.inner.UninstallLabRelease(ctx, namespace, token, target, srs)
}

// --- helpers ---

func resourceRouter(t *testing.T, catalogSource func(context.Context) (*corev1.ConfigMap, error), root, etag string) http.Handler {
	t.Helper()
	return handler.NewRouter(handler.Config{
		CatalogSource: catalogSource,
		Authenticate:  func(_ context.Context, _, token string) (string, error) { return token, nil },
		ResourceLoader: func(_ context.Context, _, _, resourcePath string) (*bundle.Resource, error) {
			resource, err := bundle.ReadResource(root, resourcePath)
			if resource != nil {
				resource.Revision = etag
			}
			return resource, err
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
	for _, dir := range []string{"lab/modules/010-intro/content", "lab/partials", "lab/images"} {
		require.NoError(t, os.MkdirAll(filepath.Join(root, dir), 0o755))
	}
	require.NoError(t, os.WriteFile(filepath.Join(root, "lab", "modules", "010-intro", "content", "intro.adoc"), []byte("= Intro\n"), 0o600))
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

// --- GET /api/healthz ---

func TestHealthzHandler(t *testing.T) {
	t.Run("returns 200", func(t *testing.T) {
		rw := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/healthz", nil)
		newRouter(t).ServeHTTP(rw, req)

		assert.Equal(t, http.StatusOK, rw.Code)
	})
}

// --- GET /api/readyz ---

func TestReadyzHandler(t *testing.T) {
	t.Run("returns 200 when ready", func(t *testing.T) {
		router := handler.NewRouter(handler.Config{
			ReadinessCheck: func() bool { return true },
		})
		rw := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/readyz", nil)
		router.ServeHTTP(rw, req)

		assert.Equal(t, http.StatusOK, rw.Code)
	})

	t.Run("returns 503 when not ready", func(t *testing.T) {
		router := handler.NewRouter(handler.Config{
			ReadinessCheck: func() bool { return false },
		})
		rw := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/readyz", nil)
		router.ServeHTTP(rw, req)

		assert.Equal(t, http.StatusServiceUnavailable, rw.Code)
	})

	t.Run("returns 200 when no readiness check configured", func(t *testing.T) {
		router := handler.NewRouter(handler.Config{})
		rw := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/readyz", nil)
		router.ServeHTTP(rw, req)

		assert.Equal(t, http.StatusOK, rw.Code)
	})
}
