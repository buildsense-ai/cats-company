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
  });
});
