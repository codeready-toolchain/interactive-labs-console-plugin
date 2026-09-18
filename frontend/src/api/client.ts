import { consoleFetch } from "@openshift-console/dynamic-plugin-sdk";
import type { CatalogList } from "../types/catalog";
import type { LabBundle, LabManifest } from "../types/manifest";
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
  return parseResponse<CatalogList>(res);
}

export async function loadLabBundle(catalogRef: string): Promise<LabBundle> {
  const res = await consoleFetch(`${PROXY_BASE}/api/v1alpha1/labs/${catalogRef}/bundle`);
  return validateLabBundle(await parseResponse<unknown>(res), catalogRef);
}

export async function setupLab(catalogRef: string): Promise<SetupResult> {
  const res = await consoleFetch(`${PROXY_BASE}/api/v1alpha1/labs/${catalogRef}/setup`, {
    method: "POST",
  });
  return parseResponse<SetupResult>(res);
}

export async function getSetupStatus(catalogRef: string): Promise<SetupStatus> {
  const res = await consoleFetch(`${PROXY_BASE}/api/v1alpha1/labs/${catalogRef}/setup/status`);
  return parseResponse<SetupStatus>(res);
}

// --- Bundle validation ---

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

export function getNavigablePagePaths(pages: Record<string, string>): string[] {
  return Object.keys(pages)
    .filter((pagePath) => pagePath.split("/").every((segment) => !segment.startsWith("_")))
    .sort();
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

  const pages = raw["pages"];
  if (!isRecord(pages) || Object.values(pages).some((source) => typeof source !== "string")) {
    throw new Error("bundle_invalid: invalid pages");
  }
  if (getNavigablePagePaths(pages as Record<string, string>).length === 0) {
    throw new Error("bundle_invalid: no navigable pages");
  }

  return raw as unknown as LabBundle;
}
