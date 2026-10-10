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
  normalizeGatewayScreenshotResult,
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

describe('single select mode host validation', () => {
  const evidence = {
    rect: { x: 0.1, y: 0.2, width: 0.3, height: 0.4 }, coordinate_space: 'viewport',
    viewport: { width: 1000, height: 1000, scroll_x: 0, scroll_y: 0 },
  };
  function readySelect(capabilities = ['element', 'text', 'region']) {
    const onModeChange = vi.fn();
    const bus = setup({ callbacks: { onModeChange } });
    bus.host.connect();
    const connect = bus.h.connectMessage();
    const send = (payload, overrides = {}) => bus.h.handleWindowMessage({
      data: { contract_version: BRIDGE, session_id: connect.session_id, ...payload },
      origin: bus.h.origin, source: bus.h.contentWindow, ...overrides,
    });
    send({ type: GATEWAY_SDK_READY_TYPE, request_id: connect.request_id, capabilities, page: { path: '/board', revision: 'r1' } });
    expect(bus.host.setMode('select')).toBe(true);
    const target = (kind, extra = {}) => send({ type: GATEWAY_SDK_TARGET_TYPE, page: { path: '/board', revision: 'r1' },
      selection: { id: `a-${kind}`, kind, label: '', target: { ...evidence, ...(kind === 'element' ? { element_id: 'button' } : kind === 'text' ? { text: 'selected' } : {}) } }, ...extra });
    return { ...bus, send, target, onModeChange, connect };
  }

  it('select accepts all declared machine kinds with bbox while explicit mode keeps kind gating', () => {
    const { host, callbacks, target } = readySelect();
    for (const kind of ['element', 'text', 'region']) target(kind);
    expect(callbacks.onSelection.mock.calls.map(([selection]) => selection.kind)).toEqual(['element', 'text', 'region']);
    host.setMode('element'); target('region');
    expect(callbacks.onSelection).toHaveBeenCalledTimes(3);
    host.dispose();
  });

  it('select requires bbox evidence and declared capability without relaxing historical normalizers', () => {
    const { host, callbacks, target } = readySelect(['element']);
    target('text');
    expect(callbacks.onUnavailable.mock.calls.at(-1)[1]).toBe('capability-mismatch');
    target('element', { selection: { id: 'no-box', kind: 'element', target: { element_id: 'button' } } });
    expect(callbacks.onUnavailable.mock.calls.at(-1)[1]).toBe('bad-selection');
    target('element', { selection: { id: 'bad-box', kind: 'element', target: { ...evidence, element_id: 'button', rect: { x: 0.9, y: 0, width: 0.4, height: 0.1 } } } });
    expect(callbacks.onSelection).not.toHaveBeenCalled();
    host.setMode('element');
    target('element', { selection: { id: 'legacy-no-box', kind: 'element', target: { element_id: 'button' } } });
    expect(callbacks.onSelection).toHaveBeenCalledTimes(1);
    host.dispose();
  });

  it('select still rejects stale session/page/frame/origin and open capability swap', () => {
    const { host, callbacks, target, send, h, setBinding, connect } = readySelect();
    target('region', { session_id: 'old-session' });
    target('region', { page: { path: '/other', revision: 'r1' } });
    expect(callbacks.onUnavailable.mock.calls.at(-1)[1]).toBe('page-drift');
    const data = { type: GATEWAY_SDK_TARGET_TYPE, page: { path: '/board', revision: 'r1' }, selection: { id: 'a', kind: 'region', target: evidence } };
    send(data, { origin: 'https://evil.example' }); send(data, { source: {} });
    expect(callbacks.onSelection).not.toHaveBeenCalled();
    setBinding({ ...bindingFor(h), openBinding: { open_ref: 'another-open' } });
    target('region', { session_id: connect.session_id });
    expect(host.hasSession()).toBe(false);
    expect(callbacks.onSelection).not.toHaveBeenCalled();
    host.dispose();
  });

  it('SDK Escape exit is restricted to ready select session/page and notifies parent once', () => {
    const { host, send, target, onModeChange, callbacks } = readySelect();
    send({ type: GATEWAY_SDK_MODE_TYPE, mode: 'off', page: { path: '/other', revision: 'r1' } });
    send({ type: GATEWAY_SDK_MODE_TYPE, mode: 'off', session_id: 'old', page: { path: '/board', revision: 'r1' } });
    send({ type: GATEWAY_SDK_MODE_TYPE, mode: 'element', page: { path: '/board', revision: 'r1' } });
    expect(onModeChange).not.toHaveBeenCalled();
    send({ type: GATEWAY_SDK_MODE_TYPE, mode: 'off', page: { path: '/board', revision: 'r1' } });
    send({ type: GATEWAY_SDK_MODE_TYPE, mode: 'off', page: { path: '/board', revision: 'r1' } });
    expect(onModeChange).toHaveBeenCalledExactlyOnceWith('off');
    target('region'); expect(callbacks.onSelection).not.toHaveBeenCalled();
    host.dispose();
  });
});

