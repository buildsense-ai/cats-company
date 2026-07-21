function groupIdFromTopicId(topicId) {
  const match = String(topicId || '').match(/^grp_(\d+)$/);
  if (!match) return undefined;
  const groupId = Number(match[1]);
  return Number.isFinite(groupId) && groupId > 0 ? groupId : undefined;
}

export function normalizeActiveTopic(value) {
  if (!value) return null;

  if (typeof value === 'string') {
    if (!value || value === '[object Object]') return null;
    const groupId = groupIdFromTopicId(value);
    return {
      topicId: value,
      name: '',
      isGroup: Boolean(groupId),
      groupId,
    };
  }

  if (typeof value === 'object' && value.topicId) {
    const explicitGroupId = Number(value.groupId);
    const groupId = Number.isFinite(explicitGroupId) && explicitGroupId > 0
      ? explicitGroupId
      : groupIdFromTopicId(value.topicId);
    return {
      topicId: value.topicId,
      name: value.name || '',
      isGroup: Boolean(value.isGroup || groupId),
      groupId,
      avatar_url: value.avatar_url || '',
      friendId: value.friendId,
    };
  }

  return null;
}
