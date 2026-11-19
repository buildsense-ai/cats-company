import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { ArrowLeft, ChevronDown, ChevronRight, GitFork, List, X } from 'lucide-react';
import { api } from '../api';
import { renderSafeMarkdown } from './markdown-utils';
import './wiki-memory-panel.css';

const dateFormat = new Intl.DateTimeFormat('zh-CN', { dateStyle: 'medium' });

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
      if (event.key === 'Escape') {
        event.stopPropagation();
        onClose();
      }
    }}>
      <header className="cc-wiki-header">
        <h2>记忆</h2>
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
  const documents = useRef(new Map());
  const visitKey = `catsco:wiki-seen:${viewerUid}:${agentUid}`;
  const [visit] = useState(() => readVisit(visitKey));
  const [refreshID, setRefreshID] = useState(0);
  useEffect(() => {
    let lastRefresh = 0;
    const refresh = () => {
      if (document.visibilityState !== 'visible' || Date.now() - lastRefresh < 250) return;
      lastRefresh = Date.now();
      documents.current.clear();
      setRefreshID(previous => previous + 1);
    };
    window.addEventListener('focus', refresh);
    document.addEventListener('visibilitychange', refresh);
    return () => {
      window.removeEventListener('focus', refresh);
      document.removeEventListener('visibilitychange', refresh);
    };
  }, []);
  return (
    <>
      <nav className="cc-wiki-navigation" aria-label="记忆范围">
        {isBotView ? (
          <button type="button" onClick={() => setBotView(false)}><ArrowLeft size={15} />当前会话</button>
        ) : (
          <>
            <span aria-current="page">当前会话</span>
            <button type="button" onClick={() => { setOpenedBotView(true); setBotView(true); }}>助手记忆<ChevronRight size={14} /></button>
          </>
        )}
      </nav>
      <WikiCatalog key={`topic:${refreshID}`} hidden={isBotView} topicId={topicId} documents={documents.current} {...{ agentUid, visit, visitKey }} />
      {openedBotView && <WikiCatalog key={`bot:${refreshID}`} hidden={!isBotView} documents={documents.current} {...{ agentUid, visit, visitKey }} />}
    </>
  );
}

