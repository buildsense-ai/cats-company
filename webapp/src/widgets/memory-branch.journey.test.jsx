import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { mkdirSync, writeFileSync } from 'node:fs';
import runtimeCapture from '../../../server/testdata/memory-branch.v1.json';
import ChatMessage from './chat-message';
import { setToken } from '../auth-session';

// E2E failure modes declared before code: B1 live/history diverge or hide
// sources with details off; B2 duplicate/stale seq rolls back injection;
// B3 read/queued/cancelled evidence falsely claims parent injection/citation;
// B4 private text/capabilities become public graph labels; B5 detail permission
// refusal or mismatched pins fall back to cached bodies; B6 changing selection
// admits a stale response; B7 a late event attaches to its origin answer;
// B8 relation kinds and complete qualifiers are lost; B9 unknown schema is
// treated as evidence. Browser capture covers actual Canvas and responsive UI.
const ref = `ref_${'a'.repeat(64)}`;
const revision = 'b'.repeat(64);
const base = { schema: 'catsco.memory_branch.v1', branch_run_id: 'memory-fixture', origin_turn_id: 'turn-1', sequence: 1, status: 'running',
  nodes: [], edges: [], read_refs: [], injected_refs: [], answer_refs: [] };
const graph = { ...base, sequence: 4, status: 'injected', consumed_turn_id: 'turn-2', observation_id: 'observation-1', read_refs: [ref], injected_refs: [ref],
  nodes: [{ id: 'assertion-1', kind: 'assertion', label: '记忆断言', ref, reader: { kind: 'projection', projection_ref: ref, revision } },
    { id: 'condition-1', kind: 'condition', label: '适用条件', ref, reader: { kind: 'projection', projection_ref: ref, revision } }],
  edges: [{ id: 'qualifier-1', from: 'condition-1', to: 'assertion-1', kind: 'qualifies' }] };
const document = { ref, revision, document: { nodes: [{ id: 'assertion-1', kind: 'assertion', text: '先确认迁移兼容性，再执行回滚。' }, { id: 'condition-1', kind: 'condition', text: '仅当旧版本仍能读取当前数据。' }],
  edges: [{ from: 'condition-1', to: 'assertion-1', kind: 'qualifies' }], conditions: ['仅当旧版本仍能读取当前数据。'], omissions: ['不保证外部系统可回滚。'], overall_coverage: 'partial', gap_acknowledgement: '外部系统未核验' } };
let container, root, requests;
const evidence = [];
const settle = async () => act(async () => { for (let i = 0; i < 15; i++) await Promise.resolve(); });
const click = async (el) => { expect(el).toBeTruthy(); await act(async () => el.click()); await settle(); };
const render = async (events, saved = false) => {
  const blocks = events.map(payload => ({ type: 'memory_branch', payload }));
  await act(async () => root.render(<ChatMessage message={{ id: 3, from_uid: 'usr574', content: '', ...(saved ? { content_blocks: blocks } : {}) }}
    workingMessages={saved ? [] : events.map((content, id) => ({ id, type: 'memory_branch', from_uid: 'usr574', content }))}
    workingOnly workingComplete showThinking={false} senderIsBot senderName="小八" />));
  await settle();
};
beforeEach(() => {
  container = globalThis.document.createElement('div'); globalThis.document.body.append(container); root = createRoot(container); requests = [];
  setToken('owner-fixture');
  vi.stubGlobal('ResizeObserver', class { observe() {} disconnect() {} });
  vi.stubGlobal('fetch', vi.fn(async (url, options) => { requests.push({ url: String(url), query: JSON.parse(options.body), auth: options.headers.Authorization }); return new Response(JSON.stringify(document), { status: 200 }); }));
});
afterEach(async () => { await act(async () => root.unmount()); container.remove(); setToken(null); vi.unstubAllGlobals(); });
afterAll(() => { mkdirSync('/tmp/catsco-branch-sources', { recursive: true }); writeFileSync('/tmp/catsco-branch-sources/ui-journey.json', JSON.stringify({ evidence }, null, 2)); });

