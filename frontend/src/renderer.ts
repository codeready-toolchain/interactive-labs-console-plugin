import { load, IncludeProcessor, Registry, type PreprocessorReader } from "@asciidoctor/core";
import { sanitizeAsciiDocHtml, sanitizeMermaidSvg } from "./sanitizer";
import hljs from "highlight.js";

const MAX_INCLUDE_DEPTH = 64;
const INCLUDE_WARNING = "Included resource is unavailable or unsafe.";
const MERMAID_WARNING = "Mermaid diagram could not be rendered.";
const HIGHLIGHT_WARNING = "Source code could not be highlighted.";
const resourcePattern = /^\/api\/proxy\/plugin\/interactive-labs-console-plugin\/backend\/api\/v1alpha1\/labs\/[^/]+\/resources\//;

type ResourceLoader = (url: string, signal: AbortSignal) => Promise<string>;

export type RenderInput = {
  source: string;
  sourceURL: string;
  attributes: Record<string, string>;
  loadResource: ResourceLoader;
  pageURLs?: string[];
  signal?: AbortSignal;
};

export type RenderResult = { html: string; warnings: string[]; title?: string };

type Prefetch = { source: string; resources: Map<string, string> };

const abort = (): DOMException => new DOMException("Aborted", "AbortError");

const warningSource = (message: string): string => `WARNING: ${message}`;

function resourceURL(target: string, sourceURL: string): string | undefined {
  try {
    const base = new URL(sourceURL, window.location.origin);
    const resolved = new URL(target, base);
    return resolved.origin === base.origin &&
      !resolved.search &&
      !resolved.hash &&
      resourcePattern.test(resolved.pathname)
      ? resolved.toString()
      : undefined;
  } catch {
    return undefined;
  }
}

async function prefetchIncludes(
  source: string,
  sourceURL: string,
  loadResource: ResourceLoader,
  controller: AbortController,
  resources = new Map<string, string>(),
  depth = 0,
): Promise<Prefetch> {
  const include = /^include::([^\x5B]+)\[[^\]]*\]\s*$/gm;
  let rewritten = "";
  let cursor = 0;
  for (let match = include.exec(source); match; match = include.exec(source)) {
    const target = resourceURL(match[1], sourceURL);
    let replacement = match[0];
    if (target && depth < MAX_INCLUDE_DEPTH) {
      let content = resources.get(target);
      if (content === undefined) {
        if (controller.signal.aborted) throw abort();
        try {
          content = await loadResource(target, controller.signal);
          resources.set(target, content);
          const nested = await prefetchIncludes(content, target, loadResource, controller, resources, depth + 1);
          content = nested.source;
          resources.set(target, content);
        } catch (error: unknown) {
          if ((error as DOMException).name === "AbortError") throw error;
        }
      }
      if (resources.has(target)) replacement = `include::${target}[${match[0].slice(match[0].indexOf("[") + 1)}`;
    }
    rewritten += source.slice(cursor, match.index) + replacement;
    cursor = match.index + match[0].length;
  }
  return { source: rewritten + source.slice(cursor), resources };
}

function registry(resources: Map<string, string>, warnings: string[]) {
  class ApprovedIncludeProcessor extends IncludeProcessor {
    handles(): boolean {
      return true;
    }

    process(_document: unknown, reader: PreprocessorReader, target: string, attributes: Record<string, string>): void {
      const resolved = resourceURL(target, reader.getCursor().getPath());
      const content = resolved && resources.get(resolved);
      if (content === undefined || reader.getIncludeDepth() >= MAX_INCLUDE_DEPTH) {
        warnings.push(INCLUDE_WARNING);
        reader.pushInclude(warningSource(INCLUDE_WARNING), resolved ?? target, resolved ?? target, 1, attributes);
        return;
      }
      reader.pushInclude(content, resolved, resolved, 1, attributes);
    }
  }

  const extensions = new Registry();
  extensions.includeProcessor(ApprovedIncludeProcessor);
  return extensions;
}

function approvedPageURL(target: string, sourceURL: string, pageURLs: string[]): string | undefined {
  try {
    const resolved = new URL(target, new URL(sourceURL, window.location.origin));
    const current = new URL(sourceURL, window.location.origin);
    resolved.hash = "";
    current.hash = "";
    if (resolved.search || resolved.href === current.href) return undefined;
    return pageURLs.find((pageURL) => {
      const approved = new URL(pageURL, window.location.origin);
      approved.hash = "";
      return approved.href === resolved.href;
    });
  } catch {
    return undefined;
  }
}

type ResolvedXrefs = { source: string; titles: Map<string, string> };

