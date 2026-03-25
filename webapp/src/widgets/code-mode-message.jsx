import React, { useState } from 'react';
import { ChevronDown, ChevronRight, Terminal, Brain } from 'lucide-react';
import Avatar from './avatar';

/* Extract a concise summary from tool input */
function toolInputSummary(input) {
  if (!input) return '';
  if (typeof input === 'string') return input;
  if (input.command) return input.command;
  if (input.file_path) return input.file_path;
  if (input.pattern) return input.pattern;
  const vals = Object.values(input);
  const first = vals.find(v => typeof v === 'string');
  if (first) return first.slice(0, 80) + (first.length > 80 ? '…' : '');
  return JSON.stringify(input).slice(0, 80);
}

/* Truncate long results */
function truncateResult(text, max = 200) {
  if (!text) return '';
  if (typeof text !== 'string') text = JSON.stringify(text);
  if (text.length <= max) return text;
  return text.slice(0, max) + '…';
}

/* Group content_blocks into structured items */
function groupBlocks(blocks) {
  const items = [];
  for (let i = 0; i < blocks.length; i++) {
    const b = blocks[i];
    if (b.type === 'thinking') {
      items.push({ type: 'thinking', content: b.thinking });
    } else if (b.type === 'tool_use') {
      const result = (i + 1 < blocks.length && blocks[i + 1].type === 'tool_result') ? blocks[i + 1] : null;
      items.push({
        type: 'tool',
        toolName: b.name,
        toolInput: toolInputSummary(b.input),
        toolResult: result ? truncateResult(result.content) : null,
      });
      if (result) i++;
    }
  }
  return items;
}

/* Single working process item */
function WorkingProcessItem({ type, content, toolName, toolInput, toolResult }) {
  if (type === 'thinking') {
    return (
      <div className="oc-wpi-thinking">
        <Brain size={14} className="oc-wpi-icon" />
        <span className="oc-wpi-text">{content}</span>
      </div>
    );
  }

  if (type === 'tool') {
    return (
      <div className="oc-wpi-tool">
        <div className="oc-wpi-tool-header">
          <Terminal size={14} className="oc-wpi-icon" />
          <span className="oc-wpi-tool-name">{toolName}</span>
          <span className="oc-wpi-tool-input">{toolInput}</span>
        </div>
        {toolResult && (
          <div className="oc-wpi-tool-result">
            <span className="oc-wpi-result-text">{toolResult}</span>
          </div>
        )}
      </div>
    );
  }

  return null;
}

export default function CodeModeMessage({ message, isSelf, isGroup, senderName, senderAvatarUrl, senderIsBot }) {
  const [isExpanded, setIsExpanded] = useState(false);

  if (!message.content_blocks || message.content_blocks.length === 0) {
    return null;
  }

  const textBlocks = message.content_blocks.filter(b => b.type === 'text');
  const workingBlocks = message.content_blocks.filter(b => b.type !== 'text');
  const steps = groupBlocks(workingBlocks);

  return (
    <div className="oc-code-mode-container">
      {/* Working Process - collapsible */}
      {steps.length > 0 && (
        <div className="oc-working-process">
          <button
            className="oc-working-toggle"
            onClick={() => setIsExpanded(!isExpanded)}
          >
            {isExpanded ? <ChevronDown size={14} /> : <ChevronRight size={14} />}
            <span className="oc-working-label">
              {isExpanded ? '思考过程' : 'Working...'}
            </span>
            {!isExpanded && (
              <span className="oc-working-hint">展开详情</span>
            )}
          </button>

          {isExpanded && (
            <div className="oc-working-steps">
              {steps.map((step, i) => (
                <WorkingProcessItem key={i} {...step} />
              ))}
            </div>
          )}
        </div>
      )}

      {/* Main message bubble - only text */}
      <div className={`oc-msg ${isSelf ? 'self' : ''}`}>
        {!isSelf && (
          <Avatar
            name={senderName || message.from_name || message.from_uid}
            src={senderAvatarUrl}
            size={40}
            isBot={senderIsBot}
            className="oc-msg-avatar"
          />
        )}
        <div className="oc-msg-body">
          {isGroup && !isSelf && senderName && (
            <div className="oc-msg-sender">{senderName}</div>
          )}
          <div className="oc-msg-bubble">
            {textBlocks.map((block, i) => (
              <div key={i} className="oc-text-block">{block.text}</div>
            ))}
          </div>
        </div>
      </div>
    </div>
  );
}
