import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { api, requestSkillHubDeviceTool } from '../api';
import { useFeedback } from '../components/feedback-system';
import SkillHubContent from './skillhub-content';
import '../css/skillhub-view.css';

const SKILLHUB_DEVICE_TOOLS = {
  workspace: 'skillhub.localWorkspace.get',
  share: 'skillhub.localSkill.share',
  finalize: 'skillhub.localSkill.finalize',
  switchBot: 'skillhub.localBot.switch',
};

const SKILLHUB_DEVICE_CAPABILITIES = Object.values(SKILLHUB_DEVICE_TOOLS);
const SKILLHUB_DEVICE_SCHEMAS = {
  [SKILLHUB_DEVICE_TOOLS.workspace]: 'xiaoba.skillhub.local_workspace.v1',
  [SKILLHUB_DEVICE_TOOLS.share]: 'xiaoba.skillhub.local_share.v1',
  [SKILLHUB_DEVICE_TOOLS.finalize]: 'xiaoba.skillhub.local_finalize.v1',
  [SKILLHUB_DEVICE_TOOLS.switchBot]: 'xiaoba.skillhub.bot_switch.v1',
};

const SKILLHUB_SELECTED_BOT_STORAGE_PREFIX = 'catsco.skillhub.selectedBot';
const SKILLHUB_SWITCH_RETRY_ATTEMPTS = 40;
const SKILLHUB_SWITCH_TIMEOUT_MS = 60_000;
const SKILLHUB_SWITCH_INITIAL_DELAY_MS = 2_000;
const SKILLHUB_SWITCH_RETRY_DELAY_MS = 1_500;
const SKILLHUB_DEVICE_LIST_TIMEOUT_MS = 5_000;
const SKILLHUB_WORKSPACE_TIMEOUT_MS = 8_000;
const RETRYABLE_SKILLHUB_SWITCH_ERRORS = new Set([
  'BOT_NOT_ACTIVE',
  'REQUEST_EXPIRED',
  'SHUTTING_DOWN',
  'device_rpc_timeout',
  'skillhub_device_timeout',
  'skillhub_websocket_disconnected',
  'skillhub_websocket_unavailable',
  'target_device_unavailable',
]);
const RETRYABLE_SKILLHUB_DEVICE_LIST_ERRORS = new Set([
  'NETWORK_ERROR',
  'REQUEST_TIMEOUT',
]);
const RETRYABLE_SKILLHUB_DEVICE_LIST_STATUSES = new Set([500, 502, 503, 504]);

const wait = (delayMs) => new Promise((resolve) => setTimeout(resolve, delayMs));

function runWithTimeout(operation, timeoutMs, createTimeoutError) {
  let result;
  try {
    result = operation();
  } catch (error) {
    return Promise.reject(error);
  }
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(createTimeoutError()), timeoutMs);
    Promise.resolve(result).then(
      (value) => {
        clearTimeout(timer);
        resolve(value);
      },
      (error) => {
        clearTimeout(timer);
        reject(error);
      },
    );
  });
}

function skillHubDeviceListTimeoutError() {
  const error = new Error('请求设备列表超时，请稍后重试');
  error.code = 'REQUEST_TIMEOUT';
  return error;
}

function skillHubWorkspaceTimeoutError() {
  const error = new Error('等待本地 XiaoBa 响应超时，请确认设备在线并已更新到最新版本。');
  error.code = 'skillhub_device_timeout';
  return error;
}

export function normalizeSkillHubDevices(response) {
  const devices = Array.isArray(response) ? response : (response?.devices || []);
  return devices.filter((device) => (
    device?.active === true
    && device?.routeConnected === true
    && device?.routable === true
    && Array.isArray(device?.capabilities)
    && SKILLHUB_DEVICE_CAPABILITIES.every((capability) => device.capabilities.includes(capability))
  ));
}

export function normalizeOwnedBots(response, userUid) {
  const bots = Array.isArray(response) ? response : (response?.bots || []);
  return bots.filter((bot) => {
    if (bot?.relation) return bot.relation === 'owner';
    if (bot?.is_owner !== undefined) return Boolean(bot.is_owner);
    const ownerUID = Number(bot?.owner_id || bot?.owner_uid || 0);
    return ownerUID > 0 && ownerUID === Number(userUid);
  });
}

function selectedBotStorageKey(userUid) {
  const uid = String(userUid || '').trim();
  return uid ? `${SKILLHUB_SELECTED_BOT_STORAGE_PREFIX}.${uid}` : '';
}

function browserStorage() {
  try {
    return globalThis.localStorage;
  } catch {
    return null;
  }
}

export function readRememberedSkillHubBotUID(userUid, storage = browserStorage()) {
  const key = selectedBotStorageKey(userUid);
  if (!key || !storage) return '';
  try {
    return String(storage.getItem(key) || '').trim();
  } catch {
    return '';
  }
}

export function rememberSkillHubBotUID(userUid, botUid, storage = browserStorage()) {
  const key = selectedBotStorageKey(userUid);
  const uid = String(botUid || '').trim();
  if (!key || !storage) return;
  try {
    if (uid) storage.setItem(key, uid);
    else storage.removeItem(key);
  } catch {
    // Storage can be unavailable in hardened or private browser contexts.
  }
}

export function resolvePreferredSkillHubBotUID(bots, userUid, storage = browserStorage()) {
  const remembered = readRememberedSkillHubBotUID(userUid, storage);
  if (remembered && bots.some((bot) => String(botUID(bot)) === remembered)) return remembered;
  const firstUID = botUID(bots[0]);
  return firstUID ? String(firstUID) : '';
}

