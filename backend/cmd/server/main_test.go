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
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	configv1 "github.com/openshift/api/config/v1"
	userv1 "github.com/openshift/api/user/v1"
	configclient "github.com/openshift/client-go/config/clientset/versioned"
	configfake "github.com/openshift/client-go/config/clientset/versioned/fake"
	userclient "github.com/openshift/client-go/user/clientset/versioned"
	userfake "github.com/openshift/client-go/user/clientset/versioned/fake"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
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

func TestRuntimeAttributes(t *testing.T) {
	t.Run("reads typed cluster config", func(t *testing.T) {
		client := typedConfigClient(configv1.InfrastructureStatus{APIServerURL: apiServerURL}, configv1.ConsoleStatus{ConsoleURL: consoleURL}, configv1.IngressSpec{Domain: ingressDomain})
		var configClient configclient.Interface = client

		attributes, err := newClusterRuntimeAttributes(configClient, nil)(httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil), "alice")

		require.NoError(t, err)
		require.Equal(t, map[string]string{
			"user":                             "alice",
			"openshift-api-url":                apiServerURL,
			"openshift-console-url":            consoleURL,
			"openshift-cluster-ingress-domain": ingressDomain,
		}, attributes)
	})

	t.Run("preserves caller context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		client := typedConfigClient(configv1.InfrastructureStatus{APIServerURL: apiServerURL}, configv1.ConsoleStatus{ConsoleURL: consoleURL}, configv1.IngressSpec{Domain: ingressDomain})
		var configClient configclient.Interface = client

		attributes, err := newClusterRuntimeAttributes(configClient, nil)(httptest.NewRequestWithContext(ctx, http.MethodGet, "/", nil), "alice")

		require.ErrorIs(t, err, context.Canceled)
		require.Nil(t, attributes)
		require.Empty(t, client.Actions())
	})

	t.Run("failures are atomic and safe to log", func(t *testing.T) {
		for _, tc := range []struct {
			name, attribute string
			client          *configfake.Clientset
		}{
			{
				name:      "missing API URL",
				attribute: "openshift-api-url",
				client:    typedConfigClient(configv1.InfrastructureStatus{}, configv1.ConsoleStatus{ConsoleURL: consoleURL}, configv1.IngressSpec{Domain: ingressDomain}),
			},
			{
				name:      "missing console URL",
				attribute: "openshift-console-url",
				client:    typedConfigClient(configv1.InfrastructureStatus{APIServerURL: apiServerURL}, configv1.ConsoleStatus{}, configv1.IngressSpec{Domain: ingressDomain}),
			},
			{
				name:      "missing ingress domain",
				attribute: "openshift-cluster-ingress-domain",
				client:    typedConfigClient(configv1.InfrastructureStatus{APIServerURL: apiServerURL}, configv1.ConsoleStatus{ConsoleURL: consoleURL}, configv1.IngressSpec{}),
			},
			{
				name:      "typed client error",
				attribute: "openshift-console-url",
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
	})
}

func TestClusterCatalogSource(t *testing.T) {
	t.Run("gets typed config map", func(t *testing.T) {
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
	})

	t.Run("preserves caller context and fails safely", func(t *testing.T) {
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
	})
}

func TestClusterIdentityResolver(t *testing.T) {
	t.Run("gets current user from per request typed client", func(t *testing.T) {
		const (
			directToken       = "forwarded-token"
			impersonatedToken = "next-forwarded-token"
			directUser        = "alice"
			impersonatedUser  = "admin-alice"
		)
		var logs bytes.Buffer
		previousLogger := slog.Default()
		slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
		t.Cleanup(func() { slog.SetDefault(previousLogger) })
		var factoryImpersonateUsers, factoryTokens []string
		var userClients []*userfake.Clientset
		resolver := newClusterIdentityResolver(func(receivedImpersonateUser, receivedToken string) (userclient.Interface, error) {
			factoryImpersonateUsers = append(factoryImpersonateUsers, receivedImpersonateUser)
			factoryTokens = append(factoryTokens, receivedToken)
			userName := directUser
			if receivedImpersonateUser != "" {
				userName = receivedImpersonateUser
			}
			client := userfake.NewSimpleClientset()
			client.PrependReactor("get", "users", func(k8stesting.Action) (bool, runtime.Object, error) {
				return true, &userv1.User{ObjectMeta: metav1.ObjectMeta{Name: userName}}, nil
			})
			userClients = append(userClients, client)
			return client, nil
		})

		directSubject, err := resolver(context.Background(), "", directToken)
		impersonatedSubject, impersonatedErr := resolver(context.Background(), impersonatedUser, impersonatedToken)

		require.NoError(t, err)
		require.NoError(t, impersonatedErr)
		require.Equal(t, directUser, directSubject)
		require.Equal(t, impersonatedUser, impersonatedSubject)
		require.Equal(t, []string{"", impersonatedUser}, factoryImpersonateUsers)
		require.Equal(t, []string{directToken, impersonatedToken}, factoryTokens)
		require.NotContains(t, logs.String(), directToken)
		require.NotContains(t, logs.String(), impersonatedToken)
		require.Len(t, userClients, 2)
		for _, client := range userClients {
			require.Len(t, client.Actions(), 1)
			action := client.Actions()[0]
			require.Equal(t, "get", action.GetVerb())
			require.Equal(t, "users", action.GetResource().Resource)
			require.Equal(t, "~", action.(k8stesting.GetAction).GetName())
		}
	})

	t.Run("preserves caller context and fails safely", func(t *testing.T) {
		const token = "forwarded-token"
		const secret = "identity credential leak"
		client := userfake.NewSimpleClientset()
		client.PrependReactor("get", "users", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, errors.New(secret)
		})
		resolver := newClusterIdentityResolver(func(string, string) (userclient.Interface, error) { return client, nil })

		subject, err := resolver(context.Background(), "", token)

		require.Empty(t, subject)
		require.EqualError(t, err, "identity unavailable")
		require.NotContains(t, err.Error(), token)
		require.NotContains(t, err.Error(), secret)

		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		factoryCalls := 0
		resolver = newClusterIdentityResolver(func(string, string) (userclient.Interface, error) {
			factoryCalls++
			return userfake.NewSimpleClientset(), nil
		})
		subject, err = resolver(ctx, "", token)
		require.ErrorIs(t, err, context.Canceled)
		require.Empty(t, subject)
		require.Zero(t, factoryCalls)
	})
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
		Authenticate: func(_ context.Context, _ string, _ string) (string, error) {
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
			return &bundle.Result{Manifest: &manifest.LabManifest{Metadata: manifest.Metadata{Title: "Lab"}}, Modules: []bundle.Module{{ID: "010-intro", Pages: []string{"intro.adoc"}}}}, nil
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

func TestRefreshCatalogEntriesParsesConfigMap(t *testing.T) {
	source := loadCatalogEntries(func(context.Context) (*corev1.ConfigMap, error) {
		return &corev1.ConfigMap{Data: map[string]string{"catalog.yaml": `- repository: https://github.com/redhat/lab
  branch: main
  labPath: one
- repository: https://github.com/redhat/lab
  branch: main
  labPath: two`}}, nil
	})

	entries, err := source(context.Background())

	require.NoError(t, err)
	require.Len(t, entries, 2)
	for i, labPath := range []string{"one", "two"} {
		assert.NotEmpty(t, entries[i].CatalogRef)
		assert.Equal(t, "https://github.com/redhat/lab", entries[i].Repository)
		assert.Equal(t, "main", entries[i].Branch)
		assert.Equal(t, labPath, entries[i].LabPath)
	}
}

func TestCatalogWatcher(t *testing.T) {
	// given
	const namespace = "configured-catalog-namespace"
	wait := func(t *testing.T, signal <-chan struct{}) {
		t.Helper()
		select {
		case <-signal:
		case <-time.After(10 * time.Second):
			require.FailNow(t, "timed out waiting for catalog watcher")
		}
	}
	start := func(t *testing.T, client kubernetes.Interface) (context.CancelFunc, <-chan struct{}, <-chan struct{}) {
		t.Helper()
		ctx, cancel := context.WithCancel(context.Background())
		notifications, done := make(chan struct{}, 1), make(chan struct{})
		go func() {
			defer close(done)
			// when
			err := watchCatalog(ctx, client, namespace, func() {
				select {
				case notifications <- struct{}{}:
				default:
				}
			})

			// then
			assert.NoError(t, err)
		}()
		t.Cleanup(func() {
			cancel()
			wait(t, done)
		})
		return cancel, notifications, done
	}

	catalogConfigMap := func() *corev1.ConfigMap {
		return &corev1.ConfigMap{
			Namespace: namespace, Name: "interactive-labs-catalog", ResourceVersion: "1",
			Data: map[string]string{"catalog.yaml": "invalid data is only a signal"},
		}
	}
	watchConfigMaps := func(client *k8sfake.Clientset) (*watch.RaceFreeFakeWatcher, <-chan struct{}) {
		events := watch.NewRaceFreeFake()
		watchStarted := make(chan struct{}, 1)
		client.PrependWatchReactor("configmaps", func(k8stesting.Action) (bool, watch.Interface, error) {
			watchStarted <- struct{}{}
			return true, events, nil
		})
		return events, watchStarted
	}
	assertStopped := func(t *testing.T, client *k8sfake.Clientset, events *watch.RaceFreeFakeWatcher, done <-chan struct{}) {
		t.Helper()
		wait(t, done)
		assert.True(t, events.IsStopped(), "cancellation must stop the active watch before returning")
		actions := client.Actions()
		require.Len(t, actions, 2)

		list := actions[0]
		require.Equal(t, "list", list.GetVerb())
		assert.Equal(t, namespace, list.GetNamespace())
		assert.Equal(t, "configmaps", list.GetResource().Resource)
		assert.Equal(t, "metadata.name=interactive-labs-catalog", list.(k8stesting.ListAction).GetListRestrictions().Fields.String())

		watchAction := actions[1]
		require.Equal(t, "watch", watchAction.GetVerb())
		assert.Equal(t, namespace, watchAction.GetNamespace())
		assert.Equal(t, "configmaps", watchAction.GetResource().Resource)
		assert.Equal(t, "metadata.name=interactive-labs-catalog", watchAction.(k8stesting.WatchAction).GetWatchRestrictions().Fields.String())
	}

	t.Run("add (initial list)", func(t *testing.T) {
		// given
		cm := catalogConfigMap()
		client := k8sfake.NewSimpleClientset(cm)
		events, watchStarted := watchConfigMaps(client)

		// when
		cancel, notifications, done := start(t, client)
		wait(t, watchStarted)

		// then
		wait(t, notifications)

		// when
		cancel()

		// then
		assertStopped(t, client, events, done)
	})

	t.Run("update", func(t *testing.T) {
		// given
		cm := catalogConfigMap()
		client := k8sfake.NewSimpleClientset(cm)
		events, watchStarted := watchConfigMaps(client)

		// when
		cancel, notifications, done := start(t, client)
		wait(t, watchStarted)

		// then
		wait(t, notifications)

		// when
		updated := cm.DeepCopy()
		updated.ResourceVersion = "2"
		events.Modify(updated)

		// then
		wait(t, notifications)

		// when
		cancel()

		// then
		assertStopped(t, client, events, done)
	})

	t.Run("delete", func(t *testing.T) {
		// given
		cm := catalogConfigMap()
		client := k8sfake.NewSimpleClientset(cm)
		events, watchStarted := watchConfigMaps(client)

		// when
		cancel, notifications, done := start(t, client)
		wait(t, watchStarted)

		// then
		wait(t, notifications)

		// when
		events.Delete(cm)

		// then
		wait(t, notifications)

		// when
		cancel()

		// then
		assertStopped(t, client, events, done)
	})

	t.Run("creation (watch add)", func(t *testing.T) {
		// given
		cm := catalogConfigMap()
		client := k8sfake.NewSimpleClientset()
		events, watchStarted := watchConfigMaps(client)

		// when
		cancel, notifications, done := start(t, client)
		wait(t, watchStarted)
		events.Add(cm)

		// then
		wait(t, notifications)

		// when
		cancel()

		// then
		assertStopped(t, client, events, done)
	})

	t.Run("ignores unrelated names and namespaces from permissive watch", func(t *testing.T) {
		// given
		for _, identity := range []struct{ namespace, name string }{
			{namespace, "another-configmap"},
			{"another-namespace", "interactive-labs-catalog"},
		} {
			t.Run(identity.namespace+"/"+identity.name, func(t *testing.T) {
				// given
				client := k8sfake.NewSimpleClientset()
				events := watch.NewRaceFreeFake()
				watchStarted := make(chan struct{}, 1)
				client.PrependWatchReactor("configmaps", func(k8stesting.Action) (bool, watch.Interface, error) {
					watchStarted <- struct{}{}
					return true, events, nil
				})
				cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
					Namespace: identity.namespace, Name: identity.name, ResourceVersion: "1",
				}}
				updated := cm.DeepCopy()
				updated.ResourceVersion = "2"

				// when
				_, notifications, _ := start(t, client)

				// then
				wait(t, watchStarted)

				for _, event := range []struct {
					name string
					send func()
				}{
					{"add", func() { events.Add(cm) }},
					{"update", func() { events.Modify(updated) }},
					{"delete", func() { events.Delete(updated) }},
				} {
					// when
					event.send()

					// then
					assert.Never(t, func() bool { return len(notifications) != 0 }, 150*time.Millisecond, 10*time.Millisecond,
						"unrelated %s must not signal refresh", event.name)
					// Discard any unexpected signal before checking the next event.
					select {
					case <-notifications:
					default:
					}
				}

				// when
				events.Add(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
					Namespace: namespace, Name: "interactive-labs-catalog", ResourceVersion: "3",
				}})

				// then
				wait(t, notifications)
			})
		}
	})

	t.Run("closed watch reconnects and later event signals", func(t *testing.T) {
		// given
		client := k8sfake.NewSimpleClientset()
		watches := make(chan *watch.RaceFreeFakeWatcher, 2)
		client.PrependWatchReactor("configmaps", func(k8stesting.Action) (bool, watch.Interface, error) {
			events := watch.NewRaceFreeFake()
			watches <- events
			return true, events, nil
		})
		nextWatch := func() *watch.RaceFreeFakeWatcher {
			select {
			case events := <-watches:
				return events
			case <-time.After(10 * time.Second):
				require.FailNow(t, "timed out waiting for watch reconnect")
				return nil
			}
		}

		// when
		cancel, notifications, done := start(t, client)
		first := nextWatch()
		first.Stop()

		// then
		reconnected := nextWatch()

		// when
		reconnected.Add(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace, Name: "interactive-labs-catalog", ResourceVersion: "2",
		}})

		// then
		wait(t, notifications)

		// when
		cancel()

		// then
		wait(t, done)
		assert.True(t, reconnected.IsStopped())
		for _, action := range client.Actions() {
			assert.Equal(t, namespace, action.GetNamespace())
			if action.GetVerb() == "watch" {
				assert.Equal(t, "metadata.name=interactive-labs-catalog", action.(k8stesting.WatchAction).GetWatchRestrictions().Fields.String())
			} else {
				require.Equal(t, "list", action.GetVerb())
				assert.Equal(t, "metadata.name=interactive-labs-catalog", action.(k8stesting.ListAction).GetListRestrictions().Fields.String())
			}
		}
	})

	t.Run("relist signals deletion missed while disconnected", func(t *testing.T) {
		// given
		cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace, Name: "interactive-labs-catalog", ResourceVersion: "1",
		}}
		client := k8sfake.NewSimpleClientset(cm)
		events := watch.NewRaceFreeFake()
		watchStarted := make(chan struct{}, 1)
		firstWatch := true
		client.PrependWatchReactor("configmaps", func(k8stesting.Action) (bool, watch.Interface, error) {
			if !firstWatch {
				return true, watch.NewRaceFreeFake(), nil
			}
			firstWatch = false
			watchStarted <- struct{}{}
			return true, events, nil
		})

		// when
		_, notifications, _ := start(t, client)
		wait(t, watchStarted)

		// then
		wait(t, notifications)

		// when
		// Remove without delivering a watch event.
		err := client.Tracker().Delete(corev1.SchemeGroupVersion.WithResource("configmaps"), namespace, cm.Name)

		// then
		require.NoError(t, err)

		// when
		events.Error(&metav1.Status{Code: http.StatusGone, Reason: metav1.StatusReasonExpired})

		// then
		wait(t, notifications)
	})

	t.Run("nil client disables watcher and catalog source fails safely", func(t *testing.T) {
		// given
		var client kubernetes.Interface

		// when
		_, notifications, done := start(t, client)

		// then
		wait(t, done)
		assert.Empty(t, notifications)

		// when
		cm, err := newClusterCatalogSource(client, namespace)(context.Background())

		// then
		require.EqualError(t, err, "catalog unavailable")
		assert.Nil(t, cm)
	})
}

