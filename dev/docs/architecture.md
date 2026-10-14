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
3. **Implemented and verified:** On first use, backend shallow-clones one configured HTTPS repository/branch into `/var/lib/interactive-labs/cache/<sha256 canonical repository+branch>/content` and reuses that snapshot for catalog, bundle, setup, and resource loading while cache storage remains. Entry directory is reserved for future metadata. `Close` preserves snapshots; current deployment has no PVC, so pod/container replacement loses cache. Future PVCs mount this fixed path. It loads `lab.yaml`, each `modules/<id>/content/` tree, and an optional Helm chart selected by authoritative lab-relative `spec.setupDir`, then lab-local `setup/helm/`, then repository-root `setup/helm/` only when local directory is absent; invalid selected charts invalidate the lab; bundle returns ordered `modules: [{id, pages: [{path, url}]}]` with module-qualified lab-relative paths and backend-relative resource URLs, never root `pages`.
4. **Implemented and verified:** Catalog returns normalized `labPath`; browser checks each module ID, navigable path, and URL against that lab path and fixed Console proxy base before using it. `GET /api/v1alpha1/labs/{catalogRef}/resources/{repositoryRelativePath...}` serves approved page/include/image bytes on demand with repository-root validation. Responses use a quoted snapshot Git `HEAD` SHA as shared `ETag` and `Cache-Control: private, no-cache`; resource-size caps are not yet enforced.

### Setup

First chart-backed launch calls `POST /api/v1alpha1/labs/{catalogRef}/setup` with no namespace body. Backend authenticates the forwarded current user, creates or reuses SpaceRequests in the user's home namespace, and starts a background worker to wait for readiness and install the selected Helm chart with current-user RBAC. `POST` returns `202` with `v1alpha1/SetupStatus` (or `200` when the release is already deployed). Opening an existing pending or deployed setup starts with `GET /api/v1alpha1/labs/{catalogRef}/setup/status`; when status reports `retrySetup: true`—no release, no active reservation, and no prior teardown receipt—the browser sends a fresh authenticated setup POST and resumes polling. Content opens after SpaceRequests are ready and Helm is deployed. Pending Helm releases are never blindly reinstalled. See [lab setup design](lab-setup-design.md).

The setup manager owns setup, status, inventory, teardown orchestration, and setup preparation. HTTP handlers authenticate and resolve the approved catalog entry before calling the manager; they do not prepare setup or expose a `SetupPreparer`. `main.go` wires `cache.WithSnapshot` into `ManagerConfig`. A private manager helper runs filesystem-only `setup.Prepare` through that callback for setup and status, against the same approved snapshot cache used for bundle and resource loading. `setup.Plan` stays internal to the backend. Teardown and setup inventory remain independent of catalog and cache availability.

Shared `chart` owns effective chart selection, path containment and symlink checks, Helm-loader validation, and optional schema decoding. `bundle` owns manifest and module loading plus early validation of selected chart content; it has no provisioning dependency. `setup.Prepare` composes bundle content loading with shared chart validation, requires a schema and namespace bindings, and derives the internal plan. Setup identity hashes the authenticated subject, unchanged `catalogRef`, normalized repository-relative effective chart path, and sorted namespace binding keys; every field is byte-length-prefixed before hashing. Identity does not depend on absolute snapshot paths, and sharing chart files never shares installed releases or SpaceRequests.

The manager reconstructs live release and Lease state ahead of advisory process-local worker records, whose updates are tied to their operation identity. Setup acquires the same per-user, per-`catalogRef` renewable Lease used by teardown before cluster mutations. After Lease expiry, an authorized setup poll can trigger a fresh POST when no Helm release exists. Before starting or reusing setup, backend checks all identity-marked SpaceRequests for the lab, including bindings removed from the current schema; a terminating SpaceRequest makes `POST` return `409`. Setup status never counts a SpaceRequest with a deletion timestamp as ready. Lease takeover is best-effort exclusion, not fencing: an old in-flight Helm call may outlive its Lease. Holders renew every 20 seconds; takeover is eligible after 200 seconds without renewal. All Helm and SpaceRequest operations still use the current user's token, held in memory only for worker lifetime.