export function isRetryableSkillHubSwitchError(error) {
  if (RETRYABLE_SKILLHUB_SWITCH_ERRORS.has(String(error?.code || ''))) return true;
  return error?.code === 'skillhub_device_request_rejected'
    && [404, 409, 503].includes(Number(error?.status || 0));
}

export function isRetryableSkillHubDeviceListError(error) {
  const status = Number(error?.status || 0);
  if (status > 0) return RETRYABLE_SKILLHUB_DEVICE_LIST_STATUSES.has(status);
  return RETRYABLE_SKILLHUB_DEVICE_LIST_ERRORS.has(String(error?.code || ''));
}

export async function waitForSkillHubWorkspaceAfterSwitch({
  deviceId,
  readWorkspace,
  getDevices = api.getDevices,
  isCurrent = () => true,
  waitFor = wait,
  maxAttempts = SKILLHUB_SWITCH_RETRY_ATTEMPTS,
  timeoutMs = SKILLHUB_SWITCH_TIMEOUT_MS,
  initialDelayMs = SKILLHUB_SWITCH_INITIAL_DELAY_MS,
  retryDelayMs = SKILLHUB_SWITCH_RETRY_DELAY_MS,
  deviceListTimeoutMs = SKILLHUB_DEVICE_LIST_TIMEOUT_MS,
  workspaceTimeoutMs = SKILLHUB_WORKSPACE_TIMEOUT_MS,
  now = () => Date.now(),
}) {
  const deadline = now() + Math.max(1, Number(timeoutMs) || SKILLHUB_SWITCH_TIMEOUT_MS);
  const remainingMs = () => Math.max(0, deadline - now());
  let lastError;
  for (let attempt = 0; attempt < maxAttempts; attempt += 1) {
    const delayMs = Math.min(
      attempt === 0 ? initialDelayMs : retryDelayMs,
      remainingMs(),
    );
    if (delayMs <= 0) break;
    await waitFor(delayMs);
    if (!isCurrent()) return null;
    if (remainingMs() <= 0) break;

    try {
      const requestTimeoutMs = Math.min(deviceListTimeoutMs, remainingMs());
      const capable = normalizeSkillHubDevices(await runWithTimeout(
        () => getDevices({ timeoutMs: requestTimeoutMs }),
        requestTimeoutMs,
        skillHubDeviceListTimeoutError,
      ));
      const routeReady = capable.some((device) => String(device.deviceId || '') === String(deviceId || ''));
      if (!routeReady) continue;
    } catch (error) {
      if (!isRetryableSkillHubDeviceListError(error)) throw error;
      lastError = error;
      continue;
    }

    try {
      const requestTimeoutMs = Math.min(workspaceTimeoutMs, remainingMs());
      return await runWithTimeout(
        () => readWorkspace(requestTimeoutMs),
        requestTimeoutMs,
        skillHubWorkspaceTimeoutError,
      );
    } catch (error) {
      if (!isRetryableSkillHubSwitchError(error)) throw error;
      lastError = error;
    }
  }
  if (!isCurrent()) return null;
  const error = new Error('本地 XiaoBa 切换超时，请确认 XiaoBa 仍在运行后重试。');
  error.code = 'skillhub_device_switch_timeout';
  error.cause = lastError;
  throw error;
}

export function normalizeSkillHubSkills(response) {
  const values = Array.isArray(response)
    ? response
    : (response?.skills || response?.items || response?.results || []);
  return values.map((skill) => ({
    ...skill,
    skillId: String(skill?.skillId || skill?.skill_id || skill?.id || '').trim(),
    displayName: String(skill?.displayName || skill?.display_name || skill?.name || skill?.skillId || skill?.id || '').trim(),
    description: String(skill?.description || '').trim(),
    author: String(skill?.author?.displayName || skill?.author?.name || skill?.author || skill?.publisher || '').trim(),
    latestVersion: String(skill?.latestVersion || skill?.latest_version || skill?.version || '').trim(),
    contentHash: String(skill?.contentHash || skill?.content_hash || skill?.sha256 || '').trim().toLowerCase(),
  })).filter((skill) => skill.skillId);
}

export function normalizeLocalSkills(response) {
  const values = Array.isArray(response) ? response : (response?.skills || []);
  return values.map((skill) => ({
    ...skill,
    name: String(skill?.name || skill?.folder || '').trim(),
    description: String(skill?.description || '').trim(),
    path: String(skill?.path || '').trim(),
    relativePath: String(skill?.relativePath || skill?.relative_path || '').trim(),
    source: String(skill?.source || 'user').trim(),
    skillHub: skill?.skillHub || skill?.skill_hub || null,
    localSkillId: String(skill?.localSkillId || skill?.local_skill_id || '').trim(),
    canShare: skill?.canShare ?? skill?.can_share ?? true,
  })).filter((skill) => skill.name);
}

export function isPrivateSkillHubReference(skillId) {
  const value = String(skillId || '');
  return value.startsWith('priv_') || value.startsWith('private/');
}

export function isLocalSkillShared(skill, installedReference) {
  const reference = skill?.skillHub?.reference;
  const isPublicReference = reference?.skillId
    && !isPrivateSkillHubReference(reference.skillId);
  const hasPublishedIdentity = Boolean(
    (skill?.skillHub?.author && skill?.skillHub?.version)
    || (
      isPublicReference
      && installedReference
      && reference.version === installedReference.version
      && reference.contentHash === installedReference.contentHash
    )
  );
  return skill?.canShare === false && hasPublishedIdentity;
}

