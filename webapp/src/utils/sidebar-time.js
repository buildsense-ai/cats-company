const RELATIVE_DAY_LABELS = new Map([
  [1, '昨天'],
  [2, '两天前'],
  [3, '三天前'],
  [4, '四天前'],
  [5, '五天前'],
  [6, '六天前'],
  [7, '一周前'],
]);

export function formatSidebarTime(value, nowValue = Date.now()) {
  const date = toValidDate(value);
  const now = toValidDate(nowValue);
  if (!date || !now) return '';

  const dayDifference = localCalendarDay(date) - localCalendarDay(now);
  if (dayDifference === 0) {
    const hours = String(date.getHours()).padStart(2, '0');
    const minutes = String(date.getMinutes()).padStart(2, '0');
    return `${hours}:${minutes}`;
  }

  const daysAgo = -dayDifference;
  if (RELATIVE_DAY_LABELS.has(daysAgo)) return RELATIVE_DAY_LABELS.get(daysAgo);

  return `${date.getMonth() + 1}月${date.getDate()}日`;
}

function toValidDate(value) {
  if (value == null || value === '') return null;
  const date = value instanceof Date ? new Date(value.getTime()) : new Date(value);
  return Number.isNaN(date.getTime()) ? null : date;
}

function localCalendarDay(date) {
  return Date.UTC(date.getFullYear(), date.getMonth(), date.getDate()) / 86400000;
}
