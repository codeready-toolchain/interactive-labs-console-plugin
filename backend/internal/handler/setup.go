package handler

import (
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"slices"

	"github.com/codeready-toolchain/interactive-labs-console-plugin/backend/internal/bundle"
	"github.com/codeready-toolchain/interactive-labs-console-plugin/backend/internal/catalog"
	laberr "github.com/codeready-toolchain/interactive-labs-console-plugin/backend/internal/errors"
	"github.com/codeready-toolchain/interactive-labs-console-plugin/backend/internal/setup"
)

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

	if cfg.SetupManager == nil {
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

func handleSetup(w http.ResponseWriter, r *http.Request, cfg Config) {
	cfg.Logger.Info("setup request received", "catalogRef", r.PathValue("catalogRef"))
	sctx, ok := resolveSetupContext(w, r, cfg)
	if !ok {
		return
	}
	status, ready, err := cfg.SetupManager.Start(r.Context(), sctx.subject, sctx.token, sctx.entry.CatalogRef, sctx.result.HelmChartDir, sctx.bindings)
	if err != nil {
		writeSetupManagerError(w, err)
		return
	}
	if ready {
		writeJSON(w, http.StatusOK, status)
	} else {
		writeJSON(w, http.StatusAccepted, status)
	}
}

func handleSetupStatus(w http.ResponseWriter, r *http.Request, cfg Config) {
	sctx, ok := resolveSetupContext(w, r, cfg)
	if !ok {
		return
	}
	status, err := cfg.SetupManager.Status(r.Context(), sctx.subject, sctx.token, sctx.entry.CatalogRef, sctx.bindings)
	if err != nil {
		writeSetupManagerError(w, err)
		return
	}
	if len(status.Attributes) > 0 {
		cfg.Logger.Info("setup status attributes", "keys", slices.Sorted(maps.Keys(status.Attributes)))
	}
	writeJSON(w, http.StatusOK, status)
}

// Only manager-owned safe errors reach clients; never serialize raw client errors.
func writeSetupManagerError(w http.ResponseWriter, err error) {
	var managerErr *setup.ManagerError
	if !errors.As(err, &managerErr) {
		writeError(w, http.StatusServiceUnavailable, laberr.New(laberr.TypeConfigurationError, "lab setup unavailable", nil))
		return
	}
	status := http.StatusServiceUnavailable
	switch managerErr.Kind {
	case setup.ManagerConflict:
		status = http.StatusConflict
	case setup.ManagerBadRequest:
		status = http.StatusBadRequest
	case setup.ManagerBadGateway:
		status = http.StatusBadGateway
	case setup.ManagerNotFound:
		status = http.StatusNotFound
	case setup.ManagerUnavailable:
		status = http.StatusServiceUnavailable
	}
	writeError(w, status, laberr.New(laberr.TypeConfigurationError, managerErr.Message, nil))
}

func setupIdentity(w http.ResponseWriter, r *http.Request, cfg Config) (string, string, bool) {
	token := bearerToken(r)
	if token == "" || cfg.Authenticate == nil {
		writeError(w, http.StatusUnauthorized, laberr.New(laberr.TypeUnauthorized, "authentication required", nil))
		return "", "", false
	}
	subject, err := cfg.Authenticate(r.Context(), r.Header.Get("Impersonate-User"), token)
	if err != nil || subject == "" {
		writeError(w, http.StatusUnauthorized, laberr.New(laberr.TypeUnauthorized, "authentication failed", nil))
		return "", "", false
	}
	if cfg.SetupManager == nil {
		writeError(w, http.StatusServiceUnavailable, laberr.New(laberr.TypeConfigurationError, "lab setup unavailable", nil))
		return "", "", false
	}
	return subject, token, true
}

func handleSetups(w http.ResponseWriter, r *http.Request, cfg Config) {
	subject, token, ok := setupIdentity(w, r, cfg)
	if !ok {
		return
	}
	items, err := cfg.SetupManager.List(r.Context(), subject, token)
	if err != nil {
		writeSetupManagerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"apiVersion": "v1alpha1", "kind": "SetupList", "items": items})
}

func handleTeardown(w http.ResponseWriter, r *http.Request, cfg Config) {
	w.Header().Set("Cache-Control", "no-store")
	subject, token, ok := setupIdentity(w, r, cfg)
	if !ok {
		return
	}
	ref := r.PathValue("catalogRef")
	if _, err := setup.CatalogRefLabel(ref); err != nil {
		writeError(w, http.StatusBadRequest, laberr.New(laberr.TypeConfigurationError, "invalid catalog identity", nil))
		return
	}
	var body struct {
		Generation string `json:"generation"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil || body.Generation == "" {
		writeError(w, http.StatusBadRequest, laberr.New(laberr.TypeConfigurationError, "teardown requires confirmation of listed setup", nil))
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		writeError(w, http.StatusBadRequest, laberr.New(laberr.TypeConfigurationError, "invalid teardown request", nil))
		return
	}
	progress, err := cfg.SetupManager.BeginTeardown(r.Context(), subject, token, ref, body.Generation)
	if err != nil {
		writeSetupManagerError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, progress)
}

func handleTeardownStatus(w http.ResponseWriter, r *http.Request, cfg Config) {
	w.Header().Set("Cache-Control", "no-store")
	subject, _, ok := setupIdentity(w, r, cfg)
	if !ok {
		return
	}
	if cfg.SetupManager == nil {
		writeSetupManagerError(w, &setup.ManagerError{Kind: setup.ManagerUnavailable, Message: "lab coordination unavailable"})
		return
	}
	progress, err := cfg.SetupManager.TeardownStatus(r.Context(), subject, r.PathValue("catalogRef"), r.URL.Query().Get("operationId"))
	if err != nil {
		writeSetupManagerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, progress)
}
