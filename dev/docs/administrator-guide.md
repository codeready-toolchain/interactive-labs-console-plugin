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
- displayName: My Lab
  repository: https://github.com/redhat-scholars/my-lab
  branch: main
  labPath: lab/
```

Repositories must be HTTPS. Customers select approved Red Hat-authored labs by adding catalog entries. Entry order controls returned catalog order. `catalogRef` is backend-derived; do not configure an ID.

Schema, validation, and attributes: [catalog ConfigMap contract](../efforts/interactive-labs/contract/catalog-configmap.md).

## RBAC and runtime security

- Backend ServiceAccount may only `get` named catalog ConfigMap.
- Setup/bundle calls authenticate forwarded current-user token; browser never sends privileged credentials.
- Pod runs non-root with privilege escalation disabled, all capabilities dropped, and RuntimeDefault seccomp.

## Validate

```sh
make test
make build
make validate-deploy
make validate-demo-catalog
make validate-catalog-namespace
```

## Operating boundary

**Implemented and verified:** catalog list and lab-bundle APIs, HTTPS repository load, manifest/path validation.

**Not implemented:** frontend launch is disabled and setup does not execute Helm. It returns a success-shaped response only. Do not treat response as cluster-resource installation.

**Decided target / planned:** synchronous bounded Helm setup has no automatic retry, cleanup, cancellation, or recovery. A failure/timeout can require manual user or administrator remediation; future operational guidance must describe chart-specific recovery.
