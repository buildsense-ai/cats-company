/**
 * 小八 (Xiaoba) — Cats Company AI Assistant Bot (TypeScript)
 *
 * Connects via the @catscompany/bot-sdk WebSocket SDK and calls an
 * OpenAI-compatible LLM API to generate replies.
 *
 * Environment variables:
 *   BOT_API_KEY   - Bot API key (required)
 *   BOT_BODY_ID   - Stable runtime/body id (required)
 *   BOT_WS_URL    - WebSocket URL (default: ws://localhost:6061/v0/channels)
 *   LLM_API_BASE  - LLM endpoint (default: https://api.openai.com/v1)
 *   LLM_API_KEY   - LLM bearer token
 *   LLM_MODEL     - Model name (default: gpt-3.5-turbo)
 *   MAX_HISTORY   - Max conversation turns per topic (default: 20)
 */

import {
  CatsBot,
  MessageContext,
  type ConversationTaskStatusInput,
  type GatewayAnnotationReply,
} from '@catscompany/bot-sdk';

// --- Configuration ---

const BOT_API_KEY = process.env.BOT_API_KEY ?? '';
const BOT_BODY_ID = process.env.BOT_BODY_ID ?? '';
const BOT_WS_URL = process.env.BOT_WS_URL ?? 'ws://localhost:6061/v0/channels';
const LLM_API_BASE = process.env.LLM_API_BASE ?? 'https://api.openai.com/v1';
const LLM_API_KEY = process.env.LLM_API_KEY ?? '';
const LLM_MODEL = process.env.LLM_MODEL ?? 'gpt-3.5-turbo';
const MAX_HISTORY = parseInt(process.env.MAX_HISTORY ?? '20', 10);

const SYSTEM_PROMPT = `你是 Cats Company 的 AI 助手「小八」。你友好、有帮助、简洁。
用中文回复，除非用户使用其他语言。保持回复简短自然，像朋友聊天一样。

当用户消息里带有应用标注上下文（「Gateway 标注」），且你的回复明确指向其中某一处标注时，
在回复末尾追加一行 [[ANNOTATION_REPLY]]标记，格式：
[[ANNOTATION_REPLY]]{"annotation_index": <标注序号>, "note": "<一句话，≤30字>"}
标注序号就是上下文里 [1] [2] 的编号。不指向具体标注时不要输出该标记。`;

// The agent point-back marker. The LLM names one annotation it is
// referring to; the target anchor comes from the server-validated
// annotation context, never from model output, so the model cannot
// point at an element the user never annotated.
const ANNOTATION_REPLY_MARKER = /\[\[ANNOTATION_REPLY\]\]\s*(\{[^\n]*\})?/;

// --- Conversation history ---

interface ChatMessage {
  role: 'user' | 'assistant' | 'system';
  content: string;
}

const conversations = new Map<string, ChatMessage[]>();
const topicTasks = new Map<string, Promise<void>>();
const topicStatusTasks = new Map<string, Promise<void>>();
const TASK_STATUS_ACK_TIMEOUT_MS = 1_000;

function getHistory(topic: string): ChatMessage[] {
  let h = conversations.get(topic);
  if (!h) {
    h = [];
    conversations.set(topic, h);
  }
  return h;
}

function addMessage(topic: string, role: 'user' | 'assistant', content: string): void {
  const h = getHistory(topic);
  h.push({ role, content });
  if (h.length > MAX_HISTORY) {
    conversations.set(topic, h.slice(-MAX_HISTORY));
  }
}

// --- LLM call ---

