export function describeBotInviteCodeError(error) {
  const message = String(error?.message || '').trim();
  if (
    !message
    || /bot invite code is invalid or expired/i.test(message)
    || /invalid or unavailable bot invite code/i.test(message)
  ) {
    return '邀请码无效或已失效';
  }
  return message;
}
