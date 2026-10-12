// Package handler wires the three MVP backend operations to http.ServeMux routes.
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/codeready-toolchain/interactive-labs-console-plugin/backend/internal/bundle"
	"github.com/codeready-toolchain/interactive-labs-console-plugin/backend/internal/catalog"
	laberr "github.com/codeready-toolchain/interactive-labs-console-plugin/backend/internal/errors"
	"github.com/codeready-toolchain/interactive-labs-console-plugin/backend/internal/manifest"
	"github.com/codeready-toolchain/interactive-labs-console-plugin/backend/internal/setup"
)

var activeSetups sync.Map

type workerState struct {
	phase string // "polling", "installing", "done", "failed"
	err   string
}

// Config holds injectable dependencies for the router.
type Config struct {
	// CatalogSource returns the live catalog ConfigMap; nil uses an empty ConfigMap.
	CatalogSource func(context.Context) (*corev1.ConfigMap, error)
	// BundleLoader loads a bundle from the configured repo; nil means bundles are unavailable.
	BundleLoader func(context.Context, string, string, string) (*bundle.Result, error)
	// ResourceLoader reads an approved resource from the configured source snapshot.
	ResourceLoader func(context.Context, string, string, string) (*bundle.Resource, error)
	// CatalogLabOmitted receives validated catalog entry omission notifications.
	CatalogLabOmitted func(ctx context.Context, entry catalog.Entry, reason string)
	// Logger receives safe operational fields only; tokens must never be passed to it.
	Logger *slog.Logger
	// Authenticate resolves a forwarded user token to its server-verified subject.
	Authenticate func(context.Context, string, string) (string, error)
	// RuntimeAttributes returns request-scoped attributes merged on top of static ones.
	// nil means no runtime attrs. The subject is the authenticated user identifier.
	// Production default may be nil while the key list is TBD.
	RuntimeAttributes func(r *http.Request, subject string) (map[string]string, error)
	// SetupClient handles SpaceRequest operations for lab setup.
	// nil means setup is unavailable.
	SetupClient *setup.SpaceRequestClient
	// SpaceRequestTierName is the tier used when creating SpaceRequests.
	SpaceRequestTierName string
	// HelmManager handles Helm release operations. nil means Helm is unavailable.
	HelmManager setup.HelmManager
	// HelmTimeout is the timeout for Helm install operations. Zero uses 300s default.
	HelmTimeout time.Duration
	// ClusterIngressDomain is the cluster's ingress domain (e.g. "apps.cluster.example.com"),
	// injected as a Helm value for properties with x-interactive-labs.kind "cluster-ingress-domain".
	ClusterIngressDomain string
}

// NewRouter returns an http.Handler with all three MVP routes registered.
func NewRouter(cfg Config) http.Handler {
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/v1alpha1/catalog", func(w http.ResponseWriter, r *http.Request) {
		handleCatalog(w, r, cfg)
	})
	mux.HandleFunc("GET /api/v1alpha1/labs/{catalogRef}/bundle", func(w http.ResponseWriter, r *http.Request) {
		handleBundle(w, r, cfg)
	})
	mux.HandleFunc("GET /api/v1alpha1/labs/{catalogRef}/resources/{resourcePath...}", func(w http.ResponseWriter, r *http.Request) {
		handleResource(w, r, cfg)
	})
	mux.HandleFunc("POST /api/v1alpha1/labs/{catalogRef}/setup", func(w http.ResponseWriter, r *http.Request) {
		handleSetup(w, r, cfg)
	})
	mux.HandleFunc("GET /api/v1alpha1/labs/{catalogRef}/setup/status", func(w http.ResponseWriter, r *http.Request) {
		handleSetupStatus(w, r, cfg)
	})

	return mux
}

// --- helpers ---

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w). //nolint:errchkjson
				Encode(body) //nolint:errcheck
}

func writeError(w http.ResponseWriter, status int, e *laberr.LabError) {
	writeJSON(w, status, e)
}

// bearerToken extracts the raw token from "Authorization: Bearer <token>".
func bearerToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		return ""
	}
	return strings.TrimPrefix(auth, "Bearer ")
}

