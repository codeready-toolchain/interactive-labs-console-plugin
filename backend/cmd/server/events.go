package main

import (
	"context"
	"log/slog"
	"os"
	"sync"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
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

func (p *catalogEventPoster) Post(ctx context.Context, catalogRef, reason string) {
	if ctx.Err() != nil {
		return
	}
	key := eventKey{catalogRef: catalogRef, reason: reason}
	p.mu.Lock()
	if _, exists := p.attempted[key]; exists {
		p.mu.Unlock()
		return
	}
	p.attempted[key] = struct{}{}
	p.mu.Unlock()

	if p.events == nil {
		p.warn(catalogRef, reason)
		return
	}
	_, err := p.events.Create(ctx, &corev1.Event{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "interactive-labs-catalog-",
			Annotations:  map[string]string{"interactive-labs.openshift.io/catalog-ref": catalogRef},
		},
		InvolvedObject: corev1.ObjectReference{APIVersion: "v1", Kind: "ConfigMap", Namespace: p.namespace, Name: "interactive-labs-catalog"},
		Reason:         reason,
		Message:        "Configured lab omitted from catalog",
		Type:           corev1.EventTypeWarning,
	}, metav1.CreateOptions{})
	if err != nil {
		p.warn(catalogRef, reason)
	}
}

func (p *catalogEventPoster) warn(catalogRef, reason string) {
	if p.log != nil {
		p.log.Warn("catalog omission event failed", "catalogRef", catalogRef, "reason", reason)
	}
}

func inClusterCatalogEventPoster(log *slog.Logger) func(context.Context, string, string) {
	namespace := os.Getenv("NAMESPACE")
	if namespace == "" {
		namespace = "interactive-labs-console-plugin"
	}
	config, err := rest.InClusterConfig()
	if err != nil {
		if log != nil {
			log.Warn("catalog event client unavailable")
		}
		return func(context.Context, string, string) {}
	}
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		if log != nil {
			log.Warn("catalog event client unavailable")
		}
		return func(context.Context, string, string) {}
	}
	return newCatalogEventPoster(namespace, client.CoreV1().Events(namespace), log).Post
}
