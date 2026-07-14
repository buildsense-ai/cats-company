function lineIcon(kind) {
  const icons = {
    chat: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M5.5 5.5h13A2.5 2.5 0 0 1 21 8v8a2.5 2.5 0 0 1-2.5 2.5H10l-5 3v-3.2A2.5 2.5 0 0 1 3 16V8a2.5 2.5 0 0 1 2.5-2.5Z"/><path d="M8 10h8M8 14h5"/></svg>',
    project: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M3.5 6.5A2.5 2.5 0 0 1 6 4h4.2l2 2.5H18A2.5 2.5 0 0 1 20.5 9v8.5A2.5 2.5 0 0 1 18 20H6a2.5 2.5 0 0 1-2.5-2.5v-11Z"/><path d="M4 9h16"/></svg>',
    group: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M8 11a3 3 0 1 0 0-6 3 3 0 0 0 0 6Z"/><path d="M16 11a3 3 0 1 0 0-6 3 3 0 0 0 0 6Z"/><path d="M3.5 19a4.5 4.5 0 0 1 9 0"/><path d="M11.5 19a4.5 4.5 0 0 1 9 0"/></svg>',
    user: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="12" cy="8" r="4"/><path d="M4.5 20a7.5 7.5 0 0 1 15 0"/></svg>',
    agent: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><rect x="5" y="7" width="14" height="11" rx="3"/><path d="M9 7V5.5a3 3 0 0 1 6 0V7"/><path d="M9.5 12h.01M14.5 12h.01"/><path d="M10 16h4"/></svg>'
  };
  return icons[kind] || icons.chat;
}

function loadSessions() {

  try {

    const raw = localStorage.getItem('mc-sessions');

    if (raw) state.sessions = JSON.parse(raw);

  } catch (e) { state.sessions = []; }

  migratePinned();

  if (!state.sessions.length) newChat(false);

  else {

    const last = localStorage.getItem('mc-current');

    state.currentId = (last && state.sessions.find(s => s.id === last)) ? last : state.sessions[0].id;

  }

}

function toggleSidebar() {

  document.getElementById('sidebar').classList.toggle('open');
  hideSessionTip();

}

function getSessionTaskState(s) {
  const messages = Array.isArray(s.messages) ? s.messages : [];
  for (let i = messages.length - 1; i >= 0; i--) {
    const m = messages[i];
    if (!m || (m.role === 'user' && !m.agentTask && !m.streaming)) continue;
    const status = m.taskProcess?.status || m.agentTask?.status;
    const markerAt = m.taskProcess?.updatedAt || m.agentTask?.updatedAt || m.time || 0;
    if (s.taskStateDismissedAt && markerAt && s.taskStateDismissedAt >= markerAt) return '';
    if (m.streaming || ['preparing', 'connecting', 'generating', 'finalizing', 'thinking', 'planning', 'executing'].includes(status)) return 'running';
    if (status === 'completed') return 'done';
    if (status === 'stopped' || status === 'error' || m.role === 'error') return 'warn';
    if (m.role === 'assistant' && m.content) return 'done';
  }
  return '';
}

function buildSessionItem(s) {

  const div = document.createElement('div');

  div.className = 'session' + (s.id === state.currentId ? ' active' : '');

  div.dataset.sid = s.id;

  div.dataset.tip = (s.title || '新对话').slice(0, 60);

  const icon = document.createElement('span');

  icon.className = 'icon line-icon';

  icon.innerHTML = lineIcon('chat');

  const title = document.createElement('span');

  title.className = 'title';

  title.textContent = s.title || '新对话';

  title.title = '双击重命名';

  title.ondblclick = (e) => { e.stopPropagation(); startRename(s.id, title); };

  div.addEventListener('mouseenter', () => showSessionTip(div));

  div.addEventListener('mouseleave', hideSessionTip);

  const pin = document.createElement('button');

  pin.className = 'pin-btn' + (s.pinned ? ' pinned' : '');

  pin.title = s.pinned ? '取消置顶' : '置顶';

  pin.setAttribute('aria-label', pin.title);

  pin.innerHTML = '<svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M9 3h6"/><path d="M10 3v6l-3 4v2h10v-2l-3-4V3"/><path d="M12 15v6"/></svg>';

  pin.onclick = (e) => { e.stopPropagation(); togglePin(s.id); };

  const more = document.createElement('button');

  more.className = 'more-btn';

  more.title = '更多';

  more.textContent = '⋯';

  more.onclick = (e) => { e.stopPropagation(); toggleSessionMenu(e, s.id); };

  const taskState = getSessionTaskState(s);

  let taskIndicator = null;

  if (taskState) {
    taskIndicator = document.createElement('span');
    taskIndicator.className = 'session-task-state ' + taskState;
    taskIndicator.title = taskState === 'running' ? '任务进行中' : (taskState === 'done' ? '任务已完成' : '任务已中止或失败');
    taskIndicator.setAttribute('aria-label', taskIndicator.title);
  }

  div.appendChild(icon);

  div.appendChild(title);

  div.appendChild(pin);

  div.appendChild(more);

  if (taskIndicator) div.appendChild(taskIndicator);

  div.onclick = () => switchSession(s.id);
  div.ondblclick = (e) => { if (e.target.closest('button')) return; e.stopPropagation(); const t = div.querySelector('.title'); if (t) startRename(s.id, t); };

  return div;

}

function buildGroupItem(g) {
  const div = document.createElement('div');
  div.className = 'group-item' + (g.id === state.groupCurrentId ? ' active' : '');
  div.dataset.gid = g.id;
  div.dataset.tip = g.name;
  const icon = document.createElement('span');
  icon.className = 'icon line-icon';
  icon.innerHTML = lineIcon('group');
  const body = document.createElement('div');
  body.className = 'group-body';
  const name = document.createElement('span');
  name.className = 'name';
  name.textContent = g.name;
  name.title = '\u53cc\u51fb\u91cd\u547d\u540d';
  name.ondblclick = (e) => { e.stopPropagation(); startRenameGroup(g.id, name); };
  const meta = document.createElement('span');
  meta.className = 'group-meta';
  meta.textContent = g.lastMessage || '\u6682\u65e0\u6d88\u606f';
  body.appendChild(name);
  body.appendChild(meta);
  const pin = document.createElement('button');
  pin.className = 'pin-btn' + (g.pinned ? ' pinned' : '');
  pin.title = g.pinned ? '取消置顶' : '置顶';
  pin.setAttribute('aria-label', pin.title);
  pin.innerHTML = '<svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M9 3h6"/><path d="M10 3v6l-3 4v2h10v-2l-3-4V3"/><path d="M12 15v6"/></svg>';
  pin.onclick = (e) => { e.stopPropagation(); toggleGroupPin(g.id); };
  const more = document.createElement('button');
  more.className = 'more-btn';
  more.title = '更多';
  more.textContent = '⋯';
  more.onclick = (e) => { e.stopPropagation(); toggleGroupMenu(e, g.id); };
  div.appendChild(icon);
  div.appendChild(body);
  div.appendChild(pin);
  div.appendChild(more);
  div.addEventListener('mouseenter', () => showSessionTip(div));
  div.addEventListener('mouseleave', hideSessionTip);
  div.onclick = () => {
    state.groupCurrentId = g.id;
    if (g.source === 'company') CompanyRuntime.openContact(g, 'group');
    renderSidebar();
  };
  return div;
}

function startRenameGroup(gid, nameEl) {
  if (renamingGroupId) return;
  renamingGroupId = gid;
  const original = nameEl.textContent;
  const input = document.createElement('input');
  input.type = 'text';
  input.className = 'rename-input';
  input.value = original;
  input.maxLength = 30;
  nameEl.replaceWith(input);
  input.focus();
  input.select();
  let done = false;
  const finish = (save) => {
    if (done) return;
    done = true;
    renamingGroupId = null;
    if (save) { saveGroupRename(gid, input.value.trim(), original); }
    else {
      const span = document.createElement('span');
      span.className = 'name';
      span.textContent = original;
      span.title = '\u53cc\u51fb\u91cd\u547d\u540d';
      span.ondblclick = (e) => { e.stopPropagation(); startRenameGroup(gid, span); };
      input.replaceWith(span);
    }
  };
  input.addEventListener('keydown', (e) => {
    if (e.key === 'Enter') { e.preventDefault(); finish(true); }
    else if (e.key === 'Escape') { e.preventDefault(); finish(false); }
  });
  input.addEventListener('blur', () => finish(true));
}

async function saveGroupRename(gid, newName, original) {
  const g = state.groups.find(x => x.id === gid);
  if (g && newName && newName !== original) {
    try {
      if (g.source === 'company') await CatsCompanyApi.updateGroup(g.remoteId, newName, g.avatarUrl || '');
      g.name = newName;
      saveGroups();
      showToast('\u5df2\u91cd\u547d\u540d');
    } catch (error) { showToast('\u7fa4\u804a\u91cd\u547d\u540d\u5931\u8d25'); }
  }
  renderSidebar();
}

function askNameDialog(options) {
  return new Promise(resolve => {
    document.querySelector('.name-dialog-overlay')?.remove();
    const overlay = document.createElement('div');
    overlay.className = 'name-dialog-overlay';
    overlay.innerHTML =
      '<div class="name-dialog" role="dialog" aria-modal="true">'
      + '<div class="name-dialog-head">'
      + '<h2>' + escapeHtml(options.title || '输入名称') + '</h2>'
      + '<button class="name-dialog-close" type="button" aria-label="关闭">×</button>'
      + '</div>'
      + '<input class="name-dialog-input" type="text" maxlength="' + (options.maxLength || 40) + '" placeholder="' + escapeHtml(options.placeholder || '') + '">'
      + '<div class="name-dialog-actions">'
      + '<button class="name-dialog-cancel" type="button">取消</button>'
      + '<button class="name-dialog-confirm" type="button">' + escapeHtml(options.confirmText || '创建') + '</button>'
      + '</div>'
      + '</div>';
    document.body.appendChild(overlay);

    const input = overlay.querySelector('.name-dialog-input');
    const finish = (value) => {
      overlay.remove();
      resolve(value);
    };

    overlay.querySelector('.name-dialog-close').onclick = () => finish(null);
    overlay.querySelector('.name-dialog-cancel').onclick = () => finish(null);
    overlay.querySelector('.name-dialog-confirm').onclick = () => finish(input.value.trim());
    overlay.onclick = (e) => {
      if (e.target === overlay) finish(null);
    };
    overlay.onkeydown = (e) => {
      if (e.key === 'Escape') {
        e.preventDefault();
        finish(null);
      } else if (e.key === 'Enter') {
        e.preventDefault();
        finish(input.value.trim());
      }
    };
    setTimeout(() => input.focus(), 0);
  });
}

