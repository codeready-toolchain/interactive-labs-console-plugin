package setup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/storage/driver"
	"helm.sh/helm/v3/pkg/strvals"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/kubernetes"
)

// HelmManager abstracts Helm operations for testability.
type HelmManager interface {
	ReleaseStatus(ctx context.Context, releaseName, namespace, token string) (string, error)
	Install(ctx context.Context, opts HelmInstallOpts) error
	ListLabReleases(ctx context.Context, namespace, token string, srs []SpaceRequestResult) ([]LabRelease, error)
	UninstallLabRelease(ctx context.Context, namespace, token string, target LabRelease, srs []SpaceRequestResult) error
}

// LabRelease carries a discovered snapshot. Config is request-local, excluded
// from JSON, and removed from inventory; only requested generated paths may leave setup.
type LabRelease struct {
	Name           string                 `json:"name"`
	Namespace      string                 `json:"-"`
	CatalogRef     string                 `json:"catalogRef"`
	Status         string                 `json:"status"`
	Revision       int                    `json:"-"`
	UID            string                 `json:"-"`
	CleanupBlocked bool                   `json:"cleanupBlocked,omitempty"`
	BlockedReason  BlockedReason          `json:"-"`
	Config         map[string]interface{} `json:"-"`
}

func (r LabRelease) hasStorageIdentity(namespace string) bool {
	return r.Name != "" && r.Namespace == namespace && r.UID != ""
}

// sameSummary preserves mutation/preflight guards without comparing secret values.
func (r LabRelease) sameSummary(other LabRelease) bool {
	return r.Name == other.Name && r.Namespace == other.Namespace && r.CatalogRef == other.CatalogRef &&
		r.Status == other.Status && r.Revision == other.Revision && r.UID == other.UID &&
		r.CleanupBlocked == other.CleanupBlocked && r.BlockedReason == other.BlockedReason
}

// HelmInstallOpts holds the parameters for a Helm install operation.
type HelmInstallOpts struct {
	ChartDir    string
	ReleaseName string
	Namespace   string
	Token       string
	Values      map[string]string
	Labels      map[string]string
	Timeout     time.Duration
}

// HelmClient implements HelmManager using the Helm Go SDK.
type HelmClient struct {
	APIHost string
	CAFile  string
	Logger  *slog.Logger
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
	// per-api-call timeout
	timeout := "30s"
	configFlags.Timeout = &timeout

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
	installAction.Labels = opts.Labels
	installAction.Namespace = opts.Namespace
	installAction.Wait = true
	installAction.Timeout = opts.Timeout

	// check for context timeout or cancel before we start the operation.
	if err = ctx.Err(); err != nil {
		return err
	}
	// RunWithContext can return while Helm's internal installer keeps mutating.
	// Wait for the installer to finish before releasing the operation reservation.
	_, err = installAction.Run(chrt, vals)
	if err != nil {
		return fmt.Errorf("helm install: %w", err)
	}
	return nil
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

func isReleaseNotFound(err error) bool {
	return errors.Is(err, driver.ErrReleaseNotFound)
}

func (c *HelmClient) ListLabReleases(ctx context.Context, namespace, token string, srs []SpaceRequestResult) ([]LabRelease, error) {
	cfg, err := c.newActionConfig(namespace, token, false)
	if err != nil {
		return nil, err
	}
	list := action.NewList(cfg)
	list.StateMask = action.ListAll
	list.Limit = 0
	list.Selector = CatalogRefLabelKey
	releases, err := list.Run()
	if err != nil {
		return nil, fmt.Errorf("list lab releases: %w", err)
	}
	restConfig, err := cfg.RESTClientGetter.ToRESTConfig()
	if err != nil {
		return nil, err
	}
	client, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, err
	}
	out := make([]LabRelease, 0, len(releases))
	for _, rel := range releases {
		ref, err := CatalogRefFromLabel(rel.Labels[CatalogRefLabelKey])
		if err != nil || rel.Name == "" || rel.Namespace != namespace || rel.Info == nil {
			return nil, errors.New("invalid lab release identity")
		}
		secret, err := client.CoreV1().Secrets(namespace).Get(ctx, fmt.Sprintf("sh.helm.release.v1.%s.v%d", rel.Name, rel.Version), metav1.GetOptions{})
		if err != nil || secret == nil || secret.UID == "" {
			return nil, errors.New("lab release storage identity unavailable")
		}
		reason := releaseCleanupBlockedReason(rel, verifiedHookNamespaces(namespace, ref, srs))
		out = append(out, LabRelease{
			Name: rel.Name, Namespace: namespace, CatalogRef: ref,
			Status: string(rel.Info.Status), Revision: rel.Version, UID: string(secret.UID),
			CleanupBlocked: reason != "", BlockedReason: reason, Config: rel.Config,
		})
	}
	return out, nil
}

