const API_BASE = import.meta.env.VITE_API_BASE || '';
const DEFAULT_WS_SCHEME = window.location.protocol === 'https:' ? 'wss' : 'ws';
const WS_URL = import.meta.env.VITE_WS_URL || `${DEFAULT_WS_SCHEME}://${window.location.host}/v0/channels`;

let token = localStorage.getItem('oc_token');
let wsConn = null;
let wsReconnectTimer = null;
let wsConnectTimer = null;
let wsGeneration = 0;
let wsReconnectAttempt = 0;
let msgHandlers = [];
let wsConnected = false;
let topicLastSeq = {};

const WS_RECONNECT_DELAYS = [1000, 2000, 4000, 8000, 15000, 30000];
const WS_CONNECT_TIMEOUT_MS = 10000;

export function updateTopicSeq(topicId, seq) {
  if (!topicLastSeq[topicId] || seq > topicLastSeq[topicId]) {
    topicLastSeq[topicId] = seq;
  }
}

export function requestMissedMessages(topicId) {
  const lastSeq = topicLastSeq[topicId] || 0;
  if (lastSeq > 0) {
    sendWS({ get: { id: nextMsgId(), topic: topicId, what: 'history', seq: lastSeq } });
  }
}

export function setToken(t) {
  token = t;
  if (t) localStorage.setItem('oc_token', t);
  else localStorage.removeItem('oc_token');
}

export function getToken() {
  return token;
}

export function getWebSocketURL() {
  return WS_URL;
}

export function getApiBaseURL() {
  if (!API_BASE) return window.location.origin.replace(/\/+$/, '');
  try {
    return new URL(API_BASE, window.location.origin).toString().replace(/\/+$/, '');
  } catch {
    return window.location.origin.replace(/\/+$/, '');
  }
}