describe('gateway screenshot result guards', () => {
  function jpeg(width, height) {
    // Minimal baseline JPEG frame: SOI + SOF0(geometry) + EOI, enough for
    // the host's strict marker/geometry validation.
    const bytes = [255, 216, 255, 192, 0, 17, 8, (height >> 8) & 255, height & 255,
      (width >> 8) & 255, width & 255, 3, 1, 0x11, 0, 2, 0x11, 1, 3, 0x11, 1, 255, 217];
    return btoa(String.fromCharCode(...bytes));
  }
  const image = (role, width, height, extra = {}) => ({
    role, mime_type: 'image/jpeg', width, height, data_url: `data:image/jpeg;base64,${jpeg(width, height)}`, ...extra,
  });
  const valid = (extra = {}) => ({ screenshots: [image('full', 400, 300), image('crop', 120, 90)], warnings: [], ...extra });

  it('accepts one full/crop pair and re-deduplicates warnings', () => {
    const result = normalizeGatewayScreenshotResult(valid({ warnings: ['video', 'video'] }));
    expect(result.screenshots.map((image) => image.role)).toEqual(['full', 'crop']);
    expect(result.warnings).toEqual(['video']);
    expect(normalizeGatewayScreenshotResult(valid({ warnings: ['sensitive-content-masked'] })).warnings).toEqual(['sensitive-content-masked']);
    expect(normalizeGatewayScreenshotResult(valid({ warnings: ['made-up'] }))).toBeNull();
    expect(normalizeGatewayScreenshotResult(valid({ warnings: Array(17).fill('video') }))).toBeNull();
  });

  it('rejects wrong count/order/role/mime/base64/size/geometry', () => {
    const cases = [
      valid({ screenshots: [image('full', 400, 300)] }),
      valid({ screenshots: [image('crop', 400, 300), image('full', 400, 300)] }),
      valid({ screenshots: [image('full', 400, 300), image('full', 400, 300)] }),
      valid({ screenshots: [image('full', 400, 300), image('crop', 500, 400)] }),
      valid({ screenshots: [image('full', 400, 300), image('crop', 500, 400)] }),
      valid({ screenshots: [image('full', 2049, 300), image('crop', 100, 90)] }),
      valid({ screenshots: [image('full', 400, 300), image('crop', 120, 90, { data_url: 'data:image/jpeg;base64,***' })] }),
      valid({ screenshots: [image('full', 400, 300), image('crop', 120, 90, { mime_type: 'image/png' })] }),
      valid({ screenshots: [image('full', 400.5, 300), image('crop', 120, 90)] }),
    ];
    for (const value of cases) expect(normalizeGatewayScreenshotResult(value)).toBeNull();
  });

  it('rejects a JPEG whose header dimensions contradict the declared geometry', () => {
    const forged = image('full', 400, 300);
    forged.data_url = `data:image/jpeg;base64,${jpeg(640, 480)}`;
    expect(normalizeGatewayScreenshotResult(valid({ screenshots: [forged, image('crop', 120, 90)] }))).toBeNull();
  });

  it('rejects a non-JPEG payload wearing the jpeg mime and data-url prefix', () => {
    const forged = image('full', 400, 300);
    const bytes = new TextEncoder().encode('not-a-jpeg-at-all-nope');
    forged.data_url = `data:image/jpeg;base64,${btoa(String.fromCharCode(...bytes))}`;
    expect(normalizeGatewayScreenshotResult(valid({ screenshots: [forged, image('crop', 120, 90)] }))).toBeNull();
  });
});

