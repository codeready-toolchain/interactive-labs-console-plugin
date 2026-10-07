/**
 * STORY-2 tests — List Available Labs
 * Tests use React 17 react-dom/test-utils; no Testing Library.
 */

import { readFileSync } from 'node:fs';
import * as React from 'react';
import * as ReactDOM from 'react-dom';
import { act } from 'react-dom/test-utils';
import { InteractiveLabsPage, InteractiveLabsProvider } from './InteractiveLabsPage';
import type { CatalogList } from './types/catalog';
import type { LabError } from './types/errors';
import type { LabBundle } from './types/manifest';
import type { LabSetup, SetupStatus } from './types/setup';
import * as client from './api/client';
import { extractAsciiDocTitle, renderAsciiDoc } from './renderer';

jest.mock('./api/client');
jest.mock('./renderer', () => ({ renderAsciiDoc: jest.fn(), extractAsciiDocTitle: jest.fn() }));
const mockNavigate = jest.fn();
let mockLocation = { pathname: '/interactive-labs', search: '' };
jest.mock('react-router-dom-v5-compat', () => ({
  useNavigate: () => mockNavigate,
  useLocation: () => mockLocation,
}));

const mockListCatalog = client.listCatalog as jest.MockedFunction<typeof client.listCatalog>;
const mockLoadLabBundle = client.loadLabBundle as jest.MockedFunction<typeof client.loadLabBundle>;
const mockSetupLab = client.setupLab as jest.MockedFunction<typeof client.setupLab>;
const mockGetSetupStatus = client.getSetupStatus as jest.MockedFunction<typeof client.getSetupStatus>;
const mockFetchResourceText = client.fetchResourceText as jest.MockedFunction<typeof client.fetchResourceText>;
const mockRenderAsciiDoc = renderAsciiDoc as jest.MockedFunction<typeof renderAsciiDoc>;
const mockExtractAsciiDocTitle = extractAsciiDocTitle as jest.MockedFunction<typeof extractAsciiDocTitle>;

const SESSION_PREFIX = 'interactive-labs-console-plugin:session:';

let container: HTMLDivElement;

beforeEach(() => {
  container = document.createElement('div');
  document.body.appendChild(container);
  mockListCatalog.mockReset();
  (client.listSetups as jest.MockedFunction<typeof client.listSetups>).mockReset();
  (client.listSetups as jest.MockedFunction<typeof client.listSetups>).mockResolvedValue({ apiVersion: 'v1alpha1', kind: 'SetupList', items: [] });
  (client.teardownLab as jest.MockedFunction<typeof client.teardownLab>).mockReset();
  (client.getTeardownStatus as jest.MockedFunction<typeof client.getTeardownStatus>).mockReset();
  sessionStorage.removeItem('interactive-labs-teardown');
  mockLoadLabBundle.mockReset();
  mockSetupLab.mockReset();
  mockGetSetupStatus.mockReset();
  mockFetchResourceText.mockReset();
  mockFetchResourceText.mockResolvedValue('= Page');
  mockRenderAsciiDoc.mockReset();
  mockRenderAsciiDoc.mockImplementation(() => new Promise<never>(() => {}));
  mockExtractAsciiDocTitle.mockReset();
  mockExtractAsciiDocTitle.mockResolvedValue(undefined);
  mockNavigate.mockReset();
  mockLocation = { pathname: '/interactive-labs', search: '' };
  localStorage.clear();
  document.querySelectorAll('[data-test-guidance-target]').forEach((target) => target.remove());
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
    { catalogRef: 'lab-1', title: 'Lab One', description: 'First lab description', labPath: 'labs/one' },
    { catalogRef: 'lab-2', title: 'Lab Two', labPath: 'labs/two' },
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
    metadata: { title: 'Lab One' },
    spec: {},
  },
  modules: [{ id: '010-intro', pages: [
    { path: 'modules/010-intro/content/pages/a-first.adoc', url: '/api/proxy/plugin/interactive-labs-console-plugin/backend/api/v1alpha1/labs/lab-1/resources/labs/one/modules/010-intro/content/pages/a-first.adoc' },
    { path: 'modules/010-intro/content/pages/z-last.adoc', url: '/api/proxy/plugin/interactive-labs-console-plugin/backend/api/v1alpha1/labs/lab-1/resources/labs/one/modules/010-intro/content/pages/z-last.adoc' },
  ] }],
  runtimeContext: { catalogRef: 'lab-1', branch: 'main', helmChartAvailable: false },
};

const chartBundle: LabBundle = {
  ...noChartBundle,
  runtimeContext: { ...noChartBundle.runtimeContext, helmChartAvailable: true },
};

const proxyNoChartBundle: LabBundle = noChartBundle;
const firstPage = noChartBundle.modules[0].pages[0];
const lastPage = noChartBundle.modules[0].pages[1];
const multiModuleBundle: LabBundle = {
  ...noChartBundle,
  modules: [
    noChartBundle.modules[0],
    { id: '020-apply', pages: [
      { path: 'modules/020-apply/content/010-do.adoc', url: '/api/proxy/plugin/interactive-labs-console-plugin/backend/api/v1alpha1/labs/lab-1/resources/labs/one/modules/020-apply/content/010-do.adoc' },
      { path: 'modules/020-apply/content/020-done.adoc', url: '/api/proxy/plugin/interactive-labs-console-plugin/backend/api/v1alpha1/labs/lab-1/resources/labs/one/modules/020-apply/content/020-done.adoc' },
    ] },
  ],
};

const sessionKey = (catalogRef: string) => `${SESSION_PREFIX}${catalogRef}`;

const getButton = (name: RegExp): HTMLButtonElement => {
  const button = Array.from(container.querySelectorAll('button')).find((candidate) =>
    name.test(`${candidate.getAttribute('aria-label') ?? ''} ${candidate.textContent ?? ''}`.trim()),
  ) as HTMLButtonElement | undefined;
  if (!button) throw new Error(`Missing button matching ${name}`);
  return button;
};

const moduleToggle = (): HTMLButtonElement => {
  const toggle = container.querySelector<HTMLButtonElement>('button[aria-label^="Module: "]');
  if (!toggle) throw new Error('Missing Module toggle');
  return toggle;
};

const moduleOptions = (): HTMLButtonElement[] =>
  Array.from(document.querySelectorAll<HTMLButtonElement>('[role="listbox"] [role="option"]'));

const deferred = <T,>() => {
  let resolve!: (value: T) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, resolve, reject };
};

const renderCatalog = async (catalog: CatalogList = sampleCatalog) => {
  mockListCatalog.mockResolvedValue(catalog);
  await act(async () => {
    ReactDOM.render(
      <InteractiveLabsProvider>
        <InteractiveLabsPage />
      </InteractiveLabsProvider>,
      container,
    );
  });
};

const guidanceTarget = (id: string, rect = { left: 10, right: 110, top: 10, bottom: 40 }): HTMLElement => {
  const target = document.createElement('button');
  target.dataset.quickstartId = id;
  target.dataset.testGuidanceTarget = '';
  target.getBoundingClientRect = () => ({ ...rect, width: rect.right - rect.left, height: rect.bottom - rect.top, x: rect.left, y: rect.top, toJSON: () => ({}) }) as DOMRect;
  document.body.appendChild(target);
  return target;
};

const launchGuidanceLab = async (html: string, trusted = true): Promise<void> => {
  const template = document.createElement('template');
  template.innerHTML = html;
  const guidanceLinks = Array.from(template.content.querySelectorAll<HTMLAnchorElement>('[data-interactive-labs-guidance-id]'))
    .map((link, index) => {
      const token = `guidance-${index}`;
      link.dataset.interactiveLabsGuidanceToken = token;
      return { id: link.dataset.interactiveLabsGuidanceId!, href: link.getAttribute('href')!, token };
    });
  mockLoadLabBundle.mockResolvedValue(noChartBundle);
  mockRenderAsciiDoc.mockImplementation(({ sourceURL }) => Promise.resolve({
    html: sourceURL.endsWith('/lab.yaml') ? '<p>Description</p>' : template.innerHTML,
    warnings: [],
    guidanceLinks: trusted ? guidanceLinks : [],
  }));
  await renderCatalog();
  await act(async () => { getButton(/^Launch$/).click(); });
};

