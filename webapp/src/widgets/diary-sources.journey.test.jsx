import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { mkdirSync, writeFileSync } from 'node:fs';
import { resolve } from 'node:path';
import ChatMessage from './chat-message';
import { setToken } from '../auth-session';
import { anchor, documentId, revision, nextRevision, entries, readPage, searchResult, graphResult, clusterGraphResult, workingMessages } from '../dev/fixtures/diary-sources';

// Journey failure modes, declared before implementation:
// D1 search hits look like read/verified evidence, or eagerly fetch private bodies.
// D2 opening an old source requests head, or displays a mismatched revision/entry.
// D3 an incomplete graph, malformed JSON, or foreign tool result becomes a source.
// D4 changing selection while reading lets an older response overwrite it.
// D5 refused/failed reads leak tool body text, have no retry, or close chat.
// D6 stored content_blocks and hidden process details lose source cards;
//    closing the reader must restore focus and Escape must close only it.
// D8 specializing a tool must not drop the assistant's preceding progress text.
// D7 Diary and Branch are two entry points for one question ("what memory
//    informed this answer?"); Diary hits must sit in the shared memory list,
//    not two clicks deep inside a tool step, and never as an unlabeled graph.
// D9 the same entry returned by a search and a later read is one row, keeping
//    the stronger (read) disclosure; long lists stay scannable.
// D10 the memory list mounts only when opened; collapsing it releases the
//     reader and aborts a pending private read. Opening never reads bodies.
// D11 a pending→completed result must not auto-open anything or reveal
//     reasoning hidden by the viewer's setting.
// D12 tool steps name what happened in product language; unknown tools keep
//     their real name, failures are visible, raw output stays one click deep.
// Origin labels (2026-11-19 review: "is this part from the branch, not the
// main agent's recall?"):
// O1 a row does not say who fetched it: Diary rows the agent read through
//    its own tool calls and Branch rows from background recall share
//    content-type headings, or show no heading when only one origin exists;
// O2 one count adds the agent's Diary reads to Branch injections, so the
//    number cannot be traced to either origin;
// O3 a Branch state (deferred, ready, carried over) is shown above the Diary
//    rows, or a row sits under the other origin's heading.
// The journey uses the real ChatMessage, reader, Markdown and authenticated API
// client; only the HTTP boundary is a fixture. Browser screenshots supplement it.
const evidence = [];
let container, root, requests;
const jsonResponse = (body, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
const settle = async () => { await act(async () => { for (let i = 0; i < 12; i += 1) await Promise.resolve(); }); };
const click = async (el) => { expect(el).toBeTruthy(); await act(async () => el.click()); await settle(); };
const sourceButtons = () => [...container.querySelectorAll('.cc-memory-source')];
const reader = () => document.querySelector('[aria-label="日记引用详情"]');
const render = async (messages = workingMessages(), extra = {}, expand = true) => {
  await act(async () => root.render(<ChatMessage message={{ id: 3, from_uid: 'usr574', content: '', created_at: '2026-11-15T10:00:00Z' }} workingMessages={messages} workingOnly workingComplete senderIsBot senderName="小八" isSelf={false} showThinking={false} {...extra} />));
  if (expand) {
    const memory = container.querySelector('.cc-memory-toggle[aria-expanded="false"]');
    if (memory) await click(memory);
  }
};

beforeEach(() => {
  container = document.createElement('div'); document.body.append(container); root = createRoot(container);
  setToken('diary-journey-owner'); requests = [];
  vi.stubGlobal('ResizeObserver', class { observe() {} disconnect() {} });
  vi.stubGlobal('fetch', vi.fn(async (url, options) => {
    requests.push({ url: String(url), body: JSON.parse(options.body), auth: options.headers.Authorization });
    const q = JSON.parse(options.body);
    return jsonResponse({ ...readPage(q.revision), entries: entries.filter((e) => e.id === q.entry_id) });
  }));
});
afterEach(async () => { await act(async () => root.unmount()); container.remove(); setToken(null); vi.unstubAllGlobals(); });
afterAll(() => {
  const dir = process.env.DIARY_ARTIFACT_DIR || '/tmp/catsco-diary-sources';
  mkdirSync(dir, { recursive: true });
  writeFileSync(resolve(dir, 'ui-journey.json'), JSON.stringify({ fixture: 'synthetic XiaoBa #642', evidence }, null, 2));
});

it('D1/D2: search → exact version read → keyboard close, without claiming a citation', async () => {
  await render(workingMessages(searchResult, 'search'));
  expect(sourceButtons()).toHaveLength(2);
  expect(container.textContent).toContain('搜索结果');
  expect(container.textContent).toContain('还有更多结果');
  expect(container.textContent).not.toContain('回答引用');
  expect(requests).toHaveLength(0);
  const button = sourceButtons()[0]; button.focus(); await click(button);
  expect(requests[0].body).toEqual({ operation: 'read', document_id: documentId, entry_id: entries[0].id, revision });
  expect(requests[0].url).toContain('/api/agents/574/daily-notes');
  expect(requests[0].auth).toBe('Bearer diary-journey-owner');
  expect(reader().textContent).toContain('数据库变更向后兼容');
  expect(reader().textContent).toContain('引用时版本');
  await act(async () => document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true })));
  expect(reader()).toBeNull(); expect(document.activeElement).toBe(button);
  expect(container.querySelector('.cc-memory-toggle').getAttribute('aria-expanded')).toBe('true');
  evidence.push({ journey: 'search/read/close', request: requests[0].body, body: 'exact version', restoredFocus: true });
});

