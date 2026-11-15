// Synthetic, protocol-shaped XiaoBa #642 responses shared by the UI journey
// and the development preview. No private Diary content is checked in.
export const documentId = 'akd-0123456789abcdef01234567';
export const revision = `akr-${'a'.repeat(64)}`;
export const nextRevision = `akr-${'b'.repeat(64)}`;
export const entryId = 'ake-0123456789abcdef01234567';
export const correctionId = 'ake-1123456789abcdef01234567';
export const anchor = (id = entryId, rev = revision) => ({ kind: 'knowledge_entry', document_id: documentId, revision: rev, id });
export const ref = (id = entryId, rev = revision) => `catslog:knowledge:${documentId}:${rev}:${id}`;
const entry = (id, title, text, status) => ({ id, title, text, status });
export const entries = [
  entry(entryId, '部署前保留可回滚版本', '部署前记录当前版本，并保留回滚入口。\n\n**适用条件**：数据库变更向后兼容。', 'active'),
  entry(correctionId, '补充：回滚后还要验证数据', '回滚完成后，验证数据可读性和后台任务状态。', 'draft'),
];
const attribution = (sources) => ({ trace_id: 'recall-01234567-89ab-cdef-0123-456789abcdef', sources, server_feedback: 'not_connected' });
export const readPage = (rev = revision) => ({ document_id: documentId, revision: rev, day: '2026-11-13', entries, exhausted: true });
export const searchResult = {
  hits: entries.map((e) => ({ document_id: documentId, revision, day: '2026-11-13', entry_id: e.id, title: e.title, status: e.status, ref: ref(e.id) })),
  exhausted: false, next_cursor: 'opaque-next-page', incomplete: true,
  recall_attribution: attribution(entries.map((e) => ({ ref: ref(e.id), anchor: anchor(e.id), disclosure: 'metadata' }))),
};
export const manyEntries = [...entries, ...['回滚前检查后台任务', '数据库迁移保持兼容', '保留上一版本构建产物', '上线后验证关键查询', '部署失败时停止后续步骤', '补充人工确认的边界'].map((title, i) => entry(`ake-${String(i + 2).padStart(24, '0')}`, title, `${title}。这是用于验证多个搜索命中的本地示例正文。`, i % 2 ? 'draft' : 'active'))];
export const manySearchResult = {
  ...searchResult,
  hits: manyEntries.map((e) => ({ document_id: documentId, revision, day: '2026-11-13', entry_id: e.id, title: e.title, status: e.status, ref: ref(e.id) })),
  recall_attribution: attribution(manyEntries.map((e) => ({ ref: ref(e.id), anchor: anchor(e.id), disclosure: 'metadata' }))),
};
export const graphResult = {
  contract: 'memory-graph/1', seeds: [anchor()], closure_complete: true,
  nodes: entries.map((e) => ({ anchor: anchor(e.id), day: '2026-11-13', entry: e, head_revision: nextRevision, current_status: e.status, ref: ref(e.id) })),
  edges: [{ link: { id: `akl-${'c'.repeat(64)}`, kind: 'corrects', source: anchor(correctionId), target: anchor() }, source: anchor(correctionId), target: anchor(), document_revision: revision }],
  recall_attribution: attribution(entries.map((e) => ({ ref: ref(e.id), anchor: anchor(e.id), disclosure: 'text' }))),
};
export const clusterEntries = [...manyEntries, ...['检查运行中的定时任务', '确认服务健康状态', '核对发布窗口', '记录发布结果'].map((title, i) => entry(`ake-${String(i + 10).padStart(24, '0')}`, title, `${title}。本地示例正文。`, 'active'))].map((e, i) => ({ ...e, source_anchors: [{ kind: 'session_query', session_type: 'catscompany', session_id: `cc_group:grp_${i < 6 ? '5100' : '5101'}`, stream_id: `fixture-stream-${i}`, byte_offset: 0 }] }));
export const clusterGraphResult = {
  ...graphResult,
  nodes: clusterEntries.map((e, i) => ({ anchor: anchor(e.id), day: `2026-11-${String(13 - i % 3).padStart(2, '0')}`, entry: e, ref: ref(e.id) })),
  recall_attribution: attribution(clusterEntries.map((e) => ({ ref: ref(e.id), anchor: anchor(e.id), disclosure: 'text' }))),
};
export function workingMessages(result = graphResult, action = 'graph') {
  const input = action === 'graph' ? { action, seeds: [anchor()] } : { action, query: '部署回滚' };
  return [
    { id: 1, from_uid: 'usr574', type: 'tool_use', content: 'catslog_knowledge_recall', metadata: { id: 'diary-call', input } },
    ...(result === null ? [] : [{ id: 2, from_uid: 'usr574', type: 'tool_result', content: typeof result === 'string' ? result : JSON.stringify(result), metadata: { tool_use_id: 'diary-call' } }]),
  ];
}
