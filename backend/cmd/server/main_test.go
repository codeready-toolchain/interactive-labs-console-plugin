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

	"github.com/codeready-toolchain/interactive-labs-console-plugin/backend/internal/handler"
)

func TestPluginHandlerServesBuiltAssets(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "plugin-manifest.json"), `{"name":"interactive-labs"}`)

	rw := httptest.NewRecorder()
	pluginHandler(dir).ServeHTTP(rw, httptest.NewRequest(http.MethodGet, "/plugin-manifest.json", nil))

	require.Equal(t, http.StatusOK, rw.Code)
	require.Contains(t, rw.Body.String(), "interactive-labs")
}

func TestTokenNotLogged(t *testing.T) {
	var logs bytes.Buffer
	router := handler.NewRouter(handler.Config{
		CatalogSource: func() (*corev1.ConfigMap, error) {
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
	token := "eyJhbGciOiJub25lIn0.eyJzdWIiOiJhbGljZSJ9."
	req := httptest.NewRequest(http.MethodPost, "/api/v1alpha1/labs/lab/setup", strings.NewReader(`{"namespaceBindings":{"releaseNamespace":"labs"}}`))
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
		CatalogSource: func() (*corev1.ConfigMap, error) {
			return &corev1.ConfigMap{Data: map[string]string{"catalog.yaml": `- displayName: Lab
  repository: https://github.com/redhat/lab
  branch: main
  labPath: lab`}}, nil
		},
		Logger: slog.New(slog.NewTextHandler(os.Stderr, nil)),
	}
	h := combinedHandler(handler.NewRouter(cfg), dir)

	t.Run("asset served", func(t *testing.T) {
		rw := httptest.NewRecorder()
		h.ServeHTTP(rw, httptest.NewRequest(http.MethodGet, "/plugin-manifest.json", nil))
		require.Equal(t, http.StatusOK, rw.Code)
	})

	t.Run("catalog reachable", func(t *testing.T) {
		rw := httptest.NewRecorder()
		h.ServeHTTP(rw, httptest.NewRequest(http.MethodGet, "/api/v1alpha1/catalog", nil))
		require.Equal(t, http.StatusOK, rw.Code)
		require.Contains(t, rw.Header().Get("Content-Type"), "application/json")
	})
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
}
