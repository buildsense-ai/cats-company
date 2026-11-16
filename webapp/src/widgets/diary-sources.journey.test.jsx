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
// D7 multiple hits are actual interactive graph nodes; only returned Diary
//    relations may connect them, and a search page never invents edges.
// D9 the graph uses the Agent Settings Canvas renderer and source-cluster
//    layout; missing source metadata must never become guessed session groups.
// D10 Diary graphs mount only after both the process and its tool step open;
//     closing either must release the graph/reader and abort pending reads.
// D11 a pending→completed result must stay collapsed; selecting a step opens
//     only that call, including when reasoning visibility is disabled.
// The journey uses the real ChatMessage, reader, Markdown and authenticated API
// client; only the HTTP boundary is a fixture. Browser screenshots supplement it.
const evidence = [];
let container, root, requests;
const jsonResponse = (body, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
const settle = async () => { await act(async () => { for (let i = 0; i < 12; i += 1) await Promise.resolve(); }); };
const click = async (el) => { expect(el).toBeTruthy(); await act(async () => el.click()); await settle(); };
const sourceButtons = () => [...container.querySelectorAll('.cc-diary-source')];
const reader = () => document.querySelector('[aria-label="日记引用详情"]');
const render = async (messages = workingMessages(), extra = {}, expand = true) => {
  await act(async () => root.render(<ChatMessage message={{ id: 3, from_uid: 'usr574', content: '', created_at: '2026-11-15T10:00:00Z' }} workingMessages={messages} workingOnly workingComplete senderIsBot senderName="小八" isSelf={false} showThinking={false} {...extra} />));
  if (expand) {
    const process = container.querySelector('.v3-working-toggle[aria-expanded="false"]');
    if (process) await click(process);
    const step = [...container.querySelectorAll('.v3-wpi-tool-header.is-toggle[aria-expanded="false"]')].find(button => button.textContent.includes('catslog_knowledge_recall'));
    if (step) await click(step);
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
  expect(container.textContent).toContain('找到 2 条笔记');
  expect(container.textContent).toContain('仅搜索命中');
  expect(container.textContent).toContain('还有更多结果');
  expect(container.textContent).not.toContain('已引用');
  expect(requests).toHaveLength(0);
  const button = sourceButtons()[0]; button.focus(); await click(button);
  expect(requests[0].body).toEqual({ operation: 'read', document_id: documentId, entry_id: entries[0].id, revision });
  expect(requests[0].url).toContain('/api/agents/574/daily-notes');
  expect(requests[0].auth).toBe('Bearer diary-journey-owner');
  expect(reader().textContent).toContain('数据库变更向后兼容');
  expect(reader().textContent).toContain('引用时版本');
  await act(async () => document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true })));
  expect(reader()).toBeNull(); expect(document.activeElement).toBe(button);
  expect(container.querySelector('.v3-working-toggle').getAttribute('aria-expanded')).toBe('true');
  evidence.push({ journey: 'search/read/close', request: requests[0].body, body: 'exact version', restoredFocus: true });
});

