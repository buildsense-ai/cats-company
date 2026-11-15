// Adapter for XiaoBa's existing catslog_knowledge_recall wire result. Only
// validated Diary records become navigation targets; prose and clipped JSON
// are never scanned for refs. Display metadata is not a claim of model use.
const DOCUMENT = /^akd-[a-f0-9]{24}$/;
const ENTRY = /^ake-[a-f0-9]{24}$/;
const REVISION = /^akr-[a-f0-9]{64}$/;
const DAY = /^\d{4}-\d{2}-\d{2}$/;
export const DIARY_STATUS = { draft: '未审定', active: '有效', superseded: '已被替代', retired: '已停用' };
export const DIARY_RELATIONS = { corrects: '更正', supplements: '补充', requires: '依赖' };
export const diaryRef = (a) => `catslog:knowledge:${a.document_id}:${a.revision}:${a.id}`;
const validAnchor = (a) => a?.kind === 'knowledge_entry' && DOCUMENT.test(a.document_id) && ENTRY.test(a.id) && REVISION.test(a.revision);
const object = (v) => v && typeof v === 'object' && !Array.isArray(v);

export function diaryToolView(item) {
  if (item.name !== 'catslog_knowledge_recall' || item.isError) return null;
  try {
    const input = typeof item.input === 'string' ? JSON.parse(item.input) : item.input;
    if (!['search', 'read', 'graph'].includes(input?.action)) return null;
    if (item.result == null) return { action: input.action, pending: true, query: typeof input.query === 'string' ? input.query.slice(0, 200) : '', sources: [], relations: [] };
    if (typeof item.result === 'string' && item.result.length > 1024 * 1024) return null;
    const body = typeof item.result === 'string' ? JSON.parse(item.result) : item.result;
    const attribution = body?.recall_attribution;
    if (!object(body) || !/^recall-[a-f0-9-]{36}$/.test(attribution?.trace_id) || !Array.isArray(attribution.sources) || attribution.sources.length > 100) return null;
    const sources = [];
    const add = (a, e, day, disclosure, ref, currentStatus) => {
      if (!validAnchor(a) || !e || e.id !== a.id || typeof e.title !== 'string' || e.title.length > 1000 || !DIARY_STATUS[e.status] || !DAY.test(day) || ref !== diaryRef(a)) throw new Error('invalid Diary identity');
      if (disclosure === 'text' && typeof e.text !== 'string') throw new Error('missing body');
      if (!attribution.sources.some((s) => s.ref === ref && s.disclosure === disclosure && validAnchor(s.anchor) && diaryRef(s.anchor) === ref)) throw new Error('missing attribution');
      if (sources.some((s) => s.ref === ref)) throw new Error('duplicate source');
      // Session anchors are display-only clustering metadata. Search results
      // normally omit them; never infer a session from dates or nearby hits.
      const sourceAnchors = (Array.isArray(e.source_anchors) ? e.source_anchors.slice(0, 32) : []).filter((a) =>
        ['session_query', 'session_result', 'graph_node', 'learning_node'].includes(a?.kind) && typeof a.session_id === 'string' && a.session_id.length > 0 && a.session_id.length <= 512);
      sources.push({ ref, anchor: a, title: e.title, day, status: e.status, currentStatus: DIARY_STATUS[currentStatus] ? currentStatus : e.status, disclosure, sourceAnchors });
    };
    const relations = [];
    if (body.contract === 'memory-graph/1') {
      if (!['graph', 'read'].includes(input.action) || body.closure_complete !== true || !Array.isArray(body.nodes) || body.nodes.length > 32 || !Array.isArray(body.edges) || body.edges.length > 128 || !Array.isArray(body.seeds) || !body.seeds.length) return null;
      for (const n of body.nodes) add(n.anchor, n.entry, n.day, 'text', n.ref, n.current_status);
      if (body.seeds.some((a) => !validAnchor(a) || !sources.some((s) => s.ref === diaryRef(a)))) return null;
      if (input.action === 'graph' && (!Array.isArray(input.seeds) || input.seeds.length !== body.seeds.length || input.seeds.some((a, i) => !validAnchor(a) || diaryRef(a) !== diaryRef(body.seeds[i])))) return null;
      if (input.action === 'read' && !body.seeds.some((a) => a.document_id === input.document_id && (!input.entry_id || a.id === input.entry_id) && (!input.revision || a.revision === input.revision))) return null;
      for (const edge of body.edges) {
        if (!DIARY_RELATIONS[edge.link?.kind] || !validAnchor(edge.source) || !validAnchor(edge.target)) return null;
        const from = diaryRef(edge.source), to = diaryRef(edge.target);
        if (![from, to].every((ref) => sources.some((s) => s.ref === ref))) return null;
        relations.push({ from, to, kind: edge.link.kind });
      }
    } else if (input.action === 'search' && Array.isArray(body.hits) && body.hits.length <= 50) {
      for (const hit of body.hits) add({ kind: 'knowledge_entry', document_id: hit.document_id, revision: hit.revision, id: hit.entry_id }, { ...hit, id: hit.entry_id }, hit.day, 'metadata', hit.ref);
    } else if (input.action === 'read' && Array.isArray(body.entries) && body.entries.length <= 100) {
      if (body.document_id !== input.document_id || input.revision && body.revision !== input.revision) return null;
      for (const e of body.entries) {
        if (input.entry_id && e.id !== input.entry_id) return null;
        add({ kind: 'knowledge_entry', document_id: body.document_id, revision: body.revision, id: e.id }, e, body.day, 'text', e.ref);
      }
    } else return null;
    return { action: input.action, pending: false, query: typeof input.query === 'string' ? input.query.slice(0, 200) : '', sources, relations, incomplete: body.incomplete === true || body.exhausted === false };
  } catch { return null; }
}

export function diaryAgentUID(value) {
  const match = String(value ?? '').match(/^(?:usr)?([1-9]\d*)$/);
  return match ? match[1] : '';
}
