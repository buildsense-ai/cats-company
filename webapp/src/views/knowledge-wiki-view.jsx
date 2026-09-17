import React, { useEffect, useMemo, useState } from 'react';
import { BookOpen, CloudOff, LoaderCircle, X } from 'lucide-react';
import { api, connectWS, disconnectWS, requestSkillHubDeviceTool } from '../api';
import './knowledge-wiki-view.css';

function parseAgentID(pathname) {
  const match = String(pathname || '').match(/^\/wiki\/agents\/([^/]+)\/?$/);
  return match ? decodeURIComponent(match[1]) : '';
}

export function knowledgeWikiAgentID(pathname) {
  return parseAgentID(pathname);
}

export default function KnowledgeWikiView({ location = window.location } = {}) {
  const agentUid = useMemo(() => parseAgentID(location.pathname), [location.pathname]);
  const [state, setState] = useState({ status: 'loading', data: null, error: '' });
  const [selected, setSelected] = useState(null);
  const [loadingMore, setLoadingMore] = useState(false);

  useEffect(() => {
    let active = true;
    if (!agentUid) {
      setState({ status: 'error', data: null, error: '知识库入口无效' });
      return () => { active = false; };
    }
    setState({ status: 'loading', data: null, error: '' });
    api.getKnowledgeWikiManifest(agentUid)
      .then(async (handoff) => {
        if (!handoff?.online) throw Object.assign(new Error('当前 Agent 设备不在线，请先启动本地 XiaoBa。'), { code: 'INSTANCE_OFFLINE' });
        const ticket = await api.getKnowledgeWikiWebSocketTicket(agentUid);
        if (!ticket?.token) throw new Error('无法建立知识库实时连接，请重新从 AI 助手管理进入。');
        await new Promise((resolve, reject) => {
          let done = false;
          const finish = (fn) => { if (!done) { done = true; clearTimeout(timer); fn(); } };
          const timer = setTimeout(() => finish(() => reject(new Error('知识库实时连接超时，请稍后重试。'))), 10000);
          const started = connectWS((message) => {
            if (message?._type === 'ws_open') finish(resolve);
            if (message?._type === 'ws_auth_expired') finish(() => reject(new Error('知识库会话已过期，请重新进入。')));
          }, { authToken: ticket.token, authQueryName: 'wiki_token' });
          if (!started) finish(() => reject(new Error('无法建立知识库实时连接，请稍后重试。')));
        });
        const result = await requestSkillHubDeviceTool({
          ownerUserId: handoff.owner_user_id,
          deviceId: handoff.target_device_id,
          toolName: 'knowledge.document.list',
          payload: { bot_uid: agentUid, offset: 0, limit: 30 },
        });
        if (active) setState({ status: 'ready', data: { ...handoff, ...result }, error: '' });
      })
      .catch((error) => {
        if (!active) return;
        const message = error?.status === 401 || error?.status === 403
          ? '知识库入口无效或已过期，请重新从 AI 助手管理进入。'
          : (error?.message || '暂时无法读取知识库');
        setState({ status: 'error', data: null, error: message });
      });
    return () => { active = false; disconnectWS(); };
  }, [agentUid]);

  async function loadMore() {
    if (loadingMore || !state.data?.next_offset) return;
    setLoadingMore(true);
    try {
      const page = await requestSkillHubDeviceTool({ ownerUserId: state.data.owner_user_id, deviceId: state.data.target_device_id, toolName: 'knowledge.document.list', payload: { bot_uid: agentUid, offset: state.data.next_offset, limit: 30 } });
      setState((current) => ({ ...current, data: { ...current.data, ...page, items: [...(current.data.items || []), ...(page.items || [])] } }));
    } finally { setLoadingMore(false); }
  }

  async function openDocument(item) {
    setSelected({ status: 'loading', item });
    try {
      const data = await requestSkillHubDeviceTool({ ownerUserId: state.data.owner_user_id, deviceId: state.data.target_device_id, toolName: 'knowledge.document.read', payload: { bot_uid: agentUid, id: item.id, revision: item.revision } });
      setSelected({ status: 'ready', item, data });
    } catch (error) { setSelected({ status: 'error', item, error: error?.message || '无法读取文档' }); }
  }

  const data = state.data || {};
  return (
    <main className="cc-knowledge-wiki" aria-busy={state.status === 'loading'}>
      <header className="cc-knowledge-wiki-header">
        <div className="cc-knowledge-wiki-title">
          <BookOpen size={22} aria-hidden="true" />
          <div><h1>{data.agent_name || '知识库 Wiki'}</h1><span>只读查看 · Agent {agentUid || '未知'}</span></div>
        </div>
      </header>
      {state.status === 'loading' && <div className="cc-knowledge-wiki-state"><LoaderCircle className="spin" size={22} aria-hidden="true" /> 正在读取知识库目录…</div>}
      {state.status === 'error' && <div className="cc-knowledge-wiki-state error"><CloudOff size={22} aria-hidden="true" /><div><strong>当前无法读取最新知识</strong><p>{state.error}</p><small>不会用空列表代替离线或无权限状态。</small></div></div>}
      {state.status === 'ready' && (
        <section className="cc-knowledge-wiki-content" aria-label="知识库概览">
          <div className="cc-knowledge-wiki-summary"><strong>{Number(data.total || 0)}</strong><span>篇知识</span><span>{data.review_pending || 0} 篇待复查</span></div>
          <div className="cc-knowledge-wiki-list">
            {(data.items || []).map((item) => <button type="button" key={`${item.id}:${item.revision}`} className="cc-knowledge-wiki-card" onClick={() => openDocument(item)}><div><h2>{item.title}</h2><p>{item.summary || '暂无摘要'}</p><small>{item.review_status === 'reviewed' ? '已复查' : '待复查'} · {item.updatedAt ? new Date(item.updatedAt).toLocaleDateString() : '时间未知'}</small></div><span>{item.category || '未分类'}</span></button>)}
            {!data.items?.length && <p className="cc-knowledge-wiki-empty">当前没有可显示的知识，或知识索引尚未完成。</p>}
          </div>
          {data.next_offset && <button type="button" className="oc-btn oc-btn-default cc-knowledge-wiki-more" onClick={loadMore} disabled={loadingMore}>{loadingMore ? '正在加载…' : '加载更多'}</button>}
        </section>
      )}
      {selected && <div className="cc-knowledge-wiki-dialog" role="dialog" aria-modal="true"><div className="cc-knowledge-wiki-dialog-inner"><button type="button" className="cc-knowledge-wiki-close" onClick={() => setSelected(null)} aria-label="关闭"><X size={18} /></button><h2>{selected.item.title}</h2>{selected.status === 'loading' && <p>正在读取正文…</p>}{selected.status === 'error' && <p className="error">{selected.error}</p>}{selected.status === 'ready' && <><p className="cc-knowledge-wiki-review">{selected.data.review_status === 'reviewed' ? '已复查' : '待复查'} · revision {selected.data.revision}</p><pre>{selected.data.body || selected.data.content || '暂无正文'}</pre><h3>来源</h3><ul className="cc-knowledge-wiki-sources">{(selected.data.sources || []).map((source, index) => <li key={`${index}:${source}`}>{source}</li>)}</ul></>}</div></div>}
    </main>
  );
}
