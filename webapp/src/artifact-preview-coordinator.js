export const ARTIFACT_VIEWER_PATH = '/artifact-viewer';
export const ARTIFACT_PREVIEW_CHANNEL = 'catsco:artifact-preview:v1';
export const ARTIFACT_PREVIEW_COORDINATION_CONTRACT = 'catsco.artifact-preview-coordination.v1';
export const ARTIFACT_VIEWER_HANDOFF_TIMEOUT_MS = 8000;
export const ARTIFACT_VIEWER_CONTEXT_TIMEOUT_MS = 5000;
export const ARTIFACT_VIEWER_HEARTBEAT_TTL_MS = 7000;
export const ARTIFACT_PREVIEW_LEASE_STORAGE_KEY = 'catsco:artifact-preview-lease:v1';

const ARTIFACT_VIEWER_DISCOVERY_TIMEOUT_MS = 3000;
const ARTIFACT_VIEWER_LATE_RESPONSE_TTL_MS = 15000;
const ARTIFACT_VIEWER_STALE_TIMEOUT_LIMIT = 2;

const ARTIFACT_ID_PATTERN = /^[a-z0-9]+(?:[a-z0-9._-]*[a-z0-9])?$/;
const TOPIC_ID_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._:-]{0,511}$/;
const COORDINATION_ID_PATTERN = /^[A-Za-z0-9_-]{8,160}$/;
const ARTIFACT_CONTEXT_REF_PATTERN = /^acr_[A-Za-z0-9_-]{43}$/;
const COORDINATION_TYPES = new Set([
  'viewer_hello',
  'viewer_ready',
  'viewer_heartbeat',
  'viewer_closed',
  'viewer_released',
  'request_current_preview',
  'current_preview',
  'sidebar_claimed',
  'context_request',
  'context_response',
]);

function positiveInteger(value) {
  const parsed = Number(value);
  return Number.isSafeInteger(parsed) && parsed > 0 ? parsed : 0;
}

function coordinationID(value) {
  const normalized = String(value || '').trim();
  return COORDINATION_ID_PATTERN.test(normalized) ? normalized : '';
}

export function normalizeArtifactPreviewIdentity(value) {
  const topicId = String(value?.topicId || value?.topic_id || '').trim();
  const agentUid = positiveInteger(value?.agentUid ?? value?.agent_uid);
  const artifactId = String(value?.artifactId || value?.artifact_id || '').trim();
  const displayedVersion = positiveInteger(
    value?.displayedVersion ?? value?.displayed_version ?? value?.version,
  );
  if (!TOPIC_ID_PATTERN.test(topicId)
    || agentUid <= 0
    || artifactId.length > 64
    || !ARTIFACT_ID_PATTERN.test(artifactId)
    || displayedVersion <= 0) return null;
  return { topicId, agentUid, artifactId, displayedVersion };
}

export function sameArtifactPreviewIdentity(left, right) {
  const normalizedLeft = normalizeArtifactPreviewIdentity(left);
  const normalizedRight = normalizeArtifactPreviewIdentity(right);
  return Boolean(normalizedLeft && normalizedRight
    && normalizedLeft.topicId === normalizedRight.topicId
    && normalizedLeft.agentUid === normalizedRight.agentUid
    && normalizedLeft.artifactId === normalizedRight.artifactId
    && normalizedLeft.displayedVersion === normalizedRight.displayedVersion);
}

export function artifactPreviewCoordinationID(prefix = 'apv') {
  const safePrefix = String(prefix || 'apv').replace(/[^A-Za-z0-9_-]/g, '').slice(0, 20) || 'apv';
  const random = globalThis.crypto?.randomUUID?.().replace(/-/g, '')
    || `${Date.now().toString(36)}${Math.random().toString(36).slice(2, 14)}`;
  return `${safePrefix}_${random}`;
}