function askProjectCreationDialog(options = {}) {
  return new Promise(resolve => {
    document.querySelector('.name-dialog-overlay')?.remove();
    const sessions = Array.isArray(options.sessions) ? options.sessions : [];
    const overlay = document.createElement('div');
    overlay.className = 'name-dialog-overlay';
    overlay.innerHTML =
      '<div class="name-dialog project-create-dialog" role="dialog" aria-modal="true" aria-label="新建项目">'
      + '<div class="name-dialog-head"><h2>新建项目</h2><button class="name-dialog-close" type="button" aria-label="关闭">×</button></div>'
      + '<label class="project-create-name"><span>项目名称</span><input class="name-dialog-input" type="text" maxlength="40" placeholder="输入项目名称"></label>'
      + '<div class="project-create-section-head"><div><strong>添加历史任务</strong><span>可选，创建后任务会移入项目</span></div><button class="project-select-all" type="button">全选</button></div>'
      + '<div class="project-create-session-list"></div>'
      + '<div class="name-dialog-actions"><button class="name-dialog-cancel" type="button">取消</button><button class="name-dialog-confirm" type="button" disabled>创建项目</button></div>'
      + '</div>';
    document.body.appendChild(overlay);
    const input = overlay.querySelector('.name-dialog-input');
    const list = overlay.querySelector('.project-create-session-list');
    const confirmButton = overlay.querySelector('.name-dialog-confirm');
    const selectAllButton = overlay.querySelector('.project-select-all');

    if (!sessions.length) {
      const empty = document.createElement('div');
      empty.className = 'project-create-empty';
      empty.textContent = '暂无可添加的历史任务';
      list.appendChild(empty);
      selectAllButton.hidden = true;
    } else {
      sessions.forEach(session => {
        const label = document.createElement('label');
        label.className = 'project-create-session';
        const checkbox = document.createElement('input');
        checkbox.type = 'checkbox';
        checkbox.value = session.id;
        const content = document.createElement('span');
        content.innerHTML = '<strong>' + escapeHtml(session.title || '新任务') + '</strong>';
        label.appendChild(checkbox);
        label.appendChild(content);
        list.appendChild(label);
      });
    }

    const sync = () => {
      confirmButton.disabled = !input.value.trim();
      const boxes = [...list.querySelectorAll('input[type="checkbox"]')];
      selectAllButton.textContent = boxes.length && boxes.every(box => box.checked) ? '取消全选' : '全选';
    };
    const finish = value => { overlay.remove(); resolve(value); };
    input.addEventListener('input', sync);
    list.addEventListener('change', sync);
    selectAllButton.onclick = () => {
      const boxes = [...list.querySelectorAll('input[type="checkbox"]')];
      const next = !boxes.every(box => box.checked);
      boxes.forEach(box => { box.checked = next; });
      sync();
    };
    overlay.querySelector('.name-dialog-close').onclick = () => finish(null);
    overlay.querySelector('.name-dialog-cancel').onclick = () => finish(null);
    confirmButton.onclick = () => finish({
      name: input.value.trim(),
      sessionIds: [...list.querySelectorAll('input[type="checkbox"]:checked')].map(box => box.value)
    });
    overlay.onclick = event => { if (event.target === overlay) finish(null); };
    overlay.onkeydown = event => {
      if (event.key === 'Escape') { event.preventDefault(); finish(null); }
    };
    sync();
    setTimeout(() => input.focus(), 0);
  });
}

function askProjectPickerDialog(options) {
  return new Promise(resolve => {
    document.querySelector('.name-dialog-overlay')?.remove();
    const projects = options.projects || [];
    const overlay = document.createElement('div');
    overlay.className = 'name-dialog-overlay';
    overlay.innerHTML =
      '<div class="name-dialog project-picker-dialog" role="dialog" aria-modal="true">'
      + '<div class="name-dialog-head">'
      + '<h2>' + escapeHtml(options.title || '选择项目') + '</h2>'
      + '<button class="name-dialog-close" type="button" aria-label="关闭">×</button>'
      + '</div>'
      + '<div class="project-picker-list"></div>'
      + '<div class="name-dialog-actions">'
      + '<button class="name-dialog-cancel" type="button">取消</button>'
      + '</div>'
      + '</div>';
    document.body.appendChild(overlay);
    const list = overlay.querySelector('.project-picker-list');
    const finish = (value) => {
      overlay.remove();
      resolve(value);
    };

    projects.forEach(project => {
      const btn = document.createElement('button');
      btn.className = 'project-picker-option';
      btn.type = 'button';
      btn.textContent = project.name;
      btn.onclick = () => finish(project.id);
      list.appendChild(btn);
    });

    overlay.querySelector('.name-dialog-close').onclick = () => finish(null);
    overlay.querySelector('.name-dialog-cancel').onclick = () => finish(null);
    overlay.onclick = (e) => {
      if (e.target === overlay) finish(null);
    };
    overlay.onkeydown = (e) => {
      if (e.key === 'Escape') {
        e.preventDefault();
        finish(null);
      }
    };
    setTimeout(() => list.querySelector('button')?.focus(), 0);
  });
}

async function addGroup() {
  const raw = await askNameDialog({
    title: '创建群聊',
    placeholder: '输入群聊名称',
    confirmText: '创建',
    maxLength: 30
  });
  if (!raw) return;
  const name = raw.trim();
  if (!name) return;
  if (!Array.isArray(state.groups)) state.groups = [];
  state.groups.unshift({
    id: 'g_' + Date.now() + '_' + Math.random().toString(36).slice(2, 6),
    name: name,
    lastMessage: '\u7fa4\u521a\u521a\u521b\u5efa',
    lastAt: Date.now()
  });
  localStorage.setItem('mc-groups-collapsed', '0');
  saveGroups();
  renderSidebar();
  showToast('\u5df2\u521b\u5efa\u7fa4\u804a');
}

async function removeGroup(gid) {
  const g = state.groups.find(x => x.id === gid);
  if (!g) return;
  if (!confirm('\u5220\u9664\u7fa4\u804a\u300c' + g.name + '\u300d\uff1f')) return;
  if (g.source === 'company') {
    try {
      await CatsCompanyApi.leaveGroup(g.remoteId);
    } catch (leaveError) {
      try {
        await CatsCompanyApi.disbandGroup(g.remoteId);
      } catch (disbandError) {
        showToast('\u9000\u51fa\u6216\u89e3\u6563\u516c\u53f8\u7fa4\u804a\u5931\u8d25');
        return;
      }
    }
  }
  state.groups = state.groups.filter(x => x.id !== gid);
  if (state.groupCurrentId === gid) state.groupCurrentId = null;
  saveGroups();
  renderSidebar();
  showToast('\u5df2\u5220\u9664');
}

function loadGroups() {
  try {
    const raw = localStorage.getItem('mc-groups');
    const arr = raw ? JSON.parse(raw) : [];
    state.groups = Array.isArray(arr) ? arr.map(group => ({ ...group, pinned: group.pinned === true })) : [];
  } catch (e) { state.groups = []; }
}

function saveGroups() {
  try { localStorage.setItem('mc-groups', JSON.stringify(state.groups || [])); } catch (e) {}
}

function toggleGroupPin(gid) {
  const group = state.groups.find(item => item.id === gid);
  if (!group) return;
  group.pinned = !group.pinned;
  saveGroups();
  renderSidebar();
  showToast(group.pinned ? '群聊已置顶' : '群聊已取消置顶');
}

function buildAgentItem(agent) {
  const div = document.createElement('div');
  div.className = 'agent-item' + (agent.id === state.agentCurrentId ? ' active' : '');
  div.dataset.aid = agent.id;
  div.dataset.tip = agent.name;
  const icon = document.createElement('span');
  icon.className = 'icon line-icon';
  icon.innerHTML = lineIcon('agent');
  const body = document.createElement('div');
  body.className = 'group-body';
  const name = document.createElement('span');
  name.className = 'name';
  name.textContent = agent.name;
  name.title = '\u53cc\u51fb\u91cd\u547d\u540d';
  name.ondblclick = (e) => { e.stopPropagation(); startRenameAgent(agent.id, name); };
  const meta = document.createElement('span');
  meta.className = 'group-meta';
  meta.textContent = agent.description || '\u52a9\u624b\u521a\u521a\u6dfb\u52a0';
  body.appendChild(name);
  const more = document.createElement('button');
  more.className = 'more-btn';
  more.title = '\u66f4\u591a';
  more.textContent = '\u22ef';
  more.onclick = (e) => { e.stopPropagation(); toggleAgentMenu(e, agent.id); };
  div.appendChild(icon);
  div.appendChild(body);
  div.appendChild(more);
  div.addEventListener('mouseenter', () => showSessionTip(div));
  div.addEventListener('mouseleave', hideSessionTip);
  div.onclick = () => { selectComposerAgent(agent.id); };
  return div;
}

function startRenameAgent(aid, nameEl) {
  if (renamingAgentId) return;
  renamingAgentId = aid;
  const original = nameEl.textContent;
  const input = document.createElement('input');
  input.type = 'text';
  input.className = 'rename-input';
  input.value = original;
  input.maxLength = 30;
  nameEl.replaceWith(input);
  input.focus();
  input.select();
  let done = false;
  const finish = (save) => {
    if (done) return;
    done = true;
    renamingAgentId = null;
    if (save) saveAgentRename(aid, input.value.trim(), original);
    else {
      const span = document.createElement('span');
      span.className = 'name';
      span.textContent = original;
      span.title = '\u53cc\u51fb\u91cd\u547d\u540d';
      span.ondblclick = (e) => { e.stopPropagation(); startRenameAgent(aid, span); };
      input.replaceWith(span);
    }
  };
  input.addEventListener('keydown', (e) => {
    if (e.key === 'Enter') { e.preventDefault(); finish(true); }
    else if (e.key === 'Escape') { e.preventDefault(); finish(false); }
  });
  input.addEventListener('blur', () => finish(true));
}

