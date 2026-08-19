import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { afterEach, beforeEach, expect, test, vi } from 'vitest';

vi.mock('../auth-session', () => ({
  getToken: vi.fn(() => ''),
}));

vi.mock('../utils/push-operation', () => ({
  enqueuePushOperation: vi.fn((operation) => operation()),
}));

vi.mock('../utils/push-tab-coordination', () => ({
  pushTabCoordinator: {
    runWhenNoOtherActiveTabs: vi.fn((callback) => callback()),
  },
}));

import PushCleanupController from './push-cleanup-controller';

let container;
let root;

beforeEach(() => {
  localStorage.clear();
  Object.defineProperty(navigator, 'onLine', { configurable: true, value: true });
  Object.defineProperty(window, 'isSecureContext', { configurable: true, value: true });
  Object.defineProperty(window, 'Notification', {
    configurable: true,
    value: { requestPermission: vi.fn() },
  });
  Object.defineProperty(window, 'PushManager', { configurable: true, value: function PushManager() {} });
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
});

afterEach(() => {
  act(() => root.unmount());
  container.remove();
  vi.clearAllMocks();
});

test('retries a pending browser cleanup while signed out without rendering PWA UI', async () => {
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

  await act(async () => {
    root.render(<PushCleanupController />);
  });

  await vi.waitFor(() => expect(browserUnsubscribe).toHaveBeenCalledTimes(1));
  expect(localStorage.getItem('oc_push_pending_unsubscribe_v1')).toBeNull();
  expect(container.childElementCount).toBe(0);
});
