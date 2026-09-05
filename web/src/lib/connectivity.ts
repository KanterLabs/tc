import { get, writable } from 'svelte/store';

// Reconnection does not enable writes until the application revalidates auth.
export const offlineReadOnly = writable(
  (typeof navigator !== 'undefined' && navigator.onLine === false)
  || (typeof document !== 'undefined' && Boolean(document.querySelector('meta[name="helm-offline-shell"]')))
);
export function writesBlocked(): boolean {
  return get(offlineReadOnly) || (typeof navigator !== 'undefined' && navigator.onLine === false);
}