async function saveAgentRename(aid, newName, original) {
  const agent = state.agents.find(x => x.id === aid);
  if (agent && newName && newName !== original) {
    try {
      if (agent.source === 'company' && agent.remoteId) {
        await CatsCompanyApi.updateBot(agent.remoteId, { display_name: newName });
      }
      agent.name = newName;
    } catch (error) { showToast('\u52a9\u624b\u91cd\u547d\u540d\u5931\u8d25'); renderSidebar(); return; }
    saveAgents();
    showToast('\u5df2\u91cd\u547d\u540d');
  }
  renderSidebar();
}

async function addAgent() {
  document.querySelector('.name-dialog-overlay')?.remove();
  if (!Array.isArray(state.agents)) state.agents = [];

  const overlay = document.createElement('div');
  overlay.className = 'name-dialog-overlay agent-manager-overlay';
  overlay.innerHTML = `
    <div class="name-dialog agent-manager-dialog" role="dialog" aria-modal="true" aria-label="AI 助手管理">
      <header class="agent-manager-head">
        <div class="agent-manager-brand"><span class="agent-bolt" aria-hidden="true">&#8623;</span><strong>AI 助手管理</strong></div>
        <nav class="agent-manager-tabs" aria-label="助手管理视图">
          <button type="button" data-agent-view="mine">我创建的助手</button>
          <button type="button" data-agent-view="create" class="active">创建新助手</button>
        </nav>
        <button class="name-dialog-close" type="button" aria-label="关闭">&times;</button>
      </header>
      <section class="agent-manager-view" data-agent-panel="mine"></section>
      <section class="agent-manager-view active" data-agent-panel="create">
        <div class="agent-create-intro">
          <h2>创建你的专属助手</h2>
          <p>先定义助手身份，再配置运行方式。</p>
        </div>
        <div class="agent-create-layout">
          <section class="agent-create-card agent-basic-card">
            <h3><span aria-hidden="true">&#9635;</span>基本信息</h3>
            <label class="agent-field"><span>助手名称 <em>*</em></span><input id="agentNameInput" maxlength="30" placeholder="例如：代码审查助手"></label>
            <label class="agent-field"><span>助手定位 <em>*</em></span><select class="ui-select" id="agentPositionInput"><option value="code-review">代码审查助手</option><option value="debug">问题排查助手</option><option value="writing">写作助手</option><option value="research">研究助手</option><option value="general">通用助手</option></select></label>
            <label class="agent-field agent-description-field"><span>用途说明 <em>*</em></span><textarea id="agentDescriptionInput" maxlength="500" placeholder="说明这个助手解决什么问题，以及你希望它如何工作"></textarea><small data-agent-description-count>0/500</small></label>
          </section>
          <section class="agent-create-card agent-capability-card">
            <h3><span aria-hidden="true">&#10022;</span>能力预览</h3>
            <div class="agent-capability-list">
              <div><span>&lt;/&gt;</span><p><strong>阅读代码</strong><small>理解代码结构与逻辑，快速定位功能</small></p></div>
              <div><span>◎</span><p><strong>分析 Bug</strong><small>识别潜在问题，分析原因与影响</small></p></div>
              <div><span>◇</span><p><strong>优化建议</strong><small>提供可行的优化建议与最佳实践</small></p></div>
              <div><span>▤</span><p><strong>生成方案</strong><small>整理可执行的修改方案与代码片段</small></p></div>
            </div>
          </section>
        </div>
        <div class="agent-deploy-section">
          <h3><span aria-hidden="true">&#9881;</span>部署方式 <small>高级设置</small></h3>
          <div class="agent-host-options" role="radiogroup" aria-label="托管方式">
            <button type="button" class="agent-host-option selected" role="radio" aria-checked="true">
              <strong>自托管</strong><span>生成本地身份 Key，后续连接你的服务。</span>
            </button>
            <button type="button" class="agent-host-option disabled" role="radio" aria-checked="false" disabled>
              <strong>云托管</strong><span>无需部署，创建后直接使用，即将推出。</span>
            </button>
          </div>
        </div>
        <button class="agent-create-submit" type="button">创建我的专属助手</button>
      </section>
    </div>`;
  document.body.appendChild(overlay);

  const dialog = overlay.querySelector('.agent-manager-dialog');
  const minePanel = overlay.querySelector('[data-agent-panel="mine"]');
  const createPanel = overlay.querySelector('[data-agent-panel="create"]');
  const tabs = Array.from(overlay.querySelectorAll('[data-agent-view]'));
  const nameInput = overlay.querySelector('#agentNameInput');
  const descriptionInput = overlay.querySelector('#agentDescriptionInput');
  const positionInput = overlay.querySelector('#agentPositionInput');
  const descriptionCount = overlay.querySelector('[data-agent-description-count]');

  const close = () => overlay.remove();
  const setView = (view) => {
    tabs.forEach(tab => tab.classList.toggle('active', tab.dataset.agentView === view));
    minePanel.classList.toggle('active', view === 'mine');
    createPanel.classList.toggle('active', view === 'create');
    if (view === 'create') requestAnimationFrame(() => nameInput.focus());
  };
  const copyText = async (value, message) => {
    try { await navigator.clipboard.writeText(value); showToast(message); }
    catch (e) { showToast('复制失败，请手动复制'); }
  };
  const renderManagedAgents = () => {
    if (!state.agents.length) {
      minePanel.innerHTML = '<div class="agent-manager-empty"><strong>还没有创建助手</strong><span>创建后，助手会显示在这里和左侧协作栏中。</span><button type="button">创建第一个助手</button></div>';
      minePanel.querySelector('button').onclick = () => setView('create');
      return;
    }
    minePanel.innerHTML = '<div class="agent-card-grid"></div>';
    const grid = minePanel.querySelector('.agent-card-grid');
    state.agents.forEach(agent => {
      const card = document.createElement('article');
      card.className = 'agent-manage-card';
      const uid = agent.uid || agent.id;
      const key = agent.apiKey || '仅创建者可见';
      card.innerHTML = `
        <div class="agent-card-top"><span class="agent-card-avatar">${escapeHtml((agent.name || 'AI').slice(0, 1))}</span><div><strong>${escapeHtml(agent.name)}</strong><span>UID ${escapeHtml(uid)}</span></div></div>
        <p>${escapeHtml(agent.description || '专属 AI 助手')}</p>
        <span class="agent-public-badge">私有助手</span>
        <div class="agent-card-actions"><button type="button" data-copy-uid>复制 UID</button><button type="button" data-copy-key>复制 Key</button><button type="button" class="danger" data-delete-agent>删除</button></div>`;
      card.querySelector('[data-copy-uid]').onclick = () => copyText(uid, '已复制 UID');
      card.querySelector('[data-copy-key]').onclick = () => copyText(key, '已复制 Key');
      card.querySelector('[data-delete-agent]').onclick = async () => {
        await removeAgent(agent.id);
        renderManagedAgents();
      };
      grid.appendChild(card);
    });
  };

  tabs.forEach(tab => tab.onclick = () => setView(tab.dataset.agentView));
  descriptionInput.addEventListener('input', () => {
    descriptionCount.textContent = descriptionInput.value.length + '/500';
  });
  overlay.querySelector('.name-dialog-close').onclick = close;
  overlay.onclick = (e) => { if (e.target === overlay) close(); };
  dialog.onclick = (e) => e.stopPropagation();
  overlay.querySelector('.agent-create-submit').onclick = async () => {
    const name = nameInput.value.trim();
    const description = descriptionInput.value.trim();
    if (!name) { nameInput.focus(); showToast('请输入助手名称'); return; }
    if (!description) { descriptionInput.focus(); showToast('请填写用途说明'); return; }
    if (state.agents.some(agent => agent.name === name)) { showToast('已有同名助手'); return; }
    const submitButton = overlay.querySelector('.agent-create-submit');
    if (CatsCompanyApi.isAuthenticated()) {
      submitButton.disabled = true;
      try {
        const randomBytes = new Uint32Array(4);
        crypto.getRandomValues(randomBytes);
        const result = await CatsCompanyApi.createAgent({
          username: 'agent_' + Date.now().toString(36) + '_' + randomBytes[0].toString(36),
          display_name: name,
          password: Array.from(randomBytes, value => value.toString(36)).join('-'),
          model: state.model,
        });
        await syncCompanyData({ silent: true });
        const created = state.agents.find(agent => String(agent.uid) === String(result.uid));
        if (created) {
          created.apiKey = result.api_key || '';
          created.description = description;
          created.position = positionInput.value;
          state.agentCurrentId = created.id;
          saveAgents();
        }
        renderSidebar();
        renderManagedAgents();
        setView('mine');
        showToast('公司 Agent 已创建');
      } catch (error) {
        showToast('创建 Agent 失败');
      } finally {
        submitButton.disabled = false;
      }
      return;
    }
    const stamp = Date.now();
    const agent = {
      id: 'a_' + stamp + '_' + Math.random().toString(36).slice(2, 6),
      uid: 'agent_' + Math.random().toString(36).slice(2, 10),
      apiKey: 'catsco_' + Math.random().toString(36).slice(2) + Math.random().toString(36).slice(2),
      name,
      description,
      position: positionInput.value,
      hosting: 'self',
      createdAt: stamp
    };
    state.agents.unshift(agent);
    state.agentCurrentId = agent.id;
    localStorage.setItem('mc-current-agent', agent.id);
    localStorage.setItem('mc-agents-collapsed', '0');
    saveAgents(); renderSidebar(); renderManagedAgents(); setView('mine');
    showToast('已创建 Agent');
  };
  overlay.addEventListener('keydown', (e) => {
    if (e.key === 'Escape') close();
    if (e.key === 'Enter' && createPanel.classList.contains('active')) overlay.querySelector('.agent-create-submit').click();
  });
  renderManagedAgents();
  requestAnimationFrame(() => nameInput.focus());
}

async function removeAgent(aid) {
  const agent = state.agents.find(x => x.id === aid);
  if (!agent) return;
  if (!confirm('\u5220\u9664 Agent\u300c' + agent.name + '\u300d\uff1f')) return;
  if (agent.source === 'company' && agent.remoteId) {
    try { await CatsCompanyApi.deleteBot(agent.remoteId); }
    catch (error) { showToast('\u5220\u9664\u516c\u53f8 Agent \u5931\u8d25'); return; }
  }
  state.agents = state.agents.filter(x => x.id !== aid);
  if (state.agentCurrentId === aid) state.agentCurrentId = null;
  saveAgents();
  renderSidebar();
  showToast('\u5df2\u5220\u9664');
}

