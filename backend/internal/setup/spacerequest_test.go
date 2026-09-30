package setup

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSpaceRequestTeardown(t *testing.T) {
	ref := "abababababababababababababababababababababababababababababababab"
	label, err := CatalogRefLabel(ref)
	require.NoError(t, err)
	t.Run("list finds only current user's tagged SRs", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "Bearer user-token", r.Header.Get("Authorization"))
			assert.Equal(t, CatalogRefLabelKey+"="+label, r.URL.Query().Get("labelSelector"))
			_, _ = w.Write([]byte(`{"items":[{"metadata":{"name":"gitea","namespace":"alice-dev","uid":"uid-1","labels":{"` + CatalogRefLabelKey + `":"` + label + `"},"annotations":{"` + BindingKeyAnnotation + `":"gitea"}}}]}`))
		}))
		defer server.Close()
		client := &SpaceRequestClient{HTTPClient: server.Client(), APIHost: server.URL}
		found, err := client.ListSpaceRequestsForLab(context.Background(), "user-token", "alice-dev", ref)
		require.NoError(t, err)
		require.Len(t, found, 1)
		assert.Equal(t, "uid-1", found[0].UID)
	})
	for _, tc := range []struct{ name, body string }{
		{"incomplete list fails closed", `{"items":[],"metadata":{"continue":"next-page"}}`},
		{"oversized list fails closed", `{"items":[],"padding":"` + strings.Repeat("x", 5<<20) + `"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(tc.body)) }))
			defer server.Close()
			client := &SpaceRequestClient{HTTPClient: server.Client(), APIHost: server.URL}
			_, err := client.ListSpaceRequestsForLab(context.Background(), "token", "alice-dev", ref)
			require.Error(t, err)
		})
	}
	t.Run("fresh UID and identity checked before delete", func(t *testing.T) {
		var deleted map[string]any
		currentUID := "uid-1"
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet {
				_, _ = w.Write([]byte(`{"metadata":{"name":"gitea","namespace":"alice-dev","uid":"` + currentUID + `","labels":{"` + CatalogRefLabelKey + `":"` + label + `"},"annotations":{"` + BindingKeyAnnotation + `":"gitea"}}}`))
				return
			}
			require.Equal(t, http.MethodDelete, r.Method)
			require.NoError(t, json.NewDecoder(r.Body).Decode(&deleted))
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()
		client := &SpaceRequestClient{HTTPClient: server.Client(), APIHost: server.URL}
		target := SpaceRequestResult{Name: "gitea", Namespace: "alice-dev", UID: "uid-1", Labels: map[string]string{CatalogRefLabelKey: label}, Annotations: map[string]string{BindingKeyAnnotation: "gitea"}}
		require.NoError(t, client.DeleteVerifiedSpaceRequest(context.Background(), "user-token", "alice-dev", ref, target))
		assert.Equal(t, "uid-1", deleted["preconditions"].(map[string]any)["uid"])
		deleted = nil
		currentUID = "replacement"
		require.Error(t, client.DeleteVerifiedSpaceRequest(context.Background(), "user-token", "alice-dev", ref, target))
		assert.Nil(t, deleted)
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

func TestSpaceRequestTermination(t *testing.T) {
	ref := strings.Repeat("ab", 32)
	label, err := CatalogRefLabel(ref)
	require.NoError(t, err)
	for _, tc := range []struct {
		name, deletion string
		ready          bool
	}{
		{name: "absent deletion timestamp remains ready", ready: true},
		{name: "null deletion timestamp remains ready", deletion: `,"deletionTimestamp":null`, ready: true},
		{name: "terminating SR with Ready=True is not ready", deletion: `,"deletionTimestamp":"2026-10-01T00:00:00Z"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mutations := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					mutations++
				}
				_, _ = w.Write([]byte(`{"metadata":{"name":"gitea","namespace":"alice-dev","uid":"uid-1","labels":{"` + CatalogRefLabelKey + `":"` + label + `"},"annotations":{"` + BindingKeyAnnotation + `":"gitea"}` + tc.deletion + `},"status":{"namespaceAccess":[{"name":"ns-gitea"}],"conditions":[{"type":"Ready","status":"True"}]}}`))
			}))
			defer server.Close()
			client := &SpaceRequestClient{HTTPClient: server.Client(), APIHost: server.URL}
			sr, err := client.GetSpaceRequest(context.Background(), "token", "alice-dev", "gitea")
			require.NoError(t, err)
			require.NotNil(t, sr)
			assert.Equal(t, tc.ready, sr.IsReady())
			ensureErr := client.EnsureSpaceRequestForLab(context.Background(), "token", "alice-dev", "gitea", "base1ns", ref, "gitea")
			if tc.ready {
				assert.NoError(t, ensureErr)
			} else {
				assert.Error(t, ensureErr, "identity-matching terminating SR must not be adopted")
			}
			assert.Zero(t, mutations)
		})
	}
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

