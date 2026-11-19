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
// treated as evidence. Production replay (2026-11-18, 54 runs) added:
// B10 a terminal run without readable sources renders nothing, so the user
//     cannot tell memory ran (59% of runs); B11 reads that were never
//     injected disappear once the run leaves running/ready; B12 one row per
//     node repeats the same readable unit and graphs unlabeled dots, instead
//     of one row per verified reader with only the state that differs.
// Inline reading (2026-11-19 review: a side drawer covers the chat):
// M1 details open in a fixed drawer or body portal instead of directly under
//    their row; M2 a second row leaves the first open, or two bodies show;
// M3 Escape closes the whole list or loses focus instead of closing the row
//    first, then the list.
// Readability (2026-11-19 review: "too hard to read"):
// R1 every point repeats its own kind heading, so nine points read as nine
//    headings; R2 grouping reorders points within a kind or merges a point
//    whose source scope is unlinked into a group that implies it is bound;
//    R3 grouping drops or shortens a point's verified text.
// Links kept from the graph (2026-11-19: node hits and links were useful; one
// production run had 9 points and 10 many-to-many qualifies/supports edges):
// G1 a point that limits or supports another shows no link to it; G2 a point
//    linked to several others is repeated or dropped; G3 an edge whose end is
//    not in the verified document invents a reference; G4 a reference does
//    not match the tag on the point it names; G5 hit marks appear when every
//    point (or no point) of the source was hit, or on points the run did not
//    hit.
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
  const disclosure = container.querySelector('.cc-memory-toggle[aria-expanded="false"]');
  if (disclosure) await click(disclosure);
};
beforeEach(() => {
  container = globalThis.document.createElement('div'); globalThis.document.body.append(container); root = createRoot(container); requests = [];
  setToken('owner-fixture');
  vi.stubGlobal('ResizeObserver', class { observe() {} disconnect() {} });
  vi.stubGlobal('fetch', vi.fn(async (url, options) => { requests.push({ url: String(url), query: JSON.parse(options.body), auth: options.headers.Authorization }); return new Response(JSON.stringify(document), { status: 200 }); }));
});
afterEach(async () => { await act(async () => root.unmount()); container.remove(); setToken(null); vi.unstubAllGlobals(); });
afterAll(() => { mkdirSync('/tmp/catsco-branch-sources', { recursive: true }); writeFileSync('/tmp/catsco-branch-sources/ui-journey.json', JSON.stringify({ evidence }, null, 2)); });

it('archive read is owner-scoped on click and only the matching pin renders', async () => {
  const reader = {kind:'archive_turn', archive:{day:'2026-11-18',offset:123,digest:revision}};
  const node = {...graph.nodes[0],kind:'archive_turn',reader};
  const event = {...graph,nodes:[node],edges:[]};
  fetch.mockResolvedValueOnce(new Response('{}',{status:404}));
  await render([event], true); await click(container.querySelector('.cc-memory-source'));
  expect(requests.length).toBe(0);
  expect(globalThis.document.body.textContent).toContain('无权查看或来源已不可用');
  fetch.mockResolvedValueOnce(new Response(JSON.stringify({archive:reader.archive,source:{status:'read',coverage:'complete',text:'verified archived conversation'}})));
  await click(globalThis.document.querySelector('.cc-diary-read-error button'));
  expect(globalThis.document.body.textContent).toContain('verified archived conversation');
});

it('shows the consumed summary from grp_7445 during replay without inventing a readable source', async () => {
  const consumed = { ...base, sequence: 3, status: 'unavailable', consumed_turn_id: base.origin_turn_id, observation_id: 'obs' };
  await render([base, consumed], true);
  expect(container.textContent).toContain('回答参考了记忆摘要');
  expect(container.querySelector('.cc-memory-toggle, canvas')).toBeNull();
  expect(container.querySelector('.cc-memory-source')).toBeNull();
  expect(requests).toHaveLength(0);
  evidence.push({journey: 'grp_7445 summary consumed', clickableSources: 0});
});

