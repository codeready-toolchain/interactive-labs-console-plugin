package main

import (
	"context"
	"log/slog"
	"sync"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"

	"github.com/codeready-toolchain/interactive-labs-console-plugin/backend/internal/catalog"
)

type eventKey struct {
	catalogRef string
	reason     string
}

type catalogEventPoster struct {
	mu        sync.Mutex
	attempted map[eventKey]struct{}
	namespace string
	events    corev1client.EventInterface
	log       *slog.Logger
}

func newCatalogEventPoster(namespace string, events corev1client.EventInterface, log *slog.Logger) *catalogEventPoster {
	return &catalogEventPoster{attempted: make(map[eventKey]struct{}), namespace: namespace, events: events, log: log}
}

func (p *catalogEventPoster) Post(ctx context.Context, entry catalog.Entry, reason string) {
	if ctx.Err() != nil {
		return
	}
	key := eventKey{catalogRef: entry.CatalogRef, reason: reason}
	p.mu.Lock()
	if _, exists := p.attempted[key]; exists {
		p.mu.Unlock()
		return
	}
	p.attempted[key] = struct{}{}
	p.mu.Unlock()

	if p.events == nil {
		p.warn(entry, reason)
		return
	}
	_, err := p.events.Create(ctx, &corev1.Event{
		GenerateName: "interactive-labs-catalog-",
		Annotations: map[string]string{
			"interactive-labs.openshift.io/catalog-ref": entry.CatalogRef,
			"interactive-labs.openshift.io/repository":  entry.Repository,
			"interactive-labs.openshift.io/branch":      entry.Branch,
			"interactive-labs.openshift.io/lab-path":    entry.LabPath,
		},
		InvolvedObject: corev1.ObjectReference{APIVersion: "v1", Kind: "ConfigMap", Namespace: p.namespace, Name: "interactive-labs-catalog"},
		Reason:         reason,
		Message:        "Configured lab omitted from catalog",
		Type:           corev1.EventTypeWarning,
	}, metav1.CreateOptions{})
	if err != nil {
		p.warn(entry, reason)
	}
}

func (p *catalogEventPoster) warn(entry catalog.Entry, reason string) {
	if p.log != nil {
		p.log.Warn("catalog omission event failed", "catalogRef", entry.CatalogRef, "repository", entry.Repository, "branch", entry.Branch, "labPath", entry.LabPath, "reason", reason)
	}
}

func inClusterCatalogEventPoster(log *slog.Logger, namespace string) func(context.Context, catalog.Entry, string) {
	if namespace == "" {
		namespace = "interactive-labs-console-plugin"
	}
	config, err := rest.InClusterConfig()
	if err != nil {
		if log != nil {
			log.Warn("catalog event client unavailable")
		}
		return func(context.Context, catalog.Entry, string) {}
	}
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		if log != nil {
			log.Warn("catalog event client unavailable")
		}
		return func(context.Context, catalog.Entry, string) {}
	}
	return newCatalogEventPoster(namespace, client.CoreV1().Events(namespace), log).Post
}
