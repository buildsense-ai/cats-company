import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { ArrowLeft, BookOpen, ChevronRight, GitFork, List, X } from 'lucide-react';
import { api } from '../api';
import { renderSafeMarkdown } from './markdown-utils';
import WikiMemoryGraph from './wiki-memory-graph';
import './wiki-memory-panel.css';

const dateFormat = new Intl.DateTimeFormat('zh-CN', { dateStyle: 'medium' });
const catalogPageSize = 100;

function rank(item) {
  return `${item.created_at} ${item.id}`;
}

function newestFirst(items) {
  return [...items].sort((a, b) => rank(a) > rank(b) ? -1 : rank(a) < rank(b) ? 1 : 0);
}

function readVisit(key) {
  try {
    const visit = JSON.parse(localStorage.getItem(key));
    return {
      baselines: visit?.baselines && typeof visit.baselines === 'object' ? visit.baselines : {},
      seen: new Set(Array.isArray(visit?.seen) ? visit.seen : []),
    };
  } catch {
    return { baselines: {}, seen: new Set() };
  }
}

function markDisplayed(key, scope, items) {
  try {
    const visit = readVisit(key);
    visit.baselines[scope] ??= items.length ? rank(items[0]) : '0';
    for (const item of items) visit.seen.add(item.id);
    localStorage.setItem(key, JSON.stringify({ baselines: visit.baselines, seen: [...visit.seen] }));
  } catch { /* storage can be disabled */ }
}

function formatDate(value) {
  const date = new Date(value);
  return Number.isNaN(+date) ? '' : dateFormat.format(date);
}

export default function WikiMemoryPanel({ viewerUid, topicId, agents, onClose }) {
  const [selectedAgent, setSelectedAgent] = useState('');
  const agentUid = agents.some(agent => agent.uid === selectedAgent) ? selectedAgent : agents[0]?.uid;

  return (
    <aside className="cc-wiki-panel" aria-label="会话记忆" onKeyDown={event => {
      if (event.key === 'Escape' && !event.defaultPrevented) {
        event.stopPropagation();
        onClose();
      }
    }}>
      <header className="cc-wiki-header">
        <h2><BookOpen size={16} aria-hidden="true" />记忆</h2>
        {agents.length > 1 && (
          <select aria-label="选择助手" value={agentUid} onChange={event => setSelectedAgent(event.target.value)}>
            {agents.map(agent => <option key={agent.uid} value={agent.uid}>{agent.name}</option>)}
          </select>
        )}
        <button type="button" className="cc-wiki-icon" aria-label="关闭记忆" onClick={onClose}><X size={17} /></button>
      </header>
      {agentUid && <WikiMemoryContent key={`${viewerUid}:${agentUid}:${topicId}`} {...{ viewerUid, agentUid, topicId }} />}
    </aside>
  );
}

function WikiMemoryContent({ viewerUid, agentUid, topicId }) {
  const [isBotView, setBotView] = useState(false);
  const [openedBotView, setOpenedBotView] = useState(false);
  const [reading, setReading] = useState(null);
  const returnFocus = useRef(null);
  const visitKey = `catsco:wiki-seen:${viewerUid}:${agentUid}`;
  const [visit] = useState(() => readVisit(visitKey));
  const [refreshID, setRefreshID] = useState(0);
  useEffect(() => {
    let lastRefresh = 0;
    const refresh = () => {
      if (document.visibilityState !== 'visible' || Date.now() - lastRefresh < 250) return;
      lastRefresh = Date.now();
      returnFocus.current = null;
      setReading(null);
      setRefreshID(previous => previous + 1);
    };
    window.addEventListener('focus', refresh);
    document.addEventListener('visibilitychange', refresh);
    return () => {
      window.removeEventListener('focus', refresh);
      document.removeEventListener('visibilitychange', refresh);
    };
  }, []);

  useEffect(() => {
    if (reading || !returnFocus.current) return;
    const target = returnFocus.current;
    returnFocus.current = null;
    target.focus({ preventScroll: true });
  }, [reading]);

  function openWiki(item, conversationTitle) {
    returnFocus.current = document.activeElement;
    setReading({ item, conversationTitle });
  }

  return (
    <>
      <div className="cc-wiki-workspace" hidden={Boolean(reading)}>
        <nav className="cc-wiki-navigation" aria-label="记忆范围">
          <button type="button" aria-current={!isBotView ? 'page' : undefined} onClick={() => setBotView(false)}>当前会话</button>
          <button type="button" aria-current={isBotView ? 'page' : undefined} onClick={() => { setOpenedBotView(true); setBotView(true); }}>助手记忆</button>
        </nav>
        <WikiCatalog key={`topic:${refreshID}`} hidden={isBotView} topicId={topicId} paused={Boolean(reading)} onOpen={openWiki}
          {...{ agentUid, visit, visitKey }} />
        {openedBotView && <WikiCatalog key={`bot:${refreshID}`} hidden={!isBotView} paused={Boolean(reading)} onOpen={openWiki}
          {...{ agentUid, visit, visitKey }} />}
      </div>
      {reading && <WikiReader key={reading.item.id} {...reading} agentUid={agentUid} onBack={() => setReading(null)} />}
    </>
  );
}

