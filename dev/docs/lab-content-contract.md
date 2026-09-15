# Lab content contract

This document distinguishes current implementation from deferred setup semantics. Author only against **implemented and verified** behavior unless explicitly testing an agreed planned story.

## Catalog entry

Lab is selected by administrator-managed `interactive-labs-catalog` ConfigMap. Required entry fields:

| Field | Meaning |
| --- | --- |
| `repository` | approved plain HTTPS repository URL |
| `branch` | approved branch |
| `labPath` | non-empty repository-relative lab root |

**Implemented and verified:** ConfigMap display fields are ignored. Backend derives catalog `displayName` and optional `description` from validated `lab.yaml` `metadata`, and shallow-clones each `(repository, branch)` into `/var/lib/interactive-labs/cache/<sha256 canonical repository+branch>/content` on first use. Entry directory is reserved for future metadata and does not expose source URLs. `Close` preserves snapshots while cache storage remains; no PVC is mounted today, so pod/container replacement loses them. A future PVC mounts this fixed path. A safely configured lab with an unavailable repository, missing manifest, or invalid bundle is omitted from the catalog; unsafe source configuration fails the whole catalog with `configuration_error`. Optional `attributeOverrides` is static string map. Catalog identity is opaque SHA-256 (`catalogRef`) over repository, branch, lab path, and overrides; duplicate stable identities are configuration errors.

**Implemented and verified:** Catalog response exposes normalized `labPath`; description is AsciiDoc rendered through the restricted, sanitised path with client-derived `lab.yaml` relative-resource base. Browser resources use the approved backend resource endpoint.

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

Backend recursively collects `.adoc` files under `content/`. Bundle key must not escape content root. At least one page must be navigable. Any path segment beginning `_` is include-only, so it cannot be navigation entry. Backend returns sorted navigable page descriptors with backend-relative resource paths; browser fetches page source and approved supporting resources on demand. Backend does not render or process AsciiDoc includes.

## Attributes

Effective attributes merge in this order; later layer wins:

```text
manifest spec.attributes -> catalog attributeOverrides -> backend runtime attributes
```

Runtime attributes are backend-only, request-scoped, excluded from `CatalogList`, `catalogRef`, and Helm release-name derivation. Bundle responses are all-or-nothing: backend returns `503 runtime_attributes_unavailable` rather than exposing a bundle if any required value is unavailable or empty.

| Attribute | Source value |
| --- | --- |
| `user` | Authenticated current user's OpenShift `User.metadata.name` from `GET /apis/user.openshift.io/v1/users/~` through Console `UserToken` proxy. |
| `openshift_api_url` | `Infrastructure.status.apiServerURL` from `GET /apis/config.openshift.io/v1/infrastructures/cluster`. |
| `openshift_console_url` | `Console.status.consoleURL` from `GET /apis/config.openshift.io/v1/consoles/cluster`. |
| `openshift_cluster_ingress_domain` | `Ingress.spec.domain` from `GET /apis/config.openshift.io/v1/ingresses/cluster`. |

Backend reads cluster configuration with its service account on each bundle request. Its ClusterRole permits only `get` on those three named `config.openshift.io` `cluster` objects; it never uses forwarded user token for those reads. On failure, backend logs only failed runtime attribute/category, never tokens or attribute values.

## Implemented authoring rules

- Bundle lists normalized navigable paths under `labPath/content/` with backend-relative approved resource paths. Client resolves each only through Console proxy. Backend serves `GET /api/v1alpha1/labs/{catalogRef}/resources/{repositoryRelativePath...}` from cached snapshot, permitting same-repository includes/images outside `labPath`. A quoted snapshot Git `HEAD` SHA is shared resource `ETag`, with `Cache-Control: private, no-cache`; resource-size caps are not yet enforced.
- Navigable pages sort lexically; first is entry page; `_` paths remain include-only. Relative links resolve from active source and navigate drawer only when result matches known page descriptor; same-page fragments remain anchors. Absolute external HTTP(S) links open safe new tab. Same-origin non-plugin-proxy links use Console navigation while drawer stays open. Plugin backend resource URLs and unknown relative links warn. A validated description page link explicitly launches target after bundle validation.
- Render documented restricted AsciiDoc subset with exact pinned dependencies. Backend-final attributes are hard-set in Asciidoctor. DOMPurify's final AsciiDoc pass uses its default allowlist (HTML, SVG, SVG filters, and MathML) and forbids `style`/`form` elements and `style` attributes; it runs before Mermaid insertion. No custom allowlist or hook is configured. Highlight source from text content; Mermaid runs strict with no auto-start or click/callback binding and native SVG labels (`htmlLabels: false`). Its separate DOMPurify SVG/SVG-filter-profile pass forbids `form` and retains allowed `style` elements, `style` attributes, and SVG filter elements. Renderer enforces include depth 64; it adds no include-count or aggregate-content limit.
- Recoverable rejected content/resources and Mermaid/highlighter failures render explicit inline warnings. Page fetch, conversion, or sanitizer failures render safe actionable page error. Remote includes, arbitrary executable extensions, arbitrary filesystem access, unsupported raw HTML or author-supplied SVG, and arbitrary DOM selectors never execute.
- `console:<quickstart-data-id>[label]` accepts only fixed 25-ID Console registry entries; authors supply no route or selector. Route-backed IDs navigate once, then wait up to 10 seconds for their exact visible `data-quickstart-id`; visible-only IDs never navigate. Found targets receive non-modal, dismissible spotlight; route-backed misses remain at fixed route and visible-only misses announce unavailable.
- `resource:<apiVersion>/<Kind>[label,scope=namespaced|cluster,name=...,namespace=...]` requires two or three GVK components (`version/kind` or `group/version/kind`) and enforces scope and the attribute schema, with the label as the first list item and optional named fields following it. `name` and `namespace` accept processor-supplied selector values, for example `resource:project.openshift.io/v1/Project[View *{user}* project,scope=cluster,name={user}-datasphere]`. Cluster scope rejects `namespace`. Every GVK component and nonempty supplied value, including unresolved or malformed attribute text, is URI-encoded into fixed derived namespaced all-namespace/namespace/name-filter/details or cluster list/details Console paths; empty supplied selector values are rejected. Console resolves resource existence. The label uses supported restricted AsciiDoc inline formatting and runtime attribute substitution. No author-controlled plural, route, selector, scope path, filter syntax, or URL is accepted; interpolation or passthrough never applies to author-controlled URLs. Resource links use normal same-origin navigation and retain drawer.
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

No author validation tool, resolved namespace policy, resolved evidence/checkpoint rules, section solver, namespace discovery UI, Helm execution, revision pinning, migration, rollback, or offline content delivery exists today.

Authoring policy and rationale: [design decisions](design-decisions.md); full target detail: [architect notes](../efforts/interactive-labs/architect-notes.md).
