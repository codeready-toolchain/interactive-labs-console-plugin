# Health probes

Design decisions for Kubernetes liveness, readiness, and startup probes.

## Endpoint layout

**Decision:** separate endpoints — `GET /api/healthz` (liveness) and `GET /api/readyz` (readiness).

**Why:** the server may be alive but not ready while it loads the catalog and warms the cache. Separate endpoints let Kubernetes distinguish "needs a restart" from "stop routing traffic here."

## Path prefix

**Decision:** under `/api/` (e.g., `/api/healthz`, `/api/readyz`).

**Why:** the `combinedHandler` already routes `/api/*` to the Go router and everything else to static file serving. Placing health endpoints under `/api/` avoids routing changes.

## Startup probe

**Decision:** yes, pointing at `/api/healthz`.

**Why:** the server clones git repositories at startup, which can take a while. Without a startup probe, a tight liveness check could kill the pod before initialization finishes. The startup probe runs instead of liveness during initial boot with a generous failure budget.

## Liveness vs. readiness checks

**Decision:** shallow liveness, deep readiness.

- **Liveness** (`/api/healthz`): returns HTTP 200 if the server can handle HTTP. No dependency checks.
- **Readiness** (`/api/readyz`): returns HTTP 200 only after the first `RefreshCoordinator` sweep completes and the server is not shutting down. Returns 503 otherwise.

**Why:** a deep liveness check risks restart cascades when a transient dependency fails. Readiness is the safe place for dependency checks — the pod stays alive but stops receiving traffic until the condition recovers.

## Shutdown behavior

**Decision:** readiness returns 503 during graceful shutdown.

**Why:** `http.Server.Shutdown()` stops accepting new connections, but Kubernetes checks readiness to decide whether to route traffic. If readiness stays 200 during shutdown, the pod remains in Service endpoints and new requests can still arrive via the load balancer. Flipping readiness to 503 before calling `Shutdown()` tells Kubernetes to remove the pod from endpoints, giving existing requests time to drain without new ones arriving.

## Response format

**Decision:** HTTP status code only (200 or 503). No JSON body.

**Why:** the kubelet only cares about the status code. A JSON body adds complexity and creates a temptation to parse it. Component-level observability belongs in metrics and logging, not probe responses.

## Authentication

**Decision:** health endpoints are unauthenticated.

**Why:** Kubernetes kubelet probes do not send authentication tokens. The catalog endpoint is already unauthenticated, so this is consistent with the existing pattern.

## Probe timing

| Probe | Endpoint | periodSeconds | timeoutSeconds | failureThreshold | Max tolerance |
|---|---|---|---|---|---|
| Startup | `/api/healthz` | 5 | — | 60 | 5 minutes |
| Liveness | `/api/healthz` | 10 | 3 | 3 | 30 seconds |
| Readiness | `/api/readyz` | 5 | 3 | 3 | 15 seconds |

**Why:**
- **Startup** budget of 5 minutes is generous but safe — git clones over flaky networks can be slow.
- **Liveness** at 10-second intervals avoids false positives from momentary slowness.
- **Readiness** at 5-second intervals is responsive enough to catch real issues without hammering the endpoint.

## Logging

**Decision:** health probe requests are not logged.

**Why:** probes hit every 5–10 seconds. Logging every 200 response adds no signal and drowns real requests. If a probe fails, Kubernetes events and pod status already surface the problem.
