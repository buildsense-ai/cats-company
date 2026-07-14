let defaultWelcomeHtml = '';

function messageActionIcon(name) {
  const paths = {
    copy: '<rect x="8" y="8" width="10" height="10" rx="2"/><path d="M16 8V6a2 2 0 0 0-2-2H6a2 2 0 0 0-2 2v8a2 2 0 0 0 2 2h2"/>',
    like: '<path d="M7 10v10H4a2 2 0 0 1-2-2v-6a2 2 0 0 1 2-2h3Zm0 10h9.2a2 2 0 0 0 1.9-1.4l1.7-5.2A2 2 0 0 0 17.9 11H14l.6-3.3A3.1 3.1 0 0 0 11.5 4L7 10Z"/>',
    regenerate: '<path d="M3 12a9 9 0 1 0 3-6.7L3 8"/><path d="M3 3v5h5"/>',
    more: '<circle cx="5" cy="12" r="1"/><circle cx="12" cy="12" r="1"/><circle cx="19" cy="12" r="1"/>'
  };
  return '<svg viewBox="0 0 24 24" aria-hidden="true">' + (paths[name] || '') + '</svg>';
}

function restoreDefaultWelcome(welcome) {
  if (!defaultWelcomeHtml) defaultWelcomeHtml = welcome.innerHTML;
  if (welcome.dataset.mode === 'project') {
    welcome.innerHTML = defaultWelcomeHtml;
    welcome.dataset.mode = 'default';
  }
}

function renderMessagesView(options = {}) {

  const main = document.getElementById('main');

  const preservedScrollTop = options.preserveScroll && main ? main.scrollTop : null;

  const welcome = document.getElementById('welcome');

  const container = document.getElementById('messages');

  container.innerHTML = '';

  const sess = currentSession();

  const mainWrap = document.querySelector('.main-wrap');

  if (!sess || !sess.messages.length) {
    if (mainWrap) mainWrap.classList.add('empty-chat');
    restoreDefaultWelcome(welcome);
    welcome.style.display = 'block';
    updateHeaderTitle();
    renderProgressBar();
    return;
  }

  if (mainWrap) mainWrap.classList.remove('empty-chat');

  restoreDefaultWelcome(welcome);
  welcome.style.display = 'none';

  sess.messages.forEach((m, idx) => {

    const isUser = m.role === 'user';

    const isError = m.role === 'error';

    const div = document.createElement('div');

    const taskProcess = !isUser ? getTaskProcess(m) : null;

    div.className = 'msg ' + (isUser ? 'user' : (isError ? 'bot error' : 'bot')) + (taskProcess ? ' agent-task' : '');

    if (isError && m.errorKind) div.dataset.errorKind = m.errorKind;

    const time = m.time ? new Date(m.time).toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit' }) : '';

    const messageFooter = !isUser
      ? '<div class="message-footer">'
        + '<div class="message-inline-actions" role="toolbar" aria-label="回复操作">'
        + '<button class="msg-action-btn" type="button" title="复制" aria-label="复制" onclick="copyAssistantMessage(' + idx + ', this)">' + messageActionIcon('copy') + '</button>'
        + '<button class="msg-action-btn msg-like-btn' + (m.liked ? ' active' : '') + '" type="button" title="点赞" aria-label="点赞" aria-pressed="' + Boolean(m.liked) + '" onclick="toggleAssistantLike(' + idx + ', this)">' + messageActionIcon('like') + '</button>'
        + '<button class="msg-action-btn" type="button" title="重新生成" aria-label="重新生成" onclick="regenerateMessage(' + idx + ')">' + messageActionIcon('regenerate') + '</button>'
        + '<div class="message-action-more">'
        + '<button class="msg-action-btn msg-more-btn" type="button" title="更多" aria-label="更多" onclick="toggleMoreMenu(event, this)">' + messageActionIcon('more') + '</button>'
        + '<div class="msg-more-menu"><button data-action="reply">回复</button><button data-action="share">分享</button></div>'
        + '</div></div>'
        + (time ? '<time class="message-time">' + time + '</time>' : '')
        + '</div>'
      : '';

    const processHtml = !isUser && taskProcess
      ? renderProcessPanel(m, idx)
      : (!isUser && m.streaming ? renderReplyWaiting() : '');

    const currentAgent = !isUser
      ? (state.agents || []).find(agent => agent.id === state.agentCurrentId)
      : null;
    const agentName = currentAgent?.name || 'AI';
    const agentAvatar = !isUser
      ? '<div class="avatar agent-message-avatar" title="' + escapeHtml(agentName) + '" aria-label="' + escapeHtml(agentName) + '">'
        + escapeHtml(selectorInitial(agentName))
        + '</div>'
      : '';

    div.innerHTML = agentAvatar + '<div class="content">'

      +   '<div class="body">' + processHtml + (isUser ? escapeHtml(m.content) : renderMarkdown(m.content)) + '</div>'

      +   messageFooter

      + '</div>';

    attachCodeCopy(div);

    if (!isUser) attachMoreMenu(div, m);

    container.appendChild(div);

  });

  updateMsgCount();

  if (preservedScrollTop !== null) {
    requestAnimationFrame(() => { main.scrollTop = preservedScrollTop; });
  } else {
    scrollToBottom();
  }

  renderProgressBar();

  updateHeaderTitle();

}

