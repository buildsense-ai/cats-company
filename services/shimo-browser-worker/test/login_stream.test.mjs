import assert from 'node:assert/strict';
import http from 'node:http';
import test from 'node:test';
import { WebSocket } from 'ws';
import { createShimoWorkerServer } from '../src/service.mjs';

const internalToken = 'worker-token-that-is-longer-than-thirty-two-characters';
const loginToken = 'c'.repeat(64);

async function startWorker() {
  const inputs = [];
  const loginManager = {
    status(token) {
      assert.equal(token, loginToken);
      return { state: 'waiting', message: '请登录', expires_at: '2026-09-14T07:00:00.000Z' };
    },
    async screenshot(token) {
      assert.equal(token, loginToken);
      return Buffer.from('png-frame');
    },
    async input(token, input) {
      assert.equal(token, loginToken);
      inputs.push(input);
      return this.status(token);
    },
  };
  const store = { load() { return null; }, delete() {}, save() {} };
  const server = createShimoWorkerServer({ internalToken, store, reader: {}, loginManager });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  return { inputs, server, url: `http://127.0.0.1:${server.address().port}` };
}

function get(url) {
  return new Promise((resolve, reject) => {
    http.get(url, response => {
      const chunks = [];
      response.on('data', chunk => chunks.push(chunk));
      response.on('end', () => resolve({ status: response.statusCode, headers: response.headers, body: Buffer.concat(chunks).toString('utf8') }));
    }).on('error', reject);
  });
}

function waitFor(predicate, timeoutMs = 1500) {
  const started = Date.now();
  return new Promise((resolve, reject) => {
    const poll = () => {
      if (predicate()) return resolve();
      if (Date.now() - started > timeoutMs) return reject(new Error('condition timed out'));
      setTimeout(poll, 10);
    };
    poll();
  });
}

test('login page exposes a directly interactive canvas without the old relay input', async () => {
  const worker = await startWorker();
  try {
    const result = await get(`${worker.url}/shimo-login/${loginToken}/`);
    assert.equal(result.status, 200);
    assert.match(result.headers['content-security-policy'], /connect-src 'self'/);
    assert.match(result.body, /<canvas id="screen"/);
    assert.match(result.body, /new WebSocket/);
    assert.match(result.body, /compositionend/);
    assert.match(result.body, /type:_type,...input/);
    assert.doesNotMatch(result.body, /id="send"/);
    assert.doesNotMatch(result.body, /需要输入手机号或验证码时/);
  } finally { await new Promise(resolve => worker.server.close(resolve)); }
});

test('login stream sends live frames and serializes pointer, keyboard, text, and wheel input', async () => {
  const worker = await startWorker();
  const socket = new WebSocket(worker.url.replace('http:', 'ws:') + `/shimo-login/${loginToken}/stream`, { origin: worker.url });
  const messages = [];
  socket.on('message', (data, isBinary) => messages.push(isBinary ? data.toString() : JSON.parse(data.toString())));
  try {
    await new Promise((resolve, reject) => { socket.once('open', resolve); socket.once('error', reject); });
    await waitFor(() => messages.some(item => item?.type === 'state') && messages.includes('png-frame'));
    socket.send(JSON.stringify({ type: 'input', action: 'click', x: 320, y: 240, count: 1 }));
    socket.send(JSON.stringify({ type: 'input', action: 'text', value: '测试用户' }));
    socket.send(JSON.stringify({ type: 'input', action: 'key', value: 'Enter' }));
    socket.send(JSON.stringify({ type: 'input', action: 'wheel', delta_x: 0, delta_y: 180 }));
    await waitFor(() => worker.inputs.length === 4);
    assert.deepEqual(worker.inputs, [
      { action: 'click', x: 320, y: 240, count: 1 },
      { action: 'text', value: '测试用户' },
      { action: 'key', value: 'Enter' },
      { action: 'wheel', delta_x: 0, delta_y: 180 },
    ]);
  } finally {
    socket.close();
    await new Promise(resolve => socket.once('close', resolve));
    await new Promise(resolve => worker.server.close(resolve));
  }
});

test('login stream rejects a cross-origin browser connection', async () => {
  const worker = await startWorker();
  const socket = new WebSocket(worker.url.replace('http:', 'ws:') + `/shimo-login/${loginToken}/stream`, { origin: 'https://attacker.example' });
  try {
    const error = await new Promise(resolve => socket.once('error', resolve));
    assert.match(error.message, /Unexpected server response: 403/);
  } finally {
    socket.close();
    await new Promise(resolve => worker.server.close(resolve));
  }
});
