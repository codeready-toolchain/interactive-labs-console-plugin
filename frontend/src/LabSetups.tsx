import * as React from 'react';
import { Alert, Button, Spinner } from '@patternfly/react-core';
import { getTeardownStatus, listSetups, teardownLab } from './api/client';
import type { CatalogEntry } from './types/catalog';
import type { BlockedReason, LabSetup } from './types/setup';

const blockedMessages: Record<BlockedReason, string> = {
  cleanup: 'Lab namespace cleanup is still in progress. Wait for previous lab resources to disappear before opening or launching; contact an administrator if cleanup stalls.',
  operation: 'A lab operation is in progress or its reservation cannot be verified. Wait for completion; interrupted operations become recoverable after the reservation expires.',
  'helm-state': 'The Helm release is not deployed or failed. Contact an administrator to recover its state before teardown.',
  'helm-hook': 'The Helm release has hooks that are not verified Jobs in dedicated lab namespaces. Contact an administrator to inspect their resources before teardown.',
  'helm-retained': 'The Helm release contains retained resources that prevent safe teardown. Contact an administrator to inspect and remove the retained resources before teardown.',
  'helm-manifest': 'The Helm release or hook manifest cannot be checked safely. Contact an administrator to inspect it before teardown.',
  pending: 'Setup is pending. Tear down is unavailable until setup finishes or recovery becomes available.',
  unknown: 'The teardown blocker could not be determined. Contact an administrator to inspect this lab and recover it before teardown.',
};

type SetupActions = {
  items: LabSetup[];
  existingCatalogRefs: string[];
  disabledFor: (catalogRef: string) => boolean;
  teardown: (catalogRef: string) => React.ReactNode;
};

type TeardownReceipt = { catalogRef: string; operationId: string };
type TeardownRow = { operationId?: string; generation?: string; busy?: boolean; awaitingInventory?: boolean; cleanupPending?: boolean; error?: string };
const teardownStorageKey = 'interactive-labs-teardown';
const teardownReceiptEvent = 'interactive-labs-teardown-receipt';
// Fence late POST responses against newer attempts, including after remount/completion.
const teardownPostOwners = new Map<string, symbol>();
const isTeardownReceipt = (value: unknown): value is TeardownReceipt => {
  const receipt = value as Partial<TeardownReceipt> | null;
  return !!receipt && typeof receipt.catalogRef === 'string' && /^[a-f0-9]{64}$/.test(receipt.catalogRef) &&
    typeof receipt.operationId === 'string' && /^[a-f0-9]{32}$/.test(receipt.operationId);
};
const storedTeardowns = (): TeardownReceipt[] => {
  try {
    const value = JSON.parse(window.sessionStorage.getItem(teardownStorageKey) || 'null');
    return (Array.isArray(value) ? value : [value]).filter(isTeardownReceipt).map(({ catalogRef, operationId }) => ({ catalogRef, operationId }));
  } catch { /* Browser storage is optional. */ }
  return [];
};
const saveTeardowns = (receipts: TeardownReceipt[]): void => {
  try {
    if (receipts.length) window.sessionStorage.setItem(teardownStorageKey, JSON.stringify(receipts));
    else window.sessionStorage.removeItem(teardownStorageKey);
  } catch { /* Browser storage is optional. */ }
};

