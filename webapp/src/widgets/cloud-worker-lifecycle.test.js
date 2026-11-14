import {
  expiryModeLabel,
  formatDate,
  isUrgentExpiry,
  lifecycleStateLabel,
  remainingLabel,
  remainingUntil,
} from './cloud-worker-lifecycle';

const NOW = Date.parse('2030-01-01T00:00:00Z');

describe('cloud worker lifecycle display', () => {
  it('splits a real deadline into days and hours', () => {
    const span = remainingUntil('2030-01-04T05:30:00Z', NOW);
    expect(span).toMatchObject({ known: true, expired: false, days: 3, hours: 5 });
    expect(remainingLabel(span)).toBe('还剩 3 天 5 小时');
  });

  it('never reports a negative remainder once the deadline has passed', () => {
    const span = remainingUntil('2029-12-25T00:00:00Z', NOW);
    expect(span).toMatchObject({ known: true, expired: true, days: 0, hours: 0, ms: 0 });
    expect(remainingLabel(span)).toBe('已过期');
    expect(remainingLabel(span)).not.toMatch(/-\d/);
  });

  it('keeps an unknown or unparseable deadline unknown instead of inventing one', () => {
    expect(remainingUntil(undefined, NOW).known).toBe(false);
    expect(remainingUntil('not-a-date', NOW).known).toBe(false);
    expect(remainingLabel(remainingUntil('not-a-date', NOW))).toBe('');
    expect(formatDate('')).toBe('');
    expect(formatDate('not-a-date')).toBe('');
  });

  it('formats a readable date', () => {
    expect(formatDate('2030-03-04T10:00:00Z')).toMatch(/^2030-03-0[45]$/);
  });

  it('marks only a remaining window below 7 days as urgent', () => {
    expect(isUrgentExpiry(remainingUntil('2030-01-06T00:00:00Z', NOW))).toBe(true);
    expect(isUrgentExpiry(remainingUntil('2030-01-10T00:00:00Z', NOW))).toBe(false);
    expect(isUrgentExpiry(remainingUntil('2029-12-01T00:00:00Z', NOW))).toBe(false);
    expect(isUrgentExpiry(remainingUntil(undefined, NOW))).toBe(false);
  });

  it('translates only lifecycle states the backend really sends', () => {
    expect(lifecycleStateLabel('active')).toBe('计费期内');
    expect(lifecycleStateLabel('DELETE_PENDING')).toBe('已在删除流程中');
    expect(lifecycleStateLabel('deleted')).toBe('已删除');
    expect(lifecycleStateLabel('some-internal-state')).toBe('');
    expect(lifecycleStateLabel(null)).toBe('');
  });

  it('describes the expiry mode the backend reports', () => {
    expect(expiryModeLabel('fixed')).toBe('按本机期限');
    expect(expiryModeLabel('follow_plan')).toBe('随套餐期限');
    expect(expiryModeLabel('permanent')).toBe('长期有效');
    expect(expiryModeLabel('legacy')).toBe('存量机器规则');
    expect(expiryModeLabel('unknown-mode')).toBe('');
    expect(expiryModeLabel(undefined)).toBe('');
  });
});