export function resolveMediaURL(url) {
  if (!url) return '';
  if (/^https?:\/\//.test(url)) return url;
  return `${API_BASE}${url}`;
}

export function isWSConnected() {
  return wsConnected;
}

export function isTokenExpired(candidate = token) {
  if (!candidate) return false;
  try {
    const encodedPayload = candidate.split('.')[1];
    if (!encodedPayload) return false;
    const normalized = encodedPayload.replace(/-/g, '+').replace(/_/g, '/');
    const padded = normalized.padEnd(Math.ceil(normalized.length / 4) * 4, '=');
    const payload = JSON.parse(atob(padded));
    const expiresAt = Number(payload.exp);
    return Number.isFinite(expiresAt) && Date.now() >= expiresAt * 1000;
  } catch {
    return false;
  }
}

async function request(method, path, body) {
  const headers = { 'Content-Type': 'application/json' };
  if (token) headers['Authorization'] = `Bearer ${token}`;

  let res;
  try {
    res = await fetch(`${API_BASE}${path}`, {
      method,
      headers,
      body: body ? JSON.stringify(body) : undefined,
    });
  } catch (cause) {
    const error = new Error('网络连接失败，请检查后端服务是否运行');
    error.code = 'NETWORK_ERROR';
    error.cause = cause;
    throw error;
  }

  let data = {};
  try {
    data = await res.json();
  } catch {
    data = {};
  }
  if (!res.ok) {
    const error = new Error(data.error || statusMessage(res.status));
    error.status = res.status;
    error.data = data;
    throw error;
  }
  return data;
}

function statusMessage(status) {
  if (status === 400) return '请求内容有误，请检查后重试';
  if (status === 401) return '登录状态已失效，请重新登录';
  if (status === 403) return '当前账号没有执行此操作的权限';
  if (status === 404) return '请求的功能暂时不可用';
  if (status === 409) return '当前数据已发生变化，请刷新后重试';
  if (status === 429) return '操作过于频繁，请稍后再试';
  if (status >= 500) return '后端服务暂时异常，请稍后重试';
  return '请求失败，请稍后重试';
}

export const api = {
  sendVerificationCode: (email) => request('POST', '/api/auth/send-code', { email }),
  sendPasswordResetCode: (email) => request('POST', '/api/auth/reset-password/send-code', { email }),
  resetPassword: (data) => request('POST', '/api/auth/reset-password', data),
  register: (data) => request('POST', '/api/auth/register', data),
  login: (data) => request('POST', '/api/auth/login', data),
  getMe: () => request('GET', '/api/me'),
  updateMe: (displayName, avatarUrl) =>
    request('POST', '/api/me/update', { display_name: displayName, avatar_url: avatarUrl }),

  getFriends: () => request('GET', '/api/friends'),
  getPendingRequests: (agentUid = '') => request('GET', `/api/friends/pending${agentUid ? `?agent_uid=${encodeURIComponent(agentUid)}` : ''}`),
  sendFriendRequest: (userId, message) =>
    request('POST', '/api/friends/request', { user_id: userId, message }),
  acceptFriend: (userId) =>
    request('POST', '/api/friends/accept', { user_id: userId }),
  acceptAgentFriend: (agentUid, userId) =>
    request('POST', '/api/friends/accept', { agent_uid: agentUid, user_id: userId }),
  rejectFriend: (userId) =>
    request('POST', '/api/friends/reject', { user_id: userId }),
  rejectAgentFriend: (agentUid, userId) =>
    request('POST', '/api/friends/reject', { agent_uid: agentUid, user_id: userId }),
  blockUser: (userId) =>
    request('POST', '/api/friends/block', { user_id: userId }),
  removeFriend: (userId) =>
    request('DELETE', `/api/friends/remove?user_id=${userId}`),

  searchUsers: (q, mode = 'name') =>
    request('GET', `/api/users/search?q=${encodeURIComponent(q)}&mode=${encodeURIComponent(mode)}`),

  // Send message via REST
  sendMessage: (topicId, content, replyTo, mentions = []) => {
    const payload = { topic_id: topicId };

    if (typeof content === 'string') {
      payload.type = 'text';
      payload.content = content;
    } else if (content && typeof content === 'object') {
      payload.type = content.type || content.msg_type || 'text';
      if (Array.isArray(content.content_blocks) && content.content_blocks.length > 0) {
        payload.content_blocks = content.content_blocks;
      }
      if (content.mode) payload.mode = content.mode;
      if (content.role) payload.role = content.role;
      if (content.metadata) payload.metadata = content.metadata;
      if (typeof content.content === 'string') {
        payload.content = content.content;
      } else if (content.payload || content.type || content.metadata) {
        payload.content = JSON.stringify(content);
      } else {
        payload.content = JSON.stringify(content);
      }
    } else {
      payload.type = 'text';
      payload.content = String(content ?? '');
    }

    if (replyTo) payload.reply_to = replyTo;
    if (Array.isArray(mentions) && mentions.length > 0) payload.mentions = mentions;
    return request('POST', '/api/messages/send', payload);
  },

  // REST fallback for message history
  getMessages: (topicId, limit, offset, latest = false, beforeId = 0) =>
    request(
      'GET',
      `/api/messages?topic_id=${encodeURIComponent(topicId)}&limit=${limit || 50}&offset=${offset || 0}${latest ? '&latest=1' : ''}${beforeId > 0 ? `&before_id=${encodeURIComponent(beforeId)}` : ''}`,
    ),
  getConversations: () => request('GET', '/api/conversations'),
  getProjects: () => request('GET', '/api/projects'),
  createProject: (name) => request('POST', '/api/projects', { name }),
  renameProject: (projectId, name) => request('PATCH', '/api/projects', { project_id: projectId, name }),
  deleteProject: (projectId) => request('DELETE', `/api/projects?project_id=${encodeURIComponent(projectId)}`),
  assignProjectTopic: (projectId, topicId) => request('POST', '/api/projects/topic', { project_id: projectId, topic_id: topicId }),
  removeProjectTopic: (topicId) => request('DELETE', `/api/projects/topic?topic_id=${encodeURIComponent(topicId)}`),
  updateConversationTitle: (topicId, name) => request('PATCH', '/api/conversations', { topic_id: topicId, name }),
  getRelayConfig: () => request('GET', '/api/relay/config'),
  getRelayCommercial: () => request('GET', '/api/relay/commercial'),
  redeemRelayInvite: (code) => request('POST', '/api/relay/invite/redeem', { code }),
  createRelaySession: () => request('POST', '/api/relay/session', {}),
  getRelayKey: () => request('GET', '/api/relay/key'),
  getRelayUsage: ({ model, source } = {}) => {
    const params = new URLSearchParams();
    if (model) params.set('model', model);
    if (source) params.set('source', source);
    const query = params.toString();
    return request('GET', `/api/relay/usage${query ? `?${query}` : ''}`);
  },
  createRelayKey: (name) => request('POST', '/api/relay/key', name ? { name } : {}),
  rotateRelayKey: () => request('POST', '/api/relay/key/rotate', {}),
  revealRelayKey: () => request('POST', '/api/relay/key/reveal', {}),
  revokeRelayKey: () => request('DELETE', '/api/relay/key'),

  getOnlineStatus: () => request('GET', '/api/users/online'),

  createDeviceConnectorPairing: (deviceName) =>
    request('POST', '/api/device-connectors/pairings', {
      device_name: deviceName || '',
      capabilities: ['read_file', 'resolve_common_directory', 'glob', 'grep'],
    }),
  getDeviceConnectorPairing: (pairingId) =>
    request('GET', `/api/device-connectors/pairings/${encodeURIComponent(pairingId)}`),
  getCatsCoDesktopReleases: () => request('GET', '/api/catsco/desktop-releases'),
  getDevices: () => request('GET', '/api/devices'),
  unlinkDevice: (deviceId) => request('DELETE', `/api/devices/${encodeURIComponent(deviceId)}`),
  getDeviceAudit: (limit = 20) => request('GET', `/api/devices/audit?limit=${limit}`),

  // Virtual employee roster
  getAgents: () => request('GET', '/api/agents'),
  getAgentQuota: (agentUid) => request('GET', `/api/agents/quota?uid=${encodeURIComponent(agentUid)}`),
  openAgent: (agentUid) => request('POST', '/api/agents/open', { agent_uid: agentUid }),
  createDesktopConnectSession: () => request('POST', '/api/desktop-connect/session', {}),
  getDesktopConnectStatus: (code) =>
    request('GET', `/api/desktop-connect/status?code=${encodeURIComponent(code)}`),
  getAgentEntries: (agentUid) => request('GET', `/api/agent-entries?agent_uid=${encodeURIComponent(agentUid)}`),
  createAgentEntry: (agentUid, channel, channelAppId = '', accessMode = 'approval_required') =>
    request('POST', '/api/agent-entries', {
      agent_uid: agentUid,
      channel,
      access_mode: accessMode,
      ...(channelAppId ? { channel_app_id: channelAppId } : {}),
    }),
  regenerateAgentEntry: (entryId) =>
    request('POST', `/api/agent-entries/${encodeURIComponent(entryId)}/regenerate`, {}),
  getAgentChannelBindings: (agentUid) =>
    request('GET', `/api/channel-agent-bindings?agent_uid=${encodeURIComponent(agentUid)}`),
  getChannelAgentEntryPreview: (sceneKey) =>
    request('GET', `/api/channel-agent-entry/preview?scene_key=${encodeURIComponent(sceneKey)}`),
  confirmChannelAgentBinding: (payload) =>
    request('POST', '/api/channel-agent-bindings/confirm', payload),
  linkChannelAgentBindingUser: (payload) =>
    request('POST', '/api/channel-agent-bindings/link-user', payload),
  createChannelIdentityMobileLink: (agentUid, channel, entryId = null) =>
    request('POST', '/api/channel-agent-bindings/mobile-link', {
      agent_uid: agentUid,
      channel,
      ...(entryId ? { entry_id: entryId } : {}),
    }),
  createChannelGroupMobileLink: (groupId, topicId, channel) =>
    request('POST', '/api/channel-agent-bindings/group-mobile-link', {
      group_id: groupId,
      topic_id: topicId,
      channel,
    }),
  getChannelPrivateBindings: ({ agentUid, groupId, topicId }) => {
    const params = new URLSearchParams();
    if (agentUid) params.set('agent_uid', agentUid);
    if (groupId) params.set('group_id', groupId);
    if (topicId) params.set('topic_id', topicId);
    return request('GET', `/api/channel-private-bindings?${params.toString()}`);
  },
  unlinkChannelPrivateBinding: ({ bindingKey, agentUid, groupId, topicId, selectedAt }) =>
    request('DELETE', '/api/channel-private-bindings', {
      binding_key: bindingKey,
      ...(agentUid ? { agent_uid: agentUid } : {}),
      ...(groupId ? { group_id: groupId } : {}),
      ...(topicId ? { topic_id: topicId } : {}),
      selected_at: selectedAt,
    }),
  getWeixinClawBotQRCodeStatus: (sceneKey, qrcode) =>
    request('GET', `/api/channel-agent-bindings/weixin-clawbot/qrcode-status?scene_key=${encodeURIComponent(sceneKey)}&qrcode=${encodeURIComponent(qrcode)}`),

  // Groups
  createGroup: (name, memberIds, { kind } = {}) => request('POST', '/api/groups/create', {
    name,
    member_ids: memberIds,
    ...(kind ? { kind } : {}),
  }),
  getGroups: () => request('GET', '/api/groups'),
  getGroupInfo: (groupId) => request('GET', `/api/groups/info?id=${groupId}`),
  updateGroup: (groupId, name, avatarUrl) =>
    request('POST', '/api/groups/update', { group_id: groupId, name, avatar_url: avatarUrl }),
  inviteToGroup: (groupId, userIds) => request('POST', '/api/groups/invite', { group_id: groupId, user_ids: userIds }),
  resolveGroupInviteRequest: (groupId, requestId, action) =>
    request('POST', '/api/groups/invite/resolve', { group_id: groupId, request_id: requestId, action }),
  leaveGroup: (groupId) => request('POST', '/api/groups/leave', { group_id: groupId }),
  kickMember: (groupId, userId) => request('POST', '/api/groups/kick', { group_id: groupId, user_id: userId }),
  disbandGroup: (groupId) => request('POST', '/api/groups/disband', { group_id: groupId }),
  updateMemberRole: (groupId, userId, role) => request('POST', '/api/groups/role', { group_id: groupId, user_id: userId, role }),
  muteMember: (groupId, userId) => request('POST', '/api/groups/mute', { group_id: groupId, user_id: userId }),
  unmuteMember: (groupId, userId) => request('POST', '/api/groups/unmute', { group_id: groupId, user_id: userId }),
  setGroupAnnouncement: (groupId, announcement) =>
    request('POST', '/api/groups/announcement', { group_id: groupId, announcement }),

  // Bot management
  getMyBots: () => request('GET', '/api/bots'),
  getBotAPIKey: (uid) => request('GET', `/api/bots/api-key?uid=${uid}`),
  createBot: ({ username, display_name }, deployToCloud = false) =>
    request('POST', deployToCloud ? '/api/bots/deploy' : '/api/bots', { username, display_name }),
  updateBot: (uid, { display_name, avatar_url }) =>
    request('PATCH', `/api/bots?uid=${uid}`, { display_name, avatar_url }),
  deleteBot: (uid) => request('DELETE', `/api/bots?uid=${uid}`),
  setBotVisibility: (uid, visibility) => request('PATCH', `/api/bots/visibility?uid=${uid}&v=${visibility}`),
  getBotModelConfig: (uid, { includeUsage = false } = {}) =>
    request('GET', `/api/bots/model-config?uid=${uid}${includeUsage ? '&include_usage=1' : ''}`),
  updateBotModelConfig: (uid, modelConfig) => request('PATCH', `/api/bots/model-config?uid=${uid}`, modelConfig),
  getBotFriends: (uid) => request('GET', `/api/bots/friends?uid=${uid}`),
  removeBotFriend: (uid, userId) => request('DELETE', `/api/bots/friends?uid=${uid}&user_id=${userId}`),
  acceptFriendAsBot: async (apiKey, userId) => {
    const res = await fetch(`${API_BASE}/api/friends/accept`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        Authorization: `ApiKey ${apiKey}`,
      },
      body: JSON.stringify({ user_id: userId }),
    });
    const data = await res.json();
    if (!res.ok) throw new Error(data.error || 'Request failed');
    return data;
  },
  uploadFile: async (file, type = 'file') => {
    const formData = new FormData();
    formData.append('file', file);
    const res = await fetch(`${API_BASE}/api/upload?type=${type}`, {
      method: 'POST',
      headers: token ? { Authorization: `Bearer ${token}` } : undefined,
      body: formData,
    });

    const raw = await res.text();
    let data = {};
    if (raw) {
      try {
        data = JSON.parse(raw);
      } catch (err) {
        if (res.status === 413 || raw.includes('413') || raw.includes('Payload Too Large')) {
          throw new Error('Payload Too Large');
        }
        if (!res.ok) {
          throw new Error(`Upload failed with HTTP ${res.status}`);
        }
        throw new Error('Upload failed: invalid server response');
      }
    }
    if (!res.ok) throw new Error(data.error || `Upload failed with HTTP ${res.status}`);
    return data;
  },
  createMobileUploadSession: async (topic) => request('POST', '/api/mobile-upload/sessions', { topic }),
  getMobileUploadSession: async (sessionId) => request('GET', `/api/mobile-upload/sessions/${encodeURIComponent(sessionId)}`),
  uploadMobileSessionFile: async (sessionId, file, type = 'file') => {
    const formData = new FormData();
    formData.append('file', file);
    const res = await fetch(`${API_BASE}/api/mobile-upload/sessions/${encodeURIComponent(sessionId)}/files?type=${type}`, {
      method: 'POST',
      body: formData,
    });
    const data = await res.json();
    if (!res.ok) throw new Error(data.error || 'Upload failed');
    return data;
  },
  uploadFeedbackImage: (file) => api.uploadFile(file, 'feedback'),
  submitFeedback: (data) => request('POST', '/api/feedback', data),
  getTutorialTasks: () => request('GET', '/api/tutorial-tasks'),
  getCloudArtifacts: (status = 'active') =>
    request('GET', `/api/artifacts?status=${encodeURIComponent(status)}`),
  deleteCloudArtifact: (artifactId) =>
    request('DELETE', `/api/artifacts/${encodeURIComponent(artifactId)}`),
  restoreCloudArtifact: (artifactId) =>
    request('POST', `/api/artifacts/${encodeURIComponent(artifactId)}/restore`, {}),
};

