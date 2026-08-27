import { consoleFetch } from "@openshift-console/dynamic-plugin-sdk";
import type { CatalogList } from "../types/catalog";
import type { LabBundle, LabManifest } from "../types/manifest";
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
  return parseResponse<CatalogList>(res);
}

export async function loadLabBundle(catalogRef: string): Promise<LabBundle> {
  const res = await consoleFetch(`${PROXY_BASE}/api/v1alpha1/labs/${catalogRef}/bundle`);
  return parseResponse<LabBundle>(res);
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

// --- Manifest validation ---

export function validateManifest(raw: unknown): LabManifest {
  if (raw === null || typeof raw !== "object") {
    throw new Error("manifest_invalid: not an object");
  }
  const m = raw as Record<string, unknown>;
  if (m["apiVersion"] !== "v1alpha1") {
    throw new Error(`manifest_version_unsupported: got ${String(m["apiVersion"])}`);
  }
  const meta = m["metadata"] as Record<string, unknown> | undefined;
  if (!meta || typeof meta["name"] !== "string" || !meta["name"]) {
    throw new Error("manifest_invalid: missing metadata.name");
  }
  if (typeof meta["displayName"] !== "string" || !meta["displayName"]) {
    throw new Error("manifest_invalid: missing metadata.displayName");
  }
  return raw as LabManifest;
}
