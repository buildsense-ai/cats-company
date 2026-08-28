import {
  getStorage,
  readStorageValue,
  removeStorageValue,
  writeStorageValue,
} from './storage-access';

export const COMPOSER_DRAFT_STORAGE_PREFIX = 'catsco_composer_drafts:v1:';
export const NEW_TASK_DRAFT_KEY = 'new-task';

function normalizeDraftKey(key) {
  return String(key || '');
}

function draftEntries(entries, acceptsValue) {
  if (!Array.isArray(entries)) return [];
  return entries.flatMap((entry) => {
    if (!Array.isArray(entry) || entry.length !== 2) return [];
    const [key, value] = entry;
    return typeof key === 'string' && key && acceptsValue(value) ? [[key, value]] : [];
  });
}

function readDraftSnapshot(storageKey, storage) {
  if (!storageKey) return {};
  try {
    const stored = JSON.parse(readStorageValue(storageKey, storage) || '{}');
    return stored && typeof stored === 'object' && !Array.isArray(stored) ? stored : {};
  } catch {
    return {};
  }
}

export function composerDraftStorageKey(userID) {
  const normalizedUserID = String(userID || '').trim();
  return normalizedUserID ? `${COMPOSER_DRAFT_STORAGE_PREFIX}${normalizedUserID}` : '';
}

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

export function createComposerDraftStore(userID, storage = 'sessionStorage') {
  const storageKey = composerDraftStorageKey(userID);
  const snapshot = readDraftSnapshot(storageKey, storage);
  const inputDrafts = new Map(draftEntries(
    snapshot.inputDrafts,
    (value) => typeof value === 'string' && value,
  ));
  const structuredMentionDrafts = new Map(draftEntries(
    snapshot.structuredMentionDrafts,
    (value) => Array.isArray(value) && value.length > 0,
  ));
  const attachmentDrafts = new Map(draftEntries(
    snapshot.attachmentDrafts,
    (value) => Array.isArray(value) && value.length > 0,
  ));
  let active = true;

  const draftStore = {
    // Maps remain exposed for read-only compatibility with older consumers.
    inputDrafts,
    structuredMentionDrafts,
    attachmentDrafts,
    getInputDraft(key) {
      const value = inputDrafts.get(normalizeDraftKey(key));
      return typeof value === 'string' ? value : '';
    },
    setInputDraft(key, value) {
      const normalizedKey = normalizeDraftKey(key);
      if (!normalizedKey) return;
      if (typeof value === 'string' && value) inputDrafts.set(normalizedKey, value);
      else inputDrafts.delete(normalizedKey);
    },
    getStructuredMentionDraft(key) {
      const value = structuredMentionDrafts.get(normalizeDraftKey(key));
      return Array.isArray(value) ? [...value] : [];
    },
    setStructuredMentionDraft(key, value) {
      const normalizedKey = normalizeDraftKey(key);
      if (!normalizedKey) return;
      if (Array.isArray(value) && value.length > 0) {
        structuredMentionDrafts.set(normalizedKey, [...value]);
      } else {
        structuredMentionDrafts.delete(normalizedKey);
      }
    },
    getAttachmentDraft(key) {
      const value = attachmentDrafts.get(normalizeDraftKey(key));
      return Array.isArray(value) ? [...value] : [];
    },
    setAttachmentDraft(key, value) {
      const normalizedKey = normalizeDraftKey(key);
      if (!normalizedKey) return;
      if (Array.isArray(value) && value.length > 0) {
        attachmentDrafts.set(normalizedKey, [...value]);
      } else {
        attachmentDrafts.delete(normalizedKey);
      }
    },
    persist() {
      if (!active || !storageKey) return;
      if (inputDrafts.size === 0
        && structuredMentionDrafts.size === 0
        && attachmentDrafts.size === 0) {
        removeStorageValue(storageKey, storage);
        return;
      }
      try {
        writeStorageValue(storageKey, JSON.stringify({
          inputDrafts: [...inputDrafts],
          structuredMentionDrafts: [...structuredMentionDrafts],
          attachmentDrafts: [...attachmentDrafts],
        }), storage);
      } catch {
        // Keep the in-memory draft when a browser cannot serialize or store it.
      }
    },
    deactivate() {
      active = false;
    },
    activate() {
      active = true;
    },
    clearPersisted() {
      active = false;
      if (storageKey) removeStorageValue(storageKey, storage);
    },
  };

  return draftStore;
}

export function readComposerInputDraft(store, key) {
  const value = typeof store?.getInputDraft === 'function'
    ? store.getInputDraft(key)
    : store?.inputDrafts?.get?.(key);
  return typeof value === 'string' ? value : '';
}

export function writeComposerInputDraft(store, key, value) {
  if (typeof store?.setInputDraft === 'function') {
    store.setInputDraft(key, value);
    return;
  }
  const normalizedKey = normalizeDraftKey(key);
  if (!normalizedKey || typeof store?.inputDrafts?.set !== 'function') return;
  if (typeof value === 'string' && value) store.inputDrafts.set(normalizedKey, value);
  else store.inputDrafts.delete?.(normalizedKey);
}

export function readComposerMentionDraft(store, key) {
  const value = typeof store?.getStructuredMentionDraft === 'function'
    ? store.getStructuredMentionDraft(key)
    : store?.structuredMentionDrafts?.get?.(key);
  return Array.isArray(value) ? [...value] : [];
}

export function writeComposerMentionDraft(store, key, value) {
  if (typeof store?.setStructuredMentionDraft === 'function') {
    store.setStructuredMentionDraft(key, value);
    return;
  }
  const normalizedKey = normalizeDraftKey(key);
  if (!normalizedKey || typeof store?.structuredMentionDrafts?.set !== 'function') return;
  if (Array.isArray(value) && value.length > 0) {
    store.structuredMentionDrafts.set(normalizedKey, [...value]);
  } else {
    store.structuredMentionDrafts.delete?.(normalizedKey);
  }
}

export function readComposerAttachmentDraft(store, key) {
  const value = typeof store?.getAttachmentDraft === 'function'
    ? store.getAttachmentDraft(key)
    : store?.attachmentDrafts?.get?.(key);
  return Array.isArray(value) ? [...value] : [];
}

export function writeComposerAttachmentDraft(store, key, value) {
  if (typeof store?.setAttachmentDraft === 'function') {
    store.setAttachmentDraft(key, value);
    return;
  }
  const normalizedKey = normalizeDraftKey(key);
  if (!normalizedKey || typeof store?.attachmentDrafts?.set !== 'function') return;
  if (Array.isArray(value) && value.length > 0) {
    store.attachmentDrafts.set(normalizedKey, [...value]);
  } else {
    store.attachmentDrafts.delete?.(normalizedKey);
  }
}

export function persistComposerDraftStore(store) {
  store?.persist?.();
}
