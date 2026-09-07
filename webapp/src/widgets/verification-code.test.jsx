import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';

vi.mock('../auth-session', () => ({
  authApi: { sendVerificationCode: vi.fn(), sendPasswordResetCode: vi.fn() },
  getToken: vi.fn(),
  isTokenExpired: vi.fn(),
  setToken: vi.fn(),
}));
vi.mock('../components/auth-flow-background', () => ({ default: () => null }));

import { authApi } from '../auth-session';
import { AuthView } from '../views/auth-gateway';
import PasswordResetForm from './password-reset-form';

describe.each([
  ['registration', () => <AuthView mode="register" />, 'sendVerificationCode'],
  ['password reset', () => <PasswordResetForm />, 'sendPasswordResetCode'],
])('%s verification request', (_, renderForm, apiName) => {
  let root;
  let container;

  beforeEach(() => {
    global.IS_REACT_ACT_ENVIRONMENT = true;
    authApi[apiName].mockReset();
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
  });
  afterEach(async () => {
    await act(async () => root.unmount());
    container.remove();
  });

  it('locks immediately, blocks repeat requests, and restores sending after an error', async () => {
    let reject;
    authApi[apiName].mockImplementationOnce(() => new Promise((_, fail) => { reject = fail; }));
    await act(async () => root.render(renderForm()));
    const email = container.querySelector('input[type="email"]');
    await act(async () => Simulate.change(email, { target: { value: 'review@example.com' } }));
    const button = [...container.querySelectorAll('button')].find((node) => node.textContent === '发送验证码');
    await act(async () => {
      Simulate.click(button);
      Simulate.click(button);
    });
    expect(authApi[apiName]).toHaveBeenCalledOnce();
    expect(button.disabled).toBe(true);
    expect(button.textContent).toBe('发送中...');
    expect(button.getAttribute('aria-busy')).toBe('true');
    await act(async () => reject(new Error('请求暂时失败')));
    expect(container.textContent).toContain('请求暂时失败');
    expect(button.disabled).toBe(false);
    expect(button.textContent).toBe('发送验证码');
    authApi[apiName].mockResolvedValueOnce({});
    await act(async () => Simulate.click(button));
    expect(authApi[apiName]).toHaveBeenCalledTimes(2);
    expect(button.disabled).toBe(true);
    expect(button.textContent).toBe('60秒');
    expect(container.textContent).not.toContain('请求暂时失败');
  });
});
