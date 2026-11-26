/**
 * CatsCo Host-side WebMCP bridge for Gateway-served Artifact iframes.
 *
 * Mirrors webapp/src/gateway-annotations.js:
 * - Handshakes with the iframe's injected `/_catsco/runtime/webmcp-v1.js` over
 *   exact-origin postMessage (`catsco.gateway-webmcp-bridge.v1`).
 * - Never sends `open_ref`, JWTs, or platform credentials into the iframe.
 * - Forwards `tools_sync`, `tool_result`, and `detach` to CatsCo Hub via WS
 *   using the parent-only `open_ref`.
 * - Receives `tool_call` from CatsCo Hub and dispatches it to the matching
 *   active iframe, replying `page_not_open` if the page is closed or stale.
 */

export const GATEWAY_WEBMCP_BRIDGE_CONTRACT = 'catsco.gateway-webmcp-bridge.v1';
export const GATEWAY_WEBMCP_CONNECT_TYPE = 'catsco.gateway.webmcp.connect.v1';
export const GATEWAY_WEBMCP_READY_TYPE = 'catsco.gateway.webmcp.ready.v1';
export const GATEWAY_WEBMCP_TOOLS_SYNC_TYPE = 'catsco.gateway.webmcp.tools_sync.v1';
export const GATEWAY_WEBMCP_TOOL_CALL_TYPE = 'catsco.gateway.webmcp.tool_call.v1';
export const GATEWAY_WEBMCP_TOOL_RESULT_TYPE = 'catsco.gateway.webmcp.tool_result.v1';

// Aligned below CatsCo Hub wspump.go maxMessageSize (64 KiB) leaving 16 KiB headroom
// for JSON envelope fields so oversized payloads never close the chat WebSocket.
export const MAX_WEBMCP_PAYLOAD_BYTES = 48 * 1024;

const utf8Encoder = typeof TextEncoder === 'function' ? new TextEncoder() : null;

function utf8ByteLength(str) {
  if (typeof str !== 'string') return 0;
  if (utf8Encoder) return utf8Encoder.encode(str).length;
  let bytes = 0;
  for (let i = 0; i < str.length; i += 1) {
    const code = str.charCodeAt(i);
    if (code <= 0x7f) bytes += 1;
    else if (code <= 0x7ff) bytes += 2;
    else if (code >= 0xd800 && code <= 0xdbff && i + 1 < str.length) {
      const next = str.charCodeAt(i + 1);
      if (next >= 0xdc00 && next <= 0xdfff) {
        bytes += 4;
        i += 1;
      } else {
        bytes += 3;
      }
    } else {
      bytes += 3;
    }
  }
  return bytes;
}

function fitToolsWithinByteCap(tools, maxBytes = MAX_WEBMCP_PAYLOAD_BYTES) {
  if (!Array.isArray(tools) || tools.length === 0) return [];
  try {
    if (utf8ByteLength(JSON.stringify(tools)) <= maxBytes) return tools;
  } catch {
    return [];
  }
  const fitted = [];
  for (const tool of tools) {
    const candidate = [...fitted, tool];
    try {
      if (utf8ByteLength(JSON.stringify(candidate)) > maxBytes) break;
      fitted.push(tool);
    } catch {
      break;
    }
  }
  return fitted;
}

function isPlainObject(value) {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value);
}

function mintToken(prefix) {
  const bytes = new Uint8Array(12);
  if (typeof crypto !== 'undefined' && typeof crypto.getRandomValues === 'function') {
    crypto.getRandomValues(bytes);
  } else {
    for (let i = 0; i < bytes.length; i += 1) bytes[i] = Math.floor(Math.random() * 256);
  }
  const hex = Array.from(bytes, (b) => b.toString(16).padStart(2, '0')).join('');
  return `${prefix}_${hex}`;
}

function exactOrigin(value) {
  if (typeof value !== 'string' || !value) return '';
  try {
    const parsed = new URL(value);
    return (parsed.protocol === 'https:' || parsed.protocol === 'http:') && parsed.origin === value
      ? value
      : '';
  } catch {
    return '';
  }
}

