import { describe, expect, it } from 'vitest';
import {
  boardColumnHasKnownGlobalBounds,
  boardColumnRequestIsCurrent,
  claimBoardColumnRequest,
  compareCodepointStrings,
  isRecoverableBoardCursorConflict,
  sortBoardTasks,
  sqliteLowerAscii
} from './boardOrdering';
import type { Task } from './types';

function task(id: string, number: number, title: string): Task {
  return {
    id,
    number,
    key: `ORD-${number}`,
    project_id: 'project-1',
    column_id: 'column-1',
    title,
    priority: 'normal',
    position: number,
    version: 1
  };
}

describe('board ordering helpers', () => {
  it('folds ASCII letters like SQLite lower while preserving non-ASCII letters', () => {
    expect(sqliteLowerAscii('ABC ÀÉİß Ω')).toBe('abc ÀÉİß Ω');
  });

  it('matches SQLite title ordering and number/id tie breakers in both directions', () => {
    const titles = [
      'bravo',
      'Alpha',
      'ALPHA',
      'alpha',
      'Bravo',
      'Àlpha',
      'Álpha',
      'Âlpha',
      'àlpha',
      'Zulu',
      'zulu'
    ];
    const tasks = titles.map((title, index) => task(`task-${index + 1}`, index + 1, title));
    const ascending = sortBoardTasks(tasks, 'title', 'asc').map((item) => item.title);
    const descending = sortBoardTasks(tasks, 'title', 'desc').map((item) => item.title);

    expect(ascending).toEqual([
      'Alpha', 'ALPHA', 'alpha',
      'bravo', 'Bravo',
      'Zulu', 'zulu',
      'Àlpha', 'Álpha', 'Âlpha', 'àlpha'
    ]);
    expect(descending).toEqual([...ascending].reverse());

    const tieTasks = [task('id-z', 7, 'same'), task('id-a', 7, 'SAME'), task('id-b', 2, 'same')];
    expect(sortBoardTasks(tieTasks, 'title', 'asc').map((item) => item.id)).toEqual(['id-b', 'id-a', 'id-z']);
    expect(sortBoardTasks(tieTasks, 'title', 'desc').map((item) => item.id)).toEqual(['id-z', 'id-a', 'id-b']);
  });

  it('keeps sub-millisecond server timestamps in created and updated ordering', () => {
    const older = { ...task('older', 1, 'same'), created_at: '2026-09-04T00:00:00.000000001Z', updated_at: '2026-09-04T00:00:00.000000001Z' };
    const newer = { ...task('newer', 2, 'same'), created_at: '2026-09-04T00:00:00.000000002Z', updated_at: '2026-09-04T00:00:00.000000002Z' };
    expect(sortBoardTasks([newer, older], 'created_at', 'asc').map((item) => item.id)).toEqual(['older', 'newer']);
    expect(sortBoardTasks([newer, older], 'created_at', 'desc').map((item) => item.id)).toEqual(['newer', 'older']);
    expect(sortBoardTasks([newer, older], 'updated_at', 'asc').map((item) => item.id)).toEqual(['older', 'newer']);
    expect(sortBoardTasks([newer, older], 'updated_at', 'desc').map((item) => item.id)).toEqual(['newer', 'older']);
  });

  it('uses number and id ties in both directions for every board sort', () => {
    const tied = [
      { ...task('id-b', 2, 'same'), position: 7, priority: 'high' as const, created_at: '2026-09-04T00:00:00.000000001Z', updated_at: '2026-09-04T00:00:00.000000001Z' },
      { ...task('id-a', 1, 'same'), position: 7, priority: 'high' as const, created_at: '2026-09-04T00:00:00.000000001Z', updated_at: '2026-09-04T00:00:00.000000001Z' },
      { ...task('id-c', 3, 'same'), position: 7, priority: 'high' as const, created_at: '2026-09-04T00:00:00.000000001Z', updated_at: '2026-09-04T00:00:00.000000001Z' }
    ];
    (['position', 'created_at', 'updated_at', 'priority', 'title'] as const).forEach((sort) => {
      expect(sortBoardTasks(tied, sort, 'asc').map((item) => item.id), sort).toEqual(['id-a', 'id-b', 'id-c']);
      expect(sortBoardTasks(tied, sort, 'desc').map((item) => item.id), sort).toEqual(['id-c', 'id-b', 'id-a']);
    });

    const tiedNumbers = tied.map((item) => ({ ...item, number: 7 }));
    expect(sortBoardTasks(tiedNumbers, 'number', 'asc').map((item) => item.id)).toEqual(['id-a', 'id-b', 'id-c']);
    expect(sortBoardTasks(tiedNumbers, 'number', 'desc').map((item) => item.id)).toEqual(['id-c', 'id-b', 'id-a']);
  });

  it('compares text by code point rather than the active browser locale', () => {
    expect(compareCodepointStrings('Z', 'a')).toBe(-1);
    expect(compareCodepointStrings('😀', '\u{ffff}')).toBe(1);
    expect(compareCodepointStrings('same', 'same')).toBe(0);
  });

  it('only treats a fully loaded unfiltered page as having known global bounds', () => {
    const complete = { loaded: true, nextCursor: '', loading: false, error: '' };
    expect(boardColumnHasKnownGlobalBounds(false, complete)).toBe(true);
    expect(boardColumnHasKnownGlobalBounds(true, complete)).toBe(false);
    expect(boardColumnHasKnownGlobalBounds(false, { ...complete, nextCursor: 'next' })).toBe(false);
    expect(boardColumnHasKnownGlobalBounds(false, { ...complete, error: 'retry' })).toBe(false);
    expect(boardColumnHasKnownGlobalBounds(false, { ...complete, loaded: false })).toBe(false);
    expect(boardColumnHasKnownGlobalBounds(false, { ...complete, loading: true })).toBe(false);
    expect(boardColumnHasKnownGlobalBounds(false, complete, { metadataLoading: true })).toBe(false);
    expect(boardColumnHasKnownGlobalBounds(false, complete, { pageRefreshActive: true })).toBe(false);
    expect(boardColumnHasKnownGlobalBounds(false, complete, { criteriaTransition: true })).toBe(false);
  });

  it('keeps global bounds disabled after a failed refresh even when filters are cleared', () => {
    const complete = { loaded: true, nextCursor: '', loading: false, error: '' };
    let filtersActive = false;
    let metadataError = false;
    expect(boardColumnHasKnownGlobalBounds(filtersActive, complete, { metadataLoading: metadataError })).toBe(true);

    metadataError = true;
    expect(boardColumnHasKnownGlobalBounds(filtersActive, complete, { metadataLoading: metadataError })).toBe(false);
    filtersActive = true;
    expect(boardColumnHasKnownGlobalBounds(filtersActive, complete, { metadataLoading: metadataError })).toBe(false);
    filtersActive = false;
    expect(boardColumnHasKnownGlobalBounds(filtersActive, complete, { metadataLoading: metadataError })).toBe(false);
  });

  it('keeps overlapping same-column refresh generations isolated', () => {
    const first = claimBoardColumnRequest({}, 'ready');
    const other = claimBoardColumnRequest(first.generations, 'backlog');
    const second = claimBoardColumnRequest(other.generations, 'ready');

    expect(boardColumnRequestIsCurrent(second.generations, 'ready', first.generation)).toBe(false);
    expect(boardColumnRequestIsCurrent(second.generations, 'ready', second.generation)).toBe(true);
    expect(boardColumnRequestIsCurrent(second.generations, 'backlog', other.generation)).toBe(true);

    const firstRefresh = { ready: 1, backlog: 1 };
    const overlappingRefresh = { ...firstRefresh, ready: 2 };
    expect(['ready', 'backlog'].every((columnId) => boardColumnRequestIsCurrent(overlappingRefresh, columnId, 1))).toBe(false);
    expect(boardColumnRequestIsCurrent(overlappingRefresh, 'ready', 2)).toBe(true);
    expect(boardColumnRequestIsCurrent(overlappingRefresh, 'backlog', 1)).toBe(true);
  });

  it('resets a stale cursor conflict once and never replays it after reset', () => {
    expect(isRecoverableBoardCursorConflict(409, 'task_collection_changed', false, 'stale-cursor')).toBe(true);
    expect(isRecoverableBoardCursorConflict(409, 'task_collection_changed', true, 'stale-cursor')).toBe(false);
    expect(isRecoverableBoardCursorConflict(409, 'task_collection_changed', false, '')).toBe(false);
    expect(isRecoverableBoardCursorConflict(409, 'stale_task', false, 'stale-cursor')).toBe(false);
    expect(isRecoverableBoardCursorConflict(500, 'task_collection_changed', false, 'stale-cursor')).toBe(false);
  });
});
