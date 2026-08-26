import {
  API_BASE,
  authApi,
  getAuthRevision,
  getPushCleanupRegistrationIDs,
  getPushPromptOwner,
  getPushRegistrationID,
  getToken,
  isCurrentAuthSession,
  isTokenExpired,
  request,
  responseErrorMessage,
  setToken as setSessionToken,
} from './auth-session';
import { fetchWithRequestError } from './utils/request-error';

export {
  getAuthRevision,
  getPushCleanupRegistrationIDs,
  getPushPromptOwner,
  getPushRegistrationID,
  getToken,
  isCurrentAuthSession,
  isTokenExpired,
} from './auth-session';

const LOCAL_XIAOBA_BASE = import.meta.env.VITE_XIAOBA_LOCAL_API || '/local-xiaoba';
const DEFAULT_WS_SCHEME = window.location.protocol === 'https:' ? 'wss' : 'ws';
const WS_URL = import.meta.env.VITE_WS_URL || `${DEFAULT_WS_SCHEME}://${window.location.host}/v0/channels`;

let wsConn = null;
let wsReconnectTimer = null;
let wsConnectTimer = null;
let wsStableTimer = null;
let wsGeneration = 0;
let wsReconnectAttempt = 0;
let msgHandlers = [];
let wsConnected = false;
let wsArtifactPreviewSession = null;
let topicLastSeq = {};
let wsActiveTopic = '';
let wsPushSubscriptionID = '';

const WS_RECONNECT_DELAYS = [1000, 2000, 4000, 8000, 15000, 30000];
const WS_CONNECT_TIMEOUT_MS = 10000;
const WS_STABLE_CONNECTION_MS = 10000;
const PUSH_UNSUBSCRIBE_TIMEOUT_MS = 3000;
const DIRECT_REQUEST_TIMEOUT_MS = 15_000;
const ARTIFACT_PREVIEW_SESSION_CONTRACT = 'catsco.artifact-preview-session.v1';

function normalizeArtifactPreviewSession(value) {
  if (!value || typeof value !== 'object'
    || value.contract_version !== ARTIFACT_PREVIEW_SESSION_CONTRACT
    || typeof value.token !== 'string' || !value.token || value.token.length > 1024) return null;
  return {
    contract_version: ARTIFACT_PREVIEW_SESSION_CONTRACT,
    token: value.token,
  };
}

export function toWritableBotSkillRefs(skills) {
  if (!Array.isArray(skills)) return skills;
  return skills.map((skill) => ({
    source: skill?.source,
    skillId: skill?.skillId,
    version: skill?.version,
    contentHash: skill?.contentHash,
  }));
}

function currentPageVisibility() {
  return typeof document !== 'undefined' && document.visibilityState === 'hidden'
    ? 'hidden'
    : 'visible';
}

function isBrowserOnline() {
  return typeof navigator === 'undefined' || navigator.onLine !== false;
}

function normalizePageVisibility(value) {
  return value === 'visible' ? 'visible' : 'hidden';
}

let wsPageVisibility = currentPageVisibility();
let wsPageFocused = currentPageVisibility() === 'visible'
  && (typeof document?.hasFocus !== 'function' || document.hasFocus());

function currentPageFocused() {
  return currentPageVisibility() === 'visible'
    && (typeof document?.hasFocus !== 'function' || document.hasFocus());
}

function wsAttentionPayload() {
  return {
    visibility: wsPageVisibility,
    focused: wsPageFocused,
    active_topic: wsActiveTopic,
    push_subscription_id: wsPushSubscriptionID,
  };
}