it('D6: saved content blocks list the read notes with draft status; relations open in the reader', async () => {
  const blocks = [{ type: 'tool_use', id: 'diary-call', name: 'catslog_knowledge_recall', input: { action: 'graph', seeds: [anchor()] } }, { type: 'tool_result', tool_use_id: 'diary-call', content: JSON.stringify(graphResult) }];
  await render([], { message: { id: 4, from_uid: 'usr574', content: '', content_blocks: blocks } });
  expect(sourceButtons()).toHaveLength(2);
  expect(container.textContent).toContain('未审定');
  expect(container.textContent).not.toContain('搜索结果');
  await click(sourceButtons()[1]);
  expect(reader().textContent).toContain('回滚完成后');
  expect(reader().textContent).toContain('同次读取的关联笔记');
  expect(reader().textContent).toContain('更正');
  evidence.push({ journey: 'saved read', rows: 2, correctionInReader: true, draft: true });
});

it('D2: a different returned revision is refused, never displayed as the cited source', async () => {
  fetch.mockResolvedValue(jsonResponse(readPage(nextRevision)));
  await render(); await click(sourceButtons()[0]);
  expect(reader().textContent).toContain('返回版本与引用不一致');
  expect(reader().textContent).not.toContain('数据库变更向后兼容');
  evidence.push({ journey: 'revision mismatch', disclosedBody: false });
});

it('D3: malformed/incomplete/foreign results retain raw tools without clickable sources', async () => {
  for (const result of ['{"contract":', { ...graphResult, closure_complete: false }]) {
    await render(workingMessages(result), { showThinking: true });
    expect(container.querySelector('.cc-memory-toggle')).toBeNull();
    expect(sourceButtons()).toHaveLength(0);
    expect(container.querySelector('.v3-working-toggle')).toBeTruthy();
  }
  const foreign = workingMessages(); foreign[1].metadata.tool_use_id = 'different-call';
  await render(foreign, { showThinking: true });
  expect(sourceButtons()).toHaveLength(0);
  evidence.push({ journey: 'invalid/partial/foreign', rows: 0 });
});

it('D4: rapid selection cancels the old read and ignores even a late successful response', async () => {
  let resolveFirst;
  fetch.mockImplementationOnce((url, options) => {
    requests.push({ body: JSON.parse(options.body), signal: options.signal });
    return new Promise((resolveRequest) => { resolveFirst = resolveRequest; });
  });
  await render(); await click(sourceButtons()[0]); await click(sourceButtons()[1]);
  resolveFirst(jsonResponse({ ...readPage(), entries: [entries[0]] })); await settle();
  expect(requests[0].signal.aborted).toBe(true);
  expect(reader().textContent).toContain('回滚完成后');
  expect(reader().querySelector('.cc-diary-reader-body').textContent).not.toContain('数据库变更向后兼容');
  evidence.push({ journey: 'selection race', firstAborted: true, selected: entries[1].id });
});

