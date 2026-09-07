package main

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"

	"github.com/codeready-toolchain/interactive-labs-console-plugin/backend/internal/bundle"
	"github.com/codeready-toolchain/interactive-labs-console-plugin/backend/internal/handler"
	"github.com/codeready-toolchain/interactive-labs-console-plugin/backend/internal/manifest"
)

func TestRuntimeAttributesReadsClusterValues(t *testing.T) {
	const (
		serviceAccountToken = "service-account-token"
		oldConsolePath      = "/apis/operator.openshift.io/v1/consoles/cluster"
	)
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		require.Equal(t, "Bearer "+serviceAccountToken, r.Header.Get("Authorization"))
		switch r.URL.Path {
		case "/apis/config.openshift.io/v1/infrastructures/cluster":
			_, _ = w.Write([]byte(`{"status":{"apiServerURL":"https://api.example.test:6443"}}`))
		case "/apis/config.openshift.io/v1/consoles/cluster":
			_, _ = w.Write([]byte(`{"status":{"consoleURL":"https://console.example.test"}}`))
		case "/apis/config.openshift.io/v1/ingresses/cluster":
			_, _ = w.Write([]byte(`{"spec":{"domain":"apps.example.test"}}`))
		default:
			http.Error(w, "unexpected request path", http.StatusNotFound)
		}
	}))
	defer server.Close()

	attributes, err := newClusterRuntimeAttributes(server.URL, func() (string, error) {
		return serviceAccountToken, nil
	}, server.Client(), nil)(httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil), "alice")

	require.NoError(t, err)
	require.Equal(t, map[string]string{
		"user":                             "alice",
		"openshift_api_url":                "https://api.example.test:6443",
		"openshift_console_url":            "https://console.example.test",
		"openshift_cluster_ingress_domain": "apps.example.test",
	}, attributes)
	require.Equal(t, []string{
		"/apis/config.openshift.io/v1/infrastructures/cluster",
		"/apis/config.openshift.io/v1/consoles/cluster",
		"/apis/config.openshift.io/v1/ingresses/cluster",
	}, paths)
	require.NotContains(t, paths, oldConsolePath)
}

func TestRuntimeAttributeFailureLogsOnlyAttributeName(t *testing.T) {
	const (
		serviceAccountToken = "service-account-token"
		consoleURL          = "https://console.example.test"
	)
	var logs bytes.Buffer
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/apis/config.openshift.io/v1/infrastructures/cluster":
			_, _ = w.Write([]byte(`{"status":{"apiServerURL":"https://api.example.test:6443"}}`))
		case "/apis/config.openshift.io/v1/consoles/cluster":
			http.Error(w, consoleURL, http.StatusForbidden)
		default:
			http.Error(w, "unexpected request path", http.StatusNotFound)
		}
	}))
	defer server.Close()

	attributes, err := newClusterRuntimeAttributes(server.URL, func() (string, error) {
		return serviceAccountToken, nil
	}, server.Client(), slog.New(slog.NewTextHandler(&logs, nil)))(httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil), "alice")

	require.Error(t, err)
	require.Nil(t, attributes)
	require.Contains(t, logs.String(), "attribute=openshift_console_url")
	require.NotContains(t, logs.String(), serviceAccountToken)
	require.NotContains(t, logs.String(), "alice")
	require.NotContains(t, logs.String(), "https://api.example.test:6443")
	require.NotContains(t, logs.String(), consoleURL)
}

