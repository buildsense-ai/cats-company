#!/usr/bin/env node
/**
 * 现场自检：把「bot 当工具」（agent-as-tool / catsco MCP）这条链路一次跑通。
 *
 * 为什么要有这个脚本：验这条链路以前靠临时探针，而探针反过来坑过三次 ——
 *   - HTTP 分片把 SSE 的 `data:` 行切成两半，JSON 解析失败被当成「空工具面」，
 *     于是把「bot 变成 0 个工具」当成回归上报；
 *   - 只看 `content[0].text` 就断言「结果拿不到」，其实正文在 `structuredContent.output.text`；
 *   - 把某个配置组合的失败说成「整个功能不可用」。
 * 结论：这类验证必须固化，且解析要按 SSE 事件边界来。
 *
 * 用法（全部只读，除 --call 会真起一单）：
 *
 *   node scripts/verify-agent-tools.mjs --uid 1619 --secret "$CATSCO_JWT_SECRET"
 *       # 打印这个身份能看到的工具清单
 *
 *   node scripts/verify-agent-tools.mjs --uid 1619 --secret "$SECRET" \
 *       --call catsco_my_tool --text "只回复 OK"
 *       # 真调一次，等到终态，打印状态与输出
 *
 *   node scripts/verify-agent-tools.mjs --token "$JWT" --uid 38 --json
 *       # 已有 token 时直接用；--json 便于接别的脚本
 *
 * 退出码：0 成功；1 结果不符预期；2 参数问题；3 调用没等到终态。
 */
import { createHmac } from 'node:crypto';
import { pathToFileURL } from 'node:url';

export const DEFAULT_BASE = 'https://api.catsco.cc';
const DEFAULT_POLL_INTERVAL_MS = 4_000;
const DEFAULT_MAX_WAIT_MS = 6 * 60_000;

/**
 * 解析 SSE 响应体。
 *
 * 两个必须的步骤（以前都栽过）：
 *   1. 先按**空行**切事件 —— 一个事件的多行 `data:` 要拼起来再解析；
 *   2. `event:` / `id:` / 注释行（`:` 开头）都不是数据。
 *
 * `fetch` 已经解掉了 chunked 传输层，所以这里只处理事件层；如果哪天改成裸 socket
 * 读字节，记得先在传输层去分片。
 */
export function parseSseEvents(body) {
  if (typeof body !== 'string' || body.length === 0) return [];
  const out = [];
  const blocks = body.replace(/\r\n/g, '\n').split('\n\n');
  for (const block of blocks) {
    const dataLines = [];
    for (const line of block.split('\n')) {
      if (!line || line.startsWith(':')) continue;
      if (!line.startsWith('data:')) continue;
      dataLines.push(line.slice(5).replace(/^ /, ''));
    }
    if (dataLines.length === 0) continue;
    const payload = dataLines.join('\n');
    if (!payload.trim()) continue;
    try {
      out.push(JSON.parse(payload));
    } catch {
      // 半截事件不该让整个验证静默通过：留一条可追踪的痕迹。
      out.push({ __unparsed: payload });
    }
  }
  return out;
}

