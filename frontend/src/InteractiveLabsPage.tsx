import * as React from "react";
import {
  Alert,
  Button,
  DataList,
  DataListAction,
  DataListCell,
  DataListContent,
  DataListItem,
  DataListItemCells,
  DataListItemRow,
  DataListToggle,
  Drawer,
  DrawerActions,
  DrawerContent,
  DrawerHead,
  DrawerPanelBody,
  DrawerPanelContent,
  EmptyState,
  EmptyStateBody,
  PageSection,
  Spinner,
  TextInput,
  Title,
} from "@patternfly/react-core";
import { getNavigablePagePaths, getSetupStatus, listCatalog, loadLabBundle, setupLab } from "./api/client";
import type { CatalogEntry } from "./types/catalog";
import type { LabError } from "./types/errors";
import type { PersistedSession } from "./types/session";

type PageState =
  | { status: "loading" }
  | { status: "success"; items: CatalogEntry[] }
  | { status: "error"; message: string };

type ActivePanel = PersistedSession & { catalogRef: string; displayName: string };

type InteractiveLabsContextValue = {
  launch: (catalogRef: string, trigger: HTMLButtonElement) => Promise<void>;
  loadingCatalogRef?: string;
  launchError?: string;
  settingUp?: string;
};

const SESSION_PREFIX = "interactive-labs-console-plugin:session:";
const InteractiveLabsContext = React.createContext<InteractiveLabsContextValue | undefined>(undefined);

const sessionKey = (catalogRef: string) => `${SESSION_PREFIX}${catalogRef}`;

const getStorage = (): Storage | undefined => {
  try {
    return window.localStorage;
  } catch {
    return undefined;
  }
};

const isPersistedSession = (value: unknown): value is PersistedSession => {
  if (value === null || typeof value !== "object" || Array.isArray(value)) return false;
  const session = value as Record<string, unknown>;
  return (
    Object.keys(session).length === 2 &&
    typeof session.currentPagePath === "string" &&
    (session.panelMode === "open" || session.panelMode === "minimized")
  );
};

const readSession = (catalogRef: string, pagePaths: string[]): PersistedSession | undefined => {
  const storage = getStorage();
  if (!storage) return undefined;

  try {
    const key = sessionKey(catalogRef);
    const raw = storage.getItem(key);
    if (!raw) return undefined;
    let session: unknown;
    try {
      session = JSON.parse(raw);
    } catch {
      storage.removeItem(key);
      return undefined;
    }
    if (!isPersistedSession(session) || !pagePaths.includes(session.currentPagePath)) {
      storage.removeItem(key);
      return undefined;
    }
    return session;
  } catch {
    return undefined;
  }
};

const saveSession = (panel: ActivePanel): void => {
  try {
    getStorage()?.setItem(
      sessionKey(panel.catalogRef),
      JSON.stringify({ currentPagePath: panel.currentPagePath, panelMode: panel.panelMode }),
    );
  } catch {
    // Browser storage is optional.
  }
};

const resetSessions = (): void => {
  const storage = getStorage();
  if (!storage) return;

  try {
    for (let index = storage.length - 1; index >= 0; index -= 1) {
      const key = storage.key(index);
      if (key?.startsWith(SESSION_PREFIX)) storage.removeItem(key);
    }
  } catch {
    // Browser storage is optional.
  }
};

const SETUP_POLL_INTERVAL_MS = 3000;

const pollSetupUntilReady = async (catalogRef: string, signal: AbortSignal): Promise<void> => {
  while (!signal.aborted) {
    const status = await getSetupStatus(catalogRef);
    if (status.status === "ready") return;
    if (status.status === "failed") throw new Error("Lab setup failed.");
    await new Promise<void>((resolve, reject) => {
      const timer = setTimeout(resolve, SETUP_POLL_INTERVAL_MS);
      signal.addEventListener("abort", () => {
        clearTimeout(timer);
        reject(new DOMException("Aborted", "AbortError"));
      }, { once: true });
    });
  }
  throw new DOMException("Aborted", "AbortError");
};

