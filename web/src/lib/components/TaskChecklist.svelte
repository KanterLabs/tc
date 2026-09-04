<script context="module" lang="ts">
  import type { TaskChecklistItem as ChecklistItem } from '../types';

  export function checklistPercent(completed: number, total: number): number {
    if (total <= 0) return 0;
    return Math.round((completed / total) * 100);
  }

  export function checklistMove(items: ChecklistItem[], itemId: string, offset: -1 | 1): string[] {
    const ids = items.map((item) => item.id);
    const index = ids.indexOf(itemId);
    const target = index + offset;
    if (index < 0 || target < 0 || target >= ids.length) return ids;
    [ids[index], ids[target]] = [ids[target], ids[index]];
    return ids;
  }
</script>

<script lang="ts">
  import { tick } from 'svelte';
  import { api } from '../api';
  import { ApiError, type Task, type TaskChecklistItem } from '../types';

  export let task: Task;
  export let onTaskUpdated: (updated: Task) => void = () => undefined;
  export let onRefreshTask: () => void | Promise<void> = () => undefined;

  let newText = '';
  let adding = false;
  let pending: Record<string, boolean> = {};
  let errors: Record<string, string> = {};
  let draftTexts: Record<string, string> = {};
  let syncedTaskID = '';
  let syncedVersion = 0;
  let announcement = '';
  let destroyed = false;

  $: items = task?.checklist || [];
  $: summary = task?.checklist_summary || {
    total: items.length,
    completed: items.filter((item) => item.completed).length,
    open: items.filter((item) => !item.completed).length,
    percent: checklistPercent(items.filter((item) => item.completed).length, items.length),
    completion_policy: 'warn' as const,
    warning: false
  };
  $: if (task?.id && (task.id !== syncedTaskID || task.version !== syncedVersion)) {
    syncedTaskID = task.id;
    syncedVersion = task.version;
    draftTexts = Object.fromEntries(items.map((item) => [item.id, item.text]));
  }
  $: busy = adding || Object.values(pending).some(Boolean);

  function errorMessage(error: unknown, fallback: string): string {
    if (error instanceof ApiError && error.code === 'stale_task') return 'This task changed elsewhere. Refreshing the checklist…';
    if (error instanceof ApiError && error.code === 'checklist_limit_exceeded') return 'The checklist is at its size limit. Remove an item or shorten its text.';
    if (error instanceof Error && error.message.trim()) return error.message;
    return fallback;
  }

  function setPending(key: string, value: boolean) {
    const next = { ...pending };
    if (value) next[key] = true;
    else delete next[key];
    pending = next;
  }

  function setError(key: string, value: string) {
    const next = { ...errors };
    if (value) next[key] = value;
    else delete next[key];
    errors = next;
  }

  async function recover(error: unknown): Promise<void> {
    if (error instanceof ApiError && error.code === 'stale_task') {
      await onRefreshTask();
    }
  }

  async function addItem(): Promise<void> {
    const text = newText.trim();
    if (!text || busy) return;
    const taskID = task.id;
    adding = true;
    setError('new', '');
    try {
      const updated = await api.addTaskChecklistItem(taskID, { text }, task.version);
      if (destroyed || task.id !== taskID) return;
      onTaskUpdated(updated);
      newText = '';
      announcement = `Added checklist item: ${text}`;
      await tick();
      document.getElementById('checklist-new-item')?.focus();
    } catch (error) {
      if (!destroyed && task.id === taskID) {
        const message = errorMessage(error, 'The checklist item could not be added.');
        setError('new', message);
        announcement = message;
        await recover(error);
      }
    } finally {
      if (!destroyed && task.id === taskID) adding = false;
    }
  }

  async function updateItem(item: TaskChecklistItem, input: { text?: string; completed?: boolean }): Promise<void> {
    if (busy) return;
    const taskID = task.id;
    setPending(item.id, true);
    setError(item.id, '');
    try {
      const updated = await api.updateTaskChecklistItem(taskID, item.id, input, task.version);
      if (destroyed || task.id !== taskID) return;
      onTaskUpdated(updated);
      announcement = input.completed === undefined ? `Updated checklist item: ${item.text}` : input.completed ? `Completed checklist item: ${item.text}` : `Reopened checklist item: ${item.text}`;
    } catch (error) {
      if (!destroyed && task.id === taskID) {
        const message = errorMessage(error, 'The checklist item could not be updated.');
        setError(item.id, message);
        announcement = message;
        await recover(error);
      }
    } finally {
      if (!destroyed && task.id === taskID) setPending(item.id, false);
    }
  }

  async function saveText(item: TaskChecklistItem): Promise<void> {
    const text = (draftTexts[item.id] ?? item.text).trim();
    if (!text || text === item.text) return;
    await updateItem(item, { text });
  }

  async function removeItem(item: TaskChecklistItem): Promise<void> {
    if (busy) return;
    const taskID = task.id;
    setPending(item.id, true);
    setError(item.id, '');
    try {
      const updated = await api.deleteTaskChecklistItem(taskID, item.id, task.version);
      if (destroyed || task.id !== taskID) return;
      onTaskUpdated(updated);
      announcement = `Removed checklist item: ${item.text}`;
    } catch (error) {
      if (!destroyed && task.id === taskID) {
        const message = errorMessage(error, 'The checklist item could not be removed.');
        setError(item.id, message);
        announcement = message;
        await recover(error);
      }
    } finally {
      if (!destroyed && task.id === taskID) setPending(item.id, false);
    }
  }

  async function moveItem(item: TaskChecklistItem, offset: -1 | 1): Promise<void> {
    if (busy) return;
    const order = checklistMove(items, item.id, offset);
    if (order.join(',') === items.map((entry) => entry.id).join(',')) return;
    const taskID = task.id;
    setPending(item.id, true);
    setError(item.id, '');
    try {
      const updated = await api.reorderTaskChecklist(taskID, order, task.version);
      if (destroyed || task.id !== taskID) return;
      onTaskUpdated(updated);
      announcement = `Moved checklist item: ${item.text}`;
    } catch (error) {
      if (!destroyed && task.id === taskID) {
        const message = errorMessage(error, 'The checklist could not be reordered.');
        setError(item.id, message);
        announcement = message;
        await recover(error);
      }
    } finally {
      if (!destroyed && task.id === taskID) setPending(item.id, false);
    }
  }

  function handleTextKeydown(event: KeyboardEvent, item: TaskChecklistItem): void {
    if (event.key === 'Enter') {
      event.preventDefault();
      void saveText(item);
    }
  }
