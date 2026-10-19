import { useSyncExternalStore } from 'react';

export function isLiquidLight() {
  return typeof document !== 'undefined'
    && document.documentElement.dataset.theme === 'liquid'
    && document.documentElement.dataset.liquidVariant !== 'green';
}

function subscribeTheme(onChange) {
  const observer = new MutationObserver(onChange);
  observer.observe(document.documentElement, {
    attributes: true, attributeFilter: ['data-theme', 'data-liquid-variant'],
  });
  return () => observer.disconnect();
}

export function useLiquidConversation() {
  return useSyncExternalStore(subscribeTheme, isLiquidLight, () => false);
}

export const CONVERSATION_ACTIVITY_TIMEOUT = 30_000;

export function mergeConversationTaskStatus(current, status, topic) {
  if (status?.topic_id !== topic || !status.source_uid || !status.run_id
    || !['running', 'waiting', 'completed', 'failed', 'cancelled', 'stale', 'idle'].includes(status.state)
    || !Number.isFinite(Date.parse(status.updated_at))) return current;
  const key = `${Number(status.source_uid)}:${status.run_id}`;
  const prior = current[key];
  if (prior && Date.parse(status.updated_at) <= Date.parse(prior.updated_at)) return current;
  // Never let an active state resurrect a run that already reached a
  // terminal state: mirrors the server's ErrConversationTaskRunTerminal
  // rule and guards against HTTP-fallback reordering on the publisher.
  if (prior && ['completed', 'failed', 'cancelled', 'stale'].includes(prior.state)
    && ['running', 'waiting'].includes(status.state)) return current;
  return { ...current, [key]: status };
}

export function isConversationProcessMessage(message) {
  if (message?._display_text_role === 'process') return true;
  if (message?.metadata?.presentation_role === 'process') return true;
  const blocks = message?.content_blocks;
  return Array.isArray(blocks) && blocks.length > 0
    && blocks.every(block => block?.type === 'assistant_text'
      || (block?.type === 'text' && block.presentation_role === 'process'));
}

export const EXECUTION_LABELS = {
  running: '正在执行',
  idle: '暂未收到新进展',
  disconnected: '连接已断开，进展暂时无法更新',
  reconnecting: '连接已恢复，等待新的进展',
  stopping: '正在发送停止请求',
  'stop-requested': '停止请求已送达，等待 Agent 确认',
  interrupted: '回复已中断，执行结果待确认',
  replied: '已回复',
  completed: '已完成',
  failed: '执行失败',
  cancelled: '已停止',
  stale: '任务已自动中止',
  waiting: '等待 Agent 继续处理',
};
