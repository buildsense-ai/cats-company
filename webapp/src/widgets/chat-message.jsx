import React, { memo, useEffect, useMemo, useState } from 'react';
import { ChevronDown, ChevronRight, Terminal, Brain, FileText, Download, CornerUpLeft, MoreHorizontal, SmilePlus, X, Eye } from 'lucide-react';
import t from '../i18n';
import Avatar from './avatar';
import { resolveMediaURL } from '../api';
import { markdownPreviewDocument, renderSafeMarkdown } from './markdown-utils';

const WORKING_TEXT_PREFIX = 'AI文本:';
const HIDDEN_TOOL_PROGRESS_NAMES = new Set([
  'send_text',
  'send_file',
]);
const HTML_FILE_EXTENSIONS = new Set(['HTML', 'HTM', 'XHTML']);
const TEXT_FILE_EXTENSIONS = new Set(['TXT', 'JSON', 'MD', 'CSV', 'JS', 'PY', 'GO', 'HTML', 'HTM', 'CSS', 'XML']);
const PREVIEW_FILE_EXTENSIONS = new Set(['PDF', ...TEXT_FILE_EXTENSIONS]);

function shouldHideToolProgressName(name) {
  return HIDDEN_TOOL_PROGRESS_NAMES.has(String(name || '').trim());
}

/* Extract concise summary from tool input */
function toolInputSummary(name, input) {
  if (!input) return '';
  if (typeof input === 'string') return input;
  if (input.command) return input.command;
  if (input.file_path) return input.file_path;
  if (input.pattern) return input.pattern;
  if (input.content && typeof input.content === 'string') return input.content.slice(0, 120) + (input.content.length > 120 ? '...' : '');
  const vals = Object.values(input);
  const first = vals.find(v => typeof v === 'string');
  if (first) return first.slice(0, 120) + (first.length > 120 ? '...' : '');
  return JSON.stringify(input).slice(0, 120);
}

function truncateResult(text, max = 300) {
  if (!text) return '';
  if (typeof text !== 'string') text = JSON.stringify(text);
  if (text.length <= max) return text;
  return text.slice(0, max) + '...';
}

function groupBlocks(messages) {
  const items = [];
  const pendingTools = {};
  const hiddenToolIds = new Set();
  let hiddenToolWithoutId = false;

  for (let i = 0; i < messages.length; i++) {
    const msg = messages[i];
    if (msg.type === 'thinking') {
      items.push({ type: 'thinking', text: msg.content });
    } else if (msg.type === 'tool_use') {
      const toolId = msg.metadata?.id || msg.metadata?.tool_call_id || msg.metadata?.tool_use_id;
      if (shouldHideToolProgressName(msg.content)) {
        if (toolId) hiddenToolIds.add(toolId);
        else hiddenToolWithoutId = true;
        continue;
      }
      const pair = {
        type: 'tool_pair',
        name: msg.content,
        input: msg.metadata?.input,
        result: null,
        isError: false,
        id: toolId
      };
      if (toolId) pendingTools[toolId] = pair;
      items.push(pair);
    } else if (msg.type === 'tool_result') {
      const toolId = msg.metadata?.tool_use_id || msg.metadata?.id || msg.metadata?.tool_call_id;
      if ((toolId && hiddenToolIds.has(toolId)) || (!toolId && hiddenToolWithoutId)) {
        if (!toolId) hiddenToolWithoutId = false;
        continue;
      }
      let matched = false;
      if (toolId && pendingTools[toolId]) {
        pendingTools[toolId].result = msg.content;
        pendingTools[toolId].isError = msg.metadata?.is_error || false;
        matched = true;
      } else {
        // Fallback: match with first unfulfilled tool_pair
        for (const item of items) {
          if (item.type === 'tool_pair' && item.result === null) {
            item.result = msg.content;
            item.isError = msg.metadata?.is_error || false;
            matched = true;
            break;
          }
        }
      }
      if (!matched) {
        items.push({ type: 'tool_result_orphan', content: msg.content, isError: msg.metadata?.is_error || false });
      }
    }
  }
  return items;
}

