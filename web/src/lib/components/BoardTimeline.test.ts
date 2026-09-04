// @vitest-environment jsdom
import { mount, unmount } from 'svelte';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { Comment, Task, TaskTimelineItem } from '../types';
import BoardTimeline from './BoardTimeline.svelte';

const mountedComponents: Array<ReturnType<typeof mount>> = [];

afterEach(async () => {
  while (mountedComponents.length) await unmount(mountedComponents.pop()!);
  document.body.replaceChildren();
  vi.restoreAllMocks();
});

function task(id: string, number: number, title: string): Task {
  return {
    id,
    number,
    key: `TC-${number}`,
    project_id: 'project-1',
    column_id: 'column-1',
    title,
    priority: 'normal',
    position: number,
    version: 1
  };
}

function cursor(eventCursor: number, kind: TaskTimelineItem['kind'], id: string): string {
  return btoa(JSON.stringify({ v: 1, at: '2026-01-01T00:00:00Z', ec: eventCursor, k: kind, id }))
    .replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

function comment(id: string, taskID: string, body: string, eventCursor: number): Comment {
  return {
    id,
    task_id: taskID,
    actor_id: 'actor-1',
    body,
    version: 1,
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z'
  };
}

function commentItem(value: Comment, eventCursor: number): TaskTimelineItem {
  return {
    id: value.id,
    cursor: cursor(eventCursor, 'comment', value.id),
    kind: 'comment',
    task_id: value.task_id,
    actor: { id: value.actor_id, kind: 'human', name: 'Author' },
    created_at: value.created_at,
    progress: null,
    comment: value,
    change: null
  };
}

describe('BoardTimeline component', () => {
  it('renders equal-time rows in API order and displays a canonical older comment', async () => {
    const older = commentItem(comment('comment-old', 'task-old', 'Canonical edit', 2), 2);
    const newer = commentItem(comment('comment-new', 'task-new', 'Newer comment', 10), 10);
    const mounted = mount(BoardTimeline, {
      target: document.body,
      props: { items: [older, newer], tasks: [task('task-old', 1, 'Older task'), task('task-new', 2, 'Newer task')] }
    });
    mountedComponents.push(mounted);

    const rows = [...document.querySelectorAll<HTMLElement>('.board-timeline-row')];
    expect(rows[0]?.textContent).toContain('Newer task');
    expect(rows[1]?.textContent).toContain('Older task');
    expect(document.body.textContent).toContain('Canonical edit');
  });

  it('invokes load older when older activity is available', () => {
    const onLoadOlder = vi.fn();
    const item = commentItem(comment('comment-1', 'task-1', 'Comment', 1), 1);
    const mounted = mount(BoardTimeline, {
      target: document.body,
      props: { items: [item], tasks: [task('task-1', 1, 'Task')], hasOlder: true, onLoadOlder }
    });
    mountedComponents.push(mounted);

    const button = document.querySelector<HTMLButtonElement>('.board-timeline-load-more button');
    expect(button?.disabled).toBe(false);
    button?.click();
    expect(onLoadOlder).toHaveBeenCalledTimes(1);
  });

  it('disables load older while the request is pending', () => {
    const item = commentItem(comment('comment-1', 'task-1', 'Comment', 1), 1);
    const mounted = mount(BoardTimeline, {
      target: document.body,
      props: { items: [item], tasks: [task('task-1', 1, 'Task')], hasOlder: true, loadingOlder: true }
    });
    mountedComponents.push(mounted);

    const button = document.querySelector<HTMLButtonElement>('.board-timeline-load-more button');
    expect(button?.disabled).toBe(true);
    expect(button?.textContent).toContain('Loading older');
  });
});
