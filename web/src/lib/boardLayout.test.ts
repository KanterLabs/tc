import { describe, expect, it } from 'vitest';
import { tick } from 'svelte';
import { createColumnScroll, tenCardColumnHeight } from './boardLayout';

describe('ten-card capacity', () => {
  it('fits ten actual card heights plus nine gaps, padding and fixed controls', () => {
    expect(tenCardColumnHeight([200, ...Array(9).fill(100)], 8, 19, 90)).toBe(1281);
  });

  it('ignores cards beyond the capacity and rounds up fractional pixels', () => {
    expect(tenCardColumnHeight([...Array(10).fill(100.25), 900], 8, 19, 90)).toBe(1184);
  });

  it('reserves ten slots from fewer cards and uses a fallback for empty boards', () => {
    expect(tenCardColumnHeight([150, 100], 8, 19, 90)).toBe(1431);
    expect(tenCardColumnHeight([], 8, 19, 90, 160)).toBe(1781);
  });
});

describe('column scroll lifecycle', () => {
  it('preserves independent positions across refresh remounts and same-context updates', async () => {
    const action = createColumnScroll();
    const context = { scope: 'project:filters', column: 'ready', page: 0 };
    const first = document.createElement('div');
    const mounted = action(first, context);
    await tick();
    first.scrollTop = 450;
    first.dispatchEvent(new Event('scroll'));
    mounted.update({ ...context });
    await tick();
    expect(first.scrollTop).toBe(450);
    mounted.destroy();
    const replacement = document.createElement('div');
    const restored = action(replacement, { ...context, ready: false });
    await tick();
    replacement.dispatchEvent(new Event('scroll'));
    expect(replacement.scrollTop).toBe(0);
    restored.update({ ...context, ready: true });
    const empty = document.createElement('div');
    const other = action(empty, { ...context, column: 'done' });
    await tick();
    expect(replacement.scrollTop).toBe(450);
    expect(empty.scrollTop).toBe(0);
    restored.destroy();
    other.destroy();
  });

  it('resets for card pages, filters and projects and ignores destroyed pending restores', async () => {
    const action = createColumnScroll();
    const context = { scope: 'project:filters', column: 'ready', page: 0 };
    const node = document.createElement('div');
    const mounted = action(node, context);
    await tick();
    for (const next of [
      { ...context, page: 100 },
      { ...context, scope: 'project:new-filters' },
      { ...context, scope: 'other-project:filters' }
    ]) {
      node.scrollTop = 500;
      node.dispatchEvent(new Event('scroll'));
      mounted.update(next);
      await tick();
      expect(node.scrollTop).toBe(0);
    }
    mounted.update(context);
    mounted.destroy();
    node.scrollTop = 99;
    await tick();
    expect(node.scrollTop).toBe(99);
  });
});
