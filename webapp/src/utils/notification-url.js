export function sameOriginNotificationURL(value, origin) {
  const fallback = new URL('/', origin);
  try {
    const target = new URL(typeof value === 'string' ? value : '/', fallback);
    return target.origin === fallback.origin ? target.href : fallback.href;
  } catch {
    return fallback.href;
  }
}
