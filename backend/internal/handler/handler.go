// Package handler wires the three MVP backend operations to http.ServeMux routes.
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"path"
	"strings"

	corev1 "k8s.io/api/core/v1"

	"github.com/codeready-toolchain/interactive-labs-console-plugin/backend/internal/bundle"
	"github.com/codeready-toolchain/interactive-labs-console-plugin/backend/internal/catalog"
	laberr "github.com/codeready-toolchain/interactive-labs-console-plugin/backend/internal/errors"
	"github.com/codeready-toolchain/interactive-labs-console-plugin/backend/internal/manifest"
	"github.com/codeready-toolchain/interactive-labs-console-plugin/backend/internal/setup"
)

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
	Authenticate func(context.Context, string) (string, error)
	// RuntimeAttributes returns request-scoped attributes merged on top of static ones.
	// nil means no runtime attrs. The subject is the authenticated user identifier.
	// Production default may be nil while the key list is TBD.
	RuntimeAttributes func(r *http.Request, subject string) (map[string]string, error)
	// SetupClient handles SpaceRequest operations for lab setup.
	// nil means setup is unavailable.
	SetupClient *setup.SpaceRequestClient
	// SpaceRequestTierName is the tier used when creating SpaceRequests.
	SpaceRequestTierName string
}

// NewRouter returns an http.Handler with all three MVP routes registered.
func NewRouter(cfg Config) http.Handler {
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
	subject, err := cfg.Authenticate(r.Context(), token)
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
	if subject, err := cfg.Authenticate(r.Context(), token); err != nil || subject == "" {
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
	catalogRef := r.PathValue("catalogRef")
	if cfg.Logger != nil {
		cfg.Logger.Info("setup request received", "catalogRef", catalogRef)
	}

	token := bearerToken(r)
	if token == "" || cfg.Authenticate == nil {
		writeError(w, http.StatusUnauthorized, laberr.New(laberr.TypeUnauthorized, "authentication required", nil))
		return
	}
	subject, err := cfg.Authenticate(r.Context(), token)
	if err != nil || subject == "" {
		writeError(w, http.StatusUnauthorized, laberr.New(laberr.TypeUnauthorized, "authentication failed", nil))
		return
	}

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

	if result.ValuesSchema == nil {
		writeError(w, http.StatusUnprocessableEntity,
			laberr.New(laberr.TypeChartNotFound, "lab has no values schema", nil))
		return
	}

	if cfg.SetupClient == nil {
		writeError(w, http.StatusServiceUnavailable,
			laberr.New(laberr.TypeConfigurationError, "setup client not configured", nil))
		return
	}

	bindings, err := setup.ParseNamespaceBindings(result.ValuesSchema)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity,
			laberr.New(laberr.TypeConfigurationError, "invalid values schema", nil))
		return
	}
	if len(bindings) == 0 {
		writeError(w, http.StatusUnprocessableEntity,
			laberr.New(laberr.TypeConfigurationError, "values schema declares no namespace bindings", nil))
		return
	}

	userNamespace := subject + "-dev"
	tierName := cfg.SpaceRequestTierName
	if tierName == "" {
		tierName = "base1ns"
	}

	for _, b := range bindings {
		if err := cfg.SetupClient.EnsureSpaceRequest(r.Context(), token, userNamespace, b.Key, tierName); err != nil {
			if cfg.Logger != nil {
				cfg.Logger.Error("create SpaceRequest failed", "name", b.Key, "namespace", userNamespace, "err", err)
			}
			writeError(w, http.StatusBadGateway,
				laberr.New(laberr.TypeConfigurationError, "failed to create SpaceRequest", nil))
			return
		}
	}

	writeJSON(w, http.StatusAccepted, map[string]any{
		"apiVersion": "v1alpha1",
		"kind":       "SetupResult",
		"outcome":    "accepted",
		"message":    "Lab setup initiated",
	})
}

