import { describe, expect, it, vi } from 'vitest';
import {
  ARTIFACT_RUNTIME_EVENT_TYPE,
  ARTIFACT_RUNTIME_REQUEST_TYPE,
  ARTIFACT_RUNTIME_RESPONSE_TYPE,
  createArtifactRuntimeHost,
  normalizeArtifactRuntimeRequest,
} from './artifact-runtime-host';

function runtimeRequest(operation, payload = {}, requestId = `request-${operation.replace('.', '-')}`) {
  return {
    type: ARTIFACT_RUNTIME_REQUEST_TYPE,
    request_id: requestId,
    operation,
    payload,
  };
}

function createHarness() {
  const posted = [];
  const contentWindow = {
    postMessage: vi.fn((message, origin) => posted.push({ message, origin })),
  };
  const binding = {
    url: 'https://agent-440.artifacts.catsco.fun:19991/artifacts/risk-register/v4/',
    frame: { contentWindow },
    artifactId: 'risk-register',
    agentUid: 440,
  };
  let current = {
    token: { id: 'focus-1' },
    identityKey: 'p2p_7_440:440:risk-register:4',
    topicId: 'p2p_7_440',
    topicGeneration: 3,
    agentUid: 440,
    artifactId: 'risk-register',
    displayedVersion: 4,
    artifactRef: {
      contract_version: 'catsco.artifact-ref.v1',
      id: 'risk-register',
      displayed_version: 4,
      currently_visible: true,
    },
    binding,
  };
  const calls = [];
  let eventCursor = 7;
  let pollFailure = false;
  let heldPoll = null;
  const apiClient = {
    artifactRuntimeRequest: vi.fn(async (request, options) => {
      calls.push({ request, options });
      if (request.operation === 'connect') {
        return {
          ok: true,
          contract_version: 'catsco.artifact-runtime-response.v1',
          operation: 'connect',
          event_cursor: eventCursor,
        };
      }
      if (request.operation === 'events.list') {
        if (heldPoll) {
          return new Promise((resolve) => { heldPoll.resolve = resolve; });
        }
        if (pollFailure) {
          pollFailure = false;
          throw new Error('temporary failure');
        }
        return {
          ok: true,
          contract_version: 'catsco.artifact-runtime-response.v1',
          operation: 'events.list',
          event_cursor: eventCursor,
          events: eventCursor > request.after_event_id ? [{
            contract_version: 'catsco.artifact-runtime-event.v1',
            event_id: eventCursor,
            type: 'state.updated',
            namespace: 'risks',
            key: 'main',
            revision: 3,
          }] : [],
        };
      }
      if (request.operation === 'state.patch') {
        eventCursor += 1;
        return {
          ok: true,
          applied: true,
          contract_version: 'catsco.artifact-runtime-response.v1',
          operation: 'state.patch',
          state: { revision: 4 },
          event: {
            contract_version: 'catsco.artifact-runtime-event.v1',
            event_id: eventCursor,
            type: 'state.updated',
            namespace: 'risks',
            key: 'main',
            revision: 4,
          },
        };
      }
      return { ok: true, operation: request.operation };
    }),
  };
  const timers = [];
  const cleared = new Set();
  const host = createArtifactRuntimeHost({
    getCurrentSession: () => current,
    apiClient,
    setTimer(callback, timeoutMs) {
      const timer = { callback, timeoutMs };
      timers.push(timer);
      return timer;
    },
    clearTimer(timer) {
      cleared.add(timer);
    },
  });
  const event = data => ({
    data,
    source: contentWindow,
    origin: new URL(binding.url).origin,
  });
  return {
    host,
    binding,
    contentWindow,
    posted,
    calls,
    timers,
    cleared,
    event,
    apiClient,
    get current() { return current; },
    setCurrent(value) { current = value; },
    setEventCursor(value) { eventCursor = value; },
    failNextPoll() { pollFailure = true; },
    holdNextPoll() { heldPoll = {}; },
    resolveHeldPoll(value) {
      const pending = heldPoll;
      heldPoll = null;
      pending?.resolve(value);
    },
  };
}