it('B1/B2/B3/B7: live replay deduplicates by run/sequence and distinguishes ready from actual injection', async () => {
  await render([base, { ...graph, status: 'ready', sequence: 2, injected_refs: [], consumed_turn_id: undefined }]);
  expect(container.textContent).not.toContain('已加入主 Agent 上下文');
  await render([graph, base, graph]);
  expect(container.querySelectorAll('[aria-label="记忆探索"]')).toHaveLength(1);
  expect(container.textContent).toContain('已加入主 Agent 上下文');
  expect(container.textContent).toContain('来自上一轮');
  expect(container.textContent).not.toContain('回答中明确引用');
  expect(requests).toHaveLength(0);
  evidence.push({ journey: 'live ordering/carryover', injected: 1, eagerReads: 0 });
});
it('B1/B5/B8: stored blocks → authorized pinned detail with all conditions and omissions → close focus', async () => {
  await render([graph], true);
  const button = container.querySelector('.cc-memory-source'); button.focus(); await click(button);
  expect(requests[0].url).toContain('/api/agents/574/memory-sources');
  expect(requests[0].query).toEqual(graph.nodes[0].reader);
  const reader = globalThis.document.querySelector('[aria-label="记忆来源详情"]');
  expect(reader.textContent).toContain('先确认迁移兼容性');
  expect(reader.textContent).toContain('仅当旧版本仍能读取当前数据');
  expect(reader.textContent).toContain('不保证外部系统可回滚');
  expect(reader.textContent).toContain('外部系统未核验');
  await act(async () => globalThis.document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true })));
  expect(globalThis.document.querySelector('[aria-label="记忆来源详情"]')).toBeNull();
  expect(globalThis.document.activeElement).toBe(button);
  evidence.push({ journey: 'saved/pinned detail', query: requests[0].query, qualifiersRetained: true });
});
it('B5: refusal and pin mismatch never reveal a source body', async () => {
  fetch.mockResolvedValueOnce(new Response('{}', { status: 404 }));
  await render([graph]); await click(container.querySelector('.cc-memory-source'));
  expect(globalThis.document.body.textContent).toContain('无权查看或来源已不可用');
  expect(globalThis.document.body.textContent).not.toContain('先确认迁移兼容性');
  evidence.push({ journey: 'private refusal', disclosed: false });
});
it('recorded runtime → persisted blocks → history renderer keeps every node and typed edge past 2KB', async () => {
  const final = runtimeCapture.events.at(-1);
  expect(JSON.stringify(final).length).toBeGreaterThan(2000);
  await render(runtimeCapture.events, true);
  expect(container.querySelectorAll('.cc-memory-source')).toHaveLength(final.nodes.length);
  expect(container.querySelector('canvas').getAttribute('aria-label')).toContain(`${final.edges.length} 条真实关系`);
  expect(container.textContent).toContain('回答中明确引用 1 份');
  expect(requests).toHaveLength(0);
  evidence.push({ journey: 'actual runtime contract replay', nodes: final.nodes.length, edges: final.edges.length, sequence: final.sequence });
});
it('B5: a different canonical pin is refused without cached text', async () => {
  fetch.mockResolvedValueOnce(new Response(JSON.stringify({ ...document, ref: `ref_${'f'.repeat(64)}` }), { status: 200 }));
  await render([graph]); await click(container.querySelector('.cc-memory-source'));
  expect(globalThis.document.body.textContent).toContain('返回版本与引用不一致');
  expect(globalThis.document.querySelector('.cc-diary-prose')).toBeNull();
});
it('B6: changing selected source aborts and ignores a late response even when transport ignores abort', async () => {
  const otherRef = `ref_${'c'.repeat(64)}`;
  let finishFirst;
  fetch.mockImplementationOnce(() => new Promise(resolve => { finishFirst = resolve; }));
  fetch.mockResolvedValueOnce(new Response(JSON.stringify({ ...document, ref: otherRef, document: { ...document.document, goal: '当前选中的第二来源' } }), { status: 200 }));
  const event = structuredClone(graph); event.nodes[1].ref = otherRef; event.nodes[1].reader.projection_ref = otherRef; event.read_refs.push(otherRef); event.injected_refs.push(otherRef);
  await render([event]); await click(container.querySelectorAll('.cc-memory-source')[0]); await click(container.querySelectorAll('.cc-memory-source')[1]);
  expect(globalThis.document.body.textContent).toContain('当前选中的第二来源');
  finishFirst(new Response(JSON.stringify({ ...document, document: { ...document.document, goal: '迟到的旧来源不得出现' } }), { status: 200 })); await settle();
  expect(globalThis.document.body.textContent).not.toContain('迟到的旧来源不得出现');
  expect(globalThis.document.body.textContent).toContain('当前选中的第二来源');
  evidence.push({ journey: 'selection race', ignoredLateResponse: true });
});

it('B9: unknown version and invalid graph endpoints never render rich source navigation', async () => {
  await render([{ ...graph, schema: 'catsco.memory_branch.v99' }]);
  expect(container.querySelector('.cc-memory-source')).toBeNull();
  await render([{ ...graph, edges: [{ id: 'invented', from: 'foreign', to: 'assertion-1', kind: 'qualifies' }] }]);
  expect(container.querySelector('.cc-memory-source')).toBeNull();
});
