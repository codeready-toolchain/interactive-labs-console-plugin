import { consoleFetch } from "@openshift-console/dynamic-plugin-sdk";
import type { CatalogEntry, CatalogList } from "../types/catalog";
import type { LabBundle, LabManifest, LabModule, LabPage } from "../types/manifest";
import type { SetupResult, SetupStatus } from "../types/setup";
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

export async function loadLabBundle(catalogRef: string, labPath: string, signal?: AbortSignal): Promise<LabBundle> {
  if (!/^[A-Za-z0-9_-]+$/.test(catalogRef) || !isNormalizedRelativePath(labPath)) {
    throw new Error("bundle_invalid: invalid catalog identity");
  }
  const res = await consoleFetch(`${PROXY_BASE}/api/v1alpha1/labs/${catalogRef}/bundle`, { signal });
  return validateLabBundle(await parseResponse<unknown>(res), catalogRef, labPath);
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

export async function setupLab(catalogRef: string, signal?: AbortSignal): Promise<SetupResult> {
  const res = await consoleFetch(`${PROXY_BASE}/api/v1alpha1/labs/${catalogRef}/setup`, {
    method: "POST",
    signal,
  });
  return parseResponse<SetupResult>(res);
}

export async function getSetupStatus(catalogRef: string, signal?: AbortSignal): Promise<SetupStatus> {
  const res = await consoleFetch(`${PROXY_BASE}/api/v1alpha1/labs/${catalogRef}/setup/status`, { signal });
  return parseResponse<SetupStatus>(res);
}

// --- Bundle validation ---

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

export function getNavigablePagePaths(pages: LabPage[]): string[] {
  return pages
    .map((page) => page.path)
    .filter((pagePath) => pagePath.split("/").every((segment) => !segment.startsWith("_")));
}

function isNormalizedRelativePath(value: unknown): value is string {
  return (
    typeof value === "string" &&
    value !== "" &&
    (value === "." ||
      (!value.startsWith("/") &&
        !value.endsWith("/") &&
        !/[\\]/.test(value) &&
        !Array.from(value).some((char) => char.charCodeAt(0) < 32) &&
        !/%[0-9a-f]{2}/i.test(value) &&
        value.split("/").every((segment) => segment !== "" && segment !== "." && segment !== "..")))
  );
}

function resolveProxyURL(value: unknown, catalogRef: string, labPath: string, pagePath: string): string {
  if (typeof value !== "string" || !value || /[\\?#]/.test(value) || /%(?:2e|2f|5c)/i.test(value)) {
    throw new Error("bundle_invalid: invalid page URL");
  }
  const expected = `api/v1alpha1/labs/${catalogRef}/resources/${labPath === "." ? "" : `${labPath}/`}${pagePath}`;
  try {
    const base = new URL(`${PROXY_BASE}/`, "https://console.invalid");
    const resolved = new URL(value, base);
    if (decodeURIComponent(value) !== expected ||
      resolved.origin !== base.origin || resolved.pathname !== `${PROXY_BASE}/${value}`) {
      throw new Error("bundle_invalid: invalid page URL");
    }
    return resolved.pathname;
  } catch {
    throw new Error("bundle_invalid: invalid page URL");
  }
}

function validateCatalogList(raw: unknown): CatalogList {
  if (raw === null || typeof raw !== "object") throw new Error("catalog_invalid: invalid envelope");
  const list = raw as Record<string, unknown>;
  if (list["apiVersion"] !== "v1alpha1" || list["kind"] !== "CatalogList" || !Array.isArray(list["items"])) {
    throw new Error("catalog_invalid: invalid envelope");
  }
  const items: CatalogEntry[] = list["items"].map((rawItem) => {
    if (!isRecord(rawItem) || typeof rawItem["catalogRef"] !== "string" || !/^[A-Za-z0-9_-]+$/.test(rawItem["catalogRef"]) ||
      typeof rawItem["title"] !== "string" || !rawItem["title"].trim() ||
      !isNormalizedRelativePath(rawItem["labPath"]) ||
      (rawItem["description"] !== undefined && typeof rawItem["description"] !== "string") ||
      Object.keys(rawItem).some((key) => !["catalogRef", "title", "description", "labPath"].includes(key))) {
      throw new Error("catalog_invalid: invalid item");
    }
    return {
      catalogRef: rawItem["catalogRef"],
      title: rawItem["title"],
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
  if (!isRecord(metadata) || typeof metadata["title"] !== "string" || !metadata["title"].trim() ||
    (metadata["description"] !== undefined && typeof metadata["description"] !== "string") ||
    Object.keys(metadata).some((key) => !["title", "description"].includes(key)) ||
    Object.keys(raw).some((key) => !["apiVersion", "kind", "metadata", "spec"].includes(key))) {
    throw new Error("manifest_invalid: invalid metadata");
  }

  const spec = raw["spec"];
  if (spec !== undefined && (!isRecord(spec) ||
    Object.keys(spec).some((key) => !["attributes", "setupDir"].includes(key)) ||
    (spec["setupDir"] !== undefined && typeof spec["setupDir"] !== "string") ||
    (spec["attributes"] !== undefined && (!isRecord(spec["attributes"]) ||
      Object.values(spec["attributes"]).some((value) => typeof value !== "string"))))) {
    throw new Error("manifest_invalid: invalid spec");
  }

  return raw as unknown as LabManifest;
}

function validateLabBundle(raw: unknown, catalogRef: string, labPath: string): LabBundle {
  if (!isRecord(raw) || raw["apiVersion"] !== "v1alpha1" || raw["kind"] !== "LabBundle" ||
    Object.keys(raw).some((key) => !["apiVersion", "kind", "manifest", "modules", "runtimeContext"].includes(key))) {
    throw new Error("bundle_invalid: invalid envelope");
  }

  const manifest = validateManifest(raw["manifest"]);
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

  if (!Array.isArray(raw["modules"]) || raw["modules"].length === 0) {
    throw new Error("bundle_invalid: no modules");
  }
  const moduleIds = new Set<string>();
  const pagePaths = new Set<string>();
  const modules: LabModule[] = raw["modules"].map((rawModule: unknown) => {
    if (!isRecord(rawModule) || typeof rawModule["id"] !== "string" ||
      !/^[A-Za-z0-9][A-Za-z0-9._-]*$/.test(rawModule["id"]) ||
      moduleIds.has(rawModule["id"]) || !Array.isArray(rawModule["pages"]) || rawModule["pages"].length === 0 ||
      Object.keys(rawModule).some((key) => !["id", "pages"].includes(key))) {
      throw new Error("bundle_invalid: invalid module");
    }
    const id = rawModule["id"];
    moduleIds.add(id);
    const prefix = `modules/${id}/content/`;
    const pages: LabPage[] = rawModule["pages"].map((rawPage: unknown) => {
      if (!isRecord(rawPage) || !isNormalizedRelativePath(rawPage["path"]) ||
        !rawPage["path"].startsWith(prefix) || !rawPage["path"].endsWith(".adoc") ||
        rawPage["path"].slice(prefix.length).split("/").some((segment) => segment.startsWith("_")) ||
        pagePaths.has(rawPage["path"]) ||
        Object.keys(rawPage).some((key) => !["path", "url"].includes(key))) {
        throw new Error("bundle_invalid: invalid page descriptor");
      }
      pagePaths.add(rawPage["path"]);
      return { path: rawPage["path"], url: resolveProxyURL(rawPage["url"], catalogRef, labPath, rawPage["path"]) };
    });
    return { id, pages };
  });

  return {
    apiVersion: "v1alpha1", kind: "LabBundle", manifest, modules,
    runtimeContext: {
      catalogRef: runtimeContext["catalogRef"],
      branch: runtimeContext["branch"],
      helmChartAvailable: runtimeContext["helmChartAvailable"],
    },
  };
}
