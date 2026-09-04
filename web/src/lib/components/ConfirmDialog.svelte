<script lang="ts">
  import { onMount, tick } from 'svelte';

  export let title: string;
  export let message: string;
  export let confirmLabel = 'Confirm';
  export let cancelLabel = 'Cancel';
  export let onConfirm: () => void;
  export let onCancel: () => void;

  let dialog: HTMLDivElement;
  const focusableSelector = [
    'button:not(:disabled)',
    'input:not(:disabled):not([type="hidden"])',
    'select:not(:disabled)',
    'textarea:not(:disabled)',
    '[href]',
    '[tabindex]:not([tabindex="-1"])'
  ].join(',');

  onMount(() => {
    void tick().then(() => dialog?.querySelector<HTMLElement>('[data-dialog-initial-focus]')?.focus());
  });

  function focusable(): HTMLElement[] {
    return Array.from(dialog?.querySelectorAll<HTMLElement>(focusableSelector) || [])
      .filter((element) => !element.hasAttribute('hidden') && element.getAttribute('aria-hidden') !== 'true');
  }

  function handleKeydown(event: KeyboardEvent) {
    if (event.key === 'Escape') {
      event.preventDefault();
      event.stopPropagation();
      onCancel();
      return;
    }
    if (event.key !== 'Tab') return;
    const elements = focusable();
    if (!elements.length) {
      event.preventDefault();
      dialog?.focus();
      return;
    }
    const active = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    const index = active ? elements.indexOf(active) : -1;
    if (event.shiftKey && (index <= 0 || index < 0)) {
      event.preventDefault();
      elements[elements.length - 1].focus();
    } else if (!event.shiftKey && (index < 0 || index === elements.length - 1)) {
      event.preventDefault();
      elements[0].focus();
    }
  }
</script>

<div class="modal-backdrop confirm-backdrop" role="presentation" on:click|self={onCancel}></div>
<div
  bind:this={dialog}
  class="modal confirm-dialog"
  role="alertdialog"
  aria-modal="true"
  aria-labelledby="confirm-dialog-title"
  aria-describedby="confirm-dialog-message"
  tabindex="-1"
  on:keydown={handleKeydown}
>
  <div class="modal-header">
    <div>
      <span class="eyebrow">Confirmation required</span>
      <h2 id="confirm-dialog-title">{title}</h2>
    </div>
  </div>
  <p id="confirm-dialog-message" class="confirm-dialog-message">{message}</p>
  <div class="modal-actions">
    <button class="text-button" type="button" data-dialog-initial-focus on:click={onCancel}>{cancelLabel}</button>
    <button class="button danger-button" type="button" on:click={onConfirm}>{confirmLabel}</button>
  </div>
</div>