async function callLLM(topic: string, userMessage: string): Promise<string> {
  addMessage(topic, 'user', userMessage);

  const messages: ChatMessage[] = [
    { role: 'system', content: SYSTEM_PROMPT },
    ...getHistory(topic),
  ];

  const headers: Record<string, string> = { 'Content-Type': 'application/json' };
  if (LLM_API_KEY) {
    headers['Authorization'] = `Bearer ${LLM_API_KEY}`;
  }

  try {
    const res = await fetch(`${LLM_API_BASE}/chat/completions`, {
      method: 'POST',
      headers,
      body: JSON.stringify({
        model: LLM_MODEL,
        messages,
        max_tokens: 1024,
      }),
      signal: AbortSignal.timeout(30_000),
    });

    if (!res.ok) {
      throw new Error(`LLM API ${res.status}`);
    }

    const data = await res.json() as any;
    const reply: string = data.choices[0].message.content.trim();
    // History keeps the human-facing text only; the point-back marker is
    // re-derived by annotationPointBack and must not teach the model to
    // emit markers on every turn.
    addMessage(topic, 'assistant', reply.replace(ANNOTATION_REPLY_MARKER, '').trim());
    return reply;
  } catch (err: unknown) {
    console.error(`[llm] call failed: ${errorMessage(err)}`);
    throw err;
  }
}

