import React, { useEffect, useId, useRef, useState } from 'react';
import { BookOpen, Check, ChevronDown, ThumbsDown, ThumbsUp, X } from 'lucide-react';
import { api } from '../api';
import { WikiReader } from './wiki-memory-panel';
import './wiki-injection-panel.css';

const timeFormat = new Intl.DateTimeFormat('zh-CN', { hour: '2-digit', minute: '2-digit', second: '2-digit' });

export function wikiInjectionBatches(blocks) {
  const unique = new Map();
  for (const { type, payload } of blocks) {
    if (type === 'wiki_injection' && payload?.event_id && Number.isFinite(Date.parse(payload.timestamp))) {
      unique.set(payload.event_id, payload);
    }
  }
  let refresh = 0;
  return [...unique.values()].sort((a, b) => Date.parse(a.timestamp) - Date.parse(b.timestamp)).map(batch => ({
    ...batch,
    time: timeFormat.format(new Date(batch.timestamp)),
    title: batch.kind === 'previous' ? '上轮补充' : batch.kind === 'refresh' ? '本轮刷新 ' + ++refresh : '本轮检索',
  }));
}

export default function WikiInjectionPanel({ agentUid, batches, selected, onSelect, onClose }) {
  const [menuOpen, setMenuOpen] = useState(false);
  const menuId = useId();
  const [reading, setReading] = useState(null);
  const heading = useRef(null);
  const closeButton = useRef(null);
  const titleButton = useRef(null);
  const returnFocus = useRef(null);

  useEffect(() => { closeButton.current?.focus({ preventScroll: true }); }, []);
  useEffect(() => {
    if (!menuOpen) return undefined;
    const dismiss = event => {
      if (!heading.current?.contains(event.target)) setMenuOpen(false);
    };
    document.addEventListener('pointerdown', dismiss);
    document.addEventListener('focusin', dismiss);
    return () => {
      document.removeEventListener('pointerdown', dismiss);
      document.removeEventListener('focusin', dismiss);
    };
  }, [menuOpen]);
  useEffect(() => {
    if (!reading && returnFocus.current) {
      returnFocus.current.focus({ preventScroll: true });
      returnFocus.current = null;
    }
  }, [reading]);

  const current = batches.find(batch => batch.event_id === selected) ?? batches[0];
  if (!current) return null;
  const reader = reading?.eventId === current.event_id ? reading : null;

  return <aside className="cc-wiki-panel" aria-label="记忆注入" onKeyDown={event => {
    if (event.key !== 'Escape' || event.defaultPrevented) return;
    event.preventDefault();
    event.stopPropagation();
    if (menuOpen) {
      setMenuOpen(false);
      titleButton.current?.focus({ preventScroll: true });
    } else {
      onClose();
    }
  }}>
    <header className="cc-wiki-header">
      <h2><BookOpen size={16} aria-hidden="true" />记忆</h2>
      <button ref={closeButton} type="button" className="cc-wiki-icon" aria-label="关闭记忆注入" onClick={onClose}><X size={17} /></button>
    </header>
    <div className="cc-wiki-injection-scroll" hidden={Boolean(reader)}>
      <div className="cc-wiki-injection-heading" ref={heading}>
        <div className="cc-wiki-injection-meta">
          {current.kind === 'previous' && '上一轮发起，本轮收到 · '}
          <time dateTime={current.timestamp}>{current.time} 注入</time>
        </div>
        <h3>{batches.length === 1 ? (current.kind === 'initial' ? '本次记忆注入' : current.title) : (
          <button ref={titleButton} type="button" aria-expanded={menuOpen} aria-controls={menuId} aria-label={current.title + '，切换注入批次'} onClick={() => setMenuOpen(open => !open)}>
            {current.title}<ChevronDown size={14} aria-hidden="true" />
          </button>
        )}</h3>
        <div id={menuId} hidden={!menuOpen} className="cc-wiki-injection-menu" role="group" aria-label="切换注入批次">
          {batches.map(batch => <button key={batch.event_id} type="button"
            aria-current={current === batch ? 'true' : undefined} onClick={() => {
              setReading(null);
              returnFocus.current = null;
              setMenuOpen(false);
              onSelect(batch.event_id);
              titleButton.current?.focus({ preventScroll: true });
            }}>
            <span>{batch.title}</span>
            <time dateTime={batch.timestamp}>{batch.time}</time>
            {current === batch && <Check size={14} aria-hidden="true" />}
          </button>)}
        </div>
      </div>
      <InjectionDetail key={current.event_id} agentUid={agentUid} eventId={current.event_id} onWiki={(ref, button) => {
        returnFocus.current = button;
        setReading({ eventId: current.event_id, item: { id: ref.wiki_id, title: ref.title } });
      }} />
    </div>
    {reader && <WikiReader agentUid={agentUid} item={reader.item} backLabel="返回注入详情" onBack={() => setReading(null)} />}
  </aside>;
}

