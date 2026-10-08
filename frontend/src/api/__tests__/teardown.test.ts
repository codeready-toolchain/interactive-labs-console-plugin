import { consoleFetch } from '@openshift-console/dynamic-plugin-sdk';
import { getTeardownStatus, listSetups, teardownLab } from '../client';

const mockFetch = consoleFetch as jest.MockedFunction<typeof consoleFetch>;
const base = '/api/proxy/plugin/interactive-labs-console-plugin/backend/api/v1alpha1/setups';
const labs = '/api/proxy/plugin/interactive-labs-console-plugin/backend/api/v1alpha1/labs';
const ref = 'ab'.repeat(32);
const item = { catalogRef: ref, generation: '12'.repeat(32), status: 'deployed', spaceRequests: ['gitea'], release: { name: 'il-lab', catalogRef: ref, status: 'deployed' } };
const response = (body: unknown, status = 200): Response => ({ ok: status < 300, status, json: async () => body } as Response);

beforeEach(() => mockFetch.mockReset());

describe('setup cleanup client', () => {
  test('lists current user setups through proxy without needing catalog', async () => {
    mockFetch.mockResolvedValue(response({ apiVersion: 'v1alpha1', kind: 'SetupList', items: [item] }));
    const result = await listSetups();
    expect(result.items).toEqual([{ ...item, release: { name: item.release.name } }]);
    expect(mockFetch.mock.calls[0][0]).toBe(base);
  });
  test('projects only release name after validating wire metadata', async () => {
    mockFetch.mockResolvedValue(response({ apiVersion: 'v1alpha1', kind: 'SetupList', items: [{ ...item, release: { ...item.release, cleanupBlocked: true, token: 'discard-me' } }] }));
    expect((await listSetups()).items[0].release).toEqual({ name: 'il-lab' });
  });
  test.each([
    { ...item.release, catalogRef: 'ef'.repeat(32) },
    { ...item.release, name: '' },
    { ...item.release, name: 42 },
    { ...item.release, status: undefined },
    { ...item.release, status: 42 },
    { ...item.release, cleanupBlocked: 'true' },
  ])('rejects invalid wire release before projection %p', async (release) => {
    mockFetch.mockResolvedValue(response({ apiVersion: 'v1alpha1', kind: 'SetupList', items: [{ ...item, release }] }));
    await expect(listSetups()).rejects.toThrow('Invalid lab release identity.');
  });
  test.each(['pending', 'deployed', 'failed'])('preserves required setup status %s', async (status) => {
    mockFetch.mockResolvedValue(response({ apiVersion: 'v1alpha1', kind: 'SetupList', items: [{ ...item, status }] }));
    expect((await listSetups()).items[0].status).toBe(status);
  });
  test.each(['cleanup', 'operation', 'helm-state', 'helm-hook', 'helm-retained', 'helm-manifest', 'pending', 'unknown'])('preserves teardown blocked reason %s', async (blockedReason) => {
    mockFetch.mockResolvedValue(response({ apiVersion: 'v1alpha1', kind: 'SetupList', items: [{ ...item, blocked: true, blockedReason }] }));
    expect((await listSetups()).items[0]).toMatchObject({ blocked: true, blockedReason });
  });
  test.each([null, '', 'backend-sensitive-detail', 42, {}, []])('rejects invalid teardown blocked reason %p', async (blockedReason) => {
    mockFetch.mockResolvedValue(response({ apiVersion: 'v1alpha1', kind: 'SetupList', items: [{ ...item, blocked: true, blockedReason }] }));
    await expect(listSetups()).rejects.toThrow('Invalid lab setup identity.');
  });
  test('keeps old backends without blocked reason compatible', async () => {
    mockFetch.mockResolvedValue(response({ apiVersion: 'v1alpha1', kind: 'SetupList', items: [{ ...item, blocked: true }] }));
    expect((await listSetups()).items[0]).toMatchObject({ blocked: true });
  });
  test.each([undefined, null, 'ready', '', 42])('rejects invalid or missing setup status %s', async (status) => {
    mockFetch.mockResolvedValue(response({ apiVersion: 'v1alpha1', kind: 'SetupList', items: [{ ...item, status }] }));
    await expect(listSetups()).rejects.toThrow('Invalid lab setup identity.');
  });
  test('starts teardown and polls by server operation identity without another POST', async () => {
    const op = '12'.repeat(16);
    mockFetch.mockResolvedValueOnce(response({ apiVersion: 'v1alpha1', kind: 'TeardownStatus', catalogRef: ref, operationId: op, status: 'running' }, 202));
    expect(await teardownLab(ref, item.generation)).toMatchObject({ operationId: op, status: 'running' });
    expect(mockFetch).toHaveBeenCalledWith(`${labs}/${ref}/teardown`, {
      method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ generation: item.generation }),
    });
    mockFetch.mockResolvedValueOnce(response({ apiVersion: 'v1alpha1', kind: 'TeardownStatus', catalogRef: ref, operationId: op, status: 'succeeded' }));
    expect((await getTeardownStatus(ref, op)).status).toBe('succeeded');
    expect(mockFetch).toHaveBeenLastCalledWith(`${labs}/${ref}/teardown/status?operationId=${op}`, { signal: undefined });
  });
  test.each(['bad/ref', 'not-a-digest'])('rejects unsafe teardown status identity %s', async (unsafe) => {
    await expect(getTeardownStatus(unsafe, '12'.repeat(16))).rejects.toThrow();
    await expect(getTeardownStatus(ref, unsafe)).rejects.toThrow();
    expect(mockFetch).not.toHaveBeenCalled();
  });
  test('recovers active teardown when POST response was lost', async () => {
    const operationId = '12'.repeat(16);
    mockFetch.mockResolvedValue(response({ apiVersion: 'v1alpha1', kind: 'TeardownStatus', catalogRef: ref, operationId, status: 'running' }));
    expect((await getTeardownStatus(ref)).operationId).toBe(operationId);
    expect(mockFetch).toHaveBeenCalledWith(`${labs}/${ref}/teardown/status`, { signal: undefined });
  });
  test('distinguishes a superseded or inaccessible teardown receipt', async () => {
    mockFetch.mockResolvedValue(response({ apiVersion: 'v1alpha1', kind: 'Error', type: 'configuration_error', message: 'not found' }, 404));
    await expect(getTeardownStatus(ref, '12'.repeat(16))).rejects.toMatchObject({ status: 404 });
  });
  test('recognizes a 404 rejected by Console fetch so teardown polling can stop', async () => {
    mockFetch.mockRejectedValue(Object.assign(new Error('Not Found'), { response: { status: 404 } }));
    await expect(getTeardownStatus(ref, '12'.repeat(16))).rejects.toMatchObject({ status: 404 });
  });
  test('preserves uncertain teardown outcome without claiming success', async () => {
    mockFetch.mockResolvedValue(response({ apiVersion: 'v1alpha1', kind: 'TeardownStatus', catalogRef: ref, operationId: '12'.repeat(16), status: 'unknown' }));
    expect((await getTeardownStatus(ref, '12'.repeat(16))).status).toBe('unknown');
  });
  test.each(['missing', 'pending', 'complete', null])('rejects invalid teardown progress %p', async (status) => {
    mockFetch.mockResolvedValue(response({ apiVersion: 'v1alpha1', kind: 'TeardownStatus', catalogRef: ref, operationId: '12'.repeat(16), status }));
    await expect(getTeardownStatus(ref, '12'.repeat(16))).rejects.toThrow('Invalid teardown status.');
  });
  test.each(['bad/ref', 'not-a-digest'])('rejects unsafe identity %s before fetching', async (unsafe) => {
    await expect(teardownLab(unsafe, item.generation)).rejects.toThrow();
    expect(mockFetch).not.toHaveBeenCalled();
  });
  test('validates inventory and maps failure response', async () => {
    mockFetch.mockResolvedValueOnce(response({ apiVersion: 'v1alpha1', kind: 'SetupList', items: [{ ...item, catalogRef: '../../evil' }] }));
    await expect(listSetups()).rejects.toThrow();
    mockFetch.mockResolvedValueOnce(response({ apiVersion: 'v1alpha1', kind: 'Error', type: 'configuration_error', message: 'Refresh and confirm again.' }, 409));
    await expect(teardownLab(ref, item.generation)).rejects.toMatchObject({ message: 'Refresh and confirm again.' });
  });
});
