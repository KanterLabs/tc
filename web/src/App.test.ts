import { describe, expect, it } from 'vitest';
import {
  boardEventPageRequiresRefresh,
  boardMetadataErrorAfterRefresh,
  boardMetadataErrorMessage,
  boardMutationReloadCanAnnounce,
  boardRefreshTargetsAreCurrent,
  mergeOwnedBoardMetadata
} from './App.svelte';
import {
  boardColumnHasKnownGlobalBounds,
  boardOrderingFiltersActive,
  boardOrderingRefreshReason,
  makeBoardOrderingGate,
  type BoardOrderingGate
} from './lib/boardOrdering';

function orderingGate(overrides: Partial<BoardOrderingGate> = {}): BoardOrderingGate {
  return makeBoardOrderingGate({
    criteriaTransition: false,
    filterTimerPending: false,
    boardLoading: false,
    metadataError: false,
    pageRefreshActive: false,
    page: { loaded: true, nextCursor: '', loading: false, error: '' },
    filters: {
      query: '',
      priority: 'all',
      label: 'all',
      assignee: 'all',
      state: 'all',
      dependency: 'all'
    },
    workFilter: 'all',
    sort: 'position',
    order: 'asc',
    ...overrides
  });
}

describe('board refresh ownership and announcement guards', () => {
  it('keeps an untouched terminal page outside global bounds after a failed full refresh', () => {
    const completePage = { loaded: true, nextCursor: '', loading: false, error: '' };
    let metadataErrors = boardMetadataErrorAfterRefresh({ full: '', targeted: {} }, 'full', [], 'Board metadata failed.');

    expect(boardColumnHasKnownGlobalBounds(false, completePage, { metadataLoading: Boolean(boardMetadataErrorMessage(metadataErrors)) })).toBe(false);

    // A targeted refresh of another column succeeds, but it does not own the
    // global columns/labels failure, so the untouched terminal page stays
    // ineligible even after the person clears filters.
    metadataErrors = boardMetadataErrorAfterRefresh(metadataErrors, 'targeted', ['other'], '');
    expect(boardMetadataErrorMessage(metadataErrors)).toBe('Board metadata failed.');
    expect(boardColumnHasKnownGlobalBounds(false, completePage, { metadataLoading: Boolean(boardMetadataErrorMessage(metadataErrors)) })).toBe(false);

    metadataErrors = boardMetadataErrorAfterRefresh(metadataErrors, 'full', [], '');
    expect(boardColumnHasKnownGlobalBounds(false, completePage, { metadataLoading: Boolean(boardMetadataErrorMessage(metadataErrors)) })).toBe(true);
  });

  it('allows disjoint targeted refreshes to complete while rejecting overlapping ownership', () => {
    const refreshes = { ready: 11, blocked: 12 };
    expect(boardRefreshTargetsAreCurrent(refreshes, ['ready'], 11)).toBe(true);
    expect(boardRefreshTargetsAreCurrent(refreshes, ['blocked'], 12)).toBe(true);
    expect(boardRefreshTargetsAreCurrent(refreshes, ['ready', 'blocked'], 11)).toBe(false);
    expect(boardRefreshTargetsAreCurrent({ ready: 13, blocked: 12 }, ['ready'], 11)).toBe(false);
  });

  it('preserves newer disjoint metadata when an older refresh resolves last', () => {
    const initial = [
      { id: 'ready', revision: 1 },
      { id: 'blocked', revision: 1 }
    ];
    const newerBlockedResponse = [
      { id: 'ready', revision: 1 },
      { id: 'blocked', revision: 2 }
    ];
    const olderReadyResponse = [
      { id: 'ready', revision: 2 },
      { id: 'blocked', revision: 1 }
    ];
    const afterBlocked = mergeOwnedBoardMetadata(initial, newerBlockedResponse, ['blocked']);
    const afterReady = mergeOwnedBoardMetadata(afterBlocked, olderReadyResponse, ['ready']);
    expect(afterReady).toEqual([
      { id: 'ready', revision: 2 },
      { id: 'blocked', revision: 2 }
    ]);
  });

  it('lets a targeted owner clear its own error without clearing a full failure', () => {
    let errors = boardMetadataErrorAfterRefresh({ full: '', targeted: {} }, 'targeted', ['ready'], 'Ready metadata failed.');
    errors = boardMetadataErrorAfterRefresh(errors, 'targeted', ['blocked'], '');
    expect(boardMetadataErrorMessage(errors)).toBe('Ready metadata failed.');
    errors = boardMetadataErrorAfterRefresh(errors, 'targeted', ['ready'], '');
    expect(boardMetadataErrorMessage(errors)).toBe('');
    errors = boardMetadataErrorAfterRefresh(errors, 'full', [], 'Full metadata failed.');
    errors = boardMetadataErrorAfterRefresh(errors, 'targeted', ['ready'], '');
    expect(boardMetadataErrorMessage(errors)).toBe('Full metadata failed.');
  });

  it('announces a board mutation only after an owned successful reload', () => {
    expect(boardMutationReloadCanAnnounce(true, true)).toBe(true);
    expect(boardMutationReloadCanAnnounce(true, false)).toBe(false);
    expect(boardMutationReloadCanAnnounce(false, true)).toBe(false);
    expect(boardMutationReloadCanAnnounce(false, false)).toBe(false);
  });

  it('refreshes a board when a truncated event page can hide newer project work', () => {
    expect(boardEventPageRequiresRefresh(true, null)).toBe(true);
    expect(boardEventPageRequiresRefresh(false, '100')).toBe(true);
    expect(boardEventPageRequiresRefresh(false, null)).toBe(false);
  });

  it('settles ordering refresh reasons when criteria and page reads finish', () => {
    expect(boardOrderingRefreshReason(orderingGate({ criteriaTransition: true }))).toContain('criteria are changing');
    expect(boardOrderingRefreshReason(orderingGate({ filterTimerPending: true }))).toContain('criteria are changing');
    expect(boardOrderingRefreshReason(orderingGate({ metadataError: true }))).toContain('metadata refresh failed');
    expect(boardOrderingRefreshReason(orderingGate({ pageRefreshActive: true }))).toContain('column is refreshing');
    expect(boardOrderingRefreshReason(orderingGate({ page: { loaded: false, nextCursor: '', loading: true, error: '' } }))).toContain('column is refreshing');
    expect(boardOrderingRefreshReason(orderingGate())).toBe('');
  });

  it('derives active filters from the explicit ordering render gate', () => {
    expect(boardOrderingFiltersActive(orderingGate())).toBe(false);
    expect(boardOrderingFiltersActive(orderingGate({
      filters: {
        query: 'Visible',
        priority: 'all',
        label: 'all',
        assignee: 'all',
        state: 'all',
        dependency: 'all'
      }
    }))).toBe(true);
    expect(boardOrderingFiltersActive(orderingGate({ workFilter: 'waiting' }))).toBe(true);
  });
});