export async function pushSubscriptionIDForEndpoint(endpoint) {
  const normalized = String(endpoint || '').trim();
  if (!normalized || !globalThis.crypto?.subtle) return '';
  const digest = await globalThis.crypto.subtle.digest(
    'SHA-256',
    new TextEncoder().encode(normalized),
  );
  let binary = '';
  for (const value of new Uint8Array(digest)) binary += String.fromCharCode(value);
  return btoa(binary).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

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

export function setToken(nextToken) {
  wsArtifactPreviewSession = null;
  if (!nextToken) {
    wsPushSubscriptionID = '';
    wsActiveTopic = '';
  }
  setSessionToken(nextToken);
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

async function localRequest(method, path, body, options = {}) {
  const { timeoutMs = 45_000 } = options;
  const controller = new AbortController();
  const timer = timeoutMs > 0 ? setTimeout(() => controller.abort(), timeoutMs) : null;
  try {
    let response;
    try {
      response = await fetch(`${LOCAL_XIAOBA_BASE}${path}`, {
        method,
        headers: { 'Content-Type': 'application/json' },
        body: body ? JSON.stringify(body) : undefined,
        signal: controller.signal,
      });
    } catch (cause) {
      const error = new Error(
        cause?.name === 'AbortError'
          ? '本地 XiaoBa 请求超时，请确认 Dashboard 和 Connector 正常运行。'
          : '无法连接本地 XiaoBa，请确认 Dashboard 已启动。',
      );
      error.code = cause?.name === 'AbortError' ? 'LOCAL_REQUEST_TIMEOUT' : 'LOCAL_REQUEST_FAILED';
      error.cause = cause;
      throw error;
    }
    let data = {};
    try {
      data = await response.json();
    } catch {
      data = {};
    }
    if (!response.ok) {
      const error = new Error(responseErrorMessage(response.status, data.error));
      error.status = response.status;
      error.data = data;
      throw error;
    }
    return data;
  } finally {
    if (timer) clearTimeout(timer);
  }
}

async function shareLocalSkillWithCatsCo(
  skillName,
  expectedBotUid,
  expectedUserUid,
  { confirmPublish = false } = {},
) {
  const body = {
    skillName,
    expectedBotUid,
    expectedUserUid,
    ...(confirmPublish ? { confirmPublish: true } : {}),
  };
  // A valid local SkillHub cookie may belong to a previously signed-in
  // CatsCo user. Always exchange the current XiaoBa CatsCo identity before
  // publishing so a stale session cannot attribute the Skill to that user.
  await localRequest('POST', '/api/skillhub/auth/catsco', {});
  return localRequest('POST', '/api/skillhub/share-local-skill', body);
}

function hasUsableLocalSkillPayload(response) {
  const skills = Array.isArray(response) ? response : (response?.skills || []);
  return skills.length === 0 || skills.some((skill) => (
    skill && typeof skill === 'object' && Object.keys(skill).length > 0
  ));
}

async function getLocalSkillsWithFallback() {
  try {
    const response = await localRequest('GET', '/api/store');
    if (hasUsableLocalSkillPayload(response)) return response;
  } catch (error) {
    if (error?.status !== 404) throw error;
  }
  return localRequest('GET', '/api/skills-all');
}

const RAW_UPLOAD_QUERY = 'raw=1';
const RAW_UPLOAD_FILE_NAME_HEADER = 'X-CatsCo-File-Name';
const RAW_UPLOAD_FILE_SIZE_HEADER = 'X-CatsCo-File-Size';
const UPLOAD_INCOMPLETE_CODE = 'upload_incomplete';
const UPLOAD_RESPONSE_INTERRUPTED_CODE = 'upload_response_interrupted';
const UPLOAD_TOO_LARGE_CODE = 'upload_too_large';

function rawUploadHeaders(file, authToken = '') {
  const headers = {
    [RAW_UPLOAD_FILE_NAME_HEADER]: encodeURIComponent(file?.name || 'upload'),
    [RAW_UPLOAD_FILE_SIZE_HEADER]: String(file?.size ?? 0),
  };
  if (file?.type) headers['Content-Type'] = file.type;
  if (authToken) headers.Authorization = `Bearer ${authToken}`;
  return headers;
}

function uploadResponseInterruptedMessage(path) {
  if (path.startsWith('/api/mobile-upload/sessions/')) {
    return '上传响应中断，无法确认是否成功；请刷新页面查看“已上传”列表，确认后再重试。';
  }
  return '上传响应中断，无法确认是否成功；请检查网络后重新选择该文件。';
}

async function readUploadResponse(response, path) {
  let raw;
  try {
    raw = await response.text();
  } catch (cause) {
    const error = new Error(uploadResponseInterruptedMessage(path));
    error.code = UPLOAD_RESPONSE_INTERRUPTED_CODE;
    error.status = response.status;
    error.cause = cause;
    throw error;
  }
  let data = {};
  if (raw) {
    try {
      data = JSON.parse(raw);
    } catch {
      if (response.status === 413 || raw.includes('413') || raw.includes('Payload Too Large')) {
        const error = new Error('Payload Too Large');
        error.code = UPLOAD_TOO_LARGE_CODE;
        error.status = response.status;
        throw error;
      }
      const error = new Error(
        response.ok
          ? 'Upload failed: invalid server response'
          : responseErrorMessage(response.status, `Upload failed with HTTP ${response.status}`),
      );
      error.status = response.status;
      throw error;
    }
  }
  if (!response.ok) {
    const message = response.status < 500 && data.code === UPLOAD_INCOMPLETE_CODE
      ? '上传过程中断，请重新选择该文件后重试。'
      : responseErrorMessage(response.status, data.error || `Upload failed with HTTP ${response.status}`);
    const error = new Error(message);
    error.code = data.code || (response.status === 413 ? UPLOAD_TOO_LARGE_CODE : 'upload_failed');
    error.status = response.status;
    error.retryable = data.retryable === true;
    error.data = data;
    throw error;
  }
  return data;
}

async function uploadRawFile(path, file, { authToken = '' } = {}) {
  let attempts = 0;
  while (true) {
    let response;
    try {
      response = await fetch(`${API_BASE}${path}`, {
        method: 'POST',
        headers: rawUploadHeaders(file, authToken),
        body: file,
      });
    } catch (cause) {
      const error = new Error('上传连接中断，请检查网络后重试。');
      error.code = 'upload_network_error';
      error.cause = cause;
      throw error;
    }

    try {
      return await readUploadResponse(response, path);
    } catch (error) {
      const canRetry = attempts === 0
        && error?.code === UPLOAD_INCOMPLETE_CODE
        && error?.retryable === true;
      if (!canRetry) throw error;
      attempts += 1;
    }
  }
}

export const api = {
  ...authApi,
  getMe: () => request('GET', '/api/me'),
  createSTTSession: () => request('POST', '/api/stt/sessions'),
  getRelayAdminAccess: () => request('GET', '/api/admin/relay/access'),
  relayAdminProxyURL: (path) => `/api/admin/relay${path}`,
  getPushConfig: (signal) => request('GET', '/api/push/config', undefined, { signal }),
  subscribePush: (subscription, registrationID, signal) => (
    request('POST', '/api/push/subscriptions', { ...subscription, registration_id: registrationID }, { signal })
  ),
  sendPushTest: (registrationID) => (
    request('POST', '/api/push/test', { registration_id: registrationID })
  ),
  unsubscribePush: (endpoint, authToken = getToken(), registrationID = getPushRegistrationID()) => {
    const controller = new AbortController();
    const timer = window.setTimeout(() => controller.abort(), PUSH_UNSUBSCRIBE_TIMEOUT_MS);
    return request('DELETE', '/api/push/subscriptions', { endpoint, registration_id: registrationID }, {
      authToken,
      signal: controller.signal,
    }).finally(() => window.clearTimeout(timer));
  },
  unsubscribeAllPushRegistrations: (endpoint) => request(
    'DELETE',
    '/api/push/subscriptions',
    { endpoint, all_registrations: true },
  ),
  unsubscribePushRegistration: (registrationID = getPushRegistrationID()) => request(
    'DELETE',
    '/api/push/subscriptions',
    { registration_id: registrationID },
  ),
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

  createArtifactContextSnapshot: (snapshot, options = {}) => {
    const previewSession = wsArtifactPreviewSession;
    return request(
      'POST',
      '/api/artifact-context/snapshots',
      {
        ...snapshot,
        ...(previewSession ? { preview_session: previewSession } : {}),
      },
      options,
    );
  },
  invalidateArtifactContextSnapshot: (contextRef, options = {}) => request(
    'DELETE',
    '/api/artifact-context/snapshots',
    { context_ref: contextRef },
    options,
  ),
  createArtifactTask: (task, options = {}) => {
    const previewSession = wsArtifactPreviewSession;
    return request(
      'POST',
      '/api/artifact-tasks',
      {
        ...task,
        ...(previewSession ? { preview_session: previewSession } : {}),
      },
      options,
    );
  },
  getArtifactTask: (taskId, options = {}) => request(
    'GET',
    `/api/artifact-tasks?task_id=${encodeURIComponent(String(taskId || ''))}`,
    undefined,
    options,
  ),
  failArtifactTask: (taskId, options = {}) => request(
    'DELETE',
    '/api/artifact-tasks',
    { task_id: taskId },
    options,
  ),

  // REST fallback for message history
  getMessages: (topicId, limit, offset, latest = false, beforeId = 0, options = {}) =>
    request(
      'GET',
      `/api/messages?topic_id=${encodeURIComponent(topicId)}&limit=${limit || 50}&offset=${offset || 0}${latest ? '&latest=1' : ''}${beforeId > 0 ? `&before_id=${encodeURIComponent(beforeId)}` : ''}${Number(options.aroundId) > 0 ? `&around_id=${encodeURIComponent(options.aroundId)}` : ''}`,
      undefined,
      options,
    ),
  getMessageSearch: (query, searchType = 'all', options = {}) =>
    request(
      'GET',
      `/api/messages/search?q=${encodeURIComponent(query)}&type=${encodeURIComponent(searchType)}`,
      undefined,
      options,
    ),
  getConversations: () => request('GET', '/api/conversations'),
  setConversationNotificationsMuted: (topicId, muted) => request(
    'PUT',
    '/api/conversations/notification-preferences',
    { topic_id: topicId, muted: Boolean(muted) },
  ),
  getProjects: () => request('GET', '/api/projects'),
  createProject: (name) => request('POST', '/api/projects', { name }),
  renameProject: (projectId, name) => request('PATCH', '/api/projects', { project_id: projectId, name }),
  deleteProject: (projectId) => request('DELETE', `/api/projects?project_id=${encodeURIComponent(projectId)}`),
  assignProjectTopic: (projectId, topicId) => request('POST', '/api/projects/topic', { project_id: projectId, topic_id: topicId }),
  removeProjectTopic: (topicId) => request('DELETE', `/api/projects/topic?topic_id=${encodeURIComponent(topicId)}`),
  updateConversationTitle: (topicId, name) => request('PATCH', '/api/conversations', { topic_id: topicId, name }),
  getRelayConfig: (options = {}) => request('GET', '/api/relay/config', undefined, options),
  getRelayCommercial: (options = {}) => request('GET', '/api/relay/commercial', undefined, options),
  redeemRelayInvite: (code) => request('POST', '/api/relay/invite/redeem', { code }),
  getCommercialCatalog: (options = {}) => request('GET', '/api/relay/commercial/catalog', undefined, options),
  getCommercialOrders: (orderNo = '', options = {}) => request('GET', `/api/relay/commercial/orders${orderNo ? `?order_no=${encodeURIComponent(orderNo)}` : ''}`, undefined, options),
  createCommercialOrder: (planId, channel, clientRequestId, options = {}) => request('POST', '/api/relay/commercial/orders', {
    plan_id: planId,
    channel,
    client_request_id: clientRequestId,
  }, options),
  cancelCommercialOrder: (orderNo, options = {}) => request('POST', '/api/relay/commercial/orders/cancel', { order_no: orderNo }, options),
  confirmCommercialTestPayment: (orderNo) => request('POST', '/api/relay/commercial/orders/test-confirm', { order_no: orderNo }),
  claimCommercialTrial: () => request('POST', '/api/relay/commercial/trial/claim', {}),
  createRelaySession: () => request('POST', '/api/relay/session', {}),
  getRelayKey: (options = {}) => request('GET', '/api/relay/key', undefined, options),
  getRelayUsage: ({ model, source, scope } = {}) => {
    const params = new URLSearchParams();
    if (model) params.set('model', model);
    if (source) params.set('source', source);
    if (scope) params.set('scope', scope);
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
      capabilities: [
        'read_file',
        'resolve_common_directory',
        'glob',
        'grep',
        'skillhub.localWorkspace.get',
        'skillhub.localSkill.share',
        'skillhub.localSkill.finalize',
        'skillhub.localBot.switch',
      ],
    }),
  getDeviceConnectorPairing: (pairingId) =>
    request('GET', `/api/device-connectors/pairings/${encodeURIComponent(pairingId)}`),
  getCatsCoDesktopReleases: () => request('GET', '/api/catsco/desktop-releases'),
  getDevices: (options = {}) => request('GET', '/api/devices', undefined, options),
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
  getBotInviteCode: (uid) => request('GET', `/api/bots/invite-code?uid=${uid}`),
  generateBotInviteCode: (uid) => request('POST', `/api/bots/invite-code?uid=${uid}`, {}),
  revokeBotInviteCode: (uid) => request('DELETE', `/api/bots/invite-code?uid=${uid}`, {}),
  redeemBotInviteCode: (code) => request('POST', '/api/bots/invite/redeem', { code }),
  createBot: ({ username, display_name, role, description }) =>
    request('POST', '/api/bots', { username, display_name, role, description }),

  // Cloud virtual employee control plane (云托管)
  getCloudWorkers: () => request('GET', '/api/cloud-workers'),
  getCloudWorkerMeta: () => request('GET', '/api/cloud-workers/meta'),
  createCloudWorker: ({ username, display_name, role, description }) =>
    request('POST', '/api/cloud-workers', { username, display_name, role, description }, { timeoutMs: 630_000 }),
  updateCloudWorker: (name, payload = {}) =>
    request('POST', `/api/cloud-workers/${encodeURIComponent(name)}/update`, payload, { timeoutMs: 630_000 }),
  rollbackCloudWorker: (name, payload = {}) =>
    request('POST', `/api/cloud-workers/${encodeURIComponent(name)}/rollback`, payload, { timeoutMs: 630_000 }),
  resetCloudWorker: (name, payload = {}) =>
    request('POST', `/api/cloud-workers/${encodeURIComponent(name)}/reset`, payload, { timeoutMs: 630_000 }),
  deleteCloudWorker: (name) =>
    request('DELETE', `/api/cloud-workers/${encodeURIComponent(name)}`, {}, { timeoutMs: 630_000 }),
  updateBot: (uid, {
    display_name,
    avatar_url,
    role,
    description,
    artifact_upload_enabled,
    skill_mutation_mode,
  }) =>
    request('PATCH', `/api/bots?uid=${uid}`, {
      display_name,
      avatar_url,
      role,
      description,
      artifact_upload_enabled,
      skill_mutation_mode,
    }),
  deleteBot: (uid) => request('DELETE', `/api/bots?uid=${uid}`),
  setBotVisibility: (uid, visibility) => request('PATCH', `/api/bots/visibility?uid=${uid}&v=${visibility}`),
  setBotSkillsVisibility: (uid, visibility) => request(
    'PATCH',
    `/api/bots/skills-visibility?uid=${encodeURIComponent(uid)}&v=${encodeURIComponent(visibility)}`,
  ),
  getBotModelConfig: (uid, { includeUsage = false } = {}) =>
    request('GET', `/api/bots/model-config?uid=${uid}${includeUsage ? '&include_usage=1' : ''}`),
  updateBotModelConfig: (uid, modelConfig) => request('PATCH', `/api/bots/model-config?uid=${uid}`, modelConfig),
  getBotDefinitionSkills: (uid) => request('GET', `/api/bots/definition/skills?uid=${encodeURIComponent(uid)}`),
  getBotDefinitionPrompt: (uid) => request(
    'GET',
    `/api/bots/definition?uid=${encodeURIComponent(uid)}`,
  ),
  getAgentPrompt: (uid) => request(
    'GET',
    `/api/agents/prompt?uid=${encodeURIComponent(uid)}`,
  ),
  updateBotDefinitionPrompt: (uid, revision, prompt) => request(
    'PATCH',
    `/api/bots/definition/prompt?uid=${encodeURIComponent(uid)}`,
    { revision, prompt },
  ),
  updateBotPromptVisibility: (uid, visibility) => request(
    'PATCH',
    `/api/bots/definition/prompt-visibility?uid=${encodeURIComponent(uid)}`,
    { prompt_visibility: visibility },
  ),
  getAgentSkills: (uid) => request('GET', `/api/agents/skills?uid=${encodeURIComponent(uid)}`),
  getAgentSkillVersions: (uid, skillId, { limit = 20, beforeRevision = 0 } = {}) => {
    const params = new URLSearchParams({
      uid: String(uid),
      skill_id: String(skillId),
      limit: String(limit),
    });
    if (beforeRevision > 0) params.set('before_revision', String(beforeRevision));
    return request('GET', `/api/agents/skill-versions?${params}`);
  },
  updateBotDefinitionSkills: (uid, revision, skills) => request(
    'PATCH',
    `/api/bots/definition/skills?uid=${encodeURIComponent(uid)}`,
    { revision, skills: toWritableBotSkillRefs(skills) },
  ),
  switchLocalBot: (botUid) => localRequest('POST', '/api/cats/switch-bot', { botUid }),
  getLocalCatsStatus: () => localRequest('GET', '/api/cats/status'),
  getLocalSkills: getLocalSkillsWithFallback,
  getLocalStatusDetails: () => localRequest('GET', '/api/status/details'),
  shareLocalSkill: shareLocalSkillWithCatsCo,
  searchSkillHubSkills: (query = '', options = {}) => {
    const params = new URLSearchParams();
    if (query) params.set('q', query);
    if (options.category) params.set('category', options.category);
    if (options.searchMode === 'name') params.set('search_mode', 'name');
    const suffix = params.toString() ? `?${params}` : '';
    return request('GET', `/api/skillhub/skills${suffix}`);
  },
  getSkillHubSkill: (skillId, options = {}) => request(
    'GET',
    `/api/skillhub/skills/${encodeSkillHubID(skillId)}`,
    undefined,
    options,
  ),
  getSkillHubVersions: (skillId, options = {}) => request(
    'GET',
    `/api/skillhub/skills/${encodeSkillHubID(skillId)}/versions`,
    undefined,
    options,
  ),
  getSkillHubVersion: (skillId, version, options = {}) => request(
    'GET',
    `/api/skillhub/skills/${encodeSkillHubID(skillId)}/versions/${encodeURIComponent(version)}`,
    undefined,
    options,
  ),
  getBotFriends: (uid) => request('GET', `/api/bots/friends?uid=${uid}`),
  removeBotFriend: (uid, userId) => request('DELETE', `/api/bots/friends?uid=${uid}&user_id=${userId}`),
  acceptFriendAsBot: async (apiKey, userId) => {
    const res = await fetchWithRequestError(`${API_BASE}/api/friends/accept`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        Authorization: `ApiKey ${apiKey}`,
      },
      body: JSON.stringify({ user_id: userId }),
      timeoutMs: DIRECT_REQUEST_TIMEOUT_MS,
    });
    let data = {};
    try {
      data = await res.json();
    } catch (cause) {
      if (res.ok) throw cause;
    }
    if (!res.ok) {
      const error = new Error(responseErrorMessage(res.status, data.error || 'Request failed'));
      error.status = res.status;
      error.data = data;
      throw error;
    }
    return data;
  },
  uploadFile: async (file, type = 'file') => {
    return uploadRawFile(`/api/upload?type=${type}&${RAW_UPLOAD_QUERY}`, file, { authToken: getToken() });
  },
  createMobileUploadSession: async (topic) => request('POST', '/api/mobile-upload/sessions', { topic }),
  getMobileUploadSession: async (sessionId) => request('GET', `/api/mobile-upload/sessions/${encodeURIComponent(sessionId)}`),
  uploadMobileSessionFile: (sessionId, file, type = 'file') => uploadRawFile(
    `/api/mobile-upload/sessions/${encodeURIComponent(sessionId)}/files?type=${type}&${RAW_UPLOAD_QUERY}`,
    file,
  ),
  uploadFeedbackImage: (file) => api.uploadFile(file, 'feedback'),
  submitFeedback: (data) => request('POST', '/api/feedback', data),
  getTutorialTasks: () => request('GET', '/api/tutorial-tasks'),
  getCloudArtifacts: (agentUid, status = 'active', options = {}) =>
    request(
      'GET',
      `/api/agents/${encodeURIComponent(agentUid)}/artifacts?status=${encodeURIComponent(status)}`,
      undefined,
      options,
    ),
  publishCloudArtifact: (agentUid, artifact) =>
    request('POST', `/api/agents/${encodeURIComponent(agentUid)}/artifacts`, artifact),
  getTopicFiles: (topicId, { beforeId = 0, beforeCreatedAt = '', limit = 40 } = {}) => {
    const params = new URLSearchParams();
    params.set('limit', String(limit));
    if (beforeId > 0) params.set('before_id', String(beforeId));
    if (beforeCreatedAt) params.set('before_created_at', String(beforeCreatedAt));
    return request('GET', `/api/topics/${encodeURIComponent(topicId)}/files?${params.toString()}`);
  },
  getAgentFiles: (agentUid, { topicId, beforeId = 0, beforeCreatedAt = '', limit = 40 } = {}) => {
    const params = new URLSearchParams();
    params.set('topic_id', String(topicId || ''));
    params.set('limit', String(limit));
    if (beforeId > 0) params.set('before_id', String(beforeId));
    if (beforeCreatedAt) params.set('before_created_at', String(beforeCreatedAt));
    return request('GET', `/api/agents/${encodeURIComponent(agentUid)}/files?${params.toString()}`);
  },
  deleteCloudArtifact: (agentUid, artifactId) =>
    request('DELETE', `/api/agents/${encodeURIComponent(agentUid)}/artifacts/${encodeURIComponent(artifactId)}`),
  restoreCloudArtifact: (agentUid, artifactId) =>
    request('POST', `/api/agents/${encodeURIComponent(agentUid)}/artifacts/${encodeURIComponent(artifactId)}/restore`, {}),
};