Evidence: [frontend client](../../frontend/src/api/client.ts), [router](../../backend/internal/handler/handler.go), [setup lifecycle](../../backend/internal/setup/manager.go), [bundle loader](../../backend/internal/bundle/loader.go).

### Explicit teardown

The catalog page merges current-user identity-marked setups from `GET /api/v1alpha1/setups` into catalog actions; setups no longer in the catalog appear above it with teardown only. Inventory reports pending, deployed, or failed state and whether cleanup is blocked, with a fixed reason code for blocked setups. Pending teardown stays disabled; failed or deployed setups can be torn down unless blocked by a held reservation or unsafe release. Explicitly namespaced `batch/v1` Job hooks are eligible only in verified dedicated SpaceRequest target namespaces, never the persistent home namespace; retained resources and unverifiable hooks stay blocked. Explicit confirmation shows release and SpaceRequests; `POST /api/v1alpha1/labs/{catalogRef}/teardown` accepts only a server-derived generation, reserves the operation before returning `202`, and runs cleanup in a background worker using the current-user token held only in memory. The browser polls `GET /api/v1alpha1/labs/{catalogRef}/teardown/status?operationId=...` for a Lease-backed receipt, even after the release and SRs disappear. The worker rechecks live identity, uninstalls Helm, then UID-conditionally deletes dedicated SRs. `succeeded` means any listed Helm release was uninstalled successfully and each dedicated SR was already absent or its UID-preconditioned DELETE was accepted; namespace finalization may continue. Closing or navigating away stops browser polling, not cleanup. A same-tab reload resumes polling from a non-secret session-storage receipt when storage is available. User-facing cleanup has no quota-monitoring state machine.

