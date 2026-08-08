import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { api } from '../api';
import { useFeedback } from '../components/feedback-system';
import SkillHubContent from './skillhub-content';
import '../css/skillhub-view.css';

const LOCAL_XIAOBA_BRIDGE_ENABLED = import.meta.env.DEV;

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
  })).filter((skill) => skill.name);
}

export function upsertSkillRef(skills, nextRef) {
  return [...(skills || []).filter((skill) => skill.skillId !== nextRef.skillId), nextRef]
    .sort((left, right) => left.skillId.localeCompare(right.skillId));
}

function botUID(bot) {
  return bot?.uid ?? bot?.id ?? '';
}

export function resolveSkillHubEntry(skill, detail) {
  const base = normalizeSkillHubSkills([detail?.skill || detail?.version || detail])[0] || skill;
  if (base?.latestVersion && isExactHash(base?.contentHash)) return base;
  const versions = normalizeSkillHubSkills(detail?.versions || []);
  const versionEntry = versions.find((entry) => (
    base?.latestVersion && entry.latestVersion === base.latestVersion
  )) || versions.find((entry) => entry.isLatest === true || entry.is_latest === true)
    || (versions.length === 1 ? versions[0] : null);
  return versionEntry ? { ...base, ...versionEntry, skillId: base.skillId || skill.skillId } : base;
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
  const selectedBotUIDRef = useRef('');
  const definitionBotUIDRef = useRef('');
  const definitionRequestRef = useRef(0);
  const catalogueRequestRef = useRef(0);
  const localRequestRef = useRef(0);
  const localSwitchQueueRef = useRef(Promise.resolve());
  const saveRequestRef = useRef(0);

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

  const loadLocalWorkspace = useCallback(async (botUID = selectedBotUIDRef.current) => {
    const requestedBotUID = String(botUID || '');
    const requestID = localRequestRef.current + 1;
    localRequestRef.current = requestID;
    if (!requestedBotUID) {
      setLocalSkills([]);
      setLocalSkillsPath('');
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
    try {
      let switchError;
      const switchTask = localSwitchQueueRef.current.then(async () => {
        if (requestID !== localRequestRef.current) return;
        try {
          await api.switchLocalBot(requestedBotUID);
        } catch (error) {
          switchError = error;
        }
      });
      localSwitchQueueRef.current = switchTask.catch(() => {});
      await switchTask;
      if (switchError) throw switchError;
      if (
        requestID !== localRequestRef.current
        || requestedBotUID !== selectedBotUIDRef.current
      ) return;
      const [status, store, details] = await Promise.all([
        api.getLocalCatsStatus(),
        api.getLocalSkills(),
        api.getLocalStatusDetails(),
      ]);
      if (String(status?.botUid || '') !== requestedBotUID) {
        throw new Error('本地 XiaoBa 当前 Agent 与页面选择不一致，请重新选择 Agent 后再试。');
      }
      if (
        requestID !== localRequestRef.current
        || requestedBotUID !== selectedBotUIDRef.current
      ) return;
      setLocalSkills(normalizeLocalSkills(store));
      setLocalSkillsPath(String(details?.skillsPath || '').trim());
    } catch (error) {
      if (
        requestID !== localRequestRef.current
        || requestedBotUID !== selectedBotUIDRef.current
      ) return;
      setLocalSkills([]);
      setLocalSkillsPath('');
      setLocalSkillsError(error?.message || '无法连接本地 XiaoBa，请确认 XiaoBa Dashboard 已启动并完成 CatsCo 登录。');
    } finally {
      if (requestID === localRequestRef.current) setLoadingLocalSkills(false);
    }
  }, []);

  useEffect(() => {
    loadBots().catch((error) => setDefinitionError(error?.message || '无法读取 Agent 列表'));
    searchCatalogue('').catch(() => {});
  }, [loadBots, searchCatalogue]);

  useEffect(() => {
    loadDefinition(selectedBotUID).catch(() => {});
    if (LOCAL_XIAOBA_BRIDGE_ENABLED) loadLocalWorkspace(selectedBotUID).catch(() => {});
  }, [loadDefinition, loadLocalWorkspace, selectedBotUID]);

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
    const skillName = details?.displayName || skillID;
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
    if (!requestedBotUID || !definitionReady || saving || sharingSkill) return;
    const requestedRevision = definition.revision;
    const requestedSkills = definition.skills;
    setSharingSkill(localSkill.name);
    setLocalSkillsError('');
    setLocalNotice('');
    let uploaded = false;
    try {
      const localStatus = await api.getLocalCatsStatus();
      if (localStatus?.authStatus !== 'valid') {
        throw new Error(localStatus?.authError || '本地 XiaoBa 的 CatsCo 登录状态无效，请重新登录。');
      }
      if (String(localStatus?.user?.uid || '') !== String(user?.uid || '')) {
        throw new Error('本地 XiaoBa 登录的 CatsCo 账号与当前 WebApp 账号不一致，已停止发布。');
      }
      if (String(localStatus?.botUid || '') !== requestedBotUID) {
        throw new Error('本地 XiaoBa 当前 Agent 与页面选择不一致，已停止发布，请重新选择 Agent。');
      }
      const shared = await api.shareLocalSkill(localSkill.name, requestedBotUID, user?.uid);
      if (String(shared?.botUid || '') !== requestedBotUID) {
        throw new Error('能力发布完成时本地 Agent 已发生变化，已停止自动添加。');
      }
      if (shared?.requiresConfirmation) {
        throw new Error('SkillHub 已存在同名能力，本期暂不支持升级或覆盖，请先更换名称。');
      }
      uploaded = true;
      const sharedSkillID = String(shared?.skill?.id || '').trim();
      if (!sharedSkillID) throw new Error('SkillHub 没有返回已发布能力的标识。');
      let resolved = resolveSkillHubEntry({
        skillId: sharedSkillID,
        latestVersion: shared?.latestVersion,
        contentHash: shared?.contentHash,
      }, shared);
      if (!resolved.latestVersion || !isExactHash(resolved.contentHash)) {
        const detail = await api.getSkillHubSkill(sharedSkillID);
        resolved = resolveSkillHubEntry({ skillId: sharedSkillID }, detail);
      }
      if (!resolved.latestVersion || !isExactHash(resolved.contentHash)) {
        throw new Error('SkillHub 没有返回可添加版本的完整哈希。');
      }
      if (requestedBotUID !== selectedBotUIDRef.current) return;
      const saved = await saveSkills(upsertSkillRef(requestedSkills, {
        source: 'skillhub',
        skillId: sharedSkillID,
        version: resolved.latestVersion,
        contentHash: resolved.contentHash,
      }), {
        botUID: requestedBotUID,
        revision: requestedRevision,
      });
      if (!saved?.ok) {
        throw new Error('能力已发布到团队，但添加到当前 Agent 失败，请刷新 Agent 能力后重试。');
      }
      if (requestedBotUID !== selectedBotUIDRef.current) return;
      await Promise.all([
        searchCatalogue(query),
        loadLocalWorkspace(requestedBotUID),
      ]);
      if (requestedBotUID !== selectedBotUIDRef.current) return;
      setLocalNotice(`“${localSkill.name}”已发布到团队，并添加到当前 Agent。`);
    } catch (error) {
      if (requestedBotUID === selectedBotUIDRef.current) {
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
    installedByID={installedByID}
    isLocalEnabled={LOCAL_XIAOBA_BRIDGE_ENABLED}
    loadingBots={loadingBots}
    loadingCatalogue={loadingCatalogue}
    loadingDefinition={loadingDefinition}
    loadingLocalSkills={loadingLocalSkills}
    localNotice={localNotice}
    localSkills={localSkills}
    localSkillsError={localSkillsError}
    localSkillsPath={localSkillsPath}
    onChangeSection={setActiveSection}
    onCopySkill={copySkill}
    onCopyLocalPath={copyLocalSkillsPath}
    onInstallSkill={installSkill}
    onQueryChange={setQuery}
    onRefreshDefinition={() => loadDefinition()}
    onRefreshLocal={() => loadLocalWorkspace()}
    onRemoveSkill={removeSkill}
    onSearch={searchCatalogue}
    onSelectAgent={setSelectedBotUID}
    onShareLocalSkill={shareLocalSkill}
    query={query}
    saving={saving}
    selectedAgentName={selectedAgent ? botLabel(selectedAgent) : ''}
    selectedBotUID={selectedBotUID}
    sharingSkill={sharingSkill}
    skillAction={skillAction}
  />;
}
