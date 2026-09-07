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
| Browser plugin | catalog UI; eventually drawer, page state, and any resolved progress model | clone repositories, execute Helm, decide authorization, persist tokens, or unilaterally choose namespace policy |
| Backend | catalog resolution, authentication, bundle validation/loading, setup authorization and release identity | log, persist, echo, or return forwarded tokens |
| Cluster administrator | install plugin and select trusted labs through catalog ConfigMap | place unapproved browser-controlled repository locations in catalog |
| Lab author | author supported lab content and optional chart | use unsupported rendering or execution capabilities |

## Implemented request flows

### Catalog

1. `InteractiveLabsPage` calls `listCatalog()`.
2. `consoleFetch` sends `GET /api/proxy/plugin/interactive-labs-console-plugin/backend/api/v1alpha1/catalog`.
3. Backend validates source locators, derives each opaque `catalogRef`, resolves each lab from its cached approved snapshot, and returns manifest `displayName` and optional `description` in declared order.
4. An unavailable repository, missing manifest, or invalid lab omits only that item. Backend logs safe opaque context and emits one deduplicated namespaced Warning Event per `(catalogRef, reason)` for its lifetime.

### Bundle

1. Browser requests `GET /api/v1alpha1/labs/{catalogRef}/bundle` through same proxy.
2. Backend requires bearer token, resolves authenticated subject, then resolves `catalogRef` from live catalog.
3. **Implemented and verified:** On first use, backend shallow-clones one configured HTTPS repository/branch into `/var/lib/interactive-labs/cache/<sha256 canonical repository+branch>/content` and reuses that snapshot for catalog, bundle, setup, and resource loading while cache storage remains. Entry directory is reserved for future metadata. `Close` preserves snapshots; current deployment has no PVC, so pod/container replacement loses cache. Future PVCs mount this fixed path. It loads `lab.yaml`, `.adoc` files below `content/`, and optional `setup/helm/Chart.yaml`; bundle returns normalized navigable page descriptors with backend-relative resource paths.
4. **Implemented and verified:** Catalog returns normalized `labPath`; browser resolves backend-relative page paths through its fixed Console proxy base. `GET /api/v1alpha1/labs/{catalogRef}/resources/{repositoryRelativePath...}` serves approved page/include/image bytes on demand with repository-root validation. Responses use a quoted snapshot Git `HEAD` SHA as shared `ETag` and `Cache-Control: private, no-cache`; resource-size caps are not yet enforced.

### Setup

`POST /api/v1alpha1/labs/{catalogRef}/setup` requires authentication and `releaseNamespace`. Backend loads bundle from the same cached source snapshot and derives a deterministic user-specific `il-...` release name. It returns `v1alpha1/SetupResult`.

**Implemented and verified:** validation, authentication boundary, and release-name derivation.

**Not implemented:** Helm installation. Current handler explicitly returns `outcome: success` with message stating Helm execution is deferred.

Evidence: [frontend client](../../frontend/src/api/client.ts), [router](../../backend/internal/handler/handler.go), [bundle loader](../../backend/internal/bundle/loader.go).

## API contracts

| Operation | Current route | Result |
| --- | --- | --- |
| List catalog | `GET /api/v1alpha1/catalog` | `v1alpha1/CatalogList` |
| Load bundle | `GET /api/v1alpha1/labs/{catalogRef}/bundle` | `v1alpha1/LabBundle` |
| Read resource | `GET /api/v1alpha1/labs/{catalogRef}/resources/{repositoryRelativePath...}` | Raw supported text or raster bytes; `v1alpha1/Error` on failure |
| Setup lab | `POST /api/v1alpha1/labs/{catalogRef}/setup` | `v1alpha1/SetupResult` |

All non-success API responses use `v1alpha1/Error`. Unknown client error types normalize to `unknown_error`.

## Deployment

- `deploy/base` is Kustomize base; `deploy/demo` replaces catalog with demo entries.
- Service requests OpenShift serving certificate and backend mounts its TLS secret.
- ConsolePlugin defines `backend` proxy alias with mandatory `UserToken` authorization.
- One backend replica runs as non-root with all Linux capabilities dropped, privilege escalation disabled, and RuntimeDefault seccomp.
- Backend service account has only `get` access to named catalog ConfigMap, `create` access to namespaced Events, and `get` access to named `config.openshift.io` `cluster` infrastructure, ingress, and console configuration objects for runtime attributes.

See [administrator guide](administrator-guide.md).

## Current and planned flows

- Enabled launch opens accessible, minimizable drawer and restores browser-local session state only after explicit user activation.
- **Implemented and verified:** Restricted AsciiDoc uses exact pinned renderer dependencies. DOMPurify's final AsciiDoc pass uses its default allowlist and forbids `style`/`form` elements and `style` attributes; Mermaid SVG receives a separate SVG/SVG-filter-profile pass that retains allowed styling and is inserted afterward. Renderer passes backend-final attributes hard-set to Asciidoctor. Relative links resolve from source URL and navigate only known pages under `labPath/content`; external HTTP(S) links open safe new tab, same-origin Console links retain drawer, and plugin backend resource URLs warn. Includes/images may come from same approved repository through backend-validated resource URLs. Underscore-prefixed paths are include-only.
- **Provisional:** explicit selection of existing release namespace and schema-annotated additional inputs; namespace selection/lifecycle policy remains open.
- Backend runs synchronous, bounded Helm setup with current-user RBAC and returns terminal success, failure, or timeout.
- Typed Quick Start-compatible guidance links navigate to supported routes and optionally spotlight `data-quickstart-id`; missing targets fall back to route-only navigation.
- **Provisional:** browser-local `type: manual` checkpoints record user attestation only; evidence/checkpoint rules and solver behavior remain open.

Design detail: [architect notes](../efforts/interactive-labs/architect-notes.md). Decisions: [design decisions](design-decisions.md).