// --- WebSocket ---

let _msgIdCounter = 0;
function nextMsgId() {
  return String(++_msgIdCounter);
}

function reconnectDelay(attempt) {
  const index = Math.max(0, Math.min(attempt - 1, WS_RECONNECT_DELAYS.length - 1));
  return WS_RECONNECT_DELAYS[index];
}

export function connectWS(onMessage, { force = false } = {}) {
  if (!token) return false;
  if (isTokenExpired()) {
    onMessage({ _type: 'ws_auth_expired' });
    return false;
  }
  if (!force && wsConn && (
    wsConn.readyState === WebSocket.OPEN || wsConn.readyState === WebSocket.CONNECTING
  )) {
    return false;
  }

  if (wsReconnectTimer) {
    clearTimeout(wsReconnectTimer);
    wsReconnectTimer = null;
  }
  if (wsConnectTimer) {
    clearTimeout(wsConnectTimer);
    wsConnectTimer = null;
  }
  const generation = ++wsGeneration;
  if (wsConn) {
    const staleConn = wsConn;
    wsConn = null;
    staleConn.onopen = null;
    staleConn.onclose = null;
    staleConn.onerror = null;
    staleConn.onmessage = null;
    staleConn.close();
  }
  wsConnected = false;
  const url = `${WS_URL}?token=${token}`;
  const conn = new WebSocket(url);
  wsConn = conn;
  const isCurrent = () => wsConn === conn && wsGeneration === generation;

  onMessage({ _type: 'ws_connecting', attempt: wsReconnectAttempt });

  wsConnectTimer = setTimeout(() => {
    if (!isCurrent() || conn.readyState !== WebSocket.CONNECTING) return;
    wsConnectTimer = null;
    wsConn = null;
    conn.onopen = null;
    conn.onclose = null;
    conn.onerror = null;
    conn.onmessage = null;
    conn.close();
    wsConnected = false;
    wsReconnectAttempt += 1;
    const retryInMs = reconnectDelay(wsReconnectAttempt);
    onMessage({ _type: 'ws_close', attempt: wsReconnectAttempt, retryInMs });
    wsReconnectTimer = setTimeout(() => {
      if (wsGeneration === generation) connectWS(onMessage);
    }, retryInMs);
  }, WS_CONNECT_TIMEOUT_MS);

  conn.onopen = () => {
    if (!isCurrent()) {
      conn.close();
      return;
    }
    if (wsConnectTimer) {
      clearTimeout(wsConnectTimer);
      wsConnectTimer = null;
    }
    console.log('WebSocket connected');
    wsConnected = true;
    wsReconnectAttempt = 0;
    // Send handshake
    sendWS({ hi: { id: nextMsgId(), ver: '0.1.0' } });
    // Request online status of friends
    sendWS({ get: { id: nextMsgId(), topic: 'me', what: 'online' } });
    // Request missed messages for all tracked topics
    Object.keys(topicLastSeq).forEach((tid) => {
      requestMissedMessages(tid);
    });
    const openMessage = { _type: 'ws_open' };
    onMessage(openMessage);
    msgHandlers.forEach((handler) => handler(openMessage));
  };

  conn.onclose = () => {
    if (!isCurrent()) return;
    if (wsConnectTimer) {
      clearTimeout(wsConnectTimer);
      wsConnectTimer = null;
    }
    console.log('WebSocket disconnected');
    wsConnected = false;
    wsConn = null;
    wsReconnectAttempt += 1;
    const retryInMs = reconnectDelay(wsReconnectAttempt);
    onMessage({ _type: 'ws_close', attempt: wsReconnectAttempt, retryInMs });
    if (isTokenExpired()) {
      onMessage({ _type: 'ws_auth_expired' });
      return;
    }
    if (token) {
      wsReconnectTimer = setTimeout(() => {
        if (wsGeneration === generation) {
          connectWS(onMessage);
        }
      }, retryInMs);
    }
  };

  conn.onerror = (err) => {
    if (!isCurrent()) return;
    console.error('WebSocket error:', err);
  };

  conn.onmessage = (evt) => {
    if (!isCurrent()) return;
    try {
      const msg = JSON.parse(evt.data);
      onMessage(msg);
      msgHandlers.forEach((h) => h(msg));
    } catch (e) {
      console.error('WS parse error:', e);
    }
  };

  return true;
}

