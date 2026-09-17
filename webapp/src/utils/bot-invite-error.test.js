import { describeBotInviteCodeError } from './bot-invite-error';

describe('describeBotInviteCodeError', () => {
  it.each([
    undefined,
    new Error('bot invite code is invalid or expired'),
    new Error('Invalid or unavailable bot invite code'),
  ])('maps invalid invite errors to the user-facing message', (error) => {
    expect(describeBotInviteCodeError(error)).toBe('邀请码无效或已失效');
  });

  it('preserves actionable server errors', () => {
    expect(describeBotInviteCodeError(new Error('邀请码已达到使用次数上限'))).toBe('邀请码已达到使用次数上限');
  });
});
