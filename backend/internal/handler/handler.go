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
	// SnapshotCommitHash returns the immutable commit hash of an approved source snapshot.
	SnapshotCommitHash func(context.Context, string, string, string) (string, error)
	// ResourceLoader reads an approved resource from the configured source snapshot.
	ResourceLoader func(context.Context, string, string, string, string) (*bundle.Resource, error)
	// CatalogLabOmitted receives catalog entry omission notifications.
	CatalogLabOmitted func(ctx context.Context, catalogRef, reason string)
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
	for _, key := range []string{"user", "openshift_api_url", "openshift_console_url", "openshift_cluster_ingress_domain"} {
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
	bindings setup.SchemaBindings
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
	if bindings == nil {
		writeError(w, http.StatusUnprocessableEntity,
			laberr.New(laberr.TypeConfigurationError, "failed to parse values schema", nil))
		return nil, false
	}
	if len(bindings.Namespaces) == 0 {
		writeError(w, http.StatusUnprocessableEntity,
			laberr.New(laberr.TypeConfigurationError, "values schema declares no namespace bindings", nil))
		return nil, false
	}

	return &setupContext{
		token:    token,
		subject:  subject,
		entry:    entry,
		result:   result,
		bindings: *bindings,
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
		DisplayName string `json:"displayName"`
		Description string `json:"description,omitempty"`
		LabPath     string `json:"labPath"`
	}
	items := make([]item, 0, len(entries))
	for _, e := range entries {
		result, err := loader(r.Context(), e.Repository, e.Branch, e.LabPath)
		if reason := omittedLabReason(err); reason != "" {
			recordCatalogOmission(r.Context(), cfg, e.CatalogRef, reason)
			continue
		}
		if err != nil || result == nil || result.Manifest == nil {
			writeError(w, http.StatusBadGateway,
				laberr.New(laberr.TypeRepositoryError, "failed to resolve lab catalog", nil))
			return
		}
		items = append(items, item{
			CatalogRef:  e.CatalogRef,
			DisplayName: result.Manifest.Metadata.DisplayName,
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

	if labErr := manifest.ValidateBundlePaths(result.Pages); labErr != nil {
		writeError(w, http.StatusUnprocessableEntity, labErr)
		return
	}
	type page struct {
		Path string `json:"path"`
		URL  string `json:"url"`
	}
	pages := make([]page, 0, len(result.Pages))
	for _, pagePath := range result.Pages {
		pages = append(pages, page{
			Path: pagePath,
			URL:  resourcePath(entry.CatalogRef, entry.LabPath, pagePath),
		})
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

	writeJSON(w, http.StatusOK, map[string]any{
		"apiVersion": "v1alpha1",
		"kind":       "LabBundle",
		"manifest":   result.Manifest,
		"pages":      pages,
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
	if cfg.SnapshotCommitHash == nil || cfg.ResourceLoader == nil {
		writeError(w, http.StatusServiceUnavailable,
			laberr.New(laberr.TypeRepositoryError, "resource loader not configured", nil))
		return
	}
	commitHash, err := cfg.SnapshotCommitHash(r.Context(), entry.Repository, entry.Branch, entry.LabPath)
	if err != nil {
		handleResourceError(w, err)
		return
	}
	etag := fmt.Sprintf("%q", commitHash)
	w.Header().Set("Cache-Control", "private, no-cache")
	w.Header().Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	resource, err := cfg.ResourceLoader(r.Context(), entry.Repository, entry.Branch, entry.LabPath, resourcePath)
	if err != nil {
		handleResourceError(w, err)
		return
	}
	if resource == nil {
		writeError(w, http.StatusUnprocessableEntity, laberr.New(laberr.TypeContentError, "resource is unsupported", nil))
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

	for _, b := range sctx.bindings.Namespaces {
		if err := cfg.SetupClient.EnsureSpaceRequest(r.Context(), sctx.token, userNamespace, b.Key, tierName); err != nil {
			cfg.Logger.Error("create SpaceRequest failed", "name", b.Key, "namespace", userNamespace, "err", err)
			writeError(w, http.StatusBadGateway,
				laberr.New(laberr.TypeConfigurationError, "failed to create SpaceRequest", nil))
			return
		}
	}

	releaseName := setup.HelmReleaseName(sctx.subject, sctx.entry.CatalogRef, sctx.bindings.Namespaces)

	// If Helm is available, check if already deployed (re-launch no-op).
	if cfg.HelmManager != nil {
		status, err := cfg.HelmManager.ReleaseStatus(r.Context(), releaseName, userNamespace, sctx.token)
		if err == nil && status == "deployed" {
			writeJSON(w, http.StatusOK, buildSetupStatusResponse(
				"ready", nil, // SpaceRequests assumed ready if Helm is deployed
				"deployed", releaseName, "", nil, nil,
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
		"pending", releaseName, "", nil, nil,
	))
}

const (
	setupWorkerPollInterval = 5 * time.Second
	setupWorkerPollTimeout  = 30 * time.Minute
)

func runSetupWorker(cfg Config, token, namespace, releaseName, chartDir string, bindings setup.SchemaBindings, helmTimeout time.Duration) {
	setState := func(phase, errMsg string) {
		activeSetups.Store(releaseName, &workerState{phase: phase, err: errMsg})
	}

	// Phase 1: wait for all SpaceRequests to become ready.
	pollCtx, pollCancel := context.WithTimeout(context.Background(), setupWorkerPollTimeout)
	defer pollCancel()

	var nsValues map[string]string
	for {
		var err error
		nsValues, err = setup.MapBindingsToValues(pollCtx, cfg.SetupClient, token, namespace, bindings.Namespaces)
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
	if len(bindings.Passwords) > 0 {
		pwPaths := make([]string, len(bindings.Passwords))
		for i, b := range bindings.Passwords {
			pwPaths[i] = b.Path
		}
		cfg.Logger.Info("generating passwords", "release", releaseName, "paths", pwPaths)
		pwValues, err := setup.GeneratePasswordValues(bindings.Passwords)
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

	for _, b := range bindings.RootURLs {
		if cfg.ClusterIngressDomain == "" {
			cfg.Logger.Error("cluster ingress domain not configured but required by schema", "release", releaseName, "path", b.Path)
			setState("failed", "cluster ingress domain not configured")
			return
		}
		nsPath := setup.NamespaceBindingPath(bindings.Namespaces, b.NamespaceRef)
		nsName := nsValues[nsPath]
		if nsName == "" {
			availableKeys := make([]string, len(bindings.Namespaces))
			for i, nb := range bindings.Namespaces {
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

func handleSetupStatus(w http.ResponseWriter, r *http.Request, cfg Config) {
	sctx, ok := resolveSetupContext(w, r, cfg)
	if !ok {
		return
	}

	userNamespace := sctx.subject + "-dev"

	type namespaceStatus struct {
		Status string `json:"status"`
		Name   string `json:"name"`
	}
	namespaces := make(map[string]namespaceStatus, len(sctx.bindings.Namespaces))
	allReady := true
	anyFailed := false

	for _, b := range sctx.bindings.Namespaces {
		sr, err := cfg.SetupClient.GetSpaceRequest(r.Context(), sctx.token, userNamespace, b.Key)
		if err != nil {
			cfg.Logger.Warn("get SpaceRequest failed", "name", b.Key, "namespace", userNamespace, "err", err)
			namespaces[b.Key] = namespaceStatus{Status: "pending", Name: b.Key}
			allReady = false
			continue
		}
		if sr == nil {
			namespaces[b.Key] = namespaceStatus{Status: "pending", Name: b.Key}
			allReady = false
			continue
		}
		if sr.IsReady() && sr.TargetNamespace() != "" {
			cfg.Logger.Info("SpaceRequest ready", "name", b.Key, "namespace", sr.TargetNamespace())
			namespaces[b.Key] = namespaceStatus{Status: "ready", Name: sr.TargetNamespace()}
		} else if sr.IsFailed() {
			namespaces[b.Key] = namespaceStatus{Status: "failed", Name: b.Key}
			anyFailed = true
			allReady = false
		} else {
			namespaces[b.Key] = namespaceStatus{Status: "pending", Name: b.Key}
			allReady = false
		}
	}

	srStatus := "provisioning"
	if anyFailed {
		srStatus = "failed"
	} else if allReady {
		srStatus = "ready"
	}

	releaseName := setup.HelmReleaseName(sctx.subject, sctx.entry.CatalogRef, sctx.bindings.Namespaces)
	helmStatus := "pending"
	helmMessage := ""
	var generatedValues map[string]string

	if cfg.HelmManager != nil {
		releaseStatus, err := cfg.HelmManager.ReleaseStatus(r.Context(), releaseName, userNamespace, sctx.token)
		if err == nil && releaseStatus != "" {
			helmStatus = setup.HelmStatusLabel(releaseStatus)
			if helmStatus == "deployed" && len(sctx.bindings.Passwords) > 0 {
				paths := make([]string, len(sctx.bindings.Passwords))
				for i, b := range sctx.bindings.Passwords {
					paths[i] = b.Path
				}
				generatedValues, _ = cfg.HelmManager.ReleaseValues(r.Context(), releaseName, userNamespace, sctx.token, paths)
			}
		} else if val, loaded := activeSetups.Load(releaseName); loaded {
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
	}

	var attributes map[string]string
	if allReady && helmStatus == "deployed" {
		attributes = make(map[string]string, len(sctx.bindings.Namespaces)+len(sctx.bindings.Passwords)+len(sctx.bindings.RootURLs))
		for _, b := range sctx.bindings.Namespaces {
			attributes[strings.ReplaceAll(b.Key, ".", "-")] = namespaces[b.Key].Name
		}
		for _, b := range sctx.bindings.Passwords {
			if v, ok := generatedValues[b.Path]; ok {
				attributes[strings.ReplaceAll(b.Key, ".", "-")] = v
			}
		}
		for _, b := range sctx.bindings.RootURLs {
			if cfg.ClusterIngressDomain == "" {
				continue
			}
			ns, ok := namespaces[b.NamespaceRef]
			if !ok {
				continue
			}
			attributes[strings.ReplaceAll(b.Key, ".", "-")] = fmt.Sprintf("%s-%s.%s", b.RouteName, ns.Name, cfg.ClusterIngressDomain)
		}
	}

	writeJSON(w, http.StatusOK, buildSetupStatusResponse(srStatus, namespaces, helmStatus, releaseName, helmMessage, generatedValues, attributes))
}

func buildSetupStatusResponse(srStatus string, namespaces any, helmStatus, releaseName, helmMessage string, generatedValues, attributes map[string]string) map[string]any {
	helm := map[string]any{
		"status":      helmStatus,
		"releaseName": releaseName,
	}
	if helmMessage != "" {
		helm["message"] = helmMessage
	}
	if len(generatedValues) > 0 {
		helm["generatedValues"] = generatedValues
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

func recordCatalogOmission(ctx context.Context, cfg Config, catalogRef, reason string) {
	cfg.Logger.Warn("catalog lab omitted", "catalogRef", catalogRef, "reason", reason)
	if cfg.CatalogLabOmitted != nil {
		cfg.CatalogLabOmitted(ctx, catalogRef, reason)
	}
}

func resourcePath(catalogRef, labPath, pagePath string) string {
	resourceURL := url.URL{Path: path.Join("api/v1alpha1/labs", catalogRef, "resources", path.Clean(labPath), "content", pagePath)}
	return resourceURL.EscapedPath()
}
