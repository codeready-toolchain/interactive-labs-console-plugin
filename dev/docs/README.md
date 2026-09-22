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
| Secure Console proxy, source-only ConfigMap catalog, manifest-derived catalog metadata, immutable hashed HTTPS repository snapshots at fixed future-PVC mount path, bundle API, manifest/path validation, typed errors, safe catalog-omission Warning Events | **Implemented and verified** |
| Catalog-listing page, loading/empty/error/retry states | **Implemented and verified** |
| Lab launch for labs without charts, drawer, AsciiDoc rendering, page navigation, browser page/include/image resource endpoint | **Implemented and verified** |
| Typed Console guidance/resource links | **Implemented and verified** |
| Helm execution; explicit existing-namespace selection with schema annotations; browser-local `type: manual` checkpoints | **Provisional direction** |
| Namespace selection/lifecycle; completion evidence and checkpoint rules; section solving; automated verification; durable/audited progress; async setup/recovery; revision lifecycle; offline delivery; author validation tool | **Open or deferred** |

Notable current boundaries: snapshots live under `/var/lib/interactive-labs/cache` and persist across backend shutdown while storage remains. No PVC is mounted today, so pod/container replacement loses them; a future PVC mounts that fixed path. Snapshots never refresh remotely; source changes appear only after cache loss. Resource responses use a quoted snapshot Git `HEAD` SHA as `ETag` with `Cache-Control: private, no-cache`; resource-size caps are not yet enforced. Labs without charts launch directly; chart-backed launches defer setup to STORY-5. Backend setup returns a success-shaped response with a derived release name but does **not** invoke Helm.

## Provisional directions

Namespace selection/lifecycle and completion/checkpoint rules remain open. Current explicit existing-namespace selection and browser-local `type: manual` checkpoint approaches are candidates for exploration, not settled policy. Section-solving behavior also remains open.
