import * as React from "react";
import { useLocation, useNavigate } from "react-router-dom-v5-compat";
import "./interactive-labs.css";
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
import {
  fetchResourceText,
  getNavigablePagePaths,
  getSetupStatus,
  listCatalog,
  loadLabBundle,
  setupLab,
} from "./api/client";
import { renderAsciiDoc, type RenderResult } from "./renderer";
import { TimesIcon, WindowMinimizeIcon } from "@patternfly/react-icons";
import type { CatalogEntry } from "./types/catalog";
import type { LabError } from "./types/errors";
import type { PersistedSession } from "./types/session";
import type { LabPage } from "./types/manifest";
import type { SetupStatus } from "./types/setup";
import { sanitizeTitle } from "./sanitizer";

type PageState =
  | { status: "loading" }
  | { status: "success"; items: CatalogEntry[] }
  | { status: "error"; message: string };

type ActivePanel = PersistedSession & {
  catalogRef: string;
  displayName: string;
  pages: LabPage[];
  attributes: Record<string, string>;
};

type RenderState =
  | { status: "loading" }
  | { status: "success"; result: RenderResult }
  | { status: "error"; message: string };

type SetupState =
  | { status: "pending" }
  | { status: "error"; message: string };

type GuidanceRequest = {
  animationKey: number;
  controller: AbortController;
  id: string;
  expectedRoute: { pathname: string; search: string };
  startingRoute: { pathname: string; search: string };
  phase: "waiting-route" | "waiting-target" | "spotlight";
  target?: HTMLElement;
};

type GuidanceState = GuidanceRequest | { phase: "notice"; message: string };

type InteractiveLabsContextValue = {
  launch: (catalogRef: string, trigger: HTMLElement, requestedPageURL?: string) => Promise<void>;
  loadingCatalogRef?: string;
  launchError?: string;
};

const SESSION_PREFIX = "interactive-labs-console-plugin:session:";
const PROXY_BASE = "/api/proxy/plugin/interactive-labs-console-plugin/backend";
const LINK_WARNING = "Link is unavailable or unsafe.";
const GUIDANCE_UNAVAILABLE = "Console target is unavailable.";
const GUIDANCE_HIGHLIGHTED = "Console target highlighted.";
const consoleGuidanceRegistry: Record<string, { route?: string }> = {
  "qs-nav-add": { route: "/add" },
  "qs-nav-topology": { route: "/topology" },
  "qs-admin-nav-topology": { route: "/topology" },
  "qs-nav-search": { route: "/search-page" },
  "qs-nav-project": { route: "/project-details" },
  "qs-nav-functions": { route: "/functions" },
  "qs-nav-helm": { route: "/helm" },
  "qs-nav-builds": { route: "/builds" },
  "qs-nav-monitoring": { route: "/dev-monitoring" },
  "qs-nav-home": {},
  "qs-nav-ecosystem": {},
  "qs-nav-workloads": {},
  "qs-nav-networking": {},
  "qs-nav-storage": {},
  "qs-nav-compute": {},
  "qs-nav-usermanagement": {},
  "qs-nav-administration": {},
  "qs-nav-serverless": {},
  "qs-perspective-switcher": {},
  "qs-masthead-cloudshell": {},
  "qs-masthead-import": {},
  "qs-masthead-utilitymenu": {},
  "qs-masthead-usermenu": {},
  "qs-masthead-notifications": {},
  "qs-masthead-help": {},
};

const routeMatches = (
  location: { pathname: string; search: string },
  expected: { pathname: string; search: string },
): boolean => location.pathname === expected.pathname && location.search === expected.search;

const visibleGuidanceTarget = (id: string): HTMLElement | undefined =>
  Array.from(document.querySelectorAll<HTMLElement>("[data-quickstart-id]")).find((target) => {
    const rect = target.getBoundingClientRect();
    const style = window.getComputedStyle(target);
    return target.dataset.quickstartId === id && target.isConnected && !target.hidden &&
      rect.width > 0 && rect.height > 0 && style.display !== "none" && style.visibility !== "hidden";
  });

