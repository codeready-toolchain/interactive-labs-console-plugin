import * as React from 'react';
import * as ReactDOM from 'react-dom';
import { act } from 'react-dom/test-utils';
import { LabSetups } from './LabSetups';
import { getTeardownStatus, listSetups, teardownLab } from './api/client';
import type { LabSetup } from './types/setup';

jest.mock('./api/client');
const list = listSetups as jest.MockedFunction<typeof listSetups>;
const teardown = teardownLab as jest.MockedFunction<typeof teardownLab>;
const getStatus = getTeardownStatus as jest.MockedFunction<typeof getTeardownStatus>;
const ref = 'ab'.repeat(32);
const operationId = '12'.repeat(16);
const progress = { apiVersion: 'v1alpha1' as const, kind: 'TeardownStatus' as const, catalogRef: ref, operationId, status: 'running' as const };
const item: LabSetup = { catalogRef: ref, generation: 'cd'.repeat(32), status: 'deployed', spaceRequests: ['gitea'], release: { name: 'il-removed-lab' } };
let container: HTMLDivElement;
let confirm: jest.SpyInstance;

beforeEach(() => {
  container = document.createElement('div'); document.body.appendChild(container);
  list.mockReset(); teardown.mockReset(); getStatus.mockReset();
  window.sessionStorage.removeItem('interactive-labs-teardown');
  list.mockResolvedValue({ apiVersion: 'v1alpha1', kind: 'SetupList', items: [item] });
  teardown.mockResolvedValue(progress);
  getStatus.mockResolvedValue({ ...progress, status: 'succeeded' });
  confirm = jest.spyOn(window, 'confirm').mockReturnValue(false);
});
afterEach(() => { act(() => { ReactDOM.unmountComponentAtNode(container); }); container.remove(); confirm.mockRestore(); window.sessionStorage.removeItem('interactive-labs-teardown'); });
// Exercise the public action slot; the page tests cover the actual PatternFly row layout.
const render = async (): Promise<void> => {
  await act(async () => {
    ReactDOM.render(<LabSetups catalog={[]}>{({ items, existingCatalogRefs, teardown }) => <ul>
      {existingCatalogRefs.map((catalogRef) => {
        const setup = items.find((candidate) => candidate.catalogRef === catalogRef);
        return <li key={catalogRef}>
          <strong>{setup?.release?.name ?? catalogRef}</strong>
          {setup && <p>SpaceRequests: {setup.spaceRequests.join(', ') || 'none'}</p>}
          {teardown(catalogRef)}
        </li>;
      })}
    </ul>}</LabSetups>, container);
  });
};
const button = (name: string): HTMLButtonElement => {
  const result = Array.from(container.querySelectorAll('button')).find((b) => b.textContent === name);
  if (!result) throw new Error(`Missing ${name}`); return result;
};

