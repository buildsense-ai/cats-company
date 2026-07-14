function saveSessions() {
  try { localStorage.setItem('mc-sessions', JSON.stringify(state.sessions)); } catch (e) {}

}

async function createBackendSession() {

  try {

    const data = await ChatApi.createSession();

    if (data.ok && data.sessionId) return data.sessionId;

  } catch (e) {}

  return null;

}

function replaceSessionId(oldId, newId) {

  if (!oldId || !newId || oldId === newId) return;

  const existing = state.sessions.find(s => s.id === newId);

  const current = state.sessions.find(s => s.id === oldId);

  if (existing && current && existing !== current) {

    existing.messages = current.messages;

    existing.title = current.title || existing.title;

    state.sessions = state.sessions.filter(s => s.id !== oldId);

  } else if (current) {

    current.id = newId;

  }

  if (state.currentId === oldId) state.currentId = newId;

  localStorage.setItem('mc-current', state.currentId);

  saveSessions();

}
function migratePinned() {

  state.sessions.forEach(s => { if (typeof s.pinned !== 'boolean') s.pinned = false; });

}

let renamingSessionId = null;

function startRename(sid, titleEl) {

  if (renamingSessionId) return;

  renamingSessionId = sid;

  const original = titleEl.textContent;

  const input = document.createElement('input');

  input.type = 'text';

  input.className = 'rename-input';

  input.value = original;

  input.maxLength = 60;

  titleEl.replaceWith(input);

  input.focus();

  input.select();

  let done = false;

  const finish = (save) => {

    if (done) return;

    done = true;

    renamingSessionId = null;

    if (save) {

      saveRename(sid, input.value.trim(), original);

    } else {

      const span = document.createElement('span');

      span.className = 'title';

      span.textContent = original;

      span.title = '双击重命名';

      span.ondblclick = (e) => { e.stopPropagation(); startRename(sid, span); };

      input.replaceWith(span);

    }

  };

  input.addEventListener('keydown', (e) => {

    if (e.key === 'Enter') { e.preventDefault(); finish(true); }

    else if (e.key === 'Escape') { e.preventDefault(); finish(false); }

  });

  input.addEventListener('blur', () => finish(true));

}

function expandHistoryList() {

  localStorage.setItem('mc-history-collapsed', '0');

  document.getElementById('historyToggle')?.classList.remove('collapsed');

  document.getElementById('history')?.classList.remove('collapsed');

}

async function saveRename(sid, newTitle, originalTitle) {

  if (!newTitle || newTitle === originalTitle) { renderSidebar(); return; }

  const session = state.sessions.find(item => item.id === sid);
  if (session?.source === 'company') {
    session.title = newTitle;
    saveSessions();
    renderSidebar();
    showToast('已在本机重命名');
    return;
  }

  try {

    const data = await ChatApi.renameSession(sid, newTitle);

    if (data.ok) {

      const s = state.sessions.find(x => x.id === sid);

      if (s) s.title = data.title;

      showToast('已重命名');

    } else showToast('重命名失败');

  } catch (err) { showToast('重命名失败: ' + err.message); }

  renderSidebar();

}

async function deleteSession(sid) {
  const sess = state.sessions.find(s => s.id === sid);

  if (!sess) return;

  if (sess.source === 'company') {
    showToast('公司后端暂不支持删除任务');
    return;
  }

  if (sess.messages.length && !confirm('删除对话「' + sess.title + '」？')) return;

  state.sessions = state.sessions.filter(s => s.id !== sid);

  if (!sid.startsWith('s_')) {

    try { await ChatApi.deleteSession(sid); } catch (e) {}

  }
  if (state.currentId === sid) {

    if (state.sessions.length) state.currentId = state.sessions[0].id;

    else newChat(false);

  }

  saveSessions();

  localStorage.setItem('mc-current', state.currentId);

  renderSidebar();

  renderMessages();

  showToast('已删除');

}

async function newChat(save = true, projectId = null) {
  const backendId = save ? await createBackendSession() : null;

  const id = backendId || ('s_' + Date.now() + '_' + Math.random().toString(36).slice(2, 6));
  const sess = { id, title: '新对话', messages: [], createdAt: Date.now() };

  if (projectId && state.projects.some(project => project.id === projectId)) {
    sess.projectId = projectId;
    setProjectOpen(projectId, true);
  }

  state.sessions.unshift(sess);

  state.currentId = id;

  state.currentProjectId = null;

  localStorage.removeItem('mc-current-project');

  if (save) { saveSessions(); localStorage.setItem('mc-current', id); }

  if (!sess.projectId) expandHistoryList();

  renderSidebar();

  renderMessages();

}

