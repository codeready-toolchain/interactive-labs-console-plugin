import * as React from "react";
import { useLocation, useNavigate } from "react-router-dom-v5-compat";
import { isAllNamespacesKey, useActiveNamespace } from "@openshift-console/dynamic-plugin-sdk";
import "./interactive-labs.css";
import {
  ActionList,
  ActionListGroup,
  ActionListItem,
  Alert,
  Button,
  DataList,
  DataListAction,
  DataListCell,
  DataListContent,
  DataListItem,
  DataListItemCells,
  DataListItemRow,
  Drawer,
  DrawerActions,
  DrawerContent,
  DrawerHead,
  DrawerPanelBody,
  DrawerPanelContent,
  EmptyState,
  EmptyStateBody,
  PageSection,
  Select,
  SelectList,
  SelectOption,
  MenuToggle,
  Spinner,
  TextInput,
  Title,
} from "@patternfly/react-core";
import {
  fetchResourceText,
  getSetupStatus,
  listCatalog,
  loadLabBundle,
  setupLab,
} from "./api/client";
import { extractAsciiDocTitle, renderAsciiDoc, type RenderResult } from "./renderer";
import { TimesIcon, WindowMinimizeIcon } from "@patternfly/react-icons";
import type { CatalogEntry } from "./types/catalog";
import type { LabError } from "./types/errors";
import type { PersistedSession } from "./types/session";
import type { LabModule, LabPage } from "./types/manifest";
import type { LabSetup, SetupStatus } from "./types/setup";
import { sanitizeTitle } from "./sanitizer";
import { LabSetups } from "./LabSetups";

type PageState =
  | { status: "loading" }
  | { status: "success"; items: CatalogEntry[] }
  | { status: "error"; message: string };

