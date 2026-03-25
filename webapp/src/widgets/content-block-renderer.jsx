import React from 'react';
import { marked } from 'marked';
import CollapsibleBlock from './collapsible-block';

marked.setOptions({ breaks: false, gfm: true });

function TextBlock({ block }) {
  const text = block.text || '';
  const hasMarkdown = /(\*\*|__|`|#{1,6}\s|^\s*[-*+]\s|^\s*\d+\.\s|\[.*\]\(.*\))/m.test(text);
  if (hasMarkdown) {
    try {
      const html = marked.parse(text);
      return <div className="oc-text-block oc-markdown" dangerouslySetInnerHTML={{ __html: html }} />;
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
