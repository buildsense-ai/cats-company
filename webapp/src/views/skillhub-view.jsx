import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Bot, Check, Clipboard, FolderOpen, Link2, Package, RefreshCw, Search, Share2, Trash2 } from 'lucide-react';
import { api } from '../api';
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
        throw new Error('本地 XiaoBa 当前 Bot 与页面选择不一致，请重新选择 Bot 后再试。');
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
    loadBots().catch((error) => setDefinitionError(error?.message || '无法读取 Bot 列表'));
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
        throw new Error('本地 XiaoBa 登录的 CatsCo 账号与当前 WebApp 账号不一致，已停止分享。');
      }
      if (String(localStatus?.botUid || '') !== requestedBotUID) {
        throw new Error('本地 XiaoBa 当前 Bot 与页面选择不一致，已停止分享，请重新选择 Bot。');
      }
      const shared = await api.shareLocalSkill(localSkill.name, requestedBotUID, user?.uid);
      if (String(shared?.botUid || '') !== requestedBotUID) {
        throw new Error('Skill 分享完成时本地 Bot 已发生变化，已停止自动绑定。');
      }
      if (shared?.requiresConfirmation) {
        throw new Error('SkillHub 已存在同名 Skill，本期暂不支持升级或覆盖，请先更换 Skill 名称。');
      }
      uploaded = true;
      const sharedSkillID = String(shared?.skill?.id || '').trim();
      if (!sharedSkillID) throw new Error('SkillHub 没有返回已分享 Skill 的标识。');
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
        throw new Error('SkillHub 没有返回可绑定版本的完整哈希。');
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
        throw new Error('Skill 已分享到全局 SkillHub，但绑定当前 Bot 失败，请刷新 Bot 配置后重试。');
      }
      if (requestedBotUID !== selectedBotUIDRef.current) return;
      await Promise.all([
        searchCatalogue(query),
        loadLocalWorkspace(requestedBotUID),
      ]);
      if (requestedBotUID !== selectedBotUIDRef.current) return;
      setLocalNotice(`“${localSkill.name}”已分享到全局 SkillHub，并绑定到当前 Bot。`);
    } catch (error) {
      if (requestedBotUID === selectedBotUIDRef.current) {
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
        <div>
          <span className="cc-skillhub-eyebrow"><Package size={15} /> SkillHub</span>
          <h1>为当前 Bot 配置 Skills</h1>
          <p>此处只保存 Skill 的精确版本引用；XiaoBa 在线后会根据 BotDefinition 同步到对应的本地工作区。</p>
        </div>
        <label className="cc-skillhub-bot-picker">
          <span><Bot size={14} /> 当前 Bot</span>
          <select
            value={selectedBotUID}
            disabled={loadingBots || bots.length === 0 || Boolean(sharingSkill)}
            onChange={(event) => setSelectedBotUID(event.target.value)}
          >
            {bots.length === 0 && <option value="">暂无自己拥有的 Bot</option>}
            {bots.map((bot) => <option key={botUID(bot)} value={botUID(bot)}>{botLabel(bot)}</option>)}
          </select>
        </label>
      </header>

      {definitionError && <div className="cc-skillhub-alert error">{definitionError}</div>}

      <section className="cc-skillhub-installed">
        <div className="cc-skillhub-section-heading">
          <div>
            <h2>当前 Bot 已配置</h2>
            <span>{definition.skills.length} 个 Skill</span>
          </div>
          <button type="button" onClick={() => loadDefinition()} disabled={!selectedBotUID || loadingDefinition || saving || Boolean(sharingSkill)}>
            <RefreshCw size={14} /> 刷新
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
                  className="danger"
                  disabled={saving || Boolean(sharingSkill) || !definitionReady}
                  onClick={() => removeSkill(skill.skillId)}
                >
                  <Trash2 size={14} /> 移除
                </button>
              </article>
            ))}
          </div>
        )}
      </section>

      {LOCAL_XIAOBA_BRIDGE_ENABLED && <section className="cc-skillhub-local">
        <div className="cc-skillhub-section-heading">
          <div>
            <h2>本地 Skills</h2>
            <span>当前 Bot 在 XiaoBa 中实际加载的工作区</span>
          </div>
          <div className="cc-skillhub-local-actions">
            <button type="button" onClick={copyLocalSkillsPath} disabled={!localSkillsPath}>
              <Clipboard size={14} /> 复制 Skills 路径
            </button>
            <button type="button" onClick={() => loadLocalWorkspace()} disabled={!selectedBotUID || loadingLocalSkills || Boolean(sharingSkill)}>
              <RefreshCw size={14} /> 刷新本地
            </button>
          </div>
        </div>
        {localSkillsPath && (
          <div className="cc-skillhub-local-path"><FolderOpen size={14} /><code>{localSkillsPath}</code></div>
        )}
        {localNotice && <div className="cc-skillhub-alert success">{localNotice}</div>}
        {localSkillsError ? (
          <div className="cc-skillhub-alert error">{localSkillsError}</div>
        ) : loadingLocalSkills ? (
          <div className="cc-skillhub-empty">正在切换本地 Bot 并同步 Skills…</div>
        ) : localSkills.length === 0 ? (
          <div className="cc-skillhub-empty">当前本地工作区还没有可用 Skill。</div>
        ) : (
          <div className="cc-skillhub-local-grid">
            {localSkills.map((skill) => {
              const shared = Boolean(skill.skillHub?.author && skill.skillHub?.version);
              const canShare = skill.source !== 'system' && !shared;
              return (
                <article key={`${skill.relativePath}:${skill.name}`} className="cc-skillhub-local-card">
                  <div>
                    <strong>{skill.name}</strong>
                    <span className={`cc-skillhub-status ${shared ? 'synced' : 'local'}`}>
                      {shared ? `已分享 v${skill.skillHub.version}` : '仅本地'}
                    </span>
                  </div>
                  <p>{skill.description || '暂无描述'}</p>
                  <code>{skill.relativePath || skill.path}</code>
                  <button
                    type="button"
                    disabled={!canShare || !definitionReady || loadingLocalSkills || saving || Boolean(sharingSkill)}
                    onClick={() => shareLocalSkill(skill)}
                  >
                    {shared ? <Check size={14} /> : <Share2 size={14} />}
                    {shared ? '已分享到 SkillHub' : sharingSkill === skill.name ? '正在分享…' : '分享到 SkillHub'}
                  </button>
                </article>
              );
            })}
          </div>
        )}
      </section>}

      <section className="cc-skillhub-catalogue">
        <form
          className="cc-skillhub-search"
          onSubmit={(event) => {
            event.preventDefault();
            searchCatalogue(query).catch(() => {});
          }}
        >
          <Search size={17} />
          <input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="搜索 Skill 名称或描述" />
          <button type="submit" disabled={loadingCatalogue}>搜索</button>
        </form>
        {catalogueError ? (
          <div className="cc-skillhub-alert error">{catalogueError}</div>
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
                  <p>{skill.description || '暂无描述'}</p>
                  <div className="cc-skillhub-card-meta">
                    <span>{skill.author || 'SkillHub'}</span>
                    <span>{skill.latestVersion ? `v${skill.latestVersion}` : '版本待确认'}</span>
                  </div>
                  <button
                    type="button"
                    disabled={!definitionReady || sameVersion || saving || Boolean(sharingSkill)}
                    onClick={() => installSkill(skill)}
                  >
                    {sameVersion ? <Check size={14} /> : <Link2 size={14} />}
                    {sameVersion ? '已绑定' : installed ? '更新绑定' : '绑定到当前 Bot'}
                  </button>
                </article>
              );
            })}
          </div>
        )}
      </section>
    </main>
  );
}
