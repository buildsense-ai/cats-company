import { readFileSync } from 'node:fs';
import { createServer } from 'node:http';
import { JSDOM } from 'jsdom';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { createGatewayAnnotationHost } from './gateway-annotations';

const source = readFileSync(`${process.cwd()}/public/catsco-annotations.js`, 'utf8');
const ORIGIN = 'https://app.catsco.example';
const OTHER = 'https://other.catsco.example';
const APP = 'https://artifact.catsco.example';
const BRIDGE = 'catsco.gateway-annotation-bridge.v1';
const CONNECT = 'catsco.gateway.annotation.connect.v1';
const READY = 'catsco.gateway.annotation.ready.v1';
const TARGET = 'catsco.gateway.annotation.target.v1';
const fixtures = [];

function fixture({ attribute = JSON.stringify([ORIGIN]), standalone = false, load = true } = {}) {
  // Real production bytes evaluated as an external classic script would be.
  // outside-only is test execution, not a production CSP relaxation.
  const dom = new JSDOM('<!doctype html><html><head></head><body><button id="publish">Publish</button><p id="text">fresh text</p></body></html>', {
    url: `${APP}/board/`, runScripts: 'outside-only',
  });
  fixtures.push(dom);
  const w = dom.window;
  const posted = [];
  const parent = standalone ? w : { postMessage: vi.fn((message, origin) => posted.push({ message, origin })) };
  Object.defineProperty(w, 'parent', { configurable: true, value: parent });
  const json = (value) => w.JSON.parse(JSON.stringify(value));
  const loadScript = (attr = attribute) => {
    const script = w.document.createElement('script');
    script.src = `${APP}/_catsco/runtime/annotations-v1.js`;
    if (attr !== null) script.setAttribute('data-catsco-parent-origins', attr);
    w.document.head.appendChild(script);
    Object.defineProperty(w.document, 'currentScript', { configurable: true, value: script });
    w.eval(source);
    Object.defineProperty(w.document, 'currentScript', { configurable: true, value: null });
  };
  const dispatch = (data, { origin = ORIGIN, sender = parent } = {}) => w.dispatchEvent(new w.MessageEvent('message', {
    origin, source: sender, data: json(data),
  }));
  const connect = (session = 'session-1', request = 'request-1', options) => dispatch({
    type: CONNECT, contract_version: BRIDGE, session_id: session, request_id: request,
  }, options);
  const mode = (kind, session = 'session-1') => dispatch({
    type: 'catsco.gateway.annotation.mode.v1', contract_version: BRIDGE, session_id: session, mode: kind,
  });
  const create = (config = {}) => w.CatsCoAnnotations.create(Object.assign(json({ parentOrigin: ORIGIN }), config));
  const messages = (type) => posted.filter((entry) => entry.message.type === type);
  if (load) loadScript();
  return { w, parent, posted, connect, mode, create, messages, dispatch, loadScript };
}

afterEach(() => {
  for (const dom of fixtures.splice(0)) {
    dom.window.CatsCoAnnotations?.dispose();
    dom.window.close();
  }
});

