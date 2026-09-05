import { afterAll, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  clearOfflineBoards,
  readOfflineBoards,
  saveOfflineBoard,
  setOfflineOwner
} from './offlineBoards';
import type { Column, Project, Task } from './types';

type MemoryRecord = { owner: string | null; boards: unknown[] };

class MemoryRequest<T = unknown> {
  result!: T;
  onsuccess: ((event: Event) => void) | null = null;
  onerror: (() => void) | null = null;
}

class MemoryTransaction {
  request: MemoryRequest | null = null;
  oncomplete: (() => void) | null = null;
  onerror: (() => void) | null = null;
  onabort: (() => void) | null = null;

  constructor(private readonly database: MemoryDatabase) {}

  objectStore(): MemoryObjectStore {
    return new MemoryObjectStore(this, this.database);
  }

  run(): void {
    if (!this.request) return;
    this.request.result = this.database.record === undefined
      ? undefined
      : clone(this.database.record);
    this.request.onsuccess?.({ target: this.request } as unknown as Event);
    queueMicrotask(() => this.oncomplete?.());
  }
}

class MemoryObjectStore {
  constructor(
    private readonly transaction: MemoryTransaction,
    private readonly database: MemoryDatabase
  ) {}

  get(): MemoryRequest {
    const request = new MemoryRequest();
    this.transaction.request = request;
    return request;
  }

  put(value: unknown): MemoryRequest {
    this.database.record = clone(value) as MemoryRecord;
    return new MemoryRequest();
  }
}

class MemoryDatabase {
  record: MemoryRecord | undefined;
  private queue = Promise.resolve();
  readonly objectStoreNames = {
    contains: (name: string) => name === 'snapshots'
  } as unknown as DOMStringList;
  onversionchange: (() => void) | null = null;

  transaction(): MemoryTransaction {
    const transaction = new MemoryTransaction(this);
    const previous = this.queue;
    this.queue = new Promise<void>((resolve) => {
      queueMicrotask(async () => {
        await previous;
        transaction.run();
        queueMicrotask(resolve);
      });
    });
    return transaction;
  }

  createObjectStore(): void {
    // The memory database always exposes the one store used by the module.
  }

  close(): void {}
}

class MemoryOpenRequest extends MemoryRequest<MemoryDatabase> {
  onupgradeneeded: (() => void) | null = null;
  onblocked: (() => void) | null = null;

  constructor(database: MemoryDatabase) {
    super();
    this.result = database;
    queueMicrotask(() => {
      this.onupgradeneeded?.();
      this.onsuccess?.({ target: this } as unknown as Event);
    });
  }
}

class MemoryFactory {
  readonly database = new MemoryDatabase();

  open(): MemoryOpenRequest {
    return new MemoryOpenRequest(this.database);
  }
}

function clone<T>(value: T): T {
  return JSON.parse(JSON.stringify(value)) as T;
}

function fixtureProject(id = 'project-1'): Project {
  return {
    id,
    key: 'PRJ',
    slug: 'project',
    name: 'Project',
    description: '',
    color: '#000000',
    favorite: false
  };
}

function fixtureColumn(projectId = 'project-1', id = 'column-1'): Column {
  return {
    id,
    project_id: projectId,
    name: 'Backlog',
    semantic_state: 'backlog',
    position: 0
  };
}

function fixtureTask(projectId = 'project-1', id = 'task-1', columnId = 'column-1'): Task {
  return {
    id,
    number: 1,
    key: 'PRJ-1',
    project_id: projectId,
    column_id: columnId,
    title: 'Task',
    description: 'Description',
    priority: 'normal',
    position: 0,
    version: 1
  };
}

let memoryFactory: MemoryFactory;

beforeAll(() => {
  memoryFactory = new MemoryFactory();
  vi.stubGlobal('indexedDB', memoryFactory as unknown as IDBFactory);
});

beforeEach(async () => {
  window.localStorage.removeItem('helm:offline-clear-marker');
  window.localStorage.removeItem('helm:offline-cleared');
  await setOfflineOwner(`test-owner-${Math.random()}`);
});

afterAll(() => {
  vi.unstubAllGlobals();
});