it('D6: saved content blocks show a complete graph with corrections and draft status', async () => {
  const blocks = [{ type: 'tool_use', id: 'diary-call', name: 'catslog_knowledge_recall', input: { action: 'graph', seeds: [anchor()] } }, { type: 'tool_result', tool_use_id: 'diary-call', content: JSON.stringify(graphResult) }];
  await render([], { message: { id: 4, from_uid: 'usr574', content: '', content_blocks: blocks } });
  expect(sourceButtons()).toHaveLength(2);
  expect(container.textContent).toContain('已返回 2 条笔记正文');
  expect(container.textContent).toContain('更正');
  expect(container.textContent).toContain('未审定');
  await click(sourceButtons()[1]);
  expect(reader().textContent).toContain('回滚完成后');
  expect(reader().textContent).toContain('同次读取的关联笔记');
  evidence.push({ journey: 'saved graph', cards: 2, correction: true, draft: true });
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
    expect(sourceButtons()).toHaveLength(0);
  }
  const foreign = workingMessages(); foreign[1].metadata.tool_use_id = 'different-call';
  await render(foreign, { showThinking: true });
  expect(sourceButtons()).toHaveLength(0);
  evidence.push({ journey: 'invalid/partial/foreign', cards: 0 });
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

it('D7: graph nodes preserve returned relations; independent search hits have no invented edges', async () => {
  await render();
  expect(container.querySelector('[aria-label="日记命中图谱"]')).toBeTruthy();
  expect(container.querySelector('.cc-daily-notes-network canvas')).toBeTruthy();
  expect(container.querySelectorAll('.cc-diary-node')).toHaveLength(2);
  expect(container.querySelectorAll('.cc-daily-notes-network-controls button')).toHaveLength(3);
  expect(container.querySelector('canvas').getAttribute('aria-label')).toContain('1 组');
  await click(container.querySelector('.cc-diary-node'));
  expect(reader().textContent).toContain('数据库变更向后兼容');
  await render(workingMessages(searchResult, 'search'));
  expect(container.querySelectorAll('.cc-diary-node')).toHaveLength(2);
  expect(container.querySelector('canvas').getAttribute('aria-label')).toContain('2 条笔记');
  expect(container.textContent).toContain('未返回关联关系');
  evidence.push({ journey: 'interactive graph', nodes: 2, returnedEdges: 1, searchEdges: 0 });
});

it('D9: returned source anchors form the same session clusters as Agent Settings', async () => {
  await render(workingMessages(clusterGraphResult));
  expect(container.querySelector('canvas').getAttribute('aria-label')).toContain('12 条笔记，按来源会话分为 2 组');
  expect(sourceButtons()).toHaveLength(12);
  expect(requests).toHaveLength(0);
  await click(sourceButtons()[0]);
  expect(requests[0].body.revision).toBe(revision);
  expect(reader().textContent).toContain('数据库变更向后兼容');
  evidence.push({ journey: 'Agent Settings source clusters', notes: 12, sessionRegions: 2, eagerReads: 0 });
});

it('D8: the assistant progress attached to a Diary read remains in the conversation', async () => {
  const messages = [{ id: 0, from_uid: 'usr574', type: 'text', content: 'AI文本:我先核对之前的部署笔记。' }, ...workingMessages()];
  await render(messages);
  expect(container.textContent).toContain('我先核对之前的部署笔记。');
  expect(container.querySelectorAll('.cc-diary-node')).toHaveLength(2);
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

it('D10: process → selected Diary shell → graph; collapsing unmounts it without reading bodies', async () => {
  await render(workingMessages(), {}, false);
  expect(container.querySelector('.cc-diary-tool')).toBeNull();
  expect(container.querySelector('canvas')).toBeNull();
  expect(requests).toHaveLength(0);
  const process = container.querySelector('.v3-working-toggle');
  await click(process);
  expect(container.querySelector('canvas')).toBeNull();
  const step = container.querySelector('.v3-wpi-tool-header.is-toggle');
  expect(step.getAttribute('aria-expanded')).toBe('false');
  await click(step);
  expect(sourceButtons()).toHaveLength(2);
  expect(container.querySelector('canvas')).toBeTruthy();
  expect(requests).toHaveLength(0);
  await click(step);
  expect(container.querySelector('canvas')).toBeNull();
  await click(step); await click(process);
  expect(container.querySelector('.cc-diary-tool')).toBeNull();
  evidence.push({ journey: 'two-stage disclosure', initialCanvases: 0, expandedProcessCanvases: 0, selectedToolCanvases: 1, eagerReads: 0 });
});

it('D11: a pending Diary result stays collapsed when it completes and hides attached reasoning', async () => {
  const pending = [{ id: 0, from_uid: 'usr574', type: 'thinking', content: 'PRIVATE_REASONING_SENTINEL' }, workingMessages()[0]];
  await render(pending, {}, false);
  await click(container.querySelector('.v3-working-toggle'));
  expect(container.textContent).not.toContain('PRIVATE_REASONING_SENTINEL');
  expect(container.querySelector('canvas')).toBeNull();
  await render([pending[0], ...workingMessages()], {}, false);
  const step = container.querySelector('.v3-wpi-tool-header.is-toggle');
  expect(step.getAttribute('aria-expanded')).toBe('false');
  expect(container.querySelector('canvas')).toBeNull();
  await click(step);
  expect(sourceButtons()).toHaveLength(2);
  expect(container.textContent).not.toContain('PRIVATE_REASONING_SENTINEL');
  evidence.push({ journey: 'streaming completion', staysCollapsed: true, hiddenReasoningDisclosed: false });
});

it('D11: selecting one Diary call does not mount graphs for the other calls', async () => {
  const second = workingMessages(searchResult, 'search').map(message => ({ ...message, id: message.id + 10, metadata: message.type === 'tool_use' ? { ...message.metadata, id: 'diary-second' } : { ...message.metadata, tool_use_id: 'diary-second' } }));
  await render([...workingMessages(), ...second], {}, false);
  await click(container.querySelector('.v3-working-toggle'));
  const steps = [...container.querySelectorAll('.v3-wpi-tool-header.is-toggle')];
  expect(steps).toHaveLength(2);
  await click(steps[1]);
  expect(container.querySelectorAll('.cc-diary-tool')).toHaveLength(1);
  expect(container.textContent).toContain('找到 2 条笔记');
  expect(container.textContent).not.toContain('已返回 2 条笔记正文');
  expect(steps[0].getAttribute('aria-expanded')).toBe('false');
  expect(requests).toHaveLength(0);
  evidence.push({ journey: 'per-call disclosure', calls: 2, mountedGraphs: 1 });
});

it('D10: closing the process aborts an open private read and removes its portal', async () => {
  let finish;
  fetch.mockImplementationOnce((url, options) => {
    requests.push({ signal: options.signal });
    return new Promise(resolve => { finish = resolve; });
  });
  await render(); await click(sourceButtons()[0]);
  expect(reader()).toBeTruthy();
  await click(container.querySelector('.v3-working-toggle'));
  expect(requests[0].signal.aborted).toBe(true);
  expect(reader()).toBeNull();
  finish(jsonResponse(readPage())); await settle();
  expect(reader()).toBeNull();
  expect(container.querySelector('canvas')).toBeNull();
  evidence.push({ journey: 'collapse cancels read', aborted: true, stalePortal: false });
});