async function flush() {
  await Promise.resolve();
  await Promise.resolve();
}

async function runTimer(timer) {
  timer.callback();
  await flush();
}

describe('normalizeArtifactRuntimeRequest', () => {
  it('accepts bounded state operations and rejects undeclared shapes', () => {
    expect(normalizeArtifactRuntimeRequest(runtimeRequest('state.patch', {
      namespace: 'risks',
      key: 'main',
      base_revision: 2,
      patch: [{ op: 'replace', path: '/status', value: 'closed' }],
    }))).toMatchObject({ operation: 'state.patch' });
    expect(normalizeArtifactRuntimeRequest(runtimeRequest('state.patch', {
      namespace: 'risks',
      key: 'main',
      base_revision: 0,
      patch: [{ op: 'replace', path: '/status', value: 'closed' }],
    }))).toBeNull();
    expect(normalizeArtifactRuntimeRequest({
      ...runtimeRequest('connect'),
      hidden_token: 'nope',
    })).toBeNull();
    expect(normalizeArtifactRuntimeRequest(runtimeRequest('state.get', {
      namespace: '../other',
      key: 'main',
    }))).toBeNull();
  });
});

describe('createArtifactRuntimeHost', () => {
  it('forwards only the exact connected frame and never gives the page CatsCo credentials', async () => {
    const harness = createHarness();
    harness.host.handleWindowMessage(harness.event(runtimeRequest('connect')));
    await flush();

    expect(harness.apiClient.artifactRuntimeRequest).toHaveBeenCalledTimes(1);
    expect(harness.calls[0].request).toMatchObject({
      contract_version: 'catsco.artifact-runtime-request.v1',
      operation: 'connect',
      topic_id: 'p2p_7_440',
      artifact_ref: harness.current.artifactRef,
    });
    expect(harness.posted[0]).toMatchObject({
      origin: new URL(harness.binding.url).origin,
      message: {
        type: ARTIFACT_RUNTIME_RESPONSE_TYPE,
        response: { ok: true, event_cursor: 7 },
      },
    });
    expect(JSON.stringify(harness.posted)).not.toMatch(/Authorization|preview_session|context_ref|task_ref/i);

    harness.host.handleWindowMessage({
      ...harness.event(runtimeRequest('connect', {}, 'attacker-1')),
      source: {},
    });
    harness.host.handleWindowMessage({
      ...harness.event(runtimeRequest('connect', {}, 'attacker-2')),
      origin: 'https://attacker.example',
    });
    await flush();
    expect(harness.apiClient.artifactRuntimeRequest).toHaveBeenCalledTimes(1);
  });

  it('subscribes from a cursor, forwards events, and resumes from the same cursor after suspension', async () => {
    const harness = createHarness();
    harness.host.handleWindowMessage(harness.event(runtimeRequest(
      'events.subscribe',
      { after_event_id: 5 },
      'subscribe-1',
    )));
    await flush();
    expect(harness.posted.at(-1).message.response.event_cursor).toBe(5);
    expect(harness.calls).toHaveLength(0);

    const firstPoll = harness.timers.at(-1);
    await runTimer(firstPoll);
    expect(harness.calls.at(-1).request).toMatchObject({
      operation: 'events.list',
      after_event_id: 5,
      limit: 100,
    });
    expect(harness.posted.some(({ message }) => (
      message.type === ARTIFACT_RUNTIME_EVENT_TYPE && message.event.event_id === 7
    ))).toBe(true);

    const scheduledBeforeSuspend = harness.timers.at(-1);
    harness.host.suspend();
    expect(harness.cleared.has(scheduledBeforeSuspend)).toBe(true);
    harness.setEventCursor(9);
    harness.host.resume();
    const resumedPoll = harness.timers.at(-1);
    expect(resumedPoll).not.toBe(scheduledBeforeSuspend);
    await runTimer(resumedPoll);
    expect(harness.calls.at(-1).request).toMatchObject({
      operation: 'events.list',
      after_event_id: 7,
    });
    expect(harness.posted.some(({ message }) => (
      message.type === ARTIFACT_RUNTIME_EVENT_TYPE && message.event.event_id === 9
    ))).toBe(true);
  });

  it('keeps the cursor after a transient poll failure and lets the ordered poll deliver a write event', async () => {
    const harness = createHarness();
    harness.host.handleWindowMessage(harness.event(runtimeRequest(
      'events.subscribe',
      { after_event_id: 7 },
      'subscribe-2',
    )));
    await flush();
    harness.failNextPoll();
    await runTimer(harness.timers.at(-1));
    const retry = harness.timers.at(-1);
    await runTimer(retry);
    expect(harness.calls.at(-1).request.after_event_id).toBe(7);

    harness.host.handleWindowMessage(harness.event(runtimeRequest('state.patch', {
      namespace: 'risks',
      key: 'main',
      base_revision: 3,
      patch: [{ op: 'replace', path: '/status', value: 'closed' }],
    }, 'patch-main')));
    await flush();
    expect(harness.posted.at(-1).message.type).toBe(ARTIFACT_RUNTIME_RESPONSE_TYPE);
    expect(harness.posted.some(({ message }) => (
      message.type === ARTIFACT_RUNTIME_EVENT_TYPE && message.event.event_id === 8
    ))).toBe(false);

    await runTimer(harness.timers.at(-1));
    expect(harness.calls.at(-1).request.after_event_id).toBe(7);
    expect(harness.posted.some(({ message }) => (
      message.type === ARTIFACT_RUNTIME_EVENT_TYPE && message.event.event_id === 8
    ))).toBe(true);
  });

  it('does not let a newer write response skip an older in-flight event', async () => {
    const harness = createHarness();
    harness.host.handleWindowMessage(harness.event(runtimeRequest(
      'events.subscribe',
      { after_event_id: 7 },
      'subscribe-race',
    )));
    await flush();
    harness.holdNextPoll();
    harness.timers.at(-1).callback();
    await flush();

    harness.setEventCursor(8);

    harness.host.handleWindowMessage(harness.event(runtimeRequest('state.patch', {
      namespace: 'risks',
      key: 'main',
      base_revision: 3,
      patch: [{ op: 'replace', path: '/status', value: 'closed' }],
    }, 'patch-race')));
    await flush();
    expect(harness.posted.filter(({ message }) => (
      message.type === ARTIFACT_RUNTIME_EVENT_TYPE
    ))).toHaveLength(0);
    harness.resolveHeldPoll({
      ok: true,
      event_cursor: 8,
      events: [{
        contract_version: 'catsco.artifact-runtime-event.v1',
        event_id: 8,
        type: 'state.updated',
        namespace: 'risks',
        key: 'main',
        revision: 4,
      }],
    });
    await flush();

    await runTimer(harness.timers.at(-1));

    const delivered = harness.posted
      .filter(({ message }) => message.type === ARTIFACT_RUNTIME_EVENT_TYPE)
      .map(({ message }) => message.event.event_id);
    expect(delivered).toEqual([8, 9]);
    expect(harness.calls.at(-1).request.after_event_id).toBe(8);
  });

  it('drops a paused subscription when the active Artifact identity changes', async () => {
    const harness = createHarness();
    harness.host.handleWindowMessage(harness.event(runtimeRequest(
      'events.subscribe',
      { after_event_id: 7 },
      'subscribe-3',
    )));
    await flush();
    harness.host.deactivate();
    harness.setCurrent({
      ...harness.current,
      identityKey: 'p2p_7_440:440:another-app:1',
      artifactId: 'another-app',
      displayedVersion: 1,
    });
    const timerCount = harness.timers.length;
    harness.host.resume();
    expect(harness.timers).toHaveLength(timerCount);
  });
});
