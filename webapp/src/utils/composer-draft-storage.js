import {
  getStorage,
  readStorageValue,
  removeStorageValue,
  writeStorageValue,
} from './storage-access';

export const COMPOSER_DRAFT_STORAGE_PREFIX = 'catsco_composer_drafts:v1:';
export const NEW_TASK_DRAFT_KEY = 'new-task';

// Async upload callbacks can outlive the composer that created them. Keep a
// process-local revision per store/key so a sent or explicitly replaced draft
// can invalidate callbacks without putting transient state in sessionStorage.
const draftRevisionStores = new WeakMap();
const draftMutationStores = new WeakMap();
const draftStoreRegistries = new Map();
const objectDraftStoreRegistries = new WeakMap();

// A SkillHub handoff may mount the workspace in a new document. Keep the
// session copy for the current tab and mirror it to localStorage so the next
// document can hydrate the same user-scoped draft.
function storageTargets(storage) {
  if (storage && typeof storage === 'object') return [storage];
  const storageType = String(storage || 'sessionStorage');
  return storageType === 'sessionStorage'
    ? ['sessionStorage', 'localStorage']
    : [storageType];
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

function normalizeTaskContext(value) {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return null;
  const agent = value.agent && typeof value.agent === 'object' && !Array.isArray(value.agent)
    ? { ...value.agent }
    : null;
  const candidateProjectId = Number(value.projectId);
  const projectId = Number.isFinite(candidateProjectId) && candidateProjectId > 0
    ? candidateProjectId
    : 0;
  if (!agent && projectId === 0) return null;
  return {
    agent,
    projectId,
    projectName: projectId > 0 ? String(value.projectName || '') : '',
  };
}

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

function draftEntries(entries, acceptsValue) {
  if (!Array.isArray(entries)) return [];
  return entries.flatMap((entry) => {
    if (!Array.isArray(entry) || entry.length !== 2) return [];
    const [key, value] = entry;
    return typeof key === 'string' && key && acceptsValue(value) ? [[key, value]] : [];
  });
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
  let selected = null;
  let selectedUpdatedAt = -1;
  let selectedIndex = Number.POSITIVE_INFINITY;
  storageTargets(storage).forEach((target, index) => {
    const candidate = readDraftSnapshot(storageKey, target);
    if (!candidate) return;
    const candidateUpdatedAt = Number(candidate.updatedAt) || 0;
    if (
      selected === null
      || candidateUpdatedAt > selectedUpdatedAt
      || (candidateUpdatedAt === selectedUpdatedAt && index < selectedIndex)
    ) {
      selected = candidate;
      selectedUpdatedAt = candidateUpdatedAt;
      selectedIndex = index;
    }
  });
  return selected || {};
}

export function composerDraftStorageKey(userID) {
  const normalizedUserID = String(userID || '').trim();
  return normalizedUserID ? `${COMPOSER_DRAFT_STORAGE_PREFIX}${normalizedUserID}` : '';
}

export function clearPersistedComposerDrafts(storage = 'sessionStorage') {
  const targets = storageTargets(storage);
  const targetObjects = targets.map((target) => getStorage(target)).filter(Boolean);
  if (targetObjects.length === 0) return 0;
  const registries = new Set(targets.map((target) => registryFor(target)));

  const keys = new Set();
  targetObjects.forEach((target) => {
    try {
      for (let index = 0; index < target.length; index += 1) {
        const key = target.key(index);
        if (typeof key === 'string' && key.startsWith(COMPOSER_DRAFT_STORAGE_PREFIX)) {
          keys.add(key);
        }
      }
    } catch {
      // A blocked storage target must not prevent another target from clearing.
    }
  });

  // Close every known account store, including stores that have not written a
  // snapshot yet. This prevents a late callback from recreating a draft after
  // logout has cleared the storage keys.
  new Set([...registries].flatMap((registry) => [...registry.values()]))
    .forEach((store) => store.close?.());
  registries.forEach((registry) => registry.clear());
  return [...keys].reduce((removed, key) => {
    let removedFromStorage = false;
    targetObjects.forEach((target) => {
      if (removeStorageValue(key, target)) removedFromStorage = true;
    });
    return removedFromStorage ? removed + 1 : removed;
  }, 0);
}

export function createComposerDraftStore(userID, storage = 'sessionStorage') {
  const storageKey = composerDraftStorageKey(userID);
  const registry = storageKey ? registryFor(storage) : null;
  const previousStore = registry?.get(storageKey);
  const snapshot = readDraftSnapshots(storageKey, storage);
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
  const phoneUploadSessions = new Map(draftEntries(
    snapshot.phoneUploadSessions,
    isPhoneUploadSession,
  ).map(([key, value]) => [key, { ...value }]));
  const taskContextDrafts = new Map(draftEntries(
    snapshot.taskContextDrafts,
    normalizeTaskContext,
  ));
  let active = true;
  let closed = false;
  let handoffTarget = null;
  const listeners = new Set();

  const close = () => {
    active = false;
    closed = true;
    handoffTarget = null;
    inputDrafts.clear();
    structuredMentionDrafts.clear();
    attachmentDrafts.clear();
    phoneUploadSessions.clear();
    taskContextDrafts.clear();
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

  const draftStore = {
    // Maps remain exposed for read-only compatibility with older consumers.
    inputDrafts,
    structuredMentionDrafts,
    attachmentDrafts,
    phoneUploadSessions,
    taskContextDrafts,
    getInputDraft(key) {
      if (handoffTarget) return handoffTarget.getInputDraft(key);
      const value = inputDrafts.get(normalizeDraftKey(key));
      return typeof value === 'string' ? value : '';
    },
    setInputDraft(key, value) {
      if (handoffTarget) {
        handoffTarget.setInputDraft(key, value);
        return;
      }
      const normalizedKey = normalizeDraftKey(key);
      if (!normalizedKey || closed) return;
      if (typeof value === 'string' && value) inputDrafts.set(normalizedKey, value);
      else inputDrafts.delete(normalizedKey);
      notify(normalizedKey);
    },
    getStructuredMentionDraft(key) {
      if (handoffTarget) return handoffTarget.getStructuredMentionDraft(key);
      const value = structuredMentionDrafts.get(normalizeDraftKey(key));
      return Array.isArray(value) ? [...value] : [];
    },
    setStructuredMentionDraft(key, value) {
      if (handoffTarget) {
        handoffTarget.setStructuredMentionDraft(key, value);
        return;
      }
      const normalizedKey = normalizeDraftKey(key);
      if (!normalizedKey || closed) return;
      if (Array.isArray(value) && value.length > 0) {
        structuredMentionDrafts.set(normalizedKey, [...value]);
      } else {
        structuredMentionDrafts.delete(normalizedKey);
      }
      notify(normalizedKey);
    },
    getAttachmentDraft(key) {
      if (handoffTarget) return handoffTarget.getAttachmentDraft(key);
      const value = attachmentDrafts.get(normalizeDraftKey(key));
      return Array.isArray(value) ? [...value] : [];
    },
    setAttachmentDraft(key, value) {
      if (handoffTarget) {
        handoffTarget.setAttachmentDraft(key, value);
        return;
      }
      const normalizedKey = normalizeDraftKey(key);
      if (!normalizedKey || closed) return;
      if (Array.isArray(value) && value.length > 0) {
        attachmentDrafts.set(normalizedKey, [...value]);
      } else {
        attachmentDrafts.delete(normalizedKey);
      }
      notify(normalizedKey);
    },
    getPhoneUploadSession(key) {
      if (handoffTarget) return handoffTarget.getPhoneUploadSession(key);
      const value = phoneUploadSessions.get(normalizeDraftKey(key));
      return isPhoneUploadSession(value) ? { ...value } : null;
    },
    setPhoneUploadSession(key, value) {
      if (handoffTarget) {
        handoffTarget.setPhoneUploadSession(key, value);
        return;
      }
      const normalizedKey = normalizeDraftKey(key);
      if (!normalizedKey || closed) return;
      if (isPhoneUploadSession(value)) phoneUploadSessions.set(normalizedKey, { ...value });
      else phoneUploadSessions.delete(normalizedKey);
      notify(normalizedKey);
    },
    getTaskContextDraft(key) {
      if (handoffTarget) return handoffTarget.getTaskContextDraft(key);
      return normalizeTaskContext(taskContextDrafts.get(normalizeDraftKey(key)));
    },
    setTaskContextDraft(key, value) {
      if (handoffTarget) {
        handoffTarget.setTaskContextDraft(key, value);
        return;
      }
      const normalizedKey = normalizeDraftKey(key);
      if (!normalizedKey || closed) return;
      const normalizedValue = normalizeTaskContext(value);
      if (normalizedValue) taskContextDrafts.set(normalizedKey, normalizedValue);
      else taskContextDrafts.delete(normalizedKey);
      notify(normalizedKey);
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
      if (inputDrafts.size === 0
        && structuredMentionDrafts.size === 0
        && attachmentDrafts.size === 0
        && phoneUploadSessions.size === 0
        && taskContextDrafts.size === 0) {
        storageTargets(storage).forEach((target) => removeStorageValue(storageKey, target));
        return;
      }
      try {
        const serialized = JSON.stringify({
          inputDrafts: [...inputDrafts],
          structuredMentionDrafts: [...structuredMentionDrafts],
          attachmentDrafts: [...attachmentDrafts],
          phoneUploadSessions: [...phoneUploadSessions],
          taskContextDrafts: [...taskContextDrafts],
          updatedAt: Date.now(),
        });
        storageTargets(storage).forEach((target) => writeStorageValue(
          storageKey,
          serialized,
          target,
        ));
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
      close();
      if (storageKey) {
        storageTargets(storage).forEach((target) => removeStorageValue(storageKey, target));
      }
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
  const value = typeof store?.getInputDraft === 'function'
    ? store.getInputDraft(key)
    : store?.inputDrafts?.get?.(key);
  return typeof value === 'string' ? value : '';
}

export function writeComposerInputDraft(store, key, value) {
  if (typeof store?.setInputDraft === 'function') {
    store.setInputDraft(key, value);
    markComposerDraftMutation(store, key);
    return;
  }
  const normalizedKey = normalizeDraftKey(key);
  if (!normalizedKey || typeof store?.inputDrafts?.set !== 'function') return;
  if (typeof value === 'string' && value) store.inputDrafts.set(normalizedKey, value);
  else store.inputDrafts.delete?.(normalizedKey);
  markComposerDraftMutation(store, normalizedKey);
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
    markComposerDraftMutation(store, key);
    return;
  }
  const normalizedKey = normalizeDraftKey(key);
  if (!normalizedKey || typeof store?.structuredMentionDrafts?.set !== 'function') return;
  if (Array.isArray(value) && value.length > 0) {
    store.structuredMentionDrafts.set(normalizedKey, [...value]);
  } else {
    store.structuredMentionDrafts.delete?.(normalizedKey);
  }
  markComposerDraftMutation(store, normalizedKey);
}

export function readComposerAttachmentDraft(store, key) {
  const value = typeof store?.getAttachmentDraft === 'function'
    ? store.getAttachmentDraft(key)
    : store?.attachmentDrafts?.get?.(key);
  return Array.isArray(value) ? [...value] : [];
}

export function readComposerPhoneUploadSession(store, key) {
  const value = typeof store?.getPhoneUploadSession === 'function'
    ? store.getPhoneUploadSession(key)
    : store?.phoneUploadSessions?.get?.(key);
  return isPhoneUploadSession(value) ? { ...value } : null;
}

export function writeComposerPhoneUploadSession(store, key, value) {
  if (typeof store?.setPhoneUploadSession === 'function') {
    store.setPhoneUploadSession(key, value);
    markComposerDraftMutation(store, key);
    return;
  }
  const normalizedKey = normalizeDraftKey(key);
  if (!normalizedKey || typeof store?.phoneUploadSessions?.set !== 'function') return;
  if (isPhoneUploadSession(value)) store.phoneUploadSessions.set(normalizedKey, { ...value });
  else store.phoneUploadSessions.delete?.(normalizedKey);
  markComposerDraftMutation(store, normalizedKey);
}

export function readComposerTaskContextDraft(store, key) {
  const value = typeof store?.getTaskContextDraft === 'function'
    ? store.getTaskContextDraft(key)
    : store?.taskContextDrafts?.get?.(key);
  return normalizeTaskContext(value);
}

export function writeComposerTaskContextDraft(store, key, value) {
  if (typeof store?.setTaskContextDraft === 'function') {
    store.setTaskContextDraft(key, value);
    return;
  }
  const normalizedKey = normalizeDraftKey(key);
  if (!normalizedKey || typeof store?.taskContextDrafts?.set !== 'function') return;
  const normalizedValue = normalizeTaskContext(value);
  if (normalizedValue) store.taskContextDrafts.set(normalizedKey, normalizedValue);
  else store.taskContextDrafts.delete?.(normalizedKey);
}

export function writeComposerAttachmentDraft(store, key, value) {
  if (typeof store?.setAttachmentDraft === 'function') {
    store.setAttachmentDraft(key, value);
    markComposerDraftMutation(store, key);
    return;
  }
  const normalizedKey = normalizeDraftKey(key);
  if (!normalizedKey || typeof store?.attachmentDrafts?.set !== 'function') return;
  if (Array.isArray(value) && value.length > 0) {
    store.attachmentDrafts.set(normalizedKey, [...value]);
  } else {
    store.attachmentDrafts.delete?.(normalizedKey);
  }
  markComposerDraftMutation(store, normalizedKey);
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

// Unlike the invalidation revision, this counter records every message-payload
// draft write. It lets a send operation distinguish newer text or attachments
// typed while its request was in flight, even when the value is identical;
// selecting or refreshing the Agent context is tracked separately.
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
