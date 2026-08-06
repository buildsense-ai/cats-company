import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Bot, Check, Clipboard, FolderOpen, Link2, Package, RefreshCw, Search, Share2, Trash2 } from 'lucide-react';
import { api, requestSkillHubDeviceTool } from '../api';
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

const wait = (delayMs) => new Promise((resolve) => setTimeout(resolve, delayMs));

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
  return bot?.display_name || bot?.displayName || bot?.username || `Bot ${bot?.uid}`;
}

function isExactHash(value) {
  return /^[0-9a-f]{64}$/.test(String(value || ''));
}

export default function SkillHubView({ user }) {
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

  useEffect(() => {
    selectedBotUIDRef.current = selectedBotUID;
    saveRequestRef.current += 1;
    setSaving(false);
    setDefinitionError('');
  }, [selectedBotUID]);

  const definitionReady = Boolean(
    selectedBotUID
    && definitionBotUID === selectedBotUID
    && !loadingDefinition,
  );

  const installedByID = useMemo(() => new Map(
    (definition.skills || []).map((skill) => [skill.skillId, skill]),
  ), [definition.skills]);

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
        const firstUID = botUID(owned[0]);
        return firstUID ? String(firstUID) : '';
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
      setDefinitionError(error?.message || '无法读取当前 Bot 的 Skills 配置');
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

  const loadLocalWorkspace = useCallback(async (botUID = selectedBotUIDRef.current, deviceID = selectedDeviceID) => {
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
        await invoke(SKILLHUB_DEVICE_TOOLS.switchBot, {}, 10_000);
        if (!isCurrentRequest()) return;
        setLocalNotice('正在切换本地 Bot，等待 XiaoBa 重新连接…');
        let lastError = error;
        for (let attempt = 0; attempt < 12; attempt += 1) {
          await wait(attempt === 0 ? 2_000 : 1_500);
          if (!isCurrentRequest()) return;
          try {
            workspace = await invoke(SKILLHUB_DEVICE_TOOLS.workspace, {}, 8_000);
            break;
          } catch (retryError) {
            lastError = retryError;
          }
        }
        if (!workspace) throw lastError;
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
    loadBots().catch((error) => setDefinitionError(error?.message || '无法读取 Bot 列表'));
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
    let resolved = skill;
    if (!resolved.latestVersion || !isExactHash(resolved.contentHash)) {
      try {
        const detail = await api.getSkillHubSkill(skill.skillId);
        if (
          initiatingBotUID !== selectedBotUIDRef.current
          || definitionBotUID !== initiatingBotUID
        ) return;
        resolved = resolveSkillHubEntry(skill, detail);
      } catch (error) {
        if (
          initiatingBotUID === selectedBotUIDRef.current
          && definitionBotUID === initiatingBotUID
        ) setDefinitionError(error?.message || '无法读取 Skill 版本信息');
        return;
      }
    }
    if (
      initiatingBotUID !== selectedBotUIDRef.current
      || definitionBotUID !== initiatingBotUID
    ) return;
    if (!resolved.latestVersion || !isExactHash(resolved.contentHash)) {
      setDefinitionError('SkillHub 没有返回可绑定版本的完整哈希，无法安全关联到 Bot。');
      return;
    }
    const nextRef = {
      source: 'skillhub',
      skillId: resolved.skillId,
      version: resolved.latestVersion,
      contentHash: resolved.contentHash,
    };
    await saveSkills(upsertSkillRef(initiatingSkills, nextRef), {
      botUID: initiatingBotUID,
      revision: initiatingRevision,
    });
  };

  const removeSkill = async (skillID) => {
    await saveSkills(definition.skills.filter((skill) => skill.skillId !== skillID));
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
        throw new Error('Skill 已分享到全局 SkillHub，但绑定当前 Bot 失败，请刷新 Bot 配置后重试。');
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
      setLocalNotice(`“${localSkill.name}”已分享到全局 SkillHub，并绑定到当前 Bot。`);
    } catch (error) {
      if (requestedBotUID === selectedBotUIDRef.current) {
        if (bound) {
          setLocalSkillsError(error?.message || 'Skill 已分享并绑定当前 Bot，但本地工作区暂未完成对齐。');
          return;
        }
        if (uploaded) {
          setLocalSkillsError('Skill 已进入全局 SkillHub，但暂未绑定到当前 Bot，请刷新后重试绑定。');
          return;
        }
        setLocalSkillsError(error?.message || '分享本地 Skill 失败');
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

  return (
    <main className="cc-skillhub-page">
      <header className="cc-skillhub-header">
        <div className="cc-skillhub-heading-copy">
          <h1>SkillHub</h1>
          <p>管理当前 Bot 的本地 Skills 与精确版本引用。</p>
        </div>
        <div className="cc-skillhub-pickers">
          <label className="cc-skillhub-bot-picker">
            <span><Bot size={14} /> 当前 Bot</span>
            <select
              value={selectedBotUID}
              disabled={loadingBots || bots.length === 0 || Boolean(sharingSkill)}
              onChange={(event) => {
                selectedBotUIDRef.current = event.target.value;
                localRequestRef.current += 1;
                setSelectedBotUID(event.target.value);
              }}
            >
              {bots.length === 0 && <option value="">暂无自己拥有的 Bot</option>}
              {bots.map((bot) => <option key={botUID(bot)} value={botUID(bot)}>{botLabel(bot)}</option>)}
            </select>
          </label>
          <label className="cc-skillhub-bot-picker">
            <span><FolderOpen size={14} /> 本地 XiaoBa</span>
            <select
              value={selectedDeviceID}
              disabled={loadingDevices || devices.length === 0 || Boolean(sharingSkill)}
              onChange={(event) => {
                selectedDeviceIDRef.current = event.target.value;
                localRequestRef.current += 1;
                if (!event.target.value) setLocalSkillsError('');
                setSelectedDeviceID(event.target.value);
              }}
            >
              {devices.length === 0 && <option value="">暂无支持 SkillHub 的在线设备</option>}
              {devices.length > 1 && <option value="">请选择要操作的设备</option>}
              {devices.map((device) => (
                <option key={device.deviceId} value={device.deviceId}>
                  {device.displayName || device.deviceId}
                </option>
              ))}
            </select>
          </label>
        </div>
      </header>

      {definitionError && <div className="cc-skillhub-alert error" role="alert">{definitionError}</div>}

      <div className="cc-skillhub-workspace">
        <section className="cc-skillhub-local">
          <div className="cc-skillhub-section-heading">
            <div>
              <h2>本地 Skills</h2>
              <span>当前 Bot 在 XiaoBa 中实际加载的工作区</span>
            </div>
            <div className="cc-skillhub-local-actions">
              <button
                type="button"
                className="cc-skillhub-icon-button"
                title="刷新本地 Skills"
                aria-label="刷新本地 Skills"
                onClick={() => loadLocalWorkspace()}
                disabled={!selectedBotUID || !selectedDeviceID || loadingLocalSkills || Boolean(sharingSkill)}
              >
                <RefreshCw size={15} />
                <span className="cc-skillhub-visually-hidden">刷新本地</span>
              </button>
            </div>
          </div>
          {localSkillsPath && (
            <div className="cc-skillhub-local-path" title={localSkillsPath}>
              <FolderOpen size={14} />
              <code>{localSkillsPath}</code>
              <button
                type="button"
                className="cc-skillhub-icon-button"
                title="复制 Skills 路径"
                aria-label="复制 Skills 路径"
                onClick={copyLocalSkillsPath}
              >
                <Clipboard size={14} />
                <span className="cc-skillhub-visually-hidden">复制 Skills 路径</span>
              </button>
            </div>
          )}
          {!loadingDevices && devices.length === 0 && (
            <div className="cc-skillhub-alert error" role="alert">没有检测到支持该功能的在线 XiaoBa，请启动或更新本地 XiaoBa。</div>
          )}
          {!loadingDevices && devices.length > 1 && !selectedDeviceID && (
            <div className="cc-skillhub-empty">请选择要操作的本地 XiaoBa，避免修改到其他电脑。</div>
          )}
          {localNotice && <div className="cc-skillhub-alert success" role="status">{localNotice}</div>}
          {localSkillsError ? (
            <div className="cc-skillhub-alert error" role="alert">{localSkillsError}</div>
          ) : loadingLocalSkills ? (
            <div className="cc-skillhub-empty">正在切换本地 Bot 并同步 Skills…</div>
          ) : localSkills.length === 0 ? (
            <div className="cc-skillhub-empty">当前本地工作区还没有可用 Skill。</div>
          ) : (
            <div className="cc-skillhub-local-grid">
              {localSkills.map((skill) => {
                const reference = skill.skillHub?.reference;
                const installedReference = reference?.skillId ? installedByID.get(reference.skillId) : null;
                const shared = isLocalSkillShared(skill, installedReference);
                const canShare = skill.canShare !== false && skill.source !== 'system' && !shared;
                const sharedVersion = skill.skillHub?.version || reference?.version;
                return (
                  <article key={`${skill.relativePath}:${skill.name}`} className="cc-skillhub-local-card">
                    <div className="cc-skillhub-local-card-title">
                      <strong>{skill.name}</strong>
                      <span className={`cc-skillhub-status ${shared ? 'synced' : 'local'}`}>
                        {shared ? `已分享 v${sharedVersion}` : '仅本地'}
                      </span>
                    </div>
                    <p title={skill.description || '暂无描述'}>{skill.description || '暂无描述'}</p>
                    <code title={skill.path || skill.relativePath}>{skill.relativePath || skill.path}</code>
                    {shared ? (
                      <div className="cc-skillhub-complete-state"><Check size={14} /> 已分享到 SkillHub</div>
                    ) : canShare ? (
                      <button
                        type="button"
                        disabled={!definitionReady || loadingLocalSkills || saving || Boolean(sharingSkill)}
                        onClick={() => shareLocalSkill(skill)}
                      >
                        <Share2 size={14} />
                        {sharingSkill === skill.name ? '正在分享…' : '分享到 SkillHub'}
                      </button>
                    ) : (
                      <div className="cc-skillhub-unavailable-state">此 Skill 不可分享</div>
                    )}
                  </article>
                );
              })}
            </div>
          )}
        </section>

        <section className="cc-skillhub-installed">
          <div className="cc-skillhub-section-heading">
            <div>
              <h2>当前 Bot 已配置</h2>
              <span>{definition.skills.length} 个 Skill</span>
            </div>
            <button
              type="button"
              className="cc-skillhub-icon-button"
              title="刷新 Bot 配置"
              aria-label="刷新 Bot 配置"
              onClick={() => loadDefinition()}
              disabled={!selectedBotUID || loadingDefinition || saving || Boolean(sharingSkill)}
            >
              <RefreshCw size={15} />
              <span className="cc-skillhub-visually-hidden">刷新</span>
            </button>
          </div>
          {!selectedBotUID ? (
            <div className="cc-skillhub-empty">先创建或选择一个自己拥有的 Bot。</div>
          ) : loadingDefinition ? (
            <div className="cc-skillhub-empty">正在读取 BotDefinition…</div>
          ) : definition.skills.length === 0 ? (
            <div className="cc-skillhub-empty">这个 Bot 还没有配置 Skill。</div>
          ) : (
            <div className="cc-skillhub-installed-list">
              {definition.skills.map((skill) => (
                <article key={skill.skillId} className="cc-skillhub-installed-item">
                  <div>
                    <strong>{skill.skillId}</strong>
                    <span>v{skill.version}</span>
                  </div>
                  <button
                    type="button"
                    className="cc-skillhub-icon-button danger"
                    title={`从当前 Bot 移除 ${skill.skillId}`}
                    aria-label={`从当前 Bot 移除 ${skill.skillId}`}
                    disabled={saving || Boolean(sharingSkill) || !definitionReady}
                    onClick={() => removeSkill(skill.skillId)}
                  >
                    <Trash2 size={14} />
                    <span className="cc-skillhub-visually-hidden">移除</span>
                  </button>
                </article>
              ))}
            </div>
          )}
        </section>
      </div>

      <section className="cc-skillhub-catalogue">
        <div className="cc-skillhub-section-heading cc-skillhub-catalogue-heading">
          <div>
            <h2>全局 SkillHub</h2>
            <span>搜索并绑定团队共享的 Skills</span>
          </div>
        </div>
        <form
          className="cc-skillhub-search"
          onSubmit={(event) => {
            event.preventDefault();
            searchCatalogue(query).catch(() => {});
          }}
        >
          <Search size={17} />
          <input
            value={query}
            aria-label="搜索 Skill 名称或描述"
            onChange={(event) => setQuery(event.target.value)}
            placeholder="搜索 Skill 名称或描述"
          />
          <button type="submit" disabled={loadingCatalogue}>搜索</button>
        </form>
        {catalogueError ? (
          <div className="cc-skillhub-alert error" role="alert">{catalogueError}</div>
        ) : loadingCatalogue ? (
          <div className="cc-skillhub-empty">正在读取 SkillHub…</div>
        ) : catalogue.length === 0 ? (
          <div className="cc-skillhub-empty">没有找到匹配的 Skill。</div>
        ) : (
          <div className="cc-skillhub-grid">
            {catalogue.map((skill) => {
              const installed = installedByID.get(skill.skillId);
              const sameVersion = Boolean(
                installed
                && skill.latestVersion
                && isExactHash(skill.contentHash)
                && installed.version === skill.latestVersion
                && installed.contentHash === skill.contentHash,
              );
              return (
                <article key={skill.skillId} className="cc-skillhub-card">
                  <div className="cc-skillhub-card-title">
                    <Package size={18} />
                    <div>
                      <h3>{skill.displayName || skill.skillId}</h3>
                      <span>{skill.skillId}</span>
                    </div>
                  </div>
                  <p title={skill.description || '暂无描述'}>{skill.description || '暂无描述'}</p>
                  <div className="cc-skillhub-card-meta">
                    <span>{skill.author || 'SkillHub'}</span>
                    <span>{skill.latestVersion ? `v${skill.latestVersion}` : '版本待确认'}</span>
                  </div>
                  {sameVersion ? (
                    <div className="cc-skillhub-complete-state"><Check size={14} /> 已绑定</div>
                  ) : (
                    <button
                      type="button"
                      disabled={!definitionReady || saving || Boolean(sharingSkill)}
                      onClick={() => installSkill(skill)}
                    >
                      <Link2 size={14} />
                      {installed ? '更新绑定' : '绑定到当前 Bot'}
                    </button>
                  )}
                </article>
              );
            })}
          </div>
        )}
      </section>
    </main>
  );
}