MessagesComponent.register(renderMessagesView);

function renderMessages(options = {}) {
  return MessagesComponent.render(options);
}

function getUserQuestions() {

  const sess = currentSession();

  if (!sess) return [];

  return sess.messages.filter(m => m.role === 'user');

}

function renderProgressBar() {

  const bar = document.getElementById('progressBar');

  const track = document.getElementById('progressTrack');

  if (!bar || !track) return;

  const qs = getUserQuestions();

  const activeIdx = qs.length ? qs.length - 1 : -1;

  track.innerHTML = '';

  if (qs.length === 0) {
    bar.classList.add('empty');
    bar.classList.remove('show');
    return;
  }

  bar.classList.remove('empty');
  bar.classList.add('show');
  track.onclick = (event) => {
    const dot = event.target.closest('.progress-dot');
    if (dot && track.contains(dot)) {
      jumpToQuestion(Number(dot.dataset.index || 0));
      return;
    }
    const dots = Array.from(track.querySelectorAll('.progress-dot'));
    if (!dots.length) return;
    const nearest = dots.reduce((best, item) => {
      const rect = item.getBoundingClientRect();
      const distance = Math.abs(event.clientY - (rect.top + rect.height / 2));
      return !best || distance < best.distance ? { item, distance } : best;
    }, null);
    if (nearest) jumpToQuestion(Number(nearest.item.dataset.index || 0));
  };

  qs.forEach((q, i) => {

    const dot = document.createElement('div');

    dot.className = 'progress-dot' + (i === activeIdx ? ' active' : '');
    dot.dataset.index = String(i);

    const fullText = (q.content || '').replace(/\s+/g, ' ').trim();

    dot.dataset.tip = fullText || ('问题 ' + (i + 1));

    dot.title = fullText.slice(0, 80);

    dot.addEventListener('mouseenter', (e) => showProgressTip(dot, e));

    dot.addEventListener('mouseleave', hideProgressTip);

    dot.addEventListener('click', (event) => {
      event.stopPropagation();
      jumpToQuestion(i);
    });

    track.appendChild(dot);

  });

}

let _progressTipEl = null;

function showProgressTip(dot, e) {

  if (!_progressTipEl) {

    _progressTipEl = document.createElement('div');

    _progressTipEl.className = 'progress-tip';

    document.body.appendChild(_progressTipEl);

  }

  const text = dot.dataset.tip || '';

  _progressTipEl.textContent = text;

  const dotRect = dot.getBoundingClientRect();

  _progressTipEl.style.visibility = 'hidden';

  _progressTipEl.classList.add('show');

  requestAnimationFrame(() => {

    const tipRect = _progressTipEl.getBoundingClientRect();

    let top = dotRect.top + dotRect.height / 2 - tipRect.height / 2;

    let right = window.innerWidth - dotRect.left + 10;

    if (top < 8) top = 8;

    if (top + tipRect.height > window.innerHeight - 8) top = window.innerHeight - tipRect.height - 8;

    if (right < 0 || right + tipRect.width > window.innerWidth) {

      _progressTipEl.style.top = top + 'px';

      _progressTipEl.style.left = (dotRect.left - tipRect.width - 10) + 'px';

    } else {

      _progressTipEl.style.top = top + 'px';

      _progressTipEl.style.right = right + 'px';

    }

    _progressTipEl.style.visibility = 'visible';

  });

}

function hideProgressTip() {

  if (_progressTipEl) _progressTipEl.classList.remove('show');

}

function jumpToQuestion(idx) {

  const userMsgs = document.querySelectorAll('.msg.user');

  if (idx < 0 || idx >= userMsgs.length) return;

  const target = userMsgs[idx];

  target.scrollIntoView({ behavior: 'smooth', block: 'center' });

  const highlightTarget = target.querySelector('.content') || target;

  document.querySelectorAll('.progress-highlight').forEach(el => el.classList.remove('progress-highlight'));

  highlightTarget.classList.add('progress-highlight');

  setTimeout(() => { highlightTarget.classList.remove('progress-highlight'); }, 1200);

  document.querySelectorAll('.progress-dot').forEach((d, i) => d.classList.toggle('active', i === idx));

}

