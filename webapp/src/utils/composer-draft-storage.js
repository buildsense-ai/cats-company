import {
  getStorage,
  readStorageValue,
  removeStorageValue,
  writeStorageValue,
} from './storage-access';

export const COMPOSER_DRAFT_STORAGE_PREFIX = 'catsco_composer_drafts:v1:';
export const NEW_TASK_DRAFT_KEY = 'new-task';
const COMPOSER_DRAFT_STATE_STORAGE_PREFIX = 'catsco_composer_draft_state:v1:';

// Async upload callbacks can outlive the composer that created them. Keep a
// process-local revision per store/key so a sent or explicitly replaced draft
// can invalidate callbacks without putting transient state in sessionStorage.
const draftRevisionStores = new WeakMap();
const draftMutationStores = new WeakMap();
const draftStoreRegistries = new Map();
const objectDraftStoreRegistries = new WeakMap();

// A SkillHub handoff can resume the workspace in a new document or tab. Keep
// sessionStorage as the fast, tab-scoped copy and mirror it to localStorage so
// that a fresh browsing context can hydrate the same draft. Both copies are
// still keyed by the authenticated user and are cleared on logout.
function storageTargets(storage) {
  if (storage && typeof storage === 'object') return [storage];
  const storageType = String(storage || 'sessionStorage');
  return storageType === 'sessionStorage'
    ? ['sessionStorage', 'localStorage']
    : [storageType];
}

function composerDraftStateStorageKey(userID) {
  const normalizedUserID = String(userID || '').trim();
  return normalizedUserID
    ? `${COMPOSER_DRAFT_STATE_STORAGE_PREFIX}${normalizedUserID}`
    : '';
}

function stateStorageKeyForDraftStorageKey(storageKey) {
  if (!storageKey || !storageKey.startsWith(COMPOSER_DRAFT_STORAGE_PREFIX)) return '';
  return `${COMPOSER_DRAFT_STATE_STORAGE_PREFIX}${storageKey.slice(COMPOSER_DRAFT_STORAGE_PREFIX.length)}`;
}

function writeStorageTargets(key, value, storage) {
  let wrote = false;
  storageTargets(storage).forEach((target) => {
    if (writeStorageValue(key, value, target)) wrote = true;
  });
  return wrote;
}

function removeStorageTargets(key, storage) {
  let removed = false;
  storageTargets(storage).forEach((target) => {
    if (removeStorageValue(key, target)) removed = true;
  });
  return removed;
}

function normalizeDraftKey(key) {
  return String(key || '');
}

function registryFor(storage) {
  if (storage && typeof storage === 'object') {
    let registry = objectDraftStoreRegistries.get(storage);
    if (!registry) {
      registry = new Map();
      objectDraftStoreRegistries.set(storage, registry);
    }
    return registry;
  }

  const storageType = String(storage || 'sessionStorage');
  let registry = draftStoreRegistries.get(storageType);
  if (!registry) {
    registry = new Map();
    draftStoreRegistries.set(storageType, registry);
  }
  return registry;
}

function mutationStoreFor(store) {
  return store?.getDraftMutationStore?.() || store;
}

function revisionStoreFor(store) {
  return store?.getDraftRevisionStore?.() || store;
}

function isPhoneUploadSession(value) {
  return value && typeof value === 'object' && !Array.isArray(value)
    && typeof value.session_id === 'string' && value.session_id.length > 0;
}

const draftFieldDefinitions = {
  input: {
    mapName: 'inputDrafts',
    getter: 'getInputDraft',
    setter: 'setInputDraft',
    normalize(value) {
      return typeof value === 'string' && value ? value : null;
    },
    read(value) {
      return typeof value === 'string' ? value : '';
    },
  },
  mention: {
    mapName: 'structuredMentionDrafts',
    getter: 'getStructuredMentionDraft',
    setter: 'setStructuredMentionDraft',
    normalize(value) {
      return Array.isArray(value) && value.length > 0 ? [...value] : null;
    },
    read(value) {
      return Array.isArray(value) ? [...value] : [];
    },
  },
  attachment: {
    mapName: 'attachmentDrafts',
    getter: 'getAttachmentDraft',
    setter: 'setAttachmentDraft',
    normalize(value) {
      return Array.isArray(value) && value.length > 0 ? [...value] : null;
    },
    read(value) {
      return Array.isArray(value) ? [...value] : [];
    },
  },
  phoneUpload: {
    mapName: 'phoneUploadSessions',
    getter: 'getPhoneUploadSession',
    setter: 'setPhoneUploadSession',
    normalize(value) {
      return isPhoneUploadSession(value) ? { ...value } : null;
    },
    read(value) {
      return isPhoneUploadSession(value) ? { ...value } : null;
    },
  },
};

