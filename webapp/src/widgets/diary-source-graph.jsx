import React, { useMemo, useRef, useState } from 'react';
import { List, Network } from 'lucide-react';
import { DIARY_STATUS } from './diary-sources';
import { sourceIdentity } from './daily-notes/daily-notes-model';
import { createDailyNotesLayout } from './daily-notes/daily-notes-layout';
import DailyNotesNetwork from './daily-notes/daily-notes-network';
import './daily-notes/daily-notes.css';

// Tool recall is a bounded subset of the Diary, using the very same source
// regions, sunflower packing, Canvas renderer and camera as Agent Settings.
// Use revision refs as model IDs: two historical versions remain distinct.
function recallModel(view) {
  const entries = new Map();
  const sources = new Map();
  const days = new Map();
  for (const source of view.sources) {
    const entry = { id: source.ref, title: source.title, day: source.day, sources: new Set(), links: [] };
    entries.set(entry.id, entry);
    if (!days.has(entry.day)) days.set(entry.day, []);
    days.get(entry.day).push(entry);
    for (const anchor of source.sourceAnchors || []) {
      const identity = sourceIdentity({ kind: anchor.kind, anchor });
      if (!sources.has(identity.key)) sources.set(identity.key, { ...identity, entries: new Set() });
      sources.get(identity.key).entries.add(entry.id);
      entry.sources.add(identity.key);
    }
  }
  const relations = view.relations.filter(({ from, to }) => from !== to && entries.has(from) && entries.has(to));
  for (const relation of relations) {
    entries.get(relation.from).links.push(relation);
    entries.get(relation.to).links.push(relation);
  }
  return { entries, sources, relations, days: [...days.keys()].sort().reverse().map((day) => ({ day, entries: days.get(day) })) };
}

export default function DiarySourceGraph({ view, selectedRef, onSelect }) {
  const wrapRef = useRef(null);
  const [mode, setMode] = useState('graph');
  const model = useMemo(() => recallModel(view), [view]);
  const layout = useMemo(() => {
    const result = createDailyNotesLayout(model);
    // The adapter caps recall at 100 entries, unlike the unbounded settings
    // corpus. This small layout can settle without an asynchronous loader.
    result.advance(Infinity);
    result.complete = true;
    if (!model.sources.size) result.regions.forEach((region) => { region.label = '未提供来源'; });
    return result;
  }, [model]);
  const selectNode = (ref) => {
    const source = view.sources.find((item) => item.ref === ref);
    if (source) onSelect(source, wrapRef.current?.querySelector('canvas'));
  };
  const rows = view.sources.map((source) => <button key={source.ref} type="button" className="cc-diary-source cc-diary-node" aria-current={source.ref === selectedRef ? 'true' : undefined}
    onClick={(event) => { event.stopPropagation(); onSelect(source, event.currentTarget); }}>
    <span className="cc-diary-source-label"><span>{source.title || '未命名笔记'}</span><small>{source.day} · {DIARY_STATUS[source.status]}{source.status === 'draft' ? ' · 尚未核验' : ''}</small></span>
  </button>);
  return <div className="cc-diary-graph-wrap" ref={wrapRef}>
    <div className="cc-diary-graph-toolbar"><span>点击节点阅读{view.action === 'search' ? ' · 未返回关联关系' : ''}</span><div className="cc-daily-notes-views" role="group" aria-label="日记来源视图"><button type="button" aria-label="列表视图" aria-pressed={mode === 'list'} onClick={() => setMode('list')}><List size={14} />列表</button><button type="button" aria-label="图谱视图" aria-pressed={mode === 'graph'} onClick={() => setMode('graph')}><Network size={14} />图谱</button></div></div>
    <div className={`cc-diary-source-graph is-${mode}`} role="group" aria-label="日记命中图谱">
      {mode === 'graph' && <DailyNotesNetwork layout={layout} model={model} selectedId={selectedRef} onSelect={selectNode} maxFitZoom={1.4} />}
      <div className={mode === 'graph' ? 'cc-diary-graph-accessible' : 'cc-diary-source-list'} aria-label="逐条阅读日记">{rows}</div>
    </div>
  </div>;
}
