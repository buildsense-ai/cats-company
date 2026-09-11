import crypto from 'node:crypto';
import { chromium } from 'playwright-core';
import { browserExecutable, ShimoWorkerError } from './reader.mjs';
import { assertBinding } from './session_store.mjs';

const DEFAULT_TTL_MS = 10 * 60_000;

export class ShimoLoginManager {
  constructor({ store, publicBaseURL, callbackAuthToken = '', completionNotifier = notifyLoginCompletion, executablePath = browserExecutable(), ttlMs = DEFAULT_TTL_MS, now = () => Date.now() }) {
    this.store = store;
    this.publicBaseURL = String(publicBaseURL || '').replace(/\/$/, '');
    this.executablePath = executablePath;
    this.ttlMs = Math.min(Math.max(ttlMs, 60_000), 15 * 60_000);
    this.now = now;
	this.callbackAuthToken = String(callbackAuthToken || '');
	this.completionNotifier = completionNotifier;
    this.attempts = new Map();
    this.attemptByBinding = new Map();
  }

  async start(binding, completion = null) {
    assertBinding(binding);
    if (!this.publicBaseURL) throw new ShimoWorkerError('WORKER_NOT_CONFIGURED', '登录页面公网地址未配置。', 503);
    const existingToken = this.attemptByBinding.get(binding);
    if (existingToken) {
      const existing = this.attempts.get(existingToken);
      if (existing && existing.expiresAt > this.now() && ['opening', 'waiting'].includes(existing.state)) {
		if (completion) existing.completion = completion;
        return { login_url: `${this.publicBaseURL}/${existing.token}/`, expires_at: new Date(existing.expiresAt).toISOString() };
      }
    }
    const token = crypto.randomBytes(32).toString('hex');
    const browser = await chromium.launch({ executablePath: this.executablePath, headless: true });
    const context = await browser.newContext({ viewport: { width: 1280, height: 900 }, locale: 'zh-CN' });
    const page = await context.newPage();
    const attempt = {
      token, binding, browser, context, page, state: 'opening',
      expiresAt: this.now() + this.ttlMs, message: '正在打开石墨登录页', monitor: null,
	  completion,
    };
    this.attempts.set(token, attempt);
    this.attemptByBinding.set(binding, token);
    try {
      context.on('page', nextPage => {
        attempt.page = nextPage;
        void nextPage.waitForLoadState('domcontentloaded').catch(() => {});
      });
      await page.goto('https://shimo.im/login', { waitUntil: 'domcontentloaded', timeout: 45_000 });
      await page.waitForFunction(() => document.body?.innerText?.trim().length > 20, null, { timeout: 15_000 });
      await page.waitForTimeout(300);
      attempt.state = 'waiting';
      attempt.message = '请在下方石墨页面完成登录';
      attempt.monitor = this.monitor(attempt);
    } catch (error) {
      await this.closeAttempt(attempt, 'failed', '无法打开石墨登录页');
      throw new ShimoWorkerError('SHIMO_LOGIN_UNAVAILABLE', '无法打开石墨登录页。', 502, { cause: String(error?.message || error) });
    }
    return {
      login_url: `${this.publicBaseURL}/${token}/`,
      expires_at: new Date(attempt.expiresAt).toISOString(),
    };
  }

  requireAttempt(token) {
    const attempt = this.attempts.get(String(token || ''));
    if (!attempt || attempt.expiresAt <= this.now()) {
      if (attempt) void this.closeAttempt(attempt, 'expired', '登录已超时');
      throw new ShimoWorkerError('LOGIN_LINK_EXPIRED', '登录链接已失效，请回到聊天重新发起。', 410);
    }
    return attempt;
  }

  status(token) {
    const attempt = this.requireAttempt(token);
    return { state: attempt.state, message: attempt.message, expires_at: new Date(attempt.expiresAt).toISOString() };
  }

  statusForBinding(binding) {
    assertBinding(binding);
    const token = this.attemptByBinding.get(binding);
    if (!token) return null;
    const attempt = this.attempts.get(token);
    if (!attempt || attempt.expiresAt <= this.now()) return null;
    return { state: attempt.state, expires_at: new Date(attempt.expiresAt).toISOString() };
  }

  async screenshot(token) {
    const attempt = this.requireAttempt(token);
    if (!attempt.page || attempt.page.isClosed()) throw new ShimoWorkerError('LOGIN_FINISHED', attempt.message, 409);
    return attempt.page.screenshot({ type: 'png' });
  }

