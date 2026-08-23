import { beforeEach, expect, test, vi } from 'vitest';

const mocks = vi.hoisted(() => {
  const updateServiceWorker = vi.fn();
  return {
    registerSW: vi.fn(() => updateServiceWorker),
    updateServiceWorker,
  };
});

vi.mock('virtual:pwa-register', () => ({
  registerSW: mocks.registerSW,
}));

let getPwaUpdateServiceWorker;
let hasPwaRefreshPresenter;
let isPwaRefreshPending;
let registerPwaServiceWorker;
let subscribeToPwaRefresh;

beforeEach(async () => {
  vi.resetModules();
  mocks.registerSW.mockClear();
  mocks.updateServiceWorker.mockClear();
  ({
    getPwaUpdateServiceWorker,
    hasPwaRefreshPresenter,
    isPwaRefreshPending,
    registerPwaServiceWorker,
    subscribeToPwaRefresh,
  } = await import('./pwa-registration'));
});

test('shares one PWA registration between anonymous and authenticated entry paths', () => {
  const onRefresh = vi.fn();
  const unsubscribe = subscribeToPwaRefresh(onRefresh);
  expect(isPwaRefreshPending()).toBe(false);

  const first = registerPwaServiceWorker();
  const second = registerPwaServiceWorker();

  expect(first).toBe(mocks.updateServiceWorker);
  expect(second).toBe(first);
  expect(getPwaUpdateServiceWorker()).toBe(first);
  expect(mocks.registerSW).toHaveBeenCalledTimes(1);

  const options = mocks.registerSW.mock.calls[0][0];
  expect(options).toMatchObject({ immediate: true });
  expect(options.onOfflineReady).toBeUndefined();
  options.onNeedRefresh();
  expect(onRefresh).toHaveBeenCalledTimes(1);
  expect(isPwaRefreshPending()).toBe(true);

  unsubscribe();
});

test('activates a waiting worker when no authenticated controller is mounted', async () => {
  registerPwaServiceWorker();
  mocks.registerSW.mock.calls[0][0].onNeedRefresh();
  await Promise.resolve();

  expect(mocks.updateServiceWorker).toHaveBeenCalledWith(true);
});

test('tracks whether a mounted view owns PWA update presentation', () => {
  expect(hasPwaRefreshPresenter()).toBe(false);

  const unsubscribe = subscribeToPwaRefresh(() => {}, { presentsRefresh: true });
  expect(hasPwaRefreshPresenter()).toBe(true);

  unsubscribe();
  expect(hasPwaRefreshPresenter()).toBe(false);
});
