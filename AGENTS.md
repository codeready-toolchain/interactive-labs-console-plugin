# Interactive Labs Dynamic Plugin

## Code conventions

Always use testify asserts for test assertions. Prefer sub-tests over multiple top level test functions testing different aspects of the same code.

## Delivery state

STORY-1, STORY-2, STORY-9, and STORY-10 are implemented: secure Console-to-backend boundary, administrator-selected source catalog, manifest-derived catalog metadata, atomic periodically refreshed hashed approved repository snapshots, validated lab-bundle API, safe catalog-omission and source-refresh Warning Events, and catalog-listing page. Frontend `Launch` remains disabled. Backend setup derives release identity and returns a success-shaped response but does **not** execute Helm.

Drawer launch, AsciiDoc rendering/navigation, browser page/include/image resource endpoint, Console guidance links, and Helm setup are decided target behavior, not current functionality. Namespace selection and manual checkpoints remain provisional directions pending open policy decisions. See [dev/docs](dev/docs/README.md) and [stories](dev/efforts/interactive-labs/stories.md).

## Product

Interactive Labs is an OpenShift Dynamic Plugin for guided, multi-page AsciiDoc labs. Target MVP shows content in a minimizable, closeable overlay, prepares optional Helm-chart prerequisites, and links users to supported OpenShift Console locations.

## Deployment and trust model

Plugin runs in Red Hat customer OpenShift clusters. Customers opt into Red Hat-authored labs through `interactive-labs-catalog` ConfigMap in namespace `interactive-labs-console-plugin`.

Browser is untrusted: it must not clone repositories, execute Helm, retain user tokens, decide authorization, or silently choose namespaces. Console forwards current user token through required `UserToken` proxy; backend uses current-user RBAC. Tokens are request-scoped: never log, persist, echo, or return them.

Backend shallow-clones each configured approved HTTPS repository/branch into a hashed snapshot under `/var/lib/interactive-labs/cache` and reuses it across process starts while that storage remains. Current deployment has no PVC, so pod/container replacement loses cache; future PVCs mount this fixed path. MVP refreshes approved branch snapshots asynchronously at validated configured cadence, but does not pin or expose revisions, notify browser clients, migrate sessions, reconcile running labs, or provide rollback. Do not assume separate CI validation or publishing environment for lab updates.

## Decided target conventions

- Catalog identity is backend-derived opaque SHA-256 over repository, branch, lab path, and attribute overrides. Attribute precedence: manifest, catalog overrides, backend runtime attributes.
- Content is data, not executable code. Target renderer allows only documented restricted AsciiDoc, bundle-local paths/includes, typed guidance links, and DOM sanitization.
- Setup is target synchronous, bounded, and current-user-RBAC based. MVP has no automatic retry, cleanup, cancellation, or recovery.
- Prefer direct, existing platform/library capabilities; do not add abstractions or dependencies without demonstrated need.

## Provisional directions

- Current namespace candidate is explicit selection of existing release namespace and `values.schema.json` namespace-name bindings. Namespace selection and lifecycle are not settled.
- Current checkpoint candidate is browser-local `type: manual` user attestation. It is not a settled completion model.

## Open or deferred

Namespace selection and lifecycle; completion evidence and checkpoint rules; and section-solving behavior remain open. Current manual namespace selection and `type: manual` checkpoint directions are provisional. Automated verification, durable/audited progress, asynchronous setup/recovery, author validation tooling, revision lifecycle/migration/rollback, and disconnected delivery are deferred.

## Sources of truth

- [Architecture](dev/docs/architecture.md)
- [Design decisions](dev/docs/design-decisions.md)
- [Engineering conventions](dev/docs/engineering-conventions.md)
- [Administrator guide](dev/docs/administrator-guide.md)
- [Lab content contract](dev/docs/lab-content-contract.md)
- [Architect notes](dev/efforts/interactive-labs/architect-notes.md)
