// Command server is the Interactive Labs backend HTTPS service.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	configclient "github.com/openshift/client-go/config/clientset/versioned"
	userclient "github.com/openshift/client-go/user/clientset/versioned"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/codeready-toolchain/interactive-labs-console-plugin/backend/internal/bundle"
	"github.com/codeready-toolchain/interactive-labs-console-plugin/backend/internal/handler"
)

const (
	backendAddr       = ":9001"
	backendTLSCert    = "/etc/tls/backend/tls.crt"
	backendTLSKey     = "/etc/tls/backend/tls.key"
	pluginAssets      = "/app/frontend"
	snapshotCacheRoot = "/var/lib/interactive-labs/cache"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cache, err := bundle.NewCache(snapshotCacheRoot)
	if err != nil {
		log.Error("create snapshot cache failed")
		return
	}

	cfg := handler.Config{
		CatalogSource:      inClusterCatalogSource(),
		BundleLoader:       cache.Load,
		SnapshotCommitHash: cache.CommitHash,
		ResourceLoader:     cache.Read,
		CatalogLabOmitted:  inClusterCatalogEventPoster(log),
		Logger:             log,
		Authenticate:       inClusterIdentityResolver(),
		RuntimeAttributes:  inClusterRuntimeAttributes(log),
	}

	backend := &http.Server{
		Addr:         backendAddr,
		Handler:      combinedHandler(handler.NewRouter(cfg), pluginAssets),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 600 * time.Second, // Helm setup can be slow
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go serveTLS(log, "backend", backend, backendTLSCert, backendTLSKey)

	<-ctx.Done()
	log.Info("shutting down")
	shutCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	backend.Shutdown(shutCtx) //nolint:errcheck
	cache.Close()             // preserves successful snapshots for next process
}

func serveTLS(log *slog.Logger, name string, server *http.Server, cert, key string) {
	log.Info("interactive-labs server starting", "name", name, "addr", server.Addr)
	if err := server.ListenAndServeTLS(cert, key); err != nil && err != http.ErrServerClosed {
		log.Error("server error", "name", name, "err", err)
	}
}

func pluginHandler(assets string) http.Handler {
	return http.FileServer(http.Dir(filepath.Clean(assets)))
}

func combinedHandler(api http.Handler, assets string) http.Handler {
	files := pluginHandler(assets)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			api.ServeHTTP(w, r)
		} else {
			files.ServeHTTP(w, r)
		}
	})
}

// inClusterIdentityResolver verifies each forwarded user token through a request-local typed client.
// The token lives only in this request and is never logged or retained.
func inClusterIdentityResolver() func(context.Context, string) (string, error) {
	baseConfig, err := rest.InClusterConfig()
	if err != nil {
		return newClusterIdentityResolver(func(string) (userclient.Interface, error) { return nil, err })
	}
	return newClusterIdentityResolver(func(token string) (userclient.Interface, error) {
		config := rest.CopyConfig(baseConfig)
		config.BearerToken = token
		config.BearerTokenFile = ""
		return userclient.NewForConfig(config)
	})
}

func newClusterIdentityResolver(newClient func(string) (userclient.Interface, error)) func(context.Context, string) (string, error) {
	return func(ctx context.Context, token string) (string, error) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		client, err := newClient(token)
		if err != nil || client == nil {
			return "", fmt.Errorf("identity unavailable")
		}
		user, err := client.UserV1().Users().Get(ctx, "~", metav1.GetOptions{})
		if err != nil || user.Name == "" {
			return "", fmt.Errorf("identity unavailable")
		}
		return user.Name, nil
	}
}

// inClusterRuntimeAttributes reads cluster values through service-account-authenticated typed API calls.
// Returned values are request-scoped and only become effective manifest attributes.
func inClusterRuntimeAttributes(log *slog.Logger) func(*http.Request, string) (map[string]string, error) {
	config, err := rest.InClusterConfig()
	if err != nil {
		log.Warn("runtime attributes unavailable", "category", "runtime_client")
		return func(*http.Request, string) (map[string]string, error) {
			return nil, fmt.Errorf("runtime attributes unavailable")
		}
	}
	client, err := configclient.NewForConfig(config)
	if err != nil {
		log.Warn("runtime attributes unavailable", "category", "runtime_client")
		return func(*http.Request, string) (map[string]string, error) {
			return nil, fmt.Errorf("runtime attributes unavailable")
		}
	}
	return newClusterRuntimeAttributes(client, log)
}

func newClusterRuntimeAttributes(client configclient.Interface, log *slog.Logger) func(*http.Request, string) (map[string]string, error) {
	return func(r *http.Request, subject string) (map[string]string, error) {
		fail := func(category string) (map[string]string, error) {
			if log != nil {
				log.Warn("runtime attributes unavailable", "attribute", category)
			}
			return nil, fmt.Errorf("runtime attributes unavailable")
		}
		if err := r.Context().Err(); err != nil {
			return nil, err
		}
		if subject == "" {
			return fail("user")
		}
		if client == nil {
			return fail("runtime_client")
		}

		infrastructure, err := client.ConfigV1().Infrastructures().Get(r.Context(), "cluster", metav1.GetOptions{})
		if err != nil || infrastructure.Status.APIServerURL == "" {
			return fail("openshift_api_url")
		}
		console, err := client.ConfigV1().Consoles().Get(r.Context(), "cluster", metav1.GetOptions{})
		if err != nil || console.Status.ConsoleURL == "" {
			return fail("openshift_console_url")
		}
		ingress, err := client.ConfigV1().Ingresses().Get(r.Context(), "cluster", metav1.GetOptions{})
		if err != nil || ingress.Spec.Domain == "" {
			return fail("openshift_cluster_ingress_domain")
		}

		return map[string]string{
			"user":                             subject,
			"openshift_api_url":                infrastructure.Status.APIServerURL,
			"openshift_console_url":            console.Status.ConsoleURL,
			"openshift_cluster_ingress_domain": ingress.Spec.Domain,
		}, nil
	}
}

// inClusterCatalogSource reads the catalog ConfigMap through the service-account-authenticated typed client.
func inClusterCatalogSource() func(ctx context.Context) (*corev1.ConfigMap, error) {
	namespace := os.Getenv("NAMESPACE")
	if namespace == "" {
		namespace = "interactive-labs-console-plugin"
	}
	config, err := rest.InClusterConfig()
	if err != nil {
		return newClusterCatalogSource(nil, namespace)
	}
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return newClusterCatalogSource(nil, namespace)
	}
	return newClusterCatalogSource(client, namespace)
}

func newClusterCatalogSource(client kubernetes.Interface, namespace string) func(context.Context) (*corev1.ConfigMap, error) {
	return func(ctx context.Context) (*corev1.ConfigMap, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if client == nil {
			return nil, fmt.Errorf("catalog unavailable")
		}
		configMap, err := client.CoreV1().ConfigMaps(namespace).Get(ctx, "interactive-labs-catalog", metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("catalog unavailable")
		}
		return configMap, nil
	}
}