function revisionMapFor(store) {
  if (!store || (typeof store !== 'object' && typeof store !== 'function')) return null;
  let revisions = draftRevisionStores.get(store);
  if (!revisions) {
    revisions = new Map();
    draftRevisionStores.set(store, revisions);
  }
  return revisions;
}

function mutationMapFor(store) {
  if (!store || (typeof store !== 'object' && typeof store !== 'function')) return null;
  let mutations = draftMutationStores.get(store);
  if (!mutations) {
    mutations = new Map();
    draftMutationStores.set(store, mutations);
  }
  return mutations;
}

function draftEntries(entries, normalizeValue) {
  if (!Array.isArray(entries)) return [];
  return entries.flatMap((entry) => {
    if (!Array.isArray(entry) || entry.length !== 2) return [];
    const [key, value] = entry;
    if (typeof key !== 'string' || !key) return [];
    const normalizedValue = normalizeValue(value);
    return normalizedValue === null ? [] : [[key, normalizedValue]];
  });
}

function draftMapsFromSnapshot(snapshot = {}) {
  const source = snapshot && typeof snapshot === 'object' ? snapshot : {};
  return Object.fromEntries(Object.entries(draftFieldDefinitions).map(([kind, definition]) => [
    kind,
    new Map(draftEntries(source[definition.mapName], definition.normalize)),
  ]));
}

function draftSnapshotFromMaps(draftMaps = {}) {
  return Object.fromEntries(Object.entries(draftFieldDefinitions).map(([kind, definition]) => [
    definition.mapName,
    [...(draftMaps[kind] || [])],
  ]));
}

function draftValueEqual(left, right) {
  if (Object.is(left, right)) return true;
  try {
    return JSON.stringify(left) === JSON.stringify(right);
  } catch {
    return false;
  }
}

function draftMapsEqual(leftMaps = {}, rightMaps = {}) {
  return Object.keys(draftFieldDefinitions).every((kind) => {
    const left = leftMaps[kind] || new Map();
    const right = rightMaps[kind] || new Map();
    const keys = new Set([...left.keys(), ...right.keys()]);
    return [...keys].every((key) => {
      if (left.has(key) !== right.has(key)) return false;
      return !left.has(key) || draftValueEqual(left.get(key), right.get(key));
    });
  });
}

// Apply local changes made since the last accepted snapshot on top of a
// newer snapshot from another browsing context. Untouched keys take the
// remote value; changed keys (including local deletions) take the local value.
function mergeDraftMaps(localMaps, baselineMaps, latestMaps) {
  return Object.fromEntries(Object.keys(draftFieldDefinitions).map((kind) => {
    const local = localMaps[kind] || new Map();
    const baseline = baselineMaps[kind] || new Map();
    const merged = new Map(latestMaps[kind] || []);
    const changedKeys = new Set([...local.keys(), ...baseline.keys()]);

    changedKeys.forEach((key) => {
      const localChanged = local.has(key) !== baseline.has(key)
        || (local.has(key) && !draftValueEqual(local.get(key), baseline.get(key)));
      if (!localChanged) return;
      if (local.has(key)) merged.set(key, local.get(key));
      else merged.delete(key);
    });

    return [kind, merged];
  }));
}

function normalizedUpdatedAt(value) {
  const candidate = Number(value);
  return Number.isFinite(candidate) && candidate > 0 ? candidate : 0;
}

function nextDraftUpdatedAt(...values) {
  const latest = values.reduce(
    (current, value) => Math.max(current, normalizedUpdatedAt(value)),
    0,
  );
  return Math.max(Number(Date.now()) || 0, latest + 1);
}

function newestSnapshot(current, candidate, index) {
  if (!candidate) return current;
  if (!current) return { ...candidate, index };
  if (
    candidate.updatedAt > current.updatedAt
    || (candidate.updatedAt === current.updatedAt && index < current.index)
  ) return { ...candidate, index };
  return current;
}

