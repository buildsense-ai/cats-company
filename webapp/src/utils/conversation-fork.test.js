import { canForkConversation, describeForkError, forkAnchorIsMidTurn, forkAnchorMessageId, forkComposerNotice, forkTargetTopic, isForkAnchorError, normalizeForkRecord } from './conversation-fork';

const agent = { uid: 2, is_owner: true };

describe('conversation fork eligibility', () => {
  it('allows the current user and an owned or friend agent in p2p', () => {
    expect(canForkConversation({ topic: 'p2p_1_2', userId: 'usr1', agents: [agent] })).toBe(true);
    expect(canForkConversation({ topic: 'p2p_3_2', userId: 1, agents: [agent] })).toBe(false);
    expect(canForkConversation({ topic: 'p2p_1_2', userId: 1, agents: [{ uid: 2 }] })).toBe(false);
    expect(canForkConversation({ topic: 'p2p_1_2', userId: 1, agents: [] })).toBe(false);
    expect(canForkConversation({ topic: 'p2p_1_2', userId: 1, agents: [{ uid: 2, relation: 'friend' }] })).toBe(true);
    expect(canForkConversation({ topic: 'p2p_1_2', userId: 1, agents: [{ uid: 2, relation: 'friend', deployment_status: 'running', visibility: 'cloud' }] })).toBe(true);
  });

  it('requires exactly the current user and one accessible agent, regardless of group kind or ownership', () => {
    const source = {
      topic: 'grp_8', userId: 1, agents: [agent],
      group: { kind: 'agent_task' },
      members: [{ user_id: 'usr1', role: 'owner' }, { user_id: 2, is_bot: true }],
    };
    expect(canForkConversation(source)).toBe(true);
    expect(canForkConversation({ ...source, group: { kind: 'group' } })).toBe(true);
    expect(canForkConversation({ ...source, members: [...source.members, { user_id: 3 }] })).toBe(false);
    expect(canForkConversation({ ...source, members: [source.members[0]] })).toBe(false);
    expect(canForkConversation({ ...source, members: [{ user_id: 1, role: 'member' }, source.members[1]] })).toBe(true);
    expect(canForkConversation({ ...source, agents: [{ uid: 2, relation: 'friend' }] })).toBe(true);
    expect(canForkConversation({ ...source, members: [{ user_id: 3 }, source.members[1]] })).toBe(false);
    expect(canForkConversation({ ...source, agents: [{ uid: 2, relation: 'friend' }, { uid: 3, relation: 'friend' }], members: [{ user_id: 2 }, { user_id: 3 }] })).toBe(false);
  });
});

describe('bounded copy anchor', () => {
  it('anchors an agent reply on its last stored segment', () => {
    // A reply over the segment limit is stored as several messages; the group
    // keeps them in sourceMessages and the displayed message is the last one.
    const group = {
      type: 'text',
      assistantAuthored: true,
      message: { id: 30, seq_id: 30 },
      sourceMessages: [{ id: 10, seq_id: 10 }, { id: 20, seq_id: 20 }, { id: 30, seq_id: 30 }],
    };
    expect(forkAnchorMessageId(group)).toBe(30);
  });

  it('has no anchor for a human message or a working block', () => {
    expect(forkAnchorMessageId({ type: 'text', assistantAuthored: false, message: { seq_id: 7 }, sourceMessages: [{ seq_id: 7 }] })).toBe(0);
    expect(forkAnchorMessageId({ type: 'working', assistantAuthored: true, messages: [{ seq_id: 8 }] })).toBe(0);
    expect(forkAnchorMessageId(null)).toBe(0);
  });

  it('has no anchor when the message carries no usable id', () => {
    expect(forkAnchorMessageId({ type: 'text', assistantAuthored: true, message: {}, sourceMessages: [{}] })).toBe(0);
  });

  it('has no anchor for a streaming placeholder', () => {
    // A streaming reply is a local placeholder: its id is `stream:<id>` and it
    // has no server-side row id yet. The server cannot resolve it, and
    // answering "the conversation changed" would be misleading.
    //
    // The seq_id here is a small number on purpose: the streaming flag and the
    // timestamp range are two separate guards, and only a small id lets the
    // first one be the reason the anchor is refused.
    const streaming = {
      type: 'text',
      assistantAuthored: true,
      message: { id: 'stream:s1', seq_id: 30, _streaming: true },
      sourceMessages: [{ id: 'stream:s1', seq_id: 30, _streaming: true }],
    };
    expect(forkAnchorMessageId(streaming)).toBe(0);
  });

  it('has no anchor for an id that cannot be a message row id', () => {
    // A timestamp-shaped id is a local identifier the server cannot resolve,
    // even when nothing marks the message as streaming.
    expect(forkAnchorMessageId({
      type: 'text', assistantAuthored: true, message: {}, sourceMessages: [{ seq_id: 1793154059347 }],
    })).toBe(0);
  });

  // The point was refused because the reply is still being produced. That is a
  // wait, not a mistake: the sentence has to say "come back when it finishes"
  // rather than describe a boundary rule the user has no reason to know.
  it('asks the user to wait for the reply to finish', () => {
    const text = describeForkError('session_fork_anchor_not_turn_end');
    expect(text).toContain('还在进行');
    expect(text).toContain('等它结束后');
  });
});

