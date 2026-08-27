export type LabErrorType =
  | "catalog_unavailable"
  | "configuration_error"
  | "unauthorized"
  | "lab_not_found"
  | "manifest_version_unsupported"
  | "repository_error"
  | "path_traversal"
  | "content_error"
  | "namespace_binding_invalid"
  | "rbac_denied"
  | "chart_not_found"
  | "setup_conflict"
  | "unknown_error";

const KNOWN_ERROR_TYPES = new Set<string>([
  "catalog_unavailable",
  "configuration_error",
  "unauthorized",
  "lab_not_found",
  "manifest_version_unsupported",
  "repository_error",
  "path_traversal",
  "content_error",
  "namespace_binding_invalid",
  "rbac_denied",
  "chart_not_found",
  "setup_conflict",
  "unknown_error",
]);

export function isKnownErrorType(t: unknown): t is LabErrorType {
  return typeof t === "string" && KNOWN_ERROR_TYPES.has(t);
}

export interface LabError {
  apiVersion: "v1alpha1";
  kind: "Error";
  type: LabErrorType;
  message: string;
  detail?: unknown;
}
