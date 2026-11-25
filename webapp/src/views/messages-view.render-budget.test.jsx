import React, { act, memo } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';

// Timeline rows are memoized with ChatMessage's real comparator. This suite
// counts how many rows re-render for the two hottest interactions — typing in
// the composer and receiving a streamed chunk — so a new per-render prop
// (an inline closure, an unmemoized derived object) cannot silently make every
// row re-render again.

const rowRenders = vi.hoisted(() => ({ count: 0 }));
const stableFeedback = vi.hoisted(() => ({ confirm: async () => true, notify: () => {} }));
const ws = vi.hoisted(() => ({ handler: null }));
const fixture = vi.hoisted(() => ({ messages: [] }));

vi.mock('../widgets/chat-message', async (importOriginal) => {
  const actual = await importOriginal();
  function CountingChatMessage({ message, workingOnly }) {
    rowRenders.count += 1;
    return (
      <div
        className="budget-row"
        data-working-only={String(Boolean(workingOnly))}
        data-message-content={typeof message?.content === 'string' ? message.content : ''}
      />
    );
  }
  return { ...actual, default: memo(CountingChatMessage, actual.chatMessagePropsEqual) };
});

vi.mock('../widgets/avatar', () => ({ default: () => null }));

vi.mock('../components/feedback-system', () => ({
  useFeedback: () => stableFeedback,
}));

vi.mock('../api', () => {
  const defaults = {
    getMessages: async () => ({ messages: fixture.messages }),
    getConversations: async () => ({ conversations: [] }),
    getConversationFork: async () => { throw Object.assign(new Error('session_fork_not_found'), { status: 404 }); },
    getFriends: async () => ({ friends: [] }),
    getAgents: async () => ({ agents: [{ uid: 2, is_bot: true, topic_id: 'p2p_1_2', display_name: 'Agent', is_owner: true }] }),
    getAgentQuota: async () => ({ configured: false, shared: true }),
    getTutorialTasks: async () => ({ tasks: [], limit: 6 }),
    getCloudArtifacts: async () => ({ artifacts: [] }),
    getAgentFiles: async () => ({ files: [], has_more: false, next_before_id: 0 }),
    getTopicFiles: async () => ({ files: [], has_more: false, next_before_id: 0 }),
    getCloudWorkers: async () => ({ workers: [] }),
    listArtifactApps: async () => ({ apps: [] }),
  };
  const api = new Proxy({}, {
    get: (target, prop) => {
      if (!target[prop]) target[prop] = vi.fn(defaults[prop] || (async () => ({})));
      return target[prop];
    },
  });
  return {
    api,
    wsSendMessage: vi.fn(),
    wsSendStreamCancel: vi.fn(),
    wsSendTyping: vi.fn(),
    wsSendRead: vi.fn(),
    wsSendArtifactResultReceipt: vi.fn(),
    onWSMessage: vi.fn((handler) => { ws.handler = handler; return vi.fn(); }),
    updateTopicSeq: vi.fn(),
    isWSConnected: vi.fn(() => true),
    getApiBaseURL: () => window.location.origin,
    resolveMediaURL: (url) => url,
  };
});

import MessagesView, { reuseUnchangedTimelineGroups } from './messages-view';

const user = { uid: 1, username: 'me', display_name: 'Me', avatar_url: '', account_type: 'human' };
const TURNS = 30;

function history(turns) {
  const messages = [];
  let id = 1;
  for (let turn = 0; turn < turns; turn += 1) {
    const at = (offset) => new Date(Date.UTC(2026, 10, 1, 0, turn, offset)).toISOString();
    messages.push({ id: id, seq_id: id++, from_uid: 1, type: 'text', content: `问题 ${turn}`, created_at: at(0) });
    messages.push({ id: id, seq_id: id++, from_uid: 2, type: 'tool_use', content: 'read_file', metadata: { turn_id: `run-${turn}` }, created_at: at(1) });
    messages.push({ id: id, seq_id: id++, from_uid: 2, type: 'text', role: 'assistant', content: `回答 ${turn}`, metadata: { turn_id: `run-${turn}` }, created_at: at(2) });
  }
  return messages;
}

async function flush(count = 20) {
  for (let i = 0; i < count; i += 1) await Promise.resolve();
}

