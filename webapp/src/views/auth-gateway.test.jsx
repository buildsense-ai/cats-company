import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest';

const mocks = vi.hoisted(() => ({
  login: vi.fn(),
  register: vi.fn(),
  getToken: vi.fn(() => ''),
  isTokenExpired: vi.fn(() => false),
  setToken: vi.fn(),
  navigateBrowserPath: vi.fn(),
}));

vi.mock('../auth-session', () => ({
  authApi: {
    login: mocks.login,
    register: mocks.register,
    sendVerificationCode: vi.fn(),
  },
  getToken: mocks.getToken,
  isTokenExpired: mocks.isTokenExpired,
  setToken: mocks.setToken,
}));

vi.mock('../utils/auth-routes', () => ({
  authModeForPathname: vi.fn((pathname) => (pathname === '/register' ? 'register' : 'login')),
  authPathForMode: vi.fn((mode) => `/${mode}`),
  authenticationRedirectPath: vi.fn(() => null),
  navigateBrowserPath: mocks.navigateBrowserPath,
  nameOnboardingPathForNext: vi.fn((nextPath) => (
    nextPath === '/' ? '/onboarding/name' : `/onboarding/name?next=${encodeURIComponent(nextPath)}`
  )),
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
    mocks.register.mockReset();
    mocks.getToken.mockReturnValue('');
    mocks.isTokenExpired.mockReturnValue(false);
    mocks.setToken.mockReset();
    mocks.navigateBrowserPath.mockReset();
    storageWrite = null;
    localStorage.clear();
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(async () => {
    await act(async () => root.unmount());
    container.remove();
    storageWrite?.mockRestore();
    localStorage.clear();
  });

  test('establishes a valid session when the profile cache cannot be written', async () => {
    storageWrite = vi.spyOn(globalThis.localStorage, 'setItem')
      .mockImplementation((key) => {
        if (key === 'oc_user') throw new Error('storage quota exceeded');
      });
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

  test('keeps the registration timestamp and enters name onboarding after signup', async () => {
    mocks.register.mockResolvedValue({});
    mocks.login.mockResolvedValue({
      token: 'session-token',
      uid: 42,
      username: 'new-user',
      email: 'new@example.com',
      created_at: '2026-09-20T08:30:00Z',
    });

    await act(async () => {
      root.render(<AuthGateway location={{ pathname: '/register', search: '', hash: '' }} />);
    });

    const setInputValue = (label, value) => {
      const input = container.querySelector(`[aria-label="${label}"]`);
      const valueSetter = Object.getOwnPropertyDescriptor(
        window.HTMLInputElement.prototype,
        'value',
      ).set;
      valueSetter.call(input, value);
      input.dispatchEvent(new Event('input', { bubbles: true }));
    };
    await act(async () => {
      setInputValue('邮箱地址', 'new@example.com');
      setInputValue('邮箱验证码', '123456');
      setInputValue('设置密码（至少6位）', 'secret123');
    });
    await act(async () => {
      container.querySelector('form').dispatchEvent(new Event('submit', {
        bubbles: true,
        cancelable: true,
      }));
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(mocks.register).toHaveBeenCalledWith({
      email: 'new@example.com',
      password: 'secret123',
      code: '123456',
      bot_invite_code: '',
    });
    expect(mocks.navigateBrowserPath).toHaveBeenCalledWith('/onboarding/name', { replace: true });
    expect(JSON.parse(localStorage.getItem('oc_user'))).toMatchObject({
      uid: 42,
      created_at: '2026-09-20T08:30:00Z',
    });
  });
});