// Each keyed poller keeps its timer/request when other labs start or finish.
const TeardownPoll = ({ catalogRef, operationId, updateRow, finish }: TeardownReceipt & {
  updateRow: (catalogRef: string, patch: TeardownRow) => void;
  finish: (catalogRef: string, error?: string, succeeded?: boolean, operationId?: string) => void;
}): null => {
  React.useEffect(() => {
    let stopped = false;
    let timer: number | undefined;
    const poll = async (): Promise<void> => {
      try {
        const progress = await getTeardownStatus(catalogRef, operationId);
        if (stopped) return;
        updateRow(catalogRef, { error: undefined });
        if (progress.status === 'running') {
          timer = window.setTimeout(() => void poll(), 3000);
          return;
        }
        finish(catalogRef, progress.status === 'succeeded' ? undefined : progress.status === 'unknown'
          ? 'Teardown outcome could not be verified. Refresh setups, then confirm cleanup again if resources remain.'
          : 'Lab cleanup failed. Refresh lab setups before retrying or contact an administrator.', progress.status === 'succeeded', operationId);
      } catch (error: unknown) {
        if (stopped) return;
        if ((error as { status?: number })?.status === 404) {
          finish(catalogRef, 'Teardown status is no longer available. Refresh setups and confirm cleanup again if resources remain.', false, operationId);
          return;
        }
        updateRow(catalogRef, { error: 'Unable to check teardown status. Teardown may still be running; retry status after connectivity returns.' });
        timer = window.setTimeout(() => void poll(), 3000);
      }
    };
    void poll();
    return () => { stopped = true; window.clearTimeout(timer); };
  }, [catalogRef, operationId, updateRow, finish]);
  return null;
};

