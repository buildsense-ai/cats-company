import http from 'node:http';
import { ShimoWorkerError } from './reader.mjs';
import { loginHTML } from './login_page.mjs';
import { attachLoginStream } from './login_stream.mjs';

const MAX_BODY_BYTES = 20 * 1024 * 1024;
const RANGE_PATTERN = /^[A-Z]{1,3}[1-9][0-9]{0,6}:[A-Z]{1,3}[1-9][0-9]{0,6}$/;

export const DEFAULT_MAX_CONCURRENCY = 2;
export const DEFAULT_MAX_QUEUE = 8;

// One browser session costs hundreds of megabytes, so the number of concurrent
// Chromium contexts must stay well below the container memory limit. Work over
// that bound waits in a short FIFO queue and is rejected with 503 once the queue
// is full, which keeps a burst of requests from taking the Worker down.
export function createConcurrencyLimiter({ maxConcurrency = DEFAULT_MAX_CONCURRENCY, maxQueue = DEFAULT_MAX_QUEUE } = {}) {
  const concurrency = Math.max(1, Math.floor(Number(maxConcurrency)) || 1);
  const queueLimit = Math.max(0, Math.floor(Number(maxQueue)) || 0);
  let active = 0;
  const waiting = [];
  function release() {
    active -= 1;
    while (active < concurrency && waiting.length > 0) {
      const entry = waiting.shift();
      active += 1;
      Promise.resolve().then(entry.task).then(entry.resolve, entry.reject).finally(release);
    }
  }
  return {
    run(task) {
      return new Promise((resolve, reject) => {
        if (active < concurrency && waiting.length === 0) {
          active += 1;
          Promise.resolve().then(task).then(resolve, reject).finally(release);
          return;
        }
        if (waiting.length >= queueLimit) {
          reject(new ShimoWorkerError('WORKER_BUSY', '浏览器 Worker 繁忙，请稍后重试。', 503));
          return;
        }
        waiting.push({ task, resolve, reject });
      });
    },
    stats: () => ({ active, queued: waiting.length }),
  };
}

