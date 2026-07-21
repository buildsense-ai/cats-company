import { formatEmptyTaskGreeting } from './empty-task-greeting';

describe('formatEmptyTaskGreeting', () => {
  it('greets the user by display name', () => {
    expect(formatEmptyTaskGreeting({ display_name: '本地预览', username: 'preview' }))
      .toBe('需要为您做什么，本地预览？');
  });

  it('falls back to username when the display name is unavailable', () => {
    expect(formatEmptyTaskGreeting({ username: 'Saturday' }))
      .toBe('需要为您做什么，Saturday？');
    expect(formatEmptyTaskGreeting({ display_name: '   ', username: 'Saturday' }))
      .toBe('需要为您做什么，Saturday？');
  });

  it('keeps a natural generic greeting when no user name is available', () => {
    expect(formatEmptyTaskGreeting(null)).toBe('需要为您做什么？');
  });
});
