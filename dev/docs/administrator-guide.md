# Administrator guide

## Prerequisites

- OpenShift cluster with Console Dynamic Plugin support compatible with declared plugin range.
- `oc`, `kustomize`, and container tool (Makefile defaults to Podman).
- Permission to create namespace/resources and configure ConsolePlugin.
- Cluster service-certificate support.

## Deploy

Base manifests live in `deploy/base`.

```sh
make image IMAGE=quay.io/example/interactive-labs:tag
make push-image IMAGE=quay.io/example/interactive-labs:tag
make deploy
```

For cluster-local development image workflow:

```sh
make dev-deploy
# or
make dev-deploy-with-demo-catalog
```

`dev-deploy` creates namespace, builds/pushes image to OpenShift registry, patches image in temporary Kustomize copy, then applies it.

## HTTPS and Console proxy

Service annotation requests TLS Secret `interactive-labs-console-plugin-tls`. Deployment mounts certificate and key at `/etc/tls/backend`; backend serves HTTPS on port `9001`.

ConsolePlugin proxy is fixed:

```yaml
alias: backend
authorization: UserToken
```

`UserToken` is required. It forwards current user bearer token, enabling backend current-user RBAC. Do not switch authorization to `None`; do not log or persist forwarded tokens.

Plugin calls backend through:

```text
/api/proxy/plugin/interactive-labs-console-plugin/backend/api/v1alpha1/...
```

Full wiring: [proxy operational contract](../efforts/interactive-labs/contract/proxy-setup.md).

## Configure catalog

Create/update `interactive-labs-catalog` ConfigMap in `interactive-labs-console-plugin` namespace. It must have one `catalog.yaml` key containing top-level YAML sequence. Each entry requires `repository`, `branch`, and `labPath`; `attributeOverrides` is optional:

```yaml
- repository: https://github.com/redhat-scholars/my-lab
  branch: main
  labPath: lab
  attributeOverrides:
    lab-title: "Workshop lab"
    greeting: "Welcome to the workshop"
```

`attributeOverrides` is a per-entry map of string values. It overrides same-named values from the lab's `lab.yaml` `spec.attributes`; backend runtime attributes take precedence over both. Authors can use these values in AsciiDoc, for example `{greeting}, welcome to {lab-title}.`. Overrides affect rendered content, not `metadata.title` or the catalog display title. Put the map inside its catalog entry in `data.catalog.yaml`, not as a top-level ConfigMap `data` key.

Repositories must be plain HTTPS URLs. `labPath` must be repository-relative. Customers select approved Red Hat-authored labs by adding catalog entries. Backend reads catalog `title` and optional `description` from each validated `lab.yaml`; legacy ConfigMap display fields are ignored. Lab authors must use `metadata.title` and `modules/<id>/content/` (not old `metadata.displayName`); see [lab content contract](lab-content-contract.md). Entry order controls returned catalog order for successfully resolved labs. `catalogRef` is backend-derived; do not configure an ID.

Backend stages each `(repository, branch)` source under `/var/lib/interactive-labs/cache/<sha256 canonical repository+branch>` and atomically publishes a validated generation. Service-account refresh starts immediately, runs every `CACHE_REFRESH_INTERVAL` (`1h` default), and responds to creation, update, or deletion of the named catalog ConfigMap, with at most `CACHE_CONCURRENT_REFRESH_LIMIT` distinct sources (`4` default). Both values must be positive; malformed or non-positive values are logged and use defaults. Each source has a fixed ten-minute deadline after receiving a worker slot. A failed remote-ref lookup, clone, or `HEAD` resolution preserves last-known-good content; a source with no good snapshot omits only dependent labs until a later sweep. `Close` keeps successful snapshots, so next backend start reuses them when storage remains. No PVC is mounted today: pod/container replacement loses cache. Future PVCs must mount `/var/lib/interactive-labs/cache`. Catalog, bundle, and session data do not expose revision identity; resource responses expose a quoted generation Git `HEAD` SHA in `ETag`.

The named ConfigMap watch coalesces changes during a sweep into one non-overlapping follow-up. Every sweep re-gets and validates the current catalog rather than trusting watch data. Watch interruptions reconnect/re-list; periodic refresh remains fallback. Deleting the catalog causes a safe read failure without evicting snapshots; recreation retriggers refresh. No browser update notification is sent.

Schema, validation, and attributes: [catalog ConfigMap contract](../efforts/interactive-labs/contract/catalog-configmap.md).

## RBAC and runtime security

