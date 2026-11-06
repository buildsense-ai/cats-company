import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { useConversationFork } from './use-conversation-fork';
import { api } from '../api';

vi.mock('../api', () => ({ api: {
  forkConversation: vi.fn(), getConversationFork: vi.fn(), getConversations: vi.fn(), cancelConversationFork: vi.fn(),
} }));

const record = { request_id: 'r1', source_topic_id: 'p2p_1_2', target_topic_id: 'grp_9', state: 'pending' };
let value, root, container;
function Harness(props) { value = useConversationFork(props); return null; }
const mount = async (props = {}) => {
  await act(async () => root.render(<Harness topic="p2p_1_2" userId={1} {...props} />));
};

beforeEach(() => {
  localStorage.clear();
  vi.resetAllMocks();
  api.getConversationFork.mockRejectedValue(Object.assign(new Error('session_fork_not_found'), { status: 404 }));
  api.getConversations.mockResolvedValue({ conversations: [{ id: 'grp_9', name: 'Copy' }] });
  container = document.createElement('div');
  document.body.append(container);
  root = createRoot(container);
});
afterEach(async () => { await act(async () => root.unmount()); container.remove(); vi.useRealTimers(); });

it('retains one request ID after uncertain failure, retry and remount', async () => {
  api.forkConversation.mockRejectedValueOnce(new Error('network interrupted'))
    .mockImplementation(async payload => ({ ...record, request_id: payload.request_id }));
  await mount();
  await act(async () => value.create());
  const first = api.forkConversation.mock.calls[0][0];
  expect(value.error).toContain('network interrupted');
  await act(async () => root.unmount());
  root = createRoot(container);
  await mount();
  await act(async () => value.create());
  expect(api.forkConversation.mock.calls[1][0]).toEqual(first);
  expect(value.operation.state).toBe('pending');
});

it('shares a pending request between header and sidebar instead of allocating a second copy', async () => {
  const handles = {};
  let complete;
  api.forkConversation.mockImplementation(payload => new Promise(resolve => {
    complete = () => resolve({ ...record, request_id: payload.request_id });
  }));
  api.getConversationFork.mockImplementation(async ({ requestId }) => ({ ...record, request_id: requestId }));
  function Control({ id }) {
    handles[id] = useConversationFork({ topic: 'p2p_1_2', userId: 1 });
    return null;
  }
  await act(async () => root.render(<><Control id="header" /><Control id="sidebar" /></>));
  let pending;
  await act(async () => { pending = handles.sidebar.create({ newRequest: true }); });
  expect(handles.header.operation.state).toBe('pending');
  await act(async () => handles.header.create({ newRequest: true }));
  expect(api.forkConversation).toHaveBeenCalledTimes(1);
  await act(async () => { complete(); await pending; });
  expect(handles.header.operation.request_id).toBe(handles.sidebar.operation.request_id);
});

it('rechecks after a delayed pre-creation 404 instead of losing a successful sidebar notification', async () => {
  const handles = {};
  let rejectLookup;
  let ready;
  api.getConversationFork.mockImplementationOnce(() => new Promise((_, reject) => { rejectLookup = reject; }))
    .mockImplementation(async () => ready);
  api.forkConversation.mockImplementation(async payload => {
    ready = { ...record, request_id: payload.request_id, state: 'ready' };
    return ready;
  });
  function Control({ id }) {
    handles[id] = useConversationFork({ topic: 'p2p_1_2', userId: 1 });
    return null;
  }
  await act(async () => root.render(<><Control id="header" /><Control id="sidebar" /></>));
  await act(async () => handles.sidebar.create({ newRequest: true }));
  expect(handles.header.busy).toBe(true);
  await act(async () => rejectLookup(Object.assign(new Error('session_fork_not_found'), { status: 404 })));
  expect(api.getConversationFork).toHaveBeenCalledTimes(2);
  expect(handles.header.error).toBe('');
  expect(handles.header.busy).toBe(false);
  expect(handles.header.operation.state).toBe('ready');
});

it('exposes source recovery as busy while reopening a saved request', async () => {
  localStorage.setItem('cc:conversation-fork:1:p2p_1_2', JSON.stringify({source_topic_id:'p2p_1_2',request_id:'r1'}));
  let resolveLookup;
  api.getConversationFork.mockImplementationOnce(() => new Promise(resolve => { resolveLookup=resolve; }));
  await mount();
  expect(value.busy).toBe(true);
  await act(async () => resolveLookup(record));
  expect(value.busy).toBe(false);
  expect(value.operation.state).toBe('pending');
});

