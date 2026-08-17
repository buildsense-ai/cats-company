import { describe, expect, test } from 'vitest';
import { normalizeUserProfile, readStoredUserProfile } from './user-profile';

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
});
