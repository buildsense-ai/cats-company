import React, { useState } from 'react';

export default function CollapsibleBlock({ title, icon, children, defaultExpanded = false }) {
  const [expanded, setExpanded] = useState(defaultExpanded);

  return (
    <div className="oc-collapsible-block">
      <div className="oc-block-header" onClick={() => setExpanded(!expanded)}>
        <span className="oc-block-icon">{icon}</span>
        <span className="oc-block-title">{title}</span>
        <span className="oc-block-chevron">{expanded ? '▼' : '▶'}</span>
      </div>
      {expanded && <div className="oc-block-content">{children}</div>}
    </div>
  );
}
