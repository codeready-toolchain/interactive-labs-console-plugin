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

Create/update `interactive-labs-catalog` ConfigMap in `interactive-labs-console-plugin` namespace. It must have one `catalog.yaml` key containing top-level YAML sequence. Each entry requires:

```yaml
- repository: https://github.com/redhat-scholars/my-lab
  branch: main
  labPath: lab
```

Repositories must be plain HTTPS URLs. `labPath` must be repository-relative. Customers select approved Red Hat-authored labs by adding catalog entries. Backend reads catalog `displayName` and optional `description` from each validated `lab.yaml`; legacy ConfigMap display fields are ignored. Entry order controls returned catalog order for successfully resolved labs. `catalogRef` is backend-derived; do not configure an ID.

Backend shallow-clones each `(repository, branch)` source once into `/var/lib/interactive-labs/cache/<sha256 canonical repository+branch>/content`; entry directory is reserved for future metadata and paths never expose source URLs. `Close` keeps successful snapshots, so next backend start reuses them when this storage remains. No PVC is mounted today: pod/container replacement loses cache. Future PVCs must mount `/var/lib/interactive-labs/cache`; clearing or replacing that storage makes next request clone a new snapshot. Backend does not poll or refresh sources. Catalog, bundle, and session data do not expose revision identity; resource responses expose a quoted snapshot Git `HEAD` SHA in `ETag`.

Schema, validation, and attributes: [catalog ConfigMap contract](../efforts/interactive-labs/contract/catalog-configmap.md).

## RBAC and runtime security

- Backend ServiceAccount may only `get` named catalog ConfigMap, `create` namespaced Events, and `get` named `config.openshift.io` cluster configuration: `infrastructures/cluster`, `ingresses/cluster`, and `consoles/cluster`. Runtime-attribute failure logs name only failed attribute/category; they never include tokens or values.
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

A safely configured lab is omitted from the catalog when its repository is unavailable, its `lab.yaml` is missing, or its bundle is invalid. Other valid entries remain available. Backend logs only opaque `catalogRef` and one safe reason (`RepositoryUnavailable`, `ManifestMissing`, or `LabInvalid`) and emits one namespaced `Warning` Event against `interactive-labs-catalog` for each `(catalogRef, reason)` during backend lifetime. Repeated requests do not create duplicate Events; Event-post failures do not change catalog response.

Fix malformed repository URLs, unsafe branches or lab paths, missing required source fields, and duplicate source identities in the ConfigMap. These are `configuration_error` failures for the whole catalog, not omitted entries.

## Operating boundary

**Implemented and verified:** catalog list and lab-bundle APIs, manifest-derived catalog metadata, immutable hashed approved-source snapshots, manifest/path validation, browser page/include/image resource serving and rendering, and safe catalog-omission Warning Events. Resource responses are private, revalidated browser-cache entries with a quoted cached snapshot Git `HEAD` SHA as `ETag`; this header exposes revision while catalog, bundle, and session data do not. Resource-size caps are not yet enforced.

**Not implemented:** setup does not execute Helm. It returns a success-shaped response only. Do not treat response as cluster-resource installation. Helm setup execution and namespace selection remain STORY-5 work.

**Deferred:** synchronous bounded Helm setup has no automatic retry, cleanup, cancellation, or recovery. A failure/timeout can require manual user or administrator remediation; future operational guidance must describe chart-specific recovery.
