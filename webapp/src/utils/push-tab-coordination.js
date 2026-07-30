const CHANNEL_NAME = 'catsco-push-tabs';
const ACTIVE_LOCK_NAME = 'catsco-push-active-tab';

function requestID() {
  if (typeof globalThis.crypto?.randomUUID === 'function') return globalThis.crypto.randomUUID();
  return `${Date.now()}-${Math.random()}`;
}

export function createPushTabCoordinator(
  BroadcastChannelImpl = globalThis.BroadcastChannel,
  locks = globalThis.navigator?.locks,
) {
  const channel = typeof BroadcastChannelImpl === 'function'
    ? new BroadcastChannelImpl(CHANNEL_NAME)
    : null;
  const pending = new Map();
  let active = false;
  let releaseActiveLock = null;
  let activeLockDone = Promise.resolve();

  if (channel) {
    channel.onmessage = ({ data }) => {
      if (data?.type === 'probe' && active) {
        channel.postMessage({ type: 'active', requestID: data.requestID });
        return;
      }
      if (data?.type === 'active') pending.get(data.requestID)?.(true);
    };
  }

  return {
    setActive(value) {
      const nextActive = Boolean(value);
      if (nextActive === active) return;
      active = nextActive;
      if (!locks?.request) return;
      if (!active) {
        releaseActiveLock?.();
        releaseActiveLock = null;
        return;
      }
      const holdLock = new Promise((resolve) => {
        releaseActiveLock = resolve;
      });
      activeLockDone = locks.request(ACTIVE_LOCK_NAME, { mode: 'shared' }, async () => {
        if (!active) return;
        await holdLock;
      }).catch(() => {});
    },
    async hasOtherActiveTab(timeoutMs = 200) {
      if (locks?.request) {
        if (!active) await activeLockDone;
        return locks.request(
          ACTIVE_LOCK_NAME,
          { mode: 'exclusive', ifAvailable: true },
          (lock) => !lock,
        );
      }
      if (!channel) return true;
      const id = requestID();
      return new Promise((resolve) => {
        const finish = (hasOtherTab) => {
          if (!pending.has(id)) return;
          pending.delete(id);
          clearTimeout(timer);
          resolve(hasOtherTab);
        };
        const timer = setTimeout(() => finish(true), timeoutMs);
        pending.set(id, finish);
        channel.postMessage({ type: 'probe', requestID: id });
      });
    },
    close() {
      active = false;
      releaseActiveLock?.();
      for (const finish of pending.values()) finish(true);
      pending.clear();
      channel?.close();
    },
  };
}

export const pushTabCoordinator = createPushTabCoordinator();
