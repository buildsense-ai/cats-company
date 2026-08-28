import { api } from './api';

export const ARTIFACT_RUNTIME_REQUEST_TYPE = 'catsco.artifact.runtime.request.v1';
export const ARTIFACT_RUNTIME_RESPONSE_TYPE = 'catsco.artifact.runtime.response.v1';
export const ARTIFACT_RUNTIME_EVENT_TYPE = 'catsco.artifact.runtime.event.v1';
export const ARTIFACT_RUNTIME_REQUEST_CONTRACT = 'catsco.artifact-runtime-request.v1';

const RUNTIME_REQUEST_TIMEOUT_MS = 5000;
const RUNTIME_EVENT_POLL_MS = 1500;
const RUNTIME_REQUEST_ID_PATTERN = /^[A-Za-z0-9._:-]{8,128}$/;
const RUNTIME_NAME_PATTERN = /^[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*$/;
const RUNTIME_KEY_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$/;
const RUNTIME_MAX_PAYLOAD_BYTES = 300 * 1024;

function bindingOrigin(binding) {
  try {
    return new URL(binding?.url || '').origin;
  } catch {
    return '';
  }
}

function postBridgeMessage(binding, message) {
  const contentWindow = binding?.frame?.contentWindow;
  const targetOrigin = bindingOrigin(binding);
  if (!contentWindow?.postMessage || !targetOrigin) return false;
  try {
    contentWindow.postMessage(message, targetOrigin);
    return true;
  } catch {
    return false;
  }
}

function currentRuntimeSession(getCurrentSession, disposed, suspended) {
  if (disposed || suspended || typeof getCurrentSession !== 'function') return null;
  const session = getCurrentSession();
  if (!session?.identityKey || !session?.topicId || !session?.artifactRef
    || !session?.binding || !session?.token || Number(session.agentUid || 0) <= 0
    || !session.artifactId || Number(session.displayedVersion || 0) <= 0) return null;
  return {
    ...session,
    topicGeneration: Number(session.topicGeneration || 0),
    agentUid: Number(session.agentUid),
    displayedVersion: Number(session.displayedVersion),
  };
}

function sessionMatches(current, record) {
  return Boolean(current && record
    && current.token === record.token
    && current.identityKey === record.identityKey
    && current.topicId === record.topicId
    && current.topicGeneration === record.topicGeneration
    && current.agentUid === record.agentUid
    && current.artifactId === record.artifactId
    && current.displayedVersion === record.displayedVersion
    && current.binding === record.binding);
}

function plainObject(value) {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return false;
  const prototype = Object.getPrototypeOf(value);
  return prototype === Object.prototype || prototype === null;
}

function cloneBounded(value) {
  let encoded;
  try {
    encoded = JSON.stringify(value);
  } catch {
    return null;
  }
  if (typeof encoded !== 'string' || new TextEncoder().encode(encoded).length > RUNTIME_MAX_PAYLOAD_BYTES) {
    return null;
  }
  try {
    return JSON.parse(encoded);
  } catch {
    return null;
  }
}

function exactKeys(value, allowed) {
  return plainObject(value) && Object.keys(value).every(
    (key) => allowed.has(key) && !['__proto__', 'constructor', 'prototype'].includes(key),
  );
}

export function normalizeArtifactRuntimeRequest(value) {
  if (!exactKeys(value, new Set(['type', 'request_id', 'operation', 'payload']))
    || value.type !== ARTIFACT_RUNTIME_REQUEST_TYPE
    || !RUNTIME_REQUEST_ID_PATTERN.test(String(value.request_id || ''))
    || typeof value.operation !== 'string') return null;
  const operation = value.operation;
  const payload = value.payload === undefined ? {} : value.payload;
  const empty = () => exactKeys(payload, new Set([])) ? {} : null;
  let normalized;
  switch (operation) {
    case 'connect':
    case 'state.list':
    case 'events.unsubscribe':
      normalized = empty();
      break;
    case 'state.get':
      if (!exactKeys(payload, new Set(['namespace', 'key']))) return null;
      normalized = runtimeStateTarget(payload);
      break;
    case 'state.put':
      if (!exactKeys(payload, new Set(['namespace', 'key', 'base_revision', 'value']))) return null;
      normalized = runtimeStateTarget(payload);
      if (!normalized || !validRevision(payload.base_revision)) return null;
      normalized.base_revision = payload.base_revision;
      normalized.value = cloneBounded(payload.value);
      if (normalized.value === null && payload.value !== null) return null;
      break;
    case 'state.patch':
      if (!exactKeys(payload, new Set(['namespace', 'key', 'base_revision', 'patch']))) return null;
      normalized = runtimeStateTarget(payload);
      if (!normalized || !validRevision(payload.base_revision) || payload.base_revision <= 0
        || !Array.isArray(payload.patch) || payload.patch.length === 0 || payload.patch.length > 64) return null;
      normalized.base_revision = payload.base_revision;
      normalized.patch = cloneBounded(payload.patch);
      if (!Array.isArray(normalized.patch)) return null;
      break;
    case 'events.subscribe':
      if (!exactKeys(payload, new Set(['after_event_id']))
        || (payload.after_event_id !== undefined && !validRevision(payload.after_event_id))) return null;
      normalized = { after_event_id: Number(payload.after_event_id || 0) };
      break;
    default:
      return null;
  }
  if (normalized === null) return null;
  return {
    requestId: value.request_id,
    operation,
    payload: normalized,
  };
}

function runtimeStateTarget(payload) {
  const namespace = String(payload.namespace || '');
  const key = String(payload.key || '');
  return RUNTIME_NAME_PATTERN.test(namespace) && RUNTIME_KEY_PATTERN.test(key)
    ? { namespace, key }
    : null;
}

function validRevision(value) {
  return Number.isSafeInteger(value) && value >= 0;
}

function publicRuntimeError(error) {
  const responseError = error?.data?.error;
  if (plainObject(responseError)) {
    return {
      code: String(responseError.code || 'runtime_request_failed').slice(0, 128),
      message: String(responseError.message || 'Artifact Runtime request failed').slice(0, 500),
      ...(Number.isSafeInteger(responseError.current_revision)
        ? { current_revision: responseError.current_revision }
        : {}),
    };
  }
  return {
    code: 'runtime_request_failed',
    message: 'Artifact Runtime request failed',
  };
}

export function createArtifactRuntimeHost({
  getCurrentSession,
  apiClient = api,
  setTimer = (callback, timeoutMs) => globalThis.setTimeout(callback, timeoutMs),
  clearTimer = (timer) => globalThis.clearTimeout(timer),
} = {}) {
  let disposed = false;
  let suspended = false;
  let subscription = null;

  const currentSession = () => currentRuntimeSession(getCurrentSession, disposed, suspended);

  const apiRequest = (session, operation, payload = {}) => apiClient.artifactRuntimeRequest({
    contract_version: ARTIFACT_RUNTIME_REQUEST_CONTRACT,
    operation,
    topic_id: session.topicId,
    artifact_ref: session.artifactRef,
    ...payload,
  }, { timeoutMs: RUNTIME_REQUEST_TIMEOUT_MS });

  const stopSubscription = () => {
    if (subscription) subscription.generation += 1;
    if (subscription?.timer) clearTimer(subscription.timer);
    subscription = null;
  };

  const pauseSubscription = () => {
    if (subscription?.timer) clearTimer(subscription.timer);
    if (subscription) {
      subscription.timer = null;
      subscription.generation += 1;
    }
  };

  const postResponse = (session, requestId, response) => postBridgeMessage(session.binding, {
    type: ARTIFACT_RUNTIME_RESPONSE_TYPE,
    request_id: requestId,
    response,
  });

  const postError = (session, requestId, error) => postBridgeMessage(session.binding, {
    type: ARTIFACT_RUNTIME_RESPONSE_TYPE,
    request_id: requestId,
    response: { ok: false, error: publicRuntimeError(error) },
  });

  const schedulePoll = (record, timeoutMs) => {
    if (disposed || suspended || subscription !== record || record.timer) return;
    const generation = record.generation;
    record.timer = setTimer(() => {
      if (subscription !== record || record.generation !== generation) return;
      record.timer = null;
      void pollEvents(record, generation);
    }, timeoutMs);
  };

  const pollEvents = async (record, generation) => {
    if (disposed || subscription !== record || record.generation !== generation) return;
    if (suspended) {
      pauseSubscription();
      return;
    }
    if (!sessionMatches(currentSession(), record)) {
      if (subscription === record) stopSubscription();
      return;
    }
    try {
      const response = await apiRequest(record, 'events.list', {
        after_event_id: record.cursor,
        limit: 100,
      });
      if (subscription !== record || record.generation !== generation
        || !sessionMatches(currentSession(), record)) return;
      const events = Array.isArray(response?.events) ? response.events : [];
      for (const event of events) {
        if (!sessionMatches(currentSession(), record)) return;
        const eventID = Number(event?.event_id || 0);
        if (!Number.isSafeInteger(eventID) || eventID <= record.cursor) continue;
        postBridgeMessage(record.binding, {
          type: ARTIFACT_RUNTIME_EVENT_TYPE,
          event,
        });
        record.cursor = eventID;
      }
      if (Number.isSafeInteger(response?.event_cursor) && response.event_cursor >= record.cursor) {
        record.cursor = response.event_cursor;
      }
    } catch {
      // A transient poll failure leaves the cursor untouched, so the next
      // successful request replays every committed event.
    }
    if (subscription === record && record.generation === generation && !suspended) {
      schedulePoll(record, RUNTIME_EVENT_POLL_MS);
    }
  };

  const beginSubscription = (session, cursor) => {
    stopSubscription();
    const record = { ...session, cursor, timer: null, generation: 1 };
    subscription = record;
    schedulePoll(record, 0);
    return record;
  };

  const handleRequest = async (event, request) => {
    const session = currentSession();
    const targetOrigin = bindingOrigin(session?.binding);
    if (!session || !targetOrigin
      || event.source !== session.binding.frame?.contentWindow
      || event.origin !== targetOrigin) return;
    try {
      if (request.operation === 'events.unsubscribe') {
        stopSubscription();
        postResponse(session, request.requestId, { ok: true, operation: request.operation });
        return;
      }
      if (request.operation === 'events.subscribe') {
        const requestedCursor = Number(request.payload.after_event_id || 0);
        const record = beginSubscription(
          session,
          Number.isSafeInteger(requestedCursor) && requestedCursor >= 0 ? requestedCursor : 0,
        );
        postResponse(session, request.requestId, {
          ok: true,
          operation: request.operation,
          event_cursor: record.cursor,
        });
        return;
      }
      const response = await apiRequest(session, request.operation, request.payload);
      if (!sessionMatches(currentSession(), session)) return;
      // A write response may commit after an older event that this Viewer has
      // not consumed yet. Only the ordered events.list stream may advance the
      // subscription cursor; the write caller already receives the new State.
      postResponse(session, request.requestId, response);
    } catch (error) {
      if (sessionMatches(currentSession(), session)) postError(session, request.requestId, error);
    }
  };

  const handleWindowMessage = (event) => {
    if (event.data?.type !== ARTIFACT_RUNTIME_REQUEST_TYPE) return;
    const request = normalizeArtifactRuntimeRequest(event.data);
    if (request) void handleRequest(event, request);
  };

  const deactivate = () => {
    if (disposed || suspended) return;
    suspended = true;
    pauseSubscription();
  };

  const suspend = () => {
    if (disposed || suspended) return;
    suspended = true;
    pauseSubscription();
  };

  const resume = () => {
    if (disposed || !suspended) return;
    suspended = false;
    if (!subscription) return;
    if (!sessionMatches(currentSession(), subscription)) {
      stopSubscription();
      return;
    }
    if (!subscription.timer) {
      schedulePoll(subscription, 0);
    }
  };

  const dispose = () => {
    if (disposed) return;
    disposed = true;
    stopSubscription();
  };

  return {
    deactivate,
    dispose,
    handleWindowMessage,
    resume,
    suspend,
  };
}
