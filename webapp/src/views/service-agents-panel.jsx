import React, { useCallback, useEffect, useState } from 'react';
import { AlertTriangle, Plus, RefreshCw, Sparkles, X } from 'lucide-react';
import { api } from '../api';

// Agent 工具（CatsCo 定制 agent 的工具集管理；设计 §3.3-9"模式二：默认开放 + 个人清单"）。
// 添加/删除跟账号、跨设备生效：删除后从你的 Agent 工具集（tools/list）隐去、调用也被拒绝；
// 重新添加即恢复。变更即时同步在线 MCP 会话（服务端 list_changed）。
export default function ServiceAgentsPanel({ onClose }) {
  const [agents, setAgents] = useState([]);
  const [bots, setBots] = useState([]);
  const [scopeBotUid, setScopeBotUid] = useState('');
  const [botDefinition, setBotDefinition] = useState(null);
  const [scopeLoading, setScopeLoading] = useState(false);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [pending, setPending] = useState(() => new Set());

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const [catalogData, botsData] = await Promise.all([
        api.getServiceAgentsCatalog(),
        typeof api.getMyBots === 'function' ? api.getMyBots().catch(() => null) : Promise.resolve(null),
      ]);
      setAgents(Array.isArray(catalogData?.agents) ? catalogData.agents : []);
      const rawBots = botsData?.bots ?? botsData?.agents ?? botsData;
      setBots(Array.isArray(rawBots) ? rawBots : []);
    } catch (err) {
      setError(err?.message || 'Agent 工具加载失败，请稍后重试。');
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  // 切换到某个 Agent：读取它的 definition（serviceAgents 清单）。
  useEffect(() => {
    if (!scopeBotUid) {
      setBotDefinition(null);
      return undefined;
    }
    let cancelled = false;
    (async () => {
      setScopeLoading(true);
      setError('');
      try {
        const data = await api.getBotDefinitionPrompt(scopeBotUid);
        if (cancelled) return;
        const rawAgents = data?.definition?.serviceAgents;
        setBotDefinition({
          revision: Number(data?.revision ?? 0),
          serviceAgents: Array.isArray(rawAgents) ? rawAgents : undefined,
        });
      } catch (err) {
        if (cancelled) return;
        setBotDefinition(null);
        setError(err?.message || '读取该 Agent 的工具清单失败，请稍后重试。');
      } finally {
        if (!cancelled) setScopeLoading(false);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [scopeBotUid]);

  const toggleSubscription = useCallback(async (agent) => {
    const slug = agent?.slug;
    if (!slug || pending.has(slug)) return;
    const nextHidden = !agent.hidden;
    setPending((prev) => new Set(prev).add(slug));
    setError('');
    try {
      const result = nextHidden
        ? await api.removeServiceAgentSubscription(slug)
        : await api.addServiceAgentSubscription(slug);
      const hidden = Boolean(result?.hidden);
      setAgents((prev) => prev.map((item) => (item.slug === slug ? { ...item, hidden } : item)));
    } catch (err) {
      setError(err?.message || (nextHidden ? '删除失败，请稍后重试。' : '添加失败，请稍后重试。'));
    } finally {
      setPending((prev) => {
        const next = new Set(prev);
        next.delete(slug);
        return next;
      });
    }
  }, [pending]);

  // 指定 Agent 作用域：把该 slug 加入/移出 bot definition.serviceAgents 并保存（带 revision）。
  const toggleBotAgent = useCallback(async (agent) => {
    const slug = agent?.slug;
    if (!slug || !scopeBotUid || !botDefinition || pending.has(slug)) return;
    const accountVisible = agents.filter((item) => !item.hidden).map((item) => item.slug);
    const current = botDefinition.serviceAgents === undefined ? accountVisible : botDefinition.serviceAgents;
    const next = current.includes(slug) ? current.filter((item) => item !== slug) : [...current, slug];
    setPending((prev) => new Set(prev).add(slug));
    setError('');
    try {
      const result = await api.updateBotDefinitionServiceAgents(scopeBotUid, botDefinition.revision, next);
      const rawAgents = result?.definition?.serviceAgents;
      setBotDefinition({
        revision: Number(result?.revision ?? botDefinition.revision + 1),
        serviceAgents: Array.isArray(rawAgents) ? rawAgents : next,
      });
    } catch (err) {
      setError(err?.message || '保存该 Agent 的工具清单失败，请稍后重试。');
    } finally {
      setPending((prev) => {
        const nextPending = new Set(prev);
        nextPending.delete(slug);
        return nextPending;
      });
    }
  }, [agents, botDefinition, pending, scopeBotUid]);

  return (
    <aside className="v3-service-agents-panel" aria-label="Agent 工具">
      <div className="v3-service-agents-header">
        <span className="v3-service-agents-title">
          Agent 工具
          {!loading && agents.length > 0 ? (
            <span className="v3-service-agents-count" aria-label={`共 ${agents.length} 个工具`}>
              {agents.length}
            </span>
          ) : null}
        </span>
        <div className="v3-service-agents-header-actions">
          <button
            type="button"
            onClick={load}
            aria-label="刷新 Agent 工具"
            title="刷新"
            className={loading ? 'is-spinning' : undefined}
          >
            <RefreshCw size={16} />
          </button>
          <button type="button" onClick={onClose} aria-label="关闭 Agent 工具" title="关闭">
            <X size={16} />
          </button>
        </div>
      </div>
      <div className="v3-service-agents-scope">
        <label htmlFor="v3-service-agents-scope-select">配置范围</label>
        <select
          id="v3-service-agents-scope-select"
          value={scopeBotUid}
          onChange={(event) => setScopeBotUid(event.target.value)}
        >
          <option value="">账号（所有 Agent 共用）</option>
          {bots.map((bot) => (
            <option key={bot.uid} value={String(bot.uid)}>
              {bot.name || bot.display_name || bot.username || bot.uid}
            </option>
          ))}
        </select>
      </div>
      <p className="v3-service-agents-hint">
        {scopeBotUid
          ? '为这个 Agent 配置可调用的工具；未配置过时默认全部可见，保存后按清单裁剪（只影响该 Agent）。'
          : 'CatsCo 定制 Agent（以工具形式接入）。添加的会出现在你的账号工具集中；删除的将被移除，调用也会被拒绝。'}
      </p>
      {scopeBotUid && scopeLoading ? <div className="v3-service-agents-empty">读取该 Agent 的工具清单…</div> : null}
      {error && (
        <div className="v3-service-agents-error" role="alert">
          <AlertTriangle size={14} aria-hidden="true" />
          <span>{error}</span>
          <button type="button" onClick={load} disabled={loading}>
            重试
          </button>
        </div>
      )}
      {loading ? (
        <ul className="v3-service-agents-skeleton" aria-busy="true" aria-label="Agent 工具加载中">
          {[0, 1, 2].map((index) => (
            <li key={index} className="v3-service-agents-skeleton-row">
              <span className="v3-service-agents-skeleton-avatar" />
              <span className="v3-service-agents-skeleton-lines">
                <span className="v3-service-agents-skeleton-line is-wide" />
                <span className="v3-service-agents-skeleton-line" />
              </span>
            </li>
          ))}
        </ul>
      ) : agents.length === 0 ? (
        <div className="v3-service-agents-placeholder" role="status">
          <span className="v3-service-agents-placeholder-icon" aria-hidden="true">
            <Sparkles size={22} />
          </span>
          <p className="v3-service-agents-placeholder-title">还没有可用的 Agent 工具</p>
          <p className="v3-service-agents-placeholder-text">
            上架后的定制 Agent 会以 <code>catsco_&lt;slug&gt;</code> 的形式出现在这里；
            当前目录里还没有对你可见的应用。
          </p>
          <button type="button" className="v3-service-agents-placeholder-action" onClick={load} disabled={loading}>
            <RefreshCw size={14} />
            刷新目录
          </button>
        </div>
      ) : (
        <ul className="v3-service-agents-list">
          {agents.map((agent) => {
            const busy = pending.has(agent.slug) || (scopeBotUid && scopeLoading);
            const accountHidden = Boolean(agent.hidden);
            const selected = scopeBotUid
              ? (accountHidden
                ? false
                : botDefinition?.serviceAgents === undefined
                  ? true
                  : botDefinition.serviceAgents.includes(agent.slug))
              : !accountHidden;
            const hidden = !selected;
            const toggle = scopeBotUid ? toggleBotAgent : toggleSubscription;
            return (
              <li key={agent.slug} className={`v3-service-agent-row${hidden ? ' is-hidden' : ''}`}>
                <div className="v3-service-agent-info">
                  <div className="v3-service-agent-name">
                    {agent.name || agent.slug}
                    <span className="v3-service-agent-version">v{agent.version}</span>
                    {agent.mine ? <span className="v3-service-agent-badge">我的</span> : null}
                    {agent.visibility === 'private' ? (
                      <span className="v3-service-agent-badge is-private">私有</span>
                    ) : null}
                  </div>
                  <div className="v3-service-agent-slug">catsco_{agent.slug}</div>
                  {scopeBotUid && accountHidden ? (
                    <div className="v3-service-agent-note">账号清单中已移除</div>
                  ) : null}
                </div>
                <button
                  type="button"
                  className={`v3-service-agent-toggle${hidden ? ' is-add' : ' is-remove'}`}
                  disabled={busy || (scopeBotUid && accountHidden)}
                  onClick={() => toggle(agent)}
                  aria-label={`${hidden ? '添加' : '删除'}${agent.name || agent.slug}`}
                  title={
                    scopeBotUid && accountHidden
                      ? '先在账号清单中添加该工具'
                      : hidden
                        ? (scopeBotUid ? '添加到该 Agent 的工具清单' : '重新添加该工具')
                        : (scopeBotUid ? '从该 Agent 的工具清单移除' : '从我的清单中删除该工具')
                  }
                >
                  {hidden ? <Plus size={15} /> : <X size={15} />}
                  <span>{hidden ? '添加' : '删除'}</span>
                </button>
              </li>
            );
          })}
        </ul>
      )}
    </aside>
  );
}

export { ServiceAgentsPanel };