it('D7: Diary and Branch share one memory list; no graph, no tool-step detour', async () => {
  const ref = `ref_${'a'.repeat(64)}`;
  const branch = { schema: 'catsco.memory_branch.v1', branch_run_id: 'memory-mixed', origin_turn_id: 'turn-1', sequence: 2, status: 'injected',
    consumed_turn_id: 'turn-1', observation_id: 'obs-1', read_refs: [ref], injected_refs: [ref], answer_refs: [], edges: [],
    nodes: [{ id: 'n1', kind: 'assertion', label: '记忆断言', ref, reader: { kind: 'projection', projection_ref: ref } }] };
  await render([...workingMessages(), { id: 9, from_uid: 'usr574', type: 'memory_branch', content: branch }]);
  expect(container.querySelectorAll('.cc-memory-toggle')).toHaveLength(1);
  expect(container.querySelectorAll('.cc-memory-source.is-diary')).toHaveLength(2);
  expect(container.querySelectorAll('.cc-memory-source.is-branch')).toHaveLength(1);
  // O1/O3: each origin has its own heading, rows and state.
  const origins = [...container.querySelectorAll('.cc-memory-origin')].map(section => ({
    heading: section.querySelector('.cc-memory-group').textContent,
    diary: section.querySelectorAll('.cc-memory-source.is-diary').length,
    branch: section.querySelectorAll('.cc-memory-source.is-branch').length }));
  expect(origins).toEqual([
    { heading: '小八主动查阅日记 · 读取 2 条', diary: 2, branch: 0 },
    { heading: '后台自动回忆历史会话 · 1 条用于回答', diary: 0, branch: 1 }]);
  // O2: no number that adds both origins together.
  expect(container.textContent).not.toMatch(/回答参考了 \d/);
  expect(container.querySelector('canvas, .cc-daily-notes-network')).toBeNull();
  // Hidden process details hide every step; sources stay reachable here.
  expect(container.querySelector('.v3-working-toggle')).toBeNull();
  expect(requests).toHaveLength(0);
  evidence.push({ journey: 'one memory list', diary: 2, branch: 1, canvases: 0 });
});

it('D9: duplicate hits collapse to the read row; long lists reveal the rest on demand', async () => {
  const second = workingMessages(searchResult, 'search').map(message => ({ ...message, id: message.id + 10, metadata: message.type === 'tool_use' ? { ...message.metadata, id: 'diary-second' } : { ...message.metadata, tool_use_id: 'diary-second' } }));
  await render([...second, ...workingMessages()]);
  expect(sourceButtons()).toHaveLength(2);
  expect(container.textContent).not.toContain('搜索结果');
  await render(workingMessages(clusterGraphResult));
  expect(sourceButtons()).toHaveLength(6);
  const more = [...container.querySelectorAll('button')].find(button => button.textContent.includes('再显示 6 条'));
  await click(more);
  expect(sourceButtons()).toHaveLength(12);
  expect(requests).toHaveLength(0);
  await click(sourceButtons()[0]);
  expect(requests[0].body.revision).toBe(revision);
  evidence.push({ journey: 'dedupe and long list', deduped: 2, initial: 6, total: 12, eagerReads: 0 });
});

it('D8: the assistant progress attached to a Diary read remains in the conversation', async () => {
  const messages = [{ id: 0, from_uid: 'usr574', type: 'text', content: 'AI文本:我先核对之前的部署笔记。' }, ...workingMessages()];
  await render(messages);
  expect(container.textContent).toContain('我先核对之前的部署笔记。');
  expect(sourceButtons()).toHaveLength(2);
  evidence.push({ journey: 'attached progress', preserved: true });
});

