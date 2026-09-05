/**
 * Small, browser-only bridge for Helm's build-time service worker.
 *
 * The service worker owns the cache policy. This module only reports its
 * lifecycle to Svelte and applies a waiting update after an explicit user
 * action. Keeping the bridge dependency-free also lets the application boot
 * normally in development, where Vite does not emit /sw.js.
 */

export const PWA_SERVICE_WORKER_URL = '/sw.js';

export type PwaUpdateGuard = () => boolean | Promise<boolean>;

export type PwaState = {
  /** Whether this browser exposes the Service Worker API. */
  supported: boolean;
  /** Whether registration succeeded for this page. */
  registered: boolean;
  /** Whether an active worker is ready to control this origin. */
  appReady: boolean;
  /** Whether the worker has confirmed that a shell cache is available. */
  cacheReady: boolean;
  /** Human-readable cache failure, if the worker could not prepare the shell. */
  cacheError: string;
  /** Whether a new worker is waiting for the user to approve an update. */
  updateAvailable: boolean;
  /** Whether this page currently believes the network is reachable. */
  online: boolean;
  /** Whether the current page is controlled by a service worker. */
  controlled: boolean;
  /** Registration or lifecycle failure, if any. */
  error: string;
  /** Whether the explicit update action is currently waiting for activation. */
  updateApplying: boolean;
};

export type RegisterPwaOptions = {
  /** Override the production-only default, primarily for integration tests. */
  enabled?: boolean;
  /** Override the emitted service-worker URL without widening its default scope. */
  serviceWorkerUrl?: string;
  /** Keep registration scoped to this same-origin path. Defaults to /. */
  scope?: string;
};

type PwaListener = (state: PwaState) => void;

const serviceWorkerSupported =
  typeof navigator !== 'undefined' && 'serviceWorker' in navigator;

let state: PwaState = {
  supported: serviceWorkerSupported,
  registered: false,
  appReady: false,
  cacheReady: false,
  cacheError: '',
  updateAvailable: false,
  online: typeof navigator === 'undefined' ? true : navigator.onLine,
  controlled: serviceWorkerSupported && Boolean(navigator.serviceWorker.controller),
  error: '',
  updateApplying: false
};

let registration: ServiceWorkerRegistration | null = null;
let registrationPromise: Promise<ServiceWorkerRegistration | null> | null = null;
let listenersAttached = false;
const listeners = new Set<PwaListener>();

function publish(next: Partial<PwaState>): void {
  state = { ...state, ...next };
  listeners.forEach((listener) => listener(state));
}

export function getPwaState(): PwaState {
  return state;
}

/** Subscribe to lifecycle updates; the current snapshot is delivered first. */
export function subscribePwa(listener: PwaListener): () => void {
  listeners.add(listener);
  listener(state);
  return () => listeners.delete(listener);
}

function errorMessage(error: unknown, fallback: string): string {
  if (error instanceof Error && error.message) return error.message;
  if (typeof error === 'string' && error) return error;
  return fallback;
}

function attachLifecycleListeners(): void {
  if (listenersAttached || !serviceWorkerSupported || typeof window === 'undefined') return;
  listenersAttached = true;

  const setOnline = () => publish({ online: true });
  const setOffline = () => publish({ online: false });
  window.addEventListener('online', setOnline);
  window.addEventListener('offline', setOffline);

  navigator.serviceWorker.addEventListener('controllerchange', () => {
    publish({ controlled: Boolean(navigator.serviceWorker.controller), updateAvailable: false });
  });

  navigator.serviceWorker.addEventListener('message', (event: MessageEvent<unknown>) => {
    const data = event.data as { type?: string; error?: unknown; shellCached?: unknown } | null;
    if (!data || typeof data !== 'object') return;
    if (data.type === 'PWA_CACHE_READY') {
      const cacheError = typeof data.error === 'string' ? data.error : '';
      publish({
        appReady: true,
        cacheReady: data.shellCached !== false && !cacheError,
        cacheError
      });
    } else if (data.type === 'PWA_CACHE_ERROR') {
      publish({ cacheReady: false, cacheError: errorMessage(data.error, 'Offline shell is unavailable.') });
    }
  });
}

function requestBestEffortPersistence(): void {
  if (typeof window === 'undefined' || !navigator.storage?.persist) return;
  const safariStandalone = Boolean((navigator as Navigator & { standalone?: boolean }).standalone);
  const homeScreenApp = safariStandalone || window.matchMedia?.('(display-mode: standalone)').matches === true;
  if (!homeScreenApp) return;
  // Persistence is a best-effort hint; WebKit may decline it and can still
  // evict origin data under storage pressure.
  void navigator.storage.persist().catch(() => undefined);
}

function monitorInstallingWorker(reg: ServiceWorkerRegistration): void {
  const installing = reg.installing;
  if (!installing) return;
  installing.addEventListener('statechange', () => {
    if (installing.state === 'redundant') {
      publish({ cacheReady: false, cacheError: 'The offline shell update failed validation and was not activated.' });
      return;
    }
    if (installing.state !== 'installed') return;
    // The first install is safe to use immediately. A later install stays in
    // `waiting` until applyPwaUpdate() sends the user-approved message.
    if (navigator.serviceWorker.controller) {
      publish({ updateAvailable: true });
    } else {
      publish({ appReady: true });
    }
  });
}

