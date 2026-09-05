<script lang="ts">
  import { boardCardHeight } from '../boardLayout';
  import type { OfflineBoard } from '../offlineBoards';

  export let boards: OfflineBoard[] = [];
  export let reconnect: () => void = () => undefined;
  export let reconnecting = false;
  export let error = '';
  export let clear: () => void = () => undefined;

  let selectedSlug = '';
  let search = '';
  let activeBoard: OfflineBoard | null = null;
  let sortedColumns: OfflineBoard['columns'] = [];
  let visibleTasks: OfflineBoard['tasks'] = [];
  let unassignedTasks: OfflineBoard['tasks'] = [];

  $: activeBoard = boards.find((board) => board.project.slug === selectedSlug) || boards[0] || null;
  $: sortedColumns = activeBoard
    ? [...activeBoard.columns].sort((left, right) => left.position - right.position)
    : [];
  $: visibleTasks = activeBoard ? activeBoard.tasks.filter((task) => matchesSearch(task, search)) : [];
  $: unassignedTasks = visibleTasks.filter((task) => !sortedColumns.some((column) => column.id === task.column_id));

  const safeBoardCardHeight = (node: HTMLElement) => {
    // jsdom and older embedded web views may not expose ResizeObserver. The
    // CSS fallback still gives those environments a useful ten-card viewport.
    if (typeof ResizeObserver === 'undefined') return {};
    return boardCardHeight(node);
  };

  function matchesSearch(task: OfflineBoard['tasks'][number], value: string): boolean {
    const needle = value.trim().toLocaleLowerCase();
    if (!needle) return true;
    return [task.key, task.title, task.description].some((part) => part.toLocaleLowerCase().includes(needle));
  }

  function tasksForColumn(columnId: string): OfflineBoard['tasks'] {
    return visibleTasks.filter((task) => task.column_id === columnId);
  }

  function priorityLabel(priority: OfflineBoard['tasks'][number]['priority']): string {
    const labels: Record<string, string> = {
      urgent: 'Urgent',
      high: 'High',
      normal: 'Normal',
      low: 'Low'
    };
    return labels[String(priority)] || 'Priority not recorded';
  }

  function priorityClass(priority: OfflineBoard['tasks'][number]['priority']): string {
    return ['urgent', 'high', 'normal', 'low'].includes(String(priority)) ? String(priority) : 'unknown';
  }

  function savedAtIso(value: number): string {
    const date = new Date(value);
    return Number.isNaN(date.getTime()) ? '' : date.toISOString();
  }

  function savedAtLabel(value: number): string {
    const date = new Date(value);
    if (Number.isNaN(date.getTime())) return 'Unknown time';
    return new Intl.DateTimeFormat(undefined, {
      dateStyle: 'medium',
      timeStyle: 'short'
    }).format(date);
  }

  function columnHeadingId(index: number): string {
    return `offline-column-heading-${index}`;
  }

  function selectBoard(event: Event): void {
    selectedSlug = (event.currentTarget as HTMLSelectElement).value;
  }

  function confirmClear(): void {
    if (typeof window === 'undefined') return;
    if (window.confirm('Clear all saved offline boards from this device? This cannot be undone.')) clear();
  }

  function reconnectLabel(): string {
    return reconnecting ? 'Reconnecting…' : 'Reconnect';
  }
</script>