export function upsertSkillRef(skills, nextRef, replacedSkillId = '') {
  const previousID = String(replacedSkillId || '').trim();
  return [...(skills || []).filter((skill) => (
    skill.skillId !== nextRef.skillId && (!previousID || skill.skillId !== previousID)
  )), nextRef]
    .sort((left, right) => left.skillId.localeCompare(right.skillId));
}

function botUID(bot) {
  return bot?.uid ?? bot?.id ?? '';
}

export function resolveSkillHubEntry(skill, detail) {
  const nested = detail?.skill || detail?.version || detail || {};
  const base = normalizeSkillHubSkills([{
    ...skill,
    ...nested,
    skillId: nested?.skillId || nested?.skill_id || nested?.id || skill?.skillId,
    latestVersion: nested?.latestVersion
      || nested?.latest_version
      || nested?.version
      || detail?.latestVersion
      || detail?.latest_version
      || skill?.latestVersion,
    contentHash: nested?.contentHash
      || nested?.content_hash
      || nested?.sha256
      || detail?.contentHash
      || detail?.content_hash
      || skill?.contentHash,
  }])[0] || skill;
  if (base?.latestVersion && isExactHash(base?.contentHash)) return base;
  const versions = normalizeSkillHubSkills(detail?.versions || []);
  const versionEntry = versions.find((entry) => (
    base?.latestVersion && entry.latestVersion === base.latestVersion
  )) || versions.find((entry) => entry.isLatest === true || entry.is_latest === true)
    || (versions.length === 1 ? versions[0] : null);
  return versionEntry ? { ...base, ...versionEntry, skillId: base.skillId || skill.skillId } : base;
}

export async function waitForPublishedSkillHubEntry({
  skillId,
  shared,
  getSkill = api.getSkillHubSkill,
  getVersion = api.getSkillHubVersion,
  waitFor = wait,
  maxAttempts = 20,
  retryDelayMs = 1_000,
  deadlineMs = 20_000,
}) {
  let resolved = resolveSkillHubEntry({
    skillId,
    latestVersion: shared?.latestVersion || shared?.latest_version,
    contentHash: shared?.contentHash || shared?.content_hash,
  }, shared);
  let lastError;
  const deadline = Date.now() + Math.max(1_000, Number(deadlineMs) || 20_000);
  for (let attempt = 0; attempt < maxAttempts && Date.now() < deadline; attempt += 1) {
    try {
      if (!resolved.latestVersion || !isExactHash(resolved.contentHash)) {
        const detail = await getSkill(skillId, {
          timeoutMs: Math.max(1, Math.min(5_000, deadline - Date.now())),
        });
        resolved = resolveSkillHubEntry({
          ...resolved,
          skillId,
        }, detail);
      }
      if (!resolved.latestVersion || !isExactHash(resolved.contentHash)) {
        throw new Error('SkillHub 尚未生成可绑定版本的完整哈希。');
      }
      const detail = await getVersion(skillId, resolved.latestVersion, {
        timeoutMs: Math.max(1, Math.min(5_000, deadline - Date.now())),
      });
      const candidate = resolveSkillHubEntry({
        skillId,
        latestVersion: resolved.latestVersion,
        contentHash: resolved.contentHash,
      }, detail);
      if (
        candidate.latestVersion !== resolved.latestVersion
        || candidate.contentHash !== resolved.contentHash
      ) {
        const mismatch = new Error('SkillHub 已发布版本与本次分享结果不一致，已停止自动绑定。');
        mismatch.code = 'skillhub_publish_mismatch';
        throw mismatch;
      }
      return candidate;
    } catch (error) {
      if (error?.code === 'skillhub_publish_mismatch') throw error;
      lastError = error;
      if (attempt < maxAttempts - 1 && Date.now() < deadline) {
        await waitFor(Math.min(retryDelayMs, Math.max(0, deadline - Date.now())));
      }
    }
  }
  throw new Error(`Skill 已上传，等待 SkillHub 发布版本超时：${lastError?.message || '请稍后刷新'}`);
}

export function resolveSharedSkillHubMetadata(shared, publishedVersion) {
  const candidates = [
    shared?.skill_hub,
    shared?.skillHub,
    shared?.skill?.skill_hub,
    shared?.skill?.skillHub,
    publishedVersion?.skill_hub,
    publishedVersion?.skillHub,
    publishedVersion?.manifest?.skillHub,
  ].filter(Boolean);
  const value = (keys) => {
    for (const candidate of candidates) {
      for (const key of keys) {
        const text = String(candidate?.[key] || '').trim();
        if (text) return text;
      }
    }
    return '';
  };
  return {
    author: value(['author']),
    version: value(['version']),
    uploadedAt: value(['uploadedAt', 'uploaded_at']),
  };
}

