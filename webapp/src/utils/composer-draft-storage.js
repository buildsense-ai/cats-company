import {
  getStorage,
  readStorageValue,
  removeStorageValue,
  writeStorageValue,
} from './storage-access';

export const COMPOSER_DRAFT_STORAGE_PREFIX = 'catsco_composer_drafts:v1:';
export const NEW_TASK_DRAFT_KEY = 'new-task';
const COMPOSER_DRAFT_STATE_STORAGE_PREFIX = 'catsco_composer_draft_state:v1:';
const COMPOSER_DRAFT_FAST_PATH_STORAGE_PREFIX = 'catsco_composer_draft_fast:v1:';
const COMPOSER_DRAFT_SENT_MARKER_STORAGE_PREFIX = 'catsco_composer_draft_sent:v1:';
const COMPOSER_DRAFT_ATTACHMENT_INTENT_STORAGE_PREFIX = 'catsco_composer_draft_attachment_intent:v1:';
const COMPOSER_DRAFT_FIELD_MARKER_STORAGE_PREFIX = 'catsco_composer_draft_field:v1:';
const COMPOSER_DRAFT_VERSION_LINEAGE_STORAGE_PREFIX = 'catsco_composer_draft_lineage:v1:';
const COMPOSER_DRAFT_LOGOUT_STORAGE_KEY = 'catsco_composer_draft_logout:v1';
const COMPOSER_DRAFT_CLEAR_REASON_LOGOUT = 'logout';
const COMPOSER_DRAFT_VERSION_MAP_NAME = 'draftVersions';
const COMPOSER_DRAFT_CLEARED_VERSION_MAP_NAME = 'clearedDraftVersions';
const COMPOSER_DRAFT_FIELD_MARKER_MAP_NAME = 'fieldMarkers';
const COMPOSER_DRAFT_CLEARED_FIELD_VERSION_MAP_NAME = 'clearedFieldVersions';
const COMPOSER_DRAFT_WRITE_LOCK_PREFIX = 'catsco-composer-draft-write:';
const COMPOSER_DRAFT_LOCK_DATABASE = 'catsco_composer_draft_locks:v1';
const COMPOSER_DRAFT_LOCK_STORE = 'locks';
const COMPOSER_DRAFT_LOCK_UNAVAILABLE = Symbol('composer-draft-lock-unavailable');
const COMPOSER_DRAFT_LOCK_FAILED = Symbol('composer-draft-lock-failed');
const PHONE_UPLOAD_REMOVED_FILE_KEYS = 'removed_file_keys';

// Async upload callbacks can outlive the composer that created them. Keep a
// process-local revision per store/key so a sent or explicitly replaced draft
// can invalidate callbacks without putting transient state in sessionStorage.
const draftRevisionStores = new WeakMap();
const draftMutationStores = new WeakMap();
const draftStoreRegistries = new Map();
const objectDraftStoreRegistries = new WeakMap();
let draftVersionSequence = 0;
let composerDraftLockDatabase = null;

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

function fastPathStorageKeyForDraftStorageKey(storageKey) {
  if (!storageKey || !storageKey.startsWith(COMPOSER_DRAFT_STORAGE_PREFIX)) return '';
  return `${COMPOSER_DRAFT_FAST_PATH_STORAGE_PREFIX}${storageKey.slice(COMPOSER_DRAFT_STORAGE_PREFIX.length)}`;
}

// Each completed send gets its own immutable marker. A shared marker map
// would be another read/modify/write race: a normal draft persist could read
// an older map and erase a marker written by a concurrent send.
function sentMarkerStoragePrefixForDraftStorageKey(storageKey) {
  return storageKey
    ? `${COMPOSER_DRAFT_SENT_MARKER_STORAGE_PREFIX}${encodeURIComponent(storageKey)}:`
    : '';
}

function sentMarkerStorageKey(storageKey, key, version) {
  const prefix = sentMarkerStoragePrefixForDraftStorageKey(storageKey);
  const normalizedKey = normalizeDraftKey(key);
  const normalizedVersion = normalizeDraftVersion(version);
  return prefix && normalizedKey && normalizedVersion
    ? `${prefix}${encodeURIComponent(normalizedKey)}:${encodeURIComponent(normalizedVersion)}`
    : '';
}

// Attachment changes are independent operations, unlike the older complete
// draft snapshot. Keep their delete/restore intent in separate immutable
// records so a stale text-only snapshot cannot bring a removed file back when
// the browser has no cross-document lock primitive available.
function attachmentIntentStoragePrefixForDraftStorageKey(storageKey) {
  return storageKey
    ? `${COMPOSER_DRAFT_ATTACHMENT_INTENT_STORAGE_PREFIX}${encodeURIComponent(storageKey)}:`
    : '';
}

function attachmentIntentStorageKey(storageKey, key, id) {
  const prefix = attachmentIntentStoragePrefixForDraftStorageKey(storageKey);
  const normalizedKey = normalizeDraftKey(key);
  return prefix && normalizedKey && id
    ? `${prefix}${encodeURIComponent(normalizedKey)}:${encodeURIComponent(id)}`
    : '';
}

// A whole draft snapshot is convenient when a real cross-document lock is
// available. Keep one bounded, per-field record alongside it as well: if a
// privacy-hardened browser exposes neither Web Locks nor IndexedDB, two tabs
// can still independently commit different fields without the later snapshot
// erasing the earlier one.
function draftFieldMarkerStoragePrefixForDraftStorageKey(storageKey) {
  return storageKey
    ? `${COMPOSER_DRAFT_FIELD_MARKER_STORAGE_PREFIX}${encodeURIComponent(storageKey)}:`
    : '';
}

function draftFieldMarkerStorageKey(storageKey, kind, key) {
  const prefix = draftFieldMarkerStoragePrefixForDraftStorageKey(storageKey);
  const normalizedKey = normalizeDraftKey(key);
  return prefix && draftFieldDefinitions?.[kind] && normalizedKey
    ? `${prefix}${encodeURIComponent(kind)}:${encodeURIComponent(normalizedKey)}`
    : '';
}

// The compatibility marker above is intentionally stable for older readers,
// but a stable key alone cannot survive two unlocked renderers doing a
// read/modify/write at the same time. Every mutation also gets an immutable
// journal entry; hydration chooses the newest entry and the stable marker is
// only a fast path for legacy consumers.
function draftFieldMarkerHistoryStorageKey(storageKey, kind, key, id) {
  const prefix = draftFieldMarkerStoragePrefixForDraftStorageKey(storageKey);
  const normalizedKey = normalizeDraftKey(key);
  const normalizedId = normalizeDraftVersion(id);
  return prefix && draftFieldDefinitions?.[kind] && normalizedKey && normalizedId
    ? `${prefix}${encodeURIComponent(kind)}:${encodeURIComponent(normalizedKey)}:${encodeURIComponent(normalizedId)}`
    : '';
}

function draftVersionLineageStoragePrefixForDraftStorageKey(storageKey) {
  return storageKey
    ? `${COMPOSER_DRAFT_VERSION_LINEAGE_STORAGE_PREFIX}${encodeURIComponent(storageKey)}:`
    : '';
}

