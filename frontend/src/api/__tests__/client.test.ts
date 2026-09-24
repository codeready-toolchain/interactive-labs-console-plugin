// Phase C — API client proxy paths and request shapes.
// consoleFetch is stubbed via moduleNameMapper → src/__mocks__/dynamic-plugin-sdk.ts

import { consoleFetch } from "@openshift-console/dynamic-plugin-sdk";
import { fetchResourceText, getSetupStatus, listCatalog, loadLabBundle, setupLab } from "../client";

const mockFetch = consoleFetch as jest.MockedFunction<typeof consoleFetch>;

const PROXY_BASE = "/api/proxy/plugin/interactive-labs-console-plugin/backend";

function makeJsonResponse(body: unknown, status = 200): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    json: () => Promise.resolve(body),
    text: () => Promise.resolve(JSON.stringify(body)),
  } as unknown as Response;
}

function validBundle() {
  return {
    apiVersion: "v1alpha1",
    kind: "LabBundle",
    manifest: {
      apiVersion: "v1alpha1",
      kind: "Lab",
      metadata: { title: "L" },
      spec: {},
    },
    modules: [{ id: "010-intro", pages: [{
      path: "modules/010-intro/content/010-intro.adoc",
      url: "api/v1alpha1/labs/l1/resources/lab/modules/010-intro/content/010-intro.adoc",
    }] }],
    runtimeContext: { catalogRef: "l1", branch: "main", helmChartAvailable: false },
  };
}

async function expectBundleValidationFailure(body: unknown): Promise<void> {
  mockFetch.mockResolvedValueOnce(makeJsonResponse(body));
  await expect(loadLabBundle("l1", "lab")).rejects.toThrow();
}

beforeEach(() => {
  mockFetch.mockReset();
});

describe("listCatalog", () => {
  it("calls GET /api/v1alpha1/catalog via proxy", async () => {
    const catalogBody = { apiVersion: "v1alpha1", kind: "CatalogList", items: [] };
    mockFetch.mockResolvedValueOnce(makeJsonResponse(catalogBody));

    const result = await listCatalog();

    expect(mockFetch).toHaveBeenCalledTimes(1);
    const [url, init] = mockFetch.mock.calls[0];
    expect(url).toBe(`${PROXY_BASE}/api/v1alpha1/catalog`);
    expect(init?.method ?? "GET").toBe("GET");
    expect(result).toEqual(catalogBody);
  });

  it("rejects catalog entries without normalized labPath", async () => {
    mockFetch.mockResolvedValueOnce(
      makeJsonResponse({
        apiVersion: "v1alpha1",
        kind: "CatalogList",
        items: [{ catalogRef: "l1", title: "Lab", labPath: "labs/../escape" }],
      }),
    );

    await expect(listCatalog()).rejects.toThrow("catalog_invalid");
  });

  it("requires title and rejects old displayName", async () => {
    mockFetch.mockResolvedValueOnce(makeJsonResponse({ apiVersion: "v1alpha1", kind: "CatalogList", items: [
      { catalogRef: "l1", title: "Lab", labPath: ".", description: "Intro" },
    ] }));
    await expect(listCatalog()).resolves.toMatchObject({ items: [{ title: "Lab", labPath: "." }] });
    for (const item of [
      { catalogRef: "l1", displayName: "Lab", labPath: "lab" },
      { catalogRef: "l1", title: "Lab", displayName: "Legacy", labPath: "lab" },
    ]) {
      mockFetch.mockResolvedValueOnce(makeJsonResponse({ apiVersion: "v1alpha1", kind: "CatalogList", items: [item] }));
      await expect(listCatalog()).rejects.toThrow("catalog_invalid");
    }
  });

  it.each(["labs/intro#one", "labs/intro?one"])('accepts backend-valid literal punctuation in catalog labPath %s', async (labPath) => {
    mockFetch.mockResolvedValueOnce(makeJsonResponse({ apiVersion: "v1alpha1", kind: "CatalogList", items: [
      { catalogRef: "l1", title: "Lab", labPath },
    ] }));

    await expect(listCatalog()).resolves.toMatchObject({ items: [{ labPath }] });
  });

  it("parses non-2xx as LabError", async () => {
    const errBody = { apiVersion: "v1alpha1", kind: "Error", type: "catalog_unavailable", message: "down" };
    mockFetch.mockResolvedValueOnce(makeJsonResponse(errBody, 503));

    await expect(listCatalog()).rejects.toMatchObject({ type: "catalog_unavailable" });
  });
});