describe('messages view render budget', () => {
  let container;
  let root;

  beforeEach(async () => {
    global.IS_REACT_ACT_ENVIRONMENT = true;
    localStorage.clear();
    sessionStorage.clear();
    window.HTMLElement.prototype.scrollIntoView = vi.fn();
    fixture.messages = history(TURNS);
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
    await act(async () => {
      root.render(
        <MessagesView topic="p2p_1_2" topicName="Agent" user={user} isGroup={false} groupId={null} topicAvatarUrl="" onTopicUpdated={vi.fn()} />,
      );
      await flush();
    });
    await act(async () => { await flush(); });
  });

  afterEach(async () => {
    await act(async () => root.unmount());
    container.remove();
  });

  it('renders the whole history', () => {
    expect(container.querySelectorAll('.budget-row')).toHaveLength(TURNS * 3);
  });

  it('does not re-render timeline rows while the user types', async () => {
    const textarea = container.querySelector('textarea');
    rowRenders.count = 0;
    let draft = '';
    for (let i = 0; i < 10; i += 1) {
      draft += '字';
      await act(async () => {
        textarea.value = draft;
        Simulate.change(textarea, { target: { value: draft, selectionStart: draft.length } });
      });
    }
    expect(textarea.value).toBe(draft);
    expect(rowRenders.count).toBe(0);
  });

  it('re-renders only the streaming reply for each streamed chunk', async () => {
    const delta = (content) => ({ data: {
      topic: 'p2p_1_2', from: 'usr2', type: 'stream_delta', metadata: { stream_id: 'budget', turn_id: 'run-live' }, content,
    } });
    await act(async () => { ws.handler(delta('第一段')); });
    rowRenders.count = 0;
    const CHUNKS = 10;
    // One chunk per frame: the case where nothing can be coalesced.
    for (let i = 0; i < CHUNKS; i += 1) {
      await act(async () => { ws.handler(delta(`片段${i}`)); });
      await act(async () => { await new Promise((resolve) => requestAnimationFrame(() => resolve())); });
    }
    expect(container.querySelector('[data-message-content*="第一段片段0片段1"]')).not.toBeNull();
    // One row (the streaming reply) per chunk; a small allowance covers a row
    // whose adjacency legitimately changes, never the whole history.
    expect(rowRenders.count).toBeLessThanOrEqual(CHUNKS + 2);
  });
});

describe('messages view stream bursts', () => {
  let container;
  let root;

  beforeEach(async () => {
    global.IS_REACT_ACT_ENVIRONMENT = true;
    window.HTMLElement.prototype.scrollIntoView = vi.fn();
    fixture.messages = history(TURNS);
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
    await act(async () => {
      root.render(
        <MessagesView topic="p2p_1_2" topicName="Agent" user={user} isGroup={false} groupId={null} topicAvatarUrl="" onTopicUpdated={vi.fn()} />,
      );
      await flush();
    });
    await act(async () => { await flush(); });
  });

  afterEach(async () => {
    await act(async () => root.unmount());
    container.remove();
  });

  it('renders a burst of deltas inside one frame once', async () => {
    const delta = (content) => ({ data: {
      topic: 'p2p_1_2', from: 'usr2', type: 'stream_delta', metadata: { stream_id: 'burst', turn_id: 'run-burst' }, content,
    } });
    await act(async () => { ws.handler(delta('开始')); });
    await act(async () => { await new Promise((resolve) => requestAnimationFrame(() => resolve())); });
    rowRenders.count = 0;
    await act(async () => {
      for (let i = 0; i < 50; i += 1) ws.handler(delta(`词${i}`));
    });
    await act(async () => { await new Promise((resolve) => requestAnimationFrame(() => resolve())); });
    expect(container.querySelector('[data-message-content*="词0词1"]')).not.toBeNull();
    expect(container.querySelector('[data-message-content*="词49"]')).not.toBeNull();
    // 50 deltas: at most the leading one plus one frame flush.
    expect(rowRenders.count).toBeLessThanOrEqual(2);
  });
});

describe('reuseUnchangedTimelineGroups', () => {
  it('returns previous group objects when a rebuilt group is equivalent', () => {
    const message = { id: 1, content: 'a' };
    const previous = [{ type: 'text', renderKey: 'text:1', message, sender: { name: 'A' } }];
    const rebuilt = [{ type: 'text', renderKey: 'text:1', message, sender: { name: 'A' } }];
    expect(reuseUnchangedTimelineGroups(rebuilt, previous)[0]).toBe(previous[0]);
  });

  it('keeps unchanged parts of a group that did change', () => {
    const step = { id: 1, type: 'tool_use', content: 'read_file' };
    const merged = { id: 2, content: 'reply', content_blocks: [] };
    const previous = [{ type: 'working', renderKey: 'working:1', messages: [step], sender: { name: 'A' } }];
    const appended = { id: 3, type: 'tool_use', content: 'write_file' };
    const [next] = reuseUnchangedTimelineGroups([
      { type: 'working', renderKey: 'working:1', messages: [{ ...step }, appended], sender: { name: 'A' } },
    ], previous);
    expect(next).not.toBe(previous[0]);
    expect(next.messages).toHaveLength(2);
    expect(next.messages[0]).toBe(step);
    expect(next.messages[1]).toBe(appended);
    expect(next.sender).toBe(previous[0].sender);

    const textPrevious = [{ type: 'text', renderKey: 'text:2', message: merged }];
    const [textNext] = reuseUnchangedTimelineGroups([
      { type: 'text', renderKey: 'text:2', message: { ...merged, content: 'reply, longer' } },
    ], textPrevious);
    expect(textNext.message.content).toBe('reply, longer');
  });

  it('does not reuse across group types or unknown keys', () => {
    const previous = [{ type: 'text', renderKey: 'k', message: { id: 1 } }];
    const rebuilt = [{ type: 'working', renderKey: 'k', messages: [{ id: 1 }] }];
    expect(reuseUnchangedTimelineGroups(rebuilt, previous)[0]).toBe(rebuilt[0]);
    expect(reuseUnchangedTimelineGroups(rebuilt, [])).toBe(rebuilt);
  });
});