func handleSetupStatus(w http.ResponseWriter, r *http.Request, cfg Config) {
	token := bearerToken(r)
	if token == "" || cfg.Authenticate == nil {
		writeError(w, http.StatusUnauthorized, laberr.New(laberr.TypeUnauthorized, "authentication required", nil))
		return
	}
	subject, err := cfg.Authenticate(r.Context(), token)
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

	if result.ValuesSchema == nil {
		writeError(w, http.StatusUnprocessableEntity,
			laberr.New(laberr.TypeChartNotFound, "lab has no values schema", nil))
		return
	}

	bindings, err := setup.ParseNamespaceBindings(result.ValuesSchema)
	if err != nil || len(bindings) == 0 {
		writeError(w, http.StatusUnprocessableEntity,
			laberr.New(laberr.TypeConfigurationError, "invalid or empty values schema", nil))
		return
	}

	if cfg.SetupClient == nil {
		writeError(w, http.StatusServiceUnavailable,
			laberr.New(laberr.TypeConfigurationError, "setup client not configured", nil))
		return
	}

	userNamespace := subject + "-dev"

	type namespaceStatus struct {
		Status string `json:"status"`
		Name   string `json:"name"`
	}
	namespaces := make(map[string]namespaceStatus, len(bindings))
	allReady := true
	anyFailed := false

	for _, b := range bindings {
		sr, err := cfg.SetupClient.GetSpaceRequest(r.Context(), token, userNamespace, b.Key)
		if err != nil {
			if cfg.Logger != nil {
				cfg.Logger.Warn("get SpaceRequest failed", "name", b.Key, "namespace", userNamespace, "err", err)
			}
			namespaces[b.Key] = namespaceStatus{Status: "pending", Name: ""}
			allReady = false
			continue
		}
		if sr == nil {
			namespaces[b.Key] = namespaceStatus{Status: "pending", Name: ""}
			allReady = false
			continue
		}
		if sr.IsReady() && sr.TargetNamespace() != "" {
			if cfg.Logger != nil {
				cfg.Logger.Info("SpaceRequest ready", "name", b.Key, "namespace", sr.TargetNamespace())
			}
			namespaces[b.Key] = namespaceStatus{Status: "ready", Name: sr.TargetNamespace()}
		} else if sr.IsFailed() {
			if cfg.Logger != nil {
				cfg.Logger.Error("SpaceRequest failed", "name", b.Key, "reason", sr.ReadyConditionReason())
			}
			namespaces[b.Key] = namespaceStatus{Status: "failed", Name: ""}
			anyFailed = true
			allReady = false
		} else {
			if cfg.Logger != nil {
				cfg.Logger.Info("SpaceRequest pending", "name", b.Key, "reason", sr.ReadyConditionReason())
			}
			namespaces[b.Key] = namespaceStatus{Status: "pending", Name: ""}
			allReady = false
		}
	}

	overallStatus := "provisioning"
	if anyFailed {
		overallStatus = "failed"
	} else if allReady {
		overallStatus = "ready"
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"apiVersion":      "v1alpha1",
		"kind":            "SetupStatus",
		"status":          overallStatus,
		"namespaces":      namespaces,
		"outcome":         "success",
		"message":         "Lab setup complete",
		"helmReleaseName": setup.HelmReleaseName(subject, catalogRef, bindings),
	})
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
	if cfg.Logger != nil {
		cfg.Logger.Warn("catalog lab omitted", "catalogRef", catalogRef, "reason", reason)
	}
	if cfg.CatalogLabOmitted != nil {
		cfg.CatalogLabOmitted(ctx, catalogRef, reason)
	}
}

func resourcePath(catalogRef, labPath, pagePath string) string {
	resourceURL := url.URL{Path: path.Join("api/v1alpha1/labs", catalogRef, "resources", path.Clean(labPath), "content", pagePath)}
	return resourceURL.EscapedPath()
}