describe('offline board snapshots', () => {
  it('whitelists fields and bounds board contents', async () => {
    const owner = `bounded-owner-${Math.random()}`;
    await setOfflineOwner(owner);
    const project = Object.assign(fixtureProject(), { secret: 'must not persist' });
    const columns = Array.from({ length: 101 }, (_, index) => Object.assign(
      fixtureColumn(project.id, `column-${index}`),
      { private_field: 'must not persist' }
    ));
    const tasks = Array.from({ length: 501 }, (_, index) => Object.assign(
      fixtureTask(project.id, `task-${index}`),
      {
        title: 't'.repeat(2_000),
        description: 'd'.repeat(20_000),
        private_field: 'must not persist'
      }
    ));

    await saveOfflineBoard(owner, project, columns, tasks, false);
    const [saved] = await readOfflineBoards();
    expect(saved).toMatchObject({
      project: { id: project.id, key: project.key },
      partial: true
    });
    expect(saved.columns).toHaveLength(100);
    expect(saved.tasks).toHaveLength(500);
    expect(saved.tasks[0].title.length).toBeLessThanOrEqual(500);
    expect(saved.tasks[0].description.length).toBeLessThanOrEqual(10_000);
    expect(Object.keys(saved.project)).toEqual(['id', 'slug', 'name', 'key']);
    expect(Object.keys(saved.columns[0])).toEqual(['id', 'name', 'position']);
    expect(Object.keys(saved.tasks[0])).toEqual(['id', 'key', 'title', 'description', 'column_id', 'priority']);
  });

  it('keeps only five newest project snapshots and drops expired records', async () => {
    const owner = `expiry-owner-${Math.random()}`;
    await setOfflineOwner(owner);
    for (let index = 0; index < 6; index += 1) {
      await saveOfflineBoard(owner, fixtureProject(`project-${index}`), [fixtureColumn(`project-${index}`)], [], false);
    }
    expect((await readOfflineBoards()).map((board) => board.project.id)).toEqual([
      'project-5', 'project-4', 'project-3', 'project-2', 'project-1'
    ]);

    const now = Date.now();
    memoryFactory.database.record = {
      owner,
      boards: [
        { project: fixtureProject('fresh'), columns: [], tasks: [], savedAt: now - 1, partial: false },
        { project: fixtureProject('boundary'), columns: [], tasks: [], savedAt: now - 7 * 24 * 60 * 60 * 1000, partial: false },
        { project: fixtureProject('old'), columns: [], tasks: [], savedAt: now - 7 * 24 * 60 * 60 * 1000 - 1, partial: false },
        { project: fixtureProject('future'), columns: [], tasks: [], savedAt: now + 24 * 60 * 60 * 1000, partial: false }
      ]
    };
    await expect(readOfflineBoards()).resolves.toEqual(expect.arrayContaining([
      expect.objectContaining({ project: expect.objectContaining({ id: 'fresh' }) })
    ]));
    expect(await readOfflineBoards()).toHaveLength(1);
  });

  it('does not expose a board after switching owners', async () => {
    const firstOwner = `first-owner-${Math.random()}`;
    const secondOwner = `second-owner-${Math.random()}`;
    await setOfflineOwner(firstOwner);
    await saveOfflineBoard(firstOwner, fixtureProject('private'), [fixtureColumn('private')], [], false);
    expect((await readOfflineBoards()).map((board) => board.project.id)).toEqual(['private']);

    await setOfflineOwner(secondOwner);
    expect(await readOfflineBoards()).toEqual([]);
    await saveOfflineBoard(firstOwner, fixtureProject('stale'), [fixtureColumn('stale')], [], false);
    expect(await readOfflineBoards()).toEqual([]);

    await setOfflineOwner(firstOwner);
    expect(await readOfflineBoards()).toEqual([]);
  });

  it('uses a persisted clear marker to force a clean same-owner reauthentication', async () => {
    const owner = `reauth-owner-${Math.random()}`;
    const saved = fixtureProject('old-session');
    memoryFactory.database.record = {
      owner,
      boards: [{ project: saved, columns: [], tasks: [], savedAt: Date.now(), partial: false }]
    };
    window.localStorage.setItem('helm:offline-clear-marker', 'pending-clear');

    await setOfflineOwner(owner);
    expect(await readOfflineBoards()).toEqual([]);
    expect(memoryFactory.database.record).toMatchObject({ owner, boards: [] });
    expect(window.localStorage.getItem('helm:offline-clear-marker')).toBeNull();
  });

  it('invalidates an in-flight read and save after an external clear signal', async () => {
    const owner = `external-owner-${Math.random()}`;
    await setOfflineOwner(owner);
    const pendingRead = readOfflineBoards();
    window.dispatchEvent(new StorageEvent('storage', {
      key: 'helm:offline-cleared',
      newValue: 'external-clear'
    }));
    expect(await pendingRead).toEqual([]);

    const pendingSave = saveOfflineBoard(owner, fixtureProject('stale-save'), [fixtureColumn('stale-save')], [], false);
    window.dispatchEvent(new StorageEvent('storage', {
      key: 'helm:offline-cleared',
      newValue: 'external-clear-again'
    }));
    await pendingSave;
    expect(memoryFactory.database.record?.boards).toEqual([]);
  });

  it('clears snapshots on owner changes and ignores stale saves', async () => {
    const owner = `race-owner-${Math.random()}`;
    await setOfflineOwner(owner);
    const pendingSave = saveOfflineBoard(owner, fixtureProject('race'), [fixtureColumn('race')], [fixtureTask('race')], false);
    await clearOfflineBoards();
    await setOfflineOwner(owner);
    await pendingSave;
    expect(await readOfflineBoards()).toEqual([]);
  });

  it('emits the clear event only for an explicit clear in the same tab', async () => {
    const owner = `event-owner-${Math.random()}`;
    await setOfflineOwner(owner);
    const events: string[] = [];
    const handler = (event: Event) => events.push(event.type);
    window.addEventListener('helm:offline-cleared', handler);
    await setOfflineOwner(owner);
    expect(events).toEqual([]);
    await clearOfflineBoards();
    window.removeEventListener('helm:offline-cleared', handler);
    expect(events).toEqual(['helm:offline-cleared']);
  });
});
