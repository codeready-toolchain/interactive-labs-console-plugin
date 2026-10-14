# Lab content contract

This document describes current lab authoring and bundle behavior. Start with the [lab author guide and runnable sample](../../examples/sample-lab/README.md); see [lab setup design](lab-setup-design.md) for chart-backed setup.

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

Backend expects this layout (chart directories are optional):

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
  setup/helm/Chart.yaml           # optional lab-local default chart
  setup/helm/values.schema.json   # optional schema beside chart
<repository-root>/setup/helm/Chart.yaml  # optional fallback chart
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
  setupDir: ../../shared/chart     # optional direct chart-directory override
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

## Prerequisite chart selection

`spec.setupDir` is an optional string naming the **chart directory itself**, relative to the lab directory, not a parent containing `helm/`. For `labPath: labs/intro`, `setupDir: ../../shared/chart` selects `<repository-root>/shared/chart`; `setupDir: setup/helm` selects the lab-local chart. An omitted or empty string uses defaults:

1. Nonempty `spec.setupDir` is authoritative. A missing or invalid target invalidates the lab; no fallback occurs.
2. Otherwise select `<labPath>/setup/helm` when that directory exists.
3. Only when the lab-local directory is absent, try `<repository-root>/setup/helm`.

Neither implicit directory present means a content-only lab. A present but invalid selected directory/chart is never treated as content-only and never triggers fallback. Backend validates the selected chart with its installed Helm loader; invalid chart configuration invalidates bundle loading and uses existing catalog omission/Warning Event handling.

Parent traversal is allowed only inside the approved repository snapshot. Absolute paths, absolute URLs, backslashes, control characters, encoded-looking `%xx` text, repository escapes, and symlinks are rejected. Checks include every path segment before normalization and the entire selected chart tree, including chart metadata, schema, templates, subchart directories, dangling links, and files excluded by `.helmignore`. Browser content/resource path rules do not change.

Chart and `values.schema.json` always come from the same selected directory. Shared `chart` owns selection, safe path/tree validation, Helm-loader validation, and optional schema decoding. Schema is optional for bundle loading; when present it must be a readable JSON object with correctly typed supported property-tree and annotation fields (for example, `x-interactive-labs.kind: 42` is rejected). This validates structure, not full JSON Schema semantics. Unknown fields/kinds remain allowed. Bundle validates this contract early without importing provisioning code; an absent schema or `{}` is valid bundle content. `setup` interprets decoded annotations and separately requires a schema declaring at least one `namespace-name` binding, preserving existing setup error mapping and namespace/reference policy.

Helm release identity includes normalized repository-relative **effective chart path**, authenticated user, unchanged `catalogRef`, and sorted namespace binding keys. Equivalent relative spellings, absolute snapshot locations, revisions, and chart contents do not affect identity. Labs may share chart files but keep separate installed releases per user/catalog identity. Changing the effective chart path does not trigger automatic migration or cleanup. A compatible, verified existing same-lab release remains authoritative for reopening and polling; replacing it requires explicitly confirmed teardown.

## Attributes

Effective attributes merge in this order; later layer wins:

```text
lab.yaml spec.attributes -> catalog entry attributeOverrides -> backend runtime attributes
```

