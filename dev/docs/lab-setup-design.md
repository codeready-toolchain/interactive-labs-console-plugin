# Lab Setup Design

Status: in progress (STORY-5 and STORY-11).

## Overview

Before a user can work through an Interactive Lab, the environment must be provisioned: namespaces created via SpaceRequests. This document captures the design decisions for the namespace provisioning flow.

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
    }
  }
}
```

This produces two SpaceRequests, one for each namespace property.

### SpaceRequest creation

- For each namespace property, create a **SpaceRequest** Custom Resource (from the [host-operator](https://github.com/codeready-toolchain/host-operator)).
- Created in the **user's home namespace** (`<username>-dev`), derived from the authenticated username.
- Named after the schema property key (e.g. `gitea`, `datasphere`).
- Spec: `tierName: base1ns`.
- Created using the **user's bearer token** (the user already has permission to create SpaceRequests in their namespace).
- **Idempotent**: if a SpaceRequest with that name already exists, skip creation and reuse it.

## Async Setup Flow (kick-and-poll)

### POST — initiate setup

`POST /api/v1alpha1/labs/{catalogRef}/setup` creates SpaceRequests sequentially. If all succeed, it returns **202 Accepted**. If any creation fails, it returns **502 Bad Gateway** with a `configuration_error` body and stops — previously created SpaceRequests from the same request are left in place. Retrying after a partial failure is safe: `EnsureSpaceRequest` is idempotent, so already-created SpaceRequests are skipped.

### GET — poll status

`GET /api/v1alpha1/labs/{catalogRef}/setup/status` returns the current state:

```json
{
  "status": "provisioning | ready | failed",
  "namespaces": {
    "gitea": { "status": "ready", "name": "user123-abc" },
    "datasphere": { "status": "pending", "name": "" }
  }
}
```

The frontend shows a spinner while `status` is `provisioning`. Intermediate SpaceRequest conditions (e.g. `Provisioning`, `SpaceNotReady`) keep the overall status at `provisioning`. A SpaceRequest with a terminal failure reason (e.g. `UnableToCreateSpace`) moves the overall status to `failed`. The overall status becomes `ready` only when every SpaceRequest has a `Ready=True` condition **and** a non-empty `namespaceAccess.name`; `Ready=True` alone is not sufficient.

### State reconstruction

No new CRD or in-memory state is needed. The backend reconstructs setup state on each poll by querying SpaceRequests in the user's namespace that match the expected names for the lab.

## Authentication and Permissions

- All Kubernetes operations (SpaceRequest CRUD) use the **user's bearer token**.
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
| 8 | `namespace-name` is the only `x-interactive-labs.kind` for now | Extensible later without redesign |
| 9 | Idempotent re-launch (reuse SpaceRequests) | Safe to retry interrupted setups |
| 10 | Cleanup/teardown is out of scope | Ship provisioning first; teardown is a separate design |

## Out of Scope

- Helm chart deployment (subsequent PR).
- Cleanup/teardown of SpaceRequests.
- `x-interactive-labs` kinds beyond `namespace-name`.
- Detailed per-namespace progress UI (spinner only for now).
- Pre-provisioning (instructor-initiated setup ahead of time).
