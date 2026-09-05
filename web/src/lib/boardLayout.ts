import { tick } from 'svelte';

/** Measure the space above the board without resizing columns as the page scrolls. */
export function boardViewport(node: HTMLElement) {
  let frame = 0;
  const measure = () => {
    frame = 0;
    const top = Math.max(0, Math.round(node.getBoundingClientRect().top + window.scrollY));
    node.style.setProperty('--board-top', `${top}px`);
  };
  const schedule = () => {
    if (!frame) frame = window.requestAnimationFrame(measure);
  };
  const resize = new ResizeObserver(schedule);
  const observeLayout = () => {
    resize.disconnect();
    if (node.parentElement) {
      resize.observe(node.parentElement);
      // Heading, toolbar and notices may change height independently of the board.
      for (const sibling of node.parentElement.children) {
        if (sibling !== node) resize.observe(sibling);
      }
    }
    const topbar = document.querySelector('.topbar');
    if (topbar) resize.observe(topbar);
    schedule();
  };
  const mutations = new MutationObserver(observeLayout);
  if (node.parentElement) mutations.observe(node.parentElement, { childList: true });
  observeLayout();
  measure();
  window.addEventListener('resize', schedule);
  window.visualViewport?.addEventListener('resize', schedule);
  return {
    destroy() {
      resize.disconnect();
      mutations.disconnect();
      window.cancelAnimationFrame(frame);
      window.removeEventListener('resize', schedule);
      window.visualViewport?.removeEventListener('resize', schedule);
    }
  };
}

type ScrollContext = { scope: string; column: string; page: number };

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
      const saved = positions.get(context.column);
      const top = saved?.page === context.page ? saved.top : 0;
      positions.set(context.column, { page: context.page, top });
      void tick().then(() => {
        if (alive) node.scrollTop = top;
      });
    };
    const save = () => {
      if (scope === context.scope) {
        positions.set(context.column, { page: context.page, top: node.scrollTop });
      }
    };
    restore();
    node.addEventListener('scroll', save, { passive: true });
    return {
      update(next: ScrollContext) {
        const changed = next.scope !== context.scope || next.column !== context.column || next.page !== context.page;
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