function WikiCatalog({ hidden, paused, agentUid, topicId, visit, visitKey, onOpen }) {
  const [items, setItems] = useState([]);
  const [conversations, setConversations] = useState({});
  const [nextCursor, setNextCursor] = useState(null);
  const [visibleLimit, setVisibleLimit] = useState(catalogPageSize);
  const [loaded, setLoaded] = useState(false);
  const [busy, setBusy] = useState(true);
  const [error, setError] = useState(false);
  const [view, setView] = useState(topicId ? 'list' : 'graph');
  const active = useRef(null);
  const started = useRef(false);
  const catalog = useRef(null);
  const endOfList = useRef(null);
  const visitScope = topicId || 'all';
  const baseline = visit.baselines[visitScope];
  const isNew = item => typeof baseline === 'string' && rank(item) > baseline && !visit.seen.has(item.id);

  const load = useCallback(async (cursor) => {
    active.current?.abort();
    const controller = new AbortController();
    active.current = controller;
    if (!cursor) {
      setLoaded(false);
      setItems([]);
      setConversations({});
      setNextCursor(null);
    }
    setBusy(true);
    setError(false);
    try {
      const page = await api.getWikiMemoryCatalog(agentUid, { topicId, cursor, signal: controller.signal });
      if (controller.signal.aborted) return;
      const fresh = Array.isArray(page.items) ? page.items : [];
      setItems(previous => {
        const byId = new Map(previous.map(item => [item.id, item]));
        for (const item of fresh) byId.set(item.id, item);
        return newestFirst(byId.values());
      });
      setConversations(previous => ({ ...previous, ...Object.fromEntries((page.conversations || []).map(conversation => [conversation.id, conversation.title])) }));
      setNextCursor(page.next_cursor && page.next_cursor !== cursor ? page.next_cursor : null);
      setLoaded(true);
    } catch {
      if (!controller.signal.aborted) setError(true);
    } finally {
      if (!controller.signal.aborted) setBusy(false);
    }
  }, [agentUid, topicId]);

  useEffect(() => {
    if (!hidden && !started.current) {
      started.current = true;
      load(null);
    }
  }, [hidden, load]);
  useEffect(() => () => {
    active.current?.abort();
    started.current = false;
  }, []);

  const paginatedList = Boolean(topicId);
  const displayedItems = useMemo(() => paginatedList ? items.slice(0, visibleLimit) : items,
    [items, paginatedList, visibleLimit]);
  const hasMore = items.length > visibleLimit || Boolean(nextCursor);

  useEffect(() => {
    if (hidden || paused || busy || error || !hasMore) return undefined;
    if (!paginatedList) {
      if (nextCursor) load(nextCursor);
      return undefined;
    }
    const sentinel = endOfList.current;
    if (!sentinel) return undefined;
    const observer = new IntersectionObserver(entries => {
      if (!entries.some(entry => entry.isIntersecting)) return;
      observer.disconnect();
      setVisibleLimit(limit => limit + catalogPageSize);
      if (items.length <= visibleLimit && nextCursor) load(nextCursor);
    }, { root: catalog.current });
    observer.observe(sentinel);
    return () => observer.disconnect();
  }, [hidden, paused, busy, error, hasMore, paginatedList, nextCursor, items.length, visibleLimit, load]);

  useEffect(() => {
    if (!hidden && loaded) markDisplayed(visitKey, visitScope, displayedItems);
  }, [hidden, visitKey, visitScope, loaded, displayedItems]);

  function getConversationTitle(item) {
    return conversations[item.conversation_ids?.[0]] || (topicId ? '' : '其他来源');
  }

  const groups = useMemo(() => {
    const grouped = new Map();
    for (const item of items) {
      const source = item.conversation_ids?.[0] || 'other';
      const group = grouped.get(source) || [];
      group.push(item);
      grouped.set(source, group);
    }
    return grouped;
  }, [items]);

  function renderList(entries) {
    return <ul className="cc-wiki-list">
      {entries.map(item => <li key={item.id}>
        <button type="button" className="cc-wiki-row" onClick={() => onOpen(item, getConversationTitle(item))}>
          <div className="cc-wiki-row-text">
            <span>{item.title}</span>
            <div className="cc-wiki-row-meta">
              {item.created_at && <time dateTime={item.created_at}>{formatDate(item.created_at)}</time>}
              {isNew(item) && <small className="cc-wiki-new">新增</small>}
            </div>
          </div>
          <ChevronRight size={15} aria-hidden="true" />
        </button>
      </li>)}
    </ul>;
  }

  return (
    <section className="cc-wiki-catalog" hidden={hidden}>
      {!topicId && <div className="cc-wiki-view-bar">
        <span className="cc-wiki-catalog-count">
          {loaded && (nextCursor ? `已载入 ${items.length} 篇` : `${items.length} 篇 · ${groups.size} 个分组`)}
        </span>
        <div role="group" aria-label="记忆视图">
          <button type="button" aria-pressed={view === 'graph'} onClick={() => setView('graph')}><GitFork size={15} aria-hidden="true" />图谱</button>
          <button type="button" aria-pressed={view === 'list'} onClick={() => setView('list')}><List size={15} aria-hidden="true" />列表</button>
        </div>
      </div>}
      {view === 'graph' && items.length > 0 &&
        <WikiMemoryGraph {...{ groups, conversations, isNew }} onOpen={item => onOpen(item, getConversationTitle(item))} />}
      <div className={view === 'list' ? 'cc-wiki-list-scroll' : 'cc-wiki-map-status'} ref={catalog}>
        {view === 'list' && displayedItems.length > 0 && (topicId ? renderList(displayedItems) :
          <div className="cc-wiki-groups">
            {[...groups].map(([id, entries]) => <WikiListGroup key={id}
              title={conversations[id] || '其他来源'} count={entries.length}>
              {() => renderList(entries)}
            </WikiListGroup>)}
          </div>)}
        {busy && <p className="cc-wiki-feedback" role="status">{items.length ? '正在补齐记忆目录…' : '正在读取记忆…'}</p>}
        {error && <div className="cc-wiki-feedback" role="status">读取失败。<button type="button" onClick={() => load(nextCursor)}>重试</button></div>}
        {!busy && !error && items.length === 0 && <p className="cc-wiki-feedback">还没有可查看的记忆</p>}
        {paginatedList && hasMore && <div className="cc-wiki-list-end" ref={endOfList} aria-hidden="true" />}
      </div>
    </section>
  );
}

