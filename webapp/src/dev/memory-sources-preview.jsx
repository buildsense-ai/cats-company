import React, { useState } from 'react';
import { createRoot } from 'react-dom/client';
import '@fontsource-variable/inter';
import '@fontsource-variable/noto-sans-sc';
import '../views/workspace-styles';
import ChatMessage from '../widgets/chat-message';
import { api } from '../api';
import capture from '../../../server/testdata/memory-branch.v1.json';
import { graphResult, workingMessages, readPage } from './fixtures/diary-sources';
import './diary-sources-preview.css';

// Development only. The first graph is a real scripted-runtime capture; the
// second cluster and all private reader bodies are explicitly synthetic. All
// API methods are intercepted, so this page cannot read an account or server.
const params = new URLSearchParams(location.search);
const recorded = capture.events.at(-1);
const nextRef = `ref_${'c'.repeat(64)}`;
const requests = [];
window.__memoryPreviewLog = requests;
document.documentElement.dataset.theme = params.get('theme') || 'light';
function snapshot(mode) {
  const event = structuredClone(recorded);
  if (mode !== 'recorded') event.answer_refs = [];
  if (['clusters', 'combined', 'saved'].includes(mode)) {
    event.nodes.push(...recorded.nodes.map(node => ({ ...node, id: `next-${node.id}`, ref: nextRef, reader: { ...node.reader, projection_ref: nextRef } })));
    event.edges.push(...recorded.edges.map(edge => ({ ...edge, id: `next-${edge.id}`, from: `next-${edge.from}`, to: `next-${edge.to}` })));
    event.read_refs.push(nextRef); event.injected_refs.push(nextRef);
  }
  if (['running', 'ready', 'stale', 'idle', 'finished-ready', 'quiet-live', 'empty', 'cancelled'].includes(mode)) { event.status = ['stale', 'idle', 'quiet-live'].includes(mode) ? 'running' : mode === 'finished-ready' ? 'ready' : mode; event.injected_refs = []; event.answer_refs = []; delete event.consumed_turn_id; delete event.observation_id; }
  if (['quiet-live', 'empty'].includes(mode)) { event.read_refs = []; event.nodes = []; event.edges = []; }
  if (['archive', 'archive-used'].includes(mode)) {
    // Shape of 24 of 25 production runs on 2026-11-18: archive turns, no edges.
    // Synthetic digests and offsets; no production identifiers.
    const turns = [['2026-11-18', 4], ['2026-11-17', 1], ['2026-11-13', 1]].flatMap(([day, count]) => Array.from({ length: count }, (_, i) => ({ day, offset: 1200 * (i + 1) })));
    event.nodes = turns.map((archive, i) => ({ id: `turn-${i}`, kind: 'archive_turn', label: '会话摘录', ref: `local:${String(i).repeat(64)}`, reader: { kind: 'archive_turn', archive: { ...archive, digest: 'd'.repeat(64) } } }));
    event.edges = []; event.read_refs = event.nodes.map(node => node.ref);
    if (mode === 'archive') { event.status = 'deferred'; event.injected_refs = []; event.answer_refs = []; delete event.consumed_turn_id; delete event.observation_id; }
    else { event.status = 'answered'; event.injected_refs = event.read_refs.slice(0, 2); event.answer_refs = event.read_refs.slice(0, 1); }
  }
  // Most common production shape (23 of 54 runs, 2026-11-18): the reply
  // finished before Branch read anything; the channel closed on 'deferred'.
  if (mode === 'missed') { event.status = 'deferred'; event.nodes = []; event.edges = []; event.read_refs = []; event.injected_refs = []; event.answer_refs = []; delete event.consumed_turn_id; delete event.observation_id; }
  if (mode === 'late') event.origin_turn_id = 'previous-turn';
  if (mode === 'unknown') event.schema = 'catsco.memory_branch.v99';
  return event;
}
api.readMemorySource = async (agentUid, query, { signal } = {}) => {
  requests.push({ agentUid, ...query });
  await new Promise((resolve, reject) => {
    const timer = setTimeout(resolve, 140);
    signal?.addEventListener('abort', () => { clearTimeout(timer); reject(new DOMException('aborted', 'AbortError')); }, { once: true });
  });
  if (params.get('refused') === '1') throw Object.assign(new Error('unavailable'), { status: 404 });
  if (query.kind === 'source') return { source: { anchor: query.anchor, status: 'read', coverage: 'complete', truncated: false, text: '原始会话记录：先确认数据库兼容性，再执行发布或回滚。' } };
  const nodes = recorded.nodes.map((node, index) => ({ id: node.id, kind: node.kind, text: node.kind === 'condition' ? '仅当旧版本仍能读取当前数据库时，才能执行回滚。' : node.kind === 'omission' ? '外部系统的回滚行为尚未验证，不应推断它会同步恢复。' : index === 0 ? '发布前保留上一版本；回滚后检查数据、队列和后台任务。' : `已核验的关联记录 ${index}：执行发布检查时保留人工确认。` }));
  return { ref: params.get('mismatch') === '1' ? `ref_${'f'.repeat(64)}` : query.projection_ref, revision: 'a'.repeat(64), document: {
    goal: '部署与回滚记录（合成示例）', nodes, edges: recorded.edges, conditions: ['以上条件来自这个来源范围，不应自动套用到其他记忆。'], omissions: ['未评估外部系统的状态。'], overall_coverage: 'partial', gap_acknowledgement: '仅返回这次已读取的局部记忆，不代表完整历史。',
  } };
};
api.queryDailyNotes = async (_agentUid, query) => readPage(query.revision);