export function createArtifactViewerURL(value, {
  handoffId = '',
  origin = globalThis.location?.origin,
} = {}) {
  const identity = normalizeArtifactPreviewIdentity(value);
  const normalizedHandoffId = coordinationID(handoffId);
  if (!identity || !origin || !normalizedHandoffId) return '';
  try {
    const url = new URL(ARTIFACT_VIEWER_PATH, origin);
    url.searchParams.set('topic', identity.topicId);
    url.searchParams.set('agent', String(identity.agentUid));
    url.searchParams.set('artifact', identity.artifactId);
    url.searchParams.set('version', String(identity.displayedVersion));
    url.searchParams.set('handoff', normalizedHandoffId);
    return url.toString();
  } catch {
    return '';
  }
}

export function parseArtifactViewerLocation(location = globalThis.location) {
  if (!location || String(location.pathname || '') !== ARTIFACT_VIEWER_PATH) return null;
  const params = new URLSearchParams(String(location.search || ''));
  const identity = normalizeArtifactPreviewIdentity({
    topicId: params.get('topic'),
    agentUid: params.get('agent'),
    artifactId: params.get('artifact'),
    displayedVersion: params.get('version'),
  });
  const handoffId = coordinationID(params.get('handoff'));
  return identity && handoffId ? { ...identity, handoffId } : null;
}

export function createArtifactPreviewMessage(type, identity, extra = {}) {
  const normalizedType = String(type || '').trim();
  const normalizedIdentity = normalizeArtifactPreviewIdentity(identity);
  if (!COORDINATION_TYPES.has(normalizedType) || !normalizedIdentity) return null;
  return {
    ...extra,
    contract_version: ARTIFACT_PREVIEW_COORDINATION_CONTRACT,
    type: normalizedType,
    topic_id: normalizedIdentity.topicId,
    agent_uid: normalizedIdentity.agentUid,
    artifact_id: normalizedIdentity.artifactId,
    displayed_version: normalizedIdentity.displayedVersion,
  };
}

export function normalizeArtifactPreviewMessage(value) {
  if (!value || typeof value !== 'object' || Array.isArray(value)
    || value.contract_version !== ARTIFACT_PREVIEW_COORDINATION_CONTRACT
    || !COORDINATION_TYPES.has(String(value.type || ''))) return null;
  const identity = normalizeArtifactPreviewIdentity(value);
  if (!identity) return null;
  const normalized = {
    type: String(value.type),
    ...identity,
  };
  const viewerId = coordinationID(value.viewer_id);
  const handoffId = coordinationID(value.handoff_id);
  const requestId = coordinationID(value.request_id);
  if (viewerId) normalized.viewerId = viewerId;
  if (handoffId) normalized.handoffId = handoffId;
  if (requestId) normalized.requestId = requestId;
  if (ARTIFACT_CONTEXT_REF_PATTERN.test(String(value.context_ref || ''))) {
    normalized.contextRef = value.context_ref;
  }
  if (typeof value.error === 'string' && value.error.length <= 128) {
    normalized.error = value.error;
  }
  const sentAt = Number(value.sent_at);
  if (Number.isFinite(sentAt) && sentAt > 0) normalized.sentAt = sentAt;
  return normalized;
}

export function createArtifactPreviewChannel() {
  if (typeof globalThis.BroadcastChannel !== 'function') return null;
  try {
    return new globalThis.BroadcastChannel(ARTIFACT_PREVIEW_CHANNEL);
  } catch {
    return null;
  }
}

function normalizeArtifactPreviewViewerLease(value) {
  const identity = normalizeArtifactPreviewIdentity(value?.identity || value);
  const viewerId = coordinationID(value?.viewerId || value?.viewer_id);
  const handoffId = coordinationID(value?.handoffId || value?.handoff_id);
  return identity && viewerId && handoffId
    ? { ...identity, viewerId, handoffId }
    : null;
}

