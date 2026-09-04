<script context="module" lang="ts">
  import type {
    Task as DependencyTask,
    TaskDependencies as DependencyRelations,
    TaskReference as DependencyReference
  } from '../types';

  /** Keep combobox filtering deterministic, project-local, and cycle-aware. */
  export function dependencyCandidates(
    tasks: DependencyTask[],
    currentTaskId: string,
    relations: DependencyRelations | null,
    query: string,
    limit = 20
  ): DependencyTask[] {
    const normalized = query.trim().toLocaleLowerCase();
    if (!normalized) return [];
    const excluded = new Set<string>([
      currentTaskId,
      ...(relations?.prerequisites || []).map((item) => item.id),
      ...(relations?.dependents || []).map((item) => item.id)
    ]);
    return tasks
      .filter((candidate) => !excluded.has(candidate.id))
      .filter((candidate) =>
        candidate.key.toLocaleLowerCase().includes(normalized)
        || candidate.title.toLocaleLowerCase().includes(normalized)
      )
      .sort((left, right) => left.number - right.number || left.key.localeCompare(right.key))
      .slice(0, Math.max(1, limit));
  }

  /** Return the next active option for the combobox's keyboard contract. */
  export function nextDependencyOptionIndex(current: number, key: string, count: number): number {
    if (count <= 0) return -1;
    if (key === 'Home') return 0;
    if (key === 'End') return count - 1;
    if (key === 'ArrowDown') return current < 0 ? 0 : (current + 1) % count;
    if (key === 'ArrowUp') return current < 0 ? count - 1 : (current - 1 + count) % count;
    return current;
  }

  export function dependencyStateLabel(reference: DependencyReference): string {
    return reference.satisfied ? 'Done' : 'Open';
  }

  export function dependencyMutationMessage(error: { code?: string; message?: string } | null, fallback: string): string {
    switch (error?.code) {
      case 'stale_task': return 'This task changed elsewhere. Relationships were refreshed; review them and try again.';
      case 'dependency_cycle': return 'That prerequisite would create a dependency cycle.';
      case 'dependency_already_exists': return 'That prerequisite is already linked.';
      case 'dependency_self_reference': return 'A task cannot depend on itself.';
      case 'dependency_cross_project': return 'Prerequisites must belong to this project.';
      case 'dependency_limit_exceeded': return 'This task has reached its direct dependency limit.';
      case 'dependency_not_found': return 'That task is no longer available.';
      case 'unmet_dependencies': return 'An unfinished prerequisite cannot be added after work has started.';
      case 'task_already_claimed': return 'Another actor currently owns this task claim.';
      default: return error?.message?.trim() || fallback;
    }
  }
</script>

