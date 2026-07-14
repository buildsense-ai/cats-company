window.DesktopConnectionFlow = (() => {
  const POLL_INTERVAL = 2000;
  const POLL_LIMIT = 60;
  const PROMPT_KEY = 'catsco-desktop-connect-prompted';
  let pollTimer = null;
  let hintTimer = null;
  let attempts = 0;
  let startInFlight = false;
  let session = null;
  let connection = {
    status: 'idle',
    error: '',
    agent: null,
    model: '',
    launchDetected: false,
  };

  function agentsFrom(result) {
    if (Array.isArray(result)) return result;
    if (Array.isArray(result?.agents)) return result.agents;
    if (Array.isArray(result?.data?.agents)) return result.data.agents;
    return [];
  }

  function connectedLocalAgent(result) {
    return agentsFrom(result).find(agent => agent?.relation === 'owner' && agent?.is_online === true) || null;
  }

  function agentModel(agent) {
    return String(
      agent?.model || agent?.model_name || agent?.current_model ||
      agent?.metadata?.model || agent?.config?.model || agent?.runtime?.model || ''
    ).trim();
  }

  function stateCopy() {
    const map = {
      idle: ['打开已安装的 CatsCo 桌面端', '选择模型并点击“检查并启动”，网页会自动确认连接。', '打开 CatsCo 桌面端'],
      opening: ['正在打开 CatsCo 桌面端', '如果浏览器询问是否允许打开 CatsCo，请选择允许。', '正在打开...'],
      waiting: ['正在等待桌面端启动模型', '请在桌面端选择模型，然后点击“检查并启动”。', '等待连接...'],
      claimed: ['账号已连接，正在等待模型启动', '桌面端已收到账号信息，请完成模型选择并启动。', '等待模型启动...'],
      download: ['没有检测到桌面端', '若尚未安装，请先下载；安装后再点击打开。', '重新打开桌面端'],
      failed: ['连接没有完成', connection.error || '请确认桌面端已安装并重试。', '重新连接'],
      connected: ['已连接本地 CatsCo 助手', connection.model ? `当前模型：${connection.model}` : '本地助手已在线，可以开始使用。', '已连接'],
    };
    return map[connection.status] || map.idle;
  }

  function paint() {
    document.documentElement.dataset.desktopConnection = connection.status;
    document.querySelectorAll('[data-desktop-connect-root]').forEach(root => {
      const [title, copy, buttonText] = stateCopy();
      root.dataset.status = connection.status;
      const titleNode = root.querySelector('[data-desktop-status-title]');
      const copyNode = root.querySelector('[data-desktop-status-copy]');
      const button = root.querySelector('[data-desktop-connect-button]');
      const error = root.querySelector('[data-desktop-error]');
      const meta = root.querySelector('[data-desktop-meta]');
      if (titleNode) titleNode.textContent = title;
      if (copyNode) copyNode.textContent = copy;
      if (button) {
        button.textContent = buttonText;
        button.disabled = ['opening', 'waiting', 'claimed', 'connected'].includes(connection.status);
      }
      if (error) {
        error.textContent = connection.error;
        error.hidden = !connection.error;
      }
      if (meta) {
        const name = connection.agent?.display_name || connection.agent?.username || '';
        meta.textContent = name ? `已连接：${name}${connection.model ? ` · ${connection.model}` : ''}` : '';
        meta.hidden = !name;
      }
    });
  }

  function setStatus(status, patch = {}) {
    connection = { ...connection, ...patch, status };
    paint();
  }

  function stopPolling() {
    if (pollTimer) window.clearInterval(pollTimer);
    if (hintTimer) window.clearTimeout(hintTimer);
    pollTimer = null;
    hintTimer = null;
  }

  function syncModel(agent) {
    const model = agentModel(agent);
    if (!model) return;
    connection.model = model;
    if (typeof MODEL_OPTIONS !== 'undefined' && typeof setAvailableModels === 'function') {
      const names = MODEL_OPTIONS.map(item => item.id);
      setAvailableModels([...names, model], model);
      state.model = model;
      localStorage.setItem('mc-selected-model', model);
      if (typeof renderModelPicker === 'function') renderModelPicker();
    }
    const selector = document.getElementById('modelSelect');
    if (selector) selector.title = `已连接本地 CatsCo · ${model}`;
  }

  async function checkExisting() {
    if (!CatsCompanyApi.isAuthenticated()) return null;
    try {
      const agent = connectedLocalAgent(await CatsCompanyApi.getAgents());
      if (!agent) return null;
      stopPolling();
      connection.agent = agent;
      syncModel(agent);
      setStatus('connected', { agent, error: '', model: agentModel(agent) });
      return agent;
    } catch (error) {
      return null;
    }
  }

  async function pollOnce() {
    attempts += 1;
    try {
      if (session?.code) {
        const status = await CatsCompanyApi.getDesktopConnectStatus(session.code).catch(() => null);
        if (status?.state === 'claimed' && connection.status !== 'connected') setStatus('claimed');
      }
      if (await checkExisting()) return;
    } catch (error) {
      // A transient polling failure should not interrupt the desktop startup flow.
    }
    if (attempts >= POLL_LIMIT) {
      stopPolling();
      setStatus('failed', { error: '等待连接超时。请确认桌面端已选择模型并点击“检查并启动”。' });
    }
  }

  function beginPolling() {
    attempts = 0;
    stopPolling();
    pollTimer = window.setInterval(pollOnce, POLL_INTERVAL);
    hintTimer = window.setTimeout(() => {
      if (!['connected', 'claimed'].includes(connection.status)) setStatus('download');
    }, 12000);
    pollOnce();
  }

  async function start() {
    if (startInFlight || ['opening', 'waiting', 'claimed', 'connected'].includes(connection.status)) return;
    startInFlight = true;
    setStatus('opening', { error: '', launchDetected: false });
    try {
      if (await checkExisting()) return;
      session = await CatsCompanyApi.createDesktopConnectSession();
      if (!session?.code) throw new Error('连接会话缺少连接码');
      setStatus('waiting');
      location.href = session.deeplink_url || `catsco://connect?code=${encodeURIComponent(session.code)}`;
      beginPolling();
      if (typeof showToast === 'function') showToast('已请求打开 CatsCo 桌面端');
    } catch (error) {
      const message = error?.message || '无法创建桌面端连接会话，请稍后重试。';
      setStatus('failed', { error: message });
    } finally {
      startInFlight = false;
      paint();
    }
  }

  async function promptAfterLogin(detail = {}) {
    if (!CatsCompanyApi.isAuthenticated()) return;
    if (await checkExisting()) return;
    const prompted = sessionStorage.getItem(PROMPT_KEY) === '1';
    if (detail.fresh || !prompted) {
      sessionStorage.setItem(PROMPT_KEY, '1');
      window.setTimeout(() => SettingsPanels.open('assistant'), 280);
    }
  }

  function reset() {
    stopPolling();
    session = null;
    attempts = 0;
    startInFlight = false;
    connection = { status: 'idle', error: '', agent: null, model: '', launchDetected: false };
    sessionStorage.removeItem(PROMPT_KEY);
    paint();
  }

  document.addEventListener('catsco:authenticated', event => promptAfterLogin(event.detail || {}));

  return { start, paint, reset, checkExisting, promptAfterLogin, getState: () => ({ ...connection }) };
})();