export function createArtifactPreviewLeaseStore(storage = undefined) {
  const resolveStorage = () => {
    if (storage !== undefined) return storage;
    try {
      return globalThis.sessionStorage || null;
    } catch {
      return null;
    }
  };

  return {
    read() {
      try {
        const serialized = resolveStorage()?.getItem(ARTIFACT_PREVIEW_LEASE_STORAGE_KEY);
        return serialized
          ? normalizeArtifactPreviewViewerLease(JSON.parse(serialized))
          : null;
      } catch {
        return null;
      }
    },
    write(value) {
      const lease = normalizeArtifactPreviewViewerLease(value);
      if (!lease) return false;
      try {
        resolveStorage()?.setItem(ARTIFACT_PREVIEW_LEASE_STORAGE_KEY, JSON.stringify(lease));
        return Boolean(resolveStorage());
      } catch {
        return false;
      }
    },
    clear() {
      try {
        resolveStorage()?.removeItem(ARTIFACT_PREVIEW_LEASE_STORAGE_KEY);
      } catch {
        // Storage can be unavailable in restricted browser contexts.
      }
    },
  };
}

export function createArtifactPreviewChatCoordinator({
  channel = createArtifactPreviewChannel(),
  now = () => Date.now(),
  setTimer = (callback, timeoutMs) => globalThis.setTimeout(callback, timeoutMs),
  clearTimer = (timer) => globalThis.clearTimeout(timer),
  recoveryLease = null,
  onViewerLeaseChange = () => {},
} = {}) {
  if (!channel) return null;

  let closed = false;
  let activeViewer = null;
  let recoverableViewer = normalizeArtifactPreviewViewerLease(recoveryLease);
  let activeContextRequest = null;
  const pendingHandoffs = new Map();
  const queuedContextRequests = [];
  const lateContextRequests = new Map();
  const pendingDiscoveries = new Map();

  const post = (type, identity, extra = {}) => {
    if (closed) return false;
    const message = createArtifactPreviewMessage(type, identity, {
      sent_at: now(),
      ...extra,
    });
    if (!message) return false;
    try {
      channel.postMessage(message);
      return true;
    } catch {
      return false;
    }
  };

  const finishHandoff = (handoffId, viewer = null) => {
    const pending = pendingHandoffs.get(handoffId);
    if (!pending) return;
    pendingHandoffs.delete(handoffId);
    clearTimer(pending.timer);
    pending.resolve(viewer);
  };

  const sameViewerLease = (left, right) => Boolean(left && right
    && left.viewerId === right.viewerId
    && left.handoffId === right.handoffId
    && sameArtifactPreviewIdentity(left.identity || left, right.identity || right));

  const sameViewerHandoff = (left, right) => Boolean(left && right
    && left.handoffId === right.handoffId
    && sameArtifactPreviewIdentity(left.identity || left, right.identity || right));

  let clearDiscovery = () => {};

  const setRecoverableViewer = (value) => {
    const next = normalizeArtifactPreviewViewerLease(value);
    const changed = !sameViewerLease(recoverableViewer, next);
    recoverableViewer = next;
    if (changed) onViewerLeaseChange(next ? { ...next } : null);
  };

  const activateViewer = (value) => {
    const viewer = normalizeArtifactPreviewViewerLease(value);
    if (!viewer) return null;
    activeViewer = {
      ...viewer,
      lastSeenAt: now(),
      consecutiveTimeouts: 0,
    };
    setRecoverableViewer(viewer);
    [...pendingDiscoveries.entries()].forEach(([requestId, discovery]) => {
      if (!sameViewerLease(discovery, viewer)) clearDiscovery(requestId);
    });
    return activeViewer;
  };

  const touchActiveViewer = (value) => {
    if (!sameViewerLease(activeViewer, value)) return false;
    activeViewer = {
      ...activeViewer,
      lastSeenAt: now(),
      consecutiveTimeouts: 0,
    };
    return true;
  };

  const settleContextRequest = (request, contextRef = '') => {
    if (!request || request.settled) return;
    request.settled = true;
    if (request.timer) clearTimer(request.timer);
    request.resolve(ARTIFACT_CONTEXT_REF_PATTERN.test(contextRef) ? contextRef : '');
  };

  const clearLateContextRequest = (requestId) => {
    const late = lateContextRequests.get(requestId);
    if (!late) return;
    lateContextRequests.delete(requestId);
    if (late.expiryTimer) clearTimer(late.expiryTimer);
  };

  const rememberLateContextRequest = (request) => {
    const expiryTimer = setTimer(
      () => clearLateContextRequest(request.requestId),
      ARTIFACT_VIEWER_LATE_RESPONSE_TTL_MS,
    );
    lateContextRequests.set(request.requestId, { ...request, expiryTimer });
  };

  clearDiscovery = (requestId) => {
    const discovery = pendingDiscoveries.get(requestId);
    if (!discovery) return;
    pendingDiscoveries.delete(requestId);
    clearTimer(discovery.timer);
  };

  const clearAllDiscoveries = () => {
    [...pendingDiscoveries.keys()].forEach(clearDiscovery);
  };

  const liveViewer = (identity = null) => {
    if (!activeViewer) return null;
    if (!activeViewer.viewerId) return null;
    return !identity || sameArtifactPreviewIdentity(activeViewer, identity)
      ? {
          ...activeViewer,
          heartbeatStale: now() - activeViewer.lastSeenAt > ARTIFACT_VIEWER_HEARTBEAT_TTL_MS,
        }
      : null;
  };

  let dispatchNextContextRequest = () => {};

  const completeActiveContextRequest = (contextRef = '') => {
    const request = activeContextRequest;
    if (!request) return;
    activeContextRequest = null;
    settleContextRequest(request, contextRef);
    dispatchNextContextRequest();
  };

  const markContextTimeout = (request, { rememberLate = true } = {}) => {
    if (activeContextRequest !== request) return;
    activeContextRequest = null;
    if (rememberLate) rememberLateContextRequest(request);
    if (sameViewerLease(activeViewer, request)) {
      const consecutiveTimeouts = Number(activeViewer.consecutiveTimeouts || 0) + 1;
      const heartbeatStale = now() - activeViewer.lastSeenAt > ARTIFACT_VIEWER_HEARTBEAT_TTL_MS;
      activeViewer = consecutiveTimeouts >= ARTIFACT_VIEWER_STALE_TIMEOUT_LIMIT && heartbeatStale
        ? null
        : { ...activeViewer, consecutiveTimeouts };
    }
    settleContextRequest(request);
    dispatchNextContextRequest();
  };

  dispatchNextContextRequest = () => {
    if (closed || activeContextRequest) return;
    while (queuedContextRequests.length > 0) {
      const request = queuedContextRequests.shift();
      const viewer = liveViewer(request.identity);
      if (!sameViewerLease(viewer, request)) {
        settleContextRequest(request);
        continue;
      }
      activeContextRequest = request;
      request.timer = setTimer(
        () => markContextTimeout(request),
        request.timeoutMs,
      );
      if (!post('context_request', request.identity, {
        viewer_id: request.viewerId,
        handoff_id: request.handoffId,
        request_id: request.requestId,
      })) {
        markContextTimeout(request, { rememberLate: false });
      }
      return;
    }
  };

  const cancelContextRequests = (matches = () => true) => {
    if (activeContextRequest && matches(activeContextRequest)) {
      const request = activeContextRequest;
      activeContextRequest = null;
      settleContextRequest(request);
    }
    for (let index = queuedContextRequests.length - 1; index >= 0; index -= 1) {
      const request = queuedContextRequests[index];
      if (!matches(request)) continue;
      queuedContextRequests.splice(index, 1);
      settleContextRequest(request);
    }
    [...lateContextRequests.entries()].forEach(([requestId, request]) => {
      if (matches(request)) clearLateContextRequest(requestId);
    });
    dispatchNextContextRequest();
  };

  const probeViewer = (value) => {
    const candidate = normalizeArtifactPreviewViewerLease(value);
    if (!candidate || !sameViewerHandoff(candidate, recoverableViewer)) return false;
    if (activeViewer
      && activeViewer.viewerId !== candidate.viewerId
      && now() - activeViewer.lastSeenAt <= ARTIFACT_VIEWER_HEARTBEAT_TTL_MS) return false;
    const existing = [...pendingDiscoveries.values()]
      .some((discovery) => sameViewerLease(discovery, candidate));
    if (existing) return true;
    const requestId = artifactPreviewCoordinationID('discover');
    const timer = setTimer(
      () => clearDiscovery(requestId),
      ARTIFACT_VIEWER_DISCOVERY_TIMEOUT_MS,
    );
    pendingDiscoveries.set(requestId, { ...candidate, requestId, timer });
    if (!post('request_current_preview', candidate, {
      viewer_id: candidate.viewerId,
      handoff_id: candidate.handoffId,
      request_id: requestId,
    })) {
      clearDiscovery(requestId);
      return false;
    }
    return true;
  };

  const handleMessage = (rawMessage) => {
    const message = normalizeArtifactPreviewMessage(rawMessage);
    if (!message) return;

    if (message.type === 'viewer_ready') {
      const pending = message.handoffId
        ? pendingHandoffs.get(message.handoffId)
        : null;
      if (pending
        && message.viewerId
        && sameArtifactPreviewIdentity(message, pending.identity)) {
        activateViewer({
          ...pending.identity,
          viewerId: message.viewerId,
          handoffId: message.handoffId,
        });
        finishHandoff(message.handoffId, { ...activeViewer });
        return;
      }
      if (message.viewerId
        && sameViewerHandoff(message, recoverableViewer)
        && (!activeViewer
          || message.viewerId === activeViewer.viewerId
          || now() - activeViewer.lastSeenAt > ARTIFACT_VIEWER_HEARTBEAT_TTL_MS)) {
        activateViewer(message);
      }
      return;
    }

    if (message.type === 'viewer_hello' || message.type === 'viewer_heartbeat') {
      if (touchActiveViewer(message)) return;
      if (message.viewerId && sameViewerHandoff(message, recoverableViewer)) {
        probeViewer(message);
      }
      return;
    }

    if (message.type === 'current_preview') {
      const discovery = message.requestId
        ? pendingDiscoveries.get(message.requestId)
        : null;
      if (discovery && sameViewerLease(message, discovery)) {
        clearDiscovery(message.requestId);
        activateViewer(discovery);
      } else {
        touchActiveViewer(message);
      }
      return;
    }

    if (message.type === 'viewer_closed' || message.type === 'viewer_released') {
      if (message.handoffId && pendingHandoffs.has(message.handoffId)) {
        const pending = pendingHandoffs.get(message.handoffId);
        if (sameArtifactPreviewIdentity(message, pending.identity)) {
          finishHandoff(message.handoffId, null);
        }
      }
      if (sameViewerLease(activeViewer, message)) {
        activeViewer = null;
        cancelContextRequests((request) => sameViewerLease(request, message));
      }
      if (message.type === 'viewer_released' && sameViewerLease(recoverableViewer, message)) {
        setRecoverableViewer(null);
        clearAllDiscoveries();
      }
      return;
    }

    if (message.type !== 'context_response' || !message.requestId) return;
    const pending = activeContextRequest?.requestId === message.requestId
      ? activeContextRequest
      : null;
    if (pending && sameViewerLease(message, pending)) {
      touchActiveViewer(message);
      completeActiveContextRequest(message.contextRef || '');
      return;
    }
    const late = lateContextRequests.get(message.requestId);
    if (late && sameViewerLease(message, late)) {
      clearLateContextRequest(message.requestId);
      if (sameViewerHandoff(late, recoverableViewer)
        && (!activeViewer || sameViewerLease(activeViewer, late))) activateViewer(late);
    }
  };

  channel.onmessage = (event) => handleMessage(event?.data);
  if (recoverableViewer) probeViewer(recoverableViewer);

  return {
    beginHandoff(identity, handoffId, {
      timeoutMs = ARTIFACT_VIEWER_HANDOFF_TIMEOUT_MS,
    } = {}) {
      const normalizedIdentity = normalizeArtifactPreviewIdentity(identity);
      const normalizedHandoffId = coordinationID(handoffId);
      if (!normalizedIdentity || !normalizedHandoffId || closed) return null;
      if (pendingHandoffs.has(normalizedHandoffId)) return null;
      let resolvePromise;
      const promise = new Promise((resolve) => {
        resolvePromise = resolve;
      });
      const timer = setTimer(
        () => finishHandoff(normalizedHandoffId, null),
        Math.max(1, Number(timeoutMs) || ARTIFACT_VIEWER_HANDOFF_TIMEOUT_MS),
      );
      pendingHandoffs.set(normalizedHandoffId, {
        identity: normalizedIdentity,
        resolve: resolvePromise,
        timer,
      });
      return {
        promise,
        cancel({ release = false } = {}) {
          if (!pendingHandoffs.has(normalizedHandoffId)) return;
          if (release) {
            post('sidebar_claimed', normalizedIdentity, {
              handoff_id: normalizedHandoffId,
            });
          }
          finishHandoff(normalizedHandoffId, null);
        },
      };
    },

    claimSidebar() {
      pendingHandoffs.forEach((pending, handoffId) => {
        post('sidebar_claimed', pending.identity, { handoff_id: handoffId });
        finishHandoff(handoffId, null);
      });
      const confirmedViewer = activeViewer;
      const recoveringViewer = confirmedViewer ? null : recoverableViewer;
      activeViewer = null;
      setRecoverableViewer(null);
      clearAllDiscoveries();
      if (confirmedViewer) {
        post('sidebar_claimed', confirmedViewer, {
          viewer_id: confirmedViewer.viewerId,
          handoff_id: confirmedViewer.handoffId,
        });
      } else if (recoveringViewer) {
        post('sidebar_claimed', recoveringViewer, {
          handoff_id: recoveringViewer.handoffId,
        });
      }
      cancelContextRequests();
    },

    getActiveViewer(identity = null) {
      return liveViewer(identity);
    },

    requestContext(identity, {
      timeoutMs = ARTIFACT_VIEWER_CONTEXT_TIMEOUT_MS,
    } = {}) {
      const normalizedIdentity = normalizeArtifactPreviewIdentity(identity);
      const viewer = normalizedIdentity ? liveViewer(normalizedIdentity) : null;
      if (!viewer || closed) return Promise.resolve('');
      const requestId = artifactPreviewCoordinationID('request');
      return new Promise((resolve) => {
        queuedContextRequests.push({
          identity: normalizedIdentity,
          viewerId: viewer.viewerId,
          handoffId: viewer.handoffId,
          requestId,
          resolve,
          timer: null,
          timeoutMs: Math.max(1, Number(timeoutMs) || ARTIFACT_VIEWER_CONTEXT_TIMEOUT_MS),
          settled: false,
        });
        dispatchNextContextRequest();
      });
    },

    requestCurrentPreview() {
      return probeViewer(liveViewer() || recoverableViewer);
    },

    close() {
      if (closed) return;
      closed = true;
      pendingHandoffs.forEach((_, handoffId) => finishHandoff(handoffId, null));
      cancelContextRequests();
      clearAllDiscoveries();
      [...lateContextRequests.keys()].forEach(clearLateContextRequest);
      channel.onmessage = null;
      channel.close();
      activeViewer = null;
    },

    handleMessage,
  };
}
