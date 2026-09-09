package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	configv1 "github.com/openshift/api/config/v1"
	userv1 "github.com/openshift/api/user/v1"
	configclient "github.com/openshift/client-go/config/clientset/versioned"
	configfake "github.com/openshift/client-go/config/clientset/versioned/fake"
	userclient "github.com/openshift/client-go/user/clientset/versioned"
	userfake "github.com/openshift/client-go/user/clientset/versioned/fake"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/codeready-toolchain/interactive-labs-console-plugin/backend/internal/bundle"
	"github.com/codeready-toolchain/interactive-labs-console-plugin/backend/internal/handler"
	"github.com/codeready-toolchain/interactive-labs-console-plugin/backend/internal/manifest"
)

const (
	apiServerURL  = "https://api.example.test:6443"
	consoleURL    = "https://console.example.test"
	ingressDomain = "apps.example.test"
)

func TestRuntimeAttributesReadsTypedClusterConfig(t *testing.T) {
	client := typedConfigClient(configv1.InfrastructureStatus{APIServerURL: apiServerURL}, configv1.ConsoleStatus{ConsoleURL: consoleURL}, configv1.IngressSpec{Domain: ingressDomain})
	var configClient configclient.Interface = client

	attributes, err := newClusterRuntimeAttributes(configClient, nil)(httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil), "alice")

	require.NoError(t, err)
	require.Equal(t, map[string]string{
		"user":                             "alice",
		"openshift_api_url":                apiServerURL,
		"openshift_console_url":            consoleURL,
		"openshift_cluster_ingress_domain": ingressDomain,
	}, attributes)
}

func TestRuntimeAttributesPreservesCallerContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := typedConfigClient(configv1.InfrastructureStatus{APIServerURL: apiServerURL}, configv1.ConsoleStatus{ConsoleURL: consoleURL}, configv1.IngressSpec{Domain: ingressDomain})
	var configClient configclient.Interface = client

	attributes, err := newClusterRuntimeAttributes(configClient, nil)(httptest.NewRequestWithContext(ctx, http.MethodGet, "/", nil), "alice")

	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, attributes)
	require.Empty(t, client.Actions())
}

func TestRuntimeAttributeFailuresAreAtomicAndSafeToLog(t *testing.T) {
	for _, tc := range []struct {
		name, attribute string
		client          *configfake.Clientset
	}{
		{
			name:      "missing API URL",
			attribute: "openshift_api_url",
			client:    typedConfigClient(configv1.InfrastructureStatus{}, configv1.ConsoleStatus{ConsoleURL: consoleURL}, configv1.IngressSpec{Domain: ingressDomain}),
		},
		{
			name:      "missing console URL",
			attribute: "openshift_console_url",
			client:    typedConfigClient(configv1.InfrastructureStatus{APIServerURL: apiServerURL}, configv1.ConsoleStatus{}, configv1.IngressSpec{Domain: ingressDomain}),
		},
		{
			name:      "missing ingress domain",
			attribute: "openshift_cluster_ingress_domain",
			client:    typedConfigClient(configv1.InfrastructureStatus{APIServerURL: apiServerURL}, configv1.ConsoleStatus{ConsoleURL: consoleURL}, configv1.IngressSpec{}),
		},
		{
			name:      "typed client error",
			attribute: "openshift_console_url",
			client: func() *configfake.Clientset {
				client := typedConfigClient(configv1.InfrastructureStatus{APIServerURL: apiServerURL}, configv1.ConsoleStatus{ConsoleURL: consoleURL}, configv1.IngressSpec{Domain: ingressDomain})
				client.PrependReactor("get", "consoles", func(k8stesting.Action) (bool, runtime.Object, error) {
					return true, nil, errors.New("console credential leak")
				})
				return client
			}(),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			var configClient configclient.Interface = tc.client

			attributes, err := newClusterRuntimeAttributes(configClient, slog.New(slog.NewTextHandler(&logs, nil)))(httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil), "alice@example.test")

			require.Error(t, err)
			require.Nil(t, attributes)
			require.Contains(t, logs.String(), "attribute="+tc.attribute)
			require.NotContains(t, logs.String(), "alice@example.test")
			require.NotContains(t, logs.String(), apiServerURL)
			require.NotContains(t, logs.String(), consoleURL)
			require.NotContains(t, logs.String(), ingressDomain)
			require.NotContains(t, logs.String(), "console credential leak")
		})
	}
}