function groupContentBlocks(blocks) {
  const items = [];
  const pendingTools = {};
  const hiddenToolIds = new Set();
  let hiddenToolWithoutId = false;

  const subAgentGroups = {};

  for (let i = 0; i < blocks.length; i++) {
    const block = blocks[i];
    if (block.type === 'thinking') {
      const text = block.thinking || block.text || block.content || '';
      const subAgentEvent = subAgentEventFromBlock(block, text);
      if (subAgentEvent) {
        const group = upsertSubAgentGroup(items, subAgentGroups, subAgentEvent);
        group.steps.push({ type: 'thinking', text: subAgentEvent.text });
      } else {
        items.push({ type: 'thinking', text });
      }
      continue;
    }
    if (block.type === 'assistant_text') {
      items.push({ type: 'assistant_text', text: block.text || block.content || '' });
      continue;
    }
    if (block.type === 'tool_use') {
      const toolId = block.id || block.tool_use_id;
      const subAgentInfo = subAgentInfoFromToolUse(block, toolId);
      if (subAgentInfo) {
        const group = upsertSubAgentGroup(items, subAgentGroups, subAgentInfo);
        group.steps.push({
          type: 'thinking',
          text: subAgentInfo.task ? `已派出：${subAgentInfo.task}` : '已派出，正在后台执行',
        });
        if (toolId) pendingTools[toolId] = group;
        continue;
      }
      if (shouldHideToolProgressName(block.name)) {
        if (toolId) hiddenToolIds.add(toolId);
        else hiddenToolWithoutId = true;
        continue;
      }

      const pair = {
        type: 'tool_pair',
        name: block.name || 'Tool',
        input: block.input,
        result: null,
        isError: false,
        id: toolId,
      };
      if (toolId) pendingTools[toolId] = pair;
      items.push(pair);
      continue;
    }
    if (block.type === 'tool_result') {
      const toolId = block.tool_use_id || block.id;
      const resultText = block.content || block.text || '';
      if ((toolId && hiddenToolIds.has(toolId)) || (!toolId && hiddenToolWithoutId)) {
        if (!toolId) hiddenToolWithoutId = false;
        continue;
      }
      let matched = false;
      if (toolId && pendingTools[toolId]) {
        if (pendingTools[toolId].type === 'subagent_group') {
          const group = pendingTools[toolId];
          const subAgentEvent = subAgentEventFromBlock(block, resultText) || {};
          upsertSubAgentGroup(items, subAgentGroups, {
            id: group.id,
            name: group.name,
            toolId,
            status: subAgentEvent.status || (block.is_error ? 'failed' : 'completed'),
          });
          group.result = resultText;
          group.isError = !!block.is_error;
          group.steps.push({ type: 'tool_result_orphan', content: resultText, isError: !!block.is_error });
        } else {
          pendingTools[toolId].result = resultText;
          pendingTools[toolId].isError = !!block.is_error;
        }
        matched = true;
      } else if (isSubAgentToolId(toolId)) {
        const subAgentEvent = subAgentEventFromBlock(block, resultText) || {};
        const group = upsertSubAgentGroup(items, subAgentGroups, {
          ...subAgentEvent,
          id: subAgentEvent.id || subAgentIdFromToolId(toolId),
          toolId,
          status: subAgentEvent.status || (block.is_error ? 'failed' : 'completed'),
        });
        group.result = resultText;
        group.isError = !!block.is_error;
        group.steps.push({ type: 'tool_result_orphan', content: resultText, isError: !!block.is_error });
        matched = true;
      } else {
        for (const item of items) {
          if (item.type === 'tool_pair' && item.result === null) {
            item.result = resultText;
            item.isError = !!block.is_error;
            matched = true;
            break;
          }
        }
      }
      if (!matched) {
        items.push({ type: 'tool_result_orphan', content: resultText, isError: !!block.is_error });
      }
    }
  }

  return items;
}

function upsertSubAgentGroup(items, groups, info) {
  const keys = [
    info.id ? `id:${info.id}` : '',
    info.toolId ? `tool:${info.toolId}` : '',
    info.name ? `name:${info.name}` : '',
  ].filter(Boolean);
  let group = keys.map((key) => groups[key]).find(Boolean);

  if (!group) {
    group = {
      type: 'subagent_group',
      id: info.id || subAgentIdFromToolId(info.toolId) || info.name || `subagent-${items.length + 1}`,
      name: info.name || '子agent',
      task: '',
      agentType: '',
      status: 'running',
      steps: [],
      result: null,
      isError: false,
    };
    items.push(group);
  }

  if (info.name) group.name = info.name;
  if (info.task) group.task = info.task;
  if (info.agentType) group.agentType = info.agentType;
  if (info.status) group.status = info.status;

  const nextKeys = [
    group.id ? `id:${group.id}` : '',
    info.id ? `id:${info.id}` : '',
    info.toolId ? `tool:${info.toolId}` : '',
    group.name ? `name:${group.name}` : '',
    info.name ? `name:${info.name}` : '',
  ].filter(Boolean);
  for (const key of nextKeys) {
    groups[key] = group;
  }

  return group;
}

function subAgentInfoFromToolUse(block, toolId) {
  const input = block.input || {};
  if (input.kind !== 'subagent' && !isSubAgentToolId(toolId)) return null;
  return {
    id: input.subagent_id || subAgentIdFromToolId(toolId),
    toolId,
    name: input.subagent_name || input.display_name || block.name,
    task: input.task,
    agentType: input.agent_type,
    status: input.status || 'running',
  };
}