function loadAgents() {
  try {
    const raw = localStorage.getItem('mc-agents');
    const arr = raw ? JSON.parse(raw) : [];
    state.agents = Array.isArray(arr) ? arr : [];
  } catch (e) { state.agents = []; }
  const savedId = localStorage.getItem('mc-current-agent');
  state.agentCurrentId = state.agents.some(agent => agent.id === savedId)
    ? savedId
    : (state.agents[0]?.id || null);
}

function saveAgents() {
  try { localStorage.setItem('mc-agents', JSON.stringify(state.agents || [])); } catch (e) {}
}

function isProjectOpen(projectId) {
  return localStorage.getItem('mc-project-open-' + projectId) !== '0';
}

function setProjectOpen(projectId, open) {
  localStorage.setItem('mc-project-open-' + projectId, open ? '1' : '0');
}

function buildProjectChatItem(session) {
  const div = document.createElement('div');
  div.className = 'project-chat-item' + (session.id === state.currentId ? ' active' : '');
  div.dataset.sid = session.id;
  div.dataset.tip = session.title || '新对话';
  const icon = document.createElement('span');
  icon.className = 'icon line-icon';
  icon.innerHTML = lineIcon('chat');
  const title = document.createElement('span');
  title.className = 'title';
  title.textContent = session.title || '新对话';
  const more = document.createElement('button');
  more.className = 'more-btn';
  more.title = '更多';
  more.textContent = '⋯';
  more.onclick = (e) => { e.stopPropagation(); toggleSessionMenu(e, session.id); };
  div.appendChild(icon);
  div.appendChild(title);
  div.appendChild(more);
  div.addEventListener('mouseenter', () => showSessionTip(div));
  div.addEventListener('mouseleave', hideSessionTip);
  div.onclick = () => switchSession(session.id);
  return div;
}

function buildProjectItem(project) {
  const wrap = document.createElement('div');
  const chats = projectSessions(project.id);
  const open = isProjectOpen(project.id) && chats.length > 0;
  wrap.className = 'project-block' + (open ? ' open' : '') + (!chats.length ? ' empty' : '');
  wrap.dataset.pid = project.id;

  const div = document.createElement('div');
  div.className = 'project-item' + ((state.currentProjectId === project.id || chats.some(s => s.id === state.currentId)) ? ' active' : '');
  div.dataset.pid = project.id;
  div.dataset.tip = project.name;
  const chevron = chats.length ? document.createElement('span') : null;
  if (chevron) {
    chevron.className = 'project-chevron';
    chevron.innerHTML = '<svg viewBox="0 0 24 24" width="12" height="12" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round"><polyline points="9 6 15 12 9 18"/></svg>';
  }
  const icon = document.createElement('span');
  icon.className = 'icon line-icon';
  icon.innerHTML = lineIcon('project');
  const body = document.createElement('div');
  body.className = 'group-body';
  const name = document.createElement('span');
  name.className = 'name';
  name.textContent = project.name;
  name.title = '双击重命名';
  name.ondblclick = (e) => { e.stopPropagation(); startRenameProject(project.id, name); };
  const meta = document.createElement('span');
  meta.className = 'group-meta';
  const count = chats.length;
  meta.textContent = count ? count + ' 个对话' : '项目为空';
  body.appendChild(name);
  body.appendChild(meta);
  const more = document.createElement('button');
  more.className = 'more-btn';
  more.title = '更多';
  more.textContent = '⋯';
  more.onclick = (e) => { e.stopPropagation(); toggleProjectMenu(e, project.id); };
  div.appendChild(icon);
  div.appendChild(body);
  if (chevron) div.appendChild(chevron);
  div.appendChild(more);
  div.addEventListener('mouseenter', () => showSessionTip(div));
  div.addEventListener('mouseleave', hideSessionTip);
  div.onclick = () => {
    if (!chats.length) {
      prepareProjectTask(project.id);
      return;
    }
    setProjectOpen(project.id, !open);
    renderSidebar();
  };
  wrap.appendChild(div);

  const list = document.createElement('div');
  list.className = 'project-chat-list' + (open ? '' : ' collapsed');
  chats.forEach(s => list.appendChild(buildProjectChatItem(s)));
  wrap.appendChild(list);
  return wrap;
}

function prepareProjectTask(projectId) {
  if (state.streaming) {
    showToast('生成中，请先停止');
    return;
  }
  const project = state.projects.find(item => item.id === projectId);
  if (!project) return;

  state.currentProjectId = project.id;
  state.currentId = null;
  localStorage.setItem('mc-current-project', project.id);
  localStorage.removeItem('mc-current');

  renderSidebar();
  renderMessages();
  requestAnimationFrame(() => document.getElementById('input')?.focus());
}

function startRenameProject(pid, nameEl) {
  if (renamingProjectId) return;
  renamingProjectId = pid;
  const original = nameEl.textContent;
  const input = document.createElement('input');
  input.type = 'text';
  input.className = 'rename-input';
  input.value = original;
  input.maxLength = 40;
  nameEl.replaceWith(input);
  input.focus();
  input.select();
  let done = false;
  const finish = (save) => {
    if (done) return;
    done = true;
    renamingProjectId = null;
    if (save) saveProjectRename(pid, input.value.trim(), original);
    else {
      const span = document.createElement('span');
      span.className = 'name';
      span.textContent = original;
      span.title = '双击重命名';
      span.ondblclick = (e) => { e.stopPropagation(); startRenameProject(pid, span); };
      input.replaceWith(span);
    }
  };
  input.addEventListener('keydown', (e) => {
    if (e.key === 'Enter') { e.preventDefault(); finish(true); }
    else if (e.key === 'Escape') { e.preventDefault(); finish(false); }
  });
  input.addEventListener('blur', () => finish(true));
}

function saveProjectRename(pid, newName, original) {
  const project = state.projects.find(p => p.id === pid);
  if (project && newName && newName !== original) {
    project.name = newName;
    saveProjects();
    showToast('已重命名项目');
  }
  renderSidebar();
  renderMessages();
}

async function addProject() {
  const availableSessions = (state.sessions || []).filter(session => (
    !session.projectId && Array.isArray(session.messages) && session.messages.length > 0
  ));
  const result = await askProjectCreationDialog({
    sessions: availableSessions
  });
  if (!result) return;
  const name = result.name.trim();
  if (!name) return;
  if (!Array.isArray(state.projects)) state.projects = [];
  const project = {
    id: 'p_' + Date.now() + '_' + Math.random().toString(36).slice(2, 6),
    name,
    instructions: '',
    files: [],
    createdAt: Date.now()
  };
  state.projects.unshift(project);
  const selectedIds = new Set(result.sessionIds || []);
  state.sessions.forEach(session => {
    if (selectedIds.has(session.id)) session.projectId = project.id;
  });
  if (selectedIds.size) setProjectOpen(project.id, true);
  localStorage.setItem('mc-projects-collapsed', '0');
  saveProjects();
  saveSessions();
  renderSidebar();
  document.getElementById('projectsToggle')?.classList.remove('collapsed');
  document.getElementById('projectsList')?.classList.remove('collapsed');
  showToast('已创建项目');
}

function removeProject(pid) {
  const project = state.projects.find(p => p.id === pid);
  if (!project) return;
  if (!confirm('删除项目「' + project.name + '」？项目内的对话会保留到全部对话。')) return;
  state.projects = state.projects.filter(p => p.id !== pid);
  state.sessions.forEach(s => {
    if (s.projectId === pid) delete s.projectId;
  });
  if (state.currentProjectId === pid) {
    state.currentProjectId = null;
    localStorage.removeItem('mc-current-project');
  }
  saveProjects();
  saveSessions();
  renderSidebar();
  renderMessages();
  showToast('已删除项目');
}

function loadProjects() {
  try {
    const raw = localStorage.getItem('mc-projects');
    const arr = raw ? JSON.parse(raw) : [];
    state.projects = Array.isArray(arr) ? arr.map(p => ({
      ...p,
      instructions: typeof p.instructions === 'string' ? p.instructions : '',
      files: Array.isArray(p.files) ? p.files.map(file => ({
        ...file,
        id: file.id || ('pf_' + Math.random().toString(36).slice(2, 10))
      })) : []
    })) : [];
  } catch (e) { state.projects = []; }
  const last = localStorage.getItem('mc-current-project');
  state.currentProjectId = null;
  if (last) localStorage.removeItem('mc-current-project');
}

function saveProjects() {
  try { localStorage.setItem('mc-projects', JSON.stringify(state.projects || [])); } catch (e) {}
}

function projectSessions(projectId) {
  return (state.sessions || []).filter(s => (
    s.projectId === projectId && Array.isArray(s.messages) && s.messages.length > 0
  ));
}

async function moveSessionToProject(sid) {
  const sess = state.sessions.find(s => s.id === sid);
  if (!sess) return;
  if (!state.projects.length) {
    showToast('请先创建项目');
    addProject();
    return;
  }
  const projectId = await askProjectPickerDialog({
    title: '加入项目',
    projects: state.projects
  });
  if (!projectId) return;
  const project = state.projects.find(p => p.id === projectId);
  if (!project) {
    showToast('没有找到这个项目');
    return;
  }
  sess.projectId = project.id;
  setProjectOpen(project.id, true);
  saveSessions();
  renderSidebar();
  showToast('已加入项目');
}

function removeSessionFromProject(sid) {
  const sess = state.sessions.find(s => s.id === sid);
  if (!sess || !sess.projectId) return;
  delete sess.projectId;
  saveSessions();
  renderSidebar();
  renderMessages();
  showToast('已移出项目');
}

