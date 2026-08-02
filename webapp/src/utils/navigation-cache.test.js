import {
  cleanupNavigationCaches,
  NAVIGATION_CACHE_NAME,
} from './navigation-cache';

describe('navigation cache cleanup', () => {
  test('removes all cached HTML from earlier service worker sessions', async () => {
    const deleteCache = vi.fn().mockResolvedValue(true);
    const cacheStorage = {
      keys: vi.fn().mockResolvedValue([
        NAVIGATION_CACHE_NAME,
        'catsco-navigation-v0',
        'workbox-precache-v2-example',
        'unrelated-runtime-cache',
      ]),
      delete: deleteCache,
    };

    await cleanupNavigationCaches(cacheStorage);

    expect(deleteCache.mock.calls.map(([cacheName]) => cacheName)).toEqual([
      NAVIGATION_CACHE_NAME,
      'catsco-navigation-v0',
    ]);
  });
});
