import React, { useCallback, useEffect, useId, useMemo, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import { Brain, ChevronRight, RotateCcw, X } from 'lucide-react';
import { api } from '../api';
import { renderSafeMarkdown } from './markdown-utils';
import { MEMORY_NODE_LABELS, MEMORY_RELATIONS, verifiedMemoryDetail } from './memory-branch';
import MemorySourceGraph from './memory-source-graph';
import './diary-sources.css';
import './memory-sources.css';

function MemoryReader({ agentUid, node, onClose }) {
  const [attempt, setAttempt] = useState(0), [state, setState] = useState({ status: 'loading' });
  const closeRef = useRef(null);
  const requestKey = `${agentUid}:${node.id}:${JSON.stringify(node.reader)}`;
  useEffect(() => { closeRef.current?.focus({ preventScroll: true }); }, []);
  useEffect(() => {
    const escape = event => { if (event.key === 'Escape') { event.preventDefault(); event.stopImmediatePropagation(); onClose(); } };
    document.addEventListener('keydown', escape, true); window.addEventListener('cc:auth-changed', onClose);
    return () => { document.removeEventListener('keydown', escape, true); window.removeEventListener('cc:auth-changed', onClose); };
  }, [onClose]);
  useEffect(() => {
    const controller = new AbortController();
    setState({ status: 'loading', key: requestKey });
    if (!agentUid) { setState({ status: 'error', key: requestKey, error: '无法确认来源助手，请重新打开这段对话。' }); return () => controller.abort(); }
    api.readMemorySource(agentUid, node.reader, { signal: controller.signal })
      .then(page => {
        if (controller.signal.aborted) return;
        const detail = verifiedMemoryDetail(page, node.reader);
        setState({ status: 'ready', key: requestKey, detail });
      }).catch(error => {
        if (controller.signal.aborted) return;
        setState({ status: 'error', key: requestKey, error: [403, 404].includes(error?.status) ? '无权查看或来源已不可用。' : error?.status === 429 ? '来源读取繁忙，请稍后重试。' : error?.status ? '暂时无法读取引用版本，请重试。' : error?.message || '读取失败，请重试。' });
      });
    return () => controller.abort();
  }, [requestKey, attempt]);
  const current = state.key === requestKey ? state : { status: 'loading' };
  const detail = current.detail;
  const prose = text => <div className="cc-diary-prose" dangerouslySetInnerHTML={{ __html: renderSafeMarkdown(text) }} />;
  return createPortal(<aside className="cc-diary-reader" role="dialog" aria-modal="false" aria-label="记忆来源详情" onClick={event => event.stopPropagation()}>
    <header className="cc-diary-reader-header"><span><Brain size={17} aria-hidden="true" />记忆来源</span><button ref={closeRef} type="button" className="cc-diary-icon-button" aria-label="关闭记忆详情" onClick={onClose}><X size={18} /></button></header>
    <div className="cc-diary-reader-scroll"><h2>{MEMORY_NODE_LABELS[node.kind] || '记忆节点'}</h2>
      <p className="cc-diary-reader-meta">{node.injected ? '已加入主 Agent 上下文' : 'Branch 已读取 · 尚未加入主 Agent 上下文'}</p>
      <p className="cc-diary-version">引用时来源 · 按当前权限核验</p>
      <section className="cc-diary-reader-body" aria-busy={current.status === 'loading'} aria-live="polite">
        {current.status === 'loading' && <div className="cc-diary-skeleton" role="status" aria-label="正在核验记忆来源"><span /><span /><span /></div>}
        {current.status === 'error' && <div className="cc-diary-read-error" role="alert"><p>{current.error}</p><button type="button" onClick={() => setAttempt(value => value + 1)}><RotateCcw size={14} />重试读取</button></div>}
        {current.status === 'ready' && <>
          {detail.goal && <p className="cc-diary-reader-note">{detail.goal}</p>}
          {detail.nodes.map((item, index) => <section className="cc-memory-detail-node" key={item.id || index}><h3>{MEMORY_NODE_LABELS[item.kind] || '记忆内容'}{item.association === 'member_scope_unlinked' ? ' · 来源范围，未绑定到此断言' : ''}</h3>{prose(item.text)}</section>)}
          {detail.conditions.length > 0 && <section className="cc-memory-detail-node"><h3>来源范围的适用条件</h3>{detail.conditions.map((text, i) => <React.Fragment key={i}>{prose(text)}</React.Fragment>)}</section>}
          {detail.omissions.length > 0 && <section className="cc-memory-detail-node"><h3>来源范围的已知缺口</h3>{detail.omissions.map((text, i) => <React.Fragment key={i}>{prose(text)}</React.Fragment>)}</section>}
          {detail.gaps && <section className="cc-memory-detail-node"><h3>尚未核验</h3>{prose(detail.gaps)}</section>}
          {detail.coverage !== 'complete' && <p className="cc-diary-reader-note">这是本次读取的局部记忆，不代表完整历史。</p>}
        </>}
      </section>
      <details className="cc-diary-reference"><summary>引用标识</summary><code>{node.ref}</code></details>
    </div>
  </aside>, document.body);
}

const SUMMARY_RECEIVED = '主 Agent 已收到记忆摘要';

export default function MemoryBranchSources({ events, active, agentUid }) {
  const [open, setOpen] = useState(false);
  const disclosureId = useId();
  const buttonRef = useRef(null);
  const hasSources = events.some(event => event.read_refs.length > 0);
  const running = active && events.some(event => event.status === 'running');
  const ready = active && events.some(event => event.status === 'ready');
  const injectedCount = new Set(events.flatMap(event => event.injected_refs)).size;

  const summaryReceived = events.some(event => event.observation_id && event.consumed_turn_id);
  if (!hasSources) {
    const status = summaryReceived ? `${SUMMARY_RECEIVED} · 暂无可打开的来源` : running ? '记忆探索中' : null;
    return status && <span className="cc-memory-status" role="status"><Brain size={14} aria-hidden="true" />{status}</span>;
  }

  return <div className="cc-memory-disclosure" onKeyDown={event => {
    if (!open || event.key !== 'Escape') return;
    event.preventDefault();
    event.stopPropagation();
    setOpen(false);
    buttonRef.current?.focus({ preventScroll: true });
  }}>
    <button ref={buttonRef} type="button" className="cc-memory-toggle" aria-expanded={open} aria-controls={open ? disclosureId : undefined}
      onClick={() => setOpen(value => !value)}>
      <Brain size={14} aria-hidden="true" />
      <span>{running && !injectedCount ? '记忆探索中' : '记忆来源'}</span>
      {injectedCount > 0 && <span className="cc-activity-count">{injectedCount} 份</span>}
      <ChevronRight size={14} className="cc-disclosure-chevron" aria-hidden="true" />
    </button>
    {open && <MemoryBranchPanel id={disclosureId} events={events} agentUid={agentUid} running={running} ready={ready} summaryReceived={summaryReceived} />}
  </div>;
}

function MemoryBranchPanel({ id, events, agentUid, running, ready, summaryReceived }) {
  const [selected, setSelected] = useState(null);
  const triggerRef = useRef(null);
  const graph = useMemo(() => {
    const read = new Set(events.flatMap(event => event.read_refs)), injected = new Set(events.flatMap(event => event.injected_refs)), cited = new Set(events.flatMap(event => event.answer_refs));
    const nodes = new Map(), edges = new Map();
    for (const event of events) {
      if (!event.injected_refs.length && !['running', 'ready', 'deferred', 'expired', 'failed', 'cancelled'].includes(event.status)) continue;
      for (const node of event.nodes) if (!injected.size || injected.has(node.ref)) nodes.set(node.id, { ...node, injected: injected.has(node.ref) });
      for (const edge of event.edges) edges.set(edge.id, edge);
    }
    const visible = [...nodes.values()].slice(0, 128), ids = new Set(visible.map(node => node.id));
    return { read, injected, cited, nodes: visible, edges: [...edges.values()].filter(edge => ids.has(edge.from) && ids.has(edge.to)), limited: visible.length < nodes.size || events.some(event => event.truncated) };
  }, [events]);
  const select = (node, trigger) => { if (!selected && trigger) triggerRef.current = trigger; setSelected(node); };
  const close = useCallback(() => { setSelected(null); if (triggerRef.current?.isConnected) triggerRef.current.focus({ preventScroll: true }); }, []);
  const localCount = [...graph.injected].filter(ref => /^local:[a-f0-9]{64}$/.test(ref)).length;
  const currentStatus = graph.injected.size
    ? `已加入主 Agent 上下文 · ${graph.injected.size} 份记忆`
    : summaryReceived ? SUMMARY_RECEIVED
    : events.some(event => event.status === 'unavailable') ? '来源暂不可核验'
    : ready ? '已读取，等待主 Agent 接收'
    : running ? '正在查找相关记忆'
    : '已读取 · 暂无注入记录';
  const selectedNode = graph.nodes.find(node => node.id === selected?.id);
  return <section id={id} className="cc-memory-branch" aria-label="记忆探索" onClick={event => event.stopPropagation()}>
    <div className="cc-memory-panel-status" role="status">{currentStatus}</div>
    {localCount > 0 && <p className="cc-memory-summary">摘要引用了 {localCount} 份本地会话记录</p>}
    {graph.read.size > 0 && <p className="cc-memory-summary">已读取 {graph.read.size} 份{graph.cited.size > 0 ? ` · 回答中明确引用 ${graph.cited.size} 份` : ''}</p>}
    {events.some(event => event.consumed_turn_id && event.consumed_turn_id !== event.origin_turn_id) && <p className="cc-diary-incomplete">来自上一轮的探索结果 · 本轮实际接收</p>}
    {graph.nodes.length > 0 && <MemorySourceGraph nodes={graph.nodes} edges={graph.edges} selectedId={selected?.id} onSelect={select} />}
    {graph.edges.length > 0 && <p className="cc-diary-closure">{[...new Set(graph.edges.map(edge => MEMORY_RELATIONS[edge.kind] || edge.kind))].join('、')} · 仅展示已读取节点间的真实关系</p>}
    {graph.limited && <p className="cc-diary-incomplete">图谱仅展示部分节点；点选来源可核验完整内容。</p>}
    {selectedNode && <MemoryReader agentUid={agentUid} node={selectedNode} onClose={close} />}
  </section>;
}