export function reconnectWS(onMessage) {
  return connectWS(onMessage, { force: true });
}

export function disconnectWS() {
  wsGeneration += 1;
  if (wsReconnectTimer) {
    clearTimeout(wsReconnectTimer);
    wsReconnectTimer = null;
  }
  if (wsConnectTimer) {
    clearTimeout(wsConnectTimer);
    wsConnectTimer = null;
  }
  if (wsConn) {
    const staleConn = wsConn;
    wsConn = null;
    staleConn.onopen = null;
    staleConn.onclose = null;
    staleConn.onerror = null;
    staleConn.onmessage = null;
    staleConn.close();
  }
  wsConnected = false;
  wsReconnectAttempt = 0;
}

export function sendWS(msg) {
  if (wsConn && wsConn.readyState === WebSocket.OPEN) {
    wsConn.send(JSON.stringify(msg));
  }
}

// Send a chat message via WebSocket, with REST fallback
export async function wsSendMessage(topicId, content, replyTo, mentions = []) {
  if (wsConn && wsConn.readyState === WebSocket.OPEN) {
    const id = nextMsgId();
    const pub = { id, topic: topicId, content };
    if (replyTo) pub.reply_to = replyTo;
    if (Array.isArray(mentions) && mentions.length > 0) pub.mentions = mentions;
    sendWS({ pub });
    return id;
  }
  // Fallback to REST if WebSocket is not connected
  await api.sendMessage(topicId, content, replyTo, mentions);
  return null;
}