it('B1/B2/B3/B7: live replay deduplicates by run/sequence and distinguishes ready from actual injection', async () => {
  await render([base, { ...graph, status: 'ready', sequence: 2, injected_refs: [], consumed_turn_id: undefined }]);
  expect(container.textContent).toContain('尚未送达');
  expect(container.textContent).not.toContain('回答参考了');
  await render([graph, base, graph]);
  expect(container.querySelectorAll('[aria-label="记忆来源"]')).toHaveLength(1);
  expect(container.textContent).toContain('回答参考了 1 条记忆');
  expect(container.textContent).toContain('上一轮');
  expect(container.textContent).not.toContain('回答引用');
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
it('M1/M2: a row expands its verified detail directly beneath it, one row at a time', async () => {
  const otherRef = `ref_${'c'.repeat(64)}`;
  const event = structuredClone(graph); event.nodes[1].ref = otherRef; event.nodes[1].reader.projection_ref = otherRef; event.read_refs.push(otherRef); event.injected_refs.push(otherRef);
  await render([event]);
  const [first, second] = container.querySelectorAll('.cc-memory-source');
  expect(first.getAttribute('aria-expanded')).toBe('false');
  await click(first);
  const detail = first.nextElementSibling;
  expect(detail.getAttribute('aria-label')).toBe('记忆来源详情');
  expect(first.getAttribute('aria-expanded')).toBe('true');
  expect(first.getAttribute('aria-controls')).toBe(detail.id);
  expect(detail.textContent).toContain('先确认迁移兼容性');
  expect([...globalThis.document.body.children].filter(node => node !== container)).toHaveLength(0);
  await click(second);
  expect(first.getAttribute('aria-expanded')).toBe('false');
  expect(container.querySelectorAll('[aria-label="记忆来源详情"]')).toHaveLength(1);
  expect(second.nextElementSibling.getAttribute('aria-label')).toBe('记忆来源详情');
  await click(second);
  expect(container.querySelector('[aria-label="记忆来源详情"]')).toBeNull();
  evidence.push({ journey: 'inline detail', portals: 0, openRows: 1 });
});
it('M3: Escape closes the open row first, then the list, returning focus each time', async () => {
  await render([graph]);
  const row = container.querySelector('.cc-memory-source'); row.focus(); await click(row);
  await act(async () => row.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true })));
  expect(container.querySelector('[aria-label="记忆来源详情"]')).toBeNull();
  expect(container.querySelector('.cc-memory-toggle').getAttribute('aria-expanded')).toBe('true');
  expect(globalThis.document.activeElement).toBe(row);
  await act(async () => row.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true })));
  expect(container.querySelector('.cc-memory-panel')).toBeNull();
  expect(globalThis.document.activeElement).toBe(container.querySelector('.cc-memory-toggle'));
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
  const readers = new Set(final.nodes.map(node => JSON.stringify(node.reader)));
  expect(container.querySelectorAll('.cc-memory-source')).toHaveLength(readers.size);
  expect(container.querySelector('canvas')).toBeNull();
  expect(container.textContent).toContain(`${final.nodes.length} 个要点`);
  expect(container.textContent).toContain('回答引用');
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

// A canonical revision prefix must not refuse a valid source, while a
// different digest or another anchor identity must still clear the body.
it.each([
  ['bare digest', '', revision, 'query-record', true],
  ['canonical prefix', 'sha256:', revision, 'query-record', true],
  ['different digest', 'sha256:', 'c'.repeat(64), 'query-record', false],
  ['different identity', 'sha256:', revision, 'another-record', false],
])('pinned session source: %s', async (scenario, prefix, returnedRevision, returnedID, accepted) => {
  const event = structuredClone(graph);
  const anchor = { kind: 'session_query', id: 'query-record', session_id: 'session-1', stream_id: 'stream-1', revision: prefix + revision };
  event.nodes = [{ ...graph.nodes[0], kind: 'session_query', reader: { kind: 'source', anchor } }];
  event.edges = [];
  fetch.mockResolvedValueOnce(new Response(JSON.stringify({ source: {
    status: 'read', coverage: 'complete', truncated: false, missing: false, revoked: false,
    anchor: { ...anchor, id: returnedID, revision: returnedRevision }, text: 'Complete pinned session source',
  } }), { status: 200 }));
  await render([event]); await click(container.querySelector('.cc-memory-source'));
  const reader = globalThis.document.querySelector('[aria-label="记忆来源详情"]');
  expect(JSON.parse(fetch.mock.calls[0][1].body).anchor).toEqual(anchor);
  if (accepted) expect(reader.textContent).toContain('Complete pinned session source');
  else {
    expect(reader.textContent).toContain('来源身份或完整性核验失败');
    expect(reader.querySelector('.cc-diary-prose')).toBeNull();
  }
  evidence.push({ journey: 'canonical source revision', scenario, accepted });
});

