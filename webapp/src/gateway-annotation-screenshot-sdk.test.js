import { readFileSync } from 'node:fs';
import { JSDOM } from 'jsdom';
import { afterEach, describe, expect, it, vi } from 'vitest';

const source = readFileSync(`${process.cwd()}/public/catsco-annotations.js`, 'utf8');
const ORIGIN = 'https://app.catsco.example';
const APP = 'https://artifact.catsco.example';
const BRIDGE = 'catsco.gateway-annotation-bridge.v1';

const windows = [];
let restoreCanvas = null;

// A real 2D context is impossible in jsdom, so the canvas surface is
// replaced with a recording stub. Everything else (SDK code, geometry,
// lifecycle guards, message protocol) runs as production bytes.
function installCanvas(w) {
  const context = { strokeStyle: '', lineWidth: 0, drawImage: vi.fn(), strokeRect: vi.fn() };
  const proto = w.HTMLCanvasElement.prototype;
  const original = proto.getContext;
  const originalToDataURL = proto.toDataURL;
  const boxes = [];
  proto.getContext = function getContext(kind) {
    if (kind !== '2d') return null;
    boxes.push(this);
    this.__ctx = context;
    return context;
  };
  const jpeg = (width, height) => btoa(String.fromCharCode(255, 216, 255, 192, 0, 17, 8,
    (height >> 8) & 255, height & 255, (width >> 8) & 255, width & 255,
    3, 1, 0x11, 0, 2, 0x11, 1, 3, 0x11, 1, 255, 217));
  proto.toDataURL = function toDataURL() { return `data:image/jpeg;base64,${jpeg(this.width, this.height)}`; };
  return { boxes, context, restore() { proto.getContext = original; proto.toDataURL = originalToDataURL; } };
}

// jsdom never executes <script src>, so the pinned self-host load is
// simulated faithfully: the appended tag must carry the fixed URL + SRI,
// and a load event exposes window.html2canvas as the pro browser bundle
// does. The SDK itself is never relaxed for the fixture.
function installRendererLoader(w, makeRenderer, behaviour = 'ok') {
  const pending = [];
  const tags = [];
  const observer = new w.MutationObserver(() => {
    for (const script of pending.splice(0)) {
      if (!script.src.includes('/_catsco/runtime/html2canvas-pro-1.6.7.min.js')
        || script.integrity !== 'sha384-CqHBfwlOY3BunFNI9xxzy+h/+/df5g0tI05vSbd8kIwTTPk23b5jYDpyrlxfukc+') continue;
      const behaviour = script.dataset.testBehaviour ?? 'ok';
      if (behaviour === 'fail') { queueMicrotask(() => script.onerror?.(new Event('error'))); return; }
      if (behaviour === 'empty') { queueMicrotask(() => script.onload?.(new Event('load'))); return; }
      queueMicrotask(() => { w.html2canvas = renderer; script.onload?.(new Event('load')); });
    }
  });
  observer.observe(w.document.head, { childList: true });
  // One frozen renderer object for the whole fixture: individual cases only
  // change its mockImplementation, never the window global identity.
  const renderer = makeRenderer();
  const original = w.document.head.appendChild.bind(w.document.head);
  w.document.head.appendChild = (node) => {
    const result = original(node);
    if (node.tagName === 'SCRIPT') { node.dataset.testBehaviour = behaviour; pending.push(node); tags.push(node); }
    return result;
  };
  return { tags, renderer };
}

