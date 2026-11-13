import React, { useEffect, useState } from 'react';
import { createRoot } from 'react-dom/client';
import '@fontsource-variable/inter';
import '@fontsource-variable/noto-sans-sc';
import '../views/workspace-styles';
import { api } from '../api';
import { FeedbackProvider } from '../components/feedback-system';
import AgentStoreModal from '../widgets/agent-store-modal';

// The Agent manage panel opened on its Daily Notes tab. Reads are local:
// synthetic notes by default, or ?fixture=<url> for a captured set of
// CatsLog diaries pages ({ pages: [...], reads?: {document_id: page} }).
// ?busy=1 refuses the second page once with 429 to exercise the backoff.
// ?days=<n> scales the synthetic set (about 4.5 notes a day) for load tests;
// ?pageDays=<n> sets how many days each graph page carries (default 8).
// ?skew=1 makes the set look like a long-lived Agent: ten notes a day spread
// over sources by a power law (a few busy group chats, many one-off
// sessions), continues/corrects within a source and a few links across.

const AGENT_UID = 574;
const params = new URLSearchParams(location.search);
const latency = Number(params.get('latency') || 400);
const days = Number(params.get('days') || 20);
const pageDays = Number(params.get('pageDays') || 8);
const skew = params.get('skew') === '1';

function syntheticFixture() {
  const titles = ['部署回滚与验证', '索引重建的根因', 'CLI 认证改用 cookie', '群聊通知去重', '会话恢复策略', '构建缓存命中率', '权限模型调整', '每日巡检结果'];
  const pages = [];
  const reads = {};
  let page = { nodes: [], edges: [], lane: 'diaries', units: 0, exhausted: false };
  let previous = null;
  const groups = Math.max(4, Math.round(days / 5));
  let seed = 7;
  const random = () => ((seed = (seed * 1103515245 + 12345) % 2147483648) / 2147483648);
  const sources = Math.max(4, Math.round(days * 1.2));
  const lastOf = new Map();
  for (let d = 0; d < days; d += 1) {
    const day = new Date(Date.UTC(2026, 9, 31 - d)).toISOString().slice(0, 10);
    const documentId = `akd-${String(d).padStart(24, '0')}`;
    const entries = [];
    const sessionOf = (index) => {
      const session = { kind: 'session_query', id: `ref_${d}_${index}`, session_id: `cc_group:grp_${5100 + index}`, session_type: 'catscompany' };
      page.nodes.push({ key: JSON.stringify(session), kind: 'session_query', label: session.id, status: 'referenced', day, anchor: session });
      return JSON.stringify(session);
    };
    const daySession = skew ? null : sessionOf(d % groups);
    for (let e = 0; e < (skew ? 10 : 3 + (d % 4)); e += 1) {
      const id = `ake-${String(d * 10 + e).padStart(24, '0')}`;
      const anchor = { kind: 'knowledge_entry', id, document_id: documentId, revision: `akr-${d}` };
      const key = JSON.stringify(anchor);
      const title = `${titles[(d + e) % titles.length]}（${day.slice(5)}）`;
      page.nodes.push({ key, kind: 'knowledge_entry', label: title, status: 'draft', day, anchor });
      if (skew) {
        const source = Math.min(sources - 1, Math.floor(sources ** random()) - 1);
        page.edges.push({ from: key, to: sessionOf(source), kind: 'cites' });
        if (lastOf.has(source) && random() < 0.6) page.edges.push({ from: key, to: lastOf.get(source), kind: random() < 0.15 ? 'corrects' : 'continues' });
        if (previous && e === 0 && random() < 0.5) page.edges.push({ from: key, to: previous, kind: 'related' });
        lastOf.set(source, key);
      } else {
        page.edges.push({ from: key, to: daySession, kind: 'cites' });
        if (previous && e === 0) page.edges.push({ from: key, to: previous, kind: d % 5 === 0 ? 'corrects' : 'continues' });
      }
      if (e === 0) previous = key;
      entries.push({ id, title, text: `${title}。\n\n这是本地预览生成的正文，用于检查阅读排版、搜索高亮与关联跳转。**关键结论**：变更已验证，剩余问题记录在后续笔记中。`, status: 'draft' });
    }
    reads[documentId] = { document_id: documentId, day, entries, exhausted: true };
    page.units += 1;
    if (page.units === pageDays || d === days - 1) {
      pages.push(page);
      page = { nodes: [], edges: [], lane: 'diaries', units: 0, exhausted: false };
    }
  }
  pages.forEach((p, i) => { p.exhausted = i === pages.length - 1; if (!p.exhausted) p.next_cursor = `c${i + 1}`; });
  return { pages, reads };
}

