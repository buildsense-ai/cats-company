function autoGrow(el) {

  el.style.height = 'auto';

  el.style.height = Math.min(el.scrollHeight, 200) + 'px';

}

function updateSendBtn() {

  const txt = document.getElementById('input').value.trim();

  const btn = document.getElementById('sendBtn');

  const box = document.querySelector('.input-box');

  const hasText = !!txt || (state.pendingAttachments || []).length > 0;

  if (box) box.classList.toggle('has-content', hasText && !state.streaming);

  if (state.streaming) {

    btn.innerHTML = '<svg viewBox="0 0 24 24" width="14" height="14" fill="currentColor"><rect x="6" y="6" width="12" height="12" rx="1"/></svg>';

    btn.classList.add('stop');

    btn.classList.remove('ready');

    btn.disabled = false;

    btn.setAttribute('aria-label', '\u505c\u6b62\u751f\u6210');

    btn.title = '停止生成';

  } else {

    btn.innerHTML = '<svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M12 19V5M5 12l7-7 7 7"/></svg>';

    btn.classList.remove('stop');

    btn.classList.toggle('ready', hasText);

    btn.disabled = !hasText;

    btn.setAttribute('aria-label', '\u53d1\u9001');

    btn.title = '发送 (Ctrl+回车)';

  }

}

function setBusy(busy) { document.getElementById('input').disabled = busy; }

function sendOrStop() {

  if (state.streaming) { stopStream(); return; }

  const txt = document.getElementById('input').value.trim();

  if (!txt && !(state.pendingAttachments || []).length) return;

  send(txt, false);

}

