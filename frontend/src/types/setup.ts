export interface SetupResult {
  apiVersion: "v1alpha1";
  kind: "SetupResult";
  outcome: "success" | "failure" | "timeout";
  message: string;
  helmReleaseName: string;
}
