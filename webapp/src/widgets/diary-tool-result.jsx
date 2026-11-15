import React, { useEffect, useId, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import { ArrowRight, BookOpen, ChevronRight, RotateCcw, Search, X } from 'lucide-react';
import { api } from '../api';
import { renderSafeMarkdown } from './markdown-utils';
import { DIARY_RELATIONS, DIARY_STATUS } from './diary-sources';
import DiarySourceGraph from './diary-source-graph';
import './diary-sources.css';

function SourceRow({ source, onSelect, selected = false }) {
  return (
    <button type="button" className="cc-diary-source" onClick={(event) => onSelect(source, event.currentTarget)} aria-current={selected ? 'true' : undefined}>
      <BookOpen size={15} aria-hidden="true" />
      <span className="cc-diary-source-label"><span>{source.title || '未命名笔记'}</span><small>{source.day} · {DIARY_STATUS[source.status]}{source.status === 'draft' ? ' · 尚未核验' : ''}</small></span>
      <ArrowRight size={14} aria-hidden="true" />
    </button>
  );
}

function DiaryReader({ agentUid, source, view, onSelect, onClose }) {
  const [attempt, setAttempt] = useState(0);
  const [state, setState] = useState({ status: 'loading' });
  const closeRef = useRef(null);
  const titleID = `diary-reader-${useId().replace(/:/g, '')}`;
  useEffect(() => { closeRef.current?.focus({ preventScroll: true }); }, []);
  useEffect(() => {
    const close = (event) => {
      if (event.key !== 'Escape') return;
      event.preventDefault(); event.stopImmediatePropagation(); onClose();
    };
    document.addEventListener('keydown', close, true);
    window.addEventListener('cc:auth-changed', onClose);
    return () => { document.removeEventListener('keydown', close, true); window.removeEventListener('cc:auth-changed', onClose); };
  }, [onClose]);
  useEffect(() => {
    const controller = new AbortController();
    setState({ status: 'loading', ref: source.ref });
    if (!agentUid) {
      setState({ status: 'error', ref: source.ref, error: '无法确认来源助手，请重新打开这段对话。' });
      return () => controller.abort();
    }
    api.queryDailyNotes(agentUid, { operation: 'read', document_id: source.anchor.document_id, entry_id: source.anchor.id, revision: source.anchor.revision }, { signal: controller.signal })
      .then((page) => {
        if (controller.signal.aborted) return;
        if (page?.document_id !== source.anchor.document_id || page?.revision !== source.anchor.revision) throw new Error('返回版本与引用不一致，未显示正文。');
        const entry = page.entries?.find((e) => e.id === source.anchor.id);
        if (!entry || typeof entry.text !== 'string' || typeof entry.title !== 'string' || !DIARY_STATUS[entry.status]) throw new Error('该版本中未找到完整条目，未显示正文。');
        setState({ status: 'ready', ref: source.ref, entry });
      })
      .catch((error) => {
        if (controller.signal.aborted) return;
        const message = error?.status === 403 || error?.status === 404 ? '无权查看或该笔记已不可用。' : error?.status === 429 ? '日记读取繁忙，请稍后重试。' : error?.status ? '暂时无法读取这个版本，请重试。' : error?.message || '读取失败，请重试。';
        setState({ status: 'error', ref: source.ref, error: message });
      });
    return () => controller.abort();
  }, [agentUid, source.ref, attempt]);
  const current = state.ref === source.ref ? state : { status: 'loading' };
  const related = view.sources.filter((s) => s.ref !== source.ref && view.relations.some((r) => r.from === source.ref && r.to === s.ref || r.to === source.ref && r.from === s.ref));
  return createPortal(
    <aside className="cc-diary-reader" role="dialog" aria-modal="false" aria-label="日记引用详情" aria-describedby={titleID} onClick={(event) => event.stopPropagation()}>
      <header className="cc-diary-reader-header"><span><BookOpen size={17} aria-hidden="true" />日记来源</span><button ref={closeRef} className="cc-diary-icon-button" type="button" aria-label="关闭日记详情" onClick={onClose}><X size={18} /></button></header>
      <div className="cc-diary-reader-scroll">
        <h2 id={titleID}>{source.title}</h2>
        <p className="cc-diary-reader-meta">{source.day} · {DIARY_STATUS[source.status]}{source.status === 'draft' ? ' · 尚未核验' : ''}</p>
        <p className="cc-diary-version" title={source.anchor.revision}>引用时版本 <code>{source.anchor.revision.slice(4, 12)}</code></p>
        {source.currentStatus !== source.status && <p className="cc-diary-reader-note">当前状态：{DIARY_STATUS[source.currentStatus]}。这里展示引用时的版本。</p>}
        <section className="cc-diary-reader-body" aria-live="polite" aria-busy={current.status === 'loading'}>
          {current.status === 'loading' && <div className="cc-diary-skeleton" role="status" aria-label="正在读取引用版本"><span /><span /><span /></div>}
          {current.status === 'ready' && (current.entry.text ? <div className="cc-diary-prose" dangerouslySetInnerHTML={{ __html: renderSafeMarkdown(current.entry.text) }} /> : <p>这个条目的正文为空。</p>)}
          {current.status === 'error' && <div className="cc-diary-read-error" role="alert"><p>{current.error}</p><button type="button" onClick={() => setAttempt((n) => n + 1)}><RotateCcw size={14} aria-hidden="true" />重试读取</button></div>}
        </section>
        {related.length > 0 && <section className="cc-diary-related"><h3>同次读取的关联笔记</h3>{related.map((s) => <div key={s.ref}><p>{view.relations.filter((r) => r.from === source.ref && r.to === s.ref || r.to === source.ref && r.from === s.ref).map((r) => `${r.from === source.ref ? '' : '被'}${DIARY_RELATIONS[r.kind]}`).join(' · ')}</p><SourceRow source={s} onSelect={onSelect} /></div>)}</section>}
        <details className="cc-diary-reference"><summary>引用标识</summary><code>{source.ref}</code></details>
      </div>
    </aside>, document.body,
  );
}

export default function DiaryToolResult({ item, view, agentUid }) {
  const [rawOpen, setRawOpen] = useState(false);
  const [selected, setSelected] = useState(null);
  const triggerRef = useRef(null);
  const select = (source, trigger) => { if (trigger && !selected) triggerRef.current = trigger; setSelected(source); };
  const close = React.useCallback(() => { setSelected(null); if (triggerRef.current?.isConnected) triggerRef.current.focus({ preventScroll: true }); }, []);
  const searching = view.action === 'search';
  const label = view.pending ? searching ? '正在搜索日记' : '正在读取日记' : searching ? `找到 ${view.sources.length} 条笔记` : `已返回 ${view.sources.length} 条笔记正文`;
  return <section className="cc-diary-tool" aria-label="日记查阅" onClick={(event) => event.stopPropagation()}>
    <div className="cc-diary-tool-heading" role="status">{searching ? <Search size={15} aria-hidden="true" /> : <BookOpen size={15} aria-hidden="true" />}<span>{label}</span>{searching && !view.pending && <small>仅搜索命中</small>}</div>
    {view.query && <p className="cc-diary-query">{view.query}</p>}
    {view.sources.length > 0 && <DiarySourceGraph view={view} selectedRef={selected?.ref} onSelect={select} />}
    {view.relations.length > 0 && <p className="cc-diary-closure">含{[...new Set(view.relations.map((r) => DIARY_RELATIONS[r.kind]))].join('、')}关系 · 一并返回</p>}
    {view.incomplete && <p className="cc-diary-incomplete">还有更多结果 · 本次仅展示已返回的条目</p>}
    {!view.pending && view.sources.length === 0 && <p className="cc-diary-incomplete">本次未返回笔记{view.incomplete ? '，结果尚不完整。' : '。'}</p>}
    {item.result != null && <><button className="cc-diary-raw-toggle" type="button" aria-expanded={rawOpen} onClick={() => setRawOpen((v) => !v)}><ChevronRight size={12} aria-hidden="true" />原始工具结果</button>{rawOpen && <pre className="cc-diary-raw">{typeof item.result === 'string' ? item.result : JSON.stringify(item.result, null, 2)}</pre>}</>}
    {selected && <DiaryReader agentUid={agentUid} source={selected} view={view} onSelect={select} onClose={close} />}
  </section>;
}