// mergeAttributes merges attribute maps left-to-right (rightmost wins).
func mergeAttributes(layers ...map[string]string) map[string]string {
	out := make(map[string]string)
	for _, layer := range layers {
		maps.Copy(out, layer)
	}
	return out
}

func completeRuntimeAttributes(attrs map[string]string) bool {
	if len(attrs) != 4 {
		return false
	}
	for _, key := range []string{"user", "openshift-api-url", "openshift-console-url", "openshift-cluster-ingress-domain"} {
		if attrs[key] == "" {
			return false
		}
	}
	return true
}

// catalogEntryByCatalogRef returns the catalog entry for the given catalogRef, or an error.
func catalogEntryByCatalogRef(ctx context.Context, cfg Config, catalogRef string) (*catalog.Entry, *laberr.LabError) {
	source := cfg.CatalogSource
	if source == nil {
		return nil, laberr.New(laberr.TypeCatalogUnavailable, "catalog not configured", nil)
	}
	cm, err := source(ctx)
	if err != nil {
		return nil, laberr.New(laberr.TypeCatalogUnavailable, "catalog read failed", nil)
	}
	entries, err := catalog.Read(cm)
	if err != nil {
		return nil, laberr.New(laberr.TypeConfigurationError, "catalog configuration error", nil)
	}
	for i := range entries {
		if entries[i].CatalogRef == catalogRef {
			return &entries[i], nil
		}
	}
	return nil, laberr.New(laberr.TypeLabNotFound, "lab not found in catalog", nil)
}

type setupContext struct {
	token    string
	subject  string
	entry    *catalog.Entry
	result   *bundle.Result
	bindings []setup.SchemaBinding
}

func resolveSetupContext(w http.ResponseWriter, r *http.Request, cfg Config) (*setupContext, bool) {
	token := bearerToken(r)
	if token == "" || cfg.Authenticate == nil {
		writeError(w, http.StatusUnauthorized, laberr.New(laberr.TypeUnauthorized, "authentication required", nil))
		return nil, false
	}
	subject, err := cfg.Authenticate(r.Context(), r.Header.Get("Impersonate-User"), token)
	if err != nil || subject == "" {
		writeError(w, http.StatusUnauthorized, laberr.New(laberr.TypeUnauthorized, "authentication failed", nil))
		return nil, false
	}

	catalogRef := r.PathValue("catalogRef")
	entry, labError := catalogEntryByCatalogRef(r.Context(), cfg, catalogRef)
	if labError != nil {
		writeError(w, http.StatusNotFound, labError)
		return nil, false
	}

	loader := cfg.BundleLoader
	if loader == nil {
		writeError(w, http.StatusServiceUnavailable,
			laberr.New(laberr.TypeRepositoryError, "bundle loader not configured", nil))
		return nil, false
	}
	result, err := loader(r.Context(), entry.Repository, entry.Branch, entry.LabPath)
	if err != nil {
		writeError(w, http.StatusBadGateway,
			laberr.New(laberr.TypeRepositoryError, "failed to load lab bundle", nil))
		return nil, false
	}

	if result.ValuesSchema == nil {
		writeError(w, http.StatusUnprocessableEntity,
			laberr.New(laberr.TypeChartNotFound, "lab has no values schema", nil))
		return nil, false
	}

	if cfg.SetupClient == nil {
		writeError(w, http.StatusServiceUnavailable,
			laberr.New(laberr.TypeConfigurationError, "setup client not configured", nil))
		return nil, false
	}

	bindings, err := setup.ParseSchemaBindings(result.ValuesSchema)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity,
			laberr.New(laberr.TypeConfigurationError, "invalid values schema", nil))
		return nil, false
	}
	if len(setup.BindingsByKind(bindings, setup.KindNamespaceName)) == 0 {
		writeError(w, http.StatusUnprocessableEntity,
			laberr.New(laberr.TypeConfigurationError, "values schema declares no namespace bindings", nil))
		return nil, false
	}

	return &setupContext{
		token:    token,
		subject:  subject,
		entry:    entry,
		result:   result,
		bindings: bindings,
	}, true
}

