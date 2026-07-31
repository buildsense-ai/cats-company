import React, { useCallback, useEffect, useRef, useState } from 'react';
import { ArrowLeft, FileText, LoaderCircle, MessageSquareText, Search, X } from 'lucide-react';
import { api } from '../api';

const SEARCH_DEBOUNCE_MS = 300;
const CATEGORIES = [
  { id: 'all', label: '全部' },
  { id: 'message', label: '消息' },
  { id: 'artifact', label: '产物' },
];

function firstValue(item, keys, fallback = '') {
  for (const key of keys) {
    const value = item?.[key];
    if (value !== undefined && value !== null && String(value).trim()) return value;
  }
  return fallback;
}

export function normalizeSearchResult(item, index = 0) {
  const message = item?.message && typeof item.message === 'object' ? item.message : item;
  const topic = item?.topic && typeof item.topic === 'object' ? item.topic : {};
  const attachment = item?.attachment && typeof item.attachment === 'object' ? item.attachment : {};
  const messageId = Number(firstValue(message, ['message_id', 'id', 'seq_id'], firstValue(item, ['message_id', 'id'], 0))) || 0;
  const topicId = String(firstValue(item, ['topic_id', 'topicId'], firstValue(topic, ['topic_id', 'id'], firstValue(message, ['topic_id'], ''))));
  const attachmentName = String(firstValue(item, ['artifact_name', 'attachment_name', 'file_name', 'filename'], firstValue(attachment, ['name', 'file_name', 'filename'], '')));
  const rawType = String(firstValue(item, ['content_type', 'category', 'kind', 'result_type', 'type'], attachmentName ? 'artifact' : 'message')).toLowerCase();
  const category = rawType.includes('conversation') || rawType.includes('topic')
    ? 'conversation'
    : (rawType.includes('artifact') || rawType.includes('attachment') || rawType.includes('file') ? 'artifact' : 'message');
  return {
    key: String(firstValue(item, ['search_id'], `${topicId}:${messageId}:${index}`)),
    raw: item,
    topicId,
    messageId,
    category,
    source: String(firstValue(item, ['source_name', 'conversation_name', 'topic_name', 'title'], firstValue(topic, ['name', 'title'], topicId || '未知会话'))),
    time: firstValue(item, ['created_at', 'timestamp', 'sent_at', 'updated_at'], firstValue(message, ['created_at', 'timestamp', 'sent_at'], '')),
    snippet: String(firstValue(item, ['snippet', 'highlight', 'content', 'text'], firstValue(message, ['snippet', 'content', 'text'], ''))),
    attachmentName,
    isGroup: Boolean(firstValue(item, ['is_group'], firstValue(topic, ['is_group'], topicId.startsWith('grp_')))),
    groupId: Number(firstValue(item, ['group_id'], firstValue(topic, ['group_id'], topicId.startsWith('grp_') ? topicId.slice(4) : 0))) || undefined,
    avatarUrl: String(firstValue(item, ['avatar_url'], firstValue(topic, ['avatar_url'], ''))),
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

function HighlightedSearchText({ text, query }) {
  return splitSearchHighlight(text, query).map((part, index) => (
    part.match
      ? <mark key={`${index}-${part.text}`}>{part.text}</mark>
      : <React.Fragment key={`${index}-${part.text}`}>{part.text}</React.Fragment>
  ));
}

function normalizeConversationResult(item, index = 0) {
  const topicId = String(item?.topic_id || item?.topicId || item?.id || '');
  const source = String(item?.name || item?.topic_name || item?.title || topicId);
  return {
    key: `conversation-${topicId || index}`,
    topicId,
    messageId: 0,
    source,
    sender: '',
    snippet: '打开会话',
    attachmentName: '',
    category: 'conversation',
    time: item?.updated_at || item?.last_message_at || item?.created_at || '',
    isGroup: Boolean(item?.is_group || item?.isGroup || topicId.startsWith('grp_')),
    groupId: item?.group_id || item?.groupId,
    avatarUrl: item?.avatar_url || item?.avatarUrl,
  };
}

function conversationArray(response) {
  if (Array.isArray(response)) return response;
  return Array.isArray(response?.conversations) ? response.conversations : [];
}

function resultArray(response) {
  if (Array.isArray(response)) return response;
  for (const key of ['results', 'messages', 'items', 'data']) {
    if (Array.isArray(response?.[key])) return response[key];
  }
  return [];
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
  const inputRef = useRef(null);
  const listRef = useRef(null);
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
    window.requestAnimationFrame(() => {
      inputRef.current?.focus();
      if (listRef.current) listRef.current.scrollTop = savedScrollRef.current;
    });
    const handleKeyDown = (event) => {
      if (event.key === 'Escape') {
        onClose();
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
    return () => document.removeEventListener('keydown', handleKeyDown);
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
        const [response, conversationResponse] = await Promise.all([
          api.getMessageSearch(keyword, category, { signal: controller.signal }),
          category === 'all'
            ? api.getConversations({ signal: controller.signal }).catch((conversationError) => {
              if (conversationError?.code === 'REQUEST_ABORTED') throw conversationError;
              return null;
            })
            : Promise.resolve(null),
        ]);
        if (requestRef.current !== requestId) return;
        const loweredKeyword = keyword.toLocaleLowerCase();
        const conversations = conversationArray(conversationResponse)
          .filter((item) => String(item?.name || item?.topic_name || item?.title || '').toLocaleLowerCase().includes(loweredKeyword))
          .map(normalizeConversationResult);
        setResults([...conversations, ...resultArray(response).map(normalizeSearchResult)]);
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
  return (
    <div className="cc-global-search-backdrop" role="presentation" onMouseDown={(event) => event.target === event.currentTarget && onClose()}>
      <section className="cc-global-search" role="dialog" aria-modal="true" aria-label="全局搜索">
        <header className="cc-global-search-header">
          <Search size={20} aria-hidden="true" />
          <input ref={inputRef} value={query} onFocus={() => updateActiveResult(-1)} onChange={(event) => setQuery(event.target.value)} placeholder="搜索消息与产物" aria-label="搜索消息与产物" />
          {query && <button type="button" onClick={() => setQuery('')} aria-label="清空搜索"><X size={18} /></button>}
          <button type="button" className="cc-global-search-close" onClick={onClose} aria-label="关闭搜索"><ArrowLeft size={19} /></button>
        </header>
        <div className="cc-global-search-tabs" role="tablist" aria-label="搜索分类">
          {CATEGORIES.map((item) => <button key={item.id} type="button" role="tab" aria-selected={category === item.id} className={category === item.id ? 'active' : ''} onClick={() => setCategory(item.id)}>{item.label}</button>)}
        </div>
        <div ref={listRef} className="cc-global-search-results" onScroll={(event) => { savedScrollRef.current = event.currentTarget.scrollTop; }}>
          {keyword.length < 2 && <div className="cc-global-search-state">输入至少 2 个字开始搜索</div>}
          {keyword.length >= 2 && loading && <div className="cc-global-search-state"><LoaderCircle className="cc-spin" size={18} />正在搜索</div>}
          {keyword.length >= 2 && !loading && error && <div className="cc-global-search-state error">{error}</div>}
          {keyword.length >= 2 && !loading && !error && results.length === 0 && <div className="cc-global-search-state">没有找到相关结果</div>}
          {!loading && results.map((result, index) => (
            <button
              key={result.key}
              ref={(node) => { resultRefs.current[index] = node; }}
              type="button"
              className={`cc-global-search-result${activeResultIndex === index ? ' is-active' : ''}`}
              disabled={!result.topicId || (result.category !== 'conversation' && !result.messageId)}
              aria-current={activeResultIndex === index ? 'true' : undefined}
              onFocus={() => updateActiveResult(index)}
              onClick={() => onSelectResult(result)}
            >
              <span className="cc-global-search-result-icon">{result.category === 'artifact' ? <FileText size={18} /> : <MessageSquareText size={18} />}</span>
              <span className="cc-global-search-result-body">
                <span className="cc-global-search-result-meta"><strong>{result.source}</strong><time>{formatSearchTime(result.time)}</time></span>
                <span className="cc-global-search-result-snippet"><HighlightedSearchText text={result.snippet || '查看命中消息'} query={keyword} /></span>
                {result.attachmentName && <span className="cc-global-search-result-file"><FileText size={13} /><HighlightedSearchText text={result.attachmentName} query={keyword} /></span>}
              </span>
            </button>
          ))}
        </div>
      </section>
    </div>
  );
}
