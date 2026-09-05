import type { Column, Priority, Project, Task } from './types';

/** A deliberately small, read-only representation of one cached board. */
export interface OfflineBoard {
  project: {
    id: string;
    slug: string;
    name: string;
    key: string;
  };
  columns: Array<{
    id: string;
    name: string;
    position: number;
  }>;
  tasks: Array<{
    id: string;
    key: string;
    title: string;
    description: string;
    column_id: string;
    priority: Priority;
  }>;
  savedAt: number;
  partial: boolean;
}

type StoredState = {
  owner: string | null;
  boards: OfflineBoard[];
};

const DB_NAME = 'helm-offline-boards';
const DB_VERSION = 1;
const STORE_NAME = 'snapshots';
const RECORD_KEY = 'current';
const BROADCAST_CHANNEL_NAME = 'helm-offline-boards';
const INVALIDATION_STORAGE_KEY = 'helm:offline-cleared';
const CLEAR_TOMBSTONE_STORAGE_KEY = 'helm:offline-clear-marker';

const MAX_BOARDS = 5;
const MAX_COLUMNS_PER_BOARD = 100;
const MAX_TASKS_PER_BOARD = 500;
const TTL_MS = 7 * 24 * 60 * 60 * 1000;

// Keep individual strings bounded even when a server response is malformed.
// These are intentionally conservative: a complete board remains small while
// retaining enough room for normal task descriptions.
const MAX_ID_LENGTH = 200;
const MAX_KEY_LENGTH = 200;
const MAX_PROJECT_KEY_LENGTH = 16;
const MAX_SLUG_LENGTH = 64;
const MAX_NAME_LENGTH = 200;
const MAX_COLUMN_NAME_LENGTH = 100;
const MAX_TITLE_LENGTH = 500;
const MAX_DESCRIPTION_LENGTH = 10_000;

const PRIORITIES: readonly Priority[] = ['low', 'normal', 'high', 'urgent'];

let databasePromise: Promise<IDBDatabase | null> | null = null;
let activeOwner: string | null = null;
let ownerGeneration = 0;

// A clear can race a login for the same actor. The next setOwner call after a
// clear must not preserve a record that was queued before that clear.
let clearGeneration = 0;
let appliedClearGeneration = 0;

let invalidationSetup = false;
let broadcastChannel: BroadcastChannel | null = null;

function boundedText(value: unknown, limit: number): string {
  if (typeof value !== 'string') return '';
  if (value.length <= limit) return value;
  // Keep truncation bounded even for a hostile multi-megabyte string. Avoid a
  // lone high surrogate at the boundary without allocating an intermediate
  // array for every code point.
  const truncated = value.slice(0, limit);
  const last = truncated.charCodeAt(truncated.length - 1);
  return last >= 0xd800 && last <= 0xdbff ? truncated.slice(0, -1) : truncated;
}

function boundedRequiredText(value: unknown, limit: number): string | null {
  const text = boundedText(value, limit);
  return text.length > 0 ? text : null;
}

function finiteNumber(value: unknown, fallback = 0): number {
  return typeof value === 'number' && Number.isFinite(value) ? value : fallback;
}

function priority(value: unknown): Priority {
  return PRIORITIES.includes(value as Priority) ? value as Priority : 'normal';
}

function ownerValue(value: unknown): string | null {
  if (typeof value !== 'string') return null;
  const owner = value.trim();
  if (!owner || owner.length > MAX_ID_LENGTH) return null;
  return owner;
}

function sourceProject(project: Project): OfflineBoard['project'] | null {
  if (!project || typeof project !== 'object') return null;
  const value = project as unknown as Record<string, unknown>;
  const id = boundedRequiredText(value.id, MAX_ID_LENGTH);
  if (!id) return null;
  return {
    id,
    slug: boundedText(value.slug, MAX_SLUG_LENGTH),
    name: boundedText(value.name, MAX_NAME_LENGTH),
    key: boundedText(value.key, MAX_PROJECT_KEY_LENGTH)
  };
}

