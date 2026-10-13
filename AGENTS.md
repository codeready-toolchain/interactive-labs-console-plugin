# Interactive Labs Dynamic Plugin

## Code conventions

Always use testify asserts for test assertions. Prefer sub-tests over multiple top level test functions testing different aspects of the same code. Tests follow the "given-when-then" convention.

## Delivery state

Current behavior and delivery status live in [dev/docs](dev/docs/README.md) and [stories](dev/efforts/interactive-labs/stories.md). Launch, persistent drawer, restricted AsciiDoc/navigation, resource API, guidance links, and asynchronous SpaceRequests/Helm setup are implemented. Asynchronous explicit teardown lists identity-marked setups independently of the catalog and uninstalls Helm before deleting dedicated SRs. SR naming is separate work: never adopt unmarked/shared/foreign SRs.

Setup and teardown for one `(authenticated subject, catalogRef)` use a cluster-visible renewable Lease with 20-second heartbeats and best-effort takeover after 200 seconds without renewal. An old in-flight Helm call may outlive its Lease; verify resource identity and reconcile before mutation. Different labs may share chart files, never installed Helm releases or SRs. Namespace selection and checkpoints remain separate open policy decisions.

## Product

Interactive Labs is an OpenShift Dynamic Plugin for guided, multi-page AsciiDoc labs. Target MVP shows content in a minimizable, closeable overlay, prepares optional Helm-chart prerequisites, and links users to supported OpenShift Console locations.

## Deployment and trust model

Plugin runs in Red Hat customer OpenShift clusters. Customers opt into Red Hat-authored labs through `interactive-labs-catalog` ConfigMap in namespace `interactive-labs-console-plugin`.

Browser is untrusted: it must not clone repositories, execute Helm, retain user tokens, decide authorization, or silently choose namespaces. Console forwards current user token through required `UserToken` proxy; backend uses current-user RBAC. Never log, persist, echo, or return user tokens. Async setup and teardown hold the forwarded token only in memory for worker lifetime.

Backend shallow-clones each configured approved HTTPS repository/branch into a hashed snapshot under `/var/lib/interactive-labs/cache` and reuses it across process starts while that storage remains. Current deployment has no PVC, so pod/container replacement loses cache; future PVCs mount this fixed path. MVP refreshes approved branch snapshots asynchronously at validated configured cadence, but does not pin or expose revisions, notify browser clients, migrate sessions, reconcile running labs, or provide rollback. Do not assume separate CI validation or publishing environment for lab updates.

## Decided target conventions

- Catalog identity is backend-derived opaque SHA-256 over repository, branch, lab path, and attribute overrides. Attribute precedence: manifest, catalog overrides, backend runtime attributes.
- Content is data, not executable code. Target renderer allows only documented restricted AsciiDoc, bundle-local paths/includes, typed guidance links, and DOM sanitization.
- Setup and explicit teardown are asynchronous and current-user-RBAC based. After Lease expiry, authorized setup polling can initiate a fresh POST when no Helm release exists; interrupted safe `pending-install` releases require explicitly confirmed teardown before retry. No silent Helm reinstall or cleanup. Service-account permissions for coordination metadata never authorize Helm/SR mutations.
- Prefer direct, existing platform/library capabilities; do not add abstractions or dependencies without demonstrated need.

## Provisional directions

- Current namespace candidate is explicit selection of existing release namespace and `values.schema.json` namespace-name bindings. Namespace selection and lifecycle are not settled.
- Current checkpoint candidate is browser-local `type: manual` user attestation. It is not a settled completion model.

## Open or deferred

Namespace selection and lifecycle; completion evidence and checkpoint rules; and section-solving behavior remain open. Current manual namespace selection and `type: manual` checkpoint directions are provisional. Automated verification, durable/audited progress, full automated Helm recovery, author validation tooling, revision lifecycle/migration/rollback, and disconnected delivery are deferred.

## Sources of truth

- [Architecture](dev/docs/architecture.md)
- [Design decisions](dev/docs/design-decisions.md)
- [Engineering conventions](dev/docs/engineering-conventions.md)
- [Administrator guide](dev/docs/administrator-guide.md)
- [Lab content contract](dev/docs/lab-content-contract.md)
- [Architect notes](dev/efforts/interactive-labs/architect-notes.md)
