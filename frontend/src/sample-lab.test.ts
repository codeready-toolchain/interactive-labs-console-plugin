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

describe("sample lab with real AsciiDoc", () => {
  it.each([
    ["without setup", {}],
    ["with setup", { "namespaces-demo": "learner-demo", "demo-password": "example-password", "demo-rootUrl": "sample-learner-demo.apps.example.test" }],
  ])("renders pages %s", async (_name, setupAttributes) => {
    // Given the sample pages and representative bundle/setup attributes.
    const attributes = {
      "lab-title": "Interactive Labs sample",
      "local-note": "External default overridden by document header",
      user: "learner",
      openshift_console_url: "https://console.example.test",
      ...setupAttributes,
    } as Record<string, string>;

    // When rendering through the public API with real includes and page-title lookup.
    const results = await Promise.all(pages.map((page, index) => renderAsciiDoc({
      source: readFileSync(resolve(sampleRoot, contentPath, page), "utf8"),
      sourceURL: pageURLs[index], attributes, pageURLs, loadResource,
    })));
    const start = document.createElement("div");
    start.innerHTML = results[0].html;

    // Then links, partials, images, and conditional setup values render without warnings.
    for (const result of results) {
      expect(result.warnings).toEqual([]);
      expect(result.html).not.toMatch(/\{(?:user|lab-title|namespaces-demo|demo-password|demo-rootUrl)\}/);
    }
    expect(start.textContent).toContain("Welcome, learner");
    expect(start.textContent).toContain("This attribute is defined in the page header.");
    expect(start.textContent).not.toContain("External default overridden by document header");
    expect(start.textContent).toContain("This note comes from an include-only partial.");
    expect(start.querySelector('a[href="020-resources.adoc"]')?.textContent).toBe("Explore resources");
    const fragment = start.querySelector('a[href^="#interactive-labs-"]');
    expect(fragment).not.toBeNull();
    expect(start.querySelector(`[id="${fragment!.getAttribute("href")!.slice(1)}"]`)).not.toBeNull();
    expect(start.querySelector("img")?.getAttribute("src")).toBe(new URL(resourcePrefix + contentPath + "images/flow.png", window.location.origin).href);
    expect(results[1].guidanceLinks).toEqual([expect.objectContaining({ id: "qs-nav-topology", href: "/topology" })]);
    expect(results[1].html).toContain('/k8s/all-namespaces/core~v1~Pod');
    if (_name === "with setup") {
      expect(results[1].html).toContain('/k8s/ns/learner-demo/core~v1~ConfigMap/sample-info');
      expect(results[1].html).toContain("example-password");
      expect(results[1].html).toContain("sample-learner-demo.apps.example.test");
    } else {
      expect(results[1].html).toContain("Setup is inactive in this repository layout.");
    }
  });
});