// --- handler functions ---

func handleCatalog(w http.ResponseWriter, r *http.Request, cfg Config) {
	source := cfg.CatalogSource
	if source == nil {
		writeError(w, http.StatusServiceUnavailable,
			laberr.New(laberr.TypeCatalogUnavailable, "catalog not configured", nil))
		return
	}
	cm, err := source(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable,
			laberr.New(laberr.TypeCatalogUnavailable, "catalog read failed", nil))
		return
	}
	entries, err := catalog.Read(cm)
	if err != nil {
		writeError(w, http.StatusInternalServerError,
			laberr.New(laberr.TypeConfigurationError, "catalog configuration error", nil))
		return
	}

	loader := cfg.BundleLoader
	if loader == nil {
		writeError(w, http.StatusServiceUnavailable,
			laberr.New(laberr.TypeRepositoryError, "bundle loader not configured", nil))
		return
	}
	type item struct {
		CatalogRef  string `json:"catalogRef"`
		Title       string `json:"title"`
		Description string `json:"description,omitempty"`
		LabPath     string `json:"labPath"`
	}
	items := make([]item, 0, len(entries))
	for _, e := range entries {
		result, err := loader(r.Context(), e.Repository, e.Branch, e.LabPath)
		if reason := omittedLabReason(err); reason != "" {
			recordCatalogOmission(r.Context(), cfg, e, reason)
			continue
		}
		if err != nil || result == nil || result.Manifest == nil {
			writeError(w, http.StatusBadGateway,
				laberr.New(laberr.TypeRepositoryError, "failed to resolve lab catalog", nil))
			return
		}
		items = append(items, item{
			CatalogRef:  e.CatalogRef,
			Title:       result.Manifest.Metadata.Title,
			Description: result.Manifest.Metadata.Description,
			LabPath:     path.Clean(e.LabPath),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"apiVersion": "v1alpha1",
		"kind":       "CatalogList",
		"items":      items,
	})
}

func handleBundle(w http.ResponseWriter, r *http.Request, cfg Config) {
	// Auth required before loading bundle.
	token := bearerToken(r)
	if token == "" || cfg.Authenticate == nil {
		writeError(w, http.StatusUnauthorized, laberr.New(laberr.TypeUnauthorized, "authentication required", nil))
		return
	}
	subject, err := cfg.Authenticate(r.Context(), r.Header.Get("Impersonate-User"), token)
	if err != nil || subject == "" {
		writeError(w, http.StatusUnauthorized, laberr.New(laberr.TypeUnauthorized, "authentication failed", nil))
		return
	}

	catalogRef := r.PathValue("catalogRef")
	entry, labError := catalogEntryByCatalogRef(r.Context(), cfg, catalogRef)
	if labError != nil {
		writeError(w, http.StatusNotFound, labError)
		return
	}

	loader := cfg.BundleLoader
	if loader == nil {
		writeError(w, http.StatusServiceUnavailable,
			laberr.New(laberr.TypeRepositoryError, "bundle loader not configured", nil))
		return
	}

	result, err := loader(r.Context(), entry.Repository, entry.Branch, entry.LabPath)
	if err != nil {
		writeError(w, http.StatusBadGateway,
			laberr.New(laberr.TypeRepositoryError, "failed to load lab bundle", nil))
		return
	}
	cfg.Logger.Info("bundle loaded", "repository", entry.Repository, "branch", entry.Branch, "commit", result.Revision)

	type page struct {
		Path string `json:"path"`
		URL  string `json:"url"`
	}
	type module struct {
		ID    string `json:"id"`
		Pages []page `json:"pages"`
	}
	if len(result.Modules) == 0 {
		writeError(w, http.StatusUnprocessableEntity, laberr.New(laberr.TypeContentError, "no modules in bundle", nil))
		return
	}
	modules := make([]module, 0, len(result.Modules))
	for _, source := range result.Modules {
		if labErr := manifest.ValidateBundlePaths(source.Pages); labErr != nil {
			writeError(w, http.StatusUnprocessableEntity, labErr)
			return
		}
		pages := make([]page, 0, len(source.Pages))
		for _, relative := range source.Pages {
			pagePath := path.Join("modules", source.ID, "content", relative)
			pages = append(pages, page{
				Path: pagePath,
				URL:  resourcePath(entry.CatalogRef, entry.LabPath, pagePath),
			})
		}
		modules = append(modules, module{ID: source.ID, Pages: pages})
	}

	// Merge attributes: manifest → catalog overrides → runtime attrs.
	staticAttrs := mergeAttributes(result.Manifest.Spec.Attributes, entry.AttributeOverrides)
	if cfg.RuntimeAttributes == nil {
		writeError(w, http.StatusServiceUnavailable,
			laberr.New(laberr.TypeRuntimeAttributesUnavailable, "runtime attributes unavailable", nil))
		return
	}
	runtimeAttrs, err := cfg.RuntimeAttributes(r, subject)
	if err != nil || !completeRuntimeAttributes(runtimeAttrs) {
		writeError(w, http.StatusServiceUnavailable,
			laberr.New(laberr.TypeRuntimeAttributesUnavailable, "runtime attributes unavailable", nil))
		return
	}
	result.Manifest.Spec.Attributes = mergeAttributes(staticAttrs, runtimeAttrs)
	cfg.Logger.Info("bundle attributes", "keys", slices.Sorted(maps.Keys(result.Manifest.Spec.Attributes)))

	writeJSON(w, http.StatusOK, map[string]any{
		"apiVersion": "v1alpha1",
		"kind":       "LabBundle",
		"manifest":   result.Manifest,
		"modules":    modules,
		"runtimeContext": map[string]any{
			"catalogRef":         entry.CatalogRef,
			"branch":             entry.Branch,
			"helmChartAvailable": result.HelmChartAvailable,
		},
	})
}

