import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { afterEach, beforeEach, expect, test, vi } from 'vitest';

const mocks = vi.hoisted(() => ({
  getToken: vi.fn(() => ''),
  getAuthRevision: vi.fn(() => 1),
  getPushPromptOwner: vi.fn(() => ''),
  pwaController: vi.fn(),
  pushCleanupController: vi.fn(),
}));

vi.mock('react-dom/client', async () => {
  const actual = await vi.importActual('react-dom/client');
  return {
    ...actual,
    default: { createRoot: vi.fn(() => ({ render: vi.fn() })) },
  };
});

vi.mock('./api', () => ({
  getToken: mocks.getToken,
  getAuthRevision: mocks.getAuthRevision,
  getPushPromptOwner: mocks.getPushPromptOwner,
}));

vi.mock('./views/tinode-web', () => ({
  default: ({ location }) => <div data-testid="tinode-web">{location.pathname}</div>,
}));

vi.mock('./views/shared-conversation-view', () => ({
  default: ({ token }) => <div data-testid="shared-conversation-view">{token}</div>,
}));

vi.mock('./components/pwa-controller', () => ({
  default: (props) => {
    mocks.pwaController(props);
    return <div data-testid="pwa-controller" />;
  },
}));

vi.mock('./components/push-cleanup-controller', () => ({
  default: () => {
    mocks.pushCleanupController();
    return <div data-testid="push-cleanup-controller" />;
  },
}));

vi.mock('./components/feedback-system', () => ({
  FeedbackProvider: ({ children }) => children,
}));

vi.mock('./utils/theme-access', () => ({
  applyThemeAttributes: vi.fn(),
  THEME_STORAGE_KEY: 'theme',
}));

import { App } from './index';

let container;
let root;

beforeEach(() => {
  mocks.getToken.mockReturnValue('');
  mocks.getAuthRevision.mockReturnValue(1);
  mocks.getPushPromptOwner.mockReturnValue('');
  mocks.pwaController.mockClear();
  mocks.pushCleanupController.mockClear();
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
});

afterEach(async () => {
  await act(async () => root.unmount());
  container.remove();
  window.history.replaceState(null, '', '/');
});

test('keeps PWA runtime mounted for a signed-out non-authentication route', async () => {
  window.history.replaceState(null, '', '/e/invite-1');

  await act(async () => {
    root.render(<App />);
  });

  expect(container.querySelector('[data-testid="pwa-controller"]')).toBeTruthy();
  expect(mocks.pwaController).toHaveBeenCalledWith(expect.objectContaining({ loggedIn: false }));
});

test('opens a shared conversation when the copied URL has a trailing slash', async () => {
  window.history.replaceState(null, '', '/share/visitor-capability/');

  await act(async () => {
    root.render(<App />);
    await Promise.resolve();
  });

  expect(container.querySelector('[data-testid="shared-conversation-view"]')?.textContent)
    .toBe('visitor-capability');
  expect(container.querySelector('[data-testid="tinode-web"]')).toBeNull();
});
