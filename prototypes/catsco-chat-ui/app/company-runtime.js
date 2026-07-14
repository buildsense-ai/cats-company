const CompanyRuntime = (() => {
  const COMPANY_WS_URL = 'wss://app.catsco.cc/v0/channels';
  const pendingReplies = new Map();
  let profile = null;
  let socket = null;
  let reconnectTimer = null;
  let reconnectAttempt = 0;
  let messageId = 0;

  function currentUserId() {
    if (profile?.id || profile?.uid) return Number(profile.id || profile.uid);
    try {
      const cached = JSON.parse(localStorage.getItem('catsco-profile') || 'null');
      return Number(cached?.id || cached?.uid || 0);
    } catch (_) {
      return 0;
    }
  }

  function setProfile(nextProfile) {
    profile = nextProfile || null;
  }

  function parseUid(value) {
    if (typeof value === 'number') return value;
    const match = String(value || '').match(/(\d+)/);
    return match ? Number(match[1]) : 0;
  }

  function p2pTopic(left, right) {
    const ids = [Number(left), Number(right)].filter(Number.isFinite).sort((a, b) => a - b);
    return ids.length === 2 ? `p2p_${ids[0]}_${ids[1]}` : '';
  }

  function remoteSessionId(topicId) {
    return 'company-topic-' + String(topicId).replace(/[^A-Za-z0-9_-]/g, '_');
  }

  function messageText(message) {
    const directContent = typeof message?.content === 'string' ? message.content : '';
    if (message?.content && typeof message.content === 'object') {
      if (typeof message.content.text === 'string') return message.content.text;
      try { return JSON.stringify(message.content); } catch (_) { return ''; }
    }
    const blocks = Array.isArray(message?.content_blocks) ? message.content_blocks : [];
    const blockText = blocks.map(block => {
      if (block?.text || block?.content) return block.text || block.content;
      const payload = block?.payload || {};
      const name = payload.name || payload.file_name || payload.filename || '';
      if (block?.type === 'image') return '[图片]' + (name ? ' ' + name : '');
      if (block?.type === 'file') return '[文件]' + (name ? ' ' + name : '');
      return '';
    }).filter(Boolean).join('\n');
    return blockText || directContent;
  }

  function isRuntimeOnly(message) {
    const type = message?.type || message?.msg_type || '';
    return ['runtime_plan', 'stream_delta', 'stream_cancel'].includes(type);
  }

  function mapMessage(message) {
    if (!message || isRuntimeOnly(message)) return null;
    const content = messageText(message).trim();
    if (!content || content.startsWith('[Working]')) return null;
    const fromUid = Number(message.from_uid || parseUid(message.from));
    return {
      role: fromUid && fromUid === currentUserId() ? 'user' : 'assistant',
      content,
      time: Date.parse(message.created_at || '') || Date.now(),
      remoteMessageId: Number(message.seq_id || message.id || 0),
      remoteFromUid: fromUid,
    };
  }

  function conversationSession(conversation, previous = {}) {
    const topicId = String(conversation.id || previous.remoteTopicId || '');
    return {
      ...previous,
      id: previous.id || remoteSessionId(topicId),
      source: 'company',
      remoteTopicId: topicId,
      remoteFriendId: conversation.friend_id || previous.remoteFriendId || 0,
      remoteGroupId: conversation.group_id || previous.remoteGroupId || 0,
      remoteIsGroup: conversation.is_group === true,
      remoteIsBot: conversation.is_bot === true,
      remoteHasBot: conversation.has_bot === true,
      remoteLatestSeq: Number(conversation.latest_seq || previous.remoteLatestSeq || 0),
      title: conversation.name || previous.title || '公司任务',
      preview: conversation.preview || previous.preview || '',
      createdAt: Date.parse(conversation.created_at || conversation.last_time || '') || previous.createdAt || Date.now(),
      messages: Array.isArray(previous.messages) ? previous.messages : [],
    };
  }

  async function syncConversations() {
    if (!CatsCompanyApi.isAuthenticated()) return [];
    const data = await CatsCompanyApi.getConversations();
    const conversations = Array.isArray(data?.conversations) ? data.conversations : [];
    const existing = new Map(
      (state.sessions || []).filter(session => session.source === 'company')
        .map(session => [String(session.remoteTopicId), session])
    );
    const remote = conversations.map(item => conversationSession(item, existing.get(String(item.id)) || {}));
    const liveTopics = new Set(remote.map(session => String(session.remoteTopicId)));
    const retainedPending = (state.sessions || []).filter(session => (
      session.source === 'company' && session.remotePending && !liveTopics.has(String(session.remoteTopicId))
    ));
    const local = (state.sessions || []).filter(session => session.source !== 'company');
    state.sessions = [...remote, ...retainedPending, ...local];

    conversations.filter(item => item.is_group).forEach(item => {
      const group = (state.groups || []).find(candidate => String(candidate.remoteId) === String(item.group_id));
      if (group) group.topicId = item.id;
    });

    if (!state.sessions.some(session => session.id === state.currentId)) {
      state.currentId = state.sessions[0]?.id || null;
      if (state.currentId) localStorage.setItem('mc-current', state.currentId);
    }
    saveSessions();
    return remote;
  }

  async function loadSession(session, force = false) {
    if (!session?.remoteTopicId || session.source !== 'company') return session;
    if (session.remoteLoaded && !force && session.remoteLoadedSeq === session.remoteLatestSeq) return session;
    const data = await CatsCompanyApi.getMessages(session.remoteTopicId, 100, 0, true);
    const mapped = (data?.messages || []).map(mapMessage).filter(Boolean)
      .sort((a, b) => (a.remoteMessageId || a.time) - (b.remoteMessageId || b.time));
    session.messages = mapped;
    session.remoteLoaded = true;
    session.remoteLoadedSeq = mapped.at(-1)?.remoteMessageId || session.remoteLatestSeq || 0;
    saveSessions();
    return session;
  }

  async function ensureTarget(session) {
    if (!CatsCompanyApi.isAuthenticated()) return null;
    const agent = (state.agents || []).find(item => item.id === state.agentCurrentId && item.source === 'company');
    if (
      session?.source === 'company'
      && session.remoteTopicId
      && (!agent?.uid || Number(session.remotePeerUid) === Number(agent.uid))
    ) return session;
    if (!agent?.uid) return null;
    const opened = await CatsCompanyApi.openAgent(agent.uid);
    const topicId = String(opened?.topic || opened?.agent?.topic_id || agent.topicId || '');
    if (!topicId) throw new Error('公司后端没有返回 Agent 会话编号');
    session.source = 'company';
    session.remoteTopicId = topicId;
    session.remotePeerUid = Number(agent.uid);
    session.remoteIsBot = true;
    session.remotePending = true;
    session.agentId = agent.id;
    session.agentName = agent.name;
    saveSessions();
    connect();
    return session;
  }

  async function openContact(item, kind) {
    if (!item || item.source !== 'company') return false;
    const uid = currentUserId();
    let topicId = item.topicId || '';
    if (!topicId && kind === 'group' && item.remoteId) topicId = 'grp_' + item.remoteId;
    if (!topicId && (kind === 'friend' || kind === 'agent')) {
      topicId = p2pTopic(uid, item.uid || item.remoteId);
    }
    if (!topicId) return false;
    let session = (state.sessions || []).find(candidate => candidate.remoteTopicId === topicId);
    if (!session) {
      session = conversationSession({
        id: topicId,
        name: item.name,
        friend_id: kind === 'friend' ? item.remoteId : 0,
        group_id: kind === 'group' ? item.remoteId : 0,
        is_group: kind === 'group',
        is_bot: kind === 'agent',
        has_bot: kind === 'agent',
      });
      session.remotePending = true;
      state.sessions.unshift(session);
      saveSessions();
    }
    switchSession(session.id);
    return true;
  }

  function wsSend(payload) {
    if (socket?.readyState === WebSocket.OPEN) socket.send(JSON.stringify(payload));
  }

  function connect() {
    const token = CatsCompanyApi.getToken();
    if (!token || socket?.readyState === WebSocket.OPEN || socket?.readyState === WebSocket.CONNECTING) return;
    clearTimeout(reconnectTimer);
    socket = new WebSocket(COMPANY_WS_URL + '?token=' + encodeURIComponent(token));
    socket.onopen = () => {
      reconnectAttempt = 0;
      wsSend({ hi: { id: String(++messageId), ver: '0.1.0' } });
      wsSend({ get: { id: String(++messageId), topic: 'me', what: 'online' } });
    };
    socket.onmessage = event => {
      try { handleSocketMessage(JSON.parse(event.data)); } catch (_) {}
    };
    socket.onclose = () => {
      socket = null;
      if (!CatsCompanyApi.isAuthenticated()) return;
      const delay = Math.min(30000, 1000 * (2 ** reconnectAttempt++));
      reconnectTimer = setTimeout(connect, delay);
    };
    socket.onerror = () => {};
  }

  function disconnect() {
    clearTimeout(reconnectTimer);
    reconnectAttempt = 0;
    if (socket) {
      socket.onclose = null;
      socket.close();
      socket = null;
    }
  }

  function handleSocketMessage(message) {
    if (Array.isArray(message?.meta?.sub)) {
      if (typeof applyCompanyOnlineStatus === 'function' && applyCompanyOnlineStatus(message)) renderSidebar();
      return;
    }
    if (message?.pres) {
      const uid = parseUid(message.pres.src);
      if (uid && typeof applyCompanyOnlineStatus === 'function') {
        const changed = applyCompanyOnlineStatus({ users: [{ uid, online: message.pres.what === 'on' }] });
        if (changed) renderSidebar();
      }
      return;
    }
    const data = message?.data;
    if (!data?.topic) return;
    const topicId = String(data.topic);
    const fromUid = parseUid(data.from);
    if (fromUid === currentUserId()) return;
    const pending = pendingReplies.get(topicId);
    const type = data.type || data.msg_type || data.metadata?.stream_event || '';
    if (type === 'stream_delta') {
      pending?.onDelta?.(messageText(data));
      return;
    }
    if (type === 'runtime_plan') {
      pending?.onProgress?.('公司 Agent 已规划任务，正在执行');
      return;
    }
    if (type === 'stream_cancel') return;
    const mapped = mapMessage({
      ...data,
      id: data.seq_id || data.seq,
      seq_id: data.seq_id || data.seq,
      from_uid: fromUid,
      created_at: new Date().toISOString(),
    });
    if (!mapped) return;
    if (pending && mapped.remoteMessageId > pending.afterSeq) {
      pending.resolve(mapped);
      return;
    }
    const session = (state.sessions || []).find(item => item.remoteTopicId === topicId);
    if (!session) {
      syncConversations().then(() => renderSidebar()).catch(() => {});
      return;
    }
    if (!session.messages.some(item => item.remoteMessageId && item.remoteMessageId === mapped.remoteMessageId)) {
      session.messages.push(mapped);
      session.remoteLatestSeq = Math.max(session.remoteLatestSeq || 0, mapped.remoteMessageId || 0);
      saveSessions();
      renderSidebar();
      if (session.id === state.currentId) renderMessages();
    }
  }

  async function waitForReply(topicId, afterSeq, signal, callbacks = {}) {
    let pollTimer;
    let timeoutTimer;
    return new Promise((resolve, reject) => {
      const finish = (fn, value) => {
        clearTimeout(pollTimer);
        clearTimeout(timeoutTimer);
        pendingReplies.delete(topicId);
        signal?.removeEventListener('abort', onAbort);
        fn(value);
      };
      const onAbort = () => finish(reject, new DOMException('Aborted', 'AbortError'));
      const poll = async () => {
        try {
          const data = await CatsCompanyApi.getMessages(topicId, 30, 0, true);
          const reply = (data?.messages || []).map(mapMessage).filter(item => (
            item && item.role === 'assistant' && (item.remoteMessageId || 0) > afterSeq
          )).at(-1);
          if (reply) return finish(resolve, reply);
        } catch (_) {}
        pollTimer = setTimeout(poll, 1400);
      };
      pendingReplies.set(topicId, {
        afterSeq,
        onDelta: callbacks.onDelta,
        onProgress: callbacks.onProgress,
        resolve: value => finish(resolve, value),
      });
      signal?.addEventListener('abort', onAbort, { once: true });
      timeoutTimer = setTimeout(() => finish(reject, new Error('公司 Agent 响应超时，请稍后重试')), 180000);
      pollTimer = setTimeout(poll, 900);
    });
  }

  async function sendTask(session, text, signal, callbacks = {}, attachments = []) {
    const target = await ensureTarget(session);
    if (!target) return null;
    connect();
    const contentBlocks = [];
    if (text) contentBlocks.push({ type: 'text', text });
    attachments.forEach(item => {
      if (!item?.payload) return;
      contentBlocks.push({ type: item.type === 'image' ? 'image' : 'file', payload: item.payload });
    });
    const content = attachments.length
      ? {
          type: 'text',
          content: text || ('[附件] ' + attachments.map(item => item.name).join('、')),
          content_blocks: contentBlocks,
        }
      : text;
    const result = await CatsCompanyApi.sendMessage(target.remoteTopicId, content);
    const sentSeq = Number(result?.seq_id || result?.id || 0);
    target.remotePending = false;
    target.remoteLatestSeq = Math.max(target.remoteLatestSeq || 0, sentSeq);
    saveSessions();
    if (!target.remoteIsBot && !target.remoteHasBot) return { sentOnly: true, sentSeq };
    const reply = await waitForReply(target.remoteTopicId, sentSeq, signal, callbacks);
    return { reply, sentSeq };
  }

  function cancel(topicId) {
    if (!topicId) return;
    wsSend({ pub: {
      id: String(++messageId),
      topic: topicId,
      type: 'stream_cancel',
      msg_type: 'stream_cancel',
      content: '',
      metadata: {
        stream_id: 'cancel-' + Date.now(),
        stream_event: 'cancel',
        control: 'interrupt',
      },
    } });
  }

  function collectModelNames(value, result = new Set()) {
    if (!value || typeof value !== 'object') return result;
    if (Array.isArray(value)) {
      value.forEach(item => collectModelNames(item, result));
      return result;
    }
    if (typeof value.model === 'string') result.add(value.model);
    if (Array.isArray(value.allowed_models)) value.allowed_models.forEach(model => result.add(String(model)));
    if (value.totals_by_model && typeof value.totals_by_model === 'object') {
      Object.keys(value.totals_by_model).forEach(model => result.add(model));
    }
    Object.values(value).forEach(item => collectModelNames(item, result));
    return result;
  }

  async function syncModels() {
    const [configResult, commercialResult] = await Promise.allSettled([
      CatsCompanyApi.getRelayConfig(),
      CatsCompanyApi.getRelayCommercial(),
    ]);
    const defaultModel = configResult.status === 'fulfilled' ? configResult.value?.default_model : '';
    const names = commercialResult.status === 'fulfilled'
      ? [...collectModelNames(commercialResult.value)]
      : [];
    if (defaultModel) names.unshift(defaultModel);
    if (typeof setAvailableModels === 'function' && names.length) setAvailableModels([...new Set(names)], defaultModel);
    return { defaultModel, names };
  }

  return {
    setProfile,
    syncConversations,
    syncModels,
    loadSession,
    ensureTarget,
    openContact,
    sendTask,
    connect,
    disconnect,
    cancel,
  };
})();
