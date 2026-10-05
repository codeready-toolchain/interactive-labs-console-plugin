# Architecture

## Runtime topology

```text
OpenShift Console user
  -> Interactive Labs dynamic plugin
  -> consoleFetch UserToken proxy
  -> HTTPS backend service (port 9001)
     -> catalog ConfigMap
     -> approved HTTPS Git repository
     -> OpenShift user API / current-user RBAC
```

One container image contains frontend assets and Go backend. One HTTPS Service exposes both. Backend routes `/api/` requests to its API handler; all other requests serve frontend assets.

Evidence: [Containerfile](../../Containerfile), [server](../../backend/cmd/server/main.go), [ConsolePlugin](../../deploy/base/console-plugin.yaml).

## Ownership and trust boundary

| Owner | Responsibilities | Must not do |
| --- | --- | --- |
| Console | authentication context, routing, active namespace, service proxy | grant backend a privileged user identity |
| Browser plugin | catalog UI, drawer, module/page navigation, browser-local panel state; any future resolved progress model | clone repositories, execute Helm, decide authorization, persist tokens, or unilaterally choose namespace policy |
| Backend | catalog resolution, authentication, bundle validation/loading, setup authorization and release identity | log, persist, echo, or return forwarded tokens |
| Cluster administrator | install plugin and select trusted labs through catalog ConfigMap | place unapproved browser-controlled repository locations in catalog |
| Lab author | author supported lab content and optional chart | use unsupported rendering or execution capabilities |

## Implemented request flows

### Catalog

1. `InteractiveLabsPage` calls `listCatalog()`.
2. `consoleFetch` sends `GET /api/proxy/plugin/interactive-labs-console-plugin/backend/api/v1alpha1/catalog`.
3. Backend validates source locators, derives each opaque `catalogRef`, resolves each lab from its cached approved snapshot, and returns manifest `title`, optional `description`, and normalized `labPath` in declared order. Invalid flat or malformed module labs are omitted safely.
4. An unavailable repository, missing manifest, or invalid lab omits only that item. Backend logs safe opaque context and emits one deduplicated namespaced Warning Event per `(catalogRef, reason)` for its lifetime.

### Catalog configuration refresh

**Implemented and verified:** A client-go informer watches only the named `interactive-labs-catalog` ConfigMap in the configured namespace. Create, update, and delete signal a coalescing coordinator; sweeps do not overlap, and changes during a sweep cause one follow-up. Every sweep performs a fresh ConfigMap `Get` and validates the full catalog; event payloads are not refresh configuration. Startup and periodic sweeps remain, and the informer reconnects/re-lists after interruption. Deletion fails safely without evicting snapshots; recreation can trigger recovery. No browser notification is added.

### Bundle

1. Browser requests `GET /api/v1alpha1/labs/{catalogRef}/bundle` through same proxy.
2. Backend requires bearer token, resolves authenticated subject, then resolves `catalogRef` from live catalog.
3. **Implemented and verified:** On first use, backend shallow-clones one configured HTTPS repository/branch into `/var/lib/interactive-labs/cache/<sha256 canonical repository+branch>/content` and reuses that snapshot for catalog, bundle, setup, and resource loading while cache storage remains. Entry directory is reserved for future metadata. `Close` preserves snapshots; current deployment has no PVC, so pod/container replacement loses cache. Future PVCs mount this fixed path. It loads `lab.yaml`, each `modules/<id>/content/` tree, and optional repository-root `setup/helm/Chart.yaml`; bundle returns ordered `modules: [{id, pages: [{path, url}]}]` with module-qualified lab-relative paths and backend-relative resource URLs, never root `pages`.
4. **Implemented and verified:** Catalog returns normalized `labPath`; browser checks each module ID, navigable path, and URL against that lab path and fixed Console proxy base before using it. `GET /api/v1alpha1/labs/{catalogRef}/resources/{repositoryRelativePath...}` serves approved page/include/image bytes on demand with repository-root validation. Responses use a quoted snapshot Git `HEAD` SHA as shared `ETag` and `Cache-Control: private, no-cache`; resource-size caps are not yet enforced.

### Setup

