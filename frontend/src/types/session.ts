export interface PersistedSession {
  catalogRef: string;
  branch: string;
  currentPagePath: string | null;
  panelMode: "open" | "minimized";
  namespaceBindings: Record<string, string>;
  /** pagePath → user-confirmed */
  checkpoints: Record<string, boolean>;
}