function buildFriendItem(f) {
  const div = document.createElement('div');
  div.className = 'friend-item' + (f.id === state.friendCurrentId ? ' active' : '');
  div.dataset.fid = f.id;
  div.dataset.tip = f.name;
  const icon = document.createElement('span');
  icon.className = 'icon line-icon';
  icon.innerHTML = lineIcon('user');
  const body = document.createElement('div');
  body.className = 'group-body';
  const name = document.createElement('span');
  name.className = 'name';
  name.textContent = f.name;
  name.title = '\u53cc\u51fb\u91cd\u547d\u540d';
  name.ondblclick = (e) => { e.stopPropagation(); startRenameFriend(f.id, name); };
  const meta = document.createElement('span');
  meta.className = 'group-meta';
  meta.textContent = f.lastMessage || '\u597d\u53cb\u521a\u521a\u6dfb\u52a0';
  body.appendChild(name);
  body.appendChild(meta);
  div.addEventListener('mouseenter', () => showSessionTip(div));
  div.addEventListener('mouseleave', hideSessionTip);
  const more = document.createElement('button');
  more.className = 'more-btn';
  more.title = '\u66f4\u591a';
  more.textContent = '\u22ef';
  more.onclick = (e) => { e.stopPropagation(); toggleFriendMenu(e, f.id); };
  div.appendChild(icon);
  div.appendChild(body);
  div.appendChild(more);
  div.onclick = () => {
    state.friendCurrentId = f.id;
    if (f.source === 'company') CompanyRuntime.openContact(f, 'friend');
    renderSidebar();
  };
  return div;
}

function startRenameFriend(fid, nameEl) {
  if (renamingFriendId) return;
  renamingFriendId = fid;
  const original = nameEl.textContent;
  const input = document.createElement('input');
  input.type = 'text';
  input.className = 'rename-input';
  input.value = original;
  input.maxLength = 30;
  nameEl.replaceWith(input);
  input.focus();
  input.select();
  let done = false;
  const finish = (save) => {
    if (done) return;
    done = true;
    renamingFriendId = null;
    if (save) { saveFriendRename(fid, input.value.trim(), original); }
    else {
      const span = document.createElement('span');
      span.className = 'name';
      span.textContent = original;
      span.title = '\u53cc\u51fb\u91cd\u547d\u540d';
      span.ondblclick = (e) => { e.stopPropagation(); startRenameFriend(fid, span); };
      input.replaceWith(span);
    }
  };
  input.addEventListener('keydown', (e) => {
    if (e.key === 'Enter') { e.preventDefault(); finish(true); }
    else if (e.key === 'Escape') { e.preventDefault(); finish(false); }
  });
  input.addEventListener('blur', () => finish(true));
}

function saveFriendRename(fid, newName, original) {
  const f = state.friends.find(x => x.id === fid);
  if (f && newName && newName !== original) { f.name = newName; saveFriends(); showToast('\u5df2\u91cd\u547d\u540d'); }
  renderSidebar();
}

async function addFriend() {
  const raw = await askNameDialog({
    title: '添加好友',
    placeholder: '输入好友名字',
    confirmText: '添加',
    maxLength: 30
  });
  if (!raw) return;
  const name = raw.trim();
  if (!name) return;
  if (!Array.isArray(state.friends)) state.friends = [];
  if (state.friends.some(f => f.name === name)) { showToast('\u5df2\u6709\u540c\u540d\u597d\u53cb'); return; }
  state.friends.unshift({ id: 'f_' + Date.now() + '_' + Math.random().toString(36).slice(2, 6), name: name, addedAt: Date.now() });
  localStorage.setItem('mc-friends-collapsed', '0');
  saveFriends();
  renderSidebar();
  showToast('\u5df2\u6dfb\u52a0\u597d\u53cb');
}

async function removeFriend(fid) {
  const f = state.friends.find(x => x.id === fid);
  if (!f) return;
  if (!confirm('\u5220\u9664\u597d\u53cb\u300c' + f.name + '\u300d\uff1f')) return;
  if (f.source === 'company') {
    try { await CatsCompanyApi.removeFriend(f.remoteId); }
    catch (error) { showToast('\u5220\u9664\u516c\u53f8\u597d\u53cb\u5931\u8d25'); return; }
  }
  state.friends = state.friends.filter(x => x.id !== fid);
  if (state.friendCurrentId === fid) state.friendCurrentId = null;
  saveFriends();
  renderSidebar();
  showToast('\u5df2\u5220\u9664');
}

async function blockFriend(fid) {
  const friend = state.friends.find(item => item.id === fid);
  if (!friend) return;
  if (!confirm('\u62c9\u9ed1\u597d\u53cb\u300c' + friend.name + '\u300d\uff1f\u62c9\u9ed1\u540e\u5c06\u4e0d\u518d\u63a5\u6536\u5bf9\u65b9\u6d88\u606f\u3002')) return;

  if (friend.source === 'company') {
    try {
      await CatsCompanyApi.blockUser(friend.remoteId);
    } catch (error) {
      showToast(AppErrors.userMessage(error, '\u62c9\u9ed1\u597d\u53cb\u5931\u8d25'));
      return;
    }
  }

  state.friends = state.friends.filter(item => item.id !== fid);
  if (state.friendCurrentId === fid) state.friendCurrentId = null;
  saveFriends();
  renderSidebar();
  showToast('\u5df2\u62c9\u9ed1\u597d\u53cb');
}

function loadFriends() {
  try {
    const raw = localStorage.getItem('mc-friends');
    const arr = raw ? JSON.parse(raw) : [];
    state.friends = Array.isArray(arr) ? arr : [];
  } catch (e) { state.friends = []; }
}

function saveFriends() {
  try { localStorage.setItem('mc-friends', JSON.stringify(state.friends || [])); } catch (e) {}
}

let renamingGroupId = null;
let renamingFriendId = null;
let renamingProjectId = null;
let renamingAgentId = null;

let _sessionTipEl = null;

function showSessionTip(divEl) {

  const sidebar = document.getElementById('sidebar');

  if (sidebar?.classList.contains('open')) {

    hideSessionTip();

    return;

  }

  if (!_sessionTipEl) {

    _sessionTipEl = document.createElement('div');

    _sessionTipEl.className = 'session-item-tip';

    document.body.appendChild(_sessionTipEl);

  }

  const text = divEl.dataset.tip || '';

  if (!text) return;

  _sessionTipEl.textContent = text;

  const rect = divEl.getBoundingClientRect();

  _sessionTipEl.style.visibility = 'hidden';

  _sessionTipEl.classList.add('show');

  requestAnimationFrame(() => {

    const tipRect = _sessionTipEl.getBoundingClientRect();

    let top = rect.top + rect.height / 2 - tipRect.height / 2;

    let left = rect.right + 10;

    if (top < 8) top = 8;

    if (top + tipRect.height > window.innerHeight - 8) top = window.innerHeight - tipRect.height - 8;

    if (left + tipRect.width > window.innerWidth - 8) left = rect.left - tipRect.width - 10;

    _sessionTipEl.style.top = top + 'px';

    _sessionTipEl.style.left = left + 'px';

    _sessionTipEl.style.visibility = 'visible';

  });

}

function hideSessionTip() {

  if (_sessionTipEl) _sessionTipEl.classList.remove('show');

}

function renderSidebar() {

  if (window.SidebarComponent) return SidebarComponent.render({
    state,
    buildSessionItem,
    buildGroupItem,
    buildAgentItem,
    buildFriendItem,
    buildProjectItem,
    renderAgentPicker,
  });

  return renderSidebarLegacy();
}

function renderSidebarLegacy() {

  const pinnedSection = document.querySelector('.pinned-section');

  const pinnedList = document.getElementById('pinnedList');

  const history = document.getElementById('history');

  if (pinnedList) pinnedList.innerHTML = '';

  if (history) history.innerHTML = '';

  const projectIds = new Set((state.projects || []).map(project => project.id));

  // Project conversations belong to their project tree, not the global chat lists.
  // Keep orphaned conversations visible so stale project data cannot hide them.
  const visibleSessions = (state.sessions || []).filter(session => (
    !session.projectId || !projectIds.has(session.projectId)
  ));

  const historyTitle = document.querySelector('#historyToggle span');
  if (historyTitle) historyTitle.textContent = '历史任务';

  const pinned = visibleSessions.filter(s => s.pinned);

  const pinnedGroups = (state.groups || []).filter(group => group.pinned);

  const unpinned = visibleSessions.filter(s => !s.pinned);

  if (pinnedSection) pinnedSection.style.display = (pinned.length || pinnedGroups.length) ? '' : 'none';

  if (pinnedList) {

    if (false) {

      const e = document.createElement('div');

      e.className = 'sidebar-empty';

      e.textContent = '置顶为空';

      pinnedList.appendChild(e);

    } else {

      pinned.forEach(s => pinnedList.appendChild(buildSessionItem(s)));

      pinnedGroups.forEach(group => pinnedList.appendChild(buildGroupItem(group)));

    }

  }

  if (history) {

    if (!unpinned.length) {

      const e = document.createElement('div');

      e.className = 'sidebar-empty';

      e.textContent = '还没有历史任务';

      history.appendChild(e);

    } else {

      unpinned.forEach(s => history.appendChild(buildSessionItem(s)));

    }

  }

  const groupsList = document.getElementById('groupsList');
  const agentsList = document.getElementById('agentsList');
  if (agentsList) agentsList.innerHTML = '';
  const agents = Array.isArray(state.agents) ? state.agents : [];
  const agentsToggle = document.getElementById('agentsToggle');
  if (agentsToggle) agentsToggle.classList.toggle('empty-section', !agents.length);
  if (agentsList) {
    if (!agents.length) {
      const e = document.createElement('div');
      e.className = 'sidebar-empty';
      e.textContent = '';
      agentsList.appendChild(e);
    } else {
      agents.forEach(agent => agentsList.appendChild(buildAgentItem(agent)));
    }
  }

  const agentsCollapsed = localStorage.getItem('mc-agents-collapsed') !== '0';

  document.getElementById('agentsToggle')?.classList.toggle('collapsed', agentsCollapsed || !agents.length);

  document.getElementById('agentsList')?.classList.toggle('collapsed', agentsCollapsed || !agents.length);

  if (groupsList) groupsList.innerHTML = '';
  const allGroups = Array.isArray(state.groups) ? state.groups : [];
  const groups = allGroups.filter(group => !group.pinned);
  const groupsToggle = document.getElementById('groupsToggle');
  if (groupsToggle) groupsToggle.classList.toggle('empty-section', !groups.length);
  if (groupsList) {
    if (!groups.length) {
      const e = document.createElement('div');
      e.className = 'sidebar-empty';
      e.textContent = '';
      groupsList.appendChild(e);
    } else {
      groups.forEach(g => groupsList.appendChild(buildGroupItem(g)));
    }
  }

  const friendsList = document.getElementById('friendsList');
  if (friendsList) friendsList.innerHTML = '';
  const friends = Array.isArray(state.friends) ? state.friends : [];
  const friendsToggle = document.getElementById('friendsToggle');
  if (friendsToggle) friendsToggle.classList.toggle('empty-section', !friends.length);
  if (friendsList) {
    if (!friends.length) {
      const e = document.createElement('div');
      e.className = 'sidebar-empty';
      e.textContent = '';
      friendsList.appendChild(e);
    } else {
      friends.forEach(f => friendsList.appendChild(buildFriendItem(f)));
    }
  }

  const projectsList = document.getElementById('projectsList');
  if (projectsList) projectsList.innerHTML = '';
  const projects = Array.isArray(state.projects) ? state.projects : [];
  const projectsToggle = document.getElementById('projectsToggle');
  if (projectsToggle) projectsToggle.classList.toggle('empty-section', !projects.length);
  if (projectsList) {
    if (!projects.length) {
      const e = document.createElement('div');
      e.className = 'sidebar-empty';
      e.textContent = '';
      projectsList.appendChild(e);
    } else {
      projects.forEach(p => projectsList.appendChild(buildProjectItem(p)));
    }
  }

  const historyCollapsed = localStorage.getItem('mc-history-collapsed') === '1';

  document.getElementById('historyToggle')?.classList.toggle('collapsed', historyCollapsed);

  document.getElementById('history')?.classList.toggle('collapsed', historyCollapsed);

  const projectsCollapsed = localStorage.getItem('mc-projects-collapsed') === '1';

  document.getElementById('projectsToggle')?.classList.toggle('collapsed', projectsCollapsed || !projects.length);

  document.getElementById('projectsList')?.classList.toggle('collapsed', projectsCollapsed || !projects.length);

  const groupsCollapsed = localStorage.getItem('mc-groups-collapsed') !== '0';

  document.getElementById('groupsToggle')?.classList.toggle('collapsed', groupsCollapsed || !groups.length);

  document.getElementById('groupsList')?.classList.toggle('collapsed', groupsCollapsed || !groups.length);

  const friendsCollapsed = localStorage.getItem('mc-friends-collapsed') !== '0';

  document.getElementById('friendsToggle')?.classList.toggle('collapsed', friendsCollapsed || !friends.length);

  document.getElementById('friendsList')?.classList.toggle('collapsed', friendsCollapsed || !friends.length);

  renderAgentPicker();

}

