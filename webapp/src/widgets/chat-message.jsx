import React, { memo, useEffect, useId, useMemo, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import { ArrowLeft, ChevronDown, ChevronLeft, ChevronRight, Terminal, Brain, MessageSquareText, FileText, FileCode2, Download, ExternalLink, CornerUpLeft, Pencil, X, Eye, Copy, RotateCcw, CheckCircle2, CircleDot, Circle, Play, Volume2, ImageDown, MoreHorizontal } from 'lucide-react';
import t from '../i18n';
import Avatar from './avatar';
import { resolveMediaURL } from '../api';
import { responseErrorMessage } from '../auth-session';
import { canDragChatAttachment, clearChatAttachmentDrag, writeChatAttachmentDrag } from '../chat-attachment-drag';
import {
  hasPlainTextTableLikeBlock,
  hasRenderableTable,
  markdownPreviewDocument,
  renderSafeMarkdown,
  shouldRenderMarkdown,
} from './markdown-utils';
import { SpreadsheetPreview, SPREADSHEET_PREVIEW_MAX_BYTES } from './spreadsheet-preview';
import MobilePdfPreview from './mobile-pdf-preview';
import { artifactRefFromPreviewFile, requestArtifactPageContext } from '../artifact-context';
import PwaDownloadLink from './pwa-download-link';

const WORKING_TEXT_PREFIX = 'AI文本:';
const HIDDEN_TOOL_PROGRESS_NAMES = new Set([
  'send_text',
  'send_file',
]);
const HTML_FILE_EXTENSIONS = new Set(['HTML', 'HTM', 'XHTML']);
const TEXT_FILE_EXTENSIONS = new Set(['TXT', 'JSON', 'MD', 'CSV', 'JS', 'PY', 'GO', 'HTML', 'HTM', 'CSS', 'XML']);
const PREVIEW_FILE_EXTENSIONS = new Set(['PDF', ...TEXT_FILE_EXTENSIONS]);
const SPREADSHEET_FILE_EXTENSIONS = new Set(['CSV', 'XLS', 'XLSX']);
const SPREADSHEET_MIME_TYPES = new Set([
  'text/csv',
  'application/csv',
  'application/vnd.ms-excel',
  'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet',
]);
const HTML_PREVIEW_SANDBOX = 'allow-scripts allow-forms allow-popups allow-modals';
const FOCUSABLE_SELECTOR = 'button:not([disabled]), [href], input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';
const REMOTE_ARTIFACT_PREVIEW_SANDBOX = `${HTML_PREVIEW_SANDBOX} allow-same-origin`;
const REMOTE_ARTIFACT_REFRESH_TIMEOUT_MS = 4000;
const REMOTE_ARTIFACT_REFRESH_HANDSHAKE_TIMEOUT_MS = 1200;
const trustedArtifactPreviewPayloads = new WeakSet();

function imageGalleryItemId(message, blockIndex, payload) {
  const src = payload?.url || payload?.thumbnail || '';
  if (!src) return '';
  return `${message?.id || message?.seq_id || 'message'}:${blockIndex}:${src}`;
}

function remoteArtifactPreviewKey(file, descriptor) {
  if (!descriptor?.isRemoteArtifact || !file?.artifact_id || !descriptor.url) return '';
  return [
    Number(file.artifact_agent_uid || 0),
    String(file.artifact_id),
    Number(file.publish_version || 0),
    descriptor.url,
  ].join('|');
}

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

function normalizePlanStatus(status) {
  const value = String(status || '').trim().toLowerCase();
  if (value === 'completed' || value === 'complete' || value === 'done' || value.includes('已完成') || value === '完成') {
    return 'completed';
  }
  if (value === 'in_progress' || value === 'in-progress' || value === 'running' || value.includes('进行中')) {
    return 'in_progress';
  }
  return 'pending';
}

function planStepText(step) {
  if (!step) return '';
  if (typeof step === 'string') return step.trim();
  return String(step.text || step.step || step.title || step.name || '').trim();
}

function planFromUpdatePlanInput(input) {
  const rawSteps = Array.isArray(input?.steps)
    ? input.steps
    : Array.isArray(input?.plan)
      ? input.plan
      : [];
  const steps = rawSteps
    .map((step) => ({
      status: normalizePlanStatus(typeof step === 'object' ? step.status : ''),
      text: planStepText(step),
    }))
    .filter((step) => step.text);
  return steps.length > 0 ? { steps } : null;
}

function planFromUpdatePlanResult(result) {
  if (!result) return null;
  const text = typeof result === 'string' ? result : JSON.stringify(result);
  const steps = [];
  const lines = text.split(/\r?\n/).map((line) => line.trim()).filter(Boolean);
  for (const line of lines) {
    const numbered = line.match(/^\d+[.)、]?\s*(已完成|完成|进行中|待处理|pending|in_progress|completed|done)\s*[-:：]\s*(.+)$/i);
    if (numbered) {
      steps.push({
        status: normalizePlanStatus(numbered[1]),
        text: numbered[2].trim(),
      });
      continue;
    }
    const current = line.match(/^进行中[:：]\s*(.+)$/);
    if (current && !steps.some((step) => step.status === 'in_progress')) {
      steps.push({
        status: 'in_progress',
        text: current[1].trim(),
      });
    }
  }
  return steps.length > 0 ? { steps } : null;
}

function planFromUpdatePlanTool(item) {
  if (String(item?.name || '').trim() !== 'update_plan') return null;
  return planFromUpdatePlanInput(item.input) || planFromUpdatePlanResult(item.result);
}

function collapsePlanUpdates(items) {
  const collapsed = [];
  let planIndex = -1;

  for (const originalItem of items || []) {
    const item = originalItem?.type === 'subagent_group'
      ? { ...originalItem, steps: collapsePlanUpdates(originalItem.steps || []) }
      : originalItem;
    const isPlan = item?.type === 'tool_pair' && planFromUpdatePlanTool(item);

    if (!isPlan) {
      collapsed.push(item);
      continue;
    }

    if (planIndex === -1) {
      planIndex = collapsed.length;
      collapsed.push(item);
    } else {
      collapsed[planIndex] = item;
    }
  }

  return collapsed;
}

function attachNarrativesToFollowingTools(items) {
  const grouped = [];
  let pendingNarratives = [];

  const flushNarratives = () => {
    if (pendingNarratives.length === 0) return;
    grouped.push(...pendingNarratives);
    pendingNarratives = [];
  };

  for (const item of items || []) {
    if (item?.type === 'thinking' || item?.type === 'assistant_text') {
      pendingNarratives.push(item);
      continue;
    }
    if (item?.type === 'tool_pair' && !planFromUpdatePlanTool(item)) {
      grouped.push({
        ...item,
        narratives: pendingNarratives,
      });
      pendingNarratives = [];
      continue;
    }
    flushNarratives();
    grouped.push(item);
  }

  flushNarratives();
  return grouped;
}

function latestWorkingPlan(items) {
  const item = latestWorkingPlanItem(items);
  return item ? planFromUpdatePlanTool(item) : null;
}

function latestWorkingPlanItem(items) {
  for (let index = (items?.length || 0) - 1; index >= 0; index -= 1) {
    const item = items[index];
    if (item?.type === 'tool_pair' && planFromUpdatePlanTool(item)) {
      return item;
    }
  }
  return null;
}

function isPlanComplete(plan) {
  return Boolean(
    plan?.steps?.length > 0
    && plan.steps.every((step) => step.status === 'completed'),
  );
}

function contentBlockCopyText(block) {
  if (!block || typeof block !== 'object') return '';
  if (block.type === 'text') return block.text || block.content || '';
  const payload = block.payload || block;
  if (block.type === 'file') {
    return `[文件] ${payload.name || payload.url || '文件'}`;
  }
  if (block.type === 'image') {
    return `[图片] ${payload.name || payload.url || '图片'}`;
  }
  if (block.type === 'audio' || block.type === 'voice') {
    return `[语音] ${payload.name || payload.url || '语音消息'}`;
  }
  return '';
}

function buildMessageCopyText(content, renderedTextContent, richBlocks, parsed) {
  const parts = [];
  if (typeof renderedTextContent === 'string' && renderedTextContent.trim()) {
    parts.push(renderedTextContent.trim());
  } else if (!parsed && renderedTextContent != null && typeof renderedTextContent !== 'string') {
    parts.push(JSON.stringify(renderedTextContent));
  }

  if (parsed && parsed.type) {
    const parsedText = contentBlockCopyText(parsed);
    if (parsedText) parts.push(parsedText);
  }

  richBlocks.forEach((block) => {
    const text = contentBlockCopyText(block);
    if (text) parts.push(text);
  });

  if (parts.length === 0) {
    if (typeof content === 'string') return content;
    if (content != null) return JSON.stringify(content);
  }
  return parts.join('\n\n');
}