function readDraftSnapshot(storageKey, storage) {
  if (!storageKey) return null;
  try {
    const serialized = readStorageValue(storageKey, storage);
    if (!serialized) return null;
    const stored = JSON.parse(serialized);
    return stored && typeof stored === 'object' && !Array.isArray(stored) ? stored : null;
  } catch {
    return null;
  }
}

function readDraftSnapshots(storageKey, storage) {
  const stateStorageKey = stateStorageKeyForDraftStorageKey(storageKey);
  let selectedDraft = null;
  let selectedState = null;
  storageTargets(storage).forEach((target, index) => {
    const draft = readDraftSnapshot(storageKey, target);
    if (draft) {
      selectedDraft = newestSnapshot(selectedDraft, {
        snapshot: draft,
        updatedAt: normalizedUpdatedAt(draft.updatedAt),
      }, index);
    }
    const state = readDraftSnapshot(stateStorageKey, target);
    if (state) {
      selectedState = newestSnapshot(selectedState, {
        cleared: state.cleared === true,
        updatedAt: normalizedUpdatedAt(state.updatedAt),
      }, index);
    }
  });
  const draftUpdatedAt = selectedDraft?.updatedAt || 0;
  const stateUpdatedAt = selectedState?.updatedAt || 0;
  if (
    selectedState
    && (
      stateUpdatedAt > draftUpdatedAt
      || (stateUpdatedAt === draftUpdatedAt && selectedState.cleared)
    )
  ) {
    return { snapshot: {}, updatedAt: stateUpdatedAt, cleared: selectedState.cleared };
  }
  return {
    snapshot: selectedDraft?.snapshot || {},
    updatedAt: draftUpdatedAt,
    cleared: false,
  };
}

function readDraftFieldFromMap(store, kind, key) {
  const definition = draftFieldDefinitions[kind];
  const normalizedKey = normalizeDraftKey(key);
  const map = definition && (store?.[definition.mapName] || store?.[kind]);
  return definition && normalizedKey
    ? definition.read(map?.get?.(normalizedKey))
    : (definition?.read?.(undefined) ?? null);
}

function writeDraftFieldToMap(store, kind, key, value) {
  const definition = draftFieldDefinitions[kind];
  const normalizedKey = normalizeDraftKey(key);
  const map = definition && (store?.[definition.mapName] || store?.[kind]);
  if (!definition || !normalizedKey || typeof map?.set !== 'function') return '';
  const normalizedValue = definition.normalize(value);
  if (normalizedValue === null) map.delete?.(normalizedKey);
  else map.set(normalizedKey, normalizedValue);
  return normalizedKey;
}

function readComposerDraftField(store, kind, key) {
  const definition = draftFieldDefinitions[kind];
  if (!definition) return null;
  const getter = store?.[definition.getter];
  if (typeof getter === 'function') return definition.read(getter.call(store, key));
  return readDraftFieldFromMap(store, kind, key);
}

function writeComposerDraftField(store, kind, key, value) {
  const definition = draftFieldDefinitions[kind];
  if (!definition) return;
  const setter = store?.[definition.setter];
  if (typeof setter === 'function') {
    setter.call(store, key, value);
    markComposerDraftMutation(store, key);
    return;
  }
  const normalizedKey = writeDraftFieldToMap(store, kind, key, value);
  if (normalizedKey) markComposerDraftMutation(store, normalizedKey);
}

export function composerDraftStorageKey(userID) {
  const normalizedUserID = String(userID || '').trim();
  return normalizedUserID ? `${COMPOSER_DRAFT_STORAGE_PREFIX}${normalizedUserID}` : '';
}