func TestClusterCatalogSourceGetsTypedConfigMap(t *testing.T) {
	const namespace = "interactive-labs-console-plugin"
	client := k8sfake.NewSimpleClientset(&corev1.ConfigMap{
		Namespace: namespace, Name: "interactive-labs-catalog",
		Data: map[string]string{"catalog.yaml": "labs: []"},
	})

	configMap, err := newClusterCatalogSource(client, namespace)(context.Background())

	require.NoError(t, err)
	require.Equal(t, "labs: []", configMap.Data["catalog.yaml"])
	require.Len(t, client.Actions(), 1)
	action := client.Actions()[0]
	require.Equal(t, "get", action.GetVerb())
	require.Equal(t, "configmaps", action.GetResource().Resource)
	require.Equal(t, namespace, action.GetNamespace())
}

func TestClusterCatalogSourcePreservesCallerContextAndFailsSafely(t *testing.T) {
	const secret = "catalog credential leak" // #nosec G101
	client := k8sfake.NewSimpleClientset()
	client.PrependReactor("get", "configmaps", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New(secret)
	})

	configMap, err := newClusterCatalogSource(client, "ns")(context.Background())

	require.Nil(t, configMap)
	require.EqualError(t, err, "catalog unavailable")
	require.NotContains(t, err.Error(), secret)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cancelledClient := k8sfake.NewSimpleClientset()
	configMap, err = newClusterCatalogSource(cancelledClient, "ns")(ctx)
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, configMap)
	require.Empty(t, cancelledClient.Actions())
}

func TestClusterIdentityResolverGetsCurrentUserFromPerRequestTypedClient(t *testing.T) {
	const (
		token     = "forwarded-token"
		nextToken = "next-forwarded-token"
	)
	var logs bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })
	var factoryTokens []string
	var userClients []*userfake.Clientset
	resolver := newClusterIdentityResolver(func(receivedToken string) (userclient.Interface, error) {
		factoryTokens = append(factoryTokens, receivedToken)
		client := userfake.NewSimpleClientset()
		client.PrependReactor("get", "users", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, &userv1.User{ObjectMeta: metav1.ObjectMeta{Name: "alice"}}, nil
		})
		userClients = append(userClients, client)
		return client, nil
	})

	subject, err := resolver(context.Background(), token)
	nextSubject, nextErr := resolver(context.Background(), nextToken)

	require.NoError(t, err)
	require.NoError(t, nextErr)
	require.Equal(t, "alice", subject)
	require.Equal(t, "alice", nextSubject)
	require.Equal(t, []string{token, nextToken}, factoryTokens)
	require.NotContains(t, logs.String(), token)
	require.NotContains(t, logs.String(), nextToken)
	require.Len(t, userClients, 2)
	for _, client := range userClients {
		require.Len(t, client.Actions(), 1)
		action := client.Actions()[0]
		require.Equal(t, "get", action.GetVerb())
		require.Equal(t, "users", action.GetResource().Resource)
		require.Equal(t, "~", action.(k8stesting.GetAction).GetName())
	}
}

