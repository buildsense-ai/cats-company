import { beforeEach, expect, test, vi } from 'vitest';

const matchPrecache = vi.hoisted(() => vi.fn());

vi.mock('workbox-precaching', () => ({ matchPrecache }));

import { navigationFallback } from './navigation-fallback';

beforeEach(() => {
  matchPrecache.mockReset();
});

test('uses the explicit offline page when an application navigation fails', async () => {
  const offlinePage = new Response('offline');
  matchPrecache.mockResolvedValueOnce(offlinePage);

  await expect(navigationFallback()).resolves.toBe(offlinePage);
  expect(matchPrecache).toHaveBeenCalledTimes(1);
  expect(matchPrecache).toHaveBeenCalledWith('/offline.html');
});
