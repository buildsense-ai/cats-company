import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest';

const mocks = vi.hoisted(() => ({
  login: vi.fn(),
  getToken: vi.fn(() => ''),
  isTokenExpired: vi.fn(() => false),
  setToken: vi.fn(),
  navigateBrowserPath: vi.fn(),
}));

vi.mock('../auth-session', () => ({
  authApi: {
    login: mocks.login,
    register: vi.fn(),
    sendVerificationCode: vi.fn(),
  },
  getToken: mocks.getToken,
  isTokenExpired: mocks.isTokenExpired,
  setToken: mocks.setToken,
}));

vi.mock('../utils/auth-routes', () => ({
  authModeForPathname: vi.fn(() => 'login'),
  authPathForMode: vi.fn((mode) => `/${mode}`),
  authenticationRedirectPath: vi.fn(() => null),
  navigateBrowserPath: mocks.navigateBrowserPath,
  postAuthenticationPathFromSearch: vi.fn(() => '/'),
}));

vi.mock('../components/auth-flow-background', () => ({
  default: () => null,
}));

vi.mock('../components/feedback-system', () => ({
  InlineFeedback: ({ children }) => <div role="alert">{children}</div>,
}));

vi.mock('../widgets/password-reset-form', () => ({
  default: () => null,
}));

vi.mock('../i18n', () => ({
  default: (key) => (key === 'username' ? '用户名' : '密码'),
}));

import AuthGateway from './auth-gateway';

describe('AuthGateway login', () => {
  let container;
  let root;
  let storageWrite;

  beforeEach(() => {
    mocks.login.mockReset();
    mocks.getToken.mockReturnValue('');
    mocks.isTokenExpired.mockReturnValue(false);
    mocks.setToken.mockReset();
    mocks.navigateBrowserPath.mockReset();
    storageWrite = vi.spyOn(globalThis.localStorage, 'setItem')
      .mockImplementation((key) => {
        if (key === 'oc_user') throw new Error('storage quota exceeded');
      });
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(async () => {
    await act(async () => root.unmount());
    container.remove();
    storageWrite.mockRestore();
  });

  test('establishes a valid session when the profile cache cannot be written', async () => {
    mocks.login.mockResolvedValue({ token: 'session-token', uid: 42, username: 'cats' });

    await act(async () => {
      root.render(<AuthGateway location={{ pathname: '/login', search: '', hash: '' }} />);
    });

    await act(async () => {
      container.querySelector('form')?.dispatchEvent(new Event('submit', {
        bubbles: true,
        cancelable: true,
      }));
      await Promise.resolve();
    });

    expect(storageWrite).toHaveBeenCalledWith('oc_user', expect.any(String));
    expect(mocks.setToken).toHaveBeenCalledWith('session-token');
    expect(mocks.navigateBrowserPath).toHaveBeenCalledWith('/', { replace: true });
    expect(container.querySelector('[role="alert"]')).toBeFalsy();
  });
});