function togglePin(sid) {

  const sess = state.sessions.find(s => s.id === sid);

  if (!sess) return;

  sess.pinned = !sess.pinned;

  saveSessions();

  renderSidebar();

  showToast(sess.pinned ? '已置顶' : '已取消置顶');

}

function toggleSessionMenu(event, sid) {

  event.stopPropagation();

  closeAllMenus();

  const sess = state.sessions.find(s => s.id === sid);

  if (!sess) return;

  const menu = document.createElement('div');

  menu.className = 'session-menu';

  const projectAction = sess.projectId
    ? '<button data-action="removeProject">移出项目</button>'
    : '<button data-action="moveProject">加入项目</button>';
  menu.innerHTML = '<button data-action="rename">重命名</button><button data-action="delete">删除对话</button>' + projectAction + '<button data-action="share">分享对话</button><button data-action="qrcode">手机扫码</button>';

  document.body.appendChild(menu);

  const anchor = event.currentTarget.closest('.session') || event.currentTarget;

  const rect = anchor.getBoundingClientRect();

  const menuRect = menu.getBoundingClientRect();

  let top = rect.bottom + 4;

  let left = rect.right - menuRect.width;

  if (left + menuRect.width > window.innerWidth - 8) left = window.innerWidth - menuRect.width - 8;

  if (left < 8) left = 8;

  if (top + menuRect.height > window.innerHeight - 8) top = rect.top - menuRect.height - 4;

  menu.style.top = top + 'px';

  menu.style.left = left + 'px';

  menu.querySelectorAll('button').forEach(btn => {

    btn.onclick = (e) => {

      e.stopPropagation();

      const action = btn.dataset.action;

      menu.remove();

      if (action === 'delete') deleteSession(sid);
      else if (action === 'rename') {
        const t = document.querySelector('.session[data-sid=' + sid + '] .title');
        if (t) startRename(sid, t);
      }

      else if (action === 'share') shareSession(sess);

      else if (action === 'qrcode') showQrCode(sess);

      else if (action === 'moveProject') moveSessionToProject(sid);

      else if (action === 'removeProject') removeSessionFromProject(sid);

    };

  });

}

function toggleGroupMenu(event, gid) {

  event.stopPropagation();

  closeAllMenus();

  const group = state.groups.find(g => g.id === gid);

  if (!group) return;

  const menu = document.createElement('div');

  menu.className = 'session-menu';

  menu.innerHTML = '<button data-action="manage">管理群聊</button><button data-action="rename">重命名</button><button data-action="delete">删除群聊</button><button data-action="share">分享群聊</button><button data-action="qrcode">手机扫码</button>';

  document.body.appendChild(menu);

  const anchor = event.currentTarget.closest('.group-item') || event.currentTarget;

  const rect = anchor.getBoundingClientRect();

  const menuRect = menu.getBoundingClientRect();

  let top = rect.bottom + 4;

  let left = rect.right - menuRect.width;

  if (left + menuRect.width > window.innerWidth - 8) left = window.innerWidth - menuRect.width - 8;

  if (left < 8) left = 8;

  if (top + menuRect.height > window.innerHeight - 8) top = rect.top - menuRect.height - 4;

  menu.style.top = top + 'px';

  menu.style.left = left + 'px';

  menu.querySelectorAll('button').forEach(btn => {

    btn.onclick = (e) => {

      e.stopPropagation();

      const action = btn.dataset.action;

      menu.remove();

      if (action === 'manage') {
        if (window.CollaborationManager) window.CollaborationManager.openGroup(group);
        else showToast('群聊管理组件未加载');
      }
      else if (action === 'delete') removeGroup(gid);
      else if (action === 'rename') {
        const t = document.querySelector('.group-item[data-gid=' + gid + '] .name');
        if (t) startRenameGroup(gid, t);
      }
      else if (action === 'share') shareGroup(group);
      else if (action === 'qrcode') showQrCode({ id: group.id, title: group.name });

    };

  });

}

function toggleFriendMenu(event, fid) {

  event.stopPropagation();

  closeAllMenus();

  const friend = state.friends.find(f => f.id === fid);

  if (!friend) return;

  const menu = document.createElement('div');

  menu.className = 'session-menu';

  menu.innerHTML = '<button data-action="rename">\u91cd\u547d\u540d\u597d\u53cb</button><button data-action="block">\u62c9\u9ed1\u597d\u53cb</button><button data-action="delete">\u5220\u9664\u597d\u53cb</button>';

  document.body.appendChild(menu);

  const anchor = event.currentTarget.closest('.friend-item') || event.currentTarget;

  const rect = anchor.getBoundingClientRect();

  const menuRect = menu.getBoundingClientRect();

  let top = rect.bottom + 4;

  let left = rect.right - menuRect.width;

  if (left + menuRect.width > window.innerWidth - 8) left = window.innerWidth - menuRect.width - 8;

  if (left < 8) left = 8;

  if (top + menuRect.height > window.innerHeight - 8) top = rect.top - menuRect.height - 4;

  menu.style.top = top + 'px';

  menu.style.left = left + 'px';

  menu.querySelectorAll('button').forEach(btn => {

    btn.onclick = (e) => {

      e.stopPropagation();

      const action = btn.dataset.action;

      menu.remove();

      if (action === 'delete') removeFriend(fid);
      else if (action === 'block') blockFriend(fid);
      else if (action === 'rename') {
        const t = document.querySelector('.friend-item[data-fid=' + fid + '] .name');
        if (t) startRenameFriend(fid, t);
      }

    };

  });

}

function toggleAgentMenu(event, aid) {

  event.stopPropagation();

  closeAllMenus();

  const agent = state.agents.find(a => a.id === aid);

  if (!agent) return;

  const menu = document.createElement('div');

  menu.className = 'session-menu';

  menu.innerHTML = '<button data-action="rename">\u91cd\u547d\u540d</button><button data-action="delete">\u5220\u9664 Agent</button>';

  document.body.appendChild(menu);

  const anchor = event.currentTarget.closest('.agent-item') || event.currentTarget;

  const rect = anchor.getBoundingClientRect();

  const menuRect = menu.getBoundingClientRect();

  let top = rect.bottom + 4;

  let left = rect.right - menuRect.width;

  if (left + menuRect.width > window.innerWidth - 8) left = window.innerWidth - menuRect.width - 8;

  if (left < 8) left = 8;

  if (top + menuRect.height > window.innerHeight - 8) top = rect.top - menuRect.height - 4;

  menu.style.top = top + 'px';

  menu.style.left = left + 'px';

  menu.querySelectorAll('button').forEach(btn => {

    btn.onclick = (e) => {

      e.stopPropagation();

      const action = btn.dataset.action;

      menu.remove();

      if (action === 'rename') {
        const t = document.querySelector('.agent-item[data-aid=' + aid + '] .name');
        if (t) startRenameAgent(aid, t);
      } else if (action === 'delete') {
        removeAgent(aid);
      }

    };

  });

}

function toggleProjectMenu(event, pid) {

  event.stopPropagation();

  closeAllMenus();

  const project = state.projects.find(p => p.id === pid);

  if (!project) return;

  const menu = document.createElement('div');

  menu.className = 'session-menu';

  menu.innerHTML = '<button data-action="rename">重命名项目</button><button data-action="delete">删除项目</button>';

  document.body.appendChild(menu);

  const anchor = event.currentTarget.closest('.project-item') || event.currentTarget;

  const rect = anchor.getBoundingClientRect();

  const menuRect = menu.getBoundingClientRect();

  let top = rect.bottom + 4;

  let left = rect.right - menuRect.width;

  if (left + menuRect.width > window.innerWidth - 8) left = window.innerWidth - menuRect.width - 8;

  if (left < 8) left = 8;

  if (top + menuRect.height > window.innerHeight - 8) top = rect.top - menuRect.height - 4;

  menu.style.top = top + 'px';

  menu.style.left = left + 'px';

  menu.querySelectorAll('button').forEach(btn => {

    btn.onclick = (e) => {

      e.stopPropagation();

      const action = btn.dataset.action;

      menu.remove();

      if (action === 'rename') {
        const t = document.querySelector('.project-item[data-pid=' + pid + '] .name');
        if (t) startRenameProject(pid, t);
      } else if (action === 'delete') {
        removeProject(pid);
      }

    };

  });

}