Authors define string values in the lab's `lab.yaml` `spec.attributes` map. Administrators set optional string-valued overrides under `attributeOverrides` on each catalog entry inside the ConfigMap's `data.catalog.yaml` sequence—not as top-level ConfigMap data. See the [administrator guide](administrator-guide.md#configure-catalog) for a catalog entry example.

For example, `lab.yaml` can contain:

```yaml
spec:
  attributes:
    lab-title: "Sample lab"
    greeting: "Hello"
```

An AsciiDoc page can use `{greeting}, welcome to {lab-title}.`; a same-named catalog override replaces the manifest value. These attributes affect rendered content only: overriding `lab-title` does not change `metadata.title` or the catalog display title.

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
- Render documented restricted AsciiDoc subset with exact pinned dependencies. Bundle attributes, overlaid by successful setup attributes, are supplied as Asciidoctor soft settings (`name@`); document header declarations can override them. DOMPurify's final AsciiDoc pass uses its default allowlist (HTML, SVG, SVG filters, and MathML) and forbids `style`/`form` elements and `style` attributes; it runs before Mermaid insertion. No custom allowlist or hook is configured. Highlight source from text content; Mermaid runs strict with no auto-start or click/callback binding and native SVG labels (`htmlLabels: false`). Its separate DOMPurify SVG/SVG-filter-profile pass forbids `form` and retains allowed `style` elements, `style` attributes, and SVG filter elements. Renderer enforces include depth 64; it adds no include-count or aggregate-content limit.
- Recoverable rejected content/resources and Mermaid/highlighter failures render explicit inline warnings. Page fetch, conversion, or sanitizer failures render safe actionable page error. Remote includes, arbitrary executable extensions, arbitrary filesystem access, unsupported raw HTML or author-supplied SVG, and arbitrary DOM selectors never execute.
- `console:<quickstart-data-id>[label]` accepts only fixed 25-ID Console registry entries; authors supply no route or selector. Route-backed IDs navigate once, then wait up to 10 seconds for their exact visible `data-quickstart-id`; visible-only IDs never navigate. Found targets receive non-modal, dismissible spotlight; route-backed misses remain at fixed route and visible-only misses announce unavailable.
- `resource:<apiVersion>/<Kind>[label,scope=namespaced|cluster,name=...,namespace=...]` requires two or three GVK components (`version/kind` or `group/version/kind`) and enforces scope and the attribute schema, with the label as the first list item and optional named fields following it. `name` and `namespace` accept processor-supplied selector values, for example `resource:project.openshift.io/v1/Project[View *{user}* project,scope=cluster,name={user}-datasphere]`. Cluster scope rejects `namespace`. Every GVK component and nonempty supplied value, including unresolved or malformed attribute text, is URI-encoded; empty supplied selector values are rejected. For namespaced resources, omitted namespace defaults to the active Console project; All Projects or an empty selection keeps the all-namespaces route, while authored namespace takes precedence. A name without authored namespace remains a `?name=`-filtered kind list, scoped to the active project when selected, not a detail page; authored namespace plus name opens namespaced details. Cluster scope is unaffected by project selection. Active-project changes rerender resource links in both drawer content and catalog descriptions. Console resolves resource existence. The label uses supported restricted AsciiDoc inline formatting and runtime attribute substitution. No author-controlled plural, route, selector, scope path, filter syntax, or URL is accepted; interpolation or passthrough never applies to author-controlled URLs. Resource links use normal same-origin navigation and retain drawer.
- Module-local `type: manual` checkpoints are inert authoring data. Browser-local attestation remains a provisional idea; no checkpoint UI or completion claim exists. Evidence/checkpoint rules and section-solver behavior remain open.

## Current chart bindings and provisional namespace direction

Namespace selection and lifecycle policy remain open. Current setup already uses `values.schema.json` fields marked:

```yaml
x-interactive-labs:
  kind: namespace-name
```

- Dot-separated JSON property path is the Helm values path. The namespace binding key is its leaf property name, except a path ending `.namespace` uses the path with that suffix removed.
- Current setup creates/reuses a SpaceRequest for each binding key; there is no namespace selector driven by `title`, `description`, or `required`. Explicit existing-namespace selection remains a proposal, not current behavior.
- Chart-backed setup starts an asynchronous Helm worker; successful status exposes namespace, generated-password, and root-url attributes with path dots replaced by hyphens. See [lab setup design](lab-setup-design.md). No automatic cleanup or cancellation.

## Not supported or deferred

No author validation tool, settled namespace policy, completion evidence/checkpoint UI, section solver, revision pinning, automatic migration, rollback, or offline content delivery exists today. **Breaking `v1alpha1` cutover:** convert old flat labs before rollout. Invalid cached old content is safely omitted from catalog until a successful refresh replaces it or cache is lost. No session migration or upstream-change reconciliation.

Authoring policy and rationale: [design decisions](design-decisions.md); full target detail: [architect notes](../efforts/interactive-labs/architect-notes.md).
