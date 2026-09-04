import React, { useCallback, useEffect, useRef, useState } from 'react';
import { FileText, LoaderCircle, MessageSquareText, Search, X } from 'lucide-react';
import { api } from '../api';

const SEARCH_DEBOUNCE_MS = 300;
const CATEGORIES = [
  { id: 'all', label: '全部' },
  { id: 'message', label: '消息' },
  { id: 'artifact', label: '文件与产物' },
];

const FOCUSABLE_SELECTOR = [
  'button:not([disabled])',
  'input:not([disabled])',
  '[href]',
  '[tabindex]:not([tabindex="-1"])',
].join(',');

export function normalizeSearchResult(item, index = 0) {
  const messageId = Number(item?.message_id) || 0;
  const topicId = String(item?.topic_id || '');
  const attachmentName = String(item?.artifact_name || '');
  const category = item?.content_type === 'artifact' ? 'artifact' : 'message';
  return {
    key: `${topicId}:${messageId}:${index}`,
    raw: item,
    topicId,
    messageId,
    category,
    source: String(item?.topic_name || topicId || '未知会话'),
    time: item?.created_at || '',
    snippet: String(item?.snippet || item?.content || ''),
    attachmentName,
    isGroup: topicId.startsWith('grp_'),
    groupId: topicId.startsWith('grp_') ? Number(topicId.slice(4)) || undefined : undefined,
    avatarUrl: '',
  };
}

export function splitSearchHighlight(text, query) {
  const source = String(text || '');
  const needle = String(query || '').trim();
  if (!source || !needle) return [{ text: source, match: false }];
  const sourceLower = source.toLocaleLowerCase();
  const needleLower = needle.toLocaleLowerCase();
  const parts = [];
  let cursor = 0;
  while (cursor < source.length) {
    const matchIndex = sourceLower.indexOf(needleLower, cursor);
    if (matchIndex < 0) {
      parts.push({ text: source.slice(cursor), match: false });
      break;
    }
    if (matchIndex > cursor) {
      parts.push({ text: source.slice(cursor, matchIndex), match: false });
    }
    const matchEnd = matchIndex + needle.length;
    parts.push({ text: source.slice(matchIndex, matchEnd), match: true });
    cursor = matchEnd;
  }
  return parts.length ? parts : [{ text: source, match: false }];
}

export function compactSearchSnippet(text, query, maxRunes = 64) {
  const source = String(text || '').trim();
  const needle = String(query || '').trim();
  const sourceRunes = Array.from(source);
  if (!source || sourceRunes.length <= maxRunes) return source;

  const matchOffset = needle
    ? source.toLocaleLowerCase().indexOf(needle.toLocaleLowerCase())
    : -1;
  if (matchOffset < 0) {
    return `${sourceRunes.slice(0, maxRunes).join('')}…`;
  }

  const matchStart = Array.from(source.slice(0, matchOffset)).length;
  const matchLength = Math.max(1, Array.from(source.slice(matchOffset, matchOffset + needle.length)).length);
  const contextBefore = Math.max(8, Math.floor((maxRunes - matchLength) * 0.34));
  let start = Math.max(0, matchStart - contextBefore);
  let end = Math.min(sourceRunes.length, start + maxRunes);
  if (end - start < maxRunes) {
    start = Math.max(0, end - maxRunes);
  }

  const excerpt = sourceRunes.slice(start, end).join('');
  return `${start > 0 ? '…' : ''}${excerpt}${end < sourceRunes.length ? '…' : ''}`;
}

function HighlightedSearchText({ text, query }) {
  return splitSearchHighlight(text, query).map((part, index) => (
    part.match
      ? <mark key={`${index}-${part.text}`}>{part.text}</mark>
      : <React.Fragment key={`${index}-${part.text}`}>{part.text}</React.Fragment>
  ));
}

function resultArray(response) {
  return Array.isArray(response?.results) ? response.results : [];
}

function formatSearchTime(value) {
  if (!value) return '';
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return String(value);
  return new Intl.DateTimeFormat('zh-CN', { month: 'numeric', day: 'numeric', hour: '2-digit', minute: '2-digit' }).format(date);
}