function iconSvg(name) {

  const icons = {
    bug: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M8 2l1.6 2.2M16 2l-1.6 2.2"/><path d="M6.5 8.5h11"/><path d="M7 12H3M21 12h-4"/><path d="M7.6 17.5L4.5 20M19.5 20l-3.1-2.5"/><path d="M8 8a4 4 0 0 1 8 0v7a4 4 0 0 1-8 0V8Z"/></svg>',
    download: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round"><path d="M12 3v12"/><path d="M7 10l5 5 5-5"/><path d="M5 21h14"/></svg>',
    laptop: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><rect x="5" y="4" width="14" height="11" rx="1.5"/><path d="M3 20h18l-2-4H5l-2 4Z"/></svg>',
    book: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M4 5.5A2.5 2.5 0 0 1 6.5 3H20v16H6.5A2.5 2.5 0 0 0 4 21V5.5Z"/><path d="M4 5.5A2.5 2.5 0 0 1 6.5 3H20"/><path d="M12 3v16"/></svg>',
    copy: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><rect x="8" y="8" width="11" height="11" rx="2"/><path d="M5 16H4a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/></svg>',
    key: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><circle cx="8" cy="15" r="4"/><path d="M11 12l8-8"/><path d="M16 7l2 2"/><path d="M14 9l2 2"/></svg>',
    settings: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M12 15.5A3.5 3.5 0 1 0 12 8a3.5 3.5 0 0 0 0 7.5Z"/><path d="M19.4 15a1.7 1.7 0 0 0 .34 1.88l.04.04a2 2 0 0 1-2.83 2.83l-.04-.04a1.7 1.7 0 0 0-1.88-.34 1.7 1.7 0 0 0-1.03 1.56V21a2 2 0 0 1-4 0v-.07A1.7 1.7 0 0 0 8.97 19.37a1.7 1.7 0 0 0-1.88.34l-.04.04a2 2 0 0 1-2.83-2.83l.04-.04A1.7 1.7 0 0 0 4.6 15a1.7 1.7 0 0 0-1.56-1.03H3a2 2 0 0 1 0-4h.07A1.7 1.7 0 0 0 4.6 8.97a1.7 1.7 0 0 0-.34-1.88l-.04-.04a2 2 0 0 1 2.83-2.83l.04.04a1.7 1.7 0 0 0 1.88.34H9A1.7 1.7 0 0 0 10 3.07V3a2 2 0 0 1 4 0v.07a1.7 1.7 0 0 0 1.03 1.56 1.7 1.7 0 0 0 1.88-.34l.04-.04a2 2 0 0 1 2.83 2.83l-.04.04a1.7 1.7 0 0 0-.34 1.88v.03A1.7 1.7 0 0 0 20.93 10H21a2 2 0 0 1 0 4h-.07A1.7 1.7 0 0 0 19.4 15Z"/></svg>',
    logout: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round"><path d="M14 8V5a2 2 0 0 0-2-2H5a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h7a2 2 0 0 0 2-2v-3"/><path d="M9 12h12"/><path d="M17 8l4 4-4 4"/></svg>'
  };

  return icons[name] || '';

}

function toggleAccountMenu(event) {

  event.stopPropagation();

  const existing = document.querySelector('.account-menu');

  closeAllMenus();

  if (existing) return;

  const menu = document.createElement('div');

  menu.className = 'account-menu';

  let items = [
    ['bug', '意见反馈', 'feedback'],
    ['download', '下载 CatsCo 桌面端', 'download'],
    ['laptop', '连接我的电脑助手', 'assistant'],
    ['book', '示例任务', 'samples'],
    ['key', 'CatsCo 中转站', 'relay'],
    ['settings', '设置与资料', 'profile'],
    ['logout', '退出登录', 'logout', true]
  ];

  items.unshift([
    'laptop',
    CatsCompanyApi.isAuthenticated() ? '\u516c\u53f8\u8d26\u53f7\u5df2\u8fde\u63a5' : '\u8fde\u63a5\u516c\u53f8\u8d26\u53f7',
    'company'
  ]);

  items = items.filter(item => item[2] !== 'samples');

  menu.innerHTML = items.map((item, index) => {
    const divider = index === items.length - 1 ? '<div class="account-menu-divider"></div>' : '';
    return divider + '<button type="button" class="' + (item[3] ? 'danger' : '') + '" data-action="' + item[2] + '"><span class="menu-icon" aria-hidden="true">' + iconSvg(item[0]) + '</span><span>' + item[1] + '</span></button>';
  }).join('');

  document.body.appendChild(menu);

  const rect = event.currentTarget.getBoundingClientRect();

  const menuRect = menu.getBoundingClientRect();

  let left = rect.right - menuRect.width;
  let top = rect.top - menuRect.height - 10;

  if (left < 8) left = 8;
  if (left + menuRect.width > window.innerWidth - 8) left = window.innerWidth - menuRect.width - 8;
  if (top < 8) top = rect.bottom + 10;

  menu.style.left = left + 'px';
  menu.style.top = top + 'px';

  menu.querySelectorAll('button').forEach(btn => {
    btn.onclick = (e) => {
      e.stopPropagation();
      const action = btn.dataset.action;
      menu.remove();
      if (typeof openSettingsPanel === 'function') {
        openSettingsPanel(action);
      } else if (action === 'samples') {
        const welcome = document.getElementById('welcome');
        if (welcome) welcome.scrollIntoView({ behavior: 'smooth', block: 'center' });
      } else if (action === 'logout') {
        showToast('已退出登录');
      } else {
        showToast(btn.innerText.trim());
      }
    };
  });

}

function closeAllMenus() {

  document.querySelectorAll('.session-menu, .account-menu').forEach(m => m.remove());

}

async function shareSession(sess) {

  const text = '对话: ' + sess.title + '\n\n' + sess.messages.map(m => '[' + (m.role === 'user' ? '我' : 'AI') + ']\n' + m.content).join('\n\n');

  if (navigator.share) {

    try { await navigator.share({ title: sess.title, text }); } catch (e) {}

  } else {

    navigator.clipboard.writeText(text).then(() => showToast('已复制'));

  }

}

async function shareGroup(group) {

  const text = '群聊: ' + group.name + '\n\n' + (group.lastMessage || '暂无消息');

  if (navigator.share) {

    try { await navigator.share({ title: group.name, text }); } catch (e) {}

  } else {

    await navigator.clipboard.writeText(text);

    showToast('群聊内容已复制');

  }

}

function showQrCode(sess) {

  const text = 'MiniChat:' + sess.id + ':' + sess.title;

  const url = 'https://api.qrserver.com/v1/create-qr-code/?size=240x240&data=' + encodeURIComponent(text);

  const overlay = document.createElement('div');

  overlay.className = 'qr-overlay';

  overlay.innerHTML = '<div class="qr-box"><div class="qr-title">扫码分享</div><img src="' + url + '" alt="QR" /><div class="qr-text">用手机扫码打开这个对话</div><button class="qr-close">关闭</button></div>';

  document.body.appendChild(overlay);

  overlay.onclick = (e) => { if (e.target === overlay || e.target.classList.contains('qr-close')) overlay.remove(); };

}

function askFriendDialog() {
  return new Promise(resolve => {
    document.querySelector('.name-dialog-overlay')?.remove();
    const overlay = document.createElement('div');
    overlay.className = 'name-dialog-overlay';
    overlay.innerHTML =
      '<div class="name-dialog friend-card-dialog" role="dialog" aria-modal="true" aria-label="\u6dfb\u52a0\u597d\u53cb">'
      + '<div class="name-dialog-head"><h2>\u6dfb\u52a0\u597d\u53cb</h2><button class="name-dialog-close" type="button" aria-label="\u5173\u95ed">\u00d7</button></div>'
      + '<div class="friend-card-tabs" role="tablist">'
      + '<button class="active" type="button" data-mode="name">\u6309\u540d\u5b57</button>'
      + '<button type="button" data-mode="uid">\u6309 UID</button>'
      + '</div>'
      + '<div class="friend-card-search">'
      + '<input class="friend-card-query" type="text" maxlength="30" placeholder="\u641c\u7d22\u8054\u7cfb\u4eba">'
      + '<button class="friend-card-submit" type="button">\u641c\u7d22</button>'
      + '</div>'
      + '<textarea class="friend-card-note" maxlength="80" rows="2">\u4f60\u597d\uff0c\u6211\u662f Cycren</textarea>'
      + '</div>';
    document.body.appendChild(overlay);

    let mode = 'name';
    const query = overlay.querySelector('.friend-card-query');
    const note = overlay.querySelector('.friend-card-note');
    const finish = (value) => {
      overlay.remove();
      resolve(value);
    };

    overlay.querySelectorAll('.friend-card-tabs button').forEach(btn => {
      btn.onclick = () => {
        mode = btn.dataset.mode || 'name';
        overlay.querySelectorAll('.friend-card-tabs button').forEach(b => b.classList.toggle('active', b === btn));
        query.placeholder = mode === 'uid' ? '\u8f93\u5165 UID' : '\u641c\u7d22\u8054\u7cfb\u4eba';
        query.focus();
      };
    });

    overlay.querySelector('.name-dialog-close').onclick = () => finish(null);
    overlay.querySelector('.friend-card-submit').onclick = () => {
      const name = query.value.trim();
      if (!name) { query.focus(); return; }
      finish({ mode, name, note: note.value.trim() });
    };
    overlay.onclick = (e) => { if (e.target === overlay) finish(null); };
    overlay.onkeydown = (e) => {
      if (e.key === 'Escape') { e.preventDefault(); finish(null); }
      else if (e.key === 'Enter' && e.target === query) {
        e.preventDefault();
        overlay.querySelector('.friend-card-submit').click();
      }
    };
    setTimeout(() => query.focus(), 0);
  });
}

