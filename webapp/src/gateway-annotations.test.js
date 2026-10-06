import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  GATEWAY_ANNOTATION_BRIDGE_CONTRACT,
  GATEWAY_ANNOTATIONS_CONTRACT,
  GATEWAY_HOST_CONNECT_TYPE,
  GATEWAY_SDK_PAGE_TYPE,
  GATEWAY_SDK_READY_TYPE,
  GATEWAY_SDK_TARGET_TYPE,
  createGatewayAnnotationHost,
  normalizeGatewayAnnotationSelection,
  normalizeGatewayAnnotations,
} from './gateway-annotations';

const BRIDGE = GATEWAY_ANNOTATION_BRIDGE_CONTRACT;
const META = GATEWAY_ANNOTATIONS_CONTRACT;

function makeFrame(origin) {
  const posted = [];
  const hostRef = { current: null };
  const contentWindow = {
    postMessage: vi.fn((message, target) => { posted.push({ message, target }); }),
  };
  return {
    frame: { contentWindow },
    contentWindow,
    origin,
    posted,
    connectMessage: () => posted.find((p) => p.message.type === GATEWAY_HOST_CONNECT_TYPE)?.message ?? null,
    modeMessages: () => posted.filter((p) => p.message.type === GATEWAY_SDK_MODE_TYPE).map((p) => p.message),
    allPostedToExactOrigin: () => posted.every((p) => p.target === origin),
    handleWindowMessage(event) {
      hostRef.current?.handleWindowMessage?.(event);
    },
    hostRef,
  };
}

const GATEWAY_SDK_MODE_TYPE = 'catsco.gateway.annotation.mode.v1';

function bindingFor(h) {
  return { frame: h.frame, url: h.origin, agentUid: 101, appId: 'board' };
}

function setup(options = {}) {
  const callbacks = {
    onReady: vi.fn(),
    onSelection: vi.fn(),
    onPageChange: vi.fn(),
    onUnavailable: vi.fn(),
    ...options.callbacks,
  };
  const h = makeFrame(options.origin ?? 'https://agent-101.example.com');
  let revoked = options.binding === undefined ? bindingFor(h) : options.binding;
  const host = createGatewayAnnotationHost({
    getBinding: () => revoked,
    ...callbacks,
  });
  h.hostRef.current = host;
  return { h, host, callbacks, setBinding: (b) => { revoked = b; } };
}

function connectReady(h, host, callbacks, pageOverrides = {}) {
  expect(host.connect()).toBe(true);
  const connect = h.connectMessage();
  expect(connect).not.toBeNull();
  h.handleWindowMessage({
    data: {
      type: GATEWAY_SDK_READY_TYPE,
      contract_version: BRIDGE,
      session_id: connect.session_id,
      request_id: connect.request_id,
      capabilities: ['element', 'text', 'region'],
      page: pageOverrides.page ?? { path: '/board' },
    },
    origin: h.origin,
    source: h.contentWindow,
  });
  return connect;
}

function validSelection(overrides = {}) {
  return {
    id: 'a1', kind: 'element', label: '发布按钮', body: '改成蓝色',
    target: { element_id: 'btn-submit' },
    ...overrides,
  };
}

function validMetadata(overrides = {}) {
  return {
    contract_version: META,
    agent_uid: 7,
    app_id: 'board',
    page: { path: '/board', revision: 'r7' },
    annotations: [validSelection()],
    ...overrides,
  };
}

afterEach(() => {
  vi.restoreAllMocks();
});