it('B9: unknown version and invalid graph endpoints never render rich source navigation', async () => {
  await render([{ ...graph, schema: 'catsco.memory_branch.v99' }]);
  expect(container.querySelector('.cc-memory-toggle')).toBeNull();
  await render([{ ...graph, edges: [{ id: 'invented', from: 'foreign', to: 'assertion-1', kind: 'qualifies' }] }]);
  expect(container.querySelector('.cc-memory-source')).toBeNull();
});

// Each status the production fleet actually ended on without sources.
it.each([
  ['deferred', '记忆未赶上本次回答'],
  ['empty', '未找到相关记忆'],
  ['cancelled', '记忆查找未完成'],
  ['failed', '记忆查找未完成'],
])('B10: a %s run without sources still says so, without a fake disclosure', async (status, copy) => {
  await render([base, { ...base, sequence: 2, status }]);
  const line = container.querySelector('.cc-memory-status');
  expect(line?.textContent).toContain(copy);
  expect(line.getAttribute('role')).toBe('status');
  expect(container.querySelector('.cc-memory-toggle, .cc-memory-source')).toBeNull();
  expect(requests).toHaveLength(0);
  evidence.push({ journey: 'terminal without sources', status, copy });
});

it('B11: reads that never reached the answer stay listed and say they were not used', async () => {
  const archive = (n) => ({ id: `turn-${n}`, kind: 'archive_turn', label: '会话摘录', ref: `local:${String(n).repeat(64)}`,
    reader: { kind: 'archive_turn', archive: { day: '2026-11-18', offset: n * 100, digest: revision } } });
  const nodes = [archive(1), archive(2), archive(3)];
  for (const status of ['unavailable', 'deferred', 'cancelled']) {
    await render([{ ...base, sequence: 9, status, nodes, read_refs: nodes.map(node => node.ref) }]);
    expect(container.querySelectorAll('.cc-memory-source')).toHaveLength(3);
    expect(container.textContent).toContain('未用于本次回答');
    expect(container.textContent).toContain('2026-11-18');
  }
  expect(requests).toHaveLength(0);
  evidence.push({ journey: 'unused reads stay visible', rows: 3 });
});

it('B12: one row per readable unit; only rows that differ from the summary carry a state', async () => {
  const otherRef = `ref_${'d'.repeat(64)}`;
  const event = structuredClone(graph);
  event.nodes.push({ id: 'other', kind: 'assertion', label: '记忆断言', ref: otherRef, reader: { kind: 'projection', projection_ref: otherRef } });
  event.read_refs.push(otherRef);
  event.status = 'answered'; event.answer_refs = [ref];
  await render([event]);
  const rows = [...container.querySelectorAll('.cc-memory-source')];
  expect(rows).toHaveLength(2);
  expect(rows[0].textContent).toContain('2 个要点');
  expect(rows[0].textContent).toContain('回答引用');
  expect(rows[1].textContent).toContain('未采用');
  expect(container.textContent).toContain('回答参考了 1 条记忆');
  expect(container.querySelector('canvas, [role="group"][aria-label*="视图"]')).toBeNull();
  evidence.push({ journey: 'grouped rows', rows: 2, cited: 1, unused: 1 });
});

it('R1/R2/R3: points of one kind share a heading in document order without dropping text', async () => {
  const kinds = ['assertion', 'assertion', 'assertion', 'condition', 'assertion', 'assertion'];
  const nodes = kinds.map((kind, i) => ({ id: `n${i}`, kind, text: `要点 ${i} 的完整核验正文` }));
  nodes[5].association = 'member_scope_unlinked';
  fetch.mockResolvedValueOnce(new Response(JSON.stringify({ ...document, document: { ...document.document, nodes, conditions: [], omissions: [], gap_acknowledgement: undefined } })));
  await render([graph], true);
  await click(container.querySelector('.cc-memory-source'));
  const detail = container.querySelector('.cc-memory-detail');
  const groups = [...detail.querySelectorAll('.cc-memory-detail-node')].map(section => ({
    heading: section.querySelector('h3').textContent,
    points: [...section.querySelectorAll('li')].map(li => li.textContent.trim()) }));
  expect(groups).toEqual([
    { heading: '记忆断言', points: ['要点 0 的完整核验正文', '要点 1 的完整核验正文', '要点 2 的完整核验正文', '要点 4 的完整核验正文'] },
    { heading: '适用条件', points: ['要点 3 的完整核验正文'] },
    { heading: '记忆断言 · 来源范围，未绑定到此断言', points: ['要点 5 的完整核验正文'] },
  ]);
  // No edges and no overlap with the run's node ids: no tags, no hit marks.
  expect(detail.querySelector('.cc-memory-point-tag, .cc-memory-point-hit')).toBeNull();
  evidence.push({ journey: 'grouped points', headings: groups.length, points: nodes.length });
});

