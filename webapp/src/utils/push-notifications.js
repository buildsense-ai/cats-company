export const PUSH_DISMISSED_KEY = 'cc_push_prompt_dismissed_v1';

export function pushDismissedStorageKey(owner) {
  const normalizedOwner = String(owner || '').trim();
  return normalizedOwner ? `${PUSH_DISMISSED_KEY}:${normalizedOwner}` : '';
}

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
    && 'PushManager' in window
    && typeof navigator.locks?.request === 'function';
}

export function shouldOfferPush({ loggedIn, permission, dismissed }) {
  return Boolean(loggedIn)
    && canUsePush()
    && permission === 'default'
    && !dismissed;
}

export function serializePushSubscription(subscription) {
  const serialized = typeof subscription?.toJSON === 'function'
    ? subscription.toJSON()
    : subscription;
  return {
    endpoint: serialized.endpoint,
    keys: serialized.keys || {},
  };
}

function pushKeyBytes(value) {
  if (value instanceof ArrayBuffer) return new Uint8Array(value);
  if (ArrayBuffer.isView(value)) {
    return new Uint8Array(value.buffer, value.byteOffset, value.byteLength);
  }
  return null;
}

function pushSubscriptionUsesKey(subscription, expectedKey) {
  const currentKey = pushKeyBytes(subscription?.options?.applicationServerKey);
  if (!currentKey || currentKey.length !== expectedKey.length) return false;
  return currentKey.every((value, index) => value === expectedKey[index]);
}

export async function ensurePushSubscription(
  publicKey,
  unsubscribeOnServer,
  isCurrent = () => true,
) {
  const registration = await navigator.serviceWorker.ready;
  if (!isCurrent()) return null;
  const applicationServerKey = urlBase64ToUint8Array(publicKey);
  const existing = await registration.pushManager.getSubscription();
  if (!isCurrent()) return null;
  if (existing && pushSubscriptionUsesKey(existing, applicationServerKey)) return existing;
  if (existing) {
    if (unsubscribeOnServer) {
      try {
        await unsubscribeOnServer(existing.endpoint);
      } catch (error) {
        console.warn('Failed to remove rotated push subscription from server:', error);
      }
    }
    if (!isCurrent()) return null;
    await existing.unsubscribe();
    if (!isCurrent()) return null;
  }
  return registration.pushManager.subscribe({
    userVisibleOnly: true,
    applicationServerKey,
  });
}

export async function getPushSubscription() {
  if (!('serviceWorker' in navigator)) return null;
  const registration = await navigator.serviceWorker.getRegistration();
  return registration?.pushManager?.getSubscription() || null;
}