it('blocks pending and failed destination composition, and unlocks only after authoritative ready', async () => {
  vi.useFakeTimers();
  api.getConversationFork.mockResolvedValueOnce(record)
    .mockResolvedValueOnce({ ...record, state: 'ready' });
  await mount({ topic: 'grp_9' });
  expect(value.blocked).toBe(true);
  expect(value.provenance.source_topic_id).toBe('p2p_1_2');
  await act(async () => vi.advanceTimersByTimeAsync(2000));
  expect(value.blocked).toBe(false);
  api.getConversationFork.mockResolvedValue({ ...record, state: 'failed', error: 'Source busy' });
  await act(async () => value.checkTarget());
  expect(value.blocked).toBe(true);
  expect(value.provenance.error).toBe('Source busy');
});

it('keeps a target blocked on network or malformed status and allows a confirmed non-fork', async () => {
  api.getConversationFork.mockRejectedValueOnce(new Error('offline'));
  await mount({ topic: 'grp_9' });
  expect(value.blocked).toBe(true);
  expect(value.targetError).toContain('offline');
  await act(async () => value.checkTarget());
  expect(value.blocked).toBe(false);
});

it('opens ready copies through the existing topic callback and refreshes normal lists', async () => {
  const activate = vi.fn();
  const changed = vi.fn();
  window.addEventListener('cc:data-changed', changed);
  api.forkConversation.mockImplementation(async payload => ({ ...record, request_id: payload.request_id, state: 'ready' }));
  await mount({ onActivateTopic: activate });
  await act(async () => value.create());
  expect(activate).not.toHaveBeenCalled();
  await act(async () => value.open());
  expect(activate).toHaveBeenCalledWith(expect.objectContaining({ topicId: 'grp_9', name: 'Copy', isAgentTask: true }));
  expect(changed).toHaveBeenCalled();
  window.removeEventListener('cc:data-changed', changed);
});

it('does not apply late responses or open a copy after navigation', async () => {
  let finish;
  api.forkConversation.mockImplementation(payload => new Promise(resolve => { finish = () => resolve({ ...record, request_id: payload.request_id, state: 'ready' }); }));
  const activate = vi.fn();
  await mount({ onActivateTopic: activate });
  let pending;
  await act(async () => { pending = value.create(); });
  await mount({ topic: 'p2p_1_3', onActivateTopic: activate });
  await act(async () => { finish(); await pending; });
  expect(value.operation).toBeNull();
  expect(activate).not.toHaveBeenCalled();
});

it('never retries a terminal record and creates a different request only on explicit recreation', async () => {
  api.forkConversation.mockImplementation(async payload => ({ ...record, request_id: payload.request_id, state: 'failed', error: 'session_fork_source_interrupted' }));
  await mount();
  await act(async () => value.create());
  const first = api.forkConversation.mock.calls[0][0];
  await act(async () => value.create());
  expect(api.forkConversation).toHaveBeenCalledTimes(1);
  await act(async () => value.create({ newRequest: true }));
  expect(api.forkConversation.mock.calls[1][0].request_id).not.toBe(first.request_id);
});

it('does not unlock composition for an unrelated endpoint 404 or invalid readiness payload', async () => {
  api.getConversationFork.mockRejectedValueOnce(Object.assign(new Error('Not Found'), { status: 404 }))
    .mockResolvedValueOnce({ ...record, state: 'ready', target_topic_id: 'grp_10' });
  await mount({ topic: 'grp_9' });
  expect(value.blocked).toBe(true);
  await act(async () => value.checkTarget());
  expect(value.blocked).toBe(true);
  expect(value.targetError).toContain('状态无效');
});

it('coalesces duplicate clicks until the first request is answered', async () => {
  let finish;
  api.forkConversation.mockImplementation(payload => new Promise(resolve => { finish = () => resolve({ ...record, request_id: payload.request_id }); }));
  await mount();
  let pending;
  await act(async () => { pending = value.create(); await value.create(); });
  expect(api.forkConversation).toHaveBeenCalledTimes(1);
  await act(async () => { finish(); await pending; });
});

it('does not borrow readiness or provenance from the previous topic while querying a new target', async () => {
  api.getConversationFork.mockResolvedValueOnce({ ...record, state: 'ready' })
    .mockImplementationOnce(() => new Promise(() => {}));
  await mount({ topic: 'grp_9' });
  expect(value.blocked).toBe(false);
  await mount({ topic: 'grp_10' });
  expect(value.blocked).toBe(true);
  expect(value.provenance).toBeNull();
});

