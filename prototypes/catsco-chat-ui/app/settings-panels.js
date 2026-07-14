const SettingsPanels = {
  live: {
    relay: null,
    devices: null,
    releases: null,
  },
  mock: {
    user: {
      name: 'Cycren',
      login: 'Yangjiazun',
      uid: 300,
      plan: 'Plus',
      avatar: '',
    },
    relay: {
      url: 'https://relay.catsco.cc',
      openaiUrl: 'https://relay.catsco.cc/v1',
      anthropicUrl: 'https://relay.catsco.cc/anthropic',
      model: 'MiniMax-M2.7',
      used: '4.1324',
      quota: '1,000.00',
      remaining: '995.8676 CNY',
      keyPrefix: 'sk-bf-71...dcb7',
      updatedAt: '2026/7/1 16:12:05',
    },
    device: {
      version: 'v1.3.0',
      name: 'LAPTOP-GHHNJ148',
      status: 'not_active',
      capabilities: 'read_file, resolve_common_directory, glob, grep, write_file, edit_file, send_file',
    },
  },

  open(action) {
    closeAllMenus();
    document.querySelectorAll('.settings-overlay').forEach(el => el.remove());
    const overlay = document.createElement('div');
    overlay.className = 'settings-overlay';
    overlay.dataset.settingsAction = action;
    overlay.innerHTML = this.render(action);
    document.body.appendChild(overlay);

    overlay.addEventListener('click', e => {
      if (e.target === overlay || e.target.closest('[data-panel-close]')) this.close();
    });

    overlay.querySelectorAll('[data-panel-action]').forEach(btn => {
      btn.addEventListener('click', e => this.handleAction(e, btn.dataset.panelAction, overlay));
    });

    overlay.querySelectorAll('.segmented-control button').forEach(btn => {
      btn.addEventListener('click', () => {
        overlay.querySelectorAll('.segmented-control button').forEach(item => item.classList.remove('active'));
        btn.classList.add('active');
      });
    });

    this.bindDrafts(overlay, action);
    if (action === 'assistant' && window.DesktopConnectionFlow) DesktopConnectionFlow.paint();
    this.hydrate(action).then(changed => {
      if (!changed || !document.body.contains(overlay)) return;
      overlay.innerHTML = this.render(action);
      overlay.querySelectorAll('[data-panel-action]').forEach(btn => {
        btn.addEventListener('click', e => this.handleAction(e, btn.dataset.panelAction, overlay));
      });
      overlay.querySelectorAll('.segmented-control button').forEach(btn => {
        btn.addEventListener('click', () => {
          overlay.querySelectorAll('.segmented-control button').forEach(item => item.classList.remove('active'));
          btn.classList.add('active');
        });
      });
      this.bindDrafts(overlay, action);
      if (action === 'assistant' && window.DesktopConnectionFlow) DesktopConnectionFlow.paint();
    }).catch(error => AppErrors.report(error, { context: 'settings-hydrate', silent: true }));
  },

  async hydrate(action) {
    if (!CatsCompanyApi.isAuthenticated()) return false;
    if (action === 'profile') {
      const profile = await CatsCompanyApi.getMe();
      localStorage.setItem('catsco-profile', JSON.stringify(profile));
      applyCompanyProfile(profile);
      return true;
    }
    if (action === 'download' || action === 'assistant') {
      const [devices, releases] = await Promise.all([
        CatsCompanyApi.getDevices(),
        CatsCompanyApi.getDesktopReleases(),
      ]);
      this.live.devices = devices;
      this.live.releases = releases;
      return true;
    }
    if (action === 'relay') {
      const settled = await Promise.allSettled([
        CatsCompanyApi.getRelayConfig(),
        CatsCompanyApi.getRelayKey(),
        CatsCompanyApi.getRelayUsage(),
        CatsCompanyApi.getRelayCommercial(),
      ]);
      this.live.relay = {
        config: settled[0].status === 'fulfilled' ? settled[0].value : null,
        key: settled[1].status === 'fulfilled' ? settled[1].value : null,
        usage: settled[2].status === 'fulfilled' ? settled[2].value : null,
        commercial: settled[3].status === 'fulfilled' ? settled[3].value : null,
      };
      return true;
    }
    return false;
  },

  close() {
    document.querySelectorAll('.settings-overlay').forEach(el => {
      (el._feedbackFiles || []).forEach(item => URL.revokeObjectURL(item.url));
      el.remove();
    });
  },

  bindDrafts(root, action) {
    if (action !== 'feedback') return;
    const desc = root.querySelector('[name="feedback-desc"]');
    const contact = root.querySelector('[name="feedback-contact"]');
    const logs = root.querySelector('[name="feedback-logs"]');
    const count = root.querySelector('[data-feedback-count]');
    const submit = root.querySelector('[data-panel-action="submit-feedback"]');
    const mediaInput = root.querySelector('[name="feedback-media"]');
    const mediaButton = root.querySelector('[data-feedback-media-button]');
    const mediaList = root.querySelector('[data-feedback-media-list]');
    root._feedbackFiles = [];
    const saved = safeParse(localStorage.getItem('mc-feedback-draft')) || {};
    if (desc) desc.value = saved.desc || '';
    if (contact) contact.value = saved.contact || '';
    if (logs && typeof saved.logs === 'boolean') logs.checked = saved.logs;
    const update = () => {
      const length = desc?.value.length || 0;
      if (count) count.textContent = length + '/800';
      if (submit) submit.disabled = length === 0;
    };
    const save = () => localStorage.setItem('mc-feedback-draft', JSON.stringify({
      desc: desc?.value || '',
      contact: contact?.value || '',
      logs: logs?.checked ?? true,
    }));
    desc?.addEventListener('input', () => { update(); save(); });
    contact?.addEventListener('input', save);
    logs?.addEventListener('change', save);
    mediaButton?.addEventListener('click', () => mediaInput?.click());
    mediaInput?.addEventListener('change', () => {
      const incoming = Array.from(mediaInput.files || []);
      incoming.forEach(file => {
        if (root._feedbackFiles.length >= 3) return;
        if (file.size > 20 * 1024 * 1024) { showToast('单个文件不能超过 20 MB'); return; }
        const item = { file, url: URL.createObjectURL(file) };
        root._feedbackFiles.push(item);
      });
      mediaInput.value = '';
      if (incoming.length && root._feedbackFiles.length >= 3) showToast('最多添加 3 个文件');
      const renderMedia = () => {
        mediaList.innerHTML = '';
        root._feedbackFiles.forEach((item, index) => {
          const preview = document.createElement('div');
          preview.className = 'feedback-media-preview';
          preview.innerHTML = item.file.type.startsWith('image/')
            ? '<img src="' + item.url + '" alt="">'
            : '<span class="feedback-video-mark">视频</span>';
          const remove = document.createElement('button');
          remove.type = 'button';
          remove.setAttribute('aria-label', '移除 ' + item.file.name);
          remove.textContent = '×';
          remove.onclick = () => {
            URL.revokeObjectURL(item.url);
            root._feedbackFiles.splice(index, 1);
            renderMedia();
          };
          preview.title = item.file.name;
          preview.appendChild(remove);
          mediaList.appendChild(preview);
        });
      };
      renderMedia();
    });
    update();
  },

  async handleAction(event, action, root) {
    event.preventDefault();
    if (action === 'company-login') {
      const accountInput = root.querySelector('[name="company-account"]');
      const passwordInput = root.querySelector('[name="company-password"]');
      const account = accountInput?.value.trim() || '';
      const password = passwordInput?.value || '';
      if (!account) { accountInput?.focus(); return; }
      if (!password) { passwordInput?.focus(); return; }
      event.currentTarget.disabled = true;
      try {
        const result = await CatsCompanyApi.login(account, password);
        CatsCompanyApi.setToken(result.token || result.access_token || result.data?.token || '');
        const profile = { ...result };
        delete profile.token;
        localStorage.setItem('catsco-profile', JSON.stringify(profile));
        applyCompanyProfile(profile);
        passwordInput.value = '';
        await syncCompanyData({ silent: true });
        this.close();
        showToast('\u516c\u53f8\u8d26\u53f7\u5df2\u8fde\u63a5');
      } catch (error) {
        passwordInput.value = '';
        passwordInput.focus();
        showToast(error.status === 401 ? '\u8d26\u53f7\u6216\u5bc6\u7801\u4e0d\u6b63\u786e' : '\u516c\u53f8\u670d\u52a1\u8fde\u63a5\u5931\u8d25');
      } finally {
        event.currentTarget.disabled = false;
      }
    } else if (action === 'company-sync') {
      event.currentTarget.disabled = true;
      await syncCompanyData();
      event.currentTarget.disabled = false;
    } else if (action === 'company-disconnect') {
      this.close();
      AuthGate.signOut({ toast: '\u5df2\u9000\u51fa\u767b\u5f55' });
    } else if (action === 'copy') {
      const text = event.currentTarget.dataset.copy || root.querySelector('[data-copy-source]')?.innerText || '';
      navigator.clipboard?.writeText(text);
      showToast('已复制');
    } else if (action === 'submit-feedback') {
      const desc = root.querySelector('[name="feedback-desc"]')?.value.trim() || '';
      if (!desc) { root.querySelector('[name="feedback-desc"]')?.focus(); return; }
      const contact = root.querySelector('[name="feedback-contact"]')?.value.trim() || '';
      if (CatsCompanyApi.isAuthenticated()) {
        event.currentTarget.disabled = true;
        try {
          const attachments = [];
          for (const item of (root._feedbackFiles || [])) {
            const purpose = item.file.type.startsWith('image/') ? 'feedback' : 'chat';
            const uploaded = await CatsCompanyApi.uploadFile(item.file, purpose);
            attachments.push({
              file_key: uploaded.file_key,
              url: uploaded.url,
              name: uploaded.name || item.file.name,
              size: uploaded.size || item.file.size,
              type: uploaded.type || (item.file.type.startsWith('image/') ? 'image' : 'file'),
            });
          }
          await CatsCompanyApi.submitFeedback({
            category: 'suggestion',
            title: '\u6765\u81ea CatsCo Chat UI \u7684\u53cd\u9988',
            description: desc + (contact ? '\n\n\u8054\u7cfb\u65b9\u5f0f\uff1a' + contact : ''),
            page_url: location.href,
            user_agent: navigator.userAgent,
            attachments,
          });
          localStorage.removeItem('mc-feedback-draft');
          this.close();
          showToast('\u53cd\u9988\u5df2\u63d0\u4ea4');
          return;
        } catch (error) {
          showToast('\u5728\u7ebf\u63d0\u4ea4\u5931\u8d25\uff0c\u5df2\u4fdd\u5b58\u5230\u672c\u673a');
        } finally {
          event.currentTarget.disabled = false;
        }
      }
      const entries = safeParse(localStorage.getItem('mc-feedback-entries')) || [];
      entries.unshift({
        id: 'feedback_' + Date.now(),
        desc,
        contact,
        logs: root.querySelector('[name="feedback-logs"]')?.checked ?? true,
        attachments: (root._feedbackFiles || []).map(item => ({
          name: item.file.name,
          type: item.file.type,
          size: item.file.size,
        })),
        createdAt: Date.now(),
      });
      localStorage.setItem('mc-feedback-entries', JSON.stringify(entries.slice(0, 20)));
      localStorage.removeItem('mc-feedback-draft');
      this.close();
      showToast('反馈已保存到本机');
    } else if (action === 'clear-feedback') {
      root.querySelectorAll('input, textarea').forEach(el => { el.value = ''; });
      localStorage.removeItem('mc-feedback-draft');
      showToast('草稿已清空');
    } else if (action === 'fill-sample') {
      const prompt = event.currentTarget.dataset.prompt || '';
      const input = document.getElementById('input');
      if (input) {
        input.value = prompt;
        autoGrow(input);
        updateSendBtn();
        input.focus();
      }
      this.close();
    } else if (action === 'profile-save') {
      const name = root.querySelector('[name="profile-name"]')?.value?.trim();
      if (!name) return;
      event.currentTarget.disabled = true;
      try {
        const current = safeParse(localStorage.getItem('catsco-profile')) || {};
        const updated = CatsCompanyApi.isAuthenticated()
          ? await CatsCompanyApi.updateMe(name, current.avatar_url || '')
          : { ...current, display_name: name };
        const profile = { ...current, ...updated, display_name: updated.display_name || name };
        localStorage.setItem('catsco-profile', JSON.stringify(profile));
        applyCompanyProfile(profile);
        this.close();
        showToast('资料已保存');
      } catch (error) {
        showToast('资料保存失败');
      } finally {
        event.currentTarget.disabled = false;
      }
    } else if (action === 'download') {
      const url = event.currentTarget.dataset.downloadUrl || '';
      if (url) window.open(url, '_blank', 'noopener');
      else showToast('暂无可用下载地址');
    } else if (action === 'open-desktop') {
      if (window.DesktopConnectionFlow) await DesktopConnectionFlow.start();
    } else if (action === 'relay-reveal') {
      try {
        const result = await CatsCompanyApi.revealRelayKey();
        const value = result.key?.key || result.key || '';
        if (value) await navigator.clipboard?.writeText(value);
        showToast(value ? '完整 Key 已复制' : '当前没有可用 Key');
      } catch (error) { showToast('无法显示 Key'); }
    } else if (action === 'relay-rotate') {
      try {
        await CatsCompanyApi.rotateRelayKey();
        await this.hydrate('relay');
        this.open('relay');
        showToast('Key 已重新生成');
      } catch (error) { showToast('Key 重新生成失败'); }
    } else if (action === 'relay-revoke') {
      try {
        await CatsCompanyApi.revokeRelayKey();
        await this.hydrate('relay');
        this.open('relay');
        showToast('Key 已撤销');
      } catch (error) { showToast('Key 撤销失败'); }
    } else if (action === 'logout') {
      this.close();
      AuthGate.signOut({ toast: '已退出登录' });
    } else if (action === 'open-relay') {
      this.open('relay');
    } else {
      showToast('这是演示面板');
    }
  },

  render(action) {
    const body = {
      company: this.companyPanel(),
      feedback: this.feedbackPanel(),
      download: this.downloadPanel(),
      assistant: this.assistantPanel(),
      samples: this.samplesPanel(),
      relay: this.relayPanel(),
      profile: this.profilePanel(),
      logout: this.logoutPanel(),
    }[action] || this.placeholderPanel(action);
    return body;
  },

  companyPanel() {
    const connected = CatsCompanyApi.isAuthenticated();
    const profile = safeParse(localStorage.getItem('catsco-profile')) || {};
    if (connected) {
      const name = profile.display_name || profile.username || '\u516c\u53f8\u8d26\u53f7';
      const identity = profile.email || profile.username || '';
      return this.shell('\u516c\u53f8\u8d26\u53f7', '', `
        <section class="settings-card company-account-card">
          <div class="settings-row">
            <span class="settings-row-icon">${iconSvg('laptop')}</span>
            <div><strong>${escapeHtml(name)}</strong><p>${escapeHtml(identity)} · UID ${escapeHtml(profile.uid || '')}</p></div>
            <span class="settings-badge">\u5df2\u8fde\u63a5</span>
          </div>
          <p class="settings-card-note">\u597d\u53cb\u3001\u7fa4\u804a\u548c Agent \u52a9\u624b\u4f1a\u4ece CatsCo \u516c\u53f8\u670d\u52a1\u540c\u6b65\uff0c\u672c\u5730\u4efb\u52a1\u6570\u636e\u4e0d\u4f1a\u88ab\u8986\u76d6\u3002</p>
          <footer class="settings-actions">
            <button type="button" data-panel-action="company-disconnect">\u65ad\u5f00\u8fde\u63a5</button>
            <button class="primary" type="button" data-panel-action="company-sync">\u7acb\u5373\u540c\u6b65</button>
          </footer>
        </section>
      `, 'settings-panel-narrow company-panel');
    }
    return this.shell('\u8fde\u63a5\u516c\u53f8\u8d26\u53f7', '', `
      <div class="feedback-intro company-intro">
        <span class="feedback-intro-icon" aria-hidden="true">${iconSvg('laptop')}</span>
        <div><h3>\u8fde\u63a5 CatsCo \u516c\u53f8\u670d\u52a1</h3><p>\u8fde\u63a5\u540e\u540c\u6b65\u4f60\u7684\u8d44\u6599\u3001\u597d\u53cb\u3001\u7fa4\u804a\u548c Agent \u52a9\u624b\u3002</p></div>
      </div>
      <label class="settings-field"><span>\u8d26\u53f7</span><input name="company-account" autocomplete="username" placeholder="\u90ae\u7bb1\u6216\u7528\u6237\u540d"></label>
      <label class="settings-field"><span>\u5bc6\u7801</span><input name="company-password" type="password" autocomplete="current-password" placeholder="\u8bf7\u8f93\u5165\u5bc6\u7801"></label>
      <p class="settings-card-note">\u5bc6\u7801\u4ec5\u7528\u4e8e\u672c\u6b21\u767b\u5f55\uff0c\u4e0d\u4f1a\u5199\u5165\u9879\u76ee\u6216\u672c\u5730\u914d\u7f6e\u3002</p>
      <footer class="settings-actions">
        <button type="button" data-panel-close>\u53d6\u6d88</button>
        <button class="primary" type="button" data-panel-action="company-login">\u8fde\u63a5</button>
      </footer>
    `, 'settings-panel-narrow company-panel');
  },

  shell(title, subtitle, content, extraClass = '') {
    return `
      <section class="settings-panel ${extraClass}" role="dialog" aria-modal="true" aria-label="${escapeHtml(title)}">
        <header class="settings-panel-head">
          <h2>${title}</h2>
          <button class="settings-close" type="button" data-panel-close aria-label="关闭">×</button>
        </header>
        ${subtitle ? `<div class="settings-title-divider"><span>${subtitle}</span></div>` : ''}
        <div class="settings-panel-body">${content}</div>
      </section>
    `;
  },

  feedbackPanel() {
    return this.shell('意见反馈', '', `
      <div class="feedback-intro">
        <span class="feedback-intro-icon" aria-hidden="true">${iconSvg('bug')}</span>
        <div><h3>你的每一条反馈，都在帮助 CatsCo 变得更好</h3><p>请描述遇到的问题或建议，我们会认真查看。</p></div>
      </div>
      <label class="feedback-message-field">
        <textarea name="feedback-desc" maxlength="800" rows="8" placeholder="描述你遇到的问题或建议"></textarea>
        <div class="feedback-media-tools">
          <div class="feedback-media-list" data-feedback-media-list></div>
          <button class="feedback-media-button" type="button" data-feedback-media-button><span aria-hidden="true">＋</span>上传图片/录屏</button>
          <input name="feedback-media" type="file" accept="image/*,video/*" multiple hidden>
        </div>
        <span class="feedback-counter" data-feedback-count>0/800</span>
      </label>
      <label class="settings-field feedback-contact-field">
        <span>联系方式 <small>可选</small></span>
        <input name="feedback-contact" maxlength="100" placeholder="手机号、邮箱或其他联系方式，方便后续跟进">
      </label>
      <div class="feedback-footer-row">
        <label class="feedback-log-consent"><input name="feedback-logs" type="checkbox" checked><span>允许附带基础运行日志，帮助定位问题</span></label>
        <footer class="settings-actions feedback-actions">
          <button type="button" data-panel-close>取消</button>
          <button class="primary" type="button" data-panel-action="submit-feedback" disabled>提交</button>
        </footer>
      </div>
    `, 'settings-panel-narrow feedback-panel');
  },

  assistantPanel() {
    const windowsDownload = this.live.releases?.downloads?.windows || '';
    return this.shell('连接本地 CatsCo 助手', '', `
      <div class="desktop-connect-flow" data-desktop-connect-root>
        <div class="desktop-connect-summary">
          <span class="desktop-connect-icon" data-desktop-status-icon>${iconSvg('laptop')}</span>
          <div>
            <strong data-desktop-status-title>打开已安装的 CatsCo 桌面端</strong>
            <p data-desktop-status-copy>选择模型并点击“检查并启动”，网页会自动确认连接。</p>
          </div>
        </div>
        <div class="desktop-connect-error" data-desktop-error hidden></div>
        <div class="desktop-connect-meta" data-desktop-meta hidden></div>
        <div class="desktop-connect-actions">
          <button class="primary" type="button" data-panel-action="open-desktop" data-desktop-connect-button>打开 CatsCo 桌面端</button>
          <button type="button" data-panel-action="download" data-download-url="${escapeHtml(windowsDownload)}" ${windowsDownload ? '' : 'disabled'}>下载桌面端</button>
        </div>
      </div>
    `, 'settings-panel-compact');
  },

  downloadPanel() {
    const devices = this.live.devices?.devices || [];
    const remoteDevice = devices[0] || {};
    const releases = this.live.releases || {};
    const d = {
      ...this.mock.device,
      version: releases.version ? ('v' + releases.version) : this.mock.device.version,
      name: remoteDevice.name || remoteDevice.device_name || this.mock.device.name,
      status: remoteDevice.status || remoteDevice.connection_status || this.mock.device.status,
    };
    const statusLabel = d.status === 'active' ? '已连接' : '未连接';
    return this.shell('CatsCo 本机设备', `当前版本 ${d.version}`, `
      <div class="settings-list">
        <div class="settings-row">
          <span class="settings-row-icon">${iconSvg('laptop')}</span>
          <div><strong>连接这台电脑</strong><p>一键打开 CatsCo 桌面端并完成设备配对。</p></div>
          <button data-panel-action="open-desktop">连接</button>
        </div>
        <div class="settings-row device-status-row">
          <span class="settings-row-icon">${iconSvg('laptop')}</span>
          <div>
            <strong>${d.name}</strong>
            <p>可读取和管理本机项目文件</p>
            <template class="device-permissions">
              <summary>查看权限详情</summary>
              <small>${d.capabilities}</small>
            </template>
          </div>
          <span class="status-pill device-status-pill">${statusLabel}</span>
        </div>
      </div>
      <div class="settings-section-label">桌面端下载</div>
      <div class="download-grid">
        ${this.downloadItem('Windows', '适用于 Windows 10/11 的安装程序', 'x64 / arm64', releases.downloads?.windows)}
        ${this.downloadItem('macOS Apple Silicon', '适用于 M 系列芯片 Mac', 'arm64', releases.downloads?.['mac-arm'])}
        ${this.downloadItem('macOS Intel', '适用于 Intel 芯片 Mac', 'x64', releases.downloads?.['mac-intel'])}
        ${this.downloadItem('Linux AppImage', '无需安装，下载后赋予执行权限运行', 'x64', releases.downloads?.['linux-appimage'])}
        ${this.downloadItem('Linux Debian / Ubuntu', '适用于 Debian、Ubuntu 等发行版', 'deb', releases.downloads?.['linux-deb'])}
      </div>
    `);
  },

  downloadItem(title, desc, tag, url = '') {
    return `<div class="download-item"><span class="settings-row-icon">${iconSvg('download')}</span><div><strong>${title}</strong><p>${desc}</p></div><span>${tag}</span><button data-panel-action="download" data-download-url="${escapeHtml(url || '')}" ${url ? '' : 'disabled'}>${iconSvg('download')}</button></div>`;
  },

  samplesPanel() {
    const prompt = '我的桌面文件有点乱，帮我整理一下，先列出分类方案和需要确认的问题。';
    return this.shell('选择示例任务', '', `
      <button class="sample-task" type="button" data-panel-action="fill-sample" data-prompt="${escapeHtml(prompt)}">
        <span class="settings-row-icon">${iconSvg('book')}</span>
        <span><strong>整理桌面</strong><p>把示例任务填入输入栏，后续 AI 会继续确认具体事项。</p></span>
        <span class="sample-arrow">›</span>
      </button>
    `, 'settings-panel-compact');
  },

  relayPanel() {
    const live = this.live.relay || {};
    const relayConfig = live.config || {};
    const keyInfo = live.key?.key || {};
    const usage = live.usage?.summary || {};
    const endpoints = relayConfig.endpoints || [];
    const openAI = endpoints.find(item => /openai/i.test(item.protocol || ''))?.base_url;
    const anthropic = endpoints.find(item => /anthropic/i.test(item.protocol || ''))?.base_url;
    const r = {
      ...this.mock.relay,
      url: relayConfig.base_url || this.mock.relay.url,
      openaiUrl: openAI || this.mock.relay.openaiUrl,
      anthropicUrl: anthropic || this.mock.relay.anthropicUrl,
      model: usage.model || relayConfig.default_model || this.mock.relay.model,
      used: Number.isFinite(usage.used_cny) ? usage.used_cny.toFixed(4) : this.mock.relay.used,
      quota: Number.isFinite(usage.limit_cny) ? usage.limit_cny.toFixed(2) : this.mock.relay.quota,
      remaining: Number.isFinite(usage.remaining_cny) ? usage.remaining_cny.toFixed(4) + ' CNY' : this.mock.relay.remaining,
      percent: Number.isFinite(usage.percent) ? Math.max(0, Math.min(100, usage.percent)) : 8,
      keyPrefix: keyInfo.prefix || this.mock.relay.keyPrefix,
      updatedAt: keyInfo.updated_at || keyInfo.created_at || this.mock.relay.updatedAt,
      keyConfigured: live.key ? !!live.key.configured : true,
    };
    const config = `OpenAI 兼容\nBase URL: ${r.openaiUrl}\nModel: ${r.model}\nAPI Key: ${r.keyPrefix}\n\nAnthropic 兼容\nBase URL: ${r.anthropicUrl}\nModel: ${r.model}\nAPI Key: ${r.keyPrefix}`;
    return this.shell('CatsCo 模型服务', '', `
      <div class="relay-hero">
        <span class="settings-row-icon">${iconSvg('key')}</span>
        <div><strong>${r.url}</strong><p>当前模型：${r.model}</p></div>
        <span class="status-pill">Key 可用</span>
        <button data-panel-action="copy" data-copy="${escapeHtml(r.keyPrefix)}">${iconSvg('copy')} 复制配置</button>
      </div>
      <section class="settings-card">
        <div class="settings-card-head"><strong>套餐与邀请码</strong><span class="warn-pill">未开放</span></div>
        <div class="quota-card">
          <div><span>当前模型额度</span><strong>${r.model}</strong><p>anthropic 已用 ${r.used} / ${r.quota} CNY</p></div>
          <strong>${r.remaining}</strong>
          <div class="quota-bar"><span style="width: ${r.percent}%"></span></div>
        </div>
        <div class="stat-grid">
          <div><strong>无套餐</strong><span>套餐账本额度</span></div>
          <div><strong>无套餐</strong><span>当前有效套餐</span></div>
          <div><strong>无套餐</strong><span>套餐最近到期</span></div>
          <div><strong>每月重置</strong><span>下次 8月1日 16:12</span></div>
        </div>
      </section>
      <section class="settings-card">
        <h3>连接地址</h3>
        <div class="endpoint-grid">
          <div><strong>OpenAI SDK</strong><code>${r.openaiUrl}</code><button data-panel-action="copy" data-copy="${r.openaiUrl}">复制</button></div>
          <div><strong>Anthropic SDK</strong><code>${r.anthropicUrl}</code><button data-panel-action="copy" data-copy="${r.anthropicUrl}">复制</button></div>
        </div>
      </section>
      <section class="settings-card">
        <div class="settings-card-head"><h3>我的 Key</h3><span class="status-pill">Key 可用</span></div>
        <div class="key-grid"><div><span>名称</span><strong>Yangjiazun</strong></div><div><span>前缀</span><strong>${r.keyPrefix}</strong></div><div><span>更新时间</span><strong>${r.updatedAt}</strong></div></div>
        <footer class="settings-actions"><button data-panel-action="relay-reveal">显示并复制</button><button data-panel-action="relay-rotate">重新生成</button><button class="danger" data-panel-action="relay-revoke">撤销</button></footer>
      </section>
      <section class="settings-card">
        <div class="settings-card-head"><h3>快速配置</h3><button data-panel-action="copy" data-copy="${escapeHtml(config)}">复制</button></div>
        <pre data-copy-source>${escapeHtml(config)}</pre>
      </section>
    `, 'settings-panel-wide settings-panel-relay');
  },

  profilePanel() {
    const profile = safeParse(localStorage.getItem('catsco-profile')) || {};
    const u = {
      ...this.mock.user,
      name: profile.display_name || profile.username || this.mock.user.name,
      login: profile.username || profile.email || this.mock.user.login,
      uid: profile.uid || this.mock.user.uid,
      avatar: profile.avatar_url || '',
    };
    return this.shell('编辑个人资料', '', `
      <div class="profile-avatar"><div>${escapeHtml(u.name.slice(0, 1))}</div><button type="button">选择头像</button></div>
      <label class="settings-field">
        <span>聊天昵称</span>
        <input name="profile-name" value="${escapeHtml(u.name)}">
      </label>
      <div class="uid-card"><div><span>我的 UID</span><strong>${u.uid}</strong><p>其他人可以通过搜索UID来申请好友或邀请你的虚拟员工</p></div><button data-panel-action="copy" data-copy="${u.uid}">复制 UID</button></div>
      <div class="settings-field profile-mode-field">
        <span>思考显示方式</span>
        <select class="ui-select" name="thinking-mode">
          <option>显示 AI 思考过程（Code Mode）</option>
          <option>隐藏 AI 思考过程（简洁模式）</option>
        </select>
      </div>
      <section class="settings-card flat"><h3>账号安全</h3><button>重置登录密码<span>通过注册邮箱验证码设置新密码。</span></button></section>
      <section class="settings-card flat"><h3>开发者工具</h3><button data-panel-action="open-relay">CatsCo 中转站<span>查看 OpenAI / Anthropic 兼容接入地址。</span></button></section>
      <footer class="settings-actions"><button type="button" data-panel-close>取消</button><button class="primary" type="button" data-panel-action="profile-save">保存</button></footer>
    `, 'settings-panel-narrow');
  },

  logoutPanel() {
    return this.shell('退出登录', '退出当前 CatsCo 公司账号。', `
      <p class="settings-note">退出后将清除本机登录状态，并返回登录页；本地界面偏好仍会保留。</p>
      <footer class="settings-actions"><button data-panel-close>取消</button><button class="danger solid" data-panel-action="logout">退出登录</button></footer>
    `, 'settings-panel-compact');
  },

  placeholderPanel(action) {
    return this.shell('功能面板', '这个功能正在整理成 CatsCo 替代版体验。', `<p class="settings-note">当前动作：${escapeHtml(action || 'unknown')}</p>`, 'settings-panel-compact');
  },
};

SettingsModalComponent.register(SettingsPanels);

function openSettingsPanel(action) {
  SettingsModalComponent.open(action);
}

function safeParse(raw) {
  try { return raw ? JSON.parse(raw) : null; } catch { return null; }
}

document.addEventListener('keydown', e => {
  if (e.key === 'Escape') SettingsModalComponent.close();
});
