import { mount, tick, unmount } from 'svelte';
import { afterEach, describe, expect, it, vi } from 'vitest';
import ConfirmDialog from './ConfirmDialog.svelte';

const mounted: Array<ReturnType<typeof mount>> = [];

afterEach(async () => {
  while (mounted.length) await unmount(mounted.pop()!);
  document.body.replaceChildren();
});

describe('ConfirmDialog', () => {
  it('names the destructive item and traps Tab focus', async () => {
    const onConfirm = vi.fn();
    const onCancel = vi.fn();
    mounted.push(mount(ConfirmDialog, {
      target: document.body,
      props: {
        title: 'Delete OPS-42?',
        message: 'Ship the board will be removed.',
        confirmLabel: 'Delete task',
        onConfirm,
        onCancel
      }
    }));
    await tick();
    const dialog = document.querySelector<HTMLElement>('[role="alertdialog"]');
    const cancel = document.querySelector<HTMLButtonElement>('[data-dialog-initial-focus]');
    const confirm = [...document.querySelectorAll<HTMLButtonElement>('button')].find((button) => button.textContent === 'Delete task');
    expect(dialog?.getAttribute('aria-labelledby')).toBe('confirm-dialog-title');
    expect(dialog?.getAttribute('aria-describedby')).toBe('confirm-dialog-message');
    expect(dialog?.textContent).toContain('OPS-42');
    cancel?.focus();
    cancel?.dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', bubbles: true, shiftKey: true }));
    expect(document.activeElement).toBe(confirm);
    confirm?.click();
    expect(onConfirm).toHaveBeenCalledOnce();
  });

  it('supports cancel and Escape without invoking confirm', async () => {
    const onConfirm = vi.fn();
    const onCancel = vi.fn();
    const parentKeydown = vi.fn();
    window.addEventListener('keydown', parentKeydown);
    mounted.push(mount(ConfirmDialog, {
      target: document.body,
      props: { title: 'Revoke token?', message: 'CI token will stop working.', onConfirm, onCancel }
    }));
    await tick();
    const dialog = document.querySelector<HTMLElement>('[role="alertdialog"]');
    dialog?.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    window.removeEventListener('keydown', parentKeydown);
    expect(onCancel).toHaveBeenCalledOnce();
    expect(onConfirm).not.toHaveBeenCalled();
    expect(parentKeydown).not.toHaveBeenCalled();
  });
});
