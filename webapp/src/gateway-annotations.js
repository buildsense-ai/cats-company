// Gateway application annotation bridge.
//
// Shared between the host page (CatsCo webapp) and the in-app annotation SDK
// script served from webapp/public/catsco-annotations.js. Gateway-only
// applications run cross-origin in an <iframe>; everything here is
// protocol-level: exact-origin + event.source validation on both sides, a
// per-binding session id, bounded schema normalization, and full dispose
// cleanup. No registry artifact identity, no task host dependency, and the
// SDK never posts chat messages by itself.

export const GATEWAY_ANNOTATIONS_CONTRACT = 'catsco.gateway-annotations.v1';
export const GATEWAY_ANNOTATION_BRIDGE_CONTRACT = 'catsco.gateway-annotation-bridge.v1';

export const GATEWAY_HOST_CONNECT_TYPE = 'catsco.gateway.annotation.connect.v1';
export const GATEWAY_SDK_READY_TYPE = 'catsco.gateway.annotation.ready.v1';
export const GATEWAY_HOST_MODE_TYPE = 'catsco.gateway.annotation.mode.v1';
export const GATEWAY_SDK_TARGET_TYPE = 'catsco.gateway.annotation.target.v1';
export const GATEWAY_SDK_PAGE_TYPE = 'catsco.gateway.annotation.page.v1';

const SUPPORTED_KINDS = ['element', 'text', 'region'];
const SUPPORTED_MODES = ['off', 'element', 'text', 'region'];
const SUPPORTED_CAPABILITIES = ['element', 'text', 'region'];

// Gateway application ids use the same naming rule as server/artifact_launch.go.
const APP_ID_PATTERN = /^[a-z][a-z0-9_-]{0,47}$/;

const MAX_ANNOTATIONS = 20;
const MAX_TOTAL_BYTES = 16 * 1024;
const MAX_BODY_CHARS = 2000;
const MAX_LABEL_CHARS = 256;
const MAX_TEXT_CHARS = 2000;
const MAX_AFFIX_CHARS = 256;
const MAX_ID_CHARS = 128;
const MAX_SELECTOR_CHARS = 512;
const MAX_PATH_CHARS = 1024;
const MAX_REVISION_CHARS = 128;
const MAX_AGENT_UID = 2 ** 53;
const MAX_VIEWPORT_VALUE = 2 ** 20;
const UNIT_EPSILON = 1e-6;

// Identifiers (ids, selectors, paths, revisions) reject every control
// character; free-form text (text/prefix/suffix) may carry line breaks.
const CONTROL_FORBIDDEN = /[\u0000-\u001f\u007f]/;
const FREE_TEXT_FORBIDDEN = /[\u0000-\u0009\u000b\u000c\u000e-\u001f\u007f]/;

function plainObject(value) {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return false;
  const prototype = Object.getPrototypeOf(value);
  return prototype === Object.prototype || prototype === null;
}

function boundedString(value, maxLength, allowEmpty = false) {
  if (typeof value !== 'string') return null;
  if (value.length > maxLength) return null;
  if (!allowEmpty && value.length === 0) return null;
  return value;
}

function finiteNumber(value) {
  return typeof value === 'number' && Number.isFinite(value) ? value : null;
}

function nonNegativeNumber(value, max) {
  const n = finiteNumber(value);
  return n !== null && n >= 0 && n <= max ? n : null;
}

function normalizedRect(value) {
  if (!plainObject(value)) return null;
  const x = finiteNumber(value.x);
  const y = finiteNumber(value.y);
  const width = finiteNumber(value.width);
  const height = finiteNumber(value.height);
  if (x === null || y === null || width === null || height === null) return null;
  // Viewport-normalized 0..1 (small float drift allowed, matching the
  // server's unit epsilon); zero-area rects carry no layout evidence.
  if (x < 0 || y < 0 || width <= 0 || height <= 0) return null;
  if (x + width > 1 + UNIT_EPSILON || y + height > 1 + UNIT_EPSILON) return null;
  return { x, y, width, height };
}

function normalizedViewport(value) {
  if (!plainObject(value)) return null;
  const width = nonNegativeNumber(value.width, MAX_VIEWPORT_VALUE);
  const height = nonNegativeNumber(value.height, MAX_VIEWPORT_VALUE);
  if (width === null || width === 0 || height === null || height === 0) return null;
  // scroll evidence is optional and defaults to 0, matching the server.
  const scrollX = value.scroll_x === undefined || value.scroll_x === null
    ? 0 : nonNegativeNumber(value.scroll_x, MAX_VIEWPORT_VALUE);
  const scrollY = value.scroll_y === undefined || value.scroll_y === null
    ? 0 : nonNegativeNumber(value.scroll_y, MAX_VIEWPORT_VALUE);
  if (scrollX === null || scrollY === null) return null;
  return { width, height, scroll_x: scrollX, scroll_y: scrollY };
}