it('G1-G5: many-to-many links from a production-shaped run resolve to tagged points, each shown once', async () => {
  // Shape of the only linked production run (2026-11-18): 4 assertions,
  // 3 conditions, 2 tool calls, 7 qualifies and 3 supports edges.
  const kinds = { a1: 'assertion', c1: 'condition', a2: 'assertion', a3: 'assertion', a4: 'assertion', c2: 'condition', c3: 'condition', t1: 'tool_call', t2: 'tool_call' };
  const nodes = Object.entries(kinds).map(([id, kind]) => ({ id, kind, text: `正文 ${id}` }));
  const edges = [['c1', 'qualifies', 'a1'], ['c1', 'qualifies', 'a3'], ['c1', 'qualifies', 'a4'], ['a2', 'qualifies', 'a1'], ['c2', 'qualifies', 'a2'],
    ['c2', 'qualifies', 'a1'], ['c3', 'qualifies', 'a3'], ['t1', 'supports', 'a2'], ['t2', 'supports', 'a3'], ['t2', 'supports', 'a4'],
    ['ghost', 'supports', 'a4'], ['a4', 'qualifies', 'ghost']].map(([from, kind, to]) => ({ from, kind, to }));
  const hitIds = ['a1', 'c1', 'a2', 'a3', 'c2', 't1', 't2'];
  const event = { ...graph, nodes: hitIds.map(id => ({ id, kind: kinds[id], label: '', ref, reader: { kind: 'projection', projection_ref: ref, revision } })), edges: [] };
  fetch.mockResolvedValueOnce(new Response(JSON.stringify({ ...document, document: { ...document.document, nodes, edges, conditions: [], omissions: [], gap_acknowledgement: undefined } })));
  await render([event], true);
  await click(container.querySelector('.cc-memory-source'));
  const points = [...container.querySelectorAll('.cc-memory-detail-node li')].map(li => ({
    tag: li.querySelector('.cc-memory-point-tag')?.textContent,
    text: li.querySelector('.cc-diary-prose').textContent.trim(),
    links: li.querySelector('.cc-memory-point-links')?.textContent,
    hit: Boolean(li.querySelector('.cc-memory-point-hit')) }));
  expect(points).toEqual([
    { tag: '断言 1', text: '正文 a1', links: '适用条件：条件 1、断言 2、条件 2', hit: true },
    { tag: '断言 2', text: '正文 a2', links: '适用条件：条件 2 · 支持：工具 1', hit: true },
    { tag: '断言 3', text: '正文 a3', links: '适用条件：条件 1、条件 3 · 支持：工具 2', hit: true },
    { tag: '断言 4', text: '正文 a4', links: '适用条件：条件 1 · 支持：工具 2', hit: false },
    { tag: '条件 1', text: '正文 c1', links: undefined, hit: true },
    { tag: '条件 2', text: '正文 c2', links: undefined, hit: true },
    { tag: '条件 3', text: '正文 c3', links: undefined, hit: false },
    { tag: '工具 1', text: '正文 t1', links: undefined, hit: true },
    { tag: '工具 2', text: '正文 t2', links: undefined, hit: true },
  ]);
  // G4: every name used in a link line is the tag of exactly one point.
  const tags = points.map(point => point.tag);
  for (const name of points.flatMap(point => (point.links || '').split(/[：、·]/).map(part => part.trim()).filter(part => /\d$/.test(part)))) expect(tags.filter(tag => tag === name)).toHaveLength(1);
  // G5: when the run hit every point, nothing is marked.
  await act(async () => root.unmount()); root = createRoot(container);
  fetch.mockResolvedValueOnce(new Response(JSON.stringify({ ...document, document: { ...document.document, nodes, edges } })));
  await render([{ ...event, nodes: nodes.map(node => ({ id: node.id, kind: node.kind, label: '', ref, reader: event.nodes[0].reader })) }], true);
  await click(container.querySelector('.cc-memory-source'));
  expect(container.querySelector('.cc-memory-point-hit')).toBeNull();
  expect(container.querySelectorAll('.cc-memory-point-links')).toHaveLength(4);
  evidence.push({ journey: 'linked points', points: points.length, linkLines: 4, hits: points.filter(point => point.hit).length });
});