export default function SearchOverlay({ open, onClose, onSelectResult }) {
  const [query, setQuery] = useState('');
  const [category, setCategory] = useState('all');
  const [results, setResults] = useState([]);
  const [activeResultIndex, setActiveResultIndex] = useState(-1);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const dialogRef = useRef(null);
  const inputRef = useRef(null);
  const listRef = useRef(null);
  const openerRef = useRef(null);
  const savedScrollRef = useRef(0);
  const requestRef = useRef(0);
  const resultRefs = useRef([]);
  const activeResultIndexRef = useRef(-1);

  const updateActiveResult = useCallback((index, shouldFocus = false) => {
    activeResultIndexRef.current = index;
    setActiveResultIndex(index);
    if (shouldFocus && index >= 0) {
      resultRefs.current[index]?.focus();
    }
  }, []);

  useEffect(() => {
    if (!open) return undefined;
    document.documentElement.classList.add('cc-global-search-open');
    openerRef.current = document.activeElement instanceof HTMLElement
      ? document.activeElement
      : null;
    const focusFrame = window.requestAnimationFrame(() => {
      inputRef.current?.focus();
      if (listRef.current) listRef.current.scrollTop = savedScrollRef.current;
    });
    return () => {
      window.cancelAnimationFrame(focusFrame);
      document.documentElement.classList.remove('cc-global-search-open');
      const opener = openerRef.current;
      window.requestAnimationFrame(() => {
        if (opener?.isConnected) opener.focus();
      });
    };
  }, [open]);

  useEffect(() => {
    if (!open) return undefined;
    const handleKeyDown = (event) => {
      if (event.key === 'Escape') {
        event.preventDefault();
        onClose();
        return;
      }
      if (event.key === 'Tab') {
        const focusable = [...(dialogRef.current?.querySelectorAll(FOCUSABLE_SELECTOR) || [])];
        if (!focusable.length) return;
        const first = focusable[0];
        const last = focusable[focusable.length - 1];
        if (event.shiftKey && (document.activeElement === first || !dialogRef.current?.contains(document.activeElement))) {
          event.preventDefault();
          last.focus();
        } else if (!event.shiftKey && (document.activeElement === last || !dialogRef.current?.contains(document.activeElement))) {
          event.preventDefault();
          first.focus();
        }
        return;
      }
      if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
        const direction = event.key === 'ArrowDown' ? 1 : -1;
        let nextIndex = activeResultIndexRef.current < 0 && direction < 0
          ? 0
          : activeResultIndexRef.current;
        for (let checked = 0; checked < results.length; checked += 1) {
          nextIndex = (nextIndex + direction + results.length) % results.length;
          if (!resultRefs.current[nextIndex]?.disabled) {
            event.preventDefault();
            updateActiveResult(nextIndex, true);
            return;
          }
        }
      }
      if (event.key === 'Enter' && activeResultIndexRef.current >= 0) {
        const result = results[activeResultIndexRef.current];
        const resultButton = resultRefs.current[activeResultIndexRef.current];
        if (result && resultButton && !resultButton.disabled && document.activeElement === resultButton) {
          event.preventDefault();
          onSelectResult(result);
        }
      }
    };
    document.addEventListener('keydown', handleKeyDown);
    return () => {
      document.removeEventListener('keydown', handleKeyDown);
    };
  }, [onClose, onSelectResult, open, results, updateActiveResult]);

  useEffect(() => {
    const keyword = query.trim();
    updateActiveResult(-1);
    resultRefs.current = [];
    if (keyword.length < 2) {
      requestRef.current += 1;
      setResults([]);
      setLoading(false);
      setError('');
      return undefined;
    }
    const requestId = ++requestRef.current;
    const controller = new AbortController();
    const timer = window.setTimeout(async () => {
      setLoading(true);
      setError('');
      try {
        const response = await api.getMessageSearch(keyword, category, { signal: controller.signal });
        if (requestRef.current !== requestId) return;
        setResults(resultArray(response).map(normalizeSearchResult));
      } catch (searchError) {
        if (requestRef.current !== requestId || searchError?.code === 'REQUEST_ABORTED') return;
        setResults([]);
        setError(searchError?.message || '搜索失败，请稍后重试');
      } finally {
        if (requestRef.current === requestId) setLoading(false);
      }
    }, SEARCH_DEBOUNCE_MS);
    return () => {
      window.clearTimeout(timer);
      controller.abort();
    };
  }, [category, query, updateActiveResult]);

  if (!open) return null;
  const keyword = query.trim();
  const liveMessage = keyword.length < 2
    ? '输入至少 2 个字开始搜索'
    : loading
      ? '正在搜索'
      : error || (results.length ? `找到 ${results.length} 条结果` : '没有找到相关结果');
  return (
    <div className="cc-global-search-backdrop" role="presentation" onMouseDown={(event) => event.target === event.currentTarget && onClose()}>
      <section ref={dialogRef} className="cc-global-search" role="dialog" aria-modal="true" aria-label="全局搜索">
        <header className="cc-global-search-header">
          <div className="cc-global-search-field">
            <Search className="cc-global-search-leading-icon" size={19} aria-hidden="true" />
            <input
              ref={inputRef}
              data-cc-focus-group="true"
              name="global-search"
              autoComplete="off"
              spellCheck={false}
              value={query}
              onFocus={() => updateActiveResult(-1)}
              onChange={(event) => setQuery(event.target.value)}
              placeholder="搜索消息、文件与产物…"
              aria-label="搜索消息、文件与产物"
            />
          </div>
          <button type="button" className="cc-global-search-close" onClick={onClose} aria-label="关闭搜索">
            <X size={19} aria-hidden="true" />
          </button>
        </header>
        <div className="cc-global-search-tabs" role="group" aria-label="搜索范围">
          {CATEGORIES.map((item) => (
            <button
              key={item.id}
              type="button"
              aria-pressed={category === item.id}
              className={category === item.id ? 'active' : ''}
              onClick={() => setCategory(item.id)}
            >
              {item.label}
            </button>
          ))}
        </div>
        <span className="oc-visually-hidden" role="status" aria-live="polite">{liveMessage}</span>
        <div
          ref={listRef}
          className="cc-global-search-results"
          aria-label="搜索结果"
          aria-busy={loading}
          onScroll={(event) => { savedScrollRef.current = event.currentTarget.scrollTop; }}
        >
          {keyword.length < 2 && (
            <div className="cc-global-search-state is-idle">
              <span>输入至少 2 个字开始搜索</span>
            </div>
          )}
          {keyword.length >= 2 && loading && (
            <div className="cc-global-search-state">
              <LoaderCircle className="cc-spin" size={18} aria-hidden="true" />
              <span>正在搜索…</span>
            </div>
          )}
          {keyword.length >= 2 && !loading && error && (
            <div className="cc-global-search-state error">
              <strong>搜索暂时不可用</strong>
              <span>{error}</span>
            </div>
          )}
          {keyword.length >= 2 && !loading && !error && results.length === 0 && (
            <div className="cc-global-search-state">
              <strong>没有找到相关结果</strong>
              <span>试试更短的关键词，或切换搜索范围</span>
            </div>
          )}
          {!loading && results.map((result, index) => (
            <button
              key={result.key}
              ref={(node) => { resultRefs.current[index] = node; }}
              type="button"
              className={`cc-global-search-result${activeResultIndex === index ? ' is-active' : ''}`}
              disabled={!result.topicId || !result.messageId}
              aria-current={activeResultIndex === index ? 'true' : undefined}
              onFocus={() => updateActiveResult(index)}
              onClick={() => onSelectResult(result)}
            >
              <span className="cc-global-search-result-icon" aria-hidden="true">{result.category === 'artifact' ? <FileText size={18} /> : <MessageSquareText size={18} />}</span>
              <span className="cc-global-search-result-body">
                <span className="cc-global-search-result-meta"><strong>{result.source}</strong><time>{formatSearchTime(result.time)}</time></span>
                <span className="cc-global-search-result-snippet">
                  <HighlightedSearchText
                    text={compactSearchSnippet(result.snippet || '查看命中消息', keyword)}
                    query={keyword}
                  />
                </span>
                {result.attachmentName && <span className="cc-global-search-result-file"><FileText size={13} aria-hidden="true" /><HighlightedSearchText text={result.attachmentName} query={keyword} /></span>}
              </span>
            </button>
          ))}
        </div>
      </section>
    </div>
  );
}