describe('normalizeGatewayAnnotationSelection', () => {
  it('rejects unknown target keys instead of silently dropping them', () => {
    expect(normalizeGatewayAnnotationSelection({
      id: 'a1', kind: 'element', label: '发布按钮', target: { element_id: 'btn', extra: 1 },
    })).toBeNull();
  });

  it('accepts rect + viewport evidence on element targets', () => {
    const rect = { x: 0.1, y: 0.2, width: 0.3, height: 0.4 };
    const viewport = { width: 1024, height: 640, scroll_x: 0, scroll_y: 0 };
    const selection = normalizeGatewayAnnotationSelection({
      id: 'a2', kind: 'element', label: 'x',
      target: { element_id: 'btn', rect, coordinate_space: 'viewport', viewport },
    });
    expect(selection.target.rect).toEqual(rect);
    expect(selection.target.viewport).toEqual(viewport);
  });

  it('rejects missing/extra/prototype-polluted keys', () => {
    expect(normalizeGatewayAnnotationSelection(null)).toBeNull();
    expect(normalizeGatewayAnnotationSelection(['array'])).toBeNull();
    expect(normalizeGatewayAnnotationSelection({})).toBeNull();
    expect(normalizeGatewayAnnotationSelection({
      id: 'a', kind: 'element', label: '', target: { element_id: 'b' }, unexpected: 1,
    })).toBeNull();
    expect(normalizeGatewayAnnotationSelection({
      id: 'a', kind: 'element', label: '', target: { element_id: 'b', href: 'x' },
    })).toBeNull();
    // JSON-borne __proto__ must not survive strict key checks.
    const polluted = JSON.parse('{"id":"a","kind":"element","label":"","target":{"element_id":"b","__proto__":{"x":1}}}');
    expect(normalizeGatewayAnnotationSelection(polluted)).toBeNull();
  });

  it('rejects oversized ids, control-bearing ids, and missing anchors', () => {
    expect(normalizeGatewayAnnotationSelection({
      id: 'a'.repeat(129), kind: 'element', label: '', target: { element_id: 'b' },
    })).toBeNull();
    expect(normalizeGatewayAnnotationSelection({
      id: 'a\nb', kind: 'element', label: '', target: { element_id: 'b' },
    })).toBeNull();
    expect(normalizeGatewayAnnotationSelection({
      id: 'a', kind: 'element', label: '', target: {},
    })).toBeNull();
  });

  it('rejects wrong kinds and bad labels', () => {
    expect(normalizeGatewayAnnotationSelection({
      id: 'a', kind: 'cookie', label: '', target: { element_id: 'b' },
    })).toBeNull();
    expect(normalizeGatewayAnnotationSelection({
      id: 'a', kind: 'element', label: 123, target: { element_id: 'b' },
    })).toBeNull();
    expect(normalizeGatewayAnnotationSelection({
      id: 'a', kind: 'element', label: 'x'.repeat(257), target: { element_id: 'b' },
    })).toBeNull();
  });

  it('text kind requires the text anchor and respects size bounds', () => {
    expect(normalizeGatewayAnnotationSelection({
      id: 't1', kind: 'text', label: '选段', target: { text: '示例文本', prefix: '前', suffix: '后' },
    })).toEqual({ id: 't1', kind: 'text', label: '选段', target: { text: '示例文本', prefix: '前', suffix: '后' } });
    expect(normalizeGatewayAnnotationSelection({ id: 't', kind: 'text', label: '', target: { text: '' } })).toBeNull();
    expect(normalizeGatewayAnnotationSelection({ id: 't', kind: 'text', label: '', target: { text: 'b'.repeat(2001) } })).toBeNull();
    expect(normalizeGatewayAnnotationSelection({ id: 't', kind: 'text', label: '', target: { selector: '#foo' } })).toBeNull();
    expect(normalizeGatewayAnnotationSelection({ id: 't', kind: 'text', label: '', target: { text: 'ok', prefix: 'p'.repeat(257) } })).toBeNull();
  });

  it('region requires rect + coordinate_space + viewport; geometry is 0..1 and non-degenerate', () => {
    const viewport = { width: 800, height: 600, scroll_x: 0, scroll_y: 0 };
    expect(normalizeGatewayAnnotationSelection({
      id: 'r1', kind: 'region', label: '',
      target: { rect: { x: 0.1, y: 0.1, width: 0.2, height: 0.2 }, coordinate_space: 'viewport', viewport },
    })).not.toBeNull();
    const badTargets = [
      { rect: { x: 0.1, y: 0.1, width: 0.2, height: 0.2 } },
      { rect: { x: 0.1, y: 0.1, width: 0.2, height: 0.2 }, coordinate_space: 'viewport' },
      { rect: { x: 0.95, y: 0.1, width: 0.2, height: 0.2 }, coordinate_space: 'viewport', viewport },
      { rect: { x: 0.1, y: 0.1, width: 0, height: 0.2 }, coordinate_space: 'viewport', viewport },
      { rect: { x: 0.1, y: 0.1, width: -0.2, height: 0.2 }, coordinate_space: 'viewport', viewport },
      { rect: { x: 0.1, y: 0.1, width: 0.2, height: 0.2 }, coordinate_space: 'page', viewport },
      { rect: { x: 0.1, y: 0.1, width: 0.2, height: 0.2 }, coordinate_space: 'viewport', viewport: { width: 800, scroll_x: 0, scroll_y: 0 } },
      { coordinate_space: 'viewport', viewport },
    ];
    for (const target of badTargets) {
      expect(normalizeGatewayAnnotationSelection({ id: 'r', kind: 'region', label: '', target })).toBeNull();
    }
  });
});

