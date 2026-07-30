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
    const existing = {
      endpoint: 'https://push.example/sub',
      options: { applicationServerKey: new Uint8Array([1, 2, 3]).buffer },
    };
    const subscribe = vi.fn();
    Object.defineProperty(navigator, 'serviceWorker', {
      configurable: true,
      value: { ready: Promise.resolve({ pushManager: { getSubscription: vi.fn().mockResolvedValue(existing), subscribe } }) },
    });

    await expect(ensurePushSubscription('AQID')).resolves.toBe(existing);
    expect(subscribe).not.toHaveBeenCalled();
  });

  test('replaces a browser subscription created with a different VAPID key', async () => {
    const unsubscribe = vi.fn().mockResolvedValue(true);
    const removeFromServer = vi.fn().mockResolvedValue(undefined);
    const replacement = { endpoint: 'https://push.example/replacement' };
    const subscribe = vi.fn().mockResolvedValue(replacement);
    const existing = {
      endpoint: 'https://push.example/old',
      options: { applicationServerKey: new Uint8Array([9, 9, 9]).buffer },
      unsubscribe,
    };
    Object.defineProperty(navigator, 'serviceWorker', {
      configurable: true,
      value: {
        ready: Promise.resolve({
          pushManager: {
            getSubscription: vi.fn().mockResolvedValue(existing),
            subscribe,
          },
        }),
      },
    });

    await expect(ensurePushSubscription('AQID', removeFromServer)).resolves.toBe(replacement);
    expect(removeFromServer).toHaveBeenCalledWith(existing.endpoint);
    expect(unsubscribe).toHaveBeenCalled();
    expect(subscribe).toHaveBeenCalledWith({
      userVisibleOnly: true,
      applicationServerKey: new Uint8Array([1, 2, 3]),
    });
  });

  test('creates a replacement when the old subscription was already deactivated', async () => {
    const replacement = { endpoint: 'https://push.example/replacement' };
    const subscribe = vi.fn().mockResolvedValue(replacement);
    const existing = {
      endpoint: 'https://push.example/old',
      options: { applicationServerKey: new Uint8Array([9, 9, 9]).buffer },
      unsubscribe: vi.fn().mockResolvedValue(false),
    };
    Object.defineProperty(navigator, 'serviceWorker', {
      configurable: true,
      value: {
        ready: Promise.resolve({
          pushManager: {
            getSubscription: vi.fn().mockResolvedValue(existing),
            subscribe,
          },
        }),
      },
    });

    await expect(ensurePushSubscription('AQID')).resolves.toBe(replacement);
    expect(subscribe).toHaveBeenCalled();
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

  test('waits for server cleanup before unsubscribing in the browser', async () => {
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

    await vi.waitFor(() => expect(finishServerCleanup).toBeTypeOf('function'));
    expect(unsubscribe).not.toHaveBeenCalled();
    finishServerCleanup();
    await expect(cleanup).resolves.toBe(true);
    expect(unsubscribe).toHaveBeenCalled();
  });

  test('does not create a subscription after the authenticated session changes', async () => {
    let resolveExisting;
    let current = true;
    const subscribe = vi.fn();
    Object.defineProperty(navigator, 'serviceWorker', {
      configurable: true,
      value: {
        ready: Promise.resolve({
          pushManager: {
            getSubscription: vi.fn().mockImplementation(() => new Promise((resolve) => {
              resolveExisting = resolve;
            })),
            subscribe,
          },
        }),
      },
    });

    const reconcile = ensurePushSubscription('AQID', undefined, () => current);
    await vi.waitFor(() => expect(resolveExisting).toBeTypeOf('function'));
    current = false;
    resolveExisting(null);

    await expect(reconcile).resolves.toBeNull();
    expect(subscribe).not.toHaveBeenCalled();
  });

  test('removes the old server record without deleting a new session browser subscription', async () => {
    let resolveSubscription;
    let current = true;
    const unsubscribe = vi.fn();
    const serverCleanup = vi.fn();
    Object.defineProperty(navigator, 'serviceWorker', {
      configurable: true,
      value: {
        getRegistration: vi.fn().mockResolvedValue({
          pushManager: {
            getSubscription: vi.fn().mockImplementation(() => new Promise((resolve) => {
              resolveSubscription = resolve;
            })),
          },
        }),
      },
    });

    const cleanup = cleanupPushSubscription(serverCleanup, () => current);
    await vi.waitFor(() => expect(resolveSubscription).toBeTypeOf('function'));
    current = false;
    resolveSubscription({ endpoint: 'https://push.example/new-session', unsubscribe });

    await expect(cleanup).resolves.toBe(true);
    await vi.waitFor(() => expect(serverCleanup).toHaveBeenCalledWith(
      'https://push.example/new-session',
    ));
    expect(unsubscribe).not.toHaveBeenCalled();
  });
});
