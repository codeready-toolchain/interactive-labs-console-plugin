package setup

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/strvals"
	"k8s.io/cli-runtime/pkg/genericclioptions"
)

// HelmManager abstracts Helm operations for testability.
type HelmManager interface {
	ReleaseStatus(ctx context.Context, releaseName, namespace, token string) (string, error)
	Install(ctx context.Context, opts HelmInstallOpts) error
	ReleaseValues(ctx context.Context, releaseName, namespace, token string, paths []string) (map[string]string, error)
	ListPods(ctx context.Context, token, namespace string) ([]PodSummary, error)
}

// PodSummary holds the fields needed for post-failure diagnostics.
type PodSummary struct {
	Name   string
	Phase  string
	Ready  bool
	Reason string
}

// HelmInstallOpts holds the parameters for a Helm install operation.
type HelmInstallOpts struct {
	ChartDir    string
	ReleaseName string
	Namespace   string
	Token       string
	Values      map[string]string
	Timeout     time.Duration
}

// HelmClient implements HelmManager using the Helm Go SDK.
type HelmClient struct {
	APIHost    string
	CAFile     string
	HTTPClient *http.Client
	Logger     *slog.Logger
}

func (c *HelmClient) newActionConfig(namespace, token string, debug bool) (*action.Configuration, error) {
	apiServer := c.APIHost
	caFile := c.CAFile
	ns := namespace
	tk := token

	configFlags := genericclioptions.NewConfigFlags(false)
	configFlags.APIServer = &apiServer
	configFlags.BearerToken = &tk
	configFlags.CAFile = &caFile
	configFlags.Namespace = &ns

	debugFn := func(_ string, _ ...interface{}) {}
	if debug && c.Logger != nil {
		debugFn = func(format string, v ...interface{}) {
			c.Logger.Info(fmt.Sprintf(format, v...), "source", "helm")
		}
	}

	actionConfig := new(action.Configuration)
	if err := actionConfig.Init(configFlags, namespace, "secret", debugFn); err != nil {
		return nil, fmt.Errorf("init helm action config: %w", err)
	}
	return actionConfig, nil
}

// ReleaseStatus returns the status of a Helm release (e.g. "deployed", "failed",
// "pending-install"). Returns empty string and nil error if the release is not found.
func (c *HelmClient) ReleaseStatus(_ context.Context, releaseName, namespace, token string) (string, error) {
	actionConfig, err := c.newActionConfig(namespace, token, false)
	if err != nil {
		return "", err
	}

	statusAction := action.NewStatus(actionConfig)
	rel, err := statusAction.Run(releaseName)
	if err != nil {
		if isReleaseNotFound(err) {
			return "", nil
		}
		return "", fmt.Errorf("helm status: %w", err)
	}
	return string(rel.Info.Status), nil
}

// Install deploys a Helm chart using helm upgrade --install semantics.
func (c *HelmClient) Install(ctx context.Context, opts HelmInstallOpts) error {
	actionConfig, err := c.newActionConfig(opts.Namespace, opts.Token, true)
	if err != nil {
		return err
	}

	chrt, err := loader.Load(opts.ChartDir)
	if err != nil {
		return fmt.Errorf("load chart from %s: %w", opts.ChartDir, err)
	}

	vals, err := BuildValues(opts.Values)
	if err != nil {
		return err
	}

	installAction := action.NewInstall(actionConfig)
	installAction.ReleaseName = opts.ReleaseName
	installAction.Namespace = opts.Namespace
	installAction.Wait = true
	installAction.Timeout = opts.Timeout

	_, err = installAction.RunWithContext(ctx, chrt, vals)
	if err != nil {
		return fmt.Errorf("helm install: %w", err)
	}
	return nil
}

// ReleaseValues retrieves values from a deployed Helm release (merging chart
// defaults with user-supplied overrides) and returns only those matching the
// given dot-separated paths.
func (c *HelmClient) ReleaseValues(_ context.Context, releaseName, namespace, token string, paths []string) (map[string]string, error) {
	actionConfig, err := c.newActionConfig(namespace, token, false)
	if err != nil {
		return nil, err
	}

	statusAction := action.NewStatus(actionConfig)
	rel, err := statusAction.Run(releaseName)
	if err != nil {
		return nil, fmt.Errorf("helm status: %w", err)
	}

	merged, err := chartutil.CoalesceValues(rel.Chart, rel.Config)
	if err != nil {
		return nil, fmt.Errorf("coalesce helm values: %w", err)
	}

	result := make(map[string]string, len(paths))
	for _, path := range paths {
		if val, ok := extractNestedValue(merged, path); ok {
			result[path] = val
		}
	}
	return result, nil
}