function WikiListGroup({ title, count, children }) {
  const [open, setOpen] = useState(false);
  return <details onToggle={event => setOpen(event.currentTarget.open)}>
    <summary><ChevronRight size={16} aria-hidden="true" /><span>{title}</span><small>{count} 篇</small></summary>
    {open && children()}
  </details>;
}

export function WikiReader({ item, agentUid, conversationTitle, onBack, backLabel = '返回目录' }) {
  const backButton = useRef(null);
  useEffect(() => backButton.current?.focus({ preventScroll: true }), []);
  return (
    <section className="cc-wiki-reader" aria-label="Wiki 正文" onKeyDown={event => {
      if (event.key !== 'Escape') return;
      event.preventDefault();
      event.stopPropagation();
      onBack();
    }}>
      <div className="cc-wiki-reader-toolbar">
        <button ref={backButton} type="button" onClick={onBack}><ArrowLeft size={16} aria-hidden="true" /> {backLabel}</button>
      </div>
      <div className="cc-wiki-reader-scroll">
        <div className="cc-wiki-reader-page">
          <header>
            <div className="cc-wiki-reader-meta">
              {conversationTitle && <span>{conversationTitle}</span>}
              <time dateTime={item.created_at}>{formatDate(item.created_at)}</time>
            </div>
            <h3>{item.title}</h3>
          </header>
          <WikiBody {...{ item, agentUid }} />
        </div>
      </div>
    </section>
  );
}

function WikiBody({ item, agentUid }) {
  const [document, setDocument] = useState(null);
  const [attempt, setAttempt] = useState(0);
  const [error, setError] = useState(false);

  useEffect(() => {
    if (document) return undefined;
    const controller = new AbortController();
    api.getWikiMemoryDocument(agentUid, item.id, { signal: controller.signal }).then(body => {
      if (controller.signal.aborted) return;
      setDocument(body);
    }).catch(() => {
      if (!controller.signal.aborted) {
        setError(true);
      }
    });
    return () => controller.abort();
  }, [agentUid, item.id, attempt, document]);

  if (error) return <div className="cc-wiki-feedback" role="status">正文不可用。<button type="button" onClick={() => { setError(false); setAttempt(previous => previous + 1); }}>重试</button></div>;
  if (!document) return <p className="cc-wiki-feedback" role="status">正在读取正文…</p>;
  const markdown = document.markdown.replace(/^# +([^\r\n]+)\r?\n/, (heading, title) =>
    title.trim() === item.title ? '' : heading);
  return <article className="cc-wiki-markdown" dangerouslySetInnerHTML={{ __html: renderSafeMarkdown(markdown) }} />;
}
