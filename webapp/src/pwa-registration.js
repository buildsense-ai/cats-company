import { registerSW } from 'virtual:pwa-register';

let updateServiceWorker = null;
let refreshPending = false;
const refreshListeners = new Set();

function notifyRefreshListeners() {
  for (const listener of refreshListeners) listener();
}

function handleNeedRefresh() {
  refreshPending = true;
  if (refreshListeners.size > 0) {
    notifyRefreshListeners();
    return;
  }

  // Preserve the existing immediate activation behavior when no authenticated
  // PWA controller is mounted to present the update state.
  Promise.resolve().then(() => {
    if (!refreshPending || refreshListeners.size > 0) return;
    updateServiceWorker?.(true);
  });
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

export function subscribeToPwaRefresh(listener) {
  if (typeof listener !== 'function') return () => {};
  refreshListeners.add(listener);
  if (refreshPending) listener();
  return () => refreshListeners.delete(listener);
}

export function getPwaUpdateServiceWorker() {
  return updateServiceWorker;
}
