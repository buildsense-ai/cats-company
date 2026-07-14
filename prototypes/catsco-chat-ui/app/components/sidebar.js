window.SidebarComponent = (() => {
  function render(context) {
    const pinnedSection = document.querySelector('.pinned-section');
    const pinnedList = document.getElementById('pinnedList');
    const history = document.getElementById('history');
    if (pinnedList) pinnedList.innerHTML = '';
    if (history) history.innerHTML = '';

    const projectIds = new Set((context.state.projects || []).map(project => project.id));
    const visibleSessions = (context.state.sessions || []).filter(session => !session.projectId || !projectIds.has(session.projectId));
    const pinned = visibleSessions.filter(session => session.pinned);
    const pinnedGroups = (context.state.groups || []).filter(group => group.pinned);
    const unpinned = visibleSessions.filter(session => !session.pinned);
    const historyTitle = document.querySelector('#historyToggle span');
    if (historyTitle) historyTitle.textContent = '历史任务';

    if (pinnedSection) pinnedSection.style.display = pinned.length || pinnedGroups.length ? '' : 'none';
    pinned.forEach(session => pinnedList?.appendChild(context.buildSessionItem(session)));
    pinnedGroups.forEach(group => pinnedList?.appendChild(context.buildGroupItem(group)));

    if (!unpinned.length && history) {
      const empty = document.createElement('div');
      empty.className = 'sidebar-empty';
      empty.textContent = '还没有历史任务';
      history.appendChild(empty);
    } else {
      unpinned.forEach(session => history?.appendChild(context.buildSessionItem(session)));
    }

    CollaborationComponent.render(context);
    ProjectsComponent.render(context);

    const historyCollapsed = localStorage.getItem('mc-history-collapsed') === '1';
    document.getElementById('historyToggle')?.classList.toggle('collapsed', historyCollapsed);
    history?.classList.toggle('collapsed', historyCollapsed);
    context.renderAgentPicker();
  }

  return { render };
})();