function normalizedTarget(kind, value) {
  if (!plainObject(value)) return null;
  const target = {};
  const strictKeys = new Set(['element_id', 'selector', 'text', 'prefix', 'suffix', 'rect', 'coordinate_space', 'viewport']);
  for (const key of Object.keys(value)) {
    if (!strictKeys.has(key) || key === '__proto__' || key === 'constructor' || key === 'prototype') return null;
  }

  if (value.element_id !== undefined && value.element_id !== null) {
    target.element_id = boundedString(value.element_id, MAX_ID_CHARS);
    if (target.element_id === null || CONTROL_FORBIDDEN.test(target.element_id)) return null;
  }
  if (value.selector !== undefined && value.selector !== null) {
    target.selector = boundedString(value.selector, MAX_SELECTOR_CHARS);
    if (target.selector === null || CONTROL_FORBIDDEN.test(target.selector)) return null;
  }
  if (value.text !== undefined && value.text !== null) {
    target.text = boundedString(value.text, MAX_TEXT_CHARS, true);
    if (target.text === null || FREE_TEXT_FORBIDDEN.test(target.text)) return null;
  }
  if (value.prefix !== undefined && value.prefix !== null) {
    target.prefix = boundedString(value.prefix, MAX_AFFIX_CHARS, true);
    if (target.prefix === null || FREE_TEXT_FORBIDDEN.test(target.prefix)) return null;
  }
  if (value.suffix !== undefined && value.suffix !== null) {
    target.suffix = boundedString(value.suffix, MAX_AFFIX_CHARS, true);
    if (target.suffix === null || FREE_TEXT_FORBIDDEN.test(target.suffix)) return null;
  }
  if (value.rect !== undefined && value.rect !== null) {
    target.rect = normalizedRect(value.rect);
    if (target.rect === null) return null;
  }
  if (value.coordinate_space !== undefined && value.coordinate_space !== null) {
    // The bridge only knows the iframe viewport; anything else is rejected.
    if (value.coordinate_space !== 'viewport') return null;
    target.coordinate_space = 'viewport';
  }
  if (value.viewport !== undefined && value.viewport !== null) {
    target.viewport = normalizedViewport(value.viewport);
    if (target.viewport === null) return null;
  }

  if (kind === 'element') {
    if (typeof target.element_id !== 'string' && typeof target.selector !== 'string') return null;
  } else if (kind === 'text') {
    if (typeof target.text !== 'string' || !/\S/.test(target.text)) return null;
  } else if (kind === 'region') {
    if (!plainObject(target.rect)) return null;
    if (target.coordinate_space !== 'viewport') return null;
    if (!plainObject(target.viewport)) return null;
  }
  return target;
}

function normalizedPage(value) {
  if (!plainObject(value)) return null;
  const page = {};
  const strictKeys = new Set(['path', 'revision']);
  for (const key of Object.keys(value)) {
    if (!strictKeys.has(key) || key === '__proto__' || key === 'constructor' || key === 'prototype') return null;
  }
  const path = value.path;
  if (typeof path !== 'string' || !path.startsWith('/') || path.length > MAX_PATH_CHARS) return null;
  if (CONTROL_FORBIDDEN.test(path) || path.includes('?') || path.includes('#')) return null;
  page.path = path;
  if (value.revision !== undefined && value.revision !== null) {
    if (typeof value.revision !== 'string' || value.revision.length === 0 || value.revision.length > MAX_REVISION_CHARS) return null;
    page.revision = value.revision;
  }
  return page;
}

function utf8Length(value) {
  return new TextEncoder().encode(value).length;
}

export function normalizeGatewayAnnotationSelection(value) {
  if (!plainObject(value)) return null;
  for (const key of Object.keys(value)) {
    if (!['id', 'kind', 'label', 'target'].includes(key)) return null;
  }
  const id = boundedString(value.id, MAX_ID_CHARS);
  if (id === null || CONTROL_FORBIDDEN.test(id)) return null;
  if (!SUPPORTED_KINDS.includes(value.kind)) return null;
  const kind = value.kind;
  const label = value.label === undefined || value.label === null ? '' : value.label;
  if (typeof label !== 'string' || label.length > MAX_LABEL_CHARS) return null;
  const target = normalizedTarget(kind, value.target);
  if (target === null) return null;
  return { id, kind, label, target };
}