function subAgentEventFromBlock(block, text) {
  const metadata = block.metadata || block.payload || {};
  const hasMetadata = metadata.kind === 'subagent_event' || metadata.subagent_id || metadata.subagent_event_type;
  if (hasMetadata) {
    const name = metadata.subagent_name || metadata.display_name || parseSubAgentPrefix(text)?.name;
    return {
      id: metadata.subagent_id,
      toolId: metadata.subagent_id ? `subagent:${metadata.subagent_id}` : undefined,
      name,
      task: metadata.subagent_task || metadata.task,
      agentType: metadata.agent_type || metadata.skill_name,
      status: metadata.subagent_status || metadata.status,
      eventType: metadata.subagent_event_type,
      text: stripSubAgentPrefix(text, name),
    };
  }

  const prefixed = parseSubAgentPrefix(text);
  if (!prefixed) return null;
  return {
    name: prefixed.name,
    text: prefixed.text,
  };
}

function parseSubAgentPrefix(text) {
  const match = String(text || '').trim().match(/^\[(子agent\d+|sub-[^\]\s]+)\]\s*(.*)$/);
  if (!match) return null;
  return {
    name: match[1],
    text: match[2] || '',
  };
}

function stripSubAgentPrefix(text, name) {
  const value = String(text || '').trim();
  if (name && value.startsWith(`[${name}]`)) {
    return value.slice(name.length + 2).trim();
  }
  const parsed = parseSubAgentPrefix(value);
  return parsed ? parsed.text : value;
}

function isSubAgentToolId(toolId) {
  return typeof toolId === 'string' && toolId.startsWith('subagent:');
}

function subAgentIdFromToolId(toolId) {
  return isSubAgentToolId(toolId) ? toolId.slice('subagent:'.length) : '';
}

function workingTextContent(text) {
  const value = messageContentText(text).trim();
  return value.startsWith(WORKING_TEXT_PREFIX)
    ? value.slice(WORKING_TEXT_PREFIX.length).trim()
    : value;
}

function messageContentText(content, fallback = '') {
  if (typeof content === 'string') return content;
  if (content == null) return fallback;
  if (typeof content === 'object' && typeof content.text === 'string') return content.text;
  try {
    return JSON.stringify(content);
  } catch (e) {
    return fallback;
  }
}

function contentBlocksFromMessage(msg) {
  const storedBlocks = Array.isArray(msg?.content_blocks) ? msg.content_blocks : [];
  if (storedBlocks.length > 0) {
    return storedBlocks.map((block) => ({
      ...block,
      metadata: block.metadata || msg?.metadata || null,
    }));
  }

  if (msg?.type === 'thinking') {
    return [{ type: 'thinking', thinking: messageContentText(msg.content), metadata: msg.metadata || null }];
  }
  if (msg?.type === 'tool_use') {
    return [{
      type: 'tool_use',
      id: msg.metadata?.id || msg.metadata?.tool_call_id || msg.metadata?.tool_use_id,
      name: messageContentText(msg.content, 'Tool'),
      input: msg.metadata?.input,
      metadata: msg.metadata || null,
    }];
  }
  if (msg?.type === 'tool_result') {
    return [{
      type: 'tool_result',
      tool_use_id: msg.metadata?.tool_use_id || msg.metadata?.id || msg.metadata?.tool_call_id,
      content: messageContentText(msg.content),
      is_error: !!msg.metadata?.is_error,
      metadata: msg.metadata || null,
    }];
  }
  if (msg?.type === 'text' && typeof msg.content === 'string' && msg.content.trim().startsWith(WORKING_TEXT_PREFIX)) {
    return [{ type: 'assistant_text', text: workingTextContent(msg.content) }];
  }

  return [];
}

function groupWorkingMessages(messages) {
  const blocks = [];
  for (const msg of messages || []) {
    blocks.push(...contentBlocksFromMessage(msg));
  }
  return groupContentBlocks(blocks);
}

function subAgentStatusText(status, isError) {
  if (isError) return '失败';
  switch (status) {
    case 'completed':
      return '已完成';
    case 'failed':
      return '失败';
    case 'stopped':
      return '已停止';
    case 'waiting_for_input':
      return '等待输入';
    default:
      return '运行中';
  }
}