export function clearPersistedComposerDrafts(storage = 'sessionStorage') {
  const targets = storageTargets(storage);
  const targetObjects = targets.map((target) => getStorage(target)).filter(Boolean);
  const registries = new Set(targets.map((target) => registryFor(target)));

  const keys = new Set();
  const stateKeys = new Set();
  targetObjects.forEach((target) => {
    try {
      for (let index = 0; index < target.length; index += 1) {
        const key = target.key(index);
        if (typeof key === 'string' && key.startsWith(COMPOSER_DRAFT_STORAGE_PREFIX)) {
          keys.add(key);
        }
        if (typeof key === 'string' && key.startsWith(COMPOSER_DRAFT_STATE_STORAGE_PREFIX)) {
          stateKeys.add(key);
        }
      }
    } catch {
      // A blocked storage area should not prevent another area from clearing.
    }
  });

  // A store may have received a draft that has not reached storage yet. Keep
  // a deletion marker for every known key so a different browsing context
  // cannot later recreate that stale in-memory snapshot.
  const knownDraftKeys = new Set([
    ...keys,
    ...[...stateKeys]
      .map((key) => `${COMPOSER_DRAFT_STORAGE_PREFIX}${key.slice(COMPOSER_DRAFT_STATE_STORAGE_PREFIX.length)}`),
    ...[...registries].flatMap((registry) => [...registry.keys()]),
  ]);

  // Close every known account store, including stores that have not written a
  // snapshot yet. This prevents a late callback from recreating a draft after
  // logout has cleared the storage keys.
  new Set([...registries].flatMap((registry) => [...registry.values()]))
    .forEach((store) => store.close?.());
  registries.forEach((registry) => registry.clear());

  knownDraftKeys.forEach((key) => {
    const stateKey = stateStorageKeyForDraftStorageKey(key);
    const current = readDraftSnapshots(key, storage);
    const updatedAt = nextDraftUpdatedAt(current.updatedAt);
    writeStorageTargets(
      stateKey,
      JSON.stringify({ updatedAt, cleared: true }),
      storage,
    );
    removeStorageTargets(key, storage);
  });
  return keys.size;
}