describe('catalog inventory polling', () => {
  const pending: LabSetup = { ...item, status: 'pending', release: undefined, blocked: true, blockedReason: 'pending' };
  const remnant: LabSetup = { ...item, release: undefined, blocked: true, blockedReason: 'cleanup' };
  const otherRef = 'ef'.repeat(32);
  const other: LabSetup = { ...item, catalogRef: otherRef, release: { name: 'il-other' } };
  const catalog = [{ catalogRef: ref, title: 'Configured lab', labPath: 'lab' }, { catalogRef: otherRef, title: 'Other lab', labPath: 'other' }];
  const inventory = (...items: LabSetup[]): Awaited<ReturnType<typeof listSetups>> => ({ apiVersion: 'v1alpha1', kind: 'SetupList', items });
  const renderCatalog = async (refreshVersion = 0): Promise<void> => {
    await act(async () => {
      ReactDOM.render(<LabSetups catalog={catalog} refreshVersion={refreshVersion}>{({ items, disabledFor, teardown }) => catalog.map((entry) => {
        const setup = items.find((candidate) => candidate.catalogRef === entry.catalogRef);
        return <div key={entry.catalogRef} data-catalog-ref={entry.catalogRef}>
          <output>{setup?.status ?? 'absent'}</output>
          <button disabled={disabledFor(entry.catalogRef)}>{setup ? 'Open' : 'Launch'}</button>
          {teardown(entry.catalogRef)}
        </div>;
      })}</LabSetups>, container);
    });
  };
  const row = (catalogRef = ref): HTMLElement => container.querySelector(`[data-catalog-ref="${catalogRef}"]`)!;
  const rowButton = (name: string, catalogRef = ref): HTMLButtonElement => {
    const result = Array.from(row(catalogRef).querySelectorAll('button')).find((candidate) => candidate.textContent === name);
    if (!result) throw new Error(`Missing ${name} for ${catalogRef}`); return result;
  };
  const advance = async (milliseconds = 10000): Promise<void> => { await act(async () => { jest.advanceTimersByTime(milliseconds); }); };

  beforeEach(() => { jest.useFakeTimers(); });
  afterEach(() => { jest.useRealTimers(); });

  test.each([
    ['pending setup', pending, item],
    ['backend cleanup blocker', remnant, undefined],
    ['held teardown on deployed release', { ...item, blocked: true, blockedReason: 'operation' }, item],
    ['pending release-free cleanup remnant', { ...remnant, status: 'pending', blocked: false, blockedReason: undefined }, undefined],
  ] as const)('refreshes %s without a click and stops when settled', async (_name, initial, settled) => {
    list.mockResolvedValueOnce(inventory(initial, other)).mockResolvedValue(inventory(...(settled ? [settled, other] : [other])));
    await renderCatalog();
    expect(row().querySelector('output')?.textContent).toBe(initial.status);
    if (initial.status === 'pending' || initial.blocked) expect(rowButton('Tear down').disabled).toBe(true);
    await advance(9999);
    expect(list).toHaveBeenCalledTimes(1);
    await advance(1);
    expect(list).toHaveBeenCalledTimes(2);
    expect(row().querySelector('output')?.textContent).toBe(settled?.status ?? 'absent');
    expect(rowButton(settled ? 'Open' : 'Launch').disabled).toBe(false);
    expect(row().textContent).not.toContain('Setup is pending.');
    expect(row().textContent).not.toContain('Lab namespace cleanup is still in progress.');
    await advance(30000);
    expect(list).toHaveBeenCalledTimes(2);
    expect(teardown).not.toHaveBeenCalled();
    expect(getStatus).not.toHaveBeenCalled();
  });

  test.each([
    ['failed release', { ...item, status: 'failed' as const }, true],
    ['held deployed release', { ...item, blocked: true, blockedReason: 'operation' as const }, true],
    ['pending held release', { ...item, status: 'pending' as const, blocked: true, blockedReason: 'operation' as const }, false],
    ['pending without release', pending, false],
    ['cleanup', remnant, true],
    ['release-free remnants', { ...item, release: undefined }, true],
    ['unsafe retained deployed release', { ...item, blocked: true, blockedReason: 'helm-retained' as const }, false],
    ['unsafe hook deployed release', { ...item, blocked: true, blockedReason: 'helm-hook' as const }, false],
  ] as const)('inventory owns Open eligibility for %s', async (_name, setup, disabled) => {
    list.mockResolvedValue(inventory(setup, other));
    await renderCatalog();
    expect(rowButton('Open').disabled).toBe(disabled);
    expect(rowButton('Open', otherRef).disabled).toBe(false);
  });

  test.each(['unknown', 'failed', 'missing', 'rejected-discovery'] as const)('%s outcome waits for fresh inventory before Open or reconfirmation, retries failed GET locally', async (outcome) => {
    list.mockResolvedValueOnce(inventory(item, other)).mockRejectedValueOnce(new Error('inventory offline'));
    await renderCatalog();
    let terminal!: (value: Awaited<ReturnType<typeof getTeardownStatus>>) => void;
    let reject!: (error: unknown) => void;
    getStatus.mockReturnValueOnce(new Promise((resolve, fail) => { terminal = resolve; reject = fail; }));
    if (outcome === 'rejected-discovery') teardown.mockRejectedValue(new Error('lost POST'));
    confirm.mockReturnValue(true);
    await act(async () => { rowButton('Tear down').click(); });
    await act(async () => {
      if (outcome === 'missing' || outcome === 'rejected-discovery') reject({ status: 404 });
      else terminal({ ...progress, status: outcome });
    });
    expect(list).toHaveBeenCalledTimes(2);
    expect(rowButton('Open').disabled).toBe(true);
    expect(rowButton('Tear down').disabled).toBe(true);
    expect(row().textContent).not.toContain('Waiting for previous lab resources to disappear.');
    expect(row().textContent).not.toMatch(/cleanup completed|cleanup succeeded/i);
    expect(rowButton('Open', otherRef).disabled).toBe(false);
    expect(rowButton('Tear down', otherRef).disabled).toBe(false);
    await act(async () => { rowButton('Tear down').click(); });
    expect(teardown).toHaveBeenCalledTimes(1);
    expect(confirm).toHaveBeenCalledTimes(1);
    let fresh!: (value: Awaited<ReturnType<typeof listSetups>>) => void;
    list.mockReturnValueOnce(new Promise((resolve) => { fresh = resolve; }));
    await advance();
    expect(list).toHaveBeenCalledTimes(3);
    expect(rowButton('Open').disabled).toBe(true);
    expect(rowButton('Tear down').disabled).toBe(true);
    expect(rowButton('Open', otherRef).disabled).toBe(false);
    const current = { ...item, generation: '01'.repeat(32), spaceRequests: ['new-sr'], release: { ...item.release!, name: 'il-current' } };
    await act(async () => { fresh(inventory(current, other)); });
    expect(rowButton('Open').disabled).toBe(false);
    expect(rowButton('Tear down').disabled).toBe(false);
    teardown.mockResolvedValue(progress);
    getStatus.mockImplementation(() => new Promise(() => {}));
    await act(async () => { rowButton('Tear down').click(); });
    expect(teardown).toHaveBeenLastCalledWith(ref, current.generation);
    expect(confirm).toHaveBeenLastCalledWith(expect.stringContaining('il-current'));
    expect(confirm).toHaveBeenLastCalledWith(expect.stringContaining('new-sr'));
  });

  test('terminal outcome invalidates an older inventory GET and waits for the next serial fresh GET', async () => {
    window.sessionStorage.setItem('interactive-labs-teardown', JSON.stringify({ catalogRef: ref, operationId }));
    let terminal!: (value: Awaited<ReturnType<typeof getTeardownStatus>>) => void;
    let stale!: (value: Awaited<ReturnType<typeof listSetups>>) => void;
    let fresh!: (value: Awaited<ReturnType<typeof listSetups>>) => void;
    getStatus.mockReturnValue(new Promise((resolve) => { terminal = resolve; }));
    list.mockResolvedValueOnce(inventory(item, { ...other, status: 'pending' }))
      .mockReturnValueOnce(new Promise((resolve) => { stale = resolve; }))
      .mockReturnValueOnce(new Promise((resolve) => { fresh = resolve; }));
    await renderCatalog();
    await advance();
    expect(list).toHaveBeenCalledTimes(2);
    await act(async () => { terminal({ ...progress, status: 'unknown' }); });
    expect(list).toHaveBeenCalledTimes(2);
    expect(rowButton('Open').disabled).toBe(true);
    await act(async () => { stale(inventory(item, other)); });
    expect(list).toHaveBeenCalledTimes(3);
    expect(rowButton('Open').disabled).toBe(true);
    expect(rowButton('Tear down').disabled).toBe(true);
    expect(row().textContent).toContain('Waiting for refreshed lab setups');
    await act(async () => { fresh(inventory(item, other)); });
    expect(rowButton('Open').disabled).toBe(false);
    expect(rowButton('Tear down').disabled).toBe(false);
    expect(row().textContent).not.toContain('Waiting for refreshed lab setups');
    await advance(30000);
    expect(list).toHaveBeenCalledTimes(3);
    expect(teardown).not.toHaveBeenCalled();
  });

  test('unknown outcome preserves notice after inventory vanishes while Launch becomes available', async () => {
    list.mockResolvedValueOnce(inventory(item, other)).mockResolvedValue(inventory(other));
    getStatus.mockResolvedValue({ ...progress, status: 'unknown' });
    await renderCatalog(); confirm.mockReturnValue(true);
    await act(async () => { rowButton('Tear down').click(); });
    expect(rowButton('Launch').disabled).toBe(false);
    expect(row().textContent).toContain('outcome could not be verified');
    expect(row().querySelectorAll('button')).toHaveLength(1);
  });

  test('missing teardown receipt with no setup permits Launch after refresh', async () => {
    window.sessionStorage.setItem('interactive-labs-teardown', JSON.stringify({ catalogRef: ref, operationId }));
    getStatus.mockRejectedValue({ status: 404 });
    list.mockResolvedValue(inventory());
    await renderCatalog();
    expect(rowButton('Launch').disabled).toBe(false);
    expect(window.sessionStorage.getItem('interactive-labs-teardown')).toBeNull();
    await advance(9000);
    expect(getStatus).toHaveBeenCalledTimes(1);
    expect(teardown).not.toHaveBeenCalled();
  });

  test.each([
    ['backend cleanup warning', { blocked: true, blockedReason: 'cleanup' }, 'Lab namespace cleanup is still in progress.', false],
    ['unrelated operation blocker', { blocked: true, blockedReason: 'operation' }, 'A lab operation is in progress', true],
    ['unrelated retained-resource blocker', { blocked: true, blockedReason: 'helm-retained' }, 'The Helm release contains retained resources', true],
    ['unblocked cleanup reason', { blocked: false, blockedReason: 'cleanup' }, undefined, true],
    ['pending unblocked cleanup reason', { status: 'pending', blocked: false, blockedReason: 'cleanup' }, 'Setup is pending.', true],
  ] as const)('successful teardown preserves notice precedence for %s', async (_name, patch, backendNotice, localNotice) => {
    // Given a teardown-eligible setup, then inventory retaining the same generation.
    list.mockResolvedValue(inventory(item, other));
    await renderCatalog(); confirm.mockReturnValue(true);
    list.mockResolvedValue(inventory({ ...item, ...patch }, other));
    // When teardown succeeds and refreshes inventory.
    await act(async () => { rowButton('Tear down').click(); });
    // Then only a displayed backend cleanup warning suppresses the local notice.
    expect(teardown).toHaveBeenCalledTimes(1);
    expect(getStatus).toHaveBeenCalledWith(ref, operationId);
    expect(list).toHaveBeenCalledTimes(2);
    expect(rowButton('Open').disabled).toBe(true);
    expect(rowButton('Tear down').disabled).toBe(true);
    if (backendNotice) expect(row().textContent).toContain(backendNotice);
    else expect(row().textContent).not.toContain('Lab namespace cleanup is still in progress.');
    expect(row().textContent?.includes('Waiting for previous lab resources to disappear. Checking setups automatically every 10 seconds.')).toBe(localNotice);
    expect(rowButton('Open', otherRef).disabled).toBe(false);
  });

  test('cleanupPending refreshes stale deployed inventory until absence restores Launch alone', async () => {
    list.mockResolvedValue(inventory(item, other));
    await renderCatalog(); confirm.mockReturnValue(true);
    await act(async () => { rowButton('Tear down').click(); });
    expect(list).toHaveBeenCalledTimes(2);
    expect(rowButton('Open').disabled).toBe(true);
    expect(row().textContent).toContain('Waiting for previous lab resources to disappear. Checking setups automatically every 10 seconds.');
    expect(row().textContent).not.toContain('Lab namespace cleanup is still in progress.');
    expect(row().textContent).not.toContain('Managed lab cleanup completed.');
    list.mockResolvedValue(inventory(other));
    await advance();
    expect(list).toHaveBeenCalledTimes(3);
    expect(rowButton('Launch').disabled).toBe(false);
    expect(row().textContent).not.toContain('Managed lab cleanup completed.');
    expect(row().textContent).not.toContain('Waiting for previous lab resources to disappear.');
    expect(Array.from(row().querySelectorAll('button')).map((button) => button.textContent)).toEqual(['Launch']);
    expect(row().querySelector('.pf-v6-c-alert')).toBeNull();
    await advance(30000);
    expect(list).toHaveBeenCalledTimes(3);
    expect(teardown).toHaveBeenCalledTimes(1);
    expect(getStatus).toHaveBeenCalledTimes(1);
  });

  test.each(['deployed', 'failed'] as const)('successful teardown clears old cleanup latch for a recreated %s release on fresh GET', async (status) => {
    list.mockResolvedValue(inventory(item, other));
    await renderCatalog(); confirm.mockReturnValue(true);
    let fresh!: (value: Awaited<ReturnType<typeof listSetups>>) => void;
    list.mockReturnValueOnce(new Promise((resolve) => { fresh = resolve; }));
    await act(async () => { rowButton('Tear down').click(); });
    expect(rowButton('Open').disabled).toBe(true);
    expect(rowButton('Tear down').disabled).toBe(true);
    const recreated: LabSetup = { ...item, generation: '01'.repeat(32), status, release: { name: 'il-recreated' } };
    list.mockResolvedValue(inventory(recreated, other));
    await act(async () => { fresh(inventory(recreated, other)); });
    expect(rowButton('Open').disabled).toBe(status === 'failed');
    expect(rowButton('Tear down').disabled).toBe(false);
    expect(row().textContent).not.toContain('Waiting for previous lab resources to disappear.');
    expect(rowButton('Open', otherRef).disabled).toBe(false);
    await advance(30000);
    expect(list).toHaveBeenCalledTimes(2);
    getStatus.mockImplementation(() => new Promise(() => {}));
    await act(async () => { rowButton('Tear down').click(); });
    expect(teardown).toHaveBeenLastCalledWith(ref, recreated.generation);
    expect(confirm).toHaveBeenLastCalledWith(expect.stringContaining('il-recreated'));
  });

  test.each(['cleanup', 'operation'] as const)('replacement release retains cleanup latch while %s remains blocked, then settles', async (blockedReason) => {
    list.mockResolvedValue(inventory(item, other));
    await renderCatalog(); confirm.mockReturnValue(true);
    const recreated: LabSetup = { ...item, generation: '01'.repeat(32), blocked: true, blockedReason, release: { name: 'il-recreated' } };
    list.mockResolvedValue(inventory(recreated, other));
    await act(async () => { rowButton('Tear down').click(); });
    expect(rowButton('Open').disabled).toBe(true);
    expect(rowButton('Tear down').disabled).toBe(true);
    expect(row().textContent?.includes('Waiting for previous lab resources to disappear.')).toBe(blockedReason !== 'cleanup');
    expect(row().textContent).toContain(blockedReason === 'cleanup' ? 'Lab namespace cleanup is still in progress.' : 'A lab operation is in progress');
    list.mockResolvedValue(inventory({ ...recreated, blocked: false, blockedReason: undefined }, other));
    await advance();
    expect(rowButton('Open').disabled).toBe(false);
    expect(rowButton('Tear down').disabled).toBe(false);
    expect(row().textContent).not.toContain('Waiting for previous lab resources to disappear.');
    await advance(30000);
    expect(list).toHaveBeenCalledTimes(3);
  });

  test('failed recreated release retains its own administrator blocker rather than old cleanup latch', async () => {
    list.mockResolvedValue(inventory(item, other));
    await renderCatalog(); confirm.mockReturnValue(true);
    list.mockResolvedValue(inventory({ ...item, generation: '01'.repeat(32), status: 'failed', blocked: true, blockedReason: 'helm-state' }, other));
    await act(async () => { rowButton('Tear down').click(); });
    expect(rowButton('Open').disabled).toBe(true);
    expect(rowButton('Tear down').disabled).toBe(true);
    expect(row().textContent).toContain('Contact an administrator to recover its state before teardown.');
    expect(row().textContent).not.toContain('Waiting for previous lab resources to disappear.');
    await advance(30000);
    expect(list).toHaveBeenCalledTimes(2);
  });

  test('background refresh leaves other row usable, shows no loading spinner and manual refresh piggybacks without overlapping requests', async () => {
    let resolve!: (value: Awaited<ReturnType<typeof listSetups>>) => void;
    list.mockResolvedValueOnce(inventory(pending, other)).mockReturnValueOnce(new Promise((finish) => { resolve = finish; }));
    await renderCatalog();
    await advance();
    expect(list).toHaveBeenCalledTimes(2);
    expect(rowButton('Open', otherRef).disabled).toBe(false);
    expect(rowButton('Tear down', otherRef).disabled).toBe(false);
    expect(rowButton('Tear down').disabled).toBe(true);
    expect(button('Refresh setups').disabled).toBe(false);
    expect(container.querySelector('[aria-label="Loading lab setups"]')).toBeNull();
    expect(container.querySelector('[role="progressbar"]')).toBeNull();
    await advance(30000);
    await act(async () => { button('Refresh setups').click(); });
    expect(list).toHaveBeenCalledTimes(2);
    list.mockResolvedValue(inventory(item, other));
    await act(async () => { resolve(inventory(pending, other)); });
    await advance();
    expect(list).toHaveBeenCalledTimes(3);
    expect(row().querySelector('output')?.textContent).toBe('deployed');
    await advance(30000);
    expect(list).toHaveBeenCalledTimes(3);
  });

  test('refreshVersion invalidates an in-flight background response and queues fresh inventory without overlap', async () => {
    let stale!: (value: Awaited<ReturnType<typeof listSetups>>) => void;
    let fresh!: (value: Awaited<ReturnType<typeof listSetups>>) => void;
    list.mockResolvedValueOnce(inventory(pending, other))
      .mockReturnValueOnce(new Promise((resolve) => { stale = resolve; }))
      .mockReturnValueOnce(new Promise((resolve) => { fresh = resolve; }));
    await renderCatalog();
    await advance();
    expect(list).toHaveBeenCalledTimes(2);
    await renderCatalog(1);
    expect(list).toHaveBeenCalledTimes(2);
    await act(async () => { stale(inventory(other)); });
    expect(list).toHaveBeenCalledTimes(3);
    expect(row().querySelector('output')?.textContent).toBe('pending');
    await act(async () => { fresh(inventory(item, other)); });
    expect(row().querySelector('output')?.textContent).toBe('deployed');
    await advance(30000);
    expect(list).toHaveBeenCalledTimes(3);
  });

  test.each([false, true])('unmount stops inventory polling with an in-flight request=%s', async (inFlight) => {
    let resolve!: (value: Awaited<ReturnType<typeof listSetups>>) => void;
    list.mockResolvedValue(inventory(pending, other));
    await renderCatalog();
    if (inFlight) list.mockReturnValueOnce(new Promise((finish) => { resolve = finish; }));
    await advance();
    expect(list).toHaveBeenCalledTimes(2);
    act(() => { ReactDOM.unmountComponentAtNode(container); });
    if (inFlight) await act(async () => { resolve(inventory(pending, other)); });
    await advance(30000);
    expect(list).toHaveBeenCalledTimes(2);
    expect(teardown).not.toHaveBeenCalled();
  });

  test('transient inventory error retries in background and clears after a successful refresh', async () => {
    list.mockResolvedValueOnce(inventory(pending, other))
      .mockRejectedValueOnce(new Error('inventory temporarily offline'))
      .mockResolvedValue(inventory(item, other));
    await renderCatalog();
    await advance();
    expect(list).toHaveBeenCalledTimes(2);
    expect(row().querySelector('output')?.textContent).toBe('pending');
    expect(rowButton('Open', otherRef).disabled).toBe(false);
    expect(rowButton('Tear down', otherRef).disabled).toBe(false);
    expect(container.querySelector('[aria-label="Loading lab setups"]')).toBeNull();
    await advance();
    expect(list).toHaveBeenCalledTimes(3);
    expect(row().querySelector('output')?.textContent).toBe('deployed');
    expect(container.textContent).not.toContain('inventory temporarily offline');
    await advance(30000);
    expect(list).toHaveBeenCalledTimes(3);
  });

  test.each([
    ['absent', []],
    ['deployed', [item]],
    ['failed, blocked release', [{ ...item, status: 'failed' as const, blocked: true, blockedReason: 'helm-state' as const }]],
    ['failed, release-free setup', [{ ...item, status: 'failed' as const, release: undefined }]],
  ] as const)('does not poll initially settled %s inventory', async (_name, items) => {
    list.mockResolvedValue(inventory(...items));
    await renderCatalog();
    await advance(30000);
    expect(list).toHaveBeenCalledTimes(1);
    expect(teardown).not.toHaveBeenCalled();
    expect(getStatus).not.toHaveBeenCalled();
  });
});