function NestedWorkingStep({ item }) {
  if (item.type === 'thinking' || item.type === 'assistant_text') {
    return (
      <div className="v3-wpi-thinking">
        <Brain size={14} className="v3-wpi-icon" />
        <span className="v3-wpi-text">{item.text}</span>
      </div>
    );
  }

  if (item.type === 'tool_pair') {
    return (
      <div className="v3-wpi-tool">
        <div className="v3-wpi-tool-header">
          <Terminal size={14} className="v3-wpi-icon" />
          <span className="v3-wpi-tool-name">{item.name}</span>
          <span className="oc-wpi-tool-input" style={{ marginLeft: 8, opacity: 0.7, fontSize: 11 }}>
            {toolInputSummary(item.name, item.input)}
          </span>
        </div>
        {item.result != null && (
          <div className="v3-wpi-tool-result">
            <div className="v3-wpi-code-block result">
              <pre><code>{typeof item.result === 'string' ? item.result : JSON.stringify(item.result, null, 2)}</code></pre>
            </div>
          </div>
        )}
      </div>
    );
  }

  if (item.type === 'tool_result_orphan') {
    return (
      <div className="v3-wpi-tool-result">
        <div className="v3-wpi-code-block result">
          <pre><code>{typeof item.content === 'string' ? item.content : JSON.stringify(item.content, null, 2)}</code></pre>
        </div>
      </div>
    );
  }

  return null;
}

function SubAgentWorkingGroup({ item }) {
  const [open, setOpen] = useState(false);
  const steps = item.steps || [];
  const status = subAgentStatusText(item.status, item.isError);

  return (
    <div className="v3-wpi-subagent">
      <button className="v3-wpi-subagent-toggle" type="button" onClick={() => setOpen(!open)}>
        {open ? <ChevronDown size={13} /> : <ChevronRight size={13} />}
        <span className="v3-wpi-subagent-name">{item.name}</span>
        {item.agentType && <span className="v3-wpi-subagent-type">{item.agentType}</span>}
        <span className={`v3-wpi-subagent-status ${item.status || 'running'}`}>{status}</span>
        {!open && steps.length > 0 && <span className="v3-wpi-subagent-count">{steps.length} 步</span>}
      </button>
      {item.task && <div className="v3-wpi-subagent-task">{item.task}</div>}
      {open && (
        <div className="v3-wpi-subagent-steps">
          {steps.map((step, index) => (
            <NestedWorkingStep key={index} item={step} />
          ))}
        </div>
      )}
    </div>
  );
}

function WorkingProcess({ blocks }) {
  const [open, setOpen] = useState(false);
  if (!blocks || blocks.length === 0) return null;

  return (
    <div className="v3-working-process">
      <button className="v3-working-toggle" onClick={() => setOpen(!open)}>
        {open ? <ChevronDown size={14} /> : <ChevronRight size={14} />}
        <span className="v3-working-label">WORKING...</span>
        {!open && <span className="v3-working-hint">展开详情</span>}
      </button>
      {open && (
        <div className="v3-working-steps">
          {blocks.map((item, i) => {
            if (item.type === 'thinking') {
              return (
                <div key={i} className="v3-wpi-thinking">
                  <Brain size={14} className="v3-wpi-icon" />
                  <span className="v3-wpi-text">{item.text}</span>
                </div>
              );
            }
            if (item.type === 'assistant_text') {
              return (
                <div key={i} className="v3-wpi-thinking">
                  <Brain size={14} className="v3-wpi-icon" />
                  <span className="v3-wpi-text">{item.text}</span>
                </div>
              );
            }
            if (item.type === 'subagent_group') {
              return <SubAgentWorkingGroup key={i} item={item} />;
            }
            if (item.type === 'tool_pair') {
              return (
                <div key={i} className="v3-wpi-tool">
                  <div className="v3-wpi-tool-header">
                    <Terminal size={14} className="v3-wpi-icon" />
                    <span className="v3-wpi-tool-name">{item.name}</span>
                    <span className="oc-wpi-tool-input" style={{ marginLeft: 8, opacity: 0.7, fontSize: 11 }}>
                      {toolInputSummary(item.name, item.input)}
                    </span>
                  </div>
                  {item.result != null && (
                    <div className="v3-wpi-tool-result">
                      <div className="v3-wpi-code-block result">
                        <pre><code>{typeof item.result === 'string' ? item.result : JSON.stringify(item.result, null, 2)}</code></pre>
                      </div>
                    </div>
                  )}
                </div>
              );
            }
            if (item.type === 'tool_result_orphan') {
              return (
                <div key={i} className="v3-wpi-tool-result">
                  <div className="v3-wpi-code-block result">
                     <pre><code>{typeof item.content === 'string' ? item.content : JSON.stringify(item.content, null, 2)}</code></pre>
                  </div>
                </div>
              );
            }
            return null;
          })}
        </div>
      )}
    </div>
  );
}

