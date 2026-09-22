import DOMPurify from "dompurify";

export const sanitizeAsciiDocHtml = (html: string): string =>
  DOMPurify.sanitize(html, { FORBID_TAGS: ["style", "form"], FORBID_ATTR: ["style"] });

export const sanitizeMermaidSvg = (svg: string): string =>
  DOMPurify.sanitize(svg, {
    USE_PROFILES: { svg: true, svgFilters: true },
    FORBID_TAGS: ["form"],
    FORBID_ATTR: [],
  });

export const sanitizeTitle = (title: string): string =>
  DOMPurify.sanitize(title, { ALLOWED_TAGS: ["br", "code", "em", "strong", "sub", "sup"], ALLOWED_ATTR: [] });
