import React, { useId, useState } from 'react';
import DiaryToolResult from './diary-tool-result';
import MemoryBranchSources from './memory-branch-sources';
import { latestMemoryBranches } from './memory-branch';

export default function TurnSources({ diaryItems, blocks, agentUid }) {
  const events = latestMemoryBranches(blocks);
  const [tab, setTab] = useState('diary');
  const id = useId().replace(/:/g, '');
  const diaries = diaryItems.map(({ item, view }, index) => <DiaryToolResult key={item.id || index} item={item} view={view} agentUid={agentUid} />);
  if (!events.length) return diaries;
  if (!diaryItems.length) return <MemoryBranchSources events={events} agentUid={agentUid} />;
  return <section className="cc-turn-sources" aria-label="本轮来源" onClick={event => event.stopPropagation()}>
    <div className="cc-turn-source-tabs" role="tablist" aria-label="来源类型" onKeyDown={event => {
      if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return;
      event.preventDefault(); const next = event.key === 'Home' ? 'diary' : event.key === 'End' ? 'memory' : tab === 'diary' ? 'memory' : 'diary';
      setTab(next); event.currentTarget.querySelector(`[id="${id}-${next}"]`)?.focus();
    }}>
      {[['diary', '日记'], ['memory', '记忆探索']].map(([key, title]) => <button key={key} id={`${id}-${key}`} type="button" role="tab" tabIndex={tab === key ? 0 : -1} aria-selected={tab === key} aria-controls={`${id}-${key}-panel`} onClick={() => setTab(key)}>{title}</button>)}
    </div>
    <div role="tabpanel" id={`${id}-diary-panel`} aria-labelledby={`${id}-diary`} hidden={tab !== 'diary'}>{tab === 'diary' && diaries}</div>
    <div role="tabpanel" id={`${id}-memory-panel`} aria-labelledby={`${id}-memory`} hidden={tab !== 'memory'}>{tab === 'memory' && <MemoryBranchSources events={events} agentUid={agentUid} />}</div>
  </section>;
}