const InteractiveLabsProvider: React.FC = ({ children }) => {
  const [activePanel, setActivePanel] = React.useState<ActivePanel | null>(null);
  const [loadingCatalogRef, setLoadingCatalogRef] = React.useState<string>();
  const [launchError, setLaunchError] = React.useState<string>();
  const [settingUp, setSettingUp] = React.useState<string>();
  const launchTriggerRef = React.useRef<HTMLButtonElement | null>(null);
  const drawerFocusRef = React.useRef<HTMLSpanElement | null>(null);
  const restoreFocusRef = React.useRef<HTMLButtonElement | null>(null);
  const setupAbortRef = React.useRef<AbortController | null>(null);
  const isOpen = activePanel?.panelMode === "open";

  React.useEffect(() => {
    if (isOpen) drawerFocusRef.current?.focus();
    else if (activePanel?.panelMode === "minimized") restoreFocusRef.current?.focus();
    else if (launchTriggerRef.current?.isConnected) launchTriggerRef.current.focus();
  }, [activePanel, isOpen]);

  React.useEffect(() => () => { setupAbortRef.current?.abort(); }, []);

  const launch = React.useCallback(async (catalogRef: string, trigger: HTMLButtonElement): Promise<void> => {
    launchTriggerRef.current = trigger;
    setupAbortRef.current?.abort();
    setActivePanel(null);
    setLoadingCatalogRef(catalogRef);
    setLaunchError(undefined);
    setSettingUp(undefined);
    try {
      const bundle = await loadLabBundle(catalogRef);

      if (bundle.runtimeContext.helmChartAvailable) {
        setSettingUp(catalogRef);
        const abortController = new AbortController();
        setupAbortRef.current = abortController;
        await setupLab(catalogRef);
        await pollSetupUntilReady(catalogRef, abortController.signal);
        setSettingUp(undefined);
      }

      const pagePaths = getNavigablePagePaths(bundle.pages);
      const entryPage = pagePaths[0];
      if (!entryPage) throw new Error("Lab has no navigable pages.");
      const saved = readSession(catalogRef, pagePaths);
      const panel: ActivePanel = {
        catalogRef,
        displayName: bundle.manifest.metadata.displayName,
        currentPagePath: saved?.currentPagePath ?? entryPage,
        panelMode: saved?.panelMode ?? "open",
      };
      setActivePanel(panel);
      saveSession(panel);
    } catch (err: unknown) {
      if (err instanceof DOMException && err.name === "AbortError") return;
      setLaunchError((err as LabError | undefined)?.message ?? "Unable to load lab.");
    } finally {
      setLoadingCatalogRef(undefined);
      setSettingUp(undefined);
    }
  }, []);

  const setPanelMode = React.useCallback((panelMode: PersistedSession["panelMode"]): void => {
    setActivePanel((current) => {
      if (!current) return current;
      const panel = { ...current, panelMode };
      saveSession(panel);
      return panel;
    });
  }, []);

  const closePanel = React.useCallback(() => setActivePanel(null), []);

  const reset = React.useCallback((): void => {
    resetSessions();
    setActivePanel(null);
  }, []);

  const value = React.useMemo(
    () => ({ launch, loadingCatalogRef, launchError, settingUp }),
    [launch, launchError, loadingCatalogRef, settingUp],
  );

  return (
    <InteractiveLabsContext.Provider value={value}>
      {children}
      {activePanel && (
        <Drawer
          isExpanded={isOpen}
          position="end"
          style={{ position: "fixed", inset: 0, zIndex: 1000, pointerEvents: "none" }}
        >
          <DrawerContent
            panelContent={
              <DrawerPanelContent style={{ pointerEvents: "auto" }}>
                <DrawerHead>
                  <span ref={drawerFocusRef} tabIndex={-1}>
                    {activePanel.displayName}
                  </span>
                  <DrawerActions>
                    <Button
                      variant="plain"
                      onClick={() => setPanelMode("minimized")}
                      aria-label={isOpen ? "Minimize lab" : "Lab panel minimized"}
                    >
                      Minimize
                    </Button>
                    <Button
                      variant="plain"
                      onClick={closePanel}
                      aria-label={isOpen ? "Close lab" : "Lab panel closed"}
                    >
                      Close
                    </Button>
                  </DrawerActions>
                </DrawerHead>
                {isOpen && (
                  <DrawerPanelBody>
                    <p>{activePanel.currentPagePath}</p>
                    <Button variant="link" onClick={reset}>
                      Reset lab
                    </Button>
                  </DrawerPanelBody>
                )}
              </DrawerPanelContent>
            }
          >
            <div />
          </DrawerContent>
        </Drawer>
      )}
      {activePanel?.panelMode === "minimized" && (
        <Button
          ref={restoreFocusRef}
          onClick={() => setPanelMode("open")}
          aria-label="Restore lab"
          style={{
            position: "fixed",
            insetInlineEnd: 0,
            top: "50%",
            zIndex: 1000,
            width: "1.5rem",
            minWidth: 0,
            height: "5rem",
            padding: 0,
            transform: "translateY(-50%)",
          }}
        >
          <svg aria-hidden="true" fill="currentColor" viewBox="0 0 384 512" width="1em" height="1em">
            <path d="M336 0H48C21.49 0 0 21.49 0 48v464l192-112 192 112V48c0-26.51-21.49-48-48-48zm0 428.43l-144-84-144 84V54a6 6 0 0 1 6-6h276c3.314 0 6 2.683 6 5.996V428.43z" />
          </svg>
        </Button>
      )}
    </InteractiveLabsContext.Provider>
  );
};