function switchSession(id) {

  if (state.streaming) { showToast('生成中，请先停止'); return; }

  const viewedSession = state.sessions.find(s => s.id === id);

  if (viewedSession) {
    collapseCompletedTaskProcesses(viewedSession);
    viewedSession.taskStateDismissedAt = Date.now();
    saveSessions();
  }

  state.currentProjectId = null;

  localStorage.removeItem('mc-current-project');

  state.currentId = id;

  localStorage.setItem('mc-current', id);

  renderSidebar();

  renderMessages();

  if (viewedSession?.source === 'company') {
    CompanyRuntime.loadSession(viewedSession).then(() => {
      if (state.currentId !== viewedSession.id) return;
      renderMessages({ preserveScroll: false });
      renderSidebar();
    }).catch(error => showToast('读取公司消息失败：' + error.message));
  }

}

function currentSession() { return state.sessions.find(s => s.id === state.currentId); }

function clearAll() {

  if (state.streaming) { showToast('生成中，请先停止'); return; }

  if (!confirm('清空所有对话历史？此操作不可恢复。')) return;

  state.sessions = [];

  newChat(false);

  saveSessions();

}

function showToast(msg) {

  const t = document.getElementById('toast');

  t.textContent = msg;

  t.classList.add('show');

  clearTimeout(showToast._t);

  showToast._t = setTimeout(() => t.classList.remove('show'), 1800);

}

function applyCompanyProfile(profile) {
  if (!profile) return;
  CompanyRuntime.setProfile(profile);
  const name = profile.display_name || profile.username || '\u6211\u5929';
  const accountName = document.querySelector('.account-name');
  const accountAvatar = document.querySelector('.account-avatar');
  if (accountName) accountName.textContent = name;
  if (accountAvatar) {
    accountAvatar.textContent = name.slice(0, 1) || '\u6211';
    accountAvatar.title = profile.email || profile.username || name;
  }
}

function clearCompanyData() {
  const removedCurrent = state.sessions.some(item => item.id === state.currentId && item.source === 'company');
  state.friends = (state.friends || []).filter(item => item.source !== 'company');
  state.groups = (state.groups || []).filter(item => item.source !== 'company');
  state.agents = (state.agents || []).filter(item => item.source !== 'company');
  state.sessions = (state.sessions || []).filter(item => item.source !== 'company');
  if (removedCurrent) state.currentId = state.sessions[0]?.id || null;
  CompanyRuntime.disconnect();
  if (!state.agents.some(item => item.id === state.agentCurrentId)) {
    state.agentCurrentId = state.agents[0]?.id || null;
  }
  saveFriends();
  saveGroups();
  saveAgents();
  saveSessions();
  renderSidebar();
}

function updateFriendRequestBadge() {
  const button = document.getElementById('friendRequestsBtn');
  const count = document.getElementById('friendRequestsCount');
  if (!button || !count) return;
  const total = (state.friendRequests || []).length;
  button.hidden = total === 0;
  count.textContent = total > 99 ? '99+' : String(total || '');
  button.setAttribute('aria-label', total ? `好友申请，${total} 条待处理` : '好友申请');
}

function applyCompanyOnlineStatus(payload) {
  const users = payload?.users || payload?.data?.users || payload?.meta?.sub || [];
  if (!Array.isArray(users)) return false;
  const statuses = new Map(users.map(user => [
    String(user.uid || user.id || ''),
    user.online === true || user.is_online === true,
  ]));
  let changed = false;
  (state.friends || []).forEach(friend => {
    if (friend.source !== 'company') return;
    const key = String(friend.remoteId || '');
    if (!statuses.has(key)) return;
    const online = statuses.get(key);
    if (friend.online !== online) {
      friend.online = online;
      changed = true;
    }
  });
  if (changed) saveFriends();
  return changed;
}

