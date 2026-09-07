export type PreprocessorReader = {
  depth: number;
  pushInclude: (content: string) => void;
  getIncludeDepth: () => number;
  getCursor: () => { getPath: () => string };
};

export class IncludeProcessor {}

export class Registry {
  processor?: new () => IncludeProcessor & { process: (_document: unknown, reader: PreprocessorReader, target: string, attributes: Record<string, string>) => void };

  includeProcessor(processor: Registry["processor"]): void {
    this.processor = processor;
  }
}

const escape = (value: string) => value.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");

export async function load(source: string, options: { attributes: Record<string, string>; extension_registry: Registry }): Promise<{
  convert: (convertOptions?: { standalone?: boolean }) => Promise<string>;
  getDocumentTitle: (options?: { sanitize?: boolean }) => string | null;
}> {
  return {
    convert: (convertOptions) => convert(source, { ...options, ...convertOptions }),
    getDocumentTitle: () => {
      const title = source.match(/^= (.*)$/m)?.[1];
      return title?.replace(/\{([^}]+)\}/g, (_match, name) => options.attributes[`${name}@`] ?? options.attributes[name] ?? _match) ?? null;
    },
  };
}

export async function convert(source: string, options: { attributes: Record<string, string>; extension_registry: Registry }): Promise<string> {
  const processor = options.extension_registry.processor && new options.extension_registry.processor();
  const expand = (input: string, depth = 0): string => input.replace(/^include::([^\x5B]+)\[[^\]]*\]\s*$/gm, (_line, target) => {
    let included = "";
    processor?.process({}, {
      depth,
      getIncludeDepth: () => depth,
      getCursor: () => ({ getPath: () => "http://localhost/index.adoc" }),
      pushInclude: (content) => { included = content; },
    }, target, {});
    return expand(included, depth + 1);
  });
  const output = expand(source)
    .replace(/\{([^}]+)\}/g, (_match, name) => options.attributes[`${name}@`] ?? options.attributes[name] ?? _match)
    .replace(/^= (.*)$/m, "<h1>$1</h1>")
    .replace(/\[source,mermaid\]\n----\n([\s\S]*?)\n----/g, (_match, code) => `<pre><code class="language-mermaid">${escape(code)}</code></pre>`)
    .replace(/image::([^\x5B]+)\[[^\]]*\]/g, '<img src="$1">')
    .replace(/xref:(next\.adoc)\[([^\]]*)\]/g, (_match, target, label) => `<a href="${target}">${label || target}</a>`)
    .replace(/^(WARNING: .*)$/gm, "<p>$1</p>");
  return output.includes("pass:[<script>") ? output.replace(/pass:\[([\s\S]*?)\]/g, "$1") : output;
}
