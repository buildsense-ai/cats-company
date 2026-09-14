import { WebSocketServer, WebSocket } from 'ws';
import { ShimoWorkerError } from './reader.mjs';

const STREAM_PATH = /^\/shimo-login\/([0-9a-f]{64})\/stream$/;
const FRAME_INTERVAL_MS = 650;

export function attachLoginStream(server, loginManager, { frameIntervalMs = FRAME_INTERVAL_MS } = {}) {
  const websocketServer = new WebSocketServer({ noServer: true, maxPayload: 4096 });
  const active = new Map();

  server.on('upgrade', (request, socket, head) => {
    try {
      const url = new URL(request.url, 'http://worker.local');
      const match = url.pathname.match(STREAM_PATH);
      if (!match) return rejectUpgrade(socket, 404, 'Not Found');
      assertSameOrigin(request);
      const token = match[1];
      loginManager.status(token);
      if (active.has(token)) return rejectUpgrade(socket, 409, 'Login stream already open');
      websocketServer.handleUpgrade(request, socket, head, websocket => {
        active.set(token, websocket);
        websocketServer.emit('connection', websocket, request, token);
      });
    } catch (error) {
      const normalized = normalizeStreamError(error);
      rejectUpgrade(socket, normalized.status, normalized.message);
    }
  });

  websocketServer.on('connection', (websocket, _request, token) => {
    let stopped = false;
    let inputChain = Promise.resolve();
    let timer;

    const stop = () => {
      stopped = true;
      clearTimeout(timer);
      if (active.get(token) === websocket) active.delete(token);
    };
    websocket.once('close', stop);
    websocket.once('error', stop);

    websocket.on('message', data => {
      if (stopped) return;
      inputChain = inputChain.then(async () => {
        const message = parseInputMessage(data);
        await loginManager.input(token, message);
      }).catch(error => {
        sendJSON(websocket, { type: 'error', message: normalizeStreamError(error).message });
      });
    });

    const pump = async () => {
      if (stopped || websocket.readyState !== WebSocket.OPEN) return;
      try {
        const state = loginManager.status(token);
        sendJSON(websocket, { type: 'state', ...state });
        if (state.state === 'waiting' || state.state === 'opening') {
          const png = await loginManager.screenshot(token);
          if (!stopped && websocket.readyState === WebSocket.OPEN) websocket.send(png, { binary: true });
          timer = setTimeout(pump, frameIntervalMs);
        } else {
          websocket.close(1000, 'Login finished');
        }
      } catch (error) {
        sendJSON(websocket, { type: 'error', message: normalizeStreamError(error).message });
        websocket.close(1011, 'Login stream ended');
      }
    };
    void pump();
  });

  server.once('close', () => websocketServer.close());
  return websocketServer;
}

function parseInputMessage(data) {
  let message;
  try { message = JSON.parse(data.toString('utf8')); } catch {
    throw new ShimoWorkerError('INVALID_ARGUMENTS', '远程登录操作格式无效。', 400);
  }
  if (!message || message.type !== 'input') throw new ShimoWorkerError('INVALID_ARGUMENTS', '远程登录操作格式无效。', 400);
  const allowed = new Set(['type', 'action', 'x', 'y', 'count', 'value', 'delta_x', 'delta_y']);
  for (const key of Object.keys(message)) if (!allowed.has(key)) throw new ShimoWorkerError('INVALID_ARGUMENTS', '远程登录操作包含未知字段。', 400);
  const { type: _type, ...input } = message;
  return input;
}

function assertSameOrigin(request) {
  const origin = String(request.headers.origin || '');
  if (!origin) return;
  let parsed;
  try { parsed = new URL(origin); } catch { throw new ShimoWorkerError('UNAUTHORIZED', '远程登录来源无效。', 403); }
  if (parsed.host !== request.headers.host || !['http:', 'https:'].includes(parsed.protocol)) {
    throw new ShimoWorkerError('UNAUTHORIZED', '远程登录来源无效。', 403);
  }
}

function sendJSON(websocket, value) {
  if (websocket.readyState === WebSocket.OPEN) websocket.send(JSON.stringify(value));
}

function normalizeStreamError(error) {
  if (error instanceof ShimoWorkerError) return error;
  return new ShimoWorkerError('WORKER_ERROR', '远程登录连接失败。', 502);
}

function rejectUpgrade(socket, status, message) {
  if (!socket.writable) return socket.destroy();
  const safeStatus = Number.isInteger(status) && status >= 400 && status <= 599 ? status : 502;
  const body = Buffer.from(String(message || 'WebSocket upgrade failed'), 'utf8');
  socket.end(`HTTP/1.1 ${safeStatus} Error\r\nConnection: close\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Length: ${body.length}\r\nCache-Control: no-store\r\n\r\n${body}`);
}