describe('gateway-injected annotation bootstrap', () => {
  it('loads real external SDK bytes over HTTP twice, reads escaped currentScript config, and answers first connect', async () => {
    const requests = [];
    const server = createServer((request, response) => {
      requests.push(request.url);
      if (request.url === '/_catsco/runtime/annotations-v1.js') {
        response.writeHead(200, { 'Content-Type': 'application/javascript', 'X-Content-Type-Options': 'nosniff' });
        response.end(source);
      } else {
        response.writeHead(200, { 'Content-Type': 'text/html' });
        const tag = '<script src="/_catsco/runtime/annotations-v1.js" data-catsco-parent-origins="[&quot;https://app.catsco.example&quot;]"></script>';
        response.end(`<!doctype html><html><head>${tag}</head><body><button id="http-target">HTTP app</button>${tag}</body></html>`);
      }
    });
    await new Promise((resolve, reject) => {
      server.once('error', reject);
      server.listen(0, '127.0.0.1', resolve);
    });
    const parent = { postMessage: vi.fn() };
    let dom;
    try {
      dom = await JSDOM.fromURL(`http://127.0.0.1:${server.address().port}/board/`, {
        resources: 'usable', runScripts: 'dangerously',
        beforeParse(w) { Object.defineProperty(w, 'parent', { configurable: true, value: parent }); },
      });
      await new Promise((resolve, reject) => {
        const timer = setTimeout(() => reject(new Error('external runtime load timed out')), 5000);
        const done = () => { clearTimeout(timer); resolve(); };
        if (dom.window.document.readyState === 'complete') done();
        else dom.window.addEventListener('load', done, { once: true });
      });
      const w = dom.window;
      expect(requests.filter((url) => url === '/_catsco/runtime/annotations-v1.js')).toHaveLength(2);
      const send = (data) => w.dispatchEvent(new w.MessageEvent('message', {
        origin: ORIGIN, source: parent, data: w.JSON.parse(JSON.stringify(data)),
      }));
      send({ type: CONNECT, contract_version: BRIDGE, session_id: 'http-session', request_id: 'http-request' });
      expect(parent.postMessage).toHaveBeenCalledTimes(1);
      expect(parent.postMessage.mock.calls[0]).toMatchObject([{ type: READY, session_id: 'http-session', request_id: 'http-request' }, ORIGIN]);
      send({ type: 'catsco.gateway.annotation.mode.v1', contract_version: BRIDGE, session_id: 'http-session', mode: 'select' });
      const button = w.document.getElementById('http-target');
      button.getBoundingClientRect = () => ({ left: 100, top: 100, width: 100, height: 40 });
      for (const type of ['mousedown', 'mouseup', 'click']) button.dispatchEvent(new w.MouseEvent(type, {
        bubbles: true, cancelable: true, button: 0, clientX: 110, clientY: 110,
      }));
      expect(parent.postMessage.mock.calls.filter(([message]) => message.type === TARGET)).toHaveLength(1);
    } finally {
      dom?.window.CatsCoAnnotations?.dispose();
      dom?.window.close();
      server.closeAllConnections();
      await new Promise((resolve) => server.close(resolve));
    }
  });

  it('needs no application create: FIRST parent connect echoes exact session/request with no invented revision', () => {
    const f = fixture();
    expect(f.messages(READY)).toHaveLength(0);
    f.connect();
    expect(f.messages(READY)).toHaveLength(1);
    expect(f.messages(READY)[0]).toMatchObject({ origin: ORIGIN, message: {
      contract_version: BRIDGE, session_id: 'session-1', request_id: 'request-1',
      capabilities: ['element', 'text', 'region'], page: { path: '/board/' },
    } });
    expect(f.messages(READY)[0].message.page.revision).toBeUndefined();
  });

  it.each(['*', 'null', 'https://app.catsco.example/', 'https://app.catsco.example/path',
    'https://app.catsco.example?query', 'https://u:p@app.catsco.example', 'javascript:alert(1)', 'data:text/html,hi'])('rejects unsafe configured origin %s', (origin) => {
    const f = fixture({ attribute: JSON.stringify([origin]) });
    f.connect();
    expect(f.messages(READY)).toHaveLength(0);
    expect(f.create({ parentOrigin: origin })).toBeNull();
  });

  it.each([null, '', '{', '{}', '[]', '["https://app.catsco.example", "*"]', '[42]'])('does not infer missing/malformed allowlist %s', (attribute) => {
    const f = fixture({ attribute });
    // Matching location/referrer hints cannot enable automatic bootstrap.
    f.connect();
    expect(f.messages(READY)).toHaveLength(0);
  });

  it('ignores wrong source/origin/contract/malformed handshake without losing the first valid connect', () => {
    const f = fixture();
    f.connect('evil', 'req', { origin: OTHER });
    f.connect('evil', 'req', { sender: f.w });
    f.dispatch({ type: CONNECT, contract_version: 'wrong', session_id: 'evil', request_id: 'req' });
    for (const [session, request] of [['', 'req'], ['id', ''], ['x'.repeat(129), 'req'], ['id', 'x'.repeat(129)], ['id\n', 'req']]) f.connect(session, request);
    expect(f.messages(READY)).toHaveLength(0);
    f.connect();
    expect(f.messages(READY)).toHaveLength(1);
  });

  it('standalone window cannot bootstrap a parent channel', () => {
    const f = fixture({ standalone: true });
    f.connect();
    expect(f.messages(READY)).toHaveLength(0);
    expect(f.w.document.querySelector('.catsco-annotation-overlay')).toBeNull();
  });

  it('double injected script leaves the API, singleton, history patch and one-message counts unchanged', () => {
    const f = fixture();
    const api = f.w.CatsCoAnnotations;
    f.loadScript(); // duplicate BEFORE connect must not double ready
    f.connect();
    expect(f.messages(READY)).toHaveLength(1);
    const sdk = f.create();
    const push = f.w.history.pushState;
    f.loadScript(); // duplicate AFTER connect must not reset state
    expect(f.w.CatsCoAnnotations).toBe(api);
    expect(f.create()).toBe(sdk);
    expect(f.w.history.pushState).toBe(push);
    f.mode('element');
    f.w.document.getElementById('publish').click();
    expect(f.messages(TARGET)).toHaveLength(1);
    expect(f.w.document.querySelectorAll('.catsco-annotation-overlay')).toHaveLength(1);
    f.w.history.pushState({}, '', '/new/');
    expect(f.messages('catsco.gateway.annotation.page.v1')).toHaveLength(1);
  });

  it('explicit create BEFORE connect wins and shares singleton', () => {
    const f = fixture();
    const sdk = f.create({ revision: 'r7', getElementId: () => 'app-id' });
    expect(f.create()).toBe(sdk);
    f.connect();
    expect(f.messages(READY)).toHaveLength(1);
    expect(f.messages(READY)[0].message.page.revision).toBe('r7');
    f.mode('element');
    f.w.document.getElementById('publish').click();
    expect(f.messages(TARGET)).toHaveLength(1);
    expect(f.messages(TARGET)[0].message.selection.target.element_id).toBe('app-id');
  });

  it('manual instance installed BEFORE gateway injection is adopted, not duplicated', () => {
    const f = fixture({ attribute: null });
    const sdk = f.create({ revision: 'manual-r' });
    f.loadScript(JSON.stringify([ORIGIN]));
    expect(f.create()).toBe(sdk);
    f.connect();
    expect(f.messages(READY)).toHaveLength(1);
    expect(f.messages(READY)[0].message.page.revision).toBe('manual-r');
  });

  it('explicit create AFTER bootstrap upgrades callbacks/revision without reconnect or duplicate targets', () => {
    const f = fixture();
    f.connect();
    const sdk = f.create({ revision: 'r9', getElementId: () => 'custom' });
    expect(f.create()).toBe(sdk);
    expect(f.messages(READY)).toHaveLength(1);
    expect(f.messages('catsco.gateway.annotation.page.v1').at(-1).message.page.revision).toBe('r9');
    f.mode('element');
    f.w.document.getElementById('publish').click();
    expect(f.messages(TARGET)).toHaveLength(1);
    expect(f.messages(TARGET)[0].message.selection.target.element_id).toBe('custom');
  });

  it('pins the first allowed parent even with multiple allowed origins; duplicate config cannot widen trust', () => {
    const f = fixture({ attribute: JSON.stringify([ORIGIN, OTHER]) });
    f.connect();
    expect(f.create({ parentOrigin: OTHER })).toBeNull();
    f.connect('session-2', 'request-2', { origin: OTHER });
    f.loadScript(JSON.stringify(['https://evil.example']));
    f.connect('evil', 'req', { origin: 'https://evil.example' });
    expect(f.messages(READY)).toHaveLength(1);
    f.connect('session-2', 'request-2');
    expect(f.messages(READY)).toHaveLength(2);
  });

  it('dispose removes automatic/manual listeners and history patch, without silent resurrection', () => {
    const f = fixture();
    const nativePush = f.w.history.pushState;
    f.connect();
    const sdk = f.create();
    f.mode('element');
    sdk.dispose();
    expect(f.w.history.pushState).toBe(nativePush);
    f.connect('disposed', 'req');
    f.loadScript();
    f.connect('duplicate-after-dispose', 'req');
    expect(f.messages(READY)).toHaveLength(1);
    expect(f.w.document.querySelector('.catsco-annotation-overlay')).toBeNull();
    const next = f.create();
    expect(next).not.toBe(sdk);
    f.connect('new', 'new');
    expect(f.messages(READY)).toHaveLength(2);
  });

  it('global dispose cancels pending bootstrap', () => {
    const f = fixture();
    f.w.CatsCoAnnotations.dispose();
    f.loadScript();
    f.connect();
    expect(f.messages(READY)).toHaveLength(0);
  });

  it('ready/target only report bridge data; never echo extra host secrets or route identities', () => {
    const f = fixture();
    f.dispatch({ type: CONNECT, contract_version: BRIDGE, session_id: 's', request_id: 'r',
      open_ref: 'aob_private', jwt: 'private-jwt', secret: 'private-secret', topic_id: 'topic-a', agent_uid: 42 });
    f.mode('element', 's');
    f.w.document.getElementById('publish').click();
    const wire = JSON.stringify(f.posted);
    for (const forbidden of ['aob_private', 'private-jwt', 'private-secret', 'topic-a', 'agent_uid', 'open_ref']) expect(wire).not.toContain(forbidden);
    expect(f.messages(TARGET)).toHaveLength(1);
  });
});

