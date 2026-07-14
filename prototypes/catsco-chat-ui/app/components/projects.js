window.ProjectsComponent = (() => {
  function render(context) {
    const list = document.getElementById('projectsList');
    const toggle = document.getElementById('projectsToggle');
    const projects = Array.isArray(context.state.projects) ? context.state.projects : [];
    if (list) {
      list.innerHTML = '';
      if (projects.length) projects.forEach(project => list.appendChild(context.buildProjectItem(project)));
      else {
        const empty = document.createElement('div');
        empty.className = 'sidebar-empty';
        empty.textContent = '';
        list.appendChild(empty);
      }
    }
    toggle?.classList.toggle('empty-section', !projects.length);
    const collapsed = localStorage.getItem('mc-projects-collapsed') === '1' || !projects.length;
    toggle?.classList.toggle('collapsed', collapsed);
    list?.classList.toggle('collapsed', collapsed);
  }

  return { render };
})();