it('D5: authorization refusal never reveals the tool body; failed reads can retry', async () => {
  fetch.mockResolvedValueOnce(jsonResponse({ error: 'daily_notes_not_found' }, 404));
  await render(); await click(sourceButtons()[0]);
  expect(reader().textContent).toContain('无权查看或该笔记已不可用');
  expect(reader().textContent).not.toContain('数据库变更向后兼容');
  const retry = [...reader().querySelectorAll('button')].find((b) => b.textContent.includes('重试'));
  await click(retry);
  expect(reader().textContent).toContain('数据库变更向后兼容');
  evidence.push({ journey: 'refusal/retry', refusedBody: false, retry: 'success' });
});

it('D10: the memory list mounts only when opened and never reads bodies by itself', async () => {
  await render(workingMessages(), {}, false);
  expect(sourceButtons()).toHaveLength(0);
  const memory = container.querySelector('.cc-memory-toggle');
  expect(memory.textContent).toContain('2');
  await click(memory);
  expect(sourceButtons()).toHaveLength(2);
  expect(requests).toHaveLength(0);
  await click(memory);
  expect(sourceButtons()).toHaveLength(0);
  evidence.push({ journey: 'lazy memory list', eagerReads: 0 });
});

it('D11: a pending Diary call shows progress, then stays collapsed and keeps reasoning hidden', async () => {
  const pending = [{ id: 0, from_uid: 'usr574', type: 'thinking', content: 'PRIVATE_REASONING_SENTINEL' }, workingMessages()[0]];
  await render(pending, { workingComplete: false }, false);
  expect(container.querySelector('.cc-memory-status')?.textContent).toContain('正在查阅日记');
  await render([pending[0], ...workingMessages()], {}, false);
  expect(container.querySelector('.cc-memory-toggle').getAttribute('aria-expanded')).toBe('false');
  expect(container.querySelector('.v3-working-toggle')).toBeNull();
  expect(container.textContent).not.toContain('PRIVATE_REASONING_SENTINEL');
  await click(container.querySelector('.cc-memory-toggle'));
  expect(sourceButtons()).toHaveLength(2);
  expect(container.textContent).not.toContain('PRIVATE_REASONING_SENTINEL');
  evidence.push({ journey: 'streaming completion', staysCollapsed: true, hiddenReasoningDisclosed: false });
});

it('D12: tool steps read as actions; raw name and output stay one click deep; failures show', async () => {
  const failing = [{ id: 20, from_uid: 'usr574', type: 'tool_use', content: 'execute_shell', metadata: { id: 'shell-1', input: { command: 'npm test' } } },
    { id: 21, from_uid: 'usr574', type: 'tool_result', content: 'exit 1', metadata: { tool_use_id: 'shell-1', is_error: true } }];
  await render([...workingMessages(searchResult, 'search'), ...failing], { showThinking: true }, false);
  await click(container.querySelector('.v3-working-toggle'));
  const steps = [...container.querySelectorAll('.v3-wpi-tool-header')];
  expect(steps[0].querySelector('.v3-wpi-tool-name').textContent).toBe('搜索日记');
  expect(steps[0].textContent).toContain('部署回滚');
  expect(steps[0].textContent).not.toContain('catslog_knowledge_recall');
  expect(steps[0].textContent).not.toContain('字符');
  expect(steps[1].querySelector('.v3-wpi-tool-name').textContent).toBe('运行命令');
  expect(steps[1].textContent).toContain('失败');
  await click(steps[0]);
  expect(container.querySelector('.v3-wpi-tool-result').textContent).toContain('catslog_knowledge_recall');
  expect(container.querySelector('.cc-diary-tool, canvas')).toBeNull();
  evidence.push({ journey: 'readable tool steps', label: '搜索日记', failureVisible: true });
});

it('D10: collapsing the memory list aborts an open private read and removes its portal', async () => {
  let finish;
  fetch.mockImplementationOnce((url, options) => {
    requests.push({ signal: options.signal });
    return new Promise(resolve => { finish = resolve; });
  });
  await render(); await click(sourceButtons()[0]);
  expect(reader()).toBeTruthy();
  await click(container.querySelector('.cc-memory-toggle'));
  expect(requests[0].signal.aborted).toBe(true);
  expect(reader()).toBeNull();
  finish(jsonResponse(readPage())); await settle();
  expect(reader()).toBeNull();
  evidence.push({ journey: 'collapse cancels read', aborted: true, stalePortal: false });
});