describe('host screenshot request guards', () => {
  function jpeg(width, height) {
    return btoa(String.fromCharCode(255, 216, 255, 192, 0, 17, 8, (height >> 8) & 255, height & 255,
      (width >> 8) & 255, width & 255, 3, 1, 0x11, 0, 2, 0x11, 1, 3, 0x11, 1, 255, 217));
  }
  const image = (role, width, height) => ({ role, mime_type: 'image/jpeg', width, height,
    data_url: `data:image/jpeg;base64,${jpeg(width, height)}` });

  function readyWithSelection() {
    const viewport = { width: 1000, height: 500, scroll_x: 0, scroll_y: 0 };
    const onPageChange = vi.fn();
    const bus = setup({ callbacks: { onPageChange } });
    bus.host.connect();
    const connect = bus.h.connectMessage();
    const send = (payload) => bus.h.handleWindowMessage({ data: { contract_version: BRIDGE, session_id: connect.session_id, ...payload },
      origin: bus.h.origin, source: bus.h.contentWindow });
    send({ type: GATEWAY_SDK_READY_TYPE, request_id: connect.request_id, screenshot_supported: true,
      capabilities: ['element', 'text', 'region'], page: { path: '/board', revision: 'r1' } });
    bus.host.setMode('select');
    send({ type: GATEWAY_SDK_TARGET_TYPE, page: { path: '/board', revision: 'r1' },
      selection: { id: 'sel-1', kind: 'element', label: '', target: { element_id: 'btn',
        rect: { x: 0.1, y: 0.1, width: 0.2, height: 0.2 }, coordinate_space: 'viewport', viewport } } });
    return { ...bus, connect, send, onPageChange,
      capture: (extra = {}) => bus.host.captureScreenshot({ selectionId: 'sel-1', page: { path: '/board', revision: 'r1' }, ...extra }) };
  }

  it.each(['unsupported-style', 'capture-failed', 'renderer-unavailable'])('preserves a recognized screenshot category (%s) without raw renderer details', async (code) => {
    const f = readyWithSelection();
    const pending = f.capture();
    const settled = pending.catch(error => error);
    const request = f.h.posted.filter(entry => entry.message.type === 'catsco.gateway.annotation.screenshot.request.v1').at(-1).message;
    f.send({ type: 'catsco.gateway.annotation.screenshot.result.v1', request_id: request.request_id,
      selection_id: 'sel-1', page: { path: '/board', revision: 'r1' }, error: { code, message: 'raw CSS or private details' } });
    expect((await settled).code).toBe(code);
    expect((await settled).message).not.toContain('private details');
    f.host.dispose();
  });

  it('advertises screenshot support from ready and resolves one validated pair', async () => {
    const f = readyWithSelection();
    expect(f.host.screenshotSupported()).toBe(true);
    const pending = f.capture();
    const request = f.h.posted.filter((entry) => entry.message.type === 'catsco.gateway.annotation.screenshot.request.v1').at(-1);
    expect(request).toBeTruthy();
    expect(request.message).toMatchObject({ contract_version: BRIDGE, session_id: f.connect.session_id, selection_id: 'sel-1', page: { path: '/board', revision: 'r1' } });
    f.h.handleWindowMessage({ data: { type: 'catsco.gateway.annotation.screenshot.result.v1', contract_version: BRIDGE,
      session_id: f.connect.session_id, request_id: request.message.request_id, selection_id: 'sel-1',
      page: { path: '/board', revision: 'r1' }, screenshots: [image('full', 1000, 500), image('crop', 232, 132)],
      warnings: ['cross-origin-image', 'cross-origin-image'] }, origin: f.h.origin, source: f.h.contentWindow });
    const result = await pending;
    expect(result).toMatchObject({ selection_id: 'sel-1', page: { path: '/board', revision: 'r1' }, warnings: ['cross-origin-image'] });
    expect(result.screenshots.map((entry) => entry.role)).toEqual(['full', 'crop']);
    f.host.dispose();
  });

  it('accepts the exact SDK crop geometry for a 1000x500 viewport (float regression)', async () => {
    // rect .1/.1/.2/.2 on a 1000x500 viewport: the SDK computes
    // ceil(0.1*1000 + 0.2*1000 + 16) = 316 and ceil(0.1*500 + 0.2*500 + 16) = 116,
    // so the crop is 232x132. Summing the rect first gives 317/117 and a false reject.
    const f = readyWithSelection();
    const pending = f.capture();
    const request = f.h.posted.filter((entry) => entry.message.type === 'catsco.gateway.annotation.screenshot.request.v1').at(-1);
    f.h.handleWindowMessage({ data: { type: 'catsco.gateway.annotation.screenshot.result.v1', contract_version: BRIDGE,
      session_id: f.connect.session_id, request_id: request.message.request_id, selection_id: 'sel-1',
      page: { path: '/board', revision: 'r1' }, screenshots: [image('full', 1000, 500), image('crop', 232, 132)],
      warnings: [] }, origin: f.h.origin, source: f.h.contentWindow });
    const result = await pending;
    expect(result.screenshots[1]).toMatchObject({ role: 'crop', width: 232, height: 132 });
    f.host.dispose();
  });

  it('rejects mismatched crop geometry and forged JPEG geometry', async () => {
    const f = readyWithSelection();
    const ask = async (screenshots, sessionId = f.connect.session_id) => {
      const pending = f.capture();
      const request = f.h.posted.filter((entry) => entry.message.type === 'catsco.gateway.annotation.screenshot.request.v1').at(-1).message;
      f.h.handleWindowMessage({ data: { type: 'catsco.gateway.annotation.screenshot.result.v1', contract_version: BRIDGE,
        session_id: sessionId, request_id: request.request_id, selection_id: 'sel-1',
        page: { path: '/board', revision: 'r1' }, screenshots, warnings: [] }, origin: f.h.origin, source: f.h.contentWindow });
      return expect(pending).rejects.toThrow('bad-screenshot');
    };
    await ask([image('full', 1000, 500), image('crop', 240, 140)]);
    await ask([image('full', 1000, 500), image('crop', 100, 100)]);
    const forged = image('full', 1000, 500);
    forged.width = 640;
    await ask([forged, image('crop', 233, 133)]);
    f.host.dispose();
  });

  it('ignores a screenshot result replayed from a foreign session', async () => {
    vi.useFakeTimers();
    try {
      const f = readyWithSelection();
      const pending = f.capture();
      const settled = pending.catch((error) => error);
      f.h.handleWindowMessage({ data: { type: 'catsco.gateway.annotation.screenshot.result.v1', contract_version: BRIDGE,
        session_id: 'attacker-session', request_id: f.h.posted.filter((entry) => entry.message.type === 'catsco.gateway.annotation.screenshot.request.v1').at(-1).message.request_id,
        selection_id: 'sel-1', page: { path: '/board', revision: 'r1' },
        screenshots: [image('full', 1000, 500), image('crop', 233, 133)], warnings: [] }, origin: f.h.origin, source: f.h.contentWindow });
      await vi.advanceTimersByTimeAsync(20000);
      await expect(settled).resolves.toBeInstanceOf(Error); expect((await settled).code).toBe('capture-timeout');
      f.host.dispose(); // clears the timeout so nothing rejects after the case
    } finally { vi.useRealTimers(); }
  });

  it('cancels in-flight capture on supersede and page report, and refuses after dispose', async () => {
    const f = readyWithSelection();
    const first = f.capture();
    const second = f.capture();
    await expect(first).rejects.toThrow('superseded');
    f.send({ type: GATEWAY_SDK_PAGE_TYPE, page: { path: '/board', revision: 'r1' } });
    await expect(second).rejects.toThrow('stale-document');
    expect(f.onPageChange).toHaveBeenCalled();
    // The page report voids the selection, so no new capture may start.
    await expect(f.capture()).rejects.toThrow('stale-selection');
    f.host.dispose();
    await expect(f.capture()).rejects.toThrow('stale-document');
    expect(f.h.posted.some((entry) => entry.message.type === 'catsco.gateway.annotation.screenshot.cancel.v1')).toBe(true);
  });

  it('times out and refuses capture without screenshot support', async () => {
    vi.useFakeTimers();
    try {
      const f = readyWithSelection();
      const pending = f.capture();
      const settled = pending.catch((error) => error);
      await vi.advanceTimersByTimeAsync(20000);
      await expect(settled).resolves.toBeInstanceOf(Error); expect((await settled).code).toBe('capture-timeout');
      f.host.dispose(); // no stray timer may outlive the case
    } finally { vi.useRealTimers(); }
    const bus = setup();
    bus.host.connect();
    const connect = bus.h.connectMessage();
    bus.h.handleWindowMessage({ data: { type: GATEWAY_SDK_READY_TYPE, contract_version: BRIDGE, session_id: connect.session_id,
      request_id: connect.request_id, capabilities: ['element'], page: { path: '/board' } }, origin: bus.h.origin, source: bus.h.contentWindow });
    expect(bus.host.screenshotSupported()).toBe(false);
    await expect(bus.host.captureScreenshot({ selectionId: 'sel-1', page: { path: '/board' } })).rejects.toThrow('screenshot-unavailable');
    bus.host.dispose();
  });

  it('aborts through the caller signal and rejects a stale selection or page', async () => {
    const f = readyWithSelection();
    const controller = new AbortController();
    const pending = f.capture({ signal: controller.signal });
    controller.abort();
    await expect(pending).rejects.toThrow('canceled');
    await expect(f.host.captureScreenshot({ selectionId: 'other', page: { path: '/board', revision: 'r1' } })).rejects.toThrow('stale-selection');
    await expect(f.host.captureScreenshot({ selectionId: 'sel-1', page: { path: '/board', revision: 'r9' } })).rejects.toThrow('stale-selection');
    f.host.dispose();
  });
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


  it('tab is legal free text across text/prefix/suffix/body; other control chars stay rejected', () => {
    const tabSelection = normalizeGatewayAnnotationSelection({
      id: 't1', kind: 'text', label: '表格',
      target: { text: 'before\tafter', prefix: '列\t头', suffix: '行\t尾' },
    });
    expect(tabSelection).not.toBeNull();
    expect(tabSelection.target.text).toContain('\t');

    const tabBody = normalizeGatewayAnnotations(validMetadata({
      annotations: [{ id: 'b1', kind: 'text', label: '', body: '评论\t带制表符', target: { text: '示例' } }],
    }));
    expect(tabBody).not.toBeNull();
    expect(tabBody.annotations[0].body).toContain('\t');

    // Non-break control characters (e.g. \u0001) remain rejected everywhere.
    expect(normalizeGatewayAnnotationSelection({
      id: 't2', kind: 'text', label: '', target: { text: 'bad\u0001char' },
    })).toBeNull();
    expect(normalizeGatewayAnnotations(validMetadata({
      annotations: [{ id: 'b2', kind: 'text', label: '', body: 'bad\u0001body', target: { text: '示例' } }],
    }))).toBeNull();
    // Identifiers still reject tabs.
    expect(normalizeGatewayAnnotationSelection({
      id: 'a\tb', kind: 'element', label: '', target: { element_id: 'x' },
    })).toBeNull();
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
