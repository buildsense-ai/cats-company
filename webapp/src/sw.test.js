const registerRoute = vi.fn();

vi.mock('workbox-core', () => ({
  clientsClaim: vi.fn(),
}));

vi.mock('workbox-precaching', () => ({
  cleanupOutdatedCaches: vi.fn(),
  precacheAndRoute: vi.fn(),
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
  NetworkFirst: class NetworkFirst {},
  NetworkOnly: class NetworkOnly {},
}));

describe('service worker API routing', () => {
  beforeEach(() => {
    vi.resetModules();
    registerRoute.mockClear();
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

  test('leaves authentication navigations out of the app navigation cache', async () => {
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

  test('leaves capability share navigations out of the app navigation cache', async () => {
    await import('./sw');

    const navigationRoute = registerRoute.mock.calls
      .map(([route]) => route)
      .find((route) => Array.isArray(route?.options?.denylist));
    const denylist = navigationRoute?.options?.denylist || [];

    expect(denylist.some((pattern) => pattern.test('/share/visitor-capability'))).toBe(true);
    expect(denylist.some((pattern) => pattern.test('/share'))).toBe(true);
  });
});
