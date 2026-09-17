import React, { useEffect, useMemo, useRef, useState } from 'react';
import { ArrowDownUp, BookOpen, CloudOff, LoaderCircle, RotateCcw, Search, X } from 'lucide-react';
import { api, connectWS, disconnectWS, requestSkillHubDeviceTool } from '../api';
import './knowledge-wiki-view.css';

// Mirror the entry-gate normalization from utils/auth-routes so the router and
// the view agree on trailing slashes. A malformed escape must not throw during
// render: the view falls back to its existing invalid-entry state instead.
function parseAgentID(pathname) {
  const normalized = String(pathname || '').replace(/\/+$/, '');
  const match = normalized.match(/^\/wiki\/agents\/([^/]+)$/);
  if (!match) return '';
  try {
    return decodeURIComponent(match[1]);
  } catch {
    return '';
  }
}

export function knowledgeWikiAgentID(pathname) {
  return parseAgentID(pathname);
}

const KNOWLEDGE_MAX_ITEMS = 300;
const SEARCH_DEBOUNCE_MS = 300;

// Order the documents the runtime returned by updatedAt (the runtime orders
// by id, which reads as random). Pure helper so the rule is testable.
export function knowledgeWikiSortedItems(items, { sortDesc = true } = {}) {
  const timestamp = (item) => {
    const value = Date.parse(String(item?.updatedAt || ''));
    return Number.isFinite(value) ? value : 0;
  };
  return (items || [])
    .slice()
    .sort((a, b) => (sortDesc ? timestamp(b) - timestamp(a) : timestamp(a) - timestamp(b))
      || String(a?.id || '').localeCompare(String(b?.id || '')));
}

