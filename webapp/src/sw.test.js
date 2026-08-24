const { matchPrecache, precacheAndRoute } = vi.hoisted(() => ({
  matchPrecache: vi.fn(),
  precacheAndRoute: vi.fn(),
}));
const registerRoute = vi.fn();

vi.mock('workbox-core', () => ({
  clientsClaim: vi.fn(),
}));

vi.mock('workbox-precaching', () => ({
  cleanupOutdatedCaches: vi.fn(),
  matchPrecache,
  precacheAndRoute,
}));

vi.mock('workbox-routing', () => ({
  registerRoute,
  NavigationRoute: class NavigationRoute {
    constructor(handler, options) {
      this.handler = handler;
      this.options = options;
    }
  },
}));

vi.mock('workbox-strategies', () => ({
  NetworkOnly: class NetworkOnly {
    constructor(options) {
      this.options = options;
    }
  },
}));

describe('service worker API routing', () => {
  beforeEach(() => {
    vi.resetModules();
    registerRoute.mockClear();
    matchPrecache.mockReset();
    precacheAndRoute.mockClear();
    vi.stubGlobal('self', {
      __WB_MANIFEST: [],
      addEventListener: vi.fn(),
      clients: {},
      location: { origin: 'https://app.catsco.cc' },
      skipWaiting: vi.fn(),
    });
    vi.stubGlobal('caches', {
      match: vi.fn(),
    });
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  test('lets API mutation requests bypass Workbox request cloning', async () => {
    await import('./sw');

    const registeredMethods = registerRoute.mock.calls
      .map((call) => call[2])
      .filter(Boolean);
    expect(registeredMethods).toEqual(['GET']);
  });

  test('leaves authentication navigations out of service worker navigation handling', async () => {
    await import('./sw');

    const navigationRoute = registerRoute.mock.calls
      .map(([route]) => route)
      .find((route) => Array.isArray(route?.options?.denylist));
    const denylist = navigationRoute?.options?.denylist || [];

    expect(denylist.some((pattern) => pattern.test('/login'))).toBe(true);
    expect(denylist.some((pattern) => pattern.test('/login?next=%2Fe%2Finvite-1'))).toBe(true);
    expect(denylist.some((pattern) => pattern.test('/register/'))).toBe(true);
    expect(denylist.some((pattern) => pattern.test('/reset-password'))).toBe(true);
    expect(denylist.some((pattern) => pattern.test('/login///'))).toBe(true);
  });

  test('uses network-only navigation with an explicit offline fallback', async () => {
    await import('./sw');

    const navigationRoute = registerRoute.mock.calls
      .map(([route]) => route)
      .find((route) => Array.isArray(route?.options?.denylist));

    expect(navigationRoute.handler.constructor.name).toBe('NetworkOnly');
    expect(navigationRoute.handler.options.networkTimeoutSeconds).toBe(4);
    const plugin = navigationRoute.handler.options.plugins[0];
    matchPrecache.mockResolvedValueOnce({ body: 'offline' });
    await expect(plugin.handlerDidError())
      .resolves.toEqual({ body: 'offline' });
    expect(matchPrecache).toHaveBeenCalledWith('/offline.html');
    expect(precacheAndRoute).toHaveBeenCalledTimes(1);
    expect(registerRoute.mock.invocationCallOrder[1])
      .toBeLessThan(precacheAndRoute.mock.invocationCallOrder[0]);
  });
});
