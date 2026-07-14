window.AppStore = (() => {
  const listeners = new Set();
  const proxyCache = new WeakMap();
  let rootState = null;
  let transactionDepth = 0;
  let pendingChange = null;

  function emit(change) {
    if (transactionDepth) {
      pendingChange = pendingChange || change;
      return;
    }
    listeners.forEach(listener => {
      try { listener(rootState, change); } catch (error) { console.error('State listener failed', error); }
    });
  }

  function observable(value, path = []) {
    if (!value || typeof value !== 'object') return value;
    if (proxyCache.has(value)) return proxyCache.get(value);
    const proxy = new Proxy(value, {
      get(target, key, receiver) {
        return observable(Reflect.get(target, key, receiver), path.concat(String(key)));
      },
      set(target, key, nextValue, receiver) {
        const previous = target[key];
        const changed = previous !== nextValue;
        const result = Reflect.set(target, key, nextValue, receiver);
        if (changed) emit({ type: 'set', path: path.concat(String(key)), value: nextValue, previous });
        return result;
      },
      deleteProperty(target, key) {
        if (!Reflect.has(target, key)) return true;
        const previous = target[key];
        const result = Reflect.deleteProperty(target, key);
        if (result) emit({ type: 'delete', path: path.concat(String(key)), previous });
        return result;
      },
    });
    proxyCache.set(value, proxy);
    return proxy;
  }

  function create(initialState) {
    if (rootState) throw new Error('AppStore can only be initialized once');
    rootState = observable(initialState);
    return rootState;
  }

  function getState() {
    return rootState;
  }

  function update(patch, meta = {}) {
    if (!rootState || !patch || typeof patch !== 'object') return rootState;
    transaction(() => Object.assign(rootState, patch), { type: 'update', ...meta });
    return rootState;
  }

  function transaction(mutator, change = { type: 'transaction' }) {
    transactionDepth += 1;
    try { return mutator(rootState); }
    finally {
      transactionDepth -= 1;
      if (!transactionDepth) {
        const nextChange = pendingChange || change;
        pendingChange = null;
        emit(nextChange);
      }
    }
  }

  function subscribe(listener) {
    listeners.add(listener);
    return () => listeners.delete(listener);
  }

  function read(key, fallback = null) {
    try {
      const raw = localStorage.getItem(key);
      return raw === null ? fallback : JSON.parse(raw);
    } catch (error) {
      return fallback;
    }
  }

  function write(key, value) {
    try {
      localStorage.setItem(key, JSON.stringify(value));
      return true;
    } catch (error) {
      AppErrors?.report?.(error, { context: 'local-storage', silent: true });
      return false;
    }
  }

  return { create, getState, update, transaction, subscribe, read, write };
})();