function WikiCatalog({ hidden, agentUid, topicId, documents, visit, visitKey }) {
  const [items, setItems] = useState([]);
  const [conversations, setConversations] = useState({});
  const [nextCursor, setNextCursor] = useState(null);
  const [loaded, setLoaded] = useState(false);
  const [busy, setBusy] = useState(true);
  const [error, setError] = useState(false);
  const [view, setView] = useState(topicId ? 'list' : 'graph');
  const [selected, setSelected] = useState(null);
  const [expandedGroups, setExpandedGroups] = useState(new Set());
  const active = useRef(null);
  const started = useRef(false);
  const visitScope = topicId || 'all';
  const baseline = visit.baselines[visitScope];
  const isNew = item => typeof baseline === 'string' && rank(item) > baseline && !visit.seen.has(item.id);

  const load = useCallback((cursor) => {
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
    api.getWikiMemoryCatalog(agentUid, { topicId, cursor, signal: controller.signal }).then(page => {
      if (controller.signal.aborted) return;
      const fresh = Array.isArray(page.items) ? page.items : [];
      setItems(previous => {
        const byId = new Map((cursor ? previous : []).map(item => [item.id, item]));
        for (const item of fresh) byId.set(item.id, item);
        return newestFirst(byId.values());
      });
      setConversations(previous => ({ ...previous, ...Object.fromEntries((page.conversations || []).map(conversation => [conversation.id, conversation.title])) }));
      setNextCursor(page.next_cursor || null);
      setLoaded(true);
    }).catch(() => {
      if (!controller.signal.aborted) setError(true);
    }).finally(() => {
      if (!controller.signal.aborted) setBusy(false);
    });
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

  useEffect(() => {
    if (!hidden && loaded) markDisplayed(visitKey, visitScope, items);
  }, [hidden, visitKey, visitScope, loaded, items]);

  function toggle(id) {
    setSelected(current => current === id ? null : id);
  }

  const read = item => <WikiBody key={`${item.id}:${item.revision}`} {...{ item, agentUid, documents }} />;
  const renderList = entries => <ul className="cc-wiki-list">
    {entries.map(item => (
      <li key={item.id}>
        <button type="button" className="cc-wiki-row" aria-expanded={selected === item.id} onClick={() => toggle(item.id)}>
          {selected === item.id ? <ChevronDown size={14} /> : <ChevronRight size={14} />}
          <span>{item.title}</span>
          {isNew(item) && <small className="cc-wiki-new">新增</small>}
          <time dateTime={item.created_at}>{formatDate(item.created_at)}</time>
        </button>
        {!hidden && selected === item.id && view === 'list' && read(item)}
      </li>
    ))}
  </ul>;

  const groups = useMemo(() => {
    const grouped = new Map();
    if (topicId) return grouped;
    for (const item of items) {
      const source = item.conversation_ids?.[0] || 'other';
      const group = grouped.get(source) || [];
      group.push(item);
      grouped.set(source, group);
    }
    return grouped;
  }, [items, topicId]);
  const selection = items.find(item => item.id === selected);
  const selectListView = () => {
    if (selection) setExpandedGroups(previous => new Set([...previous, selection.conversation_ids?.[0] || 'other']));
    setView('list');
  };
  const onGroupToggle = (id, open) => setExpandedGroups(previous => {
    if (previous.has(id) === open) return previous;
    const next = new Set(previous);
    if (open) next.add(id);
    else next.delete(id);
    return next;
  });

  return (
    <section className="cc-wiki-catalog" hidden={hidden}>
      {!topicId && <div className="cc-wiki-view-bar">
        <strong>我可见的记忆</strong>
        <div role="group" aria-label="记忆视图">
          <button type="button" className="cc-wiki-icon" aria-label="图谱" title="图谱" aria-pressed={view === 'graph'} onClick={() => setView('graph')}><GitFork size={16} /></button>
          <button type="button" className="cc-wiki-icon" aria-label="列表" title="列表" aria-pressed={view === 'list'} onClick={selectListView}><List size={17} /></button>
        </div>
      </div>}
      {items.length > 0 && (topicId ? renderList(items) : view === 'list' ? (
        <div className="cc-wiki-groups">
          {Array.from(groups, ([id, entries]) => (
            <details key={id} open={expandedGroups.has(id)} onToggle={event => onGroupToggle(id, event.currentTarget.open)}>
              <summary><span>{conversations[id] || '其他来源'}</span><span>{entries.length} 篇</span></summary>
              {renderList(entries)}
            </details>
          ))}
        </div>
      ) : (
        <>
          <WikiGraph {...{ groups, conversations, selected, toggle, isNew }} />
          {selection && !hidden && <section className="cc-wiki-graph-detail">
            <header><strong>{selection.title}</strong><button type="button" className="cc-wiki-icon" aria-label="收起正文" onClick={() => setSelected(null)}><X size={16} /></button></header>
            {read(selection)}
          </section>}
        </>
      ))}
      {busy && <p className="cc-wiki-feedback" role="status">正在读取记忆…</p>}
      {error && <div className="cc-wiki-feedback" role="status">读取失败。<button type="button" onClick={() => load(nextCursor)}>重试</button></div>}
      {!busy && !error && items.length === 0 && <p className="cc-wiki-feedback">还没有可查看的记忆</p>}
      {!busy && !error && nextCursor && <button type="button" className="cc-wiki-more" onClick={() => load(nextCursor)}>加载更多记忆</button>}
    </section>
  );
}

function WikiGraph({ groups, conversations, selected, toggle, isNew }) {
  return (
    <div className="cc-wiki-graph" aria-label="按会话聚类的记忆图谱">
      {Array.from(groups, ([id, entries]) => {
        const radius = Math.max(38, 13 * Math.sqrt(entries.length));
        const size = radius * 2 + 28;
        return (
          <div className="cc-wiki-cluster" key={id} aria-label={conversations[id] || '其他来源'}>
            <div className="cc-wiki-nodes" style={{ width: size, height: size }}>
              {entries.map((item, index) => {
                const angle = index * Math.PI * (3 - Math.sqrt(5));
                const distance = 13 * Math.sqrt(index);
                return <button key={item.id} type="button" className="cc-wiki-node" aria-label={item.title} title={item.title}
                  aria-pressed={item.id === selected} data-new={isNew(item) || undefined} onClick={() => toggle(item.id)}
                  style={{ left: size / 2 + distance * Math.cos(angle), top: size / 2 + distance * Math.sin(angle) }} />;
              })}
            </div>
            <span className="cc-wiki-cluster-label" title={conversations[id] || '其他来源'}>{conversations[id] || '其他来源'}</span>
          </div>
        );
      })}
    </div>
  );
}

function WikiBody({ item, agentUid, documents }) {
  const cached = documents.get(item.id);
  const [document, setDocument] = useState(cached?.revision === item.revision ? cached : null);
  const [attempt, setAttempt] = useState(0);
  const [error, setError] = useState(false);

  useEffect(() => {
    if (document) return undefined;
    const controller = new AbortController();
    api.getWikiMemoryDocument(agentUid, item.id, { signal: controller.signal }).then(body => {
      if (controller.signal.aborted) return;
      documents.set(item.id, body);
      setDocument(body);
    }).catch(() => {
      if (!controller.signal.aborted) {
        documents.delete(item.id);
        setError(true);
      }
    });
    return () => controller.abort();
  }, [agentUid, item.id, documents, attempt, document]);

  if (error) return <div className="cc-wiki-feedback" role="status">正文不可用。<button type="button" onClick={() => { setError(false); setAttempt(previous => previous + 1); }}>重试</button></div>;
  if (!document) return <p className="cc-wiki-feedback" role="status">正在读取正文…</p>;
  return <article className="cc-wiki-markdown" dangerouslySetInnerHTML={{ __html: renderSafeMarkdown(document.markdown) }} />;
}
