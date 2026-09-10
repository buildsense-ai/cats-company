import assert from 'node:assert/strict';
import test from 'node:test';
import { notifyLoginCompletion } from '../src/login_manager.mjs';

const token = 'e'.repeat(64);
const authorizationToken = 'worker-token-that-is-longer-than-thirty-two-characters';

test('completion notifier authenticates and sends only the opaque token', async () => {
  const calls = [];
  await notifyLoginCompletion({
    callbackURL: 'http://server:6061/internal/shimo/login-complete',
    completionToken: token,
    authorizationToken,
    fetchImpl: async (url, options) => {
      calls.push({ url, options });
      return { ok: true, status: 202 };
    },
  });
  assert.equal(calls.length, 1);
  assert.equal(calls[0].options.headers.authorization, `Bearer ${authorizationToken}`);
  assert.deepEqual(JSON.parse(calls[0].options.body), { completion_token: token });
});

test('completion notifier retries a temporary offline response', async () => {
  let attempts = 0;
  const waits = [];
  await notifyLoginCompletion({
    callbackURL: 'http://server:6061/internal/shimo/login-complete',
    completionToken: token,
    authorizationToken,
    fetchImpl: async () => {
      attempts += 1;
      return attempts === 1 ? { ok: false, status: 409 } : { ok: true, status: 202 };
    },
    wait: async ms => { waits.push(ms); },
  });
  assert.equal(attempts, 2);
  assert.deepEqual(waits, [1000]);
});
