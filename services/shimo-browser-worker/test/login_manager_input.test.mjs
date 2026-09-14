import assert from 'node:assert/strict';
import test from 'node:test';
import { ShimoLoginManager } from '../src/login_manager.mjs';

const token = 'c'.repeat(64);

function managerWithPage() {
  const calls = [];
  const page = {
    isClosed() { return false; },
    mouse: {
      async click(x, y, options) { calls.push(['click', x, y, options]); },
      async wheel(x, y) { calls.push(['wheel', x, y]); },
    },
    keyboard: {
      async type(value, options) { calls.push(['text', value, options]); },
      async press(value) { calls.push(['key', value]); },
    },
  };
  const manager = new ShimoLoginManager({ store: {}, publicBaseURL: 'https://app.catsco.test/shimo-login' });
  manager.attempts.set(token, {
    token, binding: 'a'.repeat(32), page, state: 'waiting', message: '请登录', expiresAt: Date.now() + 60_000,
  });
  return { calls, manager };
}

test('login manager forwards validated direct interaction to the remote page', async () => {
  const { calls, manager } = managerWithPage();
  await manager.input(token, { action: 'click', x: 100, y: 200, count: 1 });
  await manager.input(token, { action: 'text', value: '中文' });
  await manager.input(token, { action: 'key', value: 'Delete' });
  await manager.input(token, { action: 'wheel', delta_x: 0, delta_y: 240 });
  assert.deepEqual(calls, [
    ['click', 100, 200, { clickCount: 1 }],
    ['text', '中文', { delay: 20 }],
    ['key', 'Delete'],
    ['wheel', 0, 240],
  ]);
});

test('login manager rejects unsafe interaction values', async () => {
  const { manager } = managerWithPage();
  await assert.rejects(() => manager.input(token, { action: 'click', x: 1500, y: 1 }), error => error.code === 'INVALID_ARGUMENTS');
  await assert.rejects(() => manager.input(token, { action: 'wheel', delta_x: 0, delta_y: 9000 }), error => error.code === 'INVALID_ARGUMENTS');
  await assert.rejects(() => manager.input(token, { action: 'key', value: 'F12' }), error => error.code === 'INVALID_ARGUMENTS');
});
