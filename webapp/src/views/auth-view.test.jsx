import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest';

vi.mock('../components/auth-flow-background', () => ({
  default: () => null,
}));

import { AuthView } from './auth-gateway';

describe('AuthView route links', () => {
  let container;
  let root;

  beforeEach(() => {
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(async () => {
    await act(async () => root.unmount());
    container.remove();
  });

  test('uses real direct routes and preserves a safe return path between login screens', async () => {
    const onNavigate = vi.fn();
    await act(async () => {
      root.render(
        <AuthView
          mode="login"
          nextPath="/e/invite-1"
          onNavigate={onNavigate}
          onLogin={vi.fn()}
          onRegister={vi.fn()}
        />,
      );
    });

    const register = Array.from(container.querySelectorAll('a'))
      .find((link) => link.textContent === '立即注册');
    const reset = Array.from(container.querySelectorAll('a'))
      .find((link) => link.textContent === '忘记密码？');

    expect(register?.getAttribute('href')).toBe('/register?next=%2Fe%2Finvite-1');
    expect(reset?.getAttribute('href')).toBe('/reset-password?next=%2Fe%2Finvite-1');

    await act(async () => register?.click());
    expect(onNavigate).toHaveBeenCalledWith('register');
  });

  test('offers a direct login URL from the password-reset screen', async () => {
    await act(async () => {
      root.render(
        <AuthView
          mode="reset"
          nextPath="/e/invite-1"
          onLogin={vi.fn()}
          onRegister={vi.fn()}
        />,
      );
    });

    const login = Array.from(container.querySelectorAll('a'))
      .find((link) => link.textContent === '返回登录');
    expect(login?.getAttribute('href')).toBe('/login?next=%2Fe%2Finvite-1');
    expect(container.querySelector('input[aria-label="邮箱地址"]')).toBeTruthy();
    expect(container.querySelector('input[aria-label="邮箱验证码"]')).toBeTruthy();
    expect(container.querySelector('input[aria-label="新密码（至少6位）"]')).toBeTruthy();
    expect(container.querySelector('input[aria-label="确认新密码"]')).toBeTruthy();
  });

  test('exposes the password reveal control as a named toggle button', async () => {
    await act(async () => {
      root.render(
        <AuthView
          mode="login"
          onLogin={vi.fn()}
          onRegister={vi.fn()}
        />,
      );
    });

    const passwordInput = container.querySelector('input[type="password"]');
    const toggle = container.querySelector('button[aria-label="显示密码"]');
    expect(toggle?.getAttribute('aria-pressed')).toBe('false');
    expect(toggle?.style.width).toBe('48px');
    expect(toggle?.style.height).toBe('48px');

    await act(async () => toggle?.click());

    expect(passwordInput?.getAttribute('type')).toBe('text');
    expect(container.querySelector('button[aria-label="隐藏密码"]')?.getAttribute('aria-pressed')).toBe('true');
  });

  test('provides programmatic labels and autocomplete hints for login and registration', async () => {
    await act(async () => {
      root.render(
        <AuthView
          mode="login"
          onLogin={vi.fn()}
          onRegister={vi.fn()}
        />,
      );
    });

    expect(container.querySelector('input[aria-label="用户名"]')?.getAttribute('autocomplete')).toBe('username');
    expect(container.querySelector('input[aria-label="密码"]')?.getAttribute('autocomplete')).toBe('current-password');

    await act(async () => {
      root.render(
        <AuthView
          mode="register"
          onLogin={vi.fn()}
          onRegister={vi.fn()}
        />,
      );
    });

    expect(container.querySelector('input[aria-label="邮箱地址"]')?.getAttribute('autocomplete')).toBe('email');
    expect(container.querySelector('input[aria-label="邮箱验证码"]')?.getAttribute('autocomplete')).toBe('one-time-code');
    expect(container.querySelector('input[aria-label="设置密码（至少6位）"]')?.getAttribute('autocomplete')).toBe('new-password');
  });

  test('preloads the workspace only after authentication succeeds', async () => {
    const events = [];
    const onAuthenticationIntent = vi.fn(() => events.push('preload'));
    const onLogin = vi.fn(async () => {
      events.push('login-start');
      await Promise.resolve();
      events.push('login-success');
    });
    await act(async () => {
      root.render(
        <AuthView
          mode="login"
          onAuthenticationIntent={onAuthenticationIntent}
          onLogin={onLogin}
          onRegister={vi.fn()}
        />,
      );
    });

    await act(async () => {
      container.querySelector('input[aria-label="用户名"]')?.focus();
    });
    expect(onAuthenticationIntent).not.toHaveBeenCalled();

    await act(async () => {
      container.querySelector('form')?.dispatchEvent(new Event('submit', {
        bubbles: true,
        cancelable: true,
      }));
      await Promise.resolve();
    });
    expect(onAuthenticationIntent).toHaveBeenCalledTimes(1);
    expect(events).toEqual(['login-start', 'login-success', 'preload']);
  });

  test('does not preload the workspace when authentication fails', async () => {
    const onAuthenticationIntent = vi.fn();
    await act(async () => {
      root.render(
        <AuthView
          mode="login"
          onAuthenticationIntent={onAuthenticationIntent}
          onLogin={vi.fn().mockRejectedValue(new Error('password mismatch'))}
          onRegister={vi.fn()}
        />,
      );
    });

    await act(async () => {
      container.querySelector('form')?.dispatchEvent(new Event('submit', {
        bubbles: true,
        cancelable: true,
      }));
      await Promise.resolve();
    });

    expect(onAuthenticationIntent).not.toHaveBeenCalled();
    expect(container.textContent).toContain('密码错误，请重试');
  });
});
