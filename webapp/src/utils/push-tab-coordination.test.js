import { createPushTabCoordinator } from './push-tab-coordination';

class LinkedBroadcastChannel {
  static channels = new Map();

  constructor(name) {
    this.name = name;
    this.onmessage = null;
    const peers = LinkedBroadcastChannel.channels.get(name) || new Set();
    peers.add(this);
    LinkedBroadcastChannel.channels.set(name, peers);
  }

  postMessage(data) {
    for (const peer of LinkedBroadcastChannel.channels.get(this.name) || []) {
      if (peer === this) continue;
      queueMicrotask(() => peer.onmessage?.({ data }));
    }
  }

  close() {
    LinkedBroadcastChannel.channels.get(this.name)?.delete(this);
  }
}

class FakeLockManager {
  constructor() {
    this.states = new Map();
  }

  stateFor(name) {
    if (!this.states.has(name)) {
      this.states.set(name, {
        exclusiveHeld: false,
        pendingShared: [],
        sharedCount: 0,
      });
    }
    return this.states.get(name);
  }

  flushShared(state) {
    if (state.exclusiveHeld || state.pendingShared.length === 0) return;
    const pending = state.pendingShared.splice(0);
    pending.forEach((run) => run());
  }

  request(name, options, callback) {
    const state = this.stateFor(name);
    if (options.mode === 'exclusive') {
      if (state.sharedCount > 0 || state.exclusiveHeld) {
        return Promise.resolve(callback(null));
      }
      state.exclusiveHeld = true;
      return Promise.resolve(callback({})).finally(() => {
        state.exclusiveHeld = false;
        this.flushShared(state);
      });
    }
    return new Promise((resolve, reject) => {
      const run = () => {
        state.sharedCount += 1;
        Promise.resolve(callback({})).then(resolve, reject).finally(() => {
          state.sharedCount -= 1;
          this.flushShared(state);
        });
      };
      if (state.exclusiveHeld) state.pendingShared.push(run);
      else run();
    });
  }
}

