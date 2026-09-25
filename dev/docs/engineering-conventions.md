# Engineering conventions

## Repository structure

| Path | Convention |
| --- | --- |
| `frontend/src/api` | Console proxy client and response/error parsing |
| `frontend/src/types` | API, manifest, setup, session, and error types |
| `frontend/src/**/__tests__` | frontend unit tests colocated with concern |
| `backend/internal/{bundle,catalog,errors,handler,manifest}` | Go packages by responsibility |
| `backend/*_test.go` | external-package tests using `testify/assert` and `testify/require` |
| `deploy/base` | deployable Kustomize base |
| `deploy/demo` | catalog-only demo overlay |
| `dev/efforts/interactive-labs` | planning records and canonical contracts |

## Frontend

- TypeScript compiles in strict mode; do not introduce implicit `any` or `any` escape hatches.
- Use Dynamic Plugin SDK `consoleFetch` and canonical proxy prefix. Browser does not own authorization tokens, repository access, cluster calls, or Helm execution.
- Register exposed module and Console extensions in `frontend/package.json`; export entry component from `frontend/src/index.ts`.
- Use direct PatternFly dependencies/components, not SDK transitive dependencies.
- API failures use typed `v1alpha1/Error`; unknown errors become `unknown_error`.
- Use Jest + `ts-jest` + jsdom. Existing component tests use React 17 `act` and `ReactDOM.render`.

## Backend

- Keep dependencies injectable through config functions/structs, not global service state.
- Use standard `net/http` `ServeMux` method/path patterns.
- Return typed error envelopes for non-2xx API responses.
- Validate untrusted boundary input before use: HTTPS repository URL, branch characters, manifest version/type, bundle paths, catalog shape, and namespace request shape.
- Use `github.com/go-git/go-git/v5` for every repository clone, open, and revision operation. Never invoke a Git executable; use private temporary clone directories and cleanup.
- Preserve catalog declaration order. Derive canonical catalog identity server-side.
- Tokens are request-scoped. Never place token material in logs, errors, caches, databases, or audit records.

## Build, test, and validation

`Makefile` is local entry point:

```sh
make test                 # frontend type-check, Jest coverage, lint; Go test and vet
make build                # frontend and backend build
make all                  # test then build
make validate-deploy      # render/apply base with client dry-run
make validate-demo-catalog
```

Frontend commands use `npm --prefix frontend ci`; lockfile is committed. Backend commands run from `backend/` using Go module tooling. Container build defaults to Podman.

## Deployment and security

- Build frontend and backend into one minimal image.
- Serve UI assets and API from one HTTPS Service.
- Use OpenShift serving certificate, `UserToken` Console proxy, and non-root runtime.
- Drop all capabilities, disable privilege escalation, use RuntimeDefault seccomp, and grant least-privilege ConfigMap RBAC.
- Namespace and catalog names are fixed: `interactive-labs-console-plugin` and `interactive-labs-catalog`.

## Change discipline

- Prefer existing helpers, types, dependencies, and native platform features over new abstractions/dependencies.
- Tests precede production-code changes: reproduce failure, confirm red, implement minimum fix, keep tests green.
- In this repository use `jj`, not Git commands, for version-control operations.