function fixture({ selectionKind = 'click', dpr = 1, rendererBehaviour = 'ok' } = {}) {
  const dom = new JSDOM('<!doctype html><html><head></head><body><button id="shot">发布</button></body></html>', {
    url: `${APP}/board/`, runScripts: 'outside-only',
  });
  windows.push(dom);
  const w = dom.window;
  const canvas = installCanvas(w);
  restoreCanvas = canvas.restore;
  for (const [key, value] of [['innerWidth', 1000], ['innerHeight', 500], ['scrollX', 0], ['scrollY', 0], ['devicePixelRatio', dpr]]) {
    Object.defineProperty(w, key, { configurable: true, value });
  }
  const posted = [];
  const parent = { postMessage: vi.fn((message) => posted.push(message)) };
  Object.defineProperty(w, 'parent', { configurable: true, value: parent });
  const marker = w.document.createElement('script');
  marker.src = `${APP}/_catsco/runtime/annotations-v1.js`;
  marker.setAttribute('data-catsco-parent-origins', JSON.stringify([ORIGIN]));
  w.document.head.appendChild(marker);
  Object.defineProperty(w.document, 'currentScript', { configurable: true, value: marker });
  w.eval(source);
  Object.defineProperty(w.document, 'currentScript', { configurable: true, value: null });
  const types = w.CatsCoAnnotations.types;
  const json = (value) => w.JSON.parse(JSON.stringify(value));
  const send = (data) => w.dispatchEvent(new w.MessageEvent('message', { origin: ORIGIN, source: parent, data: json(data) }));
  const button = w.document.getElementById('shot');
  button.getBoundingClientRect = () => ({ left: 100, top: 50, width: 200, height: 100 });
  // Config must originate in the window realm: plain-object checks compare the
  // realm prototype, and a rejected create would silently fake a dead SDK.
  const sdk = w.CatsCoAnnotations.create(json({ parentOrigin: ORIGIN, revision: 'r1' }));
  if (!sdk) throw new Error('SDK create failed in fixture');
  send({ type: types.connect, contract_version: BRIDGE, session_id: 's1', request_id: 'q1' });
  send({ type: types.mode, contract_version: BRIDGE, session_id: 's1', mode: 'select' });
  const mouse = (type, x, y, target = button) => target.dispatchEvent(new w.MouseEvent(type, {
    bubbles: true, cancelable: true, button: 0, clientX: x, clientY: y,
  }));
  if (selectionKind === 'click') {
    mouse('mousedown', 110, 60); mouse('mouseup', 110, 60); mouse('click', 110, 60);
  } else {
    mouse('mousedown', 100, 100); mouse('mouseup', 300, 200); mouse('click', 300, 200);
  }
  const selection = posted.find((message) => message.type === types.target)?.selection ?? null;
  const rendererCalls = [];
  const loader = installRendererLoader(w, () => vi.fn((element, options) => {
    rendererCalls.push({ element, options });
    const bitmap = w.document.createElement('canvas');
    bitmap.width = Math.floor(options.width * options.scale);
    bitmap.height = Math.floor(options.height * options.scale);
    return Promise.resolve(bitmap);
  }), rendererBehaviour);
  const request = (overrides = {}) => send({ type: types.screenshotRequest, contract_version: BRIDGE, session_id: 's1',
    request_id: 'rq', selection_id: selection.id, page: { path: '/board/', revision: 'r1' }, ...overrides });
  return { w, posted, send, sdk, selection, rendererCalls, request, button, mouse, types, canvas, loader,
    renderer: loader.renderer,
    results: () => posted.filter((message) => message.type === types.screenshotResult),
    pages: () => posted.filter((message) => message.type === types.page) };
}

afterEach(() => {
  restoreCanvas?.();
  restoreCanvas = null;
  for (const dom of windows.splice(0)) {
    dom.window.CatsCoAnnotations?.dispose();
    dom.window.close();
  }
});

