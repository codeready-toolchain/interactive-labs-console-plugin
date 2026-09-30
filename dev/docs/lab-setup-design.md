# Lab Setup Design

Status: in progress (STORY-5 and STORY-11).

## Overview

Before a user can work through an Interactive Lab, the environment must be provisioned: namespaces created via SpaceRequests, then a Helm chart deployed with the provisioned namespace values. This document captures the design decisions for the full setup flow.

## Trigger

- User clicks **Launch** for an unprepared lab; frontend sends `POST /api/v1alpha1/labs/{catalogRef}/setup` with an empty body.
- **Open** on a pending or deployed lab loads the drawer and checks setup status without another setup POST. Failed setups cannot open; teardown remains subject to cleanup safety checks.
- Backend authenticates using the user's bearer token forwarded by the Console `UserToken` proxy.

## Namespace Provisioning via SpaceRequests

The backend determines how many namespaces a lab needs by inspecting the values schema in the lab content repository.

### Schema scanning

- Read `setup/helm/values.schema.json` from the cached lab content snapshot (at the repository root, shared by all labs).
- Walk the JSON Schema property tree at any depth.
- Collect every property whose `x-interactive-labs.kind` field equals `namespace-name`.
- Record the dot-separated JSON path for each (e.g. `namespaces.gitea`).

### Example schema

```json
{
  "properties": {
    "namespaces": {
      "type": "object",
      "properties": {
        "gitea": {
          "type": "string",
          "x-interactive-labs": { "kind": "namespace-name" }
        },
        "datasphere": {
          "type": "string",
          "x-interactive-labs": { "kind": "namespace-name" }
        }
      }
    },
    "gitea": {
      "type": "object",
      "properties": {
        "admin": {
          "type": "object",
          "properties": {
            "password": {
              "type": "string",
              "x-interactive-labs": { "kind": "generated-password" }
            }
          }
        }
      }
    },
    "postgresql": {
      "type": "object",
      "properties": {
        "auth": {
          "type": "object",
          "properties": {
            "password": {
              "type": "string",
              "x-interactive-labs": { "kind": "generated-password" }
            }
          }
        }
      }
    }
  }
}
```

This produces two SpaceRequests (one per `namespace-name` property) and two generated passwords (one per `generated-password` property).

**Important**: the `x-interactive-labs` annotation must be placed at the exact path the Helm chart expects in its `values.yaml`. The dot-separated path derived from the schema property tree is used directly as the `--set` key. Do not wrap annotated properties under artificial grouping nodes (e.g. `passwords`) that don't exist in the chart's values structure.

### SpaceRequest creation

