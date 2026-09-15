import crypto from 'node:crypto';
import { chromium } from 'playwright-core';
import { browserExecutable, ShimoWorkerError } from './reader.mjs';
import { assertBinding } from './session_store.mjs';
import { LOGIN_DEVICE_SCALE_FACTOR, LOGIN_VIEWPORT } from './login_viewport.mjs';
import { SHIMO_LOGIN_URL, shouldAdoptPopup } from './login_pages.mjs';

const DEFAULT_TTL_MS = 10 * 60_000;
const DEFAULT_PROBE_INTERVAL_MS = 1500;

// The capture runs above one device pixel per CSS pixel so the streamed page
// stays crisp in a wide browser window, while pointer input keeps using the
// logical viewport the login surface maps clicks back to.
export function loginContextOptions() {
  return {
    viewport: { width: LOGIN_VIEWPORT.width, height: LOGIN_VIEWPORT.height },
    deviceScaleFactor: LOGIN_DEVICE_SCALE_FACTOR,
    locale: 'zh-CN',
  };
}

export class ShimoLoginManager {
  constructor({ store, publicBaseURL, callbackAuthToken = '', completionNotifier = notifyLoginCompletion, executablePath = browserExecutable(), ttlMs = DEFAULT_TTL_MS, now = () => Date.now(), probeIntervalMs = DEFAULT_PROBE_INTERVAL_MS, logger = console }) {
    this.store = store;
    this.publicBaseURL = String(publicBaseURL || '').replace(/\/$/, '');
    this.executablePath = executablePath;
    this.ttlMs = Math.min(Math.max(ttlMs, 60_000), 15 * 60_000);
    this.now = now;
    this.probeIntervalMs = Math.min(Math.max(Number(probeIntervalMs) || DEFAULT_PROBE_INTERVAL_MS, 200), 10_000);
    this.logger = logger;
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
    const context = await browser.newContext(loginContextOptions());
    const page = await context.newPage();
    const attempt = {
      token, binding, browser, context, page, state: 'opening',
      expiresAt: this.now() + this.ttlMs, message: '正在打开石墨登录页', monitor: null,
	  completion,
    };
    this.attempts.set(token, attempt);
    this.attemptByBinding.set(binding, token);
    try {
      // Shimo opens 服务条款 / 隐私政策 / 用户行为规范 in new tabs.  Streaming
      // those tabs would replace the login form with a document page the visitor
      // cannot leave, so only login-flow popups take over the surface.
      attempt.primary = page;
      context.on('page', nextPage => { void this.adoptPopup(attempt, nextPage); });
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
    return {
      state: attempt.state,
      message: statusMessage(attempt),
      expires_at: new Date(attempt.expiresAt).toISOString(),
      persist_error: attempt.persistError || '',
    };
  }

  statusForBinding(binding) {
    assertBinding(binding);
    const token = this.attemptByBinding.get(binding);
    if (!token) return null;
    const attempt = this.attempts.get(token);
    if (!attempt || attempt.expiresAt <= this.now()) return null;
    return { state: attempt.state, expires_at: new Date(attempt.expiresAt).toISOString() };
  }

  // Puts the surface back on the Shimo login page.  The visitor needs this after
  // any navigation that is not the login form itself, including the legal tabs
  // Shimo opens from its consent line.
  async reset(token) {
    const attempt = this.requireAttempt(token);
    if (attempt.state !== 'waiting' || !attempt.page || attempt.page.isClosed()) {
      throw new ShimoWorkerError('LOGIN_FINISHED', attempt.message, 409);
    }
    const primary = attempt.primary && !attempt.primary.isClosed() ? attempt.primary : null;
    if (primary) attempt.page = primary;
    if (!currentURL(attempt.page).startsWith(SHIMO_LOGIN_URL)) {
      await attempt.page.goto(SHIMO_LOGIN_URL, { waitUntil: 'domcontentloaded', timeout: 45_000 });
    }
    attempt.message = '请在下方石墨页面完成登录';
    return this.status(token);
  }

  async adoptPopup(attempt, nextPage) {
    await nextPage.waitForLoadState('domcontentloaded').catch(() => {});
    if (attempt.state !== 'waiting' || attempt.page === nextPage || nextPage.isClosed()) return;
    if (!shouldAdoptPopup(currentURL(nextPage))) return;
    attempt.page = nextPage;
  }

  async screenshot(token) {
    const attempt = this.requireAttempt(token);
    if (!attempt.page || attempt.page.isClosed()) throw new ShimoWorkerError('LOGIN_FINISHED', attempt.message, 409);
    // Playwright hides the text caret in screenshots by default, so a visitor
    // typing into the streamed page sees no caret at all and cannot tell where
    // the text is going.  Keep the real caret so the frames show it blinking.
    return attempt.page.screenshot({ type: 'png', caret: 'initial' });
  }

  async input(token, input) {
    const attempt = this.requireAttempt(token);
    if (attempt.state !== 'waiting' || !attempt.page || attempt.page.isClosed()) {
      throw new ShimoWorkerError('LOGIN_NOT_INTERACTIVE', attempt.message, 409);
    }
    if (input.action === 'click') {
      const x = Number(input.x);
      const y = Number(input.y);
      const count = Number(input.count || 1);
      if (!Number.isFinite(x) || !Number.isFinite(y) || x < 0 || y < 0 || x > LOGIN_VIEWPORT.width || y > LOGIN_VIEWPORT.height) {
        throw new ShimoWorkerError('INVALID_ARGUMENTS', '点击位置无效。', 400);
      }
      if (![1, 2].includes(count)) throw new ShimoWorkerError('INVALID_ARGUMENTS', '点击次数无效。', 400);
      await attempt.page.mouse.click(x, y, { clickCount: count });
    } else if (input.action === 'text') {
      const value = String(input.value || '');
      if (!value || [...value].length > 200) throw new ShimoWorkerError('INVALID_ARGUMENTS', '输入文字为空或过长。', 400);
      await attempt.page.keyboard.type(value, { delay: 20 });
    } else if (input.action === 'key') {
      const allowed = new Set(['Enter', 'Tab', 'Escape', 'Backspace', 'Delete', 'ArrowLeft', 'ArrowRight', 'ArrowUp', 'ArrowDown', 'Home', 'End']);
      if (!allowed.has(input.value)) throw new ShimoWorkerError('INVALID_ARGUMENTS', '按键不受支持。', 400);
      await attempt.page.keyboard.press(input.value);
    } else if (input.action === 'wheel') {
      const deltaX = Number(input.delta_x || 0);
      const deltaY = Number(input.delta_y || 0);
      if (!Number.isFinite(deltaX) || !Number.isFinite(deltaY) || Math.abs(deltaX) > 5000 || Math.abs(deltaY) > 5000 || (!deltaX && !deltaY)) {
        throw new ShimoWorkerError('INVALID_ARGUMENTS', '滚动距离无效。', 400);
      }
      await attempt.page.mouse.wheel(deltaX, deltaY);
    } else {
      throw new ShimoWorkerError('INVALID_ARGUMENTS', '交互类型不受支持。', 400);
    }
    return this.status(token);
  }

  // Shimo keeps the account session in first-party cookies that only the login
  // page itself uses, so the probe runs inside the page: the same fetch the
  // product makes, with the same credentials.  The browser-context request is
  // kept as a fallback for the window where Chromium has already left the page.
  async probeAccount(attempt) {
    const failures = [];
    let pages = [];
    try { pages = attempt.context?.pages?.() || []; } catch { pages = []; }
    for (const page of pages) {
      if (!page || page.isClosed?.()) continue;
      const result = await evaluateAccountProbe(page);
      if (result.ok) return { ok: true, hint: result.hint, status: result.status, source: 'page' };
      failures.push(`page(${currentURL(page)})=${result.status || 'error'}`);
    }
    try {
      const response = await attempt.context.request.get('https://shimo.im/lizard-api/users/me', { timeout: 10_000 });
      if (response.ok()) {
        const profile = await response.json().catch(() => ({}));
        return { ok: true, hint: accountHint(profile), status: response.status(), source: 'context' };
      }
      failures.push(`context=${response.status()}`);
    } catch (error) {
      failures.push(`context=${describeError(error)}`);
    }
    return { ok: false, hint: '', failures };
  }

  async monitor(attempt) {
    const log = this.logger || console;
    let reportedFailure = false;
    while (attempt.state === 'waiting' && attempt.expiresAt > this.now()) {
      let account;
      try {
        account = await this.probeAccount(attempt);
      } catch (error) {
        account = { ok: false, failures: [describeError(error)] };
      }
      if (account.ok) {
        try {
          const storageState = await attempt.context.storageState();
          this.store.save(attempt.binding, { storageState, accountHint: account.hint, verifiedAt: new Date(this.now()).toISOString() });
          attempt.persistError = '';
          log.info?.(`[shimo-login] 已检测到登录并保存会话 binding=${attempt.binding} source=${account.source} account=${account.hint}`);
          await this.closeAttempt(attempt, 'connected', '石墨连接成功，可以返回 CatsCo 聊天');
          await this.notifyCompletion(attempt, log);
          return;
        } catch (error) {
          // Losing the login because the state directory is not writable used
          // to look identical to "the visitor has not logged in yet", which
          // cost hours of debugging: keep retrying, but never stay silent.
          attempt.persistError = describeError(error);
          log.error?.(`[shimo-login] 已登录但无法保存会话 binding=${attempt.binding}: ${attempt.persistError}`);
        }
      } else if (!reportedFailure) {
        reportedFailure = true;
        log.info?.(`[shimo-login] 等待登录 binding=${attempt.binding} probe=${account.failures?.join(',') || 'unknown'}`);
      }
      await new Promise(resolve => setTimeout(resolve, this.probeIntervalMs));
    }
    if (attempt.state === 'waiting') await this.closeAttempt(attempt, 'expired', '登录已超时，请回到聊天重试');
  }

  async notifyCompletion(attempt, log) {
    if (!attempt.completion || !this.callbackAuthToken) {
      log.info?.(`[shimo-login] 本次登录没有完成回调，跳过通知 binding=${attempt.binding}`);
      return;
    }
    try {
      await this.completionNotifier({ ...attempt.completion, authorizationToken: this.callbackAuthToken });
      log.info?.(`[shimo-login] 已通知调用方登录完成 binding=${attempt.binding}`);
    } catch (error) {
      log.error?.(`[shimo-login] 通知调用方失败 binding=${attempt.binding}: ${describeError(error)}`);
    }
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

function statusMessage(attempt) {
  if (attempt.state === 'waiting' && attempt.persistError) {
    return '已登录，但服务端暂时无法保存登录态，正在重试…';
  }
  return attempt.message;
}

function accountHint(profile) {
  return String(profile?.name || profile?.nickname || profile?.email || '已连接石墨账号').slice(0, 160);
}

export async function evaluateAccountProbe(page) {
  try {
    const result = await page.evaluate(async () => {
      try {
        const response = await fetch('https://shimo.im/lizard-api/users/me', {
          credentials: 'include',
          headers: { accept: 'application/json' },
          signal: AbortSignal.timeout(4000),
        });
        if (!response.ok) return { ok: false, status: response.status, hint: '' };
        const body = await response.json().catch(() => ({}));
        return { ok: true, status: response.status, hint: String(body?.name || body?.nickname || body?.email || '') };
      } catch (error) {
        return { ok: false, status: 0, hint: '', error: String((error && error.message) || error) };
      }
    });
    if (!result || typeof result !== 'object') return { ok: false, status: 0, hint: '' };
    return { ok: Boolean(result.ok), status: Number(result.status) || 0, hint: String(result.hint || '').slice(0, 160) };
  } catch (error) {
    return { ok: false, status: 0, hint: '', error: String((error && error.message) || error) };
  }
}

function describeError(error) {
  return String((error && error.message) || error || 'unknown error').slice(0, 300);
}

function currentURL(page) {
  try {
    return String(page.url() || '');
  } catch {
    return '';
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
