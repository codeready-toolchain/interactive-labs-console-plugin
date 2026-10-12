import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { TextEncoder } from "node:util";
import { renderAsciiDoc } from "./renderer";

// Exercise the pinned real processor, not the unit tests' lightweight AsciiDoc stub.
jest.mock("@asciidoctor/core", () => {
  // Its Node bundle derives createRequire's file URL from currentScript under jsdom.
  const script = document.createElement("script");
  script.src = `file://${__dirname}/../node_modules/@asciidoctor/core/build/node/index.cjs`;
  const currentScript = jest.spyOn(document, "currentScript", "get").mockReturnValue(script);
  try {
    return jest.requireActual("../node_modules/@asciidoctor/core/build/node/index.cjs");
  } finally {
    currentScript.mockRestore();
  }
});

// jsdom omits this browser API used by real Asciidoctor attribute parsing.
Object.assign(globalThis, { TextEncoder });

const sampleRoot = resolve(__dirname, "../../examples/sample-lab");
const resourcePrefix = "/api/proxy/plugin/interactive-labs-console-plugin/backend/api/v1alpha1/labs/sample/resources/";
const contentPath = "modules/010-intro/content/";
const pages = ["010-start.adoc", "020-resources.adoc"];
const pageURLs = pages.map((page) => resourcePrefix + contentPath + page);

const loadResource = async (url: string): Promise<string> =>
  readFileSync(resolve(sampleRoot, new URL(url, window.location.origin).pathname.slice(resourcePrefix.length)), "utf8");

const bundleAttributes: Record<string, string> = {
  "lab-title": "Interactive Labs sample",
  "local-note": "External default overridden by document header",
  user: "learner",
  openshift_console_url: "https://console.example.test",
};
const setupAttributes: Record<string, string> = {
  "namespaces-demo": "learner-demo",
  "demo-password": "example-password",
  "demo-rootUrl": "sample-learner-demo.apps.example.test",
};

const renderPage = (source: string, page: string, attributes = bundleAttributes) => renderAsciiDoc({
  source, sourceURL: resourcePrefix + contentPath + page, attributes, pageURLs, loadResource,
});