func TestClusterIdentityResolverPreservesCallerContextAndFailsSafely(t *testing.T) {
	const token = "forwarded-token"
	const secret = "identity credential leak"
	client := userfake.NewSimpleClientset()
	client.PrependReactor("get", "users", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New(secret)
	})
	resolver := newClusterIdentityResolver(func(string) (userclient.Interface, error) { return client, nil })

	subject, err := resolver(context.Background(), token)

	require.Empty(t, subject)
	require.EqualError(t, err, "identity unavailable")
	require.NotContains(t, err.Error(), token)
	require.NotContains(t, err.Error(), secret)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	factoryCalls := 0
	resolver = newClusterIdentityResolver(func(string) (userclient.Interface, error) {
		factoryCalls++
		return userfake.NewSimpleClientset(), nil
	})
	subject, err = resolver(ctx, token)
	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, subject)
	require.Zero(t, factoryCalls)
}

func typedConfigClient(infrastructure configv1.InfrastructureStatus, console configv1.ConsoleStatus, ingress configv1.IngressSpec) *configfake.Clientset {
	return configfake.NewSimpleClientset(
		&configv1.Infrastructure{ObjectMeta: metav1.ObjectMeta{Name: "cluster"}, Status: infrastructure},
		&configv1.Console{ObjectMeta: metav1.ObjectMeta{Name: "cluster"}, Status: console},
		&configv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: "cluster"}, Spec: ingress},
	)
}

func TestPluginHandlerServesBuiltAssets(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "plugin-manifest.json"), `{"name":"interactive-labs"}`)

	rw := httptest.NewRecorder()
	pluginHandler(dir).ServeHTTP(rw, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/plugin-manifest.json", nil))

	require.Equal(t, http.StatusOK, rw.Code)
	require.Contains(t, rw.Body.String(), "interactive-labs")
}

func TestTokenNotLogged(t *testing.T) {
	var logs bytes.Buffer
	router := handler.NewRouter(handler.Config{
		CatalogSource: func(_ context.Context) (*corev1.ConfigMap, error) {
			return &corev1.ConfigMap{Data: map[string]string{"catalog.yaml": `- displayName: Lab
  repository: https://github.com/redhat/lab
  branch: main
  labPath: lab`}}, nil
		},
		Logger: slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Authenticate: func(_ context.Context, _ string) (string, error) {
			return "alice", nil
		},
	})
	token := "eyJhbGciOiJub25lIn0.eyJzdWIiOiJhbGljZSJ9." //nolint:gosec
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1alpha1/labs/lab/setup", strings.NewReader(`{"namespaceBindings":{"releaseNamespace":"labs"}}`))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(httptest.NewRecorder(), req)

	require.NotContains(t, logs.String(), token)
	require.NotContains(t, logs.String(), "Bearer")
	require.Contains(t, logs.String(), "catalogRef=lab")
}

func TestCombinedHandler(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "plugin-manifest.json"), `{"name":"interactive-labs"}`)

	cfg := handler.Config{
		CatalogSource: func(_ context.Context) (*corev1.ConfigMap, error) {
			return &corev1.ConfigMap{Data: map[string]string{"catalog.yaml": `- displayName: Lab
  repository: https://github.com/redhat/lab
  branch: main
  labPath: lab`}}, nil
		},
		BundleLoader: func(context.Context, string, string, string) (*bundle.Result, error) {
			return &bundle.Result{Manifest: &manifest.LabManifest{Metadata: manifest.Metadata{Name: "lab", DisplayName: "Lab"}}, Pages: []string{"intro.adoc"}}, nil
		},
		Logger: slog.New(slog.NewTextHandler(os.Stderr, nil)),
	}
	h := combinedHandler(handler.NewRouter(cfg), dir)

	t.Run("asset served", func(t *testing.T) {
		rw := httptest.NewRecorder()
		h.ServeHTTP(rw, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/plugin-manifest.json", nil))
		require.Equal(t, http.StatusOK, rw.Code)
	})

	t.Run("catalog reachable", func(t *testing.T) {
		rw := httptest.NewRecorder()
		h.ServeHTTP(rw, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1alpha1/catalog", nil))
		require.Equal(t, http.StatusOK, rw.Code)
		require.Contains(t, rw.Header().Get("Content-Type"), "application/json")
	})
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
}
