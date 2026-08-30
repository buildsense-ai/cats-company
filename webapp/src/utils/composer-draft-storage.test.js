import { afterEach, describe, expect, test, vi } from 'vitest';
import {
  COMPOSER_DRAFT_STORAGE_PREFIX,
  appendComposerDraftAttachments,
  captureComposerDraftAttachmentRestoreTokens,
  clearPersistedComposerDrafts,
  createComposerDraftStore,
  invalidateComposerDraftRevision,
  isComposerDraftRevisionCurrent,
  readComposerTaskContextDraft,
  readComposerPhoneUploadSession,
  readComposerDraftMutationRevision,
  readComposerDraftRevision,
  readComposerDraftVersion,
  clearComposerDraftIfVersion,
  removeComposerDraftAttachment,
  subscribeComposerDraftStore,
  writeComposerAttachmentDraft,
  writeComposerPhoneUploadSession,
  writeComposerInputDraft,
  writeComposerTaskContextDraft,
} from './composer-draft-storage';

function sharedStorage(values = new Map(), { onGetItem, onSetItem } = {}) {
  return {
    get length() {
      return values.size;
    },
    key(index) {
      return [...values.keys()][index] || null;
    },
    getItem(key) {
      const normalizedKey = String(key);
      const value = values.has(normalizedKey) ? values.get(normalizedKey) : null;
      onGetItem?.(normalizedKey);
      return value;
    },
    setItem(key, value) {
      const normalizedKey = String(key);
      const normalizedValue = String(value);
      onSetItem?.(normalizedKey, normalizedValue);
      values.set(normalizedKey, normalizedValue);
    },
    removeItem(key) {
      values.delete(String(key));
    },
  };
}

function installSerializedWebLocks() {
  const descriptor = Object.getOwnPropertyDescriptor(globalThis.navigator, 'locks');
  const queues = new Map();
  Object.defineProperty(globalThis.navigator, 'locks', {
    configurable: true,
    value: {
      request(name, _options, callback) {
        const previous = queues.get(name) || Promise.resolve();
        const next = previous.catch(() => {}).then(callback);
        queues.set(name, next.catch(() => {}));
        return next;
      },
    },
  });
  return () => {
    if (descriptor) Object.defineProperty(globalThis.navigator, 'locks', descriptor);
    else delete globalThis.navigator.locks;
  };
}

function installSerializedIndexedDBLocks() {
  const descriptor = Object.getOwnPropertyDescriptor(globalThis, 'indexedDB');
  const objectStoreNames = new Set();
  let opened = false;
  let transactionQueue = Promise.resolve();
  const database = {
    objectStoreNames: {
      contains(name) {
        return objectStoreNames.has(name);
      },
    },
    createObjectStore(name) {
      objectStoreNames.add(name);
      return {};
    },
    transaction() {
      const transaction = {
        aborted: false,
        abort() {
          this.aborted = true;
        },
        objectStore() {
          return {
            put() {
              const request = {};
              transactionQueue = transactionQueue.catch(() => {}).then(() => new Promise((resolve) => {
                queueMicrotask(() => {
                  if (!transaction.aborted) request.onsuccess?.({ target: request });
                  queueMicrotask(() => {
                    if (transaction.aborted) transaction.onabort?.({ target: transaction });
                    else transaction.oncomplete?.({ target: transaction });
                    resolve();
                  });
                });
              }));
              return request;
            },
          };
        },
      };
      return transaction;
    },
  };
  Object.defineProperty(globalThis, 'indexedDB', {
    configurable: true,
    value: {
      open() {
        const request = {};
        queueMicrotask(() => {
          request.result = database;
          if (!opened) {
            opened = true;
            request.onupgradeneeded?.({ target: request });
          }
          request.onsuccess?.({ target: request });
        });
        return request;
      },
    },
  });
  return () => {
    if (descriptor) Object.defineProperty(globalThis, 'indexedDB', descriptor);
    else delete globalThis.indexedDB;
  };
}

