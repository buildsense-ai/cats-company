import {
  afterEach,
  beforeEach,
  expect,
  test,
  vi,
} from 'vitest';

beforeEach(() => {
  vi.resetModules();
  localStorage.clear();
  sessionStorage.clear();
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

test('pins the browser default to each tab once and preserves a tab-local logout', async () => {
  localStorage.setItem('oc_token', 'account-a');
  const tabA = await import('./auth-session');
  expect(tabA.getToken()).toBe('account-a');
  const savedTabA = sessionStorage.getItem('oc_token');

  sessionStorage.clear();
  vi.resetModules();
  const tabB = await import('./auth-session');
  tabB.setToken('account-b');
  expect(sessionStorage.getItem('oc_token')).toBe('account-b');
  expect(localStorage.getItem('oc_token')).toBe('account-b');

  sessionStorage.setItem('oc_token', savedTabA);
  vi.resetModules();
  const reloadedTabA = await import('./auth-session');
  expect(reloadedTabA.getToken()).toBe('account-a');
  reloadedTabA.setToken(null);
  expect(localStorage.getItem('oc_token')).toBe('account-b');

  vi.resetModules();
  expect((await import('./auth-session')).getToken()).toBeNull();
});

test('keeps an anonymous tab anonymous when another tab logs in', async () => {
  expect((await import('./auth-session')).getToken()).toBeNull();
  localStorage.setItem('oc_token', 'other-tab-token');
  vi.resetModules();
  expect((await import('./auth-session')).getToken()).toBeNull();
});

test('keeps the in-memory session and auth events when session storage is unavailable', async () => {
  localStorage.setItem('oc_token', 'other-tab-token');
  vi.stubGlobal('sessionStorage', {
    getItem: () => null,
    setItem: () => { throw new Error('storage unavailable'); },
  });

  const authSession = await import('./auth-session');
  expect(authSession.getToken()).toBeNull();
  const authChanged = vi.fn();
  window.addEventListener('cc:auth-changed', authChanged);

  try {
    authSession.setToken('memory-only-token');
    expect(authSession.getToken()).toBe('memory-only-token');
    authSession.setToken(null);
  } finally {
    window.removeEventListener('cc:auth-changed', authChanged);
  }

  expect(authSession.getToken()).toBeNull();
  expect(localStorage.getItem('oc_token')).toBe('other-tab-token');
  expect(authChanged).toHaveBeenCalledTimes(2);
  expect(authChanged.mock.calls[0][0].detail).toMatchObject({ loggedIn: true });
  expect(authChanged.mock.calls[1][0].detail).toMatchObject({ loggedIn: false });
});

test('bounds authentication requests so a network stall becomes visible', async () => {
  vi.useFakeTimers();
  vi.stubGlobal('fetch', vi.fn((_url, options) => new Promise((_resolve, reject) => {
    options.signal.addEventListener('abort', () => {
      const error = new Error('aborted');
      error.name = 'AbortError';
      reject(error);
    }, { once: true });
  })));

  try {
    const authSession = await import('./auth-session');
    const request = authSession.authApi.login({ account: 'stalled', password: 'secret' });
    const rejection = expect(request).rejects.toMatchObject({
      code: 'REQUEST_TIMEOUT',
      message: '请求超时，请稍后重试',
    });

    await vi.advanceTimersByTimeAsync(authSession.AUTH_REQUEST_TIMEOUT_MS);
    await rejection;
    expect(globalThis.fetch.mock.calls[0][1].signal.aborted).toBe(true);
  } finally {
    vi.unstubAllGlobals();
    vi.useRealTimers();
  }
});
