import { beforeEach, expect, test, vi } from 'vitest';

const matchPrecache = vi.hoisted(() => vi.fn());

vi.mock('workbox-precaching', () => ({ matchPrecache }));

import { navigationFallback } from './navigation-fallback';

beforeEach(() => {
  matchPrecache.mockReset();
});

test('uses the precached app shell when an application navigation fails', async () => {
  const appShell = new Response('<div id="root"></div>');
  matchPrecache.mockResolvedValueOnce(appShell);

  await expect(navigationFallback()).resolves.toBe(appShell);
  expect(matchPrecache).toHaveBeenCalledTimes(1);
  expect(matchPrecache).toHaveBeenCalledWith('/index.html');
});

test('uses the offline page only when the app shell is unavailable', async () => {
  const offlinePage = new Response('offline');
  matchPrecache.mockResolvedValueOnce(undefined).mockResolvedValueOnce(offlinePage);

  await expect(navigationFallback()).resolves.toBe(offlinePage);
  expect(matchPrecache).toHaveBeenNthCalledWith(1, '/index.html');
  expect(matchPrecache).toHaveBeenNthCalledWith(2, '/offline.html');
});
