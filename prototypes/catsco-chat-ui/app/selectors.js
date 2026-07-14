const MODEL_OPTIONS = [
  { id: 'MiniMax-M3', name: 'MiniMax-M3', description: '\u540e\u7aef\u9ed8\u8ba4\u6a21\u578b' },
];

function setAvailableModels(names, defaultModel) {
  const normalized = [...new Set((names || []).map(name => String(name || '').trim()).filter(Boolean))];
  if (!normalized.length && defaultModel) normalized.push(String(defaultModel));
  if (!normalized.length) return;
  MODEL_OPTIONS.splice(0, MODEL_OPTIONS.length, ...normalized.map(name => ({
    id: name,
    name,
    description: name === defaultModel ? '\u540e\u7aef\u9ed8\u8ba4\u6a21\u578b' : '\u516c\u53f8\u540e\u7aef\u53ef\u7528\u6a21\u578b',
  })));
  if (!normalized.includes(state.model)) state.model = defaultModel && normalized.includes(defaultModel) ? defaultModel : normalized[0];
  localStorage.setItem('mc-selected-model', state.model);
  renderModelPicker();
}

function selectorInitial(name) {
  const value = String(name || '').trim();
  if (!value) return 'AI';
  const ascii = value.match(/[A-Za-z0-9]/);
  return (ascii ? ascii[0] : value[0]).toUpperCase();
}

function closeSelectorMenus(except) {
  ['modelSelect', 'agentPicker'].forEach(id => {
    if (id === except) return;
    const root = document.getElementById(id);
    root?.classList.remove('open');
    root?.querySelector('[aria-expanded]')?.setAttribute('aria-expanded', 'false');
  });
}

function buildSelectorOption(item, selected, type) {
  const button = document.createElement('button');
  button.type = 'button';
  button.className = 'selector-option' + (selected ? ' selected' : '');
  button.setAttribute('role', 'option');
  button.setAttribute('aria-selected', String(selected));

  if (type === 'agent') {
    const avatar = document.createElement('span');
    avatar.className = 'selector-option-avatar';
    avatar.textContent = selectorInitial(item.name);
    button.appendChild(avatar);
  }

  const copy = document.createElement('span');
  copy.className = 'selector-option-copy';
  const name = document.createElement('strong');
  name.textContent = item.name;
  copy.appendChild(name);
  if (type !== 'agent') {
    const description = document.createElement('span');
    description.textContent = item.description || '';
    copy.appendChild(description);
  }
  button.appendChild(copy);

  const check = document.createElement('span');
  check.className = 'selector-option-check';
  check.textContent = selected ? '\u2713' : '';
  button.appendChild(check);
  return button;
}

function renderModelPicker() {
  const selected = MODEL_OPTIONS.some(model => model.id === state.model)
    ? state.model
    : MODEL_OPTIONS[0].id;
  state.model = selected;
  const label = document.getElementById('modelLabel');
  const sidebarLabel = document.getElementById('modelName');
  if (label) label.textContent = selected;
  if (sidebarLabel) sidebarLabel.textContent = selected;

  const menu = document.getElementById('modelMenu');
  if (!menu) return;
  menu.innerHTML = '';
  MODEL_OPTIONS.forEach(model => {
    const option = buildSelectorOption(model, model.id === selected, 'model');
    option.onclick = event => {
      event.stopPropagation();
      state.model = model.id;
      localStorage.setItem('mc-selected-model', model.id);
      renderModelPicker();
      closeSelectorMenus();
    };
    menu.appendChild(option);
  });
}

function selectComposerAgent(agentId) {
  state.agentCurrentId = (state.agents || []).some(agent => agent.id === agentId) ? agentId : null;
  if (state.agentCurrentId) localStorage.setItem('mc-current-agent', state.agentCurrentId);
  else localStorage.removeItem('mc-current-agent');
  closeSelectorMenus();
  renderSidebar();
  renderMessages();
}

function renderAgentPicker() {
  const agents = Array.isArray(state.agents) ? state.agents : [];
  let current = agents.find(agent => agent.id === state.agentCurrentId);
  if (!current && agents.length) {
    current = agents[0];
    state.agentCurrentId = current.id;
    localStorage.setItem('mc-current-agent', current.id);
  }

  const name = document.getElementById('agentPickerName');
  const avatar = document.getElementById('agentPickerAvatar');
  if (name) name.textContent = current?.name || '\u9009\u62e9 Agent';
  if (avatar) avatar.textContent = selectorInitial(current?.name);

  const menu = document.getElementById('agentPickerMenu');
  if (!menu) return;
  menu.innerHTML = '';
  if (!agents.length) {
    const empty = document.createElement('div');
    empty.className = 'selector-empty';
    empty.textContent = '\u8bf7\u5148\u5728\u5de6\u4fa7\u6dfb\u52a0 Agent \u52a9\u624b';
    menu.appendChild(empty);
    return;
  }

  agents.forEach(agent => {
    const option = buildSelectorOption(agent, agent.id === state.agentCurrentId, 'agent');
    option.onclick = event => {
      event.stopPropagation();
      selectComposerAgent(agent.id);
    };
    menu.appendChild(option);
  });
}

function bindSelectorButton(rootId, buttonId) {
  const root = document.getElementById(rootId);
  const button = document.getElementById(buttonId);
  if (!root || !button) return;
  button.onclick = event => {
    event.stopPropagation();
    const open = !root.classList.contains('open');
    closeSelectorMenus(open ? rootId : null);
    root.classList.toggle('open', open);
    button.setAttribute('aria-expanded', String(open));
  };
  button.onkeydown = event => {
    if (event.key === 'Escape') {
      closeSelectorMenus();
      button.focus();
    }
  };
}

document.addEventListener('DOMContentLoaded', () => {
  const savedModel = localStorage.getItem('mc-selected-model');
  if (MODEL_OPTIONS.some(model => model.id === savedModel)) state.model = savedModel;
  bindSelectorButton('modelSelect', 'modelBtn');
  bindSelectorButton('agentPicker', 'agentPickerBtn');
  renderModelPicker();
  renderAgentPicker();

  document.addEventListener('click', () => closeSelectorMenus());
  document.addEventListener('keydown', event => {
    if (event.key === 'Escape') closeSelectorMenus();
  });
});
