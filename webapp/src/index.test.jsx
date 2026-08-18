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
} from './index';

let container;
let root;

beforeEach(() => {
  mocks.getToken.mockReturnValue('');
  mocks.isTokenExpired.mockReturnValue(false);
  mocks.getAuthRevision.mockReturnValue(1);
  mocks.getPushPromptOwner.mockReturnValue('');
  mocks.readStoredUserProfile.mockReturnValue(null);
  mocks.setToken.mockClear();
  mocks.clearStoredUserProfile.mockClear();
  mocks.pwaController.mockClear();
  mocks.pushCleanupController.mockClear();
  mocks.suspendWorkspace = false;
  mocks.workspaceError = null;
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
});

afterEach(async () => {
  await act(async () => root.unmount());
  container.remove();
  window.history.replaceState(null, '', '/');
});

test('does not load the PWA runtime before authentication on a non-authentication route', async () => {
  window.history.replaceState(null, '', '/e/invite-1');

  await act(async () => {
    root.render(<App />);
  });

  expect(container.querySelector('[data-testid="pwa-controller"]')).toBeFalsy();
  expect(mocks.pwaController).not.toHaveBeenCalled();
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
  expect(mocks.pwaController).toHaveBeenCalledWith(expect.objectContaining({ loggedIn: true }));
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
      expect(container.querySelector('[role="alert"]')?.textContent).toContain('工作台加载失败');
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
