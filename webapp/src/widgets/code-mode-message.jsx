import React from 'react';
import ContentBlockRenderer from './content-block-renderer';

export default function CodeModeMessage({ message }) {
  if (!message.content_blocks || message.content_blocks.length === 0) {
    return <div className="oc-code-mode-message">{message.content}</div>;
  }

  return (
    <div className="oc-code-mode-message">
      {message.content_blocks.map((block, i) => (
        <ContentBlockRenderer key={i} block={block} />
      ))}
    </div>
  );
}
