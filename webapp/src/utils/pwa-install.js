const STATE_KEY = '__CATSCO_PWA_INSTALL__';

function isStandalone() {
  return navigator.standalone === true
    || window.matchMedia?.('(display-mode: standalone)')?.matches === true;
}

function isIOS() {
  const userAgent = navigator.userAgent || '';
  return /iPad|iPhone|iPod/.test(userAgent)
    || (navigator.platform === 'MacIntel' && navigator.maxTouchPoints > 1);
}

function installState() {
  if (!globalThis[STATE_KEY]) {
    globalThis[STATE_KEY] = {
      started: false,
      deferredPrompt: null,
      installed: isStandalone(),
      listeners: new Set(),
    };
  }
  return globalThis[STATE_KEY];
}

function snapshot(state = installState()) {
  return {
    installed: state.installed,
    canPrompt: Boolean(state.deferredPrompt) && !state.installed,
    requiresManualIOSInstall: isIOS() && !state.installed,
  };
}

function publish(state = installState()) {
  const next = snapshot(state);
  state.listeners.forEach((listener) => listener(next));
}

export function startPwaInstallLifecycle() {
  const state = installState();
  if (state.started) return;
  state.started = true;

  window.addEventListener('beforeinstallprompt', (event) => {
    event.preventDefault();
    state.deferredPrompt = event;
    publish(state);
  });
  window.addEventListener('appinstalled', () => {
    state.deferredPrompt = null;
    state.installed = true;
    publish(state);
  });
}

export function getPwaInstallState() {
  return snapshot();
}

export function subscribePwaInstall(listener) {
  const state = installState();
  state.listeners.add(listener);
  return () => state.listeners.delete(listener);
}

export async function promptPwaInstall() {
  const state = installState();
  const deferredPrompt = state.deferredPrompt;
  if (!deferredPrompt || state.installed) return 'unavailable';
  await deferredPrompt.prompt();
  const choice = await deferredPrompt.userChoice;
  state.deferredPrompt = null;
  if (choice?.outcome === 'accepted') state.installed = true;
  publish(state);
  return choice?.outcome || 'dismissed';
}
