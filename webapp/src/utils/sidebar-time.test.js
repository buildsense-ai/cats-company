import { describe, expect, it } from 'vitest';
import { formatSidebarTime } from './sidebar-time';

describe('formatSidebarTime', () => {
  const now = new Date(2026, 6, 21, 18, 30);

  it('shows the exact local time for messages sent today', () => {
    expect(formatSidebarTime(new Date(2026, 6, 21, 8, 5), now)).toBe('08:05');
    expect(formatSidebarTime(new Date(2026, 6, 21, 23, 59), now)).toBe('23:59');
  });

  it('uses calendar-day labels from yesterday through one week ago', () => {
    expect(formatSidebarTime(new Date(2026, 6, 20, 23, 59), now)).toBe('昨天');
    expect(formatSidebarTime(new Date(2026, 6, 19, 12, 0), now)).toBe('两天前');
    expect(formatSidebarTime(new Date(2026, 6, 18, 12, 0), now)).toBe('三天前');
    expect(formatSidebarTime(new Date(2026, 6, 17, 12, 0), now)).toBe('四天前');
    expect(formatSidebarTime(new Date(2026, 6, 16, 12, 0), now)).toBe('五天前');
    expect(formatSidebarTime(new Date(2026, 6, 15, 12, 0), now)).toBe('六天前');
    expect(formatSidebarTime(new Date(2026, 6, 14, 12, 0), now)).toBe('一周前');
  });

  it('shows month and day for dates older than one week', () => {
    expect(formatSidebarTime(new Date(2026, 6, 13, 12, 0), now)).toBe('7月13日');
    expect(formatSidebarTime(new Date(2026, 5, 21, 12, 0), now)).toBe('6月21日');
    expect(formatSidebarTime(new Date(2025, 11, 31, 12, 0), now)).toBe('12月31日');
  });

  it('uses month and day for future dates and ignores invalid values', () => {
    expect(formatSidebarTime(new Date(2026, 6, 22, 12, 0), now)).toBe('7月22日');
    expect(formatSidebarTime('not-a-date', now)).toBe('');
    expect(formatSidebarTime(null, now)).toBe('');
  });
});