// Document-level annotations carry the user's comment body; the server
// requires a non-blank body on every persisted annotation.
function normalizeGatewayAnnotationDocumentEntry(value) {
  if (!plainObject(value)) return null;
  for (const key of Object.keys(value)) {
    if (!['id', 'kind', 'label', 'body', 'target'].includes(key)) return null;
  }
  const body = boundedString(value.body, MAX_BODY_CHARS, true);
  if (body === null || !/\S/.test(body) || FREE_TEXT_FORBIDDEN.test(body)) return null;
  const selection = normalizeGatewayAnnotationSelection({
    id: value.id, kind: value.kind, label: value.label ?? '', target: value.target,
  });
  if (selection === null) return null;
  return { ...selection, body };
}

export function normalizeGatewayAnnotations(value) {
  if (!plainObject(value)) return null;
  for (const key of Object.keys(value)) {
    if (!['contract_version', 'agent_uid', 'app_id', 'page', 'annotations'].includes(key)) return null;
  }
  if (value.contract_version !== GATEWAY_ANNOTATIONS_CONTRACT) return null;
  const agentUid = finiteNumber(value.agent_uid);
  if (agentUid === null || agentUid < 1 || !Number.isInteger(agentUid) || agentUid > MAX_AGENT_UID) return null;
  const appId = boundedString(value.app_id, 48);
  if (appId === null || !APP_ID_PATTERN.test(appId)) return null;
  const page = normalizedPage(value.page);
  if (page === null) return null;
  if (!Array.isArray(value.annotations) || value.annotations.length > MAX_ANNOTATIONS) return null;
  const seenIds = new Set();
  const annotations = value.annotations.map((entry) => {
    const normalizedEntry = normalizeGatewayAnnotationDocumentEntry(entry);
    if (normalizedEntry === null || seenIds.has(normalizedEntry.id)) return null;
    seenIds.add(normalizedEntry.id);
    return normalizedEntry;
  });
  if (annotations.some((entry) => entry === null)) return null;
  const normalized = {
    contract_version: GATEWAY_ANNOTATIONS_CONTRACT,
    agent_uid: agentUid,
    app_id: appId,
    page,
    annotations: annotations.map(({ id, kind, label, body, target }) => ({ id, kind, label, body, target })),
  };
  let totalBytes;
  try {
    totalBytes = utf8Length(JSON.stringify(normalized));
  } catch {
    return null;
  }
  if (totalBytes > MAX_TOTAL_BYTES) return null;
  return normalized;
}

function bindingOrigin(binding) {
  try {
    return new URL(binding?.url || '', window.location.href).origin;
  } catch {
    return '';
  }
}

function inboundOrigin(event) {
  return event?.origin === 'null' ? '' : String(event?.origin || '');
}

function postToFrame(binding, message) {
  const contentWindow = binding?.frame?.contentWindow;
  const targetOrigin = bindingOrigin(binding);
  if (!contentWindow?.postMessage || !targetOrigin || targetOrigin === 'null') return false;
  try {
    contentWindow.postMessage(message, targetOrigin);
    return true;
  } catch {
    return false;
  }
}

function bindingKey(binding) {
  return `${binding?.agentUid ?? ''}|${binding?.appId ?? ''}`;
}

// Session identity -------------------------------------------------------
//
// Session tokens are opaque, monotonic per host instance, and only minted by
// this module. Frames cannot mint or guess them across hosts without seeing
// the exact token over a validated channel first, so stale frame replay from
// an old document or a foreign session collapses into 'stale-session' and is
// ignored.

function createSessionToken() {
  const cryptoInstance = typeof globalThis.crypto?.randomUUID === 'function' ? globalThis.crypto : null;
  if (cryptoInstance) {
    return `catsco_${cryptoInstance.randomUUID()}`;
  }
  return `catsco_${Math.random().toString(36).slice(2)}${Date.now().toString(36)}`;
}

