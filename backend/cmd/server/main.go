// Command server is the Interactive Labs backend HTTPS service.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
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
	"github.com/codeready-toolchain/interactive-labs-console-plugin/backend/internal/catalog"
	"github.com/codeready-toolchain/interactive-labs-console-plugin/backend/internal/config"
	"github.com/codeready-toolchain/interactive-labs-console-plugin/backend/internal/handler"
	"github.com/codeready-toolchain/interactive-labs-console-plugin/backend/internal/setup"
)

const (
	backendAddr       = ":9001"
	backendTLSCert    = "/etc/tls/backend/tls.crt"
	backendTLSKey     = "/etc/tls/backend/tls.key"
	pluginAssets      = "/app/frontend"
	snapshotCacheRoot = "/var/lib/interactive-labs/cache"
)

var (
	commit    = "dev"
	buildtime = "unknown"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	serverConfig := config.LoadFromEnv(log)
	cache, err := bundle.NewCache(snapshotCacheRoot)
	if err != nil {
		log.Error("create snapshot cache failed")
		return
	}
	catalogSource := inClusterCatalogSource(serverConfig.Namespace)
	catalogEvents := inClusterCatalogEventPoster(log, serverConfig.Namespace)

	cfg := handler.Config{
		CatalogSource:        catalogSource,
		BundleLoader:         cache.LoadBundle,
		ResourceLoader:       cache.ReadResource,
		CatalogLabOmitted:    catalogEvents,
		Logger:               log,
		Authenticate:         inClusterIdentityResolver(),
		RuntimeAttributes:    inClusterRuntimeAttributes(log),
		SetupClient:          inClusterSetupClient(log),
		SpaceRequestTierName: "base1ns",
		HelmManager:          inClusterHelmClient(log),
		HelmTimeout:          helmTimeout(),
		ClusterIngressDomain: inClusterIngressDomain(log),
	}

	backend := &http.Server{
		Addr:         backendAddr,
		Handler:      combinedHandler(handler.NewRouter(cfg), pluginAssets),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 600 * time.Second, // Helm setup can be slow
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	refreshCoordinator := bundle.NewRefreshCoordinator(
		loadCatalogEntries(catalogSource),
		cache.Refresh,
		func(ctx context.Context, entry bundle.RefreshEntry, reason string) {
			catalogEvents(ctx, catalog.Entry{CatalogRef: entry.CatalogRef, Repository: entry.Repository, Branch: entry.Branch, LabPath: entry.LabPath}, reason)
		},
		serverConfig.CacheRefreshInterval,
		int(serverConfig.CacheConcurrentRefreshLimit),
	)
	// make sure the refresh is completed before we exit
	refreshDone := make(chan struct{})
	go func() {
		defer close(refreshDone)
		refreshCoordinator.Run(ctx, func(error) { log.Warn("catalog refresh failed") })
	}()
	go serveTLS(log, "backend", backend, backendTLSCert, backendTLSKey)

	<-ctx.Done()
	log.Info("shutting down")
	shutCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	backend.Shutdown(shutCtx) //nolint:errcheck
	<-refreshDone
	cache.Close() // preserves successful snapshots for next process
}

func serveTLS(log *slog.Logger, name string, server *http.Server, cert, key string) {
	log.Info("interactive-labs server starting", "name", name, "addr", server.Addr, "commit", commit, "buildtime", buildtime)
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
func inClusterIdentityResolver() func(context.Context, string, string) (string, error) {
	baseConfig, err := rest.InClusterConfig()
	if err != nil {
		return newClusterIdentityResolver(func(string, string) (userclient.Interface, error) { return nil, err })
	}
	return newClusterIdentityResolver(func(impersonateUsername, token string) (userclient.Interface, error) {
		config := rest.CopyConfig(baseConfig)
		config.BearerToken = token
		config.BearerTokenFile = ""
		config.Impersonate.UserName = impersonateUsername
		return userclient.NewForConfig(config)
	})
}

func newClusterIdentityResolver(newClient func(string, string) (userclient.Interface, error)) func(context.Context, string, string) (string, error) {
	return func(ctx context.Context, impersonateUsername, token string) (string, error) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		client, err := newClient(impersonateUsername, token)
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
	config.Timeout = 10 * time.Minute // generous but finite timeout
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
func inClusterCatalogSource(namespace string) func(ctx context.Context) (*corev1.ConfigMap, error) {
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

func inClusterSetupClient(log *slog.Logger) *setup.SpaceRequestClient {
	caPath := "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
	apiHost := fmt.Sprintf("https://%s", os.Getenv("KUBERNETES_SERVICE_HOST")+":"+os.Getenv("KUBERNETES_SERVICE_PORT"))
	client, err := tlsClientWithCA(caPath)
	if err != nil {
		log.Warn("setup client unavailable", "err", err)
		return nil
	}
	return &setup.SpaceRequestClient{HTTPClient: client, APIHost: apiHost}
}

func loadCatalogEntries(source func(context.Context) (*corev1.ConfigMap, error)) func(context.Context) ([]bundle.RefreshEntry, error) {
	return func(ctx context.Context) ([]bundle.RefreshEntry, error) {
		cm, err := source(ctx)
		if err != nil {
			return nil, fmt.Errorf("catalog unavailable")
		}
		entries, err := catalog.Read(cm)
		if err != nil {
			return nil, fmt.Errorf("catalog configuration error")
		}
		refreshEntries := make([]bundle.RefreshEntry, len(entries))
		for i, entry := range entries {
			refreshEntries[i] = bundle.RefreshEntry{CatalogRef: entry.CatalogRef, Repository: entry.Repository, Branch: entry.Branch, LabPath: entry.LabPath}
		}
		return refreshEntries, nil
	}
}

func inClusterHelmClient(log *slog.Logger) setup.HelmManager {
	caPath := "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
	apiHost := fmt.Sprintf("https://%s", os.Getenv("KUBERNETES_SERVICE_HOST")+":"+os.Getenv("KUBERNETES_SERVICE_PORT"))
	if os.Getenv("KUBERNETES_SERVICE_HOST") == "" {
		log.Warn("helm client unavailable: KUBERNETES_SERVICE_HOST not set")
		return nil
	}
	client, err := tlsClientWithCA(caPath)
	if err != nil {
		log.Warn("helm client unavailable", "err", err)
		return nil
	}
	return &setup.HelmClient{APIHost: apiHost, CAFile: caPath, HTTPClient: client, Logger: log}
}

func helmTimeout() time.Duration {
	if s := os.Getenv("HELM_TIMEOUT_SECONDS"); s != "" {
		if v, err := strconv.Atoi(s); err == nil && v > 0 {
			return time.Duration(v) * time.Second
		}
	}
	return 300 * time.Second
}

// inClusterIngressDomain fetches the cluster's ingress domain from the
// ingresses.config.openshift.io/cluster resource at startup.
func inClusterIngressDomain(log *slog.Logger) string {
	log.Info("fetching cluster ingress domain")
	apiHost := os.Getenv("KUBERNETES_SERVICE_HOST")
	if apiHost == "" {
		log.Warn("cluster ingress domain unavailable: KUBERNETES_SERVICE_HOST not set")
		return ""
	}
	caPath := "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
	tokenPath := "/var/run/secrets/kubernetes.io/serviceaccount/token" //nolint:gosec

	token, err := os.ReadFile(tokenPath)
	if err != nil {
		log.Warn("cluster ingress domain unavailable: cannot read service account token", "err", err)
		return ""
	}

	client, err := tlsClientWithCA(caPath)
	if err != nil {
		log.Warn("cluster ingress domain unavailable", "err", err)
		return ""
	}

	url := fmt.Sprintf("https://%s:%s/apis/config.openshift.io/v1/ingresses/cluster",
		os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT"))
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil) //nolint:gosec
	if err != nil {
		log.Warn("cluster ingress domain unavailable", "err", err)
		return ""
	}
	req.Header.Set("Authorization", "Bearer "+string(token))

	resp, err := client.Do(req) //nolint:gosec // URL built from in-cluster KUBERNETES_SERVICE_HOST/PORT env vars
	if err != nil {
		log.Warn("cluster ingress domain unavailable", "err", err)
		return ""
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Warn("cluster ingress domain unavailable", "status", resp.StatusCode)
		return ""
	}

	var ingress struct {
		Spec struct {
			Domain string `json:"domain"`
		} `json:"spec"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&ingress); err != nil {
		log.Warn("cluster ingress domain unavailable: decode failed", "err", err)
		return ""
	}
	if ingress.Spec.Domain == "" {
		log.Warn("cluster ingress domain unavailable: empty domain in response")
		return ""
	}

	log.Info("cluster ingress domain resolved", "domain", ingress.Spec.Domain)
	return ingress.Spec.Domain
}