export default function KnowledgeWikiView({ location = window.location } = {}) {
  const agentUid = useMemo(() => parseAgentID(location.pathname), [location.pathname]);
  const [state, setState] = useState({ status: 'loading', data: null, error: '' });
  const [selected, setSelected] = useState(null);
  const [query, setQuery] = useState('');
  const [sortDesc, setSortDesc] = useState(true);
  const [searching, setSearching] = useState(false);
  const [loadingMore, setLoadingMore] = useState(false);
  const requestIdRef = useRef(0);
  const baseRef = useRef(null);

  async function fetchAllDocuments(base, searchQuery) {
    const requestKey = requestIdRef.current + 1;
    requestIdRef.current = requestKey;
    const items = [];
    let offset = 0;
    let total = 0;
    let reviewPending = 0;
    let nextOffset = null;
    for (let page = 0; page < 10; page += 1) {
      let result = null;
      try {
        result = await requestSkillHubDeviceTool({
          ownerUserId: base.owner_user_id,
          deviceId: base.target_device_id,
          toolName: 'knowledge.document.list',
          payload: { bot_uid: agentUid, offset, ...(searchQuery ? { query: searchQuery } : {}) },
        });
      } catch (error) {
        if (requestIdRef.current !== requestKey) return null;
        throw error;
      }
      if (requestIdRef.current !== requestKey) return null;
      const pageItems = Array.isArray(result?.items) ? result.items : [];
      items.push(...pageItems);
      total = Number(result?.total ?? items.length);
      reviewPending = Number(result?.review_pending ?? reviewPending);
      const next = Number(result?.nextOffset ?? result?.next_offset ?? NaN);
      nextOffset = Number.isFinite(next) ? next : null;
      if (nextOffset === null || pageItems.length === 0 || items.length >= KNOWLEDGE_MAX_ITEMS) break;
      offset = nextOffset;
    }
    return { items, total, reviewPending, nextOffset };
  }

  async function runSearch(nextQuery) {
    const base = baseRef.current;
    if (!base) return;
    setSearching(true);
    setState((current) => ({ ...current, searchError: '' }));
    try {
      const result = await fetchAllDocuments(base, nextQuery);
      if (!result) return;
      setState((current) => ({
        status: 'ready',
        data: {
          ...current.data,
          items: result.items,
          total: result.total,
          review_pending: result.reviewPending,
          next_offset: result.nextOffset,
          appliedQuery: nextQuery,
        },
        error: '',
        searchError: '',
      }));
    } catch (error) {
      // A failed search keeps the documents already on screen: surface the
      // error inline instead of replacing the whole page with an error state.
      const message = error?.code === 'WIKI_BUSY'
        ? '知识库正在读取中，请稍后重试。'
        : (error?.message || '搜索失败，请稍后重试。');
      setState((current) => ({ ...current, searchError: message }));
    } finally {
      setSearching(false);
    }
  }

  useEffect(() => {
    let active = true;
    if (!agentUid) {
      setState({ status: 'error', data: null, error: '知识库入口无效' });
      return () => { active = false; };
    }
    setState({ status: 'loading', data: null, error: '' });
    baseRef.current = null;
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
        if (!active) return;
        baseRef.current = handoff;
        const result = await fetchAllDocuments(handoff, '');
        if (!active || !result) return;
        setState({
          status: 'ready',
          data: {
            ...handoff,
            items: result.items,
            total: result.total,
            review_pending: result.reviewPending,
            next_offset: result.nextOffset,
            appliedQuery: '',
          },
          error: '',
        });
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

  useEffect(() => {
    const applied = state.data?.appliedQuery ?? '';
    if (state.status !== 'ready' || applied === query) return undefined;
    const timer = setTimeout(() => { void runSearch(query); }, SEARCH_DEBOUNCE_MS);
    return () => clearTimeout(timer);
  }, [query, state.status, state.data?.appliedQuery]);

  async function loadMore() {
    const data = state.data;
    const next = Number(data?.next_offset ?? NaN);
    if (loadingMore || !Number.isFinite(next) || !baseRef.current) return;
    const requestKey = requestIdRef.current;
    setLoadingMore(true);
    try {
      const page = await requestSkillHubDeviceTool({
        ownerUserId: baseRef.current.owner_user_id,
        deviceId: baseRef.current.target_device_id,
        toolName: 'knowledge.document.list',
        payload: { bot_uid: agentUid, offset: next, ...(data.appliedQuery ? { query: data.appliedQuery } : {}) },
      });
      if (requestIdRef.current !== requestKey) return;
      const pageItems = Array.isArray(page?.items) ? page.items : [];
      const nextAfter = Number(page?.nextOffset ?? page?.next_offset ?? NaN);
      setState((current) => ({
        ...current,
        data: {
          ...current.data,
          items: [...(current.data.items || []), ...pageItems],
          next_offset: Number.isFinite(nextAfter) ? nextAfter : null,
        },
      }));
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
  const filtering = Boolean(query.trim());
  const sortedItems = useMemo(
    () => knowledgeWikiSortedItems(data.items, { sortDesc }),
    [data.items, sortDesc],
  );
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
          <div className="cc-knowledge-wiki-summary">
            {filtering
              ? (<><strong>{sortedItems.length}</strong><span>篇匹配</span><span>共 {Number(data.total || 0)} 篇</span></>)
              : (<><strong>{Number(data.total || 0)}</strong><span>篇知识</span><span>{data.review_pending || 0} 篇待复查</span></>)}
          </div>
          <div className="cc-knowledge-wiki-toolbar">
            <div className="cc-knowledge-wiki-search">
              <Search size={15} aria-hidden="true" />
              <input
                type="search"
                value={query}
                onChange={(event) => setQuery(event.target.value)}
                onKeyDown={(event) => {
                  if (event.key === 'Enter' && !event.nativeEvent?.isComposing) { event.preventDefault(); void runSearch(query); }
                }}
                placeholder="搜索标题、摘要、分类"
                aria-label="搜索知识"
                maxLength={200}
              />
              {searching && <LoaderCircle className="spin" size={14} aria-hidden="true" />}
            </div>
            <button
              type="button"
              className="cc-knowledge-wiki-sort"
              onClick={() => setSortDesc((value) => !value)}
              aria-label={sortDesc ? '当前按时间最新优先，点击切换为最早优先' : '当前按时间最早优先，点击切换为最新优先'}
            >
              <ArrowDownUp size={14} aria-hidden="true" />
              {sortDesc ? '最新优先' : '最早优先'}
            </button>
            {(!sortDesc || Boolean(query.trim())) && (
              <button
                type="button"
                className="cc-knowledge-wiki-reset"
                onClick={() => {
                  const hadQuery = Boolean(query.trim());
                  setQuery('');
                  setSortDesc(true);
                  if (hadQuery) void runSearch('');
                }}
                aria-label="清除搜索并恢复默认排序"
              >
                <RotateCcw size={14} aria-hidden="true" />
                恢复默认
              </button>
            )}
          </div>
          {state.searchError && (
            <p className="cc-knowledge-wiki-search-error" role="alert">
              {state.searchError}
              <button type="button" onClick={() => { void runSearch(query); }}>重试</button>
            </p>
          )}
          <div className="cc-knowledge-wiki-list">
            {sortedItems.map((item) => <button type="button" key={`${item.id}:${item.revision}`} className="cc-knowledge-wiki-card" onClick={() => openDocument(item)}><div><h2>{item.title}</h2><p>{item.summary || '暂无摘要'}</p><small>{item.review_status === 'reviewed' ? '已复查' : '待复查'} · {item.updatedAt ? new Date(item.updatedAt).toLocaleDateString() : '时间未知'}</small></div><span>{item.category || '未分类'}</span></button>)}
            {!sortedItems.length && <p className="cc-knowledge-wiki-empty">{query.trim() ? '没有找到匹配的知识，换个关键词试试。' : '当前没有可显示的知识，或知识索引尚未完成。'}</p>}
          </div>
          {data.next_offset !== null && data.next_offset !== undefined && Number.isFinite(Number(data.next_offset)) && <button type="button" className="oc-btn oc-btn-default cc-knowledge-wiki-more" onClick={loadMore} disabled={loadingMore}>{loadingMore ? '正在加载…' : '加载更多'}</button>}
        </section>
      )}
      {selected && <div className="cc-knowledge-wiki-dialog" role="dialog" aria-modal="true"><div className="cc-knowledge-wiki-dialog-inner"><button type="button" className="cc-knowledge-wiki-close" onClick={() => setSelected(null)} aria-label="关闭"><X size={18} /></button><h2>{selected.item.title}</h2>{selected.status === 'loading' && <p>正在读取正文…</p>}{selected.status === 'error' && <p className="error">{selected.error}</p>}{selected.status === 'ready' && <><p className="cc-knowledge-wiki-review">{selected.data.review_status === 'reviewed' ? '已复查' : '待复查'} · revision {selected.data.revision}</p><pre>{selected.data.body || selected.data.content || '暂无正文'}</pre><h3>来源</h3><ul className="cc-knowledge-wiki-sources">{(selected.data.sources || []).map((source, index) => <li key={`${index}:${source}`}>{source}</li>)}</ul></>}</div></div>}
    </main>
  );
}
