package setup

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestMapBindingsToValues(t *testing.T) {
	t.Run("all ready", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if r.URL.Path[len(r.URL.Path)-len("gitea"):] == "gitea" {
				fmt.Fprint(w, `{"metadata":{"name":"gitea"},"status":{"namespaceAccess":[{"name":"ns-gitea-123","secretRef":"s"}],"conditions":[{"type":"Ready","status":"True"}]}}`)
			} else {
				fmt.Fprint(w, `{"metadata":{"name":"lab"},"status":{"namespaceAccess":[{"name":"ns-lab-456","secretRef":"s"}],"conditions":[{"type":"Ready","status":"True"}]}}`)
			}
		}))
		defer server.Close()

		client := &SpaceRequestClient{HTTPClient: server.Client(), APIHost: server.URL}
		bindings := []NamespaceBinding{
			{Key: "gitea", Path: "namespaces.gitea"},
			{Key: "lab", Path: "namespaces.lab"},
		}
		values, err := MapBindingsToValues(context.Background(), client, "token", "user1-dev", bindings)
		require.NoError(t, err)
		assert.Equal(t, "ns-gitea-123", values["namespaces.gitea"])
		assert.Equal(t, "ns-lab-456", values["namespaces.lab"])
	})

	t.Run("not found", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		defer server.Close()

		client := &SpaceRequestClient{HTTPClient: server.Client(), APIHost: server.URL}
		bindings := []NamespaceBinding{{Key: "gitea", Path: "namespaces.gitea"}}
		_, err := MapBindingsToValues(context.Background(), client, "token", "user1-dev", bindings)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not found")
	})

	t.Run("not ready", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"metadata":{"name":"gitea"},"status":{"conditions":[{"type":"Ready","status":"False","reason":"Provisioning"}]}}`)
		}))
		defer server.Close()

		client := &SpaceRequestClient{HTTPClient: server.Client(), APIHost: server.URL}
		bindings := []NamespaceBinding{{Key: "gitea", Path: "namespaces.gitea"}}
		_, err := MapBindingsToValues(context.Background(), client, "token", "user1-dev", bindings)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not ready")
	})

	t.Run("empty bindings", func(t *testing.T) {
		client := &SpaceRequestClient{HTTPClient: http.DefaultClient, APIHost: "http://unused"}
		values, err := MapBindingsToValues(context.Background(), client, "token", "user1-dev", nil)
		require.NoError(t, err)
		assert.Empty(t, values)
	})
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
