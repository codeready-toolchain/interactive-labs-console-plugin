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
      metadata: { name: "l1", displayName: "L" },
      spec: {},
    },
    pages: [{ path: "010-intro.adoc", url: "api/v1alpha1/labs/l1/resources/lab/content/010-intro.adoc" }],
    runtimeContext: { catalogRef: "l1", branch: "main", helmChartAvailable: false },
  };
}

async function expectBundleValidationFailure(body: unknown): Promise<void> {
  mockFetch.mockResolvedValueOnce(makeJsonResponse(body));
  await expect(loadLabBundle("l1")).rejects.toThrow();
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
        items: [{ catalogRef: "l1", displayName: "Lab", labPath: "labs/../escape" }],
      }),
    );

    await expect(listCatalog()).rejects.toThrow("catalog_invalid");
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

    const result = await loadLabBundle("l1", controller.signal);
    const [url, init] = mockFetch.mock.calls[0];
    expect(url).toBe(`${PROXY_BASE}/api/v1alpha1/labs/l1/bundle`);
    expect(init).toEqual({ signal: controller.signal });
    expect(result.manifest.metadata.name).toBe("l1");
    expect(result.pages).toEqual([
      {
        path: "010-intro.adoc",
        url: `${PROXY_BASE}/api/v1alpha1/labs/l1/resources/lab/content/010-intro.adoc`,
      },
    ]);
  });

  it("parses lab_not_found error on 404", async () => {
    const errBody = { apiVersion: "v1alpha1", kind: "Error", type: "lab_not_found", message: "gone" };
    mockFetch.mockResolvedValueOnce(makeJsonResponse(errBody, 404));

    await expect(loadLabBundle("missing")).rejects.toMatchObject({ type: "lab_not_found" });
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
      manifest: { ...validBundle().manifest, metadata: { name: "l1" } },
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

  it("rejects missing pages and malformed page descriptors", async () => {
    await expectBundleValidationFailure({ ...validBundle(), pages: undefined });
    await expectBundleValidationFailure({ ...validBundle(), pages: [{ path: "010-intro.adoc", url: 42 }] });
    await expectBundleValidationFailure({ ...validBundle(), pages: [{ path: "../escape.adoc", url: "api/v1alpha1/labs/l1/resources/escape.adoc" }] });
  });

  it("rejects duplicate, include-only, and proxy-escaping descriptors", async () => {
    const page = validBundle().pages[0];
    await expectBundleValidationFailure({ ...validBundle(), pages: [page, page] });
    await expectBundleValidationFailure({
      ...validBundle(),
      pages: [{ path: "_shared/notice.adoc", url: "api/v1alpha1/labs/l1/resources/lab/content/_shared/notice.adoc" }],
    });
    await expectBundleValidationFailure({
      ...validBundle(),
      pages: [{ path: "010-intro.adoc", url: "https://untrusted.example/intro.adoc" }],
    });
    await expectBundleValidationFailure({
      ...validBundle(),
      pages: [{ path: "010-intro.adoc", url: "../escape.adoc" }],
    });
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
      kind: "SetupResult",
      outcome: "accepted",
      message: "Lab setup initiated",
    };
    const controller = new AbortController();
    mockFetch.mockResolvedValueOnce(makeJsonResponse(resultBody));

    const result = await setupLab("l1", controller.signal);

    const [url, init] = mockFetch.mock.calls[0];
    expect(url).toBe(`${PROXY_BASE}/api/v1alpha1/labs/l1/setup`);
    expect(init).toEqual({ method: "POST", signal: controller.signal });
    expect(result.outcome).toBe("accepted");
  });
});

describe("getSetupStatus", () => {
  it("calls GET /api/v1alpha1/labs/{catalogRef}/setup/status and forwards cancellation", async () => {
    const statusBody = {
      apiVersion: "v1alpha1",
      kind: "SetupStatus",
      status: "ready",
      namespaces: { gitea: { status: "ready", name: "user1-abc" } },
    };
    const controller = new AbortController();
    mockFetch.mockResolvedValueOnce(makeJsonResponse(statusBody));

    const result = await getSetupStatus("l1", controller.signal);

    const [url, init] = mockFetch.mock.calls[0];
    expect(url).toBe(`${PROXY_BASE}/api/v1alpha1/labs/l1/setup/status`);
    expect(init).toEqual({ signal: controller.signal });
    expect(result.status).toBe("ready");
    expect(result.namespaces.gitea.name).toBe("user1-abc");
  });
});
