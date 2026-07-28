export const PUSH_DISMISSED_KEY = 'cc_push_prompt_dismissed_v1';

export function urlBase64ToUint8Array(value) {
  const padding = '='.repeat((4 - (value.length % 4)) % 4);
  const base64 = (value + padding).replace(/-/g, '+').replace(/_/g, '/');
  const raw = window.atob(base64);
  return Uint8Array.from(raw, (character) => character.charCodeAt(0));
}

export function canUsePush() {
  return window.isSecureContext
    && 'Notification' in window
    && 'serviceWorker' in navigator
    && 'PushManager' in window;
}

export function shouldOfferPush({ loggedIn, permission, dismissed }) {
  return Boolean(loggedIn)
    && canUsePush()
    && permission === 'default'
    && !dismissed;
}

export function serializePushSubscription(subscription) {
  if (typeof subscription?.toJSON === 'function') return subscription.toJSON();
  return {
    endpoint: subscription.endpoint,
    expirationTime: subscription.expirationTime || null,
    keys: {},
  };
}
