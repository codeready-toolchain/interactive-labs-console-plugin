// Phase C — API client proxy paths and request shapes.
// consoleFetch is stubbed via moduleNameMapper → src/__mocks__/dynamic-plugin-sdk.ts

import { consoleFetch } from "@openshift-console/dynamic-plugin-sdk";
import { listCatalog, loadLabBundle, setupLab } from "../client";

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
    const bundleBody = {
      apiVersion: "v1alpha1",
      kind: "LabBundle",
      manifest: { apiVersion: "v1alpha1", kind: "Lab", metadata: { name: "l1", displayName: "L" }, spec: {} },
      pages: {},
      runtimeContext: { catalogRef: "l1", branch: "main", helmChartAvailable: false },
    };
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
});

describe("setupLab", () => {
  it("calls POST /api/v1alpha1/labs/{catalogRef}/setup with JSON body", async () => {
    const resultBody = {
      apiVersion: "v1alpha1",
      kind: "SetupResult",
      outcome: "success",
      message: "done",
      helmReleaseName: "il-abc",
    };
    mockFetch.mockResolvedValueOnce(makeJsonResponse(resultBody));

    const bindings = { releaseNamespace: "my-ns" };
    const result = await setupLab("l1", bindings);

    const [url, init] = mockFetch.mock.calls[0];
    expect(url).toBe(`${PROXY_BASE}/api/v1alpha1/labs/l1/setup`);
    expect(init?.method).toBe("POST");
    expect(init?.headers).toMatchObject({ "Content-Type": "application/json" });
    expect(JSON.parse(init?.body as string)).toEqual({ namespaceBindings: bindings });
    expect(result.helmReleaseName).toBe("il-abc");
  });
});