describe('automatic runtime stale document defenses and real host calls', () => {
  it('first automatic connect -> select click/drag -> host bbox validation -> Escape parent mode callback', () => {
    const f = fixture();
    const callbacks = { onReady: vi.fn(), onSelection: vi.fn(), onModeChange: vi.fn() };
    const receiver = { postMessage(message, targetOrigin) { expect(targetOrigin).toBe(APP); f.dispatch(message); } };
    const host = createGatewayAnnotationHost({
      getBinding: () => ({ frame: { contentWindow: receiver }, url: `${APP}/board/`, agentUid: 9, appId: 'board' }),
      ...callbacks,
    });
    f.parent.postMessage.mockImplementation((message, origin) => {
      expect(origin).toBe(ORIGIN);
      host.handleWindowMessage({ data: JSON.parse(JSON.stringify(message)), origin: APP, source: receiver });
    });
    try {
      host.connect(); host.setMode('select');
      f.loadScript(); // duplicate external SDK remains the same singleton
      const button = f.w.document.getElementById('publish');
      button.getBoundingClientRect = () => ({ left: 100, top: 100, width: 120, height: 40 });
      const action = vi.fn(); button.addEventListener('click', action);
      const mouse = (type, x, y) => button.dispatchEvent(new f.w.MouseEvent(type, {
        bubbles: true, cancelable: true, button: 0, clientX: x, clientY: y,
      }));
      mouse('mousedown', 110, 110); mouse('mouseup', 110, 110); mouse('click', 110, 110);
      mouse('mousedown', 100, 100); mouse('mousemove', 120, 120); mouse('mouseup', 300, 400); mouse('click', 300, 400);
      expect(callbacks.onReady).toHaveBeenCalledTimes(1);
      expect(callbacks.onSelection.mock.calls.map(([selection]) => selection.kind)).toEqual(['element', 'region']);
      expect(callbacks.onSelection.mock.calls[1][0].target.rect).toMatchObject({
        x: 100 / f.w.innerWidth, y: 100 / f.w.innerHeight, width: 200 / f.w.innerWidth, height: 300 / f.w.innerHeight,
      });
      expect(action).not.toHaveBeenCalled();
      f.w.document.dispatchEvent(new f.w.KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }));
      expect(callbacks.onModeChange).toHaveBeenCalledExactlyOnceWith('off');
      expect(f.create().mode()).toBe('off');
      mouse('mousedown', 110, 110); mouse('mouseup', 110, 110); mouse('click', 110, 110);
      expect(callbacks.onSelection).toHaveBeenCalledTimes(2);
      expect(action).toHaveBeenCalledTimes(1);
    } finally { host.dispose(); }
  });

  it('new session clears real text selection and returns to off; malformed connect leaves live session intact', () => {
    const f = fixture();
    f.connect();
    f.mode('text');
    const range = f.w.document.createRange();
    range.selectNodeContents(f.w.document.getElementById('text'));
    f.w.getSelection().addRange(range);
    f.connect('session-2', 'request-2');
    expect(f.w.getSelection().rangeCount).toBe(0);
    const sdk = f.create();
    expect(sdk.mode()).toBe('off');
    f.mode('text', 'session-2');
    f.connect('bad', '');
    f.mode('region', 'session-2');
    expect(sdk.mode()).toBe('region');
    f.mode('element', 'session-1');
    expect(sdk.mode()).toBe('region');
  });

  it('mode transitions and Escape clear stale real text selections', () => {
    const f = fixture();
    f.connect();
    f.mode('text');
    const select = () => {
      const range = f.w.document.createRange();
      range.selectNodeContents(f.w.document.getElementById('text'));
      f.w.getSelection().addRange(range);
    };
    select();
    f.mode('off');
    expect(f.w.getSelection().rangeCount).toBe(0);
    f.mode('text');
    select();
    f.w.document.dispatchEvent(new f.w.KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    expect(f.w.getSelection().rangeCount).toBe(0);
    f.w.document.body.dispatchEvent(new f.w.MouseEvent('mouseup', { bubbles: true }));
    expect(f.messages(TARGET)).toHaveLength(0);
  });

  it('new session/revision/pagehide cancel old region drag and pagehide drops old session', () => {
    const f = fixture();
    const mouse = (type, x, y) => f.w.document.body.dispatchEvent(new f.w.MouseEvent(type, {
      bubbles: true, cancelable: true, button: 0, clientX: x, clientY: y,
    }));
    f.connect();
    const sdk = f.create({ revision: 'r1' });
    for (const invalidate of [() => f.connect('session-2', 'request-2'), () => sdk.setRevision('r2'),
      () => f.w.dispatchEvent(new f.w.Event('pagehide'))]) {
      f.mode('region', f.messages(READY).at(-1).message.session_id);
      mouse('mousedown', 100, 100);
      invalidate();
      mouse('mouseup', 300, 300);
      expect(f.messages(TARGET)).toHaveLength(0);
    }
    f.mode('element', 'session-2');
    f.w.document.getElementById('publish').click();
    expect(f.messages(TARGET)).toHaveLength(0);
  });

  it('actual host.connect first-call -> automatic ready -> modes -> element/text/region and reconnect', () => {
    const f = fixture();
    const callbacks = { onReady: vi.fn(), onSelection: vi.fn(), onUnavailable: vi.fn() };
    const receiver = { postMessage(message, targetOrigin) {
      expect(targetOrigin).toBe(APP);
      f.dispatch(message);
    } };
    const host = createGatewayAnnotationHost({
      getBinding: () => ({ frame: { contentWindow: receiver }, url: `${APP}/board/`, agentUid: 9, appId: 'board' }),
      ...callbacks,
    });
    // Serialize across the two JS realms, as structured clone would do.
    f.parent.postMessage.mockImplementation((message, origin) => {
      expect(origin).toBe(ORIGIN);
      host.handleWindowMessage({ data: JSON.parse(JSON.stringify(message)), origin: APP, source: receiver });
    });
    try {
      expect(host.connect()).toBe(true);
      expect(callbacks.onReady).toHaveBeenCalledTimes(1);
      expect(host.setMode('element')).toBe(true);
      f.w.document.getElementById('publish').click();
      expect(host.setMode('text')).toBe(true);
      const range = f.w.document.createRange();
      range.selectNodeContents(f.w.document.getElementById('text'));
      f.w.getSelection().addRange(range);
      f.w.document.body.dispatchEvent(new f.w.MouseEvent('mouseup', { bubbles: true }));
      expect(host.setMode('region')).toBe(true);
      for (const [type, x, y] of [['mousedown', 100, 100], ['mouseup', 300, 300]]) {
        f.w.document.body.dispatchEvent(new f.w.MouseEvent(type, { bubbles: true, cancelable: true, button: 0, clientX: x, clientY: y }));
      }
      expect(callbacks.onSelection.mock.calls.map(([selection]) => selection.kind)).toEqual(['element', 'text', 'region']);
      const oldSession = host.sessionToken;
      expect(host.connect()).toBe(true);
      expect(host.sessionToken).not.toBe(oldSession);
      expect(callbacks.onReady).toHaveBeenCalledTimes(2);
      expect(f.create().mode()).toBe('off');
    } finally { host.dispose(); }
  });
});