describe("loadLabBundle", () => {
  it("calls GET /api/v1alpha1/labs/{catalogRef}/bundle via proxy and forwards cancellation", async () => {
    const bundleBody = validBundle();
    const controller = new AbortController();
    mockFetch.mockResolvedValueOnce(makeJsonResponse(bundleBody));

    const result = await loadLabBundle("l1", "lab", controller.signal);
    const [url, init] = mockFetch.mock.calls[0];
    expect(url).toBe(`${PROXY_BASE}/api/v1alpha1/labs/l1/bundle`);
    expect(init).toEqual({ signal: controller.signal });
    expect(result.manifest.metadata.title).toBe("L");
    expect(result.modules).toEqual([{ id: "010-intro", pages: [{
      path: "modules/010-intro/content/010-intro.adoc",
      url: `${PROXY_BASE}/api/v1alpha1/labs/l1/resources/lab/modules/010-intro/content/010-intro.adoc`,
    }] }]);
  });

  it("parses lab_not_found error on 404", async () => {
    const errBody = { apiVersion: "v1alpha1", kind: "Error", type: "lab_not_found", message: "gone" };
    mockFetch.mockResolvedValueOnce(makeJsonResponse(errBody, 404));

    await expect(loadLabBundle("missing", "lab")).rejects.toMatchObject({ type: "lab_not_found" });
  });

  it("rejects an invalid LabBundle envelope", async () => {
    await expectBundleValidationFailure({ ...validBundle(), apiVersion: "v2" });
    await expectBundleValidationFailure({ ...validBundle(), kind: "Other" });
  });

  it("rejects an invalid nested manifest", async () => {
    await expectBundleValidationFailure({
      ...validBundle(),
      manifest: { ...validBundle().manifest, kind: "Other" },
    });
    await expectBundleValidationFailure({
      ...validBundle(),
      manifest: { ...validBundle().manifest, metadata: { displayName: "L" } },
    });
  });

  it("rejects malformed runtime context", async () => {
    await expectBundleValidationFailure({
      ...validBundle(),
      runtimeContext: { catalogRef: "l1", branch: "", helmChartAvailable: false },
    });
    await expectBundleValidationFailure({
      ...validBundle(),
      runtimeContext: { catalogRef: "l1", branch: "main", helmChartAvailable: "false" },
    });
  });

  it("rejects a runtime catalogRef that differs from requested catalogRef", async () => {
    await expectBundleValidationFailure({
      ...validBundle(),
      runtimeContext: { catalogRef: "other", branch: "main", helmChartAvailable: false },
    });
  });

  it("accepts ordered modules and pages at labPath '.' without sorting", async () => {
    const bundle = validBundle();
    const page = (id: string, file: string) => ({
      path: `modules/${id}/content/${file}`,
      url: `api/v1alpha1/labs/l1/resources/modules/${id}/content/${file}`,
    });
    bundle.modules = [
      { id: "020-next", pages: [page("020-next", "b.adoc"), page("020-next", "a.adoc")] },
      { id: "010-first", pages: [page("010-first", "intro.adoc")] },
    ];
    mockFetch.mockResolvedValueOnce(makeJsonResponse(bundle));
    const result = await loadLabBundle("l1", ".");
    expect(result.modules.map((module) => module.id)).toEqual(["020-next", "010-first"]);
    expect(result.modules[0].pages.map((item) => item.path)).toEqual([
      "modules/020-next/content/b.adoc", "modules/020-next/content/a.adoc",
    ]);
    expect(result.modules[0].pages[0].url).toBe(`${PROXY_BASE}/api/v1alpha1/labs/l1/resources/modules/020-next/content/b.adoc`);
  });

  it.each([
    ["step#1.adoc", "step%231.adoc"],
    ["step?1.adoc", "step%3F1.adoc"],
  ])("accepts literal page filename %s with encoded resource URL", async (filename, encodedFilename) => {
    const bundle = validBundle();
    const labPath = "labs/intro#one?two";
    const path = `modules/010-intro/content/${filename}`;
    const url = `api/v1alpha1/labs/l1/resources/labs/intro%23one%3Ftwo/modules/010-intro/content/${encodedFilename}`;
    bundle.modules[0].pages = [{ path, url }];
    mockFetch.mockResolvedValueOnce(makeJsonResponse(bundle));

    const result = await loadLabBundle("l1", labPath);

    expect(result.modules[0].pages).toEqual([{ path, url: `${PROXY_BASE}/${url}` }]);
    for (const suffix of ["?query=1", "#fragment"]) {
      mockFetch.mockResolvedValueOnce(makeJsonResponse({
        ...bundle,
        modules: [{ ...bundle.modules[0], pages: [{ path, url: `${url}${suffix}` }] }],
      }));
      await expect(loadLabBundle("l1", labPath)).rejects.toThrow("bundle_invalid: invalid page URL");
    }
  });

  it("rejects empty, duplicate or unsafe modules and old flat pages", async () => {
    const bundle = validBundle();
    const module = bundle.modules[0];
    for (const modules of [[], [module, module], [{ ...module, id: "../bad" }],
      [{ ...module, id: "_hidden" }], [{ ...module, pages: [] }]]) {
      await expectBundleValidationFailure({ ...bundle, modules });
    }
    await expectBundleValidationFailure({ ...bundle, pages: [module.pages[0]] });
    await expectBundleValidationFailure({ ...bundle, modules: undefined });
  });

  it("rejects missing, duplicated, include-only, wrong-module and non-adoc paths", async () => {
    const bundle = validBundle();
    const module = bundle.modules[0];
    for (const path of ["modules/010-intro/content/_partial.adoc", "modules/010-intro/content/sub/_note.adoc",
      "modules/other/content/page.adoc", "content/page.adoc", "modules/010-intro/content/../page.adoc",
      "modules/010-intro/content/page.txt", "modules/010-intro/content/%2e%2e/page.adoc", "modules/010-intro/content/back\\slash.adoc"]) {
      await expectBundleValidationFailure({ ...bundle, modules: [{ ...module, pages: [{ ...module.pages[0], path }] }] });
    }
    await expectBundleValidationFailure({ ...bundle, modules: [{ ...module, pages: [module.pages[0], module.pages[0]] }] });
    await expectBundleValidationFailure({ ...bundle, modules: [{ ...module, pages: [{ ...module.pages[0], url: 42 }] }] });
  });

  it("rejects wrong catalogRef, labPath, module, page, origin, query, fragment and URL traversal", async () => {
    const bundle = validBundle();
    const module = bundle.modules[0];
    const route = "api/v1alpha1/labs/l1/resources/lab/modules/010-intro/content/010-intro.adoc";
    for (const url of [
      route.replace("/labs/l1/", "/labs/other/"), route.replace("/resources/lab/", "/resources/other/"),
      route.replace("/modules/010-intro/", "/modules/other/"), route.replace("/010-intro.adoc", "/other.adoc"),
      `https://evil.example/${route}`, `//evil.example/${route}`, `/${route}`, `../${route}`,
      `${route}?query=1`, `${route}#heading`, route.replace("/content/", "/content/../content/"),
      route.replace("/content/", "/content/%2e%2e/content/"),
      route.replace("/content/", "/content/%252e%252e/content/"),
      route.replace("/content/", "/content/%2f/"),
    ]) {
      await expectBundleValidationFailure({ ...bundle, modules: [{ ...module, pages: [{ ...module.pages[0], url }] }] });
    }
    mockFetch.mockResolvedValueOnce(makeJsonResponse(bundle));
    await expect(loadLabBundle("l1", "wrong-lab")).rejects.toThrow("bundle_invalid");
  });
});

