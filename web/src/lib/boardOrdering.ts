import type { Task } from './types';

/** Sort fields exposed by the board task collection. */
export type BoardTaskSort = 'position' | 'number' | 'created_at' | 'updated_at' | 'priority' | 'title';
export type BoardTaskOrder = 'asc' | 'desc';

/**
 * The board state that can make a precise ordering control unavailable.
 *
 * Keep this as a value object rather than reaching into App.svelte from the
 * ordering helpers. Svelte's compiler cannot see reads made
 * through an opaque helper call, while callers can construct this object from
 * the current board state at the call site.
 */
export interface BoardOrderingFilters {
  query: string;
  priority: string;
  label: string;
  assignee: string;
  state: string;
  dependency?: string;
}

export interface BoardOrderingGate {
  criteriaTransition: boolean;
  filterTimerPending: boolean;
  boardLoading: boolean;
  metadataError: boolean;
  pageRefreshActive: boolean;
  page?: BoardColumnPageState | null;
  filters: Readonly<BoardOrderingFilters>;
  workFilter: string;
  sort: BoardTaskSort;
  order: BoardTaskOrder;
}

/** Identity helper used by markup so Svelte sees every ordering dependency. */
export function makeBoardOrderingGate(gate: BoardOrderingGate): BoardOrderingGate {
  return gate;
}

/** The subset of a board page needed to decide whether global bounds are known. */
export interface BoardColumnPageState {
  loaded: boolean;
  nextCursor: string;
  loading?: boolean;
  error: string;
}

export interface BoardColumnBoundsOptions {
  /** Board metadata or the initial page set is being refreshed. */
  metadataLoading?: boolean;
  /** A targeted page refresh is replacing the terminal page. */
  pageRefreshActive?: boolean;
  /** A debounced filter/sort change has not settled yet. */
  criteriaTransition?: boolean;
}

export type BoardColumnRequestGenerations = Readonly<Record<string, number>>;

/** Return whether any board-level task filter is currently active. */
export function boardOrderingFiltersActive(gate: BoardOrderingGate): boolean {
  const { filters } = gate;
  return Boolean(
    filters.query.trim()
    || filters.priority !== 'all'
    || filters.label !== 'all'
    || filters.assignee !== 'all'
    || filters.state !== 'all'
    || filters.dependency !== 'all'
    || gate.workFilter !== 'all'
  );
}

/** Precise anchors are safe only in physical board order, ascending. */
export function boardOrderingUsesPhysicalOrder(gate: BoardOrderingGate): boolean {
  return gate.sort === 'position' && gate.order === 'asc';
}

/** Explain only transient/full-refresh ordering gates, in display precedence. */
export function boardOrderingRefreshReason(gate: BoardOrderingGate): string {
  if (gate.criteriaTransition || gate.filterTimerPending) {
    return 'Precise ordering is unavailable while board criteria are changing. Wait for the refreshed board.';
  }
  if (gate.metadataError) {
    return 'Precise ordering is unavailable because the board metadata refresh failed. Retry the board before moving tasks.';
  }
  if (gate.boardLoading || gate.pageRefreshActive || gate.page?.loading) {
    return 'Precise ordering is unavailable while this board column is refreshing. Wait for the refreshed page.';
  }
  return '';
}

/** Claim a new request generation without mutating the caller's state. */
export function claimBoardColumnRequest(
  generations: BoardColumnRequestGenerations,
  columnId: string
): { generation: number; generations: Record<string, number> } {
  const generation = (generations[columnId] || 0) + 1;
  return { generation, generations: { ...generations, [columnId]: generation } };
}

/** Only the newest request for a column may commit its response or error. */
export function boardColumnRequestIsCurrent(
  generations: BoardColumnRequestGenerations,
  columnId: string,
  generation: number
): boolean {
  return generations[columnId] === generation;
}

/**
 * Mirror SQLite's built-in lower() for task title sort keys.
 *
 * SQLite's default lower() folds ASCII A-Z only; JavaScript's Unicode-aware
 * toLowerCase()/toLocaleLowerCase() would reorder non-ASCII titles relative
 * to the server. Iterating UTF-16 code units is intentional here: bytes
 * outside ASCII are preserved exactly as SQLite preserves them for this key.
 */
export function sqliteLowerAscii(value: string): string {
  let folded = '';
  for (let index = 0; index < value.length; index += 1) {
    const code = value.charCodeAt(index);
    folded += code >= 0x41 && code <= 0x5a ? String.fromCharCode(code + 0x20) : value[index];
  }
  return folded;
}

/** Compare strings by Unicode code point, independent of browser locale. */
export function compareCodepointStrings(left: string, right: string): number {
  const leftPoints = Array.from(left, (character) => character.codePointAt(0) || 0);
  const rightPoints = Array.from(right, (character) => character.codePointAt(0) || 0);
  const length = Math.min(leftPoints.length, rightPoints.length);
  for (let index = 0; index < length; index += 1) {
    if (leftPoints[index] !== rightPoints[index]) return leftPoints[index] < rightPoints[index] ? -1 : 1;
  }
  if (leftPoints.length === rightPoints.length) return 0;
  return leftPoints.length < rightPoints.length ? -1 : 1;
}

function compareNumbers(left: number, right: number): number {
  if (left === right) return 0;
  return left < right ? -1 : 1;
}

function boardPriorityRank(priority: Task['priority']): number {
  return priority === 'urgent' ? 0 : priority === 'high' ? 1 : priority === 'normal' ? 2 : 3;
}

function boardSortValue(task: Task, sort: BoardTaskSort): number | string {
  switch (sort) {
    case 'number':
      return task.number;
    case 'created_at':
      return task.created_at || '';
    case 'updated_at':
      return task.updated_at || '';
    case 'priority':
      return boardPriorityRank(task.priority);
    case 'title':
      return sqliteLowerAscii(task.title);
    case 'position':
    default:
      return task.position;
  }
}

/**
 * Sort board tasks with the same primary and tie-break keys as SQLite:
 * title keys use ASCII-only lower(), text compares by code point, and number
 * then ID provide deterministic ties in either direction.
 */
export function sortBoardTasks(items: Task[], sort: BoardTaskSort, order: BoardTaskOrder): Task[] {
  const direction = order === 'desc' ? -1 : 1;
  return [...items].sort((left, right) => {
    const leftValue = boardSortValue(left, sort);
    const rightValue = boardSortValue(right, sort);
    const primary = typeof leftValue === 'number' && typeof rightValue === 'number'
      ? compareNumbers(leftValue, rightValue)
      : compareCodepointStrings(String(leftValue), String(rightValue));
    return direction * (primary || compareNumbers(left.number, right.number) || compareCodepointStrings(left.id, right.id));
  });
}

/** Global first/last positions are safe only after an unfiltered full page. */
export function boardColumnHasKnownGlobalBounds(
  filtersActive: boolean,
  page?: BoardColumnPageState | null,
  options: BoardColumnBoundsOptions = {}
): boolean {
  return Boolean(
    !filtersActive
    && !options.metadataLoading
    && !options.pageRefreshActive
    && !options.criteriaTransition
    && page?.loaded
    && !page.loading
    && !page.nextCursor
    && !page.error
  );
}

/** A collection conflict on a cursor page must reset to page one, not replay the stale cursor. */
export function isRecoverableBoardCursorConflict(
  status: number,
  code: string,
  reset: boolean,
  cursor?: string | null
): boolean {
  return status === 409 && code === 'task_collection_changed' && !reset && Boolean(cursor);
}