export function createComposerDraftStore(userID, storage = 'sessionStorage') {
  const storageKey = composerDraftStorageKey(userID);
  const stateStorageKey = composerDraftStateStorageKey(userID);
  const registry = storageKey ? registryFor(storage) : null;
  const previousStore = registry?.get(storageKey);
  const snapshotRecord = readDraftSnapshots(storageKey, storage);
  const snapshot = snapshotRecord.snapshot;
  const draftMaps = draftMapsFromSnapshot(snapshot);
  const inputDrafts = draftMaps.input;
  const structuredMentionDrafts = draftMaps.mention;
  const attachmentDrafts = draftMaps.attachment;
  const phoneUploadSessions = draftMaps.phoneUpload;
  let active = true;
  let closed = false;
  let handoffTarget = null;
  let persistedUpdatedAt = snapshotRecord.updatedAt;
  let persistedSnapshot = draftSnapshotFromMaps(draftMaps);
  const listeners = new Set();

  const close = () => {
    active = false;
    closed = true;
    handoffTarget = null;
    inputDrafts.clear();
    structuredMentionDrafts.clear();
    attachmentDrafts.clear();
    phoneUploadSessions.clear();
    listeners.clear();
    if (registry?.get(storageKey) === draftStore) registry.delete(storageKey);
  };

  const notify = (key) => {
    const change = { key };
    listeners.forEach((listener) => {
      try {
        listener(change);
      } catch {
        // A subscriber must not prevent another draft writer from completing.
      }
    });
  };

  const replaceDraftMaps = (nextSnapshot = {}) => {
    const nextMaps = draftMapsFromSnapshot(nextSnapshot);
    const changedKeys = new Set([
      ...Object.values(draftMaps).flatMap((map) => [...map.keys()]),
      ...Object.values(nextMaps).flatMap((map) => [...map.keys()]),
    ]);
    Object.entries(draftMaps).forEach(([kind, map]) => {
      map.clear();
      nextMaps[kind].forEach((value, key) => map.set(key, value));
    });
    changedKeys.forEach(notify);
  };

  const getDraftField = (kind, key) => {
    const definition = draftFieldDefinitions[kind];
    if (!definition) return null;
    if (handoffTarget) {
      const getter = handoffTarget[definition.getter];
      return typeof getter === 'function'
        ? getter.call(handoffTarget, key)
        : readDraftFieldFromMap(handoffTarget, kind, key);
    }
    return readDraftFieldFromMap(draftMaps, kind, key);
  };

  const setDraftField = (kind, key, value) => {
    const definition = draftFieldDefinitions[kind];
    if (!definition) return;
    if (handoffTarget) {
      const setter = handoffTarget[definition.setter];
      if (typeof setter === 'function') setter.call(handoffTarget, key, value);
      return;
    }
    if (closed) return;
    const normalizedKey = writeDraftFieldToMap(draftMaps, kind, key, value);
    if (normalizedKey) notify(normalizedKey);
  };

  const draftStore = {
    // Maps remain exposed for read-only compatibility with older consumers.
    inputDrafts,
    structuredMentionDrafts,
    attachmentDrafts,
    phoneUploadSessions,
    getInputDraft(key) {
      return getDraftField('input', key);
    },
    setInputDraft(key, value) {
      setDraftField('input', key, value);
    },
    getStructuredMentionDraft(key) {
      return getDraftField('mention', key);
    },
    setStructuredMentionDraft(key, value) {
      setDraftField('mention', key, value);
    },
    getAttachmentDraft(key) {
      return getDraftField('attachment', key);
    },
    setAttachmentDraft(key, value) {
      setDraftField('attachment', key, value);
    },
    getPhoneUploadSession(key) {
      return getDraftField('phoneUpload', key);
    },
    setPhoneUploadSession(key, value) {
      setDraftField('phoneUpload', key, value);
    },
    persist() {
      if (handoffTarget) {
        handoffTarget.persist();
        return;
      }
      // A composer can finish an upload after its workspace has unmounted.
      // Keep inactive stores writable until logout closes them so a replacement
      // store can hydrate the late result from either persisted storage copy.
      if (closed || !storageKey) return;
      const latest = readDraftSnapshots(storageKey, storage);
      // Storage events are not delivered to the context that performed the
      // write. Re-read before every write so a stale tab cannot overwrite a
      // newer draft (including a cross-tab deletion tombstone).
      let mapsToPersist = draftMaps;
      if (latest.updatedAt > persistedUpdatedAt) {
        if (latest.cleared) {
          replaceDraftMaps({});
          persistedSnapshot = draftSnapshotFromMaps({});
          persistedUpdatedAt = latest.updatedAt;
          return;
        }
        const localMaps = draftMapsFromSnapshot(draftSnapshotFromMaps(draftMaps));
        const baselineMaps = draftMapsFromSnapshot(persistedSnapshot);
        const latestMaps = draftMapsFromSnapshot(latest.snapshot);
        const mergedMaps = mergeDraftMaps(localMaps, baselineMaps, latestMaps);

        // No local changes are pending. Accept the newer snapshot without
        // writing it back, preserving deletion tombstones and newer values.
        if (draftMapsEqual(mergedMaps, latestMaps)) {
          replaceDraftMaps(draftSnapshotFromMaps(mergedMaps));
          persistedSnapshot = draftSnapshotFromMaps(mergedMaps);
          persistedUpdatedAt = latest.updatedAt;
          return;
        }

        // Local changes made since the baseline win for their keys while
        // untouched keys from the newer browsing context remain intact.
        replaceDraftMaps(draftSnapshotFromMaps(mergedMaps));
        // The remote snapshot is now the baseline for any retry. Otherwise
        // untouched remote keys would look like local edits if this write
        // fails and a later snapshot arrives.
        persistedSnapshot = draftSnapshotFromMaps(latestMaps);
        persistedUpdatedAt = latest.updatedAt;
        mapsToPersist = mergedMaps;
      }
      const updatedAt = nextDraftUpdatedAt(persistedUpdatedAt, latest.updatedAt);
      const snapshotToPersist = draftSnapshotFromMaps(mapsToPersist);
      const hasDraft = Object.values(mapsToPersist).some((map) => map.size > 0);
      if (!hasDraft) {
        const stateWritten = writeStorageTargets(
          stateStorageKey,
          JSON.stringify({ updatedAt, cleared: true }),
          storage,
        );
        removeStorageTargets(storageKey, storage);
        if (stateWritten) {
          persistedSnapshot = draftSnapshotFromMaps({});
          persistedUpdatedAt = updatedAt;
        }
        return;
      }
      try {
        const serialized = JSON.stringify({
          ...snapshotToPersist,
          updatedAt,
        });
        const draftWritten = writeStorageTargets(storageKey, serialized, storage);
        const stateWritten = writeStorageTargets(
          stateStorageKey,
          JSON.stringify({ updatedAt, cleared: false }),
          storage,
        );
        if (draftWritten || stateWritten) {
          persistedSnapshot = snapshotToPersist;
          persistedUpdatedAt = updatedAt;
        }
      } catch {
        // Keep the in-memory draft when a browser cannot serialize or store it.
      }
    },
    deactivate() {
      active = false;
    },
    activate() {
      if (!closed) {
        active = true;
        if (registry) registry.set(storageKey, draftStore);
      }
    },
    clearPersisted() {
      if (storageKey) {
        const current = readDraftSnapshots(storageKey, storage);
        const updatedAt = nextDraftUpdatedAt(current.updatedAt, persistedUpdatedAt);
        writeStorageTargets(
          stateStorageKey,
          JSON.stringify({ updatedAt, cleared: true }),
          storage,
        );
        removeStorageTargets(storageKey, storage);
      }
      close();
    },
    close,
    subscribe(listener) {
      if (typeof listener !== 'function') return () => {};
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
    getDraftMutationStore() {
      return handoffTarget?.getDraftMutationStore?.() || handoffTarget || draftStore;
    },
    getDraftRevisionStore() {
      return handoffTarget?.getDraftRevisionStore?.() || handoffTarget || draftStore;
    },
    // Internal lifecycle seam used when a workspace creates a replacement
    // store while an earlier composer still has asynchronous callbacks.
    canHandoff() {
      return !active && !closed;
    },
    handoffTo(nextStore) {
      if (!nextStore || nextStore === draftStore || closed) return;
      handoffTarget = nextStore;
    },
  };

  if (previousStore?.canHandoff?.()) previousStore.handoffTo(draftStore);
  registry?.set(storageKey, draftStore);

  return draftStore;
}

export function readComposerInputDraft(store, key) {
  return readComposerDraftField(store, 'input', key);
}

export function writeComposerInputDraft(store, key, value) {
  writeComposerDraftField(store, 'input', key, value);
}

export function readComposerMentionDraft(store, key) {
  return readComposerDraftField(store, 'mention', key);
}

export function writeComposerMentionDraft(store, key, value) {
  writeComposerDraftField(store, 'mention', key, value);
}

export function readComposerAttachmentDraft(store, key) {
  return readComposerDraftField(store, 'attachment', key);
}

export function readComposerPhoneUploadSession(store, key) {
  return readComposerDraftField(store, 'phoneUpload', key);
}

export function writeComposerPhoneUploadSession(store, key, value) {
  writeComposerDraftField(store, 'phoneUpload', key, value);
}

export function writeComposerAttachmentDraft(store, key, value) {
  writeComposerDraftField(store, 'attachment', key, value);
}

export function persistComposerDraftStore(store) {
  store?.persist?.();
}

export function readComposerDraftRevision(store, key) {
  const normalizedKey = normalizeDraftKey(key);
  if (!normalizedKey) return 0;
  return revisionMapFor(revisionStoreFor(store))?.get(normalizedKey) || 0;
}

export function invalidateComposerDraftRevision(store, key) {
  const normalizedKey = normalizeDraftKey(key);
  if (!normalizedKey) return 0;
  const revisions = revisionMapFor(revisionStoreFor(store));
  if (!revisions) return 0;
  const nextRevision = (revisions.get(normalizedKey) || 0) + 1;
  revisions.set(normalizedKey, nextRevision);
  return nextRevision;
}

export function isComposerDraftRevisionCurrent(store, key, revision) {
  if (revision === undefined || revision === null) return true;
  const normalizedRevision = Number(revision);
  return Number.isFinite(normalizedRevision)
    && readComposerDraftRevision(store, key) === normalizedRevision;
}

// Unlike the invalidation revision, this counter records every draft write.
// It lets a send operation distinguish a newer draft typed while its request
// was in flight, even when the newer value happens to be identical.
export function readComposerDraftMutationRevision(store, key) {
  const normalizedKey = normalizeDraftKey(key);
  if (!normalizedKey) return 0;
  return mutationMapFor(mutationStoreFor(store))?.get(normalizedKey) || 0;
}

export function markComposerDraftMutation(store, key) {
  const normalizedKey = normalizeDraftKey(key);
  if (!normalizedKey) return 0;
  const mutations = mutationMapFor(mutationStoreFor(store));
  if (!mutations) return 0;
  const nextMutation = (mutations.get(normalizedKey) || 0) + 1;
  mutations.set(normalizedKey, nextMutation);
  return nextMutation;
}

export function subscribeComposerDraftStore(store, listener) {
  if (typeof store?.subscribe !== 'function') return () => {};
  return store.subscribe(listener);
}
