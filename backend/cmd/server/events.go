package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"sync"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type eventKey struct {
	catalogRef string
	reason     string
}

type catalogEventPoster struct {
	mu        sync.Mutex
	attempted map[eventKey]struct{}
	namespace string
	apiHost   string
	token     func() (string, error)
	client    *http.Client
	log       *slog.Logger
}

func newCatalogEventPoster(namespace, apiHost string, token func() (string, error), client *http.Client, log *slog.Logger) *catalogEventPoster {
	return &catalogEventPoster{attempted: make(map[eventKey]struct{}), namespace: namespace, apiHost: apiHost, token: token, client: client, log: log}
}

func (p *catalogEventPoster) Post(catalogRef, reason string) {
	key := eventKey{catalogRef: catalogRef, reason: reason}
	p.mu.Lock()
	if _, exists := p.attempted[key]; exists {
		p.mu.Unlock()
		return
	}
	p.attempted[key] = struct{}{}
	p.mu.Unlock()

	token, err := p.token()
	if err != nil {
		p.warn(catalogRef, reason)
		return
	}
	body, err := json.Marshal(corev1.Event{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Event"},
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "interactive-labs-catalog-",
			Annotations:  map[string]string{"interactive-labs.openshift.io/catalog-ref": catalogRef},
		},
		InvolvedObject: corev1.ObjectReference{APIVersion: "v1", Kind: "ConfigMap", Namespace: p.namespace, Name: "interactive-labs-catalog"},
		Reason:         reason,
		Message:        "Configured lab omitted from catalog",
		Type:           corev1.EventTypeWarning,
	})
	if err != nil {
		p.warn(catalogRef, reason)
		return
	}
	req, err := http.NewRequest(http.MethodPost, p.apiHost+"/api/v1/namespaces/"+url.PathEscape(p.namespace)+"/events", bytes.NewReader(body))
	if err != nil {
		p.warn(catalogRef, reason)
		return
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.client.Do(req)
	if err != nil || resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		if resp != nil {
			resp.Body.Close() //nolint:errcheck
		}
		p.warn(catalogRef, reason)
		return
	}
	resp.Body.Close() //nolint:errcheck
}

func (p *catalogEventPoster) warn(catalogRef, reason string) {
	if p.log != nil {
		p.log.Warn("catalog omission event failed", "catalogRef", catalogRef, "reason", reason)
	}
}

func inClusterCatalogEventPoster(log *slog.Logger) func(string, string) {
	namespace := os.Getenv("NAMESPACE")
	if namespace == "" {
		namespace = "interactive-labs-console-plugin"
	}
	client, err := tlsClientWithCA("/var/run/secrets/kubernetes.io/serviceaccount/ca.crt")
	if err != nil {
		log.Warn("catalog event client unavailable")
		return func(string, string) {}
	}
	poster := newCatalogEventPoster(namespace,
		fmt.Sprintf("https://%s", os.Getenv("KUBERNETES_SERVICE_HOST")+":"+os.Getenv("KUBERNETES_SERVICE_PORT")),
		func() (string, error) {
			value, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/token")
			return string(value), err
		}, client, log)
	return poster.Post
}
