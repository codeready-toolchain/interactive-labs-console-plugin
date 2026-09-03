# Lab content contract

This document separates current bundle validation from planned renderer/setup semantics. Author only against **implemented and verified** behavior unless explicitly testing an agreed planned story.

## Catalog entry

Lab is selected by administrator-managed `interactive-labs-catalog` ConfigMap. Required entry fields:

| Field | Meaning |
| --- | --- |
| `repository` | approved plain HTTPS repository URL |
| `branch` | approved branch |
| `labPath` | non-empty repository-relative lab root |

**Implemented and verified:** ConfigMap display fields are ignored. Backend derives catalog `displayName` and optional `description` from validated `lab.yaml` `metadata`, and shallow-clones each `(repository, branch)` into `/var/lib/interactive-labs/cache/<sha256 canonical repository+branch>/content` on first use. Entry directory is reserved for future metadata and does not expose source URLs. `Close` preserves snapshots while cache storage remains; no PVC is mounted today, so pod/container replacement loses them. A future PVC mounts this fixed path. A safely configured lab with an unavailable repository, missing manifest, or invalid bundle is omitted from the catalog; unsafe source configuration fails the whole catalog with `configuration_error`. Optional `attributeOverrides` is static string map. Catalog identity is opaque SHA-256 (`catalogRef`) over repository, branch, lab path, and overrides; duplicate stable identities are configuration errors.

**Decided target / planned STORY-4:** Description is AsciiDoc rendered through the restricted, sanitised path, with `lab.yaml` as its relative-resource base. No browser resource endpoint exists yet.

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
  description: Optional catalog description
spec:
  attributes: {}
  checkpoints: []
```

Backend recursively collects `.adoc` files under `content/`. Bundle key must not escape content root. At least one page must be navigable. Any path segment beginning `_` is include-only, so it cannot be navigation entry. Backend returns page source from its immutable snapshot; it does not currently render, sort/navigation-resolve, or process AsciiDoc includes.

## Attributes

Effective attributes merge in this order; later layer wins:

```text
manifest spec.attributes -> catalog attributeOverrides -> backend runtime attributes
```

Runtime attributes are backend-only, request-scoped, excluded from `CatalogList`, `catalogRef`, and Helm release-name derivation.

## Decided target / planned authoring rules

- Bundle lists normalized navigable paths under `labPath/content/` with backend-approved source URLs. Includes and images may resolve outside `labPath` only within same approved repository through backend-validated resource URLs. Relative image targets use Asciidoctor's default flattened-document base: rendered top-level page URL, even when declared in a partial.
- Navigable pages sort lexically; first is entry page; `_` paths remain include-only. Ordinary links navigate only to known normalized lab-page paths in drawer; same-page fragments remain anchors, while unknown/external targets show inline warning. A validated page link activated in catalog description is explicit Launch intent: it validates bundle then opens drawer at target, without bypassing chart setup policy.
- Render only documented restricted AsciiDoc subset, including syntax-highlighted source blocks and Mermaid diagrams. Highlight source from text content; Mermaid runs with strict security, no auto-start or click/callback binding; sanitize generated Mermaid SVG, then sanitize final DOM output.
- Recoverable rejected content/resources and Mermaid/highlighter failures render explicit inline warnings. Page fetch, conversion, or sanitizer failures render safe actionable page error. Remote includes, arbitrary executable extensions, arbitrary filesystem access, unsupported raw HTML or author-supplied SVG, external navigation directives, and arbitrary DOM selectors never execute.
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

No author validation tool, browser page/include/image resource endpoint, resolved namespace policy, resolved evidence/checkpoint rules, section solver, renderer implementation, namespace discovery UI, Helm execution, revision pinning, migration, rollback, or offline content delivery exists today.

Authoring policy and rationale: [design decisions](design-decisions.md); full target detail: [architect notes](../efforts/interactive-labs/architect-notes.md).
