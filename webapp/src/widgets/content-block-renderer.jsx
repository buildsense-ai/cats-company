import React from 'react';

function ThinkingBlock({ block }) {
  return (
    <div className="oc-cb-thinking">
      {block.thinking}
    </div>
  );
}

function ToolUseBlock({ block }) {
  // 扁平格式：tool_name: 关键参数
  const summary = summarizeToolInput(block.name, block.input);
  return (
    <div className="oc-cb-tool-use">
      <span className="oc-cb-tool-name">{block.name}</span>
      {summary && <span className="oc-cb-tool-args">: {summary}</span>}
    </div>
  );
}

function ToolResultBlock({ block }) {
  const content = typeof block.content === 'string' ? block.content : JSON.stringify(block.content);
  const icon = block.is_error ? '✗' : '↳';
  return (
    <div className={`oc-cb-tool-result ${block.is_error ? 'error' : ''}`}>
      <span className="oc-cb-result-icon">{icon}</span>
      <pre className="oc-cb-result-content">{content}</pre>
    </div>
  );
}

function TextBlock({ block }) {
  return <div className="oc-cb-text">{block.text}</div>;
}

export default function ContentBlockRenderer({ block }) {
  switch (block.type) {
    case 'thinking':
      return <ThinkingBlock block={block} />;
    case 'tool_use':
      return <ToolUseBlock block={block} />;
    case 'tool_result':
      return <ToolResultBlock block={block} />;
    case 'text':
      return <TextBlock block={block} />;
    default:
      return null;
  }
}

/** 从工具参数中提取关键摘要 */
function summarizeToolInput(name, input) {
  if (!input) return '';
  // execute_shell → command
  if (name === 'execute_shell' && input.command) {
    const cmd = input.command;
    return cmd.length > 80 ? cmd.slice(0, 80) + '...' : cmd;
  }
  // read_file / write_file / edit_file → file_path
  if (input.file_path) {
    return input.file_path;
  }
  // grep → pattern
  if (name === 'grep' && input.pattern) {
    return input.pattern;
  }
  // glob → pattern
  if (name === 'glob' && input.pattern) {
    return input.pattern;
  }
  // thinking → content preview
  if (name === 'thinking' && input.content) {
    const c = input.content;
    return c.length > 60 ? c.slice(0, 60) + '...' : c;
  }
  // send_text → text preview
  if (name === 'send_text' && input.text) {
    const t = input.text;
    return t.length > 60 ? t.slice(0, 60) + '...' : t;
  }
  // fallback: first string value
  for (const val of Object.values(input)) {
    if (typeof val === 'string' && val.length > 0) {
      return val.length > 60 ? val.slice(0, 60) + '...' : val;
    }
  }
  return '';
}
