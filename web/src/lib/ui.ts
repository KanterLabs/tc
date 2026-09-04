export type ToastKind = 'success' | 'error' | 'info';

export type ToastAccessibility = {
  role: 'alert' | 'status';
  live: 'assertive' | 'polite';
};

/** Return whether a key event originated in a control where typing is active. */
export function isEditableTarget(target: EventTarget | null): boolean {
  if (typeof HTMLElement === 'undefined' || !(target instanceof HTMLElement)) return false;
  return Boolean(target.closest('input, textarea, select, [contenteditable]:not([contenteditable="false"]), [role="textbox"]'));
}

/** Detect the Apple platform values used by navigator.platform/userAgentData. */
export function platformIsMac(platform: string | null | undefined): boolean {
  return /mac/i.test(platform || '');
}

export function platformShortcut(platform: string | null | undefined): string {
  return platformIsMac(platform) ? '⌘ K' : 'Ctrl K';
}

export function themeFromMediaPreference(prefersDark: boolean): 'light' | 'dark' {
  return prefersDark ? 'dark' : 'light';
}

export function toastAccessibility(kind: ToastKind): ToastAccessibility {
  return kind === 'error'
    ? { role: 'alert', live: 'assertive' }
    : { role: 'status', live: 'polite' };
}
