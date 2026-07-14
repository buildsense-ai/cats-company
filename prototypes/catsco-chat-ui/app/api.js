const LocalChatApi = {
  createSession() {
    return requestJson('/api/new', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: '{}',
    });
  },

  renameSession(sessionId, title) {
    return requestJson('/api/rename/' + encodeURIComponent(sessionId), {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ title }),
    });
  },

  deleteSession(sessionId) {
    return requestJson('/api/delete/' + encodeURIComponent(sessionId), {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: '{}',
    });
  },

  sendChat({ sessionId, messages, model, signal }) {
    return fetch('/api/chat', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ sessionId, messages, model }),
      signal,
    }).catch(error => { throw AppErrors.normalize(error, { context: 'chat-stream' }); });
  },

  getHealth() {
    return requestJson('/api/health', { cache: 'no-store' });
  },
};

const CatsCompanyApi = {
  baseUrl: '/company',

  setBaseUrl(url) {
    this.baseUrl = String(url || '').replace(/\/+$/, '');
  },

  setToken(token) {
    if (token) localStorage.setItem('catsco-token', token);
    else localStorage.removeItem('catsco-token');
  },

  getToken() {
    return localStorage.getItem('catsco-token') || '';
  },

  isAuthenticated() {
    return !!this.getToken();
  },

  request(method, path, body) {
    const headers = { 'Content-Type': 'application/json' };
    const token = this.getToken();
    if (token) headers.Authorization = 'Bearer ' + token;
    return requestJson(this.baseUrl + path, {
      method,
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
    });
  },

  health() { return this.request('GET', '/health'); },
  login(account, password) { return this.request('POST', '/api/auth/login', { account, password }); },
  logout() { this.setToken(''); },
  getMe() { return this.request('GET', '/api/me'); },
  updateMe(displayName, avatarUrl) { return this.request('POST', '/api/me/update', { display_name: displayName, avatar_url: avatarUrl }); },
  getOnlineStatus() { return this.request('GET', '/api/users/online'); },

  getConversations() { return this.request('GET', '/api/conversations'); },
  getMessages(topicId, limit = 50, offset = 0, latest = false) {
    return this.request('GET', '/api/messages?topic_id=' + encodeURIComponent(topicId) + '&limit=' + limit + '&offset=' + offset + (latest ? '&latest=1' : ''));
  },
  sendMessage(topicId, content, replyTo) {
    const payload = { topic_id: topicId };
    if (typeof content === 'string') {
      payload.type = 'text';
      payload.content = content;
    } else if (content && typeof content === 'object') {
      payload.type = content.type || content.msg_type || 'text';
      if (Array.isArray(content.content_blocks) && content.content_blocks.length) payload.content_blocks = content.content_blocks;
      if (typeof content.content === 'string') payload.content = content.content;
      if (content.mode) payload.mode = content.mode;
      if (content.role) payload.role = content.role;
      if (content.metadata) payload.metadata = content.metadata;
    }
    if (replyTo) payload.reply_to = replyTo;
    return this.request('POST', '/api/messages/send', payload);
  },

  getFriends() { return this.request('GET', '/api/friends'); },
  searchUsers(query, mode = 'name') {
    return this.request('GET', '/api/users/search?q=' + encodeURIComponent(query) + '&mode=' + encodeURIComponent(mode));
  },
  getPendingFriends() { return this.request('GET', '/api/friends/pending'); },
  sendFriendRequest(userId, message) { return this.request('POST', '/api/friends/request', { user_id: userId, message }); },
  acceptFriend(userId) { return this.request('POST', '/api/friends/accept', { user_id: userId }); },
  rejectFriend(userId) { return this.request('POST', '/api/friends/reject', { user_id: userId }); },
  blockUser(userId) { return this.request('POST', '/api/friends/block', { user_id: userId }); },
  removeFriend(userId) { return this.request('DELETE', '/api/friends/remove?user_id=' + encodeURIComponent(userId)); },

  getGroups() { return this.request('GET', '/api/groups'); },
  createGroup(name, memberIds = []) { return this.request('POST', '/api/groups/create', { name, member_ids: memberIds }); },
  getGroupInfo(groupId) { return this.request('GET', '/api/groups/info?id=' + encodeURIComponent(groupId)); },
  updateGroup(groupId, name, avatarUrl) { return this.request('POST', '/api/groups/update', { group_id: groupId, name, avatar_url: avatarUrl }); },
  inviteToGroup(groupId, userIds = []) { return this.request('POST', '/api/groups/invite', { group_id: groupId, user_ids: userIds }); },
  leaveGroup(groupId) { return this.request('POST', '/api/groups/leave', { group_id: groupId }); },
  kickGroupMember(groupId, userId) { return this.request('POST', '/api/groups/kick', { group_id: groupId, user_id: userId }); },
  disbandGroup(groupId) { return this.request('POST', '/api/groups/disband', { group_id: groupId }); },
  updateGroupMemberRole(groupId, userId, role) {
    return this.request('POST', '/api/groups/role', { group_id: groupId, user_id: userId, role });
  },
  muteGroupMember(groupId, userId) { return this.request('POST', '/api/groups/mute', { group_id: groupId, user_id: userId }); },
  unmuteGroupMember(groupId, userId) { return this.request('POST', '/api/groups/unmute', { group_id: groupId, user_id: userId }); },
  setGroupAnnouncement(groupId, announcement) {
    return this.request('POST', '/api/groups/announcement', { group_id: groupId, announcement });
  },

  getAgents() { return this.request('GET', '/api/agents'); },
  openAgent(agentUid) { return this.request('POST', '/api/agents/open', { agent_uid: agentUid }); },
  createAgent(data) { return this.request('POST', '/api/bots', data); },
  getAgentEntries(agentUid) { return this.request('GET', '/api/agent-entries?agent_uid=' + encodeURIComponent(agentUid)); },
  createAgentEntry(agentUid, channel, channelAppId = '', accessMode = 'approval_required') {
    return this.request('POST', '/api/agent-entries', {
      agent_uid: agentUid,
      channel,
      access_mode: accessMode,
      ...(channelAppId ? { channel_app_id: channelAppId } : {}),
    });
  },
  regenerateAgentEntry(entryId) {
    return this.request('POST', '/api/agent-entries/' + encodeURIComponent(entryId) + '/regenerate', {});
  },

  getBots() { return this.request('GET', '/api/bots'); },
  getBotApiKey(botId) { return this.request('GET', '/api/bots/api-key?uid=' + encodeURIComponent(botId)); },
  updateBot(botId, data) { return this.request('PATCH', '/api/bots?uid=' + encodeURIComponent(botId), data); },
  deleteBot(botId) { return this.request('DELETE', '/api/bots?uid=' + encodeURIComponent(botId)); },
  setBotVisibility(botId, visibility) {
    return this.request('PATCH', '/api/bots/visibility?uid=' + encodeURIComponent(botId) + '&v=' + encodeURIComponent(visibility));
  },
  getBotFriends(botId) { return this.request('GET', '/api/bots/friends?uid=' + encodeURIComponent(botId)); },
  removeBotFriend(botId, userId) {
    return this.request('DELETE', '/api/bots/friends?uid=' + encodeURIComponent(botId) + '&user_id=' + encodeURIComponent(userId));
  },

  getRelayConfig() { return this.request('GET', '/api/relay/config'); },
  getRelayCommercial() { return this.request('GET', '/api/relay/commercial'); },
  redeemRelayInvite(code) { return this.request('POST', '/api/relay/invite/redeem', { code }); },
  createRelaySession() { return this.request('POST', '/api/relay/session', {}); },
  getRelayKey() { return this.request('GET', '/api/relay/key'); },
  createRelayKey(name = '') { return this.request('POST', '/api/relay/key', name ? { name } : {}); },
  rotateRelayKey() { return this.request('POST', '/api/relay/key/rotate', {}); },
  revealRelayKey() { return this.request('POST', '/api/relay/key/reveal', {}); },
  revokeRelayKey() { return this.request('DELETE', '/api/relay/key'); },
  getRelayUsage(params = {}) {
    const query = new URLSearchParams(params).toString();
    return this.request('GET', '/api/relay/usage' + (query ? '?' + query : ''));
  },

  getDevices() { return this.request('GET', '/api/devices'); },
  unlinkDevice(deviceId) { return this.request('DELETE', '/api/devices/' + encodeURIComponent(deviceId)); },
  getDeviceAudit(limit = 20) { return this.request('GET', '/api/devices/audit?limit=' + encodeURIComponent(limit)); },
  getDesktopReleases() { return this.request('GET', '/api/catsco/desktop-releases'); },
  createDesktopConnectSession() { return this.request('POST', '/api/desktop-connect/session', {}); },
  getDesktopConnectStatus(code) { return this.request('GET', '/api/desktop-connect/status?code=' + encodeURIComponent(code)); },
  createDeviceConnectorPairing(deviceName = '') {
    return this.request('POST', '/api/device-connectors/pairings', {
      device_name: deviceName,
      capabilities: ['read_file', 'resolve_common_directory', 'glob', 'grep'],
    });
  },
  getDeviceConnectorPairing(pairingId) {
    return this.request('GET', '/api/device-connectors/pairings/' + encodeURIComponent(pairingId));
  },

  submitFeedback(data) { return this.request('POST', '/api/feedback', data); },

  async uploadFile(file, purpose = 'chat') {
    const form = new FormData();
    form.append('file', file);
    const headers = {};
    const token = this.getToken();
    if (token) headers.Authorization = 'Bearer ' + token;
    const uploadType = purpose === 'feedback' ? 'feedback' : purpose === 'image' ? 'image' : 'file';
    return requestJson(this.baseUrl + '/api/upload?type=' + encodeURIComponent(uploadType), { method: 'POST', headers, body: form });
  },
};

const ChatApi = LocalChatApi;

async function readJson(res) {
  return AppErrors.fromResponse(res);
}

async function requestJson(url, options) {
  try {
    const res = await fetch(url, options);
    return await readJson(res);
  } catch (error) {
    throw AppErrors.normalize(error, { context: options?.method + ' ' + url });
  }
}
