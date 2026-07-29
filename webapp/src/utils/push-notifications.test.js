import {
  cleanupPushSubscription,
  ensurePushSubscription,
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

  test('serializes only the fields accepted by the push subscription API', () => {
    const json = {
      endpoint: 'https://push.example/sub',
      expirationTime: 1_774_915_200_000,
      keys: { p256dh: 'a', auth: 'b' },
    };

    expect(serializePushSubscription({ toJSON: () => json })).toEqual({
      endpoint: json.endpoint,
      keys: json.keys,
    });
  });

  test('reuses an existing browser subscription', async () => {
    const existing = { endpoint: 'https://push.example/sub' };
    const subscribe = vi.fn();
    Object.defineProperty(navigator, 'serviceWorker', {
      configurable: true,
      value: { ready: Promise.resolve({ pushManager: { getSubscription: vi.fn().mockResolvedValue(existing), subscribe } }) },
    });

    await expect(ensurePushSubscription('AQID')).resolves.toBe(existing);
    expect(subscribe).not.toHaveBeenCalled();
  });

  test('unsubscribes in the browser even when server cleanup fails', async () => {
    const unsubscribe = vi.fn().mockResolvedValue(true);
    const serverCleanup = vi.fn().mockRejectedValue(new Error('offline'));
    Object.defineProperty(navigator, 'serviceWorker', {
      configurable: true,
      value: {
        getRegistration: vi.fn().mockResolvedValue({
          pushManager: { getSubscription: vi.fn().mockResolvedValue({ endpoint: 'https://push.example/sub', unsubscribe }) },
        }),
      },
    });

    await expect(cleanupPushSubscription(serverCleanup)).resolves.toBe(true);
    expect(serverCleanup).toHaveBeenCalledWith('https://push.example/sub');
    expect(unsubscribe).toHaveBeenCalled();
  });

  test('does not wait for server cleanup before unsubscribing in the browser', async () => {
    let finishServerCleanup;
    const unsubscribe = vi.fn().mockResolvedValue(true);
    const serverCleanup = vi.fn().mockImplementation(() => new Promise((resolve) => {
      finishServerCleanup = resolve;
    }));
    Object.defineProperty(navigator, 'serviceWorker', {
      configurable: true,
      value: {
        getRegistration: vi.fn().mockResolvedValue({
          pushManager: { getSubscription: vi.fn().mockResolvedValue({ endpoint: 'https://push.example/sub', unsubscribe }) },
        }),
      },
    });

    const cleanup = cleanupPushSubscription(serverCleanup);

    await vi.waitFor(() => expect(unsubscribe).toHaveBeenCalled());
    finishServerCleanup();
    await expect(cleanup).resolves.toBe(true);
  });
});