function encodeSkillHubID(skillId) {
  return String(skillId || '')
    .split('/')
    .filter(Boolean)
    .map((part) => encodeURIComponent(part))
    .join('/');
}

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
  const sessionToken = getToken();
  if (!sessionToken) return false;
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
  if (wsStableTimer) {
    clearTimeout(wsStableTimer);
    wsStableTimer = null;
  }
  const generation = ++wsGeneration;
  if (wsConn) {
    const staleConn = wsConn;
    wsConn = null;
    const closeMessage = { _type: 'ws_close', attempt: wsReconnectAttempt, retryInMs: 0 };
    onMessage(closeMessage);
    msgHandlers.forEach((handler) => handler(closeMessage));
    staleConn.onopen = null;
    staleConn.onclose = null;
    staleConn.onerror = null;
    staleConn.onmessage = null;
    staleConn.close();
  }
  wsConnected = false;
  wsArtifactPreviewSession = null;
  const url = `${WS_URL}?token=${sessionToken}`;
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
    if (isBrowserOnline()) {
      wsReconnectTimer = setTimeout(() => {
        if (wsGeneration === generation) connectWS(onMessage);
      }, retryInMs);
    }
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
    // Do not immediately reset the backoff. A socket which opens and drops
    // again right away is still an unstable connection and should not cause
    // a tight one-second reconnect loop. Reset after a stable open period.
    wsStableTimer = setTimeout(() => {
      if (isCurrent() && wsConnected) wsReconnectAttempt = 0;
      wsStableTimer = null;
    }, WS_STABLE_CONNECTION_MS);
    wsPageVisibility = currentPageVisibility();
    wsPageFocused = currentPageFocused();
    // Send handshake
    sendWS({ hi: {
      id: nextMsgId(),
      ver: '0.1.0',
      ...wsAttentionPayload(),
    } });
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
    if (wsStableTimer) {
      clearTimeout(wsStableTimer);
      wsStableTimer = null;
    }
    console.log('WebSocket disconnected');
    wsConnected = false;
    wsArtifactPreviewSession = null;
    wsConn = null;
    wsReconnectAttempt += 1;
    const retryInMs = reconnectDelay(wsReconnectAttempt);
    const closeMessage = { _type: 'ws_close', attempt: wsReconnectAttempt, retryInMs };
    onMessage(closeMessage);
    msgHandlers.forEach((handler) => handler(closeMessage));
    if (isTokenExpired()) {
      const authExpiredMessage = { _type: 'ws_auth_expired' };
      onMessage(authExpiredMessage);
      msgHandlers.forEach((handler) => handler(authExpiredMessage));
      return;
    }
    if (getToken() && isBrowserOnline()) {
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
      const previewSession = normalizeArtifactPreviewSession(
        msg?.ctrl?.params?.artifact_preview_session,
      );
      if (previewSession) wsArtifactPreviewSession = previewSession;
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
  if (wsStableTimer) {
    clearTimeout(wsStableTimer);
    wsStableTimer = null;
  }
  if (wsConn) {
    const staleConn = wsConn;
    wsConn = null;
    const closeMessage = { _type: 'ws_close', attempt: 0, retryInMs: 0 };
    msgHandlers.forEach((handler) => handler(closeMessage));
    staleConn.onopen = null;
    staleConn.onclose = null;
    staleConn.onerror = null;
    staleConn.onmessage = null;
    staleConn.close();
  }
  wsConnected = false;
  wsArtifactPreviewSession = null;
  wsReconnectAttempt = 0;
}

