import { describe, it, expect, vi } from 'vitest';
import {
  createGatewayWebMcpHost,
  GATEWAY_WEBMCP_BRIDGE_CONTRACT,
  GATEWAY_WEBMCP_CONNECT_TYPE,
  GATEWAY_WEBMCP_READY_TYPE,
  GATEWAY_WEBMCP_TOOLS_SYNC_TYPE,
  GATEWAY_WEBMCP_TOOL_CALL_TYPE,
  GATEWAY_WEBMCP_TOOL_RESULT_TYPE,
} from './gateway-webmcp.js';

function createFixture() {
  const wsSent = [];
  const iframePosted = [];
  const listeners = new Set();
  const contentWindow = {
    postMessage(msg, targetOrigin) {
      iframePosted.push({ msg, targetOrigin });
    },
  };
  const windowRef = {
    addEventListener(type, fn) { if (type === 'message') listeners.add(fn); },
    removeEventListener(type, fn) { if (type === 'message') listeners.delete(fn); },
  };
  const host = createGatewayWebMcpHost({
    sendWS: (payload) => wsSent.push(payload),
    windowRef,
    connectRetryMs: 10_000,
    maxConnectAttempts: 1,
  });
  const openBinding = {
    open_ref: 'aob_secret_open_ref_123',
    app_id: 'my-canvas',
    topic_id: 'p2p_101_365',
    app_origin: 'https://artifact.catsco.cc',
  };
  const emitFromIframe = (data, origin = 'https://artifact.catsco.cc', source = contentWindow) => {
    for (const fn of [...listeners]) fn({ origin, source, data });
  };
  return { host, wsSent, iframePosted, contentWindow, openBinding, emitFromIframe };
}

describe('createGatewayWebMcpHost', () => {
  it('handshakes without leaking open_ref, forwards tools_sync, and bridges tool_call <-> tool_result', () => {
    const f = createFixture();
    const ok = f.host.activate({ frame: { contentWindow: f.contentWindow }, openBinding: f.openBinding });
    expect(ok).toBe(true);
    expect(f.iframePosted).toHaveLength(1);
    expect(f.iframePosted[0].targetOrigin).toBe('https://artifact.catsco.cc');
    expect(f.iframePosted[0].msg.type).toBe(GATEWAY_WEBMCP_CONNECT_TYPE);
    expect(JSON.stringify(f.iframePosted[0].msg)).not.toContain('aob_secret_open_ref_123');

    const { session_id, request_id } = f.iframePosted[0].msg;

    // Wrong origin must be ignored
    f.emitFromIframe({
      contract_version: GATEWAY_WEBMCP_BRIDGE_CONTRACT,
      type: GATEWAY_WEBMCP_READY_TYPE,
      session_id,
      request_id,
      tools: [{ name: 'evil' }],
    }, 'https://evil.example');
    expect(f.wsSent).toHaveLength(0);

    // Valid ready -> sends tools_sync with open_ref to CatsCo Hub
    f.emitFromIframe({
      contract_version: GATEWAY_WEBMCP_BRIDGE_CONTRACT,
      type: GATEWAY_WEBMCP_READY_TYPE,
      session_id,
      request_id,
      tools: [{ name: 'relayout', description: 'Arrange nodes' }],
    });
    expect(f.host.ready).toBe(true);
    expect(f.wsSent).toHaveLength(1);
    expect(f.wsSent[0]).toEqual({
      type: 'tools_sync',
      open_ref: 'aob_secret_open_ref_123',
      tools: [{ name: 'relayout', description: 'Arrange nodes' }],
    });

    // Dynamic tools_sync
    f.emitFromIframe({
      contract_version: GATEWAY_WEBMCP_BRIDGE_CONTRACT,
      type: GATEWAY_WEBMCP_TOOLS_SYNC_TYPE,
      session_id,
      tools: [{ name: 'relayout' }, { name: 'highlight' }],
    });
    expect(f.wsSent).toHaveLength(2);
    expect(f.wsSent[1].tools).toHaveLength(2);

    // Server tool_call -> posted to iframe
    f.host.handleServerMessage({
      type: 'tool_call',
      bridge_id: 'awm_bridge_1',
      app_id: 'my-canvas',
      topic_id: 'p2p_101_365',
      call_id: 'call-1',
      name: 'relayout',
      arguments: { mode: 'grid' },
    });
    const postedCall = f.iframePosted.at(-1);
    expect(postedCall.targetOrigin).toBe('https://artifact.catsco.cc');
    expect(postedCall.msg).toEqual({
      contract_version: GATEWAY_WEBMCP_BRIDGE_CONTRACT,
      type: GATEWAY_WEBMCP_TOOL_CALL_TYPE,
      session_id,
      call_id: 'call-1',
      name: 'relayout',
      arguments: { mode: 'grid' },
    });

    // Iframe tool_result -> forwarded to CatsCo Hub with bridge_id + open_ref
    f.emitFromIframe({
      contract_version: GATEWAY_WEBMCP_BRIDGE_CONTRACT,
      type: GATEWAY_WEBMCP_TOOL_RESULT_TYPE,
      session_id,
      call_id: 'call-1',
      is_error: false,
      content: [{ type: 'text', text: '{"ok":true}' }],
      structured_content: { ok: true },
    });
    expect(f.wsSent.at(-1)).toEqual({
      type: 'tool_result',
      open_ref: 'aob_secret_open_ref_123',
      bridge_id: 'awm_bridge_1',
      call_id: 'call-1',
      is_error: false,
      content: [{ type: 'text', text: '{"ok":true}' }],
      structured_content: { ok: true },
    });

    // Deactivate sends detach
    f.host.deactivate('panel_closed');
    expect(f.wsSent.at(-1)).toEqual({
      type: 'detach',
      open_ref: 'aob_secret_open_ref_123',
      reason: 'panel_closed',
    });
    f.host.dispose();
  });

  it('replies page_not_open when tool_call arrives without a ready matching session', () => {
    const f = createFixture();
    f.host.handleServerMessage({
      type: 'tool_call',
      bridge_id: 'awm_bridge_x',
      app_id: 'my-canvas',
      topic_id: 'p2p_101_365',
      call_id: 'call-404',
      name: 'relayout',
    });
    expect(f.wsSent).toHaveLength(1);
    expect(f.wsSent[0].type).toBe('tool_result');
    expect(f.wsSent[0].is_error).toBe(true);
    expect(f.wsSent[0].error.code).toBe('page_not_open');
    f.host.dispose();
  });

  it('aborts and detaches when openBinding signal aborts', () => {
    const f = createFixture();
    const ac = new AbortController();
    f.host.activate({ frame: { contentWindow: f.contentWindow }, openBinding: f.openBinding, signal: ac.signal });
    const { session_id, request_id } = f.iframePosted[0].msg;
    f.emitFromIframe({
      contract_version: GATEWAY_WEBMCP_BRIDGE_CONTRACT,
      type: GATEWAY_WEBMCP_READY_TYPE,
      session_id,
      request_id,
      tools: [],
    });
    ac.abort();
    expect(f.host.active).toBe(false);
    expect(f.wsSent.at(-1)).toEqual({
      type: 'detach',
      open_ref: 'aob_secret_open_ref_123',
      reason: 'open_binding_aborted',
    });
    f.host.dispose();
  });
});