function askGroupDialog() {
  return new Promise(resolve => {
    document.querySelector('.name-dialog-overlay')?.remove();
    const friends = (Array.isArray(state.friends) ? state.friends : []).map(friend => ({
      ...friend,
      type: 'friend',
      color: friend.color || '#64748b',
    }));
    const agents = (Array.isArray(state.agents) ? state.agents : []).map(agent => ({
      ...agent,
      type: 'agent',
      color: agent.color || '#188567',
    }));
    const candidates = [...friends, ...agents].filter(item => item.id && item.name);
    const overlay = document.createElement('div');
    overlay.className = 'name-dialog-overlay';
    overlay.innerHTML =
      '<div class="name-dialog group-card-dialog" role="dialog" aria-modal="true" aria-label="\u521b\u5efa\u7fa4\u804a">'
      + '<div class="name-dialog-head"><h2>\u521b\u5efa\u7fa4\u804a</h2><button class="name-dialog-close" type="button" aria-label="\u5173\u95ed">\u00d7</button></div>'
      + '<div class="group-card-intro"><h3>\u7fa4\u804a\u4fe1\u606f</h3><p>\u8bbe\u7f6e\u540d\u79f0\uff0c\u5e76\u4ece\u597d\u53cb\u6216 Agent \u4e2d\u9009\u62e9\u6210\u5458\u3002</p></div>'
      + '<label class="group-card-field"><span>\u7fa4\u804a\u540d\u79f0</span><input class="group-card-name" type="text" maxlength="30" placeholder="#\u65b0\u7684\u8bdd\u9898"></label>'
      + '<div class="group-invite-picker group-create-picker">'
      + '<section class="group-invite-source">'
      + '<label class="group-invite-search"><span>\u2315</span><input class="group-create-search" type="search" placeholder="\u641c\u7d22\u6210\u5458"></label>'
      + '<div class="group-invite-tabs" role="tablist"><button class="active" type="button" data-type="friend">\u597d\u53cb</button><button type="button" data-type="agent">Agent</button></div>'
      + '<div class="group-invite-candidates group-card-member-list"></div>'
      + '</section>'
      + '<section class="group-invite-selected">'
      + '<div class="group-invite-selected-head"><strong>\u5df2\u9009\u6210\u5458</strong><span class="group-card-count">0 \u4eba</span></div>'
      + '<div class="group-invite-selected-list group-card-selected-list"></div>'
      + '</section>'
      + '</div>'
      + '<div class="name-dialog-actions">'
      + '<button class="name-dialog-cancel" type="button">\u53d6\u6d88</button>'
      + '<button class="name-dialog-confirm" type="button">\u521b\u5efa</button>'
      + '</div>'
      + '</div>';
    document.body.appendChild(overlay);

    const list = overlay.querySelector('.group-card-member-list');
    const selectedList = overlay.querySelector('.group-card-selected-list');
    const searchInput = overlay.querySelector('.group-create-search');
    const selected = new Map();
    const nameInput = overlay.querySelector('.group-card-name');
    const countEl = overlay.querySelector('.group-card-count');
    const initials = (name) => (name || '?').trim().slice(0, 1).toUpperCase();
    let activeType = 'friend';

    const candidateKey = item => item.type + ':' + item.id;
    const getDetail = item => item.type === 'agent'
      ? (item.description || item.position || 'Agent \u52a9\u624b')
      : (item.lastMessage || '\u597d\u53cb');
    const renderSelected = () => {
      selectedList.innerHTML = '';
      countEl.textContent = selected.size + ' \u4eba';
      if (!selected.size) {
        selectedList.innerHTML = '<div class="collaboration-empty compact"><strong>\u5c1a\u672a\u9009\u62e9\u6210\u5458</strong><span>\u4ece\u5de6\u4fa7\u5217\u8868\u4e2d\u6dfb\u52a0</span></div>';
        return;
      }
      selected.forEach((item, key) => {
        const row = document.createElement('div');
        row.className = 'group-invite-selected-row';
        row.innerHTML =
          '<span class="collaboration-avatar" style="--avatar-color:' + escapeHtml(item.color || '#64748b') + '">' + escapeHtml(initials(item.name)) + '</span>'
          + '<div><strong>' + escapeHtml(item.name) + '</strong><span>' + (item.type === 'agent' ? 'Agent' : '\u597d\u53cb') + '</span></div>'
          + '<button type="button" aria-label="\u79fb\u9664 ' + escapeHtml(item.name) + '">\u00d7</button>';
        row.querySelector('button').onclick = () => {
          selected.delete(key);
          renderCandidates();
          renderSelected();
        };
        selectedList.appendChild(row);
      });
    };
    const addOption = (item) => {
      const key = candidateKey(item);
      const row = document.createElement('label');
      row.className = 'group-invite-candidate';
      row.innerHTML =
        '<input type="checkbox" ' + (selected.has(key) ? 'checked' : '') + '>'
        + '<span class="collaboration-avatar" style="--avatar-color:' + escapeHtml(item.color || '#64748b') + '">' + escapeHtml(initials(item.name)) + '</span>'
        + '<span class="group-invite-candidate-copy"><strong>' + escapeHtml(item.name) + '</strong><small>' + escapeHtml(getDetail(item)) + '</small></span>';
      const checkbox = row.querySelector('input');
      checkbox.onchange = () => {
        if (checkbox.checked) selected.set(key, item);
        else selected.delete(key);
        renderSelected();
      };
      list.appendChild(row);
    };
    const renderCandidates = () => {
      const query = searchInput.value.trim().toLowerCase();
      const visible = candidates.filter(item => item.type === activeType && (!query || item.name.toLowerCase().includes(query)));
      list.innerHTML = '';
      if (!visible.length) {
        list.innerHTML = '<div class="collaboration-empty compact"><strong>' + (query ? '\u6ca1\u6709\u5339\u914d\u7684\u6210\u5458' : activeType === 'agent' ? '\u6682\u65e0 Agent' : '\u6682\u65e0\u597d\u53cb') + '</strong><span>' + (query ? '\u8bf7\u5c1d\u8bd5\u5176\u4ed6\u5173\u952e\u8bcd' : '\u6dfb\u52a0\u540e\u5c06\u663e\u793a\u5728\u8fd9\u91cc') + '</span></div>';
        return;
      }
      visible.forEach(addOption);
    };

    overlay.querySelectorAll('.group-invite-tabs button').forEach(button => {
      button.onclick = () => {
        activeType = button.dataset.type || 'friend';
        overlay.querySelectorAll('.group-invite-tabs button').forEach(item => item.classList.toggle('active', item === button));
        renderCandidates();
      };
    });
    searchInput.oninput = renderCandidates;
    renderCandidates();
    renderSelected();

    const finish = (value) => {
      overlay.remove();
      resolve(value);
    };
    overlay.querySelector('.name-dialog-close').onclick = () => finish(null);
    overlay.querySelector('.name-dialog-cancel').onclick = () => finish(null);
    overlay.querySelector('.name-dialog-confirm').onclick = () => {
      const groupName = nameInput.value.trim() || '#\u65b0\u7684\u8bdd\u9898';
      finish({ name: groupName, members: Array.from(selected.values(), item => item.id) });
    };
    overlay.onclick = (e) => { if (e.target === overlay) finish(null); };
    overlay.onkeydown = (e) => {
      if (e.key === 'Escape') { e.preventDefault(); finish(null); }
      else if (e.key === 'Enter' && e.target === nameInput) {
        e.preventDefault();
        overlay.querySelector('.name-dialog-confirm').click();
      }
    };
    setTimeout(() => nameInput.focus(), 0);
  });
}

async function addFriend() {
  if (window.CollaborationManager) {
    CollaborationManager.openFriends('add');
    return;
  }
  const result = await askFriendDialog();
  if (!result) return;
  const name = result.name.trim();
  if (!name) return;
  if (CatsCompanyApi.isAuthenticated()) {
    try {
      const search = await CatsCompanyApi.searchUsers(name, result.mode || 'name');
      const user = (search.users || [])[0];
      if (!user) { showToast('\u6ca1\u6709\u627e\u5230\u5339\u914d\u7684\u7528\u6237'); return; }
      await CatsCompanyApi.sendFriendRequest(user.id, result.note || '');
      showToast('\u597d\u53cb\u8bf7\u6c42\u5df2\u53d1\u9001');
      return;
    } catch (error) {
      showToast(error.status === 409 ? '\u5df2\u662f\u597d\u53cb\u6216\u8bf7\u6c42\u5df2\u53d1\u9001' : '\u6dfb\u52a0\u597d\u53cb\u5931\u8d25');
      return;
    }
  }
  if (!Array.isArray(state.friends)) state.friends = [];
  if (state.friends.some(f => f.name === name)) { showToast('\u5df2\u6709\u540c\u540d\u597d\u53cb'); return; }
  state.friends.unshift({
    id: 'f_' + Date.now() + '_' + Math.random().toString(36).slice(2, 6),
    name,
    lastMessage: result.note || '\u597d\u53cb\u521a\u521a\u6dfb\u52a0',
    searchMode: result.mode || 'name',
    addedAt: Date.now()
  });
  localStorage.setItem('mc-friends-collapsed', '0');
  saveFriends();
  renderSidebar();
  showToast('\u5df2\u6dfb\u52a0\u597d\u53cb');
}

async function addGroup() {
  const result = await askGroupDialog();
  if (!result) return;
  const name = result.name.trim();
  if (!name) return;
  if (!Array.isArray(state.groups)) state.groups = [];
  const members = Array.isArray(result.members) ? result.members : [];
  if (CatsCompanyApi.isAuthenticated()) {
    const memberIds = members.map(id => {
      const friend = (state.friends || []).find(item => item.id === id);
      const agent = (state.agents || []).find(item => item.id === id);
      return friend?.remoteId || agent?.uid || null;
    }).filter(Boolean);
    try {
      await CatsCompanyApi.createGroup(name, memberIds);
      localStorage.setItem('mc-groups-collapsed', '0');
      await syncCompanyData({ silent: true });
      showToast('\u5df2\u521b\u5efa\u516c\u53f8\u7fa4\u804a');
    } catch (error) {
      showToast('\u521b\u5efa\u7fa4\u804a\u5931\u8d25');
    }
    return;
  }
  state.groups.unshift({
    id: 'g_' + Date.now() + '_' + Math.random().toString(36).slice(2, 6),
    name,
    members,
    lastMessage: members.length ? members.length + ' \u4f4d\u6210\u5458' : '\u7fa4\u521a\u521a\u521b\u5efa',
    lastAt: Date.now()
  });
  localStorage.setItem('mc-groups-collapsed', '0');
  saveGroups();
  renderSidebar();
  showToast('\u5df2\u521b\u5efa\u7fa4\u804a');
}
