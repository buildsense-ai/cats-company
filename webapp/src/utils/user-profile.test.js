import { describe, expect, test } from 'vitest';
import {
  clearStoredUserProfile,
  normalizeUserProfile,
  readStoredUserProfile,
  writeStoredUserProfile,
} from './user-profile';

describe('normalizeUserProfile', () => {
  test('normalizes the auth and workspace user shape consistently', () => {
    expect(normalizeUserProfile({ id: 8, username: 'cats' })).toEqual({
      uid: 8,
      username: 'cats',
      email: '',
      display_name: 'cats',
      avatar_url: '',
      account_type: 'human',
    });
  });

  test('returns null when no profile is available', () => {
    expect(normalizeUserProfile(null)).toBeNull();
  });

  test('rejects an object without a stable user identifier', () => {
    expect(normalizeUserProfile({ username: 'cats' })).toBeNull();
  });

  test('safely restores a valid stored profile and rejects malformed storage', () => {
    const storage = {
      getItem: () => JSON.stringify({ id: 8, username: 'cats' }),
    };

    expect(readStoredUserProfile(storage)).toMatchObject({ uid: 8, username: 'cats' });
    expect(readStoredUserProfile({ getItem: () => '{invalid json' })).toBeNull();
  });

  test('writes the normalized profile through the shared storage contract and clears it', () => {
    const values = new Map();
    const storage = {
      getItem: (key) => values.get(key) || null,
      setItem: (key, value) => values.set(key, value),
      removeItem: (key) => values.delete(key),
    };

    expect(writeStoredUserProfile({ id: 8, username: 'cats' }, storage)).toEqual({
      uid: 8,
      username: 'cats',
      email: '',
      display_name: 'cats',
      avatar_url: '',
      account_type: 'human',
    });
    expect(readStoredUserProfile(storage)).toMatchObject({ uid: 8, username: 'cats' });

    expect(clearStoredUserProfile(storage)).toBe(true);
    expect(readStoredUserProfile(storage)).toBeNull();
  });
});