func TestCatalogWatcherRole(t *testing.T) {
	// given
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "deploy", "base", "role.yaml"))
	require.NoError(t, err)
	var role rbacv1.Role

	// when
	err = yaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096).Decode(&role)

	// then
	require.NoError(t, err)
	assert.Equal(t, "interactive-labs-console-plugin", role.Namespace)
	require.NotEmpty(t, role.Rules)
	assert.Equal(t, rbacv1.PolicyRule{
		APIGroups: []string{""}, Resources: []string{"configmaps"},
		ResourceNames: []string{"interactive-labs-catalog"}, Verbs: []string{"get", "list", "watch"},
	}, role.Rules[0])
}

func TestRefreshCatalogEntriesReloadsCurrentConfigMap(t *testing.T) {
	// given
	const namespace = "configured-catalog-namespace"
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
		Namespace: namespace, Name: "interactive-labs-catalog",
	}, Data: map[string]string{"catalog.yaml": `- repository: https://github.com/redhat/lab
  branch: main
  labPath: one`}}
	client := k8sfake.NewSimpleClientset(cm)
	load := loadCatalogEntries(newClusterCatalogSource(client, namespace))

	// when
	entries, err := load(context.Background())

	// then
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "one", entries[0].LabPath)

	// when
	cm.Data["catalog.yaml"] = `- repository: https://github.com/redhat/lab
  branch: main
  labPath: two`
	_, err = client.CoreV1().ConfigMaps(namespace).Update(context.Background(), cm, metav1.UpdateOptions{})

	// then
	require.NoError(t, err)

	// when
	entries, err = load(context.Background())

	// then
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "two", entries[0].LabPath)

	// when
	cm.Data["catalog.yaml"] = "not a catalog sequence"
	_, err = client.CoreV1().ConfigMaps(namespace).Update(context.Background(), cm, metav1.UpdateOptions{})

	// then
	require.NoError(t, err)

	// when
	entries, err = load(context.Background())

	// then
	require.EqualError(t, err, "catalog configuration error")
	assert.Nil(t, entries)

	// when
	err = client.CoreV1().ConfigMaps(namespace).Delete(context.Background(), cm.Name, metav1.DeleteOptions{})

	// then
	require.NoError(t, err)

	// when
	entries, err = load(context.Background())

	// then
	require.EqualError(t, err, "catalog unavailable")
	assert.Nil(t, entries)

	gets := 0
	for _, action := range client.Actions() {
		if action.GetVerb() == "get" {
			gets++
			assert.Equal(t, namespace, action.GetNamespace())
			assert.Equal(t, "interactive-labs-catalog", action.(k8stesting.GetAction).GetName())
		}
	}
	assert.Equal(t, 4, gets, "every sweep must freshly read and validate the current ConfigMap")
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
}
