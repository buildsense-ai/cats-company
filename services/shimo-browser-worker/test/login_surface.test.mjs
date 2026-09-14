import assert from 'node:assert/strict';
import test from 'node:test';
import { ShimoLoginManager, loginContextOptions } from '../src/login_manager.mjs';
import { loginHTML } from '../src/login_page.mjs';
import { LOGIN_DEVICE_SCALE_FACTOR, LOGIN_VIEWPORT } from '../src/login_viewport.mjs';

const token = 'c'.repeat(64);
const html = loginHTML(token);

// A visitor sees the capture stretched to their own window width, so a one-to-one
// capture is upscaled and the login page looks soft.  Rendering above one device
// pixel per CSS pixel is what keeps the text sharp.
test('login capture renders above one device pixel per CSS pixel', () => {
  assert.ok(LOGIN_DEVICE_SCALE_FACTOR >= 2, 'capture must be sharper than the visitor window');
  const options = loginContextOptions();
  assert.deepEqual(options.viewport, { width: LOGIN_VIEWPORT.width, height: LOGIN_VIEWPORT.height });
  assert.equal(options.deviceScaleFactor, LOGIN_DEVICE_SCALE_FACTOR);
  assert.equal(options.locale, 'zh-CN');
});

// The frame is wider than the logical viewport, so the canvas must adopt the
// frame size instead of downscaling the extra detail away.
test('login surface sizes its canvas from the captured frame', () => {
  assert.ok(html.includes(`<canvas id="screen" width="${LOGIN_VIEWPORT.width}" height="${LOGIN_VIEWPORT.height}"`), 'canvas starts at the logical viewport');
  assert.match(html, /if\(canvas\.width!==bitmap\.width\|\|canvas\.height!==bitmap\.height\)\{canvas\.width=bitmap\.width;canvas\.height=bitmap\.height;\}/);
  assert.match(html, /ctx\.drawImage\(bitmap,0,0,bitmap\.width,bitmap\.height\)/);
});

// Input must stay in logical viewport pixels: the same space the worker renders,
// validates, and clicks in.
test('login surface reports pointer positions in logical viewport pixels', () => {
  assert.ok(html.includes(`const viewport=${JSON.stringify(LOGIN_VIEWPORT)};`), 'page carries the logical viewport');
  assert.match(html, /\(event\.clientX-rect\.left\)\*viewport\.width\/rect\.width/);
  assert.match(html, /\(event\.clientY-rect\.top\)\*viewport\.height\/rect\.height/);
  assert.doesNotMatch(html, /\*canvas\.width\/rect\.width/);
});

test('login manager accepts clicks across the whole logical viewport', async () => {
  const calls = [];
  const manager = new ShimoLoginManager({ store: {}, publicBaseURL: 'https://app.catsco.test/shimo-login' });
  manager.attempts.set(token, {
    token, binding: 'a'.repeat(32), state: 'waiting', message: '请登录', expiresAt: Date.now() + 60_000,
    page: {
      isClosed() { return false; },
      mouse: { async click(x, y, options) { calls.push([x, y, options]); } },
    },
  });
  await manager.input(token, { action: 'click', x: LOGIN_VIEWPORT.width, y: LOGIN_VIEWPORT.height, count: 1 });
  assert.deepEqual(calls, [[LOGIN_VIEWPORT.width, LOGIN_VIEWPORT.height, { clickCount: 1 }]]);
  await assert.rejects(
    () => manager.input(token, { action: 'click', x: LOGIN_VIEWPORT.width + 1, y: 10 }),
    error => error.code === 'INVALID_ARGUMENTS',
  );
});