- For each namespace property, create a **SpaceRequest** Custom Resource (from the [host-operator](https://github.com/codeready-toolchain/host-operator)).
- Created in the **user's home namespace** (`<username>-dev`), derived from the authenticated username.
- Named after the schema property key (e.g. `gitea`, `datasphere`).
- Spec: `tierName: base1ns`.
- Created using the **user's bearer token** (the user already has permission to create SpaceRequests in their namespace).
- **Idempotent only for verified identity**: existing SR must have matching catalog label, binding annotation, namespace and UID. Unmarked/foreign SRs are never adopted. Dedicated per-lab naming is separate work; teardown rollout assumes it is established.

## Helm Chart Deployment

Once all SpaceRequests are provisioned, the backend deploys the Helm chart from `setup/helm/` in the lab content repository with the provisioned namespace names as values.

### Sequencing

SpaceRequests must **all** have a `Ready=True` condition before Helm deployment starts. The Helm chart requires the provisioned namespace names as input values, so it cannot be deployed until all namespaces are available.

### Values construction

Helm values are built from three sources, all driven by `x-interactive-labs.kind` annotations in the schema:

**Namespace bindings** (`kind: namespace-name`):
- Key: the dot-separated JSON path from the schema (e.g. `namespaces.gitea`)
- Value: the provisioned namespace name from the SpaceRequest's `status.namespaceAccess[0].name` (e.g. `user1-abc123`)

**Generated passwords** (`kind: generated-password`):
- Key: the dot-separated JSON path from the schema (e.g. `passwords.postgres`)
- Value: a 24-character cryptographically secure random password (alphanumeric, generated via `crypto/rand`)

**Root URLs** (`kind: root-url`):
- Key: the dot-separated JSON path from the schema (e.g. `gitea.rootUrl`)
- Value: a computed OpenShift Route hostname assembled as `<routeName>-<namespace>.<clusterIngressDomain>`, where:
  - `routeName` comes from the `x-interactive-labs.routeName` field (e.g. `gitea`)
  - `namespace` is resolved from the namespace binding referenced by `x-interactive-labs.namespaceRef` (e.g. `gitea`)
  - `clusterIngressDomain` is the cluster's ingress domain, fetched at backend startup from `ingresses.config.openshift.io/cluster`

All values are merged into a single values map and passed to `helm install`.

### Release identity

- **Release name**: `il-` + first 12 hex characters of SHA-256 over authenticated subject, `catalogRef`, and sorted namespace binding keys. Catalog-label discovery prevents installing a second release for the same lab when schema keys change.
- **Release namespace**: the user's home namespace (`<username>-dev`), the same namespace where SpaceRequests are created.

### Execution

- The backend uses the **Helm Go SDK** (`helm.sh/helm/v3`) to install the chart programmatically via `action.Install`.
- The chart directory is resolved from the cached lab content snapshot (`setup/helm/` at the repository root).
- The Helm install uses the **user's bearer token** for cluster authentication.
- The Helm install timeout is **configurable** (env var), default **300 seconds**.

### Generated passwords

Properties annotated with `x-interactive-labs.kind: "generated-password"` receive auto-generated values:

- **Generation**: 24-character, alphanumeric, cryptographically secure (`crypto/rand`). Fixed charset and length — no per-property configurability for now.
- **Timing**: generated in the background goroutine immediately before the Helm install, after all SpaceRequests are ready.
- **Exposure**: after successful deployment, the GET status endpoint includes `helm.generatedValues` — a map of dot-path keys to the generated values. These are read from the Helm release object (`release.Config`) via the SDK, consistent with the stateless reconstruction model.
- **Retry behavior**: on retry after a failed install, new passwords are generated. This may cause a mismatch if the previous attempt partially applied resources. Accepted as an MVP trade-off; a future UI may address this with persist-on-first-generate semantics.
- **Security**: generated values are returned over TLS in the GET response. Acceptable for ephemeral lab credentials.

### Idempotency

- If the Helm release is already deployed (re-launch), the POST handler returns 200 with the current status — no Helm operation is performed.
- Failed or pending releases block a new install; user must finish setup or explicitly clean up first. A `pending-install` release with verified safe cleanup and no active reservation can be explicitly torn down through the plugin; pending upgrades, unsafe hooks/retained resources, and unknown releases remain blocked. A deployed release is reused only when its dedicated, tagged SRs are ready and binding keys still match.

## Async Setup Flow (kick-and-poll)

### POST — initiate setup

`POST /api/v1alpha1/labs/{catalogRef}/setup`:
1. Creates SpaceRequests (idempotent).
2. Starts a **background goroutine** that watches SpaceRequests and triggers Helm when all are ready.
3. Returns **202 Accepted** immediately.

The goroutine captures the user's bearer token from the POST request. It polls SpaceRequests with retry and backoff, bounded by the Helm timeout. When all SpaceRequests are Ready, it collects the provisioned namespace names, builds the Helm values, and runs the Helm install.

If the setup is already complete (Helm release deployed), POST returns **200 OK** with the current status (no-op).

### GET — poll status

`GET /api/v1alpha1/labs/{catalogRef}/setup/status` returns the current state with two status fields:

```json
{
  "apiVersion": "v1alpha1",
  "kind": "SetupStatus",
  "spaceRequests": {
    "status": "provisioning | ready | failed",
    "namespaces": {
      "gitea": { "status": "ready", "name": "user1-abc123" },
      "datasphere": { "status": "pending", "name": "" }
    }
  },
  "helm": {
    "status": "pending | installing | deployed | failed",
    "releaseName": "il-a1b2c3d4e5f6",
    "message": "...",
    "generatedValues": {
      "passwords.postgres": "xK9mLp...",
      "passwords.giteaAdmin": "Qp7nRw..."
    }
  }
}
```

- `helm.message` is present only when `helm.status` is `failed`; it carries a safe error detail. `retrySetup: true` appears only when no Helm release exists, SRs have not failed, no active reservation remains, and the Lease holds no teardown receipt; browser sends a fresh authenticated setup POST, then resumes polling. Old setup tabs cannot recreate a lab after explicit teardown; a later user-initiated launch clears prior receipt.
- `helm.generatedValues` is present only when `helm.status` is `deployed`; it contains the generated password values used during installation, read from the Helm release object via the SDK (stateless reconstruction).

Status values:

| Field | Value | Meaning |
|-------|-------|---------|
| `spaceRequests.status` | `provisioning` | One or more SpaceRequests are not ready |
| | `ready` | All configured SpaceRequests have `Ready=True`, are not terminating, and have a target namespace |
| | `failed` | A SpaceRequest has a terminal failure condition |
| `helm.status` | `pending` | No Helm release found |
| | `installing` | Helm release exists with status `pending-install` or `pending-upgrade` while an operation reservation remains active |
| | `deployed` | Helm release exists with status `deployed` |
| | `failed` | Helm release failed, worker timed out, or existing non-deployed Helm state has no active reservation and needs cleanup |

The frontend polls GET and shows a progress indicator. The lab opens when `spaceRequests.status` is `ready` **and** `helm.status` is `deployed`.

### State reconstruction

No new CRD is needed. The setup manager reconstructs setup state on each poll by:
- Querying identity-marked SpaceRequests in the user's namespace.
- Discovering the unique identity-marked Helm release, reading its status and generated values, then rechecking release identity before returning those values.

Live release and Lease state take precedence over advisory process-local worker records. Local records retain safe failure details when the cluster has no release outcome; operation-owned updates prevent an old worker from overwriting a successor after takeover.

This supports page refresh and re-launch without losing state.

### Background goroutine lifecycle

- The goroutine has two independent timeouts: SpaceRequest polling uses a separate 30-minute timeout, while the configurable Helm timeout (default 300s) applies only to the install phase. Both use fresh contexts (not the HTTP request context).
- Transient errors (network blips, API server 503) are retried with backoff until the relevant phase timeout expires.
- A cluster-visible **renewable Lease reservation** coordinates setup and teardown per authenticated user and `catalogRef`; different labs remain independent. Reservation is acquired before setup mutation, renewed every 20 seconds while the worker runs, and eligible for compare-and-update takeover after 200 seconds without renewal. The process-local worker registry is not cross-pod coordination. This is best-effort exclusion, not fencing: an old in-flight Helm call may outlive its reservation.
- Worker records SR UIDs after provisioning and verifies UID, catalog label and binding identity on each readiness check. Helm's own waits and API transports are bounded; install waits for the actual SDK installer to finish rather than returning on a cancelled context while its internal goroutine continues mutating.
- Process restart loses goroutine and in-memory user token, but leaves SRs, Helm state, and Lease. On a new authenticated request, an expired reservation may be reclaimed atomically: verified existing SRs are reused if no Helm release exists; an interrupted `pending-install` release is not blindly reinstalled and requires explicitly confirmed safe teardown. Held reservations with missing or invalid renewal fields fail closed and require administrator recovery. Teardown uses same reservation pattern: Lease stores a non-secret status receipt; after expiry an unfinished receipt reports `unknown`, never success. See [explicit cleanup and recovery](administrator-guide.md#explicit-lab-cleanup).

## Explicit teardown

Setup and teardown use the same renewable Lease per `(authenticated subject, catalogRef)`; active holders block other operations while different labs can tear down concurrently. Expiry permits best-effort stale takeover but cannot guarantee an old in-flight Helm operation stopped. Setup `POST` checks all identity-marked SpaceRequests for its `catalogRef`, including bindings absent from the current schema, and returns HTTP `409` if one is terminating. Status never reports a deleting SpaceRequest as ready. Inventory marks a setup with a terminating SR as pending and blocked with the distinct `blockedReason: cleanup`.

Setup inventory uses one continuous list with identical rows and separators: existing user-owned setups first, including failures and cleanup remnants, then inactive catalog labs. Catalog-removed setups stay in that list with a fallback title and **Tear down** only; **Open** requires current catalog content. In the UI, there is no separate cleanup section, heading, or spacing between groups. Teardown status and errors are scoped to the matching row. Same-tab session storage can hold multiple non-secret `(catalogRef, operationId)` receipts and resume polling after reload. In a tab tracking a receipt, that lab's actions are disabled, but **Refresh setups** remains available; backend Lease rejects concurrent same-lab setup/teardown requests from other tabs or clients. Backend reports teardown `succeeded` after any listed Helm release is uninstalled successfully and each dedicated SR is already absent or its UID-preconditioned DELETE is accepted; it does not wait for namespace deletion. After any terminal or missing receipt, the UI keeps that row unavailable until a fresh inventory request succeeds; unknown or failed outcomes are never shown as success. After a success receipt, Launch/Open remains disabled while an SR remains terminating. While the catalog page is mounted, the client refreshes `GET /api/v1alpha1/setups` after each 10-second delay only while pending setups, cleanup blockers, held-operation blockers, or rows awaiting confirmed cleanup remain. Failed release-free setups without these in-progress signals do not trigger polling. Inventory requests are serial, retain usable rows on transient background failures, and do not show a global spinner or disable unrelated labs. **Refresh setups** remains available and shares an active inventory request. Once inventory no longer contains the SR, Launch is enabled and the catalog row shows final success; polling stops when no in-progress rows remain or the page unmounts. This inventory polling is separate from drawer setup-status polling: it never starts setup or teardown mutations and does not poll namespaces or quota reclamation. Teardown has no namespace- or quota-polling state machine.

## Authentication and Permissions

- All Kubernetes operations (SpaceRequest CRUD, Helm install) use the **user's bearer token**.
- The user's token already has the necessary permissions in SpaceRequest-created namespaces (granted by the CodeReady Toolchain).
- No elevated service account permissions needed for this flow.

## Decisions

| # | Decision | Rationale |
|---|----------|-----------|
| 1 | Labs may share repository chart files, never installed releases or SRs | Each user/catalogRef owns an independent setup |
| 2 | Backend creates SpaceRequests directly | No separate operator needed; keeps the system self-contained |
| 3 | User's token for all K8s operations | Respects existing RBAC; no privileged SA required |
| 4 | Kick-and-poll (not long-poll or WebSocket) | Simple, tolerant of disconnects, easy to implement |
| 5 | SpaceRequests in user's namespace | Natural scoping; CRT convention |
| 6 | SpaceRequests named after schema property keys | Enables idempotency and simple state reconstruction |
| 7 | Walk full JSON Schema tree for `x-interactive-labs` | Handles arbitrary nesting depths |
| 8 | Three `x-interactive-labs.kind` values: `namespace-name`, `generated-password`, `root-url` | Extensible later without redesign |
| 9 | Idempotent re-launch (reuse SpaceRequests) | Safe to retry interrupted setups |
| 10 | Cleanup requires explicit user confirmation | Helm uninstall precedes deletion of dedicated SRs; catalog-independent inventory survives removal |
| 11 | Helm Go SDK for chart deployment | Programmatic control, Wait support, bounded timeout |
| 12 | Single POST triggers full setup (SpaceRequests + Helm) | Simple frontend: one kick, then poll |
| 13 | Background goroutine watches SpaceRequests and triggers Helm | Async flow; Helm can't start until all namespaces are provisioned |
| 14 | Release name is deterministic (SHA-256 of subject + catalogRef + sorted binding keys) | Catalog labels discover prior setup despite schema drift |
| 15 | GET reconstructs both SpaceRequest and Helm state | Stateless; no in-memory tracking needed |
| 16 | Goroutine retries transient errors with backoff | Bounded by timeout; resilient to transient failures |
| 17 | Configurable Helm timeout (default 300s) | Operational flexibility; keeps setup bounded |
| 18 | Two status fields in response (spaceRequests + helm) | Frontend has full visibility into each phase |
| 19 | Re-launch when already deployed is a no-op | Avoids unnecessary cluster operations |
| 20 | Generated passwords: 24-char alphanumeric, crypto/rand | Universally accepted charset; sufficient entropy |
| 21 | Generated passwords exposed in GET response under `helm.generatedValues` | Frontend can display or substitute into lab content |
| 22 | Generated values read from Helm release, not in-memory | Stateless reconstruction; survives process restart |
| 23 | Password regeneration on retry accepted (MVP) | Simplicity; future UI may persist-on-first-generate |

## Out of Scope

- Automatic cleanup/recovery, legacy/shared SR adoption, and direct namespace deletion. Explicit teardown of verified dedicated setups is implemented.
- `x-interactive-labs` kinds beyond `namespace-name`, `generated-password`, and `root-url`.
- Configurable password length or charset via schema.
- Password persistence across retries (persist-on-first-generate).
- Detailed per-namespace progress UI (spinner only for now).
- Pre-provisioning (instructor-initiated setup ahead of time).
- Automatic retry, cancellation, or recovery of failed Helm installs.
