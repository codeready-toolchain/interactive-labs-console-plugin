import * as asciidoctor from "@asciidoctor/core";
import mermaid from "mermaid";
import { renderAsciiDoc } from "./renderer";

const sourceURL =
  "/api/proxy/plugin/interactive-labs-console-plugin/backend/api/v1alpha1/labs/lab-1/resources/labs/one/content/pages/intro.adoc";

const loader = jest.fn(async (url: string) => {
  if (url.endsWith("partials/outer.adoc")) return "include::inner.adoc[]\nimage::../images/diagram.png[]";
  if (url.endsWith("partials/inner.adoc")) return "Nested include";
  throw new Error("resource not found");
});

describe("renderAsciiDoc", () => {
  beforeEach(() => loader.mockClear());
  afterEach(() => jest.restoreAllMocks());

  it("returns the AsciiDoc document title for drawer navigation", async () => {
    const result = await renderAsciiDoc({
      source: "= {product} introduction\n\nWelcome",
      sourceURL,
      attributes: { product: "Interactive Labs" },
      loadResource: loader,
    });

    expect(result).toMatchObject({ title: "Interactive Labs introduction" });
  });

  describe("native page xrefs", () => {
    const targetPageURL = sourceURL.replace("intro.adoc", "next.adoc");
    const xrefInput = (source: string, loadResource: (url: string, signal: AbortSignal) => Promise<string>) => ({
      source,
      sourceURL,
      attributes: {},
      pageURLs: [targetPageURL],
      loadResource,
    });

    it("uses an approved target document title for an unlabeled xref", async () => {
      const loadResource = jest.fn(async (url: string) => {
        if (url === targetPageURL) return "= Referenced Page Title";
        throw new Error("resource not found");
      });

      const result = await renderAsciiDoc(xrefInput("xref:next.adoc[]", loadResource));

      expect(result.html).toContain('<a href="next.adoc">Referenced Page Title</a>');
    });

    it("uses an approved target document title for an unlabeled fragment xref", async () => {
      const loadResource = jest.fn(async (url: string) => {
        if (url === targetPageURL) return "= Referenced Page Title";
        throw new Error("resource not found");
      });

      const result = await renderAsciiDoc(xrefInput("xref:next.adoc#section[]", loadResource));

      expect(result.html).toContain('<a href="next.adoc#section">Referenced Page Title</a>');
    });

    it("renders safe markup in an approved target document title", async () => {
      const loadResource = jest.fn(async (url: string) => {
        if (url === targetPageURL) return "= <strong>Not markup</strong>";
        throw new Error("resource not found");
      });

      const result = await renderAsciiDoc(xrefInput("xref:next.adoc[]", loadResource));

      expect(result.html).toContain('<a href="next.adoc"><strong>Not markup</strong></a>');
    });

    it("keeps an explicit xref label unchanged", async () => {
      const loadResource = jest.fn(async () => "= Referenced Page Title");

      const result = await renderAsciiDoc(xrefInput("xref:next.adoc[Custom label]", loadResource));

      expect(result.html).toContain('<a href="next.adoc">Custom label</a>');
      expect(loadResource).not.toHaveBeenCalled();
    });

    it("keeps an unapproved xref as native fallback", async () => {
      const loadResource = jest.fn(async () => "= Referenced Page Title");

      const result = await renderAsciiDoc({ ...xrefInput("xref:next.adoc[]", loadResource), pageURLs: [] });

      expect(result.html).toContain('<a href="next.adoc">next.adoc</a>');
      expect(loadResource).not.toHaveBeenCalled();
    });

    it("loads each approved xref target once", async () => {
      const loadResource = jest.fn(async (url: string) => {
        if (url === targetPageURL) return "= Referenced Page Title";
        throw new Error("resource not found");
      });

      const result = await renderAsciiDoc(xrefInput("xref:next.adoc[]\n\nxref:next.adoc[]", loadResource));

      expect(result.html.match(/Referenced Page Title/g)).toHaveLength(2);
      expect(loadResource).toHaveBeenCalledTimes(1);
    });

    it("falls back to native xref text when target loading fails", async () => {
      const loadResource = jest.fn(async () => { throw new Error("resource not found"); });

      const result = await renderAsciiDoc(xrefInput("xref:next.adoc[]", loadResource));

      expect(result.html).toContain('<a href="next.adoc">next.adoc</a>');
    });

    it("propagates abort while loading an approved xref target", async () => {
      let started!: () => void;
      const loadingStarted = new Promise<void>((resolve) => { started = resolve; });
      const loadResource = jest.fn((_url: string, signal: AbortSignal) => new Promise<string>((_resolve, reject) => {
        started();
        signal.addEventListener("abort", () => reject(new DOMException("Aborted", "AbortError")));
      }));
      const controller = new AbortController();
      const rendering = renderAsciiDoc({ ...xrefInput("xref:next.adoc[]", loadResource), signal: controller.signal });

      await loadingStarted;
      controller.abort();

      await expect(rendering).rejects.toMatchObject({ name: "AbortError" });
    });
  });

  it("sanitizes generated markup and hard-sets backend attributes", async () => {
    const load = jest.spyOn(asciidoctor, "load");
    const result = await renderAsciiDoc({
      source: "= {product}\n\npass:[<script>window.pwned = true</script>]",
      sourceURL,
      attributes: { product: "Interactive Labs" },
      loadResource: loader,
    });

    expect(load).toHaveBeenCalledWith(expect.any(String), expect.objectContaining({
      attributes: expect.objectContaining({ "product@": "Interactive Labs", relfilesuffix: ".adoc" }),
    }));
    expect(result.html).toContain("Interactive Labs");
    expect(result.html).not.toMatch(/script|pwned/i);
    load.mockRestore();
  });

  it("resolves parser-expanded nested relative includes, blocks unsafe includes, and rewrites local images", async () => {
    const result = await renderAsciiDoc({
      source: "include::{outer}[]\n\ninclude::https://evil.example/x.adoc[]\n\nimage::data:image/png;base64,AA==[]",
      attributes: { outer: "../partials/outer.adoc" },
      sourceURL,
      loadResource: loader,
    });

    expect(loader).toHaveBeenCalledWith(expect.stringMatching(/partials\/outer\.adoc$/), expect.any(AbortSignal));
    expect(loader).toHaveBeenCalledWith(expect.stringMatching(/partials\/inner\.adoc$/), expect.any(AbortSignal));
    expect(loader).not.toHaveBeenCalledWith(expect.stringMatching(/evil\.example/), expect.any(AbortSignal));
    expect(result.html).toContain("Nested include");
    expect(result.html).toContain("/resources/labs/one/content/images/diagram.png");
    expect(result.html).not.toMatch(/evil\.example|data:image/i);
    expect(result.warnings).toContain("Included resource is unavailable or unsafe.");
  });

  it("uses native labels and preserves styles from separately sanitized Mermaid SVG", async () => {
    const result = await renderAsciiDoc({
      source: "[source,mermaid]\n----\nflowchart LR\n  A --> B\n----",
      sourceURL,
      attributes: {},
      loadResource: loader,
    });

    expect(result.html).toContain("<style>.node{fill:#fff}</style>");
    expect(mermaid.initialize).toHaveBeenCalledWith({
      startOnLoad: false,
      securityLevel: "strict",
      htmlLabels: false,
    });
    expect(result.html).toContain('style="stroke:#000"');
    expect(result.html).toContain("<text>Browser</text>");
    expect(result.html).not.toMatch(/foreignObject|script|onload/i);
  });

  it("namespaces generated fragment IDs and same-fragment links for concurrent renders", async () => {
    const load = jest.spyOn(asciidoctor, "load").mockResolvedValue({
      convert: async () => '<h2 id="step">Step</h2><a href="#step">Read step</a>',
      getDocumentTitle: () => null,
    } as never);
    const input = { source: "== Step", sourceURL, attributes: {}, loadResource: loader };

    const [first, second] = await Promise.all([renderAsciiDoc(input), renderAsciiDoc(input)]);
    const root = document.createElement("div");
    root.innerHTML = first.html + second.html;
    const headings = Array.from(root.querySelectorAll("h2"));
    const ids = Array.from(root.querySelectorAll("[id]"), (element) => element.id);

    expect(headings).toHaveLength(2);
    expect(headings.every(({ id }) => /^interactive-labs-(?:\d+-){3}\d+-step$/.test(id))).toBe(true);
    expect(headings[0].id).not.toBe(headings[1].id);
    expect(new Set(ids).size).toBe(ids.length);
    for (const heading of headings) expect(root.querySelector(`a[href="#${heading.id}"]`)).not.toBeNull();
    load.mockRestore();
  });

  it("gives concurrent Mermaid renders distinct CSS-safe IDs", async () => {
    (mermaid.render as jest.Mock).mockClear();
    const input = {
      source: "[source,mermaid]\n----\nflowchart LR\n  A --> B\n----",
      sourceURL,
      attributes: {},
      loadResource: loader,
    };

    await Promise.all([renderAsciiDoc(input), renderAsciiDoc(input)]);

    const ids = (mermaid.render as jest.Mock).mock.calls.map(([id]) => id);
    expect(ids).toHaveLength(2);
    expect(new Set(ids).size).toBe(ids.length);
    expect(ids.every((id) => /^interactive-labs-(?:\d+-){3}\d+-mermaid-0$/.test(id))).toBe(true);
  });

  it("keeps styles forbidden in AsciiDoc-derived HTML", async () => {
    const load = jest.spyOn(asciidoctor, "load").mockResolvedValue({
      convert: async () => '<p style="color:red">text</p>',
      getDocumentTitle: () => null,
    } as never);
    const result = await renderAsciiDoc({
      source: "text",
      sourceURL,
      attributes: {},
      loadResource: loader,
    });

    expect(result.html).toBe("<p>text</p>");
    load.mockRestore();
  });

  it("reports recoverable resource and Mermaid failures without returning unsafe markup", async () => {
    const result = await renderAsciiDoc({
      source: "include::missing.adoc[]\n\n[source,mermaid]\n----\nnot a diagram\n----",
      sourceURL,
      attributes: {},
      loadResource: loader,
    });

    expect(result.warnings).toContain("Included resource is unavailable or unsafe.");
    expect(result.warnings).toContain("Mermaid diagram could not be rendered.");
    expect(result.html).toContain("Included resource is unavailable or unsafe.");
  });

  it("renders approved console guidance from its fixed registry route only", async () => {
    const result = await renderAsciiDoc({
      source: "console:qs-nav-search[Open Search]",
      sourceURL,
      attributes: {},
      loadResource: loader,
    });

    expect(result.html).toMatch(/<a href="\/search-page" data-interactive-labs-guidance-id="qs-nav-search" data-interactive-labs-guidance-token="[^"]+">Open Search<\/a>/);
    expect(result.guidanceLinks).toEqual([
      expect.objectContaining({ id: "qs-nav-search", href: "/search-page", token: expect.any(String) }),
    ]);
  });

  it("renders a console positional label as inline AsciiDoc", async () => {
    const result = await renderAsciiDoc({
      source: "console:qs-nav-home[*Home*]",
      sourceURL,
      attributes: {},
      loadResource: loader,
    });

    expect(result.html).toMatch(/<a href="#" data-interactive-labs-guidance-id="qs-nav-home" data-interactive-labs-guidance-token="[^"]+"><strong>Home<\/strong><\/a>/);
  });

  it("renders ordinary resource macros embedded in text", async () => {
    const result = await renderAsciiDoc({
      source: "Open resource:v1/Pod[pods] from this page.",
      sourceURL,
      attributes: {},
      loadResource: loader,
    });

    expect(result.html).toContain('Open <a href="/k8s/all-namespaces/core~v1~Pod">pods</a> from this page.');
  });

  it.each([
    ["resource:v1/Pod[*All pods*]", {}, '<a href="/k8s/all-namespaces/core~v1~Pod"><strong>All pods</strong></a>'],
    ["resource:apps/v1/Deployment[*Namespace deployments*,namespace={lab_namespace}]", { lab_namespace: "team-a" }, '<a href="/k8s/ns/team-a/apps~v1~Deployment"><strong>Namespace deployments</strong></a>'],
    ["resource:apps/v1/Deployment[`Named deployment`,name=api-server]", {}, '<a href="/k8s/all-namespaces/apps~v1~Deployment?name=api-server"><code>Named deployment</code></a>'],
    ["resource:apps/v1/Deployment[*Open deployment*,name={deployment_name},namespace={lab_namespace}]", { deployment_name: "api", lab_namespace: "team-a" }, '<a href="/k8s/ns/team-a/apps~v1~Deployment/api"><strong>Open deployment</strong></a>'],
    ["resource:rbac.authorization.k8s.io/v1/ClusterRole[All roles,scope=cluster]", {}, '<a href="/k8s/cluster/rbac.authorization.k8s.io~v1~ClusterRole">All roles</a>'],
    ["resource:rbac.authorization.k8s.io/v1/ClusterRole[Open role,scope=cluster,name=view]", {}, '<a href="/k8s/cluster/rbac.authorization.k8s.io~v1~ClusterRole/view">Open role</a>'],
    ["resource:project.openshift.io/v1/Project[Open project,scope=cluster,name={user}-datasphere]", { user: "jane" }, '<a href="/k8s/cluster/project.openshift.io~v1~Project/jane-datasphere">Open project</a>'],
    ["resource:project.openshift.io/v1/Project[Open project,scope=cluster,name={prefix}-{suffix}]", { prefix: "jane", suffix: "datasphere" }, '<a href="/k8s/cluster/project.openshift.io~v1~Project/jane-datasphere">Open project</a>'],
    ["resource:apps/v1/Deployment[Namespace deployments,namespace={prefix}-{suffix}]", { prefix: "team", suffix: "a" }, '<a href="/k8s/ns/team-a/apps~v1~Deployment">Namespace deployments</a>'],
    ["resource:apps/v1/deployment[Lower-case kind]", {}, '<a href="/k8s/all-namespaces/apps~v1~deployment">Lower-case kind</a>'],
    ["resource:apps?group/v1/Deployment[Question mark in group]", {}, '<a href="/k8s/all-namespaces/apps%3Fgroup~v1~Deployment">Question mark in group</a>'],
    ["resource:apps/v1#beta/Deployment[Hash in version]", {}, '<a href="/k8s/all-namespaces/apps~v1%23beta~Deployment">Hash in version</a>'],
    ["resource:apps/v1/deployment~kind[Tilde in lower-case kind]", {}, '<a href="/k8s/all-namespaces/apps~v1~deployment%7Ekind">Tilde in lower-case kind</a>'],
  ])("renders resource macro with its label first and named selectors", async (source, attributes, expected) => {
    const result = await renderAsciiDoc({ source, sourceURL, attributes, loadResource: loader });

    expect(result.html).toContain(expected);
  });

  it("renders substituted resource labels as AsciiDoc inside outer bold", async () => {
    const result = await renderAsciiDoc({
      source: "*resource:project.openshift.io/v1/Project[`{user}-datasphere`,scope=cluster,name={user}-datasphere]*",
      sourceURL,
      attributes: { user: "jane" },
      loadResource: loader,
    });

    expect(result.html).toContain('<strong><a href="/k8s/cluster/project.openshift.io~v1~Project/jane-datasphere"><code>jane-datasphere</code></a></strong>');
  });

  it("substitutes runtime attributes in resource labels", async () => {
    const result = await renderAsciiDoc({
      source: "resource:v1/Pod[Pods for {user}]",
      sourceURL,
      attributes: { user: "jane" },
      loadResource: loader,
    });

    expect(result.html).toContain('<a href="/k8s/all-namespaces/core~v1~Pod">Pods for jane</a>');
  });

  it.each([
    "console:unknown[Unknown]",
    "console:qs-nav-search[]",
    "resource:apps/v1/Deployment[Bad scope,scope=namespace]",
    "resource:apps/v1/Deployment[Invalid cluster namespace,scope=cluster,namespace=team-a]",
    "resource:apps/v1/Deployment[Unknown field,name=api,unknown=value]",
  ])("rejects unsafe ordinary macro %s without an anchor", async (source) => {
    const result = await renderAsciiDoc({ source, sourceURL, attributes: {}, loadResource: loader });

    expect(result.html).toContain("Guidance link is unavailable or unsafe.");
    expect(result.html).not.toContain("<a ");
  });

  it.each([
    ["resource:apps/v1/Deployment[Missing attribute,name={missing}]", {}, '<a href="/k8s/all-namespaces/apps~v1~Deployment?name=%7Bmissing%7D">Missing attribute</a>'],
    ["resource:project.openshift.io/v1/Project[Open project,scope=cluster,name={user}-datasphere]", {}, '<a href="/k8s/cluster/project.openshift.io~v1~Project/%7Buser%7D-datasphere">Open project</a>'],
    ["resource:project.openshift.io/v1/Project[Open project,scope=cluster,name={user}-datasphere]", { user: "Jane" }, '<a href="/k8s/cluster/project.openshift.io~v1~Project/Jane-datasphere">Open project</a>'],
    ["resource:apps/v1/Deployment[Namespace deployments,namespace={missing}]", {}, '<a href="/k8s/ns/%7Bmissing%7D/apps~v1~Deployment">Namespace deployments</a>'],
  ])("renders URI-encoded nonempty resource selectors", async (source, attributes, expected) => {
    const result = await renderAsciiDoc({ source, sourceURL, attributes, loadResource: loader });

    expect(result.html).toContain(expected);
  });

  it.each([
    ["{user", "%7Buser"],
    ["user}", "user%7D"],
    ["{}", "%7B%7D"],
    ["{user}}", "jane%7D"],
  ])("renders URI-encoded malformed resource name interpolation %s", async (name, encodedName) => {
    const result = await renderAsciiDoc({
      source: `resource:apps/v1/Deployment[Open deployment,name=${name}]`,
      sourceURL,
      attributes: { user: "jane" },
      loadResource: loader,
    });

    expect(result.html).toContain(`<a href="/k8s/all-namespaces/apps~v1~Deployment?name=${encodedName}">Open deployment</a>`);
  });

  it("processes inline macros in approved included content", async () => {
    const loadResource = jest.fn(async (url: string) => {
      if (url.endsWith("partials/guidance.adoc")) return "console:qs-nav-home[*Home*]";
      throw new Error("resource not found");
    });
    const result = await renderAsciiDoc({
      source: "include::../partials/guidance.adoc[]",
      sourceURL,
      attributes: {},
      loadResource,
    });

    expect(result.html).toMatch(/<a href="#" data-interactive-labs-guidance-id="qs-nav-home" data-interactive-labs-guidance-token="[^"]+"><strong>Home<\/strong><\/a>/);
    expect(result.guidanceLinks).toEqual([
      expect.objectContaining({ id: "qs-nav-home", href: "#", token: expect.any(String) }),
    ]);
  });

  it("preserves ordinary same-origin Console links", async () => {
    const result = await renderAsciiDoc({
      source: "https://console.example.test/search-page[Normal Console link]",
      sourceURL,
      attributes: {},
      loadResource: loader,
    });

    expect(result.html).toContain("https://console.example.test/search-page[Normal Console link]");
  });

  it("rejects before fetching includes when already aborted", async () => {
    const loadResource = jest.fn(async () => "Included content");
    const controller = new AbortController();
    controller.abort();

    await expect(renderAsciiDoc({
      source: "include::partial.adoc[]",
      sourceURL,
      attributes: {},
      loadResource,
      signal: controller.signal,
    })).rejects.toMatchObject({ name: "AbortError" });
    expect(loadResource).not.toHaveBeenCalled();
  });

  it("cancels pending include fetches", async () => {
    let reject!: (reason: unknown) => void;
    let started!: () => void;
    const pendingStarted = new Promise<void>((resolve) => { started = resolve; });
    const pending = jest.fn(
      (_url: string, signal: AbortSignal) =>
        new Promise<string>((_resolve, rejectPromise) => {
          reject = rejectPromise;
          started();
          signal.addEventListener("abort", () => rejectPromise(new DOMException("Aborted", "AbortError")));
        }),
    );
    const controller = new AbortController();
    const rendering = renderAsciiDoc({
      source: "include::pending.adoc[]",
      sourceURL,
      attributes: {},
      loadResource: pending,
      signal: controller.signal,
    });

    await pendingStarted;
    controller.abort();
    await expect(rendering).rejects.toMatchObject({ name: "AbortError" });
    reject(new DOMException("Aborted", "AbortError"));
  });
});