describe('normalizeGatewayAnnotations', () => {
  it('normalizes a fully valid payload identically', () => {
    expect(normalizeGatewayAnnotations(validMetadata())).toEqual(validMetadata());
  });

  it('rejects wrong contract version, unknown keys, non-objects', () => {
    expect(normalizeGatewayAnnotations(validMetadata({ contract_version: 'v1' }))).toBeNull();
    expect(normalizeGatewayAnnotations(validMetadata({ extra: true }))).toBeNull();
    expect(normalizeGatewayAnnotations(null)).toBeNull();
    expect(normalizeGatewayAnnotations('nope')).toBeNull();
  });

  it('rejects invalid agent_uid / app_id / page fields', () => {
    expect(normalizeGatewayAnnotations(validMetadata({ agent_uid: 0 }))).toBeNull();
    expect(normalizeGatewayAnnotations(validMetadata({ agent_uid: -3 }))).toBeNull();
    expect(normalizeGatewayAnnotations(validMetadata({ agent_uid: 7.5 }))).toBeNull();
    expect(normalizeGatewayAnnotations(validMetadata({ app_id: 'BAD ID' }))).toBeNull();
    expect(normalizeGatewayAnnotations(validMetadata({ app_id: 'a'.repeat(129) }))).toBeNull();
    expect(normalizeGatewayAnnotations(validMetadata({ page: { path: 'no-slash' } }))).toBeNull();
    expect(normalizeGatewayAnnotations(validMetadata({ page: { path: '/x?tok=1' } }))).toBeNull();
    expect(normalizeGatewayAnnotations(validMetadata({ page: { path: '/x#frag' } }))).toBeNull();
    expect(normalizeGatewayAnnotations(validMetadata({ page: { path: '/x', revision: 'r'.repeat(129) } }))).toBeNull();
    expect(normalizeGatewayAnnotations(validMetadata({ page: { path: '/x', revision: '' } }))).toBeNull();
  });

  it('rejects >20 annotations and malformed entries; keeps empty arrays', () => {
    const twentyOne = Array.from({ length: 21 }, (_, i) => ({
      id: `a${i}`, kind: 'element', label: '', target: { element_id: 'b' },
    }));
    expect(normalizeGatewayAnnotations(validMetadata({ annotations: twentyOne }))).toBeNull();
    expect(normalizeGatewayAnnotations(validMetadata({ annotations: [validSelection(), null] }))).toBeNull();
    expect(normalizeGatewayAnnotations(validMetadata({ annotations: [] })))
      .toEqual({ ...validMetadata(), annotations: [] });
  });

  it('rejects entries without body and duplicate ids', () => {
    const noBody = validMetadata({ annotations: [{ ...validSelection(), body: undefined }] });
    expect(normalizeGatewayAnnotations(noBody)).toBeNull();
    const blankBody = validMetadata({ annotations: [{ ...validSelection(), body: '   ' }] });
    expect(normalizeGatewayAnnotations(blankBody)).toBeNull();
    const oversizedBody = validMetadata({ annotations: [{ ...validSelection(), body: 'x'.repeat(2001) }] });
    expect(normalizeGatewayAnnotations(oversizedBody)).toBeNull();
    const duplicate = validMetadata({ annotations: [validSelection(), validSelection()] });
    expect(normalizeGatewayAnnotations(duplicate)).toBeNull();
    // The bridge-level selection normalizer never carries body.
    expect(normalizeGatewayAnnotationSelection(validSelection())).toBeNull();
  });

  it('rejects totals over 16KiB', () => {
    // Three 2000-codepoint CJK texts are ~6000 UTF-8 bytes each → >16KiB.
    const big = validMetadata({
      annotations: Array.from({ length: 3 }, (_, i) => ({
        id: `b${i}`, kind: 'text', label: 'n', target: { text: '好'.repeat(2000) },
      })),
    });
    expect(normalizeGatewayAnnotations(big)).toBeNull();
  });

  it('accepts totals just under 16KiB', () => {
    const big = validMetadata({
      annotations: [
        { id: 'u1', kind: 'text', label: 'n', body: 'x', target: { text: '好'.repeat(2000) } },
        { id: 'u2', kind: 'text', label: 'n', body: 'x', target: { text: '好'.repeat(1500) } },
      ],
    });
    expect(normalizeGatewayAnnotations(big)).not.toBeNull();
  });
});