  async input(token, input) {
    const attempt = this.requireAttempt(token);
    if (attempt.state !== 'waiting' || !attempt.page || attempt.page.isClosed()) {
      throw new ShimoWorkerError('LOGIN_NOT_INTERACTIVE', attempt.message, 409);
    }
    if (input.action === 'click') {
      const x = Number(input.x);
      const y = Number(input.y);
      if (!Number.isFinite(x) || !Number.isFinite(y) || x < 0 || y < 0 || x > 1280 || y > 900) {
        throw new ShimoWorkerError('INVALID_ARGUMENTS', '点击位置无效。', 400);
      }
      await attempt.page.mouse.click(x, y);
    } else if (input.action === 'text') {
      const value = String(input.value || '');
      if (!value || [...value].length > 200) throw new ShimoWorkerError('INVALID_ARGUMENTS', '输入文字为空或过长。', 400);
      await attempt.page.keyboard.type(value, { delay: 20 });
    } else if (input.action === 'key') {
      const allowed = new Set(['Enter', 'Tab', 'Escape', 'Backspace', 'ArrowLeft', 'ArrowRight', 'ArrowUp', 'ArrowDown']);
      if (!allowed.has(input.value)) throw new ShimoWorkerError('INVALID_ARGUMENTS', '按键不受支持。', 400);
      await attempt.page.keyboard.press(input.value);
    } else {
      throw new ShimoWorkerError('INVALID_ARGUMENTS', '交互类型不受支持。', 400);
    }
    return this.status(token);
  }

  async monitor(attempt) {
    while (attempt.state === 'waiting' && attempt.expiresAt > this.now()) {
      try {
        const response = await attempt.context.request.get('https://shimo.im/lizard-api/users/me', { timeout: 10_000 });
        if (response.ok()) {
          const profile = await response.json().catch(() => ({}));
          const storageState = await attempt.context.storageState();
          const accountHint = String(profile?.name || profile?.nickname || profile?.email || '已连接石墨账号').slice(0, 160);
          this.store.save(attempt.binding, { storageState, accountHint, verifiedAt: new Date(this.now()).toISOString() });
          await this.closeAttempt(attempt, 'connected', '石墨连接成功，可以返回 CatsCo 聊天');
		  if (attempt.completion && this.callbackAuthToken) {
			void this.completionNotifier({ ...attempt.completion, authorizationToken: this.callbackAuthToken }).catch(() => {});
		  }
          return;
        }
      } catch {
        // A transient Shimo request failure does not end the user's login window.
      }
      await new Promise(resolve => setTimeout(resolve, 1500));
    }
    if (attempt.state === 'waiting') await this.closeAttempt(attempt, 'expired', '登录已超时，请回到聊天重试');
  }

  async closeAttempt(attempt, state, message) {
    attempt.state = state;
    attempt.message = message;
    const browser = attempt.browser;
    attempt.browser = null;
    attempt.context = null;
    attempt.page = null;
    if (browser) await browser.close().catch(() => {});
    if (this.attemptByBinding.get(attempt.binding) === attempt.token) this.attemptByBinding.delete(attempt.binding);
    const delay = Math.max(60_000, attempt.expiresAt - this.now());
    const timer = setTimeout(() => this.attempts.delete(attempt.token), delay);
    timer.unref?.();
  }

  async shutdown() {
    await Promise.all([...this.attempts.values()].map(attempt => this.closeAttempt(attempt, 'closed', '服务已停止')));
    this.attempts.clear();
  }
}

export async function notifyLoginCompletion({ callbackURL, completionToken, authorizationToken, fetchImpl = globalThis.fetch, wait = delay }) {
  if (!callbackURL || !/^[0-9a-f]{64}$/.test(String(completionToken || '')) || String(authorizationToken || '').length < 32 || typeof fetchImpl !== 'function') {
	throw new Error('invalid Shimo completion callback configuration');
  }
  const retryDelays = [0, 1000, 3000, 7000, 15000];
  let lastError;
  for (const retryDelay of retryDelays) {
	if (retryDelay) await wait(retryDelay);
	try {
	  const response = await fetchImpl(callbackURL, {
		method: 'POST',
		headers: { authorization: `Bearer ${authorizationToken}`, 'content-type': 'application/json' },
		body: JSON.stringify({ completion_token: completionToken }),
		signal: AbortSignal.timeout(5000),
	  });
	  if (response.ok) return;
	  if (response.status === 400 || response.status === 401 || response.status === 404 || response.status === 410) {
		const rejected = new Error(`Shimo completion callback rejected with ${response.status}`);
		rejected.permanent = true;
		throw rejected;
	  }
	  lastError = new Error(`Shimo completion callback returned ${response.status}`);
	} catch (error) {
	  if (error?.permanent) throw error;
	  lastError = error;
	}
  }
  throw lastError || new Error('Shimo completion callback failed');
}

function delay(ms) {
  return new Promise(resolve => setTimeout(resolve, ms));
}
