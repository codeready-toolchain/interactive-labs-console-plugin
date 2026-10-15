/** @jest-environment node */
import { execFileSync } from "node:child_process";
import { existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import hljs from "highlight.js";

const browser = process.env.CHROME_BIN ?? ["/usr/bin/chromium", "/usr/bin/chromium-browser", "/usr/bin/google-chrome"].find(existsSync);
const browserTest = browser ? it : it.skip;
if (!browser) console.warn("Syntax theme cascade tests skipped: set CHROME_BIN to a Chromium executable.");

const tokenGroups = {
  comment: ["comment", "quote"],
  keyword: ["keyword", "selector-tag", "doctag", "meta"],
  string: ["string", "regexp", "addition"],
  literal: ["number", "literal", "built_in", "type"],
  title: ["title", "section", "attribute", "attr", "name", "selector-id", "selector-class"],
  variable: ["symbol", "bullet", "variable", "template-variable", "link", "deletion"],
};
const light = { comment: "#5d5d5d", keyword: "#7b268c", string: "#226b35", literal: "#8a4300", title: "#005a9c", variable: "#a11b35" };
const dark = { comment: "#b8bbbe", keyword: "#d7a6ed", string: "#a3d9a5", literal: "#ffc78a", title: "#8bc1f7", variable: "#ffacb6" };
const tuned = { comment: "#444444", keyword: "#550066", string: "#005522", literal: "#663300", title: "#003366", variable: "#770022" };
const rgb = (hex: string) => `rgb(${hex.slice(1).match(/../g)!.map((value) => parseInt(value, 16)).join(", ")})`;
type Style = { color: string; backgroundColor: string; backgroundImage: string; fontStyle: string; fontWeight: string; textDecorationLine: string };
type Snapshot = { pre: Style; code: Style; tokens: Record<string, Style>; keyword: Style; string: Style; nested: Style; strong: Style; emphasis: Style };

function contrast(foreground: string, background: string): number {
  const luminance = (hex: string) => hex.slice(1).match(/../g)!.map((value) => {
    const channel = parseInt(value, 16) / 255;
    return channel <= 0.04045 ? channel / 12.92 : ((channel + 0.055) / 1.055) ** 2.4;
  }).reduce((sum, channel, index) => sum + channel * [0.2126, 0.7152, 0.0722][index], 0);
  const values = [luminance(foreground), luminance(background)].sort((a, b) => b - a);
  return (values[0] + 0.05) / (values[1] + 0.05);
}

describe("scoped syntax theme (Chromium cascade)", () => {
  browserTest.each([
    ["light", "", light, "#222222", "#ededed"],
    ["PF5 dark", "pf-v5-theme-dark", dark, "#f5f5f5", "#383838"],
    ["PF6 dark", "pf-v6-theme-dark", dark, "#f5f5f5", "#383838"],
  ] as const)("given %s and a later global theme, when code renders, then lab colors win without leaking", (_name, theme, palette, base, background) => {
    // Given actual highlight.js output, nested spans, and a competing host theme loaded last.
    const highlighted = hljs.highlight('// Comment\nconst message = `Hello ${name}`;\nconsole.log(message, 42);', { language: "javascript" }).value;
    const tokens = Object.values(tokenGroups).flat().concat(["subst", "operator", "punctuation", "params", "tag", "unknown"]);
    const code = `<pre class="highlight"><code class="hljs language-javascript">${highlighted}
      ${tokens.map((token) => `<span data-token="${token}" class="hljs-${token}">${token}</span>`).join(" ")}
      <span class="hljs-meta"><span class="hljs-string">nested</span></span>
      <span class="hljs-title"><span class="function_">nested title</span></span>
      <span class="hljs-strong">strong</span><span class="hljs-emphasis">emphasis</span>
    </code></pre>`;
    const variables = Object.entries(tuned).map(([role, color]) => `--syntax-${role}-font-color:${color}`).join(";");
    const directory = mkdtempSync(join(tmpdir(), "lab-syntax-theme-"));
    try {
      const fixture = join(directory, "theme.html");
      writeFileSync(fixture, `<!doctype html><html class="${theme}"><head>
        <style>${readFileSync(join(__dirname, "interactive-labs.css"), "utf8")}</style>
        <style>
          pre, .hljs, pre code.hljs { color: #010203; background: #ff00ff linear-gradient(red, blue); font-style: italic; font-weight: 900; }
          .hljs span, .hljs-keyword, .hljs .hljs-meta .hljs-string {
            color: #040506; background: #00ff00 linear-gradient(blue, red);
            font-style: italic; font-weight: 900; text-decoration: underline;
          }
        </style></head><body>
        <div id="lab" class="interactive-labs-content doc">${code}</div>
        <div id="tuned" class="interactive-labs-content doc" style="--code-font-color:#111111;--pre-background:#fafafa;${variables}">${code}</div>
        <div id="host" class="doc">${code}</div><pre id="results"></pre>
        <script>
          const style = (element) => {
            const computed = getComputedStyle(element);
            return Object.fromEntries(["color", "backgroundColor", "backgroundImage", "fontStyle", "fontWeight", "textDecorationLine"].map(key => [key, computed[key]]));
          };
          const snapshot = (id) => {
            const root = document.getElementById(id);
            return {
              pre: style(root.querySelector("pre")), code: style(root.querySelector("code")),
              tokens: Object.fromEntries(Array.from(root.querySelectorAll("[data-token]")).map(element => [element.dataset.token, style(element)])),
              keyword: style(root.querySelector(".hljs-keyword")), string: style(root.querySelector(".hljs-meta .hljs-string")),
              nested: style(root.querySelector(".function_")), strong: style(root.querySelector(".hljs-strong")), emphasis: style(root.querySelector(".hljs-emphasis"))
            };
          };
          document.getElementById("results").textContent = JSON.stringify({ lab: snapshot("lab"), tuned: snapshot("tuned"), host: snapshot("host") });
        </script></body></html>`);

      // When a real browser resolves specificity, inheritance, CSS variables, and backgrounds.
      const output = execFileSync(browser!, ["--headless", "--no-sandbox", "--disable-gpu", `--user-data-dir=${join(directory, "profile")}`, "--dump-dom", fixture], { encoding: "utf8", timeout: 20000, stdio: ["ignore", "pipe", "pipe"] });
      const result: { lab: Snapshot; tuned: Snapshot; host: Snapshot } = JSON.parse(output.match(/<pre id="results">([^<]+)<\/pre>/)![1]);

      // Then both the selected theme and caller palette overrides own every lab code surface.
      for (const [snapshot, colors, foreground, surface] of [
        [result.lab, palette, base, background], [result.tuned, tuned, "#111111", "#fafafa"],
      ] as const) {
        for (const element of [snapshot.pre, snapshot.code]) {
          expect(element).toMatchObject({ color: rgb(foreground), backgroundColor: rgb(surface), backgroundImage: "none", fontStyle: "normal", fontWeight: "400" });
        }
        for (const [role, classes] of Object.entries(tokenGroups)) {
          const color = colors[role as keyof typeof colors];
          expect(contrast(color, surface)).toBeGreaterThanOrEqual(4.5);
          for (const token of classes) expect(snapshot.tokens[token].color).toBe(rgb(color));
        }
        for (const token of ["subst", "operator", "punctuation", "params", "tag", "unknown"]) expect(snapshot.tokens[token].color).toBe(rgb(foreground));
        for (const token of Object.values(snapshot.tokens)) {
          expect(token).toMatchObject({ backgroundColor: "rgba(0, 0, 0, 0)", backgroundImage: "none", fontStyle: "normal", fontWeight: "400", textDecorationLine: "none" });
        }
        expect(snapshot.keyword.color).toBe(rgb(colors.keyword));
        expect(snapshot.string.color).toBe(rgb(colors.string));
        expect(snapshot.nested.color).toBe(rgb(colors.title));
        expect(snapshot.strong.fontWeight).toBe("700");
        expect(snapshot.emphasis.fontStyle).toBe("italic");
      }
      // Host highlighting retains its own color, backgrounds, and emphasis outside the wrapper.
      expect(result.host.code).toMatchObject({ color: "rgb(1, 2, 3)", backgroundColor: "rgb(255, 0, 255)", fontStyle: "italic", fontWeight: "900" });
      expect(result.host.code.backgroundImage).toContain("linear-gradient");
      expect(result.host.keyword).toMatchObject({ color: "rgb(4, 5, 6)", backgroundColor: "rgb(0, 255, 0)", fontWeight: "900", textDecorationLine: "underline" });
    } finally {
      rmSync(directory, { recursive: true, force: true });
    }
  });
});
