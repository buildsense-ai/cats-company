import React from 'react';
import SlidingSegments from './sliding-segments';

export default function AgentDetailTabs({ ariaLabel, idPrefix, tabs, value, onChange }) {
  return (
    <SlidingSegments className="cc-agent-detail-tabs" role="tablist" aria-label={ariaLabel} value={value} keyboardTabs>
      {tabs.map(([id, title]) => (
        <button type="button" role="tab" key={id} id={`${idPrefix}-tab-${id}`}
          aria-selected={value === id} aria-controls={`${idPrefix}-content`}
          onClick={() => onChange(id)}>
          {title}
        </button>
      ))}
    </SlidingSegments>
  );
}