function sourceColumns(columns: Column[]): OfflineBoard['columns'] {
  if (!Array.isArray(columns)) return [];
  return columns.slice(0, MAX_COLUMNS_PER_BOARD).reduce<OfflineBoard['columns']>((result, column) => {
    if (!column || typeof column !== 'object') return result;
    const value = column as unknown as Record<string, unknown>;
    const id = boundedRequiredText(value.id, MAX_ID_LENGTH);
    if (!id) return result;
    result.push({
      id,
      name: boundedText(value.name, MAX_COLUMN_NAME_LENGTH),
      position: finiteNumber(value.position)
    });
    return result;
  }, []);
}

function sourceTasks(tasks: Task[]): OfflineBoard['tasks'] {
  if (!Array.isArray(tasks)) return [];
  return tasks.slice(0, MAX_TASKS_PER_BOARD).reduce<OfflineBoard['tasks']>((result, task) => {
    if (!task || typeof task !== 'object') return result;
    const value = task as unknown as Record<string, unknown>;
    const id = boundedRequiredText(value.id, MAX_ID_LENGTH);
    const columnId = boundedRequiredText(value.column_id, MAX_ID_LENGTH);
    if (!id || !columnId) return result;
    result.push({
      id,
      key: boundedText(value.key, MAX_KEY_LENGTH),
      title: boundedText(value.title, MAX_TITLE_LENGTH),
      description: boundedText(value.description, MAX_DESCRIPTION_LENGTH),
      column_id: columnId,
      priority: priority(value.priority)
    });
    return result;
  }, []);
}

function storedProject(value: unknown): OfflineBoard['project'] | null {
  if (!value || typeof value !== 'object') return null;
  const project = value as Record<string, unknown>;
  const id = boundedRequiredText(project.id, MAX_ID_LENGTH);
  if (!id) return null;
  return {
    id,
    slug: boundedText(project.slug, MAX_SLUG_LENGTH),
    name: boundedText(project.name, MAX_NAME_LENGTH),
    key: boundedText(project.key, MAX_PROJECT_KEY_LENGTH)
  };
}

function storedColumns(value: unknown): OfflineBoard['columns'] {
  if (!Array.isArray(value)) return [];
  return value.slice(0, MAX_COLUMNS_PER_BOARD).reduce<OfflineBoard['columns']>((result, column) => {
    if (!column || typeof column !== 'object') return result;
    const source = column as Record<string, unknown>;
    const id = boundedRequiredText(source.id, MAX_ID_LENGTH);
    if (!id) return result;
    result.push({
      id,
      name: boundedText(source.name, MAX_COLUMN_NAME_LENGTH),
      position: finiteNumber(source.position)
    });
    return result;
  }, []);
}

function storedTasks(value: unknown): OfflineBoard['tasks'] {
  if (!Array.isArray(value)) return [];
  return value.slice(0, MAX_TASKS_PER_BOARD).reduce<OfflineBoard['tasks']>((result, task) => {
    if (!task || typeof task !== 'object') return result;
    const source = task as Record<string, unknown>;
    const id = boundedRequiredText(source.id, MAX_ID_LENGTH);
    const columnId = boundedRequiredText(source.column_id, MAX_ID_LENGTH);
    if (!id || !columnId) return result;
    result.push({
      id,
      key: boundedText(source.key, MAX_KEY_LENGTH),
      title: boundedText(source.title, MAX_TITLE_LENGTH),
      description: boundedText(source.description, MAX_DESCRIPTION_LENGTH),
      column_id: columnId,
      priority: priority(source.priority)
    });
    return result;
  }, []);
}

function fresh(savedAt: unknown, now: number): savedAt is number {
  return typeof savedAt === 'number'
    && Number.isFinite(savedAt)
    && Number.isFinite(now)
    && savedAt <= now
    && now - savedAt < TTL_MS;
}

