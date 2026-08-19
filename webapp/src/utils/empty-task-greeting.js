export function formatEmptyTaskGreeting(user) {
  const displayName = [user?.display_name, user?.username]
    .map((value) => String(value || '').trim())
    .find(Boolean) || '';
  return displayName ? `需要为您做什么，${displayName}？` : '需要为您做什么？';
}
