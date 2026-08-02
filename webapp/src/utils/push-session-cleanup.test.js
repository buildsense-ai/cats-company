import { cleanupPushForSession } from './push-session-cleanup';

describe('push session cleanup', () => {
  afterEach(() => {
    Object.defineProperty(navigator, 'serviceWorker', { configurable: true, value: undefined });
  });

  function installSubscription() {
    const browserUnsubscribe = vi.fn().mockResolvedValue(true);
    Object.defineProperty(navigator, 'serviceWorker', {
      configurable: true,
      value: {
        getRegistration: vi.fn().mockResolvedValue({
          pushManager: {
            getSubscription: vi.fn().mockResolvedValue({
              endpoint: 'https://push.example/subscription',
              unsubscribe: browserUnsubscribe,
            }),
          },
        }),
      },
    });
    return browserUnsubscribe;
  }

  test('keeps shared server and browser subscriptions while another tab is active', async () => {
    const browserUnsubscribe = installSubscription();
    const coordinator = {
      setActive: vi.fn(),
      runWhenRegistrationInactive: vi.fn().mockResolvedValue(false),
      runWhenNoOtherActiveTabs: vi.fn().mockResolvedValue(false),
      requestReconcile: vi.fn(),
    };
    const unsubscribeOnServer = vi.fn().mockResolvedValue({ subscribed: false });

    await expect(cleanupPushForSession({
      coordinator,
      registrationID: 'registration-old',
      registrationIDs: ['registration-old'],
      getCurrentToken: () => '',
      sessionRevision: 1,
      getCurrentSessionRevision: () => 2,
      unsubscribeOnServer,
    })).resolves.toBe(true);

    expect(coordinator.setActive).toHaveBeenCalledWith(false, 'registration-old');
    expect(unsubscribeOnServer).not.toHaveBeenCalled();
    expect(browserUnsubscribe).not.toHaveBeenCalled();
    expect(coordinator.runWhenRegistrationInactive).toHaveBeenCalledWith(
      'registration-old',
      expect.any(Function),
    );
    expect(coordinator.runWhenNoOtherActiveTabs).toHaveBeenCalledTimes(1);
    expect(coordinator.requestReconcile).toHaveBeenCalledTimes(1);
  });

  test('does not mutate an existing subscription when Web Locks are unavailable', async () => {
    const browserUnsubscribe = installSubscription();
    const coordinator = {
      setActive: vi.fn(),
      runWhenRegistrationInactive: vi.fn().mockResolvedValue(false),
      runWhenNoOtherActiveTabs: vi.fn().mockResolvedValue(false),
      requestReconcile: vi.fn(),
    };
    const unsubscribeOnServer = vi.fn().mockResolvedValue({ subscribed: false });

    await expect(cleanupPushForSession({
      coordinator,
      registrationID: 'registration-old',
      registrationIDs: ['registration-old', 'registration-legacy'],
      getCurrentToken: () => '',
      sessionRevision: 1,
      getCurrentSessionRevision: () => 2,
      unsubscribeOnServer,
    })).resolves.toBe(true);

    expect(unsubscribeOnServer).not.toHaveBeenCalled();
    expect(browserUnsubscribe).not.toHaveBeenCalled();
    expect(coordinator.requestReconcile).toHaveBeenCalledTimes(1);
  });

  test('removes the stale current record while preserving an active legacy peer', async () => {
    const browserUnsubscribe = installSubscription();
    const coordinator = {
      setActive: vi.fn(),
      runWhenRegistrationInactive: vi.fn().mockImplementation((_registrationID, callback) => callback()),
      runWhenNoOtherActiveTabs: vi.fn().mockResolvedValue(false),
      requestReconcile: vi.fn(),
    };
    const unsubscribeOnServer = vi.fn().mockResolvedValue({ subscribed: false });

    await expect(cleanupPushForSession({
      coordinator,
      registrationID: 'registration-old',
      registrationIDs: ['registration-old', 'registration-legacy'],
      getCurrentToken: () => '',
      sessionRevision: 1,
      getCurrentSessionRevision: () => 2,
      unsubscribeOnServer,
    })).resolves.toBe(true);

    expect(unsubscribeOnServer).toHaveBeenCalledWith(
      'https://push.example/subscription',
      'registration-old',
    );
    expect(unsubscribeOnServer).not.toHaveBeenCalledWith(
      'https://push.example/subscription',
      'registration-legacy',
    );
    expect(browserUnsubscribe).not.toHaveBeenCalled();
    expect(coordinator.requestReconcile).toHaveBeenCalledTimes(1);
  });

  test('removes the old server record without cancelling a newer session', async () => {
    const browserUnsubscribe = installSubscription();
    const coordinator = {
      setActive: vi.fn(),
      runWhenRegistrationInactive: vi.fn().mockImplementation((_registrationID, callback) => callback()),
      runWhenNoOtherActiveTabs: vi.fn(),
      requestReconcile: vi.fn(),
    };
    const unsubscribeOnServer = vi.fn();

    await expect(cleanupPushForSession({
      coordinator,
      registrationID: 'registration-old',
      registrationIDs: ['registration-old'],
      getCurrentToken: () => 'token-new',
      sessionRevision: 1,
      getCurrentSessionRevision: () => 3,
      unsubscribeOnServer,
    })).resolves.toBe(true);

    expect(unsubscribeOnServer).toHaveBeenCalledWith(
      'https://push.example/subscription',
      'registration-old',
    );
    expect(browserUnsubscribe).not.toHaveBeenCalled();
    expect(coordinator.runWhenNoOtherActiveTabs).not.toHaveBeenCalled();
  });

  test('does not treat an identical reissued token as the same browser session', async () => {
    const browserUnsubscribe = installSubscription();
    const coordinator = {
      setActive: vi.fn(),
      runWhenRegistrationInactive: vi.fn().mockImplementation((_registrationID, callback) => callback()),
      runWhenNoOtherActiveTabs: vi.fn(),
      requestReconcile: vi.fn(),
    };
    const unsubscribeOnServer = vi.fn().mockResolvedValue({ subscribed: false });

    await expect(cleanupPushForSession({
      coordinator,
      registrationID: 'registration-old',
      registrationIDs: ['registration-old'],
      getCurrentToken: () => 'same-token',
      sessionRevision: 1,
      getCurrentSessionRevision: () => 3,
      unsubscribeOnServer,
    })).resolves.toBe(true);

    expect(unsubscribeOnServer).toHaveBeenCalledWith(
      'https://push.example/subscription',
      'registration-old',
    );
    expect(browserUnsubscribe).not.toHaveBeenCalled();
    expect(coordinator.runWhenNoOtherActiveTabs).not.toHaveBeenCalled();
  });

  test('keeps the browser subscription when a new tab wins the cleanup lock', async () => {
    const browserUnsubscribe = installSubscription();
    const coordinator = {
      setActive: vi.fn(),
      runWhenRegistrationInactive: vi.fn().mockImplementation((_registrationID, callback) => callback()),
      runWhenNoOtherActiveTabs: vi.fn().mockResolvedValue(false),
      requestReconcile: vi.fn(),
    };
    const unsubscribeOnServer = vi.fn().mockResolvedValue({ subscribed: false });

    await expect(cleanupPushForSession({
      coordinator,
      registrationID: 'registration-old',
      registrationIDs: ['registration-old'],
      getCurrentToken: () => '',
      sessionRevision: 1,
      getCurrentSessionRevision: () => 2,
      unsubscribeOnServer,
    })).resolves.toBe(true);

    expect(unsubscribeOnServer).toHaveBeenCalledWith(
      'https://push.example/subscription',
      'registration-old',
    );
    expect(browserUnsubscribe).not.toHaveBeenCalled();
    expect(coordinator.requestReconcile).toHaveBeenCalledTimes(1);
  });

  test('removes both current and legacy records for an upgraded tab', async () => {
    const browserUnsubscribe = installSubscription();
    const coordinator = {
      setActive: vi.fn(),
      runWhenRegistrationInactive: vi.fn().mockImplementation((_registrationID, callback) => callback()),
      runWhenNoOtherActiveTabs: vi.fn().mockImplementation((callback) => callback()),
      requestReconcile: vi.fn(),
    };
    const unsubscribeOnServer = vi.fn().mockResolvedValue({ subscribed: false });

    await expect(cleanupPushForSession({
      coordinator,
      registrationID: 'registration-current',
      registrationIDs: ['registration-current', 'registration-legacy'],
      getCurrentToken: () => '',
      sessionRevision: 1,
      getCurrentSessionRevision: () => 2,
      unsubscribeOnServer,
    })).resolves.toBe(true);

    expect(unsubscribeOnServer).toHaveBeenCalledWith(
      'https://push.example/subscription',
      'registration-current',
    );
    expect(unsubscribeOnServer).toHaveBeenCalledWith(
      'https://push.example/subscription',
      'registration-legacy',
    );
    expect(browserUnsubscribe).toHaveBeenCalledTimes(1);
  });
});