// Send a non-persistent cancel event to stop the active agent turn.
export async function wsSendStreamCancel(topicId, targetBotUid = 0) {
  const streamId = `cancel-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`;
  const normalizedTargetBotUid = Number(targetBotUid);
  if (wsConn && wsConn.readyState === WebSocket.OPEN) {
    const id = nextMsgId();
    sendWS({
      pub: {
        id,
        topic: topicId,
        type: 'stream_cancel',
        msg_type: 'stream_cancel',
        content: '',
        metadata: {
          stream_id: streamId,
          stream_event: 'cancel',
          control: 'interrupt',
          ...(Number.isFinite(normalizedTargetBotUid) && normalizedTargetBotUid > 0
            ? { target_bot_uid: normalizedTargetBotUid }
            : {}),
        },
      },
    });
    return id;
  }
  // Fallback for old/offline transports: visible, but still understood by CatsCo.
  await api.sendMessage(topicId, '停止');
  return null;
}

// Send typing indicator
export function wsSendTyping(topicId) {
  sendWS({ note: { topic: topicId, what: 'kp' } });
}

// Send read receipt
export function wsSendRead(topicId, seqId) {
  sendWS({ note: { topic: topicId, what: 'read', seq: seqId } });
}

export function onWSMessage(handler) {
  msgHandlers.push(handler);
  return () => {
    msgHandlers = msgHandlers.filter((h) => h !== handler);
  };
}
