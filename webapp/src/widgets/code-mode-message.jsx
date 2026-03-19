import React, { useState } from 'react';
import ContentBlockRenderer from './content-block-renderer';

export default function CodeModeMessage({ blocks }) {
  const [expanded, setExpanded] = useState(false);

  if (!blocks || blocks.length === 0) return null;

  return (
    <div className="oc-working-process">
      <div className="oc-wp-header" onClick={() => setExpanded(!expanded)}>
        <span className="oc-wp-icon">💭</span>
        <span className="oc-wp-title">思考过程...</span>
        <span className="oc-wp-chevron">{expanded ? '▼' : '▶'}</span>
      </div>
      {expanded && (
        <div className="oc-wp-content">
          {blocks.map((block, i) => (
            <ContentBlockRenderer key={i} block={block} />
          ))}
        </div>
      )}
    </div>
  );
}
