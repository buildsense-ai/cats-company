import React, { useState } from 'react';
import { createRoot } from 'react-dom/client';
import '@fontsource-variable/inter';
import '@fontsource-variable/noto-sans-sc';
import '../views/workspace-styles';
import ChatMessage from '../widgets/chat-message';
import { api } from '../api';
import { clusterEntries, clusterGraphResult, manySearchResult, readPage, searchResult, graphResult, workingMessages, nextRevision } from './fixtures/diary-sources';
import './diary-sources-preview.css';

// Development-only preview of the actual ChatMessage integration. Every API
// call is intercepted here; this page never requests a real Diary or account.
const params = new URLSearchParams(location.search);
document.documentElement.dataset.theme = params.get('theme') || 'light';
const log = [];
window.__diaryPreviewLog = log;
api.queryDailyNotes = async (agentUid, query, { signal } = {}) => {
  log.push({ agentUid, ...query });
  await new Promise((resolve, reject) => {
    const timer = setTimeout(resolve, Number(params.get('latency') || 250));
    signal?.addEventListener('abort', () => { clearTimeout(timer); reject(new DOMException('aborted', 'AbortError')); }, { once: true });
  });
  if (params.get('refused') === '1') throw Object.assign(new Error('not found'), { status: 404 });
  const page = readPage(params.get('mismatch') === '1' ? nextRevision : query.revision);
  return { ...page, entries: clusterEntries.filter((entry) => entry.id === query.entry_id) };
};
function Preview() {
  const [mode, setMode] = useState(params.get('mode') || 'graph');
  const [theme, setTheme] = useState(document.documentElement.dataset.theme);
  const [details, setDetails] = useState(false);
  const result = mode === 'pending' ? null : mode === 'search' ? searchResult : mode === 'many' ? manySearchResult : mode === 'clusters' ? clusterGraphResult : mode === 'malformed' ? '{"contract": "memory-graph/1"...' : graphResult;
  return <main className="cc-diary-preview">
    <header><div><strong>Diary 来源</strong><p>本地合成数据 · 实际消息组件</p></div><div className="cc-diary-preview-controls">
      <label>场景 <select value={mode} onChange={(e) => setMode(e.target.value)}><option value="graph">正文与更正</option><option value="search">搜索命中</option><option value="many">多个命中</option><option value="clusters">按来源分组</option><option value="pending">正在读取</option><option value="malformed">截断结果回退</option></select></label>
      <label><input type="checkbox" checked={details} onChange={(e) => setDetails(e.target.checked)} />执行详情</label>
      <button onClick={() => { const value = theme === 'dark' ? 'light' : 'dark'; document.documentElement.dataset.theme = value; setTheme(value); }}>切换{theme === 'dark' ? '浅色' : '深色'}</button>
    </div></header>
    <section className="v3-chat-column"><div className="v3-timeline">
      <ChatMessage message={{ id: 10, from_uid: 'usr275', content: '我们之前约定的部署和回滚流程是什么？', created_at: '2026-11-15T10:00:00Z' }} isSelf senderName="你" />
      <ChatMessage key={mode} message={{ id: 11, from_uid: 'usr574', content: '', created_at: '2026-11-15T10:00:00Z' }} workingMessages={workingMessages(result, mode === 'search' || mode === 'many' ? 'search' : 'graph')} workingOnly workingComplete={mode !== 'pending'} showThinking={details} senderIsBot senderName="小八" isSelf={false} />
      {mode === 'graph' && <ChatMessage isConsecutive message={{ id: 12, from_uid: 'usr574', content: '之前的笔记要求保留可回滚版本，并补充了回滚后的数据与后台任务检查。部署前还需要确认数据库变更向后兼容。', created_at: '2026-11-15T10:00:00Z' }} senderIsBot senderName="小八" isSelf={false} />}
    </div></section>
  </main>;
}
createRoot(document.getElementById('root')).render(<Preview />);