// Only identity-marked, ready, dedicated SR targets can contain cleanup hooks.
func verifiedHookNamespaces(home, ref string, srs []SpaceRequestResult) map[string]bool {
	label, err := CatalogRefLabel(ref)
	allowed := map[string]bool{}
	if err != nil {
		return allowed
	}
	for _, sr := range srs {
		namespace := sr.readyNamespace()
		if sr.Name != "" && sr.Namespace == home && sr.UID != "" &&
			sr.Labels[CatalogRefLabelKey] == label && sr.Annotations[BindingKeyAnnotation] != "" &&
			!sr.IsFailed() && namespace != "" && namespace != home {
			allowed[namespace] = true
		}
	}
	// A target also referenced by another lab is not dedicated to this release.
	for _, sr := range srs {
		if sr.Labels[CatalogRefLabelKey] != label {
			delete(allowed, sr.TargetNamespace())
		}
	}
	return allowed
}

func releaseCleanupBlockedReason(rel *release.Release, namespaces map[string]bool) BlockedReason {
	if reason := manifestCleanupBlockedReason(rel.Manifest, false, nil); reason != "" {
		return reason
	}
	for _, hook := range rel.Hooks {
		if hook == nil {
			return BlockedHelmHook
		}
		if reason := manifestCleanupBlockedReason(hook.Manifest, true, namespaces); reason != "" {
			return reason
		}
	}
	return ""
}

func manifestCleanupBlockedReason(manifest string, hook bool, namespaces map[string]bool) BlockedReason {
	// yaml.v3 rejects duplicate keys; JSON decoding then enforces field types.
	decoder := yaml.NewDecoder(strings.NewReader(manifest))
	resources := 0
	for {
		var document map[string]any
		err := decoder.Decode(&document)
		if errors.Is(err, io.EOF) {
			if hook && resources == 0 {
				return BlockedHelmHook
			}
			return ""
		}
		if err != nil {
			return BlockedHelmManifest
		}
		if document == nil {
			continue // Empty YAML documents do not represent resources.
		}
		var object struct {
			APIVersion string `json:"apiVersion"`
			Kind       string `json:"kind"`
			Metadata   struct {
				Name        string            `json:"name"`
				Namespace   string            `json:"namespace"`
				Annotations map[string]string `json:"annotations"`
			} `json:"metadata"`
		}
		payload, err := json.Marshal(document)
		if err != nil || json.Unmarshal(payload, &object) != nil {
			return BlockedHelmManifest
		}
		if strings.EqualFold(strings.TrimSpace(object.Metadata.Annotations["helm.sh/resource-policy"]), "keep") {
			return BlockedHelmRetained
		}
		if hook && (object.APIVersion != "batch/v1" || object.Kind != "Job" ||
			object.Metadata.Name == "" || !namespaces[object.Metadata.Namespace]) {
			return BlockedHelmHook
		}
		if !hook && (object.APIVersion == "" || object.Kind == "" || object.Metadata.Name == "") {
			return BlockedHelmManifest
		}
		resources++
	}
}

func (c *HelmClient) UninstallLabRelease(ctx context.Context, namespace, token string, target LabRelease, srs []SpaceRequestResult) error {
	current, err := c.ListLabReleases(ctx, namespace, token, srs)
	if err != nil {
		return err
	}
	found := false
	for _, rel := range current {
		if rel.Name != target.Name {
			continue
		}
		if !rel.sameSummary(target) || rel.CleanupBlocked || (rel.Status != "deployed" && rel.Status != "failed" && rel.Status != "pending-install") {
			return errors.New("lab release changed or requires manual recovery")
		}
		found = true
	}
	if !found {
		return errors.New("lab release disappeared before uninstall")
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	cfg, err := c.newActionConfig(namespace, token, false)
	if err != nil {
		return err
	}
	uninstall := action.NewUninstall(cfg)
	uninstall.Wait = true
	uninstall.Timeout = 300 * time.Second
	if _, uninstallErr := uninstall.Run(target.Name); uninstallErr != nil {
		return fmt.Errorf("uninstall lab release: %w", uninstallErr)
	}
	status := action.NewStatus(cfg)
	_, err = status.Run(target.Name)
	if !isReleaseNotFound(err) {
		return errors.New("lab release uninstall could not be verified")
	}
	return nil
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

// mapVerifiedBindingsToValues maps namespaces only after checking lab identity and UID.
func mapVerifiedBindingsToValues(ctx context.Context, client *SpaceRequestClient, token, namespace, catalogRef string, bindings []NamespaceBinding, uids map[string]string) (map[string]string, error) {
	label, err := CatalogRefLabel(catalogRef)
	if err != nil {
		return nil, err
	}
	values := make(map[string]string, len(bindings))
	for _, b := range bindings {
		sr, err := client.GetSpaceRequest(ctx, token, namespace, b.Key)
		if err != nil {
			return nil, fmt.Errorf("get SpaceRequest %q: %w", b.Key, err)
		}
		if sr == nil {
			return nil, fmt.Errorf("SpaceRequest %q not found", b.Key)
		}
		if !sr.matchesLabBinding(namespace, b.Key, label, b.Key) || sr.UID != uids[b.Key] {
			return nil, errors.New("SpaceRequest identity changed")
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
