package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
)

func TestCatalogEventPosterPostsOneSafeWarningPerRefAndReason(t *testing.T) {
	var mu sync.Mutex
	posts := 0
	var event corev1.Event
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		posts++
		assert.Equal(t, "Bearer service-account-token", r.Header.Get("Authorization"))
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&event))
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	var logs bytes.Buffer
	poster := newCatalogEventPoster("interactive-labs-console-plugin", server.URL, func() (string, error) {
		return "service-account-token", nil
	}, server.Client(), slog.New(slog.NewJSONHandler(&logs, nil)))
	ctx := context.Background()
	poster.Post(ctx, "opaque-ref", "ManifestMissing")
	poster.Post(ctx, "opaque-ref", "ManifestMissing")
	poster.Post(ctx, "opaque-ref", "LabInvalid")

	mu.Lock()
	assert.Equal(t, 2, posts)
	mu.Unlock()
	assert.Equal(t, "Warning", event.Type)
	assert.Equal(t, "LabInvalid", event.Reason)
	assert.Equal(t, "interactive-labs-catalog", event.InvolvedObject.Name)
	assert.Equal(t, "ConfigMap", event.InvolvedObject.Kind)
	assert.Equal(t, "interactive-labs-console-plugin", event.InvolvedObject.Namespace)
	assert.Equal(t, "opaque-ref", event.Annotations["interactive-labs.openshift.io/catalog-ref"])
	encoded, err := json.Marshal(event)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "service-account-token")
	assert.NotContains(t, logs.String(), "service-account-token")
}

func TestCatalogEventPosterConcurrentFailureIsAttemptedOnce(t *testing.T) {
	posts := 0
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		posts++
		mu.Unlock()
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	poster := newCatalogEventPoster("ns", server.URL, func() (string, error) { return "token", nil }, server.Client(), slog.Default())

	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() { defer wg.Done(); poster.Post(context.Background(), "opaque-ref", "RepositoryUnavailable") }()
	}
	wg.Wait()
	mu.Lock()
	assert.Equal(t, 1, posts)
	mu.Unlock()
}
