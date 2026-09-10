import assert from 'node:assert/strict';
import http from 'node:http';
import test from 'node:test';
import { createShimoWorkerServer } from '../src/service.mjs';

const internalToken = 'worker-token-that-is-longer-than-thirty-two-characters';
const bindingA = 'a'.repeat(32);
const bindingB = 'b'.repeat(32);

class MemoryStore {
  constructor() { this.records = new Map(); }
  load(binding) {
    if (!/^[0-9a-f]{32,128}$/i.test(binding)) throw new Error('invalid connection binding');
    return this.records.get(binding) || null;
  }
  save(binding, record) { this.records.set(binding, record); }
  delete(binding) { return this.records.delete(binding); }
}

async function startWorker() {
  const store = new MemoryStore();
  const calls = [];
  const reader = {
    async listSheets(state, url) { calls.push(['list', state, url]); return { sheets: [{ name: '项目表', index: 0 }] }; },
    async readSheet(state, url, sheet, range) { calls.push(['sheet', state, url, sheet, range]); return { values: [['金额'], [8500]] }; },
    async readDocument(state, url, maxChars) { calls.push(['document', state, url, maxChars]); return { text: '正文', truncated: false }; },
  };
  const loginManager = {
    async start(binding, completion) { calls.push(['login', binding, completion]); return { login_url: `https://app.catsco.test/shimo-login/${'c'.repeat(64)}/` }; },
    status() { return { state: 'waiting', message: '等待登录' }; },
    async screenshot() { return Buffer.from('png'); },
    async input() { return { state: 'waiting', message: '等待登录' }; },
  };
  const server = createShimoWorkerServer({ internalToken, store, reader, loginManager });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  return { store, calls, url: `http://127.0.0.1:${server.address().port}`, close: () => new Promise(resolve => server.close(resolve)) };
}

async function request(worker, path, body, token = internalToken) {
  return new Promise((resolve, reject) => {
    const payload = Buffer.from(JSON.stringify(body));
    const req = http.request(worker.url + path, { method: 'POST', headers: { authorization: `Bearer ${token}`, 'content-type': 'application/json', 'content-length': payload.length } }, response => {
      const chunks = [];
      response.on('data', chunk => chunks.push(chunk));
      response.on('end', () => resolve({ status: response.statusCode, body: JSON.parse(Buffer.concat(chunks).toString()) }));
    });
    req.on('error', reject);
    req.end(payload);
  });
}

test('internal endpoints require service authentication', async () => {
  const worker = await startWorker();
  try {
    const result = await request(worker, '/v1/sessions/status', { connection_binding: bindingA }, 'wrong');
    assert.equal(result.status, 401);
    assert.equal(result.body.error.code, 'UNAUTHORIZED');
  } finally { await worker.close(); }
});

test('read restores only the selected encrypted user session', async () => {
  const worker = await startWorker();
  try {
    worker.store.save(bindingA, { storageState: { cookies: [{ value: 'actor-a' }] }, accountHint: 'A', verifiedAt: 'now' });
    const readA = await request(worker, '/v1/sheets/read', {
      connection_binding: bindingA, url: 'https://shimo.im/sheets/abc/', sheet_name: '项目表', range: 'A1:C20',
    });
    assert.equal(readA.status, 200);
    assert.deepEqual(readA.body.data.values, [['金额'], [8500]]);
    assert.equal(worker.calls[0][1].cookies[0].value, 'actor-a');

    const readB = await request(worker, '/v1/sheets/read', {
      connection_binding: bindingB, url: 'https://shimo.im/sheets/abc/', sheet_name: '项目表', range: 'A1:C20',
    });
    assert.equal(readB.status, 409);
    assert.equal(readB.body.error.code, 'LOGIN_REQUIRED');
  } finally { await worker.close(); }
});

test('worker rejects identity injection and supports disconnect', async () => {
  const worker = await startWorker();
  try {
    const injected = await request(worker, '/v1/sessions/status', { connection_binding: bindingA, actor_user_id: 'user-b' });
    assert.equal(injected.status, 400);
    assert.equal(injected.body.error.code, 'INVALID_ARGUMENTS');
    worker.store.save(bindingA, { storageState: { cookies: [], origins: [] }, accountHint: 'A', verifiedAt: 'now' });
    const disconnected = await request(worker, '/v1/sessions/disconnect', { connection_binding: bindingA });
    assert.equal(disconnected.status, 200);
    const status = await request(worker, '/v1/sessions/status', { connection_binding: bindingA });
    assert.equal(status.body.data.state, 'disconnected');
  } finally { await worker.close(); }
});

test('login start returns a user-facing short-lived surface', async () => {
  const worker = await startWorker();
  try {
    const completionToken = 'd'.repeat(64);
    const result = await request(worker, '/v1/sessions/login', {
      connection_binding: bindingA,
      completion_callback_url: 'http://server:6061/internal/shimo/login-complete',
      completion_token: completionToken,
    });
    assert.equal(result.status, 200);
    assert.match(result.body.data.login_url, /^https:\/\/app\.catsco\.test\/shimo-login\/[0-9a-f]{64}\/$/);
    assert.deepEqual(worker.calls[0], ['login', bindingA, {
      callbackURL: 'http://server:6061/internal/shimo/login-complete', completionToken,
    }]);
  } finally { await worker.close(); }
});

test('login start rejects a partial or malformed completion callback', async () => {
  const worker = await startWorker();
  try {
    const partial = await request(worker, '/v1/sessions/login', {
      connection_binding: bindingA, completion_token: 'd'.repeat(64),
    });
    assert.equal(partial.status, 400);
    const unsafe = await request(worker, '/v1/sessions/login', {
      connection_binding: bindingA,
      completion_callback_url: 'http://public.example/internal/shimo/login-complete',
      completion_token: 'd'.repeat(64),
    });
    assert.equal(unsafe.status, 400);
  } finally { await worker.close(); }
});