function InjectionDetail({ agentUid, eventId, onWiki }) {
  const [result, setResult] = useState(null);
  const [attempt, setAttempt] = useState(0);
  const [pending, setPending] = useState(null);
  const [voteFailed, setVoteFailed] = useState(false);
  useEffect(() => {
    const controller = new AbortController();
    api.getWikiInjection(agentUid, eventId, controller.signal).then(detail => {
      if (!controller.signal.aborted) setResult({ detail });
    }).catch(() => {
      if (!controller.signal.aborted) setResult({ error: true });
    });
    return () => controller.abort();
  }, [agentUid, eventId, attempt]);

  if (!result) return <p className="cc-wiki-feedback" role="status">正在读取注入详情…</p>;
  if (result.error) return <div className="cc-wiki-feedback" role="status">注入详情暂不可用。<button type="button" onClick={() => {
    setResult(null);
    setAttempt(value => value + 1);
  }}>重试</button></div>;

  const { detail } = result;
  async function vote(wikiId, value) {
    setPending(wikiId);
    setVoteFailed(false);
    try {
      await api.voteOnWikiInjection(agentUid, eventId, wikiId, value);
      setResult(previous => ({ detail: { ...previous.detail, votes: { ...previous.detail.votes, [wikiId]: value } } }));
    } catch {
      setVoteFailed(true);
    } finally {
      setPending(null);
    }
  }
  return <>
    {detail.summary && <p className="cc-wiki-injection-summary">{detail.summary}</p>}
    {detail.restricted && <p className="cc-wiki-injection-meta">部分引用和摘要受会话权限限制。</p>}
    {detail.references.length > 0 && <div className="cc-wiki-injection-votes">
      <span>这次注入有帮助吗？</span>
      <VoteButtons label="这次注入" value={detail.votes['']} disabled={pending !== null} onVote={value => vote('', value)} />
    </div>}
    {voteFailed && <p className="cc-wiki-feedback" role="status">评价保存失败，请重试。</p>}
    {detail.references.length > 0 && <>
      <h4>注入的引用</h4>
      <ul className="cc-wiki-injection-refs">{detail.references.map(ref => <li key={ref.wiki_id}>
        <div className="cc-wiki-injection-ref">
          <button type="button" onClick={event => onWiki(ref, event.currentTarget)}>{ref.title} <span aria-hidden="true">↗</span></button>
          <VoteButtons label={ref.title} value={detail.votes[ref.wiki_id]} disabled={pending !== null} onVote={value => vote(ref.wiki_id, value)} />
        </div>
        <p className="cc-wiki-injection-meta">{ref.evidence_level === 'wiki_body' ? '读过正文' : '搜索片段'}{ref.contribution && ' · ' + ref.contribution}</p>
      </li>)}</ul>
    </>}
  </>;
}

function VoteButtons({ label, value, disabled, onVote }) {
  return <span className="cc-wiki-injection-vote-buttons" role="group" aria-label={label}>
    <button type="button" className="cc-wiki-icon" aria-label={label + '有帮助'} aria-pressed={value === 1} disabled={disabled} onClick={() => onVote(value === 1 ? 0 : 1)}><ThumbsUp size={15} /></button>
    <button type="button" className="cc-wiki-icon" aria-label={label + '没帮助'} aria-pressed={value === -1} disabled={disabled} onClick={() => onVote(value === -1 ? 0 : -1)}><ThumbsDown size={15} /></button>
  </span>;
}