function storedBoard(value: unknown, now: number): OfflineBoard | null {
  if (!value || typeof value !== 'object') return null;
  const source = value as Record<string, unknown>;
  const project = storedProject(source.project);
  const savedAt = source.savedAt;
  if (!project || !fresh(savedAt, now)) return null;
  return {
    project,
    columns: storedColumns(source.columns),
    tasks: storedTasks(source.tasks),
    savedAt,
    partial: source.partial === true
  };
}

function normalizeState(value: unknown, now = Date.now()): StoredState {
  if (!value || typeof value !== 'object') return { owner: null, boards: [] };
  const source = value as Record<string, unknown>;
  const boards = Array.isArray(source.boards)
    ? source.boards
      .map((board) => storedBoard(board, now))
      .filter((board): board is OfflineBoard => board !== null)
      .sort((left, right) => right.savedAt - left.savedAt)
      .slice(0, MAX_BOARDS)
    : [];
  return {
    owner: ownerValue(source.owner),
    boards
  };
}

function sourceBoard(
  project: Project,
  columns: Column[],
  tasks: Task[],
  partial: boolean,
  savedAt: number
): OfflineBoard | null {
  const normalizedProject = sourceProject(project);
  if (!normalizedProject || !Number.isFinite(savedAt)) return null;
  return {
    project: normalizedProject,
    columns: sourceColumns(columns),
    tasks: sourceTasks(tasks),
    savedAt,
    partial: partial === true
      || (Array.isArray(columns) && columns.length > MAX_COLUMNS_PER_BOARD)
      || (Array.isArray(tasks) && tasks.length > MAX_TASKS_PER_BOARD)
  };
}

function idbFactory(): IDBFactory | null {
  return typeof indexedDB === 'undefined' ? null : indexedDB;
}

function openDatabase(): Promise<IDBDatabase | null> {
  if (databasePromise) return databasePromise;
  const factory = idbFactory();
  if (!factory) return Promise.resolve(null);

  let request: IDBOpenDBRequest;
  try {
    request = factory.open(DB_NAME, DB_VERSION);
  } catch {
    return Promise.resolve(null);
  }

  const promise = new Promise<IDBDatabase | null>((resolve) => {
    let settled = false;
    const finish = (database: IDBDatabase | null) => {
      if (settled) return;
      settled = true;
      resolve(database);
    };
    request.onupgradeneeded = () => {
      try {
        if (!request.result.objectStoreNames.contains(STORE_NAME)) {
          request.result.createObjectStore(STORE_NAME);
        }
      } catch {
        // The request's error/abort callback will fail closed below.
      }
    };
    request.onsuccess = () => {
      try {
        const database = request.result;
        database.onversionchange = () => {
          try { database.close(); } catch { /* fail closed */ }
          databasePromise = null;
        };
        finish(database);
      } catch {
        finish(null);
      }
    };
    request.onerror = () => finish(null);
    request.onblocked = () => finish(null);
  });
  databasePromise = promise;
  return promise;
}

function tokenIsCurrent(token: number, owner: string | null): boolean {
  return token === ownerGeneration && activeOwner === owner;
}

/**
 * Read one record and synchronously decide whether to put its replacement in
 * the same transaction. IDB transactions cannot be resumed after an await;
 * keeping the read and put in this request callback is the race boundary.
 */
async function mutateState(
  token: number,
  owner: string | null,
  mutate: (state: StoredState) => StoredState | null
): Promise<boolean> {
  const database = await openDatabase();
  if (!database || !tokenIsCurrent(token, owner)) return false;

  return new Promise<boolean>((resolve) => {
    let settled = false;
    let wrote = false;
    const finish = (result: boolean) => {
      if (settled) return;
      settled = true;
      resolve(result);
    };

    let transaction: IDBTransaction;
    try {
      transaction = database.transaction(STORE_NAME, 'readwrite');
      const store = transaction.objectStore(STORE_NAME);
      const request = store.get(RECORD_KEY);
      request.onerror = () => finish(false);
      request.onsuccess = () => {
        if (!tokenIsCurrent(token, owner)) {
          finish(false);
          return;
        }
        try {
          const next = mutate(normalizeState(request.result));
          if (!next || !tokenIsCurrent(token, owner)) {
            finish(false);
            return;
          }
          store.put(next, RECORD_KEY);
          wrote = true;
        } catch {
          finish(false);
        }
      };
      transaction.oncomplete = () => finish(wrote);
      transaction.onerror = () => finish(false);
      transaction.onabort = () => finish(false);
    } catch {
      finish(false);
    }
  });
}

