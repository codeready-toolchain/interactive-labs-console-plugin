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

- Backend ServiceAccount has `get`, `list`, and `watch` on the named catalog ConfigMap; list/watch use the `metadata.name=interactive-labs-catalog` field selector. It also has `create` on namespaced Events and `get` on named `config.openshift.io` cluster configuration objects: `infrastructures/cluster`, `ingresses/cluster`, and `consoles/cluster`. Runtime-attribute failure logs name only failed attribute/category; they never include tokens or values.
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

## Operating boundary

**Implemented and verified:** catalog list and modules-only lab-bundle APIs, manifest-derived catalog title, immutable hashed approved-source snapshots, manifest/module/path validation, browser page/include/image resource serving and rendering, and safe catalog-omission Warning Events. Resource responses are private, revalidated browser-cache entries with a quoted cached snapshot Git `HEAD` SHA as `ETag`; this header exposes revision while catalog, bundle, and session data do not. Resource-size caps are not yet enforced.

**Current chart setup:** `POST /setup` initiates current-user SpaceRequests and an asynchronous Helm installation worker. Frontend polls `GET /setup/status` and does not show pages until ready/deployed. Worker keeps forwarded token in memory after POST; never log or persist it. No automatic cleanup or cancellation. Process restart loses worker; a user can explicitly re-launch to resume setup. See [lab setup design](lab-setup-design.md) for operational details. Namespace selection/lifecycle policy remains open; explicit existing-namespace selection is a provisional proposal, not current UI.
