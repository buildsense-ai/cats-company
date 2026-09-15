import assert from 'node:assert/strict';
import test from 'node:test';
import { ShimoLoginManager } from '../src/login_manager.mjs';
import { loginHTML } from '../src/login_page.mjs';
import { SHIMO_LOGIN_URL, isLegalDocumentPage, shouldAdoptPopup } from '../src/login_pages.mjs';

const token = 'd'.repeat(64);
const binding = 'b'.repeat(32);
const html = loginHTML(token);

function fakePage(url, { closed = false } = {}) {
  let current = url;
  return {
    gotos: [],
    isClosed: () => closed,
    url: () => current,
    async waitForLoadState() {},
    async goto(target) { this.gotos.push(target); current = target; },
  };
}

function managerWith({ page, primary, state = 'waiting' }) {
  const manager = new ShimoLoginManager({ store: {}, publicBaseURL: 'https://app.catsco.test/shimo-login' });
  manager.attempts.set(token, {
    token, binding, state, message: '请在下方石墨页面完成登录',
    expiresAt: Date.now() + 60_000, page, primary,
  });
  return manager;
}

// Shimo opens 服务条款 / 隐私政策 / 用户行为规范 in new tabs from the consent line
// right under the login button.  A capture of that tab is a wall of legalese with
// no way back, which reads to the visitor as a frozen, unclickable screenshot.
test('legal documents are recognised as non-login pages', () => {
  assert.equal(isLegalDocumentPage('https://shimo.im/agreements/service'), true);
  assert.equal(isLegalDocumentPage('https://shimo.im/agreements/privacy'), true);
  assert.equal(isLegalDocumentPage('https://shimo.im/loginByPassword'), false);
  assert.equal(isLegalDocumentPage('not a url'), false);
  assert.equal(isLegalDocumentPage('https://example.com/terms'), false);
});

test('only login-flow popups take over the surface', () => {
  assert.equal(shouldAdoptPopup('about:blank'), false);
  assert.equal(shouldAdoptPopup('https://shimo.im/agreements/service'), false);
  assert.equal(shouldAdoptPopup('https://shimo.im/agreements/privacy'), false);
  assert.equal(shouldAdoptPopup('https://example.com/anything'), false);
  assert.equal(shouldAdoptPopup('https://shimo.im/loginByPassword'), true);
  assert.equal(shouldAdoptPopup('https://open.weixin.qq.com/connect/qrconnect'), true);
});

test('adopting a new tab ignores Shimo legal documents', async () => {
  const terms = fakePage('https://shimo.im/agreements/service');
  const login = fakePage('https://shimo.im/loginByPassword');
  const manager = managerWith({ page: login, primary: login });
  await manager.adoptPopup(manager.attempts.get(token), terms);
  assert.equal(manager.attempts.get(token).page, login, 'the login form stays on the surface');

  const qr = fakePage('https://open.weixin.qq.com/connect/qrconnect');
  await manager.adoptPopup(manager.attempts.get(token), qr);
  assert.equal(manager.attempts.get(token).page, qr, 'a real login popup may take over');
});

test('reset puts the login form back on the surface', async () => {
  const terms = fakePage('https://shimo.im/agreements/service');
  const login = fakePage('https://shimo.im/loginByPassword');
  const manager = managerWith({ page: terms, primary: login });
  const status = await manager.reset(token);
  assert.equal(status.state, 'waiting');
  assert.equal(manager.attempts.get(token).page, login);
  assert.deepEqual(login.gotos, [], 'the login page is already open');
});

test('reset reloads the login page when the surface drifted away', async () => {
  const drifted = fakePage('https://shimo.im/agreements/privacy');
  const manager = managerWith({ page: drifted, primary: drifted });
  await manager.reset(token);
  assert.deepEqual(drifted.gotos, [SHIMO_LOGIN_URL]);
});

test('reset reopens the login flow when the captured tab is gone', async () => {
  const gone = fakePage('https://shimo.im/agreements/service', { closed: true });
  const manager = managerWith({ page: gone, primary: gone });
  await assert.rejects(() => manager.reset(token), error => error.code === 'LOGIN_FINISHED' && error.status === 409);
});

test('login surface offers a way back to the login page', () => {
  assert.ok(html.includes('<button id="home" type="button">回到登录页</button>'), 'the escape hatch is rendered');
  assert.match(html, /api\('\/reset',\{method:'POST'\}\)/);
  assert.match(html, /回到登录页/);
});
