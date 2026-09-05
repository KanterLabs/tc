import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

type PwaModule = typeof import('./pwa');

type WorkerStub = Omit<ServiceWorker, 'state' | 'postMessage'> & {
  state: ServiceWorker['state'];
  postMessage: ReturnType<typeof vi.fn<(...args: unknown[]) => void>>;
  setState: (state: ServiceWorker['state']) => void;
};

type RegistrationStub = ServiceWorkerRegistration & {
  updatefound: () => void;
};

type ContainerStub = ServiceWorkerContainer & {
  emitMessage: (data: unknown) => void;
  emitControllerChange: () => void;
};

let originalServiceWorkerDescriptor: PropertyDescriptor | undefined;

function worker(initialState: ServiceWorker['state'] = 'activated'): WorkerStub {
  const target = new EventTarget() as WorkerStub;
  target.state = initialState;
  target.postMessage = vi.fn<(...args: unknown[]) => void>();
  target.setState = (state) => {
    target.state = state;
    target.dispatchEvent(new Event('statechange'));
  };
  return target;
}

function registration(options: {
  installing?: WorkerStub | null;
  waiting?: WorkerStub | null;
  active?: WorkerStub | null;
} = {}): RegistrationStub {
  const target = new EventTarget() as RegistrationStub;
  Object.assign(target, {
    installing: options.installing ?? null,
    waiting: options.waiting ?? null,
    active: options.active ?? worker(),
    scope: 'http://localhost/'
  });
  target.updatefound = () => target.dispatchEvent(new Event('updatefound'));
  return target;
}

function container(reg: RegistrationStub, controller: ServiceWorker | null = null): ContainerStub {
  const target = new EventTarget() as ContainerStub;
  Object.assign(target, {
    controller,
    register: vi.fn(async () => reg),
    ready: Promise.resolve(reg),
    getRegistration: vi.fn(async () => reg)
  });
  target.emitMessage = (data) => target.dispatchEvent(new MessageEvent('message', { data }));
  target.emitControllerChange = () => target.dispatchEvent(new Event('controllerchange'));
  return target;
}

async function loadPwa(options: {
  reg?: RegistrationStub;
  controller?: ServiceWorker | null;
  registerError?: Error;
} = {}): Promise<{ pwa: PwaModule; serviceWorker: ContainerStub; reg: RegistrationStub }> {
  vi.resetModules();
  const reg = options.reg || registration();
  const serviceWorker = container(reg, options.controller ?? null);
  if (options.registerError) serviceWorker.register = vi.fn(async () => { throw options.registerError; });
  Object.defineProperty(navigator, 'serviceWorker', {
    configurable: true,
    value: serviceWorker
  });
  const pwa = await import('./pwa');
  return { pwa, serviceWorker, reg };
}

beforeEach(() => {
  originalServiceWorkerDescriptor = Object.getOwnPropertyDescriptor(navigator, 'serviceWorker');
});

afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
  if (originalServiceWorkerDescriptor) {
    Object.defineProperty(navigator, 'serviceWorker', originalServiceWorkerDescriptor);
  } else {
    Reflect.deleteProperty(navigator, 'serviceWorker');
  }
});

describe('PWA lifecycle bridge', () => {
  it('keeps cacheReady false until a validated worker status message arrives', async () => {
    const { pwa, serviceWorker } = await loadPwa();

    await pwa.registerPwa({ enabled: true });
    await Promise.resolve();
    expect(pwa.getPwaState()).toMatchObject({ registered: true, appReady: true, cacheReady: false });

    serviceWorker.emitMessage({ type: 'PWA_CACHE_READY', shellCached: true });
    expect(pwa.getPwaState()).toMatchObject({ appReady: true, cacheReady: true, cacheError: '' });

    serviceWorker.emitMessage({ type: 'PWA_CACHE_ERROR', shellCached: false, error: 'shell missing' });
    expect(pwa.getPwaState()).toMatchObject({ cacheReady: false, cacheError: 'shell missing' });
  });

  it('does not send SKIP_WAITING when the explicit update guard declines', async () => {
    const waiting = worker('installed');
    const current = worker('activated');
    const reg = registration({ waiting, active: current });
    const { pwa } = await loadPwa({ reg, controller: current });
    await pwa.registerPwa({ enabled: true });

    const guard = vi.fn(() => false);
    await expect(pwa.applyPwaUpdate(guard)).resolves.toBe(false);
    expect(guard).toHaveBeenCalledOnce();
    expect(waiting.postMessage).not.toHaveBeenCalled();
    expect(pwa.getPwaState()).toMatchObject({ updateAvailable: true, updateApplying: false });
  });

  it('reports activation timeout without reloading the page', async () => {
    vi.useFakeTimers();
    const waiting = worker('installed');
    const current = worker('activated');
    const reg = registration({ waiting, active: current });
    const { pwa } = await loadPwa({ reg, controller: current });
    await pwa.registerPwa({ enabled: true });

    const update = pwa.applyPwaUpdate(() => true);
    await vi.advanceTimersByTimeAsync(7001);

    await expect(update).resolves.toBe(false);
    expect(waiting.postMessage).toHaveBeenCalledWith({ type: 'SKIP_WAITING' });
    expect(pwa.getPwaState()).toMatchObject({ updateAvailable: true, updateApplying: false });
  });

  it('exposes registration failures as cache errors', async () => {
    const { pwa } = await loadPwa({ registerError: new Error('registration failed') });

    await expect(pwa.registerPwa({ enabled: true })).resolves.toBeNull();
    expect(pwa.getPwaState()).toMatchObject({
      registered: false,
      appReady: false,
      cacheReady: false,
      error: 'registration failed',
      cacheError: 'registration failed'
    });
  });

  it('surfaces a redundant install without replacing the active app', async () => {
    const installing = worker('installing');
    const current = worker('activated');
    const reg = registration({ installing, active: current });
    const { pwa } = await loadPwa({ reg, controller: null });
    await pwa.registerPwa({ enabled: true });

    installing.setState('redundant');
    expect(pwa.getPwaState()).toMatchObject({
      registered: true,
      cacheReady: false,
      cacheError: 'The offline shell update failed validation and was not activated.'
    });
  });
});
