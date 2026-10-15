// Package handler wires backend operations to http.ServeMux routes.
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
	// ResourceLoader reads an approved resource from the configured source snapshot.
	ResourceLoader func(context.Context, string, string, string) (*bundle.Resource, error)
	// RepositoryRefresher synchronously refreshes an approved source snapshot.
	// Temporary testing control: remove this dependency and route for production.
	RepositoryRefresher bundle.RepositoryRefresher
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
	// SetupManager owns the setup, status, inventory, and teardown lifecycle.
	SetupManager *setup.Manager
	// ReadinessCheck reports whether the server is ready to handle traffic.
	// nil means always ready.
	ReadinessCheck func() bool
}

// NewRouter returns an http.Handler with the backend routes registered.
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
	mux.HandleFunc("POST /api/v1alpha1/labs/{catalogRef}/resync", func(w http.ResponseWriter, r *http.Request) {
		handleResync(w, r, cfg)
	})
	mux.HandleFunc("POST /api/v1alpha1/labs/{catalogRef}/setup", func(w http.ResponseWriter, r *http.Request) {
		handleSetup(w, r, cfg)
	})
	mux.HandleFunc("GET /api/v1alpha1/labs/{catalogRef}/setup/status", func(w http.ResponseWriter, r *http.Request) {
		handleSetupStatus(w, r, cfg)
	})
	mux.HandleFunc("GET /api/v1alpha1/setups", func(w http.ResponseWriter, r *http.Request) {
		handleSetups(w, r, cfg)
	})
	mux.HandleFunc("POST /api/v1alpha1/labs/{catalogRef}/teardown", func(w http.ResponseWriter, r *http.Request) {
		handleTeardown(w, r, cfg)
	})
	mux.HandleFunc("GET /api/v1alpha1/labs/{catalogRef}/teardown/status", func(w http.ResponseWriter, r *http.Request) {
		handleTeardownStatus(w, r, cfg)
	})

	mux.HandleFunc("GET /api/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /api/readyz", func(w http.ResponseWriter, _ *http.Request) {
		if cfg.ReadinessCheck != nil && !cfg.ReadinessCheck() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := setup.ContextWithLogger(r.Context(), cfg.Logger)
		mux.ServeHTTP(w, r.WithContext(ctx))
	})
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

// handleResync is a temporary global-impact testing control, not a lab lifecycle operation.
func handleResync(w http.ResponseWriter, r *http.Request, cfg Config) {
	w.Header().Set("Cache-Control", "no-store")
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
	entry, labError := catalogEntryByCatalogRef(r.Context(), cfg, r.PathValue("catalogRef"))
	if labError != nil {
		status := http.StatusNotFound
		switch labError.ErrorType {
		case laberr.TypeCatalogUnavailable:
			status = http.StatusServiceUnavailable
		case laberr.TypeConfigurationError:
			status = http.StatusInternalServerError
		}
		writeError(w, status, labError)
		return
	}
	if cfg.RepositoryRefresher == nil {
		writeError(w, http.StatusServiceUnavailable, laberr.New(laberr.TypeRepositoryError, "repository refresh unavailable", nil))
		return
	}
	err = cfg.RepositoryRefresher(r.Context(), bundle.RepositoryRefreshRequest{Repository: entry.Repository, Branch: entry.Branch})
	if err == nil {
		err = r.Context().Err()
	}
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, context.DeadlineExceeded) {
			status = http.StatusGatewayTimeout
		} else if errors.Is(err, context.Canceled) {
			status = http.StatusRequestTimeout
		}
		writeError(w, status, laberr.New(laberr.TypeRepositoryError, "failed to resync repository", nil))
		return
	}
	w.WriteHeader(http.StatusNoContent)
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
