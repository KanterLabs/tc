import { tick } from 'svelte';

export const visibleBoardCards = 10;

/** Reserve ten natural-height cards, estimating unused slots from the loaded sample. */
export function tenCardColumnHeight(heights: number[], gap: number, padding: number, chrome: number, fallback = 120) {
  const sample = heights.slice(0, visibleBoardCards);
  const total = sample.reduce((sum, height) => sum + height, 0);
  const average = sample.length ? total / sample.length : fallback;
  return Math.ceil(total + average * (visibleBoardCards - sample.length)
    + gap * (visibleBoardCards - 1) + padding + chrome);
}

/** Keep columns aligned using the tallest ten-card stack, independent of viewport height. */
export function boardCardHeight(node: HTMLElement) {
  let frame = 0;
  const measure = () => {
    frame = 0;
    const populated: number[] = [];
    const empty: number[] = [];
    const fallback = parseFloat(getComputedStyle(node).getPropertyValue('--board-empty-card-height')) || 120;
    for (const column of node.querySelectorAll<HTMLElement>(':scope > .board-column')) {
      const body = column.querySelector<HTMLElement>('.column-cards');
      if (!body) continue;
      const cards = Array.from(body.querySelectorAll<HTMLElement>(':scope > .task-card')).slice(0, visibleBoardCards);
      const style = getComputedStyle(body);
      const padding = parseFloat(style.paddingTop) + parseFloat(style.paddingBottom);
      const chrome = column.getBoundingClientRect().height - body.getBoundingClientRect().height;
      const height = tenCardColumnHeight(cards.map((card) => card.getBoundingClientRect().height),
        parseFloat(style.rowGap) || 0, padding, chrome, fallback);
      (cards.length ? populated : empty).push(height);
    }
    // Empty columns share the populated columns' capacity, not a competing estimate.
    const heights = populated.length ? populated : empty;
    if (!heights.length) return;
    const value = `${Math.max(...heights)}px`;
    if (node.style.getPropertyValue('--board-column-height') !== value) {
      node.style.setProperty('--board-column-height', value);
    }
  };
  const schedule = () => {
    if (!frame) frame = window.requestAnimationFrame(measure);
  };
  const resize = new ResizeObserver(schedule);
  const observeLayout = () => {
    resize.disconnect();
    resize.observe(node);
    for (const column of node.querySelectorAll<HTMLElement>(':scope > .board-column')) {
      for (const child of column.children) {
        if (!child.classList.contains('column-cards')) resize.observe(child);
      }
      // Only the visible-capacity sample needs observation, even on very large boards.
      const cards = column.querySelectorAll('.column-cards > .task-card');
      for (let index = 0; index < Math.min(cards.length, visibleBoardCards); index += 1) {
        resize.observe(cards[index]);
      }
    }
    schedule();
  };
  const mutations = new MutationObserver(observeLayout);
  mutations.observe(node, { childList: true, subtree: true });
  observeLayout();
  measure();
  window.addEventListener('resize', schedule);
  return {
    destroy() {
      resize.disconnect();
      mutations.disconnect();
      window.cancelAnimationFrame(frame);
      window.removeEventListener('resize', schedule);
    }
  };
}

type ScrollContext = { scope: string; column: string; page: number; ready?: boolean };

/** Per-app cache survives a refresh's loading placeholder, not project/filter changes. */
export function createColumnScroll() {
  let scope = '';
  const positions = new Map<string, { page: number; top: number }>();
  return (node: HTMLElement, context: ScrollContext) => {
    let alive = true;
    const restore = () => {
      if (scope !== context.scope) {
        scope = context.scope;
        positions.clear();
      }
      // Other columns can finish first and remount this body while it is still empty.
      // Restoring then would clamp the saved offset to zero before its cards arrive.
      if (context.ready === false) return;
      const saved = positions.get(context.column);
      const top = saved?.page === context.page ? saved.top : 0;
      positions.set(context.column, { page: context.page, top });
      void tick().then(() => {
        if (alive && context.ready !== false) node.scrollTop = top;
      });
    };
    const save = () => {
      if (scope === context.scope && context.ready !== false) {
        positions.set(context.column, { page: context.page, top: node.scrollTop });
      }
    };
    restore();
    node.addEventListener('scroll', save, { passive: true });
    return {
      update(next: ScrollContext) {
        const changed = next.scope !== context.scope || next.column !== context.column || next.page !== context.page
          || (context.ready === false && next.ready !== false);
        context = next;
        if (changed) restore();
      },
      destroy() {
        alive = false;
        node.removeEventListener('scroll', save);
      }
    };
  };
}
