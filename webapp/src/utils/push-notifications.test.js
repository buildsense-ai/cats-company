import {
  serializePushSubscription,
  shouldOfferPush,
  urlBase64ToUint8Array,
} from './push-notifications';

describe('push notification helpers', () => {
  beforeEach(() => {
    Object.defineProperty(window, 'isSecureContext', { configurable: true, value: true });
    Object.defineProperty(window, 'Notification', {
      configurable: true,
      value: { permission: 'default' },
    });
    Object.defineProperty(window, 'PushManager', { configurable: true, value: function PushManager() {} });
    Object.defineProperty(navigator, 'serviceWorker', { configurable: true, value: {} });
  });

  test('converts a URL-safe VAPID key to bytes', () => {
    const result = urlBase64ToUint8Array('AQID-_8');
    expect(Array.from(result)).toEqual([1, 2, 3, 251, 255]);
  });

  test('only offers push after login while permission is undecided', () => {
    expect(shouldOfferPush({ loggedIn: true, permission: 'default', dismissed: false })).toBe(true);
    expect(shouldOfferPush({ loggedIn: false, permission: 'default', dismissed: false })).toBe(false);
    expect(shouldOfferPush({ loggedIn: true, permission: 'denied', dismissed: false })).toBe(false);
    expect(shouldOfferPush({ loggedIn: true, permission: 'default', dismissed: true })).toBe(false);
  });

  test('uses the browser subscription JSON representation', () => {
    const json = { endpoint: 'https://push.example/sub', keys: { p256dh: 'a', auth: 'b' } };
    expect(serializePushSubscription({ toJSON: () => json })).toEqual(json);
  });
});
