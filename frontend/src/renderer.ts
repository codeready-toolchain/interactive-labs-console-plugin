import { Html5Converter, InlineMacroProcessor, load, IncludeProcessor, Registry, type Block, type Inline, type PreprocessorReader } from "@asciidoctor/core";
import { sanitizeAsciiDocHtml, sanitizeMermaidSvg, sanitizeTitle } from "./sanitizer";
import hljs from "highlight.js";

const MAX_INCLUDE_DEPTH = 64;
const INCLUDE_WARNING = "Included resource is unavailable or unsafe.";
const MERMAID_WARNING = "Mermaid diagram could not be rendered.";
const HIGHLIGHT_WARNING = "Source code could not be highlighted.";
const resourcePattern = /^\/api\/proxy\/plugin\/interactive-labs-console-plugin\/backend\/api\/v1alpha1\/labs\/[^/]+\/resources\//;
const GUIDANCE_WARNING = "Guidance link is unavailable or unsafe.";
const consoleRoutes: Record<string, string | undefined> = {
  "qs-nav-add": "/add",
  "qs-nav-topology": "/topology",
  "qs-admin-nav-topology": "/topology",
  "qs-nav-search": "/search-page",
  "qs-nav-project": "/project-details",
  "qs-nav-functions": "/functions",
  "qs-nav-helm": "/helm",
  "qs-nav-builds": "/builds",
  "qs-nav-monitoring": "/dev-monitoring",
  "qs-nav-home": undefined,
  "qs-nav-ecosystem": undefined,
  "qs-nav-workloads": undefined,
  "qs-nav-networking": undefined,
  "qs-nav-storage": undefined,
  "qs-nav-compute": undefined,
  "qs-nav-usermanagement": undefined,
  "qs-nav-administration": undefined,
  "qs-nav-serverless": undefined,
  "qs-perspective-switcher": undefined,
  "qs-masthead-cloudshell": undefined,
  "qs-masthead-import": undefined,
  "qs-masthead-utilitymenu": undefined,
  "qs-masthead-usermenu": undefined,
  "qs-masthead-notifications": undefined,
  "qs-masthead-help": undefined,
};
const randomToken = (): string => Array.from(crypto.getRandomValues(new Uint32Array(4))).join("-");

type ResourceLoader = (url: string, signal: AbortSignal) => Promise<string>;
type MacroAttributes = Record<string, unknown>;

export type RenderInput = {
  source: string;
  sourceURL: string;
  attributes: Record<string, string>;
  loadResource: ResourceLoader;
  pageURLs?: string[];
  signal?: AbortSignal;
};

export type GuidanceLink = { id: string; href: string; token: string };
export type RenderResult = { html: string; warnings: string[]; title?: string; guidanceLinks?: GuidanceLink[] };

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

