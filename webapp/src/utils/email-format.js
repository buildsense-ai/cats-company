// Client-side email sanity check. The server performs the authoritative
// validation (publicsuffix); this only catches obvious typos (e.g. "qq.cpm")
// before the user submits the form, so they are told immediately instead of
// silently never receiving a verification email.
const COMMON_EMAIL_TLDS = new Set([
  'com', 'net', 'org', 'cn', 'io', 'dev', 'ai', 'co', 'me', 'cc',
  'top', 'xyz', 'tech', 'online', 'site', 'live', 'icu', 'info', 'biz',
  'app', 'wang', 'vip', 'club', 'shop', 'pro', 'work', 'fun', 'cloud',
  'space', 'link', 'mobi', 'tv', 'name', 'design', 'wiki', 'store',
  'news', 'today', 'life', 'world', 'email', 'website', 'art', 'band',
  'com.cn', 'net.cn', 'org.cn', 'edu.cn', 'gov.cn', 'ac.cn',
  'eu', 'uk', 'us', 'ca', 'au', 'de', 'fr', 'jp', 'kr', 'sg', 'hk',
  'tw', 'in', 'ru', 'br', 'mx', 'za', 'nz', 'ie', 'il', 'ch', 'se',
]);

export function isValidEmailFormat(value) {
  const email = String(value || '').trim();
  if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email)) return false;
  const domain = email.split('@')[1].toLowerCase();
  const labels = domain.split('.');
  if (labels.length < 2) return false;
  // Multi-label suffixes like com.cn / co.uk must be checked as a unit.
  if (COMMON_EMAIL_TLDS.has(labels.slice(-2).join('.'))) return true;
  return COMMON_EMAIL_TLDS.has(labels[labels.length - 1]);
}
