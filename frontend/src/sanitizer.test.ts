import { sanitizeAsciiDocHtml, sanitizeMermaidSvg, sanitizeTitle } from "./sanitizer";

describe("sanitizer policies", () => {
  it("removes styles and forms from AsciiDoc HTML without removing safe content", () => {
    const html = sanitizeAsciiDocHtml(
      '<style>.hidden { display: none; }</style><p style="color: red">Safe <strong>content</strong></p><form><input name="unsafe" /></form>',
    );

    expect(html).toContain("<p>Safe <strong>content</strong></p>");
    expect(html).not.toMatch(/<style|style=|<form/i);
  });

  it("keeps Mermaid SVG styles but removes scripts and event handlers", () => {
    const svg = sanitizeMermaidSvg(
      '<svg xmlns="http://www.w3.org/2000/svg"><style>.node { fill: #fff; }</style><script>alert(1)</script><g onload="alert(1)"><text>Browser</text></g></svg>',
    );

    expect(svg).toContain("<style>.node { fill: #fff; }</style>");
    expect(svg).toContain("<text>Browser</text>");
    expect(svg).not.toMatch(/<script|onload=/i);
  });

  it("removes unsafe title markup while retaining emphasis", () => {
    expect(sanitizeTitle("<script>alert(1)</script><em>Lab title</em>")).toBe("<em>Lab title</em>");
  });
});
