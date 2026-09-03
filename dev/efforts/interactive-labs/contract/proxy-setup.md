# Console Service Proxy — Operational Contract

## ConsolePlugin proxy declaration

```yaml
proxy:
  - alias: backend
    authorization: UserToken
    endpoint:
      type: Service
      service:
        name: interactive-labs-console-plugin
        namespace: interactive-labs-console-plugin
        port: 9001
```

`authorization: UserToken` causes Console to inject the current user's bearer token into
every forwarded request. The plugin must **not** use `None` or omit this field.

## Plugin call path

```
consoleFetch("/api/proxy/plugin/interactive-labs-console-plugin/backend/api/v1alpha1/...")
```

This prefix is the canonical proxy URL for plugin name `interactive-labs-console-plugin` and alias `backend`.

## Backend token obligations

1. Never log `Authorization` header or any `Bearer`/`eyJ` substring.
2. Never persist the token in any store, cache, or audit record.
3. Treat token as request-scoped; discard on response.
4. Return typed errors only; never echo token material in response bodies.

## Backend HTTPS / serving-cert wiring

Backend Service is annotated with:
```
service.beta.openshift.io/serving-cert-secret-name: interactive-labs-console-plugin-tls
```

The cluster injects a signed TLS certificate into the named Secret. The unified Deployment mounts it at
`/etc/tls/backend/tls.crt` and `/etc/tls/backend/tls.key`. Console proxy trusts the cluster CA automatically;
no `caCertificate` field is needed in the ConsolePlugin proxy stanza.

## Unified image and Kustomize deployment

`Containerfile` builds plugin assets and backend into one image. `deploy/base` is the deployable
Kustomize base: its one HTTPS Service serves plugin assets and proxied API requests from the same Pod. Build with `podman build -f Containerfile -t interactive-labs .`;
render or apply with `oc kustomize deploy/base`.