async function readState(): Promise<StoredState | null> {
  const database = await openDatabase();
  if (!database) return null;

  return new Promise<StoredState | null>((resolve) => {
    let settled = false;
    let result: unknown;
    const finish = (state: StoredState | null) => {
      if (settled) return;
      settled = true;
      resolve(state);
    };

    try {
      const transaction = database.transaction(STORE_NAME, 'readonly');
      const request = transaction.objectStore(STORE_NAME).get(RECORD_KEY);
      request.onsuccess = () => { result = request.result; };
      request.onerror = () => finish(null);
      transaction.oncomplete = () => finish(normalizeState(result));
      transaction.onerror = () => finish(null);
      transaction.onabort = () => finish(null);
    } catch {
      finish(null);
    }
  });
}

function markClearTombstone(): void {
  try {
    if (typeof window !== 'undefined') {
      window.localStorage.setItem(CLEAR_TOMBSTONE_STORAGE_KEY, `${Date.now()}-${Math.random()}`);
    }
  } catch {
    // localStorage may be unavailable; the in-memory generation still guards
    // all requests made by this page.
  }
}

function clearTombstone(): void {
  try {
    if (typeof window !== 'undefined') window.localStorage.removeItem(CLEAR_TOMBSTONE_STORAGE_KEY);
  } catch {
    // Keeping a tombstone is safer than accidentally exposing an old board
    // when storage access is denied.
  }
}

function hasClearTombstone(): boolean {
  try {
    return typeof window !== 'undefined'
      && window.localStorage.getItem(CLEAR_TOMBSTONE_STORAGE_KEY) !== null;
  } catch {
    // If storage access is denied, fail closed rather than exposing a board
    // whose clear marker may be unreadable.
    return true;
  }
}

function dispatchCleared(): void {
  if (typeof window === 'undefined') return;
  try {
    window.dispatchEvent(new CustomEvent('helm:offline-cleared'));
  } catch {
    try { window.dispatchEvent(new Event('helm:offline-cleared')); } catch { /* advisory only */ }
  }
}

function invalidateFromAnotherTab(): void {
  activeOwner = null;
  ownerGeneration += 1;
  dispatchCleared();
}

function ensureInvalidationChannel(): void {
  if (invalidationSetup || typeof window === 'undefined') return;
  invalidationSetup = true;

  try {
    if (typeof BroadcastChannel === 'function') {
      const channel = new BroadcastChannel(BROADCAST_CHANNEL_NAME);
      const handler = (event: MessageEvent) => {
        if (event.data && typeof event.data === 'object' && event.data.type === 'cleared') {
          invalidateFromAnotherTab();
        }
      };
      if (typeof channel.addEventListener === 'function') channel.addEventListener('message', handler);
      else channel.onmessage = handler;
      broadcastChannel = channel;
      const maybeUnref = channel as BroadcastChannel & { unref?: () => void };
      maybeUnref.unref?.();
    }
  } catch {
    broadcastChannel = null;
  }

  // BroadcastChannel is not present in a few WebKit versions. A storage
  // event is only a signal; board data still lives exclusively in IndexedDB.
  try {
    window.addEventListener('storage', (event: StorageEvent) => {
      if (event.key === INVALIDATION_STORAGE_KEY && event.newValue) invalidateFromAnotherTab();
    });
  } catch {
    // Private browsing may deny event registration; IndexedDB remains safe.
  }
}