function Preview() {
  const [mode, setMode] = useState(params.get('mode') || 'clusters');
  const [details, setDetails] = useState(false), [theme, setTheme] = useState(document.documentElement.dataset.theme);
  const [complete, setComplete] = useState(false);
  const event = snapshot(mode);
  const terminalState = ['failed', 'cancelled'].find(state => params.get('execution') === state);
  const inProgress = ['running', 'ready', 'quiet-live'].includes(mode) && !complete;
  const live = [{ id: 31, from_uid: 'usr574', type: 'memory_branch', content: event }];
  const combined = mode === 'combined' ? [...workingMessages(graphResult, 'graph'), ...live] : live;
  return <main className="cc-diary-preview">
    <header><div><strong>记忆探索来源</strong><p>本地合成数据 · 实际消息组件 · 不读取线上账号</p></div><div className="cc-diary-preview-controls">
      <label>场景 <select value={mode} onChange={event => setMode(event.target.value)}>{[['clusters', '两个来源'], ['combined', '日记与记忆探索'], ['recorded', '真实运行时录制'], ['saved', '历史回放'], ['running', '正在探索'], ['ready', '已读，尚未注入'], ['stale', '完成后缺少 Branch 终态'], ['idle', '历史缺少 Branch 终态'], ['finished-ready', '已完成，结果未采纳'], ['quiet-live', '等待 Branch 更新'], ['empty', '无补充结果'], ['cancelled', '已取消'], ['late', '上一轮晚到'], ['missed', '生产形态：回答前未读到'], ['archive', '生产形态：会话摘录未赶上'], ['archive-used', '生产形态：部分采用'], ['unknown', '未知协议回退']].map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select></label>
      {mode === 'quiet-live' && <button type="button" data-preview="finish" onClick={() => setComplete(true)}>结束回合</button>}
      <label><input type="checkbox" checked={details} onChange={event => setDetails(event.target.checked)} />执行详情</label>
      <button type="button" onClick={() => { const next = theme === 'dark' ? 'light' : 'dark'; document.documentElement.dataset.theme = next; setTheme(next); }}>切换{theme === 'dark' ? '浅色' : '深色'}</button>
    </div></header>
    <section className="v3-chat-column"><div className="v3-timeline">
      <ChatMessage message={{ id: 10, from_uid: 'usr275', content: '我们之前约定的部署和回滚流程是什么？' }} isSelf senderName="你" />
      <ChatMessage key={mode} message={{ id: 11, from_uid: 'usr574', content: '', ...(mode === 'saved' ? { content_blocks: [{ type: 'memory_branch', payload: event }] } : {}) }}
        workingMessages={mode === 'saved' ? [] : combined} workingOnly workingComplete={!inProgress} executionState={mode === 'idle' ? 'idle' : terminalState} showThinking={details} senderIsBot senderName="小八" />
      {!inProgress && mode !== 'unknown' && <ChatMessage isConsecutive message={{ id: 12, from_uid: 'usr574', content: mode === 'recorded' ? `The next request received the late branch result. ${recorded.injected_refs[0]}` : '先确认数据库向后兼容，保留上一版本。回滚后检查数据和后台任务；外部系统的回滚行为仍需单独核验。' }} senderIsBot senderName="小八" />}
    </div></section>
  </main>;
}
createRoot(document.getElementById('root')).render(<Preview />);
