// Package errors defines the typed error envelope shared across all backend handlers.
package errors

const (
	TypeCatalogUnavailable           = "catalog_unavailable"
	TypeConfigurationError           = "configuration_error"
	TypeUnauthorized                 = "unauthorized"
	TypeLabNotFound                  = "lab_not_found"
	TypeManifestVersionUnsupported   = "manifest_version_unsupported"
	TypeRepositoryError              = "repository_error"
	TypePathTraversal                = "path_traversal"
	TypeContentError                 = "content_error"
	TypeNamespaceBindingInvalid      = "namespace_binding_invalid"
	TypeRBACDenied                   = "rbac_denied"
	TypeChartNotFound                = "chart_not_found"
	TypeSetupConflict                = "setup_conflict"
	TypeRuntimeAttributesUnavailable = "runtime_attributes_unavailable"
)

// LabError is the typed error response body. All non-2xx responses use this shape.
type LabError struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	ErrorType  string `json:"type"`
	Message    string `json:"message"`
	Detail     any    `json:"detail,omitempty"`
}

// New constructs a LabError. message must never contain token material.
func New(errorType, message string, detail any) *LabError {
	return &LabError{
		APIVersion: "v1alpha1",
		Kind:       "Error",
		ErrorType:  errorType,
		Message:    message,
		Detail:     detail,
	}
}
