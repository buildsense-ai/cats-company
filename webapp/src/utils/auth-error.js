const SHARED_AUTH_ERROR_MESSAGES = Object.freeze([
  ['verification code expired', '验证码已过期，请重新获取'],
  ['does not match', '验证码不正确，请使用最新邮件中的验证码'],
  ['invalid or expired verification code', '验证码无效或已过期，请重新获取并使用最新验证码'],
  ['password min 6', '密码至少 6 位'],
  ['failed to send verification code', '发送验证码失败，请稍后再试'],
]);

export function formatSharedAuthError(message) {
  const text = String(message || '').toLowerCase();
  return SHARED_AUTH_ERROR_MESSAGES.find(([phrase]) => text.includes(phrase))?.[1] || '';
}
