import {
  authModeForPathname,
  authPathForMode,
  authenticationRedirectPath,
  isAuthenticationPathname,
  loginPathForLocation,
  navigateBrowserPath,
  postAuthenticationPathFromSearch,
  shouldMountPwaForPathname,
} from './auth-routes';

describe('authentication routes', () => {
  test.each([
    ['/login', 'login'],
    ['/login/', 'login'],
    ['/register', 'register'],
    ['/reset-password', 'reset'],
  ])('maps %s to %s mode', (pathname, mode) => {
    expect(authModeForPathname(pathname)).toBe(mode);
  });

  it('recognizes only the dedicated authentication routes', () => {
    expect(isAuthenticationPathname('/login')).toBe(true);
    expect(isAuthenticationPathname('/register/')).toBe(true);
    expect(isAuthenticationPathname('/reset-password')).toBe(true);
    expect(isAuthenticationPathname('/')).toBe(false);
    expect(isAuthenticationPathname('/e/invite-1')).toBe(false);
  });

  it('keeps PWA behavior off the authentication routes', () => {
    expect(shouldMountPwaForPathname('/login')).toBe(false);
    expect(shouldMountPwaForPathname('/register/')).toBe(false);
    expect(shouldMountPwaForPathname('/reset-password')).toBe(false);
    expect(shouldMountPwaForPathname('/')).toBe(true);
  });

  it('uses stable direct paths for each authentication mode', () => {
    expect(authPathForMode('login')).toBe('/login');
    expect(authPathForMode('register')).toBe('/register');
    expect(authPathForMode('reset')).toBe('/reset-password');
  });

  it('preserves a safe internal return path when redirecting an anonymous visitor', () => {
    expect(loginPathForLocation({
      pathname: '/e/invite-1',
      search: '?source=email',
      hash: '#accept',
    })).toBe('/login?next=%2Fe%2Finvite-1%3Fsource%3Demail%23accept');
  });

  it('does not nest return URLs while an authentication route is already open', () => {
    expect(loginPathForLocation({
      pathname: '/login',
      search: '?next=%2Fe%2Finvite-1',
    })).toBe('/login');
  });

  it('allows only safe non-auth internal post-authentication paths', () => {
    expect(postAuthenticationPathFromSearch('?next=%2Fe%2Finvite-1%3Fsource%3Demail')).toBe('/e/invite-1?source=email');
    expect(postAuthenticationPathFromSearch('?next=https%3A%2F%2Fphishing.example')).toBe('/');
    expect(postAuthenticationPathFromSearch('?next=%2F%2Fphishing.example')).toBe('/');
    expect(postAuthenticationPathFromSearch('?next=%2Flogin')).toBe('/');
    expect(postAuthenticationPathFromSearch('?next=%2Flogin%2Funknown')).toBe('/');
  });

  it('redirects only when the browser route and authentication state disagree', () => {
    expect(authenticationRedirectPath({
      authenticated: false,
      location: { pathname: '/e/invite-1', search: '?source=email' },
    })).toBe('/login?next=%2Fe%2Finvite-1%3Fsource%3Demail');
    expect(authenticationRedirectPath({
      authenticated: false,
      location: { pathname: '/register' },
    })).toBe('');
    expect(authenticationRedirectPath({
      authenticated: true,
      location: { pathname: '/login', search: '?next=%2Fe%2Finvite-1' },
    })).toBe('/e/invite-1');
    expect(authenticationRedirectPath({
      authenticated: true,
      location: { pathname: '/reset-password', search: '?next=https%3A%2F%2Fphishing.example' },
    })).toBe('/');
  });

  it('navigates to authentication paths as well as application paths', () => {
    const initial = `${window.location.pathname}${window.location.search}`;
    window.history.replaceState(null, '', '/login');
    const pushed = navigateBrowserPath('/register?next=%2Fe%2Finvite-1');
    expect(pushed).toBe(true);
    expect(`${window.location.pathname}${window.location.search}`).toBe('/register?next=%2Fe%2Finvite-1');

    navigateBrowserPath('/api/secret');
    expect(window.location.pathname).toBe('/');

    window.history.replaceState(null, '', initial || '/');
  });
});
