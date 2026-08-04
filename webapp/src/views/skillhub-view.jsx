import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Bot, Check, Link2, Package, RefreshCw, Search, Trash2 } from 'lucide-react';
import { api } from '../api';
import '../css/skillhub-view.css';

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
  const [saving, setSaving] = useState(false);
  const selectedBotUIDRef = useRef('');
  const definitionBotUIDRef = useRef('');
  const definitionRequestRef = useRef(0);
  const catalogueRequestRef = useRef(0);
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

  useEffect(() => {
    loadBots().catch((error) => setDefinitionError(error?.message || '无法读取 Bot 列表'));
    searchCatalogue('').catch(() => {});
  }, [loadBots, searchCatalogue]);

  useEffect(() => {
    loadDefinition(selectedBotUID).catch(() => {});
  }, [loadDefinition, selectedBotUID]);

  const saveSkills = async (skills, expected = {}) => {
    const requestedBotUID = expected.botUID || selectedBotUIDRef.current;
    const requestedRevision = expected.revision ?? definition.revision;
    if (
      !requestedBotUID
      || requestedBotUID !== selectedBotUIDRef.current
      || definitionBotUID !== requestedBotUID
      || loadingDefinition
    ) return;
    if (saving) return;
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
      ) return;
      setDefinition({
        ...next,
        skills: Array.isArray(next?.skills) ? next.skills : [],
        revision: Number(next?.revision || 0),
      });
      definitionBotUIDRef.current = requestedBotUID;
      setDefinitionBotUID(requestedBotUID);
    } catch (error) {
      if (
        requestID !== saveRequestRef.current
        || requestedBotUID !== selectedBotUIDRef.current
      ) return;
      if (error?.status === 409) {
        await loadDefinition(requestedBotUID);
        if (
          requestID === saveRequestRef.current
          && requestedBotUID === selectedBotUIDRef.current
        ) setDefinitionError('配置刚刚被其他操作更新，已刷新，请再试一次。');
      } else {
        setDefinitionError(error?.message || '保存 Skills 配置失败');
      }
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
            disabled={loadingBots || bots.length === 0}
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
          <button type="button" onClick={() => loadDefinition()} disabled={!selectedBotUID || loadingDefinition || saving}>
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
                  disabled={saving || !definitionReady}
                  onClick={() => removeSkill(skill.skillId)}
                >
                  <Trash2 size={14} /> 移除
                </button>
              </article>
            ))}
          </div>
        )}
      </section>

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
                    disabled={!definitionReady || sameVersion || saving}
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
