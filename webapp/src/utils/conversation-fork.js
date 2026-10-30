function uid(value) {
  const text = String(value ?? '').replace(/^usr/, '');
  return /^\d+$/.test(text) && Number(text) > 0 ? text.replace(/^0+/, '') : '';
}

// The anchor for a bounded copy has to be the last message of a complete turn.
//
// A reply longer than the segment limit is stored as several messages, so one
// turn can read [user, agent, agent, agent]. The server refuses an anchor that
// is not a turn's end, because the runtime cuts whole turns: an anchor in the
// middle would copy less visible history than the model keeps in context.
//
// The displayed group already merges those segments into one row, and the
// merged message is the group's last original message. So the anchor is that
// message's id, and it is zero whenever the group is not an agent reply at all.
export function forkAnchorMessageId(group) {
  if (!group || group.type === 'working') return 0;
  if (!group.assistantAuthored) return 0;
  const sourceMessages = group.sourceMessages || [group.message];
  const last = sourceMessages[sourceMessages.length - 1];
  // A streaming placeholder has no server-side identity yet: its id is
  // `stream:<id>` and its seq_id is a wall-clock timestamp, not a message row
  // id. Sending it as an anchor would make the server look for a message that
  // does not exist and answer "the conversation changed" — the one misleading
  // message this split was meant to remove.
  if (!last || last._streaming) return 0;
  const id = Number(last.seq_id || last.id || 0);
  // Real message ids are row ids, far below any timestamp. A value this large
  // is a local identifier the server cannot resolve.
  if (!Number.isSafeInteger(id) || id <= 0 || id > 1e12) return 0;
  return id;
}

// Whether the copy point sits inside a reply that is still being produced.
//
// The server accepts an anchor only when it closes a turn, which it decides by
// looking at the next stored message: if that is still the agent talking, the
// reply is not finished. The menu can decide the same thing from the same list,
// so it does not have to guess or to wait for a rejection. Offering the entry
// regardless meant the user clicked, waited, and then read a refusal about a
// position we already knew was mid-reply.
//
// This is deliberately the adjacency rule and nothing cleverer: the displayed
// groups merge segments and would hide the very boundary being checked.
export function forkAnchorIsMidTurn(messages, anchorId, agentUid) {
  const id = Number(anchorId) || 0;
  if (!id || !Array.isArray(messages)) return false;
  const index = messages.findIndex(message => Number(message?.seq_id || message?.id || 0) === id);
  if (index < 0) return false;
  const next = messages[index + 1];
  if (!next) return false;
  const agent = String(agentUid || '') || String(messages[index]?.from_uid ?? '');
  return Boolean(agent) && String(next.from_uid ?? '') === agent;
}

// The anchor errors are a verdict about the chosen point, not about whether the
// request got through. Retrying the same point gives the same verdict every time,
// so an entry that offers "retry" for them is a button that cannot succeed: the
// only way forward is to pick a different point.
const anchorErrorCodes = new Set([
  'session_fork_anchor_not_turn_end',
  'session_fork_anchor_compacted',
]);

export function isForkAnchorError(error) {
  return anchorErrorCodes.has(String(error || ''));
}

export function canForkConversation({ topic, userId, agents = [], members = [] }) {
  const actor = uid(userId);
  if (!actor) return false;
  const accessibleAgent = (id) => agents.some(agent => uid(agent.uid || agent.id) === id
    && (agent.is_owner === true || agent.relation === 'owner' || agent.relation === 'friend'));
  const direct = /^p2p_(\d+)_(\d+)$/.exec(topic || '');
  if (direct) {
    const participants = direct.slice(1).map(uid);
    return participants.includes(actor) && participants[0] !== participants[1]
      && accessibleAgent(participants.find(id => id !== actor));
  }
  if (!/^grp_\d+$/.test(topic || '')) return false;
  if (members.length !== 2) return false;
  const self = members.find(member => uid(member.user_id) === actor);
  const peer = members.find(member => uid(member.user_id) !== actor);
  return Boolean(self && peer && accessibleAgent(uid(peer.user_id)));
}

