import { describe, expect, it } from 'vitest';
import { tick } from 'svelte';
import { createColumnScroll } from './boardLayout';

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
    const restored = action(replacement, context);
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