export function assertSkillHubDeviceResult(result, { toolName, botUID, reference } = {}) {
  const expectedSchema = SKILLHUB_DEVICE_SCHEMAS[toolName];
  if (!expectedSchema || result?.schema !== expectedSchema) {
    const error = new Error('本地 XiaoBa 返回了不兼容的 SkillHub 协议，请更新 XiaoBa 后重试。');
    error.code = 'skillhub_device_schema_mismatch';
    throw error;
  }
  if (String(result?.bot_uid || '') !== String(botUID || '')) {
    const error = new Error('本地 XiaoBa 返回了其他 Bot 的操作结果，已停止处理。');
    error.code = 'skillhub_device_bot_mismatch';
    throw error;
  }
  if (
    toolName === SKILLHUB_DEVICE_TOOLS.workspace
    && String(result?.active_bot_uid || '') !== String(botUID || '')
  ) {
    const error = new Error('本地 XiaoBa 的活动 Skill 工作区与当前 Bot 不一致。');
    error.code = 'skillhub_device_workspace_mismatch';
    throw error;
  }
  if (toolName === SKILLHUB_DEVICE_TOOLS.finalize && reference && (
    String(result?.skill_id || '') !== reference.skillId
    || String(result?.version || '') !== reference.version
    || String(result?.content_hash || '') !== reference.contentHash
  )) {
    const error = new Error('本地 XiaoBa 完成了其他 Skill 版本的对齐，已停止显示成功状态。');
    error.code = 'skillhub_device_finalize_mismatch';
    throw error;
  }
  return result;
}

function botLabel(bot) {
  return bot?.display_name || bot?.displayName || bot?.username || `Agent ${bot?.uid}`;
}

function isExactHash(value) {
  return /^[0-9a-f]{64}$/.test(String(value || ''));
}

async function copyText(value) {
  if (typeof navigator.clipboard?.writeText === 'function') {
    await navigator.clipboard.writeText(value);
    return;
  }
  if (typeof document === 'undefined' || typeof document.execCommand !== 'function') {
    throw new Error('当前浏览器无法自动复制。');
  }
  const textarea = document.createElement('textarea');
  textarea.value = value;
  textarea.setAttribute('readonly', '');
  textarea.setAttribute('aria-hidden', 'true');
  textarea.style.position = 'fixed';
  textarea.style.opacity = '0';
  document.body.appendChild(textarea);
  textarea.select();
  try {
    if (!document.execCommand('copy')) throw new Error('当前浏览器无法自动复制。');
  } finally {
    textarea.remove();
  }
}

