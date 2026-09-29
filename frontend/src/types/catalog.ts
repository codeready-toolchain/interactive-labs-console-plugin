export interface CatalogEntry {
  catalogRef: string;
  title: string;
  description?: string;
  labPath: string;
}

export interface CatalogList {
  apiVersion: "v1alpha1";
  kind: "CatalogList";
  items: CatalogEntry[];
}

export interface CatalogError {
  apiVersion: "v1alpha1";
  kind: "Error";
  type: "catalog_unavailable" | "configuration_error" | "unauthorized";
  message: string;
  detail?: unknown;
}