it('clears the saved request and the banner once a cancellation is confirmed', async () => {
  api.forkConversation.mockImplementation(async payload => ({ ...record, request_id: payload.request_id }));
  api.getConversationFork.mockResolvedValue(record);
  api.cancelConversationFork.mockImplementation(async ({ requestId }) => ({
    ...record, request_id: requestId, state: 'failed', error: 'session_fork_cancelled',
  }));
  await mount();
  await act(async () => value.create({ newRequest: true }));
  const requestId = value.operation.request_id;
  expect(value.operation.state).toBe('pending');
  expect(localStorage.getItem('cc:conversation-fork:1:p2p_1_2')).toBeTruthy();
  await act(async () => value.cancel());
  expect(api.cancelConversationFork).toHaveBeenCalledWith({ requestId }, { timeoutMs: 15000 });
  expect(value.operation).toBeNull();
  expect(value.error).toBe('');
  expect(value.busy).toBe(false);
  expect(localStorage.getItem('cc:conversation-fork:1:p2p_1_2')).toBeNull();
});

it('keeps a copy that turned ready before the cancellation landed', async () => {
  api.forkConversation.mockImplementation(async payload => ({ ...record, request_id: payload.request_id }));
  api.getConversationFork.mockResolvedValue(record);
  api.cancelConversationFork.mockImplementation(async ({ requestId }) => ({ ...record, request_id: requestId, state: 'ready' }));
  await mount();
  await act(async () => value.create({ newRequest: true }));
  const saved = localStorage.getItem('cc:conversation-fork:1:p2p_1_2');
  expect(saved).toBeTruthy();
  await act(async () => value.cancel());
  expect(value.operation.state).toBe('ready');
  expect(localStorage.getItem('cc:conversation-fork:1:p2p_1_2')).toBe(saved);
});

it('sends no cancellation request without a stored request ID and surfaces a failed cancel', async () => {
  await mount();
  await act(async () => value.cancel());
  expect(api.cancelConversationFork).not.toHaveBeenCalled();
  api.getConversationFork.mockResolvedValue(record);
  api.forkConversation.mockImplementation(async payload => ({ ...record, request_id: payload.request_id }));
  await act(async () => value.create());
  api.cancelConversationFork.mockRejectedValueOnce(new Error('offline'));
  await act(async () => value.cancel());
  expect(value.error).toContain('offline');
  expect(localStorage.getItem('cc:conversation-fork:1:p2p_1_2')).toBeTruthy();
});

// The anchor is part of the request's identity. A stored payload whose anchor
// differs must not be reused: the server would reject the mismatch with the
// same reason forever, or — worse — the anchor would be dropped and the whole
// conversation copied while the user believes they picked a point.
it('starts a new request when the anchor differs from the stored one', async () => {
  api.forkConversation.mockImplementation(async payload => ({ ...record, request_id: payload.request_id }));
  await mount();
  await act(async () => value.create({ newRequest: true, untilMessageId: 10 }));
  const first = api.forkConversation.mock.calls[0][0];
  expect(first.until_message_id).toBe(10);
  // A different anchor is a different copy, even while the first one is
  // still pending: the user is answering the mistake they just made.
  await act(async () => value.create({ newRequest: true, untilMessageId: 20 }));
  const second = api.forkConversation.mock.calls[1][0];
  expect(second.request_id).not.toBe(first.request_id);
  expect(second.until_message_id).toBe(20);
});

it('carries the anchor of an unbounded request into a bounded one', async () => {
  api.forkConversation.mockImplementation(async payload => ({ ...record, request_id: payload.request_id }));
  await mount();
  await act(async () => value.create({ newRequest: true }));
  expect(api.forkConversation.mock.calls[0][0].until_message_id).toBeUndefined();
  await act(async () => value.create({ newRequest: true, untilMessageId: 33 }));
  expect(api.forkConversation.mock.calls[1][0].until_message_id).toBe(33);
});