func TestRuntimeAttributesRejectsInvalidClusterResponses(t *testing.T) {
	const (
		infrastructurePath = "/apis/config.openshift.io/v1/infrastructures/cluster"
		consolePath        = "/apis/config.openshift.io/v1/consoles/cluster"
		ingressPath        = "/apis/config.openshift.io/v1/ingresses/cluster"
	)
	validResponse := func(path string) string {
		switch path {
		case infrastructurePath:
			return `{"status":{"apiServerURL":"https://api.example.test:6443"}}`
		case consolePath:
			return `{"status":{"consoleURL":"https://console.example.test"}}`
		default:
			return `{"spec":{"domain":"apps.example.test"}}`
		}
	}
	for _, tc := range []struct {
		name, path, body string
		status           int
	}{
		{name: "missing API URL", path: infrastructurePath, body: `{"status":{}}`},
		{name: "missing console URL", path: consolePath, body: `{"status":{}}`},
		{name: "missing ingress domain", path: ingressPath, body: `{"spec":{}}`},
		{name: "malformed response", path: consolePath, body: `{`},
		{name: "API failure", path: ingressPath, status: http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == tc.path {
					if tc.status != 0 {
						w.WriteHeader(tc.status)
						return
					}
					_, _ = w.Write([]byte(tc.body))
					return
				}
				_, _ = w.Write([]byte(validResponse(r.URL.Path)))
			}))
			defer server.Close()

			attributes, err := newClusterRuntimeAttributes(server.URL, func() (string, error) {
				return "service-account-token", nil
			}, server.Client(), nil)(httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil), "alice")

			require.Error(t, err)
			require.Nil(t, attributes)
		})
	}
}

func TestPluginHandlerServesBuiltAssets(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "plugin-manifest.json"), `{"name":"interactive-labs"}`)

	rw := httptest.NewRecorder()
	pluginHandler(dir).ServeHTTP(rw, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/plugin-manifest.json", nil))

	require.Equal(t, http.StatusOK, rw.Code)
	require.Contains(t, rw.Body.String(), "interactive-labs")
}

func TestTokenNotLogged(t *testing.T) {
	var logs bytes.Buffer
	router := handler.NewRouter(handler.Config{
		CatalogSource: func(_ context.Context) (*corev1.ConfigMap, error) {
			return &corev1.ConfigMap{Data: map[string]string{"catalog.yaml": `- displayName: Lab
  repository: https://github.com/redhat/lab
  branch: main
  labPath: lab`}}, nil
		},
		Logger: slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Authenticate: func(_ context.Context, _ string) (string, error) {
			return "alice", nil
		},
	})
	token := "eyJhbGciOiJub25lIn0.eyJzdWIiOiJhbGljZSJ9." //nolint:gosec
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1alpha1/labs/lab/setup", strings.NewReader(`{"namespaceBindings":{"releaseNamespace":"labs"}}`))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(httptest.NewRecorder(), req)

	require.NotContains(t, logs.String(), token)
	require.NotContains(t, logs.String(), "Bearer")
	require.Contains(t, logs.String(), "catalogRef=lab")
}

func TestCombinedHandler(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "plugin-manifest.json"), `{"name":"interactive-labs"}`)

	cfg := handler.Config{
		CatalogSource: func(_ context.Context) (*corev1.ConfigMap, error) {
			return &corev1.ConfigMap{Data: map[string]string{"catalog.yaml": `- displayName: Lab
  repository: https://github.com/redhat/lab
  branch: main
  labPath: lab`}}, nil
		},
		BundleLoader: func(context.Context, string, string, string) (*bundle.Result, error) {
			return &bundle.Result{Manifest: &manifest.LabManifest{Metadata: manifest.Metadata{Name: "lab", DisplayName: "Lab"}}, Pages: []string{"intro.adoc"}}, nil
		},
		Logger: slog.New(slog.NewTextHandler(os.Stderr, nil)),
	}
	h := combinedHandler(handler.NewRouter(cfg), dir)

	t.Run("asset served", func(t *testing.T) {
		rw := httptest.NewRecorder()
		h.ServeHTTP(rw, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/plugin-manifest.json", nil))
		require.Equal(t, http.StatusOK, rw.Code)
	})

	t.Run("catalog reachable", func(t *testing.T) {
		rw := httptest.NewRecorder()
		h.ServeHTTP(rw, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1alpha1/catalog", nil))
		require.Equal(t, http.StatusOK, rw.Code)
		require.Contains(t, rw.Header().Get("Content-Type"), "application/json")
	})
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
}
