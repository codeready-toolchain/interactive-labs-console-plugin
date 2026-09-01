/**
 * STORY-2 tests — List Available Labs
 * Tests use React 17 react-dom/test-utils; no Testing Library.
 */

import * as React from 'react';
import * as ReactDOM from 'react-dom';
import { act } from 'react-dom/test-utils';
import { InteractiveLabsPage } from './InteractiveLabsPage';
import type { CatalogList } from './types/catalog';
import type { LabError } from './types/errors';
import * as client from './api/client';

jest.mock('./api/client');

const mockListCatalog = client.listCatalog as jest.MockedFunction<typeof client.listCatalog>;

let container: HTMLDivElement;

beforeEach(() => {
  container = document.createElement('div');
  document.body.appendChild(container);
  mockListCatalog.mockReset();
  (client.loadLabBundle as jest.MockedFunction<typeof client.loadLabBundle>).mockReset();
  (client.setupLab as jest.MockedFunction<typeof client.setupLab>).mockReset();
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

  test('renders disabled Launch button per entry with no launch side-effects', async () => {
    mockListCatalog.mockResolvedValue(sampleCatalog);

    await act(async () => {
      ReactDOM.render(<InteractiveLabsPage />, container);
    });

    const launchButtons = Array.from(container.querySelectorAll('button')).filter(
      b => b.textContent?.trim() === 'Launch',
    );
    expect(launchButtons).toHaveLength(sampleCatalog.items.length);
    launchButtons.forEach(btn => expect((btn as HTMLButtonElement).disabled).toBe(true));

    expect(client.loadLabBundle).not.toHaveBeenCalled();
    expect(client.setupLab).not.toHaveBeenCalled();
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
