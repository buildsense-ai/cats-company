import http from 'node:http';
import { ShimoWorkerError } from './reader.mjs';

const MAX_BODY_BYTES = 20 * 1024 * 1024;
const RANGE_PATTERN = /^[A-Z]{1,3}[1-9][0-9]{0,6}:[A-Z]{1,3}[1-9][0-9]{0,6}$/;

export function createShimoWorkerServer({ internalToken, store, reader, loginManager }) {
  if (String(internalToken || '').length < 32) throw new Error('SHIMO_WORKER_TOKEN must contain at least 32 characters');
  return http.createServer(async (request, response) => {
    try {
      const url = new URL(request.url, 'http://worker.local');
      if (request.method === 'GET' && url.pathname === '/healthz') return sendJSON(response, 200, { ok: true });
      if (url.pathname.startsWith('/shimo-login/')) return handlePublicLogin(request, response, url, loginManager);
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
		const result = await loginManager.start(binding, callbackURL ? { callbackURL, completionToken } : null);
        return sendJSON(response, 200, { ok: true, data: result });
      }

      const record = store.load(binding);
      if (!record) throw new ShimoWorkerError('LOGIN_REQUIRED', '当前用户尚未连接石墨。', 409);
      if (url.pathname === '/v1/sheets/list') {
        const data = await reader.listSheets(record.storageState, String(body.url || ''));
        return sendJSON(response, 200, { ok: true, data });
      }
      if (url.pathname === '/v1/sheets/read') {
        const sheetName = String(body.sheet_name || '').trim();
        const cellRange = String(body.range || '').trim().toUpperCase();
        if (!sheetName || [...sheetName].length > 200 || !RANGE_PATTERN.test(cellRange)) {
          throw new ShimoWorkerError('INVALID_ARGUMENTS', '工作表名称或范围无效。', 400);
        }
        const data = await reader.readSheet(record.storageState, String(body.url || ''), sheetName, cellRange);
        return sendJSON(response, 200, { ok: true, data });
      }
      if (url.pathname === '/v1/documents/read') {
        const maxChars = Number(body.max_chars);
        if (!Number.isInteger(maxChars) || maxChars < 1000 || maxChars > 500000) {
          throw new ShimoWorkerError('INVALID_ARGUMENTS', 'max_chars 必须介于 1000 与 500000。', 400);
        }
        const data = await reader.readDocument(record.storageState, String(body.url || ''), maxChars);
        return sendJSON(response, 200, { ok: true, data });
      }
      throw new ShimoWorkerError('NOT_FOUND', '接口不存在。', 404);
    } catch (error) {
      sendError(response, normalizeError(error));
    }
  });
}

async function handlePublicLogin(request, response, url, manager) {
  const match = url.pathname.match(/^\/shimo-login\/([0-9a-f]{64})\/(status|screenshot|input)?$/);
  if (!match) throw new ShimoWorkerError('NOT_FOUND', '登录链接不存在。', 404);
  const [, token, action = 'page'] = match;
  setPublicHeaders(response);
  if (request.method === 'GET' && action === 'page') {
    manager.status(token);
    response.writeHead(200, { 'content-type': 'text/html; charset=utf-8' });
    return response.end(loginHTML(token));
  }
  if (request.method === 'GET' && action === 'status') return sendJSON(response, 200, { ok: true, data: manager.status(token) });
  if (request.method === 'GET' && action === 'screenshot') {
    const png = await manager.screenshot(token);
    response.writeHead(200, { 'content-type': 'image/png', 'content-length': png.length });
    return response.end(png);
  }
  if (request.method === 'POST' && action === 'input') {
    const body = await readJSON(request, 4096);
    rejectUnknown(body, new Set(['action', 'x', 'y', 'value']));
    return sendJSON(response, 200, { ok: true, data: await manager.input(token, body) });
  }
  throw new ShimoWorkerError('METHOD_NOT_ALLOWED', '请求方法不受支持。', 405);
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
  response.setHeader('content-security-policy', "default-src 'self'; img-src 'self'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'");
}

function loginHTML(token) {
  const base = `/shimo-login/${token}`;
  return `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>连接石墨</title><style>body{margin:0;background:#f5f7f5;color:#142019;font-family:system-ui,sans-serif}main{max-width:1280px;margin:auto;padding:16px}.bar{display:flex;gap:10px;align-items:center;flex-wrap:wrap;margin-bottom:12px}.screen{width:100%;height:auto;border:1px solid #cad6cd;border-radius:10px;background:white;cursor:crosshair}.text{flex:1;min-width:220px;padding:10px;border:1px solid #aebbb1;border-radius:8px}button{padding:10px 14px;border:0;border-radius:8px;background:#2f7d42;color:white}.hint{color:#526158}</style></head><body><main><h2>连接石墨</h2><p id="status" class="hint">正在加载登录页面…</p><div class="bar"><input id="text" class="text" placeholder="需要输入手机号或验证码时：先点击下方对应输入框，再在这里输入"><button id="send">输入文字</button><button data-key="Tab">Tab</button><button data-key="Enter">Enter</button><button data-key="Backspace">退格</button></div><img id="screen" class="screen" alt="石墨登录页面"></main><script>const base=${JSON.stringify(base)};const img=document.getElementById('screen');const status=document.getElementById('status');async function api(path,options){const r=await fetch(base+path,options);const j=await r.json();if(!r.ok)throw new Error(j.error?.message||'操作失败');return j.data}async function refresh(){try{const s=await api('/status');status.textContent=s.message;if(s.state==='waiting'||s.state==='opening'){img.src=base+'/screenshot?t='+Date.now();setTimeout(refresh,1200)}else{img.remove()}}catch(e){status.textContent=e.message}}img.onclick=async e=>{const r=img.getBoundingClientRect();await api('/input',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({action:'click',x:(e.clientX-r.left)*img.naturalWidth/r.width,y:(e.clientY-r.top)*img.naturalHeight/r.height})});refresh()};document.getElementById('send').onclick=async()=>{const el=document.getElementById('text');if(el.value){await api('/input',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({action:'text',value:el.value})});el.value=''}};document.querySelectorAll('[data-key]').forEach(b=>b.onclick=()=>api('/input',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({action:'key',value:b.dataset.key})}));refresh();</script></body></html>`;
}
