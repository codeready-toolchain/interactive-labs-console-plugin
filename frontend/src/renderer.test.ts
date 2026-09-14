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

  it("returns the AsciiDoc document title for drawer navigation", async () => {
    const result = await renderAsciiDoc({
      source: "= {product} introduction\n\nWelcome",
      sourceURL,
      attributes: { product: "Interactive Labs" },
      loadResource: loader,
    });

    expect(result).toMatchObject({ title: "Interactive Labs introduction" });
  });

  it("resolves approved xref titles without replacing explicit labels", async () => {
    const targetPageURL = sourceURL.replace("intro.adoc", "next.adoc");
    const input = {
      source: "xref:next.adoc[]\n\nxref:next.adoc[Custom label]",
      sourceURL,
      attributes: {},
      pageURLs: [targetPageURL],
      loadResource: jest.fn(async (url: string) => {
        if (url === targetPageURL) return "= Referenced Page Title";
        throw new Error("resource not found");
      }),
    };

    const result = await renderAsciiDoc(input);

    expect(result.html).toContain('<a href="next.adoc">Referenced Page Title</a>');
    expect(result.html).toContain('<a href="next.adoc">Custom label</a>');
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

  it("prefetches nested local includes, blocks unsafe includes, and rewrites local images", async () => {
    const result = await renderAsciiDoc({
      source: "include::../partials/outer.adoc[]\n\ninclude::https://evil.example/x.adoc[]\n\nimage::data:image/png;base64,AA==[]",
      sourceURL,
      attributes: {},
      loadResource: loader,
    });

    expect(loader).toHaveBeenCalledWith(expect.stringMatching(/partials\/outer\.adoc$/), expect.any(AbortSignal));
    expect(loader).toHaveBeenCalledWith(expect.stringMatching(/partials\/inner\.adoc$/), expect.any(AbortSignal));
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
    const pending = jest.fn(
      (_url: string, signal: AbortSignal) =>
        new Promise<string>((_resolve, rejectPromise) => {
          reject = rejectPromise;
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

    controller.abort();
    await expect(rendering).rejects.toMatchObject({ name: "AbortError" });
    reject(new DOMException("Aborted", "AbortError"));
  });
});
