export interface LabManifest {
  apiVersion: "v1alpha1";
  kind: "Lab";
  metadata: {
    name: string;
    displayName: string;
    description?: string;
  };
  spec: {
    attributes?: Record<string, string>;
    checkpoints?: Array<{
      id: string;
      type: "manual";
      pagePath: string;
      label: string;
    }>;
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

export interface LabBundle {
  apiVersion: "v1alpha1";
  kind: "LabBundle";
  manifest: LabManifest;
  /** Sorted content-relative paths with proxy-resolved source URLs. */
  pages: LabPage[];
  runtimeContext: RuntimeContext;
}

/** GuidanceLink: stub for STORY-5. */
export interface GuidanceLink {
  route: string;
  targetId?: string;
}

/** ManifestAck: reserved for future version acknowledgement flow. */
export type ManifestAck = { acknowledged: true };