describe('createGatewayAnnotationHost', () => {
  it('requires getBinding; fails cleanly without one', () => {
    expect(() => createGatewayAnnotationHost({})).toThrow(/getBinding/);
    const { host } = setup({ binding: null });
    expect(host.connect()).toBe(false);
    expect(host.setMode('element')).toBe(false);
    host.dispose();
  });

  it('connect posts a contract-versioned connect to the exact frame origin', () => {
    const { h, host } = setup();
    expect(host.connect()).toBe(true);
    const connect = h.connectMessage();
    expect(connect.type).toBe(GATEWAY_HOST_CONNECT_TYPE);
    expect(connect.contract_version).toBe(BRIDGE);
    expect(typeof connect.session_id).toBe('string');
    expect(connect.session_id.startsWith('catsco_')).toBe(true);
    expect(h.allPostedToExactOrigin()).toBe(true);
  });

  it('unknown setMode is rejected before any post', () => {
    const { h, host } = setup();
    expect(host.setMode('cookie')).toBe(false);
    expect(h.modeMessages()).toEqual([]);
    expect(h.connectMessage()).toBeNull();
  });

  it('setMode before connect lazily connects and posts the mode', () => {
    const { h, host } = setup();
    expect(host.setMode('element')).toBe(true);
    expect(h.connectMessage()).not.toBeNull();
    expect(h.modeMessages().at(-1).mode).toBe('element');
    expect(host.hasSession()).toBe(true);
  });

  it('accepts a valid ready exactly once and ignores duplicates', () => {
    const { h, host, callbacks } = setup();
    connectReady(h, host, callbacks);
    expect(callbacks.onReady).toHaveBeenCalledTimes(1);
    expect(callbacks.onReady).toHaveBeenCalledWith({
      capabilities: ['element', 'text', 'region'],
      page: { path: '/board' },
    });
    connectReady(h, host, callbacks);
    expect(callbacks.onReady).toHaveBeenCalledTimes(1);
  });

  it('rejects ready from wrong frame / origin / contract / stale session', () => {
    const { h, host, callbacks } = setup();
    host.connect();
    const connect = h.connectMessage();
    const good = {
      type: GATEWAY_SDK_READY_TYPE,
      contract_version: BRIDGE,
      session_id: connect.session_id,
      capabilities: ['element'],
      page: { path: '/' },
    };
    h.handleWindowMessage({ data: good, origin: h.origin, source: { postMessage() {} } });
    h.handleWindowMessage({ data: good, origin: 'https://evil.example', source: h.contentWindow });
    h.handleWindowMessage({ data: { ...good, contract_version: 'v9' }, origin: h.origin, source: h.contentWindow });
    h.handleWindowMessage({ data: { ...good, session_id: 'catsco_stale' }, origin: h.origin, source: h.contentWindow });
    h.handleWindowMessage({ data: { ...good, session_id: null }, origin: h.origin, source: h.contentWindow });
    expect(callbacks.onReady).not.toHaveBeenCalled();
  });

  it('empty capabilities are rejected with onUnavailable and revocation', () => {
    const { h, host, callbacks } = setup();
    host.connect();
    const connect = h.connectMessage();
    h.handleWindowMessage({
      data: {
        type: GATEWAY_SDK_READY_TYPE,
        contract_version: BRIDGE,
        session_id: connect.session_id,
        request_id: connect.request_id,
        capabilities: [],
        page: { path: '/' },
      },
      origin: h.origin,
      source: h.contentWindow,
    });
    expect(callbacks.onUnavailable).toHaveBeenCalledTimes(1);
    expect(host.hasSession()).toBe(false);
  });

  it('malformed offered page in ready falls back to default; valid later pages still accepted', () => {
    const { h, host, callbacks } = setup();
    host.connect();
    const connect = h.connectMessage();
    h.handleWindowMessage({
      data: { type: GATEWAY_SDK_READY_TYPE, contract_version: BRIDGE, session_id: connect.session_id, request_id: connect.request_id, capabilities: ['element'], page: { path: 'evil' } },
      origin: h.origin,
      source: h.contentWindow,
    });
    expect(callbacks.onReady).toHaveBeenCalled();
    h.handleWindowMessage({
      data: { type: GATEWAY_SDK_PAGE_TYPE, contract_version: BRIDGE, session_id: connect.session_id, page: { path: '/next', revision: 'r2' } },
      origin: h.origin,
      source: h.contentWindow,
    });
    expect(callbacks.onPageChange).toHaveBeenCalledWith({ path: '/next', revision: 'r2' });
    expect(host.page).toEqual({ path: '/next', revision: 'r2' });
  });

  it('routes valid selections with current page; invalid selections report onUnavailable', () => {
    const { h, host, callbacks } = setup();
    const connect = connectReady(h, host, callbacks);
    expect(host.setMode('element')).toBe(true); // targets need the active mode
    const selection = { id: 'a1', kind: 'element', label: '钮', target: { element_id: 'ok' } };
    h.handleWindowMessage({
      data: {
        type: GATEWAY_SDK_TARGET_TYPE,
        contract_version: BRIDGE,
        session_id: connect.session_id,
        page: { path: '/board' },
        selection,
      },
      origin: h.origin,
      source: h.contentWindow,
    });
    expect(callbacks.onSelection).toHaveBeenCalledTimes(1);
    expect(callbacks.onSelection).toHaveBeenCalledWith(selection, { path: '/board' });

    h.handleWindowMessage({
      data: {
        type: GATEWAY_SDK_TARGET_TYPE,
        contract_version: BRIDGE,
        session_id: connect.session_id,
        page: { path: '/board' },
        selection: { id: 'bad', kind: 'region', label: '', target: { element_id: 'not-region' } },
      },
      origin: h.origin,
      source: h.contentWindow,
    });
    expect(callbacks.onSelection).toHaveBeenCalledTimes(1);
    // kind mismatches the active mode → silently ignored, not reported.
    expect(callbacks.onUnavailable).not.toHaveBeenCalled();
  });

  it('connect to a different frame app revokes the old session and refuses stale notices', () => {
    const { h, host, callbacks } = setup();
    const firstConnect = connectReady(h, host, callbacks);
    expect(callbacks.onReady).toHaveBeenCalledTimes(1);

    const other = makeFrame('https://agent-202.example.com');
    const ok = host.connect({
      frame: other.frame,
      url: other.origin,
      agentUid: 202,
      appId: 'sheets',
    });
    expect(ok).toBe(true);
    expect(callbacks.onUnavailable).toHaveBeenCalledTimes(1);
    // Old frame replays its ready with the OLD session token — must be ignored.
    h.handleWindowMessage({
      data: { type: GATEWAY_SDK_READY_TYPE, contract_version: BRIDGE, session_id: firstConnect.session_id, capabilities: ['element'], page: { path: '/board' } },
      origin: h.origin,
      source: h.contentWindow,
    });
    expect(callbacks.onReady).toHaveBeenCalledTimes(1);
    expect(host.sessionToken).not.toBe(firstConnect.session_id);
  });

  it('deactivate revokes the session; subsequent frame messages are ignored', () => {
    const { h, host, callbacks } = setup();
    const connect = connectReady(h, host, callbacks);
    expect(host.deactivate()).toBe(true);
    expect(host.hasSession()).toBe(false);
    h.handleWindowMessage({
      data: { type: GATEWAY_SDK_TARGET_TYPE, contract_version: BRIDGE, session_id: connect.session_id, page: { path: '/board' }, selection: validSelection() },
      origin: h.origin,
      source: h.contentWindow,
    });
    expect(callbacks.onSelection).not.toHaveBeenCalled();
  });

  it('dispose makes the host permanently inert', () => {
    const { host } = setup();
    host.connect();
    expect(host.hasSession()).toBe(true);
    host.dispose();
    expect(host.hasSession()).toBe(false);
    expect(host.connect()).toBe(false);
    expect(host.setMode('element')).toBe(false);
  });

  it('an aborted binding signal revokes the session', () => {
    const { h, host, callbacks } = setup();
    const controller = new AbortController();
    const binding = { ...bindingFor(h), signal: controller.signal };
    const { host: signalHost, callbacks: signalCallbacks } = setup({ binding });
    signalHost.connect();
    expect(signalHost.hasSession()).toBe(true);
    controller.abort();
    expect(signalHost.hasSession()).toBe(false);
    expect(signalCallbacks.onUnavailable).toHaveBeenCalledWith(expect.any(Object), 'binding-gone');
    // Frame notices after abort are ignored.
    const connect = h.connectMessage();
    h.handleWindowMessage({
      data: { type: GATEWAY_SDK_READY_TYPE, contract_version: BRIDGE, session_id: connect.session_id, capabilities: ['element'], page: { path: '/' } },
      origin: h.origin,
      source: h.contentWindow,
    });
    expect(signalCallbacks.onReady).not.toHaveBeenCalled();
    host.dispose();
  });

  it('valid metadata contract constant is exported for composer parity', () => {
    expect(META).toBe('catsco.gateway-annotations.v1');
  });
});

