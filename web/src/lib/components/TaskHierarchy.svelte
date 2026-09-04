<script context="module" lang="ts">
  import type { Task as HierarchyTask, TaskHierarchy as HierarchyData, TaskHierarchyReference as HierarchyReference } from '../types';

  /** Keep parent candidates project-local and exclude every known descendant. */
  export function hierarchyCandidates(
    tasks: HierarchyTask[],
    currentTaskId: string,
    hierarchy: HierarchyData | null,
    query: string,
    limit = 20
  ): HierarchyTask[] {
    const normalized = query.trim().toLocaleLowerCase();
    if (!normalized) return [];
    const excluded = new Set<string>([
      currentTaskId,
      ...(hierarchy?.ancestors || []).map((item) => item.id),
      ...(hierarchy?.children || []).map((item) => item.id),
      ...(hierarchy?.descendants || []).map((item) => item.id)
    ]);
    return tasks
      .filter((candidate) => !excluded.has(candidate.id))
      .filter((candidate) => candidate.key.toLocaleLowerCase().includes(normalized)
        || candidate.title.toLocaleLowerCase().includes(normalized))
      .sort((left, right) => left.number - right.number || left.key.localeCompare(right.key))
      .slice(0, Math.max(1, limit));
  }

  export function nextHierarchyOptionIndex(current: number, key: string, count: number): number {
    if (count <= 0) return -1;
    if (key === 'Home') return 0;
    if (key === 'End') return count - 1;
    if (key === 'ArrowDown') return current < 0 ? 0 : (current + 1) % count;
    if (key === 'ArrowUp') return current < 0 ? count - 1 : (current - 1 + count) % count;
    return current;
  }

  export function hierarchyMutationMessage(error: { code?: string; message?: string } | null, fallback: string): string {
    switch (error?.code) {
      case 'stale_task': return 'This task changed elsewhere. The hierarchy was refreshed; review it and try again.';
      case 'hierarchy_cycle': return 'That parent would create a hierarchy cycle.';
      case 'hierarchy_self_reference': return 'A task cannot be its own parent.';
      case 'hierarchy_cross_project': return 'Parent and child tasks must belong to this project.';
      case 'hierarchy_already_exists': return 'That parent is already linked.';
      case 'hierarchy_limit_exceeded': return 'That parent has reached the direct child limit.';
      case 'hierarchy_depth_exceeded': return 'That relationship would exceed the hierarchy depth limit.';
      case 'hierarchy_in_use': return 'A parent with live children cannot be deleted.';
      case 'hierarchy_not_found': return 'That hierarchy relationship is no longer available.';
      case 'task_already_claimed': return 'Another actor currently owns this task claim.';
      default: return error?.message?.trim() || fallback;
    }
  }

  export function hierarchyReferenceTask(reference: HierarchyReference): HierarchyTask {
    return {
      id: reference.id,
      number: reference.number,
      key: reference.key,
      project_id: reference.project_id,
      title: reference.title,
      kind: reference.kind,
      column_id: reference.column_id,
      priority: 'normal',
      position: 0,
      version: reference.version,
      completed_at: reference.completed_at || undefined,
      agent_work: reference.agent_work
    };
  }
</script>

