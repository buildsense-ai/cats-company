import React from 'react';
import { act } from 'react';
import { createRoot } from 'react-dom/client';
import { afterEach, beforeEach, expect, test, vi } from 'vitest';

vi.mock('virtual:pwa-register', () => ({
  registerSW: vi.fn(() => vi.fn()),
}));

vi.mock('../api', () => ({
  api: {
    getPushConfig: vi.fn(),
    subscribePush: vi.fn(),
    unsubscribePush: vi.fn(),
  },
  getToken: vi.fn(() => ''),
  getPushRegistrationID: vi.fn(() => 'registration-1'),
}));

vi.mock('../utils/push-operation', () => ({
  enqueuePushOperation: vi.fn((operation) => operation()),
}));

vi.mock('../utils/push-tab-coordination', () => ({
  pushTabCoordinator: {
    setActive: vi.fn(),
    waitUntilActive: vi.fn(() => Promise.resolve(true)),
    onReconcile: vi.fn(() => () => {}),
    runWhenNoOtherActiveTabs: vi.fn((callback) => callback()),
  },
}));

import PwaController from './pwa-controller';
import { registerSW } from 'virtual:pwa-register';
import { api } from '../api';
import { pushTabCoordinator } from '../utils/push-tab-coordination';

let container;
let root;

beforeEach(() => {
  localStorage.clear();
  Object.defineProperty(window, 'isSecureContext', { configurable: true, value: true });
  Object.defineProperty(window, 'Notification', {
    configurable: true,
    value: { permission: 'default' },
  });
  Object.defineProperty(window, 'PushManager', { configurable: true, value: function PushManager() {} });
  Object.defineProperty(navigator, 'serviceWorker', { configurable: true, value: {} });
  Object.defineProperty(navigator, 'locks', {
    configurable: true,
    value: { request: vi.fn() },
  });
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
});

afterEach(() => {
  act(() => root.unmount());
  container.remove();
  vi.clearAllMocks();
});

function renderController(pushPromptOwner, sessionRevision = 1, loggedIn = true) {
  act(() => {
    root.render(
      <PwaController
        loggedIn={loggedIn}
        pushPromptOwner={pushPromptOwner}
        sessionRevision={sessionRevision}
      />,
    );
  });
}

test('shows the push prompt again when a different account signs in', () => {
  renderController('user:1');
  const dismiss = Array.from(container.querySelectorAll('button'))
    .find((button) => button.textContent === '暂不');
  expect(dismiss).toBeTruthy();

  act(() => {
    dismiss.dispatchEvent(new MouseEvent('click', { bubbles: true }));
  });
  expect(container.textContent).not.toContain('开启通知，及时收到新消息');

  renderController('user:2', 2);

  expect(container.textContent).toContain('开启通知，及时收到新消息');
  expect(localStorage.getItem('cc_push_prompt_dismissed_v1:user:1')).toBe('true');
  expect(localStorage.getItem('cc_push_prompt_dismissed_v1:user:2')).toBeNull();
});

test('registers an active tab under its push registration id', () => {
  window.Notification.permission = 'granted';

  renderController('user:42');

  expect(pushTabCoordinator.setActive).toHaveBeenCalledWith(true, 'registration-1');
});

test('waits for the active-tab lock before reconciling a browser subscription', async () => {
  let releaseActiveLock;
  window.Notification.permission = 'granted';
  api.getPushConfig.mockResolvedValue({ enabled: false });
  pushTabCoordinator.waitUntilActive.mockImplementationOnce(() => new Promise((resolve) => {
    releaseActiveLock = resolve;
  }));

  renderController('user:42');

  await vi.waitFor(() => expect(releaseActiveLock).toBeTypeOf('function'));
  expect(api.getPushConfig).not.toHaveBeenCalled();

  releaseActiveLock(true);
  await vi.waitFor(() => expect(api.getPushConfig).toHaveBeenCalledTimes(1));
});

test('re-registers an active account when another tab hands off the browser subscription', async () => {
  const subscription = {
    endpoint: 'https://push.example/subscription',
    keys: { p256dh: 'key', auth: 'auth' },
    options: { applicationServerKey: new Uint8Array([1, 2, 3, 4]) },
    toJSON() {
      return { endpoint: this.endpoint, keys: this.keys };
    },
  };
  window.Notification.permission = 'granted';
  Object.defineProperty(navigator, 'serviceWorker', {
    configurable: true,
    value: {
      ready: Promise.resolve({
        pushManager: { getSubscription: vi.fn().mockResolvedValue(subscription) },
      }),
    },
  });
  api.getPushConfig.mockResolvedValue({ enabled: true, public_key: 'AQIDBA' });
  api.subscribePush.mockResolvedValue({ subscribed: true });

  renderController('user:42');
  await vi.waitFor(() => expect(api.subscribePush).toHaveBeenCalledTimes(1));
  api.subscribePush.mockClear();

  const listener = pushTabCoordinator.onReconcile.mock.calls.at(-1)[0];
  act(() => listener());

  await vi.waitFor(() => expect(api.subscribePush).toHaveBeenCalledTimes(1));
});

test('retries a pending browser cleanup while signed out', async () => {
  const browserUnsubscribe = vi.fn().mockResolvedValue(true);
  localStorage.setItem('oc_push_pending_unsubscribe_v1', 'https://push.example/subscription');
  Object.defineProperty(navigator, 'serviceWorker', {
    configurable: true,
    value: {
      getRegistration: vi.fn().mockResolvedValue({
        pushManager: {
          getSubscription: vi.fn().mockResolvedValue({
            endpoint: 'https://push.example/subscription',
            unsubscribe: browserUnsubscribe,
          }),
        },
      }),
    },
  });

  renderController('', 1, false);

  await vi.waitFor(() => expect(browserUnsubscribe).toHaveBeenCalledTimes(1));
  expect(localStorage.getItem('oc_push_pending_unsubscribe_v1')).toBeNull();
});

test('updates through the service worker updater registered after mount', () => {
  renderController('user:1');
  expect(registerSW).toHaveBeenCalledTimes(1);

  const registrationOptions = registerSW.mock.calls[0][0];
  expect(registrationOptions.onOfflineReady).toBeUndefined();
  const updateServiceWorker = registerSW.mock.results[0].value;
  act(() => registrationOptions.onNeedRefresh());

  const update = Array.from(container.querySelectorAll('button'))
    .find((button) => button.textContent === '立即更新');
  expect(update).toBeTruthy();

  act(() => {
    update.dispatchEvent(new MouseEvent('click', { bubbles: true }));
  });
  expect(updateServiceWorker).toHaveBeenCalledWith(true);
});
