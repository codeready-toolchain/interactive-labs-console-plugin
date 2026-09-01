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

jest.mock('./api/client');

const mockListCatalog = client.listCatalog as jest.MockedFunction<typeof client.listCatalog>;
const mockLoadLabBundle = client.loadLabBundle as jest.MockedFunction<typeof client.loadLabBundle>;
const mockSetupLab = client.setupLab as jest.MockedFunction<typeof client.setupLab>;
const mockGetNavigablePagePaths = client.getNavigablePagePaths as jest.MockedFunction<
  typeof client.getNavigablePagePaths
>;

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
    Object.keys(pages)
      .filter((pagePath) => pagePath.split('/').every((segment) => !segment.startsWith('_')))
      .sort(),
  );
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
    { catalogRef: 'lab-1', displayName: 'Lab One', description: 'First lab description' },
    { catalogRef: 'lab-2', displayName: 'Lab Two' },
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
  pages: {
    'pages/z-last.adoc': '= Last',
    '_partials/shared.adoc': 'Include only',
    'pages/a-first.adoc': '= First',
  },
  runtimeContext: { catalogRef: 'lab-1', branch: 'main', helmChartAvailable: false },
};

const chartBundle: LabBundle = {
  ...noChartBundle,
  runtimeContext: { ...noChartBundle.runtimeContext, helmChartAvailable: true },
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

    expect(container.textContent).toContain('pages/a-first.adoc');
    expect(container.textContent).not.toContain('pages/z-last.adoc');
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

  test('uses native controls, moves focus into drawer, and restores it after close', async () => {
    mockLoadLabBundle.mockResolvedValue(noChartBundle);
    await renderCatalog();

    const launch = getButton(/^Launch$/);
    await act(async () => { launch.click(); });

    const minimize = getButton(/minimize/i);
    const close = getButton(/close/i);
    const reset = getButton(/reset/i);
    [launch, minimize, close, reset].forEach((button) => {
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
    act(() => { close.click(); });
    expect(document.activeElement).toBe(launch);
  });

  test('keeps overlay in root provider across child changes and requires explicit relaunch after close', async () => {
    mockLoadLabBundle.mockResolvedValue(noChartBundle);
    await renderCatalog();

    await act(async () => { getButton(/^Launch$/).click(); });
    expect(container.textContent).toContain('pages/a-first.adoc');

    await act(async () => {
      ReactDOM.render(
        <InteractiveLabsProvider>
          <p>Different Console page</p>
        </InteractiveLabsProvider>,
        container,
      );
    });
    expect(container.textContent).toContain('Different Console page');
    expect(container.textContent).toContain('pages/a-first.adoc');

    act(() => { getButton(/minimize/i).click(); });
    expect(container.textContent).not.toContain('pages/a-first.adoc');
    expect(container.querySelector('button[aria-label="Minimize lab"]')).toBeNull();
    expect(container.querySelector('button[aria-label="Close lab"]')).toBeNull();
    const restore = container.querySelector('button[aria-label="Restore lab"]') as HTMLButtonElement;
    expect(restore).not.toBeNull();
    expect(restore.textContent).toBe("");
    expect(restore.querySelector("svg")).not.toBeNull();
    expect(restore.style.width).toBe("1.5rem");
    expect(restore.style.height).toBe("5rem");

    act(() => { restore.click(); });
    expect(container.textContent).toContain('pages/a-first.adoc');

    act(() => { getButton(/close/i).click(); });
    expect(container.textContent).not.toContain('pages/a-first.adoc');
    expect(container.querySelector('button[aria-label="Minimize lab"]')).toBeNull();
    expect(container.querySelector('button[aria-label="Close lab"]')).toBeNull();
    expect(container.querySelector('button[aria-label="Restore lab"]')).toBeNull();
    expect(mockLoadLabBundle).toHaveBeenCalledTimes(1);

    await renderCatalog();
    expect(container.textContent).not.toContain('pages/a-first.adoc');
    expect(mockLoadLabBundle).toHaveBeenCalledTimes(1);

    await act(async () => { getButton(/^Launch$/).click(); });
    expect(mockLoadLabBundle).toHaveBeenCalledTimes(2);
    expect(container.textContent).toContain('pages/a-first.adoc');
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
    expect(container.textContent).not.toContain('pages/a-first.adoc');

    await act(async () => { getButton(/^Launch$/).click(); });
    expect(mockLoadLabBundle).toHaveBeenCalledTimes(2);
    expect(container.textContent).toContain('pages/a-first.adoc');
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
    expect(container.textContent).toContain('pages/z-last.adoc');
  });

  test('discards malformed stored session and starts at entry page', async () => {
    mockLoadLabBundle.mockResolvedValue(noChartBundle);
    localStorage.setItem(sessionKey('lab-1'), '{not json');
    await renderCatalog();

    await act(async () => { getButton(/^Launch$/).click(); });

    expect(container.textContent).toContain('pages/a-first.adoc');
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

    expect(container.textContent).toContain('pages/a-first.adoc');
    expect(localStorage.getItem(sessionKey('lab-1'))).toBe(
      JSON.stringify({ currentPagePath: 'pages/a-first.adoc', panelMode: 'open' }),
    );
  });

  test('reset removes only Interactive Labs session keys and makes no backend mutation', async () => {
    mockLoadLabBundle.mockResolvedValue(noChartBundle);
    localStorage.setItem(sessionKey('lab-1'), JSON.stringify({ currentPagePath: null, panelMode: 'open' }));
    localStorage.setItem(sessionKey('lab-2'), JSON.stringify({ currentPagePath: null, panelMode: 'open' }));
    localStorage.setItem('unrelated:session', 'keep');
    await renderCatalog();

    await act(async () => { getButton(/^Launch$/).click(); });
    act(() => { getButton(/reset/i).click(); });

    expect(localStorage.getItem(sessionKey('lab-1'))).toBeNull();
    expect(localStorage.getItem(sessionKey('lab-2'))).toBeNull();
    expect(localStorage.getItem('unrelated:session')).toBe('keep');
    expect(mockLoadLabBundle).toHaveBeenCalledTimes(1);
    expect(mockSetupLab).not.toHaveBeenCalled();
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