<main class="offline-shell offline-page" data-testid="offline-board-view" aria-label="Helm offline read-only viewer">
  <header class="offline-header">
    <div class="offline-brand-lockup">
      <span class="offline-brand-mark" aria-hidden="true">H</span>
      <div class="offline-brand-copy">
        <strong>Helm</strong>
        <span>Offline</span>
      </div>
    </div>
    <div class="offline-header-actions">
      <span class="offline-readonly-badge">Read-only</span>
      <button
        class="offline-button offline-reconnect"
        data-testid="offline-reconnect"
        type="button"
        disabled={reconnecting}
        aria-busy={reconnecting}
        on:click={reconnect}
      >
        {#if reconnecting}<span class="offline-spinner" aria-hidden="true"></span>{/if}
        <span>{reconnectLabel()}</span>
      </button>
    </div>
  </header>

  <div class="offline-content">
    {#if error}
      <div class="offline-alert offline-alert-error" data-testid="offline-error" role="alert" aria-live="assertive">
        <span class="offline-alert-icon" aria-hidden="true">!</span>
        <p>{error}</p>
      </div>
    {/if}

    <section class="offline-intro" aria-labelledby="offline-heading">
      <div>
        <span class="offline-eyebrow">Workspace snapshot</span>
        <h1 id="offline-heading">Your boards, available offline</h1>
        <p>Browse a read-only copy of the pages you loaded before the connection dropped.</p>
      </div>
      {#if boards.length}
        <button class="offline-button offline-clear" data-testid="offline-clear" type="button" on:click={confirmClear}>Clear saved boards</button>
      {/if}
    </section>

    <aside class="offline-privacy" role="note">
      <span class="offline-privacy-icon" aria-hidden="true">⌂</span>
      <p><strong>Private content stays on this device.</strong> This viewer is read-only and does not send saved board data anywhere while you are offline.</p>
    </aside>

    {#if !boards.length}
      <section class="offline-empty" data-testid="offline-empty" aria-labelledby="offline-empty-heading">
        <span class="offline-empty-icon" aria-hidden="true">◌</span>
        <h2 id="offline-empty-heading">No saved boards yet</h2>
        <p>There is no board snapshot on this device. Reconnect to Helm to load your workspace; boards you open while online can be available here the next time you are offline.</p>
        <button class="offline-button offline-empty-reconnect" type="button" disabled={reconnecting} aria-busy={reconnecting} on:click={reconnect}>
          {#if reconnecting}<span class="offline-spinner" aria-hidden="true"></span>{/if}
          <span>{reconnecting ? 'Reconnecting…' : 'Reconnect to Helm'}</span>
        </button>
      </section>
    {:else if activeBoard}
      <section class="offline-board-controls" aria-label="Offline board controls">
        <div class="offline-project-picker">
          <label for="offline-project-picker">Saved project</label>
          <select id="offline-project-picker" data-testid="offline-project-picker" value={activeBoard.project.slug} aria-label="Choose a saved project" on:change={selectBoard}>
            {#each boards as board (board.project.slug)}
              <option value={board.project.slug}>{board.project.name} · {board.project.key}</option>
            {/each}
          </select>
        </div>
        <label class="offline-search" for="offline-task-search">
          <span aria-hidden="true">⌕</span>
          <span class="sr-only">Search saved tasks</span>
          <input id="offline-task-search" data-testid="offline-search" type="search" bind:value={search} placeholder="Search saved tasks…" autocomplete="off" />
        </label>
      </section>

      <section class="offline-snapshot-meta" aria-label="Snapshot details">
        <div class="offline-snapshot-copy">
          <strong>{activeBoard.project.name}</strong>
          <span>{visibleTasks.length} of {activeBoard.tasks.length} saved {activeBoard.tasks.length === 1 ? 'task' : 'tasks'} shown</span>
        </div>
        <div class="offline-snapshot-time" data-testid="offline-saved-at">
          <span>Saved</span>
          <time datetime={savedAtIso(activeBoard.savedAt)}>{savedAtLabel(activeBoard.savedAt)}</time>
        </div>
      </section>

      {#if activeBoard.partial}
        <div class="offline-alert offline-alert-warning" data-testid="offline-partial-warning" role="status">
          <span class="offline-alert-icon" aria-hidden="true">!</span>
          <p><strong>Partial snapshot.</strong> Only previously loaded or filtered tasks were saved; some tasks/details may be missing.</p>
        </div>
      {/if}

      <p class="offline-scope-note">Offline data is limited to previously loaded pages, not the full project. Search and project switching happen locally on this device.</p>

      {#if sortedColumns.length}
        <section class="offline-board-columns" use:safeBoardCardHeight aria-label={`${activeBoard.project.name} offline board`}>
          {#each sortedColumns as column, index (column.id)}
            {@const columnTasks = tasksForColumn(column.id)}
            <article class="offline-column board-column" aria-labelledby={columnHeadingId(index)}>
              <header class="offline-column-header">
                <div class="offline-column-title">
                  <span class="offline-column-dot" aria-hidden="true"></span>
                  <h2 id={columnHeadingId(index)}>{column.name}</h2>
                </div>
                <span class="offline-column-count" aria-label={`${columnTasks.length} tasks`}>{columnTasks.length}</span>
              </header>
              <div class="offline-column-cards column-cards">
                {#if columnTasks.length}
                  {#each columnTasks as task (task.id)}
                    <details class="offline-task-card task-card" data-testid="offline-task-card" data-task-id={task.id}>
                      <summary class="offline-task-summary">
                        <span class="offline-task-top">
                          <span class="offline-task-key">{task.key}</span>
                          <span class={`offline-priority offline-priority-${priorityClass(task.priority)}`}>{priorityLabel(task.priority)}</span>
                        </span>
                        <strong>{task.title}</strong>
                      </summary>
                      <div class="offline-task-details">
                        {#if task.description}
                          <p class="offline-task-description">{task.description}</p>
                        {:else}
                          <p class="offline-task-description offline-task-description-empty">No description saved.</p>
                        {/if}
                        <dl>
                          <div><dt>Column</dt><dd>{column.name}</dd></div>
                          <div><dt>Priority</dt><dd>{priorityLabel(task.priority)}</dd></div>
                        </dl>
                        <span class="offline-card-readonly">Read-only saved detail</span>
                      </div>
                    </details>
                  {/each}
                {:else}
                  <p class="offline-column-empty">{search.trim() ? 'No saved tasks match this search.' : 'No saved tasks in this column.'}</p>
                {/if}
              </div>
            </article>
          {/each}

          {#if unassignedTasks.length}
            <article class="offline-column board-column" aria-labelledby="offline-unassigned-heading">
              <header class="offline-column-header">
                <div class="offline-column-title">
                  <span class="offline-column-dot offline-column-dot-muted" aria-hidden="true"></span>
                  <h2 id="offline-unassigned-heading">Other saved tasks</h2>
                </div>
                <span class="offline-column-count" aria-label={`${unassignedTasks.length} tasks`}>{unassignedTasks.length}</span>
              </header>
              <div class="offline-column-cards column-cards">
                {#each unassignedTasks as task (task.id)}
                  <details class="offline-task-card task-card" data-testid="offline-task-card" data-task-id={task.id}>
                    <summary class="offline-task-summary">
                      <span class="offline-task-top">
                        <span class="offline-task-key">{task.key}</span>
                        <span class={`offline-priority offline-priority-${priorityClass(task.priority)}`}>{priorityLabel(task.priority)}</span>
                      </span>
                      <strong>{task.title}</strong>
                    </summary>
                    <div class="offline-task-details">
                      {#if task.description}<p class="offline-task-description">{task.description}</p>{:else}<p class="offline-task-description offline-task-description-empty">No description saved.</p>{/if}
                      <span class="offline-card-readonly">Read-only saved detail</span>
                    </div>
                  </details>
                {/each}
              </div>
            </article>
          {/if}
        </section>
      {:else}
        <section class="offline-no-columns" aria-label="No saved columns">
          <h2>No columns in this snapshot</h2>
          <p>This project has no previously loaded column pages available offline.</p>
        </section>
      {/if}
    {/if}
  </div>
</main>

<style>
  .offline-shell {
    --offline-bg: var(--bg, #f7f8fb);
    --offline-surface: var(--surface, #ffffff);
    --offline-surface-raised: var(--surface-raised, #ffffff);
    --offline-surface-muted: var(--surface-muted, #f0f2f7);
    --offline-surface-hover: var(--surface-hover, #f4f5fa);
    --offline-ink: var(--ink, #1d2433);
    --offline-ink-soft: var(--ink-soft, #4e586a);
    --offline-muted: var(--muted, #596579);
    --offline-faint: var(--faint, #626b7d);
    --offline-border: var(--border, #e5e8ef);
    --offline-border-strong: var(--border-strong, #d9dde7);
    --offline-purple: var(--purple, #6d5efc);
    --offline-purple-soft: var(--purple-soft, #efedff);
    --offline-green: var(--green, #2ea879);
    --offline-green-soft: var(--green-soft, #e7f7f0);
    --offline-red: var(--red, #dc626f);
    --offline-red-soft: var(--red-soft, #fff0f1);
    --offline-amber: var(--amber, #d49534);
    --offline-amber-soft: var(--amber-soft, #fff7e8);
    --offline-shadow: var(--shadow-sm, 0 2px 8px rgba(35, 42, 61, .05));
    --offline-shadow-raised: var(--shadow-md, 0 14px 40px rgba(29, 36, 51, .12));
    width: 100%;
    min-width: 320px;
    min-height: 100vh;
    min-height: 100dvh;
    padding: max(18px, env(safe-area-inset-top, 0px)) max(18px, env(safe-area-inset-right, 0px)) max(26px, env(safe-area-inset-bottom, 0px)) max(18px, env(safe-area-inset-left, 0px));
    overflow-x: hidden;
    color: var(--offline-ink);
    background: radial-gradient(circle at 93% 0, color-mix(in srgb, var(--offline-purple-soft) 55%, transparent), transparent 34%), var(--offline-bg);
    font-family: var(--font-display, 'Manrope', 'DM Sans', ui-sans-serif, system-ui, sans-serif);
    text-rendering: optimizeLegibility;
  }

  .offline-header,
  .offline-content {
    width: min(1180px, 100%);
    margin: 0 auto;
  }

  .offline-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 16px;
    min-height: 64px;
    padding: 6px 0 20px;
    border-bottom: 1px solid var(--offline-border);
  }

  .offline-brand-lockup,
  .offline-header-actions,
  .offline-task-top,
  .offline-column-title,
  .offline-snapshot-meta {
    display: flex;
    align-items: center;
  }

  .offline-brand-lockup { gap: 11px; }
  .offline-brand-mark {
    width: 38px;
    height: 38px;
    display: inline-grid;
    place-items: center;
    border-radius: 12px;
    color: #fff;
    background: linear-gradient(145deg, #8276ff, #5c4ced);
    box-shadow: 0 6px 15px rgba(109, 94, 252, .24);
    font-size: 18px;
    font-weight: 850;
    letter-spacing: -.08em;
  }

  .offline-brand-copy {
    display: flex;
    flex-direction: column;
    gap: 2px;
    line-height: 1.1;
  }

  .offline-brand-copy strong { font-size: 17px; letter-spacing: -.04em; }
  .offline-brand-copy span { color: var(--offline-muted); font-size: 12px; font-weight: 700; letter-spacing: .04em; }
  .offline-header-actions { justify-content: flex-end; gap: 9px; }

  .offline-readonly-badge,
  .offline-priority,
  .offline-card-readonly {
    display: inline-flex;
    align-items: center;
    min-height: 24px;
    padding: 3px 8px;
    border-radius: 999px;
    color: var(--offline-muted);
    background: var(--offline-surface-muted);
    font-size: 12px;
    font-weight: 800;
    letter-spacing: .04em;
    white-space: nowrap;
  }

  .offline-readonly-badge { color: var(--offline-purple); background: var(--offline-purple-soft); }

  .offline-button {
    min-height: 44px;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    gap: 7px;
    padding: 0 14px;
    border: 1px solid var(--offline-border-strong);
    border-radius: 10px;
    color: var(--offline-ink-soft);
    background: var(--offline-surface);
    font-size: 13px;
    font-weight: 800;
    transition: border-color 140ms ease, background 140ms ease, color 140ms ease, transform 140ms ease;
  }

  .offline-button:hover:not(:disabled),
  .offline-button:focus-visible { border-color: var(--offline-purple); color: var(--offline-purple); background: var(--offline-surface-hover); }
  .offline-button:active:not(:disabled) { transform: scale(.98); }
  .offline-button:disabled { cursor: wait; opacity: .68; }
  .offline-reconnect { border-color: var(--offline-purple); color: var(--offline-purple); background: var(--offline-purple-soft); }
  .offline-clear { color: var(--offline-red); }

  .offline-content { padding-top: 22px; }
  .offline-alert {
    display: flex;
    align-items: flex-start;
    gap: 10px;
    padding: 11px 13px;
    margin-bottom: 18px;
    border: 1px solid var(--offline-border);
    border-radius: 11px;
    color: var(--offline-ink-soft);
    background: var(--offline-surface);
    font-size: 13px;
    line-height: 1.45;
  }

  .offline-alert p { margin: 1px 0 0; }
  .offline-alert strong { color: currentColor; }
  .offline-alert-icon {
    width: 19px;
    height: 19px;
    display: inline-grid;
    place-items: center;
    flex: 0 0 auto;
    border-radius: 50%;
    color: #fff;
    background: var(--offline-purple);
    font-size: 11px;
    font-weight: 850;
  }

  .offline-alert-error { border-color: color-mix(in srgb, var(--offline-red), var(--offline-border) 65%); color: var(--offline-red); background: var(--offline-red-soft); }
  .offline-alert-warning { border-color: color-mix(in srgb, var(--offline-amber), var(--offline-border) 65%); color: var(--offline-ink-soft); background: var(--offline-amber-soft); }
  .offline-alert-warning .offline-alert-icon { background: var(--offline-amber); }

  .offline-intro {
    display: flex;
    align-items: flex-end;
    justify-content: space-between;
    gap: 22px;
    margin-bottom: 18px;
  }

  .offline-eyebrow {
    display: block;
    margin-bottom: 8px;
    color: var(--offline-purple);
    font-size: 12px;
    font-weight: 850;
    letter-spacing: .12em;
    text-transform: uppercase;
  }

  .offline-intro h1 {
    margin: 0;
    font-size: clamp(24px, 4vw, 35px);
    line-height: 1.08;
    letter-spacing: -.055em;
  }

  .offline-intro p {
    max-width: 620px;
    margin: 7px 0 0;
    color: var(--offline-muted);
    font-size: 13px;
    line-height: 1.55;
  }

  .offline-privacy {
    display: flex;
    align-items: flex-start;
    gap: 9px;
    padding: 11px 13px;
    margin-bottom: 15px;
    border: 1px solid color-mix(in srgb, var(--offline-purple), var(--offline-border) 68%);
    border-radius: 11px;
    color: var(--offline-ink-soft);
    background: color-mix(in srgb, var(--offline-purple-soft) 42%, var(--offline-surface));
    font-size: 12px;
    line-height: 1.45;
  }

  .offline-privacy p { margin: 1px 0 0; }
  .offline-privacy strong { color: var(--offline-ink); }
  .offline-privacy-icon { color: var(--offline-purple); font-size: 16px; line-height: 1; }

  .offline-empty,
  .offline-no-columns {
    display: grid;
    justify-items: center;
    padding: clamp(34px, 9vw, 72px) 22px;
    border: 1px dashed var(--offline-border-strong);
    border-radius: 15px;
    color: var(--offline-ink-soft);
    background: color-mix(in srgb, var(--offline-surface) 75%, transparent);
    text-align: center;
  }

  .offline-empty-icon { color: var(--offline-purple); font-size: 31px; line-height: 1; }
  .offline-empty h2,
  .offline-no-columns h2 { margin: 13px 0 0; color: var(--offline-ink); font-size: 19px; letter-spacing: -.03em; }
  .offline-empty p,
  .offline-no-columns p { max-width: 490px; margin: 9px 0 20px; color: var(--offline-muted); font-size: 12px; line-height: 1.55; }
  .offline-empty-reconnect { color: var(--offline-purple); border-color: var(--offline-purple); }

  .offline-board-controls {
    display: grid;
    grid-template-columns: minmax(190px, 260px) minmax(220px, 1fr);
    align-items: end;
    gap: 12px;
    margin-bottom: 15px;
  }

  .offline-project-picker,
  .offline-search { display: grid; gap: 6px; }
  .offline-project-picker label { color: var(--offline-muted); font-size: 12px; font-weight: 850; letter-spacing: .08em; text-transform: uppercase; }
  .offline-project-picker select,
  .offline-search input {
    width: 100%;
    min-height: 44px;
    border: 1px solid var(--offline-border);
    border-radius: 9px;
    color: var(--offline-ink);
    background: var(--offline-surface);
    font-size: 16px;
  }

  .offline-project-picker select { padding: 0 31px 0 11px; }
  .offline-search { position: relative; }
  .offline-search > span:first-child { position: absolute; top: 13px; left: 12px; color: var(--offline-muted); font-size: 16px; line-height: 1; }
  .offline-search input { padding: 0 11px 0 35px; }
  .offline-project-picker select:hover,
  .offline-search input:hover { border-color: var(--offline-border-strong); }
  .offline-project-picker select:focus,
  .offline-search input:focus { outline: 3px solid color-mix(in srgb, var(--offline-purple), transparent 76%); outline-offset: 1px; border-color: var(--offline-purple); }

  .offline-snapshot-meta {
    justify-content: space-between;
    gap: 15px;
    padding: 14px 15px;
    margin-bottom: 11px;
    border: 1px solid var(--offline-border);
    border-radius: 11px;
    background: var(--offline-surface);
    box-shadow: var(--offline-shadow);
  }

  .offline-snapshot-copy { display: grid; gap: 3px; min-width: 0; }
  .offline-snapshot-copy strong { overflow: hidden; color: var(--offline-ink); font-size: 14px; text-overflow: ellipsis; white-space: nowrap; }
  .offline-snapshot-copy span,
  .offline-snapshot-time { color: var(--offline-muted); font-size: 12px; }
  .offline-snapshot-time { display: grid; gap: 3px; justify-items: end; white-space: nowrap; }
  .offline-snapshot-time time { color: var(--offline-ink-soft); font-weight: 750; }
  .offline-scope-note { margin: 8px 0 12px; color: var(--offline-muted); font-size: 12px; line-height: 1.45; }

  .offline-board-columns {
    --board-empty-card-height: 154px;
    min-width: 0;
    display: grid;
    grid-auto-flow: column;
    grid-auto-columns: minmax(275px, 1fr);
    align-items: start;
    gap: 14px;
    overflow-x: auto;
    overscroll-behavior-x: contain;
    padding: 2px 2px 18px;
    scroll-snap-type: x proximity;
    scrollbar-width: thin;
  }

  .offline-column {
    min-width: 0;
    min-height: 290px;
    height: var(--board-column-height, auto);
    display: flex;
    flex-direction: column;
    overflow: hidden;
    border: 1px solid var(--offline-border);
    border-radius: 13px;
    background: color-mix(in srgb, var(--offline-surface-muted) 68%, transparent);
    scroll-snap-align: start;
  }

  .offline-column-header {
    display: flex;
    flex: 0 0 auto;
    align-items: center;
    justify-content: space-between;
    gap: 9px;
    min-height: 53px;
    padding: 0 13px;
    background: color-mix(in srgb, var(--offline-surface-muted) 86%, transparent);
  }

  .offline-column-title { min-width: 0; gap: 9px; }
  .offline-column-title h2 { min-width: 0; margin: 0; overflow: hidden; font-size: 14px; letter-spacing: -.02em; text-overflow: ellipsis; white-space: nowrap; }
  .offline-column-dot { width: 8px; height: 8px; flex: 0 0 auto; border-radius: 50%; background: var(--offline-purple); box-shadow: 0 0 0 3px color-mix(in srgb, var(--offline-purple), transparent 82%); }
  .offline-column-dot-muted { background: var(--offline-muted); box-shadow: 0 0 0 3px color-mix(in srgb, var(--offline-muted), transparent 82%); }
  .offline-column-count { min-width: 23px; padding: 3px 6px; border-radius: 8px; color: var(--offline-muted); background: var(--offline-surface); font-size: 12px; font-weight: 850; text-align: center; }

  .offline-column-cards {
    min-height: 0;
    display: flex;
    flex: 1;
    flex-direction: column;
    gap: 8px;
    overflow-y: auto;
    overscroll-behavior-y: contain;
    padding: 12px 9px 9px;
    scrollbar-gutter: stable;
  }

  .offline-column-empty { min-height: 105px; display: grid; place-items: center; margin: 0; padding: 16px 10px; color: var(--offline-muted); font-size: 12px; line-height: 1.45; text-align: center; }
  .offline-task-card { overflow: hidden; flex: 0 0 auto; border: 1px solid var(--offline-border); border-radius: 10px; background: var(--offline-surface); box-shadow: var(--offline-shadow); transition: border-color 140ms ease, box-shadow 140ms ease; }
  .offline-task-card:hover { border-color: var(--offline-border-strong); box-shadow: var(--offline-shadow-raised); }
  .offline-task-card[open] { border-color: color-mix(in srgb, var(--offline-purple), var(--offline-border) 56%); }
  .offline-task-summary { min-height: 74px; display: grid; align-content: center; gap: 7px; padding: 11px 12px; color: var(--offline-ink); cursor: pointer; list-style-position: inside; }
  .offline-task-summary::-webkit-details-marker { color: var(--offline-purple); }
  .offline-task-summary:focus-visible { outline: 3px solid color-mix(in srgb, var(--offline-purple), transparent 76%); outline-offset: -3px; }
  .offline-task-top { min-width: 0; gap: 7px; }
  .offline-task-key { min-width: 0; overflow: hidden; color: var(--offline-muted); font: 750 12px ui-monospace, SFMono-Regular, Menlo, monospace; letter-spacing: .05em; text-overflow: ellipsis; white-space: nowrap; }
  .offline-priority { min-height: 20px; margin-left: auto; padding: 2px 6px; color: var(--offline-muted); font-size: 12px; letter-spacing: 0; }
  .offline-priority-urgent { color: var(--offline-red); background: var(--offline-red-soft); }
  .offline-priority-high { color: var(--offline-amber); background: var(--offline-amber-soft); }
  .offline-priority-normal { color: var(--offline-purple); background: var(--offline-purple-soft); }
  .offline-priority-low { color: var(--offline-muted); background: var(--offline-surface-muted); }
  .offline-task-summary > strong { overflow: hidden; font-size: 16px; line-height: 1.35; text-overflow: ellipsis; }
  .offline-task-details { padding: 0 12px 12px 30px; }
  .offline-task-description { margin: 0 0 12px; color: var(--offline-ink-soft); font-size: 14px; line-height: 1.5; white-space: pre-wrap; overflow-wrap: anywhere; }
  .offline-task-description-empty { color: var(--offline-muted); font-style: italic; }
  .offline-task-details dl { display: grid; gap: 6px; margin: 0 0 11px; }
  .offline-task-details dl > div { display: flex; justify-content: space-between; gap: 10px; font-size: 12px; }
  .offline-task-details dt { color: var(--offline-muted); }
  .offline-task-details dd { margin: 0; color: var(--offline-ink-soft); font-weight: 750; text-align: right; }
  .offline-card-readonly { min-height: 20px; padding: 2px 6px; color: var(--offline-muted); font-size: 12px; letter-spacing: 0; }

  .offline-no-columns { padding: 36px 20px; margin-top: 4px; }
  .offline-no-columns h2 { margin-top: 0; font-size: 16px; }
  .offline-no-columns p { margin-bottom: 0; }

  .offline-spinner { width: 14px; height: 14px; display: inline-block; border: 2px solid color-mix(in srgb, var(--offline-purple), transparent 65%); border-top-color: var(--offline-purple); border-radius: 50%; animation: offline-spin .75s linear infinite; }
  @keyframes offline-spin { to { transform: rotate(360deg); } }

  .sr-only { position: absolute; width: 1px; height: 1px; padding: 0; overflow: hidden; clip: rect(0, 0, 0, 0); white-space: nowrap; border: 0; }

  :global(html[data-theme='dark']) .offline-shell {
    --offline-bg: #111219;
    --offline-surface: #191b25;
    --offline-surface-raised: #20222e;
    --offline-surface-muted: #252835;
    --offline-surface-hover: #2a2d3a;
    --offline-ink: #f0f1f6;
    --offline-ink-soft: #c0c4d0;
    --offline-muted: #aab1c0;
    --offline-faint: #969eaf;
    --offline-border: #2c2f3c;
    --offline-border-strong: #3a3e4c;
    --offline-purple: #958aff;
    --offline-purple-soft: #292641;
    --offline-green: #4bc494;
    --offline-green-soft: #1e342e;
    --offline-red: #f0828d;
    --offline-red-soft: #3d242b;
    --offline-amber: #e4ad53;
    --offline-amber-soft: #392f20;
  }

  @media (max-width: 680px) {
    .offline-shell { padding-right: max(13px, env(safe-area-inset-right, 0px)); padding-left: max(13px, env(safe-area-inset-left, 0px)); }
    .offline-header { min-height: 52px; gap: 8px; padding-top: 2px; padding-bottom: 11px; }
    .offline-brand-lockup { min-width: 0; flex: 1 1 auto; }
    .offline-brand-copy { min-width: 0; }
    .offline-brand-copy strong { font-size: 16px; }
    .offline-header-actions { flex: 0 0 auto; gap: 6px; }
    .offline-header-actions .offline-reconnect { flex: 0 0 auto; padding-right: 10px; padding-left: 10px; }
    .offline-content { padding-top: 17px; }
    .offline-alert { padding: 9px 11px; margin-bottom: 12px; font-size: 12px; }
    .offline-intro { align-items: stretch; flex-direction: column; gap: 8px; margin-bottom: 11px; }
    .offline-eyebrow { margin-bottom: 5px; }
    .offline-intro h1 { font-size: 26px; }
    .offline-intro p { margin-top: 5px; font-size: 12px; }
    .offline-clear { align-self: flex-start; min-height: 40px; padding-right: 11px; padding-left: 11px; }
    .offline-privacy { padding: 8px 10px; margin-bottom: 12px; }
    .offline-board-controls { grid-template-columns: 1fr; gap: 8px; margin-bottom: 10px; }
    .offline-snapshot-meta { align-items: flex-start; flex-direction: column; gap: 7px; padding: 10px 12px; margin-bottom: 8px; }
    .offline-snapshot-time { justify-items: start; }
    .offline-scope-note { margin: 6px 0 10px; }
    .offline-board-columns { grid-auto-columns: minmax(82vw, 1fr); gap: 11px; margin-right: -13px; padding-right: 13px; scroll-snap-type: x mandatory; }
    .offline-column { min-height: 260px; }
    .offline-task-summary { min-height: 78px; padding-top: 12px; padding-bottom: 12px; }
  }

  @media (prefers-reduced-motion: reduce) {
    .offline-button,
    .offline-task-card { transition: none; }
    .offline-spinner { animation: none; }
  }
</style>
