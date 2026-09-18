// Phase A — TypeScript type shape assertions.
// These tests fail until the type files exist and are correct.

import type { CatalogEntry, CatalogList } from "../catalog";
import type { LabManifest, LabBundle, RuntimeContext } from "../manifest";
import type { SetupResult, SetupStatus } from "../setup";
import type { PersistedSession } from "../session";
import type { LabError, LabErrorType } from "../errors";

describe("CatalogEntry shape", () => {
  it("requires catalogRef and displayName; description is optional", () => {
    const e: CatalogEntry = { catalogRef: "x", displayName: "X" };
    expect(e.catalogRef).toBe("x");
    const withDesc: CatalogEntry = { catalogRef: "y", displayName: "Y", description: "d" };
    expect(withDesc.description).toBe("d");
  });
});

describe("CatalogList shape", () => {
  it("has correct apiVersion and kind literals", () => {
    const entry: CatalogEntry = { catalogRef: "a", displayName: "A" };
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
      metadata: { name: "lab1", displayName: "Lab 1" },
      spec: {},
    };
    expect(m.kind).toBe("Lab");
  });

  it("supports optional checkpoints of type manual only", () => {
    const m: LabManifest = {
      apiVersion: "v1alpha1",
      kind: "Lab",
      metadata: { name: "lab1", displayName: "Lab 1" },
      spec: {
        checkpoints: [
          { id: "cp1", type: "manual", pagePath: "010-intro.adoc", label: "Done?" },
        ],
      },
    };
    expect(m.spec.checkpoints?.[0].type).toBe("manual");
  });
});

describe("LabBundle shape", () => {
  it("has manifest, pages map, and runtimeContext", () => {
    const ctx: RuntimeContext = { catalogRef: "l1", branch: "main", helmChartAvailable: false };
    const manifest: LabManifest = {
      apiVersion: "v1alpha1",
      kind: "Lab",
      metadata: { name: "l1", displayName: "L" },
      spec: {},
    };
    const bundle: LabBundle = {
      apiVersion: "v1alpha1",
      kind: "LabBundle",
      manifest,
      pages: { "010-intro.adoc": "= Intro\n\nHello." },
      runtimeContext: ctx,
    };
    expect(bundle.kind).toBe("LabBundle");
  });
});

describe("SetupResult shape", () => {
  it("has outcome union", () => {
    const r: SetupResult = {
      apiVersion: "v1alpha1",
      kind: "SetupResult",
      outcome: "accepted",
      message: "Lab setup initiated",
    };
    expect(r.outcome).toBe("accepted");
  });
});

describe("SetupStatus shape", () => {
  it("has status and namespaces", () => {
    const s: SetupStatus = {
      apiVersion: "v1alpha1",
      kind: "SetupStatus",
      status: "ready",
      namespaces: { gitea: { status: "ready", name: "user1-abc" } },
    };
    expect(s.status).toBe("ready");
    expect(s.namespaces.gitea.name).toBe("user1-abc");
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