const overlapsHorizontally = (first: DOMRect, second: DOMRect): boolean =>
  first.left < second.right && first.right > second.left;

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

const SETUP_POLL_INTERVAL_MS = 3000;

const pollSetupUntilReady = async (catalogRef: string, signal: AbortSignal): Promise<SetupStatus> => {
  while (!signal.aborted) {
    const status = await getSetupStatus(catalogRef);
    if (status.spaceRequests.status === "failed") {
      throw new Error("Namespace provisioning failed.");
    }
    if (status.helm.status === "failed") {
      throw new Error(status.helm.message || "Lab setup failed.");
    }
    if (status.spaceRequests.status === "ready" && status.helm.status === "deployed") {
      return status;
    }
    await new Promise<void>((resolve, reject) => {
      if (signal.aborted) {
        reject(new DOMException("Aborted", "AbortError"));
        return;
      }
      const onAbort = (): void => {
        window.clearTimeout(timer);
        signal.removeEventListener("abort", onAbort);
        reject(new DOMException("Aborted", "AbortError"));
      };
      const timer = window.setTimeout(() => {
        window.clearTimeout(timer);
        signal.removeEventListener("abort", onAbort);
        resolve();
      }, SETUP_POLL_INTERVAL_MS);
      signal.addEventListener("abort", onAbort, { once: true });
    });
  }
  throw new DOMException("Aborted", "AbortError");
};
const InteractiveLabsProvider: React.FC = ({ children }) => {
  const [activePanel, setActivePanel] = React.useState<ActivePanel | null>(null);
  const [loadingCatalogRef, setLoadingCatalogRef] = React.useState<string>();
  const [launchError, setLaunchError] = React.useState<string>();
  const [setupState, setSetupState] = React.useState<SetupState>();
  const [renderState, setRenderState] = React.useState<RenderState>({ status: "loading" });
  const [renderRetry, setRenderRetry] = React.useState(0);
  const launchTriggerRef = React.useRef<HTMLElement | null>(null);
  const drawerFocusRef = React.useRef<HTMLSpanElement | null>(null);
  const restoreFocusRef = React.useRef<HTMLButtonElement | null>(null);
  const renderedContentRef = React.useRef<HTMLDivElement | null>(null);
  const pendingFragmentRef = React.useRef<string>();
  const guidanceControllerRef = React.useRef<AbortController>();
  const guidanceAnimationKeyRef = React.useRef(0);
  const launchAbortRef = React.useRef<AbortController>();
  const mountedRef = React.useRef(true);
  const [guidance, setGuidance] = React.useState<GuidanceState>();
  const [navigationWarning, setNavigationWarning] = React.useState<string>();
  const isOpen = activePanel?.panelMode === "open";
  const navigate = useNavigate();
  const location = useLocation();
  const pagePaths = activePanel ? getNavigablePagePaths(activePanel.pages) : [];
  const currentPageIndex = activePanel ? pagePaths.indexOf(activePanel.currentPagePath) : -1;

  React.useEffect(() => {
    if (isOpen) drawerFocusRef.current?.focus();
    else if (activePanel?.panelMode === "minimized") restoreFocusRef.current?.focus();
    else if (launchTriggerRef.current?.isConnected) launchTriggerRef.current.focus();
  }, [activePanel, isOpen]);

  const launch = React.useCallback(async (
    catalogRef: string,
    trigger: HTMLElement,
    requestedPageURL?: string,
  ): Promise<void> => {
    launchTriggerRef.current = trigger;
    launchAbortRef.current?.abort();
    const controller = new AbortController();
    launchAbortRef.current = controller;
    const isCurrentLaunch = (): boolean => mountedRef.current && launchAbortRef.current === controller && !controller.signal.aborted;
    let openedPanel = false;
    setActivePanel(null);
    setLoadingCatalogRef(catalogRef);
    setLaunchError(undefined);
    setSetupState(undefined);
    try {
      const bundle = await loadLabBundle(catalogRef, controller.signal);
      if (!isCurrentLaunch()) return;

      const pagePaths = getNavigablePagePaths(bundle.pages);
      const entryPage = pagePaths[0];
      if (!entryPage) throw new Error("Lab has no navigable pages.");
      const saved = readSession(catalogRef, pagePaths);
      const requestedPage = requestedPageURL ? (() => {
        const target = new URL(requestedPageURL, window.location.origin);
        if (target.search) return undefined;
        return bundle.pages.find((page) => {
          const pageURL = new URL(page.url, window.location.origin);
          return pageURL.origin === target.origin && pageURL.pathname === target.pathname;
        });
      })() : undefined;
      if (requestedPageURL && !requestedPage) throw new Error("Lab page is unavailable.");
      pendingFragmentRef.current = requestedPageURL
        ? new URL(requestedPageURL, window.location.origin).hash.slice(1) || undefined
        : undefined;
      const needsSetup = bundle.runtimeContext.helmChartAvailable;
      const panel: ActivePanel = {
        catalogRef,
        displayName: bundle.manifest.metadata.displayName,
        currentPagePath: requestedPage?.path ?? saved?.currentPagePath ?? entryPage,
        panelMode: needsSetup ? "open" : saved?.panelMode ?? "open",
        pages: bundle.pages,
        attributes: bundle.manifest.spec.attributes ?? {},
      };
      openedPanel = needsSetup;
      if (needsSetup) setSetupState({ status: "pending" });
      setActivePanel(panel);
      saveSession(panel);

      if (needsSetup) {
        await setupLab(catalogRef, controller.signal);
        const finalStatus = await pollSetupUntilReady(catalogRef, controller.signal);
        if (!isCurrentLaunch()) return;
        if (finalStatus.attributes) {
          setActivePanel((current) => current ? {
            ...current,
            attributes: { ...current.attributes, ...finalStatus.attributes },
          } : current);
        }
        setSetupState(undefined);
      }
    } catch (err: unknown) {
      if (!isCurrentLaunch()) return;
      const message = (err as LabError | undefined)?.message ?? "Unable to load lab.";
      if (openedPanel) setSetupState({ status: "error", message });
      else setLaunchError(message);
    } finally {
      if (isCurrentLaunch()) setLoadingCatalogRef(undefined);
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

  const clearGuidance = React.useCallback((message?: string): void => {
    guidanceControllerRef.current?.abort();
    guidanceControllerRef.current = undefined;
    if (mountedRef.current) setGuidance(message ? { phase: "notice", message } : undefined);
  }, []);

  const unavailableGuidance = React.useCallback((controller: AbortController): void => {
    if (controller.signal.aborted) return;
    controller.abort();
    if (guidanceControllerRef.current === controller) guidanceControllerRef.current = undefined;
    if (!mountedRef.current) return;
    setGuidance((current) => current && "controller" in current && current.controller === controller
      ? { phase: "notice", message: GUIDANCE_UNAVAILABLE }
      : current);
  }, []);

  const closePanel = React.useCallback(() => {
    launchAbortRef.current?.abort();
    clearGuidance();
    setSetupState(undefined);
    setActivePanel(null);
  }, [clearGuidance]);

  const selectPage = React.useCallback((path: string): void => {
    setNavigationWarning(undefined);
    setActivePanel((current) => {
      if (!current || !current.pages.some((page) => page.path === path)) return current;
      const panel = { ...current, currentPagePath: path };
      saveSession(panel);
      return panel;
    });
  }, []);

  const spotlightGuidanceTarget = React.useCallback((request: GuidanceRequest, target: HTMLElement): void => {
    const panel = document.getElementById("interactive-labs-drawer-panel");
    const minimize = isOpen && panel && overlapsHorizontally(target.getBoundingClientRect(), panel.getBoundingClientRect());
    if (minimize) setPanelMode("minimized");
    const frame = window.requestAnimationFrame(() => {
      if (request.controller.signal.aborted || !visibleGuidanceTarget(request.id)?.isSameNode(target)) {
        unavailableGuidance(request.controller);
        return;
      }
      target.scrollIntoView?.({ block: "center", inline: "nearest" });
      if (target.matches("a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex='-1'])")) {
        target.focus({ preventScroll: true });
      }
      setGuidance((current) => current && "controller" in current && current.controller === request.controller
        ? { ...current, phase: "spotlight", target }
        : current);
    });
    request.controller.signal.addEventListener("abort", () => window.cancelAnimationFrame(frame), { once: true });
  }, [isOpen, setPanelMode, unavailableGuidance]);

  const activateGuidance = React.useCallback((id: string): void => {
    const entry = consoleGuidanceRegistry[id];
    if (!entry) return;
    clearGuidance();
    const controller = new AbortController();
    guidanceControllerRef.current = controller;
    const startingRoute = { pathname: location.pathname, search: location.search };
    const target = visibleGuidanceTarget(id);
    const expectedRoute = entry.route && !target
      ? { pathname: entry.route, search: "" }
      : startingRoute;
    const request: GuidanceRequest = {
      animationKey: ++guidanceAnimationKeyRef.current,
      controller,
      id,
      expectedRoute,
      startingRoute,
      phase: target ? "spotlight" : entry.route && !routeMatches(startingRoute, expectedRoute)
        ? "waiting-route"
        : "waiting-target",
      target,
    };
    setGuidance(request);
    if (target) {
      spotlightGuidanceTarget(request, target);
    } else if (entry.route) {
      navigate(entry.route);
    } else {
      unavailableGuidance(controller);
    }
  }, [clearGuidance, location.pathname, location.search, navigate, spotlightGuidanceTarget, unavailableGuidance]);

  const handleRenderedLink = React.useCallback((event: React.MouseEvent<HTMLDivElement> | MouseEvent): void => {
    if (event.defaultPrevented || event.button !== 0 || !activePanel) return;
    const link = (event.target as Element).closest("a[href]") as HTMLAnchorElement | null;
    const href = link?.getAttribute("href");
    const guidanceID = link?.dataset.interactiveLabsGuidanceId;
    if (guidanceID) {
      event.preventDefault();
      const entry = consoleGuidanceRegistry[guidanceID];
      const guidanceLink = renderState.status === "success"
        ? renderState.result.guidanceLinks?.find((candidate) =>
          candidate.id === guidanceID && candidate.href === href && candidate.token === link?.dataset.interactiveLabsGuidanceToken,
        )
        : undefined;
      if (!entry || !guidanceLink || href !== (entry.route ?? "#")) {
        setNavigationWarning(LINK_WARNING);
        return;
      }
      event.stopPropagation();
      activateGuidance(guidanceID);
      return;
    }
    const currentPage = activePanel.pages.find((page) => page.path === activePanel.currentPagePath);
    if (!href || !currentPage) return;

    let target: URL;
    try {
      target = new URL(href, new URL(currentPage.url, window.location.origin));
    } catch {
      event.preventDefault();
      setNavigationWarning(LINK_WARNING);
      return;
    }
    const directPluginURL = href.startsWith("api/v1alpha1/labs/") || (target.pathname.startsWith(PROXY_BASE) &&
      (href.startsWith("/") || href.startsWith("//") || /^[a-z][a-z0-9+.-]*:/i.test(href)));
    if (directPluginURL) {
      event.preventDefault();
      setNavigationWarning(LINK_WARNING);
      return;
    }
    if ((target.protocol === "http:" || target.protocol === "https:") && target.origin !== window.location.origin) {
      event.preventDefault();
      window.open(target.href, "_blank", "noopener,noreferrer");
      return;
    }

    const targetPage = !target.search && activePanel.pages.find((page) => {
      const pageURL = new URL(page.url, window.location.origin);
      return pageURL.origin === target.origin && pageURL.pathname === target.pathname;
    });
    if (targetPage) {
      if (targetPage.path === currentPage.path && href.startsWith("#")) return;
      event.preventDefault();
      pendingFragmentRef.current = target.hash ? decodeURIComponent(target.hash.slice(1)) : undefined;
      selectPage(targetPage.path);
      return;
    }
    if (!href.startsWith("/") && !href.startsWith("//") && !/^[a-z][a-z0-9+.-]*:/i.test(href)) {
      event.preventDefault();
      setNavigationWarning(LINK_WARNING);
      return;
    }
    if (target.origin === window.location.origin && !target.pathname.startsWith(PROXY_BASE)) {
      if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
      event.preventDefault();
      navigate(`${target.pathname}${target.search}${target.hash}`);
      return;
    }
    event.preventDefault();
    setNavigationWarning(LINK_WARNING);
  }, [activePanel, activateGuidance, navigate, renderState, selectPage]);

  React.useEffect(() => () => {
    mountedRef.current = false;
    launchAbortRef.current?.abort();
    guidanceControllerRef.current?.abort();
  }, []);

  React.useEffect(() => {
    if (!guidance || guidance.phase === "notice") return;
    if (guidance.phase === "waiting-route") {
      if (routeMatches(location, guidance.expectedRoute)) {
        setGuidance((current) => current && "controller" in current && current.controller === guidance.controller
          ? { ...current, phase: "waiting-target" }
          : current);
      } else if (!routeMatches(location, guidance.startingRoute)) {
        unavailableGuidance(guidance.controller);
      }
      return;
    }
    if (!routeMatches(location, guidance.expectedRoute)) clearGuidance();
  }, [clearGuidance, guidance, location, unavailableGuidance]);

  React.useEffect(() => {
    if (!guidance || guidance.phase !== "waiting-target") return undefined;
    const findTarget = (): void => {
      const target = visibleGuidanceTarget(guidance.id);
      if (target) spotlightGuidanceTarget(guidance, target);
    };
    const observer = new MutationObserver(findTarget);
    observer.observe(document.documentElement, {
      childList: true,
      subtree: true,
      attributes: true,
      attributeFilter: ["class", "data-quickstart-id", "hidden", "style"],
    });
    const timeout = window.setTimeout(() => unavailableGuidance(guidance.controller), 10_000);
    findTarget();
    return () => {
      observer.disconnect();
      window.clearTimeout(timeout);
    };
  }, [guidance, spotlightGuidanceTarget, unavailableGuidance]);

  React.useEffect(() => {
    if (!guidance || guidance.phase !== "spotlight" || !guidance.target) return undefined;
    const { controller, id, target } = guidance;
    const dismiss = (): void => clearGuidance();
    const onTargetKeyDown = (event: KeyboardEvent): void => {
      if (event.key === "Enter" || event.key === " ") dismiss();
    };
    const onDocumentKeyDown = (event: KeyboardEvent): void => {
      if (event.key === "Escape") dismiss();
    };
    const observer = new MutationObserver(() => {
      if (!controller.signal.aborted && (!target.isConnected || !visibleGuidanceTarget(id)?.isSameNode(target))) dismiss();
    });
    target.addEventListener("pointerdown", dismiss);
    target.addEventListener("click", dismiss);
    target.addEventListener("keydown", onTargetKeyDown);
    document.addEventListener("click", dismiss);
    document.addEventListener("keydown", onDocumentKeyDown);
    observer.observe(document.documentElement, { childList: true, subtree: true, attributes: true });
    const timeout = window.setTimeout(() => {
      if (!controller.signal.aborted) dismiss();
    }, 17_800);
    return () => {
      target.removeEventListener("pointerdown", dismiss);
      target.removeEventListener("click", dismiss);
      target.removeEventListener("keydown", onTargetKeyDown);
      document.removeEventListener("click", dismiss);
      document.removeEventListener("keydown", onDocumentKeyDown);
      observer.disconnect();
      window.clearTimeout(timeout);
    };
  }, [clearGuidance, guidance]);

  React.useEffect(() => {
    if (!activePanel || activePanel.panelMode !== "open" || setupState) return undefined;
    const page = activePanel.pages.find((candidate) => candidate.path === activePanel.currentPagePath);
    if (!page) {
      setRenderState({ status: "error", message: "Lab page is unavailable." });
      return undefined;
    }
    const controller = new AbortController();
    setRenderState({ status: "loading" });
    void fetchResourceText(page.url, controller.signal)
      .then((source) => renderAsciiDoc({
        source,
        sourceURL: page.url,
        attributes: activePanel.attributes,
        loadResource: fetchResourceText,
        pageURLs: activePanel.pages.map((candidate) => candidate.url),
        signal: controller.signal,
      }))
      .then((result) => {
        if (!controller.signal.aborted) setRenderState({ status: "success", result });
      })
      .catch((error: unknown) => {
        if (!controller.signal.aborted) {
          setRenderState({ status: "error", message: (error as Error).message || "Unable to render lab page." });
        }
      });
    return () => controller.abort();
  }, [activePanel, renderRetry, setupState]);

  React.useEffect(() => {
    if (renderState.status !== "success" || !pendingFragmentRef.current) return;
    const fragment = pendingFragmentRef.current;
    pendingFragmentRef.current = undefined;
    Array.from(renderedContentRef.current?.querySelectorAll<HTMLElement>("[id]") ?? [])
      .find((element) => element.id === fragment)
      ?.scrollIntoView?.();
  }, [renderState]);

  const value = React.useMemo(
    () => ({ launch, loadingCatalogRef, launchError }),
    [launch, launchError, loadingCatalogRef],
  );
  const spotlightRect = guidance?.phase === "spotlight" && guidance.target
    ? guidance.target.getBoundingClientRect()
    : undefined;

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
            panelContent={isOpen ? (
              <DrawerPanelContent
                id="interactive-labs-drawer-panel"
                isResizable
                minSize="20rem"
                defaultSize="40%"
                resizeAriaLabel="Resize lab drawer"
                style={{ pointerEvents: "auto" }}
              >
                <DrawerHead>
                  <div>
                    <span ref={drawerFocusRef} tabIndex={-1}>
                      <strong>{activePanel.displayName}</strong>
                    </span>
                    <div style={{ display: "flex", alignItems: "center", gap: "var(--pf-t--global--spacer--sm)" }}>
                      <Button
                        variant="secondary"
                        aria-label="Previous page"
                        isDisabled={Boolean(setupState) || currentPageIndex <= 0}
                        onClick={() => selectPage(pagePaths[currentPageIndex - 1])}
                      >
                        Previous
                      </Button>
                      <span aria-live="polite">Page {currentPageIndex + 1} of {pagePaths.length}</span>
                      <Button
                        variant="secondary"
                        aria-label="Next page"
                        isDisabled={Boolean(setupState) || currentPageIndex < 0 || currentPageIndex >= pagePaths.length - 1}
                        onClick={() => selectPage(pagePaths[currentPageIndex + 1])}
                      >
                        Next
                      </Button>
                    </div>
                  </div>
                  <DrawerActions>
                    <Button variant="plain" onClick={() => setPanelMode("minimized")} aria-label="Minimize lab">
                      <WindowMinimizeIcon />
                    </Button>
                    <Button variant="plain" onClick={closePanel} aria-label="Close lab">
                      <TimesIcon />
                    </Button>
                  </DrawerActions>
                </DrawerHead>
                <DrawerPanelBody>
                  {setupState?.status === "pending" && (
                    <Alert variant="info" title="Setting up your lab environment..." isInline isLiveRegion>
                      <Spinner size="md" aria-label="Setting up lab" />
                      Lab pages will appear when setup finishes.
                    </Alert>
                  )}
                  {setupState?.status === "error" && (
                    <Alert variant="danger" title={setupState.message} isInline isLiveRegion />
                  )}
                  {!setupState && (
                    <>
                      <Title headingLevel="h2">
                        <span dangerouslySetInnerHTML={{
                          __html: renderState.status === "success"
                            ? sanitizeTitle(renderState.result.title ?? activePanel.currentPagePath)
                            : ""
                        }} />
                      </Title>
                      {navigationWarning && <Alert variant="warning" title={navigationWarning} isInline />}
                      {renderState.status === "loading" && <Spinner aria-label="Loading lab page" />}
                      {renderState.status === "error" && (
                        <>
                          <Alert variant="danger" title={renderState.message} isInline isLiveRegion />
                          <Button variant="link" onClick={() => setRenderRetry((count) => count + 1)}>Retry</Button>
                        </>
                      )}
                      {renderState.status === "success" && (
                        <>
                          {renderState.result.warnings.map((warning, index) => (
                            <Alert key={`${warning}-${index}`} variant="warning" title={warning} isInline />
                          ))}
                          <div
                            ref={renderedContentRef}
                            className="interactive-labs-content doc"
                            onClick={handleRenderedLink}
                            dangerouslySetInnerHTML={{ __html: renderState.result.html }}
                          />
                        </>
                      )}
                    </>
                  )}
                </DrawerPanelBody>
              </DrawerPanelContent>
            ) : null}
          >
            <div />
          </DrawerContent>
        </Drawer>
      )}
      {guidance?.phase === "spotlight" && guidance.target && spotlightRect && (
        <div
          key={guidance.animationKey}
          aria-hidden="true"
          className="interactive-labs-guidance-ring"
          style={{
            left: spotlightRect.left,
            top: spotlightRect.top,
            width: spotlightRect.width,
            height: spotlightRect.height,
          }}
        />
      )}
      {guidance && (
        <p className="interactive-labs-guidance-status" role="status">
          {guidance.phase === "notice" ? guidance.message : GUIDANCE_HIGHLIGHTED}
        </p>
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
            display: "flex",
            alignItems: "center",
            justifyContent: "center",
            borderRadius: "5rem 0 0 5rem",
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

const CatalogDescription: React.FC<{ item: CatalogEntry }> = ({ item }) => {
  const [state, setState] = React.useState<RenderState>({ status: "loading" });
  const [linkWarning, setLinkWarning] = React.useState<string>();
  const interactiveLabs = useInteractiveLabs();
  const sourceURL = `${PROXY_BASE}/api/v1alpha1/labs/${item.catalogRef}/resources/${item.labPath}/lab.yaml`;

  const handleLink = React.useCallback((event: React.MouseEvent<HTMLDivElement>): void => {
    if (event.defaultPrevented || event.button !== 0) return;
    const link = (event.target as Element).closest("a[href]") as HTMLAnchorElement | null;
    const href = link?.getAttribute("href");
    if (!link || !href) return;
    let target: URL;
    try {
      target = new URL(href, new URL(sourceURL, window.location.origin));
    } catch {
      event.preventDefault();
      setLinkWarning(LINK_WARNING);
      return;
    }
    const pageRoot = `${PROXY_BASE}/api/v1alpha1/labs/${item.catalogRef}/resources/${item.labPath}/content/`;
    if (target.origin === window.location.origin && target.pathname.startsWith(pageRoot)) {
      event.preventDefault();
      void interactiveLabs?.launch(item.catalogRef, link, target.href);
      return;
    }
    if ((target.protocol === "http:" || target.protocol === "https:") && target.origin !== window.location.origin) {
      event.preventDefault();
      window.open(target.href, "_blank", "noopener,noreferrer");
      return;
    }
    if (target.origin === window.location.origin && !target.pathname.startsWith(PROXY_BASE)) return;
    event.preventDefault();
    setLinkWarning(LINK_WARNING);
  }, [interactiveLabs, item.catalogRef, item.labPath, sourceURL]);

  React.useEffect(() => {
    if (!item.description) return undefined;
    const controller = new AbortController();
    void renderAsciiDoc({
      source: item.description,
      sourceURL,
      attributes: {},
      loadResource: fetchResourceText,
      signal: controller.signal,
    }).then(
      (result) => {
        if (!controller.signal.aborted) setState({ status: "success", result });
      },
      (error: unknown) => {
        if (!controller.signal.aborted) {
          setState({ status: "error", message: (error as Error).message || "Unable to render description." });
        }
      },
    );
    return () => controller.abort();
  }, [item]);

  if (state.status === "loading") return <Spinner aria-label={`Loading description for ${item.displayName}`} />;
  if (state.status === "error") return <Alert variant="warning" title={state.message} isInline />;
  return (
    <>
      {linkWarning && <Alert variant="warning" title={linkWarning} isInline />}
      {state.result.warnings.map((warning, index) => <Alert key={`${warning}-${index}`} variant="warning" title={warning} isInline />)}
      <div
        className="interactive-labs-content doc"
        onClick={handleLink}
        dangerouslySetInnerHTML={{ __html: state.result.html }}
      />
    </>
  );
};

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
                        <CatalogDescription item={item} />
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
