// Cookie writes must finish in order. Aborting an old fetch does not prevent a
// response already in flight from setting its HttpOnly cookie after logout.
export function createFileMediaSession({ base = '', current, fetcher = (...args) => fetch(...args), now = Date.now }) {
  let queue = Promise.resolve();
  let established = null;
  let pending = null;
  let renewalTimer;

  const send = async (method, token) => {
    const response = await fetcher(`${base}/api/files/media-session`, {
      method,
      credentials: 'include',
      cache: 'no-store',
      headers: { Authorization: `Bearer ${token}` },
    });
    if (!response.ok) throw new Error('File media session unavailable');
  };
  const enqueue = (operation) => {
    const locked = () => globalThis.navigator?.locks?.request
      ? navigator.locks.request('catsco-file-media-session', operation)
      : operation();
    const result = queue.then(locked, locked);
    queue = result.catch(() => {});
    return result;
  };

  const ensure = (token, revision) => {
    if (!token || !current(token, revision)) return Promise.resolve(false);
    if (established?.token === token && established.revision === revision
      && now() < established.renewAt) return Promise.resolve(true);
    if (pending?.token === token && pending.revision === revision) return pending.promise;
    const promise = enqueue(async () => {
      if (!current(token, revision)) return false;
      await send('POST', token);
      if (!current(token, revision)) {
        // The account changed during POST. Revoke its result before another
        // queued operation can establish the new account's cookie.
        await send('DELETE', token);
        return false;
      }
      established = { token, revision, renewAt: now() + 14 * 60 * 1000 };
      clearTimeout(renewalTimer);
      renewalTimer = setTimeout(() => {
        void ensure(token, revision).catch(() => {});
      }, 14 * 60 * 1000);
      renewalTimer?.unref?.();
      return true;
    });
    pending = { token, revision, promise };
    void promise.finally(() => {
      if (pending?.promise === promise) pending = null;
    }).catch(() => {});
    return promise;
  };

  return {
    ensure,
    change(previous, next, revision) {
      clearTimeout(renewalTimer);
      established = null;
      if (previous && previous !== next) {
        void enqueue(() => send('DELETE', previous)).catch(() => {});
      }
      return next ? ensure(next, revision) : queue.then(() => false);
    },
  };
}
