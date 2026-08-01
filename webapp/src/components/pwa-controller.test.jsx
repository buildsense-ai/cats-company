import React from 'react';
import { act } from 'react';
import { createRoot } from 'react-dom/client';
import { afterEach, beforeEach, expect, test, vi } from 'vitest';

vi.mock('virtual:pwa-register', () => ({
  registerSW: vi.fn(() => vi.fn()),
}));

vi.mock('../api', () => ({
  api: {},
  getPushRegistrationID: vi.fn(() => 'registration-1'),
}));

vi.mock('../utils/push-operation', () => ({
  enqueuePushOperation: vi.fn((operation) => operation()),
}));

vi.mock('../utils/push-tab-coordination', () => ({
  pushTabCoordinator: { setActive: vi.fn() },
}));

import PwaController from './pwa-controller';

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
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
});

afterEach(() => {
  act(() => root.unmount());
  container.remove();
  vi.clearAllMocks();
});

function renderController(pushPromptOwner, sessionRevision = 1) {
  act(() => {
    root.render(
      <PwaController
        loggedIn
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
