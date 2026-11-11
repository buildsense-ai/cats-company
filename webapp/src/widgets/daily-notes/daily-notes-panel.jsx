import React, { useCallback, useDeferredValue, useEffect, useMemo, useRef, useState } from 'react';
import { ArrowRight, List, LoaderCircle, Network, RotateCcw, Search, X } from 'lucide-react';
import { api } from '../../api';
import { renderSafeMarkdown } from '../markdown-utils';
import { buildDailyNotesModel, formatDay, matchEntries, RELATION_LABELS } from './daily-notes-model';
import { layoutDailyNotes } from './daily-notes-layout';
import DailyNotesNetwork from './daily-notes-network';
import './daily-notes.css';

const BUSY_RETRIES = 4;
const wait = (ms, signal) => new Promise((resolve, reject) => {
  const timer = setTimeout(resolve, ms);
  signal?.addEventListener('abort', () => { clearTimeout(timer); reject(new DOMException('aborted', 'AbortError')); }, { once: true });
});

// CatsLog refuses excess Daily Notes reads with 429 instead of queueing them.
async function queryWithBackoff(agentUid, query, signal) {
  for (let attempt = 0; ; attempt += 1) {
    try {
      return await api.queryDailyNotes(agentUid, query, { signal });
    } catch (error) {
      if (error?.status !== 429 || attempt >= BUSY_RETRIES || signal?.aborted) throw error;
      await wait(2000 * (attempt + 1), signal);
    }
  }
}

const describeError = (error) => {
  if (error?.status === 404) return '';
  if (error?.status === 429) return 'Daily Notes 正忙，请稍后重试。';
  if (error?.status === 504) return '读取超时，请重试。';
  return 'Daily Notes 暂时无法读取，请稍后重试。';
};

function Highlight({ text, terms }) {
  if (!terms.length) return text;
  const lower = text.toLocaleLowerCase('zh-CN');
  const parts = [];
  let index = 0;
  while (index < text.length) {
    let next = -1;
    let length = 0;
    for (const term of terms) {
      const found = lower.indexOf(term, index);
      if (found !== -1 && (next === -1 || found < next)) { next = found; length = term.length; }
    }
    if (next === -1) break;
    if (next > index) parts.push(text.slice(index, next));
    parts.push(<mark key={next}>{text.slice(next, next + length)}</mark>);
    index = next + length;
  }
  parts.push(text.slice(index));
  return parts;
}

