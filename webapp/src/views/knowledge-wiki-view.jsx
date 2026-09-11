import React, { useEffect, useMemo, useState } from 'react';
import { ArrowLeft, BookOpen, CloudOff, LoaderCircle } from 'lucide-react';
import { api, requestSkillHubDeviceTool } from '../api';
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
        const result = await requestSkillHubDeviceTool({
          ownerUserId: handoff.owner_user_id,
          deviceId: handoff.target_device_id,
          toolName: 'knowledge.document.list',
          payload: { bot_uid: agentUid, offset: 0, limit: 30 },
        });
        if (active) setState({ status: 'ready', data: { ...handoff, ...result }, error: '' });
      })
      .catch((error) => {
        if (active) setState({ status: 'error', data: null, error: error?.message || '暂时无法读取知识库' });
      });
    return () => { active = false; };
  }, [agentUid]);

  const data = state.data || {};
  return (
    <main className="cc-knowledge-wiki" aria-busy={state.status === 'loading'}>
      <header className="cc-knowledge-wiki-header">
        <button type="button" className="oc-btn oc-btn-default" onClick={() => window.history.back()}>
          <ArrowLeft size={15} aria-hidden="true" /> 返回 AI 助手管理
        </button>
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
            {(data.items || []).map((item) => <article key={`${item.id}:${item.revision}`} className="cc-knowledge-wiki-card"><div><h2>{item.title}</h2><p>{item.summary || '暂无摘要'}</p></div><span>{item.category || '未分类'}</span></article>)}
            {!data.items?.length && <p className="cc-knowledge-wiki-empty">当前没有可显示的知识，或知识索引尚未完成。</p>}
          </div>
        </section>
      )}
    </main>
  );
}