async function syncCompanyData(options = {}) {
  const silent = options.silent === true;
  if (!CatsCompanyApi.isAuthenticated()) return false;

  try {
    const profile = await CatsCompanyApi.getMe();
    localStorage.setItem('catsco-profile', JSON.stringify(profile));
    applyCompanyProfile(profile);

    const [friendsResult, groupsResult, agentsResult, pendingFriendsResult, onlineStatusResult] = await Promise.allSettled([
      CatsCompanyApi.getFriends(),
      CatsCompanyApi.getGroups(),
      CatsCompanyApi.getAgents(),
      CatsCompanyApi.getPendingFriends(),
      CatsCompanyApi.getOnlineStatus(),
    ]);

    if (pendingFriendsResult.status === 'fulfilled') {
      const payload = pendingFriendsResult.value || {};
      const body = payload.data && !Array.isArray(payload.data) ? payload.data : payload;
      const requests = body.requests || body.pending || body.items || body.friends || (Array.isArray(payload.data) ? payload.data : []);
      state.friendRequests = Array.isArray(requests) ? requests : [];
      updateFriendRequestBadge();
    }

    if (friendsResult.status === 'fulfilled') {
      const localFriends = (state.friends || []).filter(item => item.source !== 'company');
      const remoteFriends = (friendsResult.value.friends || []).map(friend => ({
        id: 'company-friend-' + friend.id,
        remoteId: friend.id,
        source: 'company',
        name: friend.display_name || friend.username || ('UID ' + friend.id),
        username: friend.username || '',
        avatarUrl: friend.avatar_url || '',
        accountType: friend.account_type,
        online: friend.online === true || friend.is_online === true,
        lastMessage: '\u516c\u53f8\u8054\u7cfb\u4eba',
      }));
      state.friends = [...localFriends, ...remoteFriends];
      if (onlineStatusResult.status === 'fulfilled') applyCompanyOnlineStatus(onlineStatusResult.value);
      saveFriends();
    }

    if (groupsResult.status === 'fulfilled') {
      const oldRemoteGroups = new Map(
        (state.groups || []).filter(item => item.source === 'company').map(item => [String(item.remoteId), item])
      );
      const localGroups = (state.groups || []).filter(item => item.source !== 'company');
      const remoteGroups = (groupsResult.value.groups || []).map(group => {
        const previous = oldRemoteGroups.get(String(group.id)) || {};
        return {
          id: 'company-group-' + group.id,
          remoteId: group.id,
          source: 'company',
          name: group.name || ('\u7fa4\u804a ' + group.id),
          avatarUrl: group.avatar_url || '',
          topicId: group.topic_id || previous.topicId || '',
          pinned: previous.pinned === true,
          lastMessage: '\u516c\u53f8\u7fa4\u804a',
        };
      });
      state.groups = [...localGroups, ...remoteGroups];
      saveGroups();
    }

    if (agentsResult.status === 'fulfilled') {
      const oldRemoteAgents = new Map(
        (state.agents || []).filter(item => item.source === 'company').map(item => [String(item.uid), item])
      );
      const localAgents = (state.agents || []).filter(item => item.source !== 'company');
      const remoteAgents = (agentsResult.value.agents || []).map(agent => {
        const uid = agent.uid || agent.id;
        const previous = oldRemoteAgents.get(String(uid)) || {};
        return {
          id: 'company-agent-' + uid,
          remoteId: agent.id,
          uid,
          source: 'company',
          name: agent.display_name || agent.username || ('Agent ' + uid),
          username: agent.username || '',
          avatarUrl: agent.avatar_url || '',
          topicId: agent.topic_id || '',
          online: agent.is_online === true,
          apiKey: previous.apiKey || '',
          position: previous.position || 'general',
          description: previous.description || (agent.relation === 'owned' ? '\u6211\u521b\u5efa\u7684\u52a9\u624b' : '\u516c\u53f8 Agent'),
        };
      });
      state.agents = [...localAgents, ...remoteAgents];
      if (!state.agentCurrentId && state.agents.length) state.agentCurrentId = state.agents[0].id;
      saveAgents();
    }

    await Promise.allSettled([
      CompanyRuntime.syncConversations(),
      CompanyRuntime.syncModels(),
    ]);
    CompanyRuntime.connect();

    renderSidebar();
    if (!silent) showToast('\u516c\u53f8\u6570\u636e\u5df2\u540c\u6b65');
    return true;
  } catch (error) {
    if (error.status === 401) {
      CatsCompanyApi.logout();
      localStorage.removeItem('catsco-profile');
      clearCompanyData();
      if (typeof AuthGate !== 'undefined') AuthGate.showLogin('\u767b\u5f55\u72b6\u6001\u5df2\u5931\u6548\uff0c\u8bf7\u91cd\u65b0\u767b\u5f55\u3002');
    }
    if (!silent) showToast('\u516c\u53f8\u670d\u52a1\u8fde\u63a5\u5931\u8d25');
    return false;
  }
}