Chart-backed launch calls `POST /api/v1alpha1/labs/{catalogRef}/setup` with no namespace body. Backend authenticates forwarded current user, creates/reuses SpaceRequests in user's home namespace, and starts a background worker to wait for readiness and install repository-root Helm chart with current-user RBAC. `POST` returns `202` with `v1alpha1/SetupStatus` (or `200` when release already deployed). Frontend polls `GET /api/v1alpha1/labs/{catalogRef}/setup/status` and opens content after SpaceRequests are ready and Helm is deployed. Worker is bounded but can be lost on process restart; no automatic cleanup/cancellation. See [lab setup design](lab-setup-design.md).

Evidence: [frontend client](../../frontend/src/api/client.ts), [router](../../backend/internal/handler/handler.go), [bundle loader](../../backend/internal/bundle/loader.go).

## API contracts

| Operation | Current route | Result |
| --- | --- | --- |
| List catalog | `GET /api/v1alpha1/catalog` | `v1alpha1/CatalogList` |
| Load bundle | `GET /api/v1alpha1/labs/{catalogRef}/bundle` | `v1alpha1/LabBundle` |
| Read resource | `GET /api/v1alpha1/labs/{catalogRef}/resources/{repositoryRelativePath...}` | Raw supported text or raster bytes; `v1alpha1/Error` on failure |
| Start setup | `POST /api/v1alpha1/labs/{catalogRef}/setup` | `v1alpha1/SetupStatus` |
| Check setup | `GET /api/v1alpha1/labs/{catalogRef}/setup/status` | `v1alpha1/SetupStatus` |

All non-success API responses use `v1alpha1/Error`. Unknown client error types normalize to `unknown_error`.

## Deployment

- `deploy/base` is Kustomize base; `deploy/demo` replaces catalog with demo entries.
- Service requests OpenShift serving certificate and backend mounts its TLS secret.
- ConsolePlugin defines `backend` proxy alias with mandatory `UserToken` authorization.
- One backend replica runs as non-root with all Linux capabilities dropped, privilege escalation disabled, and RuntimeDefault seccomp.
- Backend service account has `get`, `list`, and `watch` access to the named catalog ConfigMap (list/watch require `metadata.name` field selector). It also has `create` access to namespaced Events and `get` access to named `config.openshift.io` `cluster` infrastructure, ingress, and console configuration objects for runtime attributes.

See [administrator guide](administrator-guide.md).

## Current and planned flows

- Enabled launch opens accessible, minimizable drawer and restores browser-local session state only after explicit user activation.
- **Implemented and verified:** Restricted AsciiDoc uses exact pinned renderer dependencies. DOMPurify's final AsciiDoc pass uses its default allowlist and forbids `style`/`form` elements and `style` attributes; Mermaid SVG receives a separate SVG/SVG-filter-profile pass that retains allowed styling and is inserted afterward. Renderer passes backend-final attributes hard-set to Asciidoctor. Relative links resolve from source URL and navigate only known module-qualified pages (including cross-module links); external HTTP(S) links open safe new tab, same-origin Console links retain drawer, and plugin backend resource URLs warn. Includes/images may come from same approved repository through backend-validated resource URLs. Underscore-prefixed paths are include-only. Typed `console:` and `resource:` macros accept only fixed validated Console targets; guidance verifies renderer-generated provenance, waits only after required route navigation, and renders a non-modal dismissible spotlight.
- **Provisional:** explicit selection of existing release namespace and schema-annotated additional inputs; namespace selection/lifecycle policy remains open.
- **Implemented and verified:** first page of first module opens by default; dropdown for multiple modules shows safe plain-text first-page titles (or module ID fallback), fetched in background after setup. Previous/Next crosses modules, counter is module-local, and only valid page paths resume on explicit relaunch. Chart-backed launches use async SpaceRequests/Helm worker and frontend polling, not a synchronous setup result.
- **Implemented and verified:** Typed guidance uses fixed 25-ID Console registry and `resource:` links that require two or three GVK components (`version/kind` or `group/version/kind`). Scope and attribute schema remain enforced; cluster scope rejects `namespace`. Each GVK component and every nonempty supplied `name`/`namespace` selector is URI-encoded into fixed derived Console resource list/details paths, while Console resolves resource existence. Authors cannot supply routes or filter syntax. Route-backed target absence navigates once then waits; visible-only absence does not navigate. Spotlight is non-modal and cancels on interaction, Escape, route change, timeout, panel close, or unmount.
- Module-local manual checkpoint definitions are validated but not sent in bundle or presented as progress; evidence/checkpoint rules and solver behavior remain open.

Design detail: [architect notes](../efforts/interactive-labs/architect-notes.md). Decisions: [design decisions](design-decisions.md).
