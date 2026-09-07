import { consoleFetch } from "@openshift-console/dynamic-plugin-sdk";
import type { CatalogEntry, CatalogList } from "../types/catalog";
import type { LabBundle, LabManifest, LabPage } from "../types/manifest";
import type { SetupResult } from "../types/setup";
import { isKnownErrorType, type LabError } from "../types/errors";

const PROXY_BASE = "/api/proxy/plugin/interactive-labs-console-plugin/backend";

// --- Error parsing ---

export function parseLabError(body: unknown): LabError {
  if (
    body !== null &&
    typeof body === "object" &&
    (body as Record<string, unknown>)["kind"] === "Error"
  ) {
    const b = body as Record<string, unknown>;
    const type = isKnownErrorType(b["type"]) ? b["type"] : "unknown_error";
    return {
      apiVersion: "v1alpha1",
      kind: "Error",
      type,
      message: typeof b["message"] === "string" ? b["message"] : "Unknown error",
      detail: b["detail"],
    };
  }
  return { apiVersion: "v1alpha1", kind: "Error", type: "unknown_error", message: "Unexpected response" };
}

async function parseResponse<T>(res: Response): Promise<T> {
  const body: unknown = await res.json();
  if (!res.ok) throw parseLabError(body);
  return body as T;
}

// --- API operations ---

export async function listCatalog(): Promise<CatalogList> {
  const res = await consoleFetch(`${PROXY_BASE}/api/v1alpha1/catalog`);
  return validateCatalogList(await parseResponse<unknown>(res));
}

export async function loadLabBundle(catalogRef: string): Promise<LabBundle> {
  const res = await consoleFetch(`${PROXY_BASE}/api/v1alpha1/labs/${catalogRef}/bundle`);
  return validateLabBundle(await parseResponse<unknown>(res), catalogRef);
}

export async function fetchResourceText(resourceURL: string, signal: AbortSignal): Promise<string> {
  const base = new URL(`${PROXY_BASE}/`, window.location.origin);
  const resolved = new URL(resourceURL, window.location.origin);
  if (
    resolved.origin !== base.origin ||
    !/^\/api\/proxy\/plugin\/interactive-labs-console-plugin\/backend\/api\/v1alpha1\/labs\/[^/]+\/resources\//.test(resolved.pathname) ||
    resolved.search ||
    resolved.hash
  ) {
    throw new Error("resource_invalid: invalid resource URL");
  }
  const res = await consoleFetch(resolved.pathname, { signal });
  if (!res.ok) throw parseLabError(await res.json());
  return res.text();
}

export async function setupLab(
  catalogRef: string,
  namespaceBindings: Record<string, string>,
): Promise<SetupResult> {
  const res = await consoleFetch(`${PROXY_BASE}/api/v1alpha1/labs/${catalogRef}/setup`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ namespaceBindings }),
  });
  return parseResponse<SetupResult>(res);
}

// --- Bundle validation ---

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

export function getNavigablePagePaths(pages: LabPage[]): string[] {
  return pages
    .map((page) => page.path)
    .filter((pagePath) => pagePath.split("/").every((segment) => !segment.startsWith("_")))
    .sort();
}

function isNormalizedRelativePath(value: unknown): value is string {
  return (
    typeof value === "string" &&
    value !== "" &&
    (value === "." ||
      (!value.startsWith("/") &&
        !value.endsWith("/") &&
        value.split("/").every((segment) => segment !== "" && segment !== "." && segment !== "..")))
  );
}

function resolveProxyURL(value: unknown): string {
  if (typeof value !== "string" || !value || value.startsWith("/")) {
    throw new Error("bundle_invalid: invalid page URL");
  }
  const base = new URL(`${PROXY_BASE}/`, "https://console.invalid");
  const resolved = new URL(value, base);
  if (
    resolved.origin !== base.origin ||
    !resolved.pathname.startsWith(`${PROXY_BASE}/`) ||
    resolved.search ||
    resolved.hash
  ) {
    throw new Error("bundle_invalid: invalid page URL");
  }
  return resolved.pathname;
}