- Backend ServiceAccount has `get`, `list`, and `watch` on the named catalog ConfigMap; list/watch use the `metadata.name=interactive-labs-catalog` field selector. It also has `create` on namespaced Events, `get` on named `config.openshift.io` cluster configuration objects: `infrastructures/cluster`, `ingresses/cluster`, and `consoles/cluster`, and `get`, `create`, and `update` on coordination Leases in the plugin namespace. Runtime-attribute failure logs name only failed attribute/category; they never include tokens or values.
- Setup/bundle calls authenticate forwarded current-user token; browser never sends privileged credentials. Backend uses that token only for identity; service account reads cluster configuration per bundle request.
- Pod runs non-root with privilege escalation disabled, all capabilities dropped, and RuntimeDefault seccomp.

## Validate

```sh
make test
make build
make validate-deploy
make validate-demo-catalog
make validate-catalog-namespace
```

## Troubleshoot catalog omissions

A safely configured lab is omitted from the catalog when its repository is unavailable, its `lab.yaml` is missing, or its bundle is invalid. Other valid entries remain available. Backend logs and bounded namespaced `Warning` Events against `interactive-labs-catalog` carry `catalogRef`, repository, branch, and lab path with one safe reason (`RepositoryUnavailable`, `ManifestMissing`, `LabInvalid`, `RemoteReferenceLookupFailed`, `CloneFailed`, `SourceRefreshTimedOut`, or `HeadResolutionFailed`) per `(catalogRef, reason)` during backend lifetime. They never contain request headers, tokens, raw Git output, or lab content. Repeated failures do not create duplicate Events; Event-post failures do not change catalog response.

Fix malformed repository URLs, unsafe branches or lab paths, missing required source fields, and duplicate source identities in the ConfigMap. These are `configuration_error` failures for the whole catalog, not omitted entries.

## Explicit lab cleanup

Catalog entries with no setup show **Launch**; deployed entries show **Open** and **Tear down**. Pending entries show **Open** with a spinner but disable teardown; failed entries disable **Open** and allow teardown only when cleanup is safe. One continuous setup list uses identical rows and separators: existing user-owned setups first, including failures and cleanup remnants, then inactive catalog labs. Catalog-removed setups stay in that list with a fallback title and **Tear down** only; **Open** requires current catalog content. In the UI, there is no separate cleanup section, heading, or spacing between groups. **Refresh setups** reloads current-user inventory even if the catalog is unavailable and remains usable during teardown. Teardown confirms release and SR targets, reserves the operation, and returns `202`; a background worker uses the forwarded user token only in memory to uninstall Helm and delete dedicated SRs. Different labs can tear down concurrently; each progress/error stays under its catalog or orphan row. Same-tab session storage retains multiple non-secret `(catalogRef, operationId)` receipts and resumes their status polls after reload. In a tab tracking a receipt, that lab's actions are disabled; backend Lease rejects concurrent same-lab setup/teardown requests from other tabs or clients. Navigation stops polling, not cleanup. Backend success means any listed Helm release was uninstalled successfully and each dedicated SR was already absent or its UID-preconditioned DELETE was accepted, not that namespaces have finished finalizing. After a terminal or missing receipt, that row waits for a successful fresh inventory check before Open or another teardown confirmation is enabled; unknown or failed outcomes are never shown as success. After success, if a catalog row's SR is still terminating, Launch/Open stays disabled and the row waits for finalizer cleanup. While the catalog page remains mounted, the client automatically refreshes setup inventory after each 10-second delay only while pending setups, cleanup blockers, held-operation blockers, or rows awaiting confirmed cleanup remain. Failed release-free setups without these in-progress signals do not trigger polling. Requests do not overlap, background refresh does not disable unrelated rows, and transient failures keep the last inventory and retry. Once inventory confirms the SR is absent, Launch is enabled and final success is shown; polling stops when no in-progress rows remain or the page unmounts. **Refresh setups** remains available for an immediate check and shares an active inventory request. This client inventory polling never starts setup/teardown mutations and does not poll namespace deletion or quota reclamation. A terminating SR is reported with `blockedReason: cleanup`, distinct from other setup/operation blockers. Drawer close/reset never performs cleanup. Namespace removal continues through existing Toolchain finalizers; plugin does not delete namespaces directly or track quota reclamation; teardown has no namespace- or quota-polling state machine.