function broadcastCleared(): void {
  try {
    broadcastChannel?.postMessage({ type: 'cleared' });
  } catch {
    // Cross-tab notification is best effort.
  }
  try {
    if (typeof window !== 'undefined') {
      window.localStorage.setItem(INVALIDATION_STORAGE_KEY, `${Date.now()}-${Math.random()}`);
    }
  } catch {
    // localStorage may be unavailable in private browsing or sandboxed iframes.
  }
}

export async function setOfflineOwner(ownerId: string): Promise<void> {
  ensureInvalidationChannel();
  const nextOwner = ownerValue(ownerId);
  const previousOwner = activeOwner;
  const token = ++ownerGeneration;
  const resetAtStart = clearGeneration;
  const tombstoneAtStart = hasClearTombstone();
  const ownerChanged = previousOwner !== null && previousOwner !== nextOwner;
  activeOwner = nextOwner;
  if (!nextOwner) markClearTombstone();

  try {
    let clearedForOwner = false;
    const didWrite = await mutateState(token, nextOwner, (state) => {
      if (!nextOwner) return { owner: null, boards: [] };
      const forceClear = tombstoneAtStart || resetAtStart > appliedClearGeneration;
      const persistedOwnerChanged = state.owner !== nextOwner;
      clearedForOwner = forceClear || ownerChanged || persistedOwnerChanged;
      return {
        owner: nextOwner,
        boards: clearedForOwner ? [] : state.boards
      };
    });
    if (didWrite && tokenIsCurrent(token, nextOwner)) {
      if (resetAtStart > appliedClearGeneration) appliedClearGeneration = resetAtStart;
      clearTombstone();
      // A new owner (including one observed from another tab) invalidates
      // stale offline views there. Do not dispatch the same-tab event here:
      // the caller already knows that it changed identity.
      if (clearedForOwner) broadcastCleared();
    }
  } catch {
    // IndexedDB is an enhancement; authentication must remain usable when it
    // is blocked, unavailable, or reports a quota/transaction failure.
  }
}

export async function saveOfflineBoard(
  ownerId: string,
  project: Project,
  columns: Column[],
  tasks: Task[],
  partial: boolean
): Promise<void> {
  ensureInvalidationChannel();
  const owner = ownerValue(ownerId);
  const token = ownerGeneration;
  if (!owner || !tokenIsCurrent(token, owner)) return;

  let board: OfflineBoard | null;
  try {
    board = sourceBoard(project, columns, tasks, partial, Date.now());
  } catch {
    return;
  }
  if (!board) return;
  const savedBoard = board;

  try {
    await mutateState(token, owner, (state) => {
      if (state.owner !== owner) return null;
      const boards = [
        savedBoard,
        ...state.boards.filter((candidate) => candidate.project.id !== savedBoard.project.id)
      ].sort((left, right) => right.savedAt - left.savedAt).slice(0, MAX_BOARDS);
      return { owner, boards };
    });
  } catch {
    // Ignore quota, blocked, and transaction errors. The live board remains
    // available and the next successful refresh can retry the snapshot.
  }
}

export async function readOfflineBoards(): Promise<OfflineBoard[]> {
  ensureInvalidationChannel();
  const generation = ownerGeneration;
  if (hasClearTombstone()) return [];
  try {
    const state = await readState();
    if (generation !== ownerGeneration || hasClearTombstone()) return [];
    // A null owner is an explicit clear, not an anonymous board to expose.
    if (!state?.owner) return [];
    if (activeOwner !== null && state.owner !== activeOwner) return [];
    return state.boards;
  } catch {
    return [];
  }
}

export async function clearOfflineBoards(): Promise<void> {
  ensureInvalidationChannel();
  activeOwner = null;
  const token = ++ownerGeneration;
  clearGeneration += 1;
  markClearTombstone();
  try {
    await mutateState(token, null, () => ({ owner: null, boards: [] }));
  } catch {
    // Keep the tombstone when IDB cannot be written so a reload still fails
    // closed instead of revealing the previous account's board.
  }
  dispatchCleared();
  broadcastCleared();
}