describe("fetchResourceText", () => {
  it("fetches only a proxy-resolved approved resource and preserves cancellation", async () => {
    const controller = new AbortController();
    mockFetch.mockResolvedValueOnce({ ok: true, text: () => Promise.resolve("= Safe page") } as Response);

    await expect(
      fetchResourceText(`${PROXY_BASE}/api/v1alpha1/labs/l1/resources/lab/content/intro.adoc`, controller.signal),
    ).resolves.toBe("= Safe page");
    expect(mockFetch).toHaveBeenCalledWith(
      `${PROXY_BASE}/api/v1alpha1/labs/l1/resources/lab/content/intro.adoc`,
      { signal: controller.signal },
    );
  });

  it("rejects a resource URL outside its Console proxy boundary", async () => {
    await expect(fetchResourceText("https://evil.example/intro.adoc", new AbortController().signal)).rejects.toThrow(
      "resource_invalid",
    );
    expect(mockFetch).not.toHaveBeenCalled();
  });
});

describe("setupLab", () => {
  it("calls POST /api/v1alpha1/labs/{catalogRef}/setup with empty body and forwards cancellation", async () => {
    const resultBody = {
      apiVersion: "v1alpha1",
      kind: "SetupStatus",
      spaceRequests: { status: "provisioning", namespaces: null },
      helm: { status: "pending", releaseName: "il-abc123def456" },
    };
    const controller = new AbortController();
    mockFetch.mockResolvedValueOnce(makeJsonResponse(resultBody));

    const result = await setupLab("l1", controller.signal);

    const [url, init] = mockFetch.mock.calls[0];
    expect(url).toBe(`${PROXY_BASE}/api/v1alpha1/labs/l1/setup`);
    expect(init?.method).toBe("POST");
    expect(result.helm.status).toBe("pending");
  });
});

describe("getSetupStatus", () => {
  it("calls GET /api/v1alpha1/labs/{catalogRef}/setup/status and forwards cancellation", async () => {
    const statusBody = {
      apiVersion: "v1alpha1",
      kind: "SetupStatus",
      spaceRequests: {
        status: "ready",
        namespaces: { gitea: { status: "ready", name: "user1-abc" } },
      },
      helm: { status: "deployed", releaseName: "il-abc123def456" },
    };
    const controller = new AbortController();
    mockFetch.mockResolvedValueOnce(makeJsonResponse(statusBody));

    const result = await getSetupStatus("l1", controller.signal);

    const [url, init] = mockFetch.mock.calls[0];
    expect(url).toBe(`${PROXY_BASE}/api/v1alpha1/labs/l1/setup/status`);
    expect(init?.method ?? "GET").toBe("GET");
    expect(result.spaceRequests.status).toBe("ready");
    expect(result.spaceRequests.namespaces?.gitea.name).toBe("user1-abc");
    expect(result.helm.status).toBe("deployed");
  });
});
