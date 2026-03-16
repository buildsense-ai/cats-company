import React from 'react';
import CollapsibleBlock from './collapsible-block';

function TextBlock({ block }) {
  return <div className="oc-text-block">{block.text}</div>;
}

function ThinkingBlock({ block }) {
  return (
    <CollapsibleBlock title="Thinking" icon="💭" defaultExpanded={false}>
      <pre className="oc-thinking-content">{block.thinking}</pre>
    </CollapsibleBlock>
  );
}

function ToolUseBlock({ block }) {
  return (
    <CollapsibleBlock title={`Tool: ${block.name}`} icon="🔧" defaultExpanded={false}>
      <div className="oc-tool-use">
        <div className="oc-tool-id">ID: {block.id}</div>
        <pre className="oc-tool-input">{JSON.stringify(block.input, null, 2)}</pre>
      </div>
    </CollapsibleBlock>
  );
}

function ToolResultBlock({ block }) {
  const icon = block.is_error ? '❌' : '✅';
  const title = block.is_error ? 'Tool Error' : 'Tool Result';

  return (
    <CollapsibleBlock title={title} icon={icon} defaultExpanded={false}>
      <pre className={`oc-tool-result ${block.is_error ? 'error' : 'success'}`}>
        {block.content}
      </pre>
    </CollapsibleBlock>
  );
}

export default function ContentBlockRenderer({ block }) {
  switch (block.type) {
    case 'text':
      return <TextBlock block={block} />;
    case 'thinking':
      return <ThinkingBlock block={block} />;
    case 'tool_use':
      return <ToolUseBlock block={block} />;
    case 'tool_result':
      return <ToolResultBlock block={block} />;
    default:
      return null;
  }
}
