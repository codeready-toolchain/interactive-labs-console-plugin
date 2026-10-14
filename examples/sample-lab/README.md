# Author an Interactive Lab

This guide is for authors who already know AsciiDoc, OpenShift, and Helm. It walks through the sample's supported content features and optional chart-based setup.

## Choose how to catalog the sample

You can catalog this repository directly with `labPath: examples/sample-lab` and launch its two content pages with the included illustrative chart. An administrator must approve the repository's HTTPS URL and branch in the catalog. See the [administrator guide](../../dev/docs/administrator-guide.md#configure-catalog) for details.

### Publish the sample in a standalone repository

The sample chart works in either repository layout. To publish the sample separately, copy the sample's contents—not the enclosing directory—to the root of a new, standalone repository:

```sh
# From this plugin repository's root; choose a new, empty destination.
mkdir ../my-lab
cp -R examples/sample-lab/. ../my-lab/
```

Publish the new repository, then ask an administrator to catalog its approved HTTPS URL and branch with `labPath: .`. Save the following complete ConfigMap manifest as `catalog.yaml`, replacing the example URL and branch with your published source:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: interactive-labs-catalog
  namespace: interactive-labs-console-plugin
data:
  catalog.yaml: |
    - repository: https://github.com/your-organization/my-lab
      branch: main
      labPath: .
      attributeOverrides:
        lab-title: "Workshop lab"
        greeting: "Welcome to the workshop"
```

Apply the manifest with `oc apply -f catalog.yaml`. The `data.catalog.yaml` value is a YAML sequence stored as a multiline string. If the ConfigMap already lists other labs, keep those entries in the sequence; applying this manifest replaces the sequence.

You can also catalog the sample directly from this plugin repository by using its published HTTPS URL and branch, with `labPath: examples/sample-lab`.

## Understand the file layout and navigation

```text
lab.yaml
modules/010-intro/content/
  010-start.adoc
  020-resources.adoc
  _partials/note.adoc
  images/flow.png
setup/helm/
  Chart.yaml
  values.yaml
  values.schema.json
  templates/resources.yaml
```

The required fields in `lab.yaml` are `apiVersion: v1alpha1`, `kind: Lab`, and `metadata.title`. You can also set `metadata.description` for catalog text and `spec.attributes` as a map of strings. Unknown fields are rejected.

Each immediate directory under `modules/` is a module, and `.adoc` files under its `content/` directory are pages. Module IDs and page paths sort **bytewise** (by their underlying byte values), so zero-pad numbers to get the intended order. The first page in that order is the entry page. Previous and Next navigation continues across module boundaries, and each module-selector title comes from that module's first page. An `.adoc` file with any path segment beginning with `_` is include-only. Optional `module.yaml` checkpoints are validated as data only: **no checkpoint UI or completion tracking is implemented as of now**.

## Write supported AsciiDoc

| Feature | Example | Behavior |
| --- | --- | --- |
| Page xref | `xref:020-resources.adoc[]` | Source-relative path to a known navigable page; empty label uses target document title, including effective attributes. An explicit `[label]` stays yours. |
| Same-page fragment | `[[try-links]]` and `<<try-links,Return to this section>>` | Local anchor; renderer namespaces IDs and fragment links together. |
| Partial | `include::_partials/note.adoc[]` | Include resolves relative to current source; nested includes resolve from included file. |
| Local image | `image::images/flow.png[Two blue pages connected by a green arrow,width=192]` | Image resolves from active page; supply meaningful alt text. |
| Attribute | `{lab-title}`, `{user}`, `{openshift_console_url}` | Manifest, runtime, or setup values substitute into content. |

Use source-relative paths for files. To refer to a page in another module, for example, use `../../020-next/content/010-start.adoc`. Unknown-page links produce warnings. Includes and images may refer to files outside the lab directory, as long as those files are still in the approved repository. Nested includes resolve relative to the file that includes them; images in partials remain relative to the active page.

Use local PNG, JPEG, GIF, or WebP images. Remote images, remote includes, and author-provided SVG are not supported. Add meaningful alt text to images. Arbitrary scripts, selectors, and extensions are not allowed, and rendered output is sanitized. Include nesting is limited to 64 levels.

Use page xrefs to navigate between known pages and fragments to scroll to sections inside the drawer, including sections on another page. Missing sections do not scroll:

```asciidoc
xref:020-resources.adoc[]              // Open another page
<<try-links,Try links>>                // Jump within the current page
xref:010-start.adoc#try-links[Try links] // Jump to a section on another known page
```

### Lab attributes

Values from the lab's `lab.yaml` `spec.attributes` and the catalog entry's optional `attributeOverrides` merge in this order: **`lab.yaml` → catalog overrides → backend runtime**, with each later value taking precedence. Runtime attribute names are `user`, `openshift_api_url`, `openshift_console_url`, and `openshift_cluster_ingress_domain`. After successful setup, setup attributes overlay the bundle attributes before rendering.

For example, a lab can define these string values in `lab.yaml`:

```yaml
spec:
  attributes:
    lab-title: "Sample lab"
    greeting: "Hello"
