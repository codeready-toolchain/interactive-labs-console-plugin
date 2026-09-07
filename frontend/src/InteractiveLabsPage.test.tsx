/**
 * STORY-2 tests — List Available Labs
 * Tests use React 17 react-dom/test-utils; no Testing Library.
 */

import * as React from 'react';
import * as ReactDOM from 'react-dom';
import { act } from 'react-dom/test-utils';
import { InteractiveLabsPage, InteractiveLabsProvider } from './InteractiveLabsPage';
import type { CatalogList } from './types/catalog';
import type { LabError } from './types/errors';
import type { LabBundle } from './types/manifest';
import * as client from './api/client';
import { renderAsciiDoc } from './renderer';

jest.mock('./api/client');
jest.mock('./renderer', () => ({ renderAsciiDoc: jest.fn() }));
const mockNavigate = jest.fn();
jest.mock('react-router-dom-v5-compat', () => ({ useNavigate: () => mockNavigate }));

const mockListCatalog = client.listCatalog as jest.MockedFunction<typeof client.listCatalog>;
const mockLoadLabBundle = client.loadLabBundle as jest.MockedFunction<typeof client.loadLabBundle>;
const mockSetupLab = client.setupLab as jest.MockedFunction<typeof client.setupLab>;
const mockGetNavigablePagePaths = client.getNavigablePagePaths as jest.MockedFunction<
  typeof client.getNavigablePagePaths
>;
const mockFetchResourceText = client.fetchResourceText as jest.MockedFunction<typeof client.fetchResourceText>;
const mockRenderAsciiDoc = renderAsciiDoc as jest.MockedFunction<typeof renderAsciiDoc>;

const SESSION_PREFIX = 'interactive-labs-console-plugin:session:';

let container: HTMLDivElement;

beforeEach(() => {
  container = document.createElement('div');
  document.body.appendChild(container);
  mockListCatalog.mockReset();
  mockLoadLabBundle.mockReset();
  mockSetupLab.mockReset();
  mockGetNavigablePagePaths.mockReset();
  mockGetNavigablePagePaths.mockImplementation((pages) =>
    pages
      .map((page) => page.path)
      .filter((pagePath) => pagePath.split('/').every((segment) => !segment.startsWith('_')))
      .sort(),
  );
  mockFetchResourceText.mockReset();
  mockFetchResourceText.mockResolvedValue('= Page');
  mockRenderAsciiDoc.mockReset();
  mockRenderAsciiDoc.mockImplementation(() => new Promise<never>(() => {}));
  mockNavigate.mockReset();
  localStorage.clear();
});

afterEach(() => {
  ReactDOM.unmountComponentAtNode(container);
  container.remove();
});

const emptyCatalog: CatalogList = { apiVersion: 'v1alpha1', kind: 'CatalogList', items: [] };

const sampleCatalog: CatalogList = {
  apiVersion: 'v1alpha1',
  kind: 'CatalogList',
  items: [
    { catalogRef: 'lab-1', displayName: 'Lab One', description: 'First lab description', labPath: 'labs/one' },
    { catalogRef: 'lab-2', displayName: 'Lab Two', labPath: 'labs/two' },
  ],
};

const sampleError: LabError = {
  apiVersion: 'v1alpha1',
  kind: 'Error',
  type: 'catalog_unavailable',
  message: 'Catalog service unavailable',
};

const noChartBundle: LabBundle = {
  apiVersion: 'v1alpha1',
  kind: 'LabBundle',
  manifest: {
    apiVersion: 'v1alpha1',
    kind: 'Lab',
    metadata: { name: 'lab-one', displayName: 'Lab One' },
    spec: {},
  },
  pages: [
    { path: 'pages/z-last.adoc', url: 'api/v1alpha1/labs/lab-1/resources/labs/one/content/pages/z-last.adoc' },
    { path: '_partials/shared.adoc', url: 'api/v1alpha1/labs/lab-1/resources/labs/one/content/_partials/shared.adoc' },
    { path: 'pages/a-first.adoc', url: 'api/v1alpha1/labs/lab-1/resources/labs/one/content/pages/a-first.adoc' },
  ],
  runtimeContext: { catalogRef: 'lab-1', branch: 'main', helmChartAvailable: false },
};

