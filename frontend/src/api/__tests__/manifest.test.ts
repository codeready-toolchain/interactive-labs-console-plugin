// Phase D — Client-side manifest validation and bundle page normalization.

import { validateManifest } from "../client";

describe("validateManifest", () => {
  const validManifest = {
    apiVersion: "v1alpha1",
    kind: "Lab",
    metadata: { name: "l1", displayName: "Lab 1" },
    spec: {},
  };

  it("accepts a valid v1alpha1 manifest", () => {
    const result = validateManifest(validManifest);
    expect(result.metadata.name).toBe("l1");
  });

  it("throws for apiVersion !== v1alpha1", () => {
    expect(() => validateManifest({ ...validManifest, apiVersion: "v2" })).toThrow(
      /manifest_version_unsupported/,
    );
  });

  it("throws for missing metadata.name", () => {
    const bad = { apiVersion: "v1alpha1", kind: "Lab", metadata: { displayName: "X" }, spec: {} };
    expect(() => validateManifest(bad)).toThrow();
  });

  it("throws for missing metadata.displayName", () => {
    const bad = { apiVersion: "v1alpha1", kind: "Lab", metadata: { name: "x" }, spec: {} };
    expect(() => validateManifest(bad)).toThrow();
  });

  it("throws for non-object input", () => {
    expect(() => validateManifest(null)).toThrow();
    expect(() => validateManifest("string")).toThrow();
    expect(() => validateManifest(42)).toThrow();
  });
});

describe("bundle page normalization", () => {
  it("underscore-prefixed paths are include-only (not navigation targets)", () => {
    const pages = {
      "010-intro.adoc": "content",
      "_shared/notice.adoc": "include",
      "020-install.adoc": "content",
    };
    const navPaths = Object.keys(pages)
      .filter(p => !p.split("/").some(seg => seg.startsWith("_")))
      .sort();
    expect(navPaths).toEqual(["010-intro.adoc", "020-install.adoc"]);
    expect(navPaths[0]).toBe("010-intro.adoc"); // lexically first = entry page
  });

  it("entry page is lexically first navigable path", () => {
    const pages = {
      "020-step.adoc": "x",
      "010-intro.adoc": "y",
      "_shared/notice.adoc": "z",
    };
    const nav = Object.keys(pages)
      .filter(p => !p.split("/").some(s => s.startsWith("_")))
      .sort();
    expect(nav[0]).toBe("010-intro.adoc");
  });
});
