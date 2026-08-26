const AUTH_PATHS = Object.freeze({
  login: '/login',
  register: '/register',
  reset: '/reset-password',
});

export const NAME_ONBOARDING_PATH = '/onboarding/name';

const AUTHENTICATION_PATHNAMES = new Set(Object.values(AUTH_PATHS));
const NON_APPLICATION_PREFIXES = ['/api', '/v1', '/local', '/uploads'];
const ROUTE_ORIGIN = 'https://app.catsco.invalid';

function normalizedPathname(pathname) {
  const value = String(pathname || '/').trim() || '/';
  if (value === '/') return value;
  return value.replace(/\/+$/, '') || '/';
}

function isSafeApplicationPath(pathname) {
  return !NON_APPLICATION_PREFIXES.some((prefix) => (
    pathname === prefix || pathname.startsWith(`${prefix}/`)
  ));
}

function safeRelativeBrowserPath(candidate) {
  const value = String(candidate || '').trim();
  if (!value || !value.startsWith('/') || value.startsWith('//') || value.startsWith('/\\')) {
    return '/';
  }

  try {
    const destination = new URL(value, ROUTE_ORIGIN);
    if (destination.origin !== ROUTE_ORIGIN || !isSafeApplicationPath(destination.pathname)) {
      return '/';
    }
    const pathname = normalizedPathname(destination.pathname);
    return `${pathname}${destination.search}${destination.hash}`;
  } catch {
    return '/';
  }
}

export function authModeForPathname(pathname) {
  const normalized = normalizedPathname(pathname);
  return Object.entries(AUTH_PATHS)
    .find(([, path]) => path === normalized)?.[0] || 'login';
}

export function isAuthenticationPathname(pathname) {
  return AUTHENTICATION_PATHNAMES.has(normalizedPathname(pathname));
}

function isAuthenticationPathOrChild(pathname) {
  const normalized = normalizedPathname(pathname);
  return Array.from(AUTHENTICATION_PATHNAMES).some((path) => (
    normalized === path || normalized.startsWith(`${path}/`)
  ));
}

export function shouldMountPwaForPathname(pathname) {
  return !isAuthenticationPathname(pathname);
}

export function isNameOnboardingPathname(pathname) {
  return normalizedPathname(pathname) === NAME_ONBOARDING_PATH;
}

export function nameOnboardingPathForNext(nextPath = '') {
  const next = safePostAuthenticationPath(nextPath);
  return next === '/'
    ? NAME_ONBOARDING_PATH
    : `${NAME_ONBOARDING_PATH}?next=${encodeURIComponent(next)}`;
}

export function authPathForMode(mode, nextPath = '') {
  const path = AUTH_PATHS[mode] || AUTH_PATHS.login;
  const next = safePostAuthenticationPath(nextPath);
  return next === '/' ? path : `${path}?next=${encodeURIComponent(next)}`;
}

export function relativePathFromLocation({
  pathname = '/',
  search = '',
  hash = '',
} = {}) {
  const path = normalizedPathname(pathname);
  return `${path}${String(search || '')}${String(hash || '')}`;
}

export function loginPathForLocation(location) {
  const next = relativePathFromLocation(location);
  return isAuthenticationPathname(location?.pathname) ? AUTH_PATHS.login : authPathForMode('login', next);
}

export function postAuthenticationPathFromSearch(search) {
  const next = new URLSearchParams(String(search || '')).get('next');
  return safePostAuthenticationPath(next);
}

export function authenticationRedirectPath({
  authenticated = false,
  location = {},
} = {}) {
  const { pathname = '/', search = '', hash = '' } = location;
  if (authenticated) {
    return isAuthenticationPathname(pathname)
      ? postAuthenticationPathFromSearch(search)
      : '';
  }
  return isAuthenticationPathname(pathname)
    ? ''
    : loginPathForLocation({ pathname, search, hash });
}

export function safePostAuthenticationPath(candidate) {
  const value = String(candidate || '').trim();
  if (!value || !value.startsWith('/') || value.startsWith('//') || value.startsWith('/\\')) {
    return '/';
  }

  try {
    const destination = new URL(value, ROUTE_ORIGIN);
    if (destination.origin !== ROUTE_ORIGIN) return '/';
    if (isAuthenticationPathOrChild(destination.pathname)) return '/';
    if (!isSafeApplicationPath(destination.pathname)) return '/';
    return `${normalizedPathname(destination.pathname)}${destination.search}${destination.hash}`;
  } catch {
    return '/';
  }
}

export function navigateBrowserPath(path, { replace = false } = {}) {
  const browserWindow = globalThis.window;
  if (!browserWindow?.history || !browserWindow.location) return false;
  const destination = safeRelativeBrowserPath(path);
  const current = relativePathFromLocation(browserWindow.location);
  if (destination === current) return false;
  browserWindow.history[replace ? 'replaceState' : 'pushState'](null, '', destination);
  const PopStateEventConstructor = browserWindow.PopStateEvent || globalThis.PopStateEvent;
  browserWindow.dispatchEvent(
    PopStateEventConstructor
      ? new PopStateEventConstructor('popstate')
      : new Event('popstate'),
  );
  return true;
}
