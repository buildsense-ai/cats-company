import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { afterEach, beforeEach, expect, test, vi } from 'vitest';

const mocks = vi.hoisted(() => ({
  getToken: vi.fn(() => ''),
  isTokenExpired: vi.fn(() => false),
  setToken: vi.fn(),
  getAuthRevision: vi.fn(() => 1),
  getPushPromptOwner: vi.fn(() => ''),
  readStoredUserProfile: vi.fn(() => null),
  clearStoredUserProfile: vi.fn(() => true),
  registerPwaServiceWorker: vi.fn(),
  getPwaUpdateServiceWorker: vi.fn(() => null),
  isPwaRefreshPending: vi.fn(() => false),
  subscribeToPwaRefresh: vi.fn(() => () => {}),
  pwaController: vi.fn(),
  pushCleanupController: vi.fn(),
  suspendWorkspace: false,
  workspaceSuspense: new Promise(() => {}),
  workspaceError: null,
}));

vi.mock('react-dom/client', async () => {
  const actual = await vi.importActual('react-dom/client');
  return {
    ...actual,
    default: { createRoot: vi.fn(() => ({ render: vi.fn() })) },
  };
});

vi.mock('./auth-session', () => ({
  getToken: mocks.getToken,
  isTokenExpired: mocks.isTokenExpired,
  setToken: mocks.setToken,
  getAuthRevision: mocks.getAuthRevision,
  getPushPromptOwner: mocks.getPushPromptOwner,
}));

vi.mock('virtual:pwa-register', () => ({
  registerSW: vi.fn(() => vi.fn()),
}));

vi.mock('./pwa-registration', () => ({
  getPwaUpdateServiceWorker: mocks.getPwaUpdateServiceWorker,
  isPwaRefreshPending: mocks.isPwaRefreshPending,
  registerPwaServiceWorker: mocks.registerPwaServiceWorker,
  subscribeToPwaRefresh: mocks.subscribeToPwaRefresh,
}));

vi.mock('./views/tinode-web', () => ({
  default: ({ location }) => {
    if (mocks.workspaceError) throw mocks.workspaceError;
    if (mocks.suspendWorkspace) throw mocks.workspaceSuspense;
    return <div data-testid="tinode-web">{location.pathname}</div>;
  },
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
  clearStoredUserProfile: mocks.clearStoredUserProfile,
  readStoredUserProfile: mocks.readStoredUserProfile,
}));

import {
  App,
  isWorkspaceChunkLoadError,
  PwaLoadErrorBoundary,
  WorkspaceLoadErrorBoundary,
  WorkspaceLoadFailure,
  workspaceLoadFailureState,
} from './index';

let container;
let root;
let pwaRefreshListener;

beforeEach(() => {
  mocks.getToken.mockReturnValue('');
  mocks.isTokenExpired.mockReturnValue(false);
  mocks.getAuthRevision.mockReturnValue(1);
  mocks.getPushPromptOwner.mockReturnValue('');
  mocks.readStoredUserProfile.mockReturnValue(null);
  mocks.setToken.mockClear();
  mocks.clearStoredUserProfile.mockClear();
  mocks.registerPwaServiceWorker.mockClear();
  mocks.getPwaUpdateServiceWorker.mockReset().mockReturnValue(null);
  mocks.isPwaRefreshPending.mockReset().mockReturnValue(false);
  mocks.subscribeToPwaRefresh.mockReset().mockReturnValue(() => {});
  mocks.pwaController.mockClear();
  mocks.pushCleanupController.mockClear();
  mocks.suspendWorkspace = false;
  mocks.workspaceError = null;
  pwaRefreshListener = null;
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
});

afterEach(async () => {
  await act(async () => root.unmount());
  container.remove();
  window.history.replaceState(null, '', '/');
});

test('does not register the PWA before authentication on the anonymous app shell', async () => {
  window.history.replaceState(null, '', '/');

  await act(async () => {
    root.render(<App />);
  });

  expect(mocks.registerPwaServiceWorker).not.toHaveBeenCalled();
  expect(container.querySelector('[data-testid="pwa-controller"]')).toBeFalsy();
  expect(mocks.pwaController).not.toHaveBeenCalled();
});

test('does not register the PWA before authentication on an anonymous deep link', async () => {
  window.history.replaceState(null, '', '/e/invite-1');

  await act(async () => {
    root.render(<App />);
  });

  expect(mocks.registerPwaServiceWorker).not.toHaveBeenCalled();
});