func handleResource(w http.ResponseWriter, r *http.Request, cfg Config) {
	token := bearerToken(r)
	if token == "" || cfg.Authenticate == nil {
		writeError(w, http.StatusUnauthorized, laberr.New(laberr.TypeUnauthorized, "authentication required", nil))
		return
	}
	if subject, err := cfg.Authenticate(r.Context(), r.Header.Get("Impersonate-User"), token); err != nil || subject == "" {
		writeError(w, http.StatusUnauthorized, laberr.New(laberr.TypeUnauthorized, "authentication failed", nil))
		return
	}

	entry, labError := catalogEntryByCatalogRef(r.Context(), cfg, r.PathValue("catalogRef"))
	if labError != nil {
		writeError(w, http.StatusNotFound, labError)
		return
	}
	resourcePath := r.PathValue("resourcePath")
	if err := bundle.ValidateResourcePath(resourcePath); err != nil {
		writeError(w, http.StatusUnprocessableEntity, laberr.New(laberr.TypePathTraversal, "resource path is invalid", nil))
		return
	}
	if cfg.ResourceLoader == nil {
		writeError(w, http.StatusServiceUnavailable,
			laberr.New(laberr.TypeRepositoryError, "resource loader not configured", nil))
		return
	}
	resource, err := cfg.ResourceLoader(r.Context(), entry.Repository, entry.Branch, resourcePath)
	if err != nil {
		handleResourceError(w, err)
		return
	}
	if resource == nil || resource.Revision == "" {
		writeError(w, http.StatusUnprocessableEntity, laberr.New(laberr.TypeContentError, "resource is unsupported", nil))
		return
	}
	etag := fmt.Sprintf("%q", resource.Revision)
	w.Header().Set("Cache-Control", "private, no-cache")
	w.Header().Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	contentType := http.DetectContentType(resource.Bytes)
	if !isSafeResourceContentType(contentType) {
		writeError(w, http.StatusUnprocessableEntity, laberr.New(laberr.TypeContentError, "resource is unsupported", nil))
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(resource.Bytes)
}

func handleResourceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, bundle.ErrResourceNotFound):
		writeError(w, http.StatusNotFound, laberr.New(laberr.TypeContentError, "resource not found", nil))
	case errors.Is(err, bundle.ErrResourceInvalid):
		writeError(w, http.StatusUnprocessableEntity, laberr.New(laberr.TypePathTraversal, "resource path is invalid", nil))
	case errors.Is(err, bundle.ErrResourceUnsupported):
		writeError(w, http.StatusUnprocessableEntity, laberr.New(laberr.TypeContentError, "resource is unsupported", nil))
	default:
		writeError(w, http.StatusBadGateway, laberr.New(laberr.TypeRepositoryError, "failed to load resource", nil))
	}
}

