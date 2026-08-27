import { afterEach, describe, expect, test } from 'vitest';
import {
  COMPOSER_DRAFT_STORAGE_PREFIX,
  clearPersistedComposerDrafts,
  composerDraftStorageKey,
} from './composer-draft-storage';

describe('composer draft storage', () => {
  afterEach(() => {
    sessionStorage.clear();
  });

  test('normalizes account ids when creating storage keys', () => {
    expect(composerDraftStorageKey(' 42 ')).toBe(`${COMPOSER_DRAFT_STORAGE_PREFIX}42`);
    expect(composerDraftStorageKey('')).toBe('');
    expect(composerDraftStorageKey(null)).toBe('');
  });

  test('clears all account drafts while preserving unrelated session data', () => {
    sessionStorage.setItem(`${COMPOSER_DRAFT_STORAGE_PREFIX}1`, '{}');
    sessionStorage.setItem(`${COMPOSER_DRAFT_STORAGE_PREFIX}2`, '{}');
    sessionStorage.setItem('unrelated', 'keep');

    expect(clearPersistedComposerDrafts()).toBe(2);
    expect(sessionStorage.getItem(`${COMPOSER_DRAFT_STORAGE_PREFIX}1`)).toBeNull();
    expect(sessionStorage.getItem(`${COMPOSER_DRAFT_STORAGE_PREFIX}2`)).toBeNull();
    expect(sessionStorage.getItem('unrelated')).toBe('keep');
  });

  test('returns zero when the storage implementation is unavailable', () => {
    expect(clearPersistedComposerDrafts(null)).toBe(0);
  });
});
