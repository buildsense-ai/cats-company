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
    this.sharedCount = 0;
  }

  request(_name, options, callback) {
    if (options.mode === 'exclusive') {
      return Promise.resolve(callback(this.sharedCount === 0 ? {} : null));
    }
    this.sharedCount += 1;
    return Promise.resolve(callback({})).finally(() => {
      this.sharedCount -= 1;
    });
  }
}

describe('push tab coordination', () => {
  beforeEach(() => LinkedBroadcastChannel.channels.clear());

  test('detects another active tab before cleaning up a shared subscription', async () => {
    const first = createPushTabCoordinator(LinkedBroadcastChannel);
    const second = createPushTabCoordinator(LinkedBroadcastChannel);
    second.setActive(true);

    await expect(first.getOtherTabState(50)).resolves.toBe('active');

    first.close();
    second.close();
  });

  test('reports an unknown state when a broadcast probe times out', async () => {
    const coordinator = createPushTabCoordinator(LinkedBroadcastChannel);

    await expect(coordinator.getOtherTabState(5)).resolves.toBe('unknown');

    coordinator.close();
  });

  test('reports an unknown state without a coordination primitive', async () => {
    const coordinator = createPushTabCoordinator(null, null);

    await expect(coordinator.getOtherTabState()).resolves.toBe('unknown');

    coordinator.close();
  });

  test('uses shared Web Locks to detect another active tab', async () => {
    const locks = new FakeLockManager();
    const first = createPushTabCoordinator(undefined, locks);
    const second = createPushTabCoordinator(undefined, locks);
    second.setActive(true);

    await expect(first.getOtherTabState()).resolves.toBe('active');

    first.close();
    second.close();
  });

  test('waits for its own active lock to release before declaring itself the last tab', async () => {
    const locks = new FakeLockManager();
    const coordinator = createPushTabCoordinator(undefined, locks);
    coordinator.setActive(true);
    coordinator.setActive(false);

    await expect(coordinator.getOtherTabState()).resolves.toBe('none');

    coordinator.close();
  });
});