func isSafeResourceContentType(contentType string) bool {
	switch contentType {
	case "text/plain; charset=utf-8", "image/png", "image/jpeg", "image/gif", "image/webp":
		return true
	default:
		return false
	}
}

func handleSetup(w http.ResponseWriter, r *http.Request, cfg Config) {
	cfg.Logger.Info("setup request received", "catalogRef", r.PathValue("catalogRef"))

	sctx, ok := resolveSetupContext(w, r, cfg)
	if !ok {
		return
	}

	if cfg.HelmManager == nil {
		writeError(w, http.StatusServiceUnavailable,
			laberr.New(laberr.TypeConfigurationError, "Helm manager not configured", nil))
		return
	}

	userNamespace := sctx.subject + "-dev"
	tierName := cfg.SpaceRequestTierName
	if tierName == "" {
		tierName = "base1ns"
	}

	nsBindings := setup.BindingsByKind(sctx.bindings, setup.KindNamespaceName)
	for _, b := range nsBindings {
		if err := cfg.SetupClient.EnsureSpaceRequest(r.Context(), sctx.token, userNamespace, b.Key, tierName); err != nil {
			cfg.Logger.Error("create SpaceRequest failed", "name", b.Key, "namespace", userNamespace, "err", err)
			writeError(w, http.StatusBadGateway,
				laberr.New(laberr.TypeConfigurationError, "failed to create SpaceRequest", nil))
			return
		}
	}

	releaseName := setup.HelmReleaseName(sctx.subject, sctx.entry.CatalogRef, nsBindings)

	// If Helm is available, check if already deployed (re-launch no-op).
	if cfg.HelmManager != nil {
		status, err := cfg.HelmManager.ReleaseStatus(r.Context(), releaseName, userNamespace, sctx.token)
		if err == nil && status == "deployed" {
			writeJSON(w, http.StatusOK, buildSetupStatusResponse(
				"ready", nil, // SpaceRequests assumed ready if Helm is deployed
				"deployed", releaseName, "", nil,
			))
			return
		}

		// Start background goroutine if not already running.
		helmTimeout := cfg.HelmTimeout
		if helmTimeout == 0 {
			helmTimeout = 300 * time.Second
		}
		newState := &workerState{phase: "polling"}
		startWorker := false
		if val, loaded := activeSetups.LoadOrStore(releaseName, newState); loaded {
			state := val.(*workerState)
			if state.phase == "done" || state.phase == "failed" {
				startWorker = activeSetups.CompareAndSwap(releaseName, val, newState)
			}
		} else {
			startWorker = true
		}
		if startWorker {
			go runSetupWorker(cfg, sctx.token, userNamespace, releaseName, sctx.result.HelmChartDir, sctx.bindings, helmTimeout) //nolint:gosec
		}
	}

	writeJSON(w, http.StatusAccepted, buildSetupStatusResponse(
		"provisioning", nil,
		"pending", releaseName, "", nil,
	))
}

const (
	setupWorkerPollInterval = 5 * time.Second
	setupWorkerPollTimeout  = 30 * time.Minute
)

