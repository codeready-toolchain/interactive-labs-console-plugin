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
  generatedValues?: Record<string, string>;
}

export interface SetupStatus {
  apiVersion: "v1alpha1";
  kind: "SetupStatus";
  spaceRequests: SpaceRequestsStatus;
  helm: HelmStatus;
  attributes?: Record<string, string>;
}