function taskRunID(): string {
  return `llm_${Date.now()}_${Math.random().toString(36).slice(2, 10)}`;
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

function isTerminalTaskStatus(state: ConversationTaskStatusInput['state']): boolean {
  return state === 'completed' || state === 'failed' || state === 'cancelled' || state === 'stale';
}

function delay(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

async function sendTaskStatusWithTimeout(
  context: MessageContext,
  status: ConversationTaskStatusInput,
): Promise<void> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  try {
    await Promise.race([
      context.bot.sendTaskStatus(context.topic, status),
      new Promise<never>((_, reject) => {
        timer = setTimeout(() => reject(new Error('task_status acknowledgement timed out')), TASK_STATUS_ACK_TIMEOUT_MS);
      }),
    ]);
  } finally {
    if (timer) {
      clearTimeout(timer);
    }
  }
}

async function publishTaskStatus(
  context: MessageContext,
  status: ConversationTaskStatusInput,
): Promise<void> {
  const attempts = isTerminalTaskStatus(status.state) ? 3 : 1;
  for (let attempt = 1; attempt <= attempts; attempt++) {
    try {
      await sendTaskStatusWithTimeout(context, status);
      return;
    } catch (error: unknown) {
      if (attempt === attempts) {
        // Status is auxiliary: it must never keep the assistant from replying.
        console.error(`[task_status] publish failed: ${errorMessage(error)}`);
        return;
      }
      await delay(attempt * 250);
    }
  }
}

function enqueueTopicStatus(
  context: MessageContext,
  status: ConversationTaskStatusInput,
): void {
  const previous = topicStatusTasks.get(context.topic) ?? Promise.resolve();
  const next = previous.catch(() => undefined).then(() => publishTaskStatus(context, status));
  topicStatusTasks.set(context.topic, next);
  void next.finally(() => {
    if (topicStatusTasks.get(context.topic) === next) {
      topicStatusTasks.delete(context.topic);
    }
  }).catch(() => undefined);
}

function enqueueTopicTask(topic: string, task: () => Promise<void>): Promise<void> {
  const previous = topicTasks.get(topic) ?? Promise.resolve();
  const next = previous.catch(() => undefined).then(task);
  topicTasks.set(topic, next);
  void next.finally(() => {
    if (topicTasks.get(topic) === next) {
      topicTasks.delete(topic);
    }
  }).catch(() => undefined);
  return next;
}

interface ParsedReply {
  text: string;
  pointBack: GatewayAnnotationReply | null;
}

function annotationPointBack(
  context: MessageContext,
  raw: string,
): ParsedReply {
  const match = ANNOTATION_REPLY_MARKER.exec(raw);
  if (!match) return { text: raw, pointBack: null };
  // The marker is control syntax, never user-facing text: strip it even
  // when no annotation context is present to point at.
  const text = raw.replace(ANNOTATION_REPLY_MARKER, '').trimEnd() || raw;
  const ctx = context.gatewayAnnotations;
  if (!ctx?.app_id || !Array.isArray(ctx.annotations)) {
    return { text, pointBack: null };
  }
  if (!match[1]) return { text, pointBack: null };

  let index: number | null = null;
  let note: string | undefined;
  try {
    const parsed = JSON.parse(match[1]);
    if (Number.isInteger(parsed.annotation_index)) index = parsed.annotation_index;
    if (typeof parsed.note === 'string' && parsed.note.trim()) {
      note = parsed.note.trim().slice(0, 120);
    }
  } catch {
    return { text, pointBack: null };
  }
  const annotation = index !== null
    ? (ctx.annotations.find((entry) => entry.index === index) ?? ctx.annotations[index - 1])
    : null;
  const source = annotation?.target;
  if (!source || typeof source !== 'object') return { text, pointBack: null };

  const target: GatewayAnnotationReply['target'] = {};
  if (typeof source.element_id === 'string') target.element_id = source.element_id;
  if (typeof source.selector === 'string') target.selector = source.selector;
  if (!target.element_id && !target.selector && source.rect && source.coordinate_space === 'viewport') {
    target.rect = source.rect;
  }
  if (!target.element_id && !target.selector && !target.rect) return { text, pointBack: null };
  return {
    text,
    pointBack: { app_id: ctx.app_id, target, ...(note ? { note } : {}) },
  };
}

async function handleMessage(context: MessageContext): Promise<void> {
  console.log(`[msg] from=${context.from} topic=${context.topic} text="${context.text}"`);
  const runID = taskRunID();
  enqueueTopicStatus(context, {
    run_id: runID,
    state: 'running',
    summary: '正在生成回复',
  });

  let reply: string;
  try {
    reply = await context.withTyping(() => callLLM(context.topic, context.text));
  } catch (error: unknown) {
    console.error(`[error] task failed: ${errorMessage(error)}`);
    try {
      await context.reply('抱歉，我暂时无法回复，请稍后再试。');
    } catch (replyError: unknown) {
      console.error(`[error] fallback reply failed: ${errorMessage(replyError)}`);
    }
    enqueueTopicStatus(context, {
      run_id: runID,
      state: 'failed',
      summary: '任务执行失败',
      error: '任务执行失败',
    });
    return;
  }

  try {
    const { text, pointBack } = annotationPointBack(context, reply);
    console.log(`[reply] → ${context.topic}: ${text.slice(0, 80)}${text.length > 80 ? '...' : ''}${pointBack ? ' [point-back]' : ''}`);
    if (pointBack) {
      await context.replyWithAnnotation(text, pointBack);
    } else {
      await context.reply(text);
    }
  } catch (error: unknown) {
    console.error(`[error] reply acknowledgement failed: ${errorMessage(error)}`);
    enqueueTopicStatus(context, {
      run_id: runID,
      state: 'failed',
      summary: '回复状态未确认',
      error: '回复状态未确认',
    });
    return;
  }

  enqueueTopicStatus(context, {
    run_id: runID,
    state: 'completed',
    summary: '回复已完成',
  });
}

// --- Main ---

function main(): void {
  if (!BOT_API_KEY) {
    console.error('BOT_API_KEY environment variable is required');
    process.exit(1);
  }

  if (!BOT_BODY_ID) {
    console.error('BOT_BODY_ID environment variable is required');
    process.exit(1);
  }

  if (!LLM_API_KEY) {
    console.warn('[warn] LLM_API_KEY not set — LLM calls will likely fail');
  }

  const bot = new CatsBot({
    serverUrl: BOT_WS_URL,
    apiKey: BOT_API_KEY,
    bodyId: BOT_BODY_ID,
  });

  bot.on('ready', (uid) => {
    console.log(`[ready] 小八 online as ${uid}`);
    console.log(`  model: ${LLM_MODEL}`);
    console.log(`  api base: ${LLM_API_BASE}`);
  });

  bot.on('message', (context: MessageContext) =>
    enqueueTopicTask(context.topic, () => handleMessage(context)));

  bot.on('disconnect', (code, reason) => {
    console.log(`[disconnect] code=${code} reason=${reason}`);
  });

  bot.on('reconnecting', (attempt) => {
    console.log(`[reconnecting] attempt #${attempt}`);
  });

  bot.on('error', (err) => {
    console.error(`[error] ${err.message}`);
  });

  console.log(`Starting 小八, server=${BOT_WS_URL}`);
  bot.run().catch((err) => {
    console.error('Fatal:', err);
    process.exit(1);
  });
}

main();
