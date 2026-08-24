import {
  afterEach,
  beforeEach,
  expect,
  test,
  vi,
} from 'vitest';

let originalStorageDescriptor;

beforeEach(() => {
  vi.resetModules();
  originalStorageDescriptor = Object.getOwnPropertyDescriptor(globalThis, 'localStorage');
});

afterEach(() => {
  if (originalStorageDescriptor) {
    Object.defineProperty(globalThis, 'localStorage', originalStorageDescriptor);
  }
  vi.restoreAllMocks();
});

test('keeps the in-memory session and auth events when local storage is unavailable', async () => {
  Object.defineProperty(globalThis, 'localStorage', {
    configurable: true,
    value: {
      getItem: vi.fn(() => null),
      setItem: vi.fn(() => { throw new Error('quota exceeded'); }),
      removeItem: vi.fn(() => { throw new Error('storage unavailable'); }),
    },
  });

  const authSession = await import('./auth-session');
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
