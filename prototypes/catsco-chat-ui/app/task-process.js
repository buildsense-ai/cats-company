const TASK_PROCESS_VERSION = 2;

const TASK_PROCESS_STATUS_LABELS = {
  preparing: '准备中',
  connecting: '连接中',
  generating: '生成中',
  finalizing: '检查中',
  completed: '已完成',
  error: '需要处理',
  stopped: '已停止'
};

function summarizeTaskGoal(text) {
  const clean = String(text || '').replace(/\s+/g, ' ').trim();
  return clean.length > 56 ? clean.slice(0, 56) + '…' : clean;
}

function classifyTaskProcess(text) {
  const clean = String(text || '').trim();
  const codePattern = /(代码|前端|后端|接口|项目|文件|组件|页面|样式|布局|交互|修复|重构|实现|开发|测试|报错|错误|部署|数据库|脚本|函数|class|api|bug|html|css|javascript|typescript|python)/i;
  const writingPattern = /(写一|撰写|改写|润色|翻译|总结|邮件|文案|文章|报告)/;
  const planningPattern = /(规划|计划|方案|需求|设计|拆解|流程|架构)/;
  const actionPattern = /(帮我|请你|需要你|检查|分析|比较|整理|生成|创建|修改|优化|评估)/;
  const structured = /\n|(^|\s)[1-9][\.、)]|[；;]/.test(clean);

  let category = 'general';
  if (codePattern.test(clean)) category = 'code';
  else if (writingPattern.test(clean)) category = 'writing';
  else if (planningPattern.test(clean)) category = 'planning';
  else if (/(分析|检查|评估|为什么|原因)/.test(clean)) category = 'analysis';

  let score = 0;
  if (clean.length > 28) score += 1;
  if (clean.length > 80) score += 1;
  if (structured) score += 1;
  if (actionPattern.test(clean)) score += 1;
  if (category === 'code' || category === 'planning') score += 2;

  if (score === 0 && clean.length <= 28) return { kind: 'simple', category };
  return { kind: score >= 3 ? 'complex' : 'standard', category };
}

function taskProcessSteps(profile) {
  const labels = {
    code: {
      understand: '确认要解决的问题',
      generate: '分析相关代码和影响范围',
      organize: '整理修改方案与结果'
    },
    writing: {
      understand: '确认内容目标和语气',
      generate: '组织内容结构并撰写',
      organize: '整理和润色表达'
    },
    planning: {
      understand: '确认目标和完成标准',
      generate: '梳理条件、步骤和影响',
      organize: '整理成可执行方案'
    },
    analysis: {
      understand: '确认需要判断的问题',
      generate: '分析原因和关键信息',
      organize: '整理结论和建议'
    },
    general: {
      understand: '确认您的问题',
      generate: '生成清晰的回答',
      organize: '整理回答内容'
    }
  };
  const copy = labels[profile.category] || labels.general;
  const steps = [
    { id: 'understand', label: copy.understand, detail: '正在整理您的要求', state: 'active' },
    { id: 'connect', label: '连接服务并提交任务', detail: '等待发送', state: 'pending' },
    { id: 'generate', label: copy.generate, detail: '等待处理', state: 'pending' }
  ];
  if (profile.kind === 'complex') {
    steps.push({ id: 'organize', label: copy.organize, detail: '等待整理', state: 'pending' });
  }
  steps.push({ id: 'verify', label: '确认回答完整返回', detail: '等待检查', state: 'pending' });
  return steps;
}