const chartBundle: LabBundle = {
  ...noChartBundle,
  runtimeContext: { ...noChartBundle.runtimeContext, helmChartAvailable: true },
};

const proxyNoChartBundle: LabBundle = {
  ...noChartBundle,
  pages: noChartBundle.pages.map((page) => ({
    ...page,
    url: `/api/proxy/plugin/interactive-labs-console-plugin/backend/${page.url}`,
  })),
};

const sessionKey = (catalogRef: string) => `${SESSION_PREFIX}${catalogRef}`;

const getButton = (name: RegExp): HTMLButtonElement => {
  const button = Array.from(container.querySelectorAll('button')).find((candidate) =>
    name.test(`${candidate.getAttribute('aria-label') ?? ''} ${candidate.textContent ?? ''}`.trim()),
  ) as HTMLButtonElement | undefined;
  if (!button) throw new Error(`Missing button matching ${name}`);
  return button;
};

const renderCatalog = async () => {
  mockListCatalog.mockResolvedValue(sampleCatalog);
  await act(async () => {
    ReactDOM.render(
      <InteractiveLabsProvider>
        <InteractiveLabsPage />
      </InteractiveLabsProvider>,
      container,
    );
  });
};

describe('InteractiveLabsPage', () => {
  test('shows heading and loading indicator on initial render', async () => {
    // Never-resolving promise keeps component in loading state
    let resolveList!: (v: CatalogList) => void;
    mockListCatalog.mockReturnValue(new Promise<CatalogList>(r => { resolveList = r; }));

    await act(async () => {
      ReactDOM.render(<InteractiveLabsPage />, container);
    });

    expect(container.querySelector('h1')?.textContent).toContain('Interactive Labs');
    const spinner =
      container.querySelector('[role="progressbar"]') ??
      container.querySelector('[aria-label="Loading labs"]');
    expect(spinner).not.toBeNull();

    // Resolve to avoid act() warning on cleanup
    await act(async () => { resolveList(emptyCatalog); });
  });

  test('shows every displayName and description after successful response', async () => {
    mockListCatalog.mockResolvedValue(sampleCatalog);
    mockRenderAsciiDoc.mockResolvedValue({ html: '<p>First lab description</p>', warnings: [] });

    await act(async () => {
      ReactDOM.render(<InteractiveLabsPage />, container);
    });

    expect(container.textContent).toContain('Lab One');
    expect(container.textContent).toContain('First lab description');
    expect(container.textContent).toContain('Lab Two');
    expect(container.querySelector('[role="progressbar"]')).toBeNull();
  });

  test('keeps descriptions collapsed until expanded', async () => {
    mockListCatalog.mockResolvedValue(sampleCatalog);

    await act(async () => {
      ReactDOM.render(<InteractiveLabsPage />, container);
    });

    const description = container.querySelector(
      'section[aria-label="Description for Lab One"]',
    ) as HTMLElement;
    const toggle = container.querySelector(
      'button[aria-label="Toggle description for Lab One"]',
    ) as HTMLButtonElement;
    expect(description.hidden).toBe(true);

    act(() => { toggle.click(); });

    expect(description.hidden).toBe(false);
  });

  test('filters labs by display name and description', async () => {
    mockListCatalog.mockResolvedValue(sampleCatalog);

    await act(async () => {
      ReactDOM.render(<InteractiveLabsPage />, container);
    });

    const filter = container.querySelector('input[aria-label="Filter labs"]') as HTMLInputElement;
    expect(filter).not.toBeNull();

    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(
        filter,
        'Lab Two',
      );
      filter.dispatchEvent(new Event('input', { bubbles: true }));
    });

    expect(container.textContent).toContain('Lab Two');
    expect(container.textContent).not.toContain('Lab One');

    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(
        filter,
        'first lab description',
      );
      filter.dispatchEvent(new Event('input', { bubbles: true }));
    });

    expect(container.textContent).toContain('Lab One');
    expect(container.textContent).not.toContain('Lab Two');

    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(
        filter,
        'not a lab',
      );
      filter.dispatchEvent(new Event('input', { bubbles: true }));
    });

    expect(container.textContent).toContain('No labs match your filter.');
  });

  test('loads selected bundle exactly once for an explicit Launch', async () => {
    mockLoadLabBundle.mockResolvedValue(noChartBundle);
    await renderCatalog();

    const launch = getButton(/^Launch$/);
    expect(launch.disabled).toBe(false);

    await act(async () => { launch.click(); });

    expect(mockLoadLabBundle).toHaveBeenCalledTimes(1);
    expect(mockLoadLabBundle).toHaveBeenCalledWith('lab-1');
  });

  test('opens no-chart lab at lexical first navigable page without setup', async () => {
    mockLoadLabBundle.mockResolvedValue(noChartBundle);
    await renderCatalog();

    await act(async () => { getButton(/^Launch$/).click(); });

    expect(container.textContent).toContain('Page 1 of 2');
    expect(mockSetupLab).not.toHaveBeenCalled();
  });

  test('shows a load or validation error without opening a drawer panel', async () => {
    mockLoadLabBundle.mockRejectedValue({ ...sampleError, message: 'Bundle validation failed' });
    await renderCatalog();

    await act(async () => { getButton(/^Launch$/).click(); });

    expect(
      (container.querySelector('[role="alert"]') ?? container.querySelector('[aria-live]'))?.textContent,
    ).toContain('Bundle validation failed');
    expect(container.querySelector('button[aria-label="Minimize lab"]')).toBeNull();
    expect(container.querySelector('button[aria-label="Close lab"]')).toBeNull();
  });

  test('shows validated manifest displayName in drawer', async () => {
    mockLoadLabBundle.mockResolvedValue({
      ...noChartBundle,
      manifest: {
        ...noChartBundle.manifest,
        metadata: { ...noChartBundle.manifest.metadata, displayName: 'Validated bundle title' },
      },
    });
    await renderCatalog();

    await act(async () => { getButton(/^Launch$/).click(); });

    expect(container.querySelector('[class*="drawer__panel"]')?.textContent).toContain(
      'Validated bundle title',
    );
  });

  test('defers chart-backed lab setup without opening content or calling setupLab', async () => {
    mockLoadLabBundle.mockResolvedValue(chartBundle);
    await renderCatalog();

    await act(async () => { getButton(/^Launch$/).click(); });

    expect(container.textContent).toMatch(/setup.*STORY-5|STORY-5.*setup/i);
    expect(container.textContent).not.toContain('pages/a-first.adoc');
    expect(mockSetupLab).not.toHaveBeenCalled();
  });

  test('passes bundle attributes unaltered to renderer and mounts only renderer-sanitized markup', async () => {
    const attributes = {
      user: 'alice',
      openshift_api_url: 'https://api.example.test:6443',
      openshift_console_url: 'https://console.example.test',
      openshift_cluster_ingress_domain: 'apps.example.test',
    };
    mockLoadLabBundle.mockResolvedValue({
      ...noChartBundle,
      manifest: { ...noChartBundle.manifest, spec: { attributes } },
    });
    mockFetchResourceText.mockResolvedValue('= Source');
    mockRenderAsciiDoc.mockResolvedValue({ html: '<p>Sanitized output</p>', warnings: [] });
    await renderCatalog();

    await act(async () => { getButton(/^Launch$/).click(); });

    const sourceURL = noChartBundle.pages[2].url;
    expect(mockFetchResourceText).toHaveBeenCalledWith(sourceURL, expect.any(AbortSignal));
    const pageCall = mockRenderAsciiDoc.mock.calls.find(([input]) => input.sourceURL === sourceURL);
    expect(pageCall?.[0]).toEqual(expect.objectContaining({
      source: '= Source',
      sourceURL,
      attributes,
      loadResource: mockFetchResourceText,
    }));
    expect(pageCall?.[0].attributes).toBe(attributes);
    expect(container.innerHTML).toContain('Sanitized output');
  });

  test('renders catalog descriptions through restricted renderer with lab.yaml resource base', async () => {
    mockRenderAsciiDoc.mockResolvedValue({ html: '<p>Sanitized description</p>', warnings: [] });
    await renderCatalog();

    await act(async () => { getButton(/Toggle description for Lab One/).click(); });

    expect(mockRenderAsciiDoc).toHaveBeenCalledWith(expect.objectContaining({
      source: 'First lab description',
      sourceURL: '/api/proxy/plugin/interactive-labs-console-plugin/backend/api/v1alpha1/labs/lab-1/resources/labs/one/lab.yaml',
      attributes: {},
      loadResource: mockFetchResourceText,
    }));
    expect(container.innerHTML).toContain('Sanitized description');
  });

  test('marks rendered page and description roots as scoped AsciiDoc documents', async () => {
    mockLoadLabBundle.mockResolvedValue(noChartBundle);
    mockRenderAsciiDoc.mockResolvedValue({ html: '<p>Rendered content</p>', warnings: [] });
    await renderCatalog();

    await act(async () => { getButton(/^Launch$/).click(); });
    await act(async () => { getButton(/Toggle description for Lab One/).click(); });

    const page = container.querySelector('[class*="drawer__panel"] .interactive-labs-content.doc');
    const description = container.querySelector(
      '[aria-label="Description for Lab One"] .interactive-labs-content.doc',
    );
    expect(page).not.toBeNull();
    expect(description).not.toBeNull();
  });

  test('aborts obsolete page fetch on close and never mounts its stale response', async () => {
    mockLoadLabBundle.mockResolvedValue(noChartBundle);
    let resolvePage!: (value: string) => void;
    let signal!: AbortSignal;
    mockFetchResourceText.mockImplementation((_url, requestSignal) => {
      signal = requestSignal;
      return new Promise<string>((resolve) => { resolvePage = resolve; });
    });
    await renderCatalog();

    act(() => { getButton(/^Launch$/).click(); });
    await act(async () => {});
    expect(signal.aborted).toBe(false);
    act(() => { getButton(/close/i).click(); });
    expect(signal.aborted).toBe(true);
    await act(async () => { resolvePage('= Stale page'); });
    expect(container.innerHTML).not.toContain('Stale page');
  });

  test('keeps drawer open with retry after a fatal renderer error and shows recoverable warnings inline', async () => {
    mockLoadLabBundle.mockResolvedValue(noChartBundle);
    mockRenderAsciiDoc.mockResolvedValueOnce({
      html: '<p>Partial content</p>',
      warnings: ['Included resource is unavailable or unsafe.'],
    });
    await renderCatalog();
    await act(async () => { getButton(/^Launch$/).click(); });
    expect(container.textContent).toContain('Partial content');
    expect(container.textContent).toContain('Included resource is unavailable or unsafe.');

    mockRenderAsciiDoc.mockRejectedValueOnce(new Error('Sanitizer failed'));
    await act(async () => { getButton(/^Launch$/).click(); });
    expect(container.textContent).toContain('Sanitizer failed');
    expect(container.querySelector('button[aria-label="Minimize lab"]')).not.toBeNull();
    expect(getButton(/^Retry$/)).toBeTruthy();
  });

  test('launches a catalog-description page link only after matching it to a validated bundle descriptor', async () => {
    mockLoadLabBundle.mockResolvedValue(proxyNoChartBundle);
    mockRenderAsciiDoc.mockImplementation(({ sourceURL }) => Promise.resolve({
      html: sourceURL.endsWith('/lab.yaml')
        ? '<a href="content/pages/z-last.adoc">Open linked page</a>'
        : '<p>Sanitized page</p>',
      warnings: [],
    }));
    await renderCatalog();

    await act(async () => { getButton(/Toggle description for Lab One/).click(); });
    const link = Array.from(container.querySelectorAll('a')).find((anchor) => anchor.textContent === 'Open linked page')!;
    const preventNavigation = (event: Event) => event.preventDefault();
    document.addEventListener('click', preventNavigation);
    await act(async () => { link.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true })); });
    document.removeEventListener('click', preventNavigation);

    expect(mockLoadLabBundle).toHaveBeenCalledWith('lab-1');
    expect(container.textContent).toContain('pages/z-last.adoc');
    expect(mockFetchResourceText).toHaveBeenCalledWith(proxyNoChartBundle.pages[0].url, expect.any(AbortSignal));
  });

  test('loads lexical first page and keeps previous and next within page boundaries', async () => {
    mockLoadLabBundle.mockResolvedValue(noChartBundle);
    mockRenderAsciiDoc.mockResolvedValue({ html: '<p>Rendered page</p>', warnings: [] });
    await renderCatalog();

    await act(async () => { getButton(/^Launch$/).click(); });

    const previous = getButton(/previous page/i);
    const next = getButton(/next page/i);
    expect(container.textContent).toContain('pages/a-first.adoc');
    expect(container.textContent).toContain('Page 1 of 2');
    expect(previous.disabled).toBe(true);
    expect(next.disabled).toBe(false);

    await act(async () => { next.click(); });

    expect(container.textContent).toContain('pages/z-last.adoc');
    expect(container.textContent).toContain('Page 2 of 2');
    expect(getButton(/previous page/i).disabled).toBe(false);
    expect(getButton(/next page/i).disabled).toBe(true);
    expect(localStorage.getItem(sessionKey('lab-1'))).toBe(
      JSON.stringify({ currentPagePath: 'pages/z-last.adoc', panelMode: 'open' }),
    );
  });

  test('uses an accessible resizable drawer header with an empty title while page rendering loads', async () => {
    mockLoadLabBundle.mockResolvedValue(noChartBundle);
    let resolveRender!: (result: Awaited<ReturnType<typeof renderAsciiDoc>>) => void;
    mockRenderAsciiDoc.mockImplementation(() => new Promise((resolve) => { resolveRender = resolve; }));
    await renderCatalog();

    await act(async () => { getButton(/^Launch$/).click(); });

    const drawer = container.querySelector('[class*="drawer__panel"]')!;
    const labName = drawer.querySelector('strong')!;
    const pageTitle = drawer.querySelector('h2')!;
    const previous = getButton(/previous page/i);
    const next = getButton(/next page/i);
    const minimize = getButton(/^minimize lab$/i);
    const close = getButton(/^close lab$/i);

    expect(labName.textContent).toBe('Lab One');
    expect(pageTitle.textContent).toBe('');
    expect(drawer.querySelector('[aria-label="Loading lab page"]')).not.toBeNull();
    expect(drawer.textContent).not.toContain('pages/a-first.adoc');
    expect(labName.compareDocumentPosition(previous) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(previous.compareDocumentPosition(next) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(next.compareDocumentPosition(pageTitle) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(drawer.querySelector('[role="separator"][aria-label="Resize lab drawer"]')).not.toBeNull();
    [minimize, close].forEach((button) => {
      expect(button.textContent).toBe('');
      expect(button.querySelector('svg')).not.toBeNull();
    });
    expect(container.querySelector('button[aria-label="Reset lab"]')).toBeNull();

    await act(async () => {
      resolveRender({
        html: '<p>Rendered page</p>',
        warnings: [],
        title: 'Step 1.2: See what&#8217;s <em>running</em>',
      });
    });

    expect(pageTitle.textContent).toBe('Step 1.2: See what’s running');
    expect(pageTitle.querySelector('em')?.textContent).toBe('running');
  });

  test('navigates relative known pages and preserves same-page and cross-page fragments', async () => {
    mockLoadLabBundle.mockResolvedValue(noChartBundle);
    mockRenderAsciiDoc.mockImplementation(({ sourceURL }) => Promise.resolve({
      html: sourceURL.endsWith('a-first.adoc')
        ? '<a href="#local">Local fragment</a><a href="z-last.adoc#target">Linked page</a>'
        : '<h2 id="target">Target</h2>',
      warnings: [],
    }));
    await renderCatalog();

    await act(async () => { getButton(/^Launch$/).click(); });
    const local = Array.from(container.querySelectorAll('a')).find((link) => link.textContent === 'Local fragment')!;
    const localEvent = new MouseEvent('click', { bubbles: true, cancelable: true });
    local.dispatchEvent(localEvent);
    expect(localEvent.defaultPrevented).toBe(false);
    expect(container.textContent).toContain('pages/a-first.adoc');

    await act(async () => {
      Array.from(container.querySelectorAll('a')).find((link) => link.textContent === 'Linked page')!.dispatchEvent(
        new MouseEvent('click', { bubbles: true, cancelable: true }),
      );
    });
    expect(container.textContent).toContain('pages/z-last.adoc');
    expect(mockFetchResourceText).toHaveBeenCalledWith(noChartBundle.pages[0].url, expect.any(AbortSignal));
  });

  test('opens external links safely, retains drawer for Console links, and warns for plugin or unknown links', async () => {
    mockLoadLabBundle.mockResolvedValue(noChartBundle);
    mockRenderAsciiDoc.mockResolvedValue({
      html: [
        '<a href="https://docs.example.test/guide">External</a>',
        `<a href="${window.location.origin}/">Console</a>`,
        `<a href="${noChartBundle.pages[2].url}">Plugin resource</a>`,
        '<a href="missing.adoc">Missing</a>',
      ].join(''),
      warnings: [],
    });
    const open = jest.spyOn(window, 'open').mockImplementation(() => null);
    await renderCatalog();
    await act(async () => { getButton(/^Launch$/).click(); });

    const drawer = container.querySelector('[class*="drawer__panel"]')!;
    const externalEvent = new MouseEvent('click', { bubbles: true, cancelable: true });
    await act(async () => {
      Array.from(drawer.querySelectorAll('a')).find((link) => link.textContent === 'External')!.dispatchEvent(externalEvent);
    });
    expect(externalEvent.defaultPrevented).toBe(true);
    expect(open).toHaveBeenCalledWith('https://docs.example.test/guide', '_blank', 'noopener,noreferrer');

    const consoleLink = Array.from(drawer.querySelectorAll('a')).find((link) => link.textContent === 'Console')!;
    const consoleEvent = new MouseEvent('click', { bubbles: true, cancelable: true });
    await act(async () => { consoleLink.dispatchEvent(consoleEvent); });
    expect(consoleEvent.defaultPrevented).toBe(true);
    expect(mockNavigate).toHaveBeenCalledWith('/');
    expect(container.querySelector('button[aria-label="Close lab"]')).not.toBeNull();

    const missingEvent = new MouseEvent('click', { bubbles: true, cancelable: true });
    await act(async () => {
      Array.from(drawer.querySelectorAll('a')).find((link) => link.textContent === 'Missing')!.dispatchEvent(missingEvent);
    });
    expect(missingEvent.defaultPrevented).toBe(true);
    expect(container.textContent).toContain('Link is unavailable or unsafe.');

    const pluginEvent = new MouseEvent('click', { bubbles: true, cancelable: true });
    await act(async () => {
      Array.from(container.querySelector('[class*="drawer__panel"]')!.querySelectorAll('a'))
        .find((link) => link.textContent === 'Plugin resource')!
        .dispatchEvent(pluginEvent);
    });
    expect(pluginEvent.defaultPrevented).toBe(true);
    expect(container.textContent).toContain('Link is unavailable or unsafe.');
    open.mockRestore();
  });

  test('retries a failed page fetch without closing drawer', async () => {
    mockLoadLabBundle.mockResolvedValue(noChartBundle);
    mockFetchResourceText.mockRejectedValueOnce(new Error('Page unavailable'));
    mockRenderAsciiDoc.mockResolvedValue({ html: '<p>Retried page</p>', warnings: [] });
    await renderCatalog();

    await act(async () => { getButton(/^Launch$/).click(); });
    expect(container.textContent).toContain('Page unavailable');

    await act(async () => { getButton(/^Retry$/).click(); });
    expect(mockFetchResourceText).toHaveBeenCalledTimes(2);
    expect(container.textContent).toContain('Retried page');
    expect(container.querySelector('button[aria-label="Close lab"]')).not.toBeNull();
  });

  test('uses native controls, moves focus into drawer, and restores it after close', async () => {
    mockLoadLabBundle.mockResolvedValue(noChartBundle);
    await renderCatalog();

    const launch = getButton(/^Launch$/);
    await act(async () => { launch.click(); });

    const minimize = getButton(/minimize/i);
    const close = getButton(/close/i);
    [launch, minimize, close].forEach((button) => {
      expect(button.tagName).toBe('BUTTON');
      expect(button.type).toBe('button');
      expect(button.disabled).toBe(false);
    });
    expect((document.activeElement as HTMLElement).closest('[class*="drawer"]')).not.toBeNull();

    act(() => { minimize.click(); });
    expect(localStorage.getItem(sessionKey('lab-1'))).toBe(
      JSON.stringify({ currentPagePath: 'pages/a-first.adoc', panelMode: 'minimized' }),
    );
    const restore = getButton(/restore/i);
    expect(restore.tagName).toBe('BUTTON');
    expect(restore.type).toBe('button');
    expect(restore.disabled).toBe(false);
    expect(restore.closest('[class*="drawer__panel"]')).toBeNull();
    expect(document.activeElement).toBe(restore);

    act(() => { restore.click(); });
    expect(localStorage.getItem(sessionKey('lab-1'))).toBe(
      JSON.stringify({ currentPagePath: 'pages/a-first.adoc', panelMode: 'open' }),
    );
    act(() => { getButton(/close/i).click(); });
    expect(document.activeElement).toBe(launch);
  });

  test('keeps overlay in root provider across child changes and requires explicit relaunch after close', async () => {
    mockLoadLabBundle.mockResolvedValue(noChartBundle);
    await renderCatalog();

    await act(async () => { getButton(/^Launch$/).click(); });
    expect(container.textContent).toContain('Page 1 of 2');

    await act(async () => {
      ReactDOM.render(
        <InteractiveLabsProvider>
          <p>Different Console page</p>
        </InteractiveLabsProvider>,
        container,
      );
    });
    expect(container.textContent).toContain('Different Console page');
    expect(container.textContent).toContain('Page 1 of 2');

    act(() => { getButton(/minimize/i).click(); });
    expect(container.textContent).not.toContain('Page 1 of 2');
    expect(container.querySelector('button[aria-label="Minimize lab"]')).toBeNull();
    expect(container.querySelector('button[aria-label="Close lab"]')).toBeNull();
    const restore = container.querySelector('button[aria-label="Restore lab"]') as HTMLButtonElement;
    expect(restore).not.toBeNull();
    expect(restore.textContent).toBe("");
    expect(restore.querySelector("svg")).not.toBeNull();
    expect(restore.style.width).toBe("1.5rem");
    expect(restore.style.height).toBe("5rem");
    expect(restore.style.display).toBe("flex");
    expect(restore.style.alignItems).toBe("center");
    expect(restore.style.justifyContent).toBe("center");
    expect(restore.style.borderRadius).toBe("5rem 0 0 5rem");

    act(() => { restore.click(); });
    expect(container.textContent).toContain('Page 1 of 2');

    act(() => { getButton(/close/i).click(); });
    expect(container.textContent).not.toContain('Page 1 of 2');
    expect(container.querySelector('button[aria-label="Minimize lab"]')).toBeNull();
    expect(container.querySelector('button[aria-label="Close lab"]')).toBeNull();
    expect(container.querySelector('button[aria-label="Restore lab"]')).toBeNull();
    expect(mockLoadLabBundle).toHaveBeenCalledTimes(1);

    await renderCatalog();
    expect(container.textContent).not.toContain('Page 1 of 2');
    expect(mockLoadLabBundle).toHaveBeenCalledTimes(1);

    await act(async () => { getButton(/^Launch$/).click(); });
    expect(mockLoadLabBundle).toHaveBeenCalledTimes(2);
    expect(container.textContent).toContain('Page 1 of 2');
  });

  test('keeps a closed drawer closed across remount until user explicitly launches again', async () => {
    mockLoadLabBundle.mockResolvedValue(noChartBundle);
    await renderCatalog();

    const firstLaunch = getButton(/^Launch$/);
    await act(async () => { firstLaunch.click(); });
    act(() => { getButton(/close/i).click(); });
    expect(document.activeElement).toBe(firstLaunch);

    act(() => { ReactDOM.unmountComponentAtNode(container); });
    await renderCatalog();

    expect(mockLoadLabBundle).toHaveBeenCalledTimes(1);
    expect(container.textContent).not.toContain('Page 1 of 2');

    await act(async () => { getButton(/^Launch$/).click(); });
    expect(mockLoadLabBundle).toHaveBeenCalledTimes(2);
    expect(container.textContent).toContain('Page 1 of 2');
  });

  test('restores valid local session only after explicit activation', async () => {
    mockLoadLabBundle.mockResolvedValue(noChartBundle);
    localStorage.setItem(
      sessionKey('lab-1'),
      JSON.stringify({ currentPagePath: 'pages/z-last.adoc', panelMode: 'minimized' }),
    );
    await renderCatalog();

    expect(mockLoadLabBundle).not.toHaveBeenCalled();
    expect(container.textContent).not.toContain('pages/z-last.adoc');

    await act(async () => { getButton(/^Launch$/).click(); });
    expect(mockLoadLabBundle).toHaveBeenCalledTimes(1);
    expect(getButton(/restore/i)).toBeTruthy();

    act(() => { getButton(/restore/i).click(); });
    expect(container.textContent).toContain('Page 2 of 2');
  });

  test('discards malformed stored session and starts at entry page', async () => {
    mockLoadLabBundle.mockResolvedValue(noChartBundle);
    localStorage.setItem(sessionKey('lab-1'), '{not json');
    await renderCatalog();

    await act(async () => { getButton(/^Launch$/).click(); });

    expect(container.textContent).toContain('Page 1 of 2');
    expect(localStorage.getItem(sessionKey('lab-1'))).toBe(
      JSON.stringify({ currentPagePath: 'pages/a-first.adoc', panelMode: 'open' }),
    );
  });

  test.each([
    ['null', { currentPagePath: null, panelMode: 'open' }],
    ['missing', { panelMode: 'open' }],
    ['invalid', { currentPagePath: 'pages/missing.adoc', panelMode: 'minimized' }],
  ])('discards %s stored page and falls back to entry page', async (_kind, storedSession) => {
    mockLoadLabBundle.mockResolvedValue(noChartBundle);
    localStorage.setItem(sessionKey('lab-1'), JSON.stringify(storedSession));
    await renderCatalog();

    await act(async () => { getButton(/^Launch$/).click(); });

    expect(container.textContent).toContain('Page 1 of 2');
    expect(localStorage.getItem(sessionKey('lab-1'))).toBe(
      JSON.stringify({ currentPagePath: 'pages/a-first.adoc', panelMode: 'open' }),
    );
  });

  test('shows empty state when catalog returns no items', async () => {
    mockListCatalog.mockResolvedValue(emptyCatalog);

    await act(async () => {
      ReactDOM.render(<InteractiveLabsPage />, container);
    });

    expect(container.textContent).toMatch(/no labs/i);
    expect(container.querySelector('[role="progressbar"]')).toBeNull();
  });

  test('shows LabError message in alert when request rejects', async () => {
    mockListCatalog.mockRejectedValue(sampleError);

    await act(async () => {
      ReactDOM.render(<InteractiveLabsPage />, container);
    });

    expect(container.textContent).toContain('Catalog service unavailable');
    expect(container.querySelector('[role="progressbar"]')).toBeNull();
    expect(
      container.querySelector('[role="alert"]') ??
      container.querySelector('[aria-live]'),
    ).not.toBeNull();
  });

  test('retry re-calls listCatalog and replaces error with catalog on success', async () => {
    mockListCatalog
      .mockRejectedValueOnce(sampleError)
      .mockResolvedValueOnce({ ...sampleCatalog });

    await act(async () => {
      ReactDOM.render(<InteractiveLabsPage />, container);
    });

    const retryBtn = Array.from(container.querySelectorAll('button')).find(
      b => b.textContent?.toLowerCase().includes('retry'),
    );
    expect(retryBtn).not.toBeNull();

    await act(async () => { retryBtn!.click(); });

    expect(container.textContent).toContain('Lab One');
    expect(mockListCatalog).toHaveBeenCalledTimes(2);
  });

  test('does not update state after unmount (unmount safety)', async () => {
    let resolveList!: (v: CatalogList) => void;
    mockListCatalog.mockReturnValue(new Promise<CatalogList>(r => { resolveList = r; }));

    await act(async () => {
      ReactDOM.render(<InteractiveLabsPage />, container);
    });

    const errorSpy = jest.spyOn(console, 'error').mockImplementation(() => { /* suppress */ });

    act(() => { ReactDOM.unmountComponentAtNode(container); });

    // Resolve after unmount; cancelled flag must prevent setState call
    await act(async () => { resolveList(emptyCatalog); });

    const calls = errorSpy.mock.calls.map(c => String(c[0]));
    const stateUpdateWarning = calls.find(m =>
      m.includes('unmounted') || m.includes('setState') || m.includes('memory leak'),
    );
    expect(stateUpdateWarning).toBeUndefined();

    errorSpy.mockRestore();
  });
});