function base64url(buffer) {
  return Buffer.from(buffer).toString('base64').replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

/** 造一个与平台一致的 HS256 用户 token（字段与 server/auth.go 的 JWTClaims 对齐）。 */
export function mintUserToken(secret, uid, { ttlSeconds = 3600, username = 'verify-agent-tools' } = {}) {
  if (!secret) throw new Error('缺少 JWT secret');
  const now = Math.floor(Date.now() / 1000);
  const header = { alg: 'HS256', typ: 'JWT' };
  const claims = {
    token_type: 'user',
    userId: Number(uid),
    username,
    email: '',
    jti: Buffer.from(String(Math.random())).toString('hex').slice(0, 16),
    iat: now,
    iss: 'catscompany',
    exp: now + ttlSeconds,
  };
  const signing = `${base64url(JSON.stringify(header))}.${base64url(JSON.stringify(claims))}`;
  const sig = createHmac('sha256', secret).update(signing).digest();
  return `${signing}.${base64url(sig)}`;
}

/** 从 tools/call 的响应里取出结构化结果（正文在 structuredContent，不在 content[0].text）。 */
export function extractStructuredContent(events) {
  for (const event of events) {
    const result = event?.result;
    if (result && typeof result === 'object' && result.structuredContent) return result.structuredContent;
  }
  return undefined;
}

export function extractContentText(events) {
  for (const event of events) {
    const content = event?.result?.content;
    if (Array.isArray(content)) {
      const text = content.filter(item => item?.type === 'text').map(item => item.text).join('\n');
      if (text) return text;
    }
  }
  return '';
}

export function parseArgs(argv) {
  const args = { base: process.env.CATSCO_MCP_BASE || DEFAULT_BASE };
  for (let i = 0; i < argv.length; i += 1) {
    const raw = argv[i];
    if (!raw.startsWith('--')) throw new Error(`看不懂的参数：${raw}`);
    const [flag, inline] = raw.includes('=') ? raw.split(/=(.*)/s) : [raw, undefined];
    const take = () => {
      if (inline !== undefined) return inline;
      const next = argv[i + 1];
      if (next === undefined || next.startsWith('--')) throw new Error(`${flag} 需要一个值`);
      i += 1;
      return next;
    };
    switch (flag) {
      case '--base': args.base = take(); break;
      case '--uid': args.uid = Number(take()); break;
      case '--secret': args.secret = take(); break;
      case '--token': args.token = take(); break;
      case '--bot-uid': args.botUid = Number(take()); break;
      case '--call': args.call = take(); break;
      case '--text': args.text = take(); break;
      case '--session': args.session = take(); break;
      case '--max-wait-ms': args.maxWaitMs = Number(take()); break;
      case '--poll-interval-ms': args.pollIntervalMs = Number(take()); break;
      case '--json': args.json = true; break;
      case '--list': args.list = true; break;
      case '--help': case '-h': args.help = true; break;
      default: throw new Error(`未知参数：${flag}`);
    }
  }
  return args;
}

const HELP = `用法：
  node scripts/verify-agent-tools.mjs --uid <uid> --secret <jwt-secret> [--list]
  node scripts/verify-agent-tools.mjs --uid <uid> --secret <jwt-secret> --call <tool> --text "..."

可选：
  --base <url>              默认 ${DEFAULT_BASE}（或环境变量 CATSCO_MCP_BASE）
  --token <jwt>             直接给 token，免 secret
  --bot-uid <uid>           带 bot 身份调用（账号代 bot）
  --session <id>            续聊会话 id（返问后补话用）
  --max-wait-ms <n>         等终态的上限，默认 ${DEFAULT_MAX_WAIT_MS}
  --poll-interval-ms <n>    轮询间隔，默认 ${DEFAULT_POLL_INTERVAL_MS}
  --json                    机器可读输出
`;

function createSession(base, token) {
  const headers = {
    Authorization: `Bearer ${token}`,
    'Content-Type': 'application/json',
    Accept: 'application/json, text/event-stream',
  };
  let sessionId;
  const rpc = async (payload, { timeoutMs = 30_000 } = {}) => {
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), timeoutMs);
    try {
      const res = await fetch(`${base.replace(/\/$/, '')}/api/mcp`, {
        method: 'POST',
        headers: sessionId ? { ...headers, 'Mcp-Session-Id': sessionId } : headers,
        body: JSON.stringify(payload),
        signal: controller.signal,
      });
      const nextSession = res.headers.get('mcp-session-id');
      if (nextSession) sessionId = nextSession;
      const body = await res.text();
      return { status: res.status, events: parseSseEvents(body), body };
    } finally {
      clearTimeout(timer);
    }
  };

  const initialize = async () => {
    const res = await rpc({
      jsonrpc: '2.0',
      id: 1,
      method: 'initialize',
      params: {
        protocolVersion: '2024-11-05',
        capabilities: {},
        clientInfo: { name: 'verify-agent-tools', version: '1' },
      },
    });
    if (res.status !== 200) {
      throw new Error(`initialize 失败：HTTP ${res.status} ${res.body.slice(0, 200)}`);
    }
    await rpc({ jsonrpc: '2.0', method: 'notifications/initialized' });
    return res;
  };

  return { rpc, initialize };
}

async function listTools(session) {
  const res = await session.rpc({ jsonrpc: '2.0', id: 2, method: 'tools/list' });
  const tools = res.events.find(event => event?.result?.tools)?.result.tools;
  if (!tools) throw new Error(`tools/list 没有返回工具：${res.body.slice(0, 200)}`);
  return tools;
}