func runSetupWorker(cfg Config, token, namespace, releaseName, chartDir string, bindings []setup.SchemaBinding, helmTimeout time.Duration) {
	setState := func(phase, errMsg string) {
		activeSetups.Store(releaseName, &workerState{phase: phase, err: errMsg})
	}

	nsBindings := setup.BindingsByKind(bindings, setup.KindNamespaceName)
	pwBindings := setup.BindingsByKind(bindings, setup.KindGeneratedPassword)
	urlBindings := setup.BindingsByKind(bindings, setup.KindRootURL)

	// Phase 1: wait for all SpaceRequests to become ready.
	pollCtx, pollCancel := context.WithTimeout(context.Background(), setupWorkerPollTimeout)
	defer pollCancel()

	var nsValues map[string]string
	for {
		var err error
		nsValues, err = setup.MapBindingsToValues(pollCtx, cfg.SetupClient, token, namespace, nsBindings)
		if err == nil {
			for path, nsName := range nsValues {
				cfg.Logger.Info("namespace ready", "release", releaseName, "binding", path, "namespace", nsName)
			}
			break
		}

		cfg.Logger.Info("SpaceRequests not ready, retrying", "release", releaseName)

		select {
		case <-pollCtx.Done():
			cfg.Logger.Error("setup worker timed out waiting for SpaceRequests", "release", releaseName)
			setState("failed", "timed out waiting for SpaceRequests")
			return
		case <-time.After(setupWorkerPollInterval):
		}
	}

	// Generate password values into a separate map to avoid mutating nsValues.
	values := make(map[string]string, len(nsValues))
	for k, v := range nsValues {
		values[k] = v
	}
	if len(pwBindings) > 0 {
		pwPaths := make([]string, len(pwBindings))
		for i, b := range pwBindings {
			pwPaths[i] = b.Path
		}
		cfg.Logger.Info("generating passwords", "release", releaseName, "paths", pwPaths)
		pwValues, err := setup.GeneratePasswordValues(pwBindings)
		if err != nil {
			cfg.Logger.Error("password generation failed", "release", releaseName, "err", err)
			setState("failed", "password generation failed")
			return
		}
		for k, v := range pwValues {
			values[k] = v
		}
		cfg.Logger.Info("passwords generated", "release", releaseName, "count", len(pwValues))
	}

	for _, b := range urlBindings {
		if cfg.ClusterIngressDomain == "" {
			cfg.Logger.Error("cluster ingress domain not configured but required by schema", "release", releaseName, "path", b.Path)
			setState("failed", "cluster ingress domain not configured")
			return
		}
		nsPath := setup.BindingPath(nsBindings, b.NamespaceRef)
		nsName := nsValues[nsPath]
		if nsName == "" {
			availableKeys := make([]string, len(nsBindings))
			for i, nb := range nsBindings {
				availableKeys[i] = nb.Key
			}
			cfg.Logger.Error("root-url references unknown namespace binding", "release", releaseName, "namespaceRef", b.NamespaceRef, "nsPath", nsPath, "availableKeys", availableKeys, "nsValues", nsValues)
			setState("failed", "root-url references unknown namespace binding: "+b.NamespaceRef)
			return
		}
		values[b.Path] = fmt.Sprintf("%s-%s.%s", b.RouteName, nsName, cfg.ClusterIngressDomain)
	}

	// Phase 2: install Helm chart with its own timeout.
	valueKeys := make([]string, 0, len(values))
	for k := range values {
		valueKeys = append(valueKeys, k)
	}
	cfg.Logger.Info("all SpaceRequests ready, installing Helm chart", "release", releaseName, "valueKeys", valueKeys)
	setState("installing", "")

	// Give the context extra margin so Helm's own --wait timeout fires first
	// with a detailed message (e.g., which Deployment was not ready).
	installCtx, installCancel := context.WithTimeout(context.Background(), helmTimeout+30*time.Second)
	defer installCancel()

	if err := cfg.HelmManager.Install(installCtx, setup.HelmInstallOpts{
		ChartDir:    chartDir,
		ReleaseName: releaseName,
		Namespace:   namespace,
		Token:       token,
		Values:      values,
		Timeout:     helmTimeout,
	}); err != nil {
		cfg.Logger.Error("Helm install failed", "release", releaseName, "err", err)
		logPodDiagnostics(cfg, token, nsValues)
		setState("failed", err.Error())
	} else {
		cfg.Logger.Info("Helm install completed", "release", releaseName)
		setState("done", "")
	}
}