Before rollout, separate SR naming work must guarantee dedicated sets per user and `catalogRef`; this story does not change naming. Stop all older uncoordinated backend workers before enabling the new version. New SRs and Helm releases carry `interactive-labs.openshift.io/catalog-ref` labels: lowercase unpadded base32hex of all 32 SHA-256 bytes, not a truncated digest. SR binding-key annotations support identity checks. Shared, unmarked, or foreign SRs are not adopted. Legacy releases require manual cleanup. Setup `POST` returns `409` while any identity-marked SR for that `catalogRef` is terminating, including SRs for bindings no longer in the current schema; setup status does not treat a deleting SR as ready.

Current-user RBAC must allow SR list/get/create/delete in the home namespace, Helm Secret list/get/create/update/delete, and chart-resource operations. Plugin service account needs only coordination Lease get/create/update in its own namespace for setup/teardown exclusion, not elevated Helm/SR permissions. Inventory rejects incomplete or oversized SR lists (4 MiB) rather than acting on a partial set.

Pending upgrades/rollbacks and unknown releases, `helm.sh/resource-policy: keep` resources, malformed manifests, and hooks that cannot be verified as named `batch/v1` Jobs in Ready, identity-marked, dedicated SpaceRequest namespaces require administrator recovery. An unheld `pending-install` release with verified safe cleanup can be explicitly torn down through the plugin; never reinstall over it. Jobs in the persistent home namespace are not eligible; verified Jobs in dedicated SR namespaces are removed when those namespaces are removed after teardown. The catalog gives a specific reason when teardown is blocked. This check verifies where the Job object lives, not side effects of its code or its service account permissions; approved charts must keep those scoped too. Namespace removal is asynchronous through Toolchain finalizers, so investigate stuck namespaces separately. Never run manual Helm commands concurrently with plugin setup/teardown; the plugin's reservation coordinates plugin operations, not external Helm clients.

Lease holders renew every 20 seconds; after 200 seconds without renewal, another request may atomically claim the same lab's reservation. Renewals run independently of HTTP requests; losing renewal cancels the worker's operation context. This is **best-effort**, not fencing: a paused or partitioned old worker may finish an in-flight Helm call after takeover. A new claim always checks live release and SR identity before mutation. Cluster clock skew affects timestamp-based expiry; keep node clocks synchronized. Held Leases with missing or invalid renewal fields fail closed and require administrator recovery; the backend does not infer their age or start a migration grace period.

Normal setup restart: when no Helm release exists, setup status advertises retry and an open browser sends a fresh authenticated POST, reusing only identity-verified SRs. A pending Helm install without an active reservation becomes a failed/interrupted setup; user can confirm managed teardown from the inventory (when safety checks pass) and relaunch. No user token is stored for unattended recovery. Interrupted teardown receipts report `unknown` after expiry, not success; refresh inventory and explicitly confirm cleanup of any remaining verified resources. Different labs remain independent.

Administrator action remains necessary for unsafe Helm hooks/retained resources, unverified identities, failed API permissions, a Lease with invalid/unknown renewal fields, or unfinished external side effects. List Leases in `interactive-labs-console-plugin` and inspect `interactive-labs.openshift.io/subject` and `interactive-labs.openshift.io/catalog-ref` annotations; verify old workers and actual Helm/SR state before manually clearing a holder. Never clear a live reservation or run manual Helm concurrently with plugin operations. No legacy resource relabeling, automatic deletion of unsafe charts, or durable execution journal is provided.

## Operating boundary

**Implemented and verified:** catalog list and modules-only lab-bundle APIs, manifest-derived catalog title, immutable hashed approved-source snapshots, manifest/module/path validation, browser page/include/image resource serving and rendering, and safe catalog-omission Warning Events. Resource responses are private, revalidated browser-cache entries with a quoted cached snapshot Git `HEAD` SHA as `ETag`; this header exposes revision while catalog, bundle, and session data do not. Resource-size caps are not yet enforced.

**Current chart setup:** `POST /setup` initiates current-user SpaceRequests and an asynchronous Helm installation worker. Frontend polls `GET /setup/status` and does not show pages until ready/deployed. Worker keeps forwarded token in memory after POST; never log or persist it. Safe abandoned setup without a Helm release resumes on next authenticated poll after Lease expiry. No silent Helm reinstall or automatic teardown of a pending release; confirmed managed cleanup is available for safe interrupted installs. Process restart loses worker and token, not cluster state. See [lab setup design](lab-setup-design.md) for operational details. Namespace selection/lifecycle policy remains open; explicit existing-namespace selection is a provisional proposal, not current UI.
