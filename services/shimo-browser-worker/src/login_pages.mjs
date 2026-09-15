// The Shimo login page opens its legal documents (服务条款 / 隐私政策 / 用户行为
// 规范) in brand-new tabs.  The login surface streams one page at a time, so
// adopting those tabs would replace the login form with a wall of legalese the
// visitor cannot get back from: the capture then looks like a frozen screenshot
// of a document nobody asked for.
export const SHIMO_LOGIN_URL = 'https://shimo.im/login';

// Document pages are never part of the login flow itself.
const NON_LOGIN_PATHS = ['/agreements/', '/agreement', '/terms', '/privacy', '/policy', '/protocol', '/help'];

// Third-party identity providers that legitimately open their own window mid
// sign-in.  Anything outside this list and shimo.im stays off the surface.
const PARTNER_HOSTS = [
  'open.weixin.qq.com',
  'open.work.weixin.qq.com',
  'login.work.weixin.qq.com',
  'api.weibo.com',
  'open.dingtalk.com',
  'login.dingtalk.com',
  'oapi.dingtalk.com',
];

function parse(rawURL) {
  try {
    return new URL(String(rawURL || ''));
  } catch {
    return null;
  }
}

export function isLegalDocumentPage(rawURL) {
  const parsed = parse(rawURL);
  if (!parsed || !['http:', 'https:'].includes(parsed.protocol)) return false;
  const host = parsed.hostname.toLowerCase();
  if (host !== 'shimo.im' && !host.endsWith('.shimo.im')) return false;
  return NON_LOGIN_PATHS.some(path => parsed.pathname.toLowerCase().startsWith(path));
}

// A popup may be part of the login flow (a QR-code window, for example), but it
// must never be a legal document and it must never be a blank preload tab.
export function shouldAdoptPopup(rawURL) {
  const parsed = parse(rawURL);
  if (!parsed || !['http:', 'https:'].includes(parsed.protocol)) return false;
  if (isLegalDocumentPage(rawURL)) return false;
  const host = parsed.hostname.toLowerCase();
  if (host === 'shimo.im' || host.endsWith('.shimo.im')) return true;
  return PARTNER_HOSTS.includes(host);
}
