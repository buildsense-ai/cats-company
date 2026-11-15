// catsco.memory_branch.v1: body-free snapshots from the real Branch runtime.
// Unknown versions, dangling edges, or mixed attribution are never evidence.
const statuses = new Set(['running', 'ready', 'empty', 'failed', 'cancelled', 'expired', 'injected', 'answered', 'deferred', 'unavailable']);
const pin = /^ref_[a-f0-9]{64}$/;
const revision = /^[a-f0-9]{64}$/;
const object = value => value && typeof value === 'object' && !Array.isArray(value);
const bounded = value => typeof value === 'string' && value.length > 0 && value.length <= 256 && !/[\x00-\x1f]/.test(value);
const only = (value, keys) => Object.keys(value).every(key => keys.includes(key));
const refs = value => Array.isArray(value) && value.length <= 64 && value.every(bounded) && new Set(value).size === value.length;
export const MEMORY_NODE_LABELS = { assertion: '记忆断言', condition: '适用条件', omission: '已知缺口', correction: '更正', uncertainty: '不确定项', session_query: '历史提问', session_result: '历史回答', learning_node: '学习记录', source_memory_projection_node: '记忆节点', message: '原始消息', tool_call: '工具调用' };
export const MEMORY_RELATIONS = { supports: '支持', qualifies: '适用条件', omits: '缺口', corrects: '更正', supplements: '补充', requires: '前置条件', derived_from: '来源于', continues: '延续', related: '关联', used: '使用' };

export function memoryReaderValid(reader) {
  if (!object(reader)) return false;
  if (reader.kind === 'projection') return only(reader, ['kind', 'projection_ref', 'revision']) && pin.test(reader.projection_ref) && (reader.revision === undefined || revision.test(reader.revision));
  if (reader.kind === 'assertion') return only(reader, ['kind', 'projection_ref', 'member_ref', 'node_id']) && pin.test(reader.projection_ref) && pin.test(reader.member_ref) && /^[a-f0-9]{64}$/.test(reader.node_id);
  const a = reader.anchor;
  if (reader.kind !== 'source' || !only(reader, ['kind', 'anchor']) || !object(a) || !['source_memory_projection_node', 'session_query', 'session_result', 'learning_node'].includes(a.kind)) return false;
  if (!only(a, ['kind', 'id', 'revision', 'session_id', 'session_type', 'stream_id', 'byte_offset', 'byte_length', 'source_projection_ref'])) return false;
  if (!bounded(a.id) || Object.entries(a).some(([key, value]) => !['byte_offset', 'byte_length'].includes(key) && !bounded(value))) return false;
  if (a.kind === 'source_memory_projection_node') return pin.test(a.source_projection_ref) && revision.test(a.revision);
  return ['byte_offset', 'byte_length'].every(key => a[key] === undefined || Number.isSafeInteger(a[key]) && a[key] >= 0);
}
export function memoryBranchEvent(raw) {
  let event = raw;
  if (typeof event === 'string') { try { event = JSON.parse(event); } catch { return null; } }
  if (!object(event) || event.schema !== 'catsco.memory_branch.v1' || !bounded(event.branch_run_id) || !bounded(event.origin_turn_id)
    || !Number.isSafeInteger(event.sequence) || event.sequence < 1 || !statuses.has(event.status)
    || !refs(event.read_refs) || !refs(event.injected_refs) || !refs(event.answer_refs)
    || event.injected_refs.some(ref => !event.read_refs.includes(ref)) || event.answer_refs.some(ref => !event.injected_refs.includes(ref))
    || event.injected_refs.length > 0 && (!bounded(event.consumed_turn_id) || !bounded(event.observation_id))
    || !Array.isArray(event.nodes) || event.nodes.length > 64 || !Array.isArray(event.edges) || event.edges.length > 128) return null;
  if (!event.nodes.every(node => object(node) && bounded(node.id) && bounded(node.kind) && bounded(node.ref) && event.read_refs.includes(node.ref) && memoryReaderValid(node.reader))) return null;
  const ids = new Set(event.nodes.map(node => node.id));
  if (ids.size !== event.nodes.length || !event.edges.every(edge => object(edge) && bounded(edge.id) && bounded(edge.kind) && ids.has(edge.from) && ids.has(edge.to))) return null;
  return event;
}
export function latestMemoryBranches(items) {
  const latest = new Map();
  for (const item of items || []) {
    if (item?.type !== 'memory_branch') continue;
    const event = memoryBranchEvent(item.payload ?? item.content);
    if (event && (!latest.has(event.branch_run_id) || latest.get(event.branch_run_id).sequence < event.sequence)) latest.set(event.branch_run_id, event);
  }
  return [...latest.values()];
}

export function verifiedMemoryDetail(page, reader) {
  const nodeRows = nodes => Array.isArray(nodes) && nodes.every(node => object(node) && typeof node.text === 'string' && typeof node.kind === 'string');
  if (reader.kind === 'projection') {
    if (page?.ref !== reader.projection_ref || reader.revision && page.revision !== reader.revision || !object(page.document) || !nodeRows(page.document.nodes)) throw new Error('返回版本与引用不一致，未显示正文。');
    return { nodes: page.document.nodes, conditions: page.document.conditions || [], omissions: page.document.omissions || [], gaps: page.document.gap_acknowledgement, coverage: page.document.overall_coverage, goal: page.document.goal };
  }
  if (reader.kind === 'assertion') {
    if (page?.status !== 'complete' || page.read_required !== false || page.projection_ref !== reader.projection_ref || page.member_ref !== reader.member_ref || page.assertion?.id !== reader.node_id) throw new Error('该断言的完整条件尚不可核验，未显示正文。');
    const entries = page.entries || [];
    const nodes = [page.assertion, ...entries.flatMap(entry => entry.node ? [entry.node] : entry.locator ? [{ kind: entry.locator.kind, text: entry.locator.text, association: entry.locator.association }] : [])];
    if (!nodeRows(nodes)) throw new Error('断言内容不完整，未显示正文。');
    return { nodes, conditions: [], omissions: [], coverage: 'partial' };
  }
  const source = page?.source;
  if (!source || source.status !== 'read' || source.coverage !== 'complete' || source.truncated || source.missing || source.revoked || typeof source.text !== 'string'
    || Object.entries(reader.anchor).some(([key, value]) => {
      const actual = source.anchor?.[key];
      if (key !== 'revision') return actual !== value;
      // CatsLog normalizes source revisions to bare SHA-256 digests. Only
      // the optional canonical prefix may differ; identity stays exact.
      const expected = typeof value === 'string' ? value.replace(/^sha256:/, '') : '';
      return !revision.test(expected) || typeof actual !== 'string' || actual.replace(/^sha256:/, '') !== expected;
    })) throw new Error('来源身份或完整性核验失败，未显示正文。');
  const context = source.projection_context;
  if (!context) return { nodes: [{ kind: source.anchor.kind, text: source.text }], conditions: [], omissions: [], coverage: source.coverage };
  if (context.contract_version !== 'memory-graph/3' || !nodeRows(context.nodes)) throw new Error('记忆图谱不完整，未显示正文。');
  const qualifier = key => (context.qualifiers?.[key] || []).flatMap(item => typeof item.text === 'string' ? [item.text] : []);
  return { nodes: context.nodes, conditions: qualifier('conditions'), omissions: qualifier('omissions'), coverage: 'partial' };
}
