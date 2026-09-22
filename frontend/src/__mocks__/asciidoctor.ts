export type PreprocessorReader = {
  pushInclude: (content: string, file?: string, path?: string) => void;
  getIncludeDepth: () => number;
  getCursor: () => { getPath: () => string };
};

export type Block = object;
export type Inline = { context: string; text: string | null; type?: string; target?: string; attributes?: Record<string, string> };

type Converter = new (backend: string, options: object) => Html5Converter;
type RenderOptions = { attributes: Record<string, string>; extension_registry: Registry; converter?: Converter };

export class IncludeProcessor {}

export class Html5Converter {
  constructor(...args: [string, object]) { void args; }

  async convert_inline_anchor(node: Inline): Promise<string> {
    return `<a href="${node.target}">${node.text ?? node.attributes?.path ?? ""}</a>`;
  }
}

export class InlineMacroProcessor {
  static config: { name?: string; positionalAttrs?: string[] } = {};
  config: { name?: string; positionalAttrs?: string[] };

  constructor() {
    this.config = (this.constructor as typeof InlineMacroProcessor).config;
  }

  createInline(_parent: Block, context: string, text: string, options: Omit<Inline, "context" | "text"> = {}): Inline {
    return { context, text, ...options };
  }

  process(parent: Block, target: string, attributes: Record<string, unknown>): Inline {
    void parent;
    void target;
    void attributes;
    throw new Error("Inline macro processor must implement process.");
  }
}

type IncludeProcessorConstructor = new () => IncludeProcessor & {
  process: (_document: unknown, reader: PreprocessorReader, target: string, attributes: Record<string, string>) => void | Promise<void>;
};

export class Registry {
  processor?: IncludeProcessorConstructor;
  inlineProcessors = new Map<string, InlineMacroProcessor>();

  includeProcessor(processor: IncludeProcessorConstructor): void {
    this.processor = processor;
  }

  inlineMacro(processor: new () => InlineMacroProcessor): void {
    const instance = new processor();
    if (instance.config.name) this.inlineProcessors.set(instance.config.name, instance);
  }
}

const escape = (value: string) => value.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
const substituteAttributes = (value: string, attributes: Record<string, string>) =>
  value.replace(/\{([^}]+)\}/g, (_match, name) => attributes[`${name}@`] ?? attributes[name] ?? _match);

function renderInlineMacro(
  source: string,
  processors: Map<string, InlineMacroProcessor>,
): string {
  return source.replace(/([a-z]+):([^\s[]+)\[([^\]]*)\]/g, (match, name, target, content) => {
    const processor = processors.get(name);
    if (!processor) return match;
    const attributes: Record<string, unknown> = {};
    for (const [index, field] of content.split(",").entries()) {
      const named = /^([^=]+)=(.*)$/.exec(field);
      if (named) attributes[named[1]] = named[2];
      else {
        attributes[index + 1] = field;
        const positional = processor.config.positionalAttrs?.[index];
        if (positional) attributes[positional] = field;
      }
    }
    const inline = processor.process({} as Block, target, attributes);
    const role = inline.attributes?.role;
    if (inline.context === "anchor") return `<a href="${inline.target}"${role ? ` class="${role}"` : ""}>${inline.text}</a>`;
    return role ? `<span class="${role}">${inline.text ?? ""}</span>` : inline.text ?? "";
  });
}

export async function load(source: string, options: RenderOptions): Promise<{
  convert: (convertOptions?: { standalone?: boolean }) => Promise<string>;
  getDocumentTitle: (options?: { sanitize?: boolean }) => string | null;
}> {
  return {
    convert: (convertOptions) => convert(source, { ...options, ...convertOptions }),
    getDocumentTitle: () => {
      const title = source.match(/^= (.*)$/m)?.[1];
      return title ? substituteAttributes(title, options.attributes) : null;
    },
  };
}

export async function convert(source: string, options: RenderOptions): Promise<string> {
  const processor = options.extension_registry.processor && new options.extension_registry.processor();
  const expand = async (input: string, depth = 0, path = "http://localhost/index.adoc"): Promise<string> => {
    const include = /^include::([^\x5B]+)\[[^\]]*\]\s*$/gm;
    let output = "";
    let cursor = 0;
    for (let match = include.exec(input); match; match = include.exec(input)) {
      let included = "";
      let includedPath = path;
      const target = substituteAttributes(match[1], options.attributes);
      await processor?.process({}, {
        getIncludeDepth: () => depth,
        getCursor: () => ({ getPath: () => path }),
        pushInclude: (content, file, includePath) => {
          included = content;
          includedPath = includePath ?? file ?? path;
        },
      }, target, {});
      output += input.slice(cursor, match.index) + await expand(included, depth + 1, includedPath);
      cursor = match.index + match[0].length;
    }
    return output + input.slice(cursor);
  };
  const converter = options.converter ? new options.converter("html5", {}) : new Html5Converter("html5", {});
  const expanded = renderInlineMacro(
    substituteAttributes(await expand(source), options.attributes),
    options.extension_registry.inlineProcessors,
  );
  let output = "";
  let cursor = 0;
  const xref = /xref:([^\s[]+)\[([^\]]*)\]/g;
  for (let match = xref.exec(expanded); match; match = xref.exec(expanded)) {
    const [path, fragment] = match[1].split("#", 2);
    output += expanded.slice(cursor, match.index) + await converter.convert_inline_anchor({
      context: "anchor",
      text: match[2] || null,
      type: "xref",
      target: match[1],
      attributes: { path, ...(fragment ? { fragment } : {}) },
    });
    cursor = match.index + match[0].length;
  }
  output = (output + expanded.slice(cursor))
    .replace(/^= (.*)$/m, "<h1>$1</h1>")
    .replace(/\[source,mermaid\]\n----\n([\s\S]*?)\n----/g, (_match, code) => `<pre><code class="language-mermaid">${escape(code)}</code></pre>`)
    .replace(/image::([^\x5B]+)\[[^\]]*\]/g, '<img src="$1">')
    .replace(/`([^`]*)`/g, "<code>$1</code>")
    .replace(/\*([^*]+)\*/g, "<strong>$1</strong>")
    .replace(/^(WARNING: .*)$/gm, "<p>$1</p>");
  return output.includes("pass:[<script>") ? output.replace(/pass:\[([\s\S]*?)\]/g, "$1") : output;
}
