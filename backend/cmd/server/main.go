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
	"strings"
	"syscall"
	"time"

	corev1 "k8s.io/api/core/v1"

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

// inClusterIdentityResolver uses OpenShift's user endpoint to verify a forwarded token
// and obtain its subject. The token lives only in this request and is never logged.
func inClusterIdentityResolver() func(context.Context, string) (string, error) {
	caPath := "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
	apiHost := fmt.Sprintf("https://%s", os.Getenv("KUBERNETES_SERVICE_HOST")+":"+os.Getenv("KUBERNETES_SERVICE_PORT"))

	return func(ctx context.Context, token string) (string, error) {
		client, err := tlsClientWithCA(caPath)
		if err != nil {
			return "", fmt.Errorf("create identity client: %w", err)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiHost+"/apis/user.openshift.io/v1/users/~", nil) //nolint:gosec
		if err != nil {
			return "", err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := client.Do(req) //nolint:gosec
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("identity request status %d", resp.StatusCode)
		}
		var user struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
			return "", err
		}
		if user.Metadata.Name == "" {
			return "", fmt.Errorf("identity response has no user name")
		}
		return user.Metadata.Name, nil
	}
}

// inClusterRuntimeAttributes reads cluster values through service-account-authenticated API calls.
// Returned values are request-scoped and only become effective manifest attributes.
func inClusterRuntimeAttributes(log *slog.Logger) func(*http.Request, string) (map[string]string, error) {
	client, err := tlsClientWithCA("/var/run/secrets/kubernetes.io/serviceaccount/ca.crt")
	if err != nil {
		log.Warn("runtime attributes unavailable", "category", "runtime_client")
		return func(*http.Request, string) (map[string]string, error) {
			return nil, fmt.Errorf("runtime attributes unavailable")
		}
	}
	return newClusterRuntimeAttributes(
		fmt.Sprintf("https://%s", os.Getenv("KUBERNETES_SERVICE_HOST")+":"+os.Getenv("KUBERNETES_SERVICE_PORT")),
		func() (string, error) {
			value, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/token") //nolint:gosec
			return string(value), err
		},
		client,
		log,
	)
}

func newClusterRuntimeAttributes(apiHost string, token func() (string, error), client *http.Client, log *slog.Logger) func(*http.Request, string) (map[string]string, error) {
	return func(r *http.Request, subject string) (map[string]string, error) {
		fail := func(category string) (map[string]string, error) {
			if log != nil {
				log.Warn("runtime attributes unavailable", "attribute", category)
			}
			return nil, fmt.Errorf("runtime attributes unavailable")
		}
		if subject == "" {
			return fail("user")
		}
		if token == nil || client == nil {
			return fail("runtime_client")
		}
		serviceAccountToken, err := token()
		if err != nil || serviceAccountToken == "" {
			return fail("service_account_token")
		}

		var infrastructure struct {
			Status struct {
				APIServerURL string `json:"apiServerURL"`
			} `json:"status"`
		}
		if getClusterConfig(r.Context(), apiHost, serviceAccountToken, "/apis/config.openshift.io/v1/infrastructures/cluster", client, &infrastructure) != nil || infrastructure.Status.APIServerURL == "" {
			return fail("openshift_api_url")
		}

		var console struct {
			Status struct {
				ConsoleURL string `json:"consoleURL"`
			} `json:"status"`
		}
		if getClusterConfig(r.Context(), apiHost, serviceAccountToken, "/apis/config.openshift.io/v1/consoles/cluster", client, &console) != nil || console.Status.ConsoleURL == "" {
			return fail("openshift_console_url")
		}

		var ingress struct {
			Spec struct {
				Domain string `json:"domain"`
			} `json:"spec"`
		}
		if getClusterConfig(r.Context(), apiHost, serviceAccountToken, "/apis/config.openshift.io/v1/ingresses/cluster", client, &ingress) != nil || ingress.Spec.Domain == "" {
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

func getClusterConfig(ctx context.Context, apiHost, token, path string, client *http.Client, destination any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiHost+path, nil) //nolint:gosec
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req) //nolint:gosec
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("cluster configuration request status %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(destination)
}

// inClusterCatalogSource returns a function that reads the catalog ConfigMap from the cluster.
// In-cluster client configuration is loaded from the mounted service account token.
// ponytail: direct Kubernetes REST call without client-go to avoid the dependency weight.
func inClusterCatalogSource() func(ctx context.Context) (*corev1.ConfigMap, error) {
	namespace := os.Getenv("NAMESPACE")
	if namespace == "" {
		namespace = "interactive-labs-console-plugin"
	}
	cmName := "interactive-labs-catalog"

	// Read the service account token for in-cluster auth.
	tokenPath := "/var/run/secrets/kubernetes.io/serviceaccount/token" //nolint:gosec
	caPath := "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
	apiHost := fmt.Sprintf("https://%s", os.Getenv("KUBERNETES_SERVICE_HOST")+":"+os.Getenv("KUBERNETES_SERVICE_PORT"))

	return func(ctx context.Context) (*corev1.ConfigMap, error) {
		token, err := os.ReadFile(tokenPath)
		if err != nil {
			return nil, fmt.Errorf("read service account token: %w", err)
		}

		// ponytail: stdlib HTTP with CA cert — avoids k8s client-go transport overhead for one call.
		client, err := tlsClientWithCA(caPath)
		if err != nil {
			return nil, fmt.Errorf("create catalog client: %w", err)
		}

		url := fmt.Sprintf("%s/api/v1/namespaces/%s/configmaps/%s", apiHost, namespace, cmName)
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil) //nolint:gosec
		req.Header.Set("Authorization", "Bearer "+string(token))

		resp, err := client.Do(req) //nolint:gosec
		if err != nil {
			return nil, fmt.Errorf("catalog configmap GET: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("catalog configmap GET status %d", resp.StatusCode)
		}

		var cm corev1.ConfigMap
		if err := json.NewDecoder(resp.Body).Decode(&cm); err != nil {
			return nil, fmt.Errorf("decode catalog configmap: %w", err)
		}
		return &cm, nil
	}
}
