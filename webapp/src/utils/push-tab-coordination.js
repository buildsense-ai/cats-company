const CHANNEL_NAME = 'catsco-push-tabs';
const ACTIVE_LOCK_NAME = 'catsco-push-active-tab';
const REGISTRATION_LOCK_PREFIX = 'catsco-push-registration:';
const PUSH_RECONCILE_STORAGE_KEY = 'catsco-push-reconcile';

function requestID() {
  if (typeof globalThis.crypto?.randomUUID === 'function') return globalThis.crypto.randomUUID();
  return `${Date.now()}-${Math.random()}`;
}

function registrationLockName(registrationID) {
  return `${REGISTRATION_LOCK_PREFIX}${registrationID}`;
}

function startSharedLock(locks, lockName, isStillOwned) {
  if (!locks?.request) {
    return {
      done: Promise.resolve(),
      ready: Promise.resolve(true),
      release() {},
    };
  }

  let releaseHold;
  let resolveReady;
  let readySettled = false;
  const ready = new Promise((resolve) => {
    resolveReady = resolve;
  });
  const settleReady = (value) => {
    if (readySettled) return;
    readySettled = true;
    resolveReady(value);
  };
  const hold = new Promise((resolve) => {
    releaseHold = resolve;
  });
  const done = locks.request(lockName, { mode: 'shared' }, async () => {
    const ownsLock = isStillOwned();
    settleReady(ownsLock);
    if (!ownsLock) return;
    await hold;
  }).catch(() => {
    settleReady(false);
  });

  return {
    done,
    ready,
    release() {
      releaseHold?.();
      settleReady(false);
    },
  };
}

export function createPushTabCoordinator(
  BroadcastChannelImpl = globalThis.BroadcastChannel,
  locks = globalThis.navigator?.locks,
) {
  const channel = typeof BroadcastChannelImpl === 'function'
    ? new BroadcastChannelImpl(CHANNEL_NAME)
    : null;
  const reconcileListeners = new Set();
  const seenReconcileIDs = new Set();
  const releasedRegistrationLocks = new Map();
  let active = false;
  let activeRegistrationID = '';
  let activeLock = null;
  let registrationLock = null;
  let releasedActiveLock = Promise.resolve();

  const rememberReleasedRegistrationLock = (registrationID, lock) => {
    if (!registrationID || !lock) return;
    const done = lock.done;
    releasedRegistrationLocks.set(registrationID, done);
    done.finally(() => {
      if (releasedRegistrationLocks.get(registrationID) === done) {
        releasedRegistrationLocks.delete(registrationID);
      }
    });
  };

  const releaseActiveLocks = () => {
    if (activeLock) {
      releasedActiveLock = activeLock.done;
      activeLock.release();
      activeLock = null;
    }
    if (registrationLock) {
      rememberReleasedRegistrationLock(activeRegistrationID, registrationLock);
      registrationLock.release();
      registrationLock = null;
    }
  };

  const acquireActiveLocks = (registrationID) => {
    const ownsRegistration = () => active && activeRegistrationID === registrationID;
    activeLock = startSharedLock(locks, ACTIVE_LOCK_NAME, ownsRegistration);
    registrationLock = startSharedLock(
      locks,
      registrationLockName(registrationID),
      ownsRegistration,
    );
  };

  const notifyReconcile = (reconcileID) => {
    if (!active) return;
    const id = String(reconcileID || '').trim() || requestID();
    if (seenReconcileIDs.has(id)) return;
    seenReconcileIDs.add(id);
    if (seenReconcileIDs.size > 32) {
      seenReconcileIDs.delete(seenReconcileIDs.values().next().value);
    }
    for (const listener of reconcileListeners) listener();
  };

  const handleStorage = (event) => {
    if (event.key === PUSH_RECONCILE_STORAGE_KEY && event.newValue) {
      notifyReconcile(event.newValue);
    }
  };
  globalThis.addEventListener?.('storage', handleStorage);

  if (channel) {
    channel.onmessage = ({ data }) => {
      if (data?.type === 'reconcile') notifyReconcile(data.requestID);
    };
  }

  return {
    setActive(value, registrationID = '') {
      const nextActive = Boolean(value);
      const nextRegistrationID = String(registrationID || '').trim();
      if (!nextActive && nextRegistrationID !== activeRegistrationID) return;
      if (nextActive === active && nextRegistrationID === activeRegistrationID) return;

      if (active) releaseActiveLocks();
      active = nextActive;
      activeRegistrationID = nextActive ? nextRegistrationID : '';
      if (active) acquireActiveLocks(activeRegistrationID);
    },
    async waitUntilActive(registrationID = '') {
      const expectedRegistrationID = String(registrationID || '').trim();
      if (!active || (expectedRegistrationID && expectedRegistrationID !== activeRegistrationID)) {
        return false;
      }
      const expectedActiveLock = activeLock;
      const expectedRegistrationLock = registrationLock;
      const [activeReady, registrationReady] = await Promise.all([
        expectedActiveLock?.ready || true,
        expectedRegistrationLock?.ready || true,
      ]);
      return Boolean(activeReady)
        && Boolean(registrationReady)
        && active
        && activeLock === expectedActiveLock
        && registrationLock === expectedRegistrationLock
        && (!expectedRegistrationID || expectedRegistrationID === activeRegistrationID);
    },
    async runWhenRegistrationInactive(registrationID, callback) {
      const id = String(registrationID || '').trim();
      if (!id || typeof callback !== 'function' || !locks?.request) return false;
      if (active && activeRegistrationID === id) return false;
      await releasedRegistrationLocks.get(id);
      let callbackStarted = false;
      try {
        return await locks.request(
          registrationLockName(id),
          { mode: 'exclusive', ifAvailable: true },
          async (lock) => {
            if (!lock) return false;
            callbackStarted = true;
            return (await callback()) !== false;
          },
        );
      } catch (error) {
        if (callbackStarted) throw error;
        return false;
      }
    },
    async runWhenNoOtherActiveTabs(callback) {
      if (typeof callback !== 'function' || !locks?.request) return false;
      if (!active) await releasedActiveLock;
      let callbackStarted = false;
      try {
        return await locks.request(
          ACTIVE_LOCK_NAME,
          { mode: 'exclusive', ifAvailable: true },
          async (lock) => {
            if (!lock) return false;
            callbackStarted = true;
            return (await callback()) !== false;
          },
        );
      } catch (error) {
        if (callbackStarted) throw error;
        return false;
      }
    },
    requestReconcile() {
      const reconcileID = requestID();
      channel?.postMessage({ type: 'reconcile', requestID: reconcileID });
      try {
        globalThis.localStorage?.setItem(PUSH_RECONCILE_STORAGE_KEY, reconcileID);
        globalThis.localStorage?.removeItem(PUSH_RECONCILE_STORAGE_KEY);
      } catch {
        // BroadcastChannel remains available when storage events cannot be used.
      }
    },
    onReconcile(listener) {
      if (typeof listener !== 'function') return () => {};
      reconcileListeners.add(listener);
      return () => reconcileListeners.delete(listener);
    },
    close() {
      releaseActiveLocks();
      active = false;
      activeRegistrationID = '';
      reconcileListeners.clear();
      seenReconcileIDs.clear();
      globalThis.removeEventListener?.('storage', handleStorage);
      channel?.close();
    },
  };
}

export const pushTabCoordinator = createPushTabCoordinator();