function ChatMessageComponent({ message, workingMessages = null, isSelf, isGroup, senderName, senderAvatarUrl, senderIsBot, replyMessage, onReply, showThinking = true, isConsecutive, onPreviewFile, activePreviewFile }) {
  const content = message.content;
  const effectiveWorkingMessages = workingMessages || message._working || [];
  const storedBlocks = useMemo(() => Array.isArray(message.content_blocks) ? message.content_blocks : [], [message.content_blocks]);
  const workingBlocks = useMemo(() => {
    if (effectiveWorkingMessages.length > 0) {
      return groupWorkingMessages(effectiveWorkingMessages);
    }
    if (storedBlocks.length > 0) {
      return groupContentBlocks(storedBlocks);
    }
    return [];
  }, [effectiveWorkingMessages, storedBlocks]);
  const richBlocks = useMemo(() => (
    storedBlocks.filter((block) => block.type === 'image' || block.type === 'file')
  ), [storedBlocks]);
  const renderedTextContent = useMemo(() => {
    if (storedBlocks.length === 0) return content;
    return storedBlocks
      .filter((block) => block.type === 'text' && block.text)
      .map((block) => block.text)
      .join('\n\n');
  }, [storedBlocks, content]);
  const hasText = useMemo(() => (
    typeof renderedTextContent === 'string'
      ? renderedTextContent.trim().length > 0
      : renderedTextContent != null
  ), [renderedTextContent]);

  const parsed = useMemo(() => {
    if (storedBlocks.length > 0) return null;
    if (typeof content === 'object' && content !== null && content.type) {
      return content;
    }
    if (typeof content === 'string') {
      try {
        const obj = JSON.parse(content);
        if (obj && obj.type) return obj;
      } catch (e) {
        // plain text
      }
    }
    return null;
  }, [storedBlocks, content]);

  const timeString = useMemo(() => (
    new Date(message.created_at || Date.now()).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
  ), [message.created_at]);
  const displayName = senderName || message.from_name || `User ${message.from_uid || ''}`;

  if (!hasText && richBlocks.length === 0 && workingBlocks.length === 0) return null;

  return (
    <div className={`v3-message ${isConsecutive ? 'grouped' : ''}`}>
      <div className="v3-message-actions">
        <button className="v3-action-btn" aria-label="Add Reaction" type="button">
          <SmilePlus size={14} />
        </button>
        {onReply && (
          <button className="v3-action-btn" onClick={onReply} aria-label="Reply" type="button">
            <CornerUpLeft size={14} />
          </button>
        )}
        <button className="v3-action-btn" aria-label="More Options" type="button">
          <MoreHorizontal size={14} />
        </button>
      </div>

      <div className="v3-avatar-col">
        {isConsecutive ? (
          timeString
        ) : (
          <Avatar
            name={displayName}
            src={senderAvatarUrl}
            size={36}
            isBot={senderIsBot}
            className={`v3-avatar ${senderIsBot ? 'bot' : ''}`}
            style={{ borderRadius: 4, background: senderIsBot ? 'linear-gradient(135deg, #a855f7 0%, #ec4899 100%)' : '#E8E8E8', color: senderIsBot ? '#fff' : '#333' }}
          />
        )}
      </div>

      <div className="v3-msg-body">
        {!isConsecutive && (
          <div className="v3-msg-header">
            <span className="v3-msg-name">{displayName}</span>
            <span className="v3-msg-time">{timeString}</span>
          </div>
        )}

        {replyMessage && (
          <div style={{ padding: '4px 8px', background: 'rgba(255,255,255,0.05)', borderRadius: 4, marginBottom: 4, fontSize: 13, color: '#aaa', borderLeft: '3px solid var(--v3-primary)', width: 'fit-content' }}>
            <span style={{opacity: 0.8}}>
              {typeof replyMessage.content === 'string' ? replyMessage.content.slice(0, 80) : '[media]'}
            </span>
          </div>
        )}

        {!isSelf && showThinking && <WorkingProcess blocks={workingBlocks} />}

        {(hasText || richBlocks.length > 0) && (
          <div style={{lineHeight: 1.46}}>
            {hasText && (parsed ? (
              <RichContent
                content={parsed}
                onPreviewFile={onPreviewFile}
                activePreviewFile={activePreviewFile}
              />
            ) : <TextContent content={renderedTextContent} isGroup={isGroup} />)}
            {richBlocks.map((block, index) => (
              <RichContent
                key={`${block.type}-${index}`}
                content={block}
                onPreviewFile={onPreviewFile}
                activePreviewFile={activePreviewFile}
              />
            ))}
            {message._streaming && <span className="oc-streaming-cursor" aria-hidden="true">|</span>}
          </div>
        )}
      </div>
    </div>
  );
}

const ChatMessage = memo(ChatMessageComponent, (prevProps, nextProps) => {
  return prevProps.message === nextProps.message &&
    prevProps.workingMessages === nextProps.workingMessages &&
    prevProps.isSelf === nextProps.isSelf &&
    prevProps.isGroup === nextProps.isGroup &&
    prevProps.senderName === nextProps.senderName &&
    prevProps.senderAvatarUrl === nextProps.senderAvatarUrl &&
    prevProps.senderIsBot === nextProps.senderIsBot &&
    prevProps.replyMessage === nextProps.replyMessage &&
    prevProps.showThinking === nextProps.showThinking &&
    prevProps.isConsecutive === nextProps.isConsecutive &&
    prevProps.onPreviewFile === nextProps.onPreviewFile &&
    prevProps.activePreviewFile === nextProps.activePreviewFile;
});

