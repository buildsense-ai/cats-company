/// <reference lib="webworker" />

import { clientsClaim } from 'workbox-core';
import { cleanupOutdatedCaches, precacheAndRoute } from 'workbox-precaching';
import { registerRoute, NavigationRoute } from 'workbox-routing';
import { NetworkOnly } from 'workbox-strategies';

import { sameOriginNotificationURL } from './utils/notification-url';
import { cleanupNavigationCaches } from './utils/navigation-cache';
import { navigationFallback } from './utils/navigation-fallback';

clientsClaim();
cleanupOutdatedCaches();

self.addEventListener('message', (event) => {
  if (event.data?.type === 'SKIP_WAITING') self.skipWaiting();
});

self.addEventListener('activate', (event) => {
  event.waitUntil(cleanupNavigationCaches(caches));
});

const neverCache = ({ url }) => (
  url.origin === self.location.origin
  && (
    url.pathname === '/api'
    || url.pathname.startsWith('/api/')
    || url.pathname === '/v1'
    || url.pathname.startsWith('/v1/')
    || url.pathname === '/local'
    || url.pathname.startsWith('/local/')
    || url.pathname === '/uploads'
    || url.pathname.startsWith('/uploads/')
  )
);

registerRoute(neverCache, new NetworkOnly(), 'GET');
// Unhandled mutation requests fall through to the browser's native network
// stack. Non-cacheable writes do not benefit from a Workbox strategy, and the
// native path avoids an extra Request clone for large mobile upload bodies.

const navigationHandler = new NetworkOnly({
  networkTimeoutSeconds: 4,
  plugins: [{
    cacheWillUpdate: async ({ response }) => {
      if (!response || response.status !== 200) return null;
      return response;
    },
    handlerDidError: navigationFallback,
  }],
});

const authenticationNavigation = /^\/(?:login|register|reset-password)\/*(?:\?.*)?$/;

registerRoute(new NavigationRoute(navigationHandler, {
  denylist: [
    /^\/api(?:\/|$)/,
    /^\/v1(?:\/|$)/,
    /^\/local(?:\/|$)/,
    /^\/uploads(?:\/|$)/,
    authenticationNavigation,
  ],
}));

// Register the precache route after NavigationRoute. Workbox's precache
// matcher also treats `/` as `index.html`; keeping navigation first preserves
// the network-only HTML policy while still allowing navigationFallback to
// read the installed app shell when the network fails.
precacheAndRoute(self.__WB_MANIFEST);

function notificationFromEvent(event) {
  let payload = {};
  if (event.data) {
    try {
      payload = event.data.json();
    } catch {
      payload = { body: event.data.text() };
    }
  }

  const notification = payload.notification || payload;
  return {
    title: notification.title || 'CatsCo',
    options: {
      body: notification.body || '您有一条新消息',
      icon: notification.icon || '/pwa-192x192.png',
      badge: notification.badge || '/pwa-notification-badge-96x96.png',
      tag: notification.tag,
      renotify: Boolean(notification.renotify),
      data: {
        ...(notification.data || payload.data || {}),
        url: notification.url || notification.data?.url || payload.data?.url || '/',
      },
    },
  };
}

self.addEventListener('push', (event) => {
  const { title, options } = notificationFromEvent(event);
  event.waitUntil(self.registration.showNotification(title, options));
});

self.addEventListener('notificationclick', (event) => {
  event.notification.close();
  const target = sameOriginNotificationURL(event.notification.data?.url, self.location.origin);

  event.waitUntil((async () => {
    const windows = await self.clients.matchAll({ type: 'window', includeUncontrolled: true });
    const sameOriginWindow = windows.find((client) => new URL(client.url).origin === self.location.origin);
    if (sameOriginWindow) {
      await sameOriginWindow.navigate(target);
      return sameOriginWindow.focus();
    }
    return self.clients.openWindow(target);
  })());
});
