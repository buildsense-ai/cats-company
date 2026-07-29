export function groupBotMembers(members = []) {
  return members.filter((member) => member?.is_bot === true || member?.account_type === 'bot');
}

export function groupMemberName(member, members = []) {
  const displayName = String(member?.display_name || member?.username || `Agent ${member?.user_id || ''}`).trim();
  const duplicates = groupBotMembers(members).filter((candidate) => (
    candidate?.user_id !== member?.user_id
    && String(candidate?.display_name || candidate?.username || '').trim() === displayName
  ));
  if (duplicates.length === 0) return displayName;
  const handle = String(member?.username || '').trim();
  return handle && handle !== displayName ? `${displayName} · ${handle}` : `${displayName} · Agent ${member?.user_id}`;
}

export function formatGroupMentions(text, members = []) {
  const names = new Map(groupBotMembers(members).map((member) => [String(member.user_id), groupMemberName(member, members)]));
  return String(text || '').replace(/@usr(\d+)/gu, (whole, uid) => {
    const name = names.get(uid);
    return name ? `@${name}` : whole;
  });
}