func TestEnsureSpaceRequestForLab(t *testing.T) {
	ref := strings.Repeat("ab", 32)
	label, err := CatalogRefLabel(ref)
	require.NoError(t, err)
	for _, tc := range []struct {
		name           string
		getStatus      int
		existingLabels map[string]string
		existingKey    string
		postStatus     int
		wantError      bool
		wantPost       bool
	}{
		{name: "new request carries identity before provisioning", getStatus: http.StatusNotFound, postStatus: http.StatusCreated, wantPost: true},
		{name: "same lab and binding can reuse", getStatus: http.StatusOK, existingLabels: map[string]string{CatalogRefLabelKey: label}, existingKey: "namespace.gitea"},
		{name: "unmarked legacy request cannot be adopted", getStatus: http.StatusOK, wantError: true},
		{name: "foreign lab request cannot be adopted", getStatus: http.StatusOK, existingLabels: map[string]string{CatalogRefLabelKey: "other"}, existingKey: "namespace.gitea", wantError: true},
		{name: "wrong binding cannot be adopted", getStatus: http.StatusOK, existingLabels: map[string]string{CatalogRefLabelKey: label}, existingKey: "other", wantError: true},
		{name: "GET permission failure does not permit create", getStatus: http.StatusForbidden, wantError: true},
		{name: "create conflict does not permit reuse", getStatus: http.StatusNotFound, postStatus: http.StatusConflict, wantError: true, wantPost: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var posted map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
				switch r.Method {
				case http.MethodGet:
					w.WriteHeader(tc.getStatus)
					if tc.getStatus == http.StatusOK {
						_ = json.NewEncoder(w).Encode(map[string]any{"metadata": map[string]any{
							"name": "gitea", "namespace": "user-dev", "uid": "uid-1", "labels": tc.existingLabels,
							"annotations": map[string]string{BindingKeyAnnotation: tc.existingKey},
						}})
					}
				case http.MethodPost:
					require.NoError(t, json.NewDecoder(r.Body).Decode(&posted))
					w.WriteHeader(tc.postStatus)
				}
			}))
			defer server.Close()
			client := &SpaceRequestClient{HTTPClient: server.Client(), APIHost: server.URL}
			err := client.EnsureSpaceRequestForLab(context.Background(), "test-token", "user-dev", "gitea", "base1ns", ref, "namespace.gitea")
			if tc.wantError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			if tc.wantPost {
				require.NotNil(t, posted)
				assert.Equal(t, "SpaceRequest", posted["kind"])
				spec := posted["spec"].(map[string]any)
				assert.Equal(t, "base1ns", spec["tierName"])
				meta := posted["metadata"].(map[string]any)
				assert.Equal(t, "gitea", meta["name"])
				assert.Equal(t, label, meta["labels"].(map[string]any)[CatalogRefLabelKey])
				assert.Equal(t, "namespace.gitea", meta["annotations"].(map[string]any)[BindingKeyAnnotation])
			} else {
				assert.Nil(t, posted)
			}
		})
	}
}