function registry(
  sourceURL: string,
  loadResource: ResourceLoader,
  controller: AbortController,
  warnings: string[],
  guidanceLinks: Map<string, GuidanceLink>,
) {
  class ApprovedIncludeProcessor extends IncludeProcessor {
    handles(): boolean {
      return true;
    }

    async process(_document: unknown, reader: PreprocessorReader, target: string, attributes: Record<string, string>): Promise<void> {
      const baseURL = reader.getIncludeDepth() === 0 ? sourceURL : reader.getCursor().getPath();
      const resolved = resourceURL(target, baseURL);
      if (!resolved || reader.getIncludeDepth() >= MAX_INCLUDE_DEPTH) {
        warnings.push(INCLUDE_WARNING);
        reader.pushInclude(warningSource(INCLUDE_WARNING), resolved ?? target, resolved ?? target, 1, attributes);
        return;
      }
      try {
        if (controller.signal.aborted) throw abort();
        const content = await loadResource(resolved, controller.signal);
        if (controller.signal.aborted) throw abort();
        reader.pushInclude(content, resolved, resolved, 1, attributes);
      } catch (error: unknown) {
        if (controller.signal.aborted || (error as DOMException).name === "AbortError") throw abort();
        warnings.push(INCLUDE_WARNING);
        reader.pushInclude(warningSource(INCLUDE_WARNING), resolved, resolved, 1, attributes);
      }
    }
  }

  class ConsoleInlineMacro extends InlineMacroProcessor {
    static config = { name: "console", positionalAttrs: ["label"] };

    process(parent: Block, target: string, macroAttributes: MacroAttributes): Inline {
      const label = macroLabel(macroAttributes);
      if (!label || !Object.prototype.hasOwnProperty.call(consoleRoutes, target) || !onlyMacroAttributes(macroAttributes, [])) {
        return inlineWarning(this, parent, warnings);
      }
      const guidance = { id: target, href: consoleRoutes[target] ?? "#", token: randomToken() };
      const className = `interactive-labs-guidance-${guidance.token}`;
      guidanceLinks.set(className, guidance);
      return this.createInline(parent, "anchor", label, {
        type: "link",
        target: guidance.href,
        attributes: { role: className },
      });
    }
  }

  class ResourceInlineMacro extends InlineMacroProcessor {
    static config = { name: "resource", positionalAttrs: ["label"] };

    process(parent: Block, target: string, macroAttributes: MacroAttributes): Inline {
      const label = macroLabel(macroAttributes);
      const href = label && resourceHref(target, macroAttributes);
      return href
        ? this.createInline(parent, "anchor", label, { type: "link", target: href })
        : inlineWarning(this, parent, warnings);
    }
  }

  const extensions = new Registry();
  extensions.includeProcessor(ApprovedIncludeProcessor);
  extensions.inlineMacro(ConsoleInlineMacro);
  extensions.inlineMacro(ResourceInlineMacro);
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


function warningElement(message: string): HTMLElement {
  const element = document.createElement("p");
  element.className = "interactive-labs-render-warning";
  element.textContent = message;
  return element;
}

function macroLabel(attributes: MacroAttributes): string | undefined {
  return typeof attributes.label === "string" && attributes.label ? attributes.label : undefined;
}

function onlyMacroAttributes(attributes: MacroAttributes, allowed: string[]): boolean {
  return Object.keys(attributes).every((name) => name === "1" || name === "label" || allowed.includes(name));
}

function inlineWarning(processor: InlineMacroProcessor, parent: Block, warnings: string[]): Inline {
  warnings.push(GUIDANCE_WARNING);
  return processor.createInline(parent, "quoted", GUIDANCE_WARNING, {
    type: "unquoted",
    attributes: { role: "interactive-labs-render-warning" },
  });
}

function resourceHref(target: string, macroAttributes: MacroAttributes): string | undefined {
  const parts = target.split("/");
  const [group, version, kind] = parts.length === 2 ? ["core", parts[0], parts[1]] : parts;
  if (parts.length < 2 || parts.length > 3 || !onlyMacroAttributes(macroAttributes, ["scope", "name", "namespace"])) return;

  const scope = macroAttributes.scope ?? "namespaced";
  const nameValue = macroAttributes.name;
  const namespaceValue = macroAttributes.namespace;
  if (typeof scope !== "string" || (nameValue !== undefined && typeof nameValue !== "string") ||
    (namespaceValue !== undefined && typeof namespaceValue !== "string") || (scope !== "namespaced" && scope !== "cluster")) return;
  const name = nameValue;
  const namespace = namespaceValue;
  if ((nameValue !== undefined && !name) || (namespaceValue !== undefined && !namespace)) return;
  if (scope === "cluster" && namespace !== undefined) return;

  const reference = [group, version, kind].map((component) => encodeURIComponent(component).replace(/~/g, "%7E")).join("~");
  if (scope === "cluster") return name ? `/k8s/cluster/${reference}/${encodeURIComponent(name)}` : `/k8s/cluster/${reference}`;
  if (namespace && name) return `/k8s/ns/${encodeURIComponent(namespace)}/${reference}/${encodeURIComponent(name)}`;
  if (namespace) return `/k8s/ns/${encodeURIComponent(namespace)}/${reference}`;
  return name ? `/k8s/all-namespaces/${reference}?name=${encodeURIComponent(name)}` : `/k8s/all-namespaces/${reference}`;
}

function replaceGuidanceLinks(root: DocumentFragment, guidanceLinks: Map<string, GuidanceLink>): GuidanceLink[] {
  const links: GuidanceLink[] = [];
  root.querySelectorAll("a").forEach((anchor) => {
    const className = Array.from(anchor.classList).find((name) => guidanceLinks.has(name));
    if (!className) return;
    const guidance = guidanceLinks.get(className)!;
    anchor.classList.remove(className);
    if (anchor.classList.length === 0) anchor.removeAttribute("class");
    anchor.dataset.interactiveLabsGuidanceId = guidance.id;
    anchor.dataset.interactiveLabsGuidanceToken = guidance.token;
    links.push(guidance);
  });
  return links;
}

function rewriteImages(root: DocumentFragment, sourceURL: string): void {
  root.querySelectorAll("img[src]").forEach((image) => {
    const target = resourceURL(image.getAttribute("src") ?? "", sourceURL);
    if (target) image.setAttribute("src", target);
    else image.remove();
  });
}

function namespaceIds(root: DocumentFragment, prefix: string): void {
  const ids = new Map<string, string>();
  root.querySelectorAll("[id]").forEach((element) => {
    if (!(element instanceof HTMLElement)) return;
    const id = element.id;
    const namespaced = `${prefix}-${id}`;
    if (id) ids.set(id, namespaced);
    element.id = namespaced;
  });
  root.querySelectorAll("a[href]").forEach((link) => {
    const href = link.getAttribute("href");
    const namespaced = href?.startsWith("#") && ids.get(href.slice(1));
    if (namespaced) link.setAttribute("href", `#${namespaced}`);
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

async function renderMermaid(root: DocumentFragment, warnings: string[], prefix: string): Promise<void> {
  const blocks = Array.from(root.querySelectorAll("pre > code")).filter((code) =>
    Array.from(code.classList).some((name) => name === "language-mermaid" || name === "mermaid"),
  );
  if (blocks.length === 0) return;

  const { default: mermaid } = await import("mermaid");
  mermaid.initialize({ startOnLoad: false, securityLevel: "strict", htmlLabels: false });
  for (let index = 0; index < blocks.length; index += 1) {
    const code = blocks[index];
    try {
      const { svg } = await mermaid.render(`${prefix}-mermaid-${index}`, code.textContent ?? "");
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
    const warnings: string[] = [];
    const prefix = `interactive-labs-${randomToken()}`;
    const guidanceLinks = new Map<string, GuidanceLink>();

    const pageTitles = new Map<string, Promise<string | undefined>>();
    const targetTitle = (pageURL: string): Promise<string | undefined> => {
      let title = pageTitles.get(pageURL);
      if (!title) {
        title = (async () => {
          try {
            if (controller.signal.aborted) throw abort();
            const target = await load(await input.loadResource(pageURL, controller.signal), options);
            if (controller.signal.aborted) throw abort();
            const documentTitle = target.getDocumentTitle();
            return typeof documentTitle === "string" ? documentTitle : undefined;
          } catch (error: unknown) {
            if (controller.signal.aborted || (error as DOMException).name === "AbortError") throw abort();
            return undefined;
          }
        })();
        pageTitles.set(pageURL, title);
      }
      return title;
    };
    class XrefTitleConverter extends Html5Converter {
      async convert_inline_anchor(node: Inline): Promise<string> {
        if (node.type !== "xref" || node.text !== null || !node.attributes.path) {
          return super.convert_inline_anchor(node);
        }
        const pageURL = approvedPageURL(node.attributes.path, input.sourceURL, input.pageURLs ?? []);
        if (!pageURL) return super.convert_inline_anchor(node);

        const title = await targetTitle(pageURL);
        if (!title) return super.convert_inline_anchor(node);
        node.text = sanitizeTitle(title);
        try {
          return await super.convert_inline_anchor(node);
        } finally {
          node.text = null;
        }
      }
    }
    const options = {
      attributes: {
        ...Object.fromEntries(Object.entries(input.attributes).map(([name, value]) => [`${name}@`, value])),
        relfilesuffix: ".adoc",
      },
      converter: XrefTitleConverter,
      extension_registry: registry(input.sourceURL, input.loadResource, controller, warnings, guidanceLinks),
      safe: "secure" as const,
      standalone: false,
      to_file: false,
    };
    const asciidocDocument = await load(input.source, options);
    const converted = await asciidocDocument.convert({ standalone: false });
    if (typeof converted !== "string") throw new Error("AsciiDoc conversion did not return markup.");

    const template = document.createElement("template");
    template.innerHTML = converted;
    rewriteImages(template.content, input.sourceURL);
    highlight(template.content, warnings);
    template.innerHTML = sanitizeAsciiDocHtml(template.innerHTML);
    const renderedGuidanceLinks = replaceGuidanceLinks(template.content, guidanceLinks);
    namespaceIds(template.content, prefix);
    await renderMermaid(template.content, warnings, prefix);
    if (controller.signal.aborted) throw abort();
    const title = asciidocDocument.getDocumentTitle();
    return { html: template.innerHTML, warnings, title: typeof title === "string" ? title : undefined, guidanceLinks: renderedGuidanceLinks };
  } finally {
    input.signal?.removeEventListener("abort", forwardAbort);
  }
}
