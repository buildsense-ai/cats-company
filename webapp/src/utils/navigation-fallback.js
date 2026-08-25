import { matchPrecache } from 'workbox-precaching';

const OFFLINE_PAGE_URL = '/offline.html';

export async function navigationFallback() {
  return matchPrecache(OFFLINE_PAGE_URL);
}
