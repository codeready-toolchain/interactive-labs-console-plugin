# Lab content contract

This document describes current lab authoring and bundle behavior. See [lab setup design](lab-setup-design.md) for chart-backed setup.

## Catalog entry

Lab is selected by administrator-managed `interactive-labs-catalog` ConfigMap. Required entry fields:

| Field | Meaning |
| --- | --- |
| `repository` | approved plain HTTPS repository URL |
| `branch` | approved branch |
| `labPath` | non-empty repository-relative lab root |

**Implemented and verified:** ConfigMap display fields are ignored. Backend derives catalog `title` and optional `description` from validated `lab.yaml` `metadata`, stages each `(repository, branch)` source into `/var/lib/interactive-labs/cache/<sha256 canonical repository+branch>`, and atomically replaces its snapshot generation after source and configured lab-path validation. `Close` preserves snapshots while cache storage remains; no PVC is mounted today, so pod/container replacement loses them. A future PVC mounts this fixed path. A safely configured lab with an unavailable repository, missing manifest, or invalid bundle is omitted from the catalog; unsafe source configuration fails the whole catalog with `configuration_error`. Optional `attributeOverrides` is static string map. Catalog identity is opaque SHA-256 (`catalogRef`) over repository, branch, lab path, and overrides; duplicate stable identities are configuration errors.

**Implemented and verified:** Catalog response exposes normalized `labPath`; description is AsciiDoc rendered through the restricted, sanitised path with client-derived `lab.yaml` relative-resource base. Browser resources use the approved backend resource endpoint.

See [catalog ConfigMap contract](../efforts/interactive-labs/contract/catalog-configmap.md).

## Implemented bundle format

Backend expects this layout (`setup/helm/` belongs to repository root and is shared by its labs):

```text
<labPath>/
  lab.yaml
  modules/
    010-intro/
      module.yaml                 # optional
      content/
        010-start.adoc
        _partials/notes.adoc      # include-only
    020-apply/
      content/
        010-apply.adoc
<repository-root>/setup/helm/Chart.yaml  # optional chart
```

`lab.yaml` is strict YAML; only these fields are supported:

```yaml
apiVersion: v1alpha1
kind: Lab
metadata:
  title: Human-readable lab title
  description: Optional catalog description
spec:                              # optional
  attributes: {}                   # optional map of strings
```

`metadata.title` must be nonempty. Unknown or old fields (including `metadata.name`, `metadata.displayName`, and root `spec.checkpoints`) are rejected, not ignored. The optional description is displayed in catalog, not as module/drawer metadata.

`modules/` must contain at least one immediate child directory and no files or symlinks. Its literal name is module ID and must match `[A-Za-z0-9][A-Za-z0-9._-]*`; zero-padded names give predictable order. Each module needs `content/` with at least one navigable `.adoc`. Backend recurses through each content tree; any `_`-leading path segment makes an `.adoc` include-only, still available as a resource but excluded from navigation. Module IDs and navigable content-relative paths sort bytewise. `.adoc` content paths cannot contain backslashes, control characters, or literal percent-escape sequences (`%xx`); reserved `#` and `?` in filenames are supported through encoded resource URLs. Symlinks and nonregular files in a module invalidate the lab.

An optional `modules/<id>/module.yaml` accepts only top-level `checkpoints` (required array when file exists; empty allowed):

```yaml
checkpoints:
  - id: start
    type: manual
    pagePath: 010-start.adoc
    label: Read introduction
```

Each checkpoint needs nonempty string `id`, `type: manual`, `pagePath`, and `label`. Backend normalizes `pagePath` relative to **that module's** `content/` (`010-start.adoc` or `pages/010-start.adoc`); it must match a navigable page, never an include-only, missing, traversing, or other-module page. IDs must be unique per module; they may repeat in different modules. Checkpoints are validated authoring data only: no checkpoint UI, attestation, completion, persistence, or checkpoint field in bundle response. Evidence and manual-checkpoint semantics remain open.

`GET /api/v1alpha1/labs/{catalogRef}/bundle` returns `apiVersion: v1alpha1`, `kind: LabBundle`, validated `manifest` (with effective attributes), `runtimeContext`, and **only** `modules: [{id, pages: [{path, url}]}]`, never flat `pages`. Each path is lab-relative, e.g. `modules/010-intro/content/010-start.adoc`; its backend-relative URL targets `.../resources/<labPath>/modules/010-intro/content/010-start.adoc` (omit `<labPath>/` when `labPath: .`). Backend does not extract page titles or process AsciiDoc includes. Browser validates modules, paths, and exact approved proxy URLs before launch.

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

