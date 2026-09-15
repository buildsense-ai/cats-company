import assert from 'node:assert/strict';
import test from 'node:test';
import { ShimoLoginManager } from '../src/login_manager.mjs';

const token = 'f'.repeat(64);
const binding = 'b'.repeat(32);

function fakeAttempt({ pageResult, saved = [], browser = { close: async () => {} } }) {
  const page = {
    isClosed: () => false,
    url: () => 'https://shimo.im/login',
    evaluate: async () => pageResult,
  };
  const context = {
    pages: () => [page],
    storageState: async () => ({ cookies: [], origins: [] }),
    request: { get: async () => ({ ok: () => false, status: () => 401 }) },
  };
  return {
    token,
    binding,
    context,
    page,
    browser,
    state: 'waiting',
    message: '请在下方石墨页面完成登录',
    expiresAt: Date.now() + 60_000,
    completion: { callbackURL: 'http://server:6061/internal/shimo/login-complete', completionToken: 'a'.repeat(64) },
  };
}

function quietLogger(lines = []) {
  return {
    info: message => lines.push(`info:${message}`),
    error: message => lines.push(`error:${message}`),
  };
}

test('a session completed inside the page is persisted and reported', async () => {
  const saved = [];
  const notified = [];
  const lines = [];
  const store = { save: (target, record) => saved.push([target, record]) };
  const manager = new ShimoLoginManager({
    store,
    publicBaseURL: 'https://app.catsco.test/shimo-login',
    callbackAuthToken: 'worker-token-longer-than-thirty-two-characters',
    completionNotifier: async payload => notified.push(payload),
    probeIntervalMs: 20,
    logger: quietLogger(lines),
  });
  const attempt = fakeAttempt({ pageResult: { ok: true, status: 200, hint: '阿罗' } });
  manager.attempts.set(token, attempt);

  await manager.monitor(attempt);

  assert.equal(saved.length, 1);
  assert.equal(saved[0][0], binding);
  assert.equal(saved[0][1].accountHint, '阿罗');
  assert.equal(attempt.state, 'connected');
  assert.equal(notified.length, 1);
  assert.equal(notified[0].completionToken, 'a'.repeat(64));
  assert.ok(lines.some(line => line.includes('已检测到登录并保存会话')));
  assert.ok(lines.some(line => line.includes('已通知调用方登录完成')));
});

test('a rejected write keeps the login window open and is never silent', async () => {
  const lines = [];
  let failures = 1;
  const saved = [];
  const store = {
    save: (target, record) => {
      if (failures > 0) {
        failures -= 1;
        const error = new Error('EACCES: permission denied, open /var/lib/catsco-shimo/x.session');
        error.code = 'EACCES';
        throw error;
      }
      saved.push([target, record]);
    },
  };
  const manager = new ShimoLoginManager({
    store,
    publicBaseURL: 'https://app.catsco.test/shimo-login',
    probeIntervalMs: 20,
    logger: quietLogger(lines),
  });
  const attempt = fakeAttempt({ pageResult: { ok: true, status: 200, hint: '阿罗' } });
  manager.attempts.set(token, attempt);
  // The first pass fails to persist; the retry loop must not lose the visitor.
  const monitor = manager.monitor(attempt);
  await new Promise(resolve => setTimeout(resolve, 60));
  assert.ok(lines.some(line => line.startsWith('error:') && line.includes('EACCES')));
  await monitor;

  assert.equal(saved.length, 1);
  assert.equal(attempt.state, 'connected');
});

test('an unfinished login keeps probing without touching the store', async () => {
  const lines = [];
  const saved = [];
  const store = { save: (target, record) => saved.push([target, record]) };
  const manager = new ShimoLoginManager({
    store,
    publicBaseURL: 'https://app.catsco.test/shimo-login',
    probeIntervalMs: 20,
    logger: quietLogger(lines),
  });
  const attempt = fakeAttempt({ pageResult: { ok: false, status: 404, hint: '' } });
  attempt.expiresAt = Date.now() + 120;
  manager.attempts.set(token, attempt);

  await manager.monitor(attempt);

  assert.equal(saved.length, 0);
  assert.equal(attempt.state, 'expired');
  assert.ok(lines.some(line => line.includes('等待登录')));
});

test('an unwritable state directory is reported through the login status', () => {
  const manager = new ShimoLoginManager({ store: {}, publicBaseURL: 'https://app.catsco.test/shimo-login' });
  const attempt = fakeAttempt({ pageResult: { ok: false, status: 404, hint: '' } });
  attempt.persistError = 'EACCES: permission denied';
  manager.attempts.set(token, attempt);

  const status = manager.status(token);
  assert.equal(status.persist_error, 'EACCES: permission denied');
  assert.match(status.message, /无法保存登录态/);
});