<script lang="ts">
  import { onDestroy, tick } from 'svelte';
  import { api, listAllTasks } from '../api';
  import { ApiError, type Task, type TaskHierarchy, type TaskHierarchyReference } from '../types';

  export let task: Task;
  /** Incremented when an event changes hierarchy or derived rollups. */
  export let refreshToken = 0;
  export let onTaskUpdated: (updated: Task) => void = () => undefined;
  export let onNavigate: (reference: TaskHierarchyReference) => void | Promise<void> = () => undefined;
  export let onRefreshTask: () => void | Promise<void> = () => undefined;
  export let onCreateChild: () => void | Promise<void> = () => undefined;

  let hierarchy: TaskHierarchy | null = null;
  let projectTasks: Task[] = [];
  let hierarchyLoading = false;
  let candidatesLoading = false;
  let hierarchyError = '';
  let addError = '';
  let parentRemoving = false;
  let pendingAdd = false;
  let query = '';
  let comboboxOpen = false;
  let activeIndex = -1;
  let searchInput: HTMLInputElement;
  let childrenHeading: HTMLHeadingElement;
  let announcement = '';
  let loadedTaskId = '';
  let loadedRefreshToken = -1;
  let loadedProjectId = '';
  let hierarchyRequest = 0;
  let candidateRequest = 0;
  let blurTimer: ReturnType<typeof setTimeout> | undefined;
  let destroyed = false;

  $: matches = hierarchyCandidates(projectTasks, task.id, hierarchy, query);
  $: if (activeIndex >= matches.length) activeIndex = matches.length ? matches.length - 1 : -1;
  $: activeOptionId = activeIndex >= 0 && matches[activeIndex]
    ? `hierarchy-option-${matches[activeIndex].id}`
    : undefined;
  $: mutationBusy = pendingAdd || parentRemoving;
  $: if (task?.id && (task.id !== loadedTaskId || refreshToken !== loadedRefreshToken)) {
    loadedTaskId = task.id;
    loadedRefreshToken = refreshToken;
    void loadHierarchy(task.id);
  }
  $: if (task?.project_id && task.project_id !== loadedProjectId) {
    loadedProjectId = task.project_id;
    void loadCandidates(task.project_id);
  }

  onDestroy(() => {
    destroyed = true;
    hierarchyRequest += 1;
    candidateRequest += 1;
    if (blurTimer) clearTimeout(blurTimer);
  });

  async function loadHierarchy(taskId: string): Promise<boolean> {
    const requestId = ++hierarchyRequest;
    hierarchyLoading = true;
    hierarchyError = '';
    try {
      const loaded = await api.getTaskHierarchy(taskId);
      if (destroyed || requestId !== hierarchyRequest || task.id !== taskId) return false;
      hierarchy = {
        parent: loaded.parent || null,
        children: loaded.children || [],
        ancestors: loaded.ancestors || [],
        descendants: loaded.descendants || [],
        summary: loaded.summary || {
          child_count: 0,
          completed_child_count: 0,
          completion_percent: 0,
          state_counts: {},
          blocked_child_count: 0,
          live_agent_work_count: 0,
          action_needed_count: 0,
          stale_agent_work_count: 0
        }
      };
      return true;
    } catch (error) {
      if (!destroyed && requestId === hierarchyRequest && task.id === taskId) {
        hierarchyError = hierarchyMutationMessage(error instanceof ApiError ? error : null, 'Hierarchy could not be loaded.');
      }
      return false;
    } finally {
      if (!destroyed && requestId === hierarchyRequest) hierarchyLoading = false;
    }
  }

  export async function refreshRelationships(): Promise<boolean> {
    return loadHierarchy(task.id);
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
    await loadHierarchy(taskId);
  }

  async function setParent(candidate: Task): Promise<void> {
    if (mutationBusy || task.id === candidate.id) return;
    const taskId = task.id;
    pendingAdd = true;
    addError = '';
    try {
      const updated = await api.setTaskParent(taskId, candidate.id, task.version);
      if (destroyed || task.id !== taskId) return;
      onTaskUpdated(updated);
      await loadHierarchy(taskId);
      query = '';
      activeIndex = -1;
      comboboxOpen = false;
      announcement = `${candidate.key} is now this task’s parent.`;
      pendingAdd = false;
      await tick();
      searchInput?.focus();
    } catch (error) {
      if (destroyed || task.id !== taskId) return;
      addError = hierarchyMutationMessage(error instanceof ApiError ? error : null, 'The parent could not be linked.');
      announcement = addError;
      await recoverAfterMutationError(error, taskId);
    } finally {
      if (!destroyed && task.id === taskId) pendingAdd = false;
    }
  }

  async function removeParent(): Promise<void> {
    if (mutationBusy || !hierarchy?.parent) return;
    const taskId = task.id;
    parentRemoving = true;
    addError = '';
    try {
      const updated = await api.clearTaskParent(taskId, task.version);
      if (destroyed || task.id !== taskId) return;
      onTaskUpdated(updated);
      await loadHierarchy(taskId);
      announcement = 'Parent relationship removed.';
      await tick();
      searchInput?.focus();
    } catch (error) {
      if (destroyed || task.id !== taskId) return;
      addError = hierarchyMutationMessage(error instanceof ApiError ? error : null, 'The parent could not be removed.');
      announcement = addError;
      await recoverAfterMutationError(error, taskId);
    } finally {
      if (!destroyed && task.id === taskId) parentRemoving = false;
    }
  }

  async function removeChild(reference: TaskHierarchyReference): Promise<void> {
    if (mutationBusy) return;
    const taskId = task.id;
    parentRemoving = true;
    addError = '';
    try {
      const updated = await api.removeTaskChild(taskId, reference.id, reference.version);
      if (destroyed || task.id !== taskId) return;
      onTaskUpdated(updated);
      await loadHierarchy(taskId);
      announcement = `${reference.key} removed from this task.`;
      parentRemoving = false;
      await tick();
      childrenHeading?.focus();
    } catch (error) {
      if (destroyed || task.id !== taskId) return;
      addError = hierarchyMutationMessage(error instanceof ApiError ? error : null, 'The child could not be removed.');
      announcement = addError;
      await recoverAfterMutationError(error, taskId);
    } finally {
      if (!destroyed && task.id === taskId) parentRemoving = false;
    }
  }

  function handleComboboxKeydown(event: KeyboardEvent) {
    if (['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) {
      event.preventDefault();
      comboboxOpen = true;
      activeIndex = nextHierarchyOptionIndex(activeIndex, event.key, matches.length);
      return;
    }
    if (event.key === 'Enter' && activeIndex >= 0 && matches[activeIndex]) {
      event.preventDefault();
      void setParent(matches[activeIndex]);
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

  function referenceLabel(reference: TaskHierarchyReference | null | undefined): string {
    return reference ? `${reference.key}: ${reference.title}` : 'parent task';
  }
</script>

<section class="hierarchy-panel" aria-labelledby="task-hierarchy-heading" aria-busy={hierarchyLoading}>
  <div class="hierarchy-heading">
    <div>
      <h2 id="task-hierarchy-heading">Hierarchy</h2>
      <p>Organize related work under one project-local parent.</p>
    </div>
    {#if hierarchy?.summary.child_count}
      <span class="hierarchy-progress" aria-label={`${Math.round(hierarchy.summary.completion_percent)} percent of children complete`}>
        {Math.round(hierarchy.summary.completion_percent)}% complete
      </span>
    {/if}
  </div>

  <p class="sr-only" aria-live="polite" aria-atomic="true">{announcement}</p>

  {#if hierarchyLoading && !hierarchy}
    <div class="hierarchy-loading" role="status"><span></span><span>Loading hierarchy…</span></div>
  {:else if hierarchyError && !hierarchy}
    <div class="hierarchy-load-error" role="alert">
      <span>{hierarchyError}</span>
      <button type="button" on:click={() => loadHierarchy(task.id)}>Retry</button>
    </div>
  {:else}
    <div class="hierarchy-sections">
      <section aria-labelledby="task-parent-heading">
        <h3 id="task-parent-heading">Parent</h3>
        {#if hierarchy?.parent}
          <div class="hierarchy-row">
            <button class="hierarchy-link" type="button" aria-label={`Open ${referenceLabel(hierarchy?.parent)}`} on:click={() => hierarchy?.parent && onNavigate(hierarchy.parent)}>
              <span class="hierarchy-key">{hierarchy?.parent?.key}</span>
              <span class="hierarchy-title">{hierarchy?.parent?.title}</span>
            </button>
            <button class="remove-hierarchy" type="button" disabled={mutationBusy} aria-label="Remove parent relationship" on:click={removeParent}>{parentRemoving ? '…' : '×'}</button>
          </div>
        {:else}
          <p class="hierarchy-empty">No parent linked.</p>
        {/if}
      </section>

      <section aria-labelledby="task-children-heading">
        <div class="hierarchy-subheading">
          <h3 bind:this={childrenHeading} id="task-children-heading" tabindex="-1">Children <span>{hierarchy?.children.length || 0}</span></h3>
          <button class="create-child" type="button" on:click={() => onCreateChild()}>Create child</button>
        </div>
        {#if hierarchy?.children.length}
          <ul class="hierarchy-list">
            {#each hierarchy.children as reference (reference.id)}
              <li>
                <button class="hierarchy-link" type="button" aria-label={`Open ${referenceLabel(reference)}`} on:click={() => onNavigate(reference)}>
                  <span class="hierarchy-key">{reference.key}</span>
                  <span class="hierarchy-title">{reference.title}</span>
                  <span class:done={reference.semantic_state === 'completed'} class="hierarchy-state">{reference.semantic_state}</span>
                </button>
                <button class="remove-hierarchy" type="button" disabled={mutationBusy} aria-label={`Remove ${reference.key} as a child`} on:click={() => removeChild(reference)}>{parentRemoving ? '…' : '×'}</button>
              </li>
            {/each}
          </ul>
        {:else}
          <p class="hierarchy-empty">No children yet.</p>
        {/if}
      </section>

      {#if hierarchy?.ancestors.length}
        <section aria-labelledby="task-ancestors-heading">
          <h3 id="task-ancestors-heading">Ancestors <span>{hierarchy.ancestors.length}</span></h3>
          <ul class="hierarchy-list compact-list">
            {#each hierarchy.ancestors as reference (reference.id)}
              <li><button class="hierarchy-link" type="button" aria-label={`Open ${referenceLabel(reference)}`} on:click={() => onNavigate(reference)}><span class="hierarchy-key">{reference.key}</span><span class="hierarchy-title">{reference.title}</span></button></li>
            {/each}
          </ul>
        </section>
      {/if}
    </div>

    <form class="hierarchy-add" on:submit|preventDefault={() => activeIndex >= 0 && matches[activeIndex] && setParent(matches[activeIndex])}>
      <label for="hierarchy-search">Link parent</label>
      <div class="combobox-wrap">
        <input
          bind:this={searchInput}
          id="hierarchy-search"
          role="combobox"
          aria-autocomplete="list"
          aria-expanded={comboboxOpen && Boolean(query.trim())}
          aria-controls="hierarchy-options"
          aria-activedescendant={activeOptionId}
          autocomplete="off"
          placeholder="Search this project by key or title"
          disabled={mutationBusy || candidatesLoading}
          bind:value={query}
          on:focus={() => comboboxOpen = true}
          on:input={() => { comboboxOpen = true; activeIndex = matches.length ? 0 : -1; addError = ''; }}
          on:keydown={handleComboboxKeydown}
          on:blur={deferComboboxClose}
        />
        {#if comboboxOpen && query.trim()}
          <div id="hierarchy-options" class="hierarchy-options" role="listbox" aria-label="Project tasks">
            {#if matches.length}
              {#each matches as candidate, index (candidate.id)}
                <button id={`hierarchy-option-${candidate.id}`} class:active={index === activeIndex} type="button" role="option" aria-selected={index === activeIndex} tabindex="-1" on:mouseenter={() => activeIndex = index} on:mousedown|preventDefault on:click={() => setParent(candidate)}>
                  <span>{candidate.key}</span><strong>{candidate.title}</strong>
                </button>
              {/each}
            {:else}
              <p>No available tasks match “{query.trim()}”.</p>
            {/if}
          </div>
        {/if}
      </div>
      <small>{candidatesLoading ? 'Loading project tasks…' : 'Current task and descendants are excluded.'}</small>
      {#if addError}<p class="relationship-error" role="alert">{addError}</p>{/if}
    </form>

    {#if hierarchy?.summary.child_count}
      <dl class="hierarchy-summary" aria-label="Child rollup">
        <div><dt>Children complete</dt><dd>{hierarchy.summary.completed_child_count}/{hierarchy.summary.child_count}</dd></div>
        <div><dt>Blocked</dt><dd>{hierarchy.summary.blocked_child_count}</dd></div>
        <div><dt>Live work</dt><dd>{hierarchy.summary.live_agent_work_count}</dd></div>
        <div><dt>Action needed</dt><dd>{hierarchy.summary.action_needed_count}</dd></div>
      </dl>
    {/if}
  {/if}
</section>

<style>
  .hierarchy-panel { display: grid; gap: 14px; padding: 15px; border: 1px solid var(--border); border-radius: 10px; background: var(--surface-raised); }
  .hierarchy-heading, .hierarchy-subheading { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px; }
  .hierarchy-heading h2 { margin: 0; color: var(--ink); font-size: 12px; }
  .hierarchy-heading p { margin: 4px 0 0; color: var(--muted); font-size: 10px; line-height: 1.45; }
  .hierarchy-progress { flex: 0 0 auto; padding: 4px 7px; border-radius: 6px; color: var(--green); background: var(--green-soft); font-size: 9px; font-weight: 800; }
  .hierarchy-sections { display: grid; gap: 12px; }
  .hierarchy-sections section { min-width: 0; }
  .hierarchy-sections h3 { display: flex; align-items: center; gap: 5px; margin: 0 0 7px; color: var(--ink-soft); font-size: 10px; }
  .hierarchy-sections h3 span { min-width: 18px; padding: 2px 5px; border-radius: 999px; color: var(--muted); background: var(--surface-muted); font-size: 8px; text-align: center; }
  .hierarchy-list { display: grid; gap: 6px; margin: 0; padding: 0; list-style: none; }
  .hierarchy-list li, .hierarchy-row { display: grid; grid-template-columns: minmax(0, 1fr) 34px; gap: 4px; min-width: 0; }
  .compact-list li { grid-template-columns: minmax(0, 1fr); }
  .hierarchy-link { min-width: 0; min-height: 44px; display: grid; grid-template-columns: auto minmax(0, 1fr) auto; align-items: center; gap: 7px; padding: 7px 8px; border: 1px solid var(--border); border-radius: 8px; color: var(--ink); background: var(--surface); text-align: left; }
  .hierarchy-link:hover, .hierarchy-link:focus-visible { border-color: var(--purple); outline: none; }
  .hierarchy-key { color: var(--purple); font: 800 8px ui-monospace, SFMono-Regular, Menlo, monospace; }
  .hierarchy-title { min-width: 0; overflow: hidden; font-size: 9px; font-weight: 650; text-overflow: ellipsis; white-space: nowrap; }
  .hierarchy-state { padding: 2px 5px; border-radius: 5px; color: var(--amber); background: var(--amber-soft); font-size: 7px; font-weight: 800; }
  .hierarchy-state.done { color: var(--green); background: var(--green-soft); }
  .remove-hierarchy { min-width: 34px; min-height: 44px; border: 1px solid var(--border); border-radius: 8px; color: var(--muted); background: var(--surface); font-size: 16px; }
  .remove-hierarchy:hover:not(:disabled), .remove-hierarchy:focus-visible { color: var(--red); border-color: var(--red); outline: none; }
  .remove-hierarchy:disabled { cursor: wait; opacity: .55; }
  .create-child { min-height: 32px; padding: 0 9px; border: 1px solid var(--border); border-radius: 7px; color: var(--purple); background: var(--surface); font-size: 9px; font-weight: 750; }
  .create-child:hover, .create-child:focus-visible { border-color: var(--purple); outline: none; }
  .hierarchy-empty { min-height: 44px; display: flex; align-items: center; margin: 0; padding: 8px; border: 1px dashed var(--border); border-radius: 8px; color: var(--faint); font-size: 9px; }
  .hierarchy-add { display: grid; gap: 5px; padding-top: 12px; border-top: 1px solid var(--border); }
  .hierarchy-add > label { color: var(--ink-soft); font-size: 10px; font-weight: 700; }
  .hierarchy-add > small { color: var(--faint); font-size: 8px; }
  .combobox-wrap { position: relative; }
  .combobox-wrap > input { width: 100%; min-height: 44px; padding-right: 34px; }
  .hierarchy-options { position: absolute; top: calc(100% + 4px); right: 0; left: 0; z-index: 8; max-height: 225px; overflow: auto; padding: 5px; border: 1px solid var(--border-strong); border-radius: 9px; background: var(--surface-raised); box-shadow: var(--shadow-md); }
  .hierarchy-options button { width: 100%; min-height: 44px; display: grid; grid-template-columns: auto minmax(0, 1fr); align-items: center; gap: 8px; padding: 7px 8px; border: 0; border-radius: 6px; color: var(--ink); background: transparent; text-align: left; }
  .hierarchy-options button:hover, .hierarchy-options button.active { background: var(--purple-soft); }
  .hierarchy-options button span { color: var(--purple); font: 800 8px ui-monospace, SFMono-Regular, Menlo, monospace; }
  .hierarchy-options button strong { min-width: 0; overflow: hidden; font-size: 9px; text-overflow: ellipsis; white-space: nowrap; }
  .hierarchy-options > p { margin: 0; padding: 10px; color: var(--muted); font-size: 9px; }
  .hierarchy-loading, .hierarchy-load-error { min-height: 62px; display: flex; align-items: center; justify-content: center; gap: 8px; color: var(--muted); font-size: 10px; }
  .hierarchy-loading > span:first-child { width: 13px; height: 13px; border: 2px solid var(--border); border-top-color: var(--purple); border-radius: 50%; animation: hierarchy-spin .7s linear infinite; }
  .hierarchy-load-error { flex-direction: column; color: var(--red); text-align: center; }
  .hierarchy-load-error button { min-height: 36px; padding: 0 12px; border: 1px solid var(--border); border-radius: 7px; color: var(--ink); background: var(--surface); }
  .relationship-error { margin: 0; color: var(--red); font-size: 9px; line-height: 1.4; }
  .hierarchy-summary { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 6px; margin: 0; }
  .hierarchy-summary div { padding: 7px; border: 1px solid var(--border); border-radius: 7px; background: var(--surface); }
  .hierarchy-summary dt { color: var(--muted); font-size: 8px; }
  .hierarchy-summary dd { margin: 3px 0 0; color: var(--ink); font-size: 11px; font-weight: 800; }
  .sr-only { position: absolute; width: 1px; height: 1px; overflow: hidden; clip: rect(0, 0, 0, 0); white-space: nowrap; clip-path: inset(50%); }
  @keyframes hierarchy-spin { to { transform: rotate(360deg); } }
  @media (max-width: 520px) { .hierarchy-panel { padding: 13px; } .hierarchy-summary { grid-template-columns: repeat(2, minmax(0, 1fr)); } }
  @media (hover: none) { .hierarchy-link, .remove-hierarchy, .hierarchy-options button { min-height: 46px; } }
  @media (prefers-reduced-motion: reduce) { .hierarchy-loading > span:first-child { animation: none; } }
</style>