test('loads the PWA runtime for a restorable authenticated session', async () => {
  mocks.getToken.mockReturnValue('active-session-token');
  mocks.readStoredUserProfile.mockReturnValue({ uid: 42, username: 'cats' });
  window.history.replaceState(null, '', '/e/invite-1');

  await act(async () => {
    root.render(<App />);
  });

  await vi.waitFor(() => {
    expect(container.querySelector('[data-testid="pwa-controller"]')).toBeTruthy();
  });
  expect(mocks.registerPwaServiceWorker).toHaveBeenCalledTimes(1);
  expect(mocks.pwaController).toHaveBeenCalledWith(expect.objectContaining({ loggedIn: true }));
});

test('registers the PWA for a standalone route without requiring authentication', async () => {
  window.history.replaceState(null, '', '/mobile-upload/session-1');

  await act(async () => {
    root.render(<App />);
  });

  expect(mocks.registerPwaServiceWorker).toHaveBeenCalledTimes(1);
});

test('loads the workspace to recover a token whose profile cache is missing', async () => {
  mocks.getToken.mockReturnValue('stale-session-token');
  mocks.readStoredUserProfile.mockReturnValue(null);

  await act(async () => {
    root.render(<App />);
  });

  expect(container.querySelector('[data-testid="auth-gateway"]')).toBeFalsy();
  expect(container.querySelector('[data-testid="tinode-web"]')).toBeTruthy();
  expect(mocks.setToken).not.toHaveBeenCalled();
  expect(mocks.clearStoredUserProfile).not.toHaveBeenCalled();
});

test('clears an expired token before it can load the workspace or redirect a deep link', async () => {
  mocks.getToken.mockReturnValue('expired-session-token');
  mocks.isTokenExpired.mockReturnValue(true);
  mocks.readStoredUserProfile.mockReturnValue({ uid: 42, username: 'cats' });
  window.history.replaceState(null, '', '/e/invite-1');

  await act(async () => {
    root.render(<App />);
  });

  expect(container.querySelector('[data-testid="auth-gateway"]')).toBeTruthy();
  expect(container.querySelector('[data-testid="tinode-web"]')).toBeFalsy();
  expect(mocks.setToken).toHaveBeenCalledWith(null);
  expect(mocks.clearStoredUserProfile).toHaveBeenCalledTimes(1);
});

test('clears a profile that remains after the token is removed', async () => {
  mocks.readStoredUserProfile.mockReturnValue({ uid: 42, username: 'cats' });

  await act(async () => {
    root.render(<App />);
  });

  expect(container.querySelector('[data-testid="auth-gateway"]')).toBeTruthy();
  expect(mocks.clearStoredUserProfile).toHaveBeenCalledTimes(1);
  expect(mocks.setToken).not.toHaveBeenCalled();
});

test('preserves a token on an auth event while its profile is being recovered', async () => {
  await act(async () => {
    root.render(<App />);
  });

  mocks.getToken.mockReturnValue('stale-session-token');
  await act(async () => {
    window.dispatchEvent(new CustomEvent('cc:auth-changed', {
      detail: { loggedIn: true, revision: 2 },
    }));
  });

  expect(container.querySelector('[data-testid="auth-gateway"]')).toBeFalsy();
  expect(container.querySelector('[data-testid="tinode-web"]')).toBeTruthy();
  expect(mocks.setToken).not.toHaveBeenCalled();
});

test('keeps the authentication gateway visible while the workspace entry is pending', async () => {
  await act(async () => {
    root.render(<App />);
  });

  mocks.suspendWorkspace = true;
  mocks.getToken.mockReturnValue('active-session-token');
  mocks.readStoredUserProfile.mockReturnValue({ uid: 42, username: 'cats' });
  await act(async () => {
    window.dispatchEvent(new CustomEvent('cc:auth-changed', {
      detail: { loggedIn: true, revision: 2 },
    }));
    await Promise.resolve();
  });

  await vi.waitFor(() => {
    expect(container.querySelector('[data-testid="auth-gateway"]')).toBeTruthy();
  });
  expect(container.querySelector('[data-testid="tinode-web"]')).toBeFalsy();
  expect(container.querySelector('.cc-workspace-loading')).toBeFalsy();
});