export function createGatewayWebMcpHost({
  sendWS,
  windowRef = typeof window !== 'undefined' ? window : null,
  onToolsChange,
  connectRetryMs = 500,
  maxConnectAttempts = 8,
} = {}) {
  const state = {
    disposed: false,
    session: null,
    pendingBridgeByCallId: new Map(),
    retryTimer: null,
    signalCleanup: null,
  };

  function clearRetry() {
    if (state.retryTimer) {
      clearTimeout(state.retryTimer);
      state.retryTimer = null;
    }
  }

  function clearSignal() {
    if (state.signalCleanup) {
      state.signalCleanup();
      state.signalCleanup = null;
    }
  }

  function safeSendWS(payload) {
    if (typeof sendWS !== 'function') return;
    try {
      sendWS(payload);
    } catch {
      // Ignore transient WS send errors.
    }
  }

  function postToFrame(frame, targetOrigin, payload) {
    try {
      frame?.contentWindow?.postMessage(payload, targetOrigin);
      return true;
    } catch {
      return false;
    }
  }

  function failPendingCalls(reasonMessage) {
    const entries = [...state.pendingBridgeByCallId.entries()];
    state.pendingBridgeByCallId.clear();
    const openRef = state.session?.openRef || '';
    for (const [callId, bridgeId] of entries) {
      safeSendWS({
        type: 'tool_result',
        open_ref: openRef || undefined,
        bridge_id: bridgeId,
        call_id: callId,
        is_error: true,
        content: [{ type: 'text', text: reasonMessage }],
        error: { code: 'page_not_open', message: reasonMessage },
      });
    }
  }

  function deactivate(reason = 'deactivated') {
    clearRetry();
    clearSignal();
    const prev = state.session;
    failPendingCalls('Artifact page closed');
    state.session = null;
    if (prev && prev.synced) {
      safeSendWS({
        type: 'detach',
        open_ref: prev.openRef,
        reason,
      });
    }
    if (prev && typeof onToolsChange === 'function') {
      try { onToolsChange([]); } catch {}
    }
  }

  function sendConnectAttempt() {
    const current = state.session;
    if (!current || state.disposed || current.ready) return;
    postToFrame(current.frame, current.appOrigin, {
      contract_version: GATEWAY_WEBMCP_BRIDGE_CONTRACT,
      type: GATEWAY_WEBMCP_CONNECT_TYPE,
      session_id: current.token,
      request_id: current.requestId,
    });
    current.attempts += 1;
    if (current.attempts < maxConnectAttempts) {
      state.retryTimer = setTimeout(sendConnectAttempt, connectRetryMs);
    }
  }

  function activate({ frame, openBinding, signal } = {}) {
    deactivate('rebind');
    if (state.disposed) return false;
    const openRef = typeof openBinding?.open_ref === 'string' ? openBinding.open_ref.trim() : '';
    const appId = typeof openBinding?.app_id === 'string' ? openBinding.app_id.trim() : '';
    const topicId = typeof openBinding?.topic_id === 'string' ? openBinding.topic_id.trim() : '';
    const appOrigin = exactOrigin(openBinding?.app_origin);
    if (!frame?.contentWindow || !openRef || !appId || !topicId || !appOrigin) {
      return false;
    }
    if (signal?.aborted) return false;

    state.session = {
      frame,
      openRef,
      appId,
      topicId,
      appOrigin,
      token: mintToken('wm_sess'),
      requestId: mintToken('wm_req'),
      ready: false,
      synced: false,
      tools: [],
      attempts: 0,
    };

    if (signal && typeof signal.addEventListener === 'function') {
      const onAbort = () => deactivate('open_binding_aborted');
      signal.addEventListener('abort', onAbort, { once: true });
      state.signalCleanup = () => signal.removeEventListener('abort', onAbort);
    }

    sendConnectAttempt();
    return true;
  }

  function handleWindowMessage(event) {
    if (state.disposed) return;
    const current = state.session;
    if (!current) return;
    if (event.source !== current.frame?.contentWindow) return;
    if (event.origin !== current.appOrigin) return;
    const data = event.data;
    if (!isPlainObject(data) || data.contract_version !== GATEWAY_WEBMCP_BRIDGE_CONTRACT) return;
    if (data.session_id !== current.token) return;

    if (data.type === GATEWAY_WEBMCP_READY_TYPE) {
      if (data.request_id !== current.requestId || current.ready) return;
      current.ready = true;
      clearRetry();
      const tools = Array.isArray(data.tools) ? data.tools : [];
      current.tools = tools;
      current.synced = true;
      safeSendWS({
        type: 'tools_sync',
        open_ref: current.openRef,
        tools,
      });
      if (typeof onToolsChange === 'function') {
        try { onToolsChange(tools); } catch {}
      }
      return;
    }

    if (!current.ready) return;

    if (data.type === GATEWAY_WEBMCP_TOOLS_SYNC_TYPE) {
      const tools = fitToolsWithinByteCap(Array.isArray(data.tools) ? data.tools : []);
      current.tools = tools;
      current.synced = true;
      safeSendWS({
        type: 'tools_sync',
        open_ref: current.openRef,
        tools,
      });
      if (typeof onToolsChange === 'function') {
        try { onToolsChange(tools); } catch {}
      }
      return;
    }

    if (data.type === GATEWAY_WEBMCP_TOOL_RESULT_TYPE) {
      const callId = typeof data.call_id === 'string' ? data.call_id.trim() : '';
      if (!callId) return;
      const bridgeId = state.pendingBridgeByCallId.get(callId);
      if (!bridgeId) return;
      state.pendingBridgeByCallId.delete(callId);
      let reply = {
        type: 'tool_result',
        open_ref: current.openRef,
        bridge_id: bridgeId,
        call_id: callId,
        is_error: data.is_error === true,
        content: Array.isArray(data.content) ? data.content : [{ type: 'text', text: '' }],
      };
      if (isPlainObject(data.structured_content)) reply.structured_content = data.structured_content;
      if (isPlainObject(data.error)) reply.error = data.error;
      let serializedBytes = Infinity;
      try {
        serializedBytes = utf8ByteLength(JSON.stringify(reply));
      } catch {
        serializedBytes = Infinity;
      }
      if (serializedBytes > MAX_WEBMCP_PAYLOAD_BYTES) {
        reply = {
          type: 'tool_result',
          open_ref: current.openRef,
          bridge_id: bridgeId,
          call_id: callId,
          is_error: true,
          content: [{ type: 'text', text: `Tool result exceeds maximum payload size (${MAX_WEBMCP_PAYLOAD_BYTES} bytes)` }],
          error: {
            code: 'result_too_large',
            message: `Tool result exceeds maximum payload size (${MAX_WEBMCP_PAYLOAD_BYTES} bytes)`,
          },
        };
      }
      safeSendWS(reply);
    }
  }

  function handleServerMessage(msg) {
    if (state.disposed || !isPlainObject(msg)) return;
    if (msg.type !== 'tool_call') return;
    const bridgeId = typeof msg.bridge_id === 'string' ? msg.bridge_id.trim() : '';
    const callId = typeof msg.call_id === 'string' ? msg.call_id.trim() : '';
    const name = typeof msg.name === 'string' ? msg.name.trim() : '';
    if (!bridgeId || !callId || !name) return;

    const current = state.session;
    if (!current || !current.ready
      || (msg.app_id && msg.app_id !== current.appId)
      || (msg.topic_id && msg.topic_id !== current.topicId)) {
      safeSendWS({
        type: 'tool_result',
        bridge_id: bridgeId,
        call_id: callId,
        is_error: true,
        content: [{ type: 'text', text: 'Artifact page is not ready or no longer open' }],
        error: { code: 'page_not_open', message: 'Artifact page is not ready or no longer open' },
      });
      return;
    }

    state.pendingBridgeByCallId.set(callId, bridgeId);
    const posted = postToFrame(current.frame, current.appOrigin, {
      contract_version: GATEWAY_WEBMCP_BRIDGE_CONTRACT,
      type: GATEWAY_WEBMCP_TOOL_CALL_TYPE,
      session_id: current.token,
      call_id: callId,
      name,
      arguments: isPlainObject(msg.arguments) ? msg.arguments : {},
    });
    if (!posted) {
      state.pendingBridgeByCallId.delete(callId);
      safeSendWS({
        type: 'tool_result',
        open_ref: current.openRef,
        bridge_id: bridgeId,
        call_id: callId,
        is_error: true,
        content: [{ type: 'text', text: 'Failed to reach Artifact iframe' }],
        error: { code: 'page_not_open', message: 'Failed to reach Artifact iframe' },
      });
    }
  }

  if (windowRef && typeof windowRef.addEventListener === 'function') {
    windowRef.addEventListener('message', handleWindowMessage);
  }

  function dispose() {
    if (state.disposed) return;
    deactivate('disposed');
    state.disposed = true;
    if (windowRef && typeof windowRef.removeEventListener === 'function') {
      windowRef.removeEventListener('message', handleWindowMessage);
    }
  }

  function handleWSOpen() {
    if (state.disposed) return;
    const current = state.session;
    if (!current || !current.ready) return;
    const tools = fitToolsWithinByteCap(Array.isArray(current.tools) ? current.tools : []);
    if (tools.length === 0 && !current.synced) return;
    current.synced = true;
    safeSendWS({
      type: 'tools_sync',
      open_ref: current.openRef,
      tools,
    });
  }

  return {
    activate,
    deactivate,
    handleWSOpen,
    handleServerMessage,
    handleWindowMessage,
    dispose,
    get active() { return Boolean(state.session); },
    get ready() { return Boolean(state.session?.ready); },
    get tools() { return state.session ? state.session.tools : []; },
  };
}
