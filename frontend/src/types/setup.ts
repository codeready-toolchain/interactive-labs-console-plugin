export const blockedReasons = ['cleanup', 'operation', 'helm-state', 'helm-hook', 'helm-retained', 'helm-manifest', 'pending', 'unknown'] as const;
export type BlockedReason = typeof blockedReasons[number];

export interface LabSetup {
  catalogRef: string;
  generation: string;
  status: "pending" | "deployed" | "failed";
  spaceRequests: string[];
  release?: { name: string };
  blocked?: boolean;
  blockedReason?: BlockedReason;
}

export interface TeardownProgress {
  apiVersion: "v1alpha1";
  kind: "TeardownStatus";
  catalogRef: string;
  operationId: string;
  status: "running" | "succeeded" | "failed" | "unknown";
}

export interface SetupList {
  apiVersion: "v1alpha1";
  kind: "SetupList";
  items: LabSetup[];
}

export interface SetupResult {
  apiVersion: "v1alpha1";
  kind: "SetupStatus";
  spaceRequests: SpaceRequestsStatus;
  helm: HelmStatus;
}

export interface NamespaceStatus {
  status: "pending" | "ready" | "failed";
  name: string;
}

export interface SpaceRequestsStatus {
  status: "provisioning" | "ready" | "failed";
  namespaces: Record<string, NamespaceStatus> | null;
}

export interface HelmStatus {
  status: "pending" | "installing" | "deployed" | "failed";
  releaseName: string;
  message?: string;
}

export interface SetupStatus {
  apiVersion: "v1alpha1";
  kind: "SetupStatus";
  spaceRequests: SpaceRequestsStatus;
  helm: HelmStatus;
  retrySetup?: boolean;
  attributes?: Record<string, string>;
}
