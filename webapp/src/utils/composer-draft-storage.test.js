import { afterEach, describe, expect, test } from 'vitest';
import {
  COMPOSER_DRAFT_STORAGE_PREFIX,
  clearPersistedComposerDrafts,
  createComposerDraftStore,
} from './composer-draft-storage';

describe('composer draft storage', () => {
  afterEach(() => {
    sessionStorage.clear();
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
});
