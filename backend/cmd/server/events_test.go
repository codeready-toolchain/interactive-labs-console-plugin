package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/codeready-toolchain/interactive-labs-console-plugin/backend/internal/catalog"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func eventEntry() catalog.Entry {
	return catalog.Entry{CatalogRef: "opaque-ref", Repository: "https://github.com/redhat/lab", Branch: "main", LabPath: "labs/example"}
}

func TestCatalogEventPoster(t *testing.T) {
	t.Run("creates one safe typed warning per ref and reason", func(t *testing.T) {
		const namespace = "interactive-labs-console-plugin"
		client := k8sfake.NewSimpleClientset()
		poster := newCatalogEventPoster(namespace, client.CoreV1().Events(namespace), slog.Default())

		poster.Post(context.Background(), eventEntry(), "ManifestMissing")
		poster.Post(context.Background(), eventEntry(), "ManifestMissing")
		poster.Post(context.Background(), eventEntry(), "LabInvalid")

		require.Len(t, client.Actions(), 2)
		for _, action := range client.Actions() {
			require.Equal(t, "create", action.GetVerb())
			require.Equal(t, "events", action.GetResource().Resource)
			require.Equal(t, namespace, action.GetNamespace())
		}
		event := client.Actions()[1].(k8stesting.CreateAction).GetObject().(*corev1.Event)
		assert.Empty(t, event.TypeMeta)
		assert.Equal(t, "interactive-labs-catalog-", event.GenerateName)
		assert.Equal(t, "Warning", event.Type)
		assert.Equal(t, "LabInvalid", event.Reason)
		assert.Equal(t, "Configured lab omitted from catalog", event.Message)
		assert.Equal(t, "v1", event.InvolvedObject.APIVersion)
		assert.Equal(t, "ConfigMap", event.InvolvedObject.Kind)
		assert.Equal(t, namespace, event.InvolvedObject.Namespace)
		assert.Equal(t, "interactive-labs-catalog", event.InvolvedObject.Name)
		assert.Equal(t, "opaque-ref", event.Annotations["interactive-labs.openshift.io/catalog-ref"])
	})

	t.Run("annotates validated source locator", func(t *testing.T) {
		client := k8sfake.NewSimpleClientset()
		poster := newCatalogEventPoster("ns", client.CoreV1().Events("ns"), slog.Default())
		entry := eventEntry()

		poster.Post(context.Background(), entry, "CloneFailed")

		event := client.Actions()[0].(k8stesting.CreateAction).GetObject().(*corev1.Event)
		assert.Equal(t, entry.CatalogRef, event.Annotations["interactive-labs.openshift.io/catalog-ref"])
		assert.Equal(t, entry.Repository, event.Annotations["interactive-labs.openshift.io/repository"])
		assert.Equal(t, entry.Branch, event.Annotations["interactive-labs.openshift.io/branch"])
		assert.Equal(t, entry.LabPath, event.Annotations["interactive-labs.openshift.io/lab-path"])
		assert.NotContains(t, event.Message, entry.Repository)
	})

	t.Run("preserves caller context", func(t *testing.T) {
		client := k8sfake.NewSimpleClientset()
		poster := newCatalogEventPoster("ns", client.CoreV1().Events("ns"), slog.Default())
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		poster.Post(ctx, eventEntry(), "RepositoryUnavailable")

		require.Empty(t, client.Actions())
	})

	t.Run("concurrent failure is attempted once and safe", func(t *testing.T) {
		const secret = "event credential leak" // #nosec G101
		client := k8sfake.NewSimpleClientset()
		client.PrependReactor("create", "events", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, errors.New(secret)
		})
		var logs bytes.Buffer
		poster := newCatalogEventPoster("ns", client.CoreV1().Events("ns"), slog.New(slog.NewJSONHandler(&logs, nil)))

		var wg sync.WaitGroup
		for range 10 {
			wg.Go(func() {
				poster.Post(context.Background(), eventEntry(), "RepositoryUnavailable")
			})
		}
		wg.Wait()

		require.Len(t, client.Actions(), 1)
		require.Contains(t, logs.String(), "catalog omission event failed")
		require.Contains(t, logs.String(), "opaque-ref")
		require.Contains(t, logs.String(), "RepositoryUnavailable")
		require.NotContains(t, logs.String(), secret)
	})
}
