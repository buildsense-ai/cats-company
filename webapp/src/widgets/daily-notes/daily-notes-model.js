// Daily Notes arrive as CatsLog "diaries" graph pages: knowledge entries,
// the session records they cite, and typed links between entries. This module
// folds those pages into a reader model; it holds no React state.

export const RELATION_LABELS = {
  derived_from: '源自',
  continues: '延续',
  supplements: '补充',
  corrects: '更正',
  requires: '依赖',
  related: '相关',
  cites: '引用',
  used: '使用',
};

const shortID = (value, size = 6) => {
  const text = String(value || '');
  return text.length > size ? text.slice(-size) : text;
};

// One reader-facing source per session; the record hashes inside it are not
// meaningful to a person.
export function sourceIdentity(node) {
  const anchor = node?.anchor || {};
  if (node?.kind === 'learning_node') {
    return { key: `learning:${anchor.session_id || anchor.id}`, label: `学习过程 · ${shortID(anchor.session_id || anchor.id)}`, type: 'learning' };
  }
  if (anchor.session_id) {
    const id = String(anchor.session_id);
    const group = id.match(/^cc_group:(.+)$/);
    const label = group ? `群聊 ${group[1]}` : `会话 · ${shortID(id)}`;
    return { key: `session:${anchor.session_type || ''}:${id}`, label, type: group ? 'group' : 'session' };
  }
  return { key: `${node?.kind || 'source'}:${anchor.id || node?.key}`, label: `记录 · ${shortID(anchor.id || node?.label)}`, type: 'record' };
}

export function buildDailyNotesModel(pages) {
  const entries = new Map();
  const keyEntry = new Map();
  const keySource = new Map();
  const sources = new Map();
  for (const page of pages) {
    for (const node of page?.nodes || []) {
      const anchor = node?.anchor;
      if (!anchor?.id) continue;
      if (node.kind === 'knowledge_entry') {
        keyEntry.set(node.key, anchor.id);
        if (node.status !== 'referenced' && !entries.has(anchor.id)) {
          entries.set(anchor.id, {
            id: anchor.id,
            title: String(node.label || '').trim() || '未命名笔记',
            status: node.status || '',
            day: node.day || '',
            documentId: anchor.document_id || '',
            sources: new Set(),
            links: [],
          });
        }
        continue;
      }
      const identity = sourceIdentity(node);
      keySource.set(node.key, identity.key);
      if (!sources.has(identity.key)) sources.set(identity.key, { ...identity, entries: new Set() });
    }
  }
  const relations = [];
  const seen = new Set();
  for (const page of pages) {
    for (const edge of page?.edges || []) {
      const from = keyEntry.get(edge.from);
      if (!from || !entries.has(from)) continue;
      const target = keyEntry.get(edge.to);
      if (target) {
        if (target === from || !entries.has(target)) continue;
        const id = `${from}>${target}>${edge.kind}`;
        if (seen.has(id)) continue;
        seen.add(id);
        const relation = { from, to: target, kind: edge.kind };
        relations.push(relation);
        entries.get(from).links.push(relation);
        entries.get(target).links.push(relation);
        continue;
      }
      const source = keySource.get(edge.to);
      if (source) {
        entries.get(from).sources.add(source);
        sources.get(source).entries.add(from);
      }
    }
  }
  const days = new Map();
  for (const entry of entries.values()) {
    if (!days.has(entry.day)) days.set(entry.day, []);
    days.get(entry.day).push(entry);
  }
  const dayList = [...days.keys()].sort((a, b) => b.localeCompare(a));
  return {
    entries,
    sources,
    relations,
    days: dayList.map((day) => ({ day, entries: days.get(day) })),
  };
}

const normalize = (value) => String(value || '').toLocaleLowerCase('zh-CN');

// Every whitespace-separated term must appear in the title, the day, a source
// label, or the note body once its day has been read.
export function matchEntries(model, query, texts) {
  const terms = normalize(query).split(/\s+/).filter(Boolean);
  if (!terms.length) return null;
  const matches = new Set();
  for (const entry of model.entries.values()) {
    const haystack = normalize([
      entry.title,
      entry.day,
      texts.get(entry.id) || '',
      ...[...entry.sources].map((key) => model.sources.get(key)?.label),
    ].join('\n'));
    if (terms.every((term) => haystack.includes(term))) matches.add(entry.id);
  }
  return matches;
}

export function formatDay(day) {
  const match = String(day || '').match(/^(\d{4})-(\d{2})-(\d{2})$/);
  if (!match) return day || '未注明日期';
  const date = new Date(Number(match[1]), Number(match[2]) - 1, Number(match[3]));
  const weekday = ['日', '一', '二', '三', '四', '五', '六'][date.getDay()];
  return `${Number(match[2])}月${Number(match[3])}日 · 周${weekday}`;
}