function updateHeaderTitle() {

  const titleEl = document.getElementById('headerTitle');

  if (!titleEl) return;

  const sess = currentSession();

  const pendingProject = !sess && state.currentProjectId
    ? state.projects.find(project => project.id === state.currentProjectId)
    : null;

  titleEl.textContent = sess
    ? (sess.title || '新对话')
    : (pendingProject ? pendingProject.name : '新对话');

}

function attachCodeCopy(root) {

  root.querySelectorAll('pre').forEach(pre => {

    if (pre.querySelector('.copy-code-btn')) return;

    const btn = document.createElement('button');

    btn.className = 'copy-code-btn';

    btn.textContent = '复制';

    btn.onclick = (e) => {

      e.stopPropagation();

      const code = pre.querySelector('code') ? pre.querySelector('code').innerText : pre.innerText;

      navigator.clipboard.writeText(code).then(() => {

        btn.textContent = '已复制';

        setTimeout(() => btn.textContent = '复制', 1500);

      });

    };

    pre.appendChild(btn);

  });

}

function attachMoreMenu(div, m) {

  const menu = div.querySelector('.msg-more-menu');

  if (!menu) return;

  menu.querySelectorAll('button').forEach(btn => {

    btn.onclick = (e) => {

      e.stopPropagation();

      const action = btn.dataset.action;

      menu.classList.remove('open');

      if (action === 'reply') replyToMessage(m);

      else if (action === 'share') shareSingleMessage(m);

    };

  });

}

function toggleMoreMenu(event, btn) {

  event.stopPropagation();

  const msg = btn.closest('.msg');

  if (!msg) return;

  const menu = msg.querySelector('.msg-more-menu');

  if (!menu) return;

  document.querySelectorAll('.msg-more-menu.open').forEach(m => { if (m !== menu) m.classList.remove('open'); });

  menu.classList.toggle('open');

}

async function copyAssistantMessage(messageIndex, button) {
  const session = currentSession();
  const message = session?.messages?.[messageIndex];
  if (!message) return;
  try {
    await navigator.clipboard.writeText(message.content || '');
    button.classList.add('is-success');
    button.setAttribute('aria-label', '已复制');
    showToast('已复制回复');
    setTimeout(() => {
      button.classList.remove('is-success');
      button.setAttribute('aria-label', '复制');
    }, 1400);
  } catch (e) {
    showToast('复制失败');
  }
}

function toggleAssistantLike(messageIndex, button) {
  const session = currentSession();
  const message = session?.messages?.[messageIndex];
  if (!message) return;
  message.liked = !message.liked;
  button.classList.toggle('active', message.liked);
  button.setAttribute('aria-pressed', String(message.liked));
  saveSessions();
  showToast(message.liked ? '已点赞' : '已取消点赞');
}

function replyToMessage(m) {

  const input = document.getElementById('input');

  const ref = m.content.length > 80 ? m.content.slice(0, 80) + '…' : m.content;

  const quoted = ref.split('\n').map(l => '> ' + l).join('\n');

  input.value = (input.value ? input.value + '\n\n' : '') + quoted + '\n';

  autoGrow(input);

  updateSendBtn();

  input.focus();

  showToast('已插入引用');

}

async function shareSingleMessage(m) {

  const text = '[AI] ' + m.content;

  if (navigator.share) {

    try { await navigator.share({ text }); } catch (e) {}

  } else {

    navigator.clipboard.writeText(text).then(() => showToast('已复制'));

  }

}

function regenerateMessage(messageIndex) {
  const session = currentSession();
  if (!session || !session.messages[messageIndex]) return;
  if (state.streaming) { showToast('生成中，请先停止'); return; }
  if (messageIndex !== session.messages.length - 1) {
    showToast('只能重新生成最新回复');
    return;
  }
  let userIndex = messageIndex - 1;
  while (userIndex >= 0 && session.messages[userIndex].role !== 'user') userIndex -= 1;
  if (userIndex < 0) return;
  const prompt = session.messages[userIndex].content;
  session.messages.splice(userIndex);
  saveSessions();
  renderMessages();
  send(prompt, true);
}

function regenerate() {
  const session = currentSession();
  if (!session) return;
  regenerateMessage(session.messages.length - 1);
}

function updateMsgCount() {

  const sess = currentSession();

  const el = document.getElementById('msgCount');

  if (el) el.textContent = sess ? sess.messages.length : 0;

}

function scrollToBottom() {

  const main = document.getElementById('main');

  requestAnimationFrame(() => { main.scrollTop = main.scrollHeight; });

}
