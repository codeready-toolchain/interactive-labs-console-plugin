export interface LabManifest {
  apiVersion: "v1alpha1";
  kind: "Lab";
  metadata: {
    title: string;
    description?: string;
  };
  spec?: {
    attributes?: Record<string, string>;
    setupDir?: string;
  };
}

export interface RuntimeContext {
  catalogRef: string;
  branch: string;
  helmChartAvailable: boolean;
}

export interface LabPage {
  path: string;
  url: string;
}

export interface LabModule {
  id: string;
  pages: LabPage[];
}

export interface LabBundle {
  apiVersion: "v1alpha1";
  kind: "LabBundle";
  manifest: LabManifest;
  /** Backend-ordered modules and lab-relative pages with proxy-resolved URLs. */
  modules: LabModule[];
  runtimeContext: RuntimeContext;
}

/** GuidanceLink: stub for STORY-5. */
export interface GuidanceLink {
  route: string;
  targetId?: string;
}

/** ManifestAck: reserved for future version acknowledgement flow. */
export type ManifestAck = { acknowledged: true };