export function sendWS(msg) {
  if (wsConn && wsConn.readyState === WebSocket.OPEN) {
    wsConn.send(JSON.stringify(msg));
    return true;
  }
  return false;
}

export function requestSkillHubDeviceTool({
  deviceId,
  ownerUserId,
  toolName,
  payload,
  timeoutMs = 30_000,
}) {
  const requestId = globalThis.crypto?.randomUUID?.()
    || `skillhub-${Date.now()}-${Math.random().toString(36).slice(2, 12)}`;
  const messageId = nextMsgId();
  const requestTimeoutMs = Math.max(1, Math.min(Number(timeoutMs) || 30_000, 120_000));
  const expiresAt = Date.now() + requestTimeoutMs;

  return new Promise((resolve, reject) => {
    let settled = false;
    let removeHandler = () => {};
    const finish = (callback) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      removeHandler();
      callback();
    };
    const timer = setTimeout(() => finish(() => {
      const error = new Error('等待本地 XiaoBa 响应超时，请确认设备在线并已更新到最新版本。');
      error.code = 'skillhub_device_timeout';
      reject(error);
    }), requestTimeoutMs);

    removeHandler = onWSMessage((message) => {
      if (message?._type === 'ws_close' || message?._type === 'ws_auth_expired') {
        finish(() => {
          const error = new Error('CatsCo 实时连接已断开，请等待重连后再试。');
          error.code = 'skillhub_websocket_disconnected';
          reject(error);
        });
        return;
      }
      if (message?.ctrl?.id === messageId && Number(message.ctrl.code || 0) >= 400) {
        finish(() => {
          const error = new Error(message.ctrl.text || 'CatsCo 拒绝了本地设备请求。');
          error.code = 'skillhub_device_request_rejected';
          error.status = Number(message.ctrl.code || 0);
          reject(error);
        });
        return;
      }
      const response = message?.thin_tool_rpc;
      if (!response || response.type !== 'result' || response.request_id !== requestId) return;
      finish(() => {
        if (response.error) {
          const error = new Error(response.error.message || response.error.code || '本地 XiaoBa 操作失败。');
          error.code = response.error.code || 'skillhub_device_error';
          reject(error);
          return;
        }
        resolve(response.result || {});
      });
    });

    const sent = sendWS({
      thin_tool_rpc: {
        id: messageId,
        type: 'request',
        request_id: requestId,
        target_owner_user_id: String(ownerUserId || ''),
        target_device_id: String(deviceId || ''),
        tool_name: String(toolName || ''),
        payload: payload && typeof payload === 'object' ? payload : {},
        expires_at: expiresAt,
      },
    });
    if (!sent) {
      finish(() => {
        const error = new Error('CatsCo 实时连接尚未建立，请稍后重试。');
        error.code = 'skillhub_websocket_unavailable';
        reject(error);
      });
    }
  });
}

