# Interactive Labs documentation

Interactive Labs is an OpenShift Console dynamic plugin for guided, multi-page AsciiDoc labs. An optional Helm chart prepares lab resources before instructions begin.

## Status labels

Every document separates current code from target design:

- **Implemented and verified** — behavior present in source and tests.
- **Decided target / planned** — recorded design decision; implementation may be pending.
- **Provisional direction** — current candidate, not a settled policy or implementation commitment.
- **Open or deferred** — no MVP commitment or implementation.

Do not represent planned behavior as shipped.

## Documentation map

- [Architecture](architecture.md) — topology, ownership, current request flows, and APIs.
- [Design decisions](design-decisions.md) — decisions, rationale, tradeoffs, and deferrals.
- [Engineering conventions](engineering-conventions.md) — source structure, testing, build, and deployment conventions.
- [Administrator guide](administrator-guide.md) — deploy, configure, validate, and operate plugin.
- [Lab content contract](lab-content-contract.md) — catalog and lab-author requirements.

## Sources of truth

| Subject | Canonical source |
| --- | --- |
| Product problem, scope, and non-goals | [proposal](../efforts/interactive-labs/proposal.md) |
| Detailed architecture and Q1–Q9 decisions | [architect notes](../efforts/interactive-labs/architect-notes.md) |
| Delivery status and acceptance criteria | [stories](../efforts/interactive-labs/stories.md) |
| Catalog format | [catalog ConfigMap contract](../efforts/interactive-labs/contract/catalog-configmap.md) |
| Proxy, TLS, and image wiring | [proxy operational contract](../efforts/interactive-labs/contract/proxy-setup.md) |
| Current behavior | repository source, tests, and manifests |

## Current delivery snapshot

| Area | Status |
| --- | --- |
| Secure Console proxy, source-only ConfigMap catalog, manifest-derived catalog metadata, atomic refreshed hashed HTTPS repository snapshots at fixed future-PVC mount path, bundle API, manifest/path validation, typed errors, safe catalog-omission and refresh-failure Warning Events | **Implemented and verified** |
| Catalog-listing page, loading/empty/error/retry states | **Implemented and verified** |
| Modules-only lab bundle, single/multi-module drawer navigation and title fallback, AsciiDoc rendering, browser page/include/image resource endpoint | **Implemented and verified** |
| Typed Console guidance/resource links | **Implemented and verified** |
| Chart-backed async SpaceRequests/Helm setup with frontend status polling | **Implemented and verified** |
| Explicit existing-namespace selection; browser-local manual-checkpoint attestation | **Provisional direction** |
| Namespace selection/lifecycle; completion evidence and checkpoint rules; section solving; automated verification; durable/audited progress; durable setup recovery; revision lifecycle; offline delivery; author validation tool | **Open or deferred** |

Notable current boundaries: snapshots live under `/var/lib/interactive-labs/cache` and persist across backend shutdown while storage remains. No PVC is mounted today, so pod/container replacement loses them; a future PVC mounts that fixed path. Service-account refresh runs at startup and then on configured cadence (`1h` default, four source workers), stages each source for a fixed ten-minute deadline, and atomically replaces only validated generations. Resource responses use a quoted snapshot Git `HEAD` SHA as `ETag` with `Cache-Control: private, no-cache`; resource-size caps are not yet enforced. No browser update notification, revision pinning, migration, reconciliation, rollback, or cache eviction exists. Labs without charts launch directly; chart-backed launches start async SpaceRequests/Helm setup and open instructions after status polling succeeds. See [lab setup design](lab-setup-design.md).

## Provisional directions

Namespace selection/lifecycle and completion/checkpoint rules remain open. Current explicit existing-namespace selection and browser-local `type: manual` checkpoint approaches are candidates for exploration, not settled policy. Section-solving behavior also remains open.