func logPodDiagnostics(cfg Config, token string, nsValues map[string]string) {
	if cfg.HelmManager == nil {
		return
	}
	for binding, nsName := range nsValues {
		pods, err := cfg.HelmManager.ListPods(context.Background(), token, nsName)
		if err != nil {
			cfg.Logger.Warn("failed to list pods for diagnostics", "namespace", nsName, "err", err)
			continue
		}
		for _, p := range pods {
			cfg.Logger.Info("pod status", "binding", binding, "namespace", nsName, "pod", p.Name, "phase", p.Phase, "ready", p.Ready, "reason", p.Reason)
		}
	}
}

type namespaceStatus struct {
	Status string `json:"status"`
	Name   string `json:"name"`
}

type namespaceStatuses struct {
	entries   map[string]namespaceStatus
	allReady  bool
	anyFailed bool
}

func resolveNamespaceStatuses(ctx context.Context, cfg Config, token, userNamespace string, nsBindings []setup.SchemaBinding) namespaceStatuses {
	result := namespaceStatuses{
		entries:  make(map[string]namespaceStatus, len(nsBindings)),
		allReady: true,
	}
	for _, b := range nsBindings {
		sr, err := cfg.SetupClient.GetSpaceRequest(ctx, token, userNamespace, b.Key)
		if err != nil {
			cfg.Logger.Warn("get SpaceRequest failed", "name", b.Key, "namespace", userNamespace, "err", err)
			result.entries[b.Key] = namespaceStatus{Status: "pending", Name: b.Key}
			result.allReady = false
			continue
		}
		if sr == nil {
			result.entries[b.Key] = namespaceStatus{Status: "pending", Name: b.Key}
			result.allReady = false
			continue
		}
		if sr.IsReady() && sr.TargetNamespace() != "" {
			cfg.Logger.Info("SpaceRequest ready", "name", b.Key, "namespace", sr.TargetNamespace())
			result.entries[b.Key] = namespaceStatus{Status: "ready", Name: sr.TargetNamespace()}
		} else if sr.IsFailed() {
			result.entries[b.Key] = namespaceStatus{Status: "failed", Name: b.Key}
			result.anyFailed = true
			result.allReady = false
		} else {
			result.entries[b.Key] = namespaceStatus{Status: "pending", Name: b.Key}
			result.allReady = false
		}
	}
	return result
}

func (ns namespaceStatuses) srStatus() string {
	if ns.anyFailed {
		return "failed"
	}
	if ns.allReady {
		return "ready"
	}
	return "provisioning"
}

func resolveHelmStatus(ctx context.Context, cfg Config, releaseName, userNamespace, token string, bindings []setup.SchemaBinding) (helmStatus, helmMessage string, releaseValues map[string]string) {
	helmStatus = "pending"
	if cfg.HelmManager == nil {
		return
	}
	status, err := cfg.HelmManager.ReleaseStatus(ctx, releaseName, userNamespace, token)
	if err == nil && status != "" {
		helmStatus = setup.HelmStatusLabel(status)
		if helmStatus == "deployed" {
			releaseValues = fetchHelmValues(ctx, cfg, releaseName, userNamespace, token, bindings)
		}
		return
	}
	if val, loaded := activeSetups.Load(releaseName); loaded {
		state := val.(*workerState)
		switch state.phase {
		case "installing":
			helmStatus = "installing"
		case "failed":
			helmStatus = "failed"
			helmMessage = state.err
		case "polling":
			helmStatus = "pending"
		}
	}
	return
}

func fetchHelmValues(ctx context.Context, cfg Config, releaseName, userNamespace, token string, bindings []setup.SchemaBinding) map[string]string {
	var valuePaths []string
	for _, b := range bindings {
		// Namespace and root-URL bindings are resolved from SpaceRequest/cluster
		// config, not from Helm values — skip them here.
		if b.Kind == setup.KindNamespaceName || b.Kind == setup.KindRootURL {
			continue
		}
		valuePaths = append(valuePaths, b.Path)
	}
	if len(valuePaths) == 0 {
		return nil
	}
	values, _ := cfg.HelmManager.ReleaseValues(ctx, releaseName, userNamespace, token, valuePaths)
	return values
}

