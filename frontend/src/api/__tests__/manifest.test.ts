// Phase D — Client-side manifest validation and bundle page normalization.

import * as client from "../client";

const { validateManifest } = client;
const getNavigablePagePaths = (client as typeof client & {
  getNavigablePagePaths: (pages: unknown) => string[];
}).getNavigablePagePaths;

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

  it("throws for kind !== Lab", () => {
    expect(() => validateManifest({ ...validManifest, kind: "Other" })).toThrow();
  });

  it("throws for a missing or non-object spec", () => {
    expect(() => validateManifest({ ...validManifest, spec: undefined })).toThrow();
    expect(() => validateManifest({ ...validManifest, spec: "not an object" })).toThrow();
  });

  it("throws for non-object input", () => {
    expect(() => validateManifest(null)).toThrow();
    expect(() => validateManifest("string")).toThrow();
    expect(() => validateManifest(42)).toThrow();
  });

  it.each([
    ["a null value", null],
    ["a non-record value", "not an attribute record"],
    ["an array", ["not", "an", "attribute", "record"]],
    ["a record with a non-string value", { audience: 42 }],
  ])("throws for attributes with %s", (_description, attributes) => {
    expect(() => validateManifest({ ...validManifest, spec: { attributes } })).toThrow();
  });

  it.each([
    ["a non-array value", "not checkpoint records"],
    ["a record instead of an array", {}],
    ["a null item", [null]],
    ["an array item", [[]]],
    ["a scalar item", ["checkpoint"]],
    ["a non-manual type", [{ id: "done", type: "automatic", pagePath: "intro.adoc", label: "Done" }]],
    ["a missing type", [{ id: "done", pagePath: "intro.adoc", label: "Done" }]],
    ["a missing id", [{ type: "manual", pagePath: "intro.adoc", label: "Done" }]],
    ["an empty id", [{ id: "", type: "manual", pagePath: "intro.adoc", label: "Done" }]],
    ["a non-string id", [{ id: 1, type: "manual", pagePath: "intro.adoc", label: "Done" }]],
    ["a missing pagePath", [{ id: "done", type: "manual", label: "Done" }]],
    ["an empty pagePath", [{ id: "done", type: "manual", pagePath: "", label: "Done" }]],
    ["a non-string pagePath", [{ id: "done", type: "manual", pagePath: 1, label: "Done" }]],
    ["a missing label", [{ id: "done", type: "manual", pagePath: "intro.adoc" }]],
    ["an empty label", [{ id: "done", type: "manual", pagePath: "intro.adoc", label: "" }]],
    ["a non-string label", [{ id: "done", type: "manual", pagePath: "intro.adoc", label: 1 }]],
  ])("throws for checkpoints with %s", (_description, checkpoints) => {
    expect(() => validateManifest({ ...validManifest, spec: { checkpoints } })).toThrow();
  });
});

describe("bundle page normalization", () => {
  it("excludes paths with underscore-prefixed segments from lexical navigation", () => {
    const pages = [
      { path: "020-install.adoc", url: "api/v1alpha1/labs/l1/resources/lab/content/020-install.adoc" },
      { path: "010-intro.adoc", url: "api/v1alpha1/labs/l1/resources/lab/content/010-intro.adoc" },
      { path: "chapter/_shared/notice.adoc", url: "api/v1alpha1/labs/l1/resources/lab/content/chapter/_shared/notice.adoc" },
      { path: "_shared/appendix.adoc", url: "api/v1alpha1/labs/l1/resources/lab/content/_shared/appendix.adoc" },
    ];

    const navigablePaths = getNavigablePagePaths(pages);

    expect(navigablePaths).toEqual(["010-intro.adoc", "020-install.adoc"]);
    expect(navigablePaths[0]).toBe("010-intro.adoc");
  });
});