describe("sample lab with real AsciiDoc", () => {
  it("expands bundle attributes and lets the document header override external defaults", async () => {
    // Given the start page and external bundle attributes, including a local-note default.
    const source = readFileSync(resolve(sampleRoot, contentPath, "010-start.adoc"), "utf8");
    const attributes = { ...bundleAttributes };

    // When rendering through the public API.
    const result = await renderPage(source, "010-start.adoc", attributes);
    const start = document.createElement("div");
    start.innerHTML = result.html;

    // Then bundle attributes expand and the page header wins over the external default.
    expect(start.textContent).toContain("Welcome, learner");
    expect(start.textContent).toContain("This attribute is defined in the page header.");
    expect(start.textContent).not.toContain("External default overridden by document header");
  });

  it("renders the include-only partial in the start page", async () => {
    // Given the start page source and the filesystem-backed resource loader.
    const source = readFileSync(resolve(sampleRoot, contentPath, "010-start.adoc"), "utf8");

    // When rendering through the public API with real includes.
    const result = await renderPage(source, "010-start.adoc");
    const start = document.createElement("div");
    start.innerHTML = result.html;

    // Then the included partial contributes its note.
    expect(start.textContent).toContain("This note comes from an include-only partial.");
  });

  it("uses the target page title for an unlabeled page xref", async () => {
    // Given the start page source and URLs for both navigable pages.
    const source = readFileSync(resolve(sampleRoot, contentPath, "010-start.adoc"), "utf8");

    // When rendering through the public API with page-title lookup.
    const result = await renderPage(source, "010-start.adoc");
    const start = document.createElement("div");
    start.innerHTML = result.html;

    // Then the page xref is labeled with the target document title.
    expect(start.querySelector('a[href="020-resources.adoc"]')?.textContent).toBe("Explore resources");
  });

  it("keeps the fragment xref linked to its rendered target", async () => {
    // Given the start page source containing a local fragment xref.
    const source = readFileSync(resolve(sampleRoot, contentPath, "010-start.adoc"), "utf8");

    // When rendering through the public API.
    const result = await renderPage(source, "010-start.adoc");
    const start = document.createElement("div");
    start.innerHTML = result.html;
    const fragment = start.querySelector('a[href^="#interactive-labs-"]');

    // Then the fragment link and its target both survive rendering.
    expect(fragment).not.toBeNull();
    expect(start.querySelector(`[id="${fragment!.getAttribute("href")!.slice(1)}"]`)).not.toBeNull();
  });

  it("resolves the sample image against the resource endpoint", async () => {
    // Given the start page source and its backend resource URL.
    const source = readFileSync(resolve(sampleRoot, contentPath, "010-start.adoc"), "utf8");

    // When rendering through the public API.
    const result = await renderPage(source, "010-start.adoc");
    const start = document.createElement("div");
    start.innerHTML = result.html;

    // Then the image uses the absolute bundle-local resource URL.
    expect(start.querySelector("img")?.getAttribute("src")).toBe(new URL(resourcePrefix + contentPath + "images/flow.png", window.location.origin).href);
  });

  it("returns the Console topology guidance link", async () => {
    // Given the resources page source with its Console guidance macro.
    const source = readFileSync(resolve(sampleRoot, contentPath, "020-resources.adoc"), "utf8");

    // When rendering through the public API.
    const result = await renderPage(source, "020-resources.adoc");

    // Then guidance points to the supported Console topology location.
    expect(result.guidanceLinks).toEqual([expect.objectContaining({ id: "qs-nav-topology", href: "/topology" })]);
  });

  it("renders all-project and setup-namespace Console resource links", async () => {
    // Given the resources page source and representative setup attributes.
    const source = readFileSync(resolve(sampleRoot, contentPath, "020-resources.adoc"), "utf8");
    const attributes = { ...bundleAttributes, ...setupAttributes };

    // When rendering through the public API.
    const result = await renderPage(source, "020-resources.adoc", attributes);

    // Then resource links retain the all-project scope and the provisioned namespace.
    expect(result.html).toContain('/k8s/all-namespaces/core~v1~Pod');
    expect(result.html).toContain('/k8s/ns/learner-demo/core~v1~ConfigMap/sample-info');
  });

  it("renders conditional setup values when setup attributes are available", async () => {
    // Given the resources page source and representative setup attributes.
    const source = readFileSync(resolve(sampleRoot, contentPath, "020-resources.adoc"), "utf8");
    const attributes = { ...bundleAttributes, ...setupAttributes };

    // When rendering through the public API.
    const result = await renderPage(source, "020-resources.adoc", attributes);

    // Then the setup-only content contains the generated values.
    expect(result.html).toContain("example-password");
    expect(result.html).toContain("sample-learner-demo.apps.example.test");
  });

  it("renders the unavailable note when setup attributes are absent", async () => {
    // Given the resources page source and bundle attributes without setup values.
    const source = readFileSync(resolve(sampleRoot, contentPath, "020-resources.adoc"), "utf8");
    const attributes = { ...bundleAttributes };

    // When rendering through the public API.
    const result = await renderPage(source, "020-resources.adoc", attributes);

    // Then the no-setup conditional content explains the unavailable attributes.
    expect(result.html).toContain("Setup attributes are unavailable.");
  });

  it.each<[string, Record<string, string>]>([
    ["without setup", {}],
    ["with setup", setupAttributes],
  ])("renders both pages without warnings or unresolved attributes %s", async (_name, setup) => {
    // Given both sample page sources and bundle attributes with the selected setup values.
    const sources = pages.map((page) => readFileSync(resolve(sampleRoot, contentPath, page), "utf8"));
    const attributes: Record<string, string> = { ...bundleAttributes, ...setup };

    // When rendering both pages through the public API.
    const results = await Promise.all(pages.map((page, index) => renderPage(sources[index], page, attributes)));

    // Then both pages render cleanly without leaking attribute placeholders.
    for (const result of results) {
      expect(result.warnings).toEqual([]);
      expect(result.html).not.toMatch(/\{(?:user|lab-title|namespaces-demo|demo-password|demo-rootUrl)\}/);
    }
  });
});