func buildAttributes(bindings []setup.SchemaBinding, namespaces map[string]namespaceStatus, releaseValues map[string]string, clusterIngressDomain string) map[string]string {
	attributes := make(map[string]string, len(bindings))
	for _, b := range bindings {
		key := strings.ReplaceAll(b.Path, ".", "-")
		switch b.Kind {
		case setup.KindNamespaceName:
			attributes[key] = namespaces[b.Key].Name
		case setup.KindRootURL:
			if clusterIngressDomain == "" {
				continue
			}
			ns, ok := namespaces[b.NamespaceRef]
			if !ok {
				continue
			}
			attributes[key] = fmt.Sprintf("%s-%s.%s", b.RouteName, ns.Name, clusterIngressDomain)
		default:
			if v, ok := releaseValues[b.Path]; ok {
				attributes[key] = v
			}
		}
	}
	return attributes
}

func handleSetupStatus(w http.ResponseWriter, r *http.Request, cfg Config) {
	sctx, ok := resolveSetupContext(w, r, cfg)
	if !ok {
		return
	}

	userNamespace := sctx.subject + "-dev"
	nsBindings := setup.BindingsByKind(sctx.bindings, setup.KindNamespaceName)

	ns := resolveNamespaceStatuses(r.Context(), cfg, sctx.token, userNamespace, nsBindings)

	releaseName := setup.HelmReleaseName(sctx.subject, sctx.entry.CatalogRef, nsBindings)
	helmStatus, helmMessage, releaseValues := resolveHelmStatus(r.Context(), cfg, releaseName, userNamespace, sctx.token, sctx.bindings)

	var attributes map[string]string
	if ns.allReady && helmStatus == "deployed" {
		attributes = buildAttributes(sctx.bindings, ns.entries, releaseValues, cfg.ClusterIngressDomain)
	}

	if len(attributes) > 0 {
		cfg.Logger.Info("setup status attributes", "keys", slices.Sorted(maps.Keys(attributes)))
	}
	writeJSON(w, http.StatusOK, buildSetupStatusResponse(ns.srStatus(), ns.entries, helmStatus, releaseName, helmMessage, attributes))
}

func buildSetupStatusResponse(srStatus string, namespaces any, helmStatus, releaseName, helmMessage string, attributes map[string]string) map[string]any {
	helm := map[string]any{
		"status":      helmStatus,
		"releaseName": releaseName,
	}
	if helmMessage != "" {
		helm["message"] = helmMessage
	}
	resp := map[string]any{
		"apiVersion": "v1alpha1",
		"kind":       "SetupStatus",
		"spaceRequests": map[string]any{
			"status":     srStatus,
			"namespaces": namespaces,
		},
		"helm": helm,
	}
	if len(attributes) > 0 {
		resp["attributes"] = attributes
	}
	return resp
}

func omittedLabReason(err error) string {
	switch {
	case errors.Is(err, bundle.ErrRepositoryUnavailable):
		return "RepositoryUnavailable"
	case errors.Is(err, bundle.ErrManifestMissing):
		return "ManifestMissing"
	case errors.Is(err, bundle.ErrLabInvalid):
		return "LabInvalid"
	default:
		return ""
	}
}

func recordCatalogOmission(ctx context.Context, cfg Config, entry catalog.Entry, reason string) {
	if cfg.Logger != nil {
		cfg.Logger.Warn("catalog lab omitted", "catalogRef", entry.CatalogRef, "repository", entry.Repository, "branch", entry.Branch, "labPath", entry.LabPath, "reason", reason)
	}
	if cfg.CatalogLabOmitted != nil {
		cfg.CatalogLabOmitted(ctx, entry, reason)
	}
}

func resourcePath(catalogRef, labPath, pagePath string) string {
	resourceURL := url.URL{Path: path.Join("api/v1alpha1/labs", catalogRef, "resources", path.Clean(labPath), pagePath)}
	return resourceURL.EscapedPath()
}
