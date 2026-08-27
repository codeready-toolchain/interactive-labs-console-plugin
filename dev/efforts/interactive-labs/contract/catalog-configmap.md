# Catalog ConfigMap Contract

**ConfigMap name:** `interactive-labs-catalog`  
**Namespace:** `interactive-labs-console-plugin`

The ConfigMap must contain a single key `catalog.yaml` whose value is a top-level YAML sequence. Each item in the sequence is one lab entry. The declared sequence order is the order returned by the `CatalogList` API.

## Derived `catalogRef`

Each entry has no explicit `id` field. The backend derives an opaque, URL-safe `catalogRef` from the stable identity fields: `repository`, `branch`, `labPath`, and `attributeOverrides`. The ref is the SHA-256 hex digest of canonical JSON over those four fields. Display fields (`displayName`, `description`) and request-runtime attributes are excluded from the hash.

`catalogRef` is returned in `CatalogList` items and in `runtimeContext.catalogRef` of `LabBundle`. It is used as the `{catalogRef}` path segment in lab API URLs.

## Required fields (per item)

| Field | Type | Description |
|---|---|---|
| `displayName` | string | Human-readable lab name shown in catalog list |
| `repository` | string | HTTPS URL of the approved Red Hat lab repository |
| `branch` | string | Branch to load lab content from at request time |
| `labPath` | string | Path within the repository to the lab root directory |

## Optional fields (per item)

| Field | Type | Description |
|---|---|---|
| `description` | string | Short description shown in catalog list |
| `attributeOverrides` | map[string]string | Static key/value pairs merged on top of manifest attributes (catalog wins over manifest; runtime wins over catalog). Included in `catalogRef` hash — different overrides produce a different ref. |

## Attribute precedence

Final merged attributes seen by consumers: `manifest.spec.attributes` ← `attributeOverrides` ← runtime attributes (request-scoped, backend-only). Runtime attributes are **not** exposed in `CatalogList` and do not affect `catalogRef` or Helm release-name derivation.

## Example

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: interactive-labs-catalog
  namespace: interactive-labs-console-plugin
data:
  catalog.yaml: |
    - displayName: "My Lab"
      description: "Optional description"
      repository: "https://github.com/redhat-scholars/my-lab"
      branch: "main"
      labPath: "lab/"
    - displayName: "Another Lab"
      repository: "https://github.com/redhat-scholars/another-lab"
      branch: "stable"
      labPath: "."
      attributeOverrides:
        clusterVersion: "4.17"
```

## Validation rules

- Missing `catalog.yaml` key → `configuration_error`
- Empty or whitespace-only `catalog.yaml` value → empty catalog (same as `[]`)
- Invalid YAML → `configuration_error`
- Missing or empty `displayName`, `repository`, `branch`, or `labPath` → `configuration_error`
- Two entries with identical `repository`, `branch`, `labPath`, and `attributeOverrides` → `configuration_error` (duplicate `catalogRef`)
- ConfigMap size limit: 1 MiB (Kubernetes default)