</script>

<section class="checklist-panel" aria-labelledby="task-checklist-heading" aria-busy={busy}>
  <div class="checklist-heading">
    <div>
      <h2 id="task-checklist-heading">Checklist</h2>
      <p id="checklist-help">Acceptance criteria for this task. Changes are saved as you make them.</p>
    </div>
    <span class="checklist-count" aria-label={`${summary.completed} of ${summary.total} checklist items complete`}>{summary.completed}/{summary.total}</span>
  </div>

  <progress max={Math.max(summary.total, 1)} value={summary.completed} aria-label={`Checklist progress: ${summary.completed} of ${summary.total} complete`}></progress>
  {#if summary.warning}
    <p class="checklist-warning" role="alert">This task is complete with {summary.open} open checklist item{summary.open === 1 ? '' : 's'}. The project policy allows a warning.</p>
  {/if}
  <p class="sr-only" aria-live="polite" aria-atomic="true">{announcement}</p>

  <form class="checklist-add" on:submit|preventDefault={addItem}>
    <label for="checklist-new-item">Add acceptance criterion</label>
    <div>
      <input id="checklist-new-item" bind:value={newText} maxlength="1000" placeholder="What must be true when this is done?" aria-describedby="checklist-help" disabled={busy} />
      <button class="button primary compact-button" type="submit" disabled={busy || !newText.trim()}>Add</button>
    </div>
    {#if errors.new}<p class="checklist-error" role="alert">{errors.new}</p>{/if}
  </form>

  {#if items.length}
    <ol class="checklist-items">
      {#each items as item, index (item.id)}
        <li class:completed={item.completed}>
          <input class="checklist-checkbox" type="checkbox" checked={item.completed} aria-label={`${item.completed ? 'Reopen' : 'Complete'} checklist item: ${item.text}`} disabled={busy || pending[item.id]} on:change={(event) => updateItem(item, { completed: (event.currentTarget as HTMLInputElement).checked })} />
          <input class="checklist-text" value={draftTexts[item.id] ?? item.text} maxlength="1000" aria-label={`Edit checklist item: ${item.text}`} disabled={busy || pending[item.id]} on:input={(event) => { draftTexts = { ...draftTexts, [item.id]: (event.currentTarget as HTMLInputElement).value }; }} on:keydown={(event) => handleTextKeydown(event, item)} />
          <button class="text-button checklist-save" type="button" disabled={busy || pending[item.id] || !(draftTexts[item.id] ?? item.text).trim() || (draftTexts[item.id] ?? item.text).trim() === item.text} on:click={() => saveText(item)}>Save</button>
          <div class="checklist-order" role="group" aria-label={`Reorder checklist item: ${item.text}`}>
            <button class="icon-button tiny" type="button" aria-label={`Move checklist item up: ${item.text}`} disabled={busy || pending[item.id] || index === 0} on:click={() => moveItem(item, -1)}>↑</button>
            <button class="icon-button tiny" type="button" aria-label={`Move checklist item down: ${item.text}`} disabled={busy || pending[item.id] || index === items.length - 1} on:click={() => moveItem(item, 1)}>↓</button>
          </div>
          <button class="icon-button tiny danger-button" type="button" aria-label={`Remove checklist item: ${item.text}`} disabled={busy || pending[item.id]} on:click={() => removeItem(item)}>×</button>
          {#if errors[item.id]}<p class="checklist-error" role="alert">{errors[item.id]}</p>{/if}
        </li>
      {/each}
    </ol>
  {:else}
    <p class="checklist-empty">No acceptance criteria yet. Add the first one above.</p>
  {/if}
</section>

<style>
  .checklist-panel { display: grid; gap: 12px; margin-top: 22px; padding: 15px; border: 1px solid var(--border); border-radius: 10px; background: var(--surface-raised); }
  .checklist-heading { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px; }
  .checklist-heading h2 { margin: 0; color: var(--ink); font: 700 13px var(--font-display); letter-spacing: -.02em; }
  .checklist-heading p { margin: 4px 0 0; color: var(--muted); font-size: 11px; line-height: 1.45; }
  .checklist-count { flex: 0 0 auto; padding: 4px 7px; border-radius: 999px; color: var(--purple); background: var(--purple-soft); font-size: 11px; font-weight: 800; }
  progress { width: 100%; height: 7px; overflow: hidden; border: 0; border-radius: 999px; accent-color: var(--purple); }
  progress::-webkit-progress-bar { background: var(--surface-muted); border-radius: 999px; }
  progress::-webkit-progress-value { background: var(--purple); border-radius: 999px; }
  .checklist-warning { margin: 0; padding: 8px; border-radius: 7px; color: var(--amber); background: var(--amber-soft); font-size: 11px; line-height: 1.4; }
  .checklist-add { display: grid; gap: 6px; padding-top: 11px; border-top: 1px solid var(--border); }
  .checklist-add > label { color: var(--ink-soft); font-size: 11px; font-weight: 700; }
  .checklist-add > div { display: flex; gap: 7px; }
  .checklist-add input { min-width: 0; flex: 1; }
  .checklist-items { display: grid; gap: 7px; margin: 0; padding: 0; list-style: none; }
  .checklist-items li { display: grid; grid-template-columns: 26px minmax(0, 1fr) auto auto 30px; align-items: center; gap: 6px; min-width: 0; }
  .checklist-checkbox { width: 24px; height: 24px; margin: 0; accent-color: var(--purple); }
  .checklist-text { min-width: 0; width: 100%; }
  .checklist-items li.completed .checklist-text { color: var(--muted); text-decoration: line-through; }
  .checklist-save { font-size: 11px; }
  .checklist-order { display: flex; gap: 2px; }
  .checklist-order .icon-button { width: 27px; height: 30px; font-size: 12px; }
  .checklist-error { grid-column: 2 / -1; margin: 0; color: var(--red); font-size: 11px; line-height: 1.35; }
  .checklist-empty { margin: 0; padding: 9px; border: 1px dashed var(--border); border-radius: 8px; color: var(--faint); font-size: 11px; }
  @media (max-width: 620px) {
    .checklist-items li { grid-template-columns: 26px minmax(0, 1fr) auto 30px; }
    .checklist-save { grid-column: 2; justify-self: start; }
    .checklist-order { grid-column: 3; grid-row: 1; }
    .checklist-items li > .danger-button { grid-column: 4; grid-row: 1; }
  }
</style>
