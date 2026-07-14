window.AuthGate = (() => {
  let root = null;
  let initialized = false;

  const logo = `
    <svg class="auth-logo" viewBox="0 0 240 120" fill="currentColor" aria-hidden="true">
      <path d="M72 0h96l15 25H57z"/>
      <path d="M24 0h28L28 120H0z"/>
      <path d="M188 0h28l24 120h-28z"/>
    </svg>`;

  function template() {
    return `
      <section class="auth-view" id="authView" aria-live="polite">
        <div class="auth-shell">
          <header class="auth-brand">
            ${logo}
            <span>CatsCo</span>
          </header>

          <div class="auth-panel auth-status-view" data-auth-view="checking">
            <span class="auth-spinner" aria-hidden="true"></span>
            <h1>正在检查登录状态</h1>
            <p>正在连接 CatsCo 服务。</p>
          </div>

          <form class="auth-panel" data-auth-view="login" id="authLoginForm" hidden>
            <div class="auth-heading">
              <h1>登录</h1>
              <p>使用公司账号继续进入 CatsCo。</p>
            </div>
            <div class="auth-error" id="authLoginError" role="alert" hidden></div>
            <label class="auth-field">
              <span>账号或邮箱</span>
              <input name="account" type="text" autocomplete="username" inputmode="email" placeholder="输入账号或邮箱" required>
            </label>
            <label class="auth-field">
              <span>密码</span>
              <span class="auth-password-wrap">
                <input name="password" type="password" autocomplete="current-password" placeholder="输入密码" required>
                <button class="auth-eye" type="button" data-auth-action="toggle-password" aria-label="显示密码" title="显示密码">
                  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M2.5 12s3.5-6 9.5-6 9.5 6 9.5 6-3.5 6-9.5 6-9.5-6-9.5-6Z"/><circle cx="12" cy="12" r="2.75"/></svg>
                </button>
              </span>
            </label>
            <button class="auth-primary" type="submit">登录</button>
            <div class="auth-links">
              <button type="button" data-auth-mode="register">注册账号</button>
              <span aria-hidden="true"></span>
              <button type="button" data-auth-mode="reset">忘记密码</button>
            </div>
          </form>

          <section class="auth-panel" data-auth-view="register" hidden>
            <div class="auth-heading">
              <h1>注册账号</h1>
              <p>公司后端暂未开放注册接口，此页面先保留完整入口。</p>
            </div>
            <div class="auth-notice">当前无法发送验证码或创建账号。</div>
            <label class="auth-field"><span>邮箱地址</span><input type="email" placeholder="输入邮箱地址" disabled></label>
            <div class="auth-code-row">
              <label class="auth-field"><span>邮箱验证码</span><input type="text" placeholder="输入验证码" disabled></label>
              <button type="button" disabled>发送验证码</button>
            </div>
            <label class="auth-field"><span>登录名称</span><input type="text" placeholder="设置登录名称" disabled></label>
            <label class="auth-field"><span>密码</span><input type="password" placeholder="设置密码" disabled></label>
            <button class="auth-primary" type="button" disabled>暂未开放</button>
            <div class="auth-links auth-links-single"><button type="button" data-auth-mode="login">返回登录</button></div>
          </section>

          <section class="auth-panel" data-auth-view="reset" hidden>
            <div class="auth-heading">
              <h1>重置密码</h1>
              <p>公司后端暂未开放密码重置接口，此页面先保留完整入口。</p>
            </div>
            <div class="auth-notice">当前无法发送验证码或重置密码。</div>
            <label class="auth-field"><span>邮箱地址</span><input type="email" placeholder="输入注册邮箱" disabled></label>
            <div class="auth-code-row">
              <label class="auth-field"><span>邮箱验证码</span><input type="text" placeholder="输入验证码" disabled></label>
              <button type="button" disabled>发送验证码</button>
            </div>
            <label class="auth-field"><span>新密码</span><input type="password" placeholder="输入新密码" disabled></label>
            <label class="auth-field"><span>确认新密码</span><input type="password" placeholder="再次输入新密码" disabled></label>
            <button class="auth-primary" type="button" disabled>暂未开放</button>
            <div class="auth-links auth-links-single"><button type="button" data-auth-mode="login">返回登录</button></div>
          </section>

          <section class="auth-panel auth-status-view" data-auth-view="connection" hidden>
            <span class="auth-connection-icon" aria-hidden="true">
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M5 12.5a7 7 0 0 1 11.5-5.4"/><path d="M19 11.5a7 7 0 0 1-11.5 5.4"/><path d="m16 3 .5 4.1 4-.6"/><path d="m8 21-.5-4.1-4 .6"/></svg>
            </span>
            <h1>暂时无法连接服务</h1>
            <p id="authConnectionMessage">请确认网络和 CatsCo 后端状态，然后重试。</p>
            <div class="auth-status-actions">
              <button class="auth-primary" type="button" data-auth-action="retry">重试</button>
              <button class="auth-secondary" type="button" data-auth-action="other-account">使用其他账号</button>
            </div>
          </section>
        </div>
      </section>`;
  }

  function updateState(patch) {
    if (state?.auth) Object.assign(state.auth, patch);
  }

  function setView(name) {
    if (!root) return;
    root.querySelectorAll('[data-auth-view]').forEach(view => {
      view.hidden = view.dataset.authView !== name;
    });
    updateState({ mode: name });
  }

  function setLoginError(message = '') {
    const error = root?.querySelector('#authLoginError');
    if (!error) return;
    error.textContent = message;
    error.hidden = !message;
    updateState({ error: message });
  }

  function normalizeProfile(profile) {
    if (!profile || typeof profile !== 'object') return {};
    return profile.user && typeof profile.user === 'object' ? profile.user : profile;
  }

  function profileFromLoginResult(result) {
    const candidate = result?.user || result?.profile || result?.data?.user || result?.data?.profile || result;
    if (!candidate || typeof candidate !== 'object') return null;
    const profile = { ...candidate };
    delete profile.token;
    delete profile.access_token;
    delete profile.refresh_token;
    delete profile.password;
    const hasIdentity = profile.id || profile.uid || profile.username || profile.display_name || profile.email;
    return hasIdentity ? profile : null;
  }

  function saveProfile(profile) {
    const normalized = normalizeProfile(profile);
    localStorage.setItem('catsco-profile', JSON.stringify(normalized));
    updateState({ profile: normalized });
    if (typeof applyCompanyProfile === 'function') applyCompanyProfile(normalized);
    return normalized;
  }

  function readCachedProfile() {
    try {
      const cached = JSON.parse(localStorage.getItem('catsco-profile') || 'null');
      return cached && typeof cached === 'object' ? cached : null;
    } catch (error) {
      return null;
    }
  }

  function enterApp(profile, options = {}) {
    const normalized = saveProfile(profile);
    updateState({ status: 'authenticated', mode: 'login', error: '' });
    root.hidden = true;
    root.setAttribute('aria-hidden', 'true');
    document.body.classList.remove('auth-pending', 'auth-required');
    const input = document.getElementById('input');
    if (input && !window.matchMedia('(max-width: 720px)').matches) input.focus();
    document.dispatchEvent(new CustomEvent('catsco:authenticated', {
      detail: { profile: normalized, fresh: Boolean(options.fresh) },
    }));
    return normalized;
  }

  function showLogin(message = '') {
    if (!root) return;
    document.body.classList.remove('auth-pending');
    document.body.classList.add('auth-required');
    root.hidden = false;
    root.removeAttribute('aria-hidden');
    updateState({ status: 'unauthenticated' });
    setView('login');
    setLoginError(message);
    requestAnimationFrame(() => root.querySelector('[name="account"]')?.focus());
  }

  function showConnection(error) {
    const normalized = AppErrors.normalize(error, { context: 'auth-session' });
    document.body.classList.remove('auth-pending');
    document.body.classList.add('auth-required');
    root.hidden = false;
    root.removeAttribute('aria-hidden');
    updateState({ status: 'connection-error', error: normalized.message });
    const message = root.querySelector('#authConnectionMessage');
    if (message) message.textContent = normalized.code === 'NETWORK_ERROR'
      ? '无法连接 CatsCo 服务，请确认后端正在运行或网络可用。'
      : (normalized.message || '服务暂时不可用，请稍后重试。');
    setView('connection');
  }

  async function validateSession() {
    updateState({ status: 'checking', error: '' });
    setView('checking');
    document.body.classList.add('auth-pending');
    if (!CatsCompanyApi.isAuthenticated()) {
      showLogin();
      return false;
    }
    const cachedProfile = readCachedProfile();
    if (cachedProfile) {
      enterApp(cachedProfile);
      return true;
    }
    try {
      const profile = await CatsCompanyApi.getMe();
      enterApp(profile);
      return true;
    } catch (error) {
      const normalized = AppErrors.normalize(error, { context: 'auth-session' });
      if (normalized.status === 401 || normalized.status === 403) {
        CatsCompanyApi.logout();
        localStorage.removeItem('catsco-profile');
        showLogin('登录状态已失效，请重新登录。');
      } else {
        showConnection(normalized);
      }
      return false;
    }
  }

  async function login(form) {
    const account = form.elements.account.value.trim();
    const password = form.elements.password.value;
    if (!account || !password) return;
    const button = form.querySelector('[type="submit"]');
    setLoginError();
    button.disabled = true;
    button.dataset.label = button.textContent;
    button.textContent = '正在登录...';
    updateState({ status: 'submitting' });
    try {
      const result = await CatsCompanyApi.login(account, password);
      const token = result?.token || result?.access_token || result?.data?.token;
      if (!token) throw new AppErrors.AppError('登录响应中缺少访问令牌', { code: 'TOKEN_MISSING' });
      CatsCompanyApi.setToken(token);
      const profile = profileFromLoginResult(result) || await CatsCompanyApi.getMe();
      form.reset();
      enterApp(profile, { fresh: true });
      if (typeof syncCompanyData === 'function') syncCompanyData({ silent: true });
    } catch (error) {
      const normalized = AppErrors.normalize(error, { context: 'auth-login' });
      if (normalized.status === 401 || normalized.status === 403) {
        CatsCompanyApi.logout();
        setLoginError('账号或密码不正确。');
      } else if (normalized.code === 'NETWORK_ERROR') {
        setLoginError('无法连接 CatsCo 服务，请确认后端正在运行。');
      } else {
        setLoginError(normalized.message || '登录失败，请稍后重试。');
      }
      form.elements.password.value = '';
      form.elements.password.focus();
      updateState({ status: 'unauthenticated' });
    } finally {
      button.disabled = false;
      button.textContent = button.dataset.label || '登录';
    }
  }

  function signOut(options = {}) {
    if (window.DesktopConnectionFlow) DesktopConnectionFlow.reset();
    CatsCompanyApi.logout();
    localStorage.removeItem('catsco-profile');
    if (typeof clearCompanyData === 'function') clearCompanyData();
    showLogin(options.message || '');
    if (!options.silent && options.toast && typeof showToast === 'function') showToast(options.toast);
  }

  function bindEvents() {
    root.addEventListener('click', event => {
      const modeButton = event.target.closest('[data-auth-mode]');
      if (modeButton) {
        setLoginError();
        setView(modeButton.dataset.authMode);
        return;
      }
      const action = event.target.closest('[data-auth-action]')?.dataset.authAction;
      if (action === 'retry') validateSession();
      if (action === 'other-account') signOut({ silent: true });
      if (action === 'toggle-password') {
        const input = event.target.closest('.auth-password-wrap')?.querySelector('input');
        if (!input) return;
        const visible = input.type === 'text';
        input.type = visible ? 'password' : 'text';
        const button = event.target.closest('[data-auth-action="toggle-password"]');
        button.setAttribute('aria-label', visible ? '显示密码' : '隐藏密码');
        button.title = visible ? '显示密码' : '隐藏密码';
      }
    });
    root.querySelector('#authLoginForm').addEventListener('submit', event => {
      event.preventDefault();
      login(event.currentTarget);
    });
  }

  function init() {
    if (initialized) return;
    initialized = true;
    document.body.insertAdjacentHTML('afterbegin', template());
    root = document.getElementById('authView');
    bindEvents();
    validateSession();
  }

  return { init, validateSession, showLogin, showConnection, enterApp, signOut };
})();

document.addEventListener('DOMContentLoaded', () => AuthGate.init());