describe('lab setup teardown', () => {
  test('finds setup after catalog removal and displays targets before explicit confirmation', async () => {
    await render();
    expect(container.textContent).toContain('il-removed-lab');
    expect(container.textContent).toContain('gitea');
    expect(teardown).not.toHaveBeenCalled();
    await act(async () => { button('Tear down').click(); });
    expect(confirm).toHaveBeenCalledWith(expect.stringContaining('gitea'));
    expect(confirm).toHaveBeenCalledWith(expect.stringContaining('il-removed-lab'));
    expect(teardown).not.toHaveBeenCalled();
  });
  test('confirmation posts selected identity and refreshes live inventory', async () => {
    await render(); confirm.mockReturnValue(true);
    list.mockResolvedValue({ apiVersion: 'v1alpha1', kind: 'SetupList', items: [] });
    await act(async () => { button('Tear down').click(); });
    expect(teardown).toHaveBeenCalledTimes(1);
    expect(teardown).toHaveBeenCalledWith(ref, item.generation);
    expect(container.querySelectorAll('li')).toHaveLength(0);
    expect(container.textContent).not.toContain('Managed lab cleanup completed.');
  });
  test('accepted teardown shows spinner until status reports completion', async () => {
    await render(); confirm.mockReturnValue(true);
    getStatus.mockImplementation(() => new Promise(() => {}));
    await act(async () => { button('Tear down').click(); });
    expect(button('Tear down').disabled).toBe(true);
    expect(container.textContent).toContain('Tear down');
    expect(teardown).toHaveBeenCalledTimes(1);
    expect(getStatus).toHaveBeenCalledWith(ref, operationId);
    expect(window.sessionStorage.getItem('interactive-labs-teardown')).toContain(operationId);
    const row = container.querySelector('li')!;
    expect(row.textContent).toContain('Lab teardown in progress');
    expect(row.querySelector('[aria-label="Tearing down lab"]')).not.toBeNull();
    expect(row.querySelector('[aria-label="Tearing down lab"]')!.classList.contains('pf-m-inline')).toBe(true);
    expect(row.querySelector('.pf-v6-c-alert')).toBeNull();
  });
  test('cleanupPending blocks repeated teardown while another lab stays usable', async () => {
    const otherRef = 'ef'.repeat(32);
    const other = { ...item, catalogRef: otherRef, release: { name: 'il-other' } };
    const inventory = { apiVersion: 'v1alpha1' as const, kind: 'SetupList' as const, items: [item, other] };
    list.mockResolvedValue(inventory);
    let finish!: (value: Awaited<ReturnType<typeof getTeardownStatus>>) => void;
    getStatus.mockReturnValueOnce(new Promise((resolve) => { finish = resolve; })).mockImplementation(() => new Promise(() => {}));
    await render();
    confirm.mockReturnValue(true);
    const rows = Array.from(container.querySelectorAll('li'));
    await act(async () => { rows[0].querySelector('button')!.click(); });
    let refresh!: (value: typeof inventory) => void;
    list.mockReturnValueOnce(new Promise((resolve) => { refresh = resolve; }));
    await act(async () => { finish({ ...progress, status: 'succeeded' }); });
    expect(rows[0].querySelector('button')!.disabled).toBe(true);
    await act(async () => { refresh(inventory); });
    // A stale, unblocked inventory row must not allow a second POST after success.
    await act(async () => { rows[0].querySelector('button')!.click(); });
    expect(teardown).toHaveBeenCalledTimes(1);
    expect(confirm).toHaveBeenCalledTimes(1);
    expect(rows[0].querySelector('button')!.disabled).toBe(true);
    expect(rows[0].textContent).toContain('Waiting for previous lab resources to disappear.');
    expect(rows[0].textContent).not.toContain('Managed lab cleanup completed.');
    expect(rows[1].querySelector('button')!.disabled).toBe(false);
    teardown.mockResolvedValue({ ...progress, catalogRef: otherRef, operationId: '34'.repeat(16) });
    await act(async () => { rows[1].querySelector('button')!.click(); });
    expect(teardown).toHaveBeenCalledTimes(2);
    expect(teardown).toHaveBeenLastCalledWith(otherRef, other.generation);
  });
  test('synchronous reentrant DOM click cannot confirm or POST the same teardown twice before React updates', async () => {
    await render(); confirm.mockReturnValue(true);
    getStatus.mockImplementation(() => new Promise(() => {}));
    teardown.mockImplementationOnce(() => {
      // Dispatch inside the first handler, before React commits its disabled button.
      expect(button('Tear down').disabled).toBe(false);
      button('Tear down').click();
      return Promise.resolve(progress);
    });
    await act(async () => { button('Tear down').click(); });
    expect(confirm).toHaveBeenCalledTimes(1);
    expect(teardown).toHaveBeenCalledTimes(1);
    expect(button('Tear down').disabled).toBe(true);
  });
  test('vanished orphan retains uncertain notice without a stale teardown button', async () => {
    await render(); confirm.mockReturnValue(true);
    list.mockResolvedValue({ apiVersion: 'v1alpha1', kind: 'SetupList', items: [] });
    getStatus.mockResolvedValue({ ...progress, status: 'unknown' });
    await act(async () => { button('Tear down').click(); });
    expect(container.querySelector('li')?.textContent).toContain('outcome could not be verified');
    expect(container.querySelector('li button')).toBeNull();
    expect(teardown).toHaveBeenCalledTimes(1);
  });
  test.each(['accepted', 'discovered'] as const)('late %s POST after remount is adopted before deferred inventory GET and survives it', async (response) => {
    let post!: (value: Awaited<ReturnType<typeof teardownLab>>) => void;
    let rejectPost!: (error: Error) => void;
    let inventory!: (value: Awaited<ReturnType<typeof listSetups>>) => void;
    let terminal!: (value: Awaited<ReturnType<typeof getTeardownStatus>>) => void;
    const terminalStatus = new Promise<Awaited<ReturnType<typeof getTeardownStatus>>>((resolve) => { terminal = resolve; });
    teardown.mockReturnValue(new Promise((resolve, reject) => { post = resolve; rejectPost = reject; }));
    getStatus.mockImplementation((_catalogRef, operationId) => operationId ? terminalStatus : Promise.resolve(progress));
    await render(); confirm.mockReturnValue(true);
    await act(async () => { button('Tear down').click(); });
    act(() => { ReactDOM.unmountComponentAtNode(container); });
    list.mockReturnValueOnce(new Promise((resolve) => { inventory = resolve; }));
    await render();
    await act(async () => {
      if (response === 'accepted') post(progress);
      else rejectPost(new Error('lost POST response'));
    });
    expect(getStatus).toHaveBeenCalledWith(ref, operationId);
    expect(container.textContent).toContain('Lab teardown in progress');
    expect(sessionStorage.getItem('interactive-labs-teardown')).toContain(operationId);
    await act(async () => { inventory({ apiVersion: 'v1alpha1', kind: 'SetupList', items: [item] }); });
    expect(button('Tear down').disabled).toBe(true);
    expect(sessionStorage.getItem('interactive-labs-teardown')).toContain(operationId);
    expect(teardown).toHaveBeenCalledTimes(1);
    await act(async () => { terminal({ ...progress, status: 'failed' }); });
    expect(container.textContent).toContain('Lab cleanup failed.');
    expect(sessionStorage.getItem('interactive-labs-teardown')).toBeNull();
    const expectedPolls = response === 'accepted' ? 1 : 2;
    expect(getStatus).toHaveBeenCalledTimes(expectedPolls);
    await act(async () => { button('Refresh setups').click(); });
    expect(getStatus).toHaveBeenCalledTimes(expectedPolls);
    expect(container.textContent).not.toContain('Lab teardown in progress');
    act(() => { ReactDOM.unmountComponentAtNode(container); });
    await render();
    expect(getStatus).toHaveBeenCalledTimes(expectedPolls);
  });

  test('late receipt reaches remounted UI when optional browser storage is unavailable', async () => {
    const read = jest.spyOn(Storage.prototype, 'getItem').mockImplementation(() => { throw new Error('storage denied'); });
    const save = jest.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new Error('storage denied'); });
    try {
      let post!: (value: Awaited<ReturnType<typeof teardownLab>>) => void;
      let inventory!: (value: Awaited<ReturnType<typeof listSetups>>) => void;
      teardown.mockReturnValue(new Promise((resolve) => { post = resolve; }));
      getStatus.mockImplementation(() => new Promise(() => {}));
      await render(); confirm.mockReturnValue(true);
      await act(async () => { button('Tear down').click(); });
      act(() => { ReactDOM.unmountComponentAtNode(container); });
      list.mockReturnValueOnce(new Promise((resolve) => { inventory = resolve; }));
      await render();
      await act(async () => { post(progress); });
      expect(getStatus).toHaveBeenCalledWith(ref, operationId);
      expect(container.textContent).toContain('Lab teardown in progress');
      await act(async () => { inventory({ apiVersion: 'v1alpha1', kind: 'SetupList', items: [item] }); });
      expect(button('Tear down').disabled).toBe(true);
      expect(teardown).toHaveBeenCalledTimes(1);
    } finally { read.mockRestore(); save.mockRestore(); }
  });

  test('late receipt merges with an unrelated live operation and terminal removal preserves its sibling', async () => {
    const otherRef = 'ef'.repeat(32);
    const otherId = '34'.repeat(16);
    const other = { ...item, catalogRef: otherRef, release: { name: 'il-other' } };
    let oldPost!: (value: Awaited<ReturnType<typeof teardownLab>>) => void;
    let otherTerminal!: (value: Awaited<ReturnType<typeof getTeardownStatus>>) => void;
    list.mockResolvedValue({ apiVersion: 'v1alpha1', kind: 'SetupList', items: [item, other] });
    teardown.mockReturnValueOnce(new Promise((resolve) => { oldPost = resolve; }))
      .mockResolvedValue({ ...progress, catalogRef: otherRef, operationId: otherId });
    getStatus.mockImplementation((catalogRef) => catalogRef === otherRef
      ? new Promise((resolve) => { otherTerminal = resolve; }) : new Promise(() => {}));
    await render(); confirm.mockReturnValue(true);
    await act(async () => { container.querySelector('li button')!.dispatchEvent(new MouseEvent('click', { bubbles: true })); });
    act(() => { ReactDOM.unmountComponentAtNode(container); });
    await render();
    const rows = Array.from(container.querySelectorAll('li'));
    await act(async () => { rows[1].querySelector('button')!.click(); });
    await act(async () => { oldPost(progress); });
    expect(JSON.parse(sessionStorage.getItem('interactive-labs-teardown')!)).toEqual(expect.arrayContaining([
      { catalogRef: ref, operationId }, { catalogRef: otherRef, operationId: otherId },
    ]));
    expect(getStatus.mock.calls.filter(([catalogRef]) => catalogRef === otherRef)).toEqual([[otherRef, otherId]]);
    expect(rows[0].textContent).toContain('Lab teardown in progress');
    expect(rows[1].textContent).toContain('Lab teardown in progress');
    await act(async () => { otherTerminal({ ...progress, catalogRef: otherRef, operationId: otherId, status: 'failed' }); });
    expect(JSON.parse(sessionStorage.getItem('interactive-labs-teardown')!)).toEqual([{ catalogRef: ref, operationId }]);
    expect(rows[0].textContent).toContain('Lab teardown in progress');
    expect(rows[1].textContent).toContain('Lab cleanup failed.');
    expect(getStatus.mock.calls.filter(([catalogRef]) => catalogRef === ref)).toEqual([[ref, operationId]]);
    expect(teardown).toHaveBeenCalledTimes(2);
  });

  test.each([false, true])('old POST response cannot replace a newer same-ref receipt, completed=%s', async (completed) => {
    let oldPost!: (value: Awaited<ReturnType<typeof teardownLab>>) => void;
    let newTerminal!: (value: Awaited<ReturnType<typeof getTeardownStatus>>) => void;
    const newerId = '34'.repeat(16);
    const otherRef = 'ef'.repeat(32);
    const otherId = '56'.repeat(16);
    teardown.mockReturnValueOnce(new Promise((resolve) => { oldPost = resolve; }))
      .mockResolvedValue({ ...progress, operationId: newerId });
    getStatus.mockImplementation((catalogRef) => catalogRef === ref
      ? new Promise((resolve) => { newTerminal = resolve; })
      : new Promise(() => {}));
    await render(); confirm.mockReturnValue(true);
    await act(async () => { button('Tear down').click(); });
    act(() => { ReactDOM.unmountComponentAtNode(container); });
    sessionStorage.setItem('interactive-labs-teardown', JSON.stringify([{ catalogRef: otherRef, operationId: otherId }]));
    await render();
    await act(async () => { button('Tear down').click(); });
    expect(getStatus).toHaveBeenCalledWith(ref, newerId);
    if (completed) await act(async () => { newTerminal({ ...progress, operationId: newerId, status: 'failed' }); });
    await act(async () => { oldPost(progress); });
    const receipts = JSON.parse(sessionStorage.getItem('interactive-labs-teardown')!);
    expect(receipts).toContainEqual({ catalogRef: otherRef, operationId: otherId });
    if (completed) {
      expect(receipts).toEqual([{ catalogRef: otherRef, operationId: otherId }]);
      expect(container.textContent).toContain('Lab cleanup failed.');
    } else {
      expect(receipts).toContainEqual({ catalogRef: ref, operationId: newerId });
      expect(container.textContent).toContain('Lab teardown in progress');
    }
    expect(getStatus).not.toHaveBeenCalledWith(ref, operationId);
    expect(teardown).toHaveBeenCalledTimes(2);
    await act(async () => { button('Refresh setups').click(); });
    expect(sessionStorage.getItem('interactive-labs-teardown')).not.toContain(operationId);
  });

  test('remount adoption of a newer stored receipt fences an old pending POST even after terminal removal', async () => {
    let oldPost!: (value: Awaited<ReturnType<typeof teardownLab>>) => void;
    const newerId = '34'.repeat(16);
    teardown.mockReturnValue(new Promise((resolve) => { oldPost = resolve; }));
    await render(); confirm.mockReturnValue(true);
    await act(async () => { button('Tear down').click(); });
    act(() => { ReactDOM.unmountComponentAtNode(container); });
    sessionStorage.setItem('interactive-labs-teardown', JSON.stringify([{ catalogRef: ref, operationId: newerId }]));
    getStatus.mockResolvedValue({ ...progress, operationId: newerId, status: 'failed' });
    await render();
    expect(container.textContent).toContain('Lab cleanup failed.');
    expect(sessionStorage.getItem('interactive-labs-teardown')).toBeNull();
    await act(async () => { oldPost(progress); });
    expect(sessionStorage.getItem('interactive-labs-teardown')).toBeNull();
    expect(getStatus).toHaveBeenCalledTimes(1);
    expect(teardown).toHaveBeenCalledTimes(1);
    expect(container.textContent).not.toContain('Lab teardown in progress');
  });

  test('reload resumes polling without repeating teardown POST', async () => {
    window.sessionStorage.setItem('interactive-labs-teardown', JSON.stringify({ catalogRef: ref, operationId }));
    await render();
    expect(teardown).not.toHaveBeenCalled();
    expect(getStatus).toHaveBeenCalledWith(ref, operationId);
    expect(window.sessionStorage.getItem('interactive-labs-teardown')).toBeNull();
    expect(container.querySelector('li')?.textContent).toContain('Waiting for previous lab resources to disappear.');
    expect(container.querySelector('li .pf-v6-c-alert')).toBeNull();
  });
  test('worker failure reports failure and refreshes inventory', async () => {
    await render(); confirm.mockReturnValue(true);
    getStatus.mockResolvedValue({ ...progress, status: 'failed' });
    await act(async () => { button('Tear down').click(); });
    expect(container.textContent).toContain('Lab cleanup failed.');
    expect(window.sessionStorage.getItem('interactive-labs-teardown')).toBeNull();
    expect(list).toHaveBeenCalledTimes(2);
  });
  test('lost POST response discovers active teardown instead of retrying it', async () => {
    await render(); confirm.mockReturnValue(true);
    teardown.mockRejectedValue(new Error('network disconnected'));
    getStatus.mockResolvedValue(progress);
    await act(async () => { button('Tear down').click(); });
    expect(teardown).toHaveBeenCalledTimes(1);
    expect(getStatus).toHaveBeenCalledWith(ref);
    expect(button('Tear down').disabled).toBe(true);
    expect(window.sessionStorage.getItem('interactive-labs-teardown')).toContain(operationId);
  });
  test('stale receipt stops polling without claiming success or repeating POST', async () => {
    jest.useFakeTimers();
    try {
      window.sessionStorage.setItem('interactive-labs-teardown', JSON.stringify({ catalogRef: ref, operationId }));
      getStatus.mockRejectedValue({ status: 404 });
      await render();
      expect(container.textContent).toContain('Teardown status is no longer available');
      expect(container.textContent).not.toMatch(/administrator|clear.*lease/i);
      expect(container.textContent).not.toContain('Lab cleanup succeeded.');
      expect(container.textContent).not.toContain('Lab teardown in progress');
      expect(window.sessionStorage.getItem('interactive-labs-teardown')).toBeNull();
      await act(async () => { jest.advanceTimersByTime(9000); });
      expect(getStatus).toHaveBeenCalledTimes(1);
      expect(teardown).not.toHaveBeenCalled();
    } finally { jest.useRealTimers(); }
  });
  test('uncertain recovery never reports teardown success', async () => {
    await render(); confirm.mockReturnValue(true);
    getStatus.mockResolvedValue({ ...progress, status: 'unknown' });
    await act(async () => { button('Tear down').click(); });
    expect(container.textContent).toContain('outcome could not be verified');
    expect(container.textContent).not.toContain('Lab cleanup succeeded.');
    expect(window.sessionStorage.getItem('interactive-labs-teardown')).toBeNull();
  });
  test('unknown stale teardown receipt stops its spinner and permits Refresh setups then explicit Tear down retry', async () => {
    jest.useFakeTimers();
    try {
      window.sessionStorage.setItem('interactive-labs-teardown', JSON.stringify({ catalogRef: ref, operationId }));
      getStatus.mockResolvedValueOnce(progress).mockResolvedValue({ ...progress, status: 'unknown' });
      await render();
      expect(button('Tear down').disabled).toBe(true);
      expect(container.querySelector('[aria-label="Tearing down lab"]')).not.toBeNull();
      expect(teardown).not.toHaveBeenCalled();
      await act(async () => { jest.advanceTimersByTime(3000); });
      expect(container.querySelector('[aria-label="Tearing down lab"]')).toBeNull();
      expect(container.querySelector('[role="progressbar"]')).toBeNull();
      expect(container.textContent).toContain('outcome could not be verified');
      expect(container.textContent).not.toMatch(/administrator|Lease reservation|Managed lab cleanup completed/i);
      expect(window.sessionStorage.getItem('interactive-labs-teardown')).toBeNull();
      expect(button('Refresh setups').disabled).toBe(false);
      expect(button('Tear down').disabled).toBe(false);
      await act(async () => { jest.advanceTimersByTime(3000); });
      expect(getStatus).toHaveBeenCalledTimes(2);
      expect(teardown).not.toHaveBeenCalled();
      const refreshed = { ...item, generation: 'ef'.repeat(32) };
      list.mockResolvedValue({ apiVersion: 'v1alpha1', kind: 'SetupList', items: [refreshed] });
      await act(async () => { button('Refresh setups').click(); });
      confirm.mockReturnValue(true);
      getStatus.mockResolvedValue({ ...progress, status: 'succeeded' });
      await act(async () => { button('Tear down').click(); });
      expect(confirm).toHaveBeenCalledTimes(1);
      expect(teardown).toHaveBeenCalledTimes(1);
      expect(teardown).toHaveBeenCalledWith(ref, refreshed.generation);
    } finally { jest.useRealTimers(); }
  });
  test('stale confirmation or failure refreshes state and requires new confirmation', async () => {
    await render(); confirm.mockReturnValue(true);
    teardown.mockRejectedValue({ message: 'Lab setup changed. Refresh and confirm again.' });
    await act(async () => { button('Tear down').click(); });
    expect(container.textContent).toContain('Lab setup changed.');
    expect(list).toHaveBeenCalledTimes(2);
    expect(teardown).toHaveBeenCalledTimes(1);
  });
  test('pending or retained release requires administrator recovery', async () => {
    list.mockResolvedValue({ apiVersion: 'v1alpha1', kind: 'SetupList', items: [{ ...item, blocked: true }] });
    await render();
    expect(button('Tear down').disabled).toBe(true);
    expect(container.textContent).toContain('administrator');
    expect(teardown).not.toHaveBeenCalled();
  });
  test.each([
    ['cleanup', 'Lab namespace cleanup is still in progress. Wait for previous lab resources to disappear before opening or launching; contact an administrator if cleanup stalls.'],
    ['operation', 'A lab operation is in progress or its reservation cannot be verified. Wait for completion; interrupted operations become recoverable after the reservation expires.'],
    ['helm-state', 'The Helm release is not deployed or failed. Contact an administrator to recover its state before teardown.'],
    ['helm-hook', 'The Helm release has hooks that are not verified Jobs in dedicated lab namespaces. Contact an administrator to inspect their resources before teardown.'],
    ['helm-retained', 'The Helm release contains retained resources that prevent safe teardown. Contact an administrator to inspect and remove the retained resources before teardown.'],
    ['helm-manifest', 'The Helm release or hook manifest cannot be checked safely. Contact an administrator to inspect it before teardown.'],
    ['pending', 'Setup is pending. Tear down is unavailable until setup finishes or recovery becomes available.'],
    ['unknown', 'The teardown blocker could not be determined. Contact an administrator to inspect this lab and recover it before teardown.'],
  ] as const)('shows fixed recovery instructions for %s', async (blockedReason, message) => {
    const blockedItem = { ...item, blocked: true, blockedReason };
    list.mockResolvedValue({ apiVersion: 'v1alpha1', kind: 'SetupList', items: [blockedItem] });
    await render();
    expect(button('Tear down').disabled).toBe(true);
    expect(container.querySelector('li')?.textContent).toContain(message);
    expect(container.querySelector('li')?.textContent).not.toContain('Waiting for previous lab resources to disappear.');
    expect(getStatus).not.toHaveBeenCalled();
    await act(async () => { button('Tear down').click(); });
    expect(confirm).not.toHaveBeenCalled();
    expect(teardown).not.toHaveBeenCalled();
  });
  test('operation reason takes precedence over pending setup status', async () => {
    const blockedItem = { ...item, status: 'pending' as const, blocked: true, blockedReason: 'operation' as const };
    list.mockResolvedValue({ apiVersion: 'v1alpha1', kind: 'SetupList', items: [blockedItem] });
    await render();
    expect(button('Tear down').disabled).toBe(true);
    expect(container.textContent).toContain('A lab operation is in progress or its reservation cannot be verified.');
    expect(container.textContent).not.toContain('Setup is pending.');
  });
  test('missing reason retains old administrator fallback', async () => {
    list.mockResolvedValue({ apiVersion: 'v1alpha1', kind: 'SetupList', items: [{ ...item, blocked: true }] });
    await render();
    expect(container.textContent).toContain('Contact an administrator to recover this lab before teardown.');
  });
  test('pending status remains safe even without blocked flag or reason', async () => {
    list.mockResolvedValue({ apiVersion: 'v1alpha1', kind: 'SetupList', items: [{ ...item, status: 'pending', blocked: false }] });
    await render();
    expect(button('Tear down').disabled).toBe(true);
    expect(container.textContent).toContain('Setup is pending. Tear down is unavailable until setup finishes or recovery becomes available.');
    await act(async () => { button('Tear down').click(); });
    expect(confirm).not.toHaveBeenCalled();
    expect(teardown).not.toHaveBeenCalled();
  });
  test('unblocked setup shows no blocker message even if reason is present', async () => {
    const unblockedItem = { ...item, blocked: false, blockedReason: 'operation' as const };
    list.mockResolvedValue({ apiVersion: 'v1alpha1', kind: 'SetupList', items: [unblockedItem] });
    await render();
    expect(button('Tear down').disabled).toBe(false);
    expect(container.textContent).not.toContain('administrator');
    expect(container.textContent).not.toContain('reservation');
  });
  test('different orphan teardowns start independently, retain both receipts and finish only their own row', async () => {
    jest.useFakeTimers();
    try {
      const otherRef = 'ef'.repeat(32);
      const otherId = '34'.repeat(16);
      const other = { ...item, catalogRef: otherRef, release: { name: 'il-other' } };
      list.mockResolvedValue({ apiVersion: 'v1alpha1', kind: 'SetupList', items: [item, other] });
      teardown.mockImplementation(async (catalogRef) => ({ ...progress, catalogRef, operationId: catalogRef === ref ? operationId : otherId }));
      getStatus.mockImplementation(async (catalogRef) => ({ ...progress, catalogRef, operationId: catalogRef === ref ? operationId : otherId }));
      await render(); confirm.mockReturnValue(true);
      const rows = Array.from(container.querySelectorAll('li'));
      const firstButton = rows[0].querySelector('button')!;
      const secondButton = rows[1].querySelector('button')!;
      await act(async () => { firstButton.click(); firstButton.click(); });
      expect(teardown).toHaveBeenCalledTimes(1);
      expect(secondButton.disabled).toBe(false);
      expect(button('Refresh setups').disabled).toBe(false);
      expect(rows[0].textContent).toContain('Lab teardown in progress');
      expect(rows[1].textContent).not.toContain('Lab teardown in progress');
      await act(async () => { secondButton.click(); });
      expect(teardown).toHaveBeenCalledTimes(2);
      expect(getStatus).toHaveBeenCalledWith(ref, operationId);
      expect(getStatus).toHaveBeenCalledWith(otherRef, otherId);
      expect(JSON.parse(sessionStorage.getItem('interactive-labs-teardown')!)).toEqual(expect.arrayContaining([
        { catalogRef: ref, operationId }, { catalogRef: otherRef, operationId: otherId },
      ]));
      getStatus.mockImplementation(async (catalogRef) => ({ ...progress, catalogRef, operationId: catalogRef === ref ? operationId : otherId, status: catalogRef === ref ? 'succeeded' : 'running' }));
      await act(async () => { jest.advanceTimersByTime(3000); });
      expect(rows[0].textContent).toContain('Waiting for previous lab resources to disappear.');
      expect(rows[0].textContent).not.toContain('Managed lab cleanup completed.');
      expect(rows[0].textContent).not.toContain('Lab teardown in progress');
      expect(rows[1].textContent).toContain('Lab teardown in progress');
      expect(rows[1].textContent).not.toContain('Managed lab cleanup completed.');
      expect(JSON.parse(sessionStorage.getItem('interactive-labs-teardown')!)).toEqual([{ catalogRef: otherRef, operationId: otherId }]);
      list.mockResolvedValue({ apiVersion: 'v1alpha1', kind: 'SetupList', items: [other] });
      await act(async () => { button('Refresh setups').click(); });
      expect(container.querySelectorAll('li')).toHaveLength(1);
      expect(container.textContent).not.toContain('Managed lab cleanup completed.');
    } finally { jest.useRealTimers(); }
  });
  test('reload resumes every valid receipt and strips unexpected stored fields', async () => {
    const otherRef = 'ef'.repeat(32);
    const otherId = '34'.repeat(16);
    sessionStorage.setItem('interactive-labs-teardown', JSON.stringify([
      { catalogRef: ref, operationId, token: 'must-not-be-retained' },
      { catalogRef: otherRef, operationId: otherId },
      { catalogRef: '../invalid', operationId },
      { catalogRef: 'aa'.repeat(32), operationId: 'invalid' },
    ]));
    getStatus.mockImplementation(() => new Promise(() => {}));
    await render();
    expect(getStatus.mock.calls).toEqual(expect.arrayContaining([[ref, operationId], [otherRef, otherId]]));
    expect(getStatus).toHaveBeenCalledTimes(2);
    expect(teardown).not.toHaveBeenCalled();
    expect(sessionStorage.getItem('interactive-labs-teardown')).not.toMatch(/must-not-be-retained|invalid/);
    act(() => { ReactDOM.unmountComponentAtNode(container); });
    getStatus.mockClear();
    await render();
    expect(getStatus).toHaveBeenCalledTimes(2);
    expect(teardown).not.toHaveBeenCalled();
  });
  test.each([
    ['failed', 'Lab cleanup failed.'],
    ['unknown', 'Teardown outcome could not be verified.'],
    ['missing', 'Teardown status is no longer available.'],
    ['network', 'Unable to check teardown status.'],
    ['rejected', 'Lab setup changed. Refresh and confirm again.'],
  ])('%s notice belongs only below selected orphan buttons', async (outcome, notice) => {
    const otherRef = 'ef'.repeat(32);
    const other = { ...item, catalogRef: otherRef, release: { name: 'il-other' } };
    list.mockResolvedValue({ apiVersion: 'v1alpha1', kind: 'SetupList', items: [item, other] });
    if (outcome === 'missing') getStatus.mockRejectedValue({ status: 404 });
    else if (outcome === 'network') getStatus.mockRejectedValue(new Error('offline'));
    else if (outcome === 'rejected') {
      teardown.mockRejectedValue(new Error(notice));
      getStatus.mockRejectedValue({ status: 404 });
    } else getStatus.mockResolvedValue({ ...progress, status: outcome as 'failed' | 'unknown' });
    await render(); confirm.mockReturnValue(true);
    const rows = Array.from(container.querySelectorAll('li'));
    await act(async () => { rows[0].querySelector('button')!.click(); });
    const rowNotice = Array.from(rows[0].querySelectorAll('[role="alert"], [aria-live="polite"], [aria-live="assertive"]')).find((announcement) => announcement.textContent?.includes(notice));
    expect(rowNotice?.textContent).toContain(notice);
    expect(rows[0].querySelector('button')!.compareDocumentPosition(rowNotice!) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(rows[1].textContent).not.toContain(notice);
    expect(rows[1].querySelector('button')!.disabled).toBe(false);
    expect(teardown).toHaveBeenCalledTimes(1);
    expect(rows[0].querySelector('.pf-v6-c-alert')).toBeNull();
  });
  test('lost POST discovery stays scoped when another lab starts while first POST is unresolved', async () => {
    const otherRef = 'ef'.repeat(32);
    const otherId = '34'.repeat(16);
    const other = { ...item, catalogRef: otherRef, release: { name: 'il-other' } };
    list.mockResolvedValue({ apiVersion: 'v1alpha1', kind: 'SetupList', items: [item, other] });
    let reject!: (error: Error) => void;
    teardown.mockImplementation((catalogRef) => catalogRef === ref
      ? new Promise((_resolve, rejectPromise) => { reject = rejectPromise; })
      : Promise.resolve({ ...progress, catalogRef, operationId: otherId }));
    getStatus.mockImplementation(async (catalogRef) => ({ ...progress, catalogRef, operationId: catalogRef === ref ? operationId : otherId }));
    await render(); confirm.mockReturnValue(true);
    const rows = Array.from(container.querySelectorAll('li'));
    await act(async () => { rows[0].querySelector('button')!.click(); });
    await act(async () => { rows[1].querySelector('button')!.click(); });
    await act(async () => { reject(new Error('lost response')); });
    expect(teardown).toHaveBeenCalledTimes(2);
    expect(getStatus).toHaveBeenCalledWith(ref);
    expect(getStatus).toHaveBeenCalledWith(ref, operationId);
    expect(getStatus).toHaveBeenCalledWith(otherRef, otherId);
    for (const row of rows) expect(row.textContent).toContain('Lab teardown in progress');
    expect(sessionStorage.getItem('interactive-labs-teardown')).toContain(operationId);
    expect(sessionStorage.getItem('interactive-labs-teardown')).toContain(otherId);
  });
  test('renders setup actions only in caller-provided rows, without duplicating orphan presentation', async () => {
    await act(async () => {
      ReactDOM.render(<LabSetups catalog={[]}>{({ existingCatalogRefs, teardown }) => existingCatalogRefs.map((catalogRef) =>
        <div key={catalogRef}>{teardown(catalogRef)}</div>,
      )}</LabSetups>, container);
    });
    expect(container.querySelectorAll('button[aria-label^="Tear down"]')).toHaveLength(1);
    expect(container.querySelectorAll('ul')).toHaveLength(0);
    expect(container.textContent).not.toContain('Your lab setups');
  });
  test('unmount never performs cleanup', async () => {
    await render(); act(() => { ReactDOM.unmountComponentAtNode(container); });
    expect(teardown).not.toHaveBeenCalled();
  });
});