```

Administrators can override them per catalog entry under `attributeOverrides`, as in the full ConfigMap example above; this is not a top-level ConfigMap `data` key. In AsciiDoc, `{greeting}, welcome to {lab-title}.` uses the effective values. These substitutions affect rendered content only; `lab-title` does not replace `metadata.title` or change the catalog display title.

Asciidoctor receives these as **soft settings** (`name@`), which means a header declaration such as `:local-note: ...` can override a supplied value. Avoid redefining runtime or setup attribute names. This sample uses `ifdef::namespaces-demo[]` and `ifndef::namespaces-demo[]` so it can render with or without setup attributes without unresolved placeholders.

## Link to the OpenShift Console

A link such as `console:qs-nav-topology[Topology]` navigates to a fixed route and then spotlights its visible target. Authors cannot supply routes or selectors. Whether a target is available depends on the Console version, perspective, and permissions; unavailable targets produce feedback.

The supported Console guidance IDs are:

- **Navigate, then spotlight:** `qs-nav-add`, `qs-nav-topology`, `qs-admin-nav-topology`, `qs-nav-search`, `qs-nav-project`, `qs-nav-functions`, `qs-nav-helm`, `qs-nav-builds`, `qs-nav-monitoring`.
- **Spotlight only if already visible:** `qs-nav-home`, `qs-nav-ecosystem`, `qs-nav-workloads`, `qs-nav-networking`, `qs-nav-storage`, `qs-nav-compute`, `qs-nav-usermanagement`, `qs-nav-administration`, `qs-nav-serverless`, `qs-perspective-switcher`, `qs-masthead-cloudshell`, `qs-masthead-import`, `qs-masthead-utilitymenu`, `qs-masthead-usermenu`, `qs-masthead-notifications`, `qs-masthead-help`.

A resource link such as `resource:v1/Pod[All Pods]` opens a list of that resource kind in the active Console project. If the active project is All Projects, the list stays across all namespaces. A cluster-scoped link such as `resource:project.openshift.io/v1/Project[Projects,scope=cluster]` opens a cluster-wide list.

The resource target is `version/Kind` or `group/version/Kind`. Put the label first, followed by optional `scope`, `namespace`, and `name` values. Cluster scope does not accept `namespace`.

To open a specific namespaced resource's details, provide both its namespace and name. For example, `resource:v1/ConfigMap[Sample ConfigMap,namespace={namespaces-demo},name=sample-info]` opens the named ConfigMap. Providing only a name opens a filtered list, not the resource details page. Console handles resource existence and RBAC; these links do not create resources and leave the lab drawer open. See the [content contract](../../dev/docs/lab-content-contract.md#implemented-authoring-rules) for the full rules.

## Understand the Helm setup

Helm setup can be colocated with the lab: put the chart in `<labPath>/setup/helm`, beside `lab.yaml` and `modules/`. This sample already uses that layout, so its chart works both when cataloged here with `labPath: examples/sample-lab` and when copied into a standalone repository with `labPath: .`.

Chart selection follows these rules:

1. A nonempty `spec.setupDir` selects the chart directory itself, relative to the lab directory. It is authoritative: a missing or invalid chart fails validation without falling back.
2. Otherwise, use `<labPath>/setup/helm` when that directory exists. An invalid lab-local chart does not trigger fallback.
3. Only when the lab-local directory is absent, try `<repository-root>/setup/helm`.

For example, a lab at `labs/intro` can select a shared chart at `<repository-root>/shared/chart`:

```yaml
spec:
  setupDir: ../../shared/chart
```

The selected chart must remain within the approved repository; symlinks in the chart path or tree are rejected. Labs may share chart files, but never installed Helm releases or SpaceRequests. See the [chart-selection contract](../../dev/docs/lab-content-contract.md#prerequisite-chart-selection) for details. For a content-only lab, omit `spec.setupDir` and ensure neither default chart directory exists; in a standalone repository, omit `setup/`.

The sample chart creates only a `sample-info` ConfigMap and a `sample-credentials` Secret in a provisioned demo namespace. It has no dependencies, Pods, application, Service, or Route.

When a chart is available, Launch starts asynchronous setup. It creates or reuses SpaceRequests in `<user>-dev`, waits for them to become ready, and installs Helm using the current user's credentials. The frontend polls until setup is ready and deployed before showing the pages. This requires CodeReady Toolchain SpaceRequests, the user's home namespace and permissions, and cluster ingress configuration; it is not a generic namespace selector.

Place annotations on the exact JSON Schema `properties` paths that match Helm values, not in a separate annotation tree. The sample's [values schema](setup/helm/values.schema.json) shows all three supported kinds, and its [resource templates](setup/helm/templates/resources.yaml) consume the values:

| Kind | Values path | Setup value | Render attribute |
| --- | --- | --- | --- |
| `namespace-name` | `namespaces.demo` | Namespace from SpaceRequest `demo` | `{namespaces-demo}` |
| `generated-password` | `demo.password` | 24-character cryptographically random alphanumeric password, recovered from deployed Helm release values | `{demo-password}` |
| `root-url` | `demo.rootUrl` | `sample-<provisioned-namespace>.<cluster-ingress-domain>` | `{demo-rootUrl}` |

For render attributes, dots in a values path become hyphens, while case is preserved (for example, `rootUrl`). `namespaceRef: demo` refers to the binding **key**, not to `namespaces.demo`; `routeName: sample` names the intended Route. A `root-url` is a **hostname without a scheme**. It does not create or check a Route, and this chart stores the hostname without providing a working website. There is no schema-driven namespace-picker UI. Keep namespace binding keys unique. A binding normally uses the leaf property name as its key. For paths ending in `.namespace`, the key is instead the full path with that suffix removed; see the [setup reference](../../dev/docs/lab-setup-design.md).

Generated credentials are returned to the browser and rendered lab, so use them only for ephemeral demos—never commit real secrets. Reopening the lab reuses a deployed release. 