export function createGatewayAnnotationHost({
  getBinding,
  onReady,
  onSelection,
  onPageChange,
  onUnavailable,
} = {}) {
  if (typeof getBinding !== 'function') {
    throw new Error('getBinding is required');
  }
  const state = {
    disposed: false,
    session: null, // { token, binding, bindingKey, appId, agentUid, mode, frame, page }
    readyNotified: false,
    ready: null,
    page: null,
    signal: null,
    onAbort: null,
  };

  function watchSignal(binding) {
    const signal = binding?.signal;
    if (!state.onAbort) {
      state.onAbort = () => revoke('binding-gone');
    }
    if (signal && typeof signal.addEventListener === 'function' && !state.signal) {
      state.signal = signal;
      signal.addEventListener('abort', state.onAbort, { once: true });
    }
  }

  function unwatchSignal() {
    if (state.signal && state.onAbort && typeof state.signal.removeEventListener === 'function') {
      state.signal.removeEventListener('abort', state.onAbort);
    }
    state.signal = null;
  }

  function notify(callback, ...args) {
    if (state.disposed || typeof callback !== 'function') return;
    try {
      callback(...args);
    } catch {
      // Host callbacks run inside DOM event dispatch; a throwing consumer
      // must not break the dispatcher or other windows' message handlers.
    }
  }

  function revoke(reason = 'rebind', { silent = false } = {}) {
    const previous = state.session;
    state.session = null;
    state.readyNotified = false;
    state.ready = null;
    state.page = null;
    unwatchSignal();
    if (previous && !silent) {
      notify(onUnavailable, previous.binding, reason);
    }
    return previous ?? null;
  }

  function hostConnectMessage() {
    return {
      type: GATEWAY_HOST_CONNECT_TYPE,
      contract_version: GATEWAY_ANNOTATION_BRIDGE_CONTRACT,
      session_id: state.session.token,
      request_id: state.session.requestId,
    };
  }

  function validSession(event) {
    const current = state.session;
    if (!current) return 'no-session';
    if (event.source !== current.frame?.contentWindow) return 'wrong-frame';
    if (!bindingOrigin(current.binding) || bindingOrigin(current.binding) !== inboundOrigin(event)) {
      return 'bad-origin';
    }
    const payload = event.data;
    if (!plainObject(payload)) return 'bad-payload';
    if (payload.contract_version !== GATEWAY_ANNOTATION_BRIDGE_CONTRACT) return 'wrong-contract';
    if (payload.session_id !== current.token) return 'stale-session';
    return null;
  }

  function defaultPage() {
    return { path: '/', revision: undefined };
  }

  function mergedPage(incoming, preferIncoming = true) {
    const base = preferIncoming ? (state.page ?? defaultPage()) : defaultPage();
    if (!incoming) return base;
    const next = { ...base };
    if (typeof incoming.path === 'string') next.path = incoming.path;
    if (incoming.revision === undefined || incoming.revision === null) delete next.revision;
    else next.revision = incoming.revision;
    return next;
  }

  function handleReady(event, payload) {
    // A ready only completes the handshake it answers: the request_id is
    // minted per connect and echoed by the SDK, so a late ready from an
    // older connect (or a replayed one) cannot activate the new session.
    if (payload.request_id !== state.session.requestId) return;
    const capabilities = payload.capabilities;
    if (!Array.isArray(capabilities) || capabilities.length === 0
      || capabilities.some((item) => !SUPPORTED_CAPABILITIES.includes(item))) {
      notify(onUnavailable, state.session.binding, 'bad-capabilities');
      revoke('bad-capabilities', { silent: true });
      return;
    }
    const reportedPage = normalizedPage(payload.page) ?? defaultPage();
    state.page = reportedPage;
    state.ready = { capabilities: capabilities.slice(), page: reportedPage };
    // A connect may be re-sent by the host (e.g. reconnect after navigation);
    // a duplicate ready must not notify the UI twice.
    const firstReady = !state.readyNotified;
    state.readyNotified = true;
    if (firstReady) notify(onReady, state.ready);
    setModeInternal();
  }

  function setModeInternal() {
    if (!state.session) return false;
    postToFrame(state.session.binding, {
      type: GATEWAY_HOST_MODE_TYPE,
      contract_version: GATEWAY_ANNOTATION_BRIDGE_CONTRACT,
      session_id: state.session.token,
      mode: state.session.mode,
    });
    return true;
  }

  function handlePage(event, payload) {
    // Page/target reports are only meaningful on a completed handshake.
    if (!state.ready) return;
    const page = normalizedPage(payload.page);
    if (page === null) {
      notify(onUnavailable, state.session.binding, 'bad-page');
      return;
    }
    // A page report is a full snapshot of the frame's current document:
    // replace, never merge — a dropped revision must actually drop.
    state.page = page;
    notify(onPageChange, state.page);
  }

  function handleTarget(event, payload) {
    // A target is only accepted after ready, in the currently active
    // explicit mode, for a kind the SDK declared, on the page the host
    // currently tracks. Anything else is stale or mixed-document state.
    if (!state.ready) return;
    const mode = state.session.mode;
    if (mode === 'off') return;
    // Out-of-mode messages are silent noise (a buggy or racing SDK), not
    // reportable protocol violations; kind is checked on the raw payload
    // before deeper schema validation.
    if (payload?.selection?.kind !== mode) return;
    const selection = normalizeGatewayAnnotationSelection(payload.selection);
    if (selection === null) {
      notify(onUnavailable, state.session.binding, 'bad-selection');
      return;
    }
    if (!state.ready.capabilities.includes(selection.kind)) {
      notify(onUnavailable, state.session.binding, 'capability-mismatch');
      return;
    }
    const page = normalizedPage(payload.page);
    if (page === null) {
      notify(onUnavailable, state.session.binding, 'bad-page');
      return;
    }
    if (page.path !== state.page.path || (page.revision || '') !== (state.page.revision || '')) {
      // The frame is speaking for a document the host has not accepted as
      // current (missing page.v1 or out-of-order messages): the target must
      // not be silently re-anchored onto a page it does not belong to.
      notify(onUnavailable, state.session.binding, 'page-drift');
      return;
    }
    notify(onSelection, selection, state.page);
  }

  function handleWindowMessage(event) {
    if (state.disposed) return;
    const current = state.session;
    if (current?.frame) {
      // Re-check the live binding on every message: if the consumer's
      // binding has moved on or disappeared, messages from the stale frame
      // are refused even before session/origin checks.
      if (!revalidateBinding()) return;
      const failure = validSession(event);
      if (failure) return;
      const payload = event.data;
      switch (payload.type) {
        case GATEWAY_SDK_READY_TYPE:
          handleReady(event, payload);
          return;
        case GATEWAY_SDK_TARGET_TYPE:
          handleTarget(event, payload);
          return;
        case GATEWAY_SDK_PAGE_TYPE:
          handlePage(event, payload);
          return;
        default:
          return;
      }
    }
  }

  function revalidateBinding() {
    // The binding lives outside this module (messages/camera/tool state).
    // When it disappears or moves to a different frame/app, old session
    // state must be revoked immediately rather than routing messages to a
    // frame that is no longer the active binding.
    const current = state.session;
    if (!current) return true;
    const binding = getBinding();
    if (!binding) {
      revoke('binding-gone');
      return false;
    }
    if (bindingKey(binding) !== current.bindingKey || binding.frame?.contentWindow !== current.frame?.contentWindow) {
      revoke('binding-changed');
      return false;
    }
    current.binding = binding;
    return true;
  }

  function connect(bindingOverride = null) {
    if (state.disposed) return false;
    const nextBinding = bindingOverride ?? getBinding();
    if (!nextBinding?.frame?.contentWindow?.postMessage) return false;
    // Every explicit connect is a new-document handshake: it always mints a
    // fresh session token (and request_id) and posts a fresh connect. A
    // reloaded iframe must not inherit the previous document's token, and
    // any in-flight state from the old session is dead immediately.
    if (state.session) revoke('rebind');
    const token = createSessionToken();
    const requestId = createSessionToken();
    state.session = {
      token,
      requestId,
      binding: nextBinding,
      bindingKey: bindingKey(nextBinding),
      appId: nextBinding.appId ?? null,
      agentUid: nextBinding.agentUid ?? null,
      mode: 'off',
      frame: nextBinding.frame,
    };
    state.readyNotified = false;
    state.ready = null;
    state.page = null;
    watchSignal(nextBinding);
    return postToFrame(nextBinding, hostConnectMessage());
  }

  function setMode(mode) {
    if (state.disposed) return false;
    if (!SUPPORTED_MODES.includes(mode)) return false;
    const binding = getBinding();
    if (!binding) return false;
    if (!state.session) {
      const connected = connect(binding);
      if (!connected) return false;
      revalidateBinding();
    }
    if (!state.session) return false;
    state.session.mode = mode;
    return setModeInternal();
  }

  const host = {
    connect,
    setMode,
    handleWindowMessage,
    hasSession: () => state.session !== null && !state.disposed,
    readyCapabilities: () => (state.ready ? state.ready.capabilities.slice() : []),
    deactivate() {
      if (state.session) {
        revoke('deactivate');
        return true;
      }
      return false;
    },
    dispose() {
      state.disposed = true;
      revoke('dispose', { silent: true });
    },
  };
  Object.defineProperty(host, 'sessionToken', {
    get: () => (state.session ? state.session.token : null),
    enumerable: true,
  });
  Object.defineProperty(host, 'page', {
    get: () => (state.page ? { ...state.page } : null),
    enumerable: true,
  });
  return host;
}