function validateCatalogList(raw: unknown): CatalogList {
  if (raw === null || typeof raw !== "object") throw new Error("catalog_invalid: invalid envelope");
  const list = raw as Record<string, unknown>;
  if (list["apiVersion"] !== "v1alpha1" || list["kind"] !== "CatalogList" || !Array.isArray(list["items"])) {
    throw new Error("catalog_invalid: invalid envelope");
  }
  const items: CatalogEntry[] = list["items"].map((rawItem) => {
    if (!isRecord(rawItem) || typeof rawItem["catalogRef"] !== "string" || !rawItem["catalogRef"] ||
      typeof rawItem["displayName"] !== "string" || !rawItem["displayName"] ||
      !isNormalizedRelativePath(rawItem["labPath"]) ||
      (rawItem["description"] !== undefined && typeof rawItem["description"] !== "string")) {
      throw new Error("catalog_invalid: invalid item");
    }
    return {
      catalogRef: rawItem["catalogRef"],
      displayName: rawItem["displayName"],
      ...(rawItem["description"] === undefined ? {} : { description: rawItem["description"] }),
      labPath: rawItem["labPath"],
    };
  });
  return { apiVersion: "v1alpha1", kind: "CatalogList", items };
}

export function validateManifest(raw: unknown): LabManifest {
  if (!isRecord(raw)) throw new Error("manifest_invalid: not an object");
  if (raw["apiVersion"] !== "v1alpha1") {
    throw new Error(`manifest_version_unsupported: got ${String(raw["apiVersion"])}`);
  }
  if (raw["kind"] !== "Lab") throw new Error("manifest_invalid: kind must be Lab");

  const metadata = raw["metadata"];
  if (!isRecord(metadata) || typeof metadata["name"] !== "string" || !metadata["name"]) {
    throw new Error("manifest_invalid: missing metadata.name");
  }
  if (typeof metadata["displayName"] !== "string" || !metadata["displayName"]) {
    throw new Error("manifest_invalid: missing metadata.displayName");
  }

  const spec = raw["spec"];
  if (!isRecord(spec)) throw new Error("manifest_invalid: missing spec");

  if (
    spec["attributes"] !== undefined &&
    (!isRecord(spec["attributes"]) || Object.values(spec["attributes"]).some((value) => typeof value !== "string"))
  ) {
    throw new Error("manifest_invalid: invalid spec.attributes");
  }

  if (
    spec["checkpoints"] !== undefined &&
    (!Array.isArray(spec["checkpoints"]) ||
      spec["checkpoints"].some(
        (checkpoint) =>
          !isRecord(checkpoint) ||
          checkpoint["type"] !== "manual" ||
          ["id", "pagePath", "label"].some(
            (field) => typeof checkpoint[field] !== "string" || !checkpoint[field],
          ),
      ))
  ) {
    throw new Error("manifest_invalid: invalid spec.checkpoints");
  }

  return raw as unknown as LabManifest;
}

function validateLabBundle(raw: unknown, catalogRef: string): LabBundle {
  if (!isRecord(raw) || raw["apiVersion"] !== "v1alpha1" || raw["kind"] !== "LabBundle") {
    throw new Error("bundle_invalid: invalid envelope");
  }

  validateManifest(raw["manifest"]);

  const runtimeContext = raw["runtimeContext"];
  if (
    !isRecord(runtimeContext) ||
    runtimeContext["catalogRef"] !== catalogRef ||
    typeof runtimeContext["branch"] !== "string" ||
    !runtimeContext["branch"] ||
    typeof runtimeContext["helmChartAvailable"] !== "boolean"
  ) {
    throw new Error("bundle_invalid: invalid runtime context");
  }

  const rawPages = raw["pages"];
  if (!Array.isArray(rawPages)) throw new Error("bundle_invalid: invalid pages");
  const pagePaths = new Set<string>();
  const pages: LabPage[] = rawPages.map((rawPage) => {
    if (!isRecord(rawPage) || !isNormalizedRelativePath(rawPage["path"]) ||
      rawPage["path"] === "." || !rawPage["path"].endsWith(".adoc") ||
      rawPage["path"].split("/").some((segment) => segment.startsWith("_")) ||
      pagePaths.has(rawPage["path"])) {
      throw new Error("bundle_invalid: invalid page descriptor");
    }
    pagePaths.add(rawPage["path"]);
    return { path: rawPage["path"], url: resolveProxyURL(rawPage["url"]) };
  });
  if (pages.length === 0) throw new Error("bundle_invalid: no navigable pages");

  return {
    apiVersion: "v1alpha1",
    kind: "LabBundle",
    manifest: validateManifest(raw["manifest"]),
    pages,
    runtimeContext: {
      catalogRef: runtimeContext["catalogRef"],
      branch: runtimeContext["branch"],
      helmChartAvailable: runtimeContext["helmChartAvailable"],
    },
  };
}
