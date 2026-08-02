export const NAVIGATION_CACHE_PREFIX = 'catsco-navigation-';
export const NAVIGATION_CACHE_NAME = `${NAVIGATION_CACHE_PREFIX}v1`;

export async function cleanupNavigationCaches(cacheStorage) {
  const cacheNames = await cacheStorage.keys();
  await Promise.all(
    cacheNames
      .filter((cacheName) => cacheName.startsWith(NAVIGATION_CACHE_PREFIX))
      .map((cacheName) => cacheStorage.delete(cacheName)),
  );
}