function acceptAnyBinding() {
  return expect.any(Object);
}

describe('session lifecycle (reload / stale binding / gated targets)', () => {
  function targetMessage(connect, selection, page) {
    return {
      type: GATEWAY_SDK_TARGET_TYPE,
      contract_version: BRIDGE,
      session_id: connect.session_id,
      page,
      selection,
    };
  }

  it('connect on the same binding mints a NEW token and posts a fresh connect (iframe reload)', () => {
    const { h, host, callbacks } = setup();
    host.connect();
    expect(callbacks.onUnavailable).not.toHaveBeenCalled();

    expect(host.connect()).toBe(true);
    const connects = h.posted.filter((p) => p.message.type === GATEWAY_HOST_CONNECT_TYPE);
    expect(connects).toHaveLength(2);
    const first = connects[0].message;
    const second = connects[1].message;
    expect(second.session_id).not.toBe(first.session_id);
    expect(second.request_id).not.toBe(first.request_id);
    expect(host.sessionToken).toBe(second.session_id);
    expect(callbacks.onUnavailable).toHaveBeenCalledTimes(1); // old session revoked

    // A late ready answering the FIRST connect must not activate the session.
    h.handleWindowMessage({
      data: {
        type: GATEWAY_SDK_READY_TYPE,
        contract_version: BRIDGE,
        session_id: first.session_id,
        request_id: first.request_id,
        capabilities: ['element'],
        page: { path: '/' },
      },
      origin: h.origin,
      source: h.contentWindow,
    });
    expect(callbacks.onReady).not.toHaveBeenCalled();
  });

  it('ready must echo the connect request_id', () => {
    const { h, host, callbacks } = setup();
    host.connect();
    const connect = h.connectMessage();
    h.handleWindowMessage({
      data: { type: GATEWAY_SDK_READY_TYPE, contract_version: BRIDGE, session_id: connect.session_id, request_id: 'catsco_other_request', capabilities: ['element'], page: { path: '/' } },
      origin: h.origin,
      source: h.contentWindow,
    });
    expect(callbacks.onReady).not.toHaveBeenCalled();
    h.handleWindowMessage({
      data: { type: GATEWAY_SDK_READY_TYPE, contract_version: BRIDGE, session_id: connect.session_id, request_id: connect.request_id, capabilities: ['element'], page: { path: '/' } },
      origin: h.origin,
      source: h.contentWindow,
    });
    expect(callbacks.onReady).toHaveBeenCalledTimes(1);
  });

  it('messages from a frame that is no longer the current binding are refused', () => {
    const { h, host, callbacks, setBinding } = setup();
    host.connect();
    const connect = h.connectMessage();
    // The consumer's binding moved to a different frame/app.
    const other = makeFrame('https://agent-999.example.com');
    setBinding(bindingFor(other));
    h.handleWindowMessage({
      data: { type: GATEWAY_SDK_READY_TYPE, contract_version: BRIDGE, session_id: connect.session_id, request_id: connect.request_id, capabilities: ['element'], page: { path: '/' } },
      origin: h.origin,
      source: h.contentWindow,
    });
    expect(callbacks.onReady).not.toHaveBeenCalled();
    expect(callbacks.onUnavailable).toHaveBeenCalledWith(expect.anything(), 'binding-changed');
  });

  it('targets are gated by ready state, capability and active mode', () => {
    const { h, host, callbacks } = setup();
    host.connect();
    const connect = h.connectMessage();
    const elementSelection = { id: 'e1', kind: 'element', label: '', target: { element_id: 'x' } };
    // Before ready: ignored entirely.
    h.handleWindowMessage({ data: targetMessage(connect, elementSelection, { path: '/' }), origin: h.origin, source: h.contentWindow });
    expect(callbacks.onSelection).not.toHaveBeenCalled();

    h.handleWindowMessage({
      data: { type: GATEWAY_SDK_READY_TYPE, contract_version: BRIDGE, session_id: connect.session_id, request_id: connect.request_id, capabilities: ['element'], page: { path: '/board' } },
      origin: h.origin,
      source: h.contentWindow,
    });
    // Mode off: ignored.
    h.handleWindowMessage({ data: targetMessage(connect, elementSelection, { path: '/board' }), origin: h.origin, source: h.contentWindow });
    expect(callbacks.onSelection).not.toHaveBeenCalled();

    expect(host.setMode('element')).toBe(true);
    // Capability mismatch: text target while only 'element' was declared.
    expect(host.setMode('text')).toBe(true);
    h.handleWindowMessage({
      data: targetMessage(connect, { id: 't1', kind: 'text', label: '', target: { text: '示例' } }, { path: '/board' }),
      origin: h.origin,
      source: h.contentWindow,
    });
    expect(callbacks.onSelection).not.toHaveBeenCalled();
    expect(callbacks.onUnavailable).toHaveBeenCalledWith(expect.anything(), 'capability-mismatch');

    // Declared kind in the matching mode: accepted.
    expect(host.setMode('element')).toBe(true);
    h.handleWindowMessage({ data: targetMessage(connect, elementSelection, { path: '/board' }), origin: h.origin, source: h.contentWindow });
    expect(callbacks.onSelection).toHaveBeenCalledTimes(1);
    expect(callbacks.onSelection).toHaveBeenCalledWith(elementSelection, { path: '/board' });
  });

  it('target page must match the tracked page snapshot (drift refuses the stale anchor)', () => {
    const { h, host, callbacks } = setup();
    host.connect();
    const connect = h.connectMessage();
    h.handleWindowMessage({
      data: { type: GATEWAY_SDK_READY_TYPE, contract_version: BRIDGE, session_id: connect.session_id, request_id: connect.request_id, capabilities: ['element'], page: { path: '/board', revision: 'r1' } },
      origin: h.origin,
      source: h.contentWindow,
    });
    expect(host.setMode('element')).toBe(true);
    const selection = { id: 'e1', kind: 'element', label: '', target: { element_id: 'x' } };

    // Malformed page payload.
    h.handleWindowMessage({ data: targetMessage(connect, selection, { path: 'nope' }), origin: h.origin, source: h.contentWindow });
    expect(callbacks.onSelection).not.toHaveBeenCalled();
    expect(callbacks.onUnavailable).toHaveBeenCalledWith(expect.anything(), 'bad-page');

    // Page drifted ahead of the host's snapshot (no page.v1 yet).
    h.handleWindowMessage({ data: targetMessage(connect, selection, { path: '/next', revision: 'r1' }), origin: h.origin, source: h.contentWindow });
    expect(callbacks.onSelection).not.toHaveBeenCalled();
    expect(callbacks.onUnavailable).toHaveBeenCalledWith(expect.anything(), 'page-drift');

    // Revision drift is also a mismatch.
    h.handleWindowMessage({ data: targetMessage(connect, selection, { path: '/board', revision: 'r2' }), origin: h.origin, source: h.contentWindow });
    expect(callbacks.onSelection).not.toHaveBeenCalled();
    expect(callbacks.onUnavailable).toHaveBeenCalledWith(expect.anything(), 'page-drift');

    // After the frame announces the new page, matching targets are accepted.
    h.handleWindowMessage({ data: { type: GATEWAY_SDK_PAGE_TYPE, contract_version: BRIDGE, session_id: connect.session_id, page: { path: '/board', revision: 'r2' } }, origin: h.origin, source: h.contentWindow });
    expect(callbacks.onPageChange).toHaveBeenCalledWith({ path: '/board', revision: 'r2' });
    h.handleWindowMessage({ data: targetMessage(connect, selection, { path: '/board', revision: 'r2' }), origin: h.origin, source: h.contentWindow });
    expect(callbacks.onSelection).toHaveBeenCalledTimes(1);
  });


  it('duplicate ready for the completed handshake must not roll back page or capability (review round 2 probe)', () => {
    const { h, host, callbacks } = setup();
    host.connect();
    const connect = h.connectMessage();
    const ready = {
      type: GATEWAY_SDK_READY_TYPE,
      contract_version: BRIDGE,
      session_id: connect.session_id,
      request_id: connect.request_id,
      capabilities: ['element', 'text'],
      page: { path: '/old', revision: 'r1' },
    };
    h.handleWindowMessage({ data: ready, origin: h.origin, source: h.contentWindow });
    h.handleWindowMessage({ data: { type: GATEWAY_SDK_PAGE_TYPE, contract_version: BRIDGE, session_id: connect.session_id, page: { path: '/new', revision: 'r2' } }, origin: h.origin, source: h.contentWindow });
    expect(host.page).toEqual({ path: '/new', revision: 'r2' });
    expect(callbacks.onPageChange).toHaveBeenCalledWith({ path: '/new', revision: 'r2' });

    // Late duplicate ready replaying the OLD page must be a no-op: same
    // session+request, already completed handshake.
    h.handleWindowMessage({ data: ready, origin: h.origin, source: h.contentWindow });
    expect(host.page).toEqual({ path: '/new', revision: 'r2' });
    expect(host.readyCapabilities()).toEqual(['element', 'text']); // not silently mutated
    expect(callbacks.onReady).toHaveBeenCalledTimes(1);
    expect(callbacks.onPageChange).toHaveBeenCalledTimes(1);

    // With host.page and the consumer state aligned on /new/r2, a target for
    // the current page is still accepted and delivered with that exact page.
    expect(host.setMode('element')).toBe(true);
    h.handleWindowMessage({
      data: targetMessage(connect, { id: 'e1', kind: 'element', label: '', target: { element_id: 'x' } }, { path: '/new', revision: 'r2' }),
      origin: h.origin,
      source: h.contentWindow,
    });
    expect(callbacks.onSelection).toHaveBeenCalledTimes(1);
    expect(callbacks.onSelection).toHaveBeenCalledWith(expect.anything(), { path: '/new', revision: 'r2' });
    expect(host.page).toEqual({ path: '/new', revision: 'r2' });
  });

  it('a page report is a full snapshot: revision can be dropped, not merged', () => {
    const { h, host, callbacks } = setup();
    host.connect();
    const connect = h.connectMessage();
    h.handleWindowMessage({
      data: { type: GATEWAY_SDK_READY_TYPE, contract_version: BRIDGE, session_id: connect.session_id, request_id: connect.request_id, capabilities: ['element'], page: { path: '/board', revision: 'r1' } },
      origin: h.origin,
      source: h.contentWindow,
    });
    expect(host.page).toEqual({ path: '/board', revision: 'r1' });

    // The frame drops its revision (e.g. SDK setRevision(null)): the new
    // snapshot must replace the old one entirely.
    h.handleWindowMessage({ data: { type: GATEWAY_SDK_PAGE_TYPE, contract_version: BRIDGE, session_id: connect.session_id, page: { path: '/board' } }, origin: h.origin, source: h.contentWindow });
    expect(callbacks.onPageChange).toHaveBeenCalledWith({ path: '/board' });
    expect(host.page).toEqual({ path: '/board' });

    expect(host.setMode('element')).toBe(true);
    h.handleWindowMessage({
      data: targetMessage(connect, { id: 'e1', kind: 'element', label: '', target: { element_id: 'x' } }, { path: '/board' }),
      origin: h.origin,
      source: h.contentWindow,
    });
    expect(callbacks.onSelection).toHaveBeenCalledTimes(1); // revision-less target accepted
  });
});