async function loadFixture() {
  const url = params.get('fixture');
  if (!url) return syntheticFixture();
  const data = await fetch(url).then((response) => response.json());
  const reads = { ...(data.reads || {}) };
  if (data.read?.document_id) reads[data.read.document_id] = data.read;
  return { pages: data.pages, reads };
}

const fixture = loadFixture();
// Main-thread tasks over 50 ms, recorded from first load for the E2E probe.
window.__dailyNotesLongTasks = [];
new PerformanceObserver((list) => {
  for (const task of list.getEntries()) window.__dailyNotesLongTasks.push({ at: Math.round(task.startTime), ms: Math.round(task.duration) });
}).observe({ type: 'longtask', buffered: true });
const log = [];
window.__dailyNotesLog = log;
let refusedBusy = false;
const sleep = (ms, signal) => new Promise((resolve, reject) => {
  const timer = setTimeout(resolve, ms);
  signal?.addEventListener('abort', () => { clearTimeout(timer); reject(new DOMException('aborted', 'AbortError')); }, { once: true });
});

api.queryDailyNotes = async (agentUid, query, options = {}) => {
  const { pages, reads } = await fixture;
  const started = performance.now();
  await sleep(latency, options.signal);
  const entry = { at: Math.round(started), agentUid, ...query };
  log.push(entry);
  if (query.operation === 'graph') {
    const index = query.cursor ? Number(String(query.cursor).replace(/^c/, '')) : 0;
    if (params.get('busy') && index === 1 && !refusedBusy) {
      refusedBusy = true;
      entry.status = 429;
      throw Object.assign(new Error('busy'), { status: 429 });
    }
    entry.status = 200;
    const page = pages[index];
    const next = index + 1 < pages.length ? `c${index + 1}` : '';
    return { ...page, next_cursor: next, exhausted: !next };
  }
  const read = reads[query.document_id];
  if (read) { entry.status = 200; return read; }
  // A captured graph without a captured read: compose a page from titles.
  const entries = pages.flatMap((p) => p.nodes)
    .filter((node) => node.kind === 'knowledge_entry' && node.status !== 'referenced' && node.anchor?.document_id === query.document_id)
    .map((node) => ({ id: node.anchor.id, title: node.label, text: `（预览未捕获这一天的正文）${node.label}`, status: node.status }));
  entry.status = 200;
  return { document_id: query.document_id, entries, exhausted: true };
};
api.getMyBots = async () => ({ bots: [{ id: AGENT_UID, uid: AGENT_UID, username: 'pi', display_name: 'Pi 助手', is_owner: true, relation: 'owner' }] });
api.getAgents = async () => ({ agents: [] });
api.getFriends = async () => ({ friends: [] });
api.getCloudWorkers = async () => ({ workers: [] });
api.getCloudWorkerMeta = async () => ({});
api.getBotInviteCode = async () => ({});
api.getBotDefinitionSkills = async () => ({ skills: [] });
api.getLocalSkills = async () => ({ skills: [] });
api.getAgentEntries = async () => ({ entries: [] });
api.getBotFriends = async () => ({ friends: [] });

function DailyNotesPreview() {
  const [theme, setTheme] = useState(params.get('theme') || 'light');
  useEffect(() => { document.documentElement.dataset.theme = theme; }, [theme]);
  // The modal settles on its first section after loading; open the tab once it has.
  useEffect(() => {
    const timer = setInterval(() => {
      if (document.querySelector('.cc-daily-notes')) { clearInterval(timer); return; }
      [...document.querySelectorAll('.cc-agent-manage-section-trigger')].find((button) => button.textContent.includes('Daily Notes'))?.click();
    }, 250);
    return () => clearInterval(timer);
  }, []);
  return (
    <FeedbackProvider>
      <button type="button" style={{ position: 'fixed', right: 12, top: 12, zIndex: 10000 }} onClick={() => setTheme((value) => (value === 'dark' ? 'light' : 'dark'))}>
        {theme === 'dark' ? '浅色' : '深色'}
      </button>
      <AgentStoreModal initialAgentId={AGENT_UID} onClose={() => {}} user={{ uid: 275, username: 'preview' }} />
    </FeedbackProvider>
  );
}

createRoot(document.getElementById('root')).render(<DailyNotesPreview />);