export default function SkillHubView({ user }) {
  const feedback = useFeedback();
  const [bots, setBots] = useState([]);
  const [selectedBotUID, setSelectedBotUID] = useState('');
  const [definition, setDefinition] = useState({ skills: [], revision: 0 });
  const [definitionBotUID, setDefinitionBotUID] = useState('');
  const [query, setQuery] = useState('');
  const [catalogue, setCatalogue] = useState([]);
  const [loadingBots, setLoadingBots] = useState(true);
  const [loadingDefinition, setLoadingDefinition] = useState(false);
  const [loadingCatalogue, setLoadingCatalogue] = useState(true);
  const [catalogueError, setCatalogueError] = useState('');
  const [definitionError, setDefinitionError] = useState('');
  const [localSkills, setLocalSkills] = useState([]);
  const [localSkillsPath, setLocalSkillsPath] = useState('');
  const [localSkillsError, setLocalSkillsError] = useState('');
  const [localNotice, setLocalNotice] = useState('');
  const [loadingLocalSkills, setLoadingLocalSkills] = useState(false);
  const [sharingSkill, setSharingSkill] = useState('');
  const [saving, setSaving] = useState(false);
  const [activeSection, setActiveSection] = useState('added');
  const [skillAction, setSkillAction] = useState(null);
  const [actionNotice, setActionNotice] = useState('');
  const [devices, setDevices] = useState([]);
  const [selectedDeviceID, setSelectedDeviceID] = useState('');
  const [loadingDevices, setLoadingDevices] = useState(true);
  const selectedBotUIDRef = useRef('');
  const selectedDeviceIDRef = useRef('');
  const definitionBotUIDRef = useRef('');
  const definitionRequestRef = useRef(0);
  const catalogueRequestRef = useRef(0);
  const localRequestRef = useRef(0);
  const saveRequestRef = useRef(0);
  const requestedBotSwitchRef = useRef('');

  useEffect(() => {
    selectedBotUIDRef.current = selectedBotUID;
    saveRequestRef.current += 1;
    setSaving(false);
    setSkillAction(null);
    setActionNotice('');
    setDefinitionError('');
  }, [selectedBotUID]);

  useEffect(() => {
    if (!actionNotice) return undefined;
    const timer = window.setTimeout(() => setActionNotice(''), 3000);
    return () => window.clearTimeout(timer);
  }, [actionNotice]);

  const definitionReady = Boolean(
    selectedBotUID
    && definitionBotUID === selectedBotUID
    && !loadingDefinition,
  );

  const installedByID = useMemo(() => new Map(
    (definition.skills || []).map((skill) => [skill.skillId, skill]),
  ), [definition.skills]);

  const localSkillsByReference = useMemo(() => {
    const result = new Map();
    for (const skill of localSkills) {
      const skillId = String(skill?.skillHub?.reference?.skillId || '').trim();
      if (skillId) result.set(skillId, skill);
    }
    return result;
  }, [localSkills]);

  const catalogueByID = useMemo(() => new Map(
    catalogue.map((skill) => [skill.skillId, skill]),
  ), [catalogue]);

  const selectedAgent = useMemo(() => (
    bots.find((bot) => String(botUID(bot)) === selectedBotUID) || null
  ), [bots, selectedBotUID]);

  const agentOptions = useMemo(() => bots.map((bot) => ({
    value: String(botUID(bot)),
    label: botLabel(bot),
  })), [bots]);
  const loadDevices = useCallback(async () => {
    setLoadingDevices(true);
    try {
      const capable = normalizeSkillHubDevices(await api.getDevices());
      setDevices(capable);
      setSelectedDeviceID((current) => {
        const next = current && capable.some((device) => String(device.deviceId || '') === current)
          ? current
          : (capable.length === 1 ? String(capable[0].deviceId || '') : '');
        selectedDeviceIDRef.current = next;
        return next;
      });
      return capable;
    } catch (error) {
      setDevices([]);
      selectedDeviceIDRef.current = '';
      localRequestRef.current += 1;
      setSelectedDeviceID('');
      setLocalSkillsError(error?.message || '无法读取本地 XiaoBa 设备。');
      return [];
    } finally {
      setLoadingDevices(false);
    }
  }, []);

  const loadBots = useCallback(async () => {
    setLoadingBots(true);
    try {
      const response = await api.getMyBots();
      const owned = normalizeOwnedBots(response, user?.uid);
      setBots(owned);
      setSelectedBotUID((current) => {
        if (current && owned.some((bot) => String(botUID(bot)) === current)) return current;
        return resolvePreferredSkillHubBotUID(owned, user?.uid);
      });
    } finally {
      setLoadingBots(false);
    }
  }, [user?.uid]);

  const loadDefinition = useCallback(async (botUID = selectedBotUIDRef.current) => {
    const requestedBotUID = String(botUID || '');
    const requestID = definitionRequestRef.current + 1;
    definitionRequestRef.current = requestID;
    if (!requestedBotUID) {
      setDefinition({ skills: [], revision: 0 });
      definitionBotUIDRef.current = '';
      setDefinitionBotUID('');
      setLoadingDefinition(false);
      return null;
    }
    if (definitionBotUIDRef.current !== requestedBotUID) {
      setDefinition({ skills: [], revision: 0 });
      definitionBotUIDRef.current = '';
      setDefinitionBotUID('');
    }
    setLoadingDefinition(true);
    setDefinitionError('');
    try {
      const response = await api.getBotDefinitionSkills(requestedBotUID);
      if (
        requestID !== definitionRequestRef.current
        || requestedBotUID !== selectedBotUIDRef.current
      ) return null;
      const next = {
        ...response,
        skills: Array.isArray(response?.skills) ? response.skills : [],
        revision: Number(response?.revision || 0),
      };
      setDefinition(next);
      definitionBotUIDRef.current = requestedBotUID;
      setDefinitionBotUID(requestedBotUID);
      return next;
    } catch (error) {
      if (
        requestID !== definitionRequestRef.current
        || requestedBotUID !== selectedBotUIDRef.current
      ) return null;
      setDefinitionError(error?.message || '无法读取当前 Agent 的能力配置');
      return null;
    } finally {
      if (
        requestID === definitionRequestRef.current
        && requestedBotUID === selectedBotUIDRef.current
      ) setLoadingDefinition(false);
    }
  }, []);

  const searchCatalogue = useCallback(async (searchQuery = '') => {
    const requestID = catalogueRequestRef.current + 1;
    catalogueRequestRef.current = requestID;
    setLoadingCatalogue(true);
    setCatalogueError('');
    try {
      const response = await api.searchSkillHubSkills(searchQuery);
      if (requestID !== catalogueRequestRef.current) return;
      setCatalogue(normalizeSkillHubSkills(response));
    } catch (error) {
      if (requestID !== catalogueRequestRef.current) return;
      setCatalogue([]);
      setCatalogueError(error?.message || 'SkillHub 暂时无法访问');
    } finally {
      if (requestID === catalogueRequestRef.current) setLoadingCatalogue(false);
    }
  }, []);

  const loadLocalWorkspace = useCallback(async (
    botUID = selectedBotUIDRef.current,
    deviceID = selectedDeviceID,
    options = {},
  ) => {
    const requestedBotUID = String(botUID || '');
    const requestedDeviceID = String(deviceID || '');
    const requestID = localRequestRef.current + 1;
    localRequestRef.current = requestID;
    if (!requestedBotUID || !requestedDeviceID) {
      setLocalSkills([]);
      setLocalSkillsPath('');
      setLocalNotice('');
      setLoadingLocalSkills(false);
      return;
    }
    const allowBotSwitch = options.allowBotSwitch === true
      || requestedBotSwitchRef.current === requestedBotUID;
    if (requestedBotSwitchRef.current === requestedBotUID) requestedBotSwitchRef.current = '';
    setLoadingLocalSkills(true);
    // Do not leave the previous Bot's cards actionable while XiaoBa switches
    // its active workspace. The local bridge reads the currently active
    // workspace, so stale cards could otherwise upload the wrong Skill.
    setLocalSkills([]);
    setLocalSkillsPath('');
    setLocalSkillsError('');
    setLocalNotice('');
    const isCurrentRequest = () => (
      requestID === localRequestRef.current
      && requestedBotUID === selectedBotUIDRef.current
      && requestedDeviceID === selectedDeviceIDRef.current
    );
    try {
      const invoke = async (toolName, payload, timeoutMs) => assertSkillHubDeviceResult(
        await requestSkillHubDeviceTool({
          deviceId: requestedDeviceID,
          ownerUserId: user?.uid,
          toolName,
          payload: { bot_uid: requestedBotUID, ...payload },
          timeoutMs,
        }),
        { toolName, botUID: requestedBotUID },
      );
      let workspace;
      try {
        workspace = await invoke(SKILLHUB_DEVICE_TOOLS.workspace, {}, 20_000);
      } catch (error) {
        if (error?.code !== 'BOT_NOT_ACTIVE') throw error;
        if (!isCurrentRequest()) return;
        if (!allowBotSwitch) {
          setLocalNotice('当前 Bot 尚未在本地 XiaoBa 激活。');
          return;
        }
        await invoke(SKILLHUB_DEVICE_TOOLS.switchBot, {}, 10_000);
        if (!isCurrentRequest()) return;
        setLocalNotice('正在切换本地 Bot，等待 XiaoBa 重新连接…');
        workspace = await waitForSkillHubWorkspaceAfterSwitch({
          deviceId: requestedDeviceID,
          readWorkspace: (timeoutMs) => invoke(SKILLHUB_DEVICE_TOOLS.workspace, {}, timeoutMs),
          isCurrent: isCurrentRequest,
        });
        if (!workspace) return;
      }
      if (!isCurrentRequest()) return;
      if (String(workspace?.bot_uid || '') !== requestedBotUID) {
        throw new Error('本地 XiaoBa 返回了其他 Bot 的工作区，已停止展示。');
      }
      setLocalSkills(normalizeLocalSkills(workspace));
      setLocalSkillsPath(String(workspace?.skills_path || '').trim());
      setLocalNotice('');
    } catch (error) {
      if (!isCurrentRequest()) return;
      setLocalSkills([]);
      setLocalSkillsPath('');
      setLocalSkillsError(error?.message || '无法连接本地 XiaoBa，请确认 XiaoBa Dashboard 已启动并完成 CatsCo 登录。');
    } finally {
      if (isCurrentRequest()) setLoadingLocalSkills(false);
    }
  }, [selectedDeviceID, user?.uid]);

  useEffect(() => {
    loadBots().catch((error) => setDefinitionError(error?.message || '无法读取 Agent 列表'));
    searchCatalogue('').catch(() => {});
    loadDevices().catch(() => {});
  }, [loadBots, loadDevices, searchCatalogue]);

  useEffect(() => {
    loadDefinition(selectedBotUID).catch(() => {});
    loadLocalWorkspace(selectedBotUID, selectedDeviceID).catch(() => {});
  }, [loadDefinition, loadLocalWorkspace, selectedBotUID, selectedDeviceID]);

  const saveSkills = async (skills, expected = {}) => {
    const requestedBotUID = expected.botUID || selectedBotUIDRef.current;
    const requestedRevision = expected.revision ?? definition.revision;
    if (
      !requestedBotUID
      || requestedBotUID !== selectedBotUIDRef.current
      || definitionBotUID !== requestedBotUID
      || loadingDefinition
    ) return { ok: false, stale: true };
    if (saving) return { ok: false, busy: true };
    const requestID = saveRequestRef.current + 1;
    saveRequestRef.current = requestID;
    definitionRequestRef.current += 1;
    setSaving(true);
    setDefinitionError('');
    try {
      const next = await api.updateBotDefinitionSkills(
        requestedBotUID,
        requestedRevision,
        skills,
      );
      if (
        requestID !== saveRequestRef.current
        || requestedBotUID !== selectedBotUIDRef.current
      ) return { ok: false, stale: true };
      setDefinition({
        ...next,
        skills: Array.isArray(next?.skills) ? next.skills : [],
        revision: Number(next?.revision || 0),
      });
      definitionBotUIDRef.current = requestedBotUID;
      setDefinitionBotUID(requestedBotUID);
      return { ok: true, definition: next };
    } catch (error) {
      if (
        requestID !== saveRequestRef.current
        || requestedBotUID !== selectedBotUIDRef.current
      ) return { ok: false, stale: true };
      if (error?.status === 409) {
        await loadDefinition(requestedBotUID);
        if (
          requestID === saveRequestRef.current
          && requestedBotUID === selectedBotUIDRef.current
        ) setDefinitionError('配置刚刚被其他操作更新，已刷新，请再试一次。');
      } else {
        setDefinitionError(error?.message || '保存 Skills 配置失败');
      }
      return { ok: false, error };
    } finally {
      if (
        requestID === saveRequestRef.current
        && requestedBotUID === selectedBotUIDRef.current
      ) setSaving(false);
    }
  };

  const installSkill = async (skill) => {
    const initiatingBotUID = selectedBotUIDRef.current;
    if (
      !initiatingBotUID
      || definitionBotUID !== initiatingBotUID
      || loadingDefinition
    ) return;
    const initiatingRevision = definition.revision;
    const initiatingSkills = definition.skills;
    const agentName = botLabel(selectedAgent);
    setSkillAction({ type: 'add', skillId: skill.skillId });
    setActionNotice('');
    let resolved = skill;
    try {
      if (!resolved.latestVersion || !isExactHash(resolved.contentHash)) {
        const detail = await api.getSkillHubSkill(skill.skillId);
        if (
          initiatingBotUID !== selectedBotUIDRef.current
          || definitionBotUID !== initiatingBotUID
        ) return;
        resolved = resolveSkillHubEntry(skill, detail);
      }
      if (
        initiatingBotUID !== selectedBotUIDRef.current
        || definitionBotUID !== initiatingBotUID
      ) return;
      if (!resolved.latestVersion || !isExactHash(resolved.contentHash)) {
        setDefinitionError('暂时无法取得推荐稳定版本，请稍后重试。');
        return;
      }
      const nextRef = {
        source: 'skillhub',
        skillId: resolved.skillId,
        version: resolved.latestVersion,
        contentHash: resolved.contentHash,
      };
      const saved = await saveSkills(upsertSkillRef(initiatingSkills, nextRef), {
        botUID: initiatingBotUID,
        revision: initiatingRevision,
      });
      if (saved?.ok && initiatingBotUID === selectedBotUIDRef.current) {
        setActionNotice(`已为 Agent“${agentName}”添加 ${resolved.displayName || resolved.skillId}。`);
      }
    } catch (error) {
      if (
        initiatingBotUID === selectedBotUIDRef.current
        && definitionBotUID === initiatingBotUID
      ) setDefinitionError(error?.message || '添加失败，未更改 Agent 当前配置。');
    } finally {
      if (initiatingBotUID === selectedBotUIDRef.current) setSkillAction(null);
    }
  };

  const removeSkill = async (skillID) => {
    if (!skillID || !definitionReady || saving || sharingSkill || skillAction) return;
    const requestedBotUID = selectedBotUIDRef.current;
    const agentName = botLabel(selectedAgent);
    const skillName = catalogueByID.get(skillID)?.displayName || skillID;
    const confirmed = await feedback.confirm({
      title: `从“${agentName}”移除“${skillName}”？`,
      message: '该 Agent 将无法继续调用此能力。技能本身不会从 SkillHub 删除。',
      confirmLabel: '从 Agent 移除',
      tone: 'danger',
    });
    if (!confirmed || requestedBotUID !== selectedBotUIDRef.current) return;
    setSkillAction({ type: 'remove', skillId: skillID });
    setActionNotice('');
    try {
      const saved = await saveSkills(definition.skills.filter((skill) => skill.skillId !== skillID));
      if (saved?.ok && requestedBotUID === selectedBotUIDRef.current) {
        setActionNotice(`已从 Agent“${agentName}”移除 ${skillName}，不会影响其他 Agent。`);
      }
    } finally {
      if (requestedBotUID === selectedBotUIDRef.current) setSkillAction(null);
    }
  };

  const copySkill = async (skillID) => {
    if (!skillID || !definitionReady || saving || sharingSkill || skillAction) return;
    const requestedBotUID = selectedBotUIDRef.current;
    const details = catalogueByID.get(skillID);
    const localSkill = localSkillsByReference.get(skillID);
    const skillName = details?.displayName || localSkill?.name || skillID;
    const privateReference = isPrivateSkillHubReference(skillID);
    const shareURL = String(details?.shareUrl || details?.share_url || details?.url || '').trim();
    const copiedValue = shareURL || skillID;
    setSkillAction({ type: 'copy', skillId: skillID });
    setActionNotice('');
    setDefinitionError('');
    try {
      await copyText(copiedValue);
      if (requestedBotUID === selectedBotUIDRef.current) {
        setActionNotice(shareURL
          ? `已复制 ${skillName} 的链接。`
          : privateReference
            ? `已复制 ${skillName} 的私有能力引用。`
            : `已复制 ${skillName} 的 SkillHub ID。`);
      }
    } catch (error) {
      if (requestedBotUID === selectedBotUIDRef.current) {
        setDefinitionError(`${error?.message || '复制失败'} 请手动复制 SkillHub ID：${skillID}`);
      }
    } finally {
      if (requestedBotUID === selectedBotUIDRef.current) setSkillAction(null);
    }
  };

  const shareLocalSkill = async (localSkill) => {
    const requestedBotUID = selectedBotUIDRef.current;
    const requestedDeviceID = selectedDeviceID;
    if (!requestedBotUID || !requestedDeviceID || !definitionReady || saving || sharingSkill) return;
    const requestedRevision = definition.revision;
    const requestedSkills = definition.skills;
    setSharingSkill(localSkill.name);
    setLocalSkillsError('');
    setLocalNotice('');
    let uploaded = false;
    let bound = false;
    try {
      const sharePayload = {
        bot_uid: requestedBotUID,
        local_skill_id: localSkill.localSkillId,
        skill_name: localSkill.name,
      };
      let shared = assertSkillHubDeviceResult(await requestSkillHubDeviceTool({
        deviceId: requestedDeviceID,
        ownerUserId: user?.uid,
        toolName: SKILLHUB_DEVICE_TOOLS.share,
        payload: sharePayload,
        timeoutMs: 90_000,
      }), { toolName: SKILLHUB_DEVICE_TOOLS.share, botUID: requestedBotUID });
      if (shared?.requiresConfirmation || shared?.requires_confirmation) {
        const confirmed = globalThis.confirm?.(
          `SkillHub 已存在“${localSkill.name}”，是否将当前本地内容发布为新版本？`,
        );
        if (!confirmed) return;
        shared = assertSkillHubDeviceResult(await requestSkillHubDeviceTool({
          deviceId: requestedDeviceID,
          ownerUserId: user?.uid,
          toolName: SKILLHUB_DEVICE_TOOLS.share,
          payload: { ...sharePayload, confirm_publish: true },
          timeoutMs: 90_000,
        }), { toolName: SKILLHUB_DEVICE_TOOLS.share, botUID: requestedBotUID });
        if (shared?.requiresConfirmation || shared?.requires_confirmation) {
          throw new Error('SkillHub 未接受本次新版本发布确认，请稍后重试。');
        }
      }
      uploaded = true;
      const sharedSkillID = String(shared?.skill?.id || '').trim();
      if (!sharedSkillID) throw new Error('SkillHub 没有返回已分享 Skill 的标识。');
      const resolved = await waitForPublishedSkillHubEntry({
        skillId: sharedSkillID,
        shared,
      });
      const sharedMetadata = resolveSharedSkillHubMetadata(shared, resolved);
      if (
        !sharedMetadata.author
        || !sharedMetadata.version
        || !sharedMetadata.uploadedAt
        || sharedMetadata.version !== resolved.latestVersion
      ) {
        throw new Error('SkillHub 没有返回本地对齐所需的作者、版本和上传时间，已停止自动绑定。');
      }
      if (requestedBotUID !== selectedBotUIDRef.current) return;
      const saved = await saveSkills(upsertSkillRef(requestedSkills, {
        source: 'skillhub',
        skillId: sharedSkillID,
        version: resolved.latestVersion,
        contentHash: resolved.contentHash,
      }, localSkill.skillHub?.reference?.skillId), {
        botUID: requestedBotUID,
        revision: requestedRevision,
      });
      if (!saved?.ok) {
        throw new Error('能力已发布到团队，但添加到当前 Agent 失败，请刷新 Agent 能力后重试。');
      }
      bound = true;
      try {
        const finalized = await requestSkillHubDeviceTool({
          deviceId: requestedDeviceID,
          ownerUserId: user?.uid,
          toolName: SKILLHUB_DEVICE_TOOLS.finalize,
          payload: {
            bot_uid: requestedBotUID,
            local_skill_id: localSkill.localSkillId,
            skill_name: localSkill.name,
            skill_id: sharedSkillID,
            version: resolved.latestVersion,
            content_hash: resolved.contentHash,
            author: sharedMetadata.author,
            uploaded_at: sharedMetadata.uploadedAt,
          },
          timeoutMs: 120_000,
        });
        assertSkillHubDeviceResult(finalized, {
          toolName: SKILLHUB_DEVICE_TOOLS.finalize,
          botUID: requestedBotUID,
          reference: {
            skillId: sharedSkillID,
            version: resolved.latestVersion,
            contentHash: resolved.contentHash,
          },
        });
      } catch (finalizeError) {
        throw new Error(`Skill 已分享并绑定当前 Bot，但本地工作区暂未完成对齐：${finalizeError?.message || '请稍后刷新'}`);
      }
      if (requestedBotUID !== selectedBotUIDRef.current) return;
      await Promise.all([
        searchCatalogue(query),
        loadLocalWorkspace(requestedBotUID, requestedDeviceID),
      ]);
      if (requestedBotUID !== selectedBotUIDRef.current) return;
      setLocalNotice(`“${localSkill.name}”已发布到团队，并添加到当前 Agent。`);
    } catch (error) {
      if (requestedBotUID === selectedBotUIDRef.current) {
        if (bound) {
          setLocalSkillsError(error?.message || 'Skill 已分享并绑定当前 Bot，但本地工作区暂未完成对齐。');
          return;
        }
        if (uploaded) {
          setLocalSkillsError('能力已发布到团队，但暂未添加到当前 Agent，请刷新后重试。');
          return;
        }
        setLocalSkillsError(error?.message || '发布自定义能力失败');
      }
    } finally {
      if (requestedBotUID === selectedBotUIDRef.current) setSharingSkill('');
    }
  };

  const copyLocalSkillsPath = async () => {
    if (!localSkillsPath) return;
    try {
      await navigator.clipboard.writeText(localSkillsPath);
      setLocalNotice('当前生效的本地 Skills 路径已复制。');
    } catch {
      setLocalSkillsError(`无法自动复制，请手动复制：${localSkillsPath}`);
    }
  };

  return <SkillHubContent
    actionNotice={actionNotice}
    activeSection={activeSection}
    agentOptions={agentOptions}
    catalogue={catalogue}
    catalogueByID={catalogueByID}
    catalogueError={catalogueError}
    definition={definition}
    definitionError={definitionError}
    definitionReady={definitionReady}
    devices={devices}
    installedByID={installedByID}
    isLocalSkillShared={isLocalSkillShared}
    isLocalEnabled={true}
    loadingBots={loadingBots}
    loadingCatalogue={loadingCatalogue}
    loadingDefinition={loadingDefinition}
    loadingDevices={loadingDevices}
    loadingLocalSkills={loadingLocalSkills}
    localNotice={localNotice}
    localSkills={localSkills}
    localSkillsByReference={localSkillsByReference}
    localSkillsError={localSkillsError}
    localSkillsPath={localSkillsPath}
    onChangeSection={setActiveSection}
    onCopySkill={copySkill}
    onCopyLocalPath={copyLocalSkillsPath}
    onInstallSkill={installSkill}
    onQueryChange={setQuery}
    onRefreshDefinition={() => loadDefinition()}
    onRefreshLocal={() => loadLocalWorkspace(
      selectedBotUIDRef.current,
      selectedDeviceIDRef.current,
      { allowBotSwitch: true },
    )}
    onRemoveSkill={removeSkill}
    onSearch={searchCatalogue}
    onSelectAgent={(nextBotUID) => {
      selectedBotUIDRef.current = nextBotUID;
      requestedBotSwitchRef.current = nextBotUID;
      rememberSkillHubBotUID(user?.uid, nextBotUID);
      localRequestRef.current += 1;
      setSelectedBotUID(nextBotUID);
    }}
    onSelectDevice={(deviceID) => {
      selectedDeviceIDRef.current = deviceID;
      localRequestRef.current += 1;
      if (!deviceID) setLocalSkillsError('');
      setSelectedDeviceID(deviceID);
    }}
    onShareLocalSkill={shareLocalSkill}
    query={query}
    saving={saving}
    selectedAgentName={selectedAgent ? botLabel(selectedAgent) : ''}
    selectedBotUID={selectedBotUID}
    selectedDeviceID={selectedDeviceID}
    sharingSkill={sharingSkill}
    skillAction={skillAction}
  />;
}
