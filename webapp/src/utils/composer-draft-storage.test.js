import { afterEach, describe, expect, test } from 'vitest';
import {
  COMPOSER_DRAFT_STORAGE_PREFIX,
  clearPersistedComposerDrafts,
  createComposerDraftStore,
  invalidateComposerDraftRevision,
  isComposerDraftRevisionCurrent,
  readComposerPhoneUploadSession,
  readComposerDraftMutationRevision,
  readComposerDraftRevision,
  subscribeComposerDraftStore,
  writeComposerPhoneUploadSession,
  writeComposerInputDraft,
} from './composer-draft-storage';

function sharedStorage(values = new Map()) {
  return {
    get length() {
      return values.size;
    },
    key(index) {
      return [...values.keys()][index] || null;
    },
    getItem(key) {
      return values.has(String(key)) ? values.get(String(key)) : null;
    },
    setItem(key, value) {
      values.set(String(key), String(value));
    },
    removeItem(key) {
      values.delete(String(key));
    },
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

  test('fences a stale context after logout clears the shared storage', () => {
    const sharedValues = new Map();
    const tabAStorage = sharedStorage(sharedValues);
    const tabBStorage = sharedStorage(sharedValues);

    const staleStore = createComposerDraftStore('42', tabBStorage);
    writeComposerInputDraft(staleStore, 'new-task', '旧 tab 草稿');
    staleStore.persist();

    expect(clearPersistedComposerDrafts(tabAStorage)).toBe(1);
    staleStore.persist();

    expect(tabAStorage.getItem(`${COMPOSER_DRAFT_STORAGE_PREFIX}42`)).toBeNull();
    expect(createComposerDraftStore('42', tabBStorage).getInputDraft('new-task')).toBe('');
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
