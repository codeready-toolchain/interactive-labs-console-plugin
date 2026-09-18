# Lab Setup Design

Status: in progress (STORY-5 and STORY-11).

## Overview

Before a user can work through an Interactive Lab, the environment must be provisioned: namespaces created via SpaceRequests, then a Helm chart deployed with the provisioned namespace values. This document captures the design decisions for the full setup flow.

## Trigger

- User clicks **Launch** on a lab in the console UI.
- Frontend sends `POST /api/v1alpha1/labs/{catalogRef}/setup` with an empty body.
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
- **Idempotent**: if a SpaceRequest with that name already exists, skip creation and reuse it.

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

- **Release name**: `il-` + first 12 hex characters of SHA-256(`subject`, `catalogRef`, `branch`). Deterministic and user-specific.
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
- If the release exists in a failed state, a re-POST may trigger a new install attempt.

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

- `helm.message` is present only when `helm.status` is `failed`; it carries the error detail.
- `helm.generatedValues` is present only when `helm.status` is `deployed`; it contains the generated password values used during installation, read from the Helm release object via the SDK (stateless reconstruction).

Status values:

| Field | Value | Meaning |
|-------|-------|---------|
| `spaceRequests.status` | `provisioning` | One or more SpaceRequests not yet Ready |
| | `ready` | All SpaceRequests have `Ready=True` |
| | `failed` | A SpaceRequest has a terminal failure condition |
| `helm.status` | `pending` | No Helm release found |
| | `installing` | Helm release exists with status `pending-install` or `pending-upgrade` |
| | `deployed` | Helm release exists with status `deployed` |
| | `failed` | Helm release failed or goroutine timed out |

The frontend polls GET and shows a progress indicator. The lab opens when `spaceRequests.status` is `ready` **and** `helm.status` is `deployed`.

### State reconstruction

No new CRD or in-memory state is needed. The GET handler reconstructs setup state on each poll by:
- Querying SpaceRequests in the user's namespace (existing behavior).
- Querying the Helm release status via the Helm Go SDK (new).

This supports page refresh and re-launch without losing state.

### Background goroutine lifecycle

- The goroutine is bounded by the configurable Helm timeout (default 300s). It uses a fresh context (not the HTTP request context).
- Transient errors (network blips, API server 503) are retried with backoff until the timeout expires.
- A **concurrency guard** prevents duplicate Helm installs: before starting, the goroutine checks if a Helm release already exists in any state.
- If the process restarts while the goroutine is running, the goroutine is lost. The user can re-launch (POST again) to restart the flow. SpaceRequests and any partial Helm release persist in the cluster.

## Authentication and Permissions

- All Kubernetes operations (SpaceRequest CRUD, Helm install) use the **user's bearer token**.
- The user's token already has the necessary permissions in SpaceRequest-created namespaces (granted by the CodeReady Toolchain).
- No elevated service account permissions needed for this flow.

## Decisions

| # | Decision | Rationale |
|---|----------|-----------|
| 1 | Labs in a repository share the same setup | Simplicity; one values schema per content repo |
| 2 | Backend creates SpaceRequests directly | No separate operator needed; keeps the system self-contained |
| 3 | User's token for all K8s operations | Respects existing RBAC; no privileged SA required |
| 4 | Kick-and-poll (not long-poll or WebSocket) | Simple, tolerant of disconnects, easy to implement |
| 5 | SpaceRequests in user's namespace | Natural scoping; CRT convention |
| 6 | SpaceRequests named after schema property keys | Enables idempotency and simple state reconstruction |
| 7 | Walk full JSON Schema tree for `x-interactive-labs` | Handles arbitrary nesting depths |
| 8 | Three `x-interactive-labs.kind` values: `namespace-name`, `generated-password`, `root-url` | Extensible later without redesign |
| 9 | Idempotent re-launch (reuse SpaceRequests) | Safe to retry interrupted setups |
| 10 | Cleanup/teardown is out of scope | Ship provisioning first; teardown is a separate design |
| 11 | Helm Go SDK for chart deployment | Programmatic control, Wait support, bounded timeout |
| 12 | Single POST triggers full setup (SpaceRequests + Helm) | Simple frontend: one kick, then poll |
| 13 | Background goroutine watches SpaceRequests and triggers Helm | Async flow; Helm can't start until all namespaces are provisioned |
| 14 | Release name is deterministic (SHA-256 of subject + catalogRef + branch) | Enables idempotent re-launch and state reconstruction |
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

- Cleanup/teardown of SpaceRequests and Helm releases.
- `x-interactive-labs` kinds beyond `namespace-name`, `generated-password`, and `root-url`.
- Configurable password length or charset via schema.
- Password persistence across retries (persist-on-first-generate).
- Detailed per-namespace progress UI (spinner only for now).
- Pre-provisioning (instructor-initiated setup ahead of time).
- Automatic retry, cancellation, or recovery of failed Helm installs.
