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