const useInteractiveLabs = (): InteractiveLabsContextValue | undefined =>
  React.useContext(InteractiveLabsContext);

const InteractiveLabsPage: React.FC = () => {
  const [state, setState] = React.useState<PageState>({ status: "loading" });
  const [retry, setRetry] = React.useState(0);
  const [filter, setFilter] = React.useState("");
  const [expandedItems, setExpandedItems] = React.useState<Set<string>>(() => new Set());
  const interactiveLabs = useInteractiveLabs();
  const filteredItems =
    state.status === "success"
      ? state.items.filter((item) =>
          `${item.displayName} ${item.description ?? ""}`
            .toLowerCase()
            .includes(filter.toLowerCase()),
        )
      : [];

  React.useEffect(() => {
    let cancelled = false;
    setState({ status: "loading" });
    void listCatalog().then(
      (result) => {
        if (!cancelled) setState({ status: "success", items: result.items });
      },
      (err: unknown) => {
        if (!cancelled)
          setState({
            status: "error",
            message: (err as LabError | undefined)?.message ?? "Unknown error",
          });
      },
    );
    return () => {
      cancelled = true;
    };
  }, [retry]);

  return (
    <PageSection>
      <Title headingLevel="h1">Interactive Labs</Title>

      {state.status === "loading" && <Spinner aria-label="Loading labs" />}

      {state.status === "error" && (
        <>
          <Alert variant="danger" title={state.message} isInline isLiveRegion />
          <Button variant="link" onClick={() => setRetry((count) => count + 1)}>
            Retry
          </Button>
        </>
      )}

      {interactiveLabs?.launchError && (
        <Alert variant="danger" title={interactiveLabs.launchError} isInline isLiveRegion />
      )}
      {interactiveLabs?.settingUp && (
        <Alert variant="info" title="Setting up your lab environment..." isInline isLiveRegion>
          <Spinner size="md" aria-label="Setting up lab" />
        </Alert>
      )}

      {state.status === "success" && state.items.length === 0 && (
        <EmptyState titleText="No labs available">
          <EmptyStateBody>No labs are currently configured.</EmptyStateBody>
        </EmptyState>
      )}

      {state.status === "success" && state.items.length > 0 && (
        <>
          <div style={{ marginBottom: "var(--pf-t--global--spacer--md)" }}>
            <TextInput
              aria-label="Filter labs"
              type="search"
              value={filter}
              onChange={(_event, value) => setFilter(value)}
              placeholder="Filter by name or description"
            />
          </div>
          {filteredItems.length === 0 ? (
            <EmptyState titleText="No labs match your filter.">
              <EmptyStateBody>Try a different name or description.</EmptyStateBody>
            </EmptyState>
          ) : (
            <DataList aria-label="Available labs" isCompact isPlain>
              {filteredItems.map((item) => {
                const itemId = `lab-${item.catalogRef}`;
                const descriptionId = `${itemId}-description`;
                const isExpanded = expandedItems.has(item.catalogRef);
                return (
                  <DataListItem key={item.catalogRef} aria-labelledby={itemId} isExpanded={isExpanded}>
                    <DataListItemRow>
                      {item.description && (
                        <DataListToggle
                          id={`${itemId}-toggle`}
                          aria-controls={descriptionId}
                          aria-label={`Toggle description for ${item.displayName}`}
                          isExpanded={isExpanded}
                          buttonProps={{
                            onClick: () =>
                              setExpandedItems((current) => {
                                const next = new Set(current);
                                if (next.has(item.catalogRef)) next.delete(item.catalogRef);
                                else next.add(item.catalogRef);
                                return next;
                              }),
                          }}
                        />
                      )}
                      <DataListItemCells
                        dataListCells={[
                          <DataListCell key="name" width={5}>
                            <strong id={itemId}>{item.displayName}</strong>
                          </DataListCell>,
                        ]}
                      />
                      <DataListAction
                        id={`${itemId}-actions`}
                        aria-labelledby={itemId}
                        aria-label={`Actions for ${item.displayName}`}
                      >
                        <Button
                          isLoading={interactiveLabs?.loadingCatalogRef === item.catalogRef}
                          isDisabled={!interactiveLabs}
                          onClick={(event) => void interactiveLabs?.launch(item.catalogRef, event.currentTarget)}
                        >
                          Launch
                        </Button>
                      </DataListAction>
                    </DataListItemRow>
                    {item.description && (
                      <DataListContent
                        id={descriptionId}
                        aria-label={`Description for ${item.displayName}`}
                        isHidden={!isExpanded}
                      >
                        {item.description}
                      </DataListContent>
                    )}
                  </DataListItem>
                );
              })}
            </DataList>
          )}
        </>
      )}
    </PageSection>
  );
};

export { InteractiveLabsPage, InteractiveLabsProvider, useInteractiveLabs };