async function copyTextToClipboard(text) {
  if (!text) return;
  if (navigator.clipboard?.writeText) {
    await navigator.clipboard.writeText(text);
    return;
  }
  const textarea = document.createElement('textarea');
  textarea.value = text;
  textarea.setAttribute('readonly', '');
  textarea.style.position = 'fixed';
  textarea.style.left = '-9999px';
  document.body.appendChild(textarea);
  textarea.select();
  document.execCommand('copy');
  document.body.removeChild(textarea);
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
  return attachNarrativesToFollowingTools(collapsePlanUpdates(items));
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
    if (block.type === 'text' && block.presentation_role === 'process') {
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

  return attachNarrativesToFollowingTools(collapsePlanUpdates(items));
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

const ARTIFACT_DELIVERY_ANNOUNCEMENTS = new Set([
  '已发出',
  '已发出。',
  '已发送',
  '已发送。',
  '已生成',
  '已生成。',
  '已交付',
  '已交付。',
]);

function removeRedundantArtifactAnnouncement(text, artifactBlocks) {
  const artifactNames = (artifactBlocks || [])
    .map((block) => String(block?.payload?.name || '').trim())
    .filter(Boolean);
  if (artifactNames.length === 0 || typeof text !== 'string') return text;

  return text
    .split('\n')
    .filter((line) => {
      const trimmedLine = line.trim();
      if (!trimmedLine) return true;
      return !artifactNames.some((name) => {
        if (!trimmedLine.startsWith(name)) return false;
        const announcement = trimmedLine.slice(name.length).trim();
        return ARTIFACT_DELIVERY_ANNOUNCEMENTS.has(announcement);
      });
    })
    .join('\n')
    .replace(/\n{3,}/g, '\n\n')
    .trim();
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
  if (
    msg?.type === 'text'
    && typeof msg.content === 'string'
    && (
      msg.content.trim().startsWith(WORKING_TEXT_PREFIX)
      || msg._display_text_role === 'process'
    )
  ) {
    return [{
      type: 'assistant_text',
      text: workingTextContent(msg.content),
    }];
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

function serializeToolResult(value) {
  if (typeof value === 'string') return value;
  try {
    return JSON.stringify(value, null, 2);
  } catch (error) {
    return String(value ?? '');
  }
}

function toolResultSizeLabel(value) {
  if (typeof value !== 'string') return '结构化结果';
  if (!value) return '';
  if (value.length < 1000) return `${value.length} 字符`;
  return `${(value.length / 1000).toFixed(value.length >= 10000 ? 0 : 1)}k 字符`;
}

function WorkingToolStep({ item, orphan = false }) {
  const [open, setOpen] = useState(false);
  const result = orphan ? item.content : item.result;
  const hasResult = result != null && (typeof result !== 'string' || result.length > 0);
  const name = orphan ? '工具结果' : (item.name || 'Tool');
  const resultID = `tool-result-${useId().replace(/:/g, '')}`;
  const narratives = item.narratives || [];

  if (!hasResult) {
    return (
      <div className="v3-wpi-tool-step">
        {narratives.map((narrative, index) => (
          <WorkingNarrative key={`${narrative.type}-${index}`} item={narrative} />
        ))}
        <div className="v3-wpi-tool">
          <div className="v3-wpi-tool-header">
            <Terminal size={14} className="v3-wpi-icon" />
            <span className="v3-wpi-tool-name">{name}</span>
            {!orphan && (
              <span className="oc-wpi-tool-input">
                {toolInputSummary(item.name, item.input)}
              </span>
            )}
          </div>
        </div>
      </div>
    );
  }

  return (
    <div className="v3-wpi-tool-step">
      {narratives.map((narrative, index) => (
        <WorkingNarrative key={`${narrative.type}-${index}`} item={narrative} />
      ))}
      <div className="v3-wpi-tool">
        <button
          type="button"
          className="v3-wpi-tool-header is-toggle"
          aria-expanded={open}
          aria-controls={resultID}
          onClick={() => setOpen((current) => !current)}
        >
          {open ? <ChevronDown size={14} /> : <ChevronRight size={14} />}
          <Terminal size={14} className="v3-wpi-icon" />
          <span className="v3-wpi-tool-name">{name}</span>
          {!orphan && (
            <span className="oc-wpi-tool-input">
              {toolInputSummary(item.name, item.input)}
            </span>
          )}
          <span className="v3-wpi-tool-size">{toolResultSizeLabel(result)}</span>
        </button>
        {open && (
          <div id={resultID} className="v3-wpi-tool-result">
            <div className="v3-wpi-code-block result">
              <pre><code>{serializeToolResult(result)}</code></pre>
            </div>
          </div>
        )}
      </div>
    </div>
  );
}

function WorkingNarrative({ item }) {
  const NarrativeIcon = item.type === 'thinking' ? Brain : MessageSquareText;
  return (
    <div className={`v3-wpi-thinking v3-wpi-narrative is-${item.type}`}>
      <NarrativeIcon size={14} className="v3-wpi-icon" aria-hidden="true" />
      <span className="v3-wpi-text">{item.text}</span>
    </div>
  );
}

function NestedWorkingStep({ item }) {
  if (item.type === 'thinking' || item.type === 'assistant_text') {
    return <WorkingNarrative item={item} />;
  }

  if (item.type === 'tool_pair') {
    const plan = planFromUpdatePlanTool(item);
    return plan ? <WorkingPlanCard item={item} /> : <WorkingToolStep item={item} />;
  }

  if (item.type === 'tool_result_orphan') {
    return <WorkingToolStep item={item} orphan />;
  }

  return null;
}

function SubAgentWorkingGroup({ item }) {
  const [open, setOpen] = useState(false);
  const stepsID = `subagent-steps-${useId().replace(/:/g, '')}`;
  const steps = item.steps || [];
  const status = subAgentStatusText(item.status, item.isError);

  return (
    <div className="v3-wpi-subagent">
      <button
        className="v3-wpi-subagent-toggle"
        type="button"
        aria-expanded={open}
        aria-controls={stepsID}
        onClick={() => setOpen(!open)}
      >
        {open
          ? <ChevronDown size={13} aria-hidden="true" />
          : <ChevronRight size={13} aria-hidden="true" />}
        <span className="v3-wpi-subagent-name">{item.name}</span>
        {item.agentType && <span className="v3-wpi-subagent-type">{item.agentType}</span>}
        <span className={`v3-wpi-subagent-status ${item.status || 'running'}`}>{status}</span>
        {!open && steps.length > 0 && <span className="v3-wpi-subagent-count">{steps.length} 步</span>}
      </button>
      {item.task && <div className="v3-wpi-subagent-task">{item.task}</div>}
      {open && (
        <div
          id={stepsID}
          className="v3-wpi-subagent-steps"
          role="region"
          aria-label={`${item.name} 的执行步骤`}
        >
          {steps.map((step, index) => (
            <NestedWorkingStep key={index} item={step} />
          ))}
        </div>
      )}
    </div>
  );
}

function WorkingPlanCard({ item }) {
  const plan = planFromUpdatePlanTool(item);
  if (!plan) return null;
  const completed = plan.steps.filter((step) => step.status === 'completed').length;

  return (
    <div className="v3-wpi-plan" role="status">
      <div className="v3-wpi-plan-header">
        <FileText size={14} className="v3-wpi-icon" />
        <span className="v3-wpi-plan-title">计划</span>
        <span className="v3-wpi-plan-count">{completed}/{plan.steps.length}</span>
      </div>
      <div className="v3-wpi-plan-steps">
        {plan.steps.map((step, index) => (
          <div className={`v3-wpi-plan-step ${step.status}`} key={`${index}-${step.text}`}>
            {step.status === 'completed'
              ? <CheckCircle2 size={14} />
              : step.status === 'in_progress'
                ? <CircleDot size={14} />
                : <Circle size={14} />}
            <span>{step.text}</span>
          </div>
        ))}
      </div>
    </div>
  );
}

function WorkingProcess({ blocks, complete: completeOverride = false }) {
  const [open, setOpen] = useState(false);
  const triggerRef = useRef(null);
  const stepsID = `working-steps-${useId().replace(/:/g, '')}`;
  const safeBlocks = blocks || [];
  const planItem = latestWorkingPlanItem(safeBlocks);
  const plan = planItem ? planFromUpdatePlanTool(planItem) : null;
  const detailBlocks = safeBlocks.filter((item) => item !== planItem);
  const hasDetails = detailBlocks.length > 0;
  const completedPlanSteps = plan?.steps?.filter((step) => step.status === 'completed').length || 0;
  const complete = completeOverride || isPlanComplete(plan);
  const statusLabel = complete ? '已完成' : '正在执行';
  const lastTool = [...detailBlocks].reverse().find((item) => item.type === 'tool_pair');
  const summary = plan
    ? `${completedPlanSteps}/${plan.steps.length}${!complete && lastTool?.name && lastTool.name !== 'update_plan' ? ` · ${lastTool.name}` : ''}`
    : `${detailBlocks.length} 步${lastTool?.name ? ` · ${lastTool.name}` : ''}`;

  const statusContent = (
    <>
      {hasDetails && (open ? <ChevronDown size={14} /> : <ChevronRight size={14} />)}
      <span className="v3-working-label">{statusLabel}</span>
      <span className="v3-working-summary">{summary}</span>
    </>
  );
  const planContent = planItem ? (
    <div className={`v3-working-plan${open && hasDetails ? ' is-after-details' : ''}`}>
      <WorkingPlanCard item={planItem} />
    </div>
  ) : null;

  const handleDetailsToggle = (event) => {
    event.stopPropagation();
    setOpen((current) => !current);
  };

  useEffect(() => {
    if (!open || !hasDetails) return undefined;

    const handleKeyDown = (event) => {
      if (event.key !== 'Escape') return;
      event.preventDefault();
      setOpen(false);
      triggerRef.current?.focus({ preventScroll: true });
    };

    document.addEventListener('keydown', handleKeyDown);
    return () => {
      document.removeEventListener('keydown', handleKeyDown);
    };
  }, [hasDetails, open]);

  if (safeBlocks.length === 0) return null;

  return (
    <div className={`v3-working-process${planItem ? ' has-persistent-plan' : ''}`}>
      {hasDetails ? (
        <button
          ref={triggerRef}
          type="button"
          className="v3-working-toggle"
          aria-expanded={open}
          aria-controls={stepsID}
          aria-label={`${statusLabel} ${summary}，${open ? '收起任务步骤' : '展开任务步骤'}`}
          onClick={handleDetailsToggle}
        >
          {statusContent}
        </button>
      ) : (
        <div className="v3-working-status" role="status">
          {statusContent}
        </div>
      )}
      {!open && planContent}
      {open && hasDetails && (
        <div
          id={stepsID}
          className="v3-working-details-inline"
          role="region"
          aria-label="过程详情"
        >
          <div className="v3-working-steps">
            {detailBlocks.map((item, i) => {
              if (item.type === 'thinking') {
                return <WorkingNarrative key={i} item={item} />;
              }
              if (item.type === 'assistant_text') {
                return <WorkingNarrative key={i} item={item} />;
              }
              if (item.type === 'subagent_group') {
                return <SubAgentWorkingGroup key={i} item={item} />;
              }
              if (item.type === 'tool_pair') {
                return <WorkingToolStep key={i} item={item} />;
              }
              if (item.type === 'tool_result_orphan') {
                return <WorkingToolStep key={i} item={item} orphan />;
              }
              return null;
            })}
          </div>
        </div>
      )}
      {open && hasDetails && planContent}
    </div>
  );
}

function ChatMessageComponent({ message, workingMessages = null, workingOnly = false, workingComplete = false, artifactsFirst = false, isSelf, isGroup, senderName, senderAvatarUrl, senderIsBot, mentionDisplayNames = {}, replyMessage, questionAnchorKey, onReply, onEdit, onRegenerate, onCreateConversationShare, showThinking = true, isConsecutive, onPreviewFile, activePreviewFile, knownArtifacts = [], imageGallery = null, onOpenImage }) {
  const [copyState, setCopyState] = useState('');
  const [regenerateState, setRegenerateState] = useState('');
  const [moreActionsOpen, setMoreActionsOpen] = useState(false);
  const moreActionsTriggerRef = useRef(null);
  const moreActionsMenuRef = useRef(null);
  const moreActionsMenuId = `message-more-actions-${useId().replace(/:/g, '')}`;
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
  const workingPlanComplete = isPlanComplete(latestWorkingPlan(workingBlocks));
  const richBlocks = useMemo(() => (
    storedBlocks
      .map((block, index) => ({ ...block, __imageBlockIndex: index }))
      .filter((block) => ['image', 'file', 'audio', 'voice'].includes(block.type))
  ), [storedBlocks]);
  const storedTextBlocks = useMemo(() => (
    storedBlocks.filter(
      (block) => block.type === 'text'
        && block.text
        && block.presentation_role !== 'process',
    )
  ), [storedBlocks]);
  const displayTextBlocks = useMemo(() => (
    storedTextBlocks
      .map((block) => ({
        ...block,
        text: artifactsFirst
          ? removeRedundantArtifactAnnouncement(block.text, richBlocks)
          : block.text,
      }))
      .filter((block) => block.text?.trim())
  ), [artifactsFirst, richBlocks, storedTextBlocks]);
  const renderedTextContent = useMemo(() => {
    if (storedBlocks.length === 0) return content;
    return displayTextBlocks
      .map((block) => block.text)
      .join('\n\n');
  }, [storedBlocks, displayTextBlocks, content]);
  const hasText = useMemo(() => (
    typeof renderedTextContent === 'string'
      ? renderedTextContent.trim().length > 0
      : renderedTextContent != null
  ), [renderedTextContent]);
  const workingProcessComplete = workingComplete || workingPlanComplete || (
    workingBlocks.length > 0
    && !workingOnly
    && !message._streaming
  );
  const hasFileOnly = !hasText && richBlocks.length > 0 && richBlocks.every(
    (block) => (block.type === 'file' || block.type === 'audio' || block.type === 'voice')
      && !isInlineVideoFile(block.payload)
      && !isInlineAudioFile(block.payload),
  );

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
  const copyText = useMemo(() => (
    buildMessageCopyText(content, renderedTextContent, richBlocks, parsed)
  ), [content, renderedTextContent, richBlocks, parsed]);
  const hasArtifactFirstSummary = artifactsFirst && richBlocks.length > 0 && hasText;
  const artifactFollowupSections = useMemo(() => {
    if (!hasArtifactFirstSummary || displayTextBlocks.length === 0) return null;
    return displayTextBlocks.map((block, index) => {
      const role = block.presentation_role || 'body';
      return (
        <div
          key={`${role}-${index}`}
          className={`v3-message-followup-section is-${role}`}
          data-message-part={role}
        >
          <TextContent
            content={block.text}
            isGroup={isGroup}
            mentionDisplayNames={mentionDisplayNames}
            knownArtifacts={knownArtifacts}
            onPreviewFile={onPreviewFile}
            activePreviewFile={activePreviewFile}
            imageGallery={imageGallery}
            onOpenImage={onOpenImage}
          />
        </div>
      );
    });
  }, [
    activePreviewFile,
    hasArtifactFirstSummary,
    isGroup,
    knownArtifacts,
    mentionDisplayNames,
    onPreviewFile,
    displayTextBlocks,
  ]);
  const renderedMessageText = hasText && (parsed ? (
    <RichContent
      content={parsed}
      onPreviewFile={onPreviewFile}
      activePreviewFile={activePreviewFile}
      imageGallery={imageGallery}
      onOpenImage={onOpenImage}
      imageId={imageGalleryItemId(message, 0, parsed?.payload)}
    />
  ) : (
    <TextContent
      content={renderedTextContent}
      isGroup={isGroup}
      mentionDisplayNames={mentionDisplayNames}
      knownArtifacts={knownArtifacts}
      onPreviewFile={onPreviewFile}
      activePreviewFile={activePreviewFile}
    />
  ));

  const handleReplyClick = (event) => {
    event.stopPropagation();
    onReply?.();
  };

  const handleEditClick = (event) => {
    event.stopPropagation();
    onEdit?.(message);
  };

  const handleCopyClick = async (event) => {
    event.stopPropagation();
    try {
      await copyTextToClipboard(copyText);
      setCopyState('copied');
    } catch (e) {
      setCopyState('failed');
    }
  };

  const handleRegenerateClick = async (event) => {
    event.stopPropagation();
    if (!onRegenerate || regenerateState === 'pending') return;
    setRegenerateState('pending');
    try {
      await onRegenerate(message);
      setRegenerateState('done');
    } catch (error) {
      setRegenerateState('failed');
    }
  };

  const handleMoreActionsToggle = (event) => {
    event.stopPropagation();
    setMoreActionsOpen((current) => !current);
  };

  const handleCreateConversationShareClick = (event) => {
    event.stopPropagation();
    setMoreActionsOpen(false);
    onCreateConversationShare?.();
  };

  useEffect(() => {
    if (!moreActionsOpen) return undefined;

    const closeOnOutsidePointer = (event) => {
      const target = event.target;
      if (
        target instanceof Node
        && (moreActionsMenuRef.current?.contains(target) || moreActionsTriggerRef.current?.contains(target))
      ) {
        return;
      }
      setMoreActionsOpen(false);
    };
    const closeOnEscape = (event) => {
      if (event.key !== 'Escape') return;
      event.preventDefault();
      setMoreActionsOpen(false);
      moreActionsTriggerRef.current?.focus({ preventScroll: true });
    };

    document.addEventListener('pointerdown', closeOnOutsidePointer);
    document.addEventListener('keydown', closeOnEscape);
    return () => {
      document.removeEventListener('pointerdown', closeOnOutsidePointer);
      document.removeEventListener('keydown', closeOnEscape);
    };
  }, [moreActionsOpen]);

  if (!hasText && richBlocks.length === 0 && workingBlocks.length === 0) return null;

  return (
    <div
      className={`v3-message ${isSelf ? 'is-self' : 'is-peer'} ${senderIsBot ? 'is-agent' : ''} ${isConsecutive ? 'grouped' : ''}${hasFileOnly ? ' has-file-only' : ''}${artifactsFirst ? ' artifacts-first' : ''}${(workingOnly || message._streaming) && !workingProcessComplete ? ' is-working' : ''}${workingProcessComplete ? ' is-complete' : ''}`}
      data-conversation-question={questionAnchorKey || undefined}
    >
      {!isSelf && (
        <div className="v3-avatar-col">
          {!isConsecutive && (
            <Avatar
              name={displayName}
              src={senderAvatarUrl}
              size={36}
              isBot={senderIsBot}
              className={`v3-avatar ${senderIsBot ? 'bot' : ''}`}
              style={{ borderRadius: 6 }}
            />
          )}
        </div>
      )}

      <div className="v3-msg-body">
        <div className="v3-message-bubble">
          {!isConsecutive && (
            <div className="v3-msg-header">
              <span className="v3-msg-name">{displayName}</span>
            </div>
          )}

          {replyMessage && (
            <div
              className="v3-inline-reply"
              title={typeof replyMessage.content === 'string' ? replyMessage.content : undefined}
            >
              <span>
                {typeof replyMessage.content === 'string' ? replyMessage.content.slice(0, 80) : '[media]'}
              </span>
            </div>
          )}

          {!isSelf && showThinking && (
            <WorkingProcess blocks={workingBlocks} complete={workingProcessComplete} />
          )}

          {(hasText || richBlocks.length > 0) && (
            <div className="v3-message-content">
              {hasArtifactFirstSummary ? (
                <>
                  <div className="v3-message-deliverables" data-message-part="artifacts">
                    {richBlocks.map((block, index) => (
                      <RichContent
                        key={`${block.type}-${index}`}
                        content={block}
                        onPreviewFile={onPreviewFile}
                        activePreviewFile={activePreviewFile}
                        imageGallery={imageGallery}
                        onOpenImage={onOpenImage}
                        imageId={imageGalleryItemId(message, block.__imageBlockIndex ?? index, block?.payload)}
                      />
                    ))}
                  </div>
                  <div className="v3-message-followup-text" data-message-part="summary">
                    {artifactFollowupSections || renderedMessageText}
                    {message._streaming && <span className="oc-streaming-cursor" aria-hidden="true">|</span>}
                  </div>
                </>
              ) : (
                <>
                  {artifactsFirst && richBlocks.map((block, index) => (
                    <RichContent
                      key={`${block.type}-${index}`}
                      content={block}
                      onPreviewFile={onPreviewFile}
                      activePreviewFile={activePreviewFile}
                      imageGallery={imageGallery}
                      onOpenImage={onOpenImage}
                      imageId={imageGalleryItemId(message, block.__imageBlockIndex ?? index, block?.payload)}
                    />
                  ))}
                  {renderedMessageText}
                  {!artifactsFirst && richBlocks.map((block, index) => (
                    <RichContent
                      key={`${block.type}-${index}`}
                      content={block}
                      onPreviewFile={onPreviewFile}
                      activePreviewFile={activePreviewFile}
                      imageGallery={imageGallery}
                      onOpenImage={onOpenImage}
                      imageId={imageGalleryItemId(message, block.__imageBlockIndex ?? index, block?.payload)}
                    />
                  ))}
                  {message._streaming && <span className="oc-streaming-cursor" aria-hidden="true">|</span>}
                </>
              )}
            </div>
          )}
        </div>

        {!workingOnly && <div className="v3-message-footer">
          <div
            className={`v3-message-actions${moreActionsOpen ? ' open' : ''}`}
            onClick={(event) => event.stopPropagation()}
          >
            <button
              className={`v3-action-btn${copyState === 'copied' ? ' is-active' : ''}`}
              onClick={handleCopyClick}
              aria-label={copyState === 'copied' ? '已复制' : t('chat_copy')}
              title={copyState === 'copied' ? '已复制' : copyState === 'failed' ? '复制失败' : t('chat_copy')}
              disabled={!copyText}
              type="button"
            >
              <Copy size={17} />
            </button>
            {onRegenerate && (
              <button
                className={`v3-action-btn v3-regenerate-action${regenerateState === 'pending' ? ' is-pending' : ''}${regenerateState === 'done' ? ' is-active' : ''}`}
                onClick={handleRegenerateClick}
                aria-label={regenerateState === 'pending' ? '正在重新生成' : '重新生成'}
                title={regenerateState === 'failed' ? '重新生成失败，请重试' : regenerateState === 'pending' ? '正在重新生成' : '重新生成'}
                disabled={regenerateState === 'pending'}
                type="button"
              >
                <RotateCcw size={18} />
              </button>
            )}
            {!onEdit && onReply && (
              <button
                className="v3-action-btn v3-reply-action"
                onClick={handleReplyClick}
                aria-label={t('chat_reply')}
                title={t('chat_reply')}
                type="button"
              >
                <CornerUpLeft size={14} />
              </button>
            )}
            {onEdit && (
              <button
                className="v3-action-btn"
                onClick={handleEditClick}
                aria-label="修改后重新发送（原消息保留）"
                title="修改后重新发送（原消息保留）"
                type="button"
              >
                <Pencil size={14} />
              </button>
            )}
            {onCreateConversationShare && (
              <div className="v3-message-more-actions">
                <button
                  ref={moreActionsTriggerRef}
                  className="v3-action-btn"
                  onClick={handleMoreActionsToggle}
                  aria-label="更多操作"
                  aria-haspopup="menu"
                  aria-expanded={moreActionsOpen}
                  aria-controls={moreActionsOpen ? moreActionsMenuId : undefined}
                  title="更多操作"
                  type="button"
                >
                  <MoreHorizontal size={18} />
                </button>
                {moreActionsOpen && (
                  <div
                    ref={moreActionsMenuRef}
                    id={moreActionsMenuId}
                    className="v3-message-action-menu"
                    role="menu"
                    aria-label="消息更多操作"
                    onClick={(event) => event.stopPropagation()}
                  >
                    <button
                      type="button"
                      role="menuitem"
                      onClick={handleCreateConversationShareClick}
                    >
                      <ImageDown size={15} aria-hidden="true" />
                      <span>制作分享图</span>
                    </button>
                  </div>
                )}
              </div>
            )}
          </div>
          <time className="v3-msg-time" dateTime={message.created_at || undefined}>{timeString}</time>
        </div>}
      </div>
    </div>
  );
}

const ChatMessage = memo(ChatMessageComponent, (prevProps, nextProps) => {
  return prevProps.message === nextProps.message &&
    prevProps.workingMessages === nextProps.workingMessages &&
    prevProps.workingOnly === nextProps.workingOnly &&
    prevProps.workingComplete === nextProps.workingComplete &&
    prevProps.artifactsFirst === nextProps.artifactsFirst &&
    prevProps.isSelf === nextProps.isSelf &&
    prevProps.isGroup === nextProps.isGroup &&
    prevProps.senderName === nextProps.senderName &&
    prevProps.senderAvatarUrl === nextProps.senderAvatarUrl &&
    prevProps.senderIsBot === nextProps.senderIsBot &&
    prevProps.mentionDisplayNames === nextProps.mentionDisplayNames &&
    prevProps.replyMessage === nextProps.replyMessage &&
    prevProps.questionAnchorKey === nextProps.questionAnchorKey &&
    prevProps.onEdit === nextProps.onEdit &&
    prevProps.onRegenerate === nextProps.onRegenerate &&
    prevProps.onCreateConversationShare === nextProps.onCreateConversationShare &&
    prevProps.showThinking === nextProps.showThinking &&
    prevProps.isConsecutive === nextProps.isConsecutive &&
    prevProps.onPreviewFile === nextProps.onPreviewFile &&
    prevProps.activePreviewFile === nextProps.activePreviewFile &&
    prevProps.knownArtifacts === nextProps.knownArtifacts &&
    prevProps.imageGallery === nextProps.imageGallery &&
    prevProps.onOpenImage === nextProps.onOpenImage;
});

export default ChatMessage;

function TextContent({ content, isGroup, mentionDisplayNames = {}, knownArtifacts = [], onPreviewFile, activePreviewFile }) {
  const text = useMemo(() => messageContentText(content), [content]);
  const matchedArtifacts = useMemo(() => findKnownArtifactsInText(text, knownArtifacts), [knownArtifacts, text]);
  const plainText = useMemo(() => removeKnownArtifactURLs(text, matchedArtifacts), [matchedArtifacts, text]);
  const renderableTable = useMemo(() => hasRenderableTable(text), [text]);
  const plainTextTableLike = useMemo(() => (
    !renderableTable && hasPlainTextTableLikeBlock(text)
  ), [renderableTable, text]);
  const plainTextParagraphs = useMemo(() => (
    plainText.split(/\r?\n(?:[\t ]*\r?\n)+/)
  ), [plainText]);

  const markdownHtml = useMemo(() => {
    if (plainTextTableLike) return null;
    if (!shouldRenderMarkdown(text, { plainTextTables: true })) return null;
    try {
      return decorateArtifactMarkdown(renderSafeMarkdown(text, { plainTextTables: true }), matchedArtifacts);
    } catch (e) {
      console.error('Markdown parse error:', e);
      return null;
    }
  }, [matchedArtifacts, plainTextTableLike, text]);
  const markdownClassName = useMemo(() => (
    renderableTable ? 'oc-markdown oc-markdown-table' : 'oc-markdown'
  ), [renderableTable]);

  if (markdownHtml) {
    return (
      <>
        <div dangerouslySetInnerHTML={{ __html: markdownHtml }} className={markdownClassName} />
        <ArtifactMessageCards
          artifacts={matchedArtifacts}
          onPreviewFile={onPreviewFile}
          activePreviewFile={activePreviewFile}
        />
      </>
    );
  }

  if (plainTextTableLike) {
    return (
      <>
        <pre className="oc-plain-text-table">{plainText}</pre>
        <ArtifactMessageCards artifacts={matchedArtifacts} onPreviewFile={onPreviewFile} activePreviewFile={activePreviewFile} />
      </>
    );
  }

  if (isGroup) {
    const renderGroupText = (value, keyPrefix = 'group-text') => (
      value.split(/(@usr\d+)/g).map((part, i) => {
        const uidMatch = part.match(/^@usr(\d+)$/);
        if (!uidMatch) return <span key={`${keyPrefix}-${i}`}>{part}</span>;
        const displayName = mentionDisplayNames[uidMatch[1]];
        return (
          <span key={`${keyPrefix}-${i}`} className="oc-mention" data-mention-uid={uidMatch[1]}>
            @{displayName || `usr${uidMatch[1]}`}
          </span>
        );
      })
    );

    if (plainTextParagraphs.length > 1) {
      return (
        <>
          <div className="oc-plain-text-paragraphs">
            {plainTextParagraphs.map((paragraph, index) => (
              <p className="oc-plain-text-paragraph" key={index}>
                {renderGroupText(paragraph, `group-paragraph-${index}`)}
              </p>
            ))}
          </div>
          <ArtifactMessageCards artifacts={matchedArtifacts} onPreviewFile={onPreviewFile} activePreviewFile={activePreviewFile} />
        </>
      );
    }

    return (
      <>
        <span style={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>
          {renderGroupText(plainText)}
        </span>
        <ArtifactMessageCards artifacts={matchedArtifacts} onPreviewFile={onPreviewFile} activePreviewFile={activePreviewFile} />
      </>
    );
  }

  if (plainTextParagraphs.length > 1) {
    return (
      <>
        <div className="oc-plain-text-paragraphs">
          {plainTextParagraphs.map((paragraph, index) => (
            <p className="oc-plain-text-paragraph" key={index}>{paragraph}</p>
          ))}
        </div>
        <ArtifactMessageCards artifacts={matchedArtifacts} onPreviewFile={onPreviewFile} activePreviewFile={activePreviewFile} />
      </>
    );
  }

  return (
    <>
      <span style={{ whiteSpace: 'pre-wrap' }}>{removeKnownArtifactURLs(text, matchedArtifacts)}</span>
      <ArtifactMessageCards artifacts={matchedArtifacts} onPreviewFile={onPreviewFile} activePreviewFile={activePreviewFile} />
    </>
  );
}

function normalizeArtifactURL(value) {
  try {
    const base = typeof window !== 'undefined' ? window.location.origin : 'https://app.catsco.cc';
    const parsed = new URL(String(value || ''), base);
    if (!['http:', 'https:'].includes(parsed.protocol)) return '';
    return parsed.toString();
  } catch (e) {
    return '';
  }
}

function extractHTTPURLTokens(text) {
  const value = String(text || '');
  const tokens = [];
  for (const match of value.matchAll(/https?:\/\/[^\s<>"'`]+/gi)) {
    const previousCharacter = match.index > 0 ? value[match.index - 1] : '';
    if (/[A-Za-z0-9_]/.test(previousCharacter)) continue;
    const raw = trimURLToken(match[0]);
    const normalizedURL = normalizeArtifactURL(raw);
    if (!raw || !normalizedURL) continue;
    tokens.push({
      raw,
      normalizedURL,
      start: match.index,
      end: match.index + raw.length,
    });
  }
  return tokens;
}

function trimURLToken(value) {
  let token = String(value || '');
  while (/[.,;:!?，。；：！？]$/.test(token)) {
    token = token.slice(0, -1);
  }
  const pairs = { ')': '(', ']': '[', '}': '{' };
  while (pairs[token.at(-1)]) {
    const closing = token.at(-1);
    const opening = pairs[closing];
    const openingCount = token.split(opening).length - 1;
    const closingCount = token.split(closing).length - 1;
    if (closingCount <= openingCount) break;
    token = token.slice(0, -1);
  }
  return token;
}

function findKnownArtifactsInText(text, knownArtifacts) {
  const presentURLs = new Set(extractHTTPURLTokens(text).map((token) => token.normalizedURL));
  const seen = new Set();
  return (Array.isArray(knownArtifacts) ? knownArtifacts : []).filter((artifact) => {
    const normalizedURL = normalizeArtifactURL(artifact?.url);
    if (!normalizedURL || seen.has(normalizedURL) || !presentURLs.has(normalizedURL)) return false;
    seen.add(normalizedURL);
    return true;
  });
}

function decorateArtifactMarkdown(html, artifacts) {
  if (!html || !Array.isArray(artifacts) || artifacts.length === 0 || typeof document === 'undefined') return html;
  const knownURLs = new Set(artifacts.map((artifact) => normalizeArtifactURL(artifact.url)).filter(Boolean));
  const template = document.createElement('template');
  template.innerHTML = html;
  template.content.querySelectorAll('a[href]').forEach((anchor) => {
    if (!knownURLs.has(normalizeArtifactURL(anchor.getAttribute('href')))) return;
    anchor.classList.add('oc-artifact-source-link');
    anchor.setAttribute('aria-hidden', 'true');
    anchor.setAttribute('tabindex', '-1');
  });
  return template.innerHTML;
}

function removeKnownArtifactURLs(text, artifacts) {
  const value = String(text || '');
  const knownURLs = new Set(
    (artifacts || []).map((artifact) => normalizeArtifactURL(artifact?.url)).filter(Boolean),
  );
  const matches = extractHTTPURLTokens(value).filter((token) => knownURLs.has(token.normalizedURL));
  if (matches.length === 0) return value.replace(/[ \t]+(?=\r?$)/gm, '').trimEnd();

  let cursor = 0;
  let result = '';
  for (const match of matches) {
    result += value.slice(cursor, match.start);
    cursor = match.end;
  }
  result += value.slice(cursor);
  return result.replace(/[ \t]+(?=\r?$)/gm, '').trimEnd();
}

export function createCloudArtifactPreviewFile(artifact) {
  const payload = {
    name: artifact.title || artifact.id || 'Cloud artifact',
    url: artifact.url,
    mime_type: 'text/html',
    artifact_id: artifact.id || artifact.artifact_id || '',
    publish_version: artifact.publish_version || null,
  };
  const agentUID = Number(artifact.agent_uid || artifact.agentUid || artifact.artifact_agent_uid || 0);
  if (Number.isSafeInteger(agentUID) && agentUID > 0) payload.artifact_agent_uid = agentUID;
  trustedArtifactPreviewPayloads.add(payload);
  return payload;
}

function ArtifactMessageCards({ artifacts, onPreviewFile, activePreviewFile }) {
  if (!Array.isArray(artifacts) || artifacts.length === 0) return null;
  return (
    <div className="v3-message-artifact-list">
      {artifacts.map((artifact) => (
        <ArtifactMessageCard
          key={`${artifact.id || artifact.artifact_id || ''}|${artifact.url}`}
          artifact={artifact}
          onPreviewFile={onPreviewFile}
          activePreviewFile={activePreviewFile}
        />
      ))}
    </div>
  );
}

function ArtifactMessageCard({ artifact, onPreviewFile, activePreviewFile }) {
  const payload = createCloudArtifactPreviewFile(artifact);
  const descriptor = previewFileDescriptor(payload);
  const activeKey = activePreviewFile ? previewFileDescriptor(activePreviewFile)?.key : '';
  const isActive = descriptor?.canPreview && descriptor.key === activeKey;
  const version = Number(artifact.publish_version || 0);
  const subtitle = ['HTML', '云端生成物', version > 0 ? `v${version}` : ''].filter(Boolean).join(' · ');
  const previewArtifact = () => {
    if (descriptor?.canPreview) onPreviewFile?.(payload);
  };
  const openArtifact = () => {
    if (descriptor?.canPreview) {
      previewArtifact();
    } else if (payload.url) {
      window.open(payload.url, '_blank', 'noopener,noreferrer');
    }
  };
  return (
    <div className={`v3-attachment-card v3-artifact-card cloud-static${isActive ? ' active' : ''}`}>
      <button className="v3-artifact-main" onClick={openArtifact} title="预览生成物" type="button">
        <div className="v3-attachment-icon"><FileCode2 size={18} strokeWidth={1.5} /></div>
        <div className="v3-attachment-info">
          <span className="v3-attachment-name" title={payload.name}>{payload.name}</span>
          <span className="v3-attachment-size">{subtitle}</span>
        </div>
      </button>
      <div className="v3-artifact-actions">
        <button className="v3-artifact-action" disabled={!descriptor?.canPreview} onClick={previewArtifact} title="预览" type="button">
          <Eye size={15} /><span>预览</span>
        </button>
        <a className="v3-artifact-action" href={artifact.url} onClick={(event) => event.stopPropagation()} rel="noopener noreferrer" target="_blank" title="在新标签页打开">
          <ExternalLink size={15} /><span>打开</span>
        </a>
      </div>
    </div>
  );
}

function RichContent({ content, onPreviewFile, activePreviewFile, imageGallery = null, onOpenImage, imageId = '' }) {
  switch (content.type) {
    case 'image':
      return (
        <ImageContent
          payload={content.payload}
          imageGallery={imageGallery}
          onOpenImage={onOpenImage}
          imageId={imageId}
        />
      );
    case 'file':
    case 'audio':
    case 'voice':
      return <FileContent payload={content.payload} onPreviewFile={onPreviewFile} activePreviewFile={activePreviewFile} />;
    case 'link_preview':
      return <LinkPreviewContent payload={content.payload} />;
    case 'card':
      return <CardContent payload={content.payload} />;
    default:
      return <TextContent content={content.payload?.text || JSON.stringify(content)} />;
  }
}

function ImageContent({ payload, imageGallery = null, onOpenImage, imageId = '' }) {
  const [expanded, setExpanded] = useState(false);
  const previewRef = useRef(null);
  const triggerRef = useRef(null);
  const closeButtonRef = useRef(null);
  const src = payload?.url || payload?.thumbnail;
  const resolvedSrc = resolveMediaURL(payload?.url || src);
  const downloadURL = downloadableMediaURL(resolvedSrc);

  useEffect(() => {
    if (!expanded) return undefined;

    closeButtonRef.current?.focus({ preventScroll: true });
    const handleKeyDown = (event) => {
      if (event.key === 'Escape') {
        event.preventDefault();
        setExpanded(false);
        return;
      }
      if (event.key !== 'Tab') return;
      const focusable = Array.from(previewRef.current?.querySelectorAll(FOCUSABLE_SELECTOR) || []);
      const firstFocusable = focusable[0];
      const lastFocusable = focusable[focusable.length - 1];
      if (!firstFocusable || !lastFocusable) {
        event.preventDefault();
        return;
      }
      const focusIsOutsidePreview = !previewRef.current?.contains(document.activeElement);
      if (event.shiftKey && (document.activeElement === firstFocusable || focusIsOutsidePreview)) {
        event.preventDefault();
        lastFocusable.focus();
      } else if (!event.shiftKey && (document.activeElement === lastFocusable || focusIsOutsidePreview)) {
        event.preventDefault();
        firstFocusable.focus();
      }
    };
    document.addEventListener('keydown', handleKeyDown);

    return () => {
      document.removeEventListener('keydown', handleKeyDown);
      if (triggerRef.current?.isConnected) triggerRef.current.focus({ preventScroll: true });
    };
  }, [expanded]);

  if (!payload) return null;

  const galleryMode = Array.isArray(imageGallery) && imageGallery.length > 0 && typeof onOpenImage === 'function';
  const openPreview = () => {
    if (galleryMode) {
      const matchingItem = imageGallery.find((item) => item?.id === imageId)
        || imageGallery.find((item) => payload?.url && item?.payload?.url === payload.url)
        || imageGallery.find((item) => (
          payload?.thumbnail && item?.payload?.thumbnail === payload.thumbnail
        ));
      onOpenImage(matchingItem?.id || imageId || '', triggerRef.current, payload);
      return;
    }
    setExpanded(true);
  };

  if (galleryMode) {
    return (
      <div className="oc-rich-image">
        <button
          aria-label={`预览图片 ${payload.name || ''}`.trim()}
          className="oc-rich-image-trigger"
          onClick={openPreview}
          onKeyDown={(event) => {
            if (event.key !== 'Enter' && event.key !== ' ') return;
            event.preventDefault();
            openPreview();
          }}
          ref={triggerRef}
          type="button"
        >
          <img
            src={resolveMediaURL(src)}
            alt={payload.name || 'image'}
            className="oc-rich-image-thumb"
            draggable={canDragChatAttachment({ type: 'image', payload })}
            onDragStart={(event) => writeChatAttachmentDrag(event.dataTransfer, { type: 'image', payload })}
            onDragEnd={clearChatAttachmentDrag}
          />
        </button>
      </div>
    );
  }

  const preview = expanded ? createPortal(
    <div
      aria-label={`图片预览 ${payload.name || ''}`.trim()}
      aria-modal="true"
      className="oc-modal-overlay oc-rich-image-preview"
      onClick={() => setExpanded(false)}
      ref={previewRef}
      role="dialog"
    >
      <button
        aria-label="关闭图片预览"
        className="oc-rich-media-preview-close oc-rich-image-preview-close"
        onClick={() => setExpanded(false)}
        ref={closeButtonRef}
        type="button"
      >
        <X size={20} />
      </button>
      <PwaDownloadLink
        aria-label={`下载图片 ${payload.name || ''}`.trim()}
        className="oc-rich-media-preview-download"
        download={payload.name || true}
        href={downloadURL || undefined}
        onClick={(event) => event.stopPropagation()}
        rel="noopener noreferrer"
        target="_blank"
        title="下载图片"
      >
        <Download size={20} />
      </PwaDownloadLink>
      <img
        src={resolvedSrc}
        alt={payload.name ? `${payload.name} preview` : 'image preview'}
        className="oc-rich-image-preview-media"
        onClick={(event) => event.stopPropagation()}
      />
    </div>,
    document.body,
  ) : null;
  return (
    <div className="oc-rich-image">
      <button
        aria-label={`预览图片 ${payload.name || ''}`.trim()}
        className="oc-rich-image-trigger"
        onClick={openPreview}
        onKeyDown={(event) => {
          if (event.key !== 'Enter' && event.key !== ' ') return;
          event.preventDefault();
          openPreview();
        }}
        ref={triggerRef}
        type="button"
      >
        <img
          src={resolveMediaURL(src)}
          alt={payload.name || 'image'}
          className="oc-rich-image-thumb"
          draggable={canDragChatAttachment({ type: 'image', payload })}
          onDragStart={(event) => writeChatAttachmentDrag(event.dataTransfer, { type: 'image', payload })}
          onDragEnd={clearChatAttachmentDrag}
        />
      </button>
      {preview}
    </div>
  );
}

function fileExtension(payload) {
  const name = String(payload?.name || payload?.url || '').split(/[?#]/, 1)[0];
  const raw = name.includes('.') ? name.slice(name.lastIndexOf('.') + 1) : '';
  return raw ? raw.toUpperCase() : 'FILE';
}

function fileMimeType(payload) {
  return String(payload?.mime_type || payload?.mime || payload?.content_type || '')
    .split(';', 1)[0]
    .trim()
    .toLowerCase();
}

const INLINE_VIDEO_EXTENSIONS = new Set(['MP4', 'WEBM', 'OGV', 'M4V', 'MOV']);
const INLINE_VIDEO_MIME_TYPES = new Set([
  'video/mp4',
  'video/webm',
  'video/ogg',
  'video/x-m4v',
  'video/quicktime',
]);

const INLINE_AUDIO_EXTENSIONS = new Set(['MP3', 'OGG', 'WAV']);
const INLINE_AUDIO_MIME_TYPES = new Set([
  'audio/mpeg',
  'audio/mp3',
  'audio/ogg',
  'audio/wav',
  'audio/x-wav',
]);

function isInlineVideoFile(payload, ext = fileExtension(payload)) {
  return INLINE_VIDEO_EXTENSIONS.has(ext) || INLINE_VIDEO_MIME_TYPES.has(fileMimeType(payload));
}

function isInlineAudioFile(payload, ext = fileExtension(payload)) {
  // A concrete extension is authoritative. This prevents an upstream MIME
  // mislabel, such as .opus reported as audio/ogg, from turning an unsupported
  // voice attachment into a broken native player. MIME remains useful only for
  // legacy payloads with no usable file extension.
  if (fileMimeType(payload) === 'audio/opus') return false;
  return INLINE_AUDIO_EXTENSIONS.has(ext)
    || (ext === 'FILE' && INLINE_AUDIO_MIME_TYPES.has(fileMimeType(payload)));
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

function isCsvFile(payload, ext = fileExtension(payload)) {
  const mime = fileMimeType(payload);
  return ext === 'CSV' || mime === 'text/csv' || mime === 'application/csv';
}

function isXlsxFile(payload, ext = fileExtension(payload)) {
  return ext === 'XLSX' || fileMimeType(payload) === 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet';
}

function isSpreadsheetFile(payload, ext = fileExtension(payload)) {
  return SPREADSHEET_FILE_EXTENSIONS.has(ext) || SPREADSHEET_MIME_TYPES.has(fileMimeType(payload));
}

function isSpreadsheetPreviewFile(payload, ext = fileExtension(payload)) {
  return isCsvFile(payload, ext) || isXlsxFile(payload, ext);
}

function isDocxFile(payload, ext = fileExtension(payload)) {
  const mime = fileMimeType(payload);
  return ext === 'DOCX' ||
    mime === 'application/vnd.openxmlformats-officedocument.wordprocessingml.document';
}

function isPreviewableFile(payload, ext = fileExtension(payload)) {
  const mime = fileMimeType(payload);
  if (isSpreadsheetPreviewFile(payload, ext)) return true;
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
  if (isSpreadsheetFile(payload, ext)) {
    return {
      label: isCsvFile(payload, ext) ? 'CSV' : 'Excel',
      className: 'dataset',
      subtitle: isSpreadsheetPreviewFile(payload, ext) ? '可预览的表格数据' : '可下载的表格文件',
    };
  }
  if (isMarkdownFile(payload, ext)) {
    return {
      label: 'Markdown',
      className: 'document',
      subtitle: '文档产物',
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

export function downloadableMediaURL(url) {
  if (!url) return '';
  try {
    const urlObj = new URL(url, window.location.origin);
    const mediaBase = new URL(resolveMediaURL('/'), window.location.origin);
    const uploadRootPath = `${mediaBase.pathname.replace(/\/+$/, '')}/uploads/`;
    const isDownloadableUpload = ['files', 'images'].some(
      (directory) => urlObj.pathname.startsWith(`${uploadRootPath}${directory}/`),
    );
    if (urlObj.origin !== mediaBase.origin || !isDownloadableUpload) {
      return url;
    }
    urlObj.searchParams.set('download', '1');
    return /^https?:\/\//i.test(url)
      ? urlObj.toString()
      : `${urlObj.pathname}${urlObj.search}${urlObj.hash}`;
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

function isSameOriginURL(url) {
  if (!url || typeof window === 'undefined' || !window.location?.origin) return false;
  try {
    return new URL(url, window.location.origin).origin === window.location.origin;
  } catch (e) {
    return false;
  }
}

export function previewFileDescriptor(payload) {
  if (!payload) return null;
  const url = resolveMediaURL(payload.url);
  const ext = fileExtension(payload);
  const meta = artifactMeta(payload, ext);
  const isPdf = isPdfFile(payload, ext);
  const isHtml = isHtmlFile(payload, ext);
  const isMarkdown = isMarkdownFile(payload, ext);
  const isSpreadsheet = isSpreadsheetPreviewFile(payload, ext);
  const isManagedRemoteArtifact = trustedArtifactPreviewPayloads.has(payload)
    && isHtml
    && Boolean(normalizeArtifactURL(url));
  const isSameOriginRemoteArtifact = isManagedRemoteArtifact && isSameOriginURL(url);
  const isRemoteArtifact = isManagedRemoteArtifact && !isSameOriginRemoteArtifact;
  const spreadsheetKind = isCsvFile(payload, ext) ? 'csv' : isXlsxFile(payload, ext) ? 'xlsx' : '';
  const canPreview = isManagedRemoteArtifact
    ? isRemoteArtifact
    : isPreviewableFile(payload, ext) && isTrustedPreviewURL(url);
  return {
    payload,
    url,
    ext,
    meta,
    isPdf,
    isHtml,
    isMarkdown,
    isSpreadsheet,
    isRemoteArtifact,
    isSameOriginRemoteArtifact,
    spreadsheetKind,
    canPreview,
    downloadURL: downloadableMediaURL(url),
    sizeStr: payload.size ? formatFileSize(payload.size) : '',
    key: `${url}|${payload.name || ''}|${payload.size || ''}`,
  };
}

function spreadsheetPreviewTooLargeMessage() {
  return `表格文件较大，当前最多预览 ${formatFileSize(SPREADSHEET_PREVIEW_MAX_BYTES)}，请下载后查看。`;
}

function VideoContent({ payload, onPreviewFile, activePreviewFile }) {
  const [playbackFailed, setPlaybackFailed] = useState(false);
  const [previewOpen, setPreviewOpen] = useState(false);
  const previewRef = useRef(null);
  const triggerRef = useRef(null);
  const closeButtonRef = useRef(null);
  const fallbackActionRef = useRef(null);
  const shouldFocusFallbackRef = useRef(false);
  const src = resolveMediaURL(payload?.url);
  const downloadURL = downloadableMediaURL(src);

  useEffect(() => {
    setPlaybackFailed(false);
    setPreviewOpen(false);
  }, [src]);

  useEffect(() => {
    if (!previewOpen || playbackFailed) return undefined;

    closeButtonRef.current?.focus({ preventScroll: true });
    const handleKeyDown = (event) => {
      if (event.key === 'Escape') {
        event.preventDefault();
        setPreviewOpen(false);
        return;
      }
      if (event.key !== 'Tab') return;
      const focusable = Array.from(previewRef.current?.querySelectorAll(FOCUSABLE_SELECTOR) || []);
      const firstFocusable = focusable[0];
      const lastFocusable = focusable[focusable.length - 1];
      if (!firstFocusable || !lastFocusable) {
        event.preventDefault();
        return;
      }
      const focusIsOutsidePreview = !previewRef.current?.contains(document.activeElement);
      if (event.shiftKey && (document.activeElement === firstFocusable || focusIsOutsidePreview)) {
        event.preventDefault();
        lastFocusable.focus();
      } else if (!event.shiftKey && (document.activeElement === lastFocusable || focusIsOutsidePreview)) {
        event.preventDefault();
        firstFocusable.focus();
      }
    };
    document.addEventListener('keydown', handleKeyDown);

    return () => {
      document.removeEventListener('keydown', handleKeyDown);
      if (triggerRef.current?.isConnected) triggerRef.current.focus({ preventScroll: true });
    };
  }, [playbackFailed, previewOpen]);

  useEffect(() => {
    if (!playbackFailed || !shouldFocusFallbackRef.current) return;
    fallbackActionRef.current?.focus({ preventScroll: true });
    shouldFocusFallbackRef.current = false;
  }, [playbackFailed]);

  const handlePlaybackError = () => {
    const activeElement = document.activeElement;
    shouldFocusFallbackRef.current = triggerRef.current === activeElement
      || Boolean(previewRef.current?.contains(activeElement));
    setPreviewOpen(false);
    setPlaybackFailed(true);
  };

  if (!payload || !src || playbackFailed) {
    return (
      <div className="oc-rich-video-fallback">
        {playbackFailed && (
          <span className="oc-visually-hidden" role="status">
            视频无法播放，已显示下载选项。
          </span>
        )}
        <FileContent
          actionRef={fallbackActionRef}
          activePreviewFile={activePreviewFile}
          inlineMedia={false}
          onPreviewFile={onPreviewFile}
          payload={payload}
        />
      </div>
    );
  }

  return (
    <div className="oc-rich-image oc-rich-video">
      <button
        aria-label={`预览视频 ${payload.name || ''}`.trim()}
        className="oc-rich-video-trigger"
        onClick={() => setPreviewOpen(true)}
        ref={triggerRef}
        type="button"
      >
        <video
          aria-hidden="true"
          className="oc-rich-video-thumb"
          muted
          onError={handlePlaybackError}
          playsInline
          preload="metadata"
          src={src}
        />
        <span className="oc-rich-video-play" aria-hidden="true">
          <Play fill="currentColor" size={20} />
        </span>
      </button>
      {previewOpen && (
        <div
          aria-label={`视频预览 ${payload.name || ''}`.trim()}
          aria-modal="true"
          className="oc-modal-overlay oc-rich-video-preview"
          onClick={() => setPreviewOpen(false)}
          ref={previewRef}
          role="dialog"
        >
          <button
            aria-label="关闭视频预览"
            className="oc-rich-media-preview-close oc-rich-video-preview-close"
            onClick={() => setPreviewOpen(false)}
            ref={closeButtonRef}
            type="button"
          >
            <X size={20} />
          </button>
          <PwaDownloadLink
            aria-label={`下载视频 ${payload.name || ''}`.trim()}
            className="oc-rich-media-preview-download"
            download={payload.name || true}
            href={downloadURL || undefined}
            onClick={(event) => event.stopPropagation()}
            rel="noopener noreferrer"
            target="_blank"
            title="下载视频"
          >
            <Download size={20} />
          </PwaDownloadLink>
          <video
            aria-label={payload.name || '视频'}
            autoPlay
            className="oc-rich-video-player"
            controls
            onClick={(event) => event.stopPropagation()}
            onError={handlePlaybackError}
            playsInline
            preload="metadata"
            src={src}
            tabIndex={0}
          >
            您的浏览器暂不支持视频播放。
          </video>
        </div>
      )}
    </div>
  );
}

function AudioContent({ payload, onPreviewFile, activePreviewFile }) {
  const [playbackFailed, setPlaybackFailed] = useState(false);
  const fallbackActionRef = useRef(null);
  const shouldFocusFallbackRef = useRef(false);
  const src = resolveMediaURL(payload?.url);
  const downloadURL = downloadableMediaURL(src);
  const sizeStr = payload?.size ? formatFileSize(payload.size) : '';

  useEffect(() => {
    setPlaybackFailed(false);
  }, [src]);

  useEffect(() => {
    if (!playbackFailed || !shouldFocusFallbackRef.current) return;
    fallbackActionRef.current?.focus({ preventScroll: true });
    shouldFocusFallbackRef.current = false;
  }, [playbackFailed]);

  const handlePlaybackError = () => {
    shouldFocusFallbackRef.current = document.activeElement instanceof HTMLElement
      && document.activeElement.matches('audio, audio *');
    setPlaybackFailed(true);
  };

  if (!payload || !src || playbackFailed) {
    return (
      <div className="oc-rich-audio-fallback">
        {playbackFailed && (
          <span className="oc-visually-hidden" role="status">
            音频无法播放，已显示下载选项。
          </span>
        )}
        <FileContent
          actionRef={fallbackActionRef}
          activePreviewFile={activePreviewFile}
          inlineMedia={false}
          onPreviewFile={onPreviewFile}
          payload={payload}
        />
      </div>
    );
  }

  return (
    <div className="oc-rich-audio">
      <div className="oc-rich-audio-header">
        <span className="oc-rich-audio-icon" aria-hidden="true"><Volume2 size={17} /></span>
        <span className="oc-rich-audio-name" title={payload.name || '语音消息'}>{payload.name || '语音消息'}</span>
        {sizeStr && <span className="oc-rich-audio-size">{sizeStr}</span>}
      </div>
      <audio
        aria-label={`播放音频 ${payload.name || ''}`.trim()}
        className="oc-rich-audio-player"
        controls
        controlsList="nodownload"
        onError={handlePlaybackError}
        preload="metadata"
        src={src}
      >
        您的浏览器暂不支持音频播放。
      </audio>
      <PwaDownloadLink
        className="oc-rich-audio-download"
        download={payload.name || true}
        href={downloadURL || undefined}
        rel="noopener noreferrer"
        target="_blank"
        title="下载音频"
      >
        <Download size={15} />
        <span>下载</span>
      </PwaDownloadLink>
    </div>
  );
}

function FileContent({ payload, onPreviewFile, activePreviewFile, inlineMedia = true, actionRef = null }) {
  if (inlineMedia && payload && isInlineVideoFile(payload)) {
    return <VideoContent payload={payload} onPreviewFile={onPreviewFile} activePreviewFile={activePreviewFile} />;
  }
  if (inlineMedia && payload && isInlineAudioFile(payload)) {
    return <AudioContent payload={payload} onPreviewFile={onPreviewFile} activePreviewFile={activePreviewFile} />;
  }
  if (!payload) return null;
  const descriptor = previewFileDescriptor(payload);
  const { url, ext, canPreview, downloadURL, meta, sizeStr, key } = descriptor;
  const activeKey = activePreviewFile ? previewFileDescriptor(activePreviewFile)?.key : '';
  const isActive = canPreview && activeKey === key;
  const subtitle = [meta.label, sizeStr].filter(Boolean).join(' · ');
  const openFile = () => {
    if (canPreview && onPreviewFile) onPreviewFile(payload);
    else if (url) window.open(url, '_blank');
  };

  return (
    <div
      className={`v3-attachment-card v3-artifact-card ${meta.className}${isActive ? ' active' : ''}`}
      draggable={canDragChatAttachment({ type: 'file', payload })}
      onDragStart={(event) => writeChatAttachmentDrag(event.dataTransfer, { type: 'file', payload })}
      onDragEnd={clearChatAttachmentDrag}
    >
      <button
        className="v3-artifact-main"
        onClick={openFile}
        ref={actionRef}
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
        <PwaDownloadLink
          className="v3-artifact-action"
          href={downloadURL || undefined}
          download={payload.name || true}
          onClick={(event) => event.stopPropagation()}
          target="_blank"
          rel="noopener noreferrer"
          title="下载"
        >
          <Download size={15} />
          <span>下载</span>
        </PwaDownloadLink>
      </div>
    </div>
  );
}

export function FilePreviewPanel({
  file,
  onBack,
  onClose,
  backgroundRef,
  onRemoteArtifactFrameChange,
  pendingRemoteArtifactFile,
  onRemoteArtifactRefreshReady,
  onRemoteArtifactRefreshFailed,
}) {
  const [preview, setPreview] = useState(false);
  const [textContent, setTextContent] = useState(null);
  const [binaryContent, setBinaryContent] = useState(null);
  const [loadingText, setLoadingText] = useState(false);
  const [previewError, setPreviewError] = useState('');
  const [remoteFrameState, setRemoteFrameState] = useState('idle');
  const [dragOffset, setDragOffset] = useState(0);
  const [isDismissing, setIsDismissing] = useState(false);
  const dragStateRef = useRef({ active: false, startY: 0, offset: 0 });
  const dismissTimerRef = useRef(null);
  const hasDismissedRef = useRef(false);
  const panelRef = useRef(null);
  const closeButtonRef = useRef(null);
  const remoteArtifactFrameRef = useRef(null);
  const pendingRemoteArtifactFrameRef = useRef(null);
  const pendingRemoteArtifactAttemptRef = useRef(null);
  const verifiedRemoteArtifactKeysRef = useRef(new Set());
  const focusBeforeSheetRef = useRef(null);
  const [isSheetMode, setIsSheetMode] = useState(
    () => window.matchMedia?.('(max-width: 1024px)').matches ?? window.innerWidth <= 1024,
  );
  const shouldUseSheetMode = preview
    ? (window.matchMedia?.('(max-width: 1024px)').matches ?? window.innerWidth <= 1024)
    : isSheetMode;

  const descriptor = useMemo(() => previewFileDescriptor(file), [file]);
  const pendingRemoteArtifactDescriptor = useMemo(
    () => previewFileDescriptor(pendingRemoteArtifactFile),
    [pendingRemoteArtifactFile],
  );
  const url = descriptor?.url || '';
  const isPdf = descriptor?.isPdf || false;
  const isHtml = descriptor?.isHtml || false;
  const isMarkdown = descriptor?.isMarkdown || false;
  const isSpreadsheet = descriptor?.isSpreadsheet || false;
  const isRemoteArtifact = descriptor?.isRemoteArtifact || false;
  const meta = descriptor?.meta || artifactMeta(file || {});
  const sizeStr = descriptor?.sizeStr || '';
  const downloadURL = descriptor?.downloadURL || url;
  const currentRemoteArtifactKey = remoteArtifactPreviewKey(file, descriptor);
  const pendingRemoteArtifactKey = remoteArtifactPreviewKey(
    pendingRemoteArtifactFile,
    pendingRemoteArtifactDescriptor,
  );
  const pendingRemoteArtifactAttemptSeed = useMemo(() => {
    if (!pendingRemoteArtifactKey || !pendingRemoteArtifactFile) return null;
    return {
      file: pendingRemoteArtifactFile,
      key: pendingRemoteArtifactKey,
      loaded: false,
      errored: false,
      reported: false,
      frame: null,
    };
  }, [pendingRemoteArtifactFile, pendingRemoteArtifactKey]);

  const settlePendingRemoteArtifact = (status, expectedAttempt) => {
    const attempt = pendingRemoteArtifactAttemptRef.current;
    if (!attempt || attempt !== expectedAttempt || attempt.settled) return;
    attempt.settled = true;
    if (attempt.timer) window.clearTimeout(attempt.timer);
    pendingRemoteArtifactAttemptRef.current = null;
    if (attempt.seed.reported) return;
    attempt.seed.reported = true;
    if (status === 'ready') {
      const verifiedKeys = verifiedRemoteArtifactKeysRef.current;
      verifiedKeys.add(attempt.key);
      while (verifiedKeys.size > 8) {
        verifiedKeys.delete(verifiedKeys.values().next().value);
      }
      onRemoteArtifactRefreshReady?.(attempt.file);
    } else {
      onRemoteArtifactRefreshFailed?.(attempt.file);
    }
  };

  const verifyPendingRemoteArtifact = async (attempt) => {
    if (!attempt
      || pendingRemoteArtifactAttemptRef.current !== attempt
      || attempt.settled
      || attempt.verifying
      || !attempt.frame) return;
    attempt.verifying = true;
    const artifactRef = artifactRefFromPreviewFile(attempt.file);
    if (!artifactRef) {
      settlePendingRemoteArtifact('failed', attempt);
      return;
    }
    const pageContext = await requestArtifactPageContext({
      frame: attempt.frame,
      artifactId: artifactRef.id,
      url: attempt.file.url,
      agentUid: attempt.file.artifact_agent_uid,
    }, artifactRef, REMOTE_ARTIFACT_REFRESH_HANDSHAKE_TIMEOUT_MS);
    settlePendingRemoteArtifact(pageContext ? 'ready' : 'failed', attempt);
  };

  useEffect(() => {
    const previous = pendingRemoteArtifactAttemptRef.current;
    if (previous?.timer) window.clearTimeout(previous.timer);
    if (previous) previous.settled = true;
    pendingRemoteArtifactAttemptRef.current = null;
    const seed = pendingRemoteArtifactAttemptSeed;
    if (!seed) return undefined;

    const attempt = {
      seed,
      file: seed.file,
      key: seed.key,
      settled: false,
      verifying: false,
      frame: seed.frame || pendingRemoteArtifactFrameRef.current,
      timer: null,
    };
    pendingRemoteArtifactAttemptRef.current = attempt;
    if (seed.errored) {
      settlePendingRemoteArtifact('failed', attempt);
    } else {
      attempt.timer = window.setTimeout(() => {
        settlePendingRemoteArtifact('failed', attempt);
      }, REMOTE_ARTIFACT_REFRESH_TIMEOUT_MS);
      if (seed.loaded) void verifyPendingRemoteArtifact(attempt);
    }
    return () => {
      if (attempt.timer) window.clearTimeout(attempt.timer);
      attempt.settled = true;
      if (pendingRemoteArtifactAttemptRef.current === attempt) {
        pendingRemoteArtifactAttemptRef.current = null;
      }
    };
  }, [pendingRemoteArtifactAttemptSeed]);

  const handlePendingRemoteArtifactLoad = (seed, frame) => {
    if (!seed) return;
    seed.loaded = true;
    seed.frame = frame || seed.frame;
    const attempt = pendingRemoteArtifactAttemptRef.current;
    if (!attempt || attempt.seed !== seed || attempt.settled) return;
    attempt.frame = seed.frame || attempt.frame;
    void verifyPendingRemoteArtifact(attempt);
  };

  const handlePendingRemoteArtifactError = (seed) => {
    if (!seed) return;
    seed.errored = true;
    const attempt = pendingRemoteArtifactAttemptRef.current;
    if (!attempt || attempt.seed !== seed || attempt.settled) return;
    settlePendingRemoteArtifact('failed', attempt);
  };

  useEffect(() => {
    const frame = remoteArtifactFrameRef.current;
    if (!preview || !isRemoteArtifact || !frame || !file?.artifact_id) {
      onRemoteArtifactFrameChange?.(null);
      return undefined;
    }
    const binding = {
      frame,
      artifactId: String(file.artifact_id),
      agentUid: Number(file.artifact_agent_uid || 0),
      url,
    };
    onRemoteArtifactFrameChange?.(binding);
    return () => onRemoteArtifactFrameChange?.(null);
  }, [file?.artifact_agent_uid, file?.artifact_id, isRemoteArtifact, onRemoteArtifactFrameChange, preview, url]);

  useEffect(() => {
    let cancelled = false;
    setPreview(Boolean(file));
    setTextContent(null);
    setBinaryContent(null);
    setPreviewError('');
    setRemoteFrameState(
      isRemoteArtifact
        ? (verifiedRemoteArtifactKeysRef.current.has(currentRemoteArtifactKey) ? 'ready' : 'loading')
        : 'idle',
    );
    setDragOffset(0);
    setIsDismissing(false);
    hasDismissedRef.current = false;
    dragStateRef.current = { active: false, startY: 0, offset: 0 };
    if (dismissTimerRef.current) {
      window.clearTimeout(dismissTimerRef.current);
      dismissTimerRef.current = null;
    }
    if (!file || !descriptor?.canPreview || isPdf || isRemoteArtifact) {
      setLoadingText(false);
      return () => {
        cancelled = true;
      };
    }
    if (isSpreadsheet && file?.size > SPREADSHEET_PREVIEW_MAX_BYTES) {
      setPreviewError(spreadsheetPreviewTooLargeMessage());
      setLoadingText(false);
      return () => {
        cancelled = true;
      };
    }

    const load = async () => {
      setLoadingText(true);
      try {
        const res = await fetch(fetchableMediaURL(url));
        if (!res.ok) throw new Error(responseErrorMessage(res.status, `HTTP Error ${res.status}`));
        if (isSpreadsheet) {
          const contentLength = Number(res.headers?.get?.('Content-Length') || res.headers?.get?.('content-length') || 0);
          if (contentLength > SPREADSHEET_PREVIEW_MAX_BYTES) {
            throw new Error(spreadsheetPreviewTooLargeMessage());
          }
          const buffer = await res.arrayBuffer();
          if (buffer.byteLength > SPREADSHEET_PREVIEW_MAX_BYTES) {
            throw new Error(spreadsheetPreviewTooLargeMessage());
          }
          if (!cancelled) setBinaryContent(buffer);
        } else {
          const text = await res.text();
          if (!cancelled) setTextContent(text);
        }
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
  }, [currentRemoteArtifactKey, descriptor?.canPreview, file, isPdf, isRemoteArtifact, isSpreadsheet, url]);

  useEffect(() => {
    if (!preview) return undefined;

    const media = window.matchMedia?.('(max-width: 1024px)');
    const syncSheetMode = () => setIsSheetMode(media?.matches ?? window.innerWidth <= 1024);
    syncSheetMode();
    if (media?.addEventListener) media.addEventListener('change', syncSheetMode);
    else media?.addListener?.(syncSheetMode);
    window.addEventListener('resize', syncSheetMode);
    return () => {
      if (media?.removeEventListener) media.removeEventListener('change', syncSheetMode);
      else media?.removeListener?.(syncSheetMode);
      window.removeEventListener('resize', syncSheetMode);
    };
  }, [preview]);

  useEffect(() => {
    if (!preview) return undefined;
    const handleKeyDown = (event) => {
      if (event.key !== 'Escape') return;
      event.preventDefault();
      onClose?.();
    };
    window.addEventListener('keydown', handleKeyDown);
    return () => window.removeEventListener('keydown', handleKeyDown);
  }, [onClose, preview]);

  useEffect(() => {
    if (!preview || !shouldUseSheetMode) return undefined;

    focusBeforeSheetRef.current = document.activeElement instanceof HTMLElement
      ? document.activeElement
      : null;
    const background = backgroundRef?.current || null;
    const hadInert = background?.hasAttribute('inert');
    const previousAriaHidden = background?.getAttribute('aria-hidden');
    closeButtonRef.current?.focus({ preventScroll: true });
    if (background) {
      background.setAttribute('inert', '');
      background.setAttribute('aria-hidden', 'true');
    }

    const handleKeyDown = (event) => {
      if (event.key !== 'Tab') return;
      const focusable = Array.from(panelRef.current?.querySelectorAll(FOCUSABLE_SELECTOR) || []);
      const firstFocusable = focusable[0];
      const lastFocusable = focusable[focusable.length - 1];
      if (!firstFocusable || !lastFocusable) {
        event.preventDefault();
        return;
      }
      const focusIsOutsidePanel = !panelRef.current?.contains(document.activeElement);
      if (event.shiftKey && (document.activeElement === firstFocusable || focusIsOutsidePanel)) {
        event.preventDefault();
        lastFocusable.focus();
      } else if (!event.shiftKey && (document.activeElement === lastFocusable || focusIsOutsidePanel)) {
        event.preventDefault();
        firstFocusable.focus();
      }
    };
    document.addEventListener('keydown', handleKeyDown);

    return () => {
      document.removeEventListener('keydown', handleKeyDown);
      if (background) {
        if (!hadInert) background.removeAttribute('inert');
        if (previousAriaHidden == null) background.removeAttribute('aria-hidden');
        else background.setAttribute('aria-hidden', previousAriaHidden);
      }
      const priorFocus = focusBeforeSheetRef.current;
      if (priorFocus?.isConnected) priorFocus.focus({ preventScroll: true });
      focusBeforeSheetRef.current = null;
    };
  }, [backgroundRef, preview, shouldUseSheetMode]);

  useEffect(() => () => {
    if (dismissTimerRef.current) window.clearTimeout(dismissTimerRef.current);
  }, []);

  const finishDismiss = () => {
    if (hasDismissedRef.current) return;
    hasDismissedRef.current = true;
    if (dismissTimerRef.current) {
      window.clearTimeout(dismissTimerRef.current);
      dismissTimerRef.current = null;
    }
    onClose?.();
  };

  const startDismiss = () => {
    if (isDismissing) return;
    if (window.matchMedia?.('(prefers-reduced-motion: reduce)').matches) {
      finishDismiss();
      return;
    }
    setIsDismissing(true);
    setDragOffset(Math.max(window.innerHeight || 800, dragStateRef.current.offset));
    dismissTimerRef.current = window.setTimeout(finishDismiss, 500);
  };

  const handlePanelTransitionEnd = (event) => {
    if (event.target !== event.currentTarget || event.propertyName !== 'transform' || !isDismissing) return;
    finishDismiss();
  };

  const handleDragStart = (event) => {
    if (isDismissing) return;
    if (event.pointerType === 'mouse' && event.button !== 0) return;
    dragStateRef.current = { active: true, startY: event.clientY, offset: 0 };
    event.currentTarget.setPointerCapture?.(event.pointerId);
    event.preventDefault();
  };

  const handleDragMove = (event) => {
    if (!dragStateRef.current.active) return;
    const offset = Math.max(0, event.clientY - dragStateRef.current.startY);
    dragStateRef.current.offset = offset;
    setDragOffset(offset);
  };

  const handleDragEnd = (event) => {
    if (!dragStateRef.current.active) return;
    const shouldClose = dragStateRef.current.offset >= 72;
    dragStateRef.current.active = false;
    event.currentTarget.releasePointerCapture?.(event.pointerId);
    if (shouldClose) {
      startDismiss();
      return;
    }
    dragStateRef.current.offset = 0;
    setDragOffset(0);
  };

  const backdropOpacity = isDismissing ? 0 : Math.max(0.35, 1 - (dragOffset / 220));

  if (!preview || !file) return null;
  if (!descriptor?.canPreview) return null;

  return (
    <>
      <button
        className={`v3-file-preview-backdrop ${isDismissing ? 'is-dismissing' : ''}`}
        type="button"
        aria-label="关闭文件预览"
        onClick={onClose}
        style={{ '--v3-preview-backdrop-opacity': backdropOpacity }}
      />
      <aside
        ref={panelRef}
        className={`v3-file-preview-panel ${dragStateRef.current.active ? 'is-dragging' : ''} ${isDismissing ? 'is-dismissing' : ''} ${isHtml || isPdf || isSpreadsheet ? 'wide' : ''}`}
        role={shouldUseSheetMode ? 'dialog' : undefined}
        aria-modal={shouldUseSheetMode || undefined}
        aria-label="文件预览"
        style={{ '--v3-preview-drag-offset': `${dragOffset}px` }}
        onTransitionEnd={handlePanelTransitionEnd}
      >
        <button
          className="v3-file-preview-drag-handle"
          type="button"
          aria-label="向下拖动关闭预览"
          onKeyDown={(event) => {
            if (event.key !== 'Enter' && event.key !== ' ') return;
            event.preventDefault();
            startDismiss();
          }}
          onPointerDown={handleDragStart}
          onPointerMove={handleDragMove}
          onPointerUp={handleDragEnd}
          onPointerCancel={handleDragEnd}
        />
        <div className="v3-file-preview-header">
          <div className="v3-file-preview-title">
            {onBack && (
              <button
                className="v3-file-preview-back"
                type="button"
                aria-label="返回云文件"
                title="返回云文件"
                onClick={onBack}
              >
                <ArrowLeft size={18} />
              </button>
            )}
            <FileText size={18} />
            <div>
              <h3>{file.name}</h3>
              <span>{meta.label}{sizeStr ? ` · ${sizeStr}` : ''}</span>
            </div>
          </div>
          <div className="v3-file-preview-actions">
            {!isRemoteArtifact && (
              <PwaDownloadLink href={downloadURL} download={file.name || true} title="下载原文件" target="_blank" rel="noopener noreferrer" aria-label="下载原文件">
                <Download size={18} />
              </PwaDownloadLink>
            )}
            <a href={url} title="在新窗口打开" target="_blank" rel="noopener noreferrer" aria-label="在新窗口打开">
              <ExternalLink size={18} />
            </a>
            <button ref={closeButtonRef} aria-label="关闭预览" onClick={onClose} type="button">
              <X size={18} />
            </button>
          </div>
        </div>
        <div className="v3-file-preview-body">
          {isRemoteArtifact ? (
            <div className="v3-remote-artifact-preview">
              {[
                {
                  key: currentRemoteArtifactKey,
                  descriptor,
                  current: true,
                  attemptSeed: null,
                },
                pendingRemoteArtifactKey && pendingRemoteArtifactKey !== currentRemoteArtifactKey
                  ? {
                     key: pendingRemoteArtifactKey,
                     descriptor: pendingRemoteArtifactDescriptor,
                     current: false,
                     attemptSeed: pendingRemoteArtifactAttemptSeed,
                   }
                  : null,
              ].filter(Boolean).map((frameEntry) => (
                <iframe
                  key={frameEntry.key}
                  ref={frameEntry.current ? remoteArtifactFrameRef : pendingRemoteArtifactFrameRef}
                  src={frameEntry.descriptor.url}
                  className={frameEntry.current ? 'v3-file-preview-frame' : 'v3-file-preview-frame-pending'}
                  title={frameEntry.current ? 'Cloud Artifact Preview' : 'Cloud Artifact Refresh Check'}
                  sandbox={REMOTE_ARTIFACT_PREVIEW_SANDBOX}
                  credentialless=""
                  referrerPolicy="no-referrer"
                  aria-hidden={frameEntry.current ? undefined : 'true'}
                  tabIndex={frameEntry.current && !isSheetMode ? undefined : -1}
                  style={frameEntry.current ? undefined : {
                    position: 'absolute',
                    width: '1px',
                    height: '1px',
                    opacity: 0,
                    pointerEvents: 'none',
                  }}
                  onLoad={frameEntry.current
                    ? () => setRemoteFrameState('ready')
                    : (event) => handlePendingRemoteArtifactLoad(frameEntry.attemptSeed, event.currentTarget)}
                  onError={frameEntry.current
                    ? () => setRemoteFrameState('error')
                    : () => handlePendingRemoteArtifactError(frameEntry.attemptSeed)}
                />
              ))}
              {remoteFrameState === 'loading' && (
                <div className="v3-remote-artifact-preview-state" role="status">正在加载云端生成物...</div>
              )}
              {remoteFrameState === 'error' && (
                <div className="v3-remote-artifact-preview-state error" role="alert">
                  <span>预览加载失败</span>
                  <a href={url} rel="noopener noreferrer" target="_blank">在新标签页打开</a>
                </div>
              )}
            </div>
          ) : isPdf ? (
            shouldUseSheetMode ? (
              <MobilePdfPreview url={fetchableMediaURL(url)} />
            ) : (
              <iframe src={url} className="v3-file-preview-frame" title="PDF Preview" />
            )
          ) : loadingText ? (
            <div className="v3-file-preview-state">加载中...</div>
          ) : previewError ? (
            <div className="v3-file-preview-state error">{previewError}</div>
          ) : isHtml ? (
            <iframe
              className="v3-file-preview-frame"
              title="HTML Report Preview"
              sandbox={HTML_PREVIEW_SANDBOX}
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
          ) : isSpreadsheet ? (
            <SpreadsheetPreview buffer={binaryContent} kind={descriptor.spreadsheetKind} />
          ) : (
            <pre className="v3-file-preview-text">{textContent || '暂无可预览内容。'}</pre>
          )}
        </div>
        <div className="v3-file-preview-mobile-actions">
          <button type="button" onClick={onClose}>
            <X size={17} />
            <span>关闭预览</span>
          </button>
          {isRemoteArtifact ? (
            <a href={url} target="_blank" rel="noopener noreferrer">
              <ExternalLink size={17} />
              <span>新标签页打开</span>
            </a>
          ) : (
            <PwaDownloadLink href={downloadURL} download={file.name || true} target="_blank" rel="noopener noreferrer">
              <Download size={17} />
              <span>下载原文件</span>
            </PwaDownloadLink>
          )}
        </div>
      </aside>
    </>
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
