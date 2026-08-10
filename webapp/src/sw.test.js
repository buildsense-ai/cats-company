const registerRoute = vi.fn();
let eventHandlers;
let showNotification;

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
    eventHandlers = {};
    showNotification = vi.fn().mockResolvedValue(undefined);
    vi.stubGlobal('self', {
      __WB_MANIFEST: [],
      addEventListener: vi.fn((type, handler) => {
        eventHandlers[type] = handler;
      }),
      clients: {},
      location: { origin: 'https://app.catsco.cc' },
      registration: { showNotification },
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

  test('passes the detailed push body to the browser notification', async () => {
    await import('./sw');

    const event = {
      data: {
        json: () => ({
          title: '小明',
          body: '部署已经完成，报告可以查看。',
          url: '/messages',
        }),
      },
      waitUntil: vi.fn(),
    };

    eventHandlers.push(event);

    expect(showNotification).toHaveBeenCalledWith(
      '小明',
      expect.objectContaining({
        body: '部署已经完成，报告可以查看。',
        data: { url: '/messages' },
      }),
    );
    expect(event.waitUntil).toHaveBeenCalledWith(expect.any(Promise));
  });
});
