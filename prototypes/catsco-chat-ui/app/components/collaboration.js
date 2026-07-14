window.CollaborationComponent = (() => {
  function renderCollection({ listId, toggleId, items, buildItem, collapsedKey, collapsedWhenEmpty = true }) {
    const list = document.getElementById(listId);
    const toggle = document.getElementById(toggleId);
    if (list) {
      list.innerHTML = '';
      if (items.length) items.forEach(item => list.appendChild(buildItem(item)));
      else {
        const empty = document.createElement('div');
        empty.className = 'sidebar-empty';
        empty.textContent = '';
        list.appendChild(empty);
      }
    }
    toggle?.classList.toggle('empty-section', !items.length);
    const collapsed = localStorage.getItem(collapsedKey) !== '0' || (collapsedWhenEmpty && !items.length);
    toggle?.classList.toggle('collapsed', collapsed);
    list?.classList.toggle('collapsed', collapsed);
  }

  function render(context) {
    const agents = Array.isArray(context.state.agents) ? context.state.agents : [];
    const groups = (Array.isArray(context.state.groups) ? context.state.groups : []).filter(group => !group.pinned);
    const friends = Array.isArray(context.state.friends) ? context.state.friends : [];
    renderCollection({ listId: 'friendsList', toggleId: 'friendsToggle', items: friends, buildItem: context.buildFriendItem, collapsedKey: 'mc-friends-collapsed' });
    renderCollection({ listId: 'groupsList', toggleId: 'groupsToggle', items: groups, buildItem: context.buildGroupItem, collapsedKey: 'mc-groups-collapsed' });
    renderCollection({ listId: 'agentsList', toggleId: 'agentsToggle', items: agents, buildItem: context.buildAgentItem, collapsedKey: 'mc-agents-collapsed' });
  }

  return { render };
})();