describe('push tab coordination', () => {
  beforeEach(() => LinkedBroadcastChannel.channels.clear());

  test('never mutates subscriptions without Web Locks', async () => {
    const coordinator = createPushTabCoordinator(LinkedBroadcastChannel, null);
    const removeRecord = vi.fn();
    const unsubscribeBrowser = vi.fn();

    await expect(coordinator.runWhenRegistrationInactive('registration-old', removeRecord))
      .resolves.toBe(false);
    await expect(coordinator.runWhenNoOtherActiveTabs(unsubscribeBrowser))
      .resolves.toBe(false);

    expect(removeRecord).not.toHaveBeenCalled();
    expect(unsubscribeBrowser).not.toHaveBeenCalled();
    coordinator.close();
  });

  test('does not release a newer session when stale cleanup ends an older one', async () => {
    const locks = new FakeLockManager();
    const coordinator = createPushTabCoordinator(undefined, locks);
    const observer = createPushTabCoordinator(undefined, locks);
    coordinator.setActive(true, 'registration-old');
    coordinator.setActive(true, 'registration-new');

    coordinator.setActive(false, 'registration-old');

    const browserCleanup = vi.fn();
    await expect(observer.runWhenNoOtherActiveTabs(browserCleanup)).resolves.toBe(false);
    expect(browserCleanup).not.toHaveBeenCalled();

    coordinator.close();
    observer.close();
  });

  test('retires a stale registration while another registration remains active', async () => {
    const locks = new FakeLockManager();
    const stale = createPushTabCoordinator(undefined, locks);
    const newer = createPushTabCoordinator(undefined, locks);
    const removeOldRecord = vi.fn().mockResolvedValue(true);
    stale.setActive(true, 'registration-old');
    newer.setActive(true, 'registration-new');
    stale.setActive(false, 'registration-old');

    await expect(stale.runWhenRegistrationInactive('registration-old', removeOldRecord))
      .resolves.toBe(true);

    expect(removeOldRecord).toHaveBeenCalledTimes(1);
    const browserCleanup = vi.fn();
    await expect(stale.runWhenNoOtherActiveTabs(browserCleanup)).resolves.toBe(false);
    expect(browserCleanup).not.toHaveBeenCalled();

    stale.close();
    newer.close();
  });

  test('does not retire a registration that an active peer still holds', async () => {
    const locks = new FakeLockManager();
    const stale = createPushTabCoordinator(undefined, locks);
    const peer = createPushTabCoordinator(undefined, locks);
    const removeSharedRecord = vi.fn();
    stale.setActive(true, 'registration-shared');
    peer.setActive(true, 'registration-shared');
    stale.setActive(false, 'registration-shared');

    await expect(stale.runWhenRegistrationInactive('registration-shared', removeSharedRecord))
      .resolves.toBe(false);

    expect(removeSharedRecord).not.toHaveBeenCalled();

    stale.close();
    peer.close();
  });

  test('holds registration cleanup until a same-registration peer has acquired ownership', async () => {
    const locks = new FakeLockManager();
    const closingTab = createPushTabCoordinator(undefined, locks);
    const openingTab = createPushTabCoordinator(undefined, locks);
    let releaseDelete;
    closingTab.setActive(true, 'registration-shared');
    closingTab.setActive(false, 'registration-shared');
    const removeRecord = closingTab.runWhenRegistrationInactive('registration-shared', () => (
      new Promise((resolve) => {
        releaseDelete = resolve;
      })
    ));

    await vi.waitFor(() => expect(releaseDelete).toBeTypeOf('function'));
    openingTab.setActive(true, 'registration-shared');
    let peerReady = false;
    const openingReady = openingTab.waitUntilActive('registration-shared').then((ready) => {
      peerReady = ready;
      return ready;
    });

    await Promise.resolve();
    expect(peerReady).toBe(false);

    releaseDelete();
    await expect(removeRecord).resolves.toBe(true);
    await expect(openingReady).resolves.toBe(true);

    closingTab.close();
    openingTab.close();
  });

  test('releases the previous active lock when a tab changes sessions', async () => {
    const locks = new FakeLockManager();
    const coordinator = createPushTabCoordinator(undefined, locks);
    const observer = createPushTabCoordinator(undefined, locks);
    coordinator.setActive(true, 'registration-old');
    coordinator.setActive(true, 'registration-new');
    coordinator.setActive(false, 'registration-new');

    await vi.waitFor(async () => {
      await expect(observer.runWhenNoOtherActiveTabs(() => true)).resolves.toBe(true);
    });

    coordinator.close();
    observer.close();
  });

  test('holds browser cleanup until a new tab has acquired its active lock', async () => {
    const locks = new FakeLockManager();
    const closingTab = createPushTabCoordinator(undefined, locks);
    const openingTab = createPushTabCoordinator(undefined, locks);
    let releaseCleanup;
    const cleanup = closingTab.runWhenNoOtherActiveTabs(() => new Promise((resolve) => {
      releaseCleanup = resolve;
    }));

    await vi.waitFor(() => expect(releaseCleanup).toBeTypeOf('function'));
    openingTab.setActive(true, 'registration-new');
    let activeLockReady = false;
    const openingReady = openingTab.waitUntilActive('registration-new').then((ready) => {
      activeLockReady = ready;
      return ready;
    });

    await Promise.resolve();
    expect(activeLockReady).toBe(false);

    releaseCleanup();
    await expect(cleanup).resolves.toBe(true);
    await expect(openingReady).resolves.toBe(true);

    closingTab.close();
    openingTab.close();
  });

  test('asks an active peer to reconcile through storage when BroadcastChannel is unavailable', async () => {
    const first = createPushTabCoordinator(null, null);
    const second = createPushTabCoordinator(null, null);
    const reconcile = vi.fn();
    const setItem = vi.spyOn(Storage.prototype, 'setItem');
    second.setActive(true, 'registration-active');
    second.onReconcile(reconcile);

    first.requestReconcile();

    const [, reconcileID] = setItem.mock.calls.find(
      ([key]) => key === 'catsco-push-reconcile',
    ) || [];
    expect(reconcileID).toEqual(expect.any(String));
    window.dispatchEvent(new StorageEvent('storage', {
      key: 'catsco-push-reconcile',
      newValue: reconcileID,
    }));

    await vi.waitFor(() => expect(reconcile).toHaveBeenCalledTimes(1));

    setItem.mockRestore();
    first.close();
    second.close();
  });
});
