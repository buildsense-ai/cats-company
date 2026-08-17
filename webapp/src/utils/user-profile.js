export const USER_PROFILE_STORAGE_KEY = 'oc_user';

export function normalizeUserProfile(raw) {
  if (!raw || typeof raw !== 'object') return null;
  const uid = raw.uid ?? raw.id;
  if (uid === undefined || uid === null || uid === '') return null;
  const username = raw.username || '';
  return {
    uid,
    username,
    email: raw.email || '',
    display_name: raw.display_name || username,
    avatar_url: raw.avatar_url || '',
    account_type: raw.account_type || 'human',
  };
}

export function readStoredUserProfile(storage) {
  try {
    const source = storage ?? globalThis.localStorage;
    const serialized = source?.getItem(USER_PROFILE_STORAGE_KEY);
    return serialized ? normalizeUserProfile(JSON.parse(serialized)) : null;
  } catch {
    return null;
  }
}
