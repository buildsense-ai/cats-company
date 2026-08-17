import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { afterEach, beforeEach, expect, test, vi } from 'vitest';

const mocks = vi.hoisted(() => ({
  getToken: vi.fn(() => ''),
  setToken: vi.fn(),
  getAuthRevision: vi.fn(() => 1),
  getPushPromptOwner: vi.fn(() => ''),
  readStoredUserProfile: vi.fn(() => null),
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
  setToken: mocks.setToken,
  getAuthRevision: mocks.getAuthRevision,
  getPushPromptOwner: mocks.getPushPromptOwner,
}));

vi.mock('./views/tinode-web', () => ({
  default: ({ location }) => <div data-testid="tinode-web">{location.pathname}</div>,
}));

vi.mock('./views/auth-gateway', () => ({
  default: () => <div data-testid="auth-gateway" />,
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
  applyDocumentTheme: vi.fn(),
  THEME_STORAGE_KEY: 'theme',
}));

vi.mock('./utils/user-profile', () => ({
  readStoredUserProfile: mocks.readStoredUserProfile,
  USER_PROFILE_STORAGE_KEY: 'oc_user',
}));

import {
  App,
  isWorkspaceChunkLoadError,
  WorkspaceLoadErrorBoundary,
  WorkspaceLoadFailure,
} from './index';

let container;
let root;

beforeEach(() => {
  mocks.getToken.mockReturnValue('');
  mocks.getAuthRevision.mockReturnValue(1);
  mocks.getPushPromptOwner.mockReturnValue('');
  mocks.readStoredUserProfile.mockReturnValue(null);
  mocks.setToken.mockClear();
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

test('clears an orphaned token and renders the authentication gateway', async () => {
  mocks.getToken.mockReturnValue('stale-session-token');
  mocks.readStoredUserProfile.mockReturnValue(null);

  await act(async () => {
    root.render(<App />);
  });

  expect(container.querySelector('[data-testid="auth-gateway"]')).toBeTruthy();
  expect(container.querySelector('[data-testid="tinode-web"]')).toBeFalsy();
  expect(mocks.setToken).toHaveBeenCalledWith(null);
});

test('does not load the workspace when an auth event has no restorable profile', async () => {
  await act(async () => {
    root.render(<App />);
  });

  mocks.getToken.mockReturnValue('stale-session-token');
  await act(async () => {
    window.dispatchEvent(new CustomEvent('cc:auth-changed', {
      detail: { loggedIn: true, revision: 2 },
    }));
  });

  expect(container.querySelector('[data-testid="auth-gateway"]')).toBeTruthy();
  expect(container.querySelector('[data-testid="tinode-web"]')).toBeFalsy();
  expect(mocks.setToken).toHaveBeenCalledWith(null);
});

test('identifies recoverable workspace chunk failures without masking application errors', () => {
  expect(isWorkspaceChunkLoadError(new TypeError('Failed to fetch dynamically imported module'))).toBe(true);
  expect(isWorkspaceChunkLoadError(new Error('Loading chunk 42 failed.'))).toBe(true);
  expect(isWorkspaceChunkLoadError(new Error('Unexpected application error'))).toBe(false);
});

test('shows a retry action when the workspace chunk fails to load', async () => {
  const onRetry = vi.fn();
  const consoleError = vi.spyOn(console, 'error').mockImplementation(() => {});
  const FailedWorkspace = () => {
    throw new TypeError('Failed to fetch dynamically imported module');
  };

  try {
    await act(async () => {
      root.render(
        <WorkspaceLoadErrorBoundary>
          <FailedWorkspace />
        </WorkspaceLoadErrorBoundary>,
      );
    });

    expect(container.querySelector('[role="alert"]')?.textContent).toContain('工作台加载失败');

    await act(async () => {
      root.render(<WorkspaceLoadFailure onRetry={onRetry} />);
    });
    const retry = Array.from(container.querySelectorAll('button'))
      .find((button) => button.textContent === '重新加载');
    await act(async () => retry?.click());

    expect(onRetry).toHaveBeenCalledTimes(1);
  } finally {
    consoleError.mockRestore();
  }
});