export function sendWSPageVisibility(visibility = currentPageVisibility()) {
  wsPageVisibility = normalizePageVisibility(visibility);
  if (wsPageVisibility === 'hidden') wsPageFocused = false;
  sendWS({ note: { what: 'attention', ...wsAttentionPayload() } });
}

export function sendWSPageFocus(focused = currentPageFocused()) {
  wsPageFocused = Boolean(focused) && wsPageVisibility === 'visible';
  sendWS({ note: { what: 'attention', ...wsAttentionPayload() } });
}

export function sendWSActiveTopic(topic = '') {
  wsActiveTopic = String(topic || '').trim();
  sendWS({ note: { what: 'attention', ...wsAttentionPayload() } });
}

export async function setWSPushSubscriptionEndpoint(endpoint = '') {
  const subscriptionID = await pushSubscriptionIDForEndpoint(endpoint);
  wsPushSubscriptionID = subscriptionID;
  sendWS({ note: { what: 'attention', ...wsAttentionPayload() } });
  return subscriptionID;
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

export function wsSendArtifactResultReceipt(receipt) {
  return sendWS({ artifact_result: receipt });
}

export function onWSMessage(handler) {
  msgHandlers.push(handler);
  return () => {
    msgHandlers = msgHandlers.filter((h) => h !== handler);
  };
}
