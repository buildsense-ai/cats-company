import React, { useCallback, useEffect, useState } from 'react';
import { Plus, RefreshCw, X } from 'lucide-react';
import { api } from '../api';

// Agent 工具（CatsCo 定制 agent 的工具集管理；设计 §3.3-9"模式二：默认开放 + 个人清单"）。
// 添加/删除跟账号、跨设备生效：删除后从你的 Agent 工具集（tools/list）隐去、调用也被拒绝；
// 重新添加即恢复。变更即时同步在线 MCP 会话（服务端 list_changed）。
export default function ServiceAgentsPanel({ onClose }) {
  const [agents, setAgents] = useState([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [pending, setPending] = useState(() => new Set());

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const data = await api.getServiceAgentsCatalog();
      setAgents(Array.isArray(data?.agents) ? data.agents : []);
    } catch (err) {
      setError(err?.message || 'Agent 工具加载失败，请稍后重试。');
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    load();
  }, [load]);

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

  return (
    <aside className="v3-service-agents-panel" aria-label="Agent 工具">
      <div className="v3-service-agents-header">
        <span>Agent 工具</span>
        <div className="v3-service-agents-header-actions">
          <button type="button" onClick={load} aria-label="刷新 Agent 工具" title="刷新">
            <RefreshCw size={16} />
          </button>
          <button type="button" onClick={onClose} aria-label="关闭 Agent 工具" title="关闭">
            <X size={16} />
          </button>
        </div>
      </div>
      <p className="v3-service-agents-hint">
        CatsCo 定制 Agent（以工具形式接入）。添加的会出现在你的 Agent 工具集中；删除的将被移除，调用也会被拒绝。
      </p>
      {error && <div className="v3-service-agents-error" role="alert">{error}</div>}
      {loading ? (
        <div className="v3-service-agents-empty">加载中…</div>
      ) : agents.length === 0 ? (
        <div className="v3-service-agents-empty">暂无可用工具。</div>
      ) : (
        <ul className="v3-service-agents-list">
          {agents.map((agent) => {
            const busy = pending.has(agent.slug);
            const hidden = Boolean(agent.hidden);
            return (
              <li key={agent.slug} className={`v3-service-agent-row${hidden ? ' is-hidden' : ''}`}>
                <div className="v3-service-agent-info">
                  <div className="v3-service-agent-name">
                    {agent.name || agent.slug}
                    <span className="v3-service-agent-version">v{agent.version}</span>
                  </div>
                  <div className="v3-service-agent-slug">catsco_{agent.slug}</div>
                </div>
                <button
                  type="button"
                  className={`v3-service-agent-toggle${hidden ? ' is-add' : ' is-remove'}`}
                  disabled={busy}
                  onClick={() => toggleSubscription(agent)}
                  aria-label={`${hidden ? '添加' : '删除'}${agent.name || agent.slug}`}
                  title={hidden ? '重新添加该工具' : '从我的清单中删除该工具'}
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
