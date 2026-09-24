// Phase A — TypeScript type shape assertions.
// These tests fail until the type files exist and are correct.

import type { CatalogEntry, CatalogList } from "../catalog";
import type { LabManifest, LabBundle, RuntimeContext } from "../manifest";
import type { SetupResult, SetupStatus } from "../setup";
import type { PersistedSession } from "../session";
import type { LabError, LabErrorType } from "../errors";

describe("CatalogEntry shape", () => {
  it("requires catalogRef and title; description is optional", () => {
    const e: CatalogEntry = { catalogRef: "x", title: "X", labPath: "lab" };
    expect(e.catalogRef).toBe("x");
    const withDesc: CatalogEntry = { catalogRef: "y", title: "Y", description: "d", labPath: "lab" };
    expect(withDesc.description).toBe("d");
  });
});

describe("CatalogList shape", () => {
  it("has correct apiVersion and kind literals", () => {
    const entry: CatalogEntry = { catalogRef: "a", title: "A", labPath: "lab" };
    const list: CatalogList = { apiVersion: "v1alpha1", kind: "CatalogList", items: [entry] };
    expect(list.kind).toBe("CatalogList");
    expect(list.apiVersion).toBe("v1alpha1");
  });
});

describe("LabManifest shape", () => {
  it("requires apiVersion v1alpha1 and kind Lab", () => {
    const m: LabManifest = {
      apiVersion: "v1alpha1",
      kind: "Lab",
      metadata: { title: "Lab 1" },
      spec: {},
    };
    expect(m.kind).toBe("Lab");
  });

  it("supports optional description and runtime attributes", () => {
    const m: LabManifest = {
      apiVersion: "v1alpha1", kind: "Lab",
      metadata: { title: "Lab 1", description: "Intro" },
      spec: { attributes: { audience: "developer" } },
    };
    expect(m.spec?.attributes?.audience).toBe("developer");
  });
});

describe("LabBundle shape", () => {
  it("has manifest, grouped page descriptors, and runtimeContext", () => {
    const ctx: RuntimeContext = { catalogRef: "l1", branch: "main", helmChartAvailable: false };
    const manifest: LabManifest = {
      apiVersion: "v1alpha1",
      kind: "Lab",
      metadata: { title: "L" },
      spec: {},
    };
    const bundle: LabBundle = {
      apiVersion: "v1alpha1",
      kind: "LabBundle",
      manifest,
      modules: [{ id: "010-intro", pages: [{ path: "modules/010-intro/content/010-intro.adoc", url: "/api/proxy/plugin/interactive-labs-console-plugin/backend/api/v1alpha1/labs/l1/resources/lab/modules/010-intro/content/010-intro.adoc" }] }],
      runtimeContext: ctx,
    };
    expect(bundle.kind).toBe("LabBundle");
  });
});

describe("SetupResult shape", () => {
  it("has spaceRequests and helm status", () => {
    const r: SetupResult = {
      apiVersion: "v1alpha1",
      kind: "SetupStatus",
      spaceRequests: { status: "provisioning", namespaces: null },
      helm: { status: "pending", releaseName: "il-abc123" },
    };
    expect(r.helm.status).toBe("pending");
  });
});

describe("SetupStatus shape", () => {
  it("has spaceRequests and helm status", () => {
    const s: SetupStatus = {
      apiVersion: "v1alpha1",
      kind: "SetupStatus",
      spaceRequests: {
        status: "ready",
        namespaces: { gitea: { status: "ready", name: "user1-abc" } },
      },
      helm: { status: "deployed", releaseName: "il-abc123" },
    };
    expect(s.spaceRequests.status).toBe("ready");
    expect(s.spaceRequests.namespaces?.gitea.name).toBe("user1-abc");
    expect(s.helm.status).toBe("deployed");
  });

  it("supports optional attributes map", () => {
    const s: SetupStatus = {
      apiVersion: "v1alpha1",
      kind: "SetupStatus",
      spaceRequests: {
        status: "ready",
        namespaces: { gitea: { status: "ready", name: "ns-abc" } },
      },
      helm: { status: "deployed", releaseName: "il-abc123" },
      attributes: { "gitea-namespace": "ns-abc", postgres: "secret" },
    };
    expect(s.attributes?.["gitea-namespace"]).toBe("ns-abc");
    expect(s.attributes?.postgres).toBe("secret");
  });

  it("attributes is optional", () => {
    const s: SetupStatus = {
      apiVersion: "v1alpha1",
      kind: "SetupStatus",
      spaceRequests: { status: "provisioning", namespaces: null },
      helm: { status: "pending", releaseName: "il-abc123" },
    };
    expect(s.attributes).toBeUndefined();
  });
});

type IsExact<Left, Right> = (
  <Value>() => Value extends Left ? 1 : 2
) extends (
  <Value>() => Value extends Right ? 1 : 2
)
  ? true
  : false;

type PersistedSessionContract = {
  currentPagePath: string;
  panelMode: "open" | "minimized";
};

describe("PersistedSession shape", () => {
  it("contains only current page path and panel mode", () => {
    const s: PersistedSession = {
      currentPagePath: "010-intro.adoc",
      panelMode: "open",
    };
    const exactContract: IsExact<PersistedSession, PersistedSessionContract> = true;
    expect(exactContract).toBe(true);
    expect(s.panelMode).toBe("open");
  });
});

describe("LabError shape", () => {
  it("has kind Error and a known LabErrorType", () => {
    const e: LabError = {
      apiVersion: "v1alpha1",
      kind: "Error",
      type: "lab_not_found",
      message: "not found",
    };
    expect(e.kind).toBe("Error");
  });

  it("accepts unknown_error as fallback type", () => {
    const t: LabErrorType = "unknown_error";
    expect(t).toBe("unknown_error");
  });
});
