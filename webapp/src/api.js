const API_BASE = process.env.REACT_APP_API_BASE || '';
const DEFAULT_WS_SCHEME = window.location.protocol === 'https:' ? 'wss' : 'ws';
const WS_URL = process.env.REACT_APP_WS_URL || `${DEFAULT_WS_SCHEME}://${window.location.host}/v0/channels`;

let token = localStorage.getItem('oc_token');
let wsConn = null;
let wsReconnectTimer = null;
let wsGeneration = 0;
let msgHandlers = [];
let wsConnected = false;
let topicLastSeq = {};

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

export function resolveMediaURL(url) {
  if (!url) return '';
  if (/^https?:\/\//.test(url)) return url;
  return `${API_BASE}${url}`;
}

export function isWSConnected() {
  return wsConnected;
}

async function request(method, path, body) {
  const headers = { 'Content-Type': 'application/json' };
  if (token) headers['Authorization'] = `Bearer ${token}`;

  const res = await fetch(`${API_BASE}${path}`, {
    method,
    headers,
    body: body ? JSON.stringify(body) : undefined,
  });

  const data = await res.json();
  if (!res.ok) throw new Error(data.error || 'Request failed');
  return data;
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
  getPendingRequests: () => request('GET', '/api/friends/pending'),
  sendFriendRequest: (userId, message) =>
    request('POST', '/api/friends/request', { user_id: userId, message }),
  acceptFriend: (userId) =>
    request('POST', '/api/friends/accept', { user_id: userId }),
  rejectFriend: (userId) =>
    request('POST', '/api/friends/reject', { user_id: userId }),
  blockUser: (userId) =>
    request('POST', '/api/friends/block', { user_id: userId }),
  removeFriend: (userId) =>
    request('DELETE', `/api/friends/remove?user_id=${userId}`),

  searchUsers: (q) => request('GET', `/api/users/search?q=${encodeURIComponent(q)}`),

  // Send message via REST
  sendMessage: (topicId, content, replyTo) => {
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
    return request('POST', '/api/messages/send', payload);
  },

  // REST fallback for message history
  getMessages: (topicId, limit, offset, latest = false) =>
    request('GET', `/api/messages?topic_id=${encodeURIComponent(topicId)}&limit=${limit || 50}&offset=${offset || 0}${latest ? '&latest=1' : ''}`),
  getConversations: () => request('GET', '/api/conversations'),
  getRelayConfig: () => request('GET', '/api/relay/config'),
  createRelaySession: () => request('POST', '/api/relay/session', {}),
  getRelayKey: () => request('GET', '/api/relay/key'),
  createRelayKey: (name) => request('POST', '/api/relay/key', name ? { name } : {}),
  rotateRelayKey: () => request('POST', '/api/relay/key/rotate', {}),
  revokeRelayKey: () => request('DELETE', '/api/relay/key'),

  getOnlineStatus: () => request('GET', '/api/users/online'),

  // Virtual employee roster
  getAgents: () => request('GET', '/api/agents'),
  openAgent: (agentUid) => request('POST', '/api/agents/open', { agent_uid: agentUid }),

  // Groups
  createGroup: (name, memberIds) => request('POST', '/api/groups/create', { name, member_ids: memberIds }),
  getGroups: () => request('GET', '/api/groups'),
  getGroupInfo: (groupId) => request('GET', `/api/groups/info?id=${groupId}`),
  updateGroup: (groupId, name, avatarUrl) =>
    request('POST', '/api/groups/update', { group_id: groupId, name, avatar_url: avatarUrl }),
  inviteToGroup: (groupId, userIds) => request('POST', '/api/groups/invite', { group_id: groupId, user_ids: userIds }),
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
  getBotFriends: (uid) => request('GET', `/api/bots/friends?uid=${uid}`),
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
    const data = await res.json();
    if (!res.ok) throw new Error(data.error || 'Upload failed');
    return data;
  },
  uploadFeedbackImage: (file) => api.uploadFile(file, 'feedback'),
  submitFeedback: (data) => request('POST', '/api/feedback', data),
};

// --- WebSocket ---

let _msgIdCounter = 0;
function nextMsgId() {
  return String(++_msgIdCounter);
}

export function connectWS(onMessage) {
  if (wsReconnectTimer) {
    clearTimeout(wsReconnectTimer);
    wsReconnectTimer = null;
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
  if (!token) return;

  const url = `${WS_URL}?token=${token}`;
  const conn = new WebSocket(url);
  wsConn = conn;
  const isCurrent = () => wsConn === conn && wsGeneration === generation;

  conn.onopen = () => {
    if (!isCurrent()) {
      conn.close();
      return;
    }
    console.log('WebSocket connected');
    wsConnected = true;
    // Send handshake
    sendWS({ hi: { id: nextMsgId(), ver: '0.1.0' } });
    // Request online status of friends
    sendWS({ get: { id: nextMsgId(), topic: 'me', what: 'online' } });
    // Request missed messages for all tracked topics
    Object.keys(topicLastSeq).forEach((tid) => {
      requestMissedMessages(tid);
    });
    onMessage({ _type: 'ws_open' });
  };

  conn.onclose = () => {
    if (!isCurrent()) return;
    console.log('WebSocket disconnected');
    wsConnected = false;
    wsConn = null;
    onMessage({ _type: 'ws_close' });
    // Reconnect after 3s
    if (token) {
      wsReconnectTimer = setTimeout(() => {
        if (wsGeneration === generation) {
          connectWS(onMessage);
        }
      }, 3000);
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
}

export function disconnectWS() {
  wsGeneration += 1;
  if (wsReconnectTimer) {
    clearTimeout(wsReconnectTimer);
    wsReconnectTimer = null;
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
}

export function sendWS(msg) {
  if (wsConn && wsConn.readyState === WebSocket.OPEN) {
    wsConn.send(JSON.stringify(msg));
  }
}

// Send a chat message via WebSocket, with REST fallback
export async function wsSendMessage(topicId, content, replyTo) {
  if (wsConn && wsConn.readyState === WebSocket.OPEN) {
    const id = nextMsgId();
    const pub = { id, topic: topicId, content };
    if (replyTo) pub.reply_to = replyTo;
    sendWS({ pub });
    return id;
  }
  // Fallback to REST if WebSocket is not connected
  await api.sendMessage(topicId, content);
  return null;
}

// Send a non-persistent cancel event to stop the active agent turn.
export async function wsSendStreamCancel(topicId) {
  const streamId = `cancel-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`;
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
