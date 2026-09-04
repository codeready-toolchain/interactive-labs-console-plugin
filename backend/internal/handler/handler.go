// Package handler wires the three MVP backend operations to http.ServeMux routes.
package handler

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"strings"

	corev1 "k8s.io/api/core/v1"

	"github.com/codeready-toolchain/interactive-labs-console-plugin/backend/internal/bundle"
	"github.com/codeready-toolchain/interactive-labs-console-plugin/backend/internal/catalog"
	laberr "github.com/codeready-toolchain/interactive-labs-console-plugin/backend/internal/errors"
	"github.com/codeready-toolchain/interactive-labs-console-plugin/backend/internal/manifest"
)

// Config holds injectable dependencies for the router.
type Config struct {
	// CatalogSource returns the live catalog ConfigMap; nil uses an empty ConfigMap.
	CatalogSource func(context.Context) (*corev1.ConfigMap, error)
	// BundleLoader loads a bundle from the configured repo; nil means bundles are unavailable.
	BundleLoader func(context.Context, string, string, string) (*bundle.Result, error)
	// CatalogLabOmitted receives catalog entry omission notifications.
	CatalogLabOmitted func(ctx context.Context, catalogRef, reason string)
	// Logger receives safe operational fields only; tokens must never be passed to it.
	Logger *slog.Logger
	// Authenticate resolves a forwarded user token to its server-verified subject.
	Authenticate func(context.Context, string) (string, error)
	// RuntimeAttributes returns request-scoped attributes merged on top of static ones.
	// nil means no runtime attrs. The subject is the authenticated user identifier.
	// Production default may be nil while the key list is TBD.
	RuntimeAttributes func(r *http.Request, subject string) map[string]string
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
	mux.HandleFunc("POST /api/v1alpha1/labs/{catalogRef}/setup", func(w http.ResponseWriter, r *http.Request) {
		handleSetup(w, r, cfg)
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

// helmReleaseName derives a deterministic Helm release name:
// "il-" + first 40 hex chars of SHA-256 over canonical JSON.
// Uses static effective attributes (manifest + catalog overrides) — runtime attrs excluded.
func helmReleaseName(subject, repository, labPath, branch string, staticAttrs, namespaceBindings map[string]string) string {
	type canonical struct {
		Attributes        map[string]string `json:"attributes"`
		Branch            string            `json:"branch"`
		LabPath           string            `json:"labPath"`
		NamespaceBindings map[string]string `json:"namespaceBindings"`
		Repository        string            `json:"repository"`
		Subject           string            `json:"subject"`
	}
	b, _ := json.Marshal(canonical{ //nolint:errchkjson
		Attributes:        staticAttrs,
		Branch:            branch,
		LabPath:           labPath,
		NamespaceBindings: namespaceBindings,
		Repository:        repository,
		Subject:           subject,
	})
	sum := sha256.Sum256(b)
	return fmt.Sprintf("il-%x", sum)[:3+40]
}

// mergeAttributes merges attribute maps left-to-right (rightmost wins).
func mergeAttributes(layers ...map[string]string) map[string]string {
	out := make(map[string]string)
	for _, layer := range layers {
		maps.Copy(out, layer)
	}
	return out
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
		items = append(items, item{CatalogRef: e.CatalogRef, DisplayName: result.Manifest.Metadata.DisplayName, Description: result.Manifest.Metadata.Description})
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

	if labErr := manifest.ValidateBundlePaths(keys(result.Pages)); labErr != nil {
		writeError(w, http.StatusUnprocessableEntity, labErr)
		return
	}

	// Merge attributes: manifest → catalog overrides → runtime attrs.
	staticAttrs := mergeAttributes(result.Manifest.Spec.Attributes, entry.AttributeOverrides)
	var runtimeAttrs map[string]string
	if cfg.RuntimeAttributes != nil {
		runtimeAttrs = cfg.RuntimeAttributes(r, subject)
	}
	result.Manifest.Spec.Attributes = mergeAttributes(staticAttrs, runtimeAttrs)

	writeJSON(w, http.StatusOK, map[string]any{
		"apiVersion": "v1alpha1",
		"kind":       "LabBundle",
		"manifest":   result.Manifest,
		"pages":      result.Pages,
		"runtimeContext": map[string]any{
			"catalogRef":         entry.CatalogRef,
			"branch":             entry.Branch,
			"helmChartAvailable": result.HelmChartAvailable,
		},
	})
}

func handleSetup(w http.ResponseWriter, r *http.Request, cfg Config) {
	catalogRef := r.PathValue("catalogRef")
	if cfg.Logger != nil {
		cfg.Logger.Debug("setup request", "catalogRef", catalogRef)
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

	var req struct {
		NamespaceBindings map[string]string `json:"namespaceBindings"`
	}
	if derr := json.NewDecoder(r.Body).Decode(&req); derr != nil {
		writeError(w, http.StatusBadRequest,
			laberr.New(laberr.TypeNamespaceBindingInvalid, "invalid request body", nil))
		return
	}

	if req.NamespaceBindings == nil || req.NamespaceBindings["releaseNamespace"] == "" {
		writeError(w, http.StatusUnprocessableEntity,
			laberr.New(laberr.TypeNamespaceBindingInvalid, "releaseNamespace binding is required", nil))
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

	// Release name uses only static effective attributes — runtime attrs excluded.
	staticAttrs := mergeAttributes(result.Manifest.Spec.Attributes, entry.AttributeOverrides)
	name := helmReleaseName(subject, entry.Repository, entry.LabPath, entry.Branch, staticAttrs, req.NamespaceBindings)

	// ponytail: Helm execution is STORY-4; stub returns success with derived release name.
	writeJSON(w, http.StatusOK, map[string]any{
		"apiVersion":      "v1alpha1",
		"kind":            "SetupResult",
		"outcome":         "success",
		"message":         "Lab setup complete (Helm execution deferred to STORY-4)",
		"helmReleaseName": name,
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

func keys(m map[string]string) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}