export const LabSetups = ({ catalog, refreshVersion = 0, children }: {
  catalog?: CatalogEntry[];
  refreshVersion?: number;
  children?: (actions: SetupActions) => React.ReactNode;
}): React.ReactElement => {
  const [items, setItems] = React.useState<LabSetup[]>([]);
  const [loading, setLoading] = React.useState(true);
  const [rows, setRows] = React.useState<Record<string, TeardownRow>>(() => Object.fromEntries(
    storedTeardowns().map(({ catalogRef, operationId }) => [catalogRef, { operationId, busy: true }]),
  ));
  const [inventoryError, setInventoryError] = React.useState<string>();
  const mounted = React.useRef(true);
  const inFlight = React.useRef(new Map(Object.entries(rows).map(([catalogRef, row]) => [catalogRef, row.operationId])));
  const refreshRequest = React.useRef(0);
  const inventoryRequest = React.useRef<Promise<void>>();
  const updateRow = React.useCallback((catalogRef: string, patch: TeardownRow): void => {
    if (mounted.current) setRows((current) => ({ ...current, [catalogRef]: { ...current[catalogRef], ...patch } }));
  }, []);
  React.useEffect(() => {
    const adopt = ({ catalogRef, operationId }: TeardownReceipt): void => {
      teardownPostOwners.delete(catalogRef);
      const pendingPost = inFlight.current.has(catalogRef) && inFlight.current.get(catalogRef) === undefined;
      inFlight.current.set(catalogRef, operationId);
      setRows((current) => current[catalogRef]?.operationId === operationId ? current : {
        ...current, [catalogRef]: { operationId, generation: pendingPost ? current[catalogRef]?.generation : undefined, busy: true },
      });
    };
    const receive = (event: Event): void => {
      const receipt: unknown = (event as CustomEvent<unknown>).detail;
      if (isTeardownReceipt(receipt)) adopt(receipt);
    };
    window.addEventListener(teardownReceiptEvent, receive);
    const receipts = storedTeardowns();
    saveTeardowns(receipts); // Normalize storage without persisting a stale row snapshot.
    receipts.forEach(adopt);
    return () => window.removeEventListener(teardownReceiptEvent, receive);
  }, []);

  const refresh = React.useCallback((background = false): Promise<void> => {
    if (!mounted.current) return Promise.resolve();
    // Manual refresh shares an active GET rather than duplicating its request.
    if (inventoryRequest.current) return inventoryRequest.current;
    const request = ++refreshRequest.current;
    const isCurrent = (): boolean => mounted.current && request === refreshRequest.current;
    if (!background) setLoading(true);
    inventoryRequest.current = (async () => {
      try {
        const result = await listSetups();
        if (isCurrent()) {
          setItems(result.items);
          setInventoryError(undefined);
          setRows((current) => Object.fromEntries(Object.entries(current).map(([catalogRef, row]) => {
            const item = result.items.find((candidate) => candidate.catalogRef === catalogRef);
            // A different settled release is a new setup, not old cleanup remnants.
            const recreated = item?.release && item.status !== 'pending' && row.generation && item.generation !== row.generation &&
              !(item.blocked && (item.blockedReason === 'cleanup' || item.blockedReason === 'operation'));
            return [catalogRef, { ...row, generation: row.generation ?? item?.generation, awaitingInventory: false, cleanupPending: row.cleanupPending && !!item && !recreated }];
          })));
        }
      } catch (error: unknown) {
        // Keep usable inventory during transient background failures; polling retries.
        if (isCurrent() && !background) setInventoryError((error as { message?: string })?.message ?? 'Unable to list lab setups. Retry or contact an administrator.');
      } finally {
        inventoryRequest.current = undefined;
        if (isCurrent()) setLoading(false);
      }
    })();
    return inventoryRequest.current;
  }, []);
  React.useEffect(() => {
    mounted.current = true;
    return () => { mounted.current = false; ++refreshRequest.current; };
  }, []);
  React.useEffect(() => {
    let stopped = false;
    ++refreshRequest.current;
    if (inventoryRequest.current) void inventoryRequest.current.then(() => { if (!stopped) void refresh(); });
    else void refresh();
    return () => { stopped = true; ++refreshRequest.current; };
  }, [refresh, refreshVersion]);

  const needsRefresh = items.some((item) => item.status === 'pending' ||
    (item.blocked && (item.blockedReason === 'cleanup' || item.blockedReason === 'operation'))) || Object.values(rows).some((row) => row.awaitingInventory || row.cleanupPending);
  React.useEffect(() => {
    if (!needsRefresh) return;
    let stopped = false;
    let timer: number;
    const poll = async (): Promise<void> => {
      await refresh(true);
      if (!stopped) timer = window.setTimeout(() => void poll(), 10000);
    };
    timer = window.setTimeout(() => void poll(), 10000);
    return () => { stopped = true; window.clearTimeout(timer); };
  }, [needsRefresh, refresh]);

  const finish = React.useCallback((catalogRef: string, error?: string, succeeded = false, operationId?: string): void => {
    if (operationId && inFlight.current.get(catalogRef) !== operationId) return;
    saveTeardowns(storedTeardowns().filter((receipt) => receipt.catalogRef !== catalogRef || receipt.operationId !== operationId));
    inFlight.current.delete(catalogRef);
    updateRow(catalogRef, { operationId: undefined, busy: false, awaitingInventory: true, cleanupPending: succeeded, error });
    ++refreshRequest.current;
    if (inventoryRequest.current) void inventoryRequest.current.then(() => void refresh(true));
    else void refresh(true);
  }, [refresh, updateRow]);

  const disabled = loading || !!inventoryError;
  const disabledFor = (catalogRef: string): boolean => {
    const item = items.find((candidate) => candidate.catalogRef === catalogRef);
    return disabled || !!rows[catalogRef]?.busy || !!rows[catalogRef]?.awaitingInventory || !!rows[catalogRef]?.cleanupPending ||
      item?.status === 'failed' || (!!item?.blocked && (item.blockedReason === 'cleanup' || (item.blockedReason === 'operation' && item.status === 'deployed'))) ||
      (!!item && item.status !== 'pending' && !item.release && item.spaceRequests.length > 0);
  };
  const existingCatalogRefs = Array.from(new Set([
    ...items.map((item) => item.catalogRef),
    ...Object.keys(rows).filter((catalogRef) => rows[catalogRef].busy || rows[catalogRef].error || rows[catalogRef].awaitingInventory || rows[catalogRef].cleanupPending),
  ]));
  const title = (catalogRef: string): string => catalog?.find((entry) => entry.catalogRef === catalogRef)?.title ?? items.find((item) => item.catalogRef === catalogRef)?.release?.name ?? catalogRef;
  const teardownDisabled = (item: LabSetup): boolean => disabled || inFlight.current.has(item.catalogRef) || !!rows[item.catalogRef]?.busy ||
    !!rows[item.catalogRef]?.awaitingInventory || !!rows[item.catalogRef]?.cleanupPending || !!item.blocked || item.status === 'pending';
  const cleanup = async (catalogRef: string): Promise<void> => {
    const item = items.find((candidate) => candidate.catalogRef === catalogRef);
    if (!item || teardownDisabled(item)) return;
    const targets = [item.release ? `Helm release: ${item.release.name}` : '', `SpaceRequests: ${item.spaceRequests.join(', ') || 'none'}`].filter(Boolean).join('\n');
    if (!window.confirm(`Tear down ${title(item.catalogRef)}?\n${targets}\nThis deletes the lab resources and cannot be undone.`)) return;
    const owner = Symbol();
    teardownPostOwners.set(item.catalogRef, owner);
    inFlight.current.set(item.catalogRef, undefined);
    updateRow(item.catalogRef, { generation: item.generation, busy: true, error: undefined });
    const remember = (catalogRef: string, operationId: string): void => {
      if (teardownPostOwners.get(catalogRef) !== owner) return;
      teardownPostOwners.delete(catalogRef);
      const receipt = { catalogRef, operationId };
      saveTeardowns([...storedTeardowns().filter((saved) => saved.catalogRef !== catalogRef), receipt]);
      window.dispatchEvent(new CustomEvent(teardownReceiptEvent, { detail: receipt }));
    };
    try {
      const started = await teardownLab(item.catalogRef, item.generation);
      remember(started.catalogRef, started.operationId);
    } catch (error: unknown) {
      if (teardownPostOwners.get(item.catalogRef) !== owner) return;
      try {
        const active = await getTeardownStatus(item.catalogRef);
        if (active.status === 'running') {
          remember(active.catalogRef, active.operationId);
          return;
        }
      } catch { /* No known active teardown; retain original error. */ }
      if (teardownPostOwners.get(item.catalogRef) === owner) {
        teardownPostOwners.delete(item.catalogRef);
        if (mounted.current) finish(item.catalogRef, (error as { message?: string })?.message ?? 'Teardown may already be running. Refresh lab setups before retrying.');
      }
    }
  };

  const teardown = (catalogRef: string): React.ReactNode => {
    const item = items.find((candidate) => candidate.catalogRef === catalogRef);
    return <>
      {item && <>
        <Button variant="danger" isLoading={!!rows[catalogRef]?.busy} isDisabled={teardownDisabled(item)} aria-label={`Tear down ${title(catalogRef)}`} onClick={() => void cleanup(catalogRef)}>Tear down</Button>
        {(item.blocked || item.status === 'pending') && <p>{item.blocked && item.blockedReason
          ? blockedMessages[item.blockedReason]
          : item.status === 'pending' ? blockedMessages.pending : 'Contact an administrator to recover this lab before teardown.'}</p>}
      </>}
      {rows[catalogRef]?.busy && <div role="status">Lab teardown in progress. Waiting for completion or recovery.</div>}
      {rows[catalogRef]?.error && <p role="alert">{rows[catalogRef].error}</p>}
      {rows[catalogRef]?.awaitingInventory && !rows[catalogRef]?.cleanupPending && <p aria-live="polite">Waiting for refreshed lab setups before opening or confirming teardown again. Checking setups automatically every 10 seconds.</p>}
      {rows[catalogRef]?.cleanupPending && !(item?.blocked && item.blockedReason === 'cleanup') && <p aria-live="polite">Waiting for previous lab resources to disappear. Checking setups automatically every 10 seconds.</p>}
    </>;
  };

  return (
    <>
      {Object.entries(rows).map(([catalogRef, row]) => row.operationId && <TeardownPoll key={catalogRef} catalogRef={catalogRef} operationId={row.operationId} updateRow={updateRow} finish={finish} />)}
      <div style={{ marginBlockStart: 'var(--pf-t--global--spacer--lg)' }}>
        <Button variant="link" isDisabled={loading} onClick={() => void refresh()}>Refresh setups</Button>
        {loading && <Spinner aria-label="Loading lab setups" />}
        {inventoryError && <Alert variant="danger" title={inventoryError} isInline isLiveRegion />}
      </div>
      {children?.({ items, existingCatalogRefs, disabledFor, teardown })}
    </>
  );
};