function createTaskProcess(text) {
  const profile = classifyTaskProcess(text);
  if (profile.kind === 'simple') return null;
  const now = Date.now();
  return {
    version: TASK_PROCESS_VERSION,
    kind: profile.kind,
    category: profile.category,
    status: 'preparing',
    goal: summarizeTaskGoal(text),
    collapsed: false,
    technicalCollapsed: true,
    startedAt: now,
    updatedAt: now,
    finishedAt: null,
    steps: taskProcessSteps(profile),
    technical: [
      { key: 'task-type', label: '任务类型', value: profile.kind === 'complex' ? '复杂任务' : '一般任务' },
      { key: 'started', label: '开始时间', value: new Date(now).toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit', second: '2-digit' }) }
    ]
  };
}

function normalizeTaskProcess(process) {
  if (!process) return null;
  if (!Array.isArray(process.steps)) process.steps = [];
  if (!Array.isArray(process.technical)) process.technical = [];
  if (typeof process.collapsed !== 'boolean') process.collapsed = false;
  if (typeof process.technicalCollapsed !== 'boolean') process.technicalCollapsed = true;
  return process;
}

function adaptLegacyTaskProcess(message) {
  if (!message || (!message.agentTask && !Array.isArray(message.processSteps))) return null;
  const legacyStatus = message.agentTask?.status || (message.streaming ? 'executing' : 'completed');
  const statusMap = {
    thinking: 'preparing',
    planning: 'connecting',
    executing: 'generating',
    completed: 'completed',
    error: 'error',
    stopped: 'stopped'
  };
  const steps = (message.processSteps || []).map((step, index) => ({
    id: step.key || 'legacy-' + index,
    label: step.text || '处理任务',
    detail: '',
    state: step.state === 'active' && legacyStatus === 'completed' ? 'done' : (step.state || 'pending')
  }));
  const process = {
    version: 1,
    kind: 'legacy',
    category: 'general',
    status: statusMap[legacyStatus] || 'completed',
    goal: message.agentTask?.intent || '',
    collapsed: typeof message.processCollapsed === 'boolean' ? message.processCollapsed : true,
    technicalCollapsed: true,
    startedAt: message.time || Date.now(),
    updatedAt: message.agentTask?.updatedAt || message.time || Date.now(),
    finishedAt: legacyStatus === 'completed' ? (message.agentTask?.updatedAt || message.time || Date.now()) : null,
    steps: steps.length ? steps : [{ id: 'legacy', label: message.agentTask?.currentStep || '已完成', detail: '', state: legacyStatus === 'completed' ? 'done' : 'active' }],
    technical: [{ key: 'legacy', label: '记录格式', value: '早期任务记录' }]
  };
  message.taskProcess = process;
  return process;
}

function getTaskProcess(message) {
  if (!message) return null;
  return normalizeTaskProcess(message.taskProcess || adaptLegacyTaskProcess(message));
}

function setTaskTechnical(message, key, label, value) {
  const process = getTaskProcess(message);
  if (!process || value === undefined || value === null || value === '') return;
  const existing = process.technical.find(item => item.key === key);
  if (existing) {
    existing.label = label;
    existing.value = String(value);
  } else {
    process.technical.push({ key, label, value: String(value) });
  }
}

function setTaskProcessStage(message, stageId, options = {}) {
  const process = getTaskProcess(message);
  if (!process) return;
  let index = process.steps.findIndex(step => step.id === stageId);
  if (index < 0 && stageId === 'organize') index = process.steps.findIndex(step => step.id === 'generate');
  if (index < 0) return;

  process.steps.forEach((step, stepIndex) => {
    if (stepIndex < index && !['error', 'stopped'].includes(step.state)) step.state = 'done';
    else if (stepIndex === index) step.state = options.state || 'active';
    else if (!['error', 'stopped'].includes(step.state)) step.state = 'pending';
  });
  if (options.label) process.steps[index].label = options.label;
  if (options.detail !== undefined) process.steps[index].detail = options.detail;
  process.status = options.status || ({
    understand: 'preparing',
    connect: 'connecting',
    generate: 'generating',
    organize: 'generating',
    verify: 'finalizing'
  }[stageId] || process.status);
  process.updatedAt = Date.now();
  process.collapsed = false;
  if (options.technical) {
    setTaskTechnical(message, options.technical.key, options.technical.label, options.technical.value);
  }
}

function completeTaskProcess(message, options = {}) {
  const process = getTaskProcess(message);
  if (!process) return;
  process.steps.forEach(step => {
    step.state = 'done';
    if (step.id === 'verify') step.detail = options.detail || '回答已完整接收';
  });
  process.status = 'completed';
  process.updatedAt = Date.now();
  process.finishedAt = process.updatedAt;
  process.collapsed = false;
  if (options.outputLength !== undefined) setTaskTechnical(message, 'output', '回答长度', options.outputLength + ' 个字符');
  setTaskTechnical(message, 'finished', '完成时间', new Date(process.finishedAt).toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit', second: '2-digit' }));
}

function failTaskProcess(message, errorText) {
  const process = getTaskProcess(message);
  if (!process) return;
  const current = process.steps.find(step => step.state === 'active') || process.steps.find(step => step.state === 'pending') || process.steps[process.steps.length - 1];
  if (current) {
    current.state = 'error';
    current.detail = errorText || '处理过程中出现问题';
  }
  process.status = 'error';
  process.updatedAt = Date.now();
  process.finishedAt = process.updatedAt;
  process.collapsed = false;
  setTaskTechnical(message, 'error', '错误信息', errorText || '未知错误');
}

function stopTaskProcess(message, hasPartialOutput) {
  const process = getTaskProcess(message);
  if (!process) return;
  const current = process.steps.find(step => step.state === 'active') || process.steps[process.steps.length - 1];
  if (current) {
    current.state = 'stopped';
    current.detail = hasPartialOutput ? '已保留停止前收到的内容' : '用户已停止生成';
  }
  process.status = 'stopped';
  process.updatedAt = Date.now();
  process.finishedAt = process.updatedAt;
  process.collapsed = false;
  setTaskTechnical(message, 'stopped', '停止时间', new Date(process.finishedAt).toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit', second: '2-digit' }));
}

function collapseCompletedTaskProcesses(session) {
  if (!session || !Array.isArray(session.messages)) return;
  session.messages.forEach(message => {
    const process = getTaskProcess(message);
    if (process && process.status === 'completed') process.collapsed = true;
  });
}

function taskStepIcon(stepState) {
  if (stepState === 'done') {
    return '<svg viewBox="0 0 20 20" aria-hidden="true"><path d="m5 10 3 3 7-7"/></svg>';
  }
  if (stepState === 'error') {
    return '<svg viewBox="0 0 20 20" aria-hidden="true"><path d="M10 5v6M10 14.5v.1"/></svg>';
  }
  if (stepState === 'stopped') {
    return '<svg viewBox="0 0 20 20" aria-hidden="true"><rect x="6" y="6" width="8" height="8" rx="1"/></svg>';
  }
  return '';
}

function renderProcessPanel(message, messageIndex) {
  const process = getTaskProcess(message);
  if (!process) return '';
  const total = process.steps.length;
  const done = process.steps.filter(step => step.state === 'done').length;
  const statusLabel = TASK_PROCESS_STATUS_LABELS[process.status] || '处理中';
  const running = ['preparing', 'connecting', 'generating', 'finalizing'].includes(process.status);
  const stateClass = 'status-' + escapeHtml(process.status || 'preparing');
  const stepsHtml = process.steps.map(step => {
    const detail = step.detail ? '<span class="task-step-detail">' + escapeHtml(step.detail) + '</span>' : '';
    return '<li class="task-step ' + escapeHtml(step.state || 'pending') + '">'
      + '<span class="task-step-marker">' + taskStepIcon(step.state) + '</span>'
      + '<span class="task-step-copy"><span class="task-step-label">' + escapeHtml(step.label || '处理任务') + '</span>' + detail + '</span>'
      + '</li>';
  }).join('');
  const technicalHtml = process.technical.map(item => (
    '<div class="task-technical-row"><span>' + escapeHtml(item.label || '') + '</span><strong>' + escapeHtml(item.value || '') + '</strong></div>'
  )).join('');
  const technicalCount = process.technical.length;

  return '<section class="task-process ' + stateClass + (running ? ' is-running' : '') + (process.collapsed ? ' collapsed' : '') + '" aria-label="任务过程">'
    + '<button class="task-process-toggle" type="button" aria-expanded="' + (!process.collapsed) + '" onclick="toggleProcessPanel(' + messageIndex + ', this)">'
    + '<span class="task-process-chevron"><svg viewBox="0 0 20 20" aria-hidden="true"><path d="m6 8 4 4 4-4"/></svg></span>'
    + '<span class="task-process-title">任务过程</span>'
    + '<span class="task-process-summary"><span class="task-process-status">' + statusLabel + '</span><span class="task-process-progress">' + done + '/' + total + '</span></span>'
    + '</button>'
    + '<div class="task-process-body">'
    + (process.goal ? '<div class="task-process-goal"><span>任务目标</span><p>' + escapeHtml(process.goal) + '</p></div>' : '')
    + '<ol class="task-step-list">' + stepsHtml + '</ol>'
    + (technicalCount ? '<div class="task-technical ' + (process.technicalCollapsed ? 'collapsed' : '') + '">'
      + '<button class="task-technical-toggle" type="button" aria-expanded="' + (!process.technicalCollapsed) + '" onclick="toggleTaskTechnical(' + messageIndex + ', event, this)">'
      + '<span class="task-technical-chevron"><svg viewBox="0 0 20 20" aria-hidden="true"><path d="m6 8 4 4 4-4"/></svg></span>'
      + '<span>技术详情</span><small>' + technicalCount + ' 项</small>'
      + '</button>'
      + '<div class="task-technical-list">' + technicalHtml + '</div>'
      + '</div>' : '')
    + '</div>'
    + '</section>';
}

function renderReplyWaiting() {
  return '<div class="reply-waiting" role="status" aria-live="polite"><span class="reply-waiting-spinner"></span><span>正在回复</span></div>';
}

function preserveTaskToggleAnchor(toggleButton, update) {
  const main = document.getElementById('main');
  const anchorTop = toggleButton?.getBoundingClientRect().top;
  update();
  if (!main || !toggleButton || !Number.isFinite(anchorTop)) return;

  const restoreAnchor = () => {
    const currentTop = toggleButton.getBoundingClientRect().top;
    main.scrollTop += currentTop - anchorTop;
  };

  restoreAnchor();
  requestAnimationFrame(() => {
    restoreAnchor();
    requestAnimationFrame(restoreAnchor);
  });
}

function toggleProcessPanel(messageIndex, toggleButton) {
  const session = currentSession();
  if (!session || !session.messages[messageIndex]) return;
  const process = getTaskProcess(session.messages[messageIndex]);
  if (!process) return;
  process.collapsed = !process.collapsed;
  const panel = toggleButton?.closest('.task-process');
  if (panel) {
    preserveTaskToggleAnchor(toggleButton, () => {
      panel.classList.toggle('collapsed', process.collapsed);
      toggleButton.setAttribute('aria-expanded', String(!process.collapsed));
    });
  } else {
    renderMessages({ preserveScroll: true });
  }
  saveSessions();
}

function toggleTaskTechnical(messageIndex, event, toggleButton) {
  if (event) event.stopPropagation();
  const session = currentSession();
  if (!session || !session.messages[messageIndex]) return;
  const process = getTaskProcess(session.messages[messageIndex]);
  if (!process) return;
  process.technicalCollapsed = !process.technicalCollapsed;
  const technical = toggleButton?.closest('.task-technical');
  if (technical) {
    preserveTaskToggleAnchor(toggleButton, () => {
      technical.classList.toggle('collapsed', process.technicalCollapsed);
      toggleButton.setAttribute('aria-expanded', String(!process.technicalCollapsed));
    });
  } else {
    renderMessages({ preserveScroll: true });
  }
  saveSessions();
}
