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
    const normalized = {
      topicId: value.topicId,
      name: value.name || '',
      isGroup: Boolean(value.isGroup || groupId),
      groupId,
      avatar_url: value.avatar_url || '',
      friendId: value.friendId,
    };
    if (Object.prototype.hasOwnProperty.call(value, 'isBot')) normalized.isBot = Boolean(value.isBot);
    if (Object.prototype.hasOwnProperty.call(value, 'hasBot')) normalized.hasBot = Boolean(value.hasBot);
    if (Object.prototype.hasOwnProperty.call(value, 'isAgentTask')) normalized.isAgentTask = Boolean(value.isAgentTask);
    if (Object.prototype.hasOwnProperty.call(value, 'memberCount')) normalized.memberCount = Number(value.memberCount || 0);
    return normalized;
  }

  return null;
}

export function lastTopicStorageKey(uid) {
  return uid ? `v3_last_topic:${uid}` : 'v3_last_topic';
}

export function readStoredTopic(uid, storage = globalThis.localStorage) {
  if (!storage) return null;
  const keys = uid ? [lastTopicStorageKey(uid), 'v3_last_topic'] : ['v3_last_topic'];
  for (const key of keys) {
    const raw = storage.getItem(key);
    if (!raw) continue;

    try {
      const topic = normalizeActiveTopic(JSON.parse(raw));
      if (topic) return topic;
    } catch {
      const topic = normalizeActiveTopic(raw);
      if (topic) return topic;
    }
  }

  return null;
}

export function writeStoredTopic(uid, topic, storage = globalThis.localStorage) {
  if (!storage) return;
  const key = lastTopicStorageKey(uid);
  const normalized = normalizeActiveTopic(topic);
  if (!normalized) {
    storage.removeItem(key);
    storage.removeItem('v3_last_topic');
    return;
  }

  const serialized = JSON.stringify(normalized);
  storage.setItem(key, serialized);
  storage.setItem('v3_last_topic', serialized);
}

export function shouldForgetStoredTopic(error) {
  return error?.status === 400 || error?.status === 403 || error?.status === 404;
}
