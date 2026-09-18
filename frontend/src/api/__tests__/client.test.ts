// Phase C — API client proxy paths and request shapes.
// consoleFetch is stubbed via moduleNameMapper → src/__mocks__/dynamic-plugin-sdk.ts

import { consoleFetch } from "@openshift-console/dynamic-plugin-sdk";
import { getSetupStatus, listCatalog, loadLabBundle, setupLab } from "../client";

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
    pages: { "010-intro.adoc": "= Intro" },
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

  it("parses non-2xx as LabError", async () => {
    const errBody = { apiVersion: "v1alpha1", kind: "Error", type: "catalog_unavailable", message: "down" };
    mockFetch.mockResolvedValueOnce(makeJsonResponse(errBody, 503));

    await expect(listCatalog()).rejects.toMatchObject({ type: "catalog_unavailable" });
  });
});

describe("loadLabBundle", () => {
  it("calls GET /api/v1alpha1/labs/{catalogRef}/bundle via proxy", async () => {
    const bundleBody = validBundle();
    mockFetch.mockResolvedValueOnce(makeJsonResponse(bundleBody));

    const result = await loadLabBundle("l1");
    const [url] = mockFetch.mock.calls[0];
    expect(url).toBe(`${PROXY_BASE}/api/v1alpha1/labs/l1/bundle`);
    expect(result.manifest.metadata.name).toBe("l1");
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

  it("rejects missing pages and non-string page source", async () => {
    await expectBundleValidationFailure({ ...validBundle(), pages: undefined });
    await expectBundleValidationFailure({ ...validBundle(), pages: { "010-intro.adoc": 42 } });
  });

  it("rejects a bundle with no navigable page", async () => {
    await expectBundleValidationFailure({
      ...validBundle(),
      pages: { "_shared/notice.adoc": "include", "chapter/_shared/tip.adoc": "include" },
    });
  });
});

describe("setupLab", () => {
  it("calls POST /api/v1alpha1/labs/{catalogRef}/setup with empty body", async () => {
    const resultBody = {
      apiVersion: "v1alpha1",
      kind: "SetupStatus",
      spaceRequests: { status: "provisioning", namespaces: null },
      helm: { status: "pending", releaseName: "il-abc123def456" },
    };
    mockFetch.mockResolvedValueOnce(makeJsonResponse(resultBody));

    const result = await setupLab("l1");

    const [url, init] = mockFetch.mock.calls[0];
    expect(url).toBe(`${PROXY_BASE}/api/v1alpha1/labs/l1/setup`);
    expect(init?.method).toBe("POST");
    expect(result.helm.status).toBe("pending");
  });
});

describe("getSetupStatus", () => {
  it("calls GET /api/v1alpha1/labs/{catalogRef}/setup/status", async () => {
    const statusBody = {
      apiVersion: "v1alpha1",
      kind: "SetupStatus",
      spaceRequests: {
        status: "ready",
        namespaces: { gitea: { status: "ready", name: "user1-abc" } },
      },
      helm: { status: "deployed", releaseName: "il-abc123def456" },
    };
    mockFetch.mockResolvedValueOnce(makeJsonResponse(statusBody));

    const result = await getSetupStatus("l1");

    const [url, init] = mockFetch.mock.calls[0];
    expect(url).toBe(`${PROXY_BASE}/api/v1alpha1/labs/l1/setup/status`);
    expect(init?.method ?? "GET").toBe("GET");
    expect(result.spaceRequests.status).toBe("ready");
    expect(result.spaceRequests.namespaces?.gitea.name).toBe("user1-abc");
    expect(result.helm.status).toBe("deployed");
  });
});