test('shows the existing workspace retry state if the post-login chunk fails', async () => {
  const consoleError = vi.spyOn(console, 'error').mockImplementation(() => {});
  try {
    await act(async () => {
      root.render(<App />);
    });

    mocks.workspaceError = new TypeError('Failed to fetch dynamically imported module');
    mocks.getToken.mockReturnValue('active-session-token');
    await act(async () => {
      window.dispatchEvent(new CustomEvent('cc:auth-changed', {
        detail: { loggedIn: true, revision: 2 },
      }));
    });

    await vi.waitFor(() => {
      expect(container.querySelector('[role="alert"]')?.textContent).toContain('工作台资源暂时无法加载');
    });
    expect(container.querySelector('[data-testid="auth-gateway"]')).toBeFalsy();
  } finally {
    consoleError.mockRestore();
  }
});

test('identifies recoverable workspace chunk failures without masking application errors', () => {
  expect(isWorkspaceChunkLoadError(new TypeError('Failed to fetch dynamically imported module'))).toBe(true);
  expect(isWorkspaceChunkLoadError(new Error('Loading chunk 42 failed.'))).toBe(true);
  expect(isWorkspaceChunkLoadError(new Error('Unable to preload CSS for /assets/tinode-web.css'))).toBe(true);
  expect(isWorkspaceChunkLoadError(new Error('Unexpected application error'))).toBe(false);
});

test('separates observed offline and version-update states from unknown workspace failures', () => {
  expect(workspaceLoadFailureState({ online: false })).toEqual({
    kind: 'offline',
    message: '当前无网络连接，连接网络后再试。',
    retryLabel: '重新载入',
  });
  expect(workspaceLoadFailureState({ online: true, updateAvailable: true })).toEqual({
    kind: 'update_available',
    message: '检测到新版本，立即更新以继续使用工作台。',
    retryLabel: '立即更新',
  });
  expect(workspaceLoadFailureState({ online: false, updateAvailable: true })).toEqual({
    kind: 'update_available',
    message: '检测到新版本，立即更新以继续使用工作台。',
    retryLabel: '立即更新',
  });
  expect(workspaceLoadFailureState({ online: true })).toEqual({
    kind: 'unavailable',
    message: '工作台资源暂时无法加载，请重新载入。',
    retryLabel: '重新载入',
  });
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

    expect(container.querySelector('[role="alert"]')?.textContent).toContain('工作台资源暂时无法加载');

    await act(async () => {
      root.render(<WorkspaceLoadFailure onRetry={onRetry} />);
    });
    const retry = Array.from(container.querySelectorAll('button'))
      .find((button) => button.textContent === '重新载入');
    await act(async () => retry?.click());

    expect(onRetry).toHaveBeenCalledTimes(1);
  } finally {
    consoleError.mockRestore();
  }
});

test('offers the update action when the service worker detects a new version', async () => {
  const onRetry = vi.fn();
  const updateServiceWorker = vi.fn();
  mocks.getPwaUpdateServiceWorker.mockReturnValue(updateServiceWorker);
  mocks.subscribeToPwaRefresh.mockImplementation((listener) => {
    pwaRefreshListener = listener;
    return () => {};
  });

  await act(async () => {
    root.render(<WorkspaceLoadFailure onRetry={onRetry} />);
  });

  expect(container.querySelector('[role="alert"]')?.textContent).toBe(
    '工作台资源暂时无法加载，请重新载入。',
  );

  await act(async () => pwaRefreshListener());

  expect(container.querySelector('[role="alert"]')?.textContent).toBe(
    '检测到新版本，立即更新以继续使用工作台。',
  );
  const update = Array.from(container.querySelectorAll('button'))
    .find((button) => button.textContent === '立即更新');
  await act(async () => update?.click());

  expect(updateServiceWorker).toHaveBeenCalledWith(true);
  expect(onRetry).not.toHaveBeenCalled();
});

test('keeps the application mounted when the optional PWA chunk fails to load', async () => {
  const consoleError = vi.spyOn(console, 'error').mockImplementation(() => {});
  const FailedPwa = () => {
    throw new TypeError('Failed to fetch dynamically imported module');
  };

  try {
    await act(async () => {
      root.render(
        <>
          <div data-testid="workspace-still-visible" />
          <PwaLoadErrorBoundary>
            <FailedPwa />
          </PwaLoadErrorBoundary>
        </>,
      );
    });

    expect(container.querySelector('[data-testid="workspace-still-visible"]')).toBeTruthy();
  } finally {
    consoleError.mockRestore();
  }
});