async function callTool(session, name, text, { botUid, session: resumeSession }) {
  const args = { input: { text } };
  if (botUid) args.bot_uid = botUid;
  if (resumeSession) args.session_id = resumeSession;
  const res = await session.rpc({
    jsonrpc: '2.0',
    id: 3,
    method: 'tools/call',
    params: { name, arguments: args },
  }, { timeoutMs: 120_000 });
  return {
    structured: extractStructuredContent(res.events),
    text: extractContentText(res.events),
    events: res.events,
  };
}

async function waitForTerminal(session, runId, { pollIntervalMs, maxWaitMs }) {
  const started = Date.now();
  let last = '';
  while (Date.now() - started < maxWaitMs) {
    await new Promise(resolve => setTimeout(resolve, pollIntervalMs));
    const res = await session.rpc({
      jsonrpc: '2.0',
      id: 4,
      method: 'tools/call',
      params: { name: 'catsco_run_status', arguments: { run_id: runId } },
    });
    const structured = extractStructuredContent(res.events);
    const status = structured?.status || '';
    if (status && status !== last) {
      last = status;
      if (!process.env.VERIFY_QUIET) {
        process.stderr.write(`  [${Math.round((Date.now() - started) / 1000)}s] status=${status}\n`);
      }
    }
    if (['completed', 'failed', 'orphaned', 'cancelled', 'canceled', 'expired'].includes(status)) {
      return { structured, text: extractContentText(res.events) };
    }
  }
  return { structured: undefined, text: '', timedOut: true };
}

export async function main(argv = process.argv.slice(2)) {
  const args = parseArgs(argv);
  if (args.help) {
    process.stdout.write(HELP);
    return 0;
  }
  if (!Number.isFinite(args.uid) || args.uid <= 0) throw new Error('--uid 必填且必须是数字');
  const token = args.token || mintUserToken(args.secret, args.uid);
  const session = createSession(args.base, token);
  await session.initialize();

  const tools = await listTools(session);
  const summary = tools.map(tool => ({ name: tool.name, description: tool.description || '' }));
  if (args.json) {
    process.stdout.write(`${JSON.stringify({ tools: summary }, null, 2)}\n`);
  } else {
    process.stdout.write(`身份 uid=${args.uid} 可见工具 ${summary.length} 个：\n`);
    for (const tool of summary) process.stdout.write(`  - ${tool.name}\n`);
  }

  if (!args.call) return 0;
  if (!summary.some(tool => tool.name === args.call)) {
    process.stderr.write(`工具 ${args.call} 不在这个身份的清单里（先解决可见性再谈调用）\n`);
    return 1;
  }
  if (!args.text) throw new Error('--call 时 --text 必填');

  process.stderr.write(`调用 ${args.call} ...\n`);
  const called = await callTool(session, args.call, args.text, {
    botUid: args.botUid,
    session: args.session,
  });
  let structured = called.structured;
  if (!structured?.run_id) {
    process.stderr.write(`没拿到 run_id，原始结果：${called.text || JSON.stringify(called.events).slice(0, 300)}\n`);
    return 1;
  }

  let final = { structured, text: called.text };
  if (structured.status === 'running') {
    final = await waitForTerminal(session, structured.run_id, {
      pollIntervalMs: args.pollIntervalMs ?? DEFAULT_POLL_INTERVAL_MS,
      maxWaitMs: args.maxWaitMs ?? DEFAULT_MAX_WAIT_MS,
    });
    if (final.timedOut) {
      process.stderr.write(`等了 ${args.maxWaitMs ?? DEFAULT_MAX_WAIT_MS}ms 还没终态，run=${structured.run_id}\n`);
      return 3;
    }
  }

  const status = final.structured?.status || 'unknown';
  const output = final.structured?.output?.text ?? '';
  const error = final.structured?.error ?? final.structured?.output?.error ?? '';
  if (args.json) {
    process.stdout.write(`${JSON.stringify({ run_id: structured.run_id, status, output, error }, null, 2)}\n`);
  } else {
    process.stdout.write(`run=${structured.run_id} status=${status}\n`);
    if (output) process.stdout.write(`output: ${output}\n`);
    if (error) process.stdout.write(`error: ${error}\n`);
  }
  return status === 'completed' ? 0 : 1;
}

const invokedDirectly = process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href;
if (invokedDirectly) {
  main()
    .then(code => process.exit(code))
    .catch((error) => {
      process.stderr.write(`${error?.message || error}\n`);
      process.exit(2);
    });
}