async function resolveXrefTitles(
  source: string,
  input: RenderInput,
  controller: AbortController,
  options: Parameters<typeof load>[1],
): Promise<ResolvedXrefs> {
  const xref = /xref:([^\s[]+)\[\]/g;
  const titles = new Map<string, string>();
  let rewritten = "";
  let cursor = 0;
  let index = 0;
  for (let match = xref.exec(source); match; match = xref.exec(source)) {
    let replacement = match[0];
    const pageURL = approvedPageURL(match[1], input.sourceURL, input.pageURLs ?? []);
    if (pageURL) {
      try {
        if (controller.signal.aborted) throw abort();
        const target = await load(await input.loadResource(pageURL, controller.signal), options);
        if (controller.signal.aborted) throw abort();
        const title = target.getDocumentTitle();
        if (typeof title === "string") {
          let marker = `interactive-labs-xref-title-${index}`;
          while (source.includes(marker)) marker = `interactive-labs-xref-title-${++index}`;
          replacement = `xref:${match[1]}[${marker}]`;
          titles.set(marker, title);
        }
      } catch (error: unknown) {
        if (controller.signal.aborted || (error as DOMException).name === "AbortError") throw abort();
      }
    }
    rewritten += source.slice(cursor, match.index) + replacement;
    cursor = match.index + match[0].length;
    index += 1;
  }
  return { source: rewritten + source.slice(cursor), titles };
}

function replaceXrefTitles(root: DocumentFragment, titles: Map<string, string>): void {
  root.querySelectorAll("a").forEach((link) => {
    const marker = link.textContent ?? "";
    if (titles.has(marker) && !link.classList.contains("bare")) link.textContent = titles.get(marker)!;
  });
}

function warningElement(message: string): HTMLElement {
  const element = document.createElement("p");
  element.className = "interactive-labs-render-warning";
  element.textContent = message;
  return element;
}

function rewriteImages(root: DocumentFragment, sourceURL: string): void {
  root.querySelectorAll("img[src]").forEach((image) => {
    const target = resourceURL(image.getAttribute("src") ?? "", sourceURL);
    if (target) image.setAttribute("src", target);
    else image.remove();
  });
}

function highlight(root: DocumentFragment, warnings: string[]): void {
  root.querySelectorAll("pre > code").forEach((code) => {
    if (Array.from(code.classList).some((name) => name === "language-mermaid" || name === "mermaid")) return;
    if (Array.from(code.childNodes).some((node) => node.nodeType !== Node.TEXT_NODE)) {
      warnings.push(HIGHLIGHT_WARNING);
      code.closest("pre")?.replaceWith(warningElement(HIGHLIGHT_WARNING));
      return;
    }
    try {
      hljs.highlightElement(code as HTMLElement);
    } catch {
      warnings.push(HIGHLIGHT_WARNING);
      code.closest("pre")?.replaceWith(warningElement(HIGHLIGHT_WARNING));
    }
  });
}

async function renderMermaid(root: DocumentFragment, warnings: string[]): Promise<void> {
  const blocks = Array.from(root.querySelectorAll("pre > code")).filter((code) =>
    Array.from(code.classList).some((name) => name === "language-mermaid" || name === "mermaid"),
  );
  if (blocks.length === 0) return;

  const { default: mermaid } = await import("mermaid");
  mermaid.initialize({ startOnLoad: false, securityLevel: "strict", htmlLabels: false });
  for (let index = 0; index < blocks.length; index += 1) {
    const code = blocks[index];
    try {
      const { svg } = await mermaid.render(`interactive-labs-mermaid-${index}`, code.textContent ?? "");
      const cleanSvg = sanitizeMermaidSvg(svg);
      const wrapper = document.createElement("div");
      wrapper.innerHTML = cleanSvg;
      code.closest("pre")?.replaceWith(wrapper);
    } catch {
      warnings.push(MERMAID_WARNING);
      code.closest("pre")?.replaceWith(warningElement(MERMAID_WARNING));
    }
  }
}

export async function renderAsciiDoc(input: RenderInput): Promise<RenderResult> {
  if (input.signal?.aborted) throw abort();
  const controller = new AbortController();
  const forwardAbort = () => controller.abort();
  input.signal?.addEventListener("abort", forwardAbort, { once: true });

  try {
    const prefetched = await prefetchIncludes(input.source, input.sourceURL, input.loadResource, controller);
    if (controller.signal.aborted) throw abort();
    const warnings: string[] = [];
    const options = {
      attributes: {
        ...Object.fromEntries(Object.entries(input.attributes).map(([name, value]) => [`${name}@`, value])),
        relfilesuffix: ".adoc",
      },
      extension_registry: registry(prefetched.resources, warnings),
      safe: "secure" as const,
      standalone: false,
      to_file: false,
    };
    const resolvedXrefs = await resolveXrefTitles(prefetched.source, input, controller, options);
    const asciidocDocument = await load(resolvedXrefs.source, options);
    const converted = await asciidocDocument.convert({ standalone: false });
    if (typeof converted !== "string") throw new Error("AsciiDoc conversion did not return markup.");

    const template = document.createElement("template");
    template.innerHTML = converted;
    rewriteImages(template.content, input.sourceURL);
    highlight(template.content, warnings);
    template.innerHTML = sanitizeAsciiDocHtml(template.innerHTML);
    replaceXrefTitles(template.content, resolvedXrefs.titles);
    await renderMermaid(template.content, warnings);
    if (controller.signal.aborted) throw abort();
    const title = asciidocDocument.getDocumentTitle();
    return { html: template.innerHTML, warnings, title: typeof title === "string" ? title : undefined };
  } finally {
    input.signal?.removeEventListener("abort", forwardAbort);
  }
}
