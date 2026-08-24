import { registerSW } from 'virtual:pwa-register';

let updateServiceWorker = null;
let refreshPending = false;
const refreshListeners = new Set();

function notifyRefreshListeners() {
  for (const { listener } of refreshListeners) listener();
}

function handleNeedRefresh() {
  refreshPending = true;
  notifyRefreshListeners();
}

export function registerPwaServiceWorker() {
  if (updateServiceWorker) return updateServiceWorker;

  updateServiceWorker = registerSW({
    immediate: true,
    onNeedRefresh: handleNeedRefresh,
    onRegisterError: (error) => console.warn('PWA registration failed:', error),
  });
  return updateServiceWorker;
}

export function subscribeToPwaRefresh(listener, { presentsRefresh = false } = {}) {
  if (typeof listener !== 'function') return () => {};
  const subscription = { listener, presentsRefresh: Boolean(presentsRefresh) };
  refreshListeners.add(subscription);
  if (refreshPending) listener();
  return () => refreshListeners.delete(subscription);
}

export function isPwaRefreshPending() {
  return refreshPending;
}

export function hasPwaRefreshPresenter() {
  return Array.from(refreshListeners).some(({ presentsRefresh }) => presentsRefresh);
}

export function getPwaUpdateServiceWorker() {
  return updateServiceWorker;
}