// An anchor verdict is final for that point: retrying the same point produces the
// same verdict, so offering "retry" would be a button that cannot succeed.
describe('fork anchor verdicts', () => {
  it('treats both anchor errors as final', () => {
    expect(isForkAnchorError('session_fork_anchor_not_turn_end')).toBe(true);
    expect(isForkAnchorError('session_fork_anchor_compacted')).toBe(true);
  });

  it('leaves every other failure retryable', () => {
    expect(isForkAnchorError('session_fork_source_unavailable')).toBe(false);
    expect(isForkAnchorError('session_fork_source_busy')).toBe(false);
    expect(isForkAnchorError('network interrupted')).toBe(false);
    expect(isForkAnchorError('')).toBe(false);
    expect(isForkAnchorError(undefined)).toBe(false);
  });
});

describe('fork wire records', () => {
  const record = { request_id: 'r1', source_topic_id: 'p2p_1_2', target_topic_id: 'grp_9', state: 'ready' };
  it('resolves an ordinary target topic from IDs without manufacturing messages', () => {
    expect(forkTargetTopic(record, [{ id: 'grp_9', name: 'Copied task', avatar_url: '/a.png' }])).toEqual({
      topicId: 'grp_9', groupId: 9, name: 'Copied task', avatar_url: '/a.png',
      isGroup: true, isAgentTask: true, hasBot: true, memberCount: 2,
    });
    expect(forkTargetTopic(record).topicId).toBe('grp_9');
    expect(() => forkTargetTopic({ ...record, state: 'pending' })).toThrow();
  });
  it('rejects malformed, mismatched and unknown states instead of treating them as ready', () => {
    expect(normalizeForkRecord(record, { requestId: 'r1' })).toEqual(record);
    expect(() => normalizeForkRecord(record, { requestId: 'r2' })).toThrow();
    expect(() => normalizeForkRecord(record, { targetTopic: 'grp_10' })).toThrow();
    expect(() => normalizeForkRecord({ ...record, state: 'initializing' })).toThrow();
    expect(() => normalizeForkRecord({ ...record, target_topic_id: 'p2p_1_2' })).toThrow();
  });
});

describe('fork error copy', () => {
  it('turns an unanswered runtime into device advice instead of a raw code', () => {
    const text = describeForkError('session_fork_ack_timeout');
    expect(text).not.toBe('session_fork_ack_timeout');
    expect(text).toContain('设备');
    expect(text).toContain('升级');
    expect(text).toContain('重新创建');
  });
  it('states that a user cancellation leaves the source conversation untouched', () => {
    const text = describeForkError('session_fork_cancelled');
    expect(text).not.toBe('session_fork_cancelled');
    expect(text).toContain('已取消');
  });
  it('returns an unmapped code unchanged so diagnostics stay visible', () => {
    expect(describeForkError('session_fork_source_busy')).toBe('原会话正在执行，请等待完成后重试。');
    expect(describeForkError('unexpected_failure')).toBe('unexpected_failure');
    expect(describeForkError(undefined)).toBe('');
  });
});

describe('fork composer notice', () => {
  // Regression: the composer wording must key off a confirmed fork state, not
  // the blocked flag. `blocked` is also true for an ordinary group topic before
  // the first fork lookup answers, and for any lookup failure, so using it told
  // ordinary conversations they were copies.
  it('explains a copy that cannot accept messages', () => {
    expect(forkComposerNotice('failed')).toBe('此副本未能创建成功，无法发送消息');
    expect(forkComposerNotice('pending')).toBe('副本正在初始化，暂不能发送消息');
  });

  it('says nothing for ordinary topics and unresolved lookups', () => {
    // 404 (record null) means "not a copy"; '' covers an unresolved or failed
    // lookup, which the separate notice already explains.
    expect(forkComposerNotice(null)).toBe('');
    expect(forkComposerNotice(undefined)).toBe('');
    expect(forkComposerNotice('')).toBe('');
    expect(forkComposerNotice('ready')).toBe('');
  });
});

// Whether the copy point closes a turn is what the server checks before it will
// cut a copy. The menu can see the same thing from the same message list, so it
// has to say "wait" instead of sending the click and reporting a refusal.
describe('whether a copy point is mid-turn', () => {
  const messages = [
    { id: 1, from_uid: 1, content: 'question' },
    { id: 2, from_uid: 2, content: 'answer' },
    { id: 3, from_uid: 2, content: 'still working' },
    { id: 4, from_uid: 1, content: 'next question' },
  ];

  it('is mid-turn while the agent keeps talking after the anchor', () => {
    expect(forkAnchorIsMidTurn(messages, 2, 2)).toBe(true);
  });

  it('closes a turn when the next message is not the agent', () => {
    expect(forkAnchorIsMidTurn(messages, 3, 2)).toBe(false);
  });

  it('closes a turn when nothing follows', () => {
    expect(forkAnchorIsMidTurn(messages, 4, 2)).toBe(false);
  });

  it('says nothing without a usable anchor or list', () => {
    expect(forkAnchorIsMidTurn(messages, 0, 2)).toBe(false);
    expect(forkAnchorIsMidTurn(messages, 999, 2)).toBe(false);
    expect(forkAnchorIsMidTurn(null, 2, 2)).toBe(false);
  });
});
