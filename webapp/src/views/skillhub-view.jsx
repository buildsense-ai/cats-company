import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { api, requestSkillHubDeviceTool } from '../api';
import { useFeedback } from '../components/feedback-system';
import {
  normalizeLocalSkillHubSkills,
  normalizeSkillHubSkills,
  resolveSkillHubEntry,
} from '../utils/skillhub-entry';
import SkillHubContent from './skillhub-content';
import '../css/skillhub-view.css';

export { normalizeSkillHubSkills, resolveSkillHubEntry } from '../utils/skillhub-entry';

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
    device?.runtimeRole === 'desktop'
    && device?.active === true
    && device?.routeConnected === true
    && device?.routable === true
    && Array.isArray(device?.capabilities)
    && SKILLHUB_DEVICE_CAPABILITIES.every((capability) => device.capabilities.includes(capability))
  ));
}

export function resolveAutomaticSkillHubDeviceID(devices) {
  if (!Array.isArray(devices) || devices.length !== 1) return '';
  return String(devices[0]?.deviceId || '');
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

export function normalizeAccessibleBots(response, userUid) {
  const bots = Array.isArray(response) ? response : (response?.agents || response?.bots || []);
  return bots.filter((bot) => {
    if (bot?.relation === 'owner') return true;
    // /api/bots is also used as a chat roster and may include a disclosed
    // human friend. A SkillHub friend entry must identify a real Bot owner;
    // otherwise /api/agents/skills cannot resolve a BotDefinition for it.
    if (bot?.relation === 'friend') {
      const botOwnerUID = Number(bot?.owner_id || bot?.owner_uid || 0);
      return botOwnerUID > 0;
    }
    if (bot?.is_owner !== undefined) return Boolean(bot.is_owner);
    const ownerUID = Number(bot?.owner_id || bot?.owner_uid || 0);
    return ownerUID > 0 && ownerUID === Number(userUid);
  }).map((bot) => ({
    ...bot,
    uid: botUID(bot),
    relation: bot?.relation === 'friend' ? 'friend' : 'owner',
  }));
}

function isFriendBot(bot) {
  return bot?.relation === 'friend' || bot?.is_owner === false;
}

function isFriendBotUID(bots, uid) {
  const bot = bots.find((candidate) => String(botUID(candidate)) === String(uid || ''));
  return Boolean(bot && isFriendBot(bot));
}

export function buildCurrentAgentSkills(formalSkills = [], localSkills = []) {
  const formal = Array.isArray(formalSkills) ? formalSkills : [];
  const localByReference = new Map((Array.isArray(localSkills) ? localSkills : []).map((local) => [
    String(local?.skillHub?.reference?.skillId || '').trim(), local,
  ]).filter(([skillId]) => skillId));
  const formalIDs = new Set(formal.map((skill) => String(skill?.skillId || '').trim()).filter(Boolean));
  const result = formal.map((skill) => {
    const localDetails = localByReference.get(String(skill?.skillId || '').trim()) || null;
    return { ...skill, formal: true, local: Boolean(localDetails), localDetails };
  });
  for (const local of (Array.isArray(localSkills) ? localSkills : [])) {
    const reference = local?.skillHub?.reference;
    const skillId = String(reference?.skillId || '').trim();
    if (skillId && formalIDs.has(skillId)) continue;
    const localID = String(local?.localSkillId || local?.relativePath || local?.name || '').trim();
    if (!localID) continue;
    result.push({
      source: 'local',
      skillId: `local:${localID}`,
      version: String(reference?.version || '').trim(),
      contentHash: String(reference?.contentHash || '').trim().toLowerCase(),
      formal: false,
      local: true,
      localOnly: true,
      localName: local.name,
      displayName: local.name,
      description: local.description,
    });
  }
  return result;
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
  if (isSkillHubWorkspaceSwitchingError(error)) return true;
  if (RETRYABLE_SKILLHUB_SWITCH_ERRORS.has(String(error?.code || ''))) return true;
  return error?.code === 'skillhub_device_request_rejected'
    && [404, 409, 503].includes(Number(error?.status || 0));
}

export function isSkillHubWorkspaceSwitchingError(error) {
  if (String(error?.code || '') === 'WORKSPACE_SWITCHING') return true;
  return String(error?.code || '') === 'SKILLHUB_OPERATION_FAILED'
    && /^Bot Skill workspace ownership is changing \([^\r\n]+\); retry the write\.$/i.test(
      String(error?.message || '').trim(),
    );
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

export function normalizeViewerSkills(response) {
  const values = Array.isArray(response) ? response : (response?.skills || []);
  return values.map((skill) => ({
    ...skill,
    source: String(skill?.source || 'skillhub').trim().toLowerCase(),
    skillId: String(skill?.skillId || skill?.skill_id || skill?.id || '').trim(),
    version: String(skill?.version || '').trim(),
    displayName: String(skill?.displayName || skill?.display_name || skill?.name || '').trim(),
    description: String(skill?.description || '').trim(),
    author: String(skill?.author || skill?.publisher || '').trim(),
    public: skill?.public ?? skill?.is_public ?? false,
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
    shareError: String(skill?.shareError || skill?.share_error || '').trim(),
  })).filter((skill) => skill.name);
}

export function isPrivateSkillHubReference(skillId) {
  const value = String(skillId || '');
  return value.startsWith('priv_') || value.startsWith('private/');
}

export function isLocalSkillShared(skill, installedReference) {
  if (skill?.shareError) return false;
  const reference = skill?.skillHub?.reference;
  const isPublicReference = reference?.skillId
    && !isPrivateSkillHubReference(reference.skillId);
  const matchesInstalledReference = Boolean(
    isPublicReference
    && installedReference
    && reference.version === installedReference.version
    && reference.contentHash === installedReference.contentHash
  );
  return skill?.canShare === false && matchesInstalledReference;
}

export function resolveAddedSkillPresentation(skill, catalogueByID, localSkillsByReference) {
  const skillId = String(skill?.skillId || '').trim();
  const details = catalogueByID?.get(skillId);
  const candidate = localSkillsByReference?.get(skillId);
  const candidateReference = candidate?.skillHub?.reference;
  const localDetails = candidate
    && (!skill?.version || candidateReference?.version === skill.version)
    && (!skill?.contentHash || candidateReference?.contentHash === skill.contentHash)
    ? candidate
    : null;
  const privateReference = isPrivateSkillHubReference(skillId);
  return {
    details,
    localDetails,
    privateReference,
    label: details?.displayName || skill?.displayName || skill?.localName || localDetails?.name || (privateReference ? '私有能力' : skillId),
    description: details?.description || skill?.description || localDetails?.description || '此能力已添加到当前 Agent，可立即使用。',
  };
}

export function upsertSkillRef(skills, nextRef, replacedSkillId = '') {
  const previousID = String(replacedSkillId || '').trim();
  return [...(skills || []).filter((skill) => (
    skill.skillId !== nextRef.skillId && (!previousID || skill.skillId !== previousID)
  )), nextRef]
    .sort((left, right) => left.skillId.localeCompare(right.skillId));
}

export function buildSkillLibrary({ catalogue = [], installedByID = new Map(), localSkills = [], query = '' }) {
  const normalizedLocal = normalizeLocalSkillHubSkills(localSkills).map((skill) => {
    const localSkill = localSkills.find((candidate) => (
      candidate.localSkillId === skill.localSkillId
      || candidate.name === skill.displayName
    )) || skill;
    const installedReference = skill.cloudSkillId ? installedByID.get(skill.cloudSkillId) : null;
    const canBind = Boolean(
      (skill.canBind === true || isLocalSkillShared(localSkill, installedReference))
      && skill.cloudSkillId
      && !isPrivateSkillHubReference(skill.cloudSkillId)
      && skill.latestVersion
      && isExactHash(skill.contentHash),
    );
    return {
      ...skill,
      skillId: canBind ? skill.cloudSkillId : `local:${skill.localSkillId || skill.displayName}`,
      canBind,
      localSkill,
      sourceLabel: '本机',
    };
  });
  const localCloudIDs = new Set(normalizedLocal.map((skill) => skill.cloudSkillId).filter(Boolean));
  const normalizedQuery = String(query || '').trim().toLocaleLowerCase();
  const visibleLocal = normalizedQuery
    ? normalizedLocal.filter((skill) => `${skill.displayName} ${skill.description}`.toLocaleLowerCase().includes(normalizedQuery))
    : normalizedLocal;
  const online = catalogue
    .filter((skill) => !localCloudIDs.has(skill.skillId))
    .map((skill) => ({ ...skill, sourceLabel: '在线' }));
  return [...visibleLocal, ...online];
}

function botUID(bot) {
  return bot?.uid ?? bot?.id ?? '';
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

export default function SkillHubView({ user, initialAgent = null, initialAgentId = null }) {
  const feedback = useFeedback();
  const [bots, setBots] = useState([]);
  const [selectedBotUID, setSelectedBotUID] = useState('');
  const [definition, setDefinition] = useState({ skills: [], revision: 0 });
  const [viewerSkills, setViewerSkills] = useState([]);
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
  const [libraryLocalSkills, setLibraryLocalSkills] = useState([]);
  const [libraryLocalError, setLibraryLocalError] = useState('');
  const [loadingLibraryLocalSkills, setLoadingLibraryLocalSkills] = useState(true);
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

  const selectedAgentIsFriend = isFriendBotUID(bots, selectedBotUID);

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
  const librarySkills = useMemo(() => buildSkillLibrary({
    catalogue,
    installedByID,
    localSkills: libraryLocalSkills,
    query,
  }), [catalogue, installedByID, libraryLocalSkills, query]);

  const catalogueByID = useMemo(() => new Map([
    ...viewerSkills.map((skill) => [skill.skillId, skill]),
    ...librarySkills.flatMap((skill) => {
      const entries = [[skill.skillId, skill]];
      if (skill.cloudSkillId) entries.push([skill.cloudSkillId, skill]);
      return entries;
    }),
  ]), [librarySkills, viewerSkills]);

  const addedSkillPresentationByID = useMemo(() => new Map(
    buildCurrentAgentSkills(
      selectedAgentIsFriend ? viewerSkills : definition.skills,
      selectedAgentIsFriend ? [] : localSkills,
    ).map((skill) => [
      skill.skillId,
      resolveAddedSkillPresentation(skill, catalogueByID, localSkillsByReference),
    ]),
  ), [catalogueByID, definition.skills, localSkills, localSkillsByReference, selectedAgentIsFriend, viewerSkills]);

  const displaySkills = useMemo(() => buildCurrentAgentSkills(
    selectedAgentIsFriend ? viewerSkills : definition.skills,
    selectedAgentIsFriend ? [] : localSkills,
  ), [definition.skills, localSkills, selectedAgentIsFriend, viewerSkills]);

  const selectedAgent = useMemo(() => (
    bots.find((bot) => String(botUID(bot)) === selectedBotUID) || null
  ), [bots, selectedBotUID]);

  const agentOptions = useMemo(() => bots.map((bot) => ({
    value: String(botUID(bot)),
    label: `${botLabel(bot)}${isFriendBot(bot) ? '（好友）' : ''}`,
  })), [bots]);
  const loadDevices = useCallback(async (options = {}) => {
    setLoadingDevices(true);
    try {
      const capable = normalizeSkillHubDevices(await api.getDevices());
      const next = resolveAutomaticSkillHubDeviceID(capable);
      setDevices(capable);
      if (
        options.allowBotSwitchOnChange === true
        && next
        && next !== selectedDeviceIDRef.current
      ) {
        requestedBotSwitchRef.current = selectedBotUIDRef.current;
      }
      selectedDeviceIDRef.current = next;
      setSelectedDeviceID(next);
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
    const requestedUID = String(initialAgentId || botUID(initialAgent) || '');
    const requestedAgent = requestedUID ? {
      ...initialAgent,
      id: initialAgent?.id || requestedUID,
      uid: initialAgent?.uid || requestedUID,
      display_name: initialAgent?.display_name || initialAgent?.username || `Agent ${requestedUID}`,
      relation: 'owner',
      is_owner: true,
    } : null;
    setLoadingBots(true);
    try {
      const response = await api.getMyBots();
      const accessible = normalizeAccessibleBots(response, user?.uid);
      if (requestedUID && !accessible.some((bot) => String(botUID(bot)) === requestedUID)) {
        accessible.push(requestedAgent);
      }
      setBots(accessible);
      setSelectedBotUID((current) => {
        if (requestedUID && accessible.some((bot) => String(botUID(bot)) === requestedUID)) {
          return requestedUID;
        }
        if (current && accessible.some((bot) => String(botUID(bot)) === current)) return current;
        return resolvePreferredSkillHubBotUID(accessible, user?.uid);
      });
    } catch (error) {
      if (requestedAgent) {
        setBots([requestedAgent]);
        setSelectedBotUID(requestedUID);
      }
      throw error;
    } finally {
      setLoadingBots(false);
    }
  }, [initialAgent, initialAgentId, user?.uid]);

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
      const friend = isFriendBotUID(bots, requestedBotUID);
      const response = friend
        ? await api.getAgentSkills(requestedBotUID)
        : await api.getBotDefinitionSkills(requestedBotUID);
      if (
        requestID !== definitionRequestRef.current
        || requestedBotUID !== selectedBotUIDRef.current
      ) return null;
      const next = {
        ...response,
        skills: friend ? normalizeViewerSkills(response) : (Array.isArray(response?.skills) ? response.skills : []),
        revision: Number(response?.revision || 0),
      };
      setViewerSkills(friend ? next.skills : []);
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
  }, [bots]);

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

  const loadLibraryLocalSkills = useCallback(async () => {
    setLoadingLibraryLocalSkills(true);
    setLibraryLocalError('');
    try {
      setLibraryLocalSkills(normalizeLocalSkillHubSkills(await api.getLocalSkills()));
    } catch (error) {
      setLibraryLocalSkills([]);
      setLibraryLocalError(error?.message || '暂时无法读取本机能力。');
    } finally {
      setLoadingLibraryLocalSkills(false);
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
    const explicitBotSwitch = requestedBotSwitchRef.current === requestedBotUID;
    const allowBotSwitch = options.allowBotSwitch === true || explicitBotSwitch;
    if (explicitBotSwitch) requestedBotSwitchRef.current = '';
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
      let switchAccepted = false;
      let recoverySwitchAttempted = false;
      const requestBotSwitch = async ({ resubmit = false } = {}) => {
        if (switchAccepted && !resubmit) return;
        if (resubmit) {
          if (recoverySwitchAttempted) return;
          // The connector may accept a switch even when its response is lost
          // or reports a transient state. Count the attempt before awaiting so
          // workspace polling can never turn recovery into a restart loop.
          recoverySwitchAttempted = true;
        }
        await invoke(SKILLHUB_DEVICE_TOOLS.switchBot, {}, 10_000);
        switchAccepted = true;
      };
      const waitForWorkspace = () => waitForSkillHubWorkspaceAfterSwitch({
        deviceId: requestedDeviceID,
        readWorkspace: async (timeoutMs) => {
          try {
            return await invoke(SKILLHUB_DEVICE_TOOLS.workspace, {}, timeoutMs);
          } catch (error) {
            if (
              error?.code === 'BOT_NOT_ACTIVE'
              && allowBotSwitch
              && isCurrentRequest()
            ) {
              // One newer intent can be lost with the connector process that
              // an earlier switch restarts. Re-submit it once to the new
              // connector, then only poll so a broken workspace cannot cause
              // a restart loop.
              await requestBotSwitch({ resubmit: true });
            }
            throw error;
          }
        },
        isCurrent: isCurrentRequest,
      });
      if (explicitBotSwitch) {
        try {
          await requestBotSwitch();
        } catch (error) {
          if (!isRetryableSkillHubSwitchError(error)) throw error;
        }
        if (!isCurrentRequest()) return;
        setLocalNotice('正在切换本地 Bot，等待 XiaoBa 重新连接…');
        workspace = await waitForWorkspace();
        if (!workspace) return;
      } else {
        try {
          workspace = await invoke(SKILLHUB_DEVICE_TOOLS.workspace, {}, 20_000);
        } catch (error) {
          if (!isCurrentRequest()) return;
          if (error?.code === 'BOT_NOT_ACTIVE') {
            if (!allowBotSwitch) {
              setLocalNotice('当前 Bot 尚未在本地 XiaoBa 激活。');
              return;
            }
            await requestBotSwitch();
          } else if (
            !isSkillHubWorkspaceSwitchingError(error)
            && !(allowBotSwitch && isRetryableSkillHubSwitchError(error))
          ) {
            throw error;
          }
          if (!isCurrentRequest()) return;
          setLocalNotice('正在切换本地 Bot，等待 XiaoBa 重新连接…');
          workspace = await waitForWorkspace();
          if (!workspace) return;
        }
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

  const refreshLocalWorkspace = useCallback(async () => {
    const previousDeviceID = selectedDeviceIDRef.current;
    const capable = await loadDevices({ allowBotSwitchOnChange: true });
    const deviceID = resolveAutomaticSkillHubDeviceID(capable);
    if (!deviceID) return;
    // A newly discovered desktop changes selectedDeviceID and the normal
    // effect below will load it once. Only refresh directly when the route did
    // not change, avoiding duplicate RPCs on offline -> online recovery.
    if (deviceID !== previousDeviceID) return;
    await loadLocalWorkspace(
      selectedBotUIDRef.current,
      deviceID,
      { allowBotSwitch: true },
    );
  }, [loadDevices, loadLocalWorkspace]);

  useEffect(() => {
    loadBots().catch((error) => setDefinitionError(error?.message || '无法读取 Agent 列表'));
    searchCatalogue('').catch(() => {});
    loadLibraryLocalSkills().catch(() => {});
  }, [loadBots, loadLibraryLocalSkills, searchCatalogue]);

  useEffect(() => {
    if (!selectedBotUID || selectedAgentIsFriend) {
      localRequestRef.current += 1;
      selectedDeviceIDRef.current = '';
      setDevices([]);
      setSelectedDeviceID('');
      setLoadingDevices(false);
      return;
    }
    loadDevices().catch(() => {});
  }, [loadDevices, selectedAgentIsFriend, selectedBotUID]);

  useEffect(() => {
    loadDefinition(selectedBotUID).catch(() => {});
    if (isFriendBotUID(bots, selectedBotUID)) {
      localRequestRef.current += 1;
      setLocalSkills([]);
      setLocalSkillsPath('');
      setLocalSkillsError('');
      setLoadingLocalSkills(false);
      return;
    }
    loadLocalWorkspace(selectedBotUID, selectedDeviceID).catch(() => {});
  }, [bots, loadDefinition, loadLocalWorkspace, selectedBotUID, selectedDeviceID]);

  const saveSkills = async (skills, expected = {}) => {
    const requestedBotUID = expected.botUID || selectedBotUIDRef.current;
    const requestedRevision = expected.revision ?? definition.revision;
    if (
      !requestedBotUID
      || selectedAgentIsFriend
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
      || selectedAgentIsFriend
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
    if (!skillID || selectedAgentIsFriend || !definitionReady || saving || sharingSkill || skillAction) return;
    const requestedBotUID = selectedBotUIDRef.current;
    const agentName = botLabel(selectedAgent);
    const skillName = addedSkillPresentationByID.get(skillID)?.label || skillID;
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
    if (!skillID || selectedAgentIsFriend || !definitionReady || saving || sharingSkill || skillAction) return;
    const requestedBotUID = selectedBotUIDRef.current;
    const presentation = addedSkillPresentationByID.get(skillID);
    const details = presentation?.details || catalogueByID.get(skillID);
    const skillName = presentation?.label || skillID;
    const privateReference = presentation?.privateReference ?? isPrivateSkillHubReference(skillID);
    const manualCopyHint = privateReference ? '私有能力引用' : 'SkillHub ID';
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
        setDefinitionError(`${error?.message || '复制失败'} 请手动复制 ${manualCopyHint}：${skillID}`);
      }
    } finally {
      if (requestedBotUID === selectedBotUIDRef.current) setSkillAction(null);
    }
  };

  const shareLocalSkill = async (localSkill) => {
    const requestedBotUID = selectedBotUIDRef.current;
    const requestedDeviceID = selectedDeviceID;
    if (!requestedBotUID || selectedAgentIsFriend || !requestedDeviceID || !definitionReady || saving || sharingSkill) return;
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

  const syncLocalLibrarySkill = async (skill) => {
    const requestedBotUID = selectedBotUIDRef.current;
    if (!requestedBotUID || !definitionReady || saving || sharingSkill) return;
    const requestedRevision = definition.revision;
    const requestedSkills = definition.skills;
    const localName = skill.localSkillId || skill.displayName;
    setSharingSkill(localName);
    setLibraryLocalError('');
    setActionNotice('');
    let synced = false;
    try {
      let shared = await api.shareLocalSkill(localName, '', user?.uid);
      if (shared?.requiresConfirmation || shared?.requires_confirmation) {
        const confirmed = await feedback.confirm({
          title: `更新“${skill.displayName}”？`,
          message: '你的账号中已有这个能力。继续后会把本机内容同步为新版本。',
          confirmLabel: '继续同步',
        });
        if (!confirmed || requestedBotUID !== selectedBotUIDRef.current) return;
        shared = await api.shareLocalSkill(localName, '', user?.uid, { confirmPublish: true });
        if (shared?.requiresConfirmation || shared?.requires_confirmation) {
          throw new Error('SkillHub 未接受本次同步确认，请稍后重试。');
        }
      }
      const sharedSkillID = String(
        shared?.skill?.id
        || shared?.skill?.skillId
        || shared?.skill?.skill_id
        || shared?.upload?.skillId
        || shared?.upload?.skill_id
        || shared?.submission?.normalizedManifest?.id
        || '',
      ).trim();
      if (!sharedSkillID) throw new Error('SkillHub 没有返回已同步能力的标识。');
      const resolved = await waitForPublishedSkillHubEntry({ skillId: sharedSkillID, shared });
      synced = true;
      if (requestedBotUID !== selectedBotUIDRef.current) return;
      const saved = await saveSkills(upsertSkillRef(requestedSkills, {
        source: 'skillhub',
        skillId: sharedSkillID,
        version: resolved.latestVersion,
        contentHash: resolved.contentHash,
      }, skill.cloudSkillId), {
        botUID: requestedBotUID,
        revision: requestedRevision,
      });
      if (!saved?.ok) throw new Error('当前 Agent 的能力配置暂时无法更新。');
      await Promise.all([loadLibraryLocalSkills(), searchCatalogue(query)]);
      if (requestedBotUID === selectedBotUIDRef.current) {
        setActionNotice(`已同步并为 Agent“${botLabel(selectedAgent)}”添加 ${skill.displayName}。`);
      }
    } catch (error) {
      if (requestedBotUID === selectedBotUIDRef.current) {
        setLibraryLocalError(synced
          ? `“${skill.displayName}”已同步到你的账号，但未添加到当前 Agent。${error?.message || '请稍后再次点击添加。'}`
          : `“${skill.displayName}”同步失败，尚未添加到当前 Agent。${error?.message || '请检查连接后再次点击添加。'}`);
      }
    } finally {
      if (requestedBotUID === selectedBotUIDRef.current) setSharingSkill('');
    }
  };

  const installLibrarySkill = async (skill) => {
    setLibraryLocalError('');
    if (!skill?.isLocalSkill) {
      await installSkill(skill);
      return;
    }
    if (skill.canBind) {
      await installSkill(skill);
      return;
    }
    const localSkill = skill.localSkill;
    if (!localSkill?.canShare || localSkill?.source === 'system') {
      setLibraryLocalError(`“${skill.displayName}”暂时不能同步，因此还不能添加到当前 Agent。`);
      return;
    }
    const requestedBotUID = selectedBotUIDRef.current;
    const confirmed = await feedback.confirm({
      title: `添加“${skill.displayName}”？`,
      message: '此能力目前只在本机。添加到 Agent 前，需要同步到你的账号。',
      confirmLabel: '继续添加',
    });
    if (!confirmed || requestedBotUID !== selectedBotUIDRef.current) return;
    await syncLocalLibrarySkill(skill);
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
    addedSkillPresentationByID={addedSkillPresentationByID}
    agentOptions={agentOptions}
    catalogue={catalogue}
    catalogueByID={catalogueByID}
    catalogueError={catalogueError}
    definition={{ ...definition, skills: displaySkills }}
    definitionError={definitionError}
    definitionReady={definitionReady}
    devices={devices}
    installedByID={installedByID}
    isLocalSkillShared={isLocalSkillShared}
    isLocalEnabled={!selectedAgentIsFriend}
    isReadOnly={selectedAgentIsFriend}
    loadingBots={loadingBots}
    loadingCatalogue={loadingCatalogue}
    loadingDefinition={loadingDefinition}
    loadingDevices={loadingDevices}
    loadingLocalSkills={loadingLocalSkills}
    loadingLibraryLocalSkills={loadingLibraryLocalSkills}
    libraryLocalError={libraryLocalError}
    localNotice={localNotice}
    localSkills={localSkills}
    localSkillsError={localSkillsError}
    localSkillsPath={localSkillsPath}
    onChangeSection={setActiveSection}
    onCopySkill={copySkill}
    onCopyLocalPath={copyLocalSkillsPath}
    librarySkills={librarySkills}
    onInstallSkill={installLibrarySkill}
    onQueryChange={setQuery}
    onRefreshDefinition={() => loadDefinition()}
    onRefreshLocal={refreshLocalWorkspace}
    onRemoveSkill={removeSkill}
    onSearch={searchCatalogue}
    onSelectAgent={(nextBotUID) => {
      selectedBotUIDRef.current = nextBotUID;
      requestedBotSwitchRef.current = nextBotUID;
      rememberSkillHubBotUID(user?.uid, nextBotUID);
      localRequestRef.current += 1;
      setSelectedBotUID(nextBotUID);
    }}
    onShareLocalSkill={shareLocalSkill}
    query={query}
    saving={saving}
    selectedAgentName={selectedAgent ? botLabel(selectedAgent) : ''}
    selectedAgentRelation={selectedAgent?.relation || 'owner'}
    selectedBotUID={selectedBotUID}
    selectedDeviceID={selectedDeviceID}
    sharingSkill={sharingSkill}
    skillAction={skillAction}
  />;
}
