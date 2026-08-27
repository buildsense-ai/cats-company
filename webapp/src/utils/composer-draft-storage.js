import { getStorage, removeStorageValue } from './storage-access';

export const COMPOSER_DRAFT_STORAGE_PREFIX = 'catsco_composer_drafts:v1:';

export function composerDraftStorageKey(userID) {
  const normalizedUserID = String(userID ?? '').trim();
  return normalizedUserID
    ? `${COMPOSER_DRAFT_STORAGE_PREFIX}${normalizedUserID}`
    : '';
}

/**
 * Remove every persisted composer draft in this browser session.
 *
 * Drafts are intentionally scoped to sessionStorage, so clearing the
 * namespace on logout also removes drafts whose user id is no longer
 * available (for example, after an expired or invalid token).
 */
export function clearPersistedComposerDrafts(storage = 'sessionStorage') {
  const target = getStorage(storage);
  if (!target) return 0;

  const keys = [];
  try {
    for (let index = 0; index < target.length; index += 1) {
      const key = target.key(index);
      if (typeof key === 'string' && key.startsWith(COMPOSER_DRAFT_STORAGE_PREFIX)) {
        keys.push(key);
      }
    }
  } catch {
    return 0;
  }

  return keys.reduce((removed, key) => (
    removeStorageValue(key, storage) ? removed + 1 : removed
  ), 0);
}
