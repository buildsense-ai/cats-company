import { describe, expect, test } from 'vitest';
import { normalizeUserProfile } from './user-profile';

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
});