// A 4xx never creates a server-side record, so the payload written before the
// request left is dangling. Left in storage it becomes the identity of every
// later click, which pins the conversation to one rejection until the user
// clears storage by hand.
it('drops the stored payload when the request is rejected outright', async () => {
  api.forkConversation.mockRejectedValue(Object.assign(new Error('session_fork_anchor_not_turn_end'), { status: 409 }));
  await mount();
  await act(async () => value.create({ newRequest: true, untilMessageId: 10 }));
  expect(api.forkConversation).toHaveBeenCalledTimes(1);
  expect(localStorage.getItem('cc:conversation-fork:1:p2p_1_2')).toBeNull();
  // The next click starts from scratch and can carry a different anchor.
  api.forkConversation.mockImplementation(async payload => ({ ...record, request_id: payload.request_id }));
  await act(async () => value.create({ newRequest: true, untilMessageId: 20 }));
  const second = api.forkConversation.mock.calls[1][0];
  expect(second.until_message_id).toBe(20);
  expect(second.request_id).not.toBe(api.forkConversation.mock.calls[0][0].request_id);
});

it('keeps the stored payload when the failure is not a rejection', async () => {
  api.forkConversation.mockRejectedValueOnce(new Error('network interrupted'));
  await mount();
  await act(async () => value.create({ newRequest: true, untilMessageId: 10 }));
  // A network failure says nothing about whether the server created the record,
  // so the request ID has to survive: retrying it is the only way to find out.
  expect(localStorage.getItem('cc:conversation-fork:1:p2p_1_2')).not.toBeNull();
});

// The notice's "retry" carries no anchor of its own. After a rejection the
// stored payload is gone, so without remembering the user's choice the retry
// would go out unbounded and copy the whole conversation — while the user
// believes they are still copying from the point they picked. This is the
// common case: a fifth of recent replies end with tool output, and pointing at
// one of those always gets rejected.
it('retries with the anchor the user picked after a rejection', async () => {
  api.forkConversation.mockRejectedValueOnce(Object.assign(new Error('session_fork_anchor_not_turn_end'), { status: 409 }))
    .mockImplementation(async payload => ({ ...record, request_id: payload.request_id }));
  await mount();
  await act(async () => value.create({ newRequest: true, untilMessageId: 20 }));
  expect(api.forkConversation.mock.calls[0][0].until_message_id).toBe(20);
  // The retry carries no anchor of its own.
  await act(async () => value.create());
  const retry = api.forkConversation.mock.calls[1][0];
  expect(retry.until_message_id).toBe(20);
  expect(retry.request_id).not.toBe(api.forkConversation.mock.calls[0][0].request_id);
});

// An entry point that never had an anchor stays unbounded: the header and
// sidebar copy the whole conversation, and they must not inherit a point that
// was picked somewhere else.
it('does not invent an anchor for a copy that never had one', async () => {
  api.forkConversation.mockImplementation(async payload => ({ ...record, request_id: payload.request_id }));
  await mount();
  await act(async () => value.create({ newRequest: true }));
  expect(api.forkConversation.mock.calls[0][0].until_message_id).toBeUndefined();
  await act(async () => value.create());
  expect(api.forkConversation.mock.calls[1][0].until_message_id).toBeUndefined();
});

// The anchor lives in memory only until the page is reloaded. After a reload the
// stored payload is the only place it is left, so a retry has to read it from
// there: otherwise the retry goes out unbounded and the whole conversation is
// copied while the user believes they are still copying from their point.
it('retries with the stored anchor after a remount', async () => {
  localStorage.setItem('cc:conversation-fork:1:p2p_1_2',
    JSON.stringify({ source_topic_id: 'p2p_1_2', request_id: 'old', until_message_id: 20 }));
  api.getConversationFork.mockImplementation(async ({ requestId }) => ({ ...record, request_id: requestId }));
  api.forkConversation.mockImplementation(async payload => ({ ...record, request_id: payload.request_id }));
  await mount();
  await act(async () => value.create());
  expect(api.forkConversation.mock.calls[0][0].until_message_id).toBe(20);
});

// The header and sidebar copy the whole conversation on purpose. They say so
// explicitly, so they never inherit a point picked on a message.
it('keeps the header and sidebar copies unbounded', async () => {
  localStorage.setItem('cc:conversation-fork:1:p2p_1_2',
    JSON.stringify({ source_topic_id: 'p2p_1_2', request_id: 'old', until_message_id: 20 }));
  api.getConversationFork.mockImplementation(async ({ requestId }) => ({ ...record, request_id: requestId }));
  api.forkConversation.mockImplementation(async payload => ({ ...record, request_id: payload.request_id }));
  await mount();
  await act(async () => value.create({ newRequest: true, untilMessageId: 0 }));
  expect(api.forkConversation.mock.calls[0][0].until_message_id).toBeUndefined();
});
