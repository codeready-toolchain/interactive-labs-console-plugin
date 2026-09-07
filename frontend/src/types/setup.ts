export interface SetupResult {
  apiVersion: "v1alpha1";
  kind: "SetupResult";
  outcome: "accepted" | "success" | "failure" | "timeout";
  message: string;
}

export interface NamespaceStatus {
  status: "pending" | "ready" | "failed";
  name: string;
}

export interface SetupStatus {
  apiVersion: "v1alpha1";
  kind: "SetupStatus";
  status: "provisioning" | "ready" | "failed";
  namespaces: Record<string, NamespaceStatus>;
}
