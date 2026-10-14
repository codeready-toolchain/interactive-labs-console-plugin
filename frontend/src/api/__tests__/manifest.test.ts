import { getNavigablePagePaths, validateManifest } from "../client";

const valid = { apiVersion: "v1alpha1", kind: "Lab", metadata: { title: "Lab 1" } };

describe("validateManifest", () => {
  it("accepts required title, optional description and string attributes", () => {
    expect(validateManifest(valid).metadata.title).toBe("Lab 1");
    expect(validateManifest({ ...valid, metadata: { title: "Lab 1", description: "Intro" }, spec: { attributes: { audience: "dev" } } }))
      .toMatchObject({ metadata: { description: "Intro" }, spec: { attributes: { audience: "dev" } } });
  });

  it.each(["../shared/chart", "setup/helm", ""])("accepts backend-owned setupDir %s", (setupDir) => {
    // Given an approved bundle, when parsing its manifest, preserve the chart override as data.
    expect(validateManifest({ ...valid, spec: { setupDir } })).toMatchObject({ spec: { setupDir } });
  });

  it.each([null, true, 42, [], {}])("rejects nonstring setupDir %p", (setupDir) => {
    expect(() => validateManifest({ ...valid, spec: { setupDir } })).toThrow("manifest_invalid");
  });

  it("rejects missing/empty title and legacy metadata", () => {
    for (const metadata of [{}, { title: "" }, { title: 1 }, { title: "L", name: "old" },
      { title: "L", displayName: "old" }, { title: "L", description: 42 }, { title: "L", extra: "bad" }]) {
      expect(() => validateManifest({ ...valid, metadata })).toThrow("manifest_invalid");
    }
  });

  it("rejects unsupported versions, wrong kind, and unknown root keys", () => {
    expect(() => validateManifest({ ...valid, apiVersion: "v2" })).toThrow("manifest_version_unsupported");
    expect(() => validateManifest({ ...valid, kind: "Other" })).toThrow("manifest_invalid");
    expect(() => validateManifest({ ...valid, unknown: 1 })).toThrow("manifest_invalid");
  });

  it("rejects invalid spec, root checkpoints, and unknown spec keys", () => {
    for (const spec of [null, "bad", [], { attributes: null }, { attributes: { x: 1 } },
      { checkpoints: [] }, { other: true }]) {
      expect(() => validateManifest({ ...valid, spec })).toThrow("manifest_invalid");
    }
    expect(() => validateManifest({ ...valid, checkpoints: [] })).toThrow("manifest_invalid");
    expect(() => validateManifest(null)).toThrow("manifest_invalid");
  });
});

describe("navigable page paths", () => {
  it("excludes underscore-prefixed content segments and preserves backend order", () => {
    expect(getNavigablePagePaths([
      { path: "modules/020/content/b.adoc", url: "" },
      { path: "modules/020/content/_shared/notice.adoc", url: "" },
      { path: "modules/020/content/a.adoc", url: "" },
    ])).toEqual(["modules/020/content/b.adoc", "modules/020/content/a.adoc"]);
  });
});
