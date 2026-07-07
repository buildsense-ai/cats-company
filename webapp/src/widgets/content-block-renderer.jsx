import React from 'react';
import CollapsibleBlock from './collapsible-block';
import {
  hasPlainTextTableLikeBlock,
  hasRenderableTable,
  renderSafeMarkdown,
  shouldRenderMarkdown,
} from './markdown-utils';

function TextBlock({ block }) {
  const text = block.text || '';
  const renderableTable = hasRenderableTable(text);
  const plainTextTableLike = !renderableTable && hasPlainTextTableLikeBlock(text);
  if (plainTextTableLike) {
    return <pre className="oc-text-block oc-plain-text-table">{text}</pre>;
  }
  if (shouldRenderMarkdown(text, { plainTextTables: true })) {
    try {
      const html = renderSafeMarkdown(text, { plainTextTables: true });
      const className = `oc-text-block oc-markdown${renderableTable ? ' oc-markdown-table' : ''}`;
      return <div className={className} dangerouslySetInnerHTML={{ __html: html }} />;
    } catch (e) { /* fall through */ }
  }
  return <div className="oc-text-block" style={{ whiteSpace: 'pre-wrap' }}>{text}</div>;
}

function ThinkingBlock({ block }) {
  return (
    <div className="oc-thinking-block">
      {block.thinking}
    </div>
  );
}

function ToolUseBlock({ block }) {
  return (
    <CollapsibleBlock title={block.name} icon="🔧" defaultExpanded={false}>
      <pre className="oc-tool-input">{JSON.stringify(block.input, null, 2)}</pre>
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
