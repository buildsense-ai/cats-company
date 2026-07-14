window.CollaborationManager = (() => {
  const requestUser = request => request.user || request.from_user || request.requester || request.sender || request;
  const requestUserId = request => request.user_id || request.from_user_id || request.requester_id || request.sender_id || requestUser(request).id || requestUser(request).uid;
  const displayName = value => value?.display_name || value?.name || value?.username || (value?.id ? `UID ${value.id}` : '未知用户');
  const initial = value => String(value || '?').trim().slice(0, 1).toUpperCase();
  const payloadBody = value => value?.data && !Array.isArray(value.data) ? value.data : (value || {});
  const payloadList = (value, keys) => {
    const body = payloadBody(value);
    for (const key of keys) {
      if (Array.isArray(body?.[key])) return body[key];
      if (Array.isArray(value?.[key])) return value[key];
    }
    if (Array.isArray(value?.data)) return value.data;
    return [];
  };

  function closeExisting() {
    document.querySelectorAll('.collaboration-manager-overlay').forEach(node => node.remove());
  }

  function createDialog(className, title, { replace = true, overlayClass = '' } = {}) {
    if (replace) closeExisting();
    const overlay = document.createElement('div');
    overlay.className = `name-dialog-overlay collaboration-manager-overlay ${overlayClass}`.trim();
    overlay.innerHTML = `
      <section class="name-dialog collaboration-manager ${className}" role="dialog" aria-modal="true">
        <header class="name-dialog-head">
          <h2>${escapeHtml(title)}</h2>
          <button class="name-dialog-close" type="button" aria-label="关闭">&times;</button>
        </header>
        <div class="collaboration-manager-body"></div>
      </section>`;
    document.body.appendChild(overlay);
    const close = () => overlay.remove();
    overlay.querySelector('.name-dialog-close').onclick = close;
    overlay.onclick = event => { if (event.target === overlay) close(); };
    overlay.onkeydown = event => { if (event.key === 'Escape') close(); };
    return {
      overlay,
      panel: overlay.querySelector('.collaboration-manager'),
      body: overlay.querySelector('.collaboration-manager-body'),
      close
    };
  }

  function emptyState(title, detail) {
    const node = document.createElement('div');
    node.className = 'collaboration-empty';
    node.innerHTML = `<strong>${escapeHtml(title)}</strong><span>${escapeHtml(detail)}</span>`;
    return node;
  }

  async function loadFriendRequests() {
    if (!CatsCompanyApi.isAuthenticated()) return state.friendRequests || [];
    const payload = await CatsCompanyApi.getPendingFriends();
    const requests = payloadList(payload, ['requests', 'pending', 'items', 'friends']);
    state.friendRequests = Array.isArray(requests) ? requests : [];
    if (typeof updateFriendRequestBadge === 'function') updateFriendRequestBadge();
    return state.friendRequests;
  }

  function openFriends(defaultTab = 'add') {
    const dialog = createDialog('friend-manager-dialog', '好友');
    dialog.body.innerHTML = `
      <section class="collaboration-tab-panel friend-add-panel">
        <div class="collaboration-section-intro">
          <div>
            <h3>添加好友</h3>
            <p>通过名字或 UID 查找联系人并发送申请。</p>
          </div>
        </div>
        <div class="friend-search-row">
          <div class="friend-search-control">
            <select class="ui-select friend-search-mode" aria-label="搜索方式">
              <option value="name">按名字</option>
              <option value="uid">按 UID</option>
            </select>
            <input class="friend-search-input" type="text" maxlength="40" placeholder="搜索联系人">
          </div>
          <button class="primary friend-search-submit" type="button">发送申请</button>
        </div>
        <label class="collaboration-field"><span>验证消息</span><textarea class="friend-request-note" maxlength="80" rows="2" placeholder="介绍一下自己">你好，我想添加你为好友</textarea></label>
      </section>
      <section class="collaboration-tab-panel friend-requests-panel">
        <div class="collaboration-subhead friend-request-head">
          <div><strong>好友申请</strong><span>待处理的好友请求</span></div>
          <span class="request-count" aria-label="待处理数量"></span>
        </div>
        <div class="collaboration-list friend-request-list"></div>
      </section>`;

    const count = dialog.body.querySelector('.request-count');
    const query = dialog.body.querySelector('.friend-search-input');
    const modeSelect = dialog.body.querySelector('.friend-search-mode');
    const note = dialog.body.querySelector('.friend-request-note');

    const renderRequests = async () => {
      const list = dialog.body.querySelector('.friend-request-list');
      list.replaceChildren(emptyState('正在读取好友申请', '请稍候'));
      try {
        const requests = await loadFriendRequests();
        count.textContent = requests.length ? String(requests.length) : '';
        list.replaceChildren();
        if (!requests.length) {
          list.appendChild(emptyState('暂无好友申请', '新的申请会显示在这里'));
          return;
        }
        requests.forEach(request => {
          const user = requestUser(request);
          const userId = requestUserId(request);
          const row = document.createElement('article');
          row.className = 'collaboration-list-row friend-request-row';
          row.innerHTML = `
            <span class="collaboration-avatar">${escapeHtml(initial(displayName(user)))}</span>
            <div class="collaboration-row-copy"><strong>${escapeHtml(displayName(user))}</strong><span>${escapeHtml(request.message || request.note || '申请添加你为好友')}</span></div>
            <div class="collaboration-row-actions">
              <button class="request-reject" type="button">拒绝</button>
              <button class="primary request-accept" type="button">接受</button>
            </div>`;
          const act = async action => {
            row.classList.add('is-busy');
            row.querySelectorAll('button').forEach(button => { button.disabled = true; });
            try {
              await (action === 'accept' ? CatsCompanyApi.acceptFriend(userId) : CatsCompanyApi.rejectFriend(userId));
              if (action === 'accept') await syncCompanyData({ silent: true });
              showToast(action === 'accept' ? '已接受好友申请' : '已拒绝好友申请');
              await renderRequests();
            } catch (error) {
              row.classList.remove('is-busy');
              row.querySelectorAll('button').forEach(button => { button.disabled = false; });
              showToast(AppErrors.userMessage(error, '处理好友申请失败'));
            }
          };
          row.querySelector('.request-accept').onclick = () => act('accept');
          row.querySelector('.request-reject').onclick = () => act('reject');
          list.appendChild(row);
        });
      } catch (error) {
        list.replaceChildren(emptyState('好友申请读取失败', AppErrors.userMessage(error)));
      }
    };

    modeSelect.onchange = () => {
      query.placeholder = modeSelect.value === 'uid' ? '输入用户 UID' : '搜索联系人';
      query.focus();
    };
    dialog.body.querySelector('.friend-search-submit').onclick = async () => {
      const value = query.value.trim();
      if (!value) { query.focus(); return; }
      const submit = dialog.body.querySelector('.friend-search-submit');
      submit.disabled = true;
      try {
        if (!CatsCompanyApi.isAuthenticated()) throw new AppErrors.AppError('请先登录公司账号', { status: 401 });
        const search = await CatsCompanyApi.searchUsers(value, modeSelect.value);
        const user = payloadList(search, ['users', 'items', 'results'])[0];
        if (!user) throw new AppErrors.AppError('没有找到匹配的用户', { status: 404 });
        await CatsCompanyApi.sendFriendRequest(user.id || user.uid, note.value.trim());
        showToast('好友申请已发送');
        query.value = '';
      } catch (error) {
        showToast(error.status === 409 ? '已经是好友或申请已发送' : AppErrors.userMessage(error, '添加好友失败'));
      } finally { submit.disabled = false; }
    };
    query.onkeydown = event => { if (event.key === 'Enter') dialog.body.querySelector('.friend-search-submit').click(); };
    count.textContent = state.friendRequests?.length ? String(state.friendRequests.length) : '';
    renderRequests();
    setTimeout(() => query.focus(), 0);
  }

  function groupMembers(info, group) {
    const body = payloadBody(info);
    const values = body?.members || body?.group?.members || info?.group?.members || group?.memberDetails || [];
    if (Array.isArray(values) && values.length) return values;
    return (group?.members || []).map(id => {
      const item = [...(state.friends || []), ...(state.agents || [])].find(value =>
        String(value.id || value.uid || value.remoteId) === String(id)
      );
      return item || { id, name: `成员 ${id}` };
    });
  }

  const entityId = value => value?.remoteId || value?.uid || value?.user_id || value?.id;

  function inviteChoices() {
    const normalize = (item, kind) => ({
      key: `${kind}:${String(entityId(item) || item.name)}`,
      id: entityId(item),
      localId: item.id || entityId(item),
      kind,
      name: displayName(item),
      detail: kind === 'agent' ? 'Agent 助手' : '好友',
      original: item
    });
    return [
      ...(state.friends || []).map(item => normalize(item, 'friend')),
      ...(state.agents || []).map(item => normalize(item, 'agent'))
    ];
  }

  function openGroupInvite(group, members, onInvited) {
    const dialog = createDialog('group-invite-dialog', '添加群成员', {
      replace: false,
      overlayClass: 'group-invite-overlay'
    });
    dialog.body.innerHTML = `
      <div class="group-invite-picker">
        <section class="group-invite-source">
          <div class="group-invite-search">
            <span aria-hidden="true">⌕</span>
            <input type="search" maxlength="40" placeholder="搜索好友或 Agent" aria-label="搜索可邀请成员">
          </div>
          <div class="group-invite-tabs" role="tablist" aria-label="成员类型">
            <button class="active" type="button" data-kind="friend" role="tab" aria-selected="true">好友</button>
            <button type="button" data-kind="agent" role="tab" aria-selected="false">Agent 助手</button>
          </div>
          <div class="group-invite-candidates"></div>
        </section>
        <section class="group-invite-selected" aria-label="已选成员">
          <div class="group-invite-selected-head"><strong>已选成员</strong><span>0 人</span></div>
          <div class="group-invite-selected-list"></div>
        </section>
      </div>
      <div class="group-invite-footer">
        <button class="group-invite-cancel" type="button">取消</button>
        <button class="primary group-invite-confirm" type="button" disabled>发送邀请</button>
      </div>`;

    const allChoices = inviteChoices();
    const memberIdSet = new Set(members.map(member => String(entityId(member.user || member))).filter(Boolean));
    const availableChoices = allChoices.filter(item => !memberIdSet.has(String(item.id)));
    const selected = new Map();
    const searchInput = dialog.body.querySelector('.group-invite-search input');
    const candidates = dialog.body.querySelector('.group-invite-candidates');
    const selectedList = dialog.body.querySelector('.group-invite-selected-list');
    const selectedCount = dialog.body.querySelector('.group-invite-selected-head span');
    const confirmButton = dialog.body.querySelector('.group-invite-confirm');
    let activeKind = 'friend';

    const renderSelected = () => {
      selectedList.replaceChildren();
      selectedCount.textContent = `${selected.size} 人`;
      confirmButton.disabled = selected.size === 0;
      if (!selected.size) {
        selectedList.appendChild(emptyState('还没有选择成员', '从左侧列表选择要邀请的人'));
        return;
      }
      selected.forEach(item => {
        const row = document.createElement('article');
        row.className = 'group-invite-selected-row';
        row.innerHTML = `
          <span class="collaboration-avatar">${escapeHtml(initial(item.name))}</span>
          <div><strong>${escapeHtml(item.name)}</strong><span>${escapeHtml(item.detail)}</span></div>
          <button type="button" aria-label="移除 ${escapeHtml(item.name)}">&times;</button>`;
        row.querySelector('button').onclick = () => {
          selected.delete(item.key);
          renderCandidates();
          renderSelected();
        };
        selectedList.appendChild(row);
      });
    };

    const renderCandidates = () => {
      const keyword = searchInput.value.trim().toLowerCase();
      const visible = availableChoices.filter(item =>
        item.kind === activeKind && (!keyword || item.name.toLowerCase().includes(keyword))
      );
      candidates.replaceChildren();
      if (!visible.length) {
        candidates.appendChild(emptyState(
          activeKind === 'agent' ? '没有可邀请的 Agent' : '没有可邀请的好友',
          keyword ? '请尝试其他搜索词' : '新成员会显示在这里'
        ));
        return;
      }
      visible.forEach(item => {
        const row = document.createElement('label');
        row.className = 'group-invite-candidate';
        row.innerHTML = `
          <input type="checkbox" ${selected.has(item.key) ? 'checked' : ''}>
          <span class="collaboration-avatar">${escapeHtml(initial(item.name))}</span>
          <span class="group-invite-candidate-copy"><strong>${escapeHtml(item.name)}</strong><small>${escapeHtml(item.detail)}</small></span>`;
        row.querySelector('input').onchange = event => {
          if (event.target.checked) selected.set(item.key, item);
          else selected.delete(item.key);
          renderSelected();
        };
        candidates.appendChild(row);
      });
    };

    dialog.body.querySelectorAll('.group-invite-tabs button').forEach(button => {
      button.onclick = () => {
        activeKind = button.dataset.kind;
        dialog.body.querySelectorAll('.group-invite-tabs button').forEach(item => {
          const active = item === button;
          item.classList.toggle('active', active);
          item.setAttribute('aria-selected', String(active));
        });
        renderCandidates();
      };
    });
    searchInput.oninput = renderCandidates;
    dialog.body.querySelector('.group-invite-cancel').onclick = dialog.close;
    confirmButton.onclick = async () => {
      const items = [...selected.values()];
      const ids = items.map(item => item.id).filter(Boolean);
      if (!ids.length) return;
      confirmButton.disabled = true;
      try {
        if (group.source === 'company') await CatsCompanyApi.inviteToGroup(group.remoteId, ids);
        else {
          group.members = [...new Set([...(group.members || []), ...items.map(item => item.localId)])];
          saveGroups();
        }
        onInvited(items);
        dialog.close();
        showToast('邀请已发送');
      } catch (error) {
        confirmButton.disabled = false;
        showToast(AppErrors.userMessage(error, '邀请成员失败'));
      }
    };
    dialog.overlay.onkeydown = event => {
      if (event.key === 'Escape') dialog.close();
      if (event.key === 'Enter' && (event.ctrlKey || event.metaKey) && !confirmButton.disabled) confirmButton.click();
    };
    renderCandidates();
    renderSelected();
    setTimeout(() => searchInput.focus(), 0);
  }

  async function openGroup(group) {
    if (!group) return;
    const dialog = createDialog('group-manager-dialog', '群聊管理');
    dialog.body.innerHTML = '<div class="collaboration-loading">正在读取群聊信息</div>';
    let info = {};
    try {
      if (group.source === 'company') info = await CatsCompanyApi.getGroupInfo(group.remoteId);
    } catch (error) {
      dialog.body.innerHTML = '';
      dialog.body.appendChild(emptyState('群聊信息读取失败', AppErrors.userMessage(error)));
      return;
    }

    let members = groupMembers(info, group);
    const infoBody = payloadBody(info);
    const currentName = infoBody?.name || infoBody?.group?.name || info?.group?.name || group.name;
    let savedName = currentName;
    const announcement = infoBody?.announcement || infoBody?.group?.announcement || info?.group?.announcement || group.announcement || '';
    dialog.body.innerHTML = `
      <section class="group-summary">
        <span class="collaboration-avatar group-avatar">${escapeHtml(initial(currentName))}</span>
        <div class="group-summary-copy">
          <span class="group-summary-label">群聊名称</span>
          <div class="group-name-row">
            <input class="group-name-input" type="text" maxlength="40" value="${escapeHtml(currentName)}" aria-label="群聊名称">
            <button class="group-name-save" type="button" disabled>保存</button>
          </div>
          <span class="group-member-count">${members.length} 位成员</span>
        </div>
      </section>
      <div class="group-manager-grid">
        <section class="collaboration-subsection group-members-section">
          <div class="collaboration-subhead"><div><strong>群成员</strong><span>查看和管理当前成员</span></div><button class="group-invite-toggle" type="button">+ 邀请成员</button></div>
          <div class="group-member-list"></div>
        </section>
        <section class="collaboration-subsection group-settings-section">
          <div class="collaboration-subhead"><div><strong>群公告</strong><span>所有群成员均可查看</span></div></div>
          <textarea class="group-announcement" maxlength="500" rows="5" placeholder="还没有群公告">${escapeHtml(announcement)}</textarea>
          <button class="primary group-announcement-save" type="button">保存群公告</button>
        </section>
      </div>
      <div class="collaboration-footer-actions group-danger-actions">
        <button class="danger group-leave" type="button">退出群聊</button>
      </div>`;

    const memberList = dialog.body.querySelector('.group-member-list');
    const memberCount = dialog.body.querySelector('.group-member-count');
    const renderMembers = () => {
      memberList.replaceChildren();
      memberCount.textContent = `${members.length} 位成员`;
      if (!members.length) {
        const empty = emptyState('暂无成员信息', '邀请好友或 Agent 加入群聊');
        const invite = document.createElement('button');
        invite.className = 'group-empty-invite';
        invite.type = 'button';
        invite.textContent = '邀请成员';
        empty.appendChild(invite);
        invite.onclick = () => dialog.body.querySelector('.group-invite-toggle').click();
        memberList.appendChild(empty);
        return;
      }
      members.forEach(member => {
        const user = member.user || member;
        const memberId = entityId(member) || entityId(user);
        const tile = document.createElement('article');
        tile.className = 'group-member-tile';
        tile.innerHTML = `
          <span class="collaboration-avatar">${escapeHtml(initial(displayName(user)))}</span>
          <strong title="${escapeHtml(displayName(user))}">${escapeHtml(displayName(user))}</strong>
          <span>${escapeHtml(member.role || ((state.agents || []).includes(user) ? 'Agent' : '成员'))}</span>
          <button class="group-member-remove" type="button" aria-label="移出 ${escapeHtml(displayName(user))}">&times;</button>`;
        tile.querySelector('.group-member-remove').onclick = async () => {
          if (!confirm(`将「${displayName(user)}」移出群聊？`)) return;
          try {
            if (group.source === 'company') await CatsCompanyApi.kickGroupMember(group.remoteId, memberId);
            else {
              group.members = (group.members || []).filter(id => String(id) !== String(memberId));
              saveGroups();
            }
            members = members.filter(item => String(entityId(item) || entityId(item.user || item)) !== String(memberId));
            renderMembers();
            showToast('成员已移出群聊');
          } catch (error) { showToast(AppErrors.userMessage(error, '移出成员失败')); }
        };
        memberList.appendChild(tile);
      });
    };

    dialog.body.querySelector('.group-invite-toggle').onclick = () => {
      openGroupInvite(group, members, items => {
        const existing = new Set(members.map(item => String(entityId(item.user || item))));
        items.forEach(item => {
          if (existing.has(String(item.id))) return;
          members.push({ ...item.original, role: item.kind === 'agent' ? 'Agent' : '成员' });
        });
        renderMembers();
      });
    };

    const nameInput = dialog.body.querySelector('.group-name-input');
    const nameSave = dialog.body.querySelector('.group-name-save');
    const avatar = dialog.body.querySelector('.group-avatar');
    nameInput.oninput = () => {
      const value = nameInput.value.trim();
      nameSave.disabled = !value || value === savedName;
    };
    nameInput.onkeydown = event => { if (event.key === 'Enter' && !nameSave.disabled) nameSave.click(); };
    nameSave.onclick = async () => {
      const value = nameInput.value.trim();
      if (!value) return;
      nameSave.disabled = true;
      try {
        if (group.source === 'company') await CatsCompanyApi.updateGroup(group.remoteId, value, group.avatarUrl || '');
        group.name = value;
        savedName = value;
        avatar.textContent = initial(value);
        saveGroups();
        renderSidebar();
        nameSave.disabled = true;
        showToast('群聊名称已更新');
      } catch (error) {
        nameSave.disabled = false;
        showToast(AppErrors.userMessage(error, '群聊名称更新失败'));
      }
    };
    renderMembers();
    dialog.body.querySelector('.group-announcement-save').onclick = async () => {
      const value = dialog.body.querySelector('.group-announcement').value.trim();
      try {
        if (group.source === 'company') await CatsCompanyApi.setGroupAnnouncement(group.remoteId, value);
        group.announcement = value;
        saveGroups();
        showToast('群公告已保存');
      } catch (error) { showToast(AppErrors.userMessage(error, '群公告保存失败')); }
    };
    dialog.body.querySelector('.group-leave').onclick = async () => {
      if (!confirm(`退出群聊「${group.name}」？`)) return;
      try {
        if (group.source === 'company') await CatsCompanyApi.leaveGroup(group.remoteId);
        state.groups = (state.groups || []).filter(item => item.id !== group.id);
        saveGroups();
        renderSidebar();
        dialog.close();
        showToast('已退出群聊');
      } catch (error) { showToast(AppErrors.userMessage(error, '退出群聊失败')); }
    };
  }

  return { openFriends, openGroup, loadFriendRequests };
})();
