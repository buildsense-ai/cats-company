import React from 'react';
import { act } from 'react';
import { createRoot } from 'react-dom/client';
import { afterEach, beforeEach, expect, test, vi } from 'vitest';

const pwaRegistrationMocks = vi.hoisted(() => {
  const state = {
    refreshListener: null,
    updateServiceWorker: vi.fn(),
  };
  return {
    state,
    getPwaUpdateServiceWorker: vi.fn(() => state.updateServiceWorker),
    subscribeToPwaRefresh: vi.fn((listener) => {
      state.refreshListener = listener;
      return () => {
        if (state.refreshListener === listener) state.refreshListener = null;
      };
    }),
  };
});

vi.mock('../pwa-registration', () => ({
  getPwaUpdateServiceWorker: pwaRegistrationMocks.getPwaUpdateServiceWorker,
  subscribeToPwaRefresh: pwaRegistrationMocks.subscribeToPwaRefresh,
}));

import PwaUpdateController from './pwa-update-controller';

let container;
let root;

beforeEach(() => {
  pwaRegistrationMocks.state.refreshListener = null;
  pwaRegistrationMocks.state.updateServiceWorker.mockClear();
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
});

afterEach(() => {
  act(() => root.unmount());
  container.remove();
  vi.clearAllMocks();
});

test('offers every app entry a user-controlled service worker update', () => {
  act(() => {
    root.render(<PwaUpdateController />);
  });

  expect(container.textContent).toBe('');
  act(() => pwaRegistrationMocks.state.refreshListener());

  const updateButton = Array.from(container.querySelectorAll('button'))
    .find((button) => button.textContent === '立即更新');
  expect(updateButton).toBeTruthy();

  act(() => {
    updateButton.dispatchEvent(new MouseEvent('click', { bubbles: true }));
  });

  expect(pwaRegistrationMocks.state.updateServiceWorker).toHaveBeenCalledWith(true);
});

test('allows the user to defer an update without removing the waiting worker', () => {
  act(() => {
    root.render(<PwaUpdateController />);
  });
  act(() => pwaRegistrationMocks.state.refreshListener());

  const laterButton = Array.from(container.querySelectorAll('button'))
    .find((button) => button.textContent === '稍后');
  act(() => {
    laterButton.dispatchEvent(new MouseEvent('click', { bubbles: true }));
  });

  expect(container.textContent).toBe('');
  expect(pwaRegistrationMocks.state.updateServiceWorker).not.toHaveBeenCalled();
});