describe('select-mode screenshot capture', () => {
  it('derives full red-box and padded crop from ONE viewport bitmap', async () => {
    const f = fixture();
    f.request();
    await vi.waitFor(() => expect(f.results()).toHaveLength(1));
    const result = f.results()[0];
    expect(result.error).toBeUndefined();
    expect(result.request_id).toBe('rq');
    expect(result.selection_id).toBe(f.selection.id);
    expect(result.page).toEqual({ path: '/board/', revision: 'r1' });
    expect(result.warnings).toEqual([]);
    expect(result.screenshots.map((image) => image.role)).toEqual(['full', 'crop']);
    expect(result.screenshots[0]).toMatchObject({ mime_type: 'image/jpeg', width: 1000, height: 500 });
    expect(result.screenshots[1]).toMatchObject({ width: 232, height: 132 });
    expect(f.rendererCalls).toHaveLength(1);
    const { options } = f.rendererCalls[0];
    expect(options).toMatchObject({ x: 0, y: 0, width: 1000, height: 500, windowWidth: 1000, windowHeight: 500,
      scale: 1, backgroundColor: '#ffffff', allowTaint: false, useCORS: false, logging: false, imageTimeout: 5000 });
    expect(options.ignoreElements).toBeTypeOf('function');
    expect(options.onclone).toBeTypeOf('function');
    // Exactly two output canvases come from us; the bitmap is the renderer's.
    expect(f.canvas.boxes).toHaveLength(2);
    const [full, crop] = f.canvas.boxes;
    expect([full.width, full.height]).toEqual([1000, 500]);
    expect([crop.width, crop.height]).toEqual([232, 132]);
    // The red frame belongs to the full frame only; the crop stays clean.
    expect(f.canvas.context.strokeRect).toHaveBeenCalledTimes(1);
    expect(f.canvas.context.strokeStyle).toBe('#ef4444');
  });

  it('renders a viewport-sized bitmap for a long page and caps DPR at 2', async () => {
    const f = fixture({ dpr: 3 });
    const tall = f.w.document.createElement('section');
    tall.style.height = '9000px';
    f.w.document.body.append(tall);
    const calls = f.rendererCalls;
    f.request();
    await vi.waitFor(() => expect(f.results()).toHaveLength(1));
    expect(f.results()[0].error).toBeUndefined();
    expect(calls).toHaveLength(1);
    expect(calls[0].options.height).toBe(500);
    expect(calls[0].options.scale).toBe(2);
    expect(f.results()[0].screenshots[0]).toMatchObject({ width: 2000, height: 1000 });
  });

  it('flags cross-origin/video/embedded risk and masks sensitive input in the clone', async () => {
    const f = fixture();
    const { w } = f;
    const image = w.document.createElement('img');
    image.src = 'https://cdn.example.com/a.png';
    Object.defineProperty(image, 'currentSrc', { value: 'https://cdn.example.com/a.png' });
    const video = w.document.createElement('video');
    const frame = w.document.createElement('iframe');
    const password = w.document.createElement('input');
    password.setAttribute('type', 'password');
    password.value = 'secret-value';
    w.document.body.append(image, video, frame, password);
    // jsdom has no layout: give every risk element a visible box.
    for (const node of [image, video, frame, password]) {
      node.getBoundingClientRect = () => ({ left: 0, top: 0, width: 200, height: 40 });
    }
    let cloned = null;
    const realRenderer = f.renderer.getMockImplementation();
    f.renderer.mockImplementation((element, options) => {
      // Exercise the real production mask callback on a detached clone.
      const clone = element.cloneNode(true);
      const input = clone.querySelector('input[type=password]');
      const rect = input.getBoundingClientRect();
      Object.defineProperty(input, 'getBoundingClientRect', { value: () => rect });
      options.onclone(clone, documentClone(w));
      cloned = { value: input.value, text: input.textContent, style: input.getAttribute('style') || '' };
      const bitmap = w.document.createElement('canvas');
      bitmap.width = 1000; bitmap.height = 500;
      return Promise.resolve(bitmap);
    });
    f.request();
    await vi.waitFor(() => expect(f.results()).toHaveLength(1));
    expect(f.results()[0].warnings).toEqual(expect.arrayContaining(
      ['cross-origin-image', 'video', 'embedded-content', 'sensitive-content-masked']));
    expect(f.results()[0].screenshots).toHaveLength(2);
    expect(cloned.value).toBe('');
    expect(cloned.text).toBe('');
    expect(cloned.style).toMatch(/opacity:\s*0/i);
    // The real application DOM keeps its own state untouched.
    expect(password.value).toBe('secret-value');
    // Restore the default renderer implementation for the remaining assertions.
    f.renderer.mockImplementation(realRenderer);
  });

  it('ignores cross-origin backgrounds and hidden risk elements', async () => {
    const f = fixture();
    const { w } = f;
    const styled = w.document.createElement('div');
    styled.style.backgroundImage = 'url(https://cdn.example.com/bg.png)';
    const hidden = w.document.createElement('img');
    hidden.src = 'https://cdn.example.com/hidden.png';
    hidden.style.display = 'none';
    const sameOrigin = w.document.createElement('img');
    sameOrigin.src = `${APP}/local.png`;
    w.document.body.append(styled, hidden, sameOrigin);
    for (const node of [styled, hidden, sameOrigin]) {
      node.getBoundingClientRect = () => ({ left: 0, top: 0, width: 200, height: 40 });
    }
    f.request();
    await vi.waitFor(() => expect(f.results()).toHaveLength(1));
    expect(f.results()[0].warnings).toContain('cross-origin-background');
    expect(f.results()[0].warnings).not.toContain('cross-origin-image');
  });

  it('scroll after a COMPLETED capture notifies the host and refuses further shots', async () => {
    const f = fixture();
    f.request();
    await vi.waitFor(() => expect(f.results()).toHaveLength(1));
    const pagesBefore = f.pages().length;
    f.w.dispatchEvent(new f.w.Event('scroll'));
    // The popover is closed by the host's page handler, not by a second image.
    expect(f.pages().length).toBeGreaterThan(pagesBefore);
    expect(f.pages().at(-1).page).toEqual({ path: '/board/', revision: 'r1' });
    f.request({ request_id: 'rq2' });
    await new Promise((resolve) => setTimeout(resolve, 10));
    expect(f.results()).toHaveLength(1); // stale selection: no new capture
    expect(f.sdk.mode()).toBe('select'); // selection mode itself is untouched
  });

  it('resize invalidates the same way as scroll', async () => {
    const f = fixture();
    f.request();
    await vi.waitFor(() => expect(f.results()).toHaveLength(1));
    Object.defineProperty(f.w, 'innerHeight', { configurable: true, value: 640 });
    f.w.dispatchEvent(new f.w.Event('resize'));
    expect(f.pages().length).toBeGreaterThan(0);
    f.request({ request_id: 'rq2' });
    await new Promise((resolve) => setTimeout(resolve, 10));
    expect(f.results()).toHaveLength(1);
  });

  it('a capture invalidated mid-flight never publishes its bitmap', async () => {
    const f = fixture();
    let resolveRenderer = null;
    f.renderer.mockImplementation(() => new Promise((resolve) => { resolveRenderer = resolve; }));
    f.request();
    await vi.waitFor(() => expect(f.renderer).toHaveBeenCalledTimes(1));
    f.w.dispatchEvent(new f.w.Event('scroll'));
    const bitmap = f.w.document.createElement('canvas');
    bitmap.width = 1000; bitmap.height = 500;
    resolveRenderer(bitmap);
    await vi.waitFor(() => expect(f.results()).toHaveLength(1));
    expect(f.results()[0].error.code).toBe('stale-document');
    expect(f.results().filter((message) => message.screenshots)).toHaveLength(0);
  });

  it('refuses a concurrent request while the renderer is busy', async () => {
    const f = fixture();
    let resolveRenderer = null;
    f.renderer.mockImplementation(() => new Promise((resolve) => { resolveRenderer = resolve; }));
    f.request();
    await vi.waitFor(() => expect(f.renderer).toHaveBeenCalledTimes(1));
    f.request({ request_id: 'rq-b' });
    await vi.waitFor(() => expect(f.results().some((message) => message.error?.code === 'capture-busy')).toBe(true));
    const bitmap = f.w.document.createElement('canvas');
    bitmap.width = 1000; bitmap.height = 500;
    resolveRenderer(bitmap);
    await vi.waitFor(() => expect(f.results().filter((message) => message.screenshots).length).toBe(1));
    expect(f.results().filter((message) => message.screenshots)).toHaveLength(1);
  });

  it('a new selection voids the old certificate but is itself capturable', async () => {
    const f = fixture();
    f.request();
    await vi.waitFor(() => expect(f.results()).toHaveLength(1));
    const firstId = f.selection.id;
    f.mouse('mousedown', 120, 70); f.mouse('mouseup', 120, 70); f.mouse('click', 120, 70);
    const secondId = f.posted.filter((message) => message.type === f.types.target).at(-1).selection.id;
    expect(secondId).not.toBe(firstId);
    f.request({ request_id: 'rq-stale', selection_id: firstId });
    await new Promise((resolve) => setTimeout(resolve, 10));
    expect(f.results()).toHaveLength(1);
    f.request({ request_id: 'rq-new', selection_id: secondId });
    await vi.waitFor(() => expect(f.results()).toHaveLength(2));
    expect(f.results()[1].selection_id).toBe(secondId);
    expect(f.results()[1].screenshots).toHaveLength(2);
  });

  it.each([
    ['navigation', (f) => f.w.history.pushState({}, '', '/other-page'), 'stale-document'],
    ['revision', (f) => f.sdk.setRevision('r2'), 'stale-document'],
    ['session', (f) => f.send({ type: f.types.connect, contract_version: BRIDGE, session_id: 's2', request_id: 'q2' }), 'stale-document'],
    ['mode off', (f) => f.send({ type: f.types.mode, contract_version: BRIDGE, session_id: 's1', mode: 'off' }), 'stale-document'],
    ['escape', (f) => f.w.document.dispatchEvent(new f.w.KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true })), 'stale-document'],
  ])('%s after a completed capture voids the certificate', async (_name, invalidate) => {
    const f = fixture();
    f.request();
    await vi.waitFor(() => expect(f.results()).toHaveLength(1));
    invalidate(f);
    f.request({ request_id: 'rq-next' });
    await new Promise((resolve) => setTimeout(resolve, 10));
    expect(f.results().filter((message) => message.screenshots)).toHaveLength(1);
  });

  it('dispose drops the pending capture without publishing it', async () => {
    const f = fixture();
    let resolveRenderer = null;
    f.renderer.mockImplementation(() => new Promise((resolve) => { resolveRenderer = resolve; }));
    f.request();
    await vi.waitFor(() => expect(f.renderer).toHaveBeenCalledTimes(1));
    f.sdk.dispose();
    resolveRenderer(f.w.document.createElement('canvas'));
    await vi.waitFor(() => expect(f.results().some((message) => message.error)).toBe(true));
    expect(f.results().filter((message) => message.screenshots)).toHaveLength(0);
  });

  it('ignores stale page/revision/session/selection requests without renderer work', async () => {
    const f = fixture();
    f.request({ page: { path: '/other', revision: 'r1' } });
    f.request({ page: { path: '/board/', revision: 'r2' } });
    f.request({ session_id: 'old' });
    f.request({ selection_id: 'other' });
    f.request({ request_id: '' });
    f.send({ type: f.types.screenshotCancel, contract_version: BRIDGE, session_id: 's1', request_id: 'rq' });
    await new Promise((resolve) => setTimeout(resolve, 10));
    expect(f.renderer).not.toHaveBeenCalled();
    expect(f.results()).toHaveLength(0);
  });

  it('surfaces explicit renderer failures instead of a fake success', async () => {
    const f = fixture();
    f.renderer.mockImplementation(() => Promise.reject(new Error('boom')));
    f.request();
    await vi.waitFor(() => expect(f.results()).toHaveLength(1));
    expect(f.results()[0].error.code).toBe('capture-failed');
    const wrongSize = f.w.document.createElement('canvas');
    wrongSize.width = 10; wrongSize.height = 10;
    f.renderer.mockImplementation(() => Promise.resolve(wrongSize));
    f.request({ request_id: 'rq2' });
    await vi.waitFor(() => expect(f.results()).toHaveLength(2));
    expect(f.results()[1].error.code).toBe('bad-geometry');
    f.renderer.mockImplementation(() => Promise.resolve(null));
    f.request({ request_id: 'rq3' });
    await vi.waitFor(() => expect(f.results()).toHaveLength(3));
    expect(f.results()[2].error.code).toBe('bad-geometry');
    f.renderer.mockImplementation(() => Promise.reject(new Error('renderer-unavailable')));
    f.request({ request_id: 'rq4' });
    await vi.waitFor(() => expect(f.results()).toHaveLength(4));
    expect(f.results()[3].error.code).toBe('renderer-unavailable');
  });

  it.each([
    [new Error('Attempting to parse an unsupported color function "future-color"'), 'unsupported-style'],
    [new Error('Attempting to parse an unsupported image function "future-gradient"'), 'unsupported-style'],
    [new Error('unrelated renderer failure'), 'capture-failed'],
    [null, 'capture-failed'],
    ['arbitrary thrown value', 'capture-failed'],
  ])('classifies renderer errors without returning their messages (%s)', async (error, code) => {
    const f = fixture();
    f.renderer.mockRejectedValue(error);
    f.request();
    await vi.waitFor(() => expect(f.results()).toHaveLength(1));
    expect(f.results()[0].error).toEqual({ code });
    expect(f.results()[0].screenshots).toBeUndefined();
  });

  it('masks cloned shadow boundaries while preserving layout and the application DOM', async () => {
    const f = fixture();
    const host = f.w.document.createElement('div');
    const shadow = host.attachShadow({ mode: 'open' });
    host.id = 'shadow-probe';
    shadow.innerHTML = '<input value="shadow-secret"><span data-catsco-annotation-sensitive>private</span>';
    host.getBoundingClientRect = () => ({ left: 0, top: 0, width: 200, height: 60 });
    f.w.document.body.append(host);
    let cloned;
    f.renderer.mockImplementation((element, options) => {
      const clone = element.cloneNode(true);
      cloned = clone.querySelector('#shadow-probe');
      cloned.attachShadow({ mode: 'open' }).innerHTML = shadow.innerHTML;
      cloned.getBoundingClientRect = host.getBoundingClientRect;
      options.onclone(clone);
      const bitmap = f.w.document.createElement('canvas');
      bitmap.width = 1000; bitmap.height = 500;
      return Promise.resolve(bitmap);
    });
    f.request();
    await vi.waitFor(() => expect(f.results()).toHaveLength(1));
    expect(f.results()[0].error).toBeUndefined();
    expect(f.results()[0].warnings).toEqual(expect.arrayContaining(['embedded-content', 'sensitive-content-masked']));
    expect(cloned.style.opacity).toBe('0');
    expect(cloned.style.width).toBe('200px');
    expect(cloned.style.height).toBe('60px');
    expect(host.style.opacity).toBe('');
    expect(shadow.querySelector('input').value).toBe('shadow-secret');
  });

  it('includes shadow descendants in the page size limit before loading the renderer', async () => {
    const f = fixture();
    const host = f.w.document.createElement('div');
    host.attachShadow({ mode: 'open' }).innerHTML = '<span></span>'.repeat(10001);
    f.w.document.body.append(host);
    f.request();
    await vi.waitFor(() => expect(f.results()).toHaveLength(1));
    expect(f.results()[0].error.code).toBe('page-too-large');
    expect(f.renderer).not.toHaveBeenCalled();
    expect(f.loader.tags).toHaveLength(0);
  });

  it('always loads the pinned self-hosted bundle with SRI and freezes that renderer', async () => {
    const f = fixture();
    // An app-provided global must NOT satisfy the SDK without our own load.
    const appStub = vi.fn(() => Promise.reject(new Error('app-renderer')));
    f.w.html2canvas = appStub;
    f.request();
    await vi.waitFor(() => expect(f.results()).toHaveLength(1));
    const script = f.loader.tags.at(-1);
    expect(script.getAttribute('src')).toBe(`${APP}/_catsco/runtime/html2canvas-pro-1.6.7.min.js`);
    expect(script.integrity).toBe('sha384-CqHBfwlOY3BunFNI9xxzy+h/+/df5g0tI05vSbd8kIwTTPk23b5jYDpyrlxfukc+');
    expect(script.getAttribute('crossorigin')).toBe('anonymous');
    expect(f.results()[0].screenshots).toHaveLength(2);
    expect(f.rendererCalls).toHaveLength(1); // verified bundle, not the app stub
    expect(appStub).not.toHaveBeenCalled();
    // The application keeps its own renderer object after our load.
    expect(f.w.html2canvas).toBe(appStub);
  });

  it('restores a pre-existing app global and removes ours when the app had none', async () => {
    const withApp = fixture();
    const appStub = vi.fn();
    withApp.w.html2canvas = appStub;
    withApp.request();
    await vi.waitFor(() => expect(withApp.results()).toHaveLength(1));
    expect(withApp.results()[0].screenshots).toHaveLength(2);
    expect(withApp.w.html2canvas).toBe(appStub);

    const withoutApp = fixture();
    delete withoutApp.w.html2canvas;
    withoutApp.request();
    await vi.waitFor(() => expect(withoutApp.results()).toHaveLength(1));
    expect(withoutApp.results()[0].screenshots).toHaveLength(2);
    expect(withoutApp.w.html2canvas).toBeUndefined();
  });

  it('preserves the app global when the pinned bundle fails to load', async () => {
    const f = fixture({ rendererBehaviour: 'fail' });
    const appStub = vi.fn();
    f.w.html2canvas = appStub;
    f.request();
    await vi.waitFor(() => expect(f.results()).toHaveLength(1));
    expect(f.results()[0].error.code).toBe('renderer-unavailable');
    expect(f.w.html2canvas).toBe(appStub);
  });

  it('reports renderer load failure instead of any image', async () => {
    const f = fixture({ rendererBehaviour: 'fail' });
    f.request();
    await vi.waitFor(() => expect(f.results()).toHaveLength(1));
    expect(f.results()[0].error.code).toBe('renderer-unavailable');
  });

  it('reports a verified load that exposes no renderer function', async () => {
    const f = fixture({ rendererBehaviour: 'empty' });
    f.request();
    await vi.waitFor(() => expect(f.results()).toHaveLength(1));
    expect(f.results()[0].error.code).toBe('renderer-unavailable');
  });

  it('a region selection produces the matching crop from its own geometry', async () => {
    const f = fixture({ selectionKind: 'region' });
    expect(f.selection.kind).toBe('region');
    f.request();
    await vi.waitFor(() => expect(f.results()).toHaveLength(1));
    expect(f.results()[0].screenshots[0]).toMatchObject({ width: 1000, height: 500 });
    expect(f.results()[0].screenshots[1]).toMatchObject({ width: 232, height: 132 });
    expect(JSON.stringify(f.results()[0])).not.toContain('secret');
  });
});

// jsdom clone root for maskScreenshotClone; a detached <html> element is enough
// because the production callback only walks clone.querySelectorAll('*').
function documentClone(w) {
  return w.document.documentElement.cloneNode(false);
}