Setup and teardown share a cluster-visible renewable Lease per `(authenticated subject, catalogRef)`, so different labs can tear down concurrently. Holders renew every 20 seconds; after 200 seconds without renewal, a new authenticated request can atomically take over and recheck resources. This is best-effort exclusion, **not fencing**: an old Helm call may still finish after expiry. Service account manages only coordination metadata in plugin namespace; all Helm/SR operations still use current user's token. Teardown's Lease stores only operation ID and fixed running/succeeded/failed receipt; an expired running receipt reports `unknown`, never success. Verified pending-install releases may be explicitly torn down if no active reservation and Helm cleanup checks pass; unsafe/unknown states still need administrator recovery. Each teardown's progress and errors stay with its catalog row or orphan setup row. Same-tab session storage can retain multiple non-secret `(catalogRef, operationId)` receipts and resume each poll after reload. In a tab tracking that receipt, the active row's actions are disabled; the backend Lease rejects concurrent same-lab setup/teardown requests from other tabs or clients. A terminating SpaceRequest appears with the distinct `blockedReason: cleanup` and keeps that lab unavailable. `Refresh setups` remains available while teardown runs. After a terminal or missing receipt, that row remains unavailable until a fresh inventory request succeeds; unknown or failed outcomes are never shown as success. After a `succeeded` receipt, the UI waits for inventory to show terminating SpaceRequests gone before enabling Launch or showing final success for a catalog row. While the catalog page is mounted and inventory contains pending setups, cleanup blockers, held-operation blockers, or a row awaiting confirmed cleanup, the client refreshes `GET /api/v1alpha1/setups` after each 10-second delay. Requests run serially without a global loading spinner or disabling unrelated rows; manual `Refresh setups` shares an active inventory request. Transient background failures retain the last inventory and retry; polling stops when no in-progress rows remain or the page unmounts. Failed release-free setups without these in-progress signals do not trigger polling. Inventory polling never starts setup or teardown mutations and does not poll namespaces or quota reclamation. Teardown has no namespace- or quota-polling state machine. See [interrupted-operation recovery](administrator-guide.md#explicit-lab-cleanup).

Evidence: [frontend client](../../frontend/src/api/client.ts), [router](../../backend/internal/handler/handler.go), [setup lifecycle](../../backend/internal/setup/manager.go), [setup handlers](../../backend/internal/handler/setup.go), [bundle loader](../../backend/internal/bundle/loader.go).

## API contracts

| Operation | Current route | Result |
| --- | --- | --- |
| List catalog | `GET /api/v1alpha1/catalog` | `v1alpha1/CatalogList` |
| Load bundle | `GET /api/v1alpha1/labs/{catalogRef}/bundle` | `v1alpha1/LabBundle` |
| Read resource | `GET /api/v1alpha1/labs/{catalogRef}/resources/{repositoryRelativePath...}` | Raw supported text or raster bytes; `v1alpha1/Error` on failure |
| Start setup | `POST /api/v1alpha1/labs/{catalogRef}/setup` | `v1alpha1/SetupStatus` |
| Check setup | `GET /api/v1alpha1/labs/{catalogRef}/setup/status` | `v1alpha1/SetupStatus` |
| List own setups | `GET /api/v1alpha1/setups` | `v1alpha1/SetupList` |
| Start teardown | `POST /api/v1alpha1/labs/{catalogRef}/teardown` | `202 v1alpha1/TeardownStatus` |
| Check teardown | `GET /api/v1alpha1/labs/{catalogRef}/teardown/status?operationId=...` | `v1alpha1/TeardownStatus` |

All non-success API responses use `v1alpha1/Error`. Unknown client error types normalize to `unknown_error`.

## Deployment

- `deploy/base` is Kustomize base; `deploy/demo` replaces catalog with demo entries.
- Service requests OpenShift serving certificate and backend mounts its TLS secret.
- ConsolePlugin defines `backend` proxy alias with mandatory `UserToken` authorization.
- One backend replica runs as non-root with all Linux capabilities dropped, privilege escalation disabled, and RuntimeDefault seccomp.
- Backend service account has `get`, `list`, and `watch` access to the named catalog ConfigMap (list/watch require `metadata.name` field selector). It also has `create` access to namespaced Events, `get/create/update` access to coordination Leases in the plugin namespace, and `get` access to named `config.openshift.io` `cluster` infrastructure, ingress, and console configuration objects for runtime attributes.

See [administrator guide](administrator-guide.md).

## Current and planned flows

- Enabled launch opens accessible, minimizable drawer and restores browser-local session state only after explicit user activation.
- **Implemented and verified:** Restricted AsciiDoc uses exact pinned renderer dependencies. DOMPurify's final AsciiDoc pass uses its default allowlist and forbids `style`/`form` elements and `style` attributes; Mermaid SVG receives a separate SVG/SVG-filter-profile pass that retains allowed styling and is inserted afterward. Renderer passes backend-final attributes hard-set to Asciidoctor. Relative links resolve from source URL and navigate only known module-qualified pages (including cross-module links); external HTTP(S) links open safe new tab, same-origin Console links retain drawer, and plugin backend resource URLs warn. Includes/images may come from same approved repository through backend-validated resource URLs. Underscore-prefixed paths are include-only. Typed `console:` and `resource:` macros accept only fixed validated Console targets; guidance verifies renderer-generated provenance, spotlights visible targets in place, and may expand only structurally validated collapsed Console nav ancestors via their disclosure buttons. It waits for React expansion commits before spotlighting; only absent/nonexpandable targets use fixed fallback routes. The same ancestor-expansion probe runs during the post-navigation wait. Destination targets are never activated, expanded sections stay open, and the spotlight is non-modal and dismissible.
- **Provisional:** explicit selection of existing release namespace and schema-annotated additional inputs; namespace selection/lifecycle policy remains open.
- **Implemented and verified:** first page of first module opens by default; dropdown for multiple modules shows safe plain-text first-page titles (or module ID fallback), fetched in background after setup. Previous/Next crosses modules, counter is module-local, and only valid page paths resume on explicit relaunch. Chart-backed launches use async SpaceRequests/Helm worker and frontend polling, not a synchronous setup result.
- Module-local manual checkpoint definitions are validated but not sent in bundle or presented as progress; evidence/checkpoint rules and solver behavior remain open.

Decisions: [design decisions](design-decisions.md).
