import { describe, expect, test } from 'vitest';
import { formatSharedAuthError } from './auth-error';

describe('formatSharedAuthError', () => {
  test.each([
    ['verification code expired, please request a new one', '验证码已过期，请重新获取'],
    ['verification code does not match, please use the latest one', '验证码不正确，请使用最新邮件中的验证码'],
    ['invalid or expired verification code', '验证码无效或已过期，请重新获取并使用最新验证码'],
    ['password min 6 chars', '密码至少 6 位'],
    ['failed to send verification code', '发送验证码失败，请稍后再试'],
  ])('maps %s', (message, expected) => {
    expect(formatSharedAuthError(message)).toBe(expected);
  });

  test('matches server messages case-insensitively and leaves unknown errors alone', () => {
    expect(formatSharedAuthError('PASSWORD MIN 6 CHARS')).toBe('密码至少 6 位');
    expect(formatSharedAuthError('database unavailable')).toBe('');
  });
});