export function createShimoWorkerServer({ internalToken, store, reader, loginManager, limits, loginFrameIntervalMs }) {
  if (String(internalToken || '').length < 32) throw new Error('SHIMO_WORKER_TOKEN must contain at least 32 characters');
  const limiter = createConcurrencyLimiter(limits);
  const server = http.createServer(async (request, response) => {
    try {
      const url = new URL(request.url, 'http://worker.local');
      if (request.method === 'GET' && url.pathname === '/healthz') return sendJSON(response, 200, { ok: true });
      if (url.pathname.startsWith('/shimo-login/')) return await handlePublicLogin(request, response, url, loginManager);
      if (request.headers.authorization !== `Bearer ${internalToken}`) {
        return sendError(response, new ShimoWorkerError('UNAUTHORIZED', '无权访问浏览器 Worker。', 401));
      }
      if (request.method !== 'POST') return sendError(response, new ShimoWorkerError('METHOD_NOT_ALLOWED', '请求方法不受支持。', 405));
      const body = await readJSON(request);
      rejectUnknown(body, allowedFields(url.pathname));
      const binding = String(body.connection_binding || '');

      if (url.pathname === '/v1/sessions/status') {
        const record = store.load(binding);
        const pending = !record ? loginManager.statusForBinding?.(binding) : null;
        return sendJSON(response, 200, { ok: true, data: record ? {
          state: 'connected', account_hint: record.accountHint, verified_at: record.verifiedAt,
        } : pending || { state: 'disconnected' } });
      }
      if (url.pathname === '/v1/sessions/disconnect') {
        store.delete(binding);
        return sendJSON(response, 200, { ok: true, data: { state: 'disconnected' } });
      }
      if (url.pathname === '/v1/sessions/login') {
		const callbackURL = String(body.completion_callback_url || '').trim();
		const completionToken = String(body.completion_token || '').trim();
		if ((callbackURL === '') !== (completionToken === '') || (completionToken && !/^[0-9a-f]{64}$/.test(completionToken))) {
		  throw new ShimoWorkerError('INVALID_ARGUMENTS', '登录完成回调参数无效。', 400);
		}
		if (callbackURL) {
		  const parsed = new URL(callbackURL);
		  const internalHTTP = parsed.protocol === 'http:' && (parsed.hostname === '127.0.0.1' || parsed.hostname === 'localhost' || !parsed.hostname.includes('.'));
		  if ((parsed.protocol !== 'https:' && !internalHTTP) || parsed.username || parsed.password || parsed.search || parsed.hash) {
		    throw new ShimoWorkerError('INVALID_ARGUMENTS', '登录完成回调地址无效。', 400);
		  }
		}
		const result = await limiter.run(() => loginManager.start(binding, callbackURL ? { callbackURL, completionToken } : null));
        return sendJSON(response, 200, { ok: true, data: result });
      }

      const record = store.load(binding);
      if (!record) throw new ShimoWorkerError('LOGIN_REQUIRED', '当前用户尚未连接石墨。', 409);
      if (url.pathname === '/v1/sheets/list') {
        const data = await limiter.run(() => reader.listSheets(record.storageState, String(body.url || '')));
        return sendJSON(response, 200, { ok: true, data });
      }
      if (url.pathname === '/v1/sheets/read') {
        const sheetName = String(body.sheet_name || '').trim();
        const cellRange = String(body.range || '').trim().toUpperCase();
        if (!sheetName || [...sheetName].length > 200 || !RANGE_PATTERN.test(cellRange)) {
          throw new ShimoWorkerError('INVALID_ARGUMENTS', '工作表名称或范围无效。', 400);
        }
        const data = await limiter.run(() => reader.readSheet(record.storageState, String(body.url || ''), sheetName, cellRange));
        return sendJSON(response, 200, { ok: true, data });
      }
      if (url.pathname === '/v1/documents/read') {
        const maxChars = Number(body.max_chars);
        if (!Number.isInteger(maxChars) || maxChars < 1000 || maxChars > 500000) {
          throw new ShimoWorkerError('INVALID_ARGUMENTS', 'max_chars 必须介于 1000 与 500000。', 400);
        }
        const data = await limiter.run(() => reader.readDocument(record.storageState, String(body.url || ''), maxChars));
        return sendJSON(response, 200, { ok: true, data });
      }
      throw new ShimoWorkerError('NOT_FOUND', '接口不存在。', 404);
    } catch (error) {
      sendError(response, normalizeError(error));
    }
  });
  attachLoginStream(server, loginManager, Number.isFinite(loginFrameIntervalMs) ? { frameIntervalMs: loginFrameIntervalMs } : {});
  return server;
}

async function handlePublicLogin(request, response, url, manager) {
  const match = url.pathname.match(/^\/shimo-login\/([0-9a-f]{64})\/(status|screenshot|input|reset)?$/);
  setPublicHeaders(response);
  if (!match) {
    if (request.method === 'GET') return sendLoginNotice(response, 404, '登录链接无效', '这个地址不完整，请回到聊天重新打开一次性登录链接。');
    throw new ShimoWorkerError('NOT_FOUND', '登录链接不存在。', 404);
  }
  const [, token, action = 'page'] = match;
  if (request.method === 'GET' && action === 'page') {
    try {
      manager.status(token);
    } catch (error) {
      const normalized = normalizeError(error);
      return sendLoginNotice(response, normalized.status || 410, '登录链接已失效', '请回到聊天重新发起，虚拟员工会给你一条新的登录链接。');
    }
    response.writeHead(200, { 'content-type': 'text/html; charset=utf-8' });
    return response.end(loginHTML(token));
  }
  if (request.method === 'GET' && action === 'status') return sendJSON(response, 200, { ok: true, data: manager.status(token) });
  if (request.method === 'GET' && action === 'screenshot') {
    const png = await manager.screenshot(token);
    response.writeHead(200, { 'content-type': 'image/png', 'content-length': png.length });
    return response.end(png);
  }
  // Shimo opens its legal documents in new tabs from the consent line under the
  // login button.  If the visitor ends up on such a page, this puts the login
  // form back on the surface instead of leaving a document nobody can leave.
  if (request.method === 'POST' && action === 'reset') {
    return sendJSON(response, 200, { ok: true, data: await manager.reset(token) });
  }
  if (request.method === 'POST' && action === 'input') {
    const body = await readJSON(request, 4096);
    rejectUnknown(body, new Set(['action', 'x', 'y', 'count', 'value', 'delta_x', 'delta_y']));
    return sendJSON(response, 200, { ok: true, data: await manager.input(token, body) });
  }
  throw new ShimoWorkerError('METHOD_NOT_ALLOWED', '请求方法不受支持。', 405);
}