export default function DailyNotesPanel({ agentUid }) {
  const [pages, setPages] = useState([]);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState(null);
  const [cursor, setCursor] = useState('');
  const [query, setQuery] = useState('');
  const [view, setView] = useState('network');
  const [selectedId, setSelectedId] = useState('');
  const [documents, setDocuments] = useState(() => new Map());
  const [loadAttempt, setLoadAttempt] = useState(0);
  const documentsRef = useRef(documents);
  const readControllerRef = useRef(null);
  const layoutRef = useRef(null);
  const listRef = useRef(null);
  documentsRef.current = documents;

  // Graph pages load one after another and render as they arrive.
  useEffect(() => {
    const controller = new AbortController();
    let next = cursor;
    setLoading(true);
    setLoadError(null);
    (async () => {
      try {
        do {
          const page = await queryWithBackoff(agentUid, next ? { operation: 'graph', cursor: next } : { operation: 'graph' }, controller.signal);
          next = page?.exhausted ? '' : page?.next_cursor || '';
          setPages((current) => [...current, page]);
          setCursor(next);
        } while (next);
        setLoading(false);
      } catch (error) {
        if (controller.signal.aborted) return;
        setLoadError(error);
        setLoading(false);
      }
    })();
    return () => controller.abort();
    // cursor is read once per attempt; a retry resumes from it.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [agentUid, loadAttempt]);

  useEffect(() => () => readControllerRef.current?.abort(), []);

  const model = useMemo(() => buildDailyNotesModel(pages), [pages]);
  const layout = useMemo(() => {
    if (view !== 'network') return layoutRef.current;
    const previous = new Map((layoutRef.current?.nodes || []).map((node) => [node.id, node]));
    layoutRef.current = layoutDailyNotes(model, previous);
    return layoutRef.current;
  }, [model, view]);
  const graph = useMemo(() => layout && { ...layout, complete: !loading }, [layout, loading]);

  const texts = useMemo(() => {
    const out = new Map();
    for (const doc of documents.values()) {
      for (const entry of doc.entries || []) out.set(entry.id, entry.text || '');
    }
    return out;
  }, [documents]);

  const deferredQuery = useDeferredValue(query.trim());
  const terms = useMemo(() => deferredQuery.toLocaleLowerCase('zh-CN').split(/\s+/).filter(Boolean), [deferredQuery]);
  const matches = useMemo(() => matchEntries(model, deferredQuery, texts), [model, deferredQuery, texts]);

  // One document read covers a whole day. Reads are cached for the session.
  const readDocument = useCallback(async (documentId, signal) => {
    const known = documentsRef.current.get(documentId);
    if (known?.status === 'ready' || known?.status === 'loading') return;
    setDocuments((current) => new Map(current).set(documentId, { status: 'loading', entries: [] }));
    try {
      const entries = [];
      let next = '';
      do {
        const page = await queryWithBackoff(agentUid, { operation: 'read', document_id: documentId, ...(next ? { cursor: next } : {}) }, signal);
        entries.push(...(page?.entries || []));
        next = page?.exhausted ? '' : page?.next_cursor || '';
      } while (next);
      setDocuments((current) => new Map(current).set(documentId, { status: 'ready', entries }));
    } catch (error) {
      setDocuments((current) => new Map(current).set(documentId, { status: signal?.aborted ? 'idle' : 'error', entries: [] }));
      if (signal?.aborted) return;
    }
  }, [agentUid]);

  // A selection is a note or, from the graph, a source session.
  const selected = selectedId ? model.entries.get(selectedId) : null;
  const selectedSource = selectedId && !selected ? model.sources.get(selectedId) : null;
  // The list opens on the newest note; the graph opens on the whole picture.
  useEffect(() => {
    if (view === 'list' && !selectedId && model.days[0]?.entries[0]) setSelectedId(model.days[0].entries[0].id);
  }, [model, selectedId, view]);
  const selectedStatus = selected ? documents.get(selected.documentId)?.status : '';
  useEffect(() => {
    if (!selected?.documentId || selectedStatus === 'ready' || selectedStatus === 'loading' || selectedStatus === 'error') return;
    readControllerRef.current?.abort();
    const controller = new AbortController();
    readControllerRef.current = controller;
    readDocument(selected.documentId, controller.signal);
  }, [selected?.documentId, selectedStatus, readDocument]);

  // A search reaches into note bodies by reading the remaining days, one at a time.
  const unreadDays = model.days.filter(({ entries }) => {
    const status = documents.get(entries[0]?.documentId)?.status;
    return status !== 'ready' && status !== 'error';
  });
  const searchingBodies = Boolean(deferredQuery) && unreadDays.length > 0 && !loading;
  useEffect(() => {
    if (!searchingBodies) return undefined;
    const controller = new AbortController();
    (async () => {
      for (const { entries } of model.days) {
        if (controller.signal.aborted) return;
        const id = entries[0]?.documentId;
        if (id && !['ready', 'loading', 'error'].includes(documentsRef.current.get(id)?.status)) {
          await readDocument(id, controller.signal);
        }
      }
    })();
    return () => controller.abort();
  }, [searchingBodies, model, readDocument]);

  const visibleDays = useMemo(() => model.days
    .map(({ day, entries }) => ({ day, entries: matches ? entries.filter((entry) => matches.has(entry.id)) : entries }))
    .filter(({ entries }) => entries.length), [model, matches]);

  const select = (id = '') => {
    setSelectedId(id);
    requestAnimationFrame(() => listRef.current?.querySelector(`[data-entry-id="${CSS.escape(id)}"]`)?.scrollIntoView({ block: 'nearest' }));
  };

  const failure = loadError ? describeError(loadError) : '';
  const empty = !loading && !pages.some((page) => page?.nodes?.length) && !failure;
  const dayDocument = selected ? documents.get(selected.documentId) : null;
  const body = dayDocument?.entries?.find((entry) => entry.id === selected.id);
  const related = selected ? selected.links.map((link) => {
    const outgoing = link.from === selected.id;
    const other = model.entries.get(outgoing ? link.to : link.from);
    return other ? { other, label: RELATION_LABELS[link.kind] || link.kind, outgoing } : null;
  }).filter(Boolean) : [];

  const status = loading
    ? `正在加载 · 已读取 ${model.days.length} 天`
    : matches
      ? `${matches.size} 条匹配${searchingBodies ? ` · 正在检索正文（剩 ${unreadDays.length} 天）` : ''}`
      : `${model.entries.size} 条笔记 · ${model.days.length} 天`;

  return (
    <div className="cc-daily-notes" data-view={view}>
      <div className="cc-daily-notes-toolbar">
        <label className="cc-daily-notes-search">
          <Search size={15} aria-hidden="true" />
          <input
            type="search"
            data-cc-input-feedback="off"
            data-cc-focus-group="true"
            value={query}
            placeholder="搜索标题、正文、日期或来源"
            aria-label="搜索 Daily Notes"
            onChange={(event) => setQuery(event.target.value)}
            onKeyDown={(event) => {
              if (event.key === 'Enter') event.preventDefault();
              if (event.key === 'Escape' && query) { event.preventDefault(); setQuery(''); }
            }}
          />
          {query && (
            <button type="button" className="cc-daily-notes-clear" aria-label="清除搜索" onClick={() => setQuery('')}>
              <X size={14} aria-hidden="true" />
            </button>
          )}
        </label>
        <span className="cc-daily-notes-status" role="status">
          {(loading || searchingBodies) && <LoaderCircle size={13} className="cc-daily-notes-spin" aria-hidden="true" />}
          {status}
        </span>
        <div className="cc-daily-notes-views" role="group" aria-label="视图">
          <button type="button" aria-pressed={view === 'list'} onClick={() => setView('list')}><List size={14} aria-hidden="true" />列表</button>
          <button type="button" aria-pressed={view === 'network'} onClick={() => setView('network')}><Network size={14} aria-hidden="true" />图谱</button>
        </div>
      </div>

      {failure && (
        <div className="cc-daily-notes-alert" role="alert">
          <span>{failure}{pages.length ? ' 已加载的笔记仍可阅读。' : ''}</span>
          <button type="button" className="oc-btn oc-btn-default" onClick={() => setLoadAttempt((n) => n + 1)}>
            <RotateCcw size={14} aria-hidden="true" />重试
          </button>
        </div>
      )}

      {empty ? (
        <div className="cc-daily-notes-empty">
          <strong>还没有 Daily Notes</strong>
          <p>助手完成工作后会按天整理笔记，整理好的内容会出现在这里。</p>
        </div>
      ) : (
        <div className="cc-daily-notes-body">
          {view === 'list' ? (
            <nav className="cc-daily-notes-list" ref={listRef} aria-label="按天排列的笔记">
              {visibleDays.map(({ day, entries }) => (
                <section key={day} className="cc-daily-notes-day">
                  <h4>{formatDay(day)}<span>{entries.length}</span></h4>
                  <ul>
                    {entries.map((entry) => (
                      <li key={entry.id}>
                        <button
                          type="button"
                          data-entry-id={entry.id}
                          aria-current={entry.id === selectedId ? 'true' : undefined}
                          onClick={() => setSelectedId(entry.id)}
                        >
                          <Highlight text={entry.title} terms={terms} />
                        </button>
                      </li>
                    ))}
                  </ul>
                </section>
              ))}
              {matches && !visibleDays.length && (
                <p className="cc-daily-notes-none">{searchingBodies ? '正在检索正文…' : `没有找到“${deferredQuery}”。`}</p>
              )}
              {loading && <p className="cc-daily-notes-none">正在加载更早的笔记…</p>}
            </nav>
          ) : (
            graph && <DailyNotesNetwork layout={graph} model={model} matches={matches} selectedId={selectedId} onSelect={select} rightInset={selectedId ? 376 : 0} />
          )}

          {(view === 'list' || selectedId) && (
          <article className="cc-daily-notes-reader" aria-live="polite">
            {view === 'network' && (
              <button type="button" className="cc-daily-notes-reader-close" aria-label="关闭" title="关闭" onClick={() => select('')}>
                <X size={15} aria-hidden="true" />
              </button>
            )}
            {selectedSource ? (
              <>
                <p className="cc-daily-notes-reader-day">来源会话</p>
                <h3>{selectedSource.label}</h3>
                <section className="cc-daily-notes-related is-first">
                  <h4>引用它的笔记 · {selectedSource.entries.size}</h4>
                  <ul>
                    {[...selectedSource.entries].map((id) => model.entries.get(id)).filter(Boolean)
                      .sort((a, b) => b.day.localeCompare(a.day))
                      .map((other) => (
                        <li key={other.id}>
                          <button type="button" onClick={() => select(other.id)}>
                            <span className="cc-daily-notes-related-title">{other.title}</span>
                            <span className="cc-daily-notes-related-day">{formatDay(other.day).split(' · ')[0]}</span>
                            <ArrowRight size={13} aria-hidden="true" />
                          </button>
                        </li>
                      ))}
                  </ul>
                </section>
              </>
            ) : !selected ? (
              <p className="cc-daily-notes-none">{loading ? '正在加载笔记…' : '选择一条笔记阅读。'}</p>
            ) : (
              <>
                <p className="cc-daily-notes-reader-day">{formatDay(selected.day)}</p>
                <h3><Highlight text={selected.title} terms={terms} /></h3>
                {body?.text ? (
                  <div className="cc-daily-notes-text" dangerouslySetInnerHTML={{ __html: renderSafeMarkdown(body.text) }} />
                ) : dayDocument?.status === 'error' ? (
                  <div className="cc-daily-notes-alert" role="alert">
                    <span>正文读取失败。</span>
                    <button
                      type="button"
                      className="oc-btn oc-btn-default"
                      onClick={() => {
                        setDocuments((current) => { const next = new Map(current); next.delete(selected.documentId); return next; });
                        const controller = new AbortController();
                        readControllerRef.current = controller;
                        readDocument(selected.documentId, controller.signal);
                      }}
                    >
                      <RotateCcw size={14} aria-hidden="true" />重试
                    </button>
                  </div>
                ) : dayDocument?.status === 'ready' ? (
                  <p className="cc-daily-notes-none">这条笔记没有正文。</p>
                ) : (
                  <div className="cc-daily-notes-text-loading" aria-label="正在读取正文">
                    <span /><span /><span />
                  </div>
                )}
                {related.length > 0 && (
                  <section className="cc-daily-notes-related">
                    <h4>关联笔记</h4>
                    <ul>
                      {related.map(({ other, label, outgoing }) => (
                        <li key={`${other.id}-${label}-${outgoing}`}>
                          <button type="button" onClick={() => select(other.id)}>
                            <span className="cc-daily-notes-relation">{outgoing ? label : `被${label}`}</span>
                            <span className="cc-daily-notes-related-title">{other.title}</span>
                            <span className="cc-daily-notes-related-day">{formatDay(other.day).split(' · ')[0]}</span>
                            <ArrowRight size={13} aria-hidden="true" />
                          </button>
                        </li>
                      ))}
                    </ul>
                  </section>
                )}
                {selected.sources.size > 0 && (
                  <section className="cc-daily-notes-sources">
                    <h4>来源</h4>
                    <div className="cc-daily-notes-source-chips">
                      {[...selected.sources].filter((key) => model.sources.has(key)).map((key) => (
                        <button type="button" key={key} onClick={() => select(key)}>
                          {model.sources.get(key).label}
                          <span>{model.sources.get(key).entries.size}</span>
                        </button>
                      ))}
                    </div>
                  </section>
                )}
              </>
            )}
          </article>
          )}
        </div>
      )}
    </div>
  );
}