export default ChatMessage;

function TextContent({ content, isGroup }) {
  const text = useMemo(() => messageContentText(content), [content]);

  const markdownHtml = useMemo(() => {
    const hasMarkdown = /(\*\*|__|`|#{1,6}\s|^\s*[-*+]\s|^\s*\d+\.\s|\[.*\]\(.*\))/m.test(text);
    if (!hasMarkdown) return null;
    try {
      return renderSafeMarkdown(text);
    } catch (e) {
      console.error('Markdown parse error:', e);
      return null;
    }
  }, [text]);

  if (markdownHtml) {
    return <div dangerouslySetInnerHTML={{ __html: markdownHtml }} className="oc-markdown" />;
  }

  if (isGroup) {
    const parts = text.split(/(@usr\d+)/g);
    return (
      <span>
        {parts.map((part, i) =>
          part.match(/^@usr\d+$/) ? (
            <span key={i} className="oc-mention">{part}</span>
          ) : (
            <span key={i}>{part}</span>
          )
        )}
      </span>
    );
  }

  return <span style={{ whiteSpace: 'pre-wrap' }}>{text}</span>;
}

function RichContent({ content, onPreviewFile, activePreviewFile }) {
  switch (content.type) {
    case 'image':
      return <ImageContent payload={content.payload} />;
    case 'file':
      return <FileContent payload={content.payload} onPreviewFile={onPreviewFile} activePreviewFile={activePreviewFile} />;
    case 'link_preview':
      return <LinkPreviewContent payload={content.payload} />;
    case 'card':
      return <CardContent payload={content.payload} />;
    default:
      return <TextContent content={content.payload?.text || JSON.stringify(content)} />;
  }
}

function ImageContent({ payload }) {
  const [expanded, setExpanded] = useState(false);
  if (!payload) return null;
  const src = payload.url || payload.thumbnail;
  return (
    <div className="oc-rich-image">
      <img
        src={resolveMediaURL(src)}
        alt="image"
        className="oc-rich-image-thumb"
        onClick={() => setExpanded(true)}
        style={{ maxWidth: 240, maxHeight: 240, borderRadius: 4, cursor: 'pointer' }}
      />
      {expanded && (
        <div className="oc-modal-overlay" onClick={() => setExpanded(false)}>
          <img src={resolveMediaURL(payload.url || src)} alt="full" style={{ maxWidth: '90vw', maxHeight: '90vh', borderRadius: 8 }} />
        </div>
      )}
    </div>
  );
}

function fileExtension(payload) {
  const name = payload?.name || payload?.url || '';
  const raw = name.includes('.') ? name.slice(name.lastIndexOf('.') + 1) : '';
  return raw ? raw.toUpperCase() : 'FILE';
}

function fileMimeType(payload) {
  return String(payload?.mime_type || payload?.mime || payload?.content_type || '').toLowerCase();
}

function isHtmlFile(payload, ext = fileExtension(payload)) {
  const mime = fileMimeType(payload);
  return HTML_FILE_EXTENSIONS.has(ext) || mime === 'text/html' || mime === 'application/xhtml+xml';
}

function isMarkdownFile(payload, ext = fileExtension(payload)) {
  const mime = fileMimeType(payload);
  return ext === 'MD' || mime === 'text/markdown' || mime === 'text/x-markdown';
}

function isPdfFile(payload, ext = fileExtension(payload)) {
  return ext === 'PDF' || fileMimeType(payload) === 'application/pdf';
}

function isDocxFile(payload, ext = fileExtension(payload)) {
  const mime = fileMimeType(payload);
  return ext === 'DOCX' ||
    mime === 'application/vnd.openxmlformats-officedocument.wordprocessingml.document';
}

function isPreviewableFile(payload, ext = fileExtension(payload)) {
  const mime = fileMimeType(payload);
  if (PREVIEW_FILE_EXTENSIONS.has(ext) || isPdfFile(payload, ext)) return true;
  return mime.startsWith('text/') || mime === 'application/json' || mime === 'application/xml';
}

function artifactMeta(payload, ext = fileExtension(payload)) {
  if (isHtmlFile(payload, ext)) {
    return {
      label: 'HTML',
      className: 'report',
      subtitle: '可预览的工作流产物',
    };
  }
  if (isPdfFile(payload, ext)) {
    return {
      label: 'PDF',
      className: 'report',
      subtitle: '报告文件',
    };
  }
  if (isDocxFile(payload, ext)) {
    return {
      label: 'Word',
      className: 'document',
      subtitle: '可下载的文档',
    };
  }
  if (isMarkdownFile(payload, ext)) {
    return {
      label: 'Markdown',
      className: 'document',
      subtitle: '文档产物',
    };
  }
  if (ext === 'CSV' || fileMimeType(payload) === 'text/csv') {
    return {
      label: 'CSV',
      className: 'dataset',
      subtitle: '表格数据',
    };
  }
  return {
    label: ext,
    className: 'document',
    subtitle: '文件',
  };
}

function fetchableMediaURL(url) {
  if (!url) return '';
  try {
    const urlObj = new URL(url, window.location.origin);
    return urlObj.pathname + urlObj.search;
  } catch (e) {
    return url;
  }
}

function isTrustedPreviewURL(url) {
  if (!url) return false;
  try {
    const urlObj = new URL(url, window.location.origin);
    const mediaOrigin = new URL(resolveMediaURL('/'), window.location.origin).origin;
    const host = window.location.hostname;
    const isLocalDev = host === 'localhost' || host === '127.0.0.1';
    const trustedOrigin = (
      urlObj.origin === window.location.origin ||
      urlObj.origin === mediaOrigin ||
      (isLocalDev && urlObj.hostname.endsWith('catsco.cc'))
    );
    const trustedPath = /^\/uploads\/(files|images|feedback)\//.test(urlObj.pathname) ||
      (isLocalDev && urlObj.pathname.startsWith('/demo-artifacts/'));
    return trustedOrigin && trustedPath;
  } catch (e) {
    return String(url).startsWith('/uploads/') || String(url).startsWith('/demo-artifacts/');
  }
}

function previewFileDescriptor(payload) {
  if (!payload) return null;
  const url = resolveMediaURL(payload.url);
  const ext = fileExtension(payload);
  const meta = artifactMeta(payload, ext);
  const isPdf = isPdfFile(payload, ext);
  const isHtml = isHtmlFile(payload, ext);
  const isMarkdown = isMarkdownFile(payload, ext);
  const canPreview = isPreviewableFile(payload, ext) && isTrustedPreviewURL(url);
  return {
    payload,
    url,
    ext,
    meta,
    isPdf,
    isHtml,
    isMarkdown,
    canPreview,
    sizeStr: payload.size ? formatFileSize(payload.size) : '',
    key: `${url}|${payload.name || ''}|${payload.size || ''}`,
  };
}

function FileContent({ payload, onPreviewFile, activePreviewFile }) {
  if (!payload) return null;
  const descriptor = previewFileDescriptor(payload);
  const { url, ext, canPreview, meta, sizeStr, key } = descriptor;
  const activeKey = activePreviewFile ? previewFileDescriptor(activePreviewFile)?.key : '';
  const isActive = canPreview && activeKey === key;
  const subtitle = [meta.label, meta.subtitle, sizeStr, fileMimeType(payload) || ext].filter(Boolean).join(' · ');
  const openFile = () => {
    if (canPreview && onPreviewFile) onPreviewFile(payload);
    else if (url) window.open(url, '_blank');
  };

  return (
    <div
      className={`v3-attachment-card v3-artifact-card ${meta.className}${isActive ? ' active' : ''}`}
    >
      <button
        className="v3-artifact-main"
        onClick={openFile}
        title={canPreview ? '预览文件' : '打开或下载文件'}
        type="button"
      >
        <div className="v3-attachment-icon">
          <FileText size={18} strokeWidth={1.5} />
        </div>
        <div className="v3-attachment-info">
          <span className="v3-attachment-name" title={payload.name || 'File'}>{payload.name || 'File'}</span>
          <span className="v3-attachment-size">{subtitle}</span>
        </div>
      </button>
      <div className="v3-artifact-actions">
        <button
          className="v3-artifact-action"
          disabled={!canPreview}
          onClick={openFile}
          title={canPreview ? '预览' : '暂不支持预览'}
          type="button"
        >
          <Eye size={15} />
          <span>预览</span>
        </button>
        <a
          className="v3-artifact-action"
          href={url || undefined}
          download
          onClick={(event) => event.stopPropagation()}
          rel="noopener noreferrer"
          target="_blank"
          title="下载"
        >
          <Download size={15} />
          <span>下载</span>
        </a>
      </div>
    </div>
  );
}

export function FilePreviewPanel({ file, onClose }) {
  const [preview, setPreview] = useState(false);
  const [textContent, setTextContent] = useState(null);
  const [loadingText, setLoadingText] = useState(false);
  const [previewError, setPreviewError] = useState('');

  const descriptor = useMemo(() => previewFileDescriptor(file), [file]);
  const url = descriptor?.url || '';
  const isPdf = descriptor?.isPdf || false;
  const isHtml = descriptor?.isHtml || false;
  const isMarkdown = descriptor?.isMarkdown || false;
  const meta = descriptor?.meta || artifactMeta(file || {});
  const sizeStr = descriptor?.sizeStr || '';

  useEffect(() => {
    let cancelled = false;
    setPreview(Boolean(file));
    setTextContent(null);
    setPreviewError('');
    if (!file || !descriptor?.canPreview || isPdf) {
      setLoadingText(false);
      return () => {
        cancelled = true;
      };
    }

    const load = async () => {
      setLoadingText(true);
      try {
        const res = await fetch(fetchableMediaURL(url));
        if (!res.ok) throw new Error(`HTTP Error ${res.status}`);
        const text = await res.text();
        if (!cancelled) setTextContent(text);
      } catch (err) {
        if (!cancelled) setPreviewError(`预览加载失败：${err.message}`);
      } finally {
        if (!cancelled) setLoadingText(false);
      }
    };
    load();

    return () => {
      cancelled = true;
    };
  }, [descriptor?.canPreview, file, isPdf, url]);

  if (!preview || !file) return null;
  if (!descriptor?.canPreview) return null;

  return (
    <aside className={`v3-file-preview-panel ${isHtml || isPdf ? 'wide' : ''}`} aria-label="文件预览">
      <div className="v3-file-preview-header">
        <div className="v3-file-preview-title">
          <FileText size={18} />
          <div>
            <h3>{file.name}</h3>
            <span>{meta.label}{sizeStr ? ` · ${sizeStr}` : ''}</span>
          </div>
        </div>
        <div className="v3-file-preview-actions">
          <a href={url} download title="下载原文件" target="_blank" rel="noopener noreferrer">
            <Download size={18} />
          </a>
          <button aria-label="关闭预览" onClick={onClose} type="button">
            <X size={18} />
          </button>
        </div>
      </div>
      <div className="v3-file-preview-body">
        {isPdf ? (
          <iframe src={url} className="v3-file-preview-frame" title="PDF Preview" />
        ) : loadingText ? (
          <div className="v3-file-preview-state">加载中...</div>
        ) : previewError ? (
          <div className="v3-file-preview-state error">{previewError}</div>
        ) : isHtml ? (
          <iframe
            className="v3-file-preview-frame"
            title="HTML Report Preview"
            sandbox=""
            referrerPolicy="no-referrer"
            srcDoc={textContent || '<!doctype html><meta charset="utf-8"><body></body>'}
          />
        ) : isMarkdown ? (
          <iframe
            className="v3-file-preview-frame"
            title="Markdown Preview"
            sandbox=""
            referrerPolicy="no-referrer"
            srcDoc={markdownPreviewDocument(textContent || '')}
          />
        ) : (
          <pre className="v3-file-preview-text">{textContent || '暂无可预览内容。'}</pre>
        )}
      </div>
    </aside>
  );
}

function LinkPreviewContent({ payload }) {
  if (!payload) return null;
  return (
    <a href={resolveMediaURL(payload.url)} target="_blank" rel="noopener noreferrer" className="oc-rich-link" style={{ textDecoration: 'none', color: 'inherit' }}>
      {payload.image && <img src={resolveMediaURL(payload.image)} alt="" style={{ width: '100%', maxHeight: 160, objectFit: 'cover', borderRadius: '4px 4px 0 0' }} />}
      <div style={{ padding: '8px 0' }}>
        <div style={{ fontWeight: 500, fontSize: 14 }}>{payload.title || payload.url}</div>
        {payload.description && <div style={{ fontSize: 12, color: '#888', marginTop: 4 }}>{payload.description}</div>}
        {payload.site_name && <div style={{ fontSize: 11, color: '#aaa', marginTop: 4 }}>{payload.site_name}</div>}
      </div>
    </a>
  );
}

function CardContent({ payload }) {
  if (!payload) return null;
  return (
    <div className="oc-rich-card">
      {payload.image && <img src={resolveMediaURL(payload.image)} alt="" style={{ width: '100%', maxHeight: 120, objectFit: 'cover', borderRadius: '4px 4px 0 0' }} />}
      <div style={{ padding: 8 }}>
        <div style={{ fontWeight: 600, fontSize: 14 }}>{payload.title}</div>
        {payload.text && <div style={{ fontSize: 13, color: '#666', marginTop: 4 }}>{payload.text}</div>}
      </div>
      {payload.buttons && payload.buttons.length > 0 && (
        <div className="oc-rich-card-buttons">
          {payload.buttons.map((btn, i) => (
            <button
              key={i}
              className="oc-btn oc-btn-default"
              onClick={() => {
                if (btn.action === 'url') window.open(btn.value, '_blank');
                if (btn.action === 'copy') navigator.clipboard?.writeText(btn.value);
              }}
              style={{ flex: 1 }}
            >
              {btn.label}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}

function formatFileSize(bytes) {
  if (bytes < 1024) return bytes + ' B';
  if (bytes < 1024 * 1024) return (bytes / 1024).toFixed(1) + ' KB';
  return (bytes / (1024 * 1024)).toFixed(1) + ' MB';
}