describe('composer draft storage', () => {
  afterEach(() => {
    sessionStorage.clear();
    localStorage.clear();
  });

  test('clears every account draft when the auth session ends', () => {
    sessionStorage.setItem(`${COMPOSER_DRAFT_STORAGE_PREFIX}1`, '{"inputDrafts":[]}');
    sessionStorage.setItem(`${COMPOSER_DRAFT_STORAGE_PREFIX}2`, '{"inputDrafts":[]}');
    sessionStorage.setItem('unrelated-session-state', 'keep');

    expect(clearPersistedComposerDrafts()).toBe(2);
    expect(sessionStorage.getItem(`${COMPOSER_DRAFT_STORAGE_PREFIX}1`)).toBeNull();
    expect(sessionStorage.getItem(`${COMPOSER_DRAFT_STORAGE_PREFIX}2`)).toBeNull();
    expect(sessionStorage.getItem('unrelated-session-state')).toBe('keep');
  });

  test('round-trips text, mentions, and attachments through the public store API', () => {
    const store = createComposerDraftStore(' 42 ');
    store.setInputDraft('new-task', '未发送的任务');
    store.setStructuredMentionDraft('p2p_1_2', [{ target: 'usr7' }]);
    store.setAttachmentDraft('new-task', [{ name: 'brief.pdf', type: 'file' }]);
    store.persist();

    const restored = createComposerDraftStore('42');
    expect(restored.getInputDraft('new-task')).toBe('未发送的任务');
    expect(restored.getStructuredMentionDraft('p2p_1_2')).toEqual([{ target: 'usr7' }]);
    expect(restored.getAttachmentDraft('new-task')).toEqual([{ name: 'brief.pdf', type: 'file' }]);
  });

  test('mirrors drafts to localStorage so a fresh browsing context can restore them', () => {
    const store = createComposerDraftStore('42');
    store.setInputDraft('new-task', '跨工作区草稿');
    store.persist();

    expect(localStorage.getItem(`${COMPOSER_DRAFT_STORAGE_PREFIX}42`)).toContain('跨工作区草稿');
    sessionStorage.clear();

    const restored = createComposerDraftStore('42');
    expect(restored.getInputDraft('new-task')).toBe('跨工作区草稿');
  });

  test('backfills a session-only snapshot to localStorage during hydrate', () => {
    const key = `${COMPOSER_DRAFT_STORAGE_PREFIX}42`;
    sessionStorage.setItem(key, JSON.stringify({
      inputDrafts: [['new-task', '旧版本留下的草稿']],
      updatedAt: 10,
    }));

    const restored = createComposerDraftStore('42');

    expect(restored.getInputDraft('new-task')).toBe('旧版本留下的草稿');
    expect(localStorage.getItem(key)).toContain('旧版本留下的草稿');
    sessionStorage.clear();
    expect(createComposerDraftStore('42').getInputDraft('new-task'))
      .toBe('旧版本留下的草稿');
  });

  test('mirrors the journals needed by a session snapshot before a fresh local hydrate', () => {
    const storageKey = `${COMPOSER_DRAFT_STORAGE_PREFIX}mirror-journals`;
    const store = createComposerDraftStore('mirror-journals');
    const attachment = {
      type: 'file',
      name: 'remove-me.pdf',
      content: { type: 'file', payload: { file_key: 'remove-me' } },
    };
    writeComposerInputDraft(store, 'new-task', '跨上下文镜像');
    writeComposerAttachmentDraft(store, 'new-task', [attachment]);
    expect(store.persist()).toBe(true);
    expect(removeComposerDraftAttachment(store, 'new-task', attachment)?.removed).toBe(true);

    // Simulate a partial localStorage copy: the session source still has the
    // field/attachment journals, but the compatibility record and its local
    // metadata were lost before the next UHub document mounted.
    const prefixes = [
      'catsco_composer_draft_field:v1:',
      'catsco_composer_draft_attachment_intent:v1:',
      'catsco_composer_draft_lineage:v1:',
      'catsco_composer_draft_sent:v1:',
      'catsco_composer_draft_fast:v1:',
      'catsco_composer_draft_state:v1:',
    ];
    const localKeys = [...Array(localStorage.length)]
      .map((_, index) => localStorage.key(index))
      .filter(Boolean);
    localKeys.forEach((key) => {
      if (key === storageKey || prefixes.some((prefix) => key?.startsWith(prefix))) {
        localStorage.removeItem(key);
      }
    });
    expect(localStorage.getItem(storageKey)).toBeNull();

    const mirrored = createComposerDraftStore('mirror-journals');
    expect(mirrored.getInputDraft('new-task')).toBe('跨上下文镜像');
    expect(mirrored.getAttachmentDraft('new-task')).toEqual([]);

    // The next document may only have localStorage (the old session is gone),
    // yet both the text and the deletion intent must remain authoritative.
    mirrored.close();
    sessionStorage.clear();
    expect(localStorage.getItem(storageKey)).toContain('跨上下文镜像');
    const localOnly = createComposerDraftStore('mirror-journals');
    expect(localOnly.getInputDraft('new-task')).toBe('跨上下文镜像');
    expect(localOnly.getAttachmentDraft('new-task')).toEqual([]);
  });

  test('hydrates the most recent copy when both storage areas are present', () => {
    const key = `${COMPOSER_DRAFT_STORAGE_PREFIX}42`;
    sessionStorage.setItem(key, JSON.stringify({
      inputDrafts: [['new-task', '旧 tab 草稿']],
      updatedAt: 10,
    }));
    localStorage.setItem(key, JSON.stringify({
      inputDrafts: [['new-task', '最新 tab 草稿']],
      updatedAt: 20,
    }));

    const restored = createComposerDraftStore('42');
    expect(restored.getInputDraft('new-task')).toBe('最新 tab 草稿');
  });

  test('merges local edits with a newer snapshot from another browsing context', () => {
    const sharedValues = new Map();
    const tabAStorage = sharedStorage(sharedValues);
    const tabBStorage = sharedStorage(sharedValues);
    const tabA = createComposerDraftStore('42', tabAStorage);
    const tabB = createComposerDraftStore('42', tabBStorage);

    writeComposerInputDraft(tabA, 'new-task', '初始草稿');
    tabA.persist();

    writeComposerInputDraft(tabB, 'p2p_1_2', '另一个 tab 的新增草稿');
    tabB.persist();

    // Tab A has a local edit that has not been persisted since tab B wrote a
    // newer snapshot. It must retain that edit while keeping tab B's key.
    writeComposerInputDraft(tabA, 'new-task', '本地 tab 的最新输入');
    tabA.persist();

    const stored = JSON.parse(tabAStorage.getItem(`${COMPOSER_DRAFT_STORAGE_PREFIX}42`));
    expect(stored.inputDrafts).toEqual([
      ['new-task', '本地 tab 的最新输入'],
      ['p2p_1_2', '另一个 tab 的新增草稿'],
    ]);
  });

  test('does not let a stale browsing context resurrect a cleared draft', () => {
    const key = `${COMPOSER_DRAFT_STORAGE_PREFIX}42`;
    const staleStore = createComposerDraftStore('42');
    writeComposerInputDraft(staleStore, 'new-task', '旧 tab 草稿');
    staleStore.persist();

    const activeStore = createComposerDraftStore('42');
    writeComposerInputDraft(activeStore, 'new-task', '');
    activeStore.persist();

    // A writer that has not received a storage event must not overwrite the
    // newer clear marker with its in-memory snapshot.
    staleStore.persist();

    expect(sessionStorage.getItem(key)).toBeNull();
    expect(localStorage.getItem(key)).toBeNull();
    expect(createComposerDraftStore('42').getInputDraft('new-task')).toBe('');
  });

  test('does not let an old send clear an equal-valued draft rewritten in a fresh context', () => {
    const sharedValues = new Map();
    const tabAStorage = sharedStorage(sharedValues);
    const tabBStorage = sharedStorage(sharedValues);
    const verifierStorage = sharedStorage(sharedValues);
    const tabA = createComposerDraftStore('42', tabAStorage);

    writeComposerInputDraft(tabA, 'new-task', '同一段草稿');
    tabA.persist();
    const sentVersion = readComposerDraftVersion(tabA, 'new-task');

    const tabB = createComposerDraftStore('42', tabBStorage);
    writeComposerInputDraft(tabB, 'new-task', '同一段草稿');
    tabB.persist();

    // This models the late success callback from tab A. It only knows its own
    // in-memory revision, so the conditional clear must reject tab B's newer
    // equal-valued snapshot instead of treating the deletion as the winner.
    expect(clearComposerDraftIfVersion(tabA, 'new-task', sentVersion)).toBe(false);

    expect(createComposerDraftStore('42', verifierStorage).getInputDraft('new-task'))
      .toBe('同一段草稿');
  });

  test('preserves an equal-valued fresh draft when a send clear leaves a sibling draft', () => {
    const sharedValues = new Map();
    const tabAStorage = sharedStorage(sharedValues);
    const tabBStorage = sharedStorage(sharedValues);
    const verifierStorage = sharedStorage(sharedValues);
    const tabA = createComposerDraftStore('version-merge', tabAStorage);

    writeComposerInputDraft(tabA, 'new-task', '同一段草稿');
    writeComposerInputDraft(tabA, 'p2p_1_2', '不相关的会话草稿');
    tabA.persist();
    const sentVersion = readComposerDraftVersion(tabA, 'new-task');

    const tabB = createComposerDraftStore('version-merge', tabBStorage);
    // This is a new logical draft even though the field value is identical.
    // Keep it dirty until the old send removes only new-task from the shared
    // snapshot, leaving the sibling key behind.
    writeComposerInputDraft(tabB, 'new-task', '同一段草稿');

    expect(clearComposerDraftIfVersion(tabA, 'new-task', sentVersion)).toBe(true);
    expect(tabB.persist()).toBe(true);

    const verifier = createComposerDraftStore('version-merge', verifierStorage);
    expect(verifier.getInputDraft('new-task')).toBe('同一段草稿');
    expect(verifier.getInputDraft('p2p_1_2')).toBe('不相关的会话草稿');
  });

  test('does not revive a remotely removed attachment when only text changed locally', () => {
    const sharedValues = new Map();
    const staleStorage = sharedStorage(sharedValues);
    const activeStorage = sharedStorage(sharedValues);
    const verifierStorage = sharedStorage(sharedValues);
    const stale = createComposerDraftStore('field-merge', staleStorage);

    writeComposerInputDraft(stale, 'new-task', '保持相同文字');
    writeComposerAttachmentDraft(stale, 'new-task', [{ name: 'obsolete.pdf', type: 'file' }]);
    expect(stale.persist()).toBe(true);

    const active = createComposerDraftStore('field-merge', activeStorage);
    expect(removeComposerDraftAttachment(active, 'new-task', { name: 'obsolete.pdf' })?.removed)
      .toBe(true);

    // This creates a fresh logical text draft with an equal value. It must not
    // turn the stale attachment map into an edit as well.
    writeComposerInputDraft(stale, 'new-task', '保持相同文字');
    expect(stale.persist()).toBe(true);

    const verifier = createComposerDraftStore('field-merge', verifierStorage);
    expect(verifier.getInputDraft('new-task')).toBe('保持相同文字');
    expect(verifier.getAttachmentDraft('new-task')).toEqual([]);
  });

  test('keeps a removed attachment hidden when an unlocked stale snapshot writes later', () => {
    const webLocksDescriptor = Object.getOwnPropertyDescriptor(globalThis.navigator, 'locks');
    const indexedDBDescriptor = Object.getOwnPropertyDescriptor(globalThis, 'indexedDB');
    const storageTagDescriptor = Object.getOwnPropertyDescriptor(
      globalThis.localStorage,
      Symbol.toStringTag,
    );
    Object.defineProperty(globalThis.navigator, 'locks', { configurable: true, value: undefined });
    Object.defineProperty(globalThis, 'indexedDB', { configurable: true, value: undefined });
    Object.defineProperty(globalThis.localStorage, Symbol.toStringTag, {
      configurable: true,
      value: 'Storage',
    });
    try {
      const key = `${COMPOSER_DRAFT_STORAGE_PREFIX}unlocked-attachment-delete`;
      const attachment = {
        type: 'file',
        name: 'remove-me.pdf',
        content: { type: 'file', payload: { file_key: 'remove-me' } },
      };
      const source = createComposerDraftStore('unlocked-attachment-delete');
      writeComposerInputDraft(source, 'new-task', '原始文字');
      writeComposerAttachmentDraft(source, 'new-task', [attachment]);
      expect(source.persist()).toBe(true);
      const staleSnapshot = JSON.parse(localStorage.getItem(key));

      const remover = createComposerDraftStore('unlocked-attachment-delete');
      expect(removeComposerDraftAttachment(remover, 'new-task', attachment)?.removed).toBe(true);

      // Model a second document that read the old full record before the X,
      // then commits only its text edit after the delete has completed.
      localStorage.setItem(key, JSON.stringify({
        ...staleSnapshot,
        inputDrafts: [['new-task', '另一上下文的文字']],
        draftVersions: [['new-task', 'stale-text-write-version']],
        updatedAt: Math.max(Number(staleSnapshot.updatedAt) || 0, Date.now()) + 100,
      }));

      const intentKeys = [...Array(localStorage.length)]
        .map((_, index) => localStorage.key(index))
        .filter((markerKey) => markerKey?.startsWith('catsco_composer_draft_attachment_intent:v1:'));
      expect(intentKeys).toHaveLength(1);
      expect(JSON.parse(localStorage.getItem(intentKeys[0]))).toMatchObject({
        action: 'remove',
        key: 'new-task',
      });

      const restored = createComposerDraftStore('unlocked-attachment-delete');
      expect(restored.getInputDraft('new-task')).toBe('另一上下文的文字');
      expect(restored.getAttachmentDraft('new-task')).toEqual([]);
    } finally {
      if (webLocksDescriptor) Object.defineProperty(globalThis.navigator, 'locks', webLocksDescriptor);
      else delete globalThis.navigator.locks;
      if (indexedDBDescriptor) Object.defineProperty(globalThis, 'indexedDB', indexedDBDescriptor);
      else delete globalThis.indexedDB;
      if (storageTagDescriptor) {
        Object.defineProperty(globalThis.localStorage, Symbol.toStringTag, storageTagDescriptor);
      } else {
        delete globalThis.localStorage[Symbol.toStringTag];
      }
    }
  });

  test('only restores a removed attachment for an add gesture that observed its delete', () => {
    const attachment = {
      type: 'file',
      name: 'observed-remove.pdf',
      content: { type: 'file', payload: { file_key: 'observed-remove' } },
    };
    const store = createComposerDraftStore('observed-remove');
    writeComposerAttachmentDraft(store, 'new-task', [attachment]);
    expect(store.persist()).toBe(true);

    // An upload that began before the X holds no restore token. Its late
    // completion must remain hidden instead of reviving the old file.
    const staleUploadTokens = captureComposerDraftAttachmentRestoreTokens(store, 'new-task');
    expect(staleUploadTokens).toEqual([]);
    expect(removeComposerDraftAttachment(store, 'new-task', attachment)?.removed).toBe(true);
    expect(appendComposerDraftAttachments(
      store,
      'new-task',
      [attachment],
      { attachmentRestoreTokens: staleUploadTokens },
    )?.attachments).toEqual([]);

    // A new user gesture after the X observes exactly that remove token and
    // can intentionally put the attachment back into the composer.
    const explicitReaddTokens = captureComposerDraftAttachmentRestoreTokens(store, 'new-task');
    expect(explicitReaddTokens).toHaveLength(1);
    expect(appendComposerDraftAttachments(
      store,
      'new-task',
      [attachment],
      { attachmentRestoreTokens: explicitReaddTokens },
    )?.attachments).toEqual([attachment]);
    expect(createComposerDraftStore('observed-remove').getAttachmentDraft('new-task'))
      .toEqual([attachment]);
  });

  test('does not mask a concurrent replacement that reuses a removed file key', async () => {
    const attachment = {
      type: 'file',
      name: 'before-replace.pdf',
      content: { type: 'file', payload: { file_key: 'shared-file-key' } },
    };
    const replacement = {
      type: 'file',
      name: 'after-replace.pdf',
      content: { type: 'file', payload: { file_key: 'shared-file-key' } },
    };
    const remover = createComposerDraftStore('same-key-replacement');
    writeComposerAttachmentDraft(remover, 'new-task', [attachment]);
    expect(remover.persist()).toBe(true);
    const replacer = createComposerDraftStore('same-key-replacement');

    const webLocksDescriptor = Object.getOwnPropertyDescriptor(globalThis.navigator, 'locks');
    const callbacks = [];
    Object.defineProperty(globalThis.navigator, 'locks', {
      configurable: true,
      value: {
        request(_name, _options, callback) {
          return new Promise((resolve, reject) => {
            callbacks.push(() => {
              try {
                resolve(callback());
              } catch (error) {
                reject(error);
              }
            });
          });
        },
      },
    });
    try {
      const removal = removeComposerDraftAttachment(remover, 'new-task', attachment);
      expect(remover.getAttachmentDraft('new-task')).toEqual([]);

      writeComposerAttachmentDraft(replacer, 'new-task', [replacement]);
      const replaceWrite = replacer.persist();
      expect(callbacks).toHaveLength(2);

      // Let the other context win the durable write while the X is queued.
      callbacks[1]();
      await expect(replaceWrite).resolves.toBe(true);
      callbacks[0]();
      await expect(removal).resolves.toBeNull();

      expect(createComposerDraftStore('same-key-replacement').getAttachmentDraft('new-task'))
        .toEqual([replacement]);
    } finally {
      if (webLocksDescriptor) Object.defineProperty(globalThis.navigator, 'locks', webLocksDescriptor);
      else delete globalThis.navigator.locks;
    }
  });

  test('keeps a delete intent when a queued re-add becomes stale before it commits', async () => {
    const attachment = {
      type: 'file',
      name: 'canceled-readd.pdf',
      content: { type: 'file', payload: { file_key: 'canceled-readd' } },
    };
    const storageKey = `${COMPOSER_DRAFT_STORAGE_PREFIX}canceled-queued-readd`;
    const store = createComposerDraftStore('canceled-queued-readd');
    writeComposerAttachmentDraft(store, 'new-task', [attachment]);
    expect(store.persist()).toBe(true);
    const staleSnapshot = JSON.parse(localStorage.getItem(storageKey));

    const webLocksDescriptor = Object.getOwnPropertyDescriptor(globalThis.navigator, 'locks');
    const callbacks = [];
    Object.defineProperty(globalThis.navigator, 'locks', {
      configurable: true,
      value: {
        request(_name, _options, callback) {
          return new Promise((resolve, reject) => {
            callbacks.push(() => {
              try {
                resolve(callback());
              } catch (error) {
                reject(error);
              }
            });
          });
        },
      },
    });
    try {
      let readdIsCurrent = true;
      const removal = removeComposerDraftAttachment(store, 'new-task', attachment);
      const readdTokens = captureComposerDraftAttachmentRestoreTokens(store, 'new-task');
      const readd = appendComposerDraftAttachments(store, 'new-task', [attachment], {
        attachmentRestoreTokens: readdTokens,
        shouldContinue: () => readdIsCurrent,
      });
      expect(callbacks).toHaveLength(2);

      // The upload is canceled while it waits. The canceled callback must not
      // write a restore marker. The X may already have stood down for the
      // pending re-add; its original remove marker remains active either way.
      readdIsCurrent = false;
      callbacks[0]();
      await expect(removal).resolves.toBeNull();
      callbacks[1]();
      await expect(readd).resolves.toBeNull();

      localStorage.setItem(storageKey, JSON.stringify({
        ...staleSnapshot,
        updatedAt: Math.max(Number(staleSnapshot.updatedAt) || 0, Date.now()) + 100,
      }));
      expect(createComposerDraftStore('canceled-queued-readd').getAttachmentDraft('new-task'))
        .toEqual([]);
    } finally {
      if (webLocksDescriptor) Object.defineProperty(globalThis.navigator, 'locks', webLocksDescriptor);
      else delete globalThis.navigator.locks;
    }
  });

  test('uses the complete attachment snapshot for a queued partial-key removal', () => {
    const attachment = {
      type: 'file',
      name: 'full-upload.pdf',
      content: {
        type: 'file',
        payload: { file_key: 'partial-key-delete', mime: 'application/pdf' },
      },
    };
    const storageKey = `${COMPOSER_DRAFT_STORAGE_PREFIX}partial-key-delete`;
    const store = createComposerDraftStore('partial-key-delete');
    writeComposerAttachmentDraft(store, 'new-task', [attachment]);
    expect(store.persist()).toBe(true);
    const staleSnapshot = JSON.parse(localStorage.getItem(storageKey));

    const webLocksDescriptor = Object.getOwnPropertyDescriptor(globalThis.navigator, 'locks');
    const callbacks = [];
    Object.defineProperty(globalThis.navigator, 'locks', {
      configurable: true,
      value: {
        request(_name, _options, callback) {
          return new Promise((resolve, reject) => {
            callbacks.push(() => {
              try {
                resolve(callback());
              } catch (error) {
                reject(error);
              }
            });
          });
        },
      },
    });
    try {
      const removal = removeComposerDraftAttachment(store, 'new-task', {
        content: { payload: { file_key: 'partial-key-delete' } },
      });
      expect(callbacks).toHaveLength(1);
      expect(store.getAttachmentDraft('new-task')).toEqual([]);

      const removeMarker = [...Array(localStorage.length)]
        .map((_, index) => localStorage.key(index))
        .map((markerKey) => JSON.parse(localStorage.getItem(markerKey) || 'null'))
        .find((marker) => marker?.action === 'remove');
      expect(removeMarker?.attachment).toEqual(attachment);

      store.close();
      localStorage.setItem(storageKey, JSON.stringify({
        ...staleSnapshot,
        updatedAt: Math.max(Number(staleSnapshot.updatedAt) || 0, Date.now()) + 100,
      }));
      expect(createComposerDraftStore('partial-key-delete').getAttachmentDraft('new-task'))
        .toEqual([]);
      void removal;
    } finally {
      if (webLocksDescriptor) Object.defineProperty(globalThis.navigator, 'locks', webLocksDescriptor);
      else delete globalThis.navigator.locks;
    }
  });

  test('keeps a pending explicit re-add visible after a lock-delayed handoff', () => {
    const attachment = {
      type: 'file',
      name: 'handoff-readd.pdf',
      content: { type: 'file', payload: { file_key: 'handoff-readd' } },
    };
    const store = createComposerDraftStore('handoff-pending-readd');
    writeComposerAttachmentDraft(store, 'new-task', [attachment]);
    expect(store.persist()).toBe(true);

    const webLocksDescriptor = Object.getOwnPropertyDescriptor(globalThis.navigator, 'locks');
    const callbacks = [];
    Object.defineProperty(globalThis.navigator, 'locks', {
      configurable: true,
      value: {
        request(_name, _options, callback) {
          return new Promise((resolve, reject) => {
            callbacks.push(() => {
              try {
                resolve(callback());
              } catch (error) {
                reject(error);
              }
            });
          });
        },
      },
    });
    try {
      const removal = removeComposerDraftAttachment(store, 'new-task', attachment);
      const readd = appendComposerDraftAttachments(store, 'new-task', [attachment], {
        attachmentRestoreTokens: captureComposerDraftAttachmentRestoreTokens(store, 'new-task'),
      });
      expect(callbacks).toHaveLength(2);
      expect(store.getAttachmentDraft('new-task')).toEqual([attachment]);

      store.close();
      expect(createComposerDraftStore('handoff-pending-readd').getAttachmentDraft('new-task'))
        .toEqual([attachment]);
      void removal;
      void readd;
    } finally {
      if (webLocksDescriptor) Object.defineProperty(globalThis.navigator, 'locks', webLocksDescriptor);
      else delete globalThis.navigator.locks;
    }
  });

  test('retains a queued re-add marker during attachment intent compaction', () => {
    const firstAttachment = {
      type: 'file',
      name: 'compaction-readd.pdf',
      content: { type: 'file', payload: { file_key: 'compaction-readd' } },
    };
    const secondAttachment = {
      type: 'file',
      name: 'compaction-keep.pdf',
      content: { type: 'file', payload: { file_key: 'compaction-keep' } },
    };
    const store = createComposerDraftStore('compaction-readd');
    writeComposerAttachmentDraft(store, 'new-task', [firstAttachment, secondAttachment]);
    expect(store.persist()).toBe(true);

    const webLocksDescriptor = Object.getOwnPropertyDescriptor(globalThis.navigator, 'locks');
    Object.defineProperty(globalThis.navigator, 'locks', {
      configurable: true,
      value: {
        request() {
          return new Promise(() => {});
        },
      },
    });
    try {
      void removeComposerDraftAttachment(store, 'new-task', firstAttachment);
      const restoreTokens = captureComposerDraftAttachmentRestoreTokens(store, 'new-task');
      void appendComposerDraftAttachments(store, 'new-task', [firstAttachment], {
        attachmentRestoreTokens: restoreTokens,
      });

      const fastPath = JSON.parse(sessionStorage.getItem(
        'catsco_composer_draft_fast:v1:compaction-readd',
      ));
      const pendingReadd = fastPath.pendingAttachmentMutations
        .find((mutation) => mutation.type === 'append')
        ?.pendingReaddIntents?.[0];
      expect(pendingReadd?.id).toBeTruthy();

      // Push the re-add marker outside the normal 64-record retention window.
      // The marker is still referenced by the session fast path and must stay
      // available when a fresh document replays that pending mutation.
      const baseUpdatedAt = Date.now() + 100;
      for (let index = 0; index < 70; index += 1) {
        const id = `compaction-noise-${index}`;
        sessionStorage.setItem(
          `catsco_composer_draft_attachment_intent:v1:${encodeURIComponent(
            'catsco_composer_drafts:v1:compaction-readd',
          )}:new-task:${id}`,
          JSON.stringify({
            id,
            storageKey: 'catsco_composer_drafts:v1:compaction-readd',
            key: 'new-task',
            attachmentIdentity: `key:${id}`,
            action: 'restore',
            restores: [`unused-${id}`],
            updatedAt: baseUpdatedAt + index,
            logoutFenceAt: 0,
          }),
        );
      }

      // Any new lifecycle write runs compaction. Without the pending-id
      // exception this removes the supersede record above.
      void removeComposerDraftAttachment(store, 'new-task', secondAttachment);
      const supersedeKey = [...Array(sessionStorage.length)]
        .map((_, index) => sessionStorage.key(index))
        .filter(Boolean)
        .find((key) => {
          if (!key.startsWith('catsco_composer_draft_attachment_intent:v1:')) return false;
          try {
            return JSON.parse(sessionStorage.getItem(key))?.id === pendingReadd.id;
          } catch {
            return false;
          }
        });
      expect(supersedeKey).toBeTruthy();
    } finally {
      if (webLocksDescriptor) Object.defineProperty(globalThis.navigator, 'locks', webLocksDescriptor);
      else delete globalThis.navigator.locks;
    }
  });

  test('uses a versioned send marker when browser locks are unavailable', () => {
    const webLocksDescriptor = Object.getOwnPropertyDescriptor(globalThis.navigator, 'locks');
    const indexedDBDescriptor = Object.getOwnPropertyDescriptor(globalThis, 'indexedDB');
    const storageTagDescriptor = Object.getOwnPropertyDescriptor(
      globalThis.localStorage,
      Symbol.toStringTag,
    );
    Object.defineProperty(globalThis.navigator, 'locks', {
      configurable: true,
      value: undefined,
    });
    Object.defineProperty(globalThis, 'indexedDB', {
      configurable: true,
      value: undefined,
    });
    Object.defineProperty(globalThis.localStorage, Symbol.toStringTag, {
      configurable: true,
      value: 'Storage',
    });
    try {
      const store = createComposerDraftStore('no-browser-locks');
      writeComposerInputDraft(store, 'new-task', '已经发送的草稿');
      writeComposerInputDraft(store, 'p2p_1_2', '另一个已发送草稿');
      expect(store.persist()).toBe(true);
      const sentVersion = readComposerDraftVersion(store, 'new-task');
      const siblingSentVersion = readComposerDraftVersion(store, 'p2p_1_2');

      expect(clearComposerDraftIfVersion(store, 'new-task', sentVersion)).toBe(true);
      expect(clearComposerDraftIfVersion(store, 'p2p_1_2', siblingSentVersion)).toBe(true);
      const markerKeys = [...Array(localStorage.length)]
        .map((_, index) => localStorage.key(index))
        .filter((key) => key?.startsWith('catsco_composer_draft_sent:v1:'));
      expect(markerKeys).toHaveLength(2);
      expect(markerKeys.map((key) => JSON.parse(localStorage.getItem(key)))).toEqual(
        expect.arrayContaining([
          expect.objectContaining({ key: 'new-task', version: sentVersion }),
          expect.objectContaining({ key: 'p2p_1_2', version: siblingSentVersion }),
        ]),
      );

      store.close();
      const freshContext = createComposerDraftStore('no-browser-locks');
      expect(freshContext.getInputDraft('new-task')).toBe('');
      expect(freshContext.getInputDraft('p2p_1_2')).toBe('');

      writeComposerInputDraft(freshContext, 'new-task', '下一次的新草稿');
      expect(freshContext.persist()).toBe(true);
      freshContext.close();

      expect(createComposerDraftStore('no-browser-locks').getInputDraft('new-task'))
        .toBe('下一次的新草稿');
    } finally {
      if (webLocksDescriptor) Object.defineProperty(globalThis.navigator, 'locks', webLocksDescriptor);
      else delete globalThis.navigator.locks;
      if (indexedDBDescriptor) Object.defineProperty(globalThis, 'indexedDB', indexedDBDescriptor);
      else delete globalThis.indexedDB;
      if (storageTagDescriptor) {
        Object.defineProperty(globalThis.localStorage, Symbol.toStringTag, storageTagDescriptor);
      } else {
        delete globalThis.localStorage[Symbol.toStringTag];
      }
    }
  });

  test('clears every observed field marker when sending a no-lock merged frontier', () => {
    const sharedValues = new Map();
    const draftKey = `${COMPOSER_DRAFT_STORAGE_PREFIX}merged-send-frontier`;
    const seedStorage = sharedStorage(sharedValues);
    const seed = createComposerDraftStore('merged-send-frontier', seedStorage);
    writeComposerInputDraft(seed, 'new-task', '基线文字');
    expect(seed.persist()).toBe(true);

    const tabBStorage = sharedStorage(sharedValues);
    const tabB = createComposerDraftStore('merged-send-frontier', tabBStorage);
    let interleaved = true;
    const tabAStorage = sharedStorage(sharedValues, {
      onSetItem(key) {
        if (interleaved && key === draftKey) {
          interleaved = false;
          expect(tabB.persist()).toBe(true);
        }
      },
    });
    const tabA = createComposerDraftStore('merged-send-frontier', tabAStorage);
    writeComposerInputDraft(tabA, 'new-task', 'A 的文字');
    writeComposerAttachmentDraft(tabB, 'new-task', [{ name: 'B 的文件' }]);

    // The two whole-snapshot writes overlap. Field records must retain both
    // branches even though the last compatibility snapshot contains one.
    expect(tabA.persist()).toBe(true);
    const merged = createComposerDraftStore('merged-send-frontier', sharedStorage(sharedValues));
    expect(merged.getInputDraft('new-task')).toBe('A 的文字');
    expect(merged.getAttachmentDraft('new-task')).toEqual([{ name: 'B 的文件' }]);

    // A follow-up text-only write can have the same logical timestamp as the
    // sibling field marker in an unlocked renderer. It must not prune that
    // marker from the compatibility snapshot.
    writeComposerInputDraft(tabA, 'new-task', 'A 的后续文字');
    expect(tabA.persist()).toBe(true);
    const afterFollowUp = createComposerDraftStore(
      'merged-send-frontier',
      sharedStorage(sharedValues),
    );
    expect(afterFollowUp.getAttachmentDraft('new-task')).toEqual([
      { name: 'B 的文件' },
    ]);

    const sentVersion = readComposerDraftVersion(afterFollowUp, 'new-task');
    expect(clearComposerDraftIfVersion(afterFollowUp, 'new-task', sentVersion)).toBe(true);
    const restored = createComposerDraftStore('merged-send-frontier', sharedStorage(sharedValues));
    expect(restored.getInputDraft('new-task')).toBe('');
    expect(restored.getAttachmentDraft('new-task')).toEqual([]);
  });

  test('keeps the newest same-field mutation when unlocked marker writes overlap', () => {
    const sharedValues = new Map();
    const fieldPrefix = 'catsco_composer_draft_field:v1:';
    const seed = createComposerDraftStore('same-field-overlap', sharedStorage(sharedValues));
    writeComposerInputDraft(seed, 'new-task', '基线');
    expect(seed.persist()).toBe(true);

    const tabB = createComposerDraftStore('same-field-overlap', sharedStorage(sharedValues));
    const tabAStorage = sharedStorage(sharedValues, {
      onSetItem(key) {
        // Let B commit after A has appended its immutable journal entry but
        // before A writes the legacy stable marker. A stable-key CAS alone
        // would leave A's stale value as the only visible record.
        if (!interleaved && key.startsWith(fieldPrefix) && key.split(':').length >= 6) {
          interleaved = true;
          expect(tabB.persist()).toBe(true);
        }
      },
    });
    const tabA = createComposerDraftStore('same-field-overlap', tabAStorage);
    writeComposerInputDraft(tabA, 'new-task', 'A 的较早输入');
    writeComposerInputDraft(tabB, 'new-task', 'B 的较新输入');
    let interleaved = false;

    expect(tabA.persist()).toBe(true);
    expect(interleaved).toBe(true);
    // The sibling can commit after this renderer's field marker read but
    // before its compatibility snapshot/state write. The live store must
    // reconcile to the marker winner instead of clearing its local dirty
    // value and silently continuing to display stale text.
    expect(tabA.getInputDraft('new-task')).toBe('B 的较新输入');

    const verifier = createComposerDraftStore(
      'same-field-overlap',
      sharedStorage(sharedValues),
    );
    expect(verifier.getInputDraft('new-task')).toBe('B 的较新输入');
  });

  test('reconciles a same-field winner without dropping unrelated local edits', () => {
    const sharedValues = new Map();
    const fieldPrefix = 'catsco_composer_draft_field:v1:';
    const seed = createComposerDraftStore('same-field-unrelated-edit', sharedStorage(sharedValues));
    writeComposerInputDraft(seed, 'new-task', '基线');
    expect(seed.persist()).toBe(true);

    const tabB = createComposerDraftStore('same-field-unrelated-edit', sharedStorage(sharedValues));
    let interleaved = false;
    const tabAStorage = sharedStorage(sharedValues, {
      onSetItem(key) {
        if (!interleaved && key.startsWith(fieldPrefix) && key.split(':').length >= 6) {
          interleaved = true;
          expect(tabB.persist()).toBe(true);
        }
      },
    });
    const tabA = createComposerDraftStore('same-field-unrelated-edit', tabAStorage);
    writeComposerInputDraft(tabA, 'new-task', 'A 的输入');
    writeComposerAttachmentDraft(tabA, 'new-task', [{ name: 'A 的文件' }]);
    writeComposerInputDraft(tabB, 'new-task', 'B 的输入');

    expect(tabA.persist()).toBe(false);
    expect(interleaved).toBe(true);
    expect(tabA.getInputDraft('new-task')).toBe('B 的输入');
    expect(tabA.getAttachmentDraft('new-task')).toEqual([{ name: 'A 的文件' }]);

    expect(tabA.persist()).toBe(true);
    const verifier = createComposerDraftStore(
      'same-field-unrelated-edit',
      sharedStorage(sharedValues),
    );
    expect(verifier.getInputDraft('new-task')).toBe('B 的输入');
    expect(verifier.getAttachmentDraft('new-task')).toEqual([{ name: 'A 的文件' }]);
  });

  test('orders an unlocked deletion by its mutation version, not persist time', () => {
    const sharedValues = new Map();
    const seed = createComposerDraftStore('delete-version-order', sharedStorage(sharedValues));
    writeComposerInputDraft(seed, 'new-task', '基线');
    expect(seed.persist()).toBe(true);

    const stale = createComposerDraftStore('delete-version-order', sharedStorage(sharedValues));
    const fresh = createComposerDraftStore('delete-version-order', sharedStorage(sharedValues));
    writeComposerInputDraft(stale, 'new-task', '');
    writeComposerInputDraft(fresh, 'new-task', '新上下文文字');
    expect(fresh.persist()).toBe(true);
    expect(stale.persist()).toBe(true);

    const verifier = createComposerDraftStore(
      'delete-version-order',
      sharedStorage(sharedValues),
    );
    expect(verifier.getInputDraft('new-task')).toBe('新上下文文字');
  });

  test('keeps an attachment added by an interleaved no-lock writer when text clears', () => {
    const sharedValues = new Map();
    const stateKey = 'catsco_composer_draft_state:v1:clear-frontier-merge';
    const seed = createComposerDraftStore('clear-frontier-merge', sharedStorage(sharedValues));
    writeComposerInputDraft(seed, 'new-task', '待清空文字');
    expect(seed.persist()).toBe(true);

    const tabBStorage = sharedStorage(sharedValues);
    const tabB = createComposerDraftStore('clear-frontier-merge', tabBStorage);
    let interleaved = true;
    const tabAStorage = sharedStorage(sharedValues, {
      onSetItem(key) {
        if (interleaved && key === stateKey) {
          interleaved = false;
          expect(tabB.persist()).toBe(true);
        }
      },
    });
    const tabA = createComposerDraftStore('clear-frontier-merge', tabAStorage);
    writeComposerInputDraft(tabA, 'new-task', '');
    writeComposerAttachmentDraft(tabB, 'new-task', [{ name: '并发文件' }]);

    expect(tabA.persist()).toBe(true);
    const restored = createComposerDraftStore('clear-frontier-merge', sharedStorage(sharedValues));
    expect(restored.getInputDraft('new-task')).toBe('');
    expect(restored.getAttachmentDraft('new-task')).toEqual([{ name: '并发文件' }]);
  });

  test('does not revive a stale field marker that was delayed across a clear', () => {
    const sharedValues = new Map();
    const stateKey = 'catsco_composer_draft_state:v1:delayed-clear-marker';
    const seed = createComposerDraftStore('delayed-clear-marker', sharedStorage(sharedValues));
    writeComposerInputDraft(seed, 'new-task', '原始文字');
    expect(seed.persist()).toBe(true);

    const remover = createComposerDraftStore('delayed-clear-marker', sharedStorage(sharedValues));
    let clearDuringRead = false;
    const staleStorage = sharedStorage(sharedValues, {
      onGetItem(key) {
        if (clearDuringRead && key === stateKey) {
          clearDuringRead = false;
          writeComposerInputDraft(remover, 'new-task', '');
          expect(remover.persist()).toBe(true);
        }
      },
    });
    const stale = createComposerDraftStore('delayed-clear-marker', staleStorage);
    writeComposerInputDraft(stale, 'new-task', '旧 tab 的迟到文字');
    clearDuringRead = true;
    const staleResult = stale.persist();
    expect(staleResult).toBe(false);

    const restored = createComposerDraftStore(
      'delayed-clear-marker',
      sharedStorage(sharedValues),
    );
    expect(restored.getInputDraft('new-task')).toBe('');
  });

  test('keeps another local draft dirty while a send marker clears its sibling', () => {
    const webLocksDescriptor = Object.getOwnPropertyDescriptor(globalThis.navigator, 'locks');
    const indexedDBDescriptor = Object.getOwnPropertyDescriptor(globalThis, 'indexedDB');
    const storageTagDescriptor = Object.getOwnPropertyDescriptor(
      globalThis.localStorage,
      Symbol.toStringTag,
    );
    Object.defineProperty(globalThis.navigator, 'locks', { configurable: true, value: undefined });
    Object.defineProperty(globalThis, 'indexedDB', { configurable: true, value: undefined });
    Object.defineProperty(globalThis.localStorage, Symbol.toStringTag, {
      configurable: true,
      value: 'Storage',
    });
    try {
      const store = createComposerDraftStore('marker-sibling');
      writeComposerInputDraft(store, 'new-task', '即将发送');
      writeComposerInputDraft(store, 'p2p_1_2', '旧的另一份草稿');
      expect(store.persist()).toBe(true);
      const sentVersion = readComposerDraftVersion(store, 'new-task');

      writeComposerInputDraft(store, 'p2p_1_2', '尚未落盘的另一份草稿');
      expect(clearComposerDraftIfVersion(store, 'new-task', sentVersion)).toBe(true);
      expect(store.getInputDraft('p2p_1_2')).toBe('尚未落盘的另一份草稿');
      expect(store.persist()).toBe(true);

      const restored = createComposerDraftStore('marker-sibling');
      expect(restored.getInputDraft('new-task')).toBe('');
      expect(restored.getInputDraft('p2p_1_2')).toBe('尚未落盘的另一份草稿');
    } finally {
      if (webLocksDescriptor) Object.defineProperty(globalThis.navigator, 'locks', webLocksDescriptor);
      else delete globalThis.navigator.locks;
      if (indexedDBDescriptor) Object.defineProperty(globalThis, 'indexedDB', indexedDBDescriptor);
      else delete globalThis.indexedDB;
      if (storageTagDescriptor) {
        Object.defineProperty(globalThis.localStorage, Symbol.toStringTag, storageTagDescriptor);
      } else {
        delete globalThis.localStorage[Symbol.toStringTag];
      }
    }
  });

  test('does not let a stale attachment callback revive a marker-cleared draft', () => {
    const webLocksDescriptor = Object.getOwnPropertyDescriptor(globalThis.navigator, 'locks');
    const indexedDBDescriptor = Object.getOwnPropertyDescriptor(globalThis, 'indexedDB');
    const storageTagDescriptor = Object.getOwnPropertyDescriptor(
      globalThis.localStorage,
      Symbol.toStringTag,
    );
    Object.defineProperty(globalThis.navigator, 'locks', { configurable: true, value: undefined });
    Object.defineProperty(globalThis, 'indexedDB', { configurable: true, value: undefined });
    Object.defineProperty(globalThis.localStorage, Symbol.toStringTag, {
      configurable: true,
      value: 'Storage',
    });
    try {
      const stale = createComposerDraftStore('stale-marker-upload');
      writeComposerInputDraft(stale, 'new-task', '发送中的文字');
      expect(stale.persist()).toBe(true);
      const sentVersion = readComposerDraftVersion(stale, 'new-task');

      const sender = createComposerDraftStore('stale-marker-upload');
      expect(clearComposerDraftIfVersion(sender, 'new-task', sentVersion)).toBe(true);
      expect(appendComposerDraftAttachments(
        stale,
        'new-task',
        [{ name: 'late.pdf', type: 'file' }],
      )).toBeNull();

      const restored = createComposerDraftStore('stale-marker-upload');
      expect(restored.getInputDraft('new-task')).toBe('');
      expect(restored.getAttachmentDraft('new-task')).toEqual([]);
    } finally {
      if (webLocksDescriptor) Object.defineProperty(globalThis.navigator, 'locks', webLocksDescriptor);
      else delete globalThis.navigator.locks;
      if (indexedDBDescriptor) Object.defineProperty(globalThis, 'indexedDB', indexedDBDescriptor);
      else delete globalThis.indexedDB;
      if (storageTagDescriptor) {
        Object.defineProperty(globalThis.localStorage, Symbol.toStringTag, storageTagDescriptor);
      } else {
        delete globalThis.localStorage[Symbol.toStringTag];
      }
    }
  });

  test('writes a synchronous session copy before a browser lock is granted', () => {
    const webLocksDescriptor = Object.getOwnPropertyDescriptor(globalThis.navigator, 'locks');
    Object.defineProperty(globalThis.navigator, 'locks', {
      configurable: true,
      value: { request: () => new Promise(() => {}) },
    });
    try {
      const store = createComposerDraftStore('pending-browser-lock');
      writeComposerInputDraft(store, 'new-task', '切换前最后输入的内容');
      void store.persist();

      expect(sessionStorage.getItem('catsco_composer_draft_fast:v1:pending-browser-lock'))
        .toContain('切换前最后输入的内容');

      store.close();
      expect(createComposerDraftStore('pending-browser-lock').getInputDraft('new-task'))
        .toBe('切换前最后输入的内容');
    } finally {
      if (webLocksDescriptor) Object.defineProperty(globalThis.navigator, 'locks', webLocksDescriptor);
      else delete globalThis.navigator.locks;
    }
  });

  test('merges a queued text fast copy with a newer remote attachment deletion', () => {
    const key = `${COMPOSER_DRAFT_STORAGE_PREFIX}fast-field-merge`;
    const store = createComposerDraftStore('fast-field-merge');
    writeComposerInputDraft(store, 'new-task', '原始文字');
    writeComposerAttachmentDraft(store, 'new-task', [{ name: 'obsolete.pdf', type: 'file' }]);
    expect(store.persist()).toBe(true);

    const webLocksDescriptor = Object.getOwnPropertyDescriptor(globalThis.navigator, 'locks');
    Object.defineProperty(globalThis.navigator, 'locks', {
      configurable: true,
      value: { request: () => new Promise(() => {}) },
    });
    try {
      writeComposerInputDraft(store, 'new-task', '排队中的新文字');
      void store.persist();

      const durable = JSON.parse(localStorage.getItem(key));
      localStorage.setItem(key, JSON.stringify({
        ...durable,
        attachmentDrafts: [],
        draftVersions: [['new-task', 'remote-attachment-deletion']],
        updatedAt: Math.max(Number(durable.updatedAt) || 0, Date.now()) + 100,
      }));

      store.close();
      const restored = createComposerDraftStore('fast-field-merge');
      expect(restored.getInputDraft('new-task')).toBe('排队中的新文字');
      expect(restored.getAttachmentDraft('new-task')).toEqual([]);
    } finally {
      if (webLocksDescriptor) Object.defineProperty(globalThis.navigator, 'locks', webLocksDescriptor);
      else delete globalThis.navigator.locks;
    }
  });

  test('captures a queued attachment append in the session fast path', () => {
    const store = createComposerDraftStore('pending-attachment-lock');
    writeComposerInputDraft(store, 'new-task', '带附件的草稿');
    writeComposerAttachmentDraft(store, 'new-task', [{ name: 'first.pdf', type: 'file' }]);
    expect(store.persist()).toBe(true);

    const webLocksDescriptor = Object.getOwnPropertyDescriptor(globalThis.navigator, 'locks');
    Object.defineProperty(globalThis.navigator, 'locks', {
      configurable: true,
      value: { request: () => new Promise(() => {}) },
    });
    try {
      void appendComposerDraftAttachments(
        store,
        'new-task',
        [{ name: 'queued.pdf', type: 'file' }],
      );
      const fast = JSON.parse(sessionStorage.getItem(
        'catsco_composer_draft_fast:v1:pending-attachment-lock',
      ));
      expect(fast.pendingAttachmentMutations).toEqual([
        expect.objectContaining({ type: 'append', key: 'new-task' }),
      ]);

      store.close();
      const restored = createComposerDraftStore('pending-attachment-lock');
      expect(restored.getAttachmentDraft('new-task')).toEqual([
        { name: 'first.pdf', type: 'file' },
        { name: 'queued.pdf', type: 'file' },
      ]);
    } finally {
      if (webLocksDescriptor) Object.defineProperty(globalThis.navigator, 'locks', webLocksDescriptor);
      else delete globalThis.navigator.locks;
    }
  });

  test('rebases a queued attachment append without restoring a remote deletion', () => {
    const key = `${COMPOSER_DRAFT_STORAGE_PREFIX}pending-attachment-rebase`;
    const store = createComposerDraftStore('pending-attachment-rebase');
    writeComposerAttachmentDraft(store, 'new-task', [{ name: 'obsolete.pdf', type: 'file' }]);
    expect(store.persist()).toBe(true);

    const webLocksDescriptor = Object.getOwnPropertyDescriptor(globalThis.navigator, 'locks');
    Object.defineProperty(globalThis.navigator, 'locks', {
      configurable: true,
      value: { request: () => new Promise(() => {}) },
    });
    try {
      void appendComposerDraftAttachments(
        store,
        'new-task',
        [{ name: 'queued.pdf', type: 'file' }],
      );
      const durable = JSON.parse(localStorage.getItem(key));
      localStorage.setItem(key, JSON.stringify({
        ...durable,
        attachmentDrafts: [],
        draftVersions: [['new-task', 'remote-deletion-version']],
        updatedAt: Math.max(Number(durable.updatedAt) || 0, Date.now()) + 100,
      }));

      store.close();
      const restored = createComposerDraftStore('pending-attachment-rebase');
      expect(restored.getAttachmentDraft('new-task')).toEqual([
        { name: 'queued.pdf', type: 'file' },
      ]);
    } finally {
      if (webLocksDescriptor) Object.defineProperty(globalThis.navigator, 'locks', webLocksDescriptor);
      else delete globalThis.navigator.locks;
    }
  });

  test('replays a queued attachment deletion across a remote text-only edit', () => {
    const key = `${COMPOSER_DRAFT_STORAGE_PREFIX}pending-removal-text-edit`;
    const store = createComposerDraftStore('pending-removal-text-edit');
    writeComposerInputDraft(store, 'new-task', '原始文字');
    writeComposerAttachmentDraft(store, 'new-task', [{ name: 'delete-me.pdf', type: 'file' }]);
    expect(store.persist()).toBe(true);

    const webLocksDescriptor = Object.getOwnPropertyDescriptor(globalThis.navigator, 'locks');
    Object.defineProperty(globalThis.navigator, 'locks', {
      configurable: true,
      value: { request: () => new Promise(() => {}) },
    });
    try {
      void removeComposerDraftAttachment(
        store,
        'new-task',
        { name: 'delete-me.pdf', type: 'file' },
      );
      const durable = JSON.parse(localStorage.getItem(key));
      localStorage.setItem(key, JSON.stringify({
        ...durable,
        inputDrafts: [['new-task', '另一上下文的新文字']],
        draftVersions: [['new-task', 'remote-text-version']],
        updatedAt: Math.max(Number(durable.updatedAt) || 0, Date.now()) + 100,
      }));

      store.close();
      const restored = createComposerDraftStore('pending-removal-text-edit');
      expect(restored.getInputDraft('new-task')).toBe('另一上下文的新文字');
      expect(restored.getAttachmentDraft('new-task')).toEqual([]);
    } finally {
      if (webLocksDescriptor) Object.defineProperty(globalThis.navigator, 'locks', webLocksDescriptor);
      else delete globalThis.navigator.locks;
    }
  });

  test('replays a queued attachment deletion when another context appends a file', () => {
    const key = `${COMPOSER_DRAFT_STORAGE_PREFIX}pending-removal-remote-append`;
    const store = createComposerDraftStore('pending-removal-remote-append');
    const deletedAttachment = {
      type: 'file',
      name: 'delete-me.pdf',
      content: { type: 'file', payload: { file_key: 'delete-me' } },
    };
    const addedElsewhere = {
      type: 'file',
      name: 'keep-me.pdf',
      content: { type: 'file', payload: { file_key: 'keep-me' } },
    };
    writeComposerAttachmentDraft(store, 'new-task', [deletedAttachment]);
    writeComposerPhoneUploadSession(store, 'new-task', { session_id: 'phone-upload' });
    expect(store.persist()).toBe(true);

    const webLocksDescriptor = Object.getOwnPropertyDescriptor(globalThis.navigator, 'locks');
    Object.defineProperty(globalThis.navigator, 'locks', {
      configurable: true,
      value: { request: () => new Promise(() => {}) },
    });
    try {
      void removeComposerDraftAttachment(
        store,
        'new-task',
        deletedAttachment,
        { expectedPhoneUploadSessionId: 'phone-upload' },
      );
      const durable = JSON.parse(localStorage.getItem(key));
      localStorage.setItem(key, JSON.stringify({
        ...durable,
        attachmentDrafts: [['new-task', [deletedAttachment, addedElsewhere]]],
        draftVersions: [['new-task', 'remote-append-version']],
        updatedAt: Math.max(Number(durable.updatedAt) || 0, Date.now()) + 100,
      }));

      store.close();
      const restored = createComposerDraftStore('pending-removal-remote-append');
      expect(restored.getAttachmentDraft('new-task')).toEqual([addedElsewhere]);
      expect(readComposerPhoneUploadSession(restored, 'new-task')).toEqual({
        session_id: 'phone-upload',
        removed_file_keys: ['delete-me'],
      });
    } finally {
      if (webLocksDescriptor) Object.defineProperty(globalThis.navigator, 'locks', webLocksDescriptor);
      else delete globalThis.navigator.locks;
    }
  });

  test('does not record a duplicate append that could revive a deleted attachment', () => {
    const key = `${COMPOSER_DRAFT_STORAGE_PREFIX}duplicate-append`;
    const store = createComposerDraftStore('duplicate-append');
    const attachment = { name: 'already-present.pdf', type: 'file' };
    writeComposerAttachmentDraft(store, 'new-task', [attachment]);
    expect(store.persist()).toBe(true);

    const webLocksDescriptor = Object.getOwnPropertyDescriptor(globalThis.navigator, 'locks');
    Object.defineProperty(globalThis.navigator, 'locks', {
      configurable: true,
      value: { request: () => new Promise(() => {}) },
    });
    try {
      void appendComposerDraftAttachments(store, 'new-task', [attachment]);
      expect(sessionStorage.getItem('catsco_composer_draft_fast:v1:duplicate-append')).toBeNull();

      const durable = JSON.parse(localStorage.getItem(key));
      localStorage.setItem(key, JSON.stringify({
        ...durable,
        attachmentDrafts: [],
        draftVersions: [['new-task', 'remote-deletion-version']],
        updatedAt: Math.max(Number(durable.updatedAt) || 0, Date.now()) + 100,
      }));
      store.close();
      expect(createComposerDraftStore('duplicate-append').getAttachmentDraft('new-task')).toEqual([]);
    } finally {
      if (webLocksDescriptor) Object.defineProperty(globalThis.navigator, 'locks', webLocksDescriptor);
      else delete globalThis.navigator.locks;
    }
  });

  test('keeps a queued attachment when its text draft has not reached durable storage', () => {
    const webLocksDescriptor = Object.getOwnPropertyDescriptor(globalThis.navigator, 'locks');
    Object.defineProperty(globalThis.navigator, 'locks', {
      configurable: true,
      value: { request: () => new Promise(() => {}) },
    });
    try {
      const store = createComposerDraftStore('pending-new-draft-attachment');
      writeComposerInputDraft(store, 'new-task', '尚未落盘的新对话文字');
      void store.persist();
      void appendComposerDraftAttachments(
        store,
        'new-task',
        [{ name: 'queued-new.pdf', type: 'file' }],
      );

      store.close();
      const restored = createComposerDraftStore('pending-new-draft-attachment');
      expect(restored.getInputDraft('new-task')).toBe('尚未落盘的新对话文字');
      expect(restored.getAttachmentDraft('new-task')).toEqual([
        { name: 'queued-new.pdf', type: 'file' },
      ]);
    } finally {
      if (webLocksDescriptor) Object.defineProperty(globalThis.navigator, 'locks', webLocksDescriptor);
      else delete globalThis.navigator.locks;
    }
  });

  test('replays a later queued attachment mutation after an earlier one commits', async () => {
    const store = createComposerDraftStore('queued-attachment-chain');
    writeComposerAttachmentDraft(store, 'new-task', [
      { name: 'first.pdf', type: 'file' },
      { name: 'second.pdf', type: 'file' },
    ]);
    expect(store.persist()).toBe(true);

    const webLocksDescriptor = Object.getOwnPropertyDescriptor(globalThis.navigator, 'locks');
    const callbacks = [];
    Object.defineProperty(globalThis.navigator, 'locks', {
      configurable: true,
      value: {
        request(_name, _options, callback) {
          return new Promise((resolve, reject) => {
            callbacks.push(() => {
              try {
                resolve(callback());
              } catch (error) {
                reject(error);
              }
            });
          });
        },
      },
    });
    try {
      const first = removeComposerDraftAttachment(
        store,
        'new-task',
        { name: 'first.pdf', type: 'file' },
      );
      const second = removeComposerDraftAttachment(
        store,
        'new-task',
        { name: 'second.pdf', type: 'file' },
      );
      expect(callbacks).toHaveLength(2);

      callbacks[0]();
      await expect(first).resolves.toMatchObject({ removed: true });
      store.close();

      const restored = createComposerDraftStore('queued-attachment-chain');
      expect(restored.getAttachmentDraft('new-task')).toEqual([]);
      void second;
    } finally {
      if (webLocksDescriptor) Object.defineProperty(globalThis.navigator, 'locks', webLocksDescriptor);
      else delete globalThis.navigator.locks;
    }
  });

  test('does not let a queued X overtake a later explicit re-add', async () => {
    const attachment = {
      type: 'file',
      name: 'queued-readd.pdf',
      content: { type: 'file', payload: { file_key: 'queued-readd' } },
    };
    const store = createComposerDraftStore('queued-remove-readd');
    writeComposerAttachmentDraft(store, 'new-task', [attachment]);
    expect(store.persist()).toBe(true);

    const webLocksDescriptor = Object.getOwnPropertyDescriptor(globalThis.navigator, 'locks');
    const callbacks = [];
    Object.defineProperty(globalThis.navigator, 'locks', {
      configurable: true,
      value: {
        request(_name, _options, callback) {
          return new Promise((resolve, reject) => {
            callbacks.push(() => {
              try {
                resolve(callback());
              } catch (error) {
                reject(error);
              }
            });
          });
        },
      },
    });
    try {
      const removal = removeComposerDraftAttachment(store, 'new-task', attachment);
      expect(store.getAttachmentDraft('new-task')).toEqual([]);

      const readdTokens = captureComposerDraftAttachmentRestoreTokens(store, 'new-task');
      expect(readdTokens).toHaveLength(1);
      const readd = appendComposerDraftAttachments(
        store,
        'new-task',
        [attachment],
        { attachmentRestoreTokens: readdTokens },
      );
      expect(callbacks).toHaveLength(2);

      callbacks[0]();
      await expect(removal).resolves.toBeNull();
      callbacks[1]();
      await expect(readd).resolves.toMatchObject({ attachments: [attachment] });
      expect(store.getAttachmentDraft('new-task')).toEqual([attachment]);
    } finally {
      if (webLocksDescriptor) Object.defineProperty(globalThis.navigator, 'locks', webLocksDescriptor);
      else delete globalThis.navigator.locks;
    }
  });

  test('serializes a late send cleanup behind a fresh context write', async () => {
    const restoreLocks = installSerializedWebLocks();
    try {
      const tabA = createComposerDraftStore('42');
      writeComposerInputDraft(tabA, 'new-task', '发送中的旧草稿');
      await tabA.persist();
      const sentVersion = readComposerDraftVersion(tabA, 'new-task');

      const tabB = createComposerDraftStore('42');
      writeComposerInputDraft(tabB, 'new-task', '新上下文的草稿');

      // Both operations start together. The old send owns the lock first;
      // the new writer must run afterwards and revive its own fresh draft.
      const cleanup = clearComposerDraftIfVersion(tabA, 'new-task', sentVersion);
      const freshWrite = tabB.persist();

      await expect(cleanup).resolves.toBe(true);
      await expect(freshWrite).resolves.toBe(true);
      expect(createComposerDraftStore('42').getInputDraft('new-task'))
        .toBe('新上下文的草稿');
    } finally {
      restoreLocks();
    }
  });

  test('uses IndexedDB locking when Web Locks is unavailable', async () => {
    const restoreIndexedDB = installSerializedIndexedDBLocks();
    const webLocksDescriptor = Object.getOwnPropertyDescriptor(globalThis.navigator, 'locks');
    Object.defineProperty(globalThis.navigator, 'locks', {
      configurable: true,
      value: undefined,
    });
    try {
      const tabA = createComposerDraftStore('indexeddb-lock');
      writeComposerInputDraft(tabA, 'new-task', '发送中的旧草稿');
      await tabA.persist();
      const sentVersion = readComposerDraftVersion(tabA, 'new-task');

      const tabB = createComposerDraftStore('indexeddb-lock');
      writeComposerInputDraft(tabB, 'new-task', '新上下文的草稿');

      const cleanup = clearComposerDraftIfVersion(tabA, 'new-task', sentVersion);
      const freshWrite = tabB.persist();

      await expect(cleanup).resolves.toBe(true);
      await expect(freshWrite).resolves.toBe(true);
      expect(createComposerDraftStore('indexeddb-lock').getInputDraft('new-task'))
        .toBe('新上下文的草稿');
    } finally {
      if (webLocksDescriptor) Object.defineProperty(globalThis.navigator, 'locks', webLocksDescriptor);
      else delete globalThis.navigator.locks;
      restoreIndexedDB();
    }
  });

  test('uses the send marker when an IndexedDB lock transaction cannot start', async () => {
    const indexedDBDescriptor = Object.getOwnPropertyDescriptor(globalThis, 'indexedDB');
    const webLocksDescriptor = Object.getOwnPropertyDescriptor(globalThis.navigator, 'locks');
    const storageTagDescriptor = Object.getOwnPropertyDescriptor(
      globalThis.localStorage,
      Symbol.toStringTag,
    );
    const database = {
      transaction() {
        const transaction = {
          objectStore() {
            return {
              put() {
                const request = {};
                queueMicrotask(() => request.onerror?.({ target: request }));
                return request;
              },
            };
          },
        };
        return transaction;
      },
    };
    Object.defineProperty(globalThis.navigator, 'locks', { configurable: true, value: undefined });
    Object.defineProperty(globalThis, 'indexedDB', {
      configurable: true,
      value: {
        open() {
          const request = {};
          queueMicrotask(() => {
            request.result = database;
            request.onsuccess?.({ target: request });
          });
          return request;
        },
      },
    });
    Object.defineProperty(globalThis.localStorage, Symbol.toStringTag, {
      configurable: true,
      value: 'Storage',
    });
    try {
      const store = createComposerDraftStore('failed-indexeddb-lock');
      writeComposerInputDraft(store, 'new-task', '已经发送');
      await expect(store.persist()).resolves.toBe(true);
      const sentVersion = readComposerDraftVersion(store, 'new-task');

      await expect(clearComposerDraftIfVersion(store, 'new-task', sentVersion)).resolves.toBe(true);
      expect(createComposerDraftStore('failed-indexeddb-lock').getInputDraft('new-task')).toBe('');
    } finally {
      if (indexedDBDescriptor) Object.defineProperty(globalThis, 'indexedDB', indexedDBDescriptor);
      else delete globalThis.indexedDB;
      if (webLocksDescriptor) Object.defineProperty(globalThis.navigator, 'locks', webLocksDescriptor);
      else delete globalThis.navigator.locks;
      if (storageTagDescriptor) {
        Object.defineProperty(globalThis.localStorage, Symbol.toStringTag, storageTagDescriptor);
      } else {
        delete globalThis.localStorage[Symbol.toStringTag];
      }
    }
  });

  test('keeps a dirty fresh context when it receives an older send tombstone', () => {
    const key = `${COMPOSER_DRAFT_STORAGE_PREFIX}42`;
    const tabA = createComposerDraftStore('42');
    writeComposerInputDraft(tabA, 'new-task', '发送中的旧草稿');
    tabA.persist();
    const sentVersion = readComposerDraftVersion(tabA, 'new-task');

    const freshContext = createComposerDraftStore('42');
    writeComposerAttachmentDraft(freshContext, 'new-task', [{ name: 'new-context.pdf', type: 'file' }]);

    expect(clearComposerDraftIfVersion(tabA, 'new-task', sentVersion)).toBe(true);

    const storageEvent = new Event('storage');
    Object.defineProperties(storageEvent, {
      key: { value: key },
      newValue: { value: localStorage.getItem(key) },
      storageArea: { value: localStorage },
    });
    window.dispatchEvent(storageEvent);

    expect(freshContext.getAttachmentDraft('new-task'))
      .toEqual([{ name: 'new-context.pdf', type: 'file' }]);
    freshContext.persist();
    expect(createComposerDraftStore('42').getAttachmentDraft('new-task'))
      .toEqual([{ name: 'new-context.pdf', type: 'file' }]);
  });

  test('keeps a dirty fresh context when a normal remote snapshot arrives', () => {
    const key = `${COMPOSER_DRAFT_STORAGE_PREFIX}normal-snapshot`;
    const source = createComposerDraftStore('normal-snapshot');
    writeComposerInputDraft(source, 'new-task', '旧草稿');
    source.persist();

    const freshContext = createComposerDraftStore('normal-snapshot');
    writeComposerInputDraft(freshContext, 'new-task', '尚未落盘的新输入');

    writeComposerAttachmentDraft(source, 'new-task', [{ name: 'late.pdf', type: 'file' }]);
    source.persist();
    const storageEvent = new Event('storage');
    Object.defineProperties(storageEvent, {
      key: { value: key },
      newValue: { value: localStorage.getItem(key) },
      storageArea: { value: localStorage },
    });
    window.dispatchEvent(storageEvent);

    expect(freshContext.getInputDraft('new-task')).toBe('尚未落盘的新输入');
    // The remote attachment is untouched locally, so rebasing the storage
    // event should hide it immediately even while the text edit remains dirty.
    expect(freshContext.getAttachmentDraft('new-task')).toEqual([{
      name: 'late.pdf',
      type: 'file',
    }]);
    freshContext.persist();
    const verifier = createComposerDraftStore('normal-snapshot');
    expect(verifier.getInputDraft('new-task')).toBe('尚未落盘的新输入');
    expect(verifier.getAttachmentDraft('new-task')).toEqual([{ name: 'late.pdf', type: 'file' }]);
  });

  test('keeps a removed phone upload deleted while preserving another late attachment', () => {
    const source = createComposerDraftStore('phone-removal');
    const session = { session_id: 'phone-session' };
    writeComposerPhoneUploadSession(source, 'new-task', session);
    writeComposerAttachmentDraft(source, 'new-task', [{
      type: 'file',
      name: 'phone.pdf',
      content: { type: 'file', payload: { file_key: 'phone.pdf' } },
    }]);
    source.persist();

    const freshContext = createComposerDraftStore('phone-removal');
    const removed = removeComposerDraftAttachment(
      freshContext,
      'new-task',
      { content: { payload: { file_key: 'phone.pdf' } } },
      { expectedPhoneUploadSessionId: 'phone-session' },
    );
    expect(removed?.removed).toBe(true);

    const lateManualUpload = appendComposerDraftAttachments(
      source,
      'new-task',
      [{
        type: 'file',
        name: 'late.pdf',
        content: { type: 'file', payload: { file_key: 'late.pdf' } },
      }],
    );
    expect(lateManualUpload?.appended).toHaveLength(1);

    const appended = appendComposerDraftAttachments(
      source,
      'new-task',
      [{
        type: 'file',
        name: 'phone.pdf',
        content: { type: 'file', payload: { file_key: 'phone.pdf' } },
      }],
      { expectedPhoneUploadSessionId: 'phone-session' },
    );
    expect(appended?.appended).toEqual([]);

    const verifier = createComposerDraftStore('phone-removal');
    expect(verifier.getAttachmentDraft('new-task')).toEqual([
      expect.objectContaining({ name: 'late.pdf' }),
    ]);
    expect(readComposerPhoneUploadSession(verifier, 'new-task')).toMatchObject({
      removed_file_keys: ['phone.pdf'],
    });
  });

  test('orders concurrent same-millisecond writes through the shared lock', async () => {
    const restoreLocks = installSerializedWebLocks();
    const clock = vi.spyOn(Date, 'now').mockReturnValue(1_000);
    try {
      const tabA = createComposerDraftStore('42');
      const tabB = createComposerDraftStore('42');
      writeComposerInputDraft(tabA, 'new-task', '第一个上下文');
      writeComposerInputDraft(tabB, 'p2p_1_2', '第二个上下文');

      await Promise.all([tabA.persist(), tabB.persist()]);

      const stored = JSON.parse(sessionStorage.getItem(`${COMPOSER_DRAFT_STORAGE_PREFIX}42`));
      expect(stored.updatedAt).toBe(1_001);
      expect(stored.inputDrafts).toEqual([
        ['new-task', '第一个上下文'],
        ['p2p_1_2', '第二个上下文'],
      ]);
    } finally {
      clock.mockRestore();
      restoreLocks();
    }
  });

  test('carries a queued write through a remount before the lock runs', async () => {
    const restoreLocks = installSerializedWebLocks();
    try {
      const source = createComposerDraftStore('42');
      writeComposerInputDraft(source, 'new-task', '切换前尚未落盘');
      const queuedWrite = source.persist();

      source.deactivate();
      const replacement = createComposerDraftStore('42');

      await expect(queuedWrite).resolves.toBe(true);
      expect(replacement.getInputDraft('new-task')).toBe('切换前尚未落盘');
      expect(createComposerDraftStore('42').getInputDraft('new-task')).toBe('切换前尚未落盘');
    } finally {
      restoreLocks();
    }
  });

  test('carries an equal-valued fresh draft version through an in-process handoff', () => {
    const source = createComposerDraftStore('handoff-version');
    writeComposerInputDraft(source, 'new-task', '内容相同');
    expect(source.persist()).toBe(true);
    const sentVersion = readComposerDraftVersion(source, 'new-task');

    // The value is identical, but this is a new draft made while the old send
    // is in flight. The handoff must retain its new logical version.
    writeComposerInputDraft(source, 'new-task', '内容相同');
    source.deactivate();
    const replacement = createComposerDraftStore('handoff-version');

    expect(readComposerDraftVersion(replacement, 'new-task')).not.toBe(sentVersion);
    expect(clearComposerDraftIfVersion(source, 'new-task', sentVersion)).toBe(false);
    expect(replacement.getInputDraft('new-task')).toBe('内容相同');
  });

  test('notifies an already-open fresh context when the mirrored draft changes', () => {
    const key = `${COMPOSER_DRAFT_STORAGE_PREFIX}42`;
    const source = createComposerDraftStore('42');
    writeComposerInputDraft(source, 'new-task', '上传尚未完成');
    source.persist();

    const freshContext = createComposerDraftStore('42');
    const changes = [];
    const unsubscribe = subscribeComposerDraftStore(freshContext, (change) => changes.push(change));

    writeComposerAttachmentDraft(source, 'new-task', [{ name: 'late.pdf', type: 'file' }]);
    source.persist();

    const storageEvent = new Event('storage');
    Object.defineProperties(storageEvent, {
      key: { value: key },
      newValue: { value: localStorage.getItem(key) },
      storageArea: { value: localStorage },
    });
    window.dispatchEvent(storageEvent);

    expect(freshContext.getAttachmentDraft('new-task'))
      .toEqual([{ name: 'late.pdf', type: 'file' }]);
    expect(changes).toContainEqual(expect.objectContaining({ key: 'new-task' }));

    unsubscribe();
    source.close();
    freshContext.close();
  });

  test('fences a stale context after logout clears the shared storage', () => {
    const sharedValues = new Map();
    const tabAStorage = sharedStorage(sharedValues);
    const tabBStorage = sharedStorage(sharedValues);

    const staleStore = createComposerDraftStore('42', tabBStorage);
    writeComposerInputDraft(staleStore, 'new-task', '旧 tab 草稿');
    staleStore.persist();

    expect(clearPersistedComposerDrafts(tabAStorage)).toBe(1);
    staleStore.persist();
    writeComposerInputDraft(staleStore, 'new-task', '登出后不应复活');
    staleStore.persist();

    expect(tabAStorage.getItem(`${COMPOSER_DRAFT_STORAGE_PREFIX}42`)).toBeNull();
    expect(createComposerDraftStore('42', tabBStorage).getInputDraft('new-task')).toBe('');
  });

  test('fences an unsnapshotted browsing context after logout', () => {
    const sharedValues = new Map();
    const tabAStorage = sharedStorage(sharedValues);
    const tabBStorage = sharedStorage(sharedValues);
    const staleStore = createComposerDraftStore('42', tabBStorage);

    expect(clearPersistedComposerDrafts(tabAStorage)).toBe(0);
    writeComposerInputDraft(staleStore, 'new-task', '登出前尚未落盘');
    staleStore.persist();

    expect(tabAStorage.getItem(`${COMPOSER_DRAFT_STORAGE_PREFIX}42`)).toBeNull();
  });

  test('allows a newly created store to write after a logout fence', () => {
    const sharedValues = new Map();
    const tabAStorage = sharedStorage(sharedValues);
    const tabBStorage = sharedStorage(sharedValues);

    clearPersistedComposerDrafts(tabAStorage);
    const freshStore = createComposerDraftStore('42', tabBStorage);
    writeComposerInputDraft(freshStore, 'new-task', '重新登录后的新草稿');
    freshStore.persist();

    expect(JSON.parse(tabAStorage.getItem(`${COMPOSER_DRAFT_STORAGE_PREFIX}42`)).inputDrafts)
      .toEqual([['new-task', '重新登录后的新草稿']]);
  });

  test('does not restore a draft written after logout began while persist was in flight', () => {
    const sharedValues = new Map();
    const tabAStorage = sharedStorage(sharedValues);
    const stateKey = `catsco_composer_draft_state:v1:42`;
    let logoutDuringRead = false;
    const tabBStorage = sharedStorage(sharedValues, {
      onGetItem(key) {
        if (logoutDuringRead && key === stateKey) {
          logoutDuringRead = false;
          clearPersistedComposerDrafts(tabAStorage);
        }
      },
    });
    const staleStore = createComposerDraftStore('42', tabBStorage);
    writeComposerInputDraft(staleStore, 'new-task', '登出前的进行中写入');
    staleStore.persist();

    logoutDuringRead = true;
    staleStore.persist();

    // The logout fence is checked before the compatibility snapshot write, so
    // an in-flight stale writer cannot leave an inaccessible physical copy
    // behind either.
    expect(tabAStorage.getItem(`${COMPOSER_DRAFT_STORAGE_PREFIX}42`)).toBeNull();
    expect(createComposerDraftStore('42', tabAStorage).getInputDraft('new-task')).toBe('');
  });

  test('does not treat a normal draft clear as a logout fence', () => {
    const storage = sharedStorage();
    const store = createComposerDraftStore('42', storage);

    writeComposerInputDraft(store, 'new-task', '发送前草稿');
    store.persist();
    writeComposerInputDraft(store, 'new-task', '');
    store.persist();
    writeComposerInputDraft(store, 'new-task', '清空后重新输入');
    store.persist();

    expect(JSON.parse(storage.getItem(`${COMPOSER_DRAFT_STORAGE_PREFIX}42`)).inputDrafts)
      .toEqual([['new-task', '清空后重新输入']]);
  });

  test('clears both draft storage copies on logout', () => {
    const store = createComposerDraftStore('42');
    store.setInputDraft('new-task', '退出后应清除');
    store.persist();

    expect(clearPersistedComposerDrafts()).toBe(1);
    expect(sessionStorage.getItem(`${COMPOSER_DRAFT_STORAGE_PREFIX}42`)).toBeNull();
    expect(localStorage.getItem(`${COMPOSER_DRAFT_STORAGE_PREFIX}42`)).toBeNull();
  });

  test('invalidates async callbacks per draft key without persisting the revision', () => {
    const store = createComposerDraftStore('42');
    const initialRevision = readComposerDraftRevision(store, 'new-task');

    expect(initialRevision).toBe(0);
    expect(isComposerDraftRevisionCurrent(store, 'new-task', initialRevision)).toBe(true);

    const nextRevision = invalidateComposerDraftRevision(store, 'new-task');

    expect(nextRevision).toBe(1);
    expect(isComposerDraftRevisionCurrent(store, 'new-task', initialRevision)).toBe(false);
    expect(isComposerDraftRevisionCurrent(store, 'new-task', nextRevision)).toBe(true);
    expect(sessionStorage.getItem(`${COMPOSER_DRAFT_STORAGE_PREFIX}42`)).toBeNull();
  });

  test('tracks writes separately from callback invalidation', () => {
    const store = createComposerDraftStore('42');
    const initialMutation = readComposerDraftMutationRevision(store, 'new-task');

    // Public write helpers are the mutation boundary used by both composers.
    // The callback revision remains unchanged until an async operation is
    // explicitly invalidated.
    writeComposerInputDraft(store, 'new-task', 'written through helper');
    expect(readComposerDraftMutationRevision(store, 'new-task')).toBe(initialMutation + 1);
    writeComposerInputDraft(store, 'new-task', 'written again');
    expect(readComposerDraftMutationRevision(store, 'new-task')).toBe(initialMutation + 2);
    expect(readComposerDraftRevision(store, 'new-task')).toBe(0);
  });

  test('round-trips an in-progress phone upload session with the draft', () => {
    const store = createComposerDraftStore('42');
    const session = {
      session_id: 'mobile-session-1',
      upload_url: '/upload/mobile-session-1',
      topic: 'new-task',
    };

    writeComposerPhoneUploadSession(store, 'new-task', session);
    store.persist();

    const restored = createComposerDraftStore('42');
    expect(readComposerPhoneUploadSession(restored, 'new-task')).toEqual(session);
  });

  test('round-trips the selected Agent and project context with a new-task draft', () => {
    const store = createComposerDraftStore('42');
    const context = {
      agent: {
        uid: 22,
        username: 'ops-agent',
        display_name: '运营数据助手',
        topic_id: 'p2p_42_22',
        avatar_url: '/avatars/22.png',
        is_bot: true,
      },
      projectId: 12,
      projectName: 'Website',
    };

    writeComposerTaskContextDraft(store, 'new-task', context);
    store.persist();

    const restored = createComposerDraftStore('42');
    expect(readComposerTaskContextDraft(restored, 'new-task')).toEqual(context);
  });

  test('notifies subscribers when a draft is changed through the public helpers', () => {
    const store = createComposerDraftStore('42');
    const changes = [];
    const unsubscribe = subscribeComposerDraftStore(store, (change) => changes.push(change));

    writeComposerInputDraft(store, 'new-task', 'draft');
    writeComposerPhoneUploadSession(store, 'new-task', { session_id: 'mobile-session-1' });
    unsubscribe();
    writeComposerInputDraft(store, 'new-task', 'draft after unsubscribe');

    expect(changes).toEqual([
      expect.objectContaining({ key: 'new-task' }),
      expect.objectContaining({ key: 'new-task' }),
    ]);
  });

  test('forwards late writes from a deactivated store to its remounted replacement', () => {
    const staleStore = createComposerDraftStore('42');
    writeComposerInputDraft(staleStore, 'p2p_1_2', 'draft before remount');
    staleStore.persist();
    staleStore.deactivate();

    const activeStore = createComposerDraftStore('42');
    writeComposerInputDraft(staleStore, 'p2p_1_2', 'late callback draft');
    writeComposerPhoneUploadSession(staleStore, 'p2p_1_2', { session_id: 'mobile-session-2' });
    staleStore.persist();

    expect(activeStore.getInputDraft('p2p_1_2')).toBe('late callback draft');
    expect(readComposerPhoneUploadSession(activeStore, 'p2p_1_2')).toEqual({
      session_id: 'mobile-session-2',
    });
    expect(JSON.parse(sessionStorage.getItem(`${COMPOSER_DRAFT_STORAGE_PREFIX}42`)))
      .toMatchObject({
        inputDrafts: [['p2p_1_2', 'late callback draft']],
        phoneUploadSessions: [['p2p_1_2', { session_id: 'mobile-session-2' }]],
      });
  });

  test('does not forward callbacks after the store is cleared for logout', () => {
    const staleStore = createComposerDraftStore('42');
    staleStore.persist();
    staleStore.deactivate();
    const activeStore = createComposerDraftStore('42');
    staleStore.clearPersisted();

    writeComposerInputDraft(staleStore, 'p2p_1_2', 'must not resurrect');
    staleStore.persist();

    expect(activeStore.getInputDraft('p2p_1_2')).toBe('');
    expect(sessionStorage.getItem(`${COMPOSER_DRAFT_STORAGE_PREFIX}42`)).toBeNull();
  });

  test('keeps a callback chain pointed at the latest remounted store', () => {
    const firstStore = createComposerDraftStore('42');
    firstStore.deactivate();
    const secondStore = createComposerDraftStore('42');
    secondStore.deactivate();
    const latestStore = createComposerDraftStore('42');

    writeComposerInputDraft(firstStore, 'p2p_1_2', 'latest store draft');
    expect(latestStore.getInputDraft('p2p_1_2')).toBe('latest store draft');
    expect(readComposerDraftMutationRevision(latestStore, 'p2p_1_2')).toBe(1);
    expect(readComposerDraftMutationRevision(firstStore, 'p2p_1_2')).toBe(1);
  });

  test('closes stores without snapshots so logout cannot resurrect a late draft', () => {
    const staleStore = createComposerDraftStore('42');
    staleStore.deactivate();

    expect(clearPersistedComposerDrafts()).toBe(0);
    writeComposerInputDraft(staleStore, 'p2p_1_2', 'must stay cleared');
    staleStore.persist();

    expect(sessionStorage.getItem(`${COMPOSER_DRAFT_STORAGE_PREFIX}42`)).toBeNull();
  });
});
