export function normalizeUserProfile(raw) {
  if (!raw) return null;
  const username = raw.username || '';
  return {
    uid: raw.uid || raw.id,
    username,
    email: raw.email || '',
    display_name: raw.display_name || username,
    avatar_url: raw.avatar_url || '',
    account_type: raw.account_type || 'human',
  };
}
