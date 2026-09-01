# Lab content contract

This document separates current bundle validation from planned renderer/setup semantics. Author only against **implemented and verified** behavior unless explicitly testing an agreed planned story.

## Catalog entry

Lab is selected by administrator-managed `interactive-labs-catalog` ConfigMap. Required entry fields:

| Field | Meaning |
| --- | --- |
| `displayName` | catalog label |
| `repository` | approved HTTPS repository URL |
| `branch` | branch loaded at request time |
| `labPath` | lab root relative to repository |

Optional `description` is catalog text. Optional `attributeOverrides` is static string map. Catalog identity is opaque SHA-256 (`catalogRef`) over repository, branch, lab path, and overrides; duplicate stable identities are configuration errors.

See [catalog ConfigMap contract](../efforts/interactive-labs/contract/catalog-configmap.md).

## Implemented bundle format

Backend expects lab root:

```text
<labPath>/
  lab.yaml
  content/
    **/*.adoc
  setup/helm/Chart.yaml   # optional; only availability is detected today
```

`lab.yaml` must be valid YAML with:

```yaml
apiVersion: v1alpha1
kind: Lab
metadata:
  name: machine-name
  displayName: Human-readable name
spec:
  attributes: {}
  checkpoints: []
```

Backend recursively collects `.adoc` files under `content/`. Bundle key must not escape content root. At least one page must be navigable. Any path segment beginning `_` is include-only, so it cannot be navigation entry. Backend returns page source; it does not currently render, sort/navigation-resolve, or process AsciiDoc includes.

## Attributes

Effective attributes merge in this order; later layer wins:

```text
manifest spec.attributes -> catalog attributeOverrides -> backend runtime attributes
```

Runtime attributes are backend-only, request-scoped, excluded from `CatalogList`, `catalogRef`, and Helm release-name derivation.

## Decided target / planned authoring rules

- Pages/includes resolve only from normalized paths in loaded bundle under `content/`.
- Navigable pages sort lexically; first is entry page; `_` paths remain include-only.
- Render only documented restricted AsciiDoc subset, then sanitize DOM output.
- Reject remote includes, executable extensions, arbitrary filesystem access, unsupported raw HTML, external navigation directives, and arbitrary DOM selectors.
- Guidance links use typed Quick Start-compatible same-Console route plus optional symbolic target ID. Missing/unsupported targets fall back to route-only navigation.
- **Provisional:** `type: manual` browser-local checkpoint is current candidate for user attestation. Evidence/checkpoint rules and section-solver behavior remain open; confirmation is not automated evidence.

## Provisional chart namespace direction

Namespace selection and lifecycle remain open. Current candidate uses explicit existing release-namespace selection and additional `values.schema.json` fields marked:

```yaml
x-interactive-labs:
  kind: namespace-name
```

- JSON property path would be binding key. Standard JSON Schema `title`, `description`, and `required` would drive selector UI.
- Current candidate may preselect accessible current namespace and permit shared bindings; this is not final selection/lifecycle policy.
- Backend revalidates bindings and derives release identity. Intended setup is synchronous, bounded, and Wait-based; no auto-retry or cleanup.

## Not supported or deferred

No author validation tool, resolved namespace policy, resolved evidence/checkpoint rules, section solver, renderer implementation, namespace discovery UI, Helm execution, revision pinning, migration, rollback, or offline content delivery exists today.

Authoring policy and rationale: [design decisions](design-decisions.md); full target detail: [architect notes](../efforts/interactive-labs/architect-notes.md).