<script lang="ts">
  import { onDestroy, tick } from 'svelte';
  import { api, listAllTasks } from '../api';
  import {
    ApiError,
    type Task,
    type TaskDependencies,
    type TaskReference
  } from '../types';

  export let task: Task;
  /** Incremented when an event changes derived readiness without a task version bump. */
  export let refreshToken = 0;
  export let onTaskUpdated: (updated: Task) => void = () => undefined;
  export let onNavigate: (reference: TaskReference) => void | Promise<void> = () => undefined;
  export let onRefreshTask: () => void | Promise<void> = () => undefined;

  let relations: TaskDependencies | null = null;
  let projectTasks: Task[] = [];
  let relationsLoading = false;
  let candidatesLoading = false;
  let relationsError = '';
  let addError = '';
  let removeErrors: Record<string, string> = {};
  let pendingAdd = false;
  let removing: Record<string, boolean> = {};
  let query = '';
  let comboboxOpen = false;
  let activeIndex = -1;
  let searchInput: HTMLInputElement;
  let announcement = '';
  let loadedTaskId = '';
  let loadedRefreshToken = -1;
  let loadedProjectId = '';
  let relationRequest = 0;
  let candidateRequest = 0;
  let blurTimer: ReturnType<typeof setTimeout> | undefined;
  let destroyed = false;

  $: matches = dependencyCandidates(projectTasks, task.id, relations, query);
  $: if (activeIndex >= matches.length) activeIndex = matches.length ? matches.length - 1 : -1;
  $: activeOptionId = activeIndex >= 0 && matches[activeIndex]
    ? `dependency-option-${matches[activeIndex].id}`
    : undefined;
  $: mutationBusy = pendingAdd || Object.values(removing).some(Boolean);
  $: if (task?.id && (task.id !== loadedTaskId || refreshToken !== loadedRefreshToken)) {
    loadedTaskId = task.id;
    loadedRefreshToken = refreshToken;
    void loadRelations(task.id);
  }
  $: if (task?.project_id && task.project_id !== loadedProjectId) {
    loadedProjectId = task.project_id;
    void loadCandidates(task.project_id);
  }

  onDestroy(() => {
    destroyed = true;
    relationRequest += 1;
    candidateRequest += 1;
    if (blurTimer) clearTimeout(blurTimer);
  });

  async function loadRelations(taskId: string): Promise<boolean> {
    const requestId = ++relationRequest;
    relationsLoading = true;
    relationsError = '';
    try {
      const loaded = await api.getTaskDependencies(taskId);
      if (destroyed || requestId !== relationRequest || task.id !== taskId) return false;
      relations = {
        prerequisites: loaded.prerequisites || [],
        dependents: loaded.dependents || []
      };
      return true;
    } catch (error) {
      if (!destroyed && requestId === relationRequest && task.id === taskId) {
        relationsError = dependencyMutationMessage(error instanceof ApiError ? error : null, 'Dependencies could not be loaded.');
      }
      return false;
    } finally {
      if (!destroyed && requestId === relationRequest) relationsLoading = false;
    }
  }

  /** Lets the event poller include this independently loaded view in its retry gate. */
  export async function refreshRelationships(): Promise<boolean> {
    return loadRelations(task.id);
  }

  async function loadCandidates(projectId: string): Promise<void> {
    const requestId = ++candidateRequest;
    candidatesLoading = true;
    try {
      const loaded = await listAllTasks(projectId, { limit: 200 });
      if (destroyed || requestId !== candidateRequest || task.project_id !== projectId) return;
      projectTasks = loaded.data || [];
    } catch {
      if (!destroyed && requestId === candidateRequest && task.project_id === projectId) projectTasks = [];
    } finally {
      if (!destroyed && requestId === candidateRequest) candidatesLoading = false;
    }
  }

  function staleTask(error: unknown): Task | null {
    if (!(error instanceof ApiError) || error.code !== 'stale_task') return null;
    const current = error.details.current;
    if (!current || typeof current !== 'object') return null;
    const candidate = current as Partial<Task>;
    return typeof candidate.id === 'string' && typeof candidate.version === 'number' && typeof candidate.title === 'string'
      ? candidate as Task
      : null;
  }

  async function recoverAfterMutationError(error: unknown, taskId: string): Promise<void> {
    const current = staleTask(error);
    if (current) onTaskUpdated(current);
    if (error instanceof ApiError && error.code === 'stale_task') await onRefreshTask();
    await loadRelations(taskId);
  }

  async function addPrerequisite(candidate: Task): Promise<void> {
    if (mutationBusy || task.id === candidate.id) return;
    const taskId = task.id;
    const version = task.version;
    pendingAdd = true;
    addError = '';
    try {
      const updated = await api.addTaskDependency(taskId, candidate.id, version);
      if (destroyed || task.id !== taskId) return;
      onTaskUpdated(updated);
      await loadRelations(taskId);
      query = '';
      activeIndex = -1;
      comboboxOpen = false;
      announcement = `${candidate.key} added as a prerequisite.`;
      // Re-enable the control before restoring focus; disabled inputs cannot
      // receive focus in browsers or assistive-technology test environments.
      pendingAdd = false;
      await tick();
      searchInput?.focus();
    } catch (error) {
      if (destroyed || task.id !== taskId) return;
      addError = dependencyMutationMessage(error instanceof ApiError ? error : null, 'The prerequisite could not be added.');
      announcement = addError;
      await recoverAfterMutationError(error, taskId);
    } finally {
      if (!destroyed && task.id === taskId) pendingAdd = false;
    }
  }

  async function removePrerequisite(reference: TaskReference): Promise<void> {
    if (mutationBusy) return;
    const taskId = task.id;
    const version = task.version;
    removing = { ...removing, [reference.id]: true };
    removeErrors = { ...removeErrors, [reference.id]: '' };
    try {
      const updated = await api.removeTaskDependency(taskId, reference.id, version);
      if (destroyed || task.id !== taskId) return;
      onTaskUpdated(updated);
      await loadRelations(taskId);
      announcement = `${reference.key} removed as a prerequisite.`;
      await tick();
      searchInput?.focus();
    } catch (error) {
      if (destroyed || task.id !== taskId) return;
      const message = dependencyMutationMessage(error instanceof ApiError ? error : null, 'The prerequisite could not be removed.');
      removeErrors = { ...removeErrors, [reference.id]: message };
      announcement = message;
      await recoverAfterMutationError(error, taskId);
    } finally {
      if (!destroyed && task.id === taskId) {
        const next = { ...removing };
        delete next[reference.id];
        removing = next;
      }
    }
  }

  function handleComboboxKeydown(event: KeyboardEvent) {
    if (['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) {
      event.preventDefault();
      comboboxOpen = true;
      activeIndex = nextDependencyOptionIndex(activeIndex, event.key, matches.length);
      return;
    }
    if (event.key === 'Enter' && activeIndex >= 0 && matches[activeIndex]) {
      event.preventDefault();
      void addPrerequisite(matches[activeIndex]);
      return;
    }
    if (event.key === 'Escape') {
      event.preventDefault();
      comboboxOpen = false;
      activeIndex = -1;
    }
  }

  function deferComboboxClose() {
    if (blurTimer) clearTimeout(blurTimer);
    blurTimer = setTimeout(() => {
      comboboxOpen = false;
      activeIndex = -1;
    }, 120);
  }
</script>

<section class="dependency-panel" aria-labelledby="task-dependencies-heading" aria-busy={relationsLoading}>
  <div class="dependency-heading">
    <div>
      <h2 id="task-dependencies-heading">Dependencies</h2>
      <p>Finish every prerequisite before this task starts or completes.</p>
    </div>
    {#if task.dependency_summary?.blocked}
      <span class="dependency-blocked">Blocked by {task.dependency_summary.unmet_prerequisite_count}</span>
    {:else if task.dependency_summary?.prerequisite_count}
      <span class="dependency-ready">Ready</span>
    {/if}
  </div>

  <p class="sr-only" aria-live="polite" aria-atomic="true">{announcement}</p>

  {#if relationsLoading && !relations}
    <div class="dependency-loading" role="status"><span></span><span>Loading relationships…</span></div>
  {:else if relationsError && !relations}
    <div class="dependency-load-error" role="alert">
      <span>{relationsError}</span>
      <button type="button" on:click={() => loadRelations(task.id)}>Retry</button>
    </div>
  {:else}
    <div class="relationship-grid">
      <section aria-labelledby="waiting-on-heading">
        <h3 id="waiting-on-heading">Waiting on <span>{relations?.prerequisites.length || 0}</span></h3>
        {#if relations?.prerequisites.length}
          <ul class="relationship-list">
            {#each relations.prerequisites as reference (reference.id)}
              <li>
                <button class="relationship-link" type="button" aria-label={`Open ${reference.key}: ${reference.title}`} on:click={() => onNavigate(reference)}>
                  <span class="relationship-key">{reference.key}</span>
                  <span class="relationship-title">{reference.title}</span>
                  <span class:done={reference.satisfied} class="relationship-state">{dependencyStateLabel(reference)}</span>
                </button>
                <button
                  class="remove-relationship"
                  type="button"
                  aria-label={`Remove ${reference.key} as a prerequisite`}
                  disabled={mutationBusy}
                  on:click={() => removePrerequisite(reference)}
                >{removing[reference.id] ? '…' : '×'}</button>
                {#if removeErrors[reference.id]}<p class="relationship-error" role="alert">{removeErrors[reference.id]}</p>{/if}
              </li>
            {/each}
          </ul>
        {:else}
          <p class="relationship-empty">No prerequisites yet.</p>
        {/if}
      </section>

      <section aria-labelledby="blocking-heading">
        <h3 id="blocking-heading">Blocking <span>{relations?.dependents.length || 0}</span></h3>
        {#if relations?.dependents.length}
          <ul class="relationship-list dependent-list">
            {#each relations.dependents as reference (reference.id)}
              <li>
                <button class="relationship-link" type="button" aria-label={`Open ${reference.key}: ${reference.title}`} on:click={() => onNavigate(reference)}>
                  <span class="relationship-key">{reference.key}</span>
                  <span class="relationship-title">{reference.title}</span>
                  <span class:done={reference.satisfied} class="relationship-state">{dependencyStateLabel(reference)}</span>
                </button>
              </li>
            {/each}
          </ul>
        {:else}
          <p class="relationship-empty">No tasks depend on this one.</p>
        {/if}
      </section>
    </div>

    <form class="dependency-add" on:submit|preventDefault={() => activeIndex >= 0 && matches[activeIndex] && addPrerequisite(matches[activeIndex])}>
      <label for="dependency-search">Add prerequisite</label>
      <div class="combobox-wrap">
        <input
          bind:this={searchInput}
          id="dependency-search"
          role="combobox"
          aria-autocomplete="list"
          aria-expanded={comboboxOpen && Boolean(query.trim())}
          aria-controls="dependency-options"
          aria-activedescendant={activeOptionId}
          autocomplete="off"
          placeholder="Search this project by key or title"
          disabled={pendingAdd || candidatesLoading}
          bind:value={query}
          on:focus={() => comboboxOpen = true}
          on:input={() => { comboboxOpen = true; activeIndex = matches.length ? 0 : -1; addError = ''; }}
          on:keydown={handleComboboxKeydown}
          on:blur={deferComboboxClose}
        />
        {#if pendingAdd}<span class="add-spinner" aria-hidden="true"></span>{/if}
        {#if comboboxOpen && query.trim()}
          <div id="dependency-options" class="dependency-options" role="listbox" aria-label="Project tasks">
            {#if matches.length}
              {#each matches as candidate, index (candidate.id)}
                <button
                  id={`dependency-option-${candidate.id}`}
                  class:active={index === activeIndex}
                  type="button"
                  role="option"
                  aria-selected={index === activeIndex}
                  tabindex="-1"
                  on:mouseenter={() => activeIndex = index}
                  on:mousedown|preventDefault
                  on:click={() => addPrerequisite(candidate)}
                >
                  <span>{candidate.key}</span>
                  <strong>{candidate.title}</strong>
                  {#if candidate.completed_at}<small>Done</small>{/if}
                </button>
              {/each}
            {:else}
              <p>No available tasks match “{query.trim()}”.</p>
            {/if}
          </div>
        {/if}
      </div>
      <small>Current task, existing links, and direct dependents are excluded.</small>
      {#if candidatesLoading}<p class="dependency-note" role="status">Loading project tasks…</p>{/if}
      {#if addError}<p class="relationship-error add-error" role="alert">{addError}</p>{/if}
    </form>
  {/if}
</section>

<style>
  .dependency-panel {
    display: grid;
    gap: 14px;
    padding: 15px;
    border: 1px solid var(--border);
    border-radius: 10px;
    background: var(--surface-raised);
  }
  .dependency-heading { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px; }
  .dependency-heading h2 { margin: 0; color: var(--ink); font-size: 12px; }
  .dependency-heading p { margin: 4px 0 0; color: var(--muted); font-size: 11px; line-height: 1.45; }
  .dependency-blocked, .dependency-ready { flex: 0 0 auto; padding: 4px 7px; border-radius: 6px; font-size: 11px; font-weight: 800; }
  .dependency-blocked { color: var(--red); background: var(--red-soft); }
  .dependency-ready { color: var(--green); background: var(--green-soft); }
  .relationship-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 10px; }
  .relationship-grid section { min-width: 0; }
  .relationship-grid h3 { display: flex; align-items: center; gap: 5px; margin: 0 0 7px; color: var(--ink-soft); font-size: 11px; }
  .relationship-grid h3 span { min-width: 18px; padding: 2px 5px; border-radius: 999px; color: var(--muted); background: var(--surface-muted); font-size: 11px; text-align: center; }
  .relationship-list { display: grid; gap: 6px; margin: 0; padding: 0; list-style: none; }
  .relationship-list li { position: relative; display: grid; grid-template-columns: minmax(0, 1fr) 34px; gap: 4px; min-width: 0; }
  .dependent-list li { grid-template-columns: minmax(0, 1fr); }
  .relationship-link { min-width: 0; min-height: 44px; display: grid; grid-template-columns: auto minmax(0, 1fr) auto; align-items: center; gap: 7px; padding: 7px 8px; border: 1px solid var(--border); border-radius: 8px; color: var(--ink); background: var(--surface); text-align: left; }
  .relationship-link:hover, .relationship-link:focus-visible { border-color: var(--purple); outline: none; }
  .relationship-key { color: var(--purple); font: 800 11px ui-monospace, SFMono-Regular, Menlo, monospace; }
  .relationship-title { min-width: 0; overflow: hidden; font-size: 11px; font-weight: 650; text-overflow: ellipsis; white-space: nowrap; }
  .relationship-state { padding: 2px 5px; border-radius: 5px; color: var(--amber); background: var(--amber-soft); font-size: 11px; font-weight: 800; }
  .relationship-state.done { color: var(--green); background: var(--green-soft); }
  .remove-relationship { min-width: 34px; min-height: 44px; border: 1px solid var(--border); border-radius: 8px; color: var(--muted); background: var(--surface); font-size: 16px; }
  .remove-relationship:hover:not(:disabled), .remove-relationship:focus-visible { color: var(--red); border-color: var(--red); outline: none; }
  .remove-relationship:disabled { cursor: wait; opacity: .55; }
  .relationship-empty { min-height: 44px; display: flex; align-items: center; margin: 0; padding: 8px; border: 1px dashed var(--border); border-radius: 8px; color: var(--faint); font-size: 11px; }
  .relationship-error { grid-column: 1 / -1; margin: 0; color: var(--red); font-size: 11px; line-height: 1.4; }
  .dependency-add { display: grid; gap: 5px; padding-top: 12px; border-top: 1px solid var(--border); }
  .dependency-add > label { color: var(--ink-soft); font-size: 11px; font-weight: 700; }
  .dependency-add > small { color: var(--faint); font-size: 11px; }
  .combobox-wrap { position: relative; }
  .combobox-wrap > input { width: 100%; min-height: 44px; padding-right: 34px; }
  .dependency-options { position: absolute; top: calc(100% + 4px); right: 0; left: 0; z-index: 8; max-height: 225px; overflow: auto; padding: 5px; border: 1px solid var(--border-strong); border-radius: 9px; background: var(--surface-raised); box-shadow: var(--shadow-md); }
  .dependency-options button { width: 100%; min-height: 44px; display: grid; grid-template-columns: auto minmax(0, 1fr) auto; align-items: center; gap: 8px; padding: 7px 8px; border: 0; border-radius: 6px; color: var(--ink); background: transparent; text-align: left; }
  .dependency-options button:hover, .dependency-options button.active { background: var(--purple-soft); }
  .dependency-options button span { color: var(--purple); font: 800 11px ui-monospace, SFMono-Regular, Menlo, monospace; }
  .dependency-options button strong { min-width: 0; overflow: hidden; font-size: 11px; text-overflow: ellipsis; white-space: nowrap; }
  .dependency-options button small { color: var(--green); font-size: 11px; font-weight: 700; }
  .dependency-options > p { margin: 0; padding: 10px; color: var(--muted); font-size: 11px; }
  .dependency-loading, .dependency-load-error { min-height: 62px; display: flex; align-items: center; justify-content: center; gap: 8px; color: var(--muted); font-size: 11px; }
  .dependency-loading > span:first-child, .add-spinner { width: 13px; height: 13px; border: 2px solid var(--border); border-top-color: var(--purple); border-radius: 50%; animation: spin .7s linear infinite; }
  .dependency-load-error { flex-direction: column; color: var(--red); text-align: center; }
  .dependency-load-error button { min-height: 36px; padding: 0 12px; border: 1px solid var(--border); border-radius: 7px; color: var(--ink); background: var(--surface); }
  .add-spinner { position: absolute; top: 15px; right: 11px; }
  .dependency-note { margin: 0; color: var(--muted); font-size: 11px; }
  .add-error { margin-top: 2px; }
  .sr-only { position: absolute; width: 1px; height: 1px; overflow: hidden; clip: rect(0, 0, 0, 0); white-space: nowrap; clip-path: inset(50%); }
  @keyframes spin { to { transform: rotate(360deg); } }
  @media (max-width: 520px) {
    .relationship-grid { grid-template-columns: 1fr; }
    .dependency-panel { padding: 13px; }
  }
  @media (hover: none) {
    .relationship-link, .remove-relationship, .dependency-options button { min-height: 46px; }
  }
  @media (prefers-reduced-motion: reduce) {
    .dependency-loading > span:first-child, .add-spinner { animation: none; }
  }
</style>
