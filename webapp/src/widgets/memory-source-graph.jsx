import React, { useMemo, useRef, useState } from 'react';
import { List, Network } from 'lucide-react';
import DailyNotesNetwork from './daily-notes/daily-notes-network';
import { createDailyNotesLayout } from './daily-notes/daily-notes-layout';
import { MEMORY_NODE_LABELS } from './memory-branch';
import './daily-notes/daily-notes.css';

export default function MemorySourceGraph({ nodes, edges, selectedId, onSelect }) {
  const wrapRef = useRef(null);
  const [mode, setMode] = useState('graph');
  const model = useMemo(() => {
    const entries = new Map(), sources = new Map();
    for (const node of nodes) {
      const entry = { id: node.id, title: MEMORY_NODE_LABELS[node.kind] || '记忆节点', day: '', sources: new Set([node.ref]), links: [] };
      entries.set(node.id, entry);
      if (!sources.has(node.ref)) sources.set(node.ref, { key: node.ref, label: `记忆来源 ${sources.size + 1}`, entries: new Set() });
      sources.get(node.ref).entries.add(node.id);
    }
    const relations = edges.filter(edge => entries.has(edge.from) && entries.has(edge.to)).map(edge => ({ ...edge, source: edge.from, target: edge.to }));
    for (const edge of relations) { entries.get(edge.from).links.push(edge); entries.get(edge.to).links.push(edge); }
    return { entries, sources, relations, days: [{ day: '', entries: [...entries.values()] }] };
  }, [nodes, edges]);
  const layout = useMemo(() => { const result = createDailyNotesLayout(model); result.advance(Infinity); result.complete = true; return result; }, [model]);
  return <div className="cc-diary-graph-wrap" ref={wrapRef}>
    <div className="cc-diary-graph-toolbar"><span>点击节点阅读来源</span><div className="cc-daily-notes-views" role="group" aria-label="记忆来源视图">
      <button type="button" aria-label="记忆列表视图" aria-pressed={mode === 'list'} onClick={() => setMode('list')}><List size={14} />列表</button>
      <button type="button" aria-label="记忆图谱视图" aria-pressed={mode === 'graph'} onClick={() => setMode('graph')}><Network size={14} />图谱</button>
    </div></div>
    <div className={`cc-diary-source-graph is-${mode}`} role="group" aria-label="已读取的记忆子图">
      {mode === 'graph' && <DailyNotesNetwork layout={layout} model={model} selectedId={selectedId} ariaLabel={`记忆图谱：${nodes.length} 个节点，${edges.length} 条真实关系。可在列表视图中逐条阅读。`}
        legend={<><span><i className="is-entry" /><i className="is-entry is-second" />记忆节点（同色为同一读取来源）</span><span><i className="is-link" />实际关系</span>{edges.some(edge => edge.kind === 'corrects') && <span><i className="is-correct" />更正</span>}</>}
        onSelect={id => { const node = nodes.find(item => item.id === id); if (node) onSelect(node, wrapRef.current?.querySelector('canvas')); }} maxFitZoom={1.4} />}
      <div className={mode === 'graph' ? 'cc-diary-graph-accessible' : 'cc-diary-source-list'} aria-label="逐条阅读记忆">
        {nodes.map((node, index) => <button key={node.id} type="button" className="cc-diary-source cc-memory-source" aria-current={selectedId === node.id ? 'true' : undefined} onClick={event => onSelect(node, event.currentTarget)}>
          <span className="cc-diary-source-label"><span>{MEMORY_NODE_LABELS[node.kind] || '记忆节点'} {index + 1}</span><small>{node.injected ? '已加入上下文' : '已读取 · 未加入上下文'}</small></span>
        </button>)}
      </div>
    </div>
  </div>;
}
