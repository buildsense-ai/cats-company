import { matchPrecache } from 'workbox-precaching';

const APP_SHELL_URL = '/index.html';
const OFFLINE_PAGE_URL = '/offline.html';

export async function navigationFallback() {
  const appShell = await matchPrecache(APP_SHELL_URL);
  return appShell || matchPrecache(OFFLINE_PAGE_URL);
}