async function checkBackend() {

  const dot = document.getElementById('statusDot');

  try {

    const d = await ChatApi.getHealth();

    state.backendOk = !!d.ok;

    if (d.ok) {

      dot.classList.remove('off');

      if (d.model) state.backendModel = d.model;
      if (!CatsCompanyApi.isAuthenticated() && typeof setAvailableModels === 'function') {
        setAvailableModels(Array.isArray(d.available_models) && d.available_models.length ? d.available_models : [d.model], d.model);
      }
      renderModelPicker();

    } else dot.classList.add('off');

  } catch (e) { state.backendOk = false; dot.classList.add('off'); }

}

document.addEventListener('DOMContentLoaded', () => {

  applyTheme();

  const toggleBtn = document.getElementById('toggleBtn');
  if (toggleBtn) toggleBtn.onclick = toggleSidebar;

  const themeBtn = document.getElementById('themeBtn');
  if (themeBtn) themeBtn.onclick = toggleTheme;

  const downloadClientBtn = document.getElementById('downloadClientBtn');
  if (downloadClientBtn) downloadClientBtn.onclick = () => openSettingsPanel('download');

  loadSessions();

  loadProjects();

  loadAgents();

  loadGroups();

  loadFriends();

  const cachedCompanyProfile = safeParse(localStorage.getItem('catsco-profile'));
  if (cachedCompanyProfile) applyCompanyProfile(cachedCompanyProfile);

  renderSidebar();

  renderMessages();

  checkBackend();

  syncCompanyData({ silent: true });

  setInterval(checkBackend, 30000);

  const accountSettingsBtn = document.getElementById('accountSettingsBtn');
  if (accountSettingsBtn) accountSettingsBtn.onclick = toggleAccountMenu;

  const accountAvatar = document.querySelector('.account-avatar');
  if (accountAvatar) {
    accountAvatar.setAttribute('role', 'button');
    accountAvatar.setAttribute('tabindex', '0');
    accountAvatar.onclick = () => openSettingsPanel('profile');
    accountAvatar.onkeydown = (event) => {
      if (event.key === 'Enter' || event.key === ' ') {
        event.preventDefault();
        openSettingsPanel('profile');
      }
    };
  }

  document.querySelectorAll('.welcome-pill').forEach(c => c.onclick = () => useSample(c));

  document.getElementById('input').focus();

  const historyToggle = document.getElementById('historyToggle');

  if (historyToggle) {

    historyToggle.onclick = (e) => {
      if (e.target.closest('.history-add-btn')) return;
      historyToggle.classList.toggle('collapsed');

      const body = document.getElementById('history');

      if (body) body.classList.toggle('collapsed');

      localStorage.setItem('mc-history-collapsed', historyToggle.classList.contains('collapsed') ? '1' : '0');

    };

  }

  let historyAddBtn = document.getElementById('historyAddBtn');
  if (historyToggle && !historyAddBtn) {
    historyAddBtn = document.createElement('span');
    historyAddBtn.id = 'historyAddBtn';
    historyAddBtn.className = 'section-add-btn history-add-btn';
    historyAddBtn.setAttribute('role', 'button');
    historyAddBtn.setAttribute('tabindex', '0');
    historyAddBtn.setAttribute('aria-label', '\u65b0\u5efa\u4efb\u52a1');
    historyAddBtn.setAttribute('title', '\u65b0\u5efa\u4efb\u52a1');
    historyToggle.appendChild(historyAddBtn);
  }
  if (historyAddBtn) {
    const createHistoryTask = async (e) => {
      e.preventDefault();
      e.stopPropagation();
      localStorage.setItem('mc-history-collapsed', '0');
      document.getElementById('historyToggle')?.classList.remove('collapsed');
      document.getElementById('history')?.classList.remove('collapsed');
      await newChat();
    };
    historyAddBtn.onclick = createHistoryTask;
    historyAddBtn.onkeydown = (e) => {
      if (e.key !== 'Enter' && e.key !== ' ') return;
      createHistoryTask(e);
    };
  }

  const attachmentPicker = document.getElementById('attachmentPicker');
  const attachmentMenu = document.getElementById('attachmentMenu');
  const plusBtn = document.getElementById('plusBtn');
  const imageInput = document.getElementById('imageInput');
  const fileInput = document.getElementById('fileInput');

  const closeAttachmentMenu = () => {
    attachmentMenu.classList.remove('open');
    plusBtn.setAttribute('aria-expanded', 'false');
  };

  plusBtn.addEventListener('click', (e) => {
    e.stopPropagation();
    const open = attachmentMenu.classList.toggle('open');
    plusBtn.setAttribute('aria-expanded', open ? 'true' : 'false');
  });

  attachmentMenu.addEventListener('click', (e) => {
    const option = e.target.closest('[data-attachment-type]');
    if (!option) return;
    e.stopPropagation();
    closeAttachmentMenu();
    (option.dataset.attachmentType === 'image' ? imageInput : fileInput).click();
  });

  const handleAttachmentChange = async (e) => {
    const files = Array.from(e.target.files || []);
    e.target.value = '';
    if (!files.length) return;
    if (!CatsCompanyApi.isAuthenticated()) {
      showToast('请先连接公司账号后再上传附件');
      return;
    }
    const available = Math.max(0, 5 - (state.pendingAttachments || []).length);
    if (!available) {
      showToast('每条任务最多添加 5 个附件');
      return;
    }
    plusBtn.disabled = true;
    try {
      for (const file of files.slice(0, available)) {
        const type = file.type.startsWith('image/') ? 'image' : 'file';
        const uploaded = await CatsCompanyApi.uploadFile(file, type);
        state.pendingAttachments.push({
          type,
          name: uploaded.name || file.name,
          size: uploaded.size || file.size,
          payload: {
            file_key: uploaded.file_key,
            url: uploaded.url,
            name: uploaded.name || file.name,
            size: uploaded.size || file.size,
            mime_type: uploaded.mime_type || file.type || '',
            ...(type === 'image' ? { thumbnail: uploaded.url } : {}),
          },
        });
      }
      showToast(`已添加 ${state.pendingAttachments.length} 个附件`);
      updateSendBtn();
    } catch (error) {
      showToast('附件上传失败：' + (error.message || '公司服务未响应'));
    } finally {
      plusBtn.disabled = false;
    }
  };

  imageInput.addEventListener('change', handleAttachmentChange);
  fileInput.addEventListener('change', handleAttachmentChange);

  updateSendBtn();

  document.addEventListener('click', (event) => {

    if (attachmentPicker && !attachmentPicker.contains(event.target)) closeAttachmentMenu();

    document.querySelectorAll('.msg-more-menu.open').forEach(m => m.classList.remove('open'));

  closeAllMenus();

  });

  const collaborationToggle = document.getElementById('collaborationToggle');
  const collaborationBody = document.getElementById('collaborationBody');
  const setCollaborationCollapsed = (collapsed) => {
    collaborationToggle?.classList.toggle('collapsed', collapsed);
    collaborationToggle?.setAttribute('aria-expanded', String(!collapsed));
    collaborationBody?.classList.toggle('collapsed', collapsed);
    localStorage.setItem('mc-collaboration-collapsed', collapsed ? '1' : '0');
  };
  if (collaborationToggle) {
    collaborationToggle.onclick = () => {
      setCollaborationCollapsed(!collaborationToggle.classList.contains('collapsed'));
    };
    collaborationToggle.onkeydown = (e) => {
      if (e.key !== 'Enter' && e.key !== ' ') return;
      e.preventDefault();
      collaborationToggle.click();
    };
    setCollaborationCollapsed(localStorage.getItem('mc-collaboration-collapsed') === '1');
  }

  const agentAddBtn = document.getElementById('agentAddBtn');
  if (agentAddBtn) agentAddBtn.onclick = (e) => {
    e.preventDefault();
    e.stopPropagation();
    addAgent();
  };
  const agentsToggle = document.getElementById('agentsToggle');
  if (agentsToggle) {
    agentsToggle.onclick = (e) => {
      if (e.target.closest('.agent-add-btn')) return;
      if (!(state.agents || []).length) return;
      agentsToggle.classList.toggle('collapsed');
      const body = document.getElementById('agentsList');
      if (body) body.classList.toggle('collapsed');
      localStorage.setItem('mc-agents-collapsed', agentsToggle.classList.contains('collapsed') ? '1' : '0');
    };
    agentsToggle.onkeydown = (e) => {
      if (e.key !== 'Enter' && e.key !== ' ') return;
      e.preventDefault();
      agentsToggle.click();
    };
  }
  if (localStorage.getItem('mc-agents-collapsed') !== '0') {
    document.getElementById('agentsToggle')?.classList.add('collapsed');
    document.getElementById('agentsList')?.classList.add('collapsed');
  }

  const groupAddBtn = document.getElementById('groupAddBtn');
  if (groupAddBtn) groupAddBtn.onclick = (e) => {
    e.preventDefault();
    e.stopPropagation();
    addGroup();
  };
  const groupsToggle = document.getElementById('groupsToggle');
  if (groupsToggle) {
    groupsToggle.onclick = (e) => {
      if (e.target.closest('.group-add-btn')) return;
      if (!(state.groups || []).length) return;
      groupsToggle.classList.toggle('collapsed');
      const body = document.getElementById('groupsList');
      if (body) body.classList.toggle('collapsed');
      localStorage.setItem('mc-groups-collapsed', groupsToggle.classList.contains('collapsed') ? '1' : '0');
    };
    groupsToggle.onkeydown = (e) => {
      if (e.key !== 'Enter' && e.key !== ' ') return;
      e.preventDefault();
      groupsToggle.click();
    };
  }
  if (localStorage.getItem('mc-groups-collapsed') !== '0') {
    document.getElementById('groupsToggle')?.classList.add('collapsed');
    document.getElementById('groupsList')?.classList.add('collapsed');
  }
  const friendAddBtn = document.getElementById('friendAddBtn');
  if (friendAddBtn) friendAddBtn.onclick = (e) => {
    e.preventDefault();
    e.stopPropagation();
    addFriend();
  };
  const friendRequestsBtn = document.getElementById('friendRequestsBtn');
  if (friendRequestsBtn) friendRequestsBtn.onclick = (e) => {
    e.preventDefault();
    e.stopPropagation();
    CollaborationManager.openFriends('requests');
  };
  updateFriendRequestBadge();
  const friendsToggle = document.getElementById('friendsToggle');
  if (friendsToggle) {
    friendsToggle.onclick = (e) => {
      if (e.target.closest('.friend-add-btn, .friend-requests-btn')) return;
      if (!(state.friends || []).length) return;
      friendsToggle.classList.toggle('collapsed');
      const body = document.getElementById('friendsList');
      if (body) body.classList.toggle('collapsed');
      localStorage.setItem('mc-friends-collapsed', friendsToggle.classList.contains('collapsed') ? '1' : '0');
    };
    friendsToggle.onkeydown = (e) => {
      if (e.key !== 'Enter' && e.key !== ' ') return;
      e.preventDefault();
      friendsToggle.click();
    };
  }
  if (localStorage.getItem('mc-friends-collapsed') !== '0') {
    document.getElementById('friendsToggle')?.classList.add('collapsed');
    document.getElementById('friendsList')?.classList.add('collapsed');
  }

  const projectAddBtn = document.getElementById('projectAddBtn');
  if (projectAddBtn) projectAddBtn.onclick = (e) => {
    e.preventDefault();
    e.stopPropagation();
    addProject();
  };
  const projectsToggle = document.getElementById('projectsToggle');
  if (projectsToggle) {
    projectsToggle.onclick = (e) => {
      if (e.target.closest('.project-add-btn')) return;
      if (!(state.projects || []).length) return;
      projectsToggle.classList.toggle('collapsed');
      const body = document.getElementById('projectsList');
      if (body) body.classList.toggle('collapsed');
      localStorage.setItem('mc-projects-collapsed', projectsToggle.classList.contains('collapsed') ? '1' : '0');
    };
    projectsToggle.onkeydown = (e) => {
      if (e.key !== 'Enter' && e.key !== ' ') return;
      e.preventDefault();
      projectsToggle.click();
    };
  }
  if (localStorage.getItem('mc-projects-collapsed') === '1') {
    document.getElementById('projectsToggle')?.classList.add('collapsed');
    document.getElementById('projectsList')?.classList.add('collapsed');
  }

});