export function normalizeForkRecord(record, { requestId, sourceTopic, targetTopic } = {}) {
  if (!record || typeof record.request_id !== 'string' || !record.request_id
    || typeof record.source_topic_id !== 'string' || !record.source_topic_id
    || !/^grp_[1-9]\d*$/.test(record.target_topic_id || '')
    || !['pending', 'ready', 'failed'].includes(record.state)
    || (requestId && record.request_id !== requestId)
    || (sourceTopic && record.source_topic_id !== sourceTopic)
    || (targetTopic && record.target_topic_id !== targetTopic)) {
    throw new Error('会话复制状态无效，请重试查询。');
  }
  return record;
}

export function forkTargetTopic(record, conversations = []) {
  normalizeForkRecord(record);
  if (record.state !== 'ready') throw new Error('新会话尚未就绪');
  const summary = conversations.find(item => (item.id || item.topic_id) === record.target_topic_id);
  return {
    topicId: record.target_topic_id,
    groupId: Number(record.target_topic_id.slice(4)),
    name: summary?.name || '复制的会话',
    avatar_url: summary?.avatar_url || '',
    isGroup: true,
    isAgentTask: true,
    hasBot: true,
    memberCount: 2,
  };
}

export function describeForkError(error) {
  const text = String(error || '');
  const messages = {
    session_fork_source_interrupted: '原会话还有中断的任务，请先恢复或结束该任务，再重新创建副本。',
    session_fork_initialization_failed: '无法读取或复制会话上下文，请检查运行该 Agent 的设备或云端记录后重新创建。',
    session_fork_source_busy: '原会话正在执行，请等待完成后重试。',
    session_fork_agent_offline: 'Agent 当前离线，请连接后重试。',
    session_fork_forbidden: '只能复制你参与的一人一 Agent 会话，请确认该 Agent 仍在好友列表中。',
    session_fork_request_conflict: '请求标识与原请求不一致，请重新查询原复制结果。',
    session_fork_source_unavailable: '原会话不可用或已变化，请检查原会话后重试。',
    // The chosen spot is inside a reply that is still being produced. This is a
    // wait, not a mistake the user made, so the wording says what to do rather
    // than what went wrong: the reply is not finished yet, come back when it is.
    // Naming the tool process here would explain the machinery instead of the
    // single thing the user needs to know.
    session_fork_anchor_not_turn_end: '这一轮回复还在进行，等它结束后就能从这里复制。',
    session_fork_unavailable: '暂时无法复制会话，请稍后重试。',
    session_fork_not_found: '尚未找到此复制请求，可以使用原请求重试。',
    session_fork_ack_timeout: '运行该 Agent 的设备长时间没有响应，可能版本过旧或已离线。请确认设备在线并升级到支持会话复制的版本后，重新创建副本。',
    session_fork_cancelled: '复制已取消，原会话不受影响。',
    // The chosen point was summarized away during context compaction, so that part
    // of the conversation no longer exists message by message. Plain wording: the
    // place is too old, pick a newer one.
    session_fork_anchor_compacted: '这个位置太靠前了，早期对话已经被系统压缩整理，没法从这里复制。请往下选一个更接近现在的位置再复制。',
  };
  return messages[text] || text;
}

// Explains a disabled composer inside a copy.
//
// This deliberately keys off a *confirmed* provenance state and not the
// `blocked` flag: `blocked` is also true for an ordinary group topic until the
// first fork lookup answers, and for any lookup failure. Using it would tell
// ordinary conversations they are copies, and would contradict the banner that
// reports an unknown state.
export function forkComposerNotice(provenanceState) {
  if (provenanceState === 'failed') return '此副本未能创建成功，无法发送消息';
  if (provenanceState === 'pending') return '副本正在初始化，暂不能发送消息';
  return '';
}
