function resolveStorage(storage) {
  if (typeof storage === 'string') {
    try {
      return globalThis?.[storage] || null;
    } catch {
      return null;
    }
  }

  return storage || null;
}

export function getStorage(storageType = 'localStorage') {
  return resolveStorage(storageType);
}

export function readStorageValue(key, storage = 'localStorage') {
  try {
    return resolveStorage(storage)?.getItem(key) ?? null;
  } catch {
    return null;
  }
}

export function writeStorageValue(key, value, storage = 'localStorage') {
  try {
    const target = resolveStorage(storage);
    if (!target) return false;
    target.setItem(key, String(value));
    return true;
  } catch {
    return false;
  }
}

export function removeStorageValue(key, storage = 'localStorage') {
  try {
    const target = resolveStorage(storage);
    if (!target) return false;
    target.removeItem(key);
    return true;
  } catch {
    return false;
  }
}

/**
 * Every key currently stored.
 *
 * Needed to find records whose key encodes data (a per-source version pointer), where the
 * caller knows the prefix but not the full key. Returns an empty list when storage is
 * unavailable, so callers can treat "nothing found" and "no storage" the same way.
 */
export function storageKeys(storage = 'localStorage') {
  try {
    const target = resolveStorage(storage);
    const length = Number(target?.length) || 0;
    const keys = [];
    for (let i = 0; i < length; i += 1) {
      const key = target.key(i);
      if (typeof key === 'string') keys.push(key);
    }
    return keys;
  } catch {
    return [];
  }
}