func extractNestedValue(m map[string]interface{}, dotPath string) (string, bool) {
	parts := strings.Split(dotPath, ".")
	current := m
	for i, part := range parts {
		val, ok := current[part]
		if !ok {
			return "", false
		}
		if i == len(parts)-1 {
			s, ok := val.(string) //nolint:govet
			return s, ok
		}
		next, ok := val.(map[string]interface{})
		if !ok {
			return "", false
		}
		current = next
	}
	return "", false
}

// ListPods returns a summary of all pods in the given namespace for diagnostics.
func (c *HelmClient) ListPods(ctx context.Context, token, namespace string) ([]PodSummary, error) {
	listURL := fmt.Sprintf("%s/api/v1/namespaces/%s/pods", c.APIHost, url.PathEscape(namespace))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, listURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list pods in %q: status %d", namespace, resp.StatusCode)
	}

	var list struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Status struct {
				Phase             string `json:"phase"`
				Reason            string `json:"reason,omitempty"`
				ContainerStatuses []struct {
					Ready bool `json:"ready"`
				} `json:"containerStatuses"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, err
	}

	pods := make([]PodSummary, 0, len(list.Items))
	for _, item := range list.Items {
		allReady := len(item.Status.ContainerStatuses) > 0
		for _, cs := range item.Status.ContainerStatuses {
			if !cs.Ready {
				allReady = false
				break
			}
		}
		pods = append(pods, PodSummary{
			Name:   item.Metadata.Name,
			Phase:  item.Status.Phase,
			Ready:  allReady,
			Reason: item.Status.Reason,
		})
	}
	return pods, nil
}

func isReleaseNotFound(err error) bool {
	return err != nil && strings.Contains(err.Error(), "not found")
}

// BuildValues converts a flat map of dot-separated keys to a nested map
// suitable for Helm values (e.g. {"namespaces.gitea": "ns-abc"} becomes
// {"namespaces": {"gitea": "ns-abc"}}).
func BuildValues(flat map[string]string) (map[string]interface{}, error) {
	vals := map[string]interface{}{}
	for k, v := range flat {
		if err := strvals.ParseInto(k+"="+v, vals); err != nil {
			return nil, fmt.Errorf("parse helm value %s=%s: %w", k, v, err)
		}
	}
	return vals, nil
}

// MapBindingsToValues collects provisioned namespace names from SpaceRequests
// and maps them to Helm values paths.
func MapBindingsToValues(
	ctx context.Context,
	client *SpaceRequestClient,
	token, namespace string,
	bindings []SchemaBinding,
) (map[string]string, error) {
	values := make(map[string]string, len(bindings))
	for _, b := range bindings {
		sr, err := client.GetSpaceRequest(ctx, token, namespace, b.Key)
		if err != nil {
			return nil, fmt.Errorf("get SpaceRequest %q: %w", b.Key, err)
		}
		if sr == nil {
			return nil, fmt.Errorf("SpaceRequest %q not found", b.Key)
		}
		if !sr.IsReady() {
			return nil, fmt.Errorf("SpaceRequest %q not ready", b.Key)
		}
		ns := sr.TargetNamespace()
		if ns == "" {
			return nil, fmt.Errorf("SpaceRequest %q has no namespace", b.Key)
		}
		values[b.Path] = ns
	}
	return values, nil
}

// HelmStatusLabel maps a Helm release status to the API status label
// used in the setup status response.
func HelmStatusLabel(status string) string {
	switch release.Status(status) {
	case release.StatusDeployed:
		return "deployed"
	case release.StatusFailed:
		return "failed"
	case release.StatusPendingInstall, release.StatusPendingUpgrade, release.StatusPendingRollback:
		return "installing"
	default:
		return "pending"
	}
}