function sendLoginNotice(response, status, title, detail) {
  const body = Buffer.from(`<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width,initial-scale=1">
  <title>${title}</title>
  <style>
    body{margin:0;background:#f4f7f4;color:#142019;font-family:system-ui,-apple-system,"Segoe UI",sans-serif}
    main{width:min(560px,100%);margin:14vh auto;padding:0 18px;text-align:center}
    h1{font-size:22px;margin:0 0 10px}
    p{margin:0;color:#526158;font-size:15px;line-height:1.6}
  </style>
</head>
<body><main><h1>${title}</h1><p>${detail}</p></main></body>
</html>`, 'utf8');
  response.writeHead(status, { 'content-type': 'text/html; charset=utf-8', 'content-length': body.length });
  response.end(body);
}

function allowedFields(pathname) {
  const common = new Set(['connection_binding']);
  if (pathname === '/v1/sessions/status' || pathname === '/v1/sessions/disconnect') return common;
  if (pathname === '/v1/sessions/login') return new Set([...common, 'completion_callback_url', 'completion_token']);
  if (pathname === '/v1/sheets/list') return new Set([...common, 'url']);
  if (pathname === '/v1/sheets/read') return new Set([...common, 'url', 'sheet_name', 'range']);
  if (pathname === '/v1/documents/read') return new Set([...common, 'url', 'max_chars']);
  return new Set();
}

function rejectUnknown(body, allowed) {
  if (!body || typeof body !== 'object' || Array.isArray(body)) throw new ShimoWorkerError('INVALID_ARGUMENTS', '请求体必须是 JSON 对象。', 400);
  for (const key of Object.keys(body)) if (!allowed.has(key)) throw new ShimoWorkerError('INVALID_ARGUMENTS', `请求包含未知字段：${key}`, 400);
}

async function readJSON(request, limit = MAX_BODY_BYTES) {
  const chunks = [];
  let size = 0;
  for await (const chunk of request) {
    size += chunk.length;
    if (size > limit) throw new ShimoWorkerError('REQUEST_TOO_LARGE', '请求体过大。', 413);
    chunks.push(chunk);
  }
  try { return JSON.parse(Buffer.concat(chunks).toString('utf8')); } catch {
    throw new ShimoWorkerError('INVALID_ARGUMENTS', '请求 JSON 无效。', 400);
  }
}

function normalizeError(error) {
  if (error instanceof ShimoWorkerError) return error;
  if (String(error?.message || '').includes('connection binding')) return new ShimoWorkerError('INVALID_ARGUMENTS', '连接标识无效。', 400);
  return new ShimoWorkerError('WORKER_ERROR', '浏览器 Worker 处理失败。', 502);
}

function sendError(response, error) {
  sendJSON(response, error.status || 502, { ok: false, error: { code: error.code || 'WORKER_ERROR', message: error.message } });
}

function sendJSON(response, status, value) {
  if (response.headersSent) return;
  const body = Buffer.from(JSON.stringify(value), 'utf8');
  response.writeHead(status, { 'content-type': 'application/json; charset=utf-8', 'content-length': body.length, 'cache-control': 'no-store', 'x-content-type-options': 'nosniff' });
  response.end(body);
}

function setPublicHeaders(response) {
  response.setHeader('cache-control', 'no-store');
  response.setHeader('referrer-policy', 'no-referrer');
  response.setHeader('x-content-type-options', 'nosniff');
  response.setHeader('content-security-policy', "default-src 'self'; connect-src 'self'; img-src 'self' blob:; style-src 'unsafe-inline'; script-src 'unsafe-inline'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'");
}
