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
  NavigationRoute: class NavigationRoute {},
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
});