type ActivePanel = PersistedSession & {
  catalogRef: string;
  title: string;
  modules: LabModule[];
  pages: LabPage[];
  attributes: Record<string, string>;
  launchController: AbortController;
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
  launch: (catalogRef: string, labPath: string, trigger: HTMLElement, requestedPageURL?: string, resumeSetup?: boolean) => Promise<void>;
  setupsVersion: number;
  loadingCatalogRef?: string;
  launchError?: string;
  setupError?: { catalogRef: string; message: string };
  defaultNamespace?: string;
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

const handleCopyButton = (event: { target: EventTarget | null; preventDefault: () => void }): boolean => {
  const copyButton = (event.target as Element).closest("button.copy-to-clipboard") as HTMLButtonElement | null;
  if (!copyButton) return false;
  event.preventDefault();
  const code = copyButton.closest("pre")?.querySelector("code");
  if (code) {
    const savedHTML = copyButton.innerHTML;
    const hadAriaLabel = copyButton.hasAttribute("aria-label");
    const savedAriaLabel = copyButton.getAttribute("aria-label");
    navigator.clipboard?.writeText(code.textContent ?? "").catch(() => { });
    copyButton.classList.add("copied");
    copyButton.setAttribute("aria-label", "Copied to clipboard");
    copyButton.innerHTML =
      '<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="20 6 9 17 4 12"/></svg>';
    const tooltip = document.createElement("span");
    tooltip.className = "copy-tooltip";
    tooltip.textContent = "Copied!";
    copyButton.appendChild(tooltip);
    setTimeout(() => {
      copyButton.classList.remove("copied");
      tooltip.remove();
      copyButton.innerHTML = savedHTML;
      if (hadAriaLabel) copyButton.setAttribute("aria-label", savedAriaLabel!);
      else copyButton.removeAttribute("aria-label");
    }, 1500);
  }
  return true;
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
    const status = await getSetupStatus(catalogRef, signal);
    if (signal.aborted) throw new DOMException("Aborted", "AbortError");
    if (status.spaceRequests.status === "failed") {
      throw new Error("Namespace provisioning failed.");
    }
    if (status.helm.status === "failed") {
      throw new Error(status.helm.message || "Lab setup failed.");
    }
    if (status.retrySetup && status.helm.status === "pending") {
      await setupLab(catalogRef, signal); // Fresh user token; GET status never mutates the lab.
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
  const [activeNamespace] = useActiveNamespace();
  const defaultNamespace = isAllNamespacesKey(activeNamespace) ? undefined : activeNamespace || undefined;
  const [activePanel, setActivePanel] = React.useState<ActivePanel | null>(null);
  const [loadingCatalogRef, setLoadingCatalogRef] = React.useState<string>();
  const [launchError, setLaunchError] = React.useState<string>();
  const [setupsVersion, setSetupsVersion] = React.useState(0);
  const [setupState, setSetupState] = React.useState<SetupState>();
  const [renderState, setRenderState] = React.useState<RenderState>({ status: "loading" });
  const [renderRetry, setRenderRetry] = React.useState(0);
  const [moduleTitles, setModuleTitles] = React.useState<Record<string, string>>({});
  const [moduleSelectOpen, setModuleSelectOpen] = React.useState(false);
  const launchTriggerRef = React.useRef<HTMLElement | null>(null);
  const drawerFocusRef = React.useRef<HTMLDivElement | null>(null);
  const restoreFocusRef = React.useRef<HTMLButtonElement | null>(null);
  const renderedContentRef = React.useRef<HTMLDivElement | null>(null);
  const pendingFragmentRef = React.useRef<string>();
  const guidanceControllerRef = React.useRef<AbortController>();
  const guidanceAnimationKeyRef = React.useRef(0);
  const launchAbortRef = React.useRef<AbortController>();
  const inFlightCatalogRef = React.useRef<string>();
  const mountedRef = React.useRef(true);
  const [guidance, setGuidance] = React.useState<GuidanceState>();
  const [navigationWarning, setNavigationWarning] = React.useState<string>();
  const isOpen = activePanel?.panelMode === "open";
  const navigate = useNavigate();
  const location = useLocation();
  const pagePaths = activePanel ? activePanel.pages.map((page) => page.path) : [];
  const currentPageIndex = activePanel ? pagePaths.indexOf(activePanel.currentPagePath) : -1;
  const activeModule = activePanel?.modules.find((module) => module.pages.some((page) => page.path === activePanel.currentPagePath));
  const modulePageIndex = activeModule?.pages.findIndex((page) => page.path === activePanel?.currentPagePath) ?? -1;

  React.useEffect(() => {
    if (isOpen) drawerFocusRef.current?.focus();
    else if (activePanel?.panelMode === "minimized") restoreFocusRef.current?.focus();
    else if (launchTriggerRef.current?.isConnected) launchTriggerRef.current.focus();
  }, [activePanel?.panelMode, isOpen]);

  const launch = React.useCallback(async (
    catalogRef: string,
    labPath: string,
    trigger: HTMLElement,
    requestedPageURL?: string,
    resumeSetup = false,
  ): Promise<void> => {
    if (!mountedRef.current) return;
    launchTriggerRef.current = trigger;
    if (inFlightCatalogRef.current === catalogRef && !launchAbortRef.current?.signal.aborted) {
      setModuleSelectOpen(false);
      setActivePanel((current) => {
        if (!current || current.catalogRef !== catalogRef) return current;
        const panel = { ...current, panelMode: "open" as const };
        saveSession(panel);
        return panel;
      });
      return;
    }
    launchAbortRef.current?.abort();
    setModuleTitles({});
    setModuleSelectOpen(false);
    const controller = new AbortController();
    launchAbortRef.current = controller;
    inFlightCatalogRef.current = catalogRef;
    const isCurrentLaunch = (): boolean => mountedRef.current && launchAbortRef.current === controller && !controller.signal.aborted;
    let openedPanel = false;
    setActivePanel(null);
    setLoadingCatalogRef(catalogRef);
    setLaunchError(undefined);
    setSetupState(undefined);
    try {
      const bundle = await loadLabBundle(catalogRef, labPath, controller.signal);
      if (!isCurrentLaunch()) return;

      const pages = bundle.modules.flatMap((module) => module.pages);
      const pagePaths = pages.map((page) => page.path);
      const entryPage = pagePaths[0];
      if (!entryPage) throw new Error("Lab has no navigable pages.");
      const saved = readSession(catalogRef, pagePaths);
      const requestedPage = requestedPageURL ? (() => {
        const target = new URL(requestedPageURL, window.location.origin);
        if (target.search) return undefined;
        return pages.find((page) => {
          const pageURL = new URL(page.url, window.location.origin);
          return pageURL.origin === target.origin && pageURL.pathname === target.pathname;
        });
      })() : undefined;
      if (requestedPageURL && !requestedPage) throw new Error(LINK_WARNING);
      pendingFragmentRef.current = requestedPageURL
        ? new URL(requestedPageURL, window.location.origin).hash.slice(1) || undefined
        : undefined;
      const needsSetup = bundle.runtimeContext.helmChartAvailable || resumeSetup;
      const panel: ActivePanel = {
        catalogRef,
        title: bundle.manifest.metadata.title,
        modules: bundle.modules,
        currentPagePath: requestedPage?.path ?? saved?.currentPagePath ?? entryPage,
        panelMode: needsSetup ? "open" : saved?.panelMode ?? "open",
        pages,
        attributes: bundle.manifest.spec?.attributes ?? {},
        launchController: controller,
      };
      openedPanel = needsSetup;
      if (needsSetup) setSetupState({ status: "pending" });
      setActivePanel(panel);
      saveSession(panel);

      if (needsSetup) {
        if (!resumeSetup) {
          await setupLab(catalogRef, controller.signal);
          if (!isCurrentLaunch()) return;
          setSetupsVersion((version) => version + 1);
        }
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
      if (launchAbortRef.current === controller) inFlightCatalogRef.current = undefined;
      if (isCurrentLaunch()) {
        setLoadingCatalogRef(undefined);
        if (openedPanel) setSetupsVersion((version) => version + 1);
      }
    }
  }, []);

  const setPanelMode = React.useCallback((panelMode: PersistedSession["panelMode"]): void => {
    setModuleSelectOpen(false);
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
    setModuleSelectOpen(false);
    launchAbortRef.current?.abort();
    inFlightCatalogRef.current = undefined;
    clearGuidance();
    setLoadingCatalogRef(undefined);
    if (setupState) setSetupsVersion((version) => version + 1);
    setSetupState(undefined);
    setActivePanel(null);
  }, [clearGuidance, setupState]);

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

    if (handleCopyButton(event)) return;

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
    if (!activePanel || setupState) return undefined;
    const controller = new AbortController();
    for (const module of activePanel.modules) {
      const page = module.pages[0];
      void fetchResourceText(page.url, controller.signal)
        .then((source) => extractAsciiDocTitle({
          source,
          sourceURL: page.url,
          attributes: activePanel.attributes,
          loadResource: fetchResourceText,
          signal: controller.signal,
        }))
        .then((title) => {
          if (controller.signal.aborted || activePanel.launchController.signal.aborted || !title) return;
          const template = document.createElement("template");
          template.innerHTML = sanitizeTitle(title);
          const text = template.content.textContent?.trim();
          if (text) setModuleTitles((current) => ({ ...current, [module.id]: text }));
        })
        .catch(() => { /* Use literal module ID if its first page or title is unavailable. */ });
    }
    return () => controller.abort();
  }, [activePanel?.launchController, setupState]);

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
        defaultNamespace,
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
  }, [activePanel, defaultNamespace, renderRetry, setupState]);

  React.useEffect(() => {
    if (renderState.status !== "success" || !pendingFragmentRef.current) return;
    const fragment = pendingFragmentRef.current;
    pendingFragmentRef.current = undefined;
    Array.from(renderedContentRef.current?.querySelectorAll<HTMLElement>("[id]") ?? [])
      .find((element) => element.id === fragment)
      ?.scrollIntoView?.();
  }, [renderState]);

  const value = React.useMemo(
    () => ({
      launch, loadingCatalogRef, launchError, setupsVersion,
      setupError: setupState?.status === "error" && activePanel ? { catalogRef: activePanel.catalogRef, message: setupState.message } : undefined,
      defaultNamespace,
    }),
    [launch, launchError, loadingCatalogRef, setupsVersion, setupState, activePanel?.catalogRef, defaultNamespace],
  );
  const spotlightRect = guidance?.phase === "spotlight" && guidance.target
    ? guidance.target.getBoundingClientRect()
    : undefined;

  return (
    <InteractiveLabsContext.Provider value={value}>
      {children}
      {activePanel && (
        <Drawer
          className="pfext-lab-drawer"
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
                <div className="pfext-lab-panel-content">
                  <DrawerHead>
                    <div ref={drawerFocusRef} tabIndex={-1}>
                      <Title headingLevel="h2" style={{ marginRight: "var(--pf-t--global--spacer--md)" }}>
                        {activePanel.title}
                      </Title>
                      {!setupState && (
                        <>
                          <div className="pfext-lab-module-title">{activeModule && (moduleTitles[activeModule.id] ?? activeModule.id)}</div>
                          <span aria-live="polite">Page {modulePageIndex + 1} of {activeModule?.pages.length ?? 0}</span>
                        </>
                      )}
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
                </div>
                <DrawerPanelBody className="pfext-lab-panel-content__body">
                  <div>
                    {setupState?.status === "pending" && (
                      <Alert variant="info" title="Setting up your lab environment..." isInline isLiveRegion>
                        <Spinner size="md" aria-label="Setting up lab" className="pf-v5-u-mr-xs" />
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
                  </div>
                </DrawerPanelBody>
                <div className="pfext-lab-footer">
                  <ActionList>
                    <ActionListGroup className="pfext-lab-footer__actions">
                      <ActionListItem>
                        <Button
                          variant="primary"
                          isDisabled={!!setupState || currentPageIndex < 0 || currentPageIndex >= pagePaths.length - 1}
                          onClick={() => selectPage(pagePaths[currentPageIndex + 1])}
                        >
                          Next
                        </Button>
                      </ActionListItem>
                      <ActionListItem>
                        <Button
                          variant="secondary"
                          isDisabled={!!setupState || currentPageIndex <= 0}
                          onClick={() => selectPage(pagePaths[currentPageIndex - 1])}
                        >
                          Back
                        </Button>
                      </ActionListItem>
                      {activePanel.modules.length > 1 && (
                        <ActionListItem className="pfext-lab-footer__module">
                          <Select
                            isOpen={!setupState && moduleSelectOpen}
                            selected={setupState ? undefined : activeModule?.id}
                            onOpenChange={setModuleSelectOpen}
                            shouldFocusToggleOnSelect
                            onSelect={(_event, id) => {
                              const firstPage = activePanel.modules.find((module) => module.id === id)?.pages[0];
                              if (firstPage) selectPage(firstPage.path);
                              setModuleSelectOpen(false);
                            }}
                            toggle={(toggleRef) => (
                              <MenuToggle
                                ref={toggleRef}
                                className="pfext-lab-module-toggle"
                                aria-label={setupState ? "Module" : `Module: ${activeModule && (moduleTitles[activeModule.id] ?? activeModule.id)}`}
                                title={setupState ? undefined : activeModule && (moduleTitles[activeModule.id] ?? activeModule.id)}
                                aria-haspopup="listbox"
                                isExpanded={!setupState && moduleSelectOpen}
                                isDisabled={!!setupState}
                                onClick={() => setModuleSelectOpen((open) => !open)}
                              >
                                {setupState ? "Module" : activeModule && (moduleTitles[activeModule.id] ?? activeModule.id)}
                              </MenuToggle>
                            )}
                          >
                            <SelectList>
                              {!setupState && activePanel.modules.map((module) => (
                                <SelectOption key={module.id} value={module.id}>
                                  {moduleTitles[module.id] ?? module.id}
                                </SelectOption>
                              ))}
                            </SelectList>
                          </Select>
                        </ActionListItem>
                      )}
                    </ActionListGroup>
                  </ActionList>
                </div>
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

const LabItem: React.FC<{
  catalogRef: string;
  item?: CatalogEntry;
  setup?: LabSetup;
  disabled: boolean;
  teardown?: React.ReactNode;
}> = ({ catalogRef, item, setup, disabled, teardown }) => {
  const [state, setState] = React.useState<RenderState>({ status: "loading" });
  const [isExpanded, setIsExpanded] = React.useState(false);
  const [linkWarning, setLinkWarning] = React.useState<string>();
  const interactiveLabs = useInteractiveLabs();
  const title = item?.title ?? setup?.release?.name ?? catalogRef;
  const defaultNamespace = interactiveLabs?.defaultNamespace;
  const resourceRoot = item ? `${PROXY_BASE}/api/v1alpha1/labs/${catalogRef}/resources/${item.labPath === "." ? "" : `${item.labPath.split("/").map(encodeURIComponent).join("/")}/`}` : "";
  const sourceURL = `${resourceRoot}lab.yaml`;
  const itemId = `lab-${catalogRef}`;
  const descriptionId = `${itemId}-description`;
  const setupPending = setup?.status === "pending" && !disabled;
  const actionDisabled = !interactiveLabs || disabled || (!setup && interactiveLabs?.loadingCatalogRef === catalogRef);

  const handleLink = React.useCallback((event: React.MouseEvent<HTMLDivElement>): void => {
    if (event.defaultPrevented || event.button !== 0 || !item) return;
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
    const pageRoot = `${resourceRoot}modules/`;
    if (target.origin === window.location.origin && target.pathname.startsWith(pageRoot)) {
      event.preventDefault();
      if (actionDisabled) {
        setLinkWarning("Lab cannot be opened until its setup is available.");
        return;
      }
      void interactiveLabs?.launch(catalogRef, item.labPath, link, target.href, !!setup);
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
  }, [actionDisabled, interactiveLabs, catalogRef, item, resourceRoot, setup, sourceURL]);

  React.useEffect(() => {
    if (!item?.description) return undefined;
    const controller = new AbortController();
    void renderAsciiDoc({
      source: item.description,
      sourceURL,
      defaultNamespace,
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
  }, [item, defaultNamespace]);

  const template = document.createElement("template");
  if (item?.description && state.status === "success") {
    template.innerHTML = state.result.html;
    template.content.querySelectorAll("button.copy-to-clipboard").forEach((button) => button.remove());
  }
  const paragraph = template.content.querySelector("div.paragraph > p, p");
  const preview = paragraph?.parentElement?.matches("div.paragraph") ? paragraph.parentElement : paragraph;
  const previewHTML = preview?.outerHTML;
  let parent = preview?.parentElement;
  preview?.remove();
  while (parent && !parent.innerHTML.trim()) {
    const next = parent.parentElement;
    parent.remove();
    parent = next;
  }
  const remainingHTML = template.innerHTML.trim();

  return (
    <DataListItem aria-labelledby={itemId} isExpanded={isExpanded}>
      <DataListItemRow style={{ paddingBlockStart: "var(--pf-t--global--spacer--md)" }}>
        <DataListItemCells dataListCells={[
          <DataListCell key="name" width={5}>
            <Title headingLevel="h2" size="lg" id={itemId}>{title}</Title>
            {!item && setup && <>
              {setup.release && <p>Helm release: {setup.release.name}</p>}
              <p>SpaceRequests: {setup.spaceRequests.join(', ') || 'none'}</p>
            </>}
            {item && linkWarning && <Alert variant="warning" title={linkWarning} isInline />}
            {item?.description && state.status === "loading" && <Spinner aria-label={`Loading description for ${title}`} />}
            {item?.description && state.status === "error" && <Alert variant="warning" title={state.message} isInline />}
            {item?.description && state.status === "success" && state.result.warnings.map((warning, index) => (
              <Alert key={`${warning}-${index}`} variant="warning" title={warning} isInline />
            ))}
            {previewHTML && (
              <div className="interactive-labs-content doc catalog-description" onClick={handleLink} dangerouslySetInnerHTML={{ __html: previewHTML }} />
            )}
            {remainingHTML && (
              <Button
                variant="link"
                aria-controls={descriptionId}
                aria-expanded={isExpanded}
                onClick={() => setIsExpanded((expanded) => !expanded)}
              >
                {isExpanded ? "Show less" : "Show more…"}
              </Button>
            )}
          </DataListCell>,
        ]} />
        <DataListAction id={`${itemId}-actions`} aria-labelledby={itemId} aria-label={`Actions for ${title}`}>
          <div className="pfext-lab-catalog-actions" style={{ display: "flex", flexDirection: "column", gap: "var(--pf-t--global--spacer--sm)" }}>
            {item && <Button
              isLoading={!disabled && (setupPending || interactiveLabs?.loadingCatalogRef === catalogRef)}
              isDisabled={actionDisabled}
              spinnerAriaLabel={setupPending ? "Lab setup pending" : "Loading lab"}
              onClick={(event) => void interactiveLabs?.launch(catalogRef, item.labPath, event.currentTarget, undefined, !!setup)}
            >
              {setup ? "Open" : "Launch"}
            </Button>}
            {teardown}
            {interactiveLabs?.setupError?.catalogRef === catalogRef && <p role="alert">{interactiveLabs.setupError.message}</p>}
          </div>
        </DataListAction>
      </DataListItemRow>
      {remainingHTML && (
        <DataListContent id={descriptionId} aria-label={`Description for ${title}`} isHidden={!isExpanded}>
          <div className="interactive-labs-content doc catalog-description" onClick={handleLink} dangerouslySetInnerHTML={{ __html: remainingHTML }} />
        </DataListContent>
      )}
    </DataListItem>
  );
};

const InteractiveLabsPage: React.FC = () => {
  const [state, setState] = React.useState<PageState>({ status: "loading" });
  const [retry, setRetry] = React.useState(0);
  const [filter, setFilter] = React.useState("");
  const interactiveLabs = useInteractiveLabs();
  const filteredItems =
    state.status === "success"
      ? state.items.filter((item) =>
        `${item.title} ${item.description ?? ""}`
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
      <LabSetups
        catalog={state.status === "loading" ? undefined : state.status === "success" ? state.items : []}
        refreshVersion={interactiveLabs?.setupsVersion}
      >
        {({ items, existingCatalogRefs, disabledFor, teardown }) => {
          const catalog = state.status === "success" ? state.items : [];
          const existing = new Set(existingCatalogRefs);
          const orphans = existingCatalogRefs.filter((catalogRef) => !catalog.some((item) => item.catalogRef === catalogRef));
          const visibleRefs = [
            ...filteredItems.filter((item) => existing.has(item.catalogRef)).map((item) => item.catalogRef),
            ...orphans,
            ...filteredItems.filter((item) => !existing.has(item.catalogRef)).map((item) => item.catalogRef),
          ];
          return (
            <>
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
              {state.status === "success" && state.items.length === 0 && visibleRefs.length === 0 && (
                <EmptyState titleText="No labs available">
                  <EmptyStateBody>No labs are currently configured.</EmptyStateBody>
                </EmptyState>
              )}
              {state.status === "success" && state.items.length > 0 && (
                <>
                  <div style={{ marginBlockStart: "var(--pf-t--global--spacer--lg)", marginBottom: "var(--pf-t--global--spacer--md)" }}>
                    <TextInput
                      aria-label="Filter labs"
                      type="search"
                      value={filter}
                      onChange={(_event, value) => setFilter(value)}
                      placeholder="Filter by name or description"
                    />
                  </div>
                  {visibleRefs.length === 0 && (
                    <EmptyState titleText="No labs match your filter.">
                      <EmptyStateBody>Try a different name or description.</EmptyStateBody>
                    </EmptyState>
                  )}
                </>
              )}
              {visibleRefs.length > 0 && (
                <DataList aria-label="Available labs" isCompact isPlain>
                  {visibleRefs.map((catalogRef) => (
                    <LabItem
                      key={catalogRef}
                      catalogRef={catalogRef}
                      item={catalog.find((item) => item.catalogRef === catalogRef)}
                      setup={items.find((item) => item.catalogRef === catalogRef)}
                      disabled={disabledFor(catalogRef)}
                      teardown={teardown(catalogRef)}
                    />
                  ))}
                </DataList>
              )}
            </>
          );
        }}
      </LabSetups>
    </PageSection>
  );
};

export { InteractiveLabsPage, InteractiveLabsProvider, useInteractiveLabs };
