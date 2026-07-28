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

export async function ensurePushSubscription(publicKey) {
  const registration = await navigator.serviceWorker.ready;
  const existing = await registration.pushManager.getSubscription();
  if (existing) return existing;
  return registration.pushManager.subscribe({
    userVisibleOnly: true,
    applicationServerKey: urlBase64ToUint8Array(publicKey),
  });
}

export async function cleanupPushSubscription(unsubscribeOnServer) {
  if (!('serviceWorker' in navigator)) return false;
  const registration = await navigator.serviceWorker.getRegistration();
  const subscription = await registration?.pushManager?.getSubscription();
  if (!subscription) return false;

  if (unsubscribeOnServer) {
    try {
      await unsubscribeOnServer(subscription.endpoint);
    } catch (error) {
      console.warn('Failed to remove push subscription from server:', error);
    }
  }

  try {
    await subscription.unsubscribe();
  } catch (error) {
    console.warn('Failed to remove browser push subscription:', error);
  }
  return true;
}