const renderPageCatalog = renderCatalog;
describe('catalog setup actions', () => {
  const ref = 'ab'.repeat(32);
  const otherRef = 'ef'.repeat(32);
  const setupCatalog: CatalogList = { ...sampleCatalog, items: sampleCatalog.items.map((entry, index) => ({ ...entry, catalogRef: index === 0 ? ref : otherRef })) };
  const renderCatalog = (catalog = setupCatalog) => renderPageCatalog(catalog);
  const chartBundle: LabBundle = {
    ...noChartBundle,
    runtimeContext: { ...noChartBundle.runtimeContext, catalogRef: ref, helmChartAvailable: true },
    modules: noChartBundle.modules.map((module) => ({ ...module, pages: module.pages.map((page) => ({ ...page, url: page.url.replace('/labs/lab-1/', `/labs/${ref}/`) })) })),
  };
  const firstPage = chartBundle.modules[0].pages[0];
  const inventory = client.listSetups as jest.MockedFunction<typeof client.listSetups>;
  const teardown = client.teardownLab as jest.MockedFunction<typeof client.teardownLab>;
  const setup: LabSetup = {
    catalogRef: ref, generation: 'cd'.repeat(32), status: 'deployed', spaceRequests: ['lab-sr'],
    release: { name: 'il-one' },
  };
  const ready: SetupStatus = {
    apiVersion: 'v1alpha1', kind: 'SetupStatus',
    spaceRequests: { status: 'ready', namespaces: {} },
    helm: { status: 'deployed', releaseName: 'il-one' },
    attributes: { 'lab-namespace': 'runtime-ns' },
  };
  const pending: SetupStatus = {
    ...ready, attributes: undefined,
    spaceRequests: { status: 'provisioning', namespaces: null },
    helm: { status: 'pending', releaseName: 'il-one' },
  };
  const setInventory = (items: LabSetup[]): void => {
    inventory.mockResolvedValue({ apiVersion: 'v1alpha1', kind: 'SetupList', items });
  };
  const row = (): HTMLElement => container.querySelector(`#lab-${ref}`)!.closest<HTMLElement>('.pf-v6-c-data-list__item-row')!;
  const actions = (): HTMLElement => {
    const action = row().querySelector<HTMLElement>('.pf-v6-c-data-list__item-action');
    if (!action) throw new Error('Missing catalog actions');
    return action;
  };
  const rowButton = (name: string): HTMLButtonElement => {
    const button = Array.from(actions().querySelectorAll('button')).find((candidate) => candidate.textContent?.trim() === name);
    if (!button) throw new Error(`Missing catalog action ${name}`);
    return button;
  };
  const filterBy = async (value: string): Promise<void> => {
    const input = container.querySelector<HTMLInputElement>('input[aria-label="Filter labs"]')!;
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(input, value);
      input.dispatchEvent(new Event('input', { bubbles: true }));
    });
  };

  test('given interleaved catalog entries, when inventory loads, then one list stably places every existing setup before inactive entries', async () => {
    const entries = ['inactive-one', 'failed', 'pending', 'inactive-two', 'deployed', 'remnants'].map((catalogRef) => ({ catalogRef, title: catalogRef, labPath: catalogRef }));
    setInventory([
      { ...setup, catalogRef: 'deployed' },
      { ...setup, catalogRef: 'remnants', release: undefined },
      { ...setup, catalogRef: 'failed', status: 'failed' },
      { ...setup, catalogRef: 'pending', status: 'pending', blocked: true },
    ]);

    await renderCatalog({ ...setupCatalog, items: entries });

    const lists = container.querySelectorAll('.pf-v6-c-data-list');
    expect(lists).toHaveLength(1);
    const list = lists[0];
    expect(list.tagName).toBe('UL');
    expect(Array.from(list.children).map((child) => child.querySelector('h2')?.id)).toEqual([
      'lab-failed', 'lab-pending', 'lab-deployed', 'lab-remnants', 'lab-inactive-one', 'lab-inactive-two',
    ]);
    expect(Array.from(list.children).every((child) => child.classList.contains('pf-v6-c-data-list__item'))).toBe(true);
    expect(container.querySelector('section[aria-label="Your lab setups"]')).toBeNull();
    expect(Array.from(list.querySelectorAll('h2')).map((heading) => heading.textContent)).toEqual([
      'failed', 'pending', 'deployed', 'remnants', 'inactive-one', 'inactive-two',
    ]);
    const order = list.innerHTML;
    await act(async () => { getButton(/Refresh setups/).click(); });
    expect(list.innerHTML).toBe(order);
    expect(list.contains(getButton(/Refresh setups/))).toBe(false);
  });

  test('unprepared catalog row shows only Launch', async () => {
    await renderCatalog();
    expect(Array.from(actions().querySelectorAll('button')).map((button) => button.textContent?.trim())).toEqual(['Launch']);
    expect(rowButton('Launch').disabled).toBe(false);
    expect(container.textContent).not.toContain('No lab setups found.');
  });

  test.each(['pending', 'deployed', 'failed'] as const)('%s setup replaces Launch with stacked Open and Tear down', async (status) => {
    setInventory([{ ...setup, status, blocked: status === 'pending' }]);
    await renderCatalog();
    expect(Array.from(actions().querySelectorAll('button')).map((button) => button.textContent?.trim())).toEqual(['Open', 'Tear down']);
    expect(rowButton('Open').disabled).toBe(status === 'failed');
    expect(rowButton('Tear down').disabled).toBe(status === 'pending');
    expect(rowButton('Open').querySelector('[role="progressbar"]') !== null).toBe(status === 'pending');
    const stack = rowButton('Open').parentElement!;
    expect(stack.contains(rowButton('Tear down'))).toBe(true);
    expect(stack.style.flexDirection).toBe('column');
    expect(container.querySelectorAll('section[aria-label="Your lab setups"] li')).toHaveLength(0);
  });

  test.each(['deployed', 'failed'] as const)('blocked %s setup keeps Tear down visible but disabled with recovery text', async (status) => {
    setInventory([{ ...setup, status, blocked: true }]);
    await renderCatalog();
    expect(rowButton('Tear down').disabled).toBe(true);
    expect(row().textContent).toMatch(/administrator/i);
    expect(teardown).not.toHaveBeenCalled();
  });

  test.each(['helm-hook', 'helm-retained', 'helm-manifest'] as const)('unsafe-to-delete deployed release with %s remains openable via GET', async (blockedReason) => {
    setInventory([{ ...setup, blocked: true, blockedReason }]);
    mockLoadLabBundle.mockResolvedValue(chartBundle);
    mockGetSetupStatus.mockResolvedValue(ready);
    await renderCatalog();
    expect(rowButton('Open').disabled).toBe(false);
    expect(rowButton('Tear down').disabled).toBe(true);
    await act(async () => { rowButton('Open').click(); });
    expect(mockGetSetupStatus).toHaveBeenCalledWith(ref, expect.any(AbortSignal));
    expect(mockSetupLab).not.toHaveBeenCalled();
    expect(teardown).not.toHaveBeenCalled();
  });

  test.each(['pending', 'deployed', 'failed'] as const)('%s setup keeps catalog buttons equally sized independently of recovery text', async (status) => {
    setInventory([{ ...setup, status, blocked: true }]);
    await renderCatalog();
    const stack = rowButton('Open').parentElement!;
    expect(stack.classList.contains('pfext-lab-catalog-actions')).toBe(true);
    expect(Array.from(stack.querySelectorAll(':scope > button'))).toEqual([rowButton('Open'), rowButton('Tear down')]);
    expect(stack.querySelector(':scope > p')?.textContent).toMatch(/recovery|administrator/i);
    const css = readFileSync(`${__dirname}/interactive-labs.css`, 'utf8');
    const buttons = css.match(/\.pfext-lab-catalog-actions\s*>\s*button\s*\{([^}]*)\}/)?.[1];
    expect(buttons).toContain('inline-size: 9rem;');
    expect(buttons).toContain('max-inline-size: 100%;');
    expect(buttons).toContain('margin-inline-start: auto;');
  });

  test('catalog row explains held operation rather than pending status', async () => {
    const blockedSetup = { ...setup, status: 'pending' as const, blocked: true, blockedReason: 'operation' as const };
    setInventory([blockedSetup]);
    await renderCatalog();
    expect(rowButton('Tear down').disabled).toBe(true);
    expect(row().textContent).toContain('A lab operation is in progress or its reservation cannot be verified.');
    expect(row().textContent).toContain('Wait for completion; interrupted operations become recoverable after the reservation expires.');
    expect(row().textContent).not.toContain('Setup is pending.');
    expect(teardown).not.toHaveBeenCalled();
  });

  test.each([true, false])('pending held operation with release=%s permits Open to resume GET without setup POST', async (hasRelease) => {
    setInventory([{ ...setup, status: 'pending', blocked: true, blockedReason: 'operation', release: hasRelease ? setup.release : undefined }]);
    mockLoadLabBundle.mockResolvedValue(chartBundle);
    mockGetSetupStatus.mockImplementation(() => new Promise(() => {}));
    await renderCatalog();
    expect(rowButton('Open').disabled).toBe(false);
    expect(rowButton('Open').querySelector('[role="progressbar"]')).not.toBeNull();
    expect(rowButton('Tear down').disabled).toBe(true);
    expect(row().textContent).not.toContain('Opening is unavailable');
    await act(async () => { rowButton('Open').click(); });
    expect(mockGetSetupStatus).toHaveBeenCalledTimes(1);
    expect(mockSetupLab).not.toHaveBeenCalled();
    expect(getButton(/^Launch$/).disabled).toBe(false);
  });

  test.each([true, false])('pending cleanup with release=%s blocks Open and catalog page links', async (hasRelease) => {
    setInventory([{ ...setup, status: 'pending', blocked: true, blockedReason: 'cleanup', release: hasRelease ? setup.release : undefined }]);
    mockRenderAsciiDoc.mockResolvedValue({ html: `<p><a href="${firstPage.url}">Lab page</a></p>`, warnings: [] });
    await renderCatalog();
    expect(rowButton('Open').disabled).toBe(true);
    expect(rowButton('Open').querySelector('[role="progressbar"]')).toBeNull();
    expect(row().textContent).toContain('Lab namespace cleanup is still in progress.');
    expect(row().textContent).not.toContain('Lab setup pending');
    await act(async () => {
      rowButton('Open').click();
      row().querySelector('a')!.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }));
    });
    expect(row().textContent).toContain('Lab cannot be opened until its setup is available.');
    expect(mockLoadLabBundle).not.toHaveBeenCalled();
    expect(mockGetSetupStatus).not.toHaveBeenCalled();
    expect(mockSetupLab).not.toHaveBeenCalled();
    expect(getButton(/^Launch$/).disabled).toBe(false);
  });

  test('deployed lab with held operation cannot be opened during teardown in another tab', async () => {
    setInventory([{ ...setup, status: 'deployed', blocked: true, blockedReason: 'operation' }]);
    mockRenderAsciiDoc.mockResolvedValue({ html: `<p><a href="${firstPage.url}">Lab page</a></p>`, warnings: [] });
    await renderCatalog();
    expect(rowButton('Open').disabled).toBe(true);
    await act(async () => {
      rowButton('Open').click();
      row().querySelector('a')!.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }));
    });
    expect(row().textContent).toContain('Lab cannot be opened until its setup is available.');
    expect(mockLoadLabBundle).not.toHaveBeenCalled();
    expect(mockGetSetupStatus).not.toHaveBeenCalled();
  });

  test('ordinary pending setup without release still permits Open to resume GET polling', async () => {
    setInventory([{ ...setup, release: undefined, status: 'pending', blocked: true, blockedReason: 'pending' }]);
    mockLoadLabBundle.mockResolvedValue(chartBundle);
    mockGetSetupStatus.mockImplementation(() => new Promise(() => {}));
    await renderCatalog();
    expect(rowButton('Open').disabled).toBe(false);
    expect(rowButton('Open').querySelector('[role="progressbar"]')).not.toBeNull();
    await act(async () => { rowButton('Open').click(); });
    expect(mockGetSetupStatus).toHaveBeenCalledTimes(1);
    expect(mockSetupLab).not.toHaveBeenCalled();
  });

  test('deployed Open fetches runtime attributes without setup POST, and Close never tears down', async () => {
    setInventory([setup]);
    mockLoadLabBundle.mockResolvedValue({ ...chartBundle, manifest: { ...chartBundle.manifest, spec: { attributes: { author: 'value', 'lab-namespace': 'old' } } } });
    mockGetSetupStatus.mockResolvedValue(ready);
    mockRenderAsciiDoc.mockResolvedValue({ html: '<p>Existing environment</p>', warnings: [] });
    await renderCatalog();
    await act(async () => { rowButton('Open').click(); });
    expect(mockLoadLabBundle).toHaveBeenCalledWith(ref, 'labs/one', expect.any(AbortSignal));
    expect(mockGetSetupStatus).toHaveBeenCalled();
    expect(mockSetupLab).not.toHaveBeenCalled();
    const renderedPage = mockRenderAsciiDoc.mock.calls.find(([input]) => input.sourceURL === firstPage.url);
    expect(renderedPage?.[0].attributes).toEqual({ author: 'value', 'lab-namespace': 'runtime-ns' });
    expect(container.querySelector('#interactive-labs-drawer-panel')?.textContent).toContain('Existing environment');
    await act(async () => { getButton(/Close lab/).click(); });
    expect(teardown).not.toHaveBeenCalled();
  });

  test('pending Open resumes GET polling and enables teardown when setup becomes deployed', async () => {
    jest.useFakeTimers();
    try {
      setInventory([{ ...setup, status: 'pending', blocked: true }]);
      mockLoadLabBundle.mockResolvedValue(chartBundle);
      mockGetSetupStatus.mockResolvedValueOnce(pending).mockResolvedValueOnce(ready);
      mockRenderAsciiDoc.mockResolvedValue({ html: '<p>Ready environment</p>', warnings: [] });
      await renderCatalog();
      expect(rowButton('Open').disabled).toBe(false);
      await act(async () => { rowButton('Open').click(); });
      expect(container.querySelector('#interactive-labs-drawer-panel')?.textContent).toContain('Setting up your lab environment...');
      expect(mockGetSetupStatus).toHaveBeenCalledTimes(1);
      expect(mockSetupLab).not.toHaveBeenCalled();
      expect(rowButton('Tear down').disabled).toBe(true);
      setInventory([setup]);
      await act(async () => { jest.advanceTimersByTime(3000); });
      expect(mockGetSetupStatus).toHaveBeenCalledTimes(2);
      expect(inventory.mock.calls.length).toBeGreaterThan(1);
      expect(rowButton('Tear down').disabled).toBe(false);
      expect(rowButton('Open').querySelector('[role="progressbar"]')).toBeNull();
      expect(container.querySelector('#interactive-labs-drawer-panel')?.textContent).toContain('Ready environment');
      expect(mockSetupLab).not.toHaveBeenCalled();
    } finally {
      jest.useRealTimers();
    }
  });

  test('pending Open retries setup once through the client when GET explicitly allows retry, then polls until deployed', async () => {
    jest.useFakeTimers();
    try {
      const retryable = { ...pending, retrySetup: true };
      setInventory([{ ...setup, release: undefined, status: 'pending', blocked: true, blockedReason: 'pending' }]);
      mockLoadLabBundle.mockResolvedValue(chartBundle);
      mockGetSetupStatus.mockResolvedValueOnce(retryable).mockResolvedValue(pending);
      mockSetupLab.mockResolvedValue(pending);
      mockRenderAsciiDoc.mockResolvedValue({ html: '<p>Recovered environment</p>', warnings: [] });
      await renderCatalog();
      await act(async () => { rowButton('Open').click(); });
      expect(mockSetupLab).toHaveBeenCalledTimes(1);
      expect(mockSetupLab).toHaveBeenCalledWith(ref, expect.any(AbortSignal));
      expect(mockGetSetupStatus.mock.invocationCallOrder[0]).toBeLessThan(mockSetupLab.mock.invocationCallOrder[0]);
      expect(container.querySelector('#interactive-labs-drawer-panel')?.textContent).toContain('Setting up your lab environment...');
      const initialPolls = mockGetSetupStatus.mock.calls.length;
      await act(async () => { jest.advanceTimersByTime(3000); });
      expect(mockGetSetupStatus.mock.calls.length).toBeGreaterThan(initialPolls);
      expect(mockSetupLab).toHaveBeenCalledTimes(1);
      mockGetSetupStatus.mockResolvedValue(ready);
      setInventory([setup]);
      await act(async () => { jest.advanceTimersByTime(3000); });
      expect(container.querySelector('#interactive-labs-drawer-panel')?.textContent).toContain('Recovered environment');
      expect(rowButton('Tear down').disabled).toBe(false);
      expect(rowButton('Open').querySelector('[role="progressbar"]')).toBeNull();
      expect(mockSetupLab).toHaveBeenCalledTimes(1);
    } finally {
      jest.useRealTimers();
    }
  });

  test('Open on the same pending lab preserves its in-flight setup POST and waits to poll', async () => {
    const post = deferred<Awaited<ReturnType<typeof client.setupLab>>>();
    let postSignal!: AbortSignal;
    mockLoadLabBundle.mockResolvedValue(chartBundle);
    mockSetupLab.mockImplementation((_catalogRef, signal) => {
      postSignal = signal!;
      return post.promise;
    });
    mockGetSetupStatus.mockResolvedValue(ready);
    mockRenderAsciiDoc.mockResolvedValue({ html: '<p>Ready environment</p>', warnings: [] });
    await renderCatalog();
    await act(async () => { rowButton('Launch').click(); });
    expect(mockSetupLab).toHaveBeenCalledTimes(1);
    expect(postSignal.aborted).toBe(false);

    try {
      setInventory([{ ...setup, status: 'pending', blocked: true }]);
      await act(async () => { getButton(/Refresh setups/).click(); });
      expect(rowButton('Open').disabled).toBe(false);
      await act(async () => { rowButton('Open').click(); });

      expect(postSignal.aborted).toBe(false);
      expect(mockSetupLab).toHaveBeenCalledTimes(1);
      expect(container.querySelector('#interactive-labs-drawer-panel')?.textContent).toContain('Setting up your lab environment...');
      expect(mockGetSetupStatus).not.toHaveBeenCalled();

      setInventory([setup]);
      await act(async () => { post.resolve(pending); });
      expect(postSignal.aborted).toBe(false);
      expect(mockSetupLab).toHaveBeenCalledTimes(1);
      expect(mockGetSetupStatus).toHaveBeenCalledTimes(1);
      expect(container.querySelector('#interactive-labs-drawer-panel')?.textContent).toContain('Ready environment');
    } finally {
      await act(async () => { post.resolve(pending); });
    }
  });

  test('new launch refreshes accepted pending inventory, then refreshes successful setup', async () => {
    const final = deferred<SetupStatus>();
    mockLoadLabBundle.mockResolvedValue(chartBundle);
    mockSetupLab.mockImplementation(async () => {
      setInventory([{ ...setup, status: 'pending', blocked: true }]);
      return pending;
    });
    mockGetSetupStatus.mockReturnValue(final.promise);
    await renderCatalog();
    await act(async () => { rowButton('Launch').click(); });
    expect(rowButton('Open').disabled).toBe(false);
    expect(rowButton('Tear down').disabled).toBe(true);
    setInventory([setup]);
    await act(async () => { final.resolve(ready); });
    expect(rowButton('Tear down').disabled).toBe(false);
    expect(rowButton('Open').querySelector('[role="progressbar"]')).toBeNull();
    expect(mockSetupLab).toHaveBeenCalledTimes(1);
  });

  test.each(['POST', 'status'] as const)('refreshes inventory after %s setup failure so retry cannot silently launch', async (failure) => {
    mockLoadLabBundle.mockResolvedValue(chartBundle);
    const fail = async (): Promise<never> => {
      setInventory([{ ...setup, status: 'failed' }]);
      throw new Error('Setup failed');
    };
    if (failure === 'POST') mockSetupLab.mockImplementation(fail);
    else {
      mockSetupLab.mockResolvedValue(pending);
      mockGetSetupStatus.mockImplementation(fail);
    }
    await renderCatalog();
    await act(async () => { rowButton('Launch').click(); });
    expect(rowButton('Open').disabled).toBe(true);
    expect(rowButton('Tear down').disabled).toBe(false);
    expect(container.querySelector('#interactive-labs-drawer-panel')?.textContent).toContain('Setup failed');
    expect(actions().querySelector('[role="alert"], [aria-live="polite"], [aria-live="assertive"]')?.textContent).toContain('Setup failed');
    expect(actions().querySelector('.pf-v6-c-alert')).toBeNull();
  });

  test('catalog teardown preserves confirmation targets and generation and returns row to Launch', async () => {
    const confirm = jest.spyOn(window, 'confirm').mockReturnValue(false);
    try {
      setInventory([setup]);
      await renderCatalog();
      await act(async () => { rowButton('Tear down').click(); });
      expect(confirm).toHaveBeenCalledWith(expect.stringContaining('Lab One'));
      expect(confirm).toHaveBeenCalledWith(expect.stringContaining('il-one'));
      expect(confirm).toHaveBeenCalledWith(expect.stringContaining('lab-sr'));
      expect(teardown).not.toHaveBeenCalled();
      confirm.mockReturnValue(true);
      setInventory([]);
      teardown.mockResolvedValue({ apiVersion: 'v1alpha1', kind: 'TeardownStatus', catalogRef: setup.catalogRef, operationId: '12'.repeat(16), status: 'running' });
      (client.getTeardownStatus as jest.MockedFunction<typeof client.getTeardownStatus>).mockResolvedValue({ apiVersion: 'v1alpha1', kind: 'TeardownStatus', catalogRef: setup.catalogRef, operationId: '12'.repeat(16), status: 'succeeded' });
      await act(async () => { rowButton('Tear down').click(); });
      expect(teardown).toHaveBeenCalledWith(setup.catalogRef, setup.generation);
      expect(rowButton('Launch').disabled).toBe(false);
      expect(actions().textContent).not.toContain('Managed lab cleanup completed.');
      expect(actions().textContent?.trim()).toBe('Launch');
      expect(getButton(/Refresh setups/).parentElement?.textContent).not.toContain('Managed lab cleanup completed.');
    } finally {
      confirm.mockRestore();
    }
  });

  test('catalog A and B tear down concurrently with row-local notices and direct equal-width buttons', async () => {
    const confirm = jest.spyOn(window, 'confirm').mockReturnValue(true);
    const getStatus = client.getTeardownStatus as jest.MockedFunction<typeof client.getTeardownStatus>;
    const firstRef = 'ab'.repeat(32);
    const secondRef = 'ef'.repeat(32);
    const firstSetup = { ...setup, catalogRef: firstRef };
    const secondSetup = { ...setup, catalogRef: secondRef, release: { name: 'il-two' } };
    const catalog = { ...setupCatalog, items: setupCatalog.items.map((item, index) => ({ ...item, catalogRef: index === 0 ? firstRef : secondRef })) };
    const status = (catalogRef: string) => ({ apiVersion: 'v1alpha1' as const, kind: 'TeardownStatus' as const, catalogRef, operationId: (catalogRef === firstRef ? '12' : '34').repeat(16), status: 'running' as const });
    const catalogActions = (catalogRef: string) => container.querySelector(`#lab-${catalogRef}`)!.closest('.pf-v6-c-data-list__item-row')!.querySelector('.pf-v6-c-data-list__item-action')!;
    try {
      setInventory([firstSetup, secondSetup]);
      teardown.mockImplementation(async (catalogRef) => status(catalogRef));
      getStatus.mockImplementation(() => new Promise(() => {}));
      await renderCatalog(catalog);
      const firstActions = catalogActions(firstRef);
      const secondActions = catalogActions(secondRef);
      const firstButtons = Array.from(firstActions.querySelectorAll('button'));
      const secondButtons = Array.from(secondActions.querySelectorAll('button'));
      await act(async () => { firstButtons[1].click(); });
      expect(firstButtons[0].disabled).toBe(true);
      expect(firstButtons[1].disabled).toBe(true);
      expect(secondButtons[0].disabled).toBe(false);
      expect(secondButtons[1].disabled).toBe(false);
      expect(getButton(/Refresh setups/).disabled).toBe(false);
      expect(firstActions.textContent).toContain('Lab teardown in progress');
      expect(secondActions.textContent).not.toContain('Lab teardown in progress');
      await act(async () => { secondButtons[1].click(); });
      expect(teardown).toHaveBeenCalledTimes(2);
      expect(secondActions.textContent).toContain('Lab teardown in progress');
      for (const action of [firstActions, secondActions]) {
        const stack = action.querySelector('.pfext-lab-catalog-actions')!;
        const buttons = Array.from(stack.querySelectorAll(':scope > button'));
        expect(buttons).toHaveLength(2);
        const notice = Array.from(stack.children).find((child) => child.textContent?.includes('Lab teardown in progress'));
        expect(notice).toBeDefined();
        expect(buttons[1].compareDocumentPosition(notice!) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
        expect(buttons.some((button) => button.contains(notice!))).toBe(false);
        expect(buttons[1].querySelector('[role="progressbar"]')).not.toBeNull();
        expect(notice?.querySelector('[role="progressbar"]')).toBeNull();
        expect(notice?.querySelector('[aria-label="Tearing down lab"]')).toBeNull();
        expect(stack.querySelector('.pf-v6-c-alert')).toBeNull();
      }
      expect(getButton(/Refresh setups/).parentElement?.textContent).not.toContain('Lab teardown in progress');
    } finally { confirm.mockRestore(); }
  });

  test('teardown terminal status keeps same-lab Open disabled until live remnant SpaceRequest disappears', async () => {
    const confirm = jest.spyOn(window, 'confirm').mockReturnValue(true);
    const getStatus = client.getTeardownStatus as jest.MockedFunction<typeof client.getTeardownStatus>;
    const firstRef = 'ab'.repeat(32);
    const firstSetup = { ...setup, catalogRef: firstRef };
    const catalog = { ...setupCatalog, items: [{ ...setupCatalog.items[0], catalogRef: firstRef }, setupCatalog.items[1]] };
    const terminal = deferred<Awaited<ReturnType<typeof client.getTeardownStatus>>>();
    const progress = { apiVersion: 'v1alpha1' as const, kind: 'TeardownStatus' as const, catalogRef: firstRef, operationId: '12'.repeat(16), status: 'running' as const };
    const labActions = () => container.querySelector(`#lab-${firstRef}`)!.closest('.pf-v6-c-data-list__item-row')!.querySelector('.pf-v6-c-data-list__item-action')!;
    const labButton = () => labActions().querySelector('button')!;
    try {
      setInventory([firstSetup]);
      teardown.mockResolvedValue(progress);
      getStatus.mockReturnValue(terminal.promise);
      await renderCatalog(catalog);
      await act(async () => { labActions().querySelectorAll('button')[1].click(); });
      expect(labButton().disabled).toBe(true);
      expect(getButton(/^Launch$/).disabled).toBe(false);
      setInventory([{ ...firstSetup, release: undefined, status: 'pending', blocked: true, blockedReason: 'cleanup', spaceRequests: ['terminating-sr'] }]);
      await act(async () => { terminal.resolve({ ...progress, status: 'succeeded' }); });
      expect(labButton().textContent).toBe('Open');
      expect(labButton().disabled).toBe(true);
      expect(labButton().querySelector('[role="progressbar"]')).toBeNull();
      expect(labActions().querySelectorAll('button')[1].querySelector('[role="progressbar"]')).toBeNull();
      expect(labActions().textContent).toContain('Lab namespace cleanup is still in progress. Wait for previous lab resources to disappear before opening or launching; contact an administrator if cleanup stalls.');
      expect(labActions().textContent).not.toContain('Waiting for previous lab resources to disappear.');
      expect(labActions().textContent).not.toContain('Checking setups automatically every 10 seconds.');
      expect(labActions().textContent).not.toContain('Managed lab cleanup completed.');
      expect(labActions().querySelector('.pf-v6-c-alert')).toBeNull();
      expect(labActions().textContent).not.toMatch(/namespace.*(gone|deleted)|SpaceRequest deletion requested/i);
      expect(getButton(/^Launch$/).disabled).toBe(false);
      await act(async () => { labButton().click(); });
      expect(mockLoadLabBundle).not.toHaveBeenCalled();
      setInventory([]);
      await act(async () => { getButton(/Refresh setups/).click(); });
      expect(labButton().textContent).toBe('Launch');
      expect(labButton().disabled).toBe(false);
      expect(labActions().textContent).not.toContain('Managed lab cleanup completed.');
      expect(labActions().textContent).not.toContain('Lab namespace cleanup is still in progress.');
      expect(labActions().textContent?.trim()).toBe('Launch');
      expect(labActions().querySelector('.pf-v6-c-alert')).toBeNull();
    } finally { confirm.mockRestore(); }
  });

  test('inventory alone blocks terminating cleanup after reload but permits a pending held setup', async () => {
    setInventory([{ ...setup, release: undefined, status: 'pending', blocked: true, blockedReason: 'cleanup', spaceRequests: ['terminating-sr'] }]);
    await renderCatalog();
    expect(rowButton('Open').disabled).toBe(true);
    expect(getButton(/^Launch$/).disabled).toBe(false);
    setInventory([{ ...setup, status: 'pending', blocked: true, blockedReason: 'operation' }]);
    await act(async () => { getButton(/Refresh setups/).click(); });
    expect(rowButton('Open').disabled).toBe(false);
    expect(getButton(/^Launch$/).disabled).toBe(false);
    expect(mockLoadLabBundle).not.toHaveBeenCalled();
  });

  test('receipt keeps Launch disabled when inventory disappears before worker terminal status', async () => {
    const confirm = jest.spyOn(window, 'confirm').mockReturnValue(true);
    const getStatus = client.getTeardownStatus as jest.MockedFunction<typeof client.getTeardownStatus>;
    const terminal = deferred<Awaited<ReturnType<typeof client.getTeardownStatus>>>();
    const progress = { apiVersion: 'v1alpha1' as const, kind: 'TeardownStatus' as const, catalogRef: setup.catalogRef, operationId: '12'.repeat(16), status: 'running' as const };
    try {
      setInventory([setup]);
      teardown.mockResolvedValue(progress);
      getStatus.mockReturnValue(terminal.promise);
      await renderCatalog();
      await act(async () => { rowButton('Tear down').click(); });
      setInventory([]);
      await act(async () => { getButton(/Refresh setups/).click(); });
      expect(rowButton('Launch').disabled).toBe(true);
      expect(actions().textContent).toContain('Lab teardown in progress');
      const refreshed = deferred<Awaited<ReturnType<typeof client.listSetups>>>();
      inventory.mockReturnValue(refreshed.promise);
      await act(async () => { terminal.resolve({ ...progress, status: 'succeeded' }); });
      expect(rowButton('Launch').disabled).toBe(true);
      await act(async () => { refreshed.resolve({ apiVersion: 'v1alpha1', kind: 'SetupList', items: [] }); });
      expect(rowButton('Launch').disabled).toBe(false);
      expect(actions().textContent).not.toContain('Managed lab cleanup completed.');
      expect(actions().textContent?.trim()).toBe('Launch');
    } finally { confirm.mockRestore(); }
  });

  test('backend cleanup conflict appears below affected row buttons while another lab stays usable', async () => {
    const message = 'Lab namespace cleanup is still in progress. Wait until the SpaceRequest disappears before relaunch.';
    mockLoadLabBundle.mockResolvedValue(chartBundle);
    mockSetupLab.mockRejectedValue({ ...sampleError, type: 'configuration_error', message });
    await renderCatalog();
    await act(async () => { rowButton('Launch').click(); });
    expect(actions().textContent).toContain(message);
    const notice = actions().querySelector('[role="alert"], [aria-live="polite"], [aria-live="assertive"]');
    expect(notice?.textContent).toContain(message);
    expect(rowButton('Launch').compareDocumentPosition(notice!) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    const otherActions = container.querySelector(`#lab-${otherRef}`)!.closest('.pf-v6-c-data-list__item-row')!.querySelector('.pf-v6-c-data-list__item-action')!;
    expect(otherActions.textContent).not.toContain(message);
    expect(otherActions.querySelector('button')!.disabled).toBe(false);
    expect(actions().querySelector('.pf-v6-c-alert')).toBeNull();
  });

  test('catalog teardown failure is unframed, announced and scoped below affected row buttons', async () => {
    const confirm = jest.spyOn(window, 'confirm').mockReturnValue(true);
    const progress = { apiVersion: 'v1alpha1' as const, kind: 'TeardownStatus' as const, catalogRef: setup.catalogRef, operationId: '12'.repeat(16), status: 'running' as const };
    try {
      setInventory([setup]);
      teardown.mockResolvedValue(progress);
      (client.getTeardownStatus as jest.MockedFunction<typeof client.getTeardownStatus>).mockResolvedValue({ ...progress, status: 'failed' });
      await renderCatalog();
      await act(async () => { rowButton('Tear down').click(); });
      const notice = actions().querySelector('[role="alert"], [aria-live="polite"], [aria-live="assertive"]');
      expect(notice?.textContent).toContain('Lab cleanup failed.');
      expect(rowButton('Tear down').compareDocumentPosition(notice!) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
      const otherActions = container.querySelector(`#lab-${otherRef}`)!.closest('.pf-v6-c-data-list__item-row')!.querySelector('.pf-v6-c-data-list__item-action')!;
      expect(otherActions.textContent).not.toContain('Lab cleanup failed.');
      expect(otherActions.querySelector('button')!.disabled).toBe(false);
      expect(actions().querySelector('.pf-v6-c-alert')).toBeNull();
    } finally { confirm.mockRestore(); }
  });

  test('release-only cleanup success returns to Launch without an SR or namespace deletion notice', async () => {
    const confirm = jest.spyOn(window, 'confirm').mockReturnValue(true);
    const progress = { apiVersion: 'v1alpha1' as const, kind: 'TeardownStatus' as const, catalogRef: setup.catalogRef, operationId: '12'.repeat(16), status: 'succeeded' as const };
    try {
      setInventory([{ ...setup, spaceRequests: [] }]);
      teardown.mockResolvedValue(progress);
      (client.getTeardownStatus as jest.MockedFunction<typeof client.getTeardownStatus>).mockResolvedValue(progress);
      await renderCatalog();
      setInventory([]);
      await act(async () => { rowButton('Tear down').click(); });
      expect(actions().textContent).not.toContain('Managed lab cleanup completed.');
      expect(actions().textContent?.trim()).toBe('Launch');
      expect(actions().textContent).not.toMatch(/SpaceRequest deletion requested|namespace.*(gone|deleted)/i);
      expect(rowButton('Launch').disabled).toBe(false);
    } finally { confirm.mockRestore(); }
  });

  test('given mixed orphan and catalog setups, when searching, then fallback cleanup stays in the same row layout without Open', async () => {
    const orphan = { ...setup, catalogRef: '01'.repeat(32), blocked: true, release: undefined, spaceRequests: ['removed-lab-sr'] };
    setInventory([setup, orphan]);
    await renderCatalog();

    const list = container.querySelector('.pf-v6-c-data-list')!;
    const orphanRow = container.querySelector(`#lab-${orphan.catalogRef}`)!.closest<HTMLElement>('.pf-v6-c-data-list__item-row')!;
    expect(container.querySelectorAll('.pf-v6-c-data-list')).toHaveLength(1);
    expect(Array.from(list.children).map((child) => child.querySelector('h2')?.id)).toEqual([
      `lab-${ref}`, `lab-${orphan.catalogRef}`, `lab-${otherRef}`,
    ]);
    expect(container.querySelector('section[aria-label="Your lab setups"]')).toBeNull();
    expect(orphanRow.className).toBe(row().className);
    expect(orphanRow.style.cssText).toBe(row().style.cssText);
    expect(orphanRow.querySelector('.pf-v6-c-data-list__cell')?.className).toBe(row().querySelector('.pf-v6-c-data-list__cell')?.className);
    expect(orphanRow.querySelector('h2')?.textContent).toBe(orphan.catalogRef);
    expect(orphanRow.textContent).toContain('removed-lab-sr');
    const orphanActions = orphanRow.querySelector('.pfext-lab-catalog-actions')!;
    expect(Array.from(orphanActions.querySelectorAll('button')).map((button) => button.textContent)).toEqual(['Tear down']);
    expect(orphanActions.querySelector('button')?.disabled).toBe(true);
    expect(orphanActions.textContent).toMatch(/administrator/i);
    expect(list.querySelectorAll('h2')).toHaveLength(3);

    await filterBy('Lab Two');
    expect(Array.from(list.children).map((child) => child.querySelector('h2')?.id)).toEqual([`lab-${orphan.catalogRef}`, `lab-${otherRef}`]);
    expect(container.textContent).not.toContain('Lab One');
    await filterBy('nothing matches');
    expect(container.textContent).not.toContain('No labs match your filter.');
    expect(list.children).toHaveLength(1);
    expect(list.textContent).toContain('removed-lab-sr');
    expect(mockLoadLabBundle).not.toHaveBeenCalled();
    expect(teardown).not.toHaveBeenCalled();
  });

  test.each(['empty', 'error'] as const)('given an %s catalog, when inventory loads, then cleanup remains in the unified list with release-name fallback', async (catalogState) => {
    setInventory([setup]);
    if (catalogState === 'error') mockListCatalog.mockRejectedValue(sampleError);
    else mockListCatalog.mockResolvedValue(emptyCatalog);
    await act(async () => { ReactDOM.render(<InteractiveLabsPage />, container); });

    const lists = container.querySelectorAll('.pf-v6-c-data-list');
    expect(lists).toHaveLength(1);
    expect(lists[0].querySelector('h2')?.textContent).toBe('il-one');
    expect(lists[0].textContent).toContain('lab-sr');
    expect(Array.from(lists[0].querySelectorAll('button')).map((button) => button.textContent)).toEqual(['Tear down']);
    expect(getButton(/^Tear down /).disabled).toBe(false);
    if (catalogState === 'error') expect(container.textContent).toContain('Catalog service unavailable');
    expect(container.textContent).not.toContain('No labs available');
    expect(lists[0].contains(getButton(/Refresh setups/))).toBe(false);
    expect(mockLoadLabBundle).not.toHaveBeenCalled();
    expect(teardown).not.toHaveBeenCalled();
  });

  test.each(['running', 'unknown'] as const)('given receipt-only %s cleanup, when resources disappear, then row-local orphan notices and existing-first order remain', async (status) => {
    const orphanRef = '01'.repeat(32);
    const operationId = '12'.repeat(16);
    sessionStorage.setItem('interactive-labs-teardown', JSON.stringify([{ catalogRef: ref, operationId }, { catalogRef: orphanRef, operationId }]));
    (client.getTeardownStatus as jest.MockedFunction<typeof client.getTeardownStatus>).mockImplementation((catalogRef) => catalogRef === ref
      ? new Promise(() => {})
      : Promise.resolve({ apiVersion: 'v1alpha1', kind: 'TeardownStatus', catalogRef, operationId, status }));

    await renderCatalog({ ...setupCatalog, items: [...setupCatalog.items].reverse() });

    const list = container.querySelector('.pf-v6-c-data-list')!;
    expect(container.querySelectorAll('.pf-v6-c-data-list')).toHaveLength(1);
    expect(Array.from(list.children).map((child) => child.querySelector('h2')?.id)).toEqual([
      `lab-${ref}`, `lab-${orphanRef}`, `lab-${otherRef}`,
    ]);
    const orphanRow = container.querySelector(`#lab-${orphanRef}`)!.closest('.pf-v6-c-data-list__item-row')!;
    expect(orphanRow.querySelector('h2')?.textContent).toBe(orphanRef);
    expect(orphanRow.querySelectorAll('button')).toHaveLength(0);
    const notice = orphanRow.querySelector(status === 'running' ? '[role="status"]' : '[role="alert"]');
    expect(notice?.textContent).toContain(status === 'running' ? 'Lab teardown in progress' : 'outcome could not be verified');
    expect(orphanRow.querySelector('.pf-v6-c-alert')).toBeNull();
    expect(rowButton('Launch').disabled).toBe(true);
    expect(container.querySelector(`#lab-${otherRef}`)!.closest('.pf-v6-c-data-list__item-row')!.querySelector('button')?.disabled).toBe(false);
    expect(mockLoadLabBundle).not.toHaveBeenCalled();
    expect(teardown).not.toHaveBeenCalled();
  });

  test('inventory loading and error fail closed instead of assuming no setup', async () => {
    const load = deferred<Awaited<ReturnType<typeof client.listSetups>>>();
    inventory.mockReturnValue(load.promise);
    await renderCatalog();
    expect(rowButton('Launch').disabled).toBe(true);
    await act(async () => { load.reject(new Error('Inventory unavailable')); });
    expect(container.textContent).toContain('Inventory unavailable');
    expect(rowButton('Launch').disabled).toBe(true);
    expect(mockLoadLabBundle).not.toHaveBeenCalled();
    setInventory([]);
    await act(async () => { getButton(/Refresh setups/).click(); });
    expect(rowButton('Launch').disabled).toBe(false);
  });

  test('failed inventory refresh disables known actions without reclassifying an existing setup as Launch', async () => {
    setInventory([setup]);
    await renderCatalog();
    inventory.mockRejectedValue(new Error('Inventory unavailable'));
    await act(async () => { getButton(/Refresh setups/).click(); });
    expect(rowButton('Open').disabled).toBe(true);
    expect(rowButton('Tear down').disabled).toBe(true);
    expect(actions().textContent).not.toContain('Launch');
    expect(teardown).not.toHaveBeenCalled();
  });
});

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

  test('shows each catalog title as a large heading above its description', async () => {
    mockListCatalog.mockResolvedValue(sampleCatalog);
    mockRenderAsciiDoc.mockResolvedValue({ html: '<p>First lab description</p>', warnings: [] });

    await act(async () => {
      ReactDOM.render(<InteractiveLabsPage />, container);
    });

    expect(container.textContent).toContain('Lab One');
    expect(container.textContent).toContain('First lab description');
    expect(container.querySelector('#lab-lab-1')?.nextElementSibling?.classList.contains('catalog-description')).toBe(true);
    for (const item of sampleCatalog.items) {
      const heading = container.querySelector(`#lab-${item.catalogRef}`);
      expect(heading?.id).toBe(`lab-${item.catalogRef}`);
      expect(heading?.textContent).toBe(item.title);
      expect(heading?.tagName).toBe('H2');
      expect(heading?.classList.contains('pf-v6-c-title')).toBe(true);
      expect(heading?.classList.contains('pf-m-lg')).toBe(true);
    }
    expect(container.querySelector('#lab-lab-1')?.closest('.pf-v6-c-data-list__item-row')?.querySelectorAll('button')).toHaveLength(1);
    expect(container.textContent).toContain('Lab Two');
    expect(container.querySelector('[role="progressbar"]')).toBeNull();
  });

  test('spaces the filter wrapper below the page heading', async () => {
    await renderCatalog();

    const filter = container.querySelector('input[aria-label="Filter labs"]')!;
    const wrapper = filter.parentElement?.parentElement;
    expect(wrapper?.tagName).toBe('DIV');
    expect(wrapper?.style.marginBlockStart).toBe('var(--pf-t--global--spacer--lg)');
  });

  test('pads each catalog row so heading and Launch sit below the divider', async () => {
    await renderCatalog();

    for (const item of sampleCatalog.items) {
      const heading = container.querySelector(`#lab-${item.catalogRef}`)!;
      const row = heading.closest<HTMLElement>('.pf-v6-c-data-list__item-row')!;
      expect(row.querySelector('button')?.textContent?.trim()).toBe('Launch');
      expect(row.style.paddingBlockStart).toBe('var(--pf-t--global--spacer--md)');
    }
  });

  test('shows first rendered paragraph and toggles remaining description without duplication', async () => {
    mockRenderAsciiDoc.mockResolvedValue({
      html: '<div class="paragraph"><p>First <strong>intro</strong> paragraph.</p></div>' +
        '<div class="paragraph"><p>Additional details.</p></div>',
      warnings: ['Unsafe include was omitted.'],
    });
    await renderCatalog();

    const titleRow = container.querySelector('#lab-lab-1')!.closest('.pf-v6-c-data-list__item-row')!;
    const preview = container.querySelector('.catalog-description')!;
    const remaining = container.querySelectorAll('.catalog-description')[1]!;
    const toggle = Array.from(titleRow.querySelectorAll('button')).find((button) => button.textContent?.trim() === 'Show more…')!;
    expect(titleRow.querySelector('.pf-v6-c-data-list__toggle')).toBeNull();
    expect(toggle).toBeDefined();
    expect(titleRow.querySelector('button[aria-label^="Toggle description"]')).toBeNull();
    expect(titleRow.querySelectorAll('button')).toHaveLength(2);
    expect(titleRow.contains(preview)).toBe(true);
    expect(preview.compareDocumentPosition(toggle) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(preview.textContent).toContain('First intro paragraph.');
    expect(preview.querySelector('p strong')?.textContent).toBe('intro');
    expect(toggle.getAttribute('aria-expanded')).toBe('false');
    expect(toggle.getAttribute('aria-controls')).toBeTruthy();
    expect(container.querySelector(`#${toggle.getAttribute('aria-controls')}`)?.contains(remaining)).toBe(true);
    expect(remaining.closest('[hidden]')).not.toBeNull();
    expect(container.textContent).toContain('Unsafe include was omitted.');
    await act(async () => { toggle.click(); });
    expect(remaining.closest('[hidden]')).toBeNull();
    expect(toggle.textContent?.trim()).toBe('Show less');
    expect(toggle.getAttribute('aria-expanded')).toBe('true');
    expect(remaining.textContent).toContain('Additional details.');
    expect(container.querySelectorAll('.catalog-description p')).toHaveLength(2);
    await act(async () => { toggle.click(); });
    expect(toggle.textContent?.trim()).toBe('Show more…');
    expect(toggle.getAttribute('aria-expanded')).toBe('false');
    expect(remaining.closest('[hidden]')).not.toBeNull();
    expect(preview.closest('[hidden]')).toBeNull();
    expect(mockRenderAsciiDoc).toHaveBeenCalledTimes(1);
  });

  test('shows a single rendered paragraph in the row without a description button', async () => {
    mockRenderAsciiDoc.mockResolvedValue({
      html: '<div class="paragraph"><p>Only description.</p></div>',
      warnings: [],
    });
    await renderCatalog({ ...sampleCatalog, items: [sampleCatalog.items[0]] });

    const preview = container.querySelector('.catalog-description')!;
    expect(preview.textContent).toContain('Only description.');
    const row = preview.closest('.pf-v6-c-data-list__item-row')!;
    expect(row.querySelectorAll('button')).toHaveLength(1);
    expect(row.querySelector('button')?.textContent?.trim()).toBe('Launch');
  });

  test('does not leave a description button after extracting a nested sole paragraph', async () => {
    mockRenderAsciiDoc.mockResolvedValue({
      html: '<div id="preamble"><div class="sectionbody"><div class="paragraph"><p>Nested intro.</p></div></div></div>',
      warnings: [],
    });
    await renderCatalog();
    expect(container.querySelector('.catalog-description')?.textContent).toContain('Nested intro.');
    expect(container.querySelector('#lab-lab-1')?.closest('.pf-v6-c-data-list__item-row')?.querySelectorAll('button')).toHaveLength(1);
  });

  test('puts description button after title when no paragraph exists', async () => {
    mockRenderAsciiDoc.mockResolvedValue({ html: '<div class="imageblock">Image only</div>', warnings: [] });
    await renderCatalog();
    const title = container.querySelector('#lab-lab-1')!;
    const row = title.closest('.pf-v6-c-data-list__item-row')!;
    const toggle = Array.from(row.querySelectorAll('button')).find((button) => button.textContent?.trim() === 'Show more…')!;
    const description = container.querySelector('.catalog-description')!;
    expect(row.querySelector('.pf-v6-c-data-list__toggle')).toBeNull();
    expect(title.compareDocumentPosition(toggle) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(toggle.getAttribute('aria-expanded')).toBe('false');
    expect(container.querySelector(`#${toggle.getAttribute('aria-controls')}`)?.contains(description)).toBe(true);
    expect(description.closest('[hidden]')).not.toBeNull();
    await act(async () => { toggle.click(); });
    expect(toggle.textContent?.trim()).toBe('Show less');
    expect(toggle.getAttribute('aria-expanded')).toBe('true');
    expect(description.closest('[hidden]')).toBeNull();
    expect(description.textContent).toContain('Image only');
  });

  test('toggles only the selected lab in a list of labs', async () => {
    const labs = { ...sampleCatalog, items: [sampleCatalog.items[0], { ...sampleCatalog.items[1], description: 'Second lab description' }] };
    mockRenderAsciiDoc.mockImplementation(({ source }) => Promise.resolve({ html: `<p>${source} intro</p><p>${source} details</p>`, warnings: [] }));
    await renderCatalog(labs);
    const firstRow = container.querySelector('#lab-lab-1')!.closest('.pf-v6-c-data-list__item-row')!;
    const secondRow = container.querySelector('#lab-lab-2')!.closest('.pf-v6-c-data-list__item-row')!;
    const first = Array.from(firstRow.querySelectorAll('button')).find((button) => button.textContent?.trim() === 'Show more…')!;
    const second = Array.from(secondRow.querySelectorAll('button')).find((button) => button.textContent?.trim() === 'Show more…')!;
    expect(first).toBeDefined();
    expect(second).toBeDefined();
    expect(first.getAttribute('aria-controls')).not.toBe(second.getAttribute('aria-controls'));
    expect(firstRow.textContent).toContain('First lab description intro');
    expect(secondRow.textContent).toContain('Second lab description intro');
    await act(async () => { second.click(); });
    expect(first.textContent?.trim()).toBe('Show more…');
    expect(second.textContent?.trim()).toBe('Show less');
    expect(first.getAttribute('aria-expanded')).toBe('false');
    expect(second.getAttribute('aria-expanded')).toBe('true');
    expect(container.querySelector(`#${first.getAttribute('aria-controls')}`)?.hasAttribute('hidden')).toBe(true);
    expect(container.querySelector(`#${second.getAttribute('aria-controls')}`)?.hasAttribute('hidden')).toBe(false);
    await act(async () => { first.click(); });
    expect(first.textContent?.trim()).toBe('Show less');
    expect(second.textContent?.trim()).toBe('Show less');
    await act(async () => { second.click(); });
    expect(first.getAttribute('aria-expanded')).toBe('true');
    expect(second.getAttribute('aria-expanded')).toBe('false');
  });

  test('handles links from collapsed preview and expanded description', async () => {
    mockRenderAsciiDoc.mockResolvedValue({
      html: '<div class="paragraph"><p><a href="https://docs.example.test/intro">Intro</a></p></div>' +
        '<div class="paragraph"><p><a href="https://docs.example.test/details">Details</a></p></div>',
      warnings: [],
    });
    const open = jest.spyOn(window, 'open').mockImplementation(() => null);
    try {
      await renderCatalog();
      const click = async (text: string) => {
        const link = Array.from(container.querySelectorAll('a')).find((anchor) => anchor.textContent === text)!;
        const event = new MouseEvent('click', { bubbles: true, cancelable: true });
        await act(async () => { link.dispatchEvent(event); });
        expect(event.defaultPrevented).toBe(true);
      };
      await click('Intro');
      await act(async () => { getButton(/Show more…/).click(); });
      await click('Details');
      expect(open).toHaveBeenNthCalledWith(1, 'https://docs.example.test/intro', '_blank', 'noopener,noreferrer');
      expect(open).toHaveBeenNthCalledWith(2, 'https://docs.example.test/details', '_blank', 'noopener,noreferrer');
    } finally {
      open.mockRestore();
    }
  });

  test('shows renderer warnings even when only one paragraph needs no toggle', async () => {
    mockRenderAsciiDoc.mockResolvedValue({ html: '<p>Visible intro.</p>', warnings: ['Unsafe include was omitted.'] });
    await renderCatalog();
    expect(container.textContent).toContain('Visible intro.');
    expect(container.textContent).toContain('Unsafe include was omitted.');
    expect(container.querySelector('#lab-lab-1')?.closest('.pf-v6-c-data-list__item-row')?.querySelectorAll('button')).toHaveLength(1);
  });

  test('shows description render errors without expansion', async () => {
    mockRenderAsciiDoc.mockRejectedValue(new Error('Description unavailable'));
    await renderCatalog();
    expect(container.textContent).toContain('Description unavailable');
  });

  test('always shows catalog descriptions', async () => {
    mockListCatalog.mockResolvedValue(sampleCatalog);
    mockRenderAsciiDoc.mockResolvedValue({ html: '<p>First lab description</p>', warnings: [] });

    await act(async () => {
      ReactDOM.render(<InteractiveLabsPage />, container);
    });

    expect(container.textContent).toContain('First lab description');
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
    expect(mockLoadLabBundle).toHaveBeenCalledWith('lab-1', 'labs/one', expect.any(AbortSignal));
  });

  test('opens no-chart lab at first validated page without setup', async () => {
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

  test('shows validated manifest title in drawer', async () => {
    mockLoadLabBundle.mockResolvedValue({
      ...noChartBundle,
      manifest: {
        ...noChartBundle.manifest,
        metadata: { ...noChartBundle.manifest.metadata, title: 'Validated bundle title' },
      },
    });
    await renderCatalog();

    await act(async () => { getButton(/^Launch$/).click(); });

    expect(container.querySelector('[class*="drawer__panel"]')?.textContent).toContain(
      'Validated bundle title',
    );
  });

  test('opens chart-backed drawer and defers page rendering while setup is pending', async () => {
    const setup = deferred<Awaited<ReturnType<typeof client.setupLab>>>();
    mockLoadLabBundle.mockResolvedValue({ ...chartBundle, modules: multiModuleBundle.modules });
    mockSetupLab.mockReturnValue(setup.promise);
    await renderCatalog();

    act(() => { getButton(/^Launch$/).click(); });
    await act(async () => {});

    const drawer = container.querySelector('[class*="drawer__panel"]')!;
    expect(drawer.querySelector('.pfext-lab-panel-content h2')?.textContent).toBe('Lab One');
    expect(drawer.textContent).toContain('Setting up your lab environment...');
    expect(drawer.querySelector('[aria-label="Setting up lab"]')).not.toBeNull();
    expect(drawer.querySelector('.pfext-lab-module-title')).toBeNull();
    expect(drawer.querySelector('.pfext-lab-panel-content [aria-live="polite"]')).toBeNull();
    expect(drawer.textContent).not.toMatch(/010-intro|020-apply|Page 1 of 2/);
    expect(drawer.querySelector('.pfext-lab-footer')).not.toBeNull();
    expect(getButton(/^Back$/).disabled).toBe(true);
    expect(getButton(/^Next$/).disabled).toBe(true);
    const toggle = drawer.querySelector<HTMLButtonElement>('.pfext-lab-module-toggle');
    expect(toggle?.getAttribute('aria-label')).toBe('Module');
    expect(toggle?.textContent?.trim()).toBe('Module');
    expect(toggle?.disabled).toBe(true);
    expect(moduleOptions()).toHaveLength(0);
    expect(mockFetchResourceText).not.toHaveBeenCalled();
    expect(mockSetupLab).toHaveBeenCalledWith('lab-1', expect.any(AbortSignal));
  });

  test('renders chart-backed first page after setup becomes ready', async () => {
    const status = deferred<Awaited<ReturnType<typeof client.getSetupStatus>>>();
    mockLoadLabBundle.mockResolvedValue({ ...chartBundle, modules: multiModuleBundle.modules });
    mockSetupLab.mockResolvedValue({
      apiVersion: 'v1alpha1',
      kind: 'SetupStatus',
      spaceRequests: { status: 'provisioning', namespaces: null },
      helm: { status: 'pending', releaseName: 'il-abc123' },
    });
    mockGetSetupStatus.mockReturnValueOnce(status.promise);
    mockRenderAsciiDoc.mockResolvedValue({ html: '<p>Ready page</p>', warnings: [] });
    await renderCatalog();

    act(() => { getButton(/^Launch$/).click(); });
    await act(async () => {});
    expect(mockFetchResourceText).not.toHaveBeenCalled();
    expect(getButton(/^Next$/).disabled).toBe(true);
    expect(container.querySelector('.pfext-lab-module-title')).toBeNull();

    await act(async () => {
      status.resolve({
        apiVersion: 'v1alpha1', kind: 'SetupStatus',
        spaceRequests: { status: 'ready', namespaces: { gitea: { status: 'ready', name: 'user1-abc' } } },
        helm: { status: 'deployed', releaseName: 'il-abc123' },
      });
    });

    expect(mockGetSetupStatus).toHaveBeenCalledWith('lab-1', expect.any(AbortSignal));
    expect(mockFetchResourceText).toHaveBeenCalledWith(firstPage.url, expect.any(AbortSignal));
    expect(container.textContent).toContain('Ready page');
    expect(container.querySelector('.pfext-lab-module-title')?.textContent).toBe('010-intro');
    expect(container.querySelector('.pfext-lab-panel-content [aria-live="polite"]')?.textContent).toBe('Page 1 of 2');
    expect(getButton(/^Back$/).disabled).toBe(true);
    expect(getButton(/^Next$/).disabled).toBe(false);
    expect(moduleToggle().disabled).toBe(false);
    await act(async () => { moduleToggle().click(); });
    expect(moduleOptions().map((option) => option.textContent?.trim())).toEqual(['010-intro', '020-apply']);
  });

  test('removes setup poll abort listener after normal delay', async () => {
    jest.useFakeTimers();
    try {
      let addAbortListener!: jest.SpyInstance;
      let removeAbortListener!: jest.SpyInstance;
      mockLoadLabBundle.mockResolvedValue(chartBundle);
      mockSetupLab.mockImplementation((_catalogRef, signal) => {
        addAbortListener = jest.spyOn(signal!, 'addEventListener');
        removeAbortListener = jest.spyOn(signal!, 'removeEventListener');
        return Promise.resolve({
          apiVersion: 'v1alpha1' as const, kind: 'SetupStatus' as const,
          spaceRequests: { status: 'provisioning' as const, namespaces: null },
          helm: { status: 'pending' as const, releaseName: 'il-abc123' },
        });
      });
      mockGetSetupStatus
        .mockResolvedValueOnce({
          apiVersion: 'v1alpha1', kind: 'SetupStatus',
          spaceRequests: { status: 'provisioning', namespaces: null },
          helm: { status: 'pending', releaseName: 'il-abc123' },
        })
        .mockResolvedValueOnce({
          apiVersion: 'v1alpha1', kind: 'SetupStatus',
          spaceRequests: { status: 'ready', namespaces: { gitea: { status: 'ready', name: 'user1-abc' } } },
          helm: { status: 'deployed', releaseName: 'il-abc123' },
        });
      await renderCatalog();

      act(() => { getButton(/^Launch$/).click(); });
      await act(async () => {});
      const abortListener = addAbortListener.mock.calls.find(([type]: [string]) => type === 'abort')?.[1];
      expect(abortListener).toEqual(expect.any(Function));

      await act(async () => { jest.advanceTimersByTime(3000); });

      expect(removeAbortListener).toHaveBeenCalledWith('abort', abortListener);
    } finally {
      jest.useRealTimers();
    }
  });

  test('shows chart setup failure inside its open drawer', async () => {
    mockLoadLabBundle.mockResolvedValue({ ...chartBundle, modules: multiModuleBundle.modules });
    mockSetupLab.mockRejectedValue(new Error('Cluster setup failed'));
    await renderCatalog();

    await act(async () => { getButton(/^Launch$/).click(); });

    const drawer = container.querySelector('[class*="drawer__panel"]')!;
    expect(drawer.querySelector('.pfext-lab-panel-content h2')?.textContent).toBe('Lab One');
    expect(drawer.textContent).toContain('Cluster setup failed');
    expect(drawer.querySelector('.pfext-lab-module-title')).toBeNull();
    expect(drawer.querySelector('.pfext-lab-panel-content [aria-live="polite"]')).toBeNull();
    expect(drawer.textContent).not.toMatch(/010-intro|020-apply|Page 1 of 2/);
    expect(drawer.querySelector('.pfext-lab-footer')).not.toBeNull();
    expect(getButton(/^Back$/).disabled).toBe(true);
    expect(getButton(/^Next$/).disabled).toBe(true);
    const toggle = drawer.querySelector<HTMLButtonElement>('.pfext-lab-module-toggle');
    expect(toggle?.getAttribute('aria-label')).toBe('Module');
    expect(toggle?.textContent?.trim()).toBe('Module');
    expect(toggle?.disabled).toBe(true);
    expect(moduleOptions()).toHaveLength(0);
    expect(mockFetchResourceText).not.toHaveBeenCalled();
  });

  test('aborts obsolete bundle request before a new launch', async () => {
    const bundle = deferred<LabBundle>();
    let firstSignal!: AbortSignal;
    mockLoadLabBundle
      .mockImplementationOnce((_catalogRef, _labPath, signal) => {
        firstSignal = signal!;
        return bundle.promise;
      })
      .mockResolvedValueOnce({
        ...noChartBundle,
        manifest: {
          ...noChartBundle.manifest,
          metadata: { ...noChartBundle.manifest.metadata, title: 'Lab Two' },
        },
      });
    await renderCatalog();

    const launches = Array.from(container.querySelectorAll('button')).filter((button) => button.textContent === 'Launch');
    act(() => { launches[0].click(); });
    await act(async () => {});
    act(() => { launches[1].click(); });
    await act(async () => {});

    expect(firstSignal.aborted).toBe(true);
    expect(container.textContent).toContain('Lab Two');
  });

  test('aborts setup on close and ignores its stale completion', async () => {
    const setup = deferred<Awaited<ReturnType<typeof client.setupLab>>>();
    let setupSignal!: AbortSignal;
    mockLoadLabBundle.mockResolvedValue(chartBundle);
    mockSetupLab.mockImplementation((_catalogRef, signal) => {
      setupSignal = signal!;
      return setup.promise;
    });
    await renderCatalog();

    act(() => { getButton(/^Launch$/).click(); });
    await act(async () => {});
    act(() => { getButton(/close lab/i).click(); });
    expect(setupSignal.aborted).toBe(true);

    await act(async () => {
      setup.resolve({
        apiVersion: 'v1alpha1', kind: 'SetupStatus',
        spaceRequests: { status: 'provisioning', namespaces: null },
        helm: { status: 'pending', releaseName: 'il-abc123' },
      });
    });

    expect(container.querySelector('button[aria-label="Close lab"]')).toBeNull();
    expect(mockGetSetupStatus).not.toHaveBeenCalled();
  });

  test('aborts pending setup status on unmount', async () => {
    const status = deferred<Awaited<ReturnType<typeof client.getSetupStatus>>>();
    let launchSignal!: AbortSignal;
    mockLoadLabBundle.mockResolvedValue(chartBundle);
    mockSetupLab.mockImplementation((_catalogRef, signal) => {
      launchSignal = signal!;
      return Promise.resolve({
        apiVersion: 'v1alpha1' as const, kind: 'SetupStatus' as const,
        spaceRequests: { status: 'provisioning' as const, namespaces: null },
        helm: { status: 'pending' as const, releaseName: 'il-abc123' },
      });
    });
    mockGetSetupStatus.mockReturnValue(status.promise);
    await renderCatalog();

    act(() => { getButton(/^Launch$/).click(); });
    await act(async () => {});
    act(() => { ReactDOM.unmountComponentAtNode(container); });

    expect(launchSignal.aborted).toBe(true);
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

    const sourceURL = firstPage.url;
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

    expect(container.querySelector('#lab-lab-1')?.closest('.pf-v6-c-data-list__item-row')?.querySelectorAll('button')).toHaveLength(1);
    expect(container.querySelector('.catalog-description')?.textContent).toContain('Sanitized description');
    expect(mockRenderAsciiDoc).toHaveBeenCalledWith(expect.objectContaining({
      source: 'First lab description',
      sourceURL: '/api/proxy/plugin/interactive-labs-console-plugin/backend/api/v1alpha1/labs/lab-1/resources/labs/one/lab.yaml',
      attributes: {},
      loadResource: mockFetchResourceText,
    }));
    expect(container.innerHTML).toContain('Sanitized description');
  });

  test.each([
    ['labs/lab#1', 'labs/lab%231'],
    ['labs/lab?1', 'labs/lab%3F1'],
    ['.', ''],
  ])('keeps description resources and links inside labPath %s', async (labPath, encodedPath) => {
    const prefix = `/api/proxy/plugin/interactive-labs-console-plugin/backend/api/v1alpha1/labs/lab-1/resources/${encodedPath ? `${encodedPath}/` : ''}`;
    const bundle = {
      ...noChartBundle,
      modules: noChartBundle.modules.map((module) => ({
        ...module,
        pages: module.pages.map((page) => ({ ...page, url: page.url.replace('/resources/labs/one/', `/resources/${encodedPath ? `${encodedPath}/` : ''}`) })),
      })),
    };
    mockLoadLabBundle.mockResolvedValue(bundle);
    mockRenderAsciiDoc.mockResolvedValue({
      html: '<a href="modules/010-intro/content/pages/z-last.adoc">Known</a>' +
        '<a href="modules/010-intro/content/pages/missing.adoc">Unknown</a>' +
        '<a href="../other/modules/010-intro/content/pages/z-last.adoc">Other lab</a>',
      warnings: [],
    });
    await renderCatalog({ ...sampleCatalog, items: [{ ...sampleCatalog.items[0], labPath }] });
    expect(mockRenderAsciiDoc).toHaveBeenCalledWith(expect.objectContaining({ sourceURL: `${prefix}lab.yaml`, loadResource: mockFetchResourceText }));
    const click = async (text: string) => {
      const link = Array.from(container.querySelectorAll('a')).find((anchor) => anchor.textContent === text)!;
      const event = new MouseEvent('click', { bubbles: true, cancelable: true });
      await act(async () => { link.dispatchEvent(event); });
      expect(event.defaultPrevented).toBe(true);
    };
    await click('Other lab');
    expect(mockLoadLabBundle).not.toHaveBeenCalled();
    await click('Unknown');
    expect(mockLoadLabBundle).toHaveBeenCalledTimes(1);
    expect(container.textContent).toContain('Link is unavailable or unsafe.');
    expect(container.querySelector('button[aria-label="Close lab"]')).toBeNull();
    expect(mockFetchResourceText).not.toHaveBeenCalled();
    await click('Known');
    expect(mockLoadLabBundle).toHaveBeenLastCalledWith('lab-1', labPath, expect.any(AbortSignal));
    expect(mockFetchResourceText).toHaveBeenCalledWith(`${prefix}modules/010-intro/content/pages/z-last.adoc`, expect.any(AbortSignal));
  });

  test('marks rendered page and description roots as scoped AsciiDoc documents', async () => {
    mockLoadLabBundle.mockResolvedValue(noChartBundle);
    mockRenderAsciiDoc.mockResolvedValue({ html: '<p>Rendered content</p>', warnings: [] });
    await renderCatalog();

    await act(async () => { getButton(/^Launch$/).click(); });

    const page = container.querySelector('[class*="drawer__panel"] .interactive-labs-content.doc');
    const description = container.querySelector('.interactive-labs-content.doc.catalog-description');
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
        ? '<a href="modules/010-intro/content/pages/z-last.adoc">Open linked page</a>'
        : '<p>Sanitized page</p>',
      warnings: [],
    }));
    await renderCatalog();

    const link = Array.from(container.querySelectorAll('a')).find((anchor) => anchor.textContent === 'Open linked page')!;
    const preventNavigation = (event: Event) => event.preventDefault();
    document.addEventListener('click', preventNavigation);
    await act(async () => { link.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true })); });
    document.removeEventListener('click', preventNavigation);

    expect(mockLoadLabBundle).toHaveBeenCalledWith('lab-1', 'labs/one', expect.any(AbortSignal));
    expect(container.textContent).toContain('pages/z-last.adoc');
    expect(mockFetchResourceText).toHaveBeenCalledWith(lastPage.url, expect.any(AbortSignal));
  });

  test('loads first validated page and keeps previous and next within page boundaries', async () => {
    mockLoadLabBundle.mockResolvedValue(noChartBundle);
    mockRenderAsciiDoc.mockResolvedValue({ html: '<p>Rendered page</p>', warnings: [] });
    await renderCatalog();

    await act(async () => { getButton(/^Launch$/).click(); });

    const back = getButton(/^Back$/);
    const next = getButton(/^Next$/);
    expect(container.textContent).toContain('pages/a-first.adoc');
    expect(container.textContent).toContain('Page 1 of 2');
    expect(back.disabled).toBe(true);
    expect(next.disabled).toBe(false);

    await act(async () => { next.click(); });

    expect(container.textContent).toContain('pages/z-last.adoc');
    expect(container.textContent).toContain('Page 2 of 2');
    expect(getButton(/^Back$/).disabled).toBe(false);
    expect(getButton(/^Next$/).disabled).toBe(true);
    expect(localStorage.getItem(sessionKey('lab-1'))).toBe(
      JSON.stringify({ currentPagePath: lastPage.path, panelMode: 'open' }),
    );
  });

  test('provides branded drawer header colors without PatternFly 6 theme tokens', () => {
    const css = readFileSync(`${__dirname}/interactive-labs.css`, 'utf8');
    const header = css.match(/\.pfext-lab-panel-content\s*\{([^}]*)\}/)?.[1];
    expect(header).toContain('background-color: var(--pf-t--global--color--brand--default, #06c)');
    expect(header).toContain('color: var(--pf-t--global--text--color--inverse, #fff)');
  });

  test('constrains drawer layout even without host PatternFly styles', () => {
    const css = readFileSync(`${__dirname}/interactive-labs.css`, 'utf8');
    expect(css).toMatch(/\.pfext-lab-drawer\s*\{[^}]*display:\s*flex;[^}]*height:\s*100%;[^}]*overflow:\s*hidden;/);
    expect(css).toMatch(/#interactive-labs-drawer-panel\s*\{[^}]*display:\s*flex;[^}]*height:\s*100%;/);
    expect(css).toMatch(/#interactive-labs-drawer-panel \.pf-v6-c-drawer__panel-main\s*\{[^}]*display:\s*flex;[^}]*flex:\s*1;[^}]*flex-direction:\s*column;/);
  });

  test('does not stack the resizable drawer splitter above the content', () => {
    const css = readFileSync(`${__dirname}/interactive-labs.css`, 'utf8');
    const panel = css.match(/#interactive-labs-drawer-panel\s*\{([^}]*)\}/)?.[1];
    expect(panel).not.toContain('flex-direction: column');
  });

  test('keeps blue header flush with drawer top and plain controls legible', () => {
    const css = readFileSync(`${__dirname}/interactive-labs.css`, 'utf8');
    const panelMain = css.match(/#interactive-labs-drawer-panel \.pf-v6-c-drawer__panel-main\s*\{([^}]*)\}/)?.[1];
    expect(panelMain).toContain('padding-block-start: 0;');
    expect(css).toMatch(/\.pfext-lab-panel-content \.pf-v6-c-button\.pf-m-plain[^{}]*\{[^}]*color:\s*inherit;/);
  });

  test('keeps footer top spacing over PatternFly drawer sibling reset', () => {
    const css = readFileSync(`${__dirname}/interactive-labs.css`, 'utf8');
    expect(css).toMatch(/#interactive-labs-drawer-panel \.pfext-lab-footer\s*\{[^}]*padding-block-start:\s*var\(--pf-t--global--spacer--md, 0\.25rem\);/);
  });

  test('stretches module selector without long titles changing layout', () => {
    const css = readFileSync(`${__dirname}/interactive-labs.css`, 'utf8');
    const moduleItem = css.match(/\.pfext-lab-footer__module\s*\{([^}]*)\}/)?.[1];
    const toggle = css.match(/\.pfext-lab-module-toggle\s*\{([^}]*)\}/)?.[1];
    expect(moduleItem).toContain('flex: 1 1 0;');
    expect(toggle).toContain('width: 100%;');
    expect(css).toMatch(/\.pfext-lab-module-toggle \.pf-v6-c-menu-toggle__text\s*\{[^}]*text-overflow:\s*ellipsis;/);
  });

  test('uses an accessible resizable drawer header with an empty title while page rendering loads', async () => {
    mockLoadLabBundle.mockResolvedValue(noChartBundle);
    let resolveRender!: (result: Awaited<ReturnType<typeof renderAsciiDoc>>) => void;
    mockRenderAsciiDoc.mockImplementation(() => new Promise((resolve) => { resolveRender = resolve; }));
    await renderCatalog();

    await act(async () => { getButton(/^Launch$/).click(); });

    const drawer = container.querySelector('[class*="drawer__panel"]')!;
    expect(container.querySelector('.pfext-lab-drawer')).not.toBeNull();
    const labName = drawer.querySelector('.pfext-lab-panel-content h2')!;
    const allH2s = drawer.querySelectorAll('h2');
    const pageTitle = allH2s[1]!;
    const next = getButton(/^Next$/);
    const back = getButton(/^Back$/);
    const minimize = getButton(/^minimize lab$/i);
    const close = getButton(/^close lab$/i);

    expect(labName.textContent).toBe('Lab One');
    expect(drawer.querySelector('.pfext-lab-module-title')?.textContent).toBe('010-intro');
    expect(drawer.querySelector('.pfext-lab-panel-content [aria-live="polite"]')?.textContent).toBe('Page 1 of 2');
    expect(labName.nextElementSibling?.textContent).toBe('010-intro');
    expect(pageTitle.textContent).toBe('');
    expect(drawer.querySelector('[aria-label="Loading lab page"]')).not.toBeNull();
    expect(drawer.textContent).not.toContain('pages/a-first.adoc');
    expect(labName.compareDocumentPosition(pageTitle) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(pageTitle.compareDocumentPosition(next) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(next.compareDocumentPosition(back) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    const body = drawer.querySelector('.pfext-lab-panel-content__body')!;
    const footer = drawer.querySelector('.pfext-lab-footer')!;
    expect(body.contains(pageTitle)).toBe(true);
    expect(body.contains(footer)).toBe(false);
    expect(body.parentElement).toBe(footer.parentElement);
    expect(footer.contains(next)).toBe(true);
    expect(footer.contains(back)).toBe(true);
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
    expect(mockFetchResourceText).toHaveBeenCalledWith(lastPage.url, expect.any(AbortSignal));
  });

  test('opens external links safely, retains drawer for Console links, and warns for plugin or unknown links', async () => {
    mockLoadLabBundle.mockResolvedValue(noChartBundle);
    mockRenderAsciiDoc.mockResolvedValue({
      html: [
        '<a href="https://docs.example.test/guide">External</a>',
        `<a href="${window.location.origin}/">Console</a>`,
        `<a href="${firstPage.url}">Plugin resource</a>`,
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
    let firstPageReads = 0;
    mockFetchResourceText.mockImplementation((url) => url === firstPage.url && ++firstPageReads === 2
      ? Promise.reject(new Error('Page unavailable'))
      : Promise.resolve('= Page'));
    mockRenderAsciiDoc.mockResolvedValue({ html: '<p>Retried page</p>', warnings: [] });
    await renderCatalog();

    await act(async () => { getButton(/^Launch$/).click(); });
    expect(container.textContent).toContain('Page unavailable');

    await act(async () => { getButton(/^Retry$/).click(); });
    expect(mockFetchResourceText.mock.calls.filter(([url]) => url === firstPage.url)).toHaveLength(3);
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
      JSON.stringify({ currentPagePath: firstPage.path, panelMode: 'minimized' }),
    );
    const restore = getButton(/restore/i);
    expect(restore.tagName).toBe('BUTTON');
    expect(restore.type).toBe('button');
    expect(restore.disabled).toBe(false);
    expect(restore.closest('[class*="drawer__panel"]')).toBeNull();
    expect(document.activeElement).toBe(restore);

    act(() => { restore.click(); });
    expect(localStorage.getItem(sessionKey('lab-1'))).toBe(
      JSON.stringify({ currentPagePath: firstPage.path, panelMode: 'open' }),
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
      JSON.stringify({ currentPagePath: lastPage.path, panelMode: 'minimized' }),
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
      JSON.stringify({ currentPagePath: firstPage.path, panelMode: 'open' }),
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
      JSON.stringify({ currentPagePath: firstPage.path, panelMode: 'open' }),
    );
  });

  test('follows backend module/page order, selects first module page, and crosses both boundaries', async () => {
    mockLoadLabBundle.mockResolvedValue(multiModuleBundle);
    mockRenderAsciiDoc.mockResolvedValue({ html: '<p>Page content</p>', warnings: [] });
    await renderCatalog();
    await act(async () => { getButton(/^Launch$/).click(); });

    expect(moduleToggle().textContent).toContain('010-intro');
    const drawer = container.querySelector('[class*="drawer__panel"]')!;
    expect(drawer.querySelector('.pfext-lab-module-title')?.textContent).toBe('010-intro');
    expect(drawer.querySelector('.pfext-lab-panel-content [aria-live="polite"]')?.textContent).toBe('Page 1 of 2');
    expect(drawer.querySelector('.pfext-lab-footer')?.contains(moduleToggle())).toBe(true);
    expect(drawer.querySelector('.pfext-lab-footer')?.contains(getButton(/^Next$/))).toBe(true);
    expect(drawer.querySelector('.pfext-lab-footer')?.contains(getButton(/^Back$/))).toBe(true);
    expect(drawer.querySelector('.pfext-lab-panel-content__body')?.contains(moduleToggle())).toBe(false);
    const footerItems = drawer.querySelectorAll('.pfext-lab-footer [class*="action-list__item"]');
    expect(footerItems).toHaveLength(3);
    expect(footerItems[0].contains(getButton(/^Next$/))).toBe(true);
    expect(footerItems[1].contains(getButton(/^Back$/))).toBe(true);
    expect(footerItems[2].contains(moduleToggle())).toBe(true);
    expect(moduleToggle().getAttribute('aria-label')).toBe('Module: 010-intro');
    expect(moduleToggle().title).toBe('010-intro');
    await act(async () => { moduleToggle().click(); });
    expect(moduleToggle().getAttribute('aria-expanded')).toBe('true');
    expect(moduleOptions()[0].closest('#interactive-labs-drawer-panel')).toBeNull();
    expect(moduleOptions().map((option) => option.textContent?.trim())).toEqual(['010-intro', '020-apply']);
    expect(moduleOptions().map((option) => option.getAttribute('aria-selected'))).toEqual(['true', 'false']);
    await act(async () => { moduleToggle().click(); });
    expect(container.textContent).toContain('Page 1 of 2');
    await act(async () => { getButton(/^Next$/).click(); });
    expect(container.textContent).toContain('Page 2 of 2');
    await act(async () => { getButton(/^Next$/).click(); });
    expect(moduleToggle().textContent).toContain('020-apply');
    expect(drawer.querySelector('.pfext-lab-module-title')?.textContent).toBe('020-apply');
    expect(drawer.querySelector('.pfext-lab-panel-content [aria-live="polite"]')?.textContent).toBe('Page 1 of 2');
    expect(localStorage.getItem(sessionKey('lab-1'))).toBe(JSON.stringify({ currentPagePath: multiModuleBundle.modules[1].pages[0].path, panelMode: 'open' }));
    await act(async () => { getButton(/^Back$/).click(); });
    expect(moduleToggle().textContent).toContain('010-intro');
    expect(drawer.querySelector('.pfext-lab-module-title')?.textContent).toBe('010-intro');
    expect(container.textContent).toContain('Page 2 of 2');
    await act(async () => { moduleToggle().click(); });
    await act(async () => { moduleOptions()[1].click(); });
    expect(moduleToggle().getAttribute('aria-expanded')).toBe('false');
    expect(moduleToggle().textContent).toContain('020-apply');
    expect(container.textContent).toContain('Page 1 of 2');
    await act(async () => { getButton(/^Next$/).click(); });
    expect(container.textContent).toContain('Page 2 of 2');
    expect(getButton(/^Next$/).disabled).toBe(true);
  });

  test('keeps module keyboard focus and closes menu on Escape, outside click, selection, and minimize', async () => {
    mockLoadLabBundle.mockResolvedValue(multiModuleBundle);
    await renderCatalog();
    await act(async () => { getButton(/^Launch$/).click(); });

    const toggle = moduleToggle();
    act(() => { toggle.focus(); toggle.click(); });
    expect(toggle.getAttribute('aria-expanded')).toBe('true');
    act(() => { toggle.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true })); });
    expect(document.activeElement).toBe(moduleOptions()[0]);
    act(() => { moduleOptions()[0].dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true })); });
    expect(toggle.getAttribute('aria-expanded')).toBe('false');
    expect(document.activeElement).toBe(toggle);

    act(() => { toggle.click(); });
    act(() => { container.querySelector('#interactive-labs-drawer-panel h2')!.dispatchEvent(new MouseEvent('click', { bubbles: true })); });
    expect(toggle.getAttribute('aria-expanded')).toBe('false');

    act(() => { toggle.click(); });
    act(() => { moduleOptions()[1].click(); });
    expect(document.activeElement).toBe(toggle);
    expect(toggle.textContent).toContain('020-apply');

    act(() => { toggle.click(); });
    act(() => { getButton(/minimize lab/i).click(); });
    act(() => { getButton(/restore lab/i).click(); });
    expect(moduleToggle().getAttribute('aria-expanded')).toBe('false');
  });

  test('preserves validated descriptor order even when names are not lexically ordered', async () => {
    const reordered: LabBundle = {
      ...multiModuleBundle,
      modules: [multiModuleBundle.modules[1], { ...multiModuleBundle.modules[0], pages: [...multiModuleBundle.modules[0].pages].reverse() }],
    };
    mockLoadLabBundle.mockResolvedValue(reordered);
    await renderCatalog();
    await act(async () => { getButton(/^Launch$/).click(); });
    expect(moduleToggle().textContent).toContain('020-apply');
    await act(async () => { moduleToggle().click(); });
    expect(moduleOptions().map((option) => option.textContent?.trim())).toEqual(['020-apply', '010-intro']);
    expect(moduleOptions()[0].getAttribute('aria-selected')).toBe('true');
    await act(async () => { moduleToggle().click(); });
    expect(mockFetchResourceText).toHaveBeenCalledWith(reordered.modules[0].pages[0].url, expect.any(AbortSignal));
    await act(async () => { getButton(/^Next$/).click(); });
    await act(async () => { getButton(/^Next$/).click(); });
    expect(moduleToggle().textContent).toContain('010-intro');
    expect(localStorage.getItem(sessionKey('lab-1'))).toContain(lastPage.path);
  });

  test('loads plain module labels in background after setup without blocking pages', async () => {
    const status = deferred<Awaited<ReturnType<typeof client.getSetupStatus>>>();
    const title = deferred<string | undefined>();
    mockLoadLabBundle.mockResolvedValue({ ...multiModuleBundle, runtimeContext: { ...multiModuleBundle.runtimeContext, helmChartAvailable: true }, manifest: { ...multiModuleBundle.manifest, spec: { attributes: { product: 'Resolved' } } } });
    mockSetupLab.mockResolvedValue({ apiVersion: 'v1alpha1', kind: 'SetupStatus', spaceRequests: { status: 'provisioning', namespaces: null }, helm: { status: 'pending', releaseName: 'il-test' } });
    mockGetSetupStatus.mockReturnValue(status.promise);
    mockExtractAsciiDocTitle.mockReturnValueOnce(title.promise).mockRejectedValueOnce(new Error('Title missing'));
    mockRenderAsciiDoc.mockResolvedValue({ html: '<p>Already visible</p>', warnings: [] });
    await renderCatalog();
    act(() => { getButton(/^Launch$/).click(); });
    await act(async () => {});
    expect(mockExtractAsciiDocTitle).not.toHaveBeenCalled();
    expect(mockFetchResourceText).not.toHaveBeenCalled();
    await act(async () => { status.resolve({ apiVersion: 'v1alpha1', kind: 'SetupStatus', spaceRequests: { status: 'ready', namespaces: {} }, helm: { status: 'deployed', releaseName: 'il-test' } }); });
    expect(moduleToggle().textContent).toContain('010-intro');
    expect(container.querySelector('.pfext-lab-module-title')?.textContent).toBe('010-intro');
    await act(async () => { moduleToggle().click(); });
    expect(moduleOptions()[0].textContent?.trim()).toBe('010-intro');
    expect(container.textContent).toContain('Already visible');
    expect(mockFetchResourceText).toHaveBeenCalledWith(firstPage.url, expect.any(AbortSignal));
    expect(mockFetchResourceText).toHaveBeenCalledWith(multiModuleBundle.modules[1].pages[0].url, expect.any(AbortSignal));
    expect(mockExtractAsciiDocTitle).toHaveBeenCalledWith(expect.objectContaining({ sourceURL: firstPage.url, attributes: { product: 'Resolved' }, loadResource: mockFetchResourceText, signal: expect.any(AbortSignal) }));
    await act(async () => { title.resolve('Safe <em>module</em> &amp; <script>unsafe</script>'); });
    expect(moduleToggle().textContent).toContain('Safe module &');
    expect(container.querySelector('.pfext-lab-module-title')?.textContent).toBe('Safe module &');
    expect(container.querySelector('.pfext-lab-module-title em')).toBeNull();
    expect(moduleOptions()[0].textContent?.trim()).toBe('Safe module &');
    expect(moduleOptions()[0].querySelector('em')).toBeNull();
    expect(moduleOptions()[1].textContent?.trim()).toBe('020-apply');
  });

  test('ignores obsolete title results after close, relaunch, and unmount', async () => {
    const stale = deferred<string | undefined>();
    let signal!: AbortSignal;
    mockLoadLabBundle.mockResolvedValue(multiModuleBundle);
    mockExtractAsciiDocTitle.mockImplementationOnce(({ signal: requestSignal }) => { signal = requestSignal!; return stale.promise; }).mockResolvedValue(undefined);
    await renderCatalog();
    await act(async () => { getButton(/^Launch$/).click(); });
    act(() => { getButton(/close lab/i).click(); });
    expect(signal.aborted).toBe(true);
    await act(async () => { getButton(/^Launch$/).click(); });
    await act(async () => { stale.resolve('Stale name'); });
    expect(container.textContent).not.toContain('Stale name');
    act(() => { ReactDOM.unmountComponentAtNode(container); });
  });

  test('aborts pending module titles on unmount', async () => {
    const pending = deferred<string | undefined>();
    let signal!: AbortSignal;
    mockLoadLabBundle.mockResolvedValue(multiModuleBundle);
    mockExtractAsciiDocTitle.mockImplementationOnce(({ signal: requestSignal }) => { signal = requestSignal!; return pending.promise; });
    await renderCatalog();
    await act(async () => { getButton(/^Launch$/).click(); });
    act(() => { ReactDOM.unmountComponentAtNode(container); });
    expect(signal.aborted).toBe(true);
    await act(async () => { pending.resolve('Never shown'); });
    expect(container.textContent).not.toContain('Never shown');
  });

  test('restores a valid page in a later module, but rejects a stale page path', async () => {
    mockLoadLabBundle.mockResolvedValue(multiModuleBundle);
    localStorage.setItem(sessionKey('lab-1'), JSON.stringify({ currentPagePath: multiModuleBundle.modules[1].pages[1].path, panelMode: 'open' }));
    await renderCatalog();
    await act(async () => { getButton(/^Launch$/).click(); });
    expect(moduleToggle().textContent).toContain('020-apply');
    expect(container.textContent).toContain('Page 2 of 2');
    act(() => { getButton(/close lab/i).click(); });
    localStorage.setItem(sessionKey('lab-1'), JSON.stringify({ currentPagePath: 'modules/020-apply/content/missing.adoc', panelMode: 'minimized' }));
    await act(async () => { getButton(/^Launch$/).click(); });
    expect(moduleToggle().textContent).toContain('010-intro');
    expect(container.textContent).toContain('Page 1 of 2');
  });

  test('has no selector for a single module and resumes only a known module page', async () => {
    mockLoadLabBundle.mockResolvedValue(noChartBundle);
    localStorage.setItem(sessionKey('lab-1'), JSON.stringify({ currentPagePath: lastPage.path, panelMode: 'open' }));
    await renderCatalog();
    await act(async () => { getButton(/^Launch$/).click(); });
    expect(container.querySelector('button[aria-label="Module"]')).toBeNull();
    expect(container.querySelector('.pfext-lab-module-title')?.textContent).toBe('010-intro');
    expect(container.textContent).toContain('Page 2 of 2');
    expect(mockExtractAsciiDocTitle).toHaveBeenCalledWith(expect.objectContaining({ sourceURL: firstPage.url }));
  });

  test('updates single-module header with plain first-page title when background loading finishes', async () => {
    const title = deferred<string | undefined>();
    mockLoadLabBundle.mockResolvedValue(noChartBundle);
    mockExtractAsciiDocTitle.mockReturnValue(title.promise);
    await renderCatalog();
    await act(async () => { getButton(/^Launch$/).click(); });
    expect(container.querySelector('.pfext-lab-module-title')?.textContent).toBe('010-intro');
    await act(async () => { title.resolve('Intro <em>overview</em> &amp; more'); });
    expect(container.querySelector('.pfext-lab-module-title')?.textContent).toBe('Intro overview & more');
    expect(container.querySelector('.pfext-lab-module-title em')).toBeNull();
    expect(moduleOptions()).toHaveLength(0);
  });

  test('opens known cross-module relative links and catalog-description links only on descriptor match', async () => {
    mockLoadLabBundle.mockResolvedValue(multiModuleBundle);
    mockRenderAsciiDoc.mockImplementation(({ sourceURL }) => Promise.resolve({
      html: sourceURL.endsWith('/lab.yaml')
        ? '<a href="modules/020-apply/content/020-done.adoc">Catalog link</a><a href="modules/020-apply/content/missing.adoc">Unknown</a>'
        : '<a href="../../../020-apply/content/010-do.adoc">Cross module</a>', warnings: [],
    }));
    await renderCatalog();
    await act(async () => { getButton(/^Launch$/).click(); });
    await act(async () => {
      Array.from(container.querySelectorAll('a')).find((link) => link.textContent === 'Cross module')!.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }));
    });
    expect(moduleToggle().textContent).toContain('020-apply');
    await act(async () => {
      Array.from(container.querySelectorAll('a')).find((link) => link.textContent === 'Catalog link')!.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }));
    });
    expect(localStorage.getItem(sessionKey('lab-1'))).toContain('modules/020-apply/content/020-done.adoc');
    await act(async () => {
      Array.from(container.querySelectorAll('a')).find((link) => link.textContent === 'Unknown')!.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }));
    });
    expect(container.textContent).toContain('Link is unavailable or unsafe.');
    expect(mockLoadLabBundle).toHaveBeenCalledTimes(3);
  });

  test('highlights only renderer-created valid guidance markers already visible in Console', async () => {
    const target = guidanceTarget('qs-nav-search');
    const scrollIntoView = jest.fn();
    target.scrollIntoView = scrollIntoView;
    await launchGuidanceLab('<a href="/search-page" data-interactive-labs-guidance-id="qs-nav-search">Search</a>');

    const link = Array.from(container.querySelectorAll('a')).find((candidate) => candidate.textContent === 'Search')!;
    expect(link.dataset.interactiveLabsGuidanceId).toBe('qs-nav-search');
    expect(link.closest('.interactive-labs-content')).not.toBeNull();
    const click = new MouseEvent('click', { bubbles: true, cancelable: true });
    await act(async () => { link.dispatchEvent(click); });
    await act(async () => { await new Promise((resolve) => requestAnimationFrame(resolve)); });
    expect(click.defaultPrevented).toBe(true);
    expect(mockNavigate).not.toHaveBeenCalled();
    expect(container.querySelector('.interactive-labs-guidance-ring')).not.toBeNull();
    expect(scrollIntoView).toHaveBeenCalled();
    expect(container.querySelector('[role="status"]')?.textContent).toMatch(/highlighted/i);
  });

  test('rejects a forged guidance marker rather than spotlighting its matching target', async () => {
    const target = guidanceTarget('qs-nav-search');
    await launchGuidanceLab('<a href="/search-page" data-interactive-labs-guidance-id="qs-nav-search">Search</a>', false);

    await act(async () => {
      Array.from(container.querySelectorAll('a')).find((candidate) => candidate.textContent === 'Search')!
        .dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }));
    });

    expect(mockNavigate).not.toHaveBeenCalled();
    expect(container.querySelector('.interactive-labs-guidance-ring')).toBeNull();
    expect(container.textContent).toMatch(/unavailable or unsafe/i);
    target.remove();
  });

  test('revalidates a guidance marker against fixed registry instead of trusting its href or arbitrary ID', async () => {
    await launchGuidanceLab([
      '<a href="/arbitrary" data-interactive-labs-guidance-id="qs-nav-search">Bad route</a>',
      '<a href="/search-page" data-interactive-labs-guidance-id="not-a-registry-id">Bad ID</a>',
    ].join(''));

    for (const link of Array.from(container.querySelectorAll('a'))) {
      await act(async () => { link.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true })); });
    }

    expect(mockNavigate).not.toHaveBeenCalled();
    expect(document.querySelector('[data-quickstart-id="not-a-registry-id"]')).toBeNull();
    expect(container.querySelector('.interactive-labs-guidance-ring')).toBeNull();
  });

  test('never navigates or waits for an absent visible-only guidance target', async () => {
    await launchGuidanceLab('<a href="#" data-interactive-labs-guidance-id="qs-nav-home">Home</a>');
    const observer = jest.spyOn(window, 'MutationObserver');

    await act(async () => {
      Array.from(container.querySelectorAll('a')).find((candidate) => candidate.textContent === 'Home')!
        .dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }));
    });

    expect(mockNavigate).not.toHaveBeenCalled();
    expect(observer).not.toHaveBeenCalled();
    expect(container.querySelector('[role="status"]')?.textContent).toMatch(/unavailable/i);
    observer.mockRestore();
  });

  test('navigates route-backed guidance once, then observes exact delayed visible target at expected route', async () => {
    mockNavigate.mockImplementation((route: string) => { mockLocation = { pathname: route, search: '' }; });
    await launchGuidanceLab('<a href="/search-page" data-interactive-labs-guidance-id="qs-nav-search">Search</a>');
    const link = Array.from(container.querySelectorAll('a')).find((candidate) => candidate.textContent === 'Search')!;

    await act(async () => { link.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true })); });
    expect(mockNavigate).toHaveBeenCalledTimes(1);
    expect(mockNavigate).toHaveBeenCalledWith('/search-page');

    const target = guidanceTarget('qs-nav-search');
    const scrollIntoView = jest.fn();
    target.scrollIntoView = scrollIntoView;
    await act(async () => { await new Promise((resolve) => requestAnimationFrame(resolve)); });

    expect(scrollIntoView).toHaveBeenCalled();
    expect(container.querySelector('.interactive-labs-guidance-ring')).not.toBeNull();
  });

  test('dismisses spotlight on any subsequent document click', async () => {
    const target = guidanceTarget('qs-nav-search');
    await launchGuidanceLab('<a href="/search-page" data-interactive-labs-guidance-id="qs-nav-search">Search</a>');
    const link = Array.from(container.querySelectorAll('a')).find((candidate) => candidate.textContent === 'Search')!;

    await act(async () => { link.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true })); });
    await act(async () => { await new Promise((resolve) => requestAnimationFrame(resolve)); });
    expect(container.querySelector('.interactive-labs-guidance-ring')).not.toBeNull();

    await act(async () => { document.body.dispatchEvent(new MouseEvent('click', { bubbles: true })); });
    expect(container.querySelector('.interactive-labs-guidance-ring')).toBeNull();
    target.remove();
  });

  test('keeps replacement guidance spotlight after its activating click bubbles', async () => {
    guidanceTarget('qs-nav-search');
    guidanceTarget('qs-nav-topology', { left: 220, right: 320, top: 10, bottom: 40 });
    await launchGuidanceLab([
      '<a href="/search-page" data-interactive-labs-guidance-id="qs-nav-search">Search</a>',
      '<a href="/topology" data-interactive-labs-guidance-id="qs-nav-topology">Topology</a>',
    ].join(''));
    const search = Array.from(container.querySelectorAll('a')).find((candidate) => candidate.textContent === 'Search')!;
    const topology = Array.from(container.querySelectorAll('a')).find((candidate) => candidate.textContent === 'Topology')!;

    await act(async () => { search.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true })); });
    await act(async () => { await new Promise((resolve) => requestAnimationFrame(resolve)); });
    const previousRing = container.querySelector<HTMLElement>('.interactive-labs-guidance-ring');
    await act(async () => { topology.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true })); });
    await act(async () => { await new Promise((resolve) => requestAnimationFrame(resolve)); });

    const ring = container.querySelector<HTMLElement>('.interactive-labs-guidance-ring');
    expect(ring).not.toBeNull();
    expect(ring).not.toBe(previousRing);
    expect(ring?.style.left).toBe('220px');
  });

  test('keeps spotlight through Quick Starts pulse and removes it after its full animation', async () => {
    jest.useFakeTimers();
    try {
      guidanceTarget('qs-nav-search');
      await launchGuidanceLab('<a href="/search-page" data-interactive-labs-guidance-id="qs-nav-search">Search</a>');
      const link = Array.from(container.querySelectorAll('a')).find((candidate) => candidate.textContent === 'Search')!;

      await act(async () => { link.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true })); });
      await act(async () => { jest.advanceTimersByTime(16); });
      expect(container.querySelector('.interactive-labs-guidance-ring')).not.toBeNull();

      await act(async () => { jest.advanceTimersByTime(10_000); });
      expect(container.querySelector('.interactive-labs-guidance-ring')).not.toBeNull();
      await act(async () => { jest.advanceTimersByTime(7_800); });
      expect(container.querySelector('.interactive-labs-guidance-ring')).toBeNull();
    } finally {
      jest.useRealTimers();
    }
  });

  test('dismisses spotlight on Escape and target interaction, and cancels it on drawer close', async () => {
    const target = guidanceTarget('qs-nav-search');
    await launchGuidanceLab('<a href="/search-page" data-interactive-labs-guidance-id="qs-nav-search">Search</a>');
    const link = Array.from(container.querySelectorAll('a')).find((candidate) => candidate.textContent === 'Search')!;

    await act(async () => { link.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true })); });
    await act(async () => { await new Promise((resolve) => requestAnimationFrame(resolve)); });
    expect(container.querySelector('.interactive-labs-guidance-ring')).not.toBeNull();
    await act(async () => { document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true })); });
    expect(container.querySelector('.interactive-labs-guidance-ring')).toBeNull();

    await act(async () => { link.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true })); });
    await act(async () => { await new Promise((resolve) => requestAnimationFrame(resolve)); });
    await act(async () => { target.dispatchEvent(new MouseEvent('click', { bubbles: true })); });
    expect(container.querySelector('.interactive-labs-guidance-ring')).toBeNull();

    await act(async () => { link.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true })); });
    await act(async () => { await new Promise((resolve) => requestAnimationFrame(resolve)); });
    act(() => { getButton(/close/i).click(); });
    expect(container.querySelector('.interactive-labs-guidance-ring')).toBeNull();
  });

  test('minimizes only horizontally overlapping guidance targets; resource links retain ordinary open-drawer navigation', async () => {
    const target = guidanceTarget('qs-nav-search', { left: 700, right: 800, top: 900, bottom: 930 });
    const scrollIntoView = jest.fn();
    target.scrollIntoView = scrollIntoView;
    await launchGuidanceLab([
      '<a href="/search-page" data-interactive-labs-guidance-id="qs-nav-search">Search</a>',
      '<a href="/k8s/all-namespaces/apps~v1~Deployment">Deployment</a>',
    ].join(''));
    const panel = container.querySelector('[class*="drawer__panel"]') as HTMLElement;
    panel.getBoundingClientRect = () => ({ left: 600, right: 1000, top: 0, bottom: 1000, width: 400, height: 1000, x: 600, y: 0, toJSON: () => ({}) }) as DOMRect;

    await act(async () => {
      Array.from(container.querySelectorAll('a')).find((candidate) => candidate.textContent === 'Search')!
        .dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }));
    });
    await act(async () => { await new Promise((resolve) => requestAnimationFrame(resolve)); });
    expect(getButton(/restore lab/i)).toBeTruthy();
    expect(scrollIntoView).toHaveBeenCalled();

    act(() => { getButton(/restore lab/i).click(); });
    await act(async () => {});
    await act(async () => {
      Array.from(container.querySelectorAll('a')).find((candidate) => candidate.textContent === 'Deployment')!
        .dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }));
    });
    expect(mockNavigate).toHaveBeenLastCalledWith('/k8s/all-namespaces/apps~v1~Deployment');
    expect(container.querySelector('button[aria-label="Minimize lab"]')).not.toBeNull();
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