function draftVersionLineageStorageKey(storageKey, key, version) {
  const prefix = draftVersionLineageStoragePrefixForDraftStorageKey(storageKey);
  const normalizedKey = normalizeDraftKey(key);
  const normalizedVersion = normalizeDraftVersion(version);
  return prefix && normalizedKey && normalizedVersion
    ? `${prefix}${encodeURIComponent(normalizedKey)}:${encodeURIComponent(normalizedVersion)}`
    : '';
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

function draftStateWriteIsCurrent(
  stateStorageKey,
  storage,
  observedUpdatedAt,
  observedCleared,
  { allowNewerNonCleared = false } = {},
) {
  const baselineAt = normalizedUpdatedAt(observedUpdatedAt);
  return storageTargets(storage).every((target) => {
    const current = readDraftSnapshot(stateStorageKey, target);
    if (!current) return true;
    const currentAt = normalizedUpdatedAt(current.updatedAt);
    if (currentAt > baselineAt) {
      // A newer non-cleared state is another ordinary draft commit. The
      // field journal can merge it with this writer's fields, so it is safe
      // to advance the state timestamp. A newer clear is a destructive
      // frontier and must always win this conditional write.
      return allowNewerNonCleared && current.cleared !== true;
    }
    // Equal timestamps are possible across renderers. A clear state which was
    // not present in the caller's read is still newer for the purpose of a
    // conditional write; otherwise a delayed stale writer can replace it.
    return !(currentAt === baselineAt && current.cleared === true && !observedCleared);
  });
}

function isInjectedBrowserPrimitive(value, method) {
  return typeof value?.[method] === 'function'
    && Object.getPrototypeOf(value) === Object.prototype;
}

function isNodeRuntime() {
  return typeof process !== 'undefined' && Boolean(process.versions?.node);
}

// A fresh SkillHub workspace can live in a different document. localStorage
// itself intentionally provides no cross-document locking, so serialize every
// shared-snapshot read/modify/write. Web Locks is the preferred primitive;
// IndexedDB readwrite transactions are the fallback for Safari and older
// browsers. Custom storage adapters are deliberately synchronous because they
// are single-process seams, not shared browser storage.
function usesSharedBrowserDraftStorage(storage) {
  if (typeof storage !== 'string' || !storageTargets(storage).includes('localStorage')) {
    return false;
  }
  const localStorageArea = getStorage('localStorage');
  const isNativeStorage = Object.prototype.toString.call(localStorageArea) === '[object Storage]';
  // Node currently exposes an experimental LockManager in test environments.
  // A plain injected primitive is still useful for exercising the browser
  // branch, but the in-memory test storage itself is one process and must stay
  // synchronous.
  return isNativeStorage
    || isInjectedBrowserPrimitive(globalThis.navigator?.locks, 'request')
    || isInjectedBrowserPrimitive(globalThis.indexedDB, 'open');
}

function supportsComposerDraftWriteLock(storage) {
  return usesSharedBrowserDraftStorage(storage)
    && typeof globalThis.navigator?.locks?.request === 'function'
    && (!isNodeRuntime() || isInjectedBrowserPrimitive(globalThis.navigator?.locks, 'request'));
}

function openComposerDraftLockDatabase() {
  const indexedDB = globalThis.indexedDB;
  if (typeof indexedDB?.open !== 'function') return null;
  if (composerDraftLockDatabase?.indexedDB === indexedDB) {
    return composerDraftLockDatabase.promise;
  }

  const promise = new Promise((resolve) => {
    let request;
    try {
      request = indexedDB.open(COMPOSER_DRAFT_LOCK_DATABASE, 1);
    } catch {
      resolve(null);
      return;
    }
    request.onupgradeneeded = () => {
      try {
        if (!request.result.objectStoreNames.contains(COMPOSER_DRAFT_LOCK_STORE)) {
          request.result.createObjectStore(COMPOSER_DRAFT_LOCK_STORE);
        }
      } catch {
        // The request error handler below turns an unusable database into a
        // conservative no-clear result.
      }
    };
    request.onsuccess = () => resolve(request.result || null);
    request.onerror = () => resolve(null);
    request.onblocked = () => resolve(null);
  });
  composerDraftLockDatabase = { indexedDB, promise };
  return promise;
}

function withIndexedDBComposerDraftWriteLock(storageKey, operation) {
  const databasePromise = openComposerDraftLockDatabase();
  if (!databasePromise) return null;
  return databasePromise.then((database) => {
    if (!database) return COMPOSER_DRAFT_LOCK_UNAVAILABLE;
    return new Promise((resolve) => {
      let completed = false;
      let operationStarted = false;
      let operationResult = false;
      const finish = (value) => {
        if (completed) return;
        completed = true;
        resolve(value);
      };

      let transaction;
      try {
        transaction = database.transaction(COMPOSER_DRAFT_LOCK_STORE, 'readwrite');
        const lockStore = transaction.objectStore(COMPOSER_DRAFT_LOCK_STORE);
        const request = lockStore.put(Date.now(), `${COMPOSER_DRAFT_WRITE_LOCK_PREFIX}${storageKey}`);
        request.onsuccess = () => {
          operationStarted = true;
          try {
            const result = operation();
            // Locked operations are intentionally synchronous. A promise here
            // would outlive the transaction and therefore would not be safe.
            if (result && typeof result.then === 'function') {
              Promise.resolve(result).catch(() => {});
              operationResult = false;
            } else {
              operationResult = result;
            }
          } catch {
            operationResult = false;
            try {
              transaction.abort();
            } catch {
              finish(false);
            }
          }
        };
        request.onerror = () => finish(
          operationStarted ? COMPOSER_DRAFT_LOCK_FAILED : COMPOSER_DRAFT_LOCK_UNAVAILABLE,
        );
        transaction.oncomplete = () => finish(
          operationStarted ? operationResult : COMPOSER_DRAFT_LOCK_UNAVAILABLE,
        );
        transaction.onerror = () => finish(
          operationStarted ? COMPOSER_DRAFT_LOCK_FAILED : COMPOSER_DRAFT_LOCK_UNAVAILABLE,
        );
        transaction.onabort = () => finish(
          operationStarted ? COMPOSER_DRAFT_LOCK_FAILED : COMPOSER_DRAFT_LOCK_UNAVAILABLE,
        );
      } catch {
        finish(COMPOSER_DRAFT_LOCK_UNAVAILABLE);
      }
    });
  }).catch(() => COMPOSER_DRAFT_LOCK_UNAVAILABLE);
}

function withComposerDraftWriteLock(storageKey, storage, operation, { failClosed = false } = {}) {
  if (!storageKey || !usesSharedBrowserDraftStorage(storage)) return operation();

  const unavailable = () => (failClosed ? COMPOSER_DRAFT_LOCK_UNAVAILABLE : operation());
  const fallback = () => {
    const indexedDBLock = withIndexedDBComposerDraftWriteLock(storageKey, operation);
    if (!indexedDBLock) return unavailable();
    return indexedDBLock.then((result) => {
      if (
        result !== COMPOSER_DRAFT_LOCK_UNAVAILABLE
        && result !== COMPOSER_DRAFT_LOCK_FAILED
      ) return result;
      return unavailable();
    });
  };

  if (!supportsComposerDraftWriteLock(storage)) return fallback();

  let operationStarted = false;
  try {
    return Promise.resolve(globalThis.navigator.locks.request(
      `${COMPOSER_DRAFT_WRITE_LOCK_PREFIX}${storageKey}`,
      { mode: 'exclusive' },
      () => {
        operationStarted = true;
        return operation();
      },
    )).catch(() => (operationStarted ? COMPOSER_DRAFT_LOCK_FAILED : fallback()));
  } catch {
    return fallback();
  }
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

function normalizedPhoneUploadRemovedFileKeys(value) {
  if (!Array.isArray(value)) return [];
  return [...new Set(value
    .map((fileKey) => String(fileKey || '').trim())
    .filter(Boolean))];
}

function normalizePhoneUploadSession(value) {
  if (!isPhoneUploadSession(value)) return null;
  const session = { ...value };
  const removedFileKeys = normalizedPhoneUploadRemovedFileKeys(session[PHONE_UPLOAD_REMOVED_FILE_KEYS]);
  if (removedFileKeys.length > 0) session[PHONE_UPLOAD_REMOVED_FILE_KEYS] = removedFileKeys;
  else delete session[PHONE_UPLOAD_REMOVED_FILE_KEYS];
  return session;
}

function phoneUploadRemovedFileKeys(session) {
  return new Set(normalizedPhoneUploadRemovedFileKeys(
    session?.[PHONE_UPLOAD_REMOVED_FILE_KEYS],
  ));
}

function attachmentDraftKey(attachment) {
  return String(
    attachment?.content?.payload?.file_key
    || attachment?.content?.payload?.url
    || attachment?.file_key
    || attachment?.url
    || attachment?.name
    || '',
  );
}

function attachmentDraftIdentity(attachment) {
  const key = attachmentDraftKey(attachment);
  if (key) return `key:${key}`;
  try {
    const serialized = JSON.stringify(attachment);
    return serialized ? `value:${serialized}` : '';
  } catch {
    return '';
  }
}

function attachmentIntentForAttachment(intents, key, attachment) {
  const normalizedKey = normalizeDraftKey(key);
  const identity = attachmentDraftIdentity(attachment);
  return normalizedKey && identity ? intents?.get?.(normalizedKey)?.get?.(identity) || null : null;
}

function attachmentIntentTargetsAttachment(intent, attachment) {
  // Markers written before this field was introduced were identity-only. Keep
  // honoring them broadly, while new markers are precise snapshots so an old
  // X cannot hide a concurrent replacement with the same file key.
  return !Object.prototype.hasOwnProperty.call(intent || {}, 'attachment')
    || draftValueEqual(intent.attachment, attachment);
}

function attachmentRestoreTokenMatchesAttachment(token, attachment) {
  return token?.attachmentIdentity === attachmentDraftIdentity(attachment)
    && attachmentIntentTargetsAttachment(token, attachment);
}

function attachmentRestoreTokensForAttachments(tokens, attachments) {
  const candidates = Array.isArray(attachments) ? attachments : [];
  return (Array.isArray(tokens) ? tokens : []).filter((token) => (
    candidates.some((attachment) => attachmentRestoreTokenMatchesAttachment(token, attachment))
  ));
}

function attachmentRestoreRemovalIds(tokens) {
  return new Set((Array.isArray(tokens) ? tokens : []).flatMap((token) => (
    Array.isArray(token?.removes) ? token.removes : []
  )).map((id) => String(id || '').trim()).filter(Boolean));
}

function pendingAttachmentRestoreRemovalIds(mutations, attachmentIntentMetadata) {
  const supersededRemovalIds = attachmentIntentMetadata?.supersededRemovalIds;
  if (!supersededRemovalIds?.size) return new Set();
  const tokens = (Array.isArray(mutations) ? mutations : []).flatMap((mutation) => {
    const attachments = ['append', 'replace'].includes(mutation?.type)
      && Array.isArray(mutation?.attachments)
      ? mutation.attachments
      : [];
    return attachmentRestoreTokensForAttachments(mutation?.attachmentRestoreTokens, attachments);
  });
  return new Set([...attachmentRestoreRemovalIds(tokens)].filter((id) => (
    supersededRemovalIds.has(id)
  )));
}

function attachmentIsRemovedByIntent(intents, key, attachment) {
  const intent = attachmentIntentForAttachment(intents, key, attachment);
  if (!intent) return false;
  if (intent.removals?.size) {
    return [...intent.removals.values()].some((removal) => (
      attachmentIntentTargetsAttachment(removal, attachment)
    ));
  }
  return (intent.removeIds?.size || 0) > 0;
}

function filterAttachmentsByIntent(attachments, intents, key) {
  const current = Array.isArray(attachments) ? attachments : [];
  if (!intents?.size || !normalizeDraftKey(key)) return [...current];
  return current.filter((attachment) => !attachmentIsRemovedByIntent(intents, key, attachment));
}

function attachmentIntentsIgnoringRemovalIds(intents, ignoredRemovalIds) {
  if (!intents?.size || !ignoredRemovalIds?.size) return intents;
  const filteredIntents = new Map();
  intents.forEach((byIdentity, key) => {
    const filteredByIdentity = new Map();
    byIdentity.forEach((intent, identity) => {
      const removals = intent?.removals?.size
        ? new Map([...intent.removals].filter(([id]) => !ignoredRemovalIds.has(id)))
        : null;
      const removeIds = removals
        ? new Set(removals.keys())
        : new Set([...(intent?.removeIds || [])].filter((id) => !ignoredRemovalIds.has(id)));
      if (removeIds.size === 0) return;
      filteredByIdentity.set(identity, {
        removeIds,
        ...(removals ? { removals } : {}),
      });
    });
    if (filteredByIdentity.size > 0) filteredIntents.set(key, filteredByIdentity);
  });
  return filteredIntents;
}

function filterDraftMapsByAttachmentIntents(draftMaps, intents) {
  if (!intents?.size) return draftMaps;
  const attachmentDrafts = draftMaps?.attachment;
  if (!attachmentDrafts?.size) return draftMaps;
  let nextAttachmentDrafts = null;
  attachmentDrafts.forEach((value, key) => {
    const current = draftFieldDefinitions.attachment.read(value);
    const next = filterAttachmentsByIntent(current, intents, key);
    if (draftValueEqual(current, next)) return;
    if (!nextAttachmentDrafts) nextAttachmentDrafts = new Map(attachmentDrafts);
    const normalized = draftFieldDefinitions.attachment.normalize(next);
    if (normalized === null) nextAttachmentDrafts.delete(key);
    else nextAttachmentDrafts.set(key, normalized);
  });
  return nextAttachmentDrafts ? { ...draftMaps, attachment: nextAttachmentDrafts } : draftMaps;
}

function attachmentsRemovedByReplacement(currentAttachments, nextAttachments) {
  const remaining = Array.isArray(nextAttachments) ? [...nextAttachments] : [];
  return (Array.isArray(currentAttachments) ? currentAttachments : []).filter((attachment) => {
    const matchingIndex = remaining.findIndex((candidate) => (
      draftValueEqual(candidate, attachment)
    ));
    if (matchingIndex < 0) return true;
    remaining.splice(matchingIndex, 1);
    return false;
  });
}

function attachmentMutationTargetsCurrentAttachment(mutation, attachments) {
  const hasAttachmentSnapshot = mutation
    && Object.prototype.hasOwnProperty.call(mutation, 'attachment');
  const attachmentKey = String(mutation?.attachmentKey || '');
  return (Array.isArray(attachments) ? attachments : []).some((attachment) => (
    hasAttachmentSnapshot
      ? draftValueEqual(attachment, mutation.attachment)
      : attachmentKey && attachmentDraftKey(attachment) === attachmentKey
  ));
}

function attachmentMutationTargetFromAttachments(mutation, attachments) {
  const current = Array.isArray(attachments) ? attachments : [];
  const exactMatch = current.find((attachment) => (
    attachmentMutationTargetsCurrentAttachment(mutation, [attachment])
  ));
  if (exactMatch) return exactMatch;
  const attachmentKey = String(mutation?.attachmentKey || '');
  return attachmentKey
    ? current.find((attachment) => attachmentDraftKey(attachment) === attachmentKey) || null
    : null;
}

function applyAttachmentMutationToList(currentAttachments, mutation) {
  const current = Array.isArray(currentAttachments) ? [...currentAttachments] : [];
  if (mutation?.type === 'replace') {
    return Array.isArray(mutation.attachments) ? [...mutation.attachments] : [];
  }
  if (mutation?.type === 'append') {
    const knownKeys = new Set(current.map(attachmentDraftKey).filter(Boolean));
    const next = [...current];
    (Array.isArray(mutation.attachments) ? mutation.attachments : []).forEach((attachment) => {
      const fileKey = attachmentDraftKey(attachment);
      const duplicate = fileKey
        ? knownKeys.has(fileKey)
        : next.some((candidate) => draftValueEqual(candidate, attachment));
      if (duplicate) return;
      next.push(attachment);
      if (fileKey) knownKeys.add(fileKey);
    });
    return next;
  }
  if (mutation?.type === 'remove') {
    let removedOne = false;
    return current.filter((attachment) => {
      const matches = attachmentMutationTargetsCurrentAttachment(mutation, [attachment]);
      if (!matches || removedOne) return true;
      removedOne = true;
      return false;
    });
  }
  return current;
}

function attachmentMutationBaseSnapshots(currentAttachments, precedingMutations = []) {
  const snapshots = [Array.isArray(currentAttachments) ? [...currentAttachments] : []];
  let next = snapshots[0];
  precedingMutations.forEach((mutation) => {
    next = applyAttachmentMutationToList(next, mutation);
    if (!snapshots.some((snapshot) => draftValueEqual(snapshot, next))) {
      snapshots.push(next);
    }
  });
  return snapshots;
}

function attachmentMutationMatchesCurrentBase(mutation, currentAttachments) {
  const baseSnapshots = Array.isArray(mutation?.attachmentBaseSnapshots)
    ? mutation.attachmentBaseSnapshots
    : [];
  if (baseSnapshots.length === 0) return true;

  // Removing an attachment is an identity-level intent. A sibling context
  // can safely append an unrelated file while this operation waits for the
  // shared lock; requiring the entire array to stay identical would then
  // discard the user's X and make the removed file reappear after a handoff.
  // Still require the attachment the user acted on to be unchanged from one
  // of the known bases, so an old removal cannot delete a freshly replaced
  // attachment that merely happens to share a display name or file key.
  if (mutation?.type === 'remove') {
    return baseSnapshots.some((snapshot) => snapshot.some((baseAttachment) => {
      const isTarget = attachmentMutationTargetsCurrentAttachment(mutation, [baseAttachment]);
      return isTarget && currentAttachments.some(
        (currentAttachment) => draftValueEqual(currentAttachment, baseAttachment),
      );
    }));
  }

  return baseSnapshots.some((snapshot) => draftValueEqual(snapshot, currentAttachments));
}

const draftAgentFields = [
  'uid',
  'id',
  'username',
  'display_name',
  'topic_id',
  'avatar_url',
  'is_bot',
  'is_owner',
  'isOwner',
  'relation',
  'account_type',
];

function normalizeDraftAgent(value) {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return null;
  const identity = value.uid ?? value.id;
  if (identity === undefined || identity === null || String(identity).trim() === '') return null;

  const agent = {};
  draftAgentFields.forEach((field) => {
    const candidate = value[field];
    if (candidate === null || ['string', 'number', 'boolean'].includes(typeof candidate)) {
      agent[field] = candidate;
    }
  });
  if (agent.uid === undefined || agent.uid === null || String(agent.uid).trim() === '') {
    agent.uid = identity;
  }
  return agent;
}

function normalizeTaskContext(value) {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return null;
  const agent = normalizeDraftAgent(value.agent);
  const candidateProjectId = Number(value.projectId);
  const projectId = Number.isFinite(candidateProjectId) && candidateProjectId > 0
    ? candidateProjectId
    : 0;
  const projectName = projectId > 0 ? String(value.projectName || '') : '';
  if (!agent && projectId <= 0) return null;
  return { agent, projectId, projectName };
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
      return normalizePhoneUploadSession(value);
    },
    read(value) {
      return normalizePhoneUploadSession(value);
    },
  },
  taskContext: {
    mapName: 'taskContextDrafts',
    getter: 'getTaskContextDraft',
    setter: 'setTaskContextDraft',
    normalize: normalizeTaskContext,
    read: normalizeTaskContext,
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

function copyCounterState(sourceStore, targetStore, counterStores) {
  const source = counterStores.get(sourceStore);
  if (!source) return;
  const target = counterStores === draftRevisionStores
    ? revisionMapFor(targetStore)
    : mutationMapFor(targetStore);
  if (!target) return;
  source.forEach((value, key) => {
    target.set(key, Math.max(Number(target.get(key)) || 0, Number(value) || 0));
  });
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

function normalizeDraftVersion(value) {
  return typeof value === 'string' && value ? value : null;
}

function nextDraftVersion() {
  draftVersionSequence += 1;
  const randomPart = globalThis.crypto?.randomUUID?.()
    || Math.random().toString(36).slice(2);
  return `${Date.now().toString(36)}:${draftVersionSequence.toString(36)}:${randomPart}`;
}

function draftMapsFromSnapshot(snapshot = {}) {
  const source = snapshot && typeof snapshot === 'object' ? snapshot : {};
  return Object.fromEntries(Object.entries(draftFieldDefinitions).map(([kind, definition]) => [
    kind,
    new Map(draftEntries(source[definition.mapName], definition.normalize)),
  ]));
}

function draftKeysFromMaps(draftMaps = {}) {
  return new Set(Object.values(draftMaps).flatMap((map) => [...(map?.keys?.() || [])]));
}

function draftVersionsFromSnapshot(snapshot = {}, draftMaps = draftMapsFromSnapshot(snapshot)) {
  const source = snapshot && typeof snapshot === 'object' ? snapshot : {};
  const versions = new Map(draftEntries(
    source[COMPOSER_DRAFT_VERSION_MAP_NAME],
    normalizeDraftVersion,
  ));
  const legacyPrefix = `legacy:${normalizedUpdatedAt(source.updatedAt)}`;
  draftKeysFromMaps(draftMaps).forEach((key) => {
    if (!versions.has(key)) versions.set(key, `${legacyPrefix}:${key}`);
  });
  return versions;
}

function draftFieldMarkerManifestFromSnapshot(snapshot = {}) {
  const entries = Array.isArray(snapshot?.[COMPOSER_DRAFT_FIELD_MARKER_MAP_NAME])
    ? snapshot[COMPOSER_DRAFT_FIELD_MARKER_MAP_NAME]
    : [];
  return new Map(entries.flatMap((entry) => (
    Array.isArray(entry)
    && entry.length === 2
    && typeof entry[0] === 'string'
    && typeof entry[1] === 'string'
    && entry[0]
    && entry[1]
      ? [[entry[0], entry[1]]]
      : []
  )));
}

function snapshotHasDraftFieldMarkerManifest(snapshot = {}) {
  return Array.isArray(snapshot?.[COMPOSER_DRAFT_FIELD_MARKER_MAP_NAME]);
}

function compareGeneratedDraftVersions(left, right) {
  const leftMatch = /^([0-9a-z]+):([0-9a-z]+):/i.exec(String(left || ''));
  const rightMatch = /^([0-9a-z]+):([0-9a-z]+):/i.exec(String(right || ''));
  if (!leftMatch || !rightMatch) return 0;
  const leftTime = Number.parseInt(leftMatch[1], 36);
  const rightTime = Number.parseInt(rightMatch[1], 36);
  const leftSequence = Number.parseInt(leftMatch[2], 36);
  const rightSequence = Number.parseInt(rightMatch[2], 36);
  if (![leftTime, rightTime, leftSequence, rightSequence].every(Number.isFinite)) return 0;
  if (leftTime !== rightTime) return leftTime > rightTime ? 1 : -1;
  if (leftSequence !== rightSequence) return leftSequence > rightSequence ? 1 : -1;
  return 0;
}

function draftFieldMarkerManifestForMaps(manifest, draftMaps, dirtyFields = null) {
  const next = new Map(manifest || []);
  Object.entries(draftFieldDefinitions).forEach(([kind]) => {
    const field = draftMaps?.[kind] || new Map();
    [...next.keys()].forEach((fieldKey) => {
      const separator = fieldKey.indexOf(':');
      if (separator < 0 || fieldKey.slice(0, separator) !== kind) return;
      const key = fieldKey.slice(separator + 1);
      if (
        !field.has(key)
        && (!dirtyFields || dirtyFields?.[kind]?.has?.(key))
      ) next.delete(fieldKey);
    });
  });
  return next;
}

function newerDraftFieldMarker(current, candidate, index) {
  if (!current) return { ...candidate, index };
  // The draft version is allocated at the user mutation boundary. Prefer it
  // over the enclosing persist timestamp: a renderer which was suspended
  // after reading an older value must not win merely because it eventually
  // wrote its marker with a later wall clock.
  const versionOrder = compareGeneratedDraftVersions(candidate.version, current.version);
  if (versionOrder > 0) return { ...candidate, index };
  if (versionOrder < 0) return current;
  const candidateOrderAt = Math.max(
    normalizedUpdatedAt(candidate.committedAt),
    normalizedUpdatedAt(candidate.updatedAt),
  );
  const currentOrderAt = Math.max(
    normalizedUpdatedAt(current.committedAt),
    normalizedUpdatedAt(current.updatedAt),
  );
  if (candidateOrderAt > currentOrderAt) return { ...candidate, index };
  if (candidateOrderAt < currentOrderAt) return current;
  const candidateId = String(candidate.id || '');
  const currentId = String(current.id || '');
  if (candidateId > currentId || (candidateId === currentId && index < current.index)) {
    return { ...candidate, index };
  }
  return current;
}

function readCurrentDraftFieldMarker(markerStorageKey, storage, expectedKind = '', expectedKey = '') {
  if (!markerStorageKey) return null;
  const markerPrefix = markerStorageKey.slice(0, markerStorageKey.lastIndexOf(':') + 1);
  // Stable keys are of the form `<prefix><kind>:<key>`. History keys append
  // `:<marker-id>`; scan the complete account journal so a stale renderer
  // cannot hide a newer immutable record by overwriting the stable key.
  const stableMarker = markerStorageKey;
  let selected = null;
  storageTargets(storage).forEach((target, targetIndex) => {
    const area = getStorage(target);
    if (!area) return;
    try {
      for (let index = 0; index < area.length; index += 1) {
        const candidateKey = area.key(index);
        if (candidateKey !== stableMarker && !candidateKey?.startsWith(markerPrefix)) continue;
        const marker = readDraftSnapshot(candidateKey, target);
        if (!marker || typeof marker !== 'object') continue;
        if (expectedKind && marker.kind !== expectedKind) continue;
        if (expectedKey && normalizeDraftKey(marker.key) !== expectedKey) continue;
        selected = newerDraftFieldMarker(selected, marker, targetIndex * 1_000_000 + index);
      }
    } catch {
      // Keep a marker recovered from another storage target.
    }
  });
  return selected;
}

function readDraftFieldMarkers(
  storageKey,
  storage,
  logoutFenceUpdatedAt = 0,
  clearedDraftVersions = new Map(),
  {
    clearedFieldMarkerIds = new Set(),
    clearedFieldMarkerVersions = new Map(),
    clearedBeforeUpdatedAt = new Map(),
  } = {},
) {
  const prefix = draftFieldMarkerStoragePrefixForDraftStorageKey(storageKey);
  const currentLogoutFenceAt = normalizedUpdatedAt(logoutFenceUpdatedAt);
  const markers = new Map();
  let updatedAt = 0;
  let logoutFenceAt = currentLogoutFenceAt;
  if (!prefix) return { markers, updatedAt, logoutFenceAt };

  storageTargets(storage).forEach((target, index) => {
    const area = getStorage(target);
    if (!area) return;
    try {
      for (let markerIndex = 0; markerIndex < area.length; markerIndex += 1) {
        const markerStorageKey = area.key(markerIndex);
        if (typeof markerStorageKey !== 'string' || !markerStorageKey.startsWith(prefix)) continue;
        const marker = readDraftSnapshot(markerStorageKey, target);
        const kind = typeof marker?.kind === 'string' && draftFieldDefinitions[marker.kind]
          ? marker.kind
          : '';
        const key = normalizeDraftKey(marker?.key);
        const markerFenceAt = normalizedUpdatedAt(marker?.logoutFenceAt);
        const deleted = marker?.deleted === true;
        const version = normalizeDraftVersion(marker?.version);
        const markerId = String(marker?.id || markerStorageKey);
        const markerCutoff = normalizedUpdatedAt(
          clearedBeforeUpdatedAt?.get?.(key),
        );
        if (
          marker?.storageKey !== storageKey
          || !kind
          || !key
          || markerFenceAt < currentLogoutFenceAt
          || clearedFieldMarkerIds?.has?.(markerId)
          || (version && clearedFieldMarkerVersions?.get?.(key)?.has?.(version))
          || (version && isDraftVersionCleared(clearedDraftVersions, key, version))
          // A writer which started from a pre-send baseline may physically
          // arrive after the send marker. Its marker must not recreate the
          // old draft merely because it received a newer wall-clock write
          // timestamp. New edits made after observing the send use the send
          // timestamp as their baseline and therefore remain visible.
          || (
            markerCutoff > 0
            && normalizedUpdatedAt(marker?.baseUpdatedAt) < markerCutoff
          )
        ) continue;
        const value = deleted ? null : draftFieldDefinitions[kind].normalize(marker?.value);
        if (!deleted && value === null) continue;
        const candidate = {
          id: markerId,
          kind,
          key,
          deleted,
          ...(deleted ? {} : { value }),
          version,
          baseUpdatedAt: normalizedUpdatedAt(marker?.baseUpdatedAt),
          updatedAt: normalizedUpdatedAt(marker?.updatedAt),
          committedAt: normalizedUpdatedAt(marker?.committedAt),
          logoutFenceAt: markerFenceAt,
        };
        const markerKey = `${kind}:${key}`;
        markers.set(markerKey, newerDraftFieldMarker(markers.get(markerKey), candidate, index));
        updatedAt = Math.max(updatedAt, candidate.updatedAt);
        logoutFenceAt = Math.max(logoutFenceAt, markerFenceAt);
      }
    } catch {
      // Keep independent field records recovered from another storage target.
    }
  });
  return { markers, updatedAt, logoutFenceAt };
}

function applyDraftFieldMarkers(
  snapshot = {},
  markers = new Map(),
  { clearedFieldMarkerIds = new Set() } = {},
) {
  if (!markers?.size && !snapshotHasDraftFieldMarkerManifest(snapshot)) return snapshot;
  const maps = draftMapsFromSnapshot(snapshot);
  const versions = draftVersionsFromSnapshot(snapshot, maps);
  const hasManifest = snapshotHasDraftFieldMarkerManifest(snapshot);
  const manifest = draftFieldMarkerManifestFromSnapshot(snapshot);
  const snapshotUpdatedAt = normalizedUpdatedAt(snapshot?.updatedAt);
  const newestVersionMarkers = new Map();

  // A manifest is a claim about which bounded field record supplied each
  // value. If that record was superseded (or explicitly cleared by a send),
  // do not trust the copied value in a stale whole-snapshot write. The next
  // valid field marker, if any, is applied below.
  if (hasManifest) {
    const markerById = new Map([...markers.values()].map((marker) => [marker.id, marker]));
    [...manifest].forEach(([fieldKey, markerId]) => {
      const marker = markerById.get(markerId);
      if (
        clearedFieldMarkerIds?.has?.(markerId)
        || !marker
        || `${marker.kind}:${marker.key}` !== fieldKey
      ) {
        const separator = fieldKey.indexOf(':');
        const kind = separator >= 0 ? fieldKey.slice(0, separator) : '';
        const key = separator >= 0 ? fieldKey.slice(separator + 1) : '';
        if (draftFieldDefinitions[kind]) maps[kind].delete(key);
        manifest.delete(fieldKey);
      }
    });
  }
  markers.forEach((marker) => {
    const field = maps[marker.kind];
    if (!field) return;
    const fieldKey = `${marker.kind}:${marker.key}`;
    const snapshotAlreadyIncludesMarker = hasManifest && manifest.get(fieldKey) === marker.id;
    const shouldApply = hasManifest
      ? !snapshotAlreadyIncludesMarker
      : marker.updatedAt >= snapshotUpdatedAt;
    if (shouldApply) {
      if (marker.deleted) field.delete(marker.key);
      else field.set(marker.key, marker.value);
      if (marker.deleted) manifest.delete(fieldKey);
      else manifest.set(fieldKey, marker.id);
    }
    const current = newestVersionMarkers.get(marker.key);
    newestVersionMarkers.set(
      marker.key,
      newerDraftFieldMarker(current, marker, marker.index ?? Number.MAX_SAFE_INTEGER),
    );
  });
  const liveKeys = draftKeysFromMaps(maps);
  newestVersionMarkers.forEach((marker, key) => {
    if (liveKeys.has(key) && marker.version) versions.set(key, marker.version);
  });
  [...versions.keys()].forEach((key) => {
    if (!liveKeys.has(key)) versions.delete(key);
  });
  return draftSnapshotFromMaps(
    maps,
    versions,
    draftFieldMarkerManifestForMaps(manifest, maps),
  );
}

function writeDraftFieldMarkers(
  storageKey,
  storage,
  draftMaps,
  draftVersions,
  dirtyFields,
  {
    updatedAt = 0,
    logoutFenceAt = 0,
    baseUpdatedAt = 0,
    deletedVersions = null,
  } = {},
) {
  if (!storageKey) return { wrote: false, markers: new Map(), rejected: new Map() };
  let wrote = false;
  const markers = new Map();
  const rejected = new Map();
  Object.entries(draftFieldDefinitions).forEach(([kind, definition]) => {
    const field = draftMaps?.[kind] || new Map();
    dirtyFields?.[kind]?.forEach?.((key) => {
      const normalizedKey = normalizeDraftKey(key);
      const markerStorageKey = draftFieldMarkerStorageKey(storageKey, kind, normalizedKey);
      if (!markerStorageKey) return;
      const deleted = !field.has(normalizedKey);
      const marker = {
        id: nextDraftVersion(),
        storageKey,
        kind,
        key: normalizedKey,
        deleted,
        ...(deleted ? {} : { value: definition.normalize(field.get(normalizedKey)) }),
        version: (deleted
          && (deletedVersions?.get?.(`${kind}:${normalizedKey}`)
            || deletedVersions?.get?.(normalizedKey)))
          || draftVersions?.get?.(normalizedKey)
          || nextDraftVersion(),
        updatedAt: normalizedUpdatedAt(updatedAt),
        baseUpdatedAt: normalizedUpdatedAt(baseUpdatedAt),
        logoutFenceAt: normalizedUpdatedAt(logoutFenceAt),
      };
      try {
        // Keep the mutation timestamp separate from the physical write. A
        // suspended renderer must not become newer merely because another
        // context advanced the state while it was waiting to write.
        const currentState = readDraftSnapshot(
          stateStorageKeyForDraftStorageKey(storageKey),
          storage,
        );
        const committedMarker = {
          ...marker,
          committedAt: marker.updatedAt,
        };
        const currentMarker = readCurrentDraftFieldMarker(
          markerStorageKey,
          storage,
          kind,
          normalizedKey,
        );
        const currentWins = currentMarker
          && newerDraftFieldMarker(currentMarker, committedMarker, Number.MAX_SAFE_INTEGER).id
            !== committedMarker.id;
        const clearFieldKeys = new Set(
          Array.isArray(currentState?.clearedFieldKeys)
            ? currentState.clearedFieldKeys
            : [],
        );
        const clearAt = normalizedUpdatedAt(currentState?.updatedAt);
        const lateValueAfterClear = currentState?.cleared === true
          && clearFieldKeys.has(`${kind}:${normalizedKey}`)
          && !deleted
          && normalizedUpdatedAt(baseUpdatedAt) < clearAt;
        if (currentWins || lateValueAfterClear) {
          rejected.set(`${kind}:${normalizedKey}`, {
            marker: currentMarker || {
              ...committedMarker,
              deleted: true,
              value: undefined,
            },
            reason: lateValueAfterClear ? 'late-clear' : 'newer-marker',
          });
          return;
        }
        const historyStorageKey = draftFieldMarkerHistoryStorageKey(
          storageKey,
          kind,
          normalizedKey,
          committedMarker.id,
        );
        const historyWritten = historyStorageKey
          && writeStorageTargets(historyStorageKey, JSON.stringify(committedMarker), storage);
        const stableWritten = !currentWins
          && writeStorageTargets(markerStorageKey, JSON.stringify(committedMarker), storage);
        const winner = currentWins ? currentMarker : committedMarker;
        if (historyWritten || stableWritten) {
          wrote = true;
          markers.set(`${kind}:${normalizedKey}`, winner);
        }
      } catch {
        // The whole snapshot write below remains the compatibility fallback.
      }
    });
  });
  compactDraftFieldMarkers(storageKey, storage);
  return { wrote, markers, rejected };
}

const COMPOSER_DRAFT_FIELD_HISTORY_LIMIT = 12;

function compactDraftFieldMarkers(
  storageKey,
  storage,
  maxHistoryPerField = COMPOSER_DRAFT_FIELD_HISTORY_LIMIT,
) {
  const prefix = draftFieldMarkerStoragePrefixForDraftStorageKey(storageKey);
  if (!prefix || maxHistoryPerField < 1) return;
  storageTargets(storage).forEach((target) => {
    const area = getStorage(target);
    if (!area) return;
    const grouped = new Map();
    try {
      for (let index = 0; index < area.length; index += 1) {
        const markerStorageKey = area.key(index);
        if (typeof markerStorageKey !== 'string' || !markerStorageKey.startsWith(prefix)) continue;
        const marker = readDraftSnapshot(markerStorageKey, target);
        const kind = typeof marker?.kind === 'string' && draftFieldDefinitions[marker.kind]
          ? marker.kind
          : '';
        const key = normalizeDraftKey(marker?.key);
        if (marker?.storageKey !== storageKey || !kind || !key) continue;
        const stableKey = draftFieldMarkerStorageKey(storageKey, kind, key);
        if (markerStorageKey === stableKey) continue;
        const records = grouped.get(`${kind}:${key}`) || [];
        records.push({ markerStorageKey, marker, index });
        grouped.set(`${kind}:${key}`, records);
      }
    } catch {
      return;
    }
    const keysToRemove = [];
    grouped.forEach((records) => {
      if (records.length <= maxHistoryPerField) return;
      records.sort((left, right) => {
        const winner = newerDraftFieldMarker(
          left.marker,
          right.marker,
          right.index,
        );
        return winner.id === right.marker.id ? 1 : -1;
      });
      records.slice(maxHistoryPerField).forEach((record) => keysToRemove.push(record.markerStorageKey));
    });
    keysToRemove.forEach((markerStorageKey) => removeStorageValue(markerStorageKey, target));
  });
}

function removeDraftFieldMarkers(storageKey, storage) {
  const prefix = draftFieldMarkerStoragePrefixForDraftStorageKey(storageKey);
  if (!prefix) return;
  storageTargets(storage).forEach((target) => {
    const area = getStorage(target);
    if (!area) return;
    const keys = [];
    try {
      for (let index = 0; index < area.length; index += 1) {
        const key = area.key(index);
        if (typeof key === 'string' && key.startsWith(prefix)) keys.push(key);
      }
    } catch {
      return;
    }
    keys.forEach((key) => removeStorageValue(key, target));
  });
}

function readDraftVersionLineages(storageKey, storage, logoutFenceUpdatedAt = 0) {
  const prefix = draftVersionLineageStoragePrefixForDraftStorageKey(storageKey);
  const currentLogoutFenceAt = normalizedUpdatedAt(logoutFenceUpdatedAt);
  const lineages = new Map();
  let updatedAt = 0;
  let logoutFenceAt = currentLogoutFenceAt;
  if (!prefix) return { lineages, updatedAt, logoutFenceAt };
  storageTargets(storage).forEach((target) => {
    const area = getStorage(target);
    if (!area) return;
    try {
      for (let index = 0; index < area.length; index += 1) {
        const markerStorageKey = area.key(index);
        if (typeof markerStorageKey !== 'string' || !markerStorageKey.startsWith(prefix)) continue;
        const marker = readDraftSnapshot(markerStorageKey, target);
        const key = normalizeDraftKey(marker?.key);
        const version = normalizeDraftVersion(marker?.version);
        const markerFenceAt = normalizedUpdatedAt(marker?.logoutFenceAt);
        if (marker?.storageKey !== storageKey || !key || !version || markerFenceAt < currentLogoutFenceAt) {
          continue;
        }
        const byVersion = lineages.get(key) || new Map();
        const parents = byVersion.get(version) || new Set();
        (Array.isArray(marker?.parents) ? marker.parents : []).forEach((parent) => {
          const normalizedParent = normalizeDraftVersion(parent);
          if (normalizedParent && normalizedParent !== version) parents.add(normalizedParent);
        });
        byVersion.set(version, parents);
        lineages.set(key, byVersion);
        updatedAt = Math.max(updatedAt, normalizedUpdatedAt(marker?.updatedAt));
        logoutFenceAt = Math.max(logoutFenceAt, markerFenceAt);
      }
    } catch {
      // A missing storage target must not erase ancestry found in another one.
    }
  });
  return { lineages, updatedAt, logoutFenceAt };
}

function expandDraftClearVersions(clearVersions, lineages) {
  const expanded = mergeDraftClearVersions(clearVersions);
  expanded.forEach((versions, key) => {
    const byVersion = lineages?.get?.(key);
    if (!byVersion?.size) return;
    const pending = [...versions];
    while (pending.length > 0) {
      const version = pending.pop();
      (byVersion.get(version) || []).forEach((parent) => {
        if (versions.has(parent)) return;
        versions.add(parent);
        pending.push(parent);
      });
    }
  });
  return expanded;
}

function writeDraftVersionLineageMarkers(
  storageKey,
  storage,
  draftVersions,
  dirtyFields,
  parentVersionsByKey,
  { updatedAt = 0, logoutFenceAt = 0, lineages = null } = {},
) {
  if (!storageKey) return false;
  const dirtyKeys = new Set(Object.values(dirtyFields || {}).flatMap((keys) => [...(keys || [])]));
  let wrote = false;
  dirtyKeys.forEach((candidateKey) => {
    const key = normalizeDraftKey(candidateKey);
    const version = draftVersions?.get?.(key);
    const markerStorageKey = draftVersionLineageStorageKey(storageKey, key, version);
    if (!markerStorageKey) return;
    const parentSeeds = new Set([
      ...(parentVersionsByKey?.get?.(key) || []),
      ...(lineages?.get?.(key)?.get?.(version) || []),
    ]);
    const parents = [...(
      expandDraftClearVersions(
        new Map([[key, parentSeeds]]),
        lineages,
      ).get(key) || parentSeeds
    )]
      .map(normalizeDraftVersion)
      .filter((parent) => parent && parent !== version);
    const marker = {
      id: nextDraftVersion(),
      storageKey,
      key,
      version,
      parents: [...new Set(parents)],
      updatedAt: normalizedUpdatedAt(updatedAt),
      logoutFenceAt: normalizedUpdatedAt(logoutFenceAt),
    };
    try {
      if (writeStorageTargets(markerStorageKey, JSON.stringify(marker), storage)) wrote = true;
    } catch {
      // Field records remain independently useful without a lineage record.
    }
  });
  return wrote;
}

// Lineage records are only needed long enough to carry ancestry into the
// current version (or into an immutable send marker). Keep a small recent
// frontier rather than deleting everything not known by this renderer: an
// unlocked sibling may have committed a version between our read and this
// compaction pass.
const COMPOSER_DRAFT_LINEAGE_HISTORY_LIMIT = 32;

function compactDraftVersionLineageMarkers(
  storageKey,
  storage,
  draftVersions,
  maxPerKey = COMPOSER_DRAFT_LINEAGE_HISTORY_LIMIT,
) {
  const prefix = draftVersionLineageStoragePrefixForDraftStorageKey(storageKey);
  if (!prefix) return;
  const keep = new Map(
    [...(draftVersions || [])]
      .map(([key, version]) => [normalizeDraftKey(key), normalizeDraftVersion(version)])
      .filter(([key, version]) => key && version),
  );
  storageTargets(storage).forEach((target) => {
    const area = getStorage(target);
    if (!area) return;
    const grouped = new Map();
    try {
      for (let index = 0; index < area.length; index += 1) {
        const markerStorageKey = area.key(index);
        if (typeof markerStorageKey !== 'string' || !markerStorageKey.startsWith(prefix)) continue;
        const marker = readDraftSnapshot(markerStorageKey, target);
        const key = normalizeDraftKey(marker?.key);
        const version = normalizeDraftVersion(marker?.version);
        if (!key || !version || marker?.storageKey !== storageKey) continue;
        const records = grouped.get(key) || [];
        records.push({ markerStorageKey, marker, index });
        grouped.set(key, records);
      }
    } catch {
      return;
    }
    const keysToRemove = [];
    grouped.forEach((records, key) => {
      if (records.length <= maxPerKey) return;
      const byVersion = new Map(records.map((record) => [record.marker.version, record]));
      const retained = new Set();
      const currentVersion = keep.get(key);
      if (currentVersion && byVersion.has(currentVersion)) retained.add(currentVersion);
      const ordered = [...records].sort((left, right) => {
        const winner = newerDraftFieldMarker(left.marker, right.marker, right.index);
        return winner.id === right.marker.id ? 1 : -1;
      });
      ordered.forEach((record) => {
        if (retained.size >= maxPerKey) return;
        retained.add(record.marker.version);
      });
      // Keep ancestry referenced by the retained frontier when room remains;
      // current markers carry the transitive parent list, so old records can
      // still be safely reclaimed once the bounded set is full.
      const pending = [...retained];
      while (pending.length > 0 && retained.size < maxPerKey) {
        const version = pending.pop();
        const marker = byVersion.get(version)?.marker;
        (Array.isArray(marker?.parents) ? marker.parents : []).forEach((parent) => {
          const normalizedParent = normalizeDraftVersion(parent);
          if (normalizedParent && byVersion.has(normalizedParent) && !retained.has(normalizedParent)) {
            retained.add(normalizedParent);
            pending.push(normalizedParent);
          }
        });
      }
      records.forEach((record) => {
        if (!retained.has(record.marker.version)) keysToRemove.push(record.markerStorageKey);
      });
    });
    keysToRemove.forEach((markerStorageKey) => removeStorageValue(markerStorageKey, target));
  });
}

function removeDraftVersionLineageMarkers(storageKey, storage) {
  const prefix = draftVersionLineageStoragePrefixForDraftStorageKey(storageKey);
  if (!prefix) return;
  storageTargets(storage).forEach((target) => {
    const area = getStorage(target);
    if (!area) return;
    const keys = [];
    try {
      for (let index = 0; index < area.length; index += 1) {
        const key = area.key(index);
        if (typeof key === 'string' && key.startsWith(prefix)) keys.push(key);
      }
    } catch {
      return;
    }
    keys.forEach((key) => removeStorageValue(key, target));
  });
}

function createDraftDirtyFields() {
  return Object.fromEntries(Object.keys(draftFieldDefinitions).map((kind) => [kind, new Set()]));
}

function cloneDraftDirtyFields(source = {}) {
  return Object.fromEntries(Object.keys(draftFieldDefinitions).map((kind) => [
    kind,
    new Set(source[kind] || []),
  ]));
}

function mergeDraftDirtyFields(...sources) {
  const merged = createDraftDirtyFields();
  sources.forEach((source) => {
    Object.keys(draftFieldDefinitions).forEach((kind) => {
      source?.[kind]?.forEach?.((key) => {
        const normalizedKey = normalizeDraftKey(key);
        if (normalizedKey) merged[kind].add(normalizedKey);
      });
    });
  });
  return merged;
}

function markDraftFieldDirty(dirtyFields, kind, key) {
  const normalizedKey = normalizeDraftKey(key);
  if (normalizedKey && dirtyFields?.[kind]) dirtyFields[kind].add(normalizedKey);
}

function clearDraftDirtyFields(dirtyFields) {
  Object.values(dirtyFields || {}).forEach((keys) => keys?.clear?.());
}

function clearDraftDirtyFieldsForKey(dirtyFields, key) {
  const normalizedKey = normalizeDraftKey(key);
  if (!normalizedKey) return;
  Object.values(dirtyFields || {}).forEach((keys) => keys?.delete?.(normalizedKey));
}

function hasDirtyDraftFields(dirtyFields) {
  return Object.values(dirtyFields || {}).some((keys) => keys?.size > 0);
}

function draftDirtyFieldsForRecord(dirtyFields) {
  return Object.fromEntries(Object.keys(draftFieldDefinitions).map((kind) => [
    kind,
    [...(dirtyFields?.[kind] || [])],
  ]));
}

function draftDirtyFieldsFromRecord(value) {
  const dirtyFields = createDraftDirtyFields();
  Object.keys(draftFieldDefinitions).forEach((kind) => {
    if (!Array.isArray(value?.[kind])) return;
    value[kind].forEach((key) => markDraftFieldDirty(dirtyFields, kind, key));
  });
  return dirtyFields;
}

function draftDirtyFieldsFromSnapshotDifference(snapshot = {}, baselineSnapshot = {}) {
  const dirtyFields = createDraftDirtyFields();
  const maps = draftMapsFromSnapshot(snapshot);
  const baselineMaps = draftMapsFromSnapshot(baselineSnapshot);
  Object.keys(draftFieldDefinitions).forEach((kind) => {
    const keys = new Set([
      ...(maps[kind]?.keys?.() || []),
      ...(baselineMaps[kind]?.keys?.() || []),
    ]);
    keys.forEach((key) => {
      if (draftFieldChangedSinceBaseline(maps, baselineMaps, null, kind, key)) {
        markDraftFieldDirty(dirtyFields, kind, key);
      }
    });
  });
  return dirtyFields;
}

function draftClearVersionsFromState(state = {}) {
  const clearVersions = new Map();
  const entries = Array.isArray(state?.[COMPOSER_DRAFT_CLEARED_VERSION_MAP_NAME])
    ? state[COMPOSER_DRAFT_CLEARED_VERSION_MAP_NAME]
    : [];
  entries.forEach((entry) => {
    if (!Array.isArray(entry) || entry.length !== 2) return;
    const key = normalizeDraftKey(entry[0]);
    if (!key) return;
    const candidates = Array.isArray(entry[1]) ? entry[1] : [entry[1]];
    const versions = new Set(candidates.map(normalizeDraftVersion).filter(Boolean));
    if (versions.size > 0) clearVersions.set(key, versions);
  });
  return clearVersions;
}

function draftClearFieldVersionsFromState(state = {}) {
  const versions = new Map();
  const entries = Array.isArray(state?.[COMPOSER_DRAFT_CLEARED_FIELD_VERSION_MAP_NAME])
    ? state[COMPOSER_DRAFT_CLEARED_FIELD_VERSION_MAP_NAME]
    : [];
  entries.forEach((entry) => {
    if (!Array.isArray(entry) || entry.length !== 2) return;
    const fieldKey = String(entry[0] || '').trim();
    const version = normalizeDraftVersion(entry[1]);
    if (fieldKey && version) versions.set(fieldKey, version);
  });
  return versions;
}

function mergeDraftClearVersions(...sources) {
  const merged = new Map();
  sources.forEach((source) => {
    source?.forEach?.((versions, key) => {
      const normalizedKey = normalizeDraftKey(key);
      if (!normalizedKey) return;
      const target = merged.get(normalizedKey) || new Set();
      versions?.forEach?.((version) => {
        const normalizedVersion = normalizeDraftVersion(version);
        if (normalizedVersion) target.add(normalizedVersion);
      });
      if (target.size > 0) merged.set(normalizedKey, target);
    });
  });
  return merged;
}

function isDraftVersionCleared(clearVersions, key, version) {
  const normalizedKey = normalizeDraftKey(key);
  const normalizedVersion = normalizeDraftVersion(version);
  return Boolean(
    normalizedKey
    && normalizedVersion
    && clearVersions?.get?.(normalizedKey)?.has?.(normalizedVersion),
  );
}

function filterDraftSnapshotByClearedVersions(snapshot = {}, clearVersions = new Map()) {
  if (!clearVersions?.size) return snapshot;
  const maps = draftMapsFromSnapshot(snapshot);
  const versions = draftVersionsFromSnapshot(snapshot, maps);
  let changed = false;
  clearVersions.forEach((clearedVersions, key) => {
    if (!clearedVersions?.has?.(versions.get(key))) return;
    Object.values(maps).forEach((map) => map.delete(key));
    versions.delete(key);
    changed = true;
  });
  return changed
    ? draftSnapshotFromMaps(
      maps,
      versions,
      draftFieldMarkerManifestForMaps(draftFieldMarkerManifestFromSnapshot(snapshot), maps),
    )
    : snapshot;
}

function draftDataWithoutClearedVersions(snapshot = {}, dirtyFields, clearVersions = new Map()) {
  const maps = draftMapsFromSnapshot(snapshot);
  const versions = draftVersionsFromSnapshot(snapshot, maps);
  const nextDirtyFields = cloneDraftDirtyFields(dirtyFields);
  clearVersions.forEach((clearedVersions, key) => {
    if (!clearedVersions?.has?.(versions.get(key))) return;
    Object.values(maps).forEach((map) => map.delete(key));
    versions.delete(key);
    clearDraftDirtyFieldsForKey(nextDirtyFields, key);
  });
  return { maps, versions, dirtyFields: nextDirtyFields };
}

function mergeFastPathWithDurableSnapshot(durableRecord, fastRecord) {
  const clearVersions = durableRecord.clearedDraftVersions || new Map();
  const attachmentIntents = durableRecord.attachmentIntents || new Map();
  const pendingAttachmentMutations = (fastRecord?.pendingAttachmentMutations || []).filter(
    (mutation) => ![
      mutation?.baseVersion,
      mutation?.version,
      ...(Array.isArray(mutation?.precedingVersions) ? mutation.precedingVersions : []),
    ].some((version) => isDraftVersionCleared(clearVersions, mutation?.key, version)),
  );
  if (
    !fastRecord
    || (
      !hasDirtyDraftFields(fastRecord.dirtyFields)
      && pendingAttachmentMutations.length === 0
    )
  ) return null;
  const durableData = draftDataWithoutClearedVersions(
    durableRecord.cleared ? {} : durableRecord.snapshot,
    createDraftDirtyFields(),
    clearVersions,
  );
  const fastData = draftDataWithoutClearedVersions(
    fastRecord.snapshot,
    fastRecord.dirtyFields,
    clearVersions,
  );
  const fastBaselineData = draftDataWithoutClearedVersions(
    fastRecord.baselineSnapshot,
    createDraftDirtyFields(),
    clearVersions,
  );
  if (
    !hasDirtyDraftFields(fastData.dirtyFields)
    && pendingAttachmentMutations.length === 0
  ) return null;

  // A re-add that has started but has not yet received the shared write lock
  // is local state, not a durable restore. Its pending supersede marker lets a
  // queued X stand down, while this tab-scoped overlay keeps the intended file
  // visible across a SkillHub document handoff until it commits or is canceled.
  const pendingRestoreRemovalIds = pendingAttachmentRestoreRemovalIds(
    pendingAttachmentMutations,
    durableRecord.attachmentIntentMetadata,
  );
  const visibleAttachmentIntents = attachmentIntentsIgnoringRemovalIds(
    attachmentIntents,
    pendingRestoreRemovalIds,
  );

  let maps = mergeDraftMaps(
    fastData.maps,
    fastBaselineData.maps,
    durableData.maps,
    fastData.dirtyFields,
  );
  maps = filterDraftMapsByAttachmentIntents(maps, visibleAttachmentIntents);
  const versions = mergeDraftVersions(
    fastData.maps,
    fastBaselineData.maps,
    maps,
    fastData.versions,
    fastBaselineData.versions,
    durableData.versions,
    fastData.dirtyFields,
  );
  applyFastPathAttachmentMutations(
    maps,
    versions,
    fastData.dirtyFields,
    pendingAttachmentMutations,
    visibleAttachmentIntents,
    durableRecord.attachmentIntentMetadata,
  );
  maps = filterDraftMapsByAttachmentIntents(maps, visibleAttachmentIntents);
  const liveKeys = draftKeysFromMaps(maps);
  [...versions.keys()].forEach((key) => {
    if (!liveKeys.has(key)) versions.delete(key);
  });
  if (!hasDirtyDraftFields(fastData.dirtyFields)) return null;
  return {
    snapshot: draftSnapshotFromMaps(maps, versions),
    maps,
    versions,
    dirtyFields: fastData.dirtyFields,
  };
}

function draftSnapshotFromMaps(draftMaps = {}, draftVersions = null, fieldMarkerManifest = null) {
  const snapshot = Object.fromEntries(Object.entries(draftFieldDefinitions).map(([kind, definition]) => [
    definition.mapName,
    [...(draftMaps[kind] || [])],
  ]));
  if (draftVersions) {
    const liveKeys = draftKeysFromMaps(draftMaps);
    snapshot[COMPOSER_DRAFT_VERSION_MAP_NAME] = [...draftVersions]
      .filter(([key]) => liveKeys.has(key));
  }
  if (fieldMarkerManifest) {
    snapshot[COMPOSER_DRAFT_FIELD_MARKER_MAP_NAME] = [...fieldMarkerManifest]
      .filter(([fieldKey, markerId]) => typeof fieldKey === 'string' && fieldKey
        && typeof markerId === 'string' && markerId);
  }
  return snapshot;
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

function draftVersionsEqual(leftVersions = new Map(), rightVersions = new Map()) {
  const keys = new Set([...leftVersions.keys(), ...rightVersions.keys()]);
  return [...keys].every((key) => leftVersions.get(key) === rightVersions.get(key));
}

function draftKeyChanged(localMaps, baselineMaps, key) {
  return Object.keys(draftFieldDefinitions).some((kind) => {
    const local = localMaps[kind] || new Map();
    const baseline = baselineMaps[kind] || new Map();
    return local.has(key) !== baseline.has(key)
      || (local.has(key) && !draftValueEqual(local.get(key), baseline.get(key)));
  });
}

function draftFieldChangedSinceBaseline(
  localMaps,
  baselineMaps,
  localDirtyFields,
  kind,
  key,
) {
  const local = localMaps[kind] || new Map();
  const baseline = baselineMaps[kind] || new Map();
  return local.has(key) !== baseline.has(key)
    || (local.has(key) && !draftValueEqual(local.get(key), baseline.get(key)))
    || Boolean(localDirtyFields?.[kind]?.has(key));
}

function draftKeyChangedSinceBaseline(localMaps, baselineMaps, localDirtyFields, key) {
  return Object.keys(draftFieldDefinitions).some((kind) => (
    draftFieldChangedSinceBaseline(localMaps, baselineMaps, localDirtyFields, kind, key)
  ));
}

// Apply local changes made since the last accepted snapshot on top of a
// newer snapshot from another browsing context. Untouched keys take the
// remote value; changed keys (including local deletions and a fresh logical
// draft with an equal field value) take the local value.
function mergeDraftMaps(
  localMaps,
  baselineMaps,
  latestMaps,
  localDirtyFields = null,
) {
  return Object.fromEntries(Object.keys(draftFieldDefinitions).map((kind) => {
    const local = localMaps[kind] || new Map();
    const baseline = baselineMaps[kind] || new Map();
    const merged = new Map(latestMaps[kind] || []);
    const changedKeys = new Set([...local.keys(), ...baseline.keys()]);

    changedKeys.forEach((key) => {
      const localChanged = draftFieldChangedSinceBaseline(
        localMaps,
        baselineMaps,
        localDirtyFields,
        kind,
        key,
      );
      if (!localChanged) return;
      if (local.has(key)) merged.set(key, local.get(key));
      else merged.delete(key);
    });

    return [kind, merged];
  }));
}

function mergeDraftVersions(
  localMaps,
  baselineMaps,
  mergedMaps,
  localVersions,
  baselineVersions,
  latestVersions,
  localDirtyFields = null,
) {
  const merged = new Map(latestVersions);
  const keys = new Set([
    ...draftKeysFromMaps(localMaps),
    ...draftKeysFromMaps(baselineMaps),
    ...draftKeysFromMaps(mergedMaps),
    ...localVersions.keys(),
    ...baselineVersions.keys(),
    ...latestVersions.keys(),
  ]);
  keys.forEach((key) => {
    const locallyChanged = draftKeyChangedSinceBaseline(
      localMaps,
      baselineMaps,
      localDirtyFields,
      key,
    );
    if (!locallyChanged) return;
    if (draftKeysFromMaps(mergedMaps).has(key)) {
      merged.set(key, localVersions.get(key) || nextDraftVersion());
    } else {
      merged.delete(key);
    }
  });
  const liveKeys = draftKeysFromMaps(mergedMaps);
  [...merged.keys()].forEach((key) => {
    if (!liveKeys.has(key)) merged.delete(key);
  });
  return merged;
}

function applyFastPathAttachmentMutations(
  maps,
  versions,
  dirtyFields,
  mutations = [],
  attachmentIntents = new Map(),
  attachmentIntentMetadata = null,
) {
  mutations.forEach((mutation) => {
    const normalizedKey = normalizeDraftKey(mutation?.key);
    if (!normalizedKey) return;
    if (
      mutation?.removalIntentId
      && (
        attachmentIntentMetadata?.restoredRemovalIds?.has(mutation.removalIntentId)
        || attachmentIntentMetadata?.supersededRemovalIds?.has(mutation.removalIntentId)
      )
    ) return;
    const expectedSessionId = String(mutation.expectedPhoneUploadSessionId || '');
    const currentPhoneUploadSession = normalizePhoneUploadSession(
      maps.phoneUpload.get(normalizedKey),
    );
    if (expectedSessionId && currentPhoneUploadSession?.session_id !== expectedSessionId) return;

    const currentAttachments = draftFieldDefinitions.attachment.read(
      maps.attachment.get(normalizedKey),
    );
    const removalIntentIsActive = mutation?.type === 'remove'
      && mutation?.removalIntentId
      && attachmentIntentMetadata?.removalIds?.has(mutation.removalIntentId);
    if (
      mutation.type !== 'append'
      && !attachmentMutationMatchesCurrentBase(mutation, currentAttachments)
      && !removalIntentIsActive
    ) return;
    let nextAttachments = [...currentAttachments];
    if (mutation.type === 'replace') {
      nextAttachments = Array.isArray(mutation.attachments) ? [...mutation.attachments] : [];
    } else if (mutation.type === 'append') {
      const knownKeys = new Set(nextAttachments.map(attachmentDraftKey).filter(Boolean));
      const removedFileKeys = phoneUploadRemovedFileKeys(currentPhoneUploadSession);
      (Array.isArray(mutation.attachments) ? mutation.attachments : []).forEach((attachment) => {
        const fileKey = attachmentDraftKey(attachment);
        if (expectedSessionId && fileKey && removedFileKeys.has(fileKey)) return;
        const duplicate = fileKey
          ? knownKeys.has(fileKey)
          : nextAttachments.some((candidate) => draftValueEqual(candidate, attachment));
        if (duplicate) return;
        nextAttachments.push(attachment);
        if (fileKey) knownKeys.add(fileKey);
      });
    } else if (mutation.type === 'remove') {
      let removedOne = false;
      nextAttachments = nextAttachments.filter((attachment) => {
        const matches = attachmentMutationTargetsCurrentAttachment(mutation, [attachment]);
        if (!matches || removedOne) return true;
        removedOne = true;
        return false;
      });
    } else {
      return;
    }

    nextAttachments = filterAttachmentsByIntent(
      nextAttachments,
      attachmentIntents,
      normalizedKey,
    );
    const normalizedAttachments = draftFieldDefinitions.attachment.normalize(nextAttachments);
    const normalizedNextAttachments = draftFieldDefinitions.attachment.read(normalizedAttachments);
    let nextPhoneUploadSession = currentPhoneUploadSession;
    const removedFileKeys = normalizedPhoneUploadRemovedFileKeys(
      mutation.removedPhoneUploadFileKeys,
    );
    if (removedFileKeys.length > 0 && expectedSessionId && currentPhoneUploadSession) {
      const rememberedRemovedKeys = phoneUploadRemovedFileKeys(currentPhoneUploadSession);
      removedFileKeys.forEach((fileKey) => rememberedRemovedKeys.add(fileKey));
      nextPhoneUploadSession = normalizePhoneUploadSession({
        ...currentPhoneUploadSession,
        [PHONE_UPLOAD_REMOVED_FILE_KEYS]: [...rememberedRemovedKeys],
      });
    }
    const attachmentChanged = !draftValueEqual(currentAttachments, normalizedNextAttachments);
    const phoneUploadSessionChanged = !draftValueEqual(
      currentPhoneUploadSession,
      nextPhoneUploadSession,
    );
    if (!attachmentChanged && !phoneUploadSessionChanged) return;

    if (normalizedAttachments === null) maps.attachment.delete(normalizedKey);
    else maps.attachment.set(normalizedKey, normalizedAttachments);
    if (nextPhoneUploadSession === null) maps.phoneUpload.delete(normalizedKey);
    else maps.phoneUpload.set(normalizedKey, nextPhoneUploadSession);
    if (attachmentChanged) markDraftFieldDirty(dirtyFields, 'attachment', normalizedKey);
    if (phoneUploadSessionChanged) markDraftFieldDirty(dirtyFields, 'phoneUpload', normalizedKey);
    if (draftKeysFromMaps(maps).has(normalizedKey)) {
      versions.set(normalizedKey, normalizeDraftVersion(mutation.version) || nextDraftVersion());
    } else {
      versions.delete(normalizedKey);
    }
  });
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

function readAttachmentDraftIntents(
  storageKey,
  storage,
  logoutFenceUpdatedAt = 0,
  { ignoredRemovalIds = null } = {},
) {
  const prefix = attachmentIntentStoragePrefixForDraftStorageKey(storageKey);
  const currentLogoutFenceAt = normalizedUpdatedAt(logoutFenceUpdatedAt);
  const removals = new Map();
  const restoredRemovalIds = new Set();
  const supersedes = new Map();
  const abandonedSupersedeIds = new Set();
  const ignored = new Set(
    ignoredRemovalIds && typeof ignoredRemovalIds[Symbol.iterator] === 'function'
      ? ignoredRemovalIds
      : [],
  );
  let updatedAt = 0;
  let logoutFenceAt = currentLogoutFenceAt;
  if (!prefix) {
    return {
      intents: new Map(),
      removalIds: new Set(),
      restoredRemovalIds,
      activeSupersedeIds: new Set(),
      supersededRemovalIds: new Set(),
      updatedAt,
      logoutFenceAt,
    };
  }

  storageTargets(storage).forEach((target) => {
    const area = getStorage(target);
    if (!area) return;
    try {
      for (let index = 0; index < area.length; index += 1) {
        const markerStorageKey = area.key(index);
        if (typeof markerStorageKey !== 'string' || !markerStorageKey.startsWith(prefix)) continue;
        const marker = readDraftSnapshot(markerStorageKey, target);
        const key = normalizeDraftKey(marker?.key);
        const identity = typeof marker?.attachmentIdentity === 'string'
          ? marker.attachmentIdentity
          : attachmentDraftIdentity(marker?.attachment);
        const action = ['remove', 'restore', 'supersede', 'abandon'].includes(marker?.action)
          ? marker.action
          : '';
        const markerFenceAt = normalizedUpdatedAt(marker?.logoutFenceAt);
        if (
          marker?.storageKey !== storageKey
          || !key
          || !identity
          || !action
          || markerFenceAt < currentLogoutFenceAt
        ) continue;
        const id = String(marker?.id || markerStorageKey);
        if (action === 'remove') {
          const hasAttachmentSnapshot = Object.prototype.hasOwnProperty.call(marker, 'attachment');
          removals.set(id, {
            id,
            key,
            attachmentIdentity: identity,
            ...(hasAttachmentSnapshot ? { attachment: marker.attachment } : {}),
          });
        } else {
          if (action === 'restore') {
            normalizedPhoneUploadRemovedFileKeys(marker?.restores)
              .forEach((removeId) => restoredRemovalIds.add(removeId));
          } else if (action === 'supersede') {
            const supersededIds = normalizedPhoneUploadRemovedFileKeys(marker?.supersedes);
            if (supersededIds.length > 0) supersedes.set(id, supersededIds);
          } else if (action === 'abandon') {
            normalizedPhoneUploadRemovedFileKeys(marker?.abandons)
              .forEach((supersedeId) => abandonedSupersedeIds.add(supersedeId));
          }
        }
        updatedAt = Math.max(updatedAt, normalizedUpdatedAt(marker?.updatedAt));
        logoutFenceAt = Math.max(logoutFenceAt, markerFenceAt);
      }
    } catch {
      // Keep the intents recovered from other storage targets.
    }
  });
  const intents = new Map();
  removals.forEach((marker) => {
    if (restoredRemovalIds.has(marker.id) || ignored.has(marker.id)) return;
    const byIdentity = intents.get(marker.key) || new Map();
    const current = byIdentity.get(marker.attachmentIdentity) || {
      removeIds: new Set(),
      removals: new Map(),
    };
    current.removeIds.add(marker.id);
    current.removals.set(marker.id, marker);
    byIdentity.set(marker.attachmentIdentity, current);
    intents.set(marker.key, byIdentity);
  });
  const activeSupersedeIds = new Set();
  const supersededRemovalIds = new Set();
  supersedes.forEach((removalIds, id) => {
    if (abandonedSupersedeIds.has(id)) return;
    let hasUnrestoredRemoval = false;
    removalIds.forEach((removeId) => {
      if (!restoredRemovalIds.has(removeId)) hasUnrestoredRemoval = true;
      supersededRemovalIds.add(removeId);
    });
    if (hasUnrestoredRemoval) activeSupersedeIds.add(id);
  });
  return {
    intents,
    removalIds: new Set(removals.keys()),
    restoredRemovalIds,
    activeSupersedeIds,
    supersededRemovalIds,
    updatedAt,
    logoutFenceAt,
  };
}

function writeAttachmentDraftIntent(
  storageKey,
  storage,
  key,
  attachment,
  action,
  {
    attachmentIdentity: providedAttachmentIdentity = '',
    restores = [],
    supersedes = [],
    abandons = [],
    updatedAt = 0,
    logoutFenceAt = 0,
  } = {},
) {
  const normalizedKey = normalizeDraftKey(key);
  const attachmentIdentity = providedAttachmentIdentity || attachmentDraftIdentity(attachment);
  const normalizedRestores = normalizedPhoneUploadRemovedFileKeys(restores);
  const normalizedSupersedes = normalizedPhoneUploadRemovedFileKeys(supersedes);
  const normalizedAbandons = normalizedPhoneUploadRemovedFileKeys(abandons);
  if (
    !storageKey
    || !normalizedKey
    || !attachmentIdentity
    || !['remove', 'restore', 'supersede', 'abandon'].includes(action)
  ) {
    return null;
  }
  if (action === 'restore' && normalizedRestores.length === 0) return null;
  if (action === 'supersede' && normalizedSupersedes.length === 0) return null;
  if (action === 'abandon' && normalizedAbandons.length === 0) return null;
  const existing = readAttachmentDraftIntents(storageKey, storage, logoutFenceAt);
  const intentUpdatedAt = nextDraftUpdatedAt(updatedAt, existing.updatedAt);
  const id = nextDraftVersion();
  const markerStorageKey = attachmentIntentStorageKey(storageKey, normalizedKey, id);
  if (!markerStorageKey) return null;
  const marker = {
    id,
    storageKey,
    key: normalizedKey,
    attachmentIdentity,
    action,
    ...(action === 'remove' && attachment !== undefined ? { attachment } : {}),
    ...(action === 'restore' ? { restores: normalizedRestores } : {}),
    ...(action === 'supersede' ? { supersedes: normalizedSupersedes } : {}),
    ...(action === 'abandon' ? { abandons: normalizedAbandons } : {}),
    updatedAt: intentUpdatedAt,
    logoutFenceAt: normalizedUpdatedAt(logoutFenceAt),
  };
  if (!writeStorageTargets(markerStorageKey, JSON.stringify(marker), storage)) return null;
  compactAttachmentDraftIntents(storageKey, storage);
  return marker;
}

const COMPOSER_DRAFT_ATTACHMENT_INTENT_HISTORY_LIMIT = 64;

function pendingAttachmentIntentIdsFromFastPath(storageKey, storage) {
  const ids = new Set();
  // The fast path is intentionally tab-scoped. Custom storage adapters and
  // localStorage-only callers never create one, so there is no pending
  // mutation journal to protect for those targets.
  if (storage !== 'sessionStorage' || !storageKey) return ids;
  const fastPath = readDraftSnapshot(
    fastPathStorageKeyForDraftStorageKey(storageKey),
    'sessionStorage',
  );
  (Array.isArray(fastPath?.pendingAttachmentMutations)
    ? fastPath.pendingAttachmentMutations
    : []
  ).forEach((mutation) => {
    (Array.isArray(mutation?.pendingReaddIntents) ? mutation.pendingReaddIntents : [])
      .forEach((intent) => {
        const id = String(intent?.id || '').trim();
        if (id) ids.add(id);
      });
  });
  return ids;
}

// Attachment intent records are immutable by design, but remove/restore
// gestures can otherwise grow without bound. Retain active removals and
// unresolved supersede records (a queued re-add still depends on them), plus
// a bounded recent window of the remaining lifecycle records.
function compactAttachmentDraftIntents(
  storageKey,
  storage,
  maxPerKey = COMPOSER_DRAFT_ATTACHMENT_INTENT_HISTORY_LIMIT,
) {
  const prefix = attachmentIntentStoragePrefixForDraftStorageKey(storageKey);
  if (!prefix || maxPerKey < 1) return;
  const metadata = readAttachmentDraftIntents(storageKey, storage);
  const pendingIntentIds = pendingAttachmentIntentIdsFromFastPath(storageKey, storage);
  const activeSupersedeIds = metadata.activeSupersedeIds || new Set();
  const activeRemovalIds = new Set(
    [...(metadata.removalIds || [])].filter((id) => !metadata.restoredRemovalIds?.has(id)),
  );
  storageTargets(storage).forEach((target) => {
    const area = getStorage(target);
    if (!area) return;
    const grouped = new Map();
    try {
      for (let index = 0; index < area.length; index += 1) {
        const markerStorageKey = area.key(index);
        if (typeof markerStorageKey !== 'string' || !markerStorageKey.startsWith(prefix)) continue;
        const marker = readDraftSnapshot(markerStorageKey, target);
        const key = normalizeDraftKey(marker?.key);
        const id = String(marker?.id || markerStorageKey);
        if (marker?.storageKey !== storageKey || !key || !id) continue;
        const records = grouped.get(key) || [];
        records.push({ markerStorageKey, marker, id, index });
        grouped.set(key, records);
      }
    } catch {
      return;
    }
    const keysToRemove = [];
    grouped.forEach((records) => {
      const ordered = [...records].sort((left, right) => (
        normalizedUpdatedAt(right.marker.updatedAt) - normalizedUpdatedAt(left.marker.updatedAt)
          || right.index - left.index
      ));
      const retained = new Set(ordered.slice(0, maxPerKey).map((record) => record.markerStorageKey));
      records.forEach((record) => {
        if (record.marker.action === 'remove' && activeRemovalIds.has(record.id)) {
          retained.add(record.markerStorageKey);
        }
        // A queued explicit re-add has already written a supersede marker but
        // may not have acquired the shared lock yet. Keep that exact marker
        // even when a burst of unrelated lifecycle records would otherwise
        // evict it; the session fast path still references its id and needs it
        // to remain authoritative after a document handoff.
        if (pendingIntentIds.has(record.id) || activeSupersedeIds.has(record.id)) {
          retained.add(record.markerStorageKey);
        }
      });
      records.forEach((record) => {
        if (!retained.has(record.markerStorageKey)) keysToRemove.push(record.markerStorageKey);
      });
    });
    keysToRemove.forEach((markerStorageKey) => removeStorageValue(markerStorageKey, target));
  });
}

function captureAttachmentDraftRestoreTokens(
  storageKey,
  storage,
  key,
  attachments,
  { logoutFenceAt = 0 } = {},
) {
  const normalizedKey = normalizeDraftKey(key);
  if (!storageKey || !normalizedKey) return [];
  const intents = readAttachmentDraftIntents(storageKey, storage, logoutFenceAt);
  const tokens = [];
  const byIdentity = intents.intents.get(normalizedKey) || new Map();
  const candidates = Array.isArray(attachments) ? attachments : null;
  byIdentity.forEach((intent, identity) => {
    const removals = intent.removals?.size
      ? [...intent.removals.values()]
      : [...(intent.removeIds || [])].map((id) => ({ id }));
    removals.forEach((removal) => {
      if (
        candidates
        && !candidates.some((attachment) => (
          attachmentDraftIdentity(attachment) === identity
          && attachmentIntentTargetsAttachment(removal, attachment)
        ))
      ) return;
      tokens.push({
        key: normalizedKey,
        attachmentIdentity: identity,
        ...(Object.prototype.hasOwnProperty.call(removal, 'attachment')
          ? { attachment: removal.attachment }
          : {}),
        removes: [removal.id],
      });
    });
  });
  return tokens;
}

function writeCapturedAttachmentDraftRestoreIntents(
  storageKey,
  storage,
  tokens,
  { updatedAt = 0, logoutFenceAt = 0 } = {},
) {
  const written = [];
  (Array.isArray(tokens) ? tokens : []).forEach((token) => {
    const marker = writeAttachmentDraftIntent(
      storageKey,
      storage,
      token?.key,
      null,
      'restore',
      {
        attachmentIdentity: token?.attachmentIdentity,
        restores: token?.removes,
        updatedAt,
        logoutFenceAt,
      },
    );
    if (marker) written.push(marker);
  });
  return written;
}

function writePendingAttachmentReaddIntents(
  storageKey,
  storage,
  tokens,
  { updatedAt = 0, logoutFenceAt = 0 } = {},
) {
  const written = [];
  (Array.isArray(tokens) ? tokens : []).forEach((token) => {
    const marker = writeAttachmentDraftIntent(
      storageKey,
      storage,
      token?.key,
      token?.attachment,
      'supersede',
      {
        attachmentIdentity: token?.attachmentIdentity,
        supersedes: token?.removes,
        updatedAt,
        logoutFenceAt,
      },
    );
    if (marker) written.push(marker);
  });
  return written;
}

function abandonPendingAttachmentReaddIntents(
  storageKey,
  storage,
  pendingIntents,
  { updatedAt = 0, logoutFenceAt = 0 } = {},
) {
  const written = [];
  (Array.isArray(pendingIntents) ? pendingIntents : []).forEach((intent) => {
    const marker = writeAttachmentDraftIntent(
      storageKey,
      storage,
      intent?.key,
      intent?.attachment,
      'abandon',
      {
        attachmentIdentity: intent?.attachmentIdentity,
        abandons: [intent?.id],
        updatedAt,
        logoutFenceAt,
      },
    );
    if (marker) written.push(marker);
  });
  return written;
}

function filterDraftSnapshotByAttachmentIntents(snapshot = {}, intents = new Map()) {
  const maps = draftMapsFromSnapshot(snapshot);
  const filteredMaps = filterDraftMapsByAttachmentIntents(maps, intents);
  if (filteredMaps === maps) return snapshot;
  return draftSnapshotFromMaps(
    filteredMaps,
    draftVersionsFromSnapshot(snapshot, maps),
    draftFieldMarkerManifestForMaps(draftFieldMarkerManifestFromSnapshot(snapshot), filteredMaps),
  );
}

function removeAttachmentDraftIntents(storageKey, storage) {
  const prefix = attachmentIntentStoragePrefixForDraftStorageKey(storageKey);
  if (!prefix) return;
  storageTargets(storage).forEach((target) => {
    const area = getStorage(target);
    if (!area) return;
    const keys = [];
    try {
      for (let index = 0; index < area.length; index += 1) {
        const key = area.key(index);
        if (typeof key === 'string' && key.startsWith(prefix)) keys.push(key);
      }
    } catch {
      return;
    }
    keys.forEach((key) => removeStorageValue(key, target));
  });
}

function readSentDraftMarkers(storageKey, storage, logoutFenceUpdatedAt = 0) {
  const prefix = sentMarkerStoragePrefixForDraftStorageKey(storageKey);
  const currentLogoutFenceAt = normalizedUpdatedAt(logoutFenceUpdatedAt);
  let clearedDraftVersions = new Map();
  const clearedFieldMarkerIds = new Set();
  const clearedFieldMarkerVersions = new Map();
  const clearedBeforeUpdatedAt = new Map();
  let updatedAt = 0;
  let logoutFenceAt = currentLogoutFenceAt;
  if (!prefix) {
    return {
      clearedDraftVersions,
      clearedFieldMarkerIds,
      clearedFieldMarkerVersions,
      clearedBeforeUpdatedAt,
      updatedAt,
      logoutFenceAt,
    };
  }

  storageTargets(storage).forEach((target) => {
    const area = getStorage(target);
    if (!area) return;
    try {
      for (let index = 0; index < area.length; index += 1) {
        const markerStorageKey = area.key(index);
        if (typeof markerStorageKey !== 'string' || !markerStorageKey.startsWith(prefix)) continue;
        const marker = readDraftSnapshot(markerStorageKey, target);
        const key = normalizeDraftKey(marker?.key);
        const version = normalizeDraftVersion(marker?.version);
        const markerFenceAt = normalizedUpdatedAt(marker?.logoutFenceAt);
        if (!key || !version || markerFenceAt < currentLogoutFenceAt) continue;
        const versions = [
          version,
          ...(Array.isArray(marker?.clearedVersions) ? marker.clearedVersions : []),
          ...(Array.isArray(marker?.fieldMarkerVersions) ? marker.fieldMarkerVersions : []),
        ].map(normalizeDraftVersion).filter(Boolean);
        clearedDraftVersions = mergeDraftClearVersions(
          clearedDraftVersions,
          new Map([[key, new Set(versions)]]),
        );
        (Array.isArray(marker?.fieldMarkerIds) ? marker.fieldMarkerIds : [])
          .map((id) => String(id || '').trim())
          .filter(Boolean)
          .forEach((id) => clearedFieldMarkerIds.add(id));
        const markerVersions = clearedFieldMarkerVersions.get(key) || new Set();
        (Array.isArray(marker?.fieldMarkerVersions) ? marker.fieldMarkerVersions : [])
          .map(normalizeDraftVersion)
          .filter(Boolean)
          .forEach((candidateVersion) => markerVersions.add(candidateVersion));
        if (markerVersions.size > 0) {
          clearedFieldMarkerVersions.set(key, markerVersions);
        }
        const markerCutoff = normalizedUpdatedAt(
          marker?.clearBeforeUpdatedAt,
        ) || normalizedUpdatedAt(marker?.updatedAt);
        if (markerCutoff > 0) {
          clearedBeforeUpdatedAt.set(
            key,
            Math.max(markerCutoff, clearedBeforeUpdatedAt.get(key) || 0),
          );
        }
        updatedAt = Math.max(updatedAt, normalizedUpdatedAt(marker?.updatedAt));
        logoutFenceAt = Math.max(logoutFenceAt, markerFenceAt);
      }
    } catch {
      // A blocked storage area cannot invalidate markers read from another area.
    }
  });
  return {
    clearedDraftVersions,
    clearedFieldMarkerIds,
    clearedFieldMarkerVersions,
    clearedBeforeUpdatedAt,
    updatedAt,
    logoutFenceAt,
  };
}

function removeSentDraftMarkers(storageKey, storage) {
  const prefix = sentMarkerStoragePrefixForDraftStorageKey(storageKey);
  if (!prefix) return;
  storageTargets(storage).forEach((target) => {
    const area = getStorage(target);
    if (!area) return;
    const keys = [];
    try {
      for (let index = 0; index < area.length; index += 1) {
        const key = area.key(index);
        if (typeof key === 'string' && key.startsWith(prefix)) keys.push(key);
      }
    } catch {
      return;
    }
    keys.forEach((key) => removeStorageValue(key, target));
  });
}

const COMPOSER_DRAFT_SENT_MARKER_HISTORY_LIMIT = 32;

function sentMarkerCompactionStorageKey(storageKey, key) {
  const prefix = sentMarkerStoragePrefixForDraftStorageKey(storageKey);
  const normalizedKey = normalizeDraftKey(key);
  return prefix && normalizedKey
    ? `${prefix}__compacted:${encodeURIComponent(normalizedKey)}`
    : '';
}

// Keep sent invalidations bounded while retaining one aggregate frontier for
// older versions. The aggregate carries every cleared version/field marker,
// so removing the individual records does not reopen a stale send cleanup.
function compactSentDraftMarkers(
  storageKey,
  storage,
  maxPerKey = COMPOSER_DRAFT_SENT_MARKER_HISTORY_LIMIT,
) {
  const prefix = sentMarkerStoragePrefixForDraftStorageKey(storageKey);
  if (!prefix || maxPerKey < 1) return;
  storageTargets(storage).forEach((target) => {
    const area = getStorage(target);
    if (!area) return;
    const grouped = new Map();
    try {
      for (let index = 0; index < area.length; index += 1) {
        const markerStorageKey = area.key(index);
        if (typeof markerStorageKey !== 'string' || !markerStorageKey.startsWith(prefix)) continue;
        const marker = readDraftSnapshot(markerStorageKey, target);
        const key = normalizeDraftKey(marker?.key);
        const version = normalizeDraftVersion(marker?.version);
        if (!key || !version || marker?.storageKey !== storageKey) continue;
        const records = grouped.get(key) || [];
        records.push({ markerStorageKey, marker, index });
        grouped.set(key, records);
      }
    } catch {
      return;
    }
    grouped.forEach((records, key) => {
      if (records.length <= maxPerKey) return;
      const ordered = [...records].sort((left, right) => {
        const leftAt = normalizedUpdatedAt(left.marker.updatedAt);
        const rightAt = normalizedUpdatedAt(right.marker.updatedAt);
        if (leftAt !== rightAt) return rightAt - leftAt;
        const winner = newerDraftFieldMarker(left.marker, right.marker, right.index);
        return winner.id === right.marker.id ? 1 : -1;
      });
      const aggregate = {
        storageKey,
        key,
        version: ordered[0].marker.version,
        id: `compact:${key}:${ordered[0].marker.version}`,
        compacted: true,
        clearedVersions: [...new Set(ordered.flatMap(({ marker }) => [
          marker.version,
          ...(Array.isArray(marker.clearedVersions) ? marker.clearedVersions : []),
        ]).map(normalizeDraftVersion).filter(Boolean))],
        fieldMarkerIds: [...new Set(ordered.flatMap(({ marker }) => (
          Array.isArray(marker.fieldMarkerIds) ? marker.fieldMarkerIds : []
        )).map((id) => String(id || '').trim()).filter(Boolean))],
        fieldMarkerVersions: [...new Set(ordered.flatMap(({ marker }) => (
          Array.isArray(marker.fieldMarkerVersions) ? marker.fieldMarkerVersions : []
        )).map(normalizeDraftVersion).filter(Boolean))],
        clearBeforeUpdatedAt: Math.max(...ordered.map(({ marker }) => (
          normalizedUpdatedAt(marker.clearBeforeUpdatedAt) || normalizedUpdatedAt(marker.updatedAt)
        ))),
        updatedAt: Math.max(...ordered.map(({ marker }) => normalizedUpdatedAt(marker.updatedAt))),
        logoutFenceAt: Math.max(...ordered.map(({ marker }) => normalizedUpdatedAt(marker.logoutFenceAt))),
      };
      const aggregateKey = sentMarkerCompactionStorageKey(storageKey, key);
      if (!aggregateKey || !writeStorageValue(aggregateKey, JSON.stringify(aggregate), target)) return;
      const retained = new Set(ordered.slice(0, Math.max(0, maxPerKey - 1))
        .map(({ markerStorageKey }) => markerStorageKey));
      records.forEach(({ markerStorageKey }) => {
        if (!retained.has(markerStorageKey) && markerStorageKey !== aggregateKey) {
          removeStorageValue(markerStorageKey, target);
        }
      });
    });
  });
}

function readDraftFastPath(storageKey, logoutFenceUpdatedAt = 0) {
  const fastPathStorageKey = fastPathStorageKeyForDraftStorageKey(storageKey);
  const record = readDraftSnapshot(fastPathStorageKey, 'sessionStorage');
  if (!record || normalizedUpdatedAt(record.logoutFenceAt) < logoutFenceUpdatedAt) return null;
  const snapshot = record.snapshot && typeof record.snapshot === 'object'
    && !Array.isArray(record.snapshot)
    ? record.snapshot
    : {};
  const baselineSnapshot = record.baselineSnapshot && typeof record.baselineSnapshot === 'object'
    && !Array.isArray(record.baselineSnapshot)
    ? record.baselineSnapshot
    : {};
  const hasDirtyFieldRecord = record.dirtyFields && typeof record.dirtyFields === 'object';
  return {
    snapshot,
    baselineSnapshot,
    dirtyFields: hasDirtyFieldRecord
      ? draftDirtyFieldsFromRecord(record.dirtyFields)
      : draftDirtyFieldsFromSnapshotDifference(snapshot, baselineSnapshot),
    pendingAttachmentMutations: Array.isArray(record.pendingAttachmentMutations)
      ? record.pendingAttachmentMutations.filter((mutation) => mutation && typeof mutation === 'object')
      : [],
    updatedAt: normalizedUpdatedAt(record.updatedAt),
    cleared: record.cleared === true,
    clearReason: '',
    logoutFenceAt: normalizedUpdatedAt(record.logoutFenceAt),
  };
}

function readLogoutFence(storage) {
  let selectedFence = null;
  storageTargets(storage).forEach((target, index) => {
    const fence = readDraftSnapshot(COMPOSER_DRAFT_LOGOUT_STORAGE_KEY, target);
    if (fence) {
      selectedFence = newestSnapshot(selectedFence, {
        updatedAt: normalizedUpdatedAt(fence.updatedAt),
      }, index);
    }
  });
  return { updatedAt: selectedFence?.updatedAt || 0 };
}

function readDraftSnapshots(
  storageKey,
  storage,
  logoutFenceUpdatedAt = null,
  { ignoredAttachmentRemovalIds = null, rescanState = true } = {},
) {
  const stateStorageKey = stateStorageKeyForDraftStorageKey(storageKey);
  const currentLogoutFenceAt = logoutFenceUpdatedAt === null
    ? readLogoutFence(storage).updatedAt
    : normalizedUpdatedAt(logoutFenceUpdatedAt);
  let selectedDraft = null;
  let selectedState = null;
  let clearedDraftVersions = new Map();
  storageTargets(storage).forEach((target, index) => {
    const draft = readDraftSnapshot(storageKey, target);
    if (draft && currentLogoutFenceAt <= normalizedUpdatedAt(draft.logoutFenceAt)) {
      selectedDraft = newestSnapshot(selectedDraft, {
        snapshot: draft,
        updatedAt: normalizedUpdatedAt(draft.updatedAt),
        logoutFenceAt: normalizedUpdatedAt(draft.logoutFenceAt),
      }, index);
    }
    const state = readDraftSnapshot(stateStorageKey, target);
    if (state && currentLogoutFenceAt <= normalizedUpdatedAt(state.logoutFenceAt)) {
      clearedDraftVersions = mergeDraftClearVersions(
        clearedDraftVersions,
        draftClearVersionsFromState(state),
      );
      selectedState = newestSnapshot(selectedState, {
        cleared: state.cleared === true,
        clearReason: state.clearReason === COMPOSER_DRAFT_CLEAR_REASON_LOGOUT
          ? COMPOSER_DRAFT_CLEAR_REASON_LOGOUT
          : '',
        clearedFieldMarkerIds: Array.isArray(state.clearedFieldMarkerIds)
          ? state.clearedFieldMarkerIds
            .map((id) => String(id || '').trim())
            .filter(Boolean)
          : [],
        clearedFieldKeys: Array.isArray(state.clearedFieldKeys)
          ? state.clearedFieldKeys
            .map((fieldKey) => String(fieldKey || '').trim())
            .filter(Boolean)
          : [],
        clearedFieldVersions: draftClearFieldVersionsFromState(state),
        updatedAt: normalizedUpdatedAt(state.updatedAt),
        logoutFenceAt: normalizedUpdatedAt(state.logoutFenceAt),
      }, index);
    }
  });
  const sentMarkers = readSentDraftMarkers(storageKey, storage, currentLogoutFenceAt);
  const versionLineages = readDraftVersionLineages(
    storageKey,
    storage,
    currentLogoutFenceAt,
  );
  clearedDraftVersions = expandDraftClearVersions(
    mergeDraftClearVersions(clearedDraftVersions, sentMarkers.clearedDraftVersions),
    versionLineages.lineages,
  );
  const fieldMarkers = readDraftFieldMarkers(
    storageKey,
    storage,
    currentLogoutFenceAt,
    clearedDraftVersions,
    sentMarkers,
  );
  const attachmentIntents = readAttachmentDraftIntents(
    storageKey,
    storage,
    currentLogoutFenceAt,
    { ignoredRemovalIds: ignoredAttachmentRemovalIds },
  );

  // Storage reads can be interleaved by another renderer. Re-check the small
  // state record after collecting field markers so a clear that landed during
  // the first scan is not mistaken for an older baseline. One bounded rescan
  // is enough; callers still receive a deterministic snapshot if a storage
  // adapter keeps changing on every read.
  if (rescanState) {
    let rescannedState = null;
    storageTargets(storage).forEach((target, index) => {
      const state = readDraftSnapshot(stateStorageKey, target);
      if (!state || currentLogoutFenceAt > normalizedUpdatedAt(state.logoutFenceAt)) return;
      rescannedState = newestSnapshot(rescannedState, {
        cleared: state.cleared === true,
        clearReason: state.clearReason === COMPOSER_DRAFT_CLEAR_REASON_LOGOUT
          ? COMPOSER_DRAFT_CLEAR_REASON_LOGOUT
          : '',
        clearedFieldMarkerIds: Array.isArray(state.clearedFieldMarkerIds)
          ? state.clearedFieldMarkerIds
            .map((id) => String(id || '').trim())
            .filter(Boolean)
          : [],
        clearedFieldKeys: Array.isArray(state.clearedFieldKeys)
          ? state.clearedFieldKeys
            .map((fieldKey) => String(fieldKey || '').trim())
            .filter(Boolean)
          : [],
        clearedFieldVersions: draftClearFieldVersionsFromState(state),
        updatedAt: normalizedUpdatedAt(state.updatedAt),
        logoutFenceAt: normalizedUpdatedAt(state.logoutFenceAt),
      }, index);
    });
    const stateChanged = Boolean(
      rescannedState
      && (
        !selectedState
        || rescannedState.updatedAt !== selectedState.updatedAt
        || rescannedState.cleared !== selectedState.cleared
        || rescannedState.clearReason !== selectedState.clearReason
      )
    );
    if (stateChanged) {
      return readDraftSnapshots(
        storageKey,
        storage,
        currentLogoutFenceAt,
        { ignoredAttachmentRemovalIds, rescanState: false },
      );
    }
  }
  const draftUpdatedAt = selectedDraft?.updatedAt || 0;
  const stateUpdatedAt = selectedState?.updatedAt || 0;
  const fieldMarkersUpdatedAt = fieldMarkers.updatedAt;
  if (
    selectedState
    && (
      stateUpdatedAt > Math.max(draftUpdatedAt, fieldMarkersUpdatedAt)
      || (
        stateUpdatedAt === Math.max(draftUpdatedAt, fieldMarkersUpdatedAt)
        && selectedState.cleared
      )
    )
  ) {
    // A normal clear is a collection of field tombstones plus a compatibility
    // state record. Do not let that state record erase a field written by a
    // concurrent no-lock context at the same logical time. Rebuild the
    // visible draft from markers that belong to this clear frontier; stale
    // whole-snapshot values which have no such marker stay suppressed.
    if (
      selectedState.cleared
      && selectedState.clearReason !== COMPOSER_DRAFT_CLEAR_REASON_LOGOUT
    ) {
      const observedFieldMarkerIds = new Set(selectedState.clearedFieldMarkerIds || []);
      const clearedFieldKeys = new Set(selectedState.clearedFieldKeys || []);
      const clearedFieldVersions = selectedState.clearedFieldVersions || new Map();
      const clearFrontierMarkers = new Map(
        [...fieldMarkers.markers].filter(([, marker]) => {
          const markerUpdatedAt = normalizedUpdatedAt(marker?.committedAt)
            || normalizedUpdatedAt(marker?.updatedAt);
          const markerBaseUpdatedAt = normalizedUpdatedAt(marker?.baseUpdatedAt);
          const fieldKey = `${marker?.kind}:${marker?.key}`;
          if (observedFieldMarkerIds.has(marker?.id)) return false;
          if (clearedFieldKeys.has(fieldKey) && !marker?.deleted) {
            const clearedVersion = clearedFieldVersions.get(fieldKey);
            if (
              clearedVersion
              && compareGeneratedDraftVersions(marker?.version, clearedVersion) > 0
            ) return true;
            // A value for a field explicitly cleared by this state must have
            // started from the clear frontier. An old renderer may otherwise
            // arrive with a newly-written marker whose wall clock is later
            // even though its edit began before the clear.
            return markerBaseUpdatedAt >= stateUpdatedAt;
          }
          // Keep an unobserved marker which raced before/at the clear. A
          // marker arriving later is only trusted when it started from the
          // clear frontier (a genuinely new edit); an old writer arriving
          // after the clear remains suppressed.
          return markerUpdatedAt <= stateUpdatedAt
            || markerBaseUpdatedAt >= stateUpdatedAt;
        }),
      );
      const frontierSnapshot = applyDraftFieldMarkers(
        {},
        clearFrontierMarkers,
        sentMarkers,
      );
      const filteredFrontierSnapshot = filterDraftSnapshotByAttachmentIntents(
        frontierSnapshot,
        attachmentIntents.intents,
      );
      const frontierMaps = draftMapsFromSnapshot(filteredFrontierSnapshot);
      const hasFrontierDraft = Object.values(frontierMaps).some((map) => map.size > 0);
      return {
        snapshot: filteredFrontierSnapshot,
        updatedAt: stateUpdatedAt,
        cleared: !hasFrontierDraft,
        clearReason: hasFrontierDraft ? '' : selectedState.clearReason,
        clearedFieldKeys: selectedState.clearedFieldKeys || [],
        clearedFieldVersions: selectedState.clearedFieldVersions || new Map(),
        logoutFenceAt: Math.max(
          selectedState.logoutFenceAt,
          sentMarkers.logoutFenceAt,
          versionLineages.logoutFenceAt,
          fieldMarkers.logoutFenceAt,
          attachmentIntents.logoutFenceAt,
        ),
        clearedDraftVersions,
        sentMarkerMetadata: sentMarkers,
        versionLineageMetadata: versionLineages,
        fieldMarkerMetadata: fieldMarkers,
        attachmentIntents: attachmentIntents.intents,
        attachmentIntentMetadata: attachmentIntents,
      };
    }
    return {
      snapshot: {},
      updatedAt: stateUpdatedAt,
      cleared: selectedState.cleared,
      clearReason: selectedState.clearReason,
      clearedFieldKeys: selectedState.clearedFieldKeys || [],
      clearedFieldVersions: selectedState.clearedFieldVersions || new Map(),
      logoutFenceAt: Math.max(
        selectedState.logoutFenceAt,
        sentMarkers.logoutFenceAt,
        versionLineages.logoutFenceAt,
        fieldMarkers.logoutFenceAt,
        attachmentIntents.logoutFenceAt,
      ),
      clearedDraftVersions,
      sentMarkerMetadata: sentMarkers,
      versionLineageMetadata: versionLineages,
      fieldMarkerMetadata: fieldMarkers,
      attachmentIntents: attachmentIntents.intents,
      attachmentIntentMetadata: attachmentIntents,
    };
  }
  const snapshotAfterFieldMarkers = applyDraftFieldMarkers(
    selectedDraft?.snapshot || {},
    fieldMarkers.markers,
    sentMarkers,
  );
  const snapshotAfterClearedVersions = filterDraftSnapshotByClearedVersions(
    snapshotAfterFieldMarkers,
    clearedDraftVersions,
  );
  const snapshot = filterDraftSnapshotByAttachmentIntents(
    snapshotAfterClearedVersions,
    attachmentIntents.intents,
  );
  return {
    snapshot,
    updatedAt: Math.max(
      draftUpdatedAt,
      stateUpdatedAt,
      sentMarkers.updatedAt,
      fieldMarkers.updatedAt,
      attachmentIntents.updatedAt,
    ),
    cleared: false,
    clearReason: '',
    clearedFieldKeys: selectedState?.clearedFieldKeys || [],
    clearedFieldVersions: selectedState?.clearedFieldVersions || new Map(),
    logoutFenceAt: Math.max(
      selectedDraft?.logoutFenceAt || 0,
      selectedState?.logoutFenceAt || 0,
      sentMarkers.logoutFenceAt,
      versionLineages.logoutFenceAt,
      fieldMarkers.logoutFenceAt,
      attachmentIntents.logoutFenceAt,
      currentLogoutFenceAt,
    ),
    clearedDraftVersions,
    sentMarkerMetadata: sentMarkers,
    versionLineageMetadata: versionLineages,
    fieldMarkerMetadata: fieldMarkers,
    attachmentIntents: attachmentIntents.intents,
    attachmentIntentMetadata: attachmentIntents,
  };
}

// A session snapshot may carry a field-marker manifest, while the marker
// records themselves have been evicted from localStorage (for example after a
// quota error or a partial previous handoff). Copy the immutable journals
// before copying the compatibility snapshot; otherwise a later local-only
// hydrate sees a manifest with no matching marker and drops the values it was
// supposed to preserve. If a record cannot be mirrored, omit the manifest from
// the fallback snapshot so its concrete values remain usable.
function mirrorDraftMetadataToLocalStorage(storageKey, normalizedFenceAt) {
  const source = getStorage('sessionStorage');
  const target = getStorage('localStorage');
  if (!storageKey || !source || !target) return;

  const prefixes = [
    sentMarkerStoragePrefixForDraftStorageKey(storageKey),
    attachmentIntentStoragePrefixForDraftStorageKey(storageKey),
    draftFieldMarkerStoragePrefixForDraftStorageKey(storageKey),
    draftVersionLineageStoragePrefixForDraftStorageKey(storageKey),
  ].filter(Boolean);
  const sourceRecords = [];
  try {
    for (let index = 0; index < source.length; index += 1) {
      const key = source.key(index);
      if (!prefixes.some((prefix) => key?.startsWith(prefix))) continue;
      const record = readDraftSnapshot(key, 'sessionStorage');
      if (
        !record
        || record.storageKey !== storageKey
        || normalizedUpdatedAt(record.logoutFenceAt) < normalizedFenceAt
      ) continue;
      sourceRecords.push({ key, record });
    }
  } catch {
    return;
  }

  sourceRecords.forEach(({ key, record }) => {
    let existing = readDraftSnapshot(key, 'localStorage');
    if (existing) {
      // Stable field markers can legitimately be replaced by a newer marker;
      // immutable journal entries should never be overwritten once present.
      const isStableFieldMarker = key === draftFieldMarkerStorageKey(
        storageKey,
        record.kind,
        record.key,
      );
      if (isStableFieldMarker && record.kind && record.key) {
        const winner = newerDraftFieldMarker(existing, record, Number.MAX_SAFE_INTEGER);
        if (winner.id !== record.id) return;
      } else {
        return;
      }
    }
    if (writeStorageValue(key, JSON.stringify(record), 'localStorage')) {
      existing = record;
    }
  });
}

function mirroredSnapshotWithAvailableFieldMarkers(storageKey, snapshot = {}) {
  if (!snapshotHasDraftFieldMarkerManifest(snapshot)) return snapshot;
  const manifest = draftFieldMarkerManifestFromSnapshot(snapshot);
  const missingMarker = [...manifest].some(([fieldKey, markerId]) => {
    const separator = fieldKey.indexOf(':');
    if (separator < 0) return true;
    const kind = fieldKey.slice(0, separator);
    const key = fieldKey.slice(separator + 1);
    const markerStorageKey = draftFieldMarkerStorageKey(storageKey, kind, key);
    const marker = readCurrentDraftFieldMarker(markerStorageKey, 'localStorage', kind, key);
    return marker?.id !== markerId;
  });
  if (!missingMarker) return snapshot;
  const fallback = { ...snapshot };
  delete fallback[COMPOSER_DRAFT_FIELD_MARKER_MAP_NAME];
  return fallback;
}

function mirrorHydratedSnapshotToLocalStorage(storageKey, stateStorageKey, snapshotRecord, logoutFenceAt) {
  if (!storageKey || !stateStorageKey || !snapshotRecord?.updatedAt) return;
  const localRecord = readDraftSnapshots(storageKey, 'localStorage', logoutFenceAt);
  const sourceIsNewer = snapshotRecord.updatedAt > localRecord.updatedAt;
  const sourceWinsTie = snapshotRecord.updatedAt === localRecord.updatedAt
    && snapshotRecord.cleared
    && !localRecord.cleared;
  if (!sourceIsNewer && !sourceWinsTie) return;

  const normalizedFenceAt = Math.max(
    normalizedUpdatedAt(logoutFenceAt),
    normalizedUpdatedAt(snapshotRecord.logoutFenceAt),
  );
  mirrorDraftMetadataToLocalStorage(storageKey, normalizedFenceAt);
  if (snapshotRecord.cleared) {
    writeStorageValue(
      stateStorageKey,
      JSON.stringify({
        updatedAt: snapshotRecord.updatedAt,
        cleared: true,
        ...(snapshotRecord.clearReason ? { clearReason: snapshotRecord.clearReason } : {}),
        ...(Array.isArray(snapshotRecord.clearedFieldKeys)
          ? { clearedFieldKeys: snapshotRecord.clearedFieldKeys }
          : {}),
        ...(snapshotRecord.clearedFieldVersions instanceof Map
          ? { clearedFieldVersions: [...snapshotRecord.clearedFieldVersions] }
          : Array.isArray(snapshotRecord.clearedFieldVersions)
            ? { clearedFieldVersions: snapshotRecord.clearedFieldVersions }
            : {}),
        logoutFenceAt: normalizedFenceAt,
      }),
      'localStorage',
    );
    removeStorageValue(storageKey, 'localStorage');
    return;
  }

  writeStorageValue(
    storageKey,
    JSON.stringify({
      ...mirroredSnapshotWithAvailableFieldMarkers(storageKey, snapshotRecord.snapshot || {}),
      updatedAt: snapshotRecord.updatedAt,
      logoutFenceAt: normalizedFenceAt,
    }),
    'localStorage',
  );
  writeStorageValue(
    stateStorageKey,
    JSON.stringify({
      updatedAt: snapshotRecord.updatedAt,
      cleared: false,
      ...(Array.isArray(snapshotRecord.clearedFieldKeys)
        ? { clearedFieldKeys: snapshotRecord.clearedFieldKeys }
        : {}),
      ...(snapshotRecord.clearedFieldVersions instanceof Map
        ? { clearedFieldVersions: [...snapshotRecord.clearedFieldVersions] }
        : Array.isArray(snapshotRecord.clearedFieldVersions)
          ? { clearedFieldVersions: snapshotRecord.clearedFieldVersions }
          : {}),
      logoutFenceAt: normalizedFenceAt,
    }),
    'localStorage',
  );
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

function hasComposerPhoneUploadSessionStore(store) {
  return typeof store?.getPhoneUploadSession === 'function'
    || typeof store?.phoneUploadSessions?.get === 'function';
}

function writeComposerDraftField(store, kind, key, value) {
  const definition = draftFieldDefinitions[kind];
  if (!definition) return;
  const setter = store?.[definition.setter];
  if (typeof setter === 'function') {
    const result = setter.call(store, key, value);
    if (result === false || result === '') return;
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
  const sentMarkerKeys = new Set();
  const attachmentIntentKeys = new Set();
  const fieldMarkerKeys = new Set();
  const versionLineageKeys = new Set();
  const markerDraftKeys = new Set();
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
        if (typeof key === 'string' && key.startsWith(COMPOSER_DRAFT_SENT_MARKER_STORAGE_PREFIX)) {
          sentMarkerKeys.add(key);
          const marker = readDraftSnapshot(key, target);
          if (typeof marker?.storageKey === 'string'
            && marker.storageKey.startsWith(COMPOSER_DRAFT_STORAGE_PREFIX)) {
            markerDraftKeys.add(marker.storageKey);
          }
        }
        if (typeof key === 'string' && key.startsWith(COMPOSER_DRAFT_ATTACHMENT_INTENT_STORAGE_PREFIX)) {
          attachmentIntentKeys.add(key);
          const marker = readDraftSnapshot(key, target);
          if (typeof marker?.storageKey === 'string'
            && marker.storageKey.startsWith(COMPOSER_DRAFT_STORAGE_PREFIX)) {
            markerDraftKeys.add(marker.storageKey);
          }
        }
        if (typeof key === 'string' && key.startsWith(COMPOSER_DRAFT_FIELD_MARKER_STORAGE_PREFIX)) {
          fieldMarkerKeys.add(key);
          const marker = readDraftSnapshot(key, target);
          if (typeof marker?.storageKey === 'string'
            && marker.storageKey.startsWith(COMPOSER_DRAFT_STORAGE_PREFIX)) {
            markerDraftKeys.add(marker.storageKey);
          }
        }
        if (typeof key === 'string' && key.startsWith(COMPOSER_DRAFT_VERSION_LINEAGE_STORAGE_PREFIX)) {
          versionLineageKeys.add(key);
          const marker = readDraftSnapshot(key, target);
          if (typeof marker?.storageKey === 'string'
            && marker.storageKey.startsWith(COMPOSER_DRAFT_STORAGE_PREFIX)) {
            markerDraftKeys.add(marker.storageKey);
          }
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
    ...markerDraftKeys,
    ...[...registries].flatMap((registry) => [...registry.keys()]),
  ]);

  // Close every known account store, including stores that have not written a
  // snapshot yet. This prevents a late callback from recreating a draft after
  // logout has cleared the storage keys.
  new Set([...registries].flatMap((registry) => [...registry.values()]))
    .forEach((store) => store.close?.());
  registries.forEach((registry) => registry.clear());

  // Keep a cross-context logout fence even when another browsing context has
  // an in-memory draft that has never reached storage and therefore cannot be
  // discovered by the key scan above.
  const logoutUpdatedAt = nextDraftUpdatedAt(readLogoutFence(storage).updatedAt);
  writeStorageTargets(
    COMPOSER_DRAFT_LOGOUT_STORAGE_KEY,
    JSON.stringify({ updatedAt: logoutUpdatedAt }),
    storage,
  );

  knownDraftKeys.forEach((key) => {
    const stateKey = stateStorageKeyForDraftStorageKey(key);
    const current = readDraftSnapshots(key, storage);
    const updatedAt = nextDraftUpdatedAt(current.updatedAt, logoutUpdatedAt);
    writeStorageTargets(
      stateKey,
      JSON.stringify({
        updatedAt,
        cleared: true,
        clearReason: COMPOSER_DRAFT_CLEAR_REASON_LOGOUT,
        logoutFenceAt: logoutUpdatedAt,
      }),
      storage,
    );
    removeStorageTargets(key, storage);
    removeSentDraftMarkers(key, storage);
    removeAttachmentDraftIntents(key, storage);
    removeDraftFieldMarkers(key, storage);
    removeDraftVersionLineageMarkers(key, storage);
  });
  sentMarkerKeys.forEach((key) => removeStorageTargets(key, storage));
  attachmentIntentKeys.forEach((key) => removeStorageTargets(key, storage));
  fieldMarkerKeys.forEach((key) => removeStorageTargets(key, storage));
  versionLineageKeys.forEach((key) => removeStorageTargets(key, storage));
  return keys.size;
}

export function createComposerDraftStore(userID, storage = 'sessionStorage') {
  const storageKey = composerDraftStorageKey(userID);
  const stateStorageKey = composerDraftStateStorageKey(userID);
  const fastPathStorageKey = fastPathStorageKeyForDraftStorageKey(storageKey);
  const registry = storageKey ? registryFor(storage) : null;
  const previousStore = registry?.get(storageKey);
  const logoutFence = readLogoutFence(storage);
  const durableSnapshotRecord = readDraftSnapshots(storageKey, storage, logoutFence.updatedAt);
  const fastSnapshotRecord = storage === 'sessionStorage'
    ? readDraftFastPath(storageKey, logoutFence.updatedAt)
    : null;
  const hydratedFastPath = mergeFastPathWithDurableSnapshot(
    durableSnapshotRecord,
    fastSnapshotRecord,
  );
  if (storage === 'sessionStorage' && !hydratedFastPath) {
    const mirrored = withComposerDraftWriteLock(storageKey, storage, () => (
      mirrorHydratedSnapshotToLocalStorage(
        storageKey,
        stateStorageKey,
        durableSnapshotRecord,
        logoutFence.updatedAt,
      )
    ));
    // A failed optional mirror must not prevent the tab-scoped snapshot from
    // hydrating. The next successful write will retry the shared copy.
    mirrored?.catch?.(() => {});
  }
  const snapshot = hydratedFastPath?.snapshot || durableSnapshotRecord.snapshot;
  const draftMaps = draftMapsFromSnapshot(snapshot);
  const draftVersions = draftVersionsFromSnapshot(snapshot, draftMaps);
  const draftDirtyFields = hydratedFastPath?.dirtyFields || createDraftDirtyFields();
  const inputDrafts = draftMaps.input;
  const structuredMentionDrafts = draftMaps.mention;
  const attachmentDrafts = draftMaps.attachment;
  const phoneUploadSessions = draftMaps.phoneUpload;
  const taskContextDrafts = draftMaps.taskContext;
  let active = true;
  let closed = false;
  let handoffTarget = null;
  let logoutFenced = false;
  let logoutFenceAt = logoutFence.updatedAt;
  let persistedUpdatedAt = Math.max(durableSnapshotRecord.updatedAt, logoutFence.updatedAt);
  let persistedSnapshot = durableSnapshotRecord.cleared
    ? draftSnapshotFromMaps({}, new Map())
    : draftSnapshotFromMaps(
      draftMapsFromSnapshot(durableSnapshotRecord.snapshot),
      draftVersionsFromSnapshot(durableSnapshotRecord.snapshot),
    );
  const listeners = new Set();
  let storageListener = null;
  let draftStore;

  const close = () => {
    active = false;
    closed = true;
    handoffTarget = null;
    inputDrafts.clear();
    structuredMentionDrafts.clear();
    attachmentDrafts.clear();
    phoneUploadSessions.clear();
    taskContextDrafts.clear();
    draftVersions.clear();
    clearDraftDirtyFields(draftDirtyFields);
    listeners.clear();
    if (storageListener) window.removeEventListener('storage', storageListener);
    storageListener = null;
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
    const nextVersions = draftVersionsFromSnapshot(nextSnapshot, nextMaps);
    const changedKeys = new Set([
      ...Object.values(draftMaps).flatMap((map) => [...map.keys()]),
      ...Object.values(nextMaps).flatMap((map) => [...map.keys()]),
      ...draftVersions.keys(),
      ...nextVersions.keys(),
    ]);
    Object.entries(draftMaps).forEach(([kind, map]) => {
      map.clear();
      nextMaps[kind].forEach((value, key) => map.set(key, value));
    });
    draftVersions.clear();
    nextVersions.forEach((value, key) => draftVersions.set(key, value));
    changedKeys.forEach(notify);
  };

  const invalidateKnownDraftRevisions = () => {
    const knownKeys = new Set([
      ...Object.values(draftMaps).flatMap((map) => [...map.keys()]),
      ...Object.values(draftMapsFromSnapshot(persistedSnapshot)).flatMap((map) => [...map.keys()]),
      ...draftVersions.keys(),
      ...(draftRevisionStores.get(draftStore)?.keys?.() || []),
      ...(draftMutationStores.get(draftStore)?.keys?.() || []),
    ]);
    knownKeys.forEach((key) => invalidateComposerDraftRevision(draftStore, key));
  };

  const applyLatestSnapshot = (latest) => {
    if (latest.cleared) {
      invalidateKnownDraftRevisions();
      replaceDraftMaps({});
      clearDraftDirtyFields(draftDirtyFields);
      persistedSnapshot = draftSnapshotFromMaps({}, new Map());
      persistedUpdatedAt = latest.updatedAt;
      logoutFenced = latest.clearReason === COMPOSER_DRAFT_CLEAR_REASON_LOGOUT;
      if (logoutFenced) logoutFenceAt = Math.max(logoutFenceAt, latest.updatedAt);
      return false;
    }

    const latestMaps = draftMapsFromSnapshot(latest.snapshot);
    const latestVersions = draftVersionsFromSnapshot(latest.snapshot, latestMaps);
    const latestSnapshot = draftSnapshotFromMaps(latestMaps, latestVersions);
    const nextKeys = draftKeysFromMaps(latestMaps);
    draftKeysFromMaps(draftMaps).forEach((key) => {
      if (!nextKeys.has(key)) invalidateComposerDraftRevision(draftStore, key);
    });
    replaceDraftMaps(latestSnapshot);
    clearDraftDirtyFields(draftDirtyFields);
    persistedSnapshot = latestSnapshot;
    persistedUpdatedAt = latest.updatedAt;
    return true;
  };

  const hasUnpersistedDraftChanges = () => {
    const baselineMaps = draftMapsFromSnapshot(persistedSnapshot);
    const baselineVersions = draftVersionsFromSnapshot(persistedSnapshot, baselineMaps);
    return !draftMapsEqual(draftMaps, baselineMaps)
      || !draftVersionsEqual(draftVersions, baselineVersions)
      || hasDirtyDraftFields(draftDirtyFields);
  };

  const mergeLatestSnapshotWithLocalChanges = (latest) => {
    if (latest.cleared && latest.clearReason === COMPOSER_DRAFT_CLEAR_REASON_LOGOUT) {
      return applyLatestSnapshot(latest);
    }
    const localSnapshot = draftSnapshotFromMaps(draftMaps, draftVersions);
    const localMaps = draftMapsFromSnapshot(localSnapshot);
    const localVersions = new Map(draftVersions);
    const baselineMaps = draftMapsFromSnapshot(persistedSnapshot);
    const baselineVersions = draftVersionsFromSnapshot(persistedSnapshot, baselineMaps);
    const localDirtyFields = mergeDraftDirtyFields(
      draftDirtyFields,
      draftDirtyFieldsFromSnapshotDifference(localSnapshot, persistedSnapshot),
    );
    const latestMaps = latest.cleared ? draftMapsFromSnapshot({}) : draftMapsFromSnapshot(latest.snapshot);
    const latestVersions = latest.cleared
      ? new Map()
      : draftVersionsFromSnapshot(latest.snapshot, latestMaps);
    const mergedMaps = mergeDraftMaps(
      localMaps,
      baselineMaps,
      latestMaps,
      localDirtyFields,
    );
    const mergedVersions = mergeDraftVersions(
      localMaps,
      baselineMaps,
      mergedMaps,
      localVersions,
      baselineVersions,
      latestVersions,
      localDirtyFields,
    );
    replaceDraftMaps(draftSnapshotFromMaps(mergedMaps, mergedVersions));
    // Keep the inferred dirty set in sync with the rebased maps. In normal
    // UI paths setters already mark fields dirty, but fast-path/handoff data
    // can differ from its baseline without that in-memory marker. Dropping it
    // here would make the next persist treat the local value as untouched and
    // allow a later remote snapshot to overwrite it.
    clearDraftDirtyFields(draftDirtyFields);
    Object.entries(localDirtyFields).forEach(([kind, keys]) => {
      keys.forEach((key) => markDraftFieldDirty(draftDirtyFields, kind, key));
    });
    persistedSnapshot = draftSnapshotFromMaps(latestMaps, latestVersions);
    persistedUpdatedAt = latest.updatedAt;
    return true;
  };

  const synchronizeFromStorage = () => {
    if (closed || logoutFenced || handoffTarget || !storageKey) return;
    const latestLogoutFence = readLogoutFence(storage);
    if (latestLogoutFence.updatedAt > logoutFenceAt) {
      invalidateKnownDraftRevisions();
      logoutFenced = true;
      logoutFenceAt = latestLogoutFence.updatedAt;
      replaceDraftMaps({});
      clearDraftDirtyFields(draftDirtyFields);
      persistedSnapshot = draftSnapshotFromMaps({}, new Map());
      persistedUpdatedAt = latestLogoutFence.updatedAt;
      return;
    }
    let latest = readDraftSnapshots(storageKey, storage, latestLogoutFence.updatedAt);
    if (latest.updatedAt > persistedUpdatedAt) {
      // A storage event can arrive while this context has a local write queued
      // behind the shared lock. Rebase dirty fields onto the newer durable
      // snapshot instead of leaving the entire view stale. The merge is
      // field-aware: an untouched attachment therefore reflects a sibling's X
      // immediately, while a locally edited input/attachment remains intact
      // and is persisted on the next write.
      if (hasUnpersistedDraftChanges()) {
        mergeLatestSnapshotWithLocalChanges(latest);
      } else {
        applyLatestSnapshot(latest);
      }
    }
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
    const value = readDraftFieldFromMap(draftMaps, kind, key);
    if (kind !== 'attachment') return value;
    const intentMetadata = readAttachmentDraftIntents(storageKey, storage, logoutFenceAt);
    const pendingAttachmentMutations = storage === 'sessionStorage'
      ? readDraftFastPath(storageKey, logoutFenceAt)?.pendingAttachmentMutations
      : [];
    const pendingRestoreRemovalIds = pendingAttachmentRestoreRemovalIds(
      pendingAttachmentMutations,
      intentMetadata,
    );
    return filterAttachmentsByIntent(
      value,
      attachmentIntentsIgnoringRemovalIds(intentMetadata.intents, pendingRestoreRemovalIds),
      key,
    );
  };

  const setDraftField = (kind, key, value) => {
    const definition = draftFieldDefinitions[kind];
    if (!definition) return '';
    if (logoutFenced) return '';
    if (handoffTarget) {
      const setter = handoffTarget[definition.setter];
      return typeof setter === 'function' ? setter.call(handoffTarget, key, value) : '';
    }
    if (closed) return '';
    const normalizedKey = writeDraftFieldToMap(draftMaps, kind, key, value);
    if (normalizedKey) {
      markDraftFieldDirty(draftDirtyFields, kind, normalizedKey);
      draftVersions.set(normalizedKey, nextDraftVersion());
      notify(normalizedKey);
    }
    return normalizedKey;
  };

  // Attachment updates are the only draft field that is commonly produced by
  // a late asynchronous callback. Run their read/modify/write against the
  // newest durable snapshot while holding the shared lock, so an old upload
  // cannot replace a newer context's deletion with its stale whole array.
  const mutateAttachmentDraft = (
    key,
    updater,
    {
      expectedPhoneUploadSessionId = '',
      removedPhoneUploadFileKeys = [],
      shouldContinue,
      fastPathMutation = null,
      attachmentRestoreTokens = [],
      fastPathId = '',
      skipWriteLock = false,
    } = {},
  ) => {
    const normalizedKey = normalizeDraftKey(key);
    if (!normalizedKey || typeof updater !== 'function' || logoutFenced || closed) return null;
    if (handoffTarget) {
      return handoffTarget.mutateAttachmentDraft?.(normalizedKey, updater, {
        expectedPhoneUploadSessionId,
        removedPhoneUploadFileKeys,
        shouldContinue,
        fastPathMutation,
        attachmentRestoreTokens,
        fastPathId,
        skipWriteLock,
      }) || null;
    }
    if (!skipWriteLock) {
      if (typeof shouldContinue === 'function' && !shouldContinue()) return null;
      const restoreIntendedAttachments = ['append', 'replace'].includes(fastPathMutation?.type)
        && Array.isArray(fastPathMutation?.attachments)
        ? fastPathMutation.attachments
        : [];
      const pendingRestoreTokens = attachmentRestoreTokensForAttachments(
        attachmentRestoreTokens,
        restoreIntendedAttachments,
      );
      // A later re-add supersedes a queued X immediately, but it deliberately
      // does not unmask the file. The X can therefore skip its phone tombstone
      // while a stale/canceled re-add still leaves the original remove marker
      // active until this callback commits a durable restore.
      const pendingReaddIntents = writePendingAttachmentReaddIntents(
        storageKey,
        storage,
        pendingRestoreTokens,
        { updatedAt: persistedUpdatedAt, logoutFenceAt },
      );
      const currentAttachments = draftFieldDefinitions.attachment.read(
        draftMaps.attachment.get(normalizedKey),
      );
      // A removal can be invoked with a phone-upload `{ file_key }` partial.
      // Resolve that selector to the complete attachment while it is still in
      // the current array. The durable marker and queued descriptor must carry
      // this precise snapshot, otherwise a later same-key replacement would be
      // hidden or deleted by the old X.
      const removalTarget = fastPathMutation?.type === 'remove'
        ? attachmentMutationTargetFromAttachments(fastPathMutation, currentAttachments)
        : null;
      const mutationWithResolvedRemovalTarget = removalTarget
        ? { ...fastPathMutation, attachment: removalTarget }
        : fastPathMutation;
      const removalIntent = removalTarget
        ? writeAttachmentDraftIntent(
          storageKey,
          storage,
          normalizedKey,
          removalTarget,
          'remove',
          { updatedAt: persistedUpdatedAt, logoutFenceAt },
        )
        : null;
      const mutationWithIntent = removalIntent
        ? { ...mutationWithResolvedRemovalTarget, removalIntentId: removalIntent.id }
        : mutationWithResolvedRemovalTarget;
      if (removalIntent) notify(normalizedKey);
      const priorPendingAttachmentMutations = readDraftSnapshot(
        fastPathStorageKey,
        'sessionStorage',
      )?.pendingAttachmentMutations;
      const precedingAttachmentMutations = Array.isArray(priorPendingAttachmentMutations)
        ? priorPendingAttachmentMutations
          .filter((mutation) => normalizeDraftKey(mutation?.key) === normalizedKey)
        : [];
      const precedingVersions = precedingAttachmentMutations
        .map((mutation) => normalizeDraftVersion(mutation?.version))
        .filter(Boolean);
      const pendingAttachmentMutation = mutationWithIntent && typeof mutationWithIntent === 'object'
        ? {
          ...mutationWithIntent,
          key: normalizedKey,
          baseVersion: draftVersions.get(normalizedKey) || '',
          attachmentBaseSnapshots: attachmentMutationBaseSnapshots(
            currentAttachments,
            precedingAttachmentMutations,
          ),
          precedingVersions: [...new Set(precedingVersions)],
          version: normalizeDraftVersion(mutationWithIntent.version) || nextDraftVersion(),
          expectedPhoneUploadSessionId,
          removedPhoneUploadFileKeys,
          attachmentRestoreTokens: pendingRestoreTokens,
          pendingReaddIntents,
        }
        : null;
      const capturedFastPathId = pendingAttachmentMutation
        ? persistSessionDraftFastPath({
          pendingAttachmentMutations: [pendingAttachmentMutation],
        })
        : '';
      return withComposerDraftWriteLock(storageKey, storage, () => (
        mutateAttachmentDraft(normalizedKey, updater, {
          expectedPhoneUploadSessionId,
          removedPhoneUploadFileKeys,
          shouldContinue,
          fastPathMutation: pendingAttachmentMutation,
          attachmentRestoreTokens: pendingAttachmentMutation?.attachmentRestoreTokens || [],
          fastPathId: capturedFastPathId,
          skipWriteLock: true,
        })
      ));
    }
    let abandonedPendingReadd = false;
    const discardFastPathMutation = () => {
      if (!abandonedPendingReadd && fastPathMutation?.pendingReaddIntents?.length) {
        abandonPendingAttachmentReaddIntents(
          storageKey,
          storage,
          fastPathMutation.pendingReaddIntents,
          { updatedAt: persistedUpdatedAt, logoutFenceAt },
        );
        abandonedPendingReadd = true;
      }
      if (fastPathMutation) removeFastPathAttachmentMutation(fastPathMutation);
    };
    if (typeof shouldContinue === 'function' && !shouldContinue()) {
      discardFastPathMutation();
      return null;
    }

    const latestLogoutFence = readLogoutFence(storage);
    if (latestLogoutFence.updatedAt > logoutFenceAt) {
      discardFastPathMutation();
      applyLatestSnapshot({
        snapshot: {},
        updatedAt: latestLogoutFence.updatedAt,
        cleared: true,
        clearReason: COMPOSER_DRAFT_CLEAR_REASON_LOGOUT,
      });
      return null;
    }
    let latest = readDraftSnapshots(storageKey, storage, latestLogoutFence.updatedAt);
    if (latest.cleared && latest.clearReason === COMPOSER_DRAFT_CLEAR_REASON_LOGOUT) {
      discardFastPathMutation();
      applyLatestSnapshot(latest);
      return null;
    }
    // An old callback that only knows the pre-send snapshot must not revive a
    // newer send tombstone. A context created after that tombstone has the
    // tombstone as its own baseline and therefore still accepts a new upload.
    if (latest.cleared && latest.updatedAt > persistedUpdatedAt && !hasUnpersistedDraftChanges()) {
      discardFastPathMutation();
      applyLatestSnapshot(latest);
      return null;
    }
    if (typeof shouldContinue === 'function' && !shouldContinue()) {
      discardFastPathMutation();
      return null;
    }

    const removalIntentId = String(fastPathMutation?.removalIntentId || '');
    const restoreIntendedAttachments = ['append', 'replace'].includes(fastPathMutation?.type)
      && Array.isArray(fastPathMutation?.attachments)
      ? fastPathMutation.attachments
      : [];
    const capturedRestoreTokens = attachmentRestoreTokensForAttachments(
      fastPathMutation?.attachmentRestoreTokens || attachmentRestoreTokens,
      restoreIntendedAttachments,
    );
    const activeRestoreTokens = capturedRestoreTokens.filter((token) => (
      (Array.isArray(token?.removes) ? token.removes : []).some((removeId) => (
        latest.attachmentIntentMetadata?.removalIds?.has(removeId)
        && !latest.attachmentIntentMetadata?.restoredRemovalIds?.has(removeId)
      ))
    ));
    const ignoredAttachmentRemovalIds = attachmentRestoreRemovalIds(activeRestoreTokens);
    if (
      removalIntentId
      && (
        latest.attachmentIntentMetadata?.restoredRemovalIds?.has(removalIntentId)
        || latest.attachmentIntentMetadata?.supersededRemovalIds?.has(removalIntentId)
      )
    ) {
      // A later explicit re-add either committed already or is still pending.
      // In both cases the earlier X must not overtake it (notably by adding a
      // phone-upload tombstone). A canceled re-add emits an abandon marker,
      // which removes this supersession from the next read.
      discardFastPathMutation();
      mergeLatestSnapshotWithLocalChanges(latest);
      return null;
    }
    if (removalIntentId && latest.attachmentIntentMetadata?.removalIds?.has(removalIntentId)) {
      // The remove token is deliberately visible before a queued lock runs so
      // other documents can neither revive it nor miss it when they start an
      // explicit re-add. Re-read without only our own token to perform the
      // underlying array/phone-session mutation against its real base.
      ignoredAttachmentRemovalIds.add(removalIntentId);
    }
    if (ignoredAttachmentRemovalIds.size > 0) {
      latest = readDraftSnapshots(
        storageKey,
        storage,
        latestLogoutFence.updatedAt,
        { ignoredAttachmentRemovalIds },
      );
    }

    const localMaps = draftMapsFromSnapshot(draftSnapshotFromMaps(draftMaps, draftVersions));
    const localVersions = new Map(draftVersions);
    const localDirtyFields = cloneDraftDirtyFields(draftDirtyFields);
    const baselineMaps = draftMapsFromSnapshot(persistedSnapshot);
    const baselineVersions = draftVersionsFromSnapshot(persistedSnapshot, baselineMaps);
    if (
      isDraftVersionCleared(
        latest.clearedDraftVersions,
        normalizedKey,
        draftVersions.get(normalizedKey),
      )
      && !draftKeyChangedSinceBaseline(
        localMaps,
        baselineMaps,
        localDirtyFields,
        normalizedKey,
      )
    ) {
      discardFastPathMutation();
      mergeLatestSnapshotWithLocalChanges(latest);
      return null;
    }
    const latestMaps = latest.cleared
      ? draftMapsFromSnapshot({})
      : draftMapsFromSnapshot(latest.snapshot);
    const latestVersions = latest.cleared
      ? new Map()
      : draftVersionsFromSnapshot(latest.snapshot, latestMaps);
    const mapsToMutate = latest.cleared
      ? draftMapsFromSnapshot(draftSnapshotFromMaps(localMaps, localVersions))
      : mergeDraftMaps(
        localMaps,
        baselineMaps,
        latestMaps,
        localDirtyFields,
      );

    // A remote snapshot wins the attachment base. Other local fields remain
    // merged as usual, but this one field must never start from an old array.
    if (!latest.cleared && latest.updatedAt > persistedUpdatedAt) {
      mapsToMutate.attachment = new Map(latestMaps.attachment);
    }
    let currentAttachments = draftFieldDefinitions.attachment.read(
      mapsToMutate.attachment.get(normalizedKey),
    );
    if (
      removalIntentId
      && !attachmentMutationTargetsCurrentAttachment(fastPathMutation, currentAttachments)
    ) {
      const localAttachments = draftFieldDefinitions.attachment.read(
        localMaps.attachment.get(normalizedKey),
      );
      const localTarget = localAttachments.find((attachment) => (
        attachmentMutationTargetsCurrentAttachment(fastPathMutation, [attachment])
      ));
      if (localTarget) {
        currentAttachments = applyAttachmentMutationToList(currentAttachments, {
          type: 'append',
          attachments: [localTarget],
        });
        const normalizedCurrentAttachments = draftFieldDefinitions.attachment.normalize(
          currentAttachments,
        );
        if (normalizedCurrentAttachments === null) mapsToMutate.attachment.delete(normalizedKey);
        else mapsToMutate.attachment.set(normalizedKey, normalizedCurrentAttachments);
      }
    }
    if (
      fastPathMutation?.type !== 'append'
      && !attachmentMutationMatchesCurrentBase(fastPathMutation, currentAttachments)
    ) {
      discardFastPathMutation();
      mergeLatestSnapshotWithLocalChanges(latest);
      return null;
    }
    const currentPhoneUploadSession = normalizePhoneUploadSession(
      mapsToMutate.phoneUpload.get(normalizedKey),
    );
    const expectedSessionId = String(expectedPhoneUploadSessionId || '');
    if (expectedSessionId && currentPhoneUploadSession?.session_id !== expectedSessionId) {
      discardFastPathMutation();
      return null;
    }

    const nextValue = updater([...currentAttachments], {
      phoneUploadSession: currentPhoneUploadSession,
      removedPhoneUploadFileKeys: phoneUploadRemovedFileKeys(currentPhoneUploadSession),
    });
    if (nextValue === null || nextValue === undefined) {
      discardFastPathMutation();
      return null;
    }
    const normalizedCandidateAttachments = draftFieldDefinitions.attachment.normalize(nextValue);
    const candidateAttachments = draftFieldDefinitions.attachment.read(normalizedCandidateAttachments);
    // The remove marker is intentionally visible while an upload waits for a
    // lock. Do not undo it merely because that upload once started: commit its
    // matching restore token only after the callback is still current and has
    // produced the attachment it claims to re-add.
    if (typeof shouldContinue === 'function' && !shouldContinue()) {
      discardFastPathMutation();
      return null;
    }
    const restoreIntentMarkers = writeCapturedAttachmentDraftRestoreIntents(
      storageKey,
      storage,
      attachmentRestoreTokensForAttachments(activeRestoreTokens, candidateAttachments),
      { updatedAt: Math.max(persistedUpdatedAt, latest.updatedAt), logoutFenceAt },
    );
    // `latest` may deliberately ignore this operation's observed delete while
    // checking its base. Always use the ordinary intent view for the final
    // filter so an unmatched or failed restore cannot expose the old file.
    const attachmentIntentsForCandidate = activeRestoreTokens.length > 0
      ? readAttachmentDraftIntents(storageKey, storage, latestLogoutFence.updatedAt).intents
      : latest.attachmentIntents;
    const nextAttachments = filterAttachmentsByIntent(
      candidateAttachments,
      attachmentIntentsForCandidate,
      normalizedKey,
    );
    const normalizedAttachments = draftFieldDefinitions.attachment.normalize(nextAttachments);
    const attachmentChanged = !draftValueEqual(currentAttachments, nextAttachments);
    if (restoreIntentMarkers.length > 0) notify(normalizedKey);

    let nextPhoneUploadSession = currentPhoneUploadSession;
    const removedFileKeys = normalizedPhoneUploadRemovedFileKeys(removedPhoneUploadFileKeys);
    if (removedFileKeys.length > 0 && expectedSessionId && currentPhoneUploadSession) {
      const rememberedRemovedKeys = phoneUploadRemovedFileKeys(currentPhoneUploadSession);
      removedFileKeys.forEach((fileKey) => rememberedRemovedKeys.add(fileKey));
      nextPhoneUploadSession = normalizePhoneUploadSession({
        ...currentPhoneUploadSession,
        [PHONE_UPLOAD_REMOVED_FILE_KEYS]: [...rememberedRemovedKeys],
      });
    }
    const phoneUploadSessionChanged = !draftValueEqual(
      currentPhoneUploadSession,
      nextPhoneUploadSession,
    );

    const attachmentsToRemove = fastPathMutation?.type === 'remove'
      && !fastPathMutation?.removalIntentId
      ? [fastPathMutation.attachment]
      : fastPathMutation?.type === 'replace'
        ? attachmentsRemovedByReplacement(currentAttachments, nextAttachments)
        : [];
    if (attachmentChanged) {
      attachmentsToRemove.forEach((attachment) => {
        writeAttachmentDraftIntent(
          storageKey,
          storage,
          normalizedKey,
          attachment,
          'remove',
          { updatedAt: Math.max(persistedUpdatedAt, latest.updatedAt), logoutFenceAt },
        );
      });
    }

    if (normalizedAttachments === null) mapsToMutate.attachment.delete(normalizedKey);
    else mapsToMutate.attachment.set(normalizedKey, normalizedAttachments);
    if (nextPhoneUploadSession === null) mapsToMutate.phoneUpload.delete(normalizedKey);
    else mapsToMutate.phoneUpload.set(normalizedKey, nextPhoneUploadSession);

    const versionsToPersist = mergeDraftVersions(
      localMaps,
      baselineMaps,
      mapsToMutate,
      localVersions,
      baselineVersions,
      latestVersions,
      localDirtyFields,
    );
    if (attachmentChanged || phoneUploadSessionChanged) {
      if (attachmentChanged) markDraftFieldDirty(draftDirtyFields, 'attachment', normalizedKey);
      if (phoneUploadSessionChanged) {
        markDraftFieldDirty(draftDirtyFields, 'phoneUpload', normalizedKey);
      }
      if (draftKeysFromMaps(mapsToMutate).has(normalizedKey)) {
        versionsToPersist.set(
          normalizedKey,
          normalizeDraftVersion(fastPathMutation?.version) || nextDraftVersion(),
        );
      } else {
        versionsToPersist.delete(normalizedKey);
      }
      markComposerDraftMutation(draftStore, normalizedKey);
    }
    replaceDraftMaps(draftSnapshotFromMaps(mapsToMutate, versionsToPersist));

    const persisted = draftStore.persist({ skipWriteLock: true, fastPathId });
    if (!persisted) return null;
    if (fastPathMutation) removeFastPathAttachmentMutation(fastPathMutation);
    return {
      attachments: nextAttachments,
      changed: attachmentChanged,
      phoneUploadSession: nextPhoneUploadSession,
    };
  };

  // A send marker is append-only rather than a field in the shared state
  // record. That keeps a normal draft write, or another send, from erasing a
  // marker it did not observe during its own read/modify/write.
  const clearDraftIfVersionWithMarker = (normalizedKey, expectedVersion) => {
    if (logoutFenced || closed || draftVersions.get(normalizedKey) !== expectedVersion) {
      return false;
    }
    const latestLogoutFence = readLogoutFence(storage);
    if (latestLogoutFence.updatedAt > logoutFenceAt) {
      applyLatestSnapshot({
        snapshot: {},
        updatedAt: latestLogoutFence.updatedAt,
        cleared: true,
        clearReason: COMPOSER_DRAFT_CLEAR_REASON_LOGOUT,
      });
      return false;
    }
    const latest = readDraftSnapshots(storageKey, storage, latestLogoutFence.updatedAt);
    const latestMaps = draftMapsFromSnapshot(latest.snapshot);
    const latestVersions = draftVersionsFromSnapshot(latest.snapshot, latestMaps);
    if (latest.cleared || latestVersions.get(normalizedKey) !== expectedVersion) {
      applyLatestSnapshot(latest);
      return false;
    }

    const updatedAt = nextDraftUpdatedAt(persistedUpdatedAt, latest.updatedAt);
    const markerStorageKey = sentMarkerStorageKey(storageKey, normalizedKey, expectedVersion);
    const fieldMarkersForKey = [
      ...(latest.fieldMarkerMetadata?.markers?.values?.() || []),
    ].filter((marker) => marker?.key === normalizedKey);
    const fieldMarkerIds = [...new Set(fieldMarkersForKey
      .map((marker) => String(marker?.id || '').trim())
      .filter(Boolean))];
    const fieldMarkerVersions = [...new Set(fieldMarkersForKey
      .map((marker) => normalizeDraftVersion(marker?.version))
      .filter(Boolean))];
    const lineageVersions = new Set([
      expectedVersion,
      ...(latest.clearedDraftVersions?.get?.(normalizedKey) || []),
      ...fieldMarkerVersions,
    ]);
    const lineageByKey = latest.versionLineageMetadata?.lineages;
    const expandedLineageVersions = expandDraftClearVersions(
      new Map([[normalizedKey, lineageVersions]]),
      lineageByKey,
    ).get(normalizedKey) || lineageVersions;
    const markerWritten = writeStorageTargets(
      markerStorageKey,
      JSON.stringify({
        storageKey,
        key: normalizedKey,
        version: expectedVersion,
        clearedVersions: [...expandedLineageVersions],
        fieldMarkerIds,
        fieldMarkerVersions,
        clearBeforeUpdatedAt: updatedAt,
        updatedAt,
        logoutFenceAt: Math.max(logoutFenceAt, latest.logoutFenceAt),
      }),
      storage,
    );
    if (!markerWritten) return false;
    compactSentDraftMarkers(storageKey, storage);

    markComposerDraftMutation(draftStore, normalizedKey);
    invalidateComposerDraftRevision(draftStore, normalizedKey);
    mergeLatestSnapshotWithLocalChanges(
      readDraftSnapshots(storageKey, storage, latestLogoutFence.updatedAt),
    );
    return true;
  };

  const removeDraftFastPath = () => {
    if (fastPathStorageKey) removeStorageValue(fastPathStorageKey, 'sessionStorage');
  };

  const removeDraftFastPathIfId = (id) => {
    if (!id || !fastPathStorageKey) return;
    const record = readDraftSnapshot(fastPathStorageKey, 'sessionStorage');
    if (record?.id === id) removeDraftFastPath();
  };

  const removeFastPathAttachmentMutation = (mutation) => {
    const normalizedKey = normalizeDraftKey(mutation?.key);
    const version = normalizeDraftVersion(mutation?.version);
    if (!fastPathStorageKey || !normalizedKey || !version) return;
    const rawRecord = readDraftSnapshot(fastPathStorageKey, 'sessionStorage');
    if (!rawRecord) return;
    const pendingAttachmentMutations = (Array.isArray(rawRecord.pendingAttachmentMutations)
      ? rawRecord.pendingAttachmentMutations
      : []).filter((candidate) => !(
      normalizeDraftKey(candidate?.key) === normalizedKey
      && normalizeDraftVersion(candidate?.version) === version
    ));
    if (pendingAttachmentMutations.length === (rawRecord.pendingAttachmentMutations || []).length) {
      return;
    }
    const dirtyFields = draftDirtyFieldsFromRecord(rawRecord.dirtyFields);
    if (!hasDirtyDraftFields(dirtyFields) && pendingAttachmentMutations.length === 0) {
      removeDraftFastPath();
      return;
    }
    try {
      writeStorageValue(
        fastPathStorageKey,
        JSON.stringify({ ...rawRecord, pendingAttachmentMutations }),
        'sessionStorage',
      );
    } catch {
      // The durable write has already succeeded, so a stale fast copy is only
      // an optimization concern and must not change the mutation result.
    }
  };

  const scrubDraftFastPathIfVersion = (normalizedKey, expectedVersion) => {
    if (!fastPathStorageKey) return false;
    const rawRecord = readDraftSnapshot(fastPathStorageKey, 'sessionStorage');
    const fastPath = readDraftFastPath(storageKey, logoutFenceAt);
    if (!rawRecord || !fastPath) return false;
    const data = draftDataWithoutClearedVersions(
      fastPath.snapshot,
      fastPath.dirtyFields,
      new Map(),
    );
    const snapshotMatchesVersion = data.versions.get(normalizedKey) === expectedVersion;
    const pendingAttachmentMutations = (Array.isArray(rawRecord.pendingAttachmentMutations)
      ? rawRecord.pendingAttachmentMutations
      : []).filter((mutation) => !(
      normalizeDraftKey(mutation?.key) === normalizedKey
      && [
        mutation?.baseVersion,
        mutation?.version,
        ...(Array.isArray(mutation?.precedingVersions) ? mutation.precedingVersions : []),
      ].some((version) => normalizeDraftVersion(version) === expectedVersion)
    ));
    const removedPendingMutation = pendingAttachmentMutations.length
      !== (rawRecord.pendingAttachmentMutations || []).length;
    if (!snapshotMatchesVersion && !removedPendingMutation) return false;
    if (snapshotMatchesVersion) {
      Object.values(data.maps).forEach((map) => map.delete(normalizedKey));
      data.versions.delete(normalizedKey);
      clearDraftDirtyFieldsForKey(data.dirtyFields, normalizedKey);
    }
    if (!hasDirtyDraftFields(data.dirtyFields) && pendingAttachmentMutations.length === 0) {
      removeDraftFastPath();
      return true;
    }
    try {
      return writeStorageValue(
        fastPathStorageKey,
        JSON.stringify({
          ...rawRecord,
          snapshot: draftSnapshotFromMaps(data.maps, data.versions),
          dirtyFields: draftDirtyFieldsForRecord(data.dirtyFields),
          pendingAttachmentMutations,
        }),
        'sessionStorage',
      );
    } catch {
      return false;
    }
  };

  // A navigation can destroy a document before an asynchronous browser lock
  // grants its queued write. Keep an isolated tab-scoped copy synchronous so
  // the next SkillHub document can hydrate it without changing the shared
  // snapshot observed by an already-queued conditional send cleanup.
  const persistSessionDraftFastPath = ({
    maps = draftMaps,
    versions = draftVersions,
    dirtyFields = draftDirtyFields,
    baselineSnapshot = persistedSnapshot,
    pendingAttachmentMutations = [],
  } = {}) => {
    if (storage !== 'sessionStorage' || closed || logoutFenced || !storageKey) return '';
    const sessionStorageArea = getStorage('sessionStorage');
    if (!sessionStorageArea || !fastPathStorageKey) return '';
    const latestLogoutFence = readLogoutFence(storage);
    if (latestLogoutFence.updatedAt > logoutFenceAt) return '';
    const latest = readDraftSnapshots(storageKey, storage, latestLogoutFence.updatedAt);
    const priorRecord = readDraftSnapshot(fastPathStorageKey, 'sessionStorage');
    const snapshot = draftSnapshotFromMaps(maps, versions);
    const effectiveDirtyFields = mergeDraftDirtyFields(
      dirtyFields,
      draftDirtyFieldsFromSnapshotDifference(snapshot, baselineSnapshot),
    );
    const priorPendingAttachmentMutations = Array.isArray(priorRecord?.pendingAttachmentMutations)
      ? priorRecord.pendingAttachmentMutations
      : [];
    const normalizedPendingAttachmentMutations = [...new Map(
      [...priorPendingAttachmentMutations, ...(Array.isArray(pendingAttachmentMutations)
        ? pendingAttachmentMutations
        : [])]
        .filter((mutation) => mutation && typeof mutation === 'object')
        .map((mutation) => [
          `${normalizeDraftKey(mutation.key)}:${normalizeDraftVersion(mutation.version) || ''}`,
          mutation,
        ]),
    ).values()];
    if (
      !hasDirtyDraftFields(effectiveDirtyFields)
      && normalizedPendingAttachmentMutations.length === 0
    ) return '';

    const priorFastPath = readDraftFastPath(storageKey, latestLogoutFence.updatedAt);
    const comparableRecord = {
      snapshot,
      baselineSnapshot,
      dirtyFields: draftDirtyFieldsForRecord(effectiveDirtyFields),
      pendingAttachmentMutations: normalizedPendingAttachmentMutations,
      logoutFenceAt,
    };
    if (priorRecord?.id && draftValueEqual({
      snapshot: priorRecord.snapshot || {},
      baselineSnapshot: priorRecord.baselineSnapshot || {},
      dirtyFields: priorRecord.dirtyFields || {},
      pendingAttachmentMutations: priorRecord.pendingAttachmentMutations || [],
      logoutFenceAt: normalizedUpdatedAt(priorRecord.logoutFenceAt),
    }, comparableRecord)) {
      return priorRecord.id;
    }
    const updatedAt = nextDraftUpdatedAt(
      persistedUpdatedAt,
      latest.updatedAt,
      priorFastPath?.updatedAt,
    );
    const id = nextDraftVersion();
    try {
      return writeStorageValue(
        fastPathStorageKey,
        JSON.stringify({
          ...comparableRecord,
          id,
          updatedAt,
        }),
        'sessionStorage',
      ) ? id : '';
    } catch {
      return '';
    }
  };

  draftStore = {
    // Maps remain exposed for read-only compatibility with older consumers.
    inputDrafts,
    structuredMentionDrafts,
    attachmentDrafts,
    phoneUploadSessions,
    getInputDraft(key) {
      return getDraftField('input', key);
    },
    setInputDraft(key, value) {
      return setDraftField('input', key, value);
    },
    getStructuredMentionDraft(key) {
      return getDraftField('mention', key);
    },
    setStructuredMentionDraft(key, value) {
      return setDraftField('mention', key, value);
    },
    getAttachmentDraft(key) {
      return getDraftField('attachment', key);
    },
    setAttachmentDraft(key, value) {
      return setDraftField('attachment', key, value);
    },
    mutateAttachmentDraft,
    captureAttachmentRestoreTokens(key, attachments) {
      const normalizedKey = normalizeDraftKey(key);
      if (!normalizedKey || logoutFenced || closed) return [];
      if (handoffTarget) {
        return handoffTarget.captureAttachmentRestoreTokens?.(normalizedKey, attachments) || [];
      }
      return captureAttachmentDraftRestoreTokens(
        storageKey,
        storage,
        normalizedKey,
        attachments,
        { logoutFenceAt },
      );
    },
    getPhoneUploadSession(key) {
      return getDraftField('phoneUpload', key);
    },
    setPhoneUploadSession(key, value) {
      return setDraftField('phoneUpload', key, value);
    },
    getTaskContextDraft(key) {
      return getDraftField('taskContext', key);
    },
    setTaskContextDraft(key, value) {
      return setDraftField('taskContext', key, value);
    },
    getDraftVersion(key) {
      const normalizedKey = normalizeDraftKey(key);
      if (!normalizedKey) return '';
      if (handoffTarget) return handoffTarget.getDraftVersion?.(normalizedKey) || '';
      return draftVersions.get(normalizedKey) || '';
    },
    persist({
      expectedDraftVersions = null,
      skipWriteLock = false,
      fastPathId = '',
      deletedVersions = null,
    } = {}) {
      if (logoutFenced) return false;
      if (handoffTarget) {
        return handoffTarget.persist({
          expectedDraftVersions,
          skipWriteLock,
          fastPathId,
          deletedVersions,
        });
      }
      if (!skipWriteLock) {
        const capturedFastPathId = persistSessionDraftFastPath();
        return withComposerDraftWriteLock(storageKey, storage, () => (
          draftStore.persist({
            expectedDraftVersions,
            skipWriteLock: true,
            fastPathId: capturedFastPathId,
            deletedVersions,
          })
        ));
      }
      // A composer can finish an upload after its workspace has unmounted.
      // Keep inactive stores writable until logout closes them so a replacement
      // store can hydrate the late result from either persisted storage copy.
      if (closed || logoutFenced || !storageKey) return false;
      const latestLogoutFence = readLogoutFence(storage);
      const latest = readDraftSnapshots(storageKey, storage, latestLogoutFence.updatedAt);
      if (latestLogoutFence.updatedAt > logoutFenceAt) {
        invalidateKnownDraftRevisions();
        logoutFenced = true;
        logoutFenceAt = latestLogoutFence.updatedAt;
        replaceDraftMaps({});
        clearDraftDirtyFields(draftDirtyFields);
        persistedSnapshot = draftSnapshotFromMaps({}, new Map());
        persistedUpdatedAt = latestLogoutFence.updatedAt;
        return false;
      }

      const expectedVersions = expectedDraftVersions instanceof Map
        ? expectedDraftVersions
        : null;
      if (expectedVersions?.size) {
        const latestMaps = draftMapsFromSnapshot(latest.snapshot);
        const latestVersions = draftVersionsFromSnapshot(latest.snapshot, latestMaps);
        const expectedStillCurrent = !latest.cleared
          && [...expectedVersions].every(([key, version]) => latestVersions.get(key) === version);
        if (!expectedStillCurrent) {
          applyLatestSnapshot(latest);
          return false;
        }
      }

      // Storage events are not delivered to the context that performed the
      // write. Re-read before every write so a stale tab cannot overwrite a
      // newer draft (including a cross-tab deletion tombstone).
      const localDirtyFields = mergeDraftDirtyFields(
        draftDirtyFields,
        draftDirtyFieldsFromSnapshotDifference(
          draftSnapshotFromMaps(draftMaps, draftVersions),
          persistedSnapshot,
        ),
      );
      // A deleted field is omitted from the serialized maps, but its logical
      // mutation version still matters. Preserve that version separately so a
      // delayed tombstone cannot become newer than a fresh value written by a
      // sibling context merely because the tombstone is persisted later.
      const deletedVersionsToPersist = new Map(
        deletedVersions instanceof Map ? deletedVersions : [],
      );
      Object.entries(localDirtyFields).forEach(([kind, keys]) => {
        keys.forEach((key) => {
          if (draftMaps[kind]?.has?.(key)) return;
          const version = normalizeDraftVersion(draftVersions.get(key));
          if (version && !deletedVersionsToPersist.has(`${kind}:${key}`)) {
            deletedVersionsToPersist.set(`${kind}:${key}`, version);
          }
        });
      });
      const lineageParentVersions = new Map();
      const addLineageParent = (key, version) => {
        const normalizedKey = normalizeDraftKey(key);
        const normalizedVersion = normalizeDraftVersion(version);
        if (!normalizedKey || !normalizedVersion) return;
        const parents = lineageParentVersions.get(normalizedKey) || new Set();
        parents.add(normalizedVersion);
        lineageParentVersions.set(normalizedKey, parents);
      };
      const dirtyKeys = new Set(Object.values(localDirtyFields).flatMap((keys) => [...keys]));
      const persistedMapsForLineage = draftMapsFromSnapshot(persistedSnapshot);
      const persistedVersionsForLineage = draftVersionsFromSnapshot(
        persistedSnapshot,
        persistedMapsForLineage,
      );
      dirtyKeys.forEach((key) => addLineageParent(key, persistedVersionsForLineage.get(key)));
      let mapsToPersist = draftMaps;
      let versionsToPersist = draftVersions;
      const latestNeedsRebase = latest.updatedAt > persistedUpdatedAt
        || (
          latest.updatedAt === persistedUpdatedAt
          && hasDirtyDraftFields(localDirtyFields)
        );
      if (latestNeedsRebase) {
        const clearedFieldKeys = new Set(latest.clearedFieldKeys || []);
        const baselineMaps = draftMapsFromSnapshot(persistedSnapshot);
        const staleClearedFieldEdit = [...clearedFieldKeys].some((fieldKey) => {
          const separator = fieldKey.indexOf(':');
          if (separator < 0) return false;
          const kind = fieldKey.slice(0, separator);
          const key = fieldKey.slice(separator + 1);
          if (!draftFieldDefinitions[kind] || !localDirtyFields?.[kind]?.has?.(key)) {
            return false;
          }
          const result = persistedUpdatedAt < latest.updatedAt
            || baselineMaps[kind]?.has?.(key);
          return result;
        });
        if (
          latest.clearReason !== COMPOSER_DRAFT_CLEAR_REASON_LOGOUT
          && staleClearedFieldEdit
        ) {
          // A local edit based on the pre-clear draft is a late writer. It
          // must not be promoted to a new draft merely because its callback
          // reached persist after the clear state was committed.
          applyLatestSnapshot(latest);
          return false;
        }
        if (latest.cleared) {
          if (
            latest.clearReason === COMPOSER_DRAFT_CLEAR_REASON_LOGOUT
            || !hasUnpersistedDraftChanges()
          ) {
            applyLatestSnapshot(latest);
            return false;
          }

          // A newer context changed this logical draft while an older send was
          // completing. Its queued write runs after the send tombstone, so its
          // whole local snapshot is the new draft and must revive the key.
          persistedSnapshot = draftSnapshotFromMaps({}, new Map());
          persistedUpdatedAt = latest.updatedAt;
        } else {
          const localMaps = draftMapsFromSnapshot(draftSnapshotFromMaps(draftMaps, draftVersions));
          const localVersions = new Map(draftVersions);
          const baselineMaps = draftMapsFromSnapshot(persistedSnapshot);
          const baselineVersions = draftVersionsFromSnapshot(persistedSnapshot, baselineMaps);
          const latestMaps = draftMapsFromSnapshot(latest.snapshot);
          const latestVersions = draftVersionsFromSnapshot(latest.snapshot, latestMaps);
          dirtyKeys.forEach((key) => addLineageParent(key, latestVersions.get(key)));
          const mergedMaps = mergeDraftMaps(
            localMaps,
            baselineMaps,
            latestMaps,
            localDirtyFields,
          );
          const mergedVersions = mergeDraftVersions(
            localMaps,
            baselineMaps,
            mergedMaps,
            localVersions,
            baselineVersions,
            latestVersions,
            localDirtyFields,
          );

          // No local changes are pending. Accept the newer snapshot without
          // writing it back, preserving deletion tombstones and newer values.
          if (draftMapsEqual(mergedMaps, latestMaps)
            && draftVersionsEqual(mergedVersions, latestVersions)) {
            applyLatestSnapshot(latest);
            removeDraftFastPathIfId(fastPathId);
            return true;
          }

          // Local changes made since the baseline win for their keys while
          // untouched keys from the newer browsing context remain intact.
          replaceDraftMaps(draftSnapshotFromMaps(mergedMaps, mergedVersions));
          // The remote snapshot is now the baseline for any retry. Otherwise
          // untouched remote keys would look like local edits if this write
          // fails and a later snapshot arrives.
          persistedSnapshot = draftSnapshotFromMaps(latestMaps, latestVersions);
          persistedUpdatedAt = latest.updatedAt;
          mapsToPersist = mergedMaps;
          versionsToPersist = mergedVersions;
        }
      }
      const intentFilteredMaps = filterDraftMapsByAttachmentIntents(
        mapsToPersist,
        latest.attachmentIntents,
      );
      if (intentFilteredMaps !== mapsToPersist) {
        mapsToPersist = intentFilteredMaps;
        replaceDraftMaps(draftSnapshotFromMaps(mapsToPersist, versionsToPersist));
      }
      let updatedAt = nextDraftUpdatedAt(persistedUpdatedAt, latest.updatedAt);
      let dirtyFieldsToPersist = cloneDraftDirtyFields(localDirtyFields);
      let hasDraft = Object.values(mapsToPersist).some((map) => map.size > 0);
      let clearedFieldKeys = !hasDraft
        ? [...new Set(Object.entries(dirtyFieldsToPersist).flatMap(([kind, keys]) => (
          [...keys].map((key) => `${kind}:${normalizeDraftKey(key)}`)
        )))]
        : [];
      const clearedFieldKeySet = new Set(clearedFieldKeys);
      const clearVersionForField = (fieldKey) => (
        deletedVersionsToPersist.get(fieldKey) || null
      );
      const markerBelongsToClearFrontier = (marker, fieldKey) => {
        if (!clearedFieldKeySet.has(fieldKey)) return false;
        const clearVersion = clearVersionForField(fieldKey);
        if (!clearVersion) return true;
        const versionOrder = compareGeneratedDraftVersions(marker?.version, clearVersion);
        if (versionOrder !== 0) return versionOrder < 0;
        return normalizedUpdatedAt(marker?.updatedAt) <= updatedAt;
      };
      const observedFieldMarkerIds = [...new Set([
        ...[...(latest.fieldMarkerMetadata?.markers?.values?.() || [])]
          .filter((marker) => markerBelongsToClearFrontier(
            marker,
            `${marker?.kind}:${marker?.key}`,
          ))
          .map((marker) => String(marker?.id || '').trim()),
        ...[...draftFieldMarkerManifestFromSnapshot(latest.snapshot).entries()]
          .filter(([fieldKey, markerId]) => {
            const marker = latest.fieldMarkerMetadata?.markers?.get?.(fieldKey);
            return markerBelongsToClearFrontier(marker || { id: markerId }, fieldKey);
          })
          .map(([, id]) => String(id || '').trim()),
      ].filter(Boolean))];
      const lineageVersionsToPersist = new Map(versionsToPersist);
      deletedVersionsToPersist.forEach((version, fieldKey) => {
        const separator = String(fieldKey).indexOf(':');
        const key = separator >= 0 ? String(fieldKey).slice(separator + 1) : String(fieldKey);
        if (key && !lineageVersionsToPersist.has(key)) {
          lineageVersionsToPersist.set(key, version);
        }
      });
      // Persist independent dirty fields before the compatibility whole-record
      // snapshot. If this browser has no usable cross-document lock and a
      // second tab interleaves its own snapshot write, these bounded records
      // still replay both field changes during hydration.
      writeDraftVersionLineageMarkers(
        storageKey,
        storage,
        lineageVersionsToPersist,
        dirtyFieldsToPersist,
        lineageParentVersions,
        {
          updatedAt,
          logoutFenceAt,
          lineages: latest.versionLineageMetadata?.lineages,
        },
      );
      compactDraftVersionLineageMarkers(storageKey, storage, lineageVersionsToPersist);
      const fieldMarkerWrite = writeDraftFieldMarkers(
        storageKey,
        storage,
        mapsToPersist,
        versionsToPersist,
        dirtyFieldsToPersist,
        {
          updatedAt,
          logoutFenceAt,
          baseUpdatedAt: persistedUpdatedAt,
          deletedVersions: deletedVersionsToPersist,
        },
      );
      if (fieldMarkerWrite.rejected?.size > 0) {
        // Resolve conflicts per field. A sibling may have won one marker
        // while this writer still has unrelated dirty fields; discarding the
        // whole in-memory snapshot would lose those unrelated edits.
        const rejectedMaps = draftMapsFromSnapshot(
          draftSnapshotFromMaps(mapsToPersist, versionsToPersist),
        );
        const rejectedVersions = new Map(versionsToPersist);
        let lateClearRejection = false;
        fieldMarkerWrite.rejected.forEach((entry, fieldKey) => {
          const separator = fieldKey.indexOf(':');
          if (separator < 0) return;
          const kind = fieldKey.slice(0, separator);
          const key = fieldKey.slice(separator + 1);
          const winner = entry?.marker || entry;
          if (!draftFieldDefinitions[kind] || !key) return;
          if (entry?.reason === 'late-clear') lateClearRejection = true;
          if (winner?.deleted || !Object.prototype.hasOwnProperty.call(winner || {}, 'value')) {
            rejectedMaps[kind].delete(key);
            rejectedVersions.delete(key);
          } else {
            rejectedMaps[kind].set(key, winner.value);
            if (winner.version) rejectedVersions.set(key, winner.version);
          }
          dirtyFieldsToPersist[kind]?.delete(key);
        });
        mapsToPersist = rejectedMaps;
        versionsToPersist = rejectedVersions;
        replaceDraftMaps(draftSnapshotFromMaps(rejectedMaps, rejectedVersions));
        hasDraft = Object.values(mapsToPersist).some((map) => map.size > 0);
        clearedFieldKeys = !hasDraft
          ? [...new Set(Object.entries(dirtyFieldsToPersist).flatMap(([kind, keys]) => (
            [...keys].map((key) => `${kind}:${normalizeDraftKey(key)}`)
          )))]
          : [];
        if (lateClearRejection && !hasDirtyDraftFields(dirtyFieldsToPersist)) {
          applyLatestSnapshot(readDraftSnapshots(storageKey, storage, latestLogoutFence.updatedAt));
          return false;
        }
      }
      const fieldMarkerManifestSeed = draftFieldMarkerManifestFromSnapshot(latest.snapshot);
      latest.fieldMarkerMetadata?.markers?.forEach?.((marker, fieldKey) => {
        if (marker?.deleted) fieldMarkerManifestSeed.delete(fieldKey);
        else if (marker?.id) fieldMarkerManifestSeed.set(fieldKey, marker.id);
      });
      fieldMarkerWrite.rejected?.forEach?.((entry, fieldKey) => {
        const marker = entry?.marker || entry;
        if (marker?.deleted) fieldMarkerManifestSeed.delete(fieldKey);
        else if (marker?.id) fieldMarkerManifestSeed.set(fieldKey, marker.id);
      });
      const fieldMarkerManifest = draftFieldMarkerManifestForMaps(
        fieldMarkerManifestSeed,
        mapsToPersist,
        dirtyFieldsToPersist,
      );
      fieldMarkerWrite.markers.forEach((marker, fieldKey) => {
        if (marker.deleted) fieldMarkerManifest.delete(fieldKey);
        else fieldMarkerManifest.set(fieldKey, marker.id);
      });
      const snapshotToPersist = draftSnapshotFromMaps(
        mapsToPersist,
        versionsToPersist,
        fieldMarkerManifest,
      );
      if (!hasDraft) {
        if (!draftStateWriteIsCurrent(
          stateStorageKey,
          storage,
          latest.updatedAt,
          latest.cleared,
        )) {
          applyLatestSnapshot(readDraftSnapshots(storageKey, storage, latestLogoutFence.updatedAt));
          return false;
        }
        const stateWritten = writeStorageTargets(
          stateStorageKey,
          JSON.stringify({
            updatedAt,
            cleared: true,
            clearedFieldKeys,
            clearedFieldMarkerIds: observedFieldMarkerIds,
            clearedFieldVersions: [...deletedVersionsToPersist]
              .filter(([fieldKey, version]) => clearedFieldKeys.includes(fieldKey)
                && normalizeDraftVersion(version)),
            logoutFenceAt,
          }),
          storage,
        );
        removeStorageTargets(storageKey, storage);
        if (stateWritten) {
          draftVersions.clear();
          clearDraftDirtyFields(draftDirtyFields);
          persistedSnapshot = draftSnapshotFromMaps({}, new Map());
          persistedUpdatedAt = updatedAt;
          removeDraftFastPathIfId(fastPathId);
        }
        return stateWritten;
      }
      try {
        let serialized = JSON.stringify({
          ...snapshotToPersist,
          updatedAt,
          logoutFenceAt,
        });
        if (!draftStateWriteIsCurrent(
          stateStorageKey,
          storage,
          latest.updatedAt,
          latest.cleared,
          { allowNewerNonCleared: true },
        )) {
          applyLatestSnapshot(readDraftSnapshots(storageKey, storage, latestLogoutFence.updatedAt));
          return false;
        }
        const draftWritten = writeStorageTargets(storageKey, serialized, storage);
        let stateWritten = false;
        const stateBeforeCommit = readDraftSnapshot(stateStorageKey, storage);
        if (
          stateBeforeCommit?.cleared === true
          && normalizedUpdatedAt(stateBeforeCommit.updatedAt) > latest.updatedAt
        ) {
          applyLatestSnapshot(readDraftSnapshots(storageKey, storage, latestLogoutFence.updatedAt));
          return false;
        }
        if (normalizedUpdatedAt(stateBeforeCommit?.updatedAt) >= updatedAt) {
          updatedAt = nextDraftUpdatedAt(updatedAt, stateBeforeCommit.updatedAt);
          serialized = JSON.stringify({
            ...snapshotToPersist,
            updatedAt,
            logoutFenceAt,
          });
          writeStorageTargets(storageKey, serialized, storage);
        }
        if (draftStateWriteIsCurrent(
          stateStorageKey,
          storage,
          latest.updatedAt,
          latest.cleared,
          { allowNewerNonCleared: true },
        )) {
          stateWritten = writeStorageTargets(
            stateStorageKey,
            JSON.stringify({
              updatedAt,
              cleared: false,
              logoutFenceAt,
            }),
            storage,
          );
        } else {
          applyLatestSnapshot(readDraftSnapshots(storageKey, storage, latestLogoutFence.updatedAt));
          return false;
        }
        if (draftWritten || stateWritten) {
          // A renderer without Web Locks can be interrupted between its field
          // marker write and the compatibility snapshot/state write. A sibling
          // may commit the same field in that gap, after our pre-write marker
          // check has already passed. Re-read the journal before clearing local
          // dirty state so this context does not keep displaying the stale
          // value that it just lost to the sibling.
          const latestAfterCommit = readDraftSnapshots(
            storageKey,
            storage,
            latestLogoutFence.updatedAt,
          );
          if (
            latestAfterCommit.cleared
            && latestAfterCommit.clearReason === COMPOSER_DRAFT_CLEAR_REASON_LOGOUT
          ) {
            applyLatestSnapshot(latestAfterCommit);
            return false;
          }

          const latestAfterCommitMarkers = latestAfterCommit.fieldMarkerMetadata?.markers;
          const postCommitConflicts = new Set();
          fieldMarkerWrite.markers.forEach((ownMarker, fieldKey) => {
            const separator = fieldKey.indexOf(':');
            const kind = separator >= 0 ? fieldKey.slice(0, separator) : '';
            const key = separator >= 0 ? fieldKey.slice(separator + 1) : '';
            if (!draftFieldDefinitions[kind] || !key) return;
            const winner = latestAfterCommitMarkers?.get?.(fieldKey);
            if (winner) {
              const selected = newerDraftFieldMarker(
                ownMarker,
                winner,
                Number.MAX_SAFE_INTEGER,
              );
              if (selected.id !== ownMarker.id) postCommitConflicts.add(fieldKey);
              return;
            }

            // A marker can be filtered by a send frontier between the commit
            // and this read. Treat the field as conflicted only when the
            // durable value no longer matches our own marker; a legacy record
            // with no marker but the same value is still a successful write.
            const latestMaps = draftMapsFromSnapshot(latestAfterCommit.snapshot);
            const latestVersions = draftVersionsFromSnapshot(
              latestAfterCommit.snapshot,
              latestMaps,
            );
            const latestField = latestMaps[kind];
            const sameValue = ownMarker.deleted
              ? !latestField?.has?.(key)
              : latestField?.has?.(key)
                && draftValueEqual(latestField.get(key), ownMarker.value);
            const sameVersion = !ownMarker.version
              || latestVersions.get(key) === ownMarker.version;
            if (!sameValue || !sameVersion) postCommitConflicts.add(fieldKey);
          });

          if (postCommitConflicts.size > 0) {
            const latestMaps = draftMapsFromSnapshot(latestAfterCommit.snapshot);
            const latestVersions = draftVersionsFromSnapshot(
              latestAfterCommit.snapshot,
              latestMaps,
            );
            const localSnapshot = draftSnapshotFromMaps(draftMaps, draftVersions);
            const localMaps = draftMapsFromSnapshot(localSnapshot);
            const localVersions = new Map(draftVersions);
            const baselineMaps = draftMapsFromSnapshot(persistedSnapshot);
            const baselineVersions = draftVersionsFromSnapshot(
              persistedSnapshot,
              baselineMaps,
            );
            const localDirtyAfterCommit = mergeDraftDirtyFields(
              draftDirtyFields,
              draftDirtyFieldsFromSnapshotDifference(localSnapshot, persistedSnapshot),
            );
            const remainingDirtyFields = cloneDraftDirtyFields(localDirtyAfterCommit);
            const mergeLocalMaps = Object.fromEntries(Object.entries(localMaps).map(([kind, map]) => [
              kind,
              new Map(map),
            ]));
            const mergeLocalVersions = new Map(localVersions);
            postCommitConflicts.forEach((fieldKey) => {
              const separator = fieldKey.indexOf(':');
              if (separator < 0) return;
              const kind = fieldKey.slice(0, separator);
              const key = fieldKey.slice(separator + 1);
              remainingDirtyFields[kind]?.delete(key);
              // `mergeDraftMaps` also detects value differences in addition to
              // the dirty set. Reset the conflicted local field to its old
              // baseline in the merge input so the durable marker winner is
              // not overwritten by that value-difference check.
              if (baselineMaps[kind]?.has?.(key)) {
                mergeLocalMaps[kind].set(key, baselineMaps[kind].get(key));
                if (baselineVersions.has(key)) {
                  mergeLocalVersions.set(key, baselineVersions.get(key));
                }
              } else {
                mergeLocalMaps[kind].delete(key);
                mergeLocalVersions.delete(key);
              }
            });
            const reconciledMaps = mergeDraftMaps(
              mergeLocalMaps,
              baselineMaps,
              latestMaps,
              remainingDirtyFields,
            );
            const reconciledVersions = mergeDraftVersions(
              mergeLocalMaps,
              baselineMaps,
              reconciledMaps,
              mergeLocalVersions,
              baselineVersions,
              latestVersions,
              remainingDirtyFields,
            );
            replaceDraftMaps(draftSnapshotFromMaps(reconciledMaps, reconciledVersions));
            clearDraftDirtyFields(draftDirtyFields);
            Object.entries(remainingDirtyFields).forEach(([kind, keys]) => {
              keys.forEach((key) => markDraftFieldDirty(draftDirtyFields, kind, key));
            });
            persistedSnapshot = draftSnapshotFromMaps(latestMaps, latestVersions);
            persistedUpdatedAt = latestAfterCommit.updatedAt;
            if (!hasDirtyDraftFields(draftDirtyFields)) {
              removeDraftFastPathIfId(fastPathId);
              return true;
            }
            return false;
          }

          persistedSnapshot = snapshotToPersist;
          persistedUpdatedAt = updatedAt;
          clearDraftDirtyFields(draftDirtyFields);
          removeDraftFastPathIfId(fastPathId);
          return true;
        }
      } catch {
        // Keep the in-memory draft when a browser cannot serialize or store it.
      }
      return false;
    },
    clearDraftIfVersion(key, expectedVersion, skipWriteLock = false) {
      const normalizedKey = normalizeDraftKey(key);
      if (!normalizedKey || !expectedVersion || logoutFenced || closed) return false;
      if (handoffTarget) {
        return handoffTarget.clearDraftIfVersion?.(
          normalizedKey,
          expectedVersion,
          skipWriteLock,
        ) || false;
      }
      if (!skipWriteLock) {
        scrubDraftFastPathIfVersion(normalizedKey, expectedVersion);
        if (usesSharedBrowserDraftStorage(storage)) {
          const result = withComposerDraftWriteLock(storageKey, storage, () => (
            clearDraftIfVersionWithMarker(normalizedKey, expectedVersion)
          ), { failClosed: true });
          return mapComposerDraftOperation(result, (lockedResult) => (
            lockedResult === COMPOSER_DRAFT_LOCK_UNAVAILABLE
              || lockedResult === COMPOSER_DRAFT_LOCK_FAILED
              ? clearDraftIfVersionWithMarker(normalizedKey, expectedVersion)
              : lockedResult
          ));
        }
        const result = withComposerDraftWriteLock(storageKey, storage, () => (
          draftStore.clearDraftIfVersion(normalizedKey, expectedVersion, true)
        ));
        return result;
      }
      if (logoutFenced || closed || draftVersions.get(normalizedKey) !== expectedVersion) {
        return false;
      }
      const latestLogoutFence = readLogoutFence(storage);
      if (latestLogoutFence.updatedAt > logoutFenceAt) {
        applyLatestSnapshot({
          snapshot: {},
          updatedAt: latestLogoutFence.updatedAt,
          cleared: true,
          clearReason: COMPOSER_DRAFT_CLEAR_REASON_LOGOUT,
        });
        return false;
      }
      const latest = readDraftSnapshots(storageKey, storage, latestLogoutFence.updatedAt);
      const latestMaps = draftMapsFromSnapshot(latest.snapshot);
      const latestVersions = draftVersionsFromSnapshot(latest.snapshot, latestMaps);
      if (latest.cleared || latestVersions.get(normalizedKey) !== expectedVersion) {
        applyLatestSnapshot(latest);
        return false;
      }

      let changed = false;
      Object.values(draftMaps).forEach((map) => {
        if (map.delete(normalizedKey)) changed = true;
      });
      draftVersions.delete(normalizedKey);
      if (changed) {
        Object.keys(draftFieldDefinitions).forEach((kind) => {
          markDraftFieldDirty(draftDirtyFields, kind, normalizedKey);
        });
        markComposerDraftMutation(draftStore, normalizedKey);
        notify(normalizedKey);
      }
      const persisted = draftStore.persist({
        expectedDraftVersions: new Map([[normalizedKey, expectedVersion]]),
        skipWriteLock: true,
        deletedVersions: new Map([[normalizedKey, expectedVersion]]),
      });
      return mapComposerDraftOperation(persisted, (wasPersisted) => {
        return wasPersisted;
      });
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
          JSON.stringify({
            updatedAt,
            cleared: true,
            clearReason: COMPOSER_DRAFT_CLEAR_REASON_LOGOUT,
            logoutFenceAt,
          }),
          storage,
        );
        removeStorageTargets(storageKey, storage);
        removeSentDraftMarkers(storageKey, storage);
        removeAttachmentDraftIntents(storageKey, storage);
        removeDraftFieldMarkers(storageKey, storage);
        removeDraftVersionLineageMarkers(storageKey, storage);
        removeDraftFastPath();
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
    // store while an earlier composer still has asynchronous callbacks. The
    // callback guards belong to the logical draft, so carry their counters
    // across the handoff instead of resetting them with the new store.
    canHandoff() {
      return !active && !closed;
    },
    acceptHandoffDrafts(sourceSnapshot, sourceBaselineSnapshot, sourceDirtyFieldRecord = null) {
      if (closed || !sourceSnapshot || !sourceBaselineSnapshot) return;
      const sourceMaps = draftMapsFromSnapshot(sourceSnapshot);
      const sourceVersions = draftVersionsFromSnapshot(sourceSnapshot, sourceMaps);
      const sourceBaselineMaps = draftMapsFromSnapshot(sourceBaselineSnapshot);
      const sourceBaselineVersions = draftVersionsFromSnapshot(
        sourceBaselineSnapshot,
        sourceBaselineMaps,
      );
      const sourceDirtyFields = sourceDirtyFieldRecord
        ? draftDirtyFieldsFromRecord(sourceDirtyFieldRecord)
        : draftDirtyFieldsFromSnapshotDifference(sourceSnapshot, sourceBaselineSnapshot);
      const localMaps = draftMapsFromSnapshot(draftSnapshotFromMaps(draftMaps, draftVersions));
      const localVersions = new Map(draftVersions);
      const localBaselineMaps = draftMapsFromSnapshot(persistedSnapshot);
      const localBaselineVersions = draftVersionsFromSnapshot(
        persistedSnapshot,
        localBaselineMaps,
      );
      const localDirtyFields = mergeDraftDirtyFields(
        draftDirtyFields,
        draftDirtyFieldsFromSnapshotDifference(
          draftSnapshotFromMaps(draftMaps, draftVersions),
          persistedSnapshot,
        ),
      );

      // First carry the old composer's unsaved changes into this store. Then
      // reapply any edits already made by this newly mounted context, which
      // are necessarily newer than the handoff.
      const sourceMergedMaps = mergeDraftMaps(
        sourceMaps,
        sourceBaselineMaps,
        localMaps,
        sourceDirtyFields,
      );
      const sourceMergedVersions = mergeDraftVersions(
        sourceMaps,
        sourceBaselineMaps,
        sourceMergedMaps,
        sourceVersions,
        sourceBaselineVersions,
        localVersions,
        sourceDirtyFields,
      );
      const mergedMaps = mergeDraftMaps(
        localMaps,
        localBaselineMaps,
        sourceMergedMaps,
        localDirtyFields,
      );
      const mergedVersions = mergeDraftVersions(
        localMaps,
        localBaselineMaps,
        mergedMaps,
        localVersions,
        localBaselineVersions,
        sourceMergedVersions,
        localDirtyFields,
      );
      replaceDraftMaps(draftSnapshotFromMaps(mergedMaps, mergedVersions));
      const mergedDirtyFields = mergeDraftDirtyFields(sourceDirtyFields, localDirtyFields);
      clearDraftDirtyFields(draftDirtyFields);
      Object.keys(draftFieldDefinitions).forEach((kind) => {
        mergedDirtyFields[kind].forEach((key) => markDraftFieldDirty(draftDirtyFields, kind, key));
      });
    },
    handoffTo(nextStore) {
      if (!nextStore || nextStore === draftStore || closed) return;
      if (hasUnpersistedDraftChanges()) {
        nextStore.acceptHandoffDrafts?.(
          draftSnapshotFromMaps(draftMaps, draftVersions),
          persistedSnapshot,
          draftDirtyFieldsForRecord(mergeDraftDirtyFields(
            draftDirtyFields,
            draftDirtyFieldsFromSnapshotDifference(
              draftSnapshotFromMaps(draftMaps, draftVersions),
              persistedSnapshot,
            ),
          )),
        );
      }
      copyCounterState(draftStore, mutationStoreFor(nextStore), draftMutationStores);
      copyCounterState(draftStore, revisionStoreFor(nextStore), draftRevisionStores);
      handoffTarget = nextStore;
    },
  };

  if (
    storageKey
    && typeof storage === 'string'
    && (storage === 'sessionStorage' || storage === 'localStorage')
    && typeof window !== 'undefined'
  ) {
    const sentMarkerPrefix = sentMarkerStoragePrefixForDraftStorageKey(storageKey);
    const attachmentIntentPrefix = attachmentIntentStoragePrefixForDraftStorageKey(storageKey);
    const fieldMarkerPrefix = draftFieldMarkerStoragePrefixForDraftStorageKey(storageKey);
    storageListener = (event) => {
      if (event?.key && event.key !== storageKey
        && event.key !== stateStorageKey
        && event.key !== COMPOSER_DRAFT_LOGOUT_STORAGE_KEY
        && !event.key.startsWith(sentMarkerPrefix)
        && !event.key.startsWith(attachmentIntentPrefix)
        && !event.key.startsWith(fieldMarkerPrefix)) return;
      synchronizeFromStorage();
    };
    window.addEventListener('storage', storageListener);
  }

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

export function readComposerTaskContextDraft(store, key) {
  return readComposerDraftField(store, 'taskContext', key);
}

export function writeComposerTaskContextDraft(store, key, value) {
  writeComposerDraftField(store, 'taskContext', key, value);
}

export function writeComposerAttachmentDraft(store, key, value) {
  writeComposerDraftField(store, 'attachment', key, value);
}

// Capture the delete intents visible when a user explicitly starts adding a
// file. The async upload completion must carry these tokens back to append so
// an upload that began before a later X can never revive that attachment.
export function captureComposerDraftAttachmentRestoreTokens(store, key, attachments) {
  const normalizedKey = normalizeDraftKey(key);
  if (!normalizedKey) return [];
  return store?.captureAttachmentRestoreTokens?.(normalizedKey, attachments) || [];
}

function mapComposerDraftOperation(value, mapper) {
  return value && typeof value.then === 'function' ? value.then(mapper) : mapper(value);
}

// Mutate attachment state from the newest durable value. This is intentionally
// separate from the basic write helper above: uploads and removal buttons need
// an atomic read/modify/write, whereas ordinary draft hydration still relies
// on simple field setters.
export function mutateComposerDraftAttachments(store, key, updater, options = {}) {
  const normalizedKey = normalizeDraftKey(key);
  if (!normalizedKey || typeof updater !== 'function') return null;
  if (typeof store?.mutateAttachmentDraft === 'function') {
    return store.mutateAttachmentDraft(normalizedKey, updater, options);
  }

  const expectedSessionId = String(options.expectedPhoneUploadSessionId || '');
  const currentPhoneUploadSession = readComposerPhoneUploadSession(store, normalizedKey);
  const supportsPhoneUploadSessions = hasComposerPhoneUploadSessionStore(store);
  if (
    expectedSessionId
    && supportsPhoneUploadSessions
    && currentPhoneUploadSession?.session_id !== expectedSessionId
  ) return null;
  if (typeof options.shouldContinue === 'function' && !options.shouldContinue()) return null;
  const currentAttachments = readComposerAttachmentDraft(store, normalizedKey);
  const nextValue = updater([...currentAttachments], {
    phoneUploadSession: currentPhoneUploadSession,
    removedPhoneUploadFileKeys: phoneUploadRemovedFileKeys(currentPhoneUploadSession),
  });
  if (nextValue === null || nextValue === undefined) return null;
  const normalizedAttachments = draftFieldDefinitions.attachment.normalize(nextValue);
  const nextAttachments = draftFieldDefinitions.attachment.read(normalizedAttachments);
  writeComposerAttachmentDraft(store, normalizedKey, nextAttachments);

  let nextPhoneUploadSession = currentPhoneUploadSession;
  const removedFileKeys = normalizedPhoneUploadRemovedFileKeys(options.removedPhoneUploadFileKeys);
  if (
    removedFileKeys.length > 0
    && expectedSessionId
    && supportsPhoneUploadSessions
    && currentPhoneUploadSession
  ) {
    const rememberedRemovedKeys = phoneUploadRemovedFileKeys(currentPhoneUploadSession);
    removedFileKeys.forEach((fileKey) => rememberedRemovedKeys.add(fileKey));
    nextPhoneUploadSession = normalizePhoneUploadSession({
      ...currentPhoneUploadSession,
      [PHONE_UPLOAD_REMOVED_FILE_KEYS]: [...rememberedRemovedKeys],
    });
    writeComposerPhoneUploadSession(store, normalizedKey, nextPhoneUploadSession);
  }

  const result = {
    attachments: nextAttachments,
    changed: !draftValueEqual(currentAttachments, nextAttachments),
    phoneUploadSession: nextPhoneUploadSession,
  };
  const persisted = store?.persist?.();
  return mapComposerDraftOperation(
    persisted,
    (wasPersisted) => (wasPersisted === false ? null : result),
  );
}

export function appendComposerDraftAttachments(store, key, attachments, options = {}) {
  const candidates = Array.isArray(attachments) ? attachments.filter(Boolean) : [];
  if (candidates.length === 0) return null;
  const baseAttachments = readComposerAttachmentDraft(store, key);
  const baseKnownKeys = new Set(baseAttachments.map(attachmentDraftKey).filter(Boolean));
  const candidatesToAppend = candidates.filter((attachment) => {
    const fileKey = attachmentDraftKey(attachment);
    return fileKey
      ? !baseKnownKeys.has(fileKey)
      : !baseAttachments.some((candidate) => draftValueEqual(candidate, attachment));
  });
  const appended = [];
  const result = mutateComposerDraftAttachments(store, key, (current, context) => {
    const knownKeys = new Set(current.map(attachmentDraftKey).filter(Boolean));
    const next = [...current];
    candidatesToAppend.forEach((attachment) => {
      const fileKey = attachmentDraftKey(attachment);
      if (
        options.expectedPhoneUploadSessionId
        && fileKey
        && context.removedPhoneUploadFileKeys.has(fileKey)
      ) return;
      const duplicate = fileKey
        ? knownKeys.has(fileKey)
        : next.some((candidate) => draftValueEqual(candidate, attachment));
      if (duplicate) return;
      next.push(attachment);
      appended.push(attachment);
      if (fileKey) knownKeys.add(fileKey);
    });
    return next;
  }, {
    ...options,
    fastPathMutation: candidatesToAppend.length > 0 ? {
      type: 'append',
      attachments: candidatesToAppend,
    } : null,
  });
  return mapComposerDraftOperation(result, (mutation) => (
    mutation ? { ...mutation, appended: [...appended] } : null
  ));
}

export function removeComposerDraftAttachment(store, key, attachment, options = {}) {
  const attachmentKey = attachmentDraftKey(attachment);
  const mutation = {
    type: 'remove',
    attachmentKey,
    attachment,
  };
  let removed = false;
  const result = mutateComposerDraftAttachments(store, key, (current) => {
    const target = attachmentMutationTargetFromAttachments(mutation, current);
    if (!target) return current;
    let removedOne = false;
    return current.filter((candidate) => {
      const matches = draftValueEqual(candidate, target);
      if (!matches || removedOne) return true;
      removedOne = true;
      removed = true;
      return false;
    });
  }, {
    ...options,
    removedPhoneUploadFileKeys: options.expectedPhoneUploadSessionId && attachmentKey
      ? [attachmentKey]
      : options.removedPhoneUploadFileKeys,
    fastPathMutation: mutation,
  });
  return mapComposerDraftOperation(result, (mutation) => (
    mutation ? { ...mutation, removed } : null
  ));
}

export function persistComposerDraftStore(store) {
  return store?.persist?.();
}

// This version is durable for real composer stores, unlike the callback and
// mutation counters below. A send completion can therefore distinguish a
// fresh document's replacement draft even when its text is identical.
export function readComposerDraftVersion(store, key) {
  const normalizedKey = normalizeDraftKey(key);
  if (!normalizedKey) return '';
  return store?.getDraftVersion?.(normalizedKey) || '';
}

// `null` means a legacy/in-memory store does not implement durable conditional
// clearing; callers retain their existing local race guard for that seam.
export function clearComposerDraftIfVersion(store, key, expectedVersion) {
  if (!expectedVersion || typeof store?.clearDraftIfVersion !== 'function') return null;
  return store.clearDraftIfVersion(key, expectedVersion);
}

export function readComposerDraftRevision(store, key) {
  const normalizedKey = normalizeDraftKey(key);
  if (!normalizedKey) return 0;
  const revisions = revisionMapFor(revisionStoreFor(store));
  if (!revisions) return 0;
  if (!revisions.has(normalizedKey)) revisions.set(normalizedKey, 0);
  return revisions.get(normalizedKey) || 0;
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