- Bundle groups navigable pages by module with module-qualified lab-relative paths and backend-relative approved resource paths. Client resolves each only through Console proxy. Backend serves `GET /api/v1alpha1/labs/{catalogRef}/resources/{repositoryRelativePath...}` from cached snapshot, permitting same-repository includes/images outside `labPath`. A quoted snapshot Git `HEAD` SHA is shared resource `ETag`, with `Cache-Control: private, no-cache`; resource-size caps are not yet enforced. Refresh is atomic; `catalogRef` stays stable and browser receives neither update notification nor revision pin.
- First page of first sorted module is entry page. A native module dropdown appears only for multiple modules, listing modules (not pages). Initially it shows literal directory IDs; after setup, first navigable page title for each module is fetched in background, parsed with effective attributes, sanitized to plain text, and replaces ID when available. Failed/empty titles keep ID. Selection opens that module's first page; Previous/Next cross module boundaries while `Page j of n` counts pages in active module. Only `{currentPagePath, panelMode}` is saved per `catalogRef`; explicit relaunch restores a still-valid page or starts at entry, never auto-opens.
- Navigable pages sort lexically; first is entry page; `_` paths remain include-only. Relative links resolve from active source and navigate drawer only when result matches known page descriptor; same-page fragments remain anchors. Absolute external HTTP(S) links open safe new tab. Same-origin non-plugin-proxy links use Console navigation while drawer stays open. Plugin backend resource URLs and unknown relative links warn. A validated description link to `modules/<id>/content/...` explicitly launches target after bundle validation and chart setup.
- Render documented restricted AsciiDoc subset with exact pinned dependencies. Backend-final attributes are hard-set in Asciidoctor. DOMPurify's final AsciiDoc pass uses its default allowlist (HTML, SVG, SVG filters, and MathML) and forbids `style`/`form` elements and `style` attributes; it runs before Mermaid insertion. No custom allowlist or hook is configured. Highlight source from text content; Mermaid runs strict with no auto-start or click/callback binding and native SVG labels (`htmlLabels: false`). Its separate DOMPurify SVG/SVG-filter-profile pass forbids `form` and retains allowed `style` elements, `style` attributes, and SVG filter elements. Renderer enforces include depth 64; it adds no include-count or aggregate-content limit.
- Recoverable rejected content/resources and Mermaid/highlighter failures render explicit inline warnings. Page fetch, conversion, or sanitizer failures render safe actionable page error. Remote includes, arbitrary executable extensions, arbitrary filesystem access, unsupported raw HTML or author-supplied SVG, and arbitrary DOM selectors never execute.
- `console:<quickstart-data-id>[label]` accepts only fixed 25-ID Console registry entries; authors supply no route or selector. Route-backed IDs navigate once, then wait up to 10 seconds for their exact visible `data-quickstart-id`; visible-only IDs never navigate. Found targets receive non-modal, dismissible spotlight; route-backed misses remain at fixed route and visible-only misses announce unavailable.
- `resource:<apiVersion>/<Kind>[label,scope=namespaced|cluster,name=...,namespace=...]` requires two or three GVK components (`version/kind` or `group/version/kind`) and enforces scope and the attribute schema, with the label as the first list item and optional named fields following it. `name` and `namespace` accept processor-supplied selector values, for example `resource:project.openshift.io/v1/Project[View *{user}* project,scope=cluster,name={user}-datasphere]`. Cluster scope rejects `namespace`. Every GVK component and nonempty supplied value, including unresolved or malformed attribute text, is URI-encoded into fixed derived namespaced all-namespace/namespace/name-filter/details or cluster list/details Console paths; empty supplied selector values are rejected. Console resolves resource existence. The label uses supported restricted AsciiDoc inline formatting and runtime attribute substitution. No author-controlled plural, route, selector, scope path, filter syntax, or URL is accepted; interpolation or passthrough never applies to author-controlled URLs. Resource links use normal same-origin navigation and retain drawer.
- Module-local `type: manual` checkpoints are inert authoring data. Browser-local attestation remains a provisional idea; no checkpoint UI or completion claim exists. Evidence/checkpoint rules and section-solver behavior remain open.

## Provisional chart namespace direction

Namespace selection and lifecycle remain open. Current candidate uses explicit existing release-namespace selection and additional `values.schema.json` fields marked:

```yaml
x-interactive-labs:
  kind: namespace-name
```

- JSON property path would be binding key. Standard JSON Schema `title`, `description`, and `required` would drive selector UI.
- Current candidate may preselect accessible current namespace and permit shared bindings; this is not final selection/lifecycle policy.
- This existing-namespace selection proposal is not current setup behavior. Chart-backed setup currently creates SpaceRequests and starts an asynchronous Helm worker; see [lab setup design](lab-setup-design.md). No automatic cleanup or cancellation.

## Not supported or deferred

No author validation tool, settled namespace policy, completion evidence/checkpoint UI, section solver, revision pinning, automatic migration, rollback, or offline content delivery exists today. **Breaking `v1alpha1` cutover:** convert old flat labs before rollout. Cached old snapshots are not auto-refreshed; invalid old content is safely omitted from catalog until cache is replaced or lost. No session migration or upstream-change reconciliation.

Authoring policy and rationale: [design decisions](design-decisions.md); full target detail: [architect notes](../efforts/interactive-labs/architect-notes.md).
