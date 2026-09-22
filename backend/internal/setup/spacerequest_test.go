package setup

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnsureSpaceRequest(t *testing.T) {
	t.Run("creates when not exists", func(t *testing.T) {
		var created bool
		var receivedBody map[string]any
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			created = true
			json.NewDecoder(r.Body).Decode(&receivedBody) //nolint:errcheck
			w.WriteHeader(http.StatusCreated)
		}))
		defer server.Close()

		client := &SpaceRequestClient{HTTPClient: server.Client(), APIHost: server.URL}
		err := client.EnsureSpaceRequest(context.Background(), "test-token", "user1-dev", "gitea", "base1ns")

		require.NoError(t, err)
		assert.True(t, created)
		assert.Equal(t, "SpaceRequest", receivedBody["kind"])
		spec := receivedBody["spec"].(map[string]any)
		assert.Equal(t, "base1ns", spec["tierName"])
		meta := receivedBody["metadata"].(map[string]any)
		assert.Equal(t, "gitea", meta["name"])
		assert.Equal(t, "user1-dev", meta["namespace"])
	})

	t.Run("skips when exists", func(t *testing.T) {
		var createCalled bool
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet {
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
					"metadata": map[string]any{"name": "gitea"},
					"status":   map[string]any{},
				})
				return
			}
			createCalled = true
			w.WriteHeader(http.StatusCreated)
		}))
		defer server.Close()

		client := &SpaceRequestClient{HTTPClient: server.Client(), APIHost: server.URL}
		err := client.EnsureSpaceRequest(context.Background(), "test-token", "user1-dev", "gitea", "base1ns")

		require.NoError(t, err)
		assert.False(t, createCalled)
	})

	t.Run("handles conflict", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusConflict)
		}))
		defer server.Close()

		client := &SpaceRequestClient{HTTPClient: server.Client(), APIHost: server.URL}
		err := client.EnsureSpaceRequest(context.Background(), "test-token", "user1-dev", "gitea", "base1ns")

		require.NoError(t, err)
	})
}

func TestGetSpaceRequest(t *testing.T) {
	t.Run("returns result", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
				"metadata": map[string]any{"name": "gitea"},
				"status": map[string]any{
					"namespaceAccess": []map[string]any{
						{"name": "user1-abc123", "secretRef": "secret-ref"},
					},
					"conditions": []map[string]any{
						{"type": "Ready", "status": "True"},
					},
				},
			})
		}))
		defer server.Close()

		client := &SpaceRequestClient{HTTPClient: server.Client(), APIHost: server.URL}
		result, err := client.GetSpaceRequest(context.Background(), "test-token", "user1-dev", "gitea")

		require.NoError(t, err)
		require.NotNil(t, result)
		assert.Equal(t, "gitea", result.Name)
		assert.True(t, result.IsReady())
		assert.Equal(t, "user1-abc123", result.TargetNamespace())
	})

	t.Run("not found", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		defer server.Close()

		client := &SpaceRequestClient{HTTPClient: server.Client(), APIHost: server.URL}
		result, err := client.GetSpaceRequest(context.Background(), "test-token", "user1-dev", "gitea")

		require.NoError(t, err)
		assert.Nil(t, result)
	})
}

func TestSpaceRequestResult(t *testing.T) {
	t.Run("IsReady", func(t *testing.T) {
		tests := []struct {
			name       string
			conditions []SpaceRequestCondition
			expected   bool
		}{
			{"ready", []SpaceRequestCondition{{Type: "Ready", Status: "True"}}, true},
			{"not ready", []SpaceRequestCondition{{Type: "Ready", Status: "False"}}, false},
			{"no conditions", nil, false},
			{"other condition", []SpaceRequestCondition{{Type: "Provisioning", Status: "True"}}, false},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				r := &SpaceRequestResult{Conditions: tt.conditions}
				assert.Equal(t, tt.expected, r.IsReady())
			})
		}
	})

	t.Run("ReadyConditionReason", func(t *testing.T) {
		tests := []struct {
			name       string
			conditions []SpaceRequestCondition
			expected   string
		}{
			{"has reason", []SpaceRequestCondition{{Type: "Ready", Status: "False", Reason: "Provisioning"}}, "Provisioning"},
			{"ready with no reason", []SpaceRequestCondition{{Type: "Ready", Status: "True"}}, ""},
			{"no conditions", nil, ""},
			{"other condition only", []SpaceRequestCondition{{Type: "Approved", Status: "True", Reason: "Auto"}}, ""},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				r := &SpaceRequestResult{Conditions: tt.conditions}
				assert.Equal(t, tt.expected, r.ReadyConditionReason())
			})
		}
	})

	t.Run("IsFailed", func(t *testing.T) {
		tests := []struct {
			name       string
			conditions []SpaceRequestCondition
			expected   bool
		}{
			{"terminal failure", []SpaceRequestCondition{{Type: "Ready", Status: "False", Reason: "UnableToCreateSpace"}}, true},
			{"provisioning (not failed)", []SpaceRequestCondition{{Type: "Ready", Status: "False", Reason: "Provisioning"}}, false},
			{"ready (not failed)", []SpaceRequestCondition{{Type: "Ready", Status: "True"}}, false},
			{"no reason (not failed)", []SpaceRequestCondition{{Type: "Ready", Status: "False"}}, false},
			{"no conditions", nil, false},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				r := &SpaceRequestResult{Conditions: tt.conditions}
				assert.Equal(t, tt.expected, r.IsFailed())
			})
		}
	})
}