function monitorRegistration(reg: ServiceWorkerRegistration): void {
  registration = reg;
  publish({
    registered: true,
    controlled: Boolean(navigator.serviceWorker.controller),
    updateAvailable: Boolean(reg.waiting)
  });
  reg.addEventListener('updatefound', () => monitorInstallingWorker(reg));
  monitorInstallingWorker(reg);
}

async function registerPwaInternal(options: RegisterPwaOptions): Promise<ServiceWorkerRegistration | null> {
  if (!serviceWorkerSupported) {
    publish({
      supported: false,
      error: 'Service workers are not available in this browser.',
      cacheError: 'Offline shell is unavailable in this browser.'
    });
    return null;
  }

  const scope = options.scope || '/';
  if (!scope.startsWith('/') || scope.startsWith('//') || scope.includes('://')) {
    publish({ error: 'The service-worker scope must stay on this origin.', cacheError: 'Offline shell scope is invalid.' });
    return null;
  }

  const workerUrl = options.serviceWorkerUrl || PWA_SERVICE_WORKER_URL;
  try {
    const parsedWorkerUrl = new URL(workerUrl, window.location.origin);
    if (parsedWorkerUrl.origin !== window.location.origin || !parsedWorkerUrl.pathname.startsWith('/')) {
      publish({ error: 'The service-worker script must stay on this origin.', cacheError: 'Offline shell script is not same-origin.' });
      return null;
    }
  } catch {
    publish({ error: 'The service-worker script URL is invalid.', cacheError: 'Offline shell script URL is invalid.' });
    return null;
  }

  attachLifecycleListeners();
  requestBestEffortPersistence();
  const reg = await navigator.serviceWorker.register(workerUrl, {
    // The scope is deliberately fixed to this origin. Callers may narrow it,
    // but cannot use this helper to register an arbitrary cross-origin scope.
    scope,
    updateViaCache: 'none'
  });
  monitorRegistration(reg);

  void navigator.serviceWorker.ready.then((active) => {
    publish({ appReady: true, controlled: Boolean(navigator.serviceWorker.controller) });
    // Existing workers may have activated before this page attached its
    // message listener. Ask for a fresh shell status when supported.
    active.active?.postMessage({ type: 'PWA_STATUS' });
  }).catch((error) => {
    publish({ cacheReady: false, cacheError: errorMessage(error, 'Offline shell could not be prepared.') });
  });

  return reg;
}

/**
 * Register the build-emitted worker once. In Vite development builds this is
 * a no-op so a missing /sw.js does not turn local development into an error.
 */
export function registerPwa(options: RegisterPwaOptions = {}): Promise<ServiceWorkerRegistration | null> {
  const enabled = options.enabled ?? Boolean(import.meta.env.PROD);
  if (!enabled) return Promise.resolve(null);
  if (registrationPromise) return registrationPromise;

  registrationPromise = registerPwaInternal(options).catch((error) => {
    const message = errorMessage(error, 'Helm could not register its offline shell.');
    publish({ registered: false, appReady: false, cacheReady: false, error: message, cacheError: message });
    return null;
  });
  return registrationPromise;
}

function waitForControllerChange(timeoutMs = 7000): Promise<boolean> {
  if (!serviceWorkerSupported) return Promise.resolve(false);
  return new Promise((resolve) => {
    let settled = false;
    const finish = (changed: boolean) => {
      if (settled) return;
      settled = true;
      window.clearTimeout(timeout);
      navigator.serviceWorker.removeEventListener('controllerchange', onControllerChange);
      resolve(changed);
    };
    const onControllerChange = () => finish(true);
    const timeout = window.setTimeout(() => finish(false), timeoutMs);
    navigator.serviceWorker.addEventListener('controllerchange', onControllerChange, { once: true });
  });
}

/**
 * Activate a waiting worker only after the caller has obtained user consent.
 * The reload is intentional and happens after controllerchange; a native
 * beforeunload handler on the host app still gets the opportunity to protect
 * unsaved drafts.
 */
export async function applyPwaUpdate(beforeUpdate?: PwaUpdateGuard): Promise<boolean> {
  const waiting = registration?.waiting;
  if (!waiting) return false;

  if (beforeUpdate && !(await beforeUpdate())) return false;
  publish({ updateApplying: true });

  try {
    const changed = waitForControllerChange();
    waiting.postMessage({ type: 'SKIP_WAITING' });
    if (!(await changed)) {
      publish({
        updateApplying: false,
        updateAvailable: true,
        error: 'The new Helm version did not become active. Try again when online.'
      });
      return false;
    }
    if (typeof window !== 'undefined') window.location.reload();
    return true;
  } catch (error) {
    publish({
      updateApplying: false,
      updateAvailable: true,
      error: errorMessage(error, 'Helm could not apply the available update.')
    });
    return false;
  }
}