async function send(text, isRegen) {

  const input = document.getElementById('input');

  let sess = currentSession();

  if (!sess) {
    await newChat(true, state.currentProjectId);
    sess = currentSession();
  }

  if (!sess) return;

  const attachments = (state.pendingAttachments || []).splice(0);
  const attachmentSummary = attachments.length
    ? '[附件] ' + attachments.map(item => item.name).join('、')
    : '';
  const displayText = text || attachmentSummary;

  if (sess.messages.length === 0) sess.title = displayText.slice(0, 24) + (displayText.length > 24 ? '…' : '');

  sess.messages.push({ role: 'user', content: displayText, attachments, time: Date.now() });

  const placeholder = {
    role: 'assistant',
    content: '',
    time: Date.now(),
    streaming: true,
    taskProcess: createTaskProcess(displayText)
  };

  sess.messages.push(placeholder);

  const placeholderIdx = sess.messages.length - 1;

  input.value = '';

  autoGrow(input);

  renderSidebar();

  renderMessages();

  scrollToBottom();

  state.streaming = new AbortController();

  updateSendBtn();

  setBusy(true);

  saveSessions();

  try {

    setTaskProcessStage(placeholder, 'connect', {
      detail: '请求已整理，正在连接模型服务',
      technical: { key: 'request', label: '请求地址', value: '/api/chat' }
    });

    renderMessages();

    const companyTarget = await CompanyRuntime.ensureTarget(sess);
    if (companyTarget) {
      let streamedText = '';
      setTaskTechnical(placeholder, 'service', '\u6d88\u606f\u670d\u52a1', 'CatsCo \u516c\u53f8\u540e\u7aef');
      const result = await CompanyRuntime.sendTask(sess, text, state.streaming.signal, {
        onProgress(detail) {
          setTaskProcessStage(placeholder, 'generate', { detail });
          renderMessages();
        },
        onDelta(delta) {
          if (!delta) return;
          if (!streamedText) setTaskProcessStage(placeholder, 'organize', { detail: '\u6b63\u5728\u63a5\u6536\u516c\u53f8 Agent \u7684\u56de\u590d' });
          streamedText += delta;
          placeholder.content = streamedText;
          setTaskTechnical(placeholder, 'output', '\u5f53\u524d\u56de\u7b54\u957f\u5ea6', streamedText.length + ' \u4e2a\u5b57\u7b26');
          renderMessages();
          scrollToBottom();
        },
      }, attachments);

      if (result?.sentOnly) {
        sess.messages.splice(placeholderIdx, 1);
        showToast('\u6d88\u606f\u5df2\u53d1\u9001\u5230\u516c\u53f8\u4f1a\u8bdd');
      } else if (result?.reply) {
        placeholder.streaming = false;
        placeholder.content = result.reply.content || streamedText;
        placeholder.remoteMessageId = result.reply.remoteMessageId;
        placeholder.remoteFromUid = result.reply.remoteFromUid;
        completeTaskProcess(placeholder, {
          detail: '\u516c\u53f8 Agent \u56de\u590d\u5df2\u5b8c\u6574\u63a5\u6536',
          outputLength: placeholder.content.length,
        });
      }
      sess.remoteLoaded = true;
      sess.remoteLoadedSeq = Math.max(sess.remoteLoadedSeq || 0, result?.reply?.remoteMessageId || result?.sentSeq || 0);
      return;
    }

    const history = sess.messages.filter(m => m.role !== 'error').slice(0, -1).map(m => ({ role: m.role, content: m.content }));

    if (attachments.length) showToast('本地模型接口暂不支持附件，已仅发送附件名称');

    const r = await ChatApi.sendChat({
      sessionId: sess.id,
      messages: history,
      model: state.model,
      signal: state.streaming.signal,
    });

    if (!r.ok) {

      const t = await r.text();

      let msg = r.status === 404
        ? '本地聊天服务未启动，请运行 CatsCo 后端后重试'
        : 'HTTP ' + r.status;

      try {
        const j = JSON.parse(t);
        if (j.error) msg = j.error;
        else if (j.detail && r.status !== 404) msg += ' - ' + j.detail.slice(0, 200);
      } catch {}

      throw new AppErrors.AppError(msg, {
        status: r.status,
        data: t,
        context: 'send-message'
      });

    }

    setTaskProcessStage(placeholder, 'generate', {
      detail: '服务已响应，正在生成内容',
      technical: { key: 'connection', label: '连接状态', value: '已连接' }
    });

    renderMessages();

    const reader = r.body.getReader();

    const decoder = new TextDecoder();

    let buffer = '';

    let fullText = '';

    let hasFirstChunk = false;

    let sawDone = false;

    while (true) {

      const { done, value } = await reader.read();

      if (done) break;

      buffer += decoder.decode(value, { stream: true });

      const lines = buffer.split('\n');

      buffer = lines.pop();

      for (const line of lines) {

        if (!line.startsWith('data: ')) continue;

        const payload = line.slice(6).trim();

        if (payload === '[DONE]' || payload.startsWith('{"[DONE]"')) continue;

        let obj;
        try {
          obj = JSON.parse(payload);
        } catch (parseError) {
          console.warn('stream parse error', parseError, payload);
          continue;
        }

        if (obj.error) {
          const streamMessage = typeof obj.error === 'string'
            ? obj.error
            : (obj.error.message || obj.message || '模型服务返回异常');
          throw new AppErrors.AppError(streamMessage, {
            status: Number(obj.status || obj.error?.status || 0),
            code: obj.code || obj.error?.code || '',
            data: obj,
            context: 'message-stream'
          });
        }

        if (obj.sessionId) {
          replaceSessionId(sess.id, obj.sessionId);
          sess.id = obj.sessionId;
          if (obj.model) setTaskTechnical(placeholder, 'model', '使用模型', obj.model);
        }

        if (obj.type === 'progress') {
          if (obj.stage === 'connected') {
            setTaskProcessStage(placeholder, 'generate', { detail: obj.detail || '模型服务已连接，正在处理任务' });
          } else if (obj.stage === 'first_content') {
            setTaskProcessStage(placeholder, 'organize', { detail: obj.detail || '已开始收到内容，正在整理回答' });
          } else if (obj.stage === 'finalizing') {
            setTaskProcessStage(placeholder, 'verify', { detail: obj.detail || '内容接收完成，正在确认结果' });
          }
        }

        if (obj.content) {
          if (!hasFirstChunk) {
            hasFirstChunk = true;
            setTaskProcessStage(placeholder, 'organize', {
              detail: '已开始收到内容，正在整理为清晰回答',
              technical: { key: 'first-content', label: '首段内容', value: '已收到' }
            });
          }

          fullText += obj.content;
          sess.messages[placeholderIdx].content = fullText;
          setTaskTechnical(placeholder, 'output', '当前回答长度', fullText.length + ' 个字符');
          renderMessages();
          scrollToBottom();
        }

        if (obj.done) {
          sawDone = true;
          setTaskProcessStage(placeholder, 'verify', { detail: '内容已接收，正在确认回答是否完整' });
        }

      }

    }

    sess.messages[placeholderIdx].streaming = false;

    if (!fullText) {

      sess.messages[placeholderIdx].role = 'error';

      sess.messages[placeholderIdx].content = '（AI 没有返回内容）';

      sess.messages[placeholderIdx].errorKind = 'server';

      sess.messages[placeholderIdx].errorRetryable = true;

      failTaskProcess(sess.messages[placeholderIdx], '模型服务没有返回内容，请重新尝试');

    } else {

      completeTaskProcess(sess.messages[placeholderIdx], {
        detail: sawDone ? '回答已完整接收' : '连接已结束，回答内容已保留',
        outputLength: fullText.length
      });

    }

  } catch (e) {

    if (e.name === 'AbortError') {

      sess.messages[placeholderIdx].streaming = false;

      stopTaskProcess(sess.messages[placeholderIdx], !!sess.messages[placeholderIdx].content);

      if (!sess.messages[placeholderIdx].content) {

        sess.messages[placeholderIdx].role = 'error';

        sess.messages[placeholderIdx].content = '（已停止）';

        sess.messages[placeholderIdx].errorKind = 'cancelled';

        sess.messages[placeholderIdx].errorRetryable = false;

      } else {

        sess.messages[placeholderIdx].content += '\n\n_[已停止]_';

      }

    } else {

      sess.messages[placeholderIdx].streaming = false;

      sess.messages[placeholderIdx].role = 'error';

      const userError = /Failed to fetch|NetworkError|无法连接/.test(e.message)
        ? (sess.source === 'company'
          ? '无法连接到 CatsCo 公司服务，请检查账号连接和网络状态'
          : '无法连接到本地服务，请确认 CatsCo 后端已经启动')
        : e.message;

      sess.messages[placeholderIdx].content = '无法完成：' + userError;

      const errorView = AppErrors.presentation(e, userError);
      sess.messages[placeholderIdx].errorKind = errorView.kind;
      sess.messages[placeholderIdx].errorRetryable = errorView.retryable;
      sess.messages[placeholderIdx].content = errorView.title + '：' + errorView.message;
      failTaskProcess(sess.messages[placeholderIdx], errorView.message);

    }

  } finally {

    state.streaming = null;

    setBusy(false);

    updateSendBtn();

    renderMessages();

    renderSidebar();

    saveSessions();

    updateHeaderTitle();

  }

}

function stopStream() {
  if (!state.streaming) return;
  const session = currentSession();
  if (session?.source === 'company') CompanyRuntime.cancel(session.remoteTopicId);
  state.streaming.abort();
}

function onKey(e) {

  if (e.key === 'Enter' && !e.shiftKey) {

    e.preventDefault();

    if (state.streaming) return;

    sendOrStop();

  } else if (e.ctrlKey && e.key === 'b') {

    e.preventDefault();

    toggleSidebar();

  }

}
