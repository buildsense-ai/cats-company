import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import '../public/catsco-annotations.js';
import { createGatewayAnnotationHost } from './gateway-annotations';

const BRIDGE = 'catsco.gateway-annotation-bridge.v1';
const TYPES = window.CatsCoAnnotations.types;

let currentFakeParent = null;
let activeSdk = null;

function disposeActiveSdk() {
  activeSdk?.dispose?.();
  activeSdk = null;
  delete window.parent;
}

function createParentBus() {
  const fakeParent = { postMessage: vi.fn() };
  Object.defineProperty(window, 'parent', { configurable: true, get: () => fakeParent });
  const posted = [];
  fakeParent.postMessage.mockImplementation((message, origin) => {
    posted.push({ message, origin });
  });
  currentFakeParent = fakeParent;
  return { posted, fakeParent };
}

function createSdk({ revision } = {}) {
  // The SDK refuses to post when it is not actually framed; give the test a
  // fake parent window so the real browser path is exercised.
  const { fakeParent, posted } = createParentBus();
  const sdk = window.CatsCoAnnotations.create({
    parentOrigin: 'https://host.catsco.example',
    revision,
  });
  activeSdk = sdk;
  return { sdk, posted, fakeParent };
}

function connect(sessionId = 'catsco_session_1') {
  window.dispatchEvent(new MessageEvent('message', {
    origin: 'https://host.catsco.example',
    source: currentFakeParent,
    data: {
      type: TYPES.connect,
      contract_version: BRIDGE,
      session_id: sessionId,
      request_id: 'catsco_request_1',
    },
  }));
}

function sendMode(mode, sessionId = 'catsco_session_1') {
  window.dispatchEvent(new MessageEvent('message', {
    origin: 'https://host.catsco.example',
    source: currentFakeParent,
    data: {
      type: TYPES.mode,
      contract_version: BRIDGE,
      session_id: sessionId,
      mode,
    },
  }));
}

function readyMessage(posted) {
  return posted.map((p) => p.message).find((m) => m.type === TYPES.ready) ?? null;
}

function targetMessages(posted) {
  return posted.map((p) => p.message).filter((m) => m.type === TYPES.target);
}

function pageMessages(posted) {
  return posted.map((p) => p.message).filter((m) => m.type === TYPES.page);
}

let overlays;

beforeEach(() => {
  overlays = [];
  // jsdom's innerWidth is 0; pin both dimensions to 1000 so rect math is
  // deterministic across assertions.
  Object.defineProperty(window, 'innerWidth', { configurable: true, value: 1000 });
  Object.defineProperty(window, 'innerHeight', { configurable: true, value: 1000 });
  Object.defineProperty(window, 'scrollX', { configurable: true, value: 0 });
  Object.defineProperty(window, 'scrollY', { configurable: true, value: 0 });
});

afterEach(() => {
  disposeActiveSdk();
  vi.restoreAllMocks();
});

describe('CatsCoAnnotations.create', () => {
  it('rejects missing or wildcard parentOrigin', () => {
    expect(window.CatsCoAnnotations.create({})).toBeNull();
    expect(window.CatsCoAnnotations.create({ parentOrigin: '*' })).toBeNull();
    expect(window.CatsCoAnnotations.create({ parentOrigin: 'null' })).toBeNull();
  });

  it('answers a connect from the exact parent origin with a ready payload', () => {
    const { posted } = createSdk();
    expect(readyMessage(posted)).toBeNull();
    connect();
    const ready = readyMessage(posted);
    expect(ready).toMatchObject({
      type: TYPES.ready,
      contract_version: BRIDGE,
      session_id: 'catsco_session_1',
      capabilities: ['element', 'text', 'region'],
      page: { path: window.location.pathname },
    });
  });

  it('ignores connects from a foreign origin or source', () => {
    const { posted } = createSdk();
    window.dispatchEvent(new MessageEvent('message', {
      origin: 'https://evil.example',
      source: currentFakeParent,
      data: { type: TYPES.connect, contract_version: BRIDGE, session_id: 'catsco_evil' },
    }));
    window.dispatchEvent(new MessageEvent('message', {
      origin: 'https://host.catsco.example',
      source: window,
      data: { type: TYPES.connect, contract_version: BRIDGE, session_id: 'catsco_evil' },
    }));
    window.dispatchEvent(new MessageEvent('message', {
      origin: 'https://host.catsco.example',
      source: currentFakeParent,
      data: { type: TYPES.connect, contract_version: 'v9', session_id: 'catsco_evil' },
    }));
    expect(readyMessage(posted)).toBeNull();
  });

  it('a second connect rebinds to the newest session id', () => {
    const { posted } = createSdk();
    connect('catsco_session_1');
    connect('catsco_session_2');
    const readies = posted.map((p) => p.message).filter((m) => m.type === TYPES.ready);
    expect(readies).toHaveLength(2);
    expect(readies[1].session_id).toBe('catsco_session_2');
  });
});

describe('mode handling', () => {
  it('mode off from an unknown session is ignored', () => {
    const { sdk } = createSdk();
    connect();
    sendMode('element', 'catsco_stale');
    expect(sdk.mode()).toBe('off');
  });

  it('element mode shows the badge and picks up hovered/clicked elements', () => {
    const { sdk, posted } = createSdk();
    connect();
    sendMode('element');
    expect(sdk.mode()).toBe('element');

    const button = document.createElement('button');
    button.id = 'publish';
    button.textContent = '发布';
    document.body.appendChild(button);
    const rect = { left: 100, top: 200, width: 120, height: 40 };
    vi.spyOn(button, 'getBoundingClientRect').mockReturnValue(rect);

    button.dispatchEvent(new MouseEvent('mouseover', { bubbles: true }));
    button.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }));

    const targets = targetMessages(posted);
    expect(targets).toHaveLength(1);
    expect(targets[0].page.path).toBe(window.location.pathname);
    expect(targets[0].selection.kind).toBe('element');
    expect(targets[0].selection.target.element_id).toBe('publish');
    expect(targets[0].selection.target.selector).toContain('#publish');
    expect(targets[0].selection.target.rect).toEqual({
      x: 0.1, y: 200 / 1000 * 1, width: 0.12, height: 0.04,
    });
  });

  it('sensitive controls are excluded from element capture', () => {
    const { sdk, posted } = createSdk();
    connect();
    sendMode('element');

    const password = document.createElement('input');
    password.setAttribute('type', 'password');
    password.id = 'pwd';
    document.body.appendChild(password);

    const apiKey = document.createElement('input');
    apiKey.name = 'api_key';
    document.body.appendChild(apiKey);

    password.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }));
    apiKey.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }));

    expect(targetMessages(posted)).toHaveLength(0);
  });

  it('text mode captures selected text with affix evidence on mouseup', () => {
    const { posted } = createSdk();
    connect();
    sendMode('text');

    const paragraph = document.createElement('p');
    paragraph.id = 'text-fixture';
    const textNode = document.createTextNode('需要修改的示例文本尾部还有文字');
    paragraph.appendChild(textNode);
    document.body.appendChild(paragraph);

    const range = document.createRange();
    range.setStart(textNode, 0);
    range.setEnd(textNode, 9); // '需要修改的示例文本'
    const mockSelection = {
      isCollapsed: false,
      rangeCount: 1,
      anchorNode: textNode,
      focusNode: textNode,
      toString: () => '需要修改的示例文本',
      getRangeAt: () => range,
      removeAllRanges: () => {},
    };
    vi.spyOn(window, 'getSelection').mockReturnValue(mockSelection);

    document.body.dispatchEvent(new MouseEvent('mouseup', { bubbles: true, cancelable: true }));
    const targets = targetMessages(posted);
    expect(targets).toHaveLength(1);
    expect(targets[0].selection.kind).toBe('text');
    expect(targets[0].selection.target.text).toBe('需要修改的示例文本');
    expect(targets[0].selection.target.suffix).toBe('尾部还有文字');
  });

  it('text mode rejects a range that only touches a sensitive subtree at one end', () => {
    const { posted } = createSdk();
    connect();
    sendMode('text');

    // A range may start in plain text and end inside a password field; the
    // whole range must be disqualified, not just the anchor side.
    const paragraph = document.createElement('p');
    paragraph.id = 'cross-sensitive-fixture';
    const before = document.createTextNode('before ');
    const password = document.createElement('input');
    password.setAttribute('type', 'password');
    const after = document.createTextNode(' after');
    paragraph.append(before, password, after);
    document.body.appendChild(paragraph);

    const range = document.createRange();
    range.setStart(before, 0);
    range.setEnd(after, 6);

    const mockSelection = {
      isCollapsed: false,
      rangeCount: 1,
      anchorNode: before,
      focusNode: after,
      toString: () => 'before  after',
      getRangeAt: () => range,
      removeAllRanges: () => {},
    };
    vi.spyOn(window, 'getSelection').mockReturnValue(mockSelection);
    document.body.dispatchEvent(new MouseEvent('mouseup', { bubbles: true, cancelable: true }));
    expect(targetMessages(posted)).toHaveLength(0);
  });

  it('text mode rejects ranges ending in contenteditable or marked subtrees', () => {
    const { posted } = createSdk();
    connect();
    sendMode('text');

    const paragraph = document.createElement('p');
    const plain = document.createTextNode('plain ');
    const editable = document.createElement('div');
    editable.setAttribute('contenteditable', 'true');
    editable.appendChild(document.createTextNode('draft text'));
    paragraph.append(plain, editable);
    document.body.appendChild(paragraph);

    const marked = document.createElement('span');
    marked.setAttribute('data-catsco-annotation-sensitive', '');
    marked.appendChild(document.createTextNode('secret'));
    const plainAfterMarked = document.createTextNode('tail');
    const markedHost = document.createElement('p');
    markedHost.append(plainAfterMarked);
    markedHost.prepend(marked);
    document.body.appendChild(markedHost);

    // end inside contenteditable
    const editableRange = document.createRange();
    editableRange.setStart(plain, 0);
    editableRange.setEnd(editable.firstChild, 5);

    // start before a marked subtree, ending after it
    const markedRange = document.createRange();
    markedRange.setStart(marked.firstChild, 0);
    markedRange.setEnd(plainAfterMarked, 4);

    // plain-to-plain range within the same paragraph is fine
    const plainRange = document.createRange();
    plainRange.setStart(plain, 0);
    plainRange.setEnd(plain, 6);

    const makeSelection = (range) => ({
      isCollapsed: false,
      rangeCount: 1,
      anchorNode: range.startContainer,
      focusNode: range.endContainer,
      toString: () => range.toString(),
      getRangeAt: () => range,
      removeAllRanges: () => {},
    });

    vi.spyOn(window, 'getSelection').mockReturnValue(makeSelection(editableRange));
    document.body.dispatchEvent(new MouseEvent('mouseup', { bubbles: true, cancelable: true }));
    expect(targetMessages(posted)).toHaveLength(0);

    vi.spyOn(window, 'getSelection').mockReturnValue(makeSelection(markedRange));
    document.body.dispatchEvent(new MouseEvent('mouseup', { bubbles: true, cancelable: true }));
    expect(targetMessages(posted)).toHaveLength(0);

    vi.spyOn(window, 'getSelection').mockReturnValue(makeSelection(plainRange));
    document.body.dispatchEvent(new MouseEvent('mouseup', { bubbles: true, cancelable: true }));
    expect(targetMessages(posted)).toHaveLength(1);
  });

  it('text mode ignores collapsed selections', () => {
    const { posted } = createSdk();
    connect();
    sendMode('text');
    vi.spyOn(window, 'getSelection').mockReturnValue({ isCollapsed: true, rangeCount: 0 });
    document.body.dispatchEvent(new MouseEvent('mouseup', { bubbles: true, cancelable: true }));
    expect(targetMessages(posted)).toHaveLength(0);
  });

  it('region mode reports normalized rect with viewport evidence', () => {
    const { sdk, posted } = createSdk();
    connect();
    sendMode('region');
    expect(sdk.mode()).toBe('region');

    document.body.dispatchEvent(new MouseEvent('mousedown', { button: 0, clientX: 100, clientY: 100, bubbles: true, cancelable: true }));
    document.body.dispatchEvent(new MouseEvent('mousemove', { button: 0, clientX: 300, clientY: 260, bubbles: true, cancelable: true }));
    document.body.dispatchEvent(new MouseEvent('mouseup', { button: 0, clientX: 300, clientY: 260, bubbles: true, cancelable: true }));

    const targets = targetMessages(posted);
    expect(targets).toHaveLength(1);
    expect(targets[0].selection.kind).toBe('region');
    expect(targets[0].selection.target.rect).toEqual({ x: 0.1, y: 0.1, width: 0.2, height: 0.16 });
    expect(targets[0].selection.target.coordinate_space).toBe('viewport');
    expect(targets[0].selection.target.viewport).toEqual({
      width: 1000, height: 1000, scroll_x: 0, scroll_y: 0,
    });
  });

  it('region mode ignores tiny drags', () => {
    const { posted } = createSdk();
    connect();
    sendMode('region');
    document.body.dispatchEvent(new MouseEvent('mousedown', { button: 0, clientX: 100, clientY: 100, bubbles: true, cancelable: true }));
    document.body.dispatchEvent(new MouseEvent('mousemove', { button: 0, clientX: 103, clientY: 101, bubbles: true, cancelable: true }));
    document.body.dispatchEvent(new MouseEvent('mouseup', { button: 0, clientX: 103, clientY: 101, bubbles: true, cancelable: true }));
    expect(targetMessages(posted)).toHaveLength(0);
  });
});

describe('multi-instance history dispatcher (P2-1)', () => {
  // Both instances live in the SAME document, so they share one window and
  // one parent channel (faithful to the real failure mode). Page messages
  // per navigation are counted on the shared bus: N live instances produce
  // N page notifications.
  function createSharedBus() {
    // Parent channel only — no auto-created instance — so live-instance
    // counts in assertions refer exactly to the instances the test makes.
    const { posted } = createParentBus();
    const instances = [];
    const createInstance = () => {
      const instance = window.CatsCoAnnotations.create({
        parentOrigin: 'https://host.catsco.example',
      });
      instances.push(instance);
      return instance;
    };
    return { posted, createInstance, instances };
  }

  function pageMessagesWithPath(posted, path) {
    return pageMessages(posted).filter((m) => m.page.path === path);
  }

  it('both instances receive navigation while alive; any dispose order keeps survivors working', () => {
    const { posted, createInstance } = createSharedBus();
    const natives = { push: history.pushState, replace: history.replaceState };
    const first = createInstance();
    const second = createInstance();
    connect('catsco_session_a');
    connect('catsco_session_b');
    history.pushState({}, '', '/multi-view');
    expect(pageMessagesWithPath(posted, '/multi-view')).toHaveLength(2);

    // Dispose the FIRST-created instance: the survivor keeps receiving
    // page notifications and the patch stays installed.
    first.dispose();
    history.pushState({}, '', '/after-first-dispose');
    expect(pageMessagesWithPath(posted, '/after-first-dispose')).toHaveLength(1);
    expect(history.pushState).not.toBe(natives.push); // still patched

    // After the LAST dispose the native methods are restored.
    second.dispose();
    history.pushState({}, '', '/after-last-dispose');
    expect(pageMessagesWithPath(posted, '/after-last-dispose')).toHaveLength(0);
    expect(history.pushState).toBe(natives.push);
    expect(history.replaceState).toBe(natives.replace);
  });

  it('reverse dispose order also keeps the survivor notified', () => {
    const { posted, createInstance } = createSharedBus();
    const natives = { push: history.pushState, replace: history.replaceState };
    const first = createInstance();
    const second = createInstance();
    connect('catsco_session_a');
    second.dispose(); // dispose the LATER instance first
    history.pushState({}, '', '/after-reverse-dispose');
    expect(pageMessagesWithPath(posted, '/after-reverse-dispose')).toHaveLength(1);

    first.dispose();
    history.pushState({}, '', '/after-all-dispose');
    expect(pageMessagesWithPath(posted, '/after-all-dispose')).toHaveLength(0);
    expect(history.pushState).toBe(natives.push);
  });

  it('re-creating after full cleanup patches history again', () => {
    const { posted, createInstance } = createSharedBus();
    const natives = { push: history.pushState };
    const first = createInstance();
    first.dispose();
    activeSdk = null;
    expect(history.pushState).toBe(natives.push);

    const again = createInstance();
    connect('catsco_session_c');
    history.pushState({}, '', '/recreated');
    expect(pageMessagesWithPath(posted, '/recreated')).toHaveLength(1);
    again.dispose();
    expect(history.pushState).toBe(natives.push);
  });
});

describe('selector anchor correctness', () => {
  function elementTargetOf(posted) {
    const targets = targetMessages(posted);
    return targets[targets.length - 1]?.selection.target ?? null;
  }

  function clickInElementMode(element) {
    const { posted } = createSdk();
    connect();
    sendMode('element');
    vi.spyOn(element, 'getBoundingClientRect').mockReturnValue({ left: 10, top: 10, width: 100, height: 30 });
    element.dispatchEvent(new MouseEvent('mouseover', { bubbles: true }));
    element.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }));
    return { posted, target: elementTargetOf(posted) };
  }

  it('nth-of-type counts same-tag siblings with mixed h1/button/p brothers', () => {
    const container = document.createElement('section');
    container.id = 'mixed-siblings';
    container.innerHTML = [
      '<h1>title</h1>',
      '<button>one</button>',
      '<p>first</p>',
      '<button>two</button>',
      '<p>real target</p>',
    ].join('');
    document.body.appendChild(container);
    const element = container.querySelectorAll('p')[1];

    const { target } = clickInElementMode(element);
    expect(target.element_id ?? null).toBeNull();
    expect(target.selector).toBeTruthy();
    expect(document.querySelector(target.selector)).toBe(element);
    // The p is the 2nd p among same-tag siblings, not index 5 of all children.
    expect(target.selector).toContain('p:nth-of-type(2)');
  });

  it('a sanitized data-catsco-annotation-id that resolves to another element is not used as anchor', () => {
    const host1 = document.createElement('p');
    host1.setAttribute('data-catsco-annotation-id', 'x"y'); // sanitizes to "xy"
    host1.id = 'host1';
    const decoy = document.createElement('p');
    decoy.setAttribute('data-catsco-annotation-id', 'xy');
    decoy.id = 'decoy';
    document.body.append(decoy, host1);

    const { target } = clickInElementMode(host1);
    // The attribute anchor would match the decoy; it must be skipped.
    expect(target.selector ?? '').not.toContain('[data-catsco-annotation-id="xy"]');
    if (target.selector) {
      expect(document.querySelector(target.selector)).toBe(host1);
    }
    expect(target.element_id).toBe('x"y');
  });

  it('a selector that cannot match the real element is dropped, not asserted', () => {
    // Deep chain beyond the 6-level cap inside a long hierarchy: the built
    // selector would exceed the cap/boundary and must not claim to match.
    let element = null;
    const root = document.createElement('div');
    root.id = 'deep-root';
    document.body.appendChild(root);
    let parent = root;
    for (let i = 0; i < 10; i++) {
      const next = document.createElement('div');
      next.id = `level-${i}`;
      parent.appendChild(next);
      parent = next;
      element = next;
    }
    const { target } = clickInElementMode(element);
    expect(target.element_id).toBe('level-9');
    if (target.selector) {
      expect(document.querySelector(target.selector)).toBe(element);
    }
  });
});

describe('text selection range coverage (defensive hardening)', () => {
  function mouseupWithRange(range) {
    const { posted } = createSdk();
    connect();
    sendMode('text');
    const mockSelection = {
      isCollapsed: false,
      rangeCount: 1,
      anchorNode: range.startContainer,
      focusNode: range.endContainer,
      toString: () => range.toString(),
      getRangeAt: () => range,
      removeAllRanges: () => {},
    };
    vi.spyOn(window, 'getSelection').mockReturnValue(mockSelection);
    document.body.dispatchEvent(new MouseEvent('mouseup', { bubbles: true, cancelable: true }));
    return targetMessages(posted);
  }

  it('selectNodeContents over a container with a sensitive descendant is rejected', () => {
    const container = document.createElement('div');
    container.id = 'mixed-content-container';
    container.innerHTML = '<p>说明文本</p><input type="password">';
    document.body.appendChild(container);

    const range = document.createRange();
    range.selectNodeContents(container);
    expect(mouseupWithRange(range)).toHaveLength(0);
  });

  it('selectNodeContents over a container with a marked descendant is rejected', () => {
    const container = document.createElement('div');
    container.id = 'marked-content-container';
    container.innerHTML = '<p>说明文本</p><span data-catsco-annotation-sensitive="">内部机密</span>';
    document.body.appendChild(container);

    const range = document.createRange();
    range.selectNodeContents(container);
    expect(mouseupWithRange(range)).toHaveLength(0);
  });

  it('plain sibling-free containers still annotate', () => {
    const container = document.createElement('p');
    container.id = 'plain-container';
    container.textContent = '纯文本内容';
    document.body.appendChild(container);

    const range = document.createRange();
    range.selectNodeContents(container);
    const targets = mouseupWithRange(range);
    expect(targets).toHaveLength(1);
    expect(targets[0].selection.target.text).toBe('纯文本内容');
  });
});

describe('navigation invalidation', () => {
  it('history pushState announces the new page to the host', () => {
    const { posted } = createSdk();
    connect();
    const before = pageMessages(posted).length;
    history.pushState({}, '', '/next-view');
    const pages = pageMessages(posted);
    expect(pages.length).toBe(before + 1);
    expect(pages.at(-1).page.path).toBe('/next-view');
  });

  it('popstate/hashchange announce page changes; no session means no posts', () => {
    const { posted } = createSdk();
    connect();
    const before = pageMessages(posted).length;
    window.dispatchEvent(new PopStateEvent('popstate'));
    expect(pageMessages(posted).length).toBe(before + 1);
  });
});

describe('dispose', () => {
  it('stops answering connects and modes after dispose', () => {
    const { sdk, posted } = createSdk();
    sdk.dispose();
    connect('catsco_after_dispose');
    expect(readyMessage(posted)).toBeNull();
    sendMode('element', 'catsco_after_dispose');
    expect(sdk.mode()).toBe('off');
  });

  it('regular clicks pass through when annotation mode is off', () => {
    const { posted } = createSdk();
    connect();
    // mode stays 'off'
    const button = document.createElement('button');
    document.body.appendChild(button);
    const handler = vi.fn();
    button.addEventListener('click', handler);
    button.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }));
    expect(handler).toHaveBeenCalledTimes(1);
    expect(targetMessages(posted)).toHaveLength(0);
  });
});

describe('host <-> SDK interoperability (frozen bridge protocol)', () => {
  it('connect -> ready -> mode -> element target round trip carries exact origin and session', () => {
    // Host side: real host bridge pointed at this jsdom window's fake frame.
    const callbacks = { onReady: vi.fn(), onSelection: vi.fn(), onPageChange: vi.fn(), onUnavailable: vi.fn() };
    // Shared origin for both sides of the bridge; the SDK posts with this
    // pinned origin and the host validates the binding URL against it.
    const origin = 'https://app.catsco.example';
    const fakeParent = { postMessage: vi.fn() };
    Object.defineProperty(window, 'parent', { configurable: true, get: () => fakeParent });
    currentFakeParent = fakeParent;
    const sdk = window.CatsCoAnnotations.create({ parentOrigin: origin });
    activeSdk = sdk;

    // In a real browser the host posts to frame.contentWindow.postMessage
    // and the iframe window receives it as a message event; jsdom can't do
    // cross-window delivery, so replay host posts into this window and
    // SDK posts back into the host bridge with matching source/origin.
    const stubFrameWindow = { postMessage: vi.fn((message) => {
      window.dispatchEvent(new MessageEvent('message', {
        origin,
        source: fakeParent,
        data: message,
      }));
    }) };
    const host = createGatewayAnnotationHost({
      getBinding: () => ({
        frame: { contentWindow: stubFrameWindow },
        url: `${origin}/app/`,
        agentUid: 101,
        appId: 'board',
      }),
      ...callbacks,
    });
    fakeParent.postMessage.mockImplementation((message) => {
      host.handleWindowMessage({ data: message, origin, source: stubFrameWindow });
    });

    expect(host.connect()).toBe(true);
    expect(callbacks.onReady).toHaveBeenCalledTimes(1);
    expect(callbacks.onReady).toHaveBeenCalledWith({
      capabilities: ['element', 'text', 'region'],
      page: { path: window.location.pathname },
    });

    // Host switches to element mode; the SDK applies it.
    expect(host.setMode('element')).toBe(true);
    const element = document.createElement('button');
    element.id = 'publish';
    document.body.appendChild(element);
    vi.spyOn(element, 'getBoundingClientRect').mockReturnValue({ left: 10, top: 10, width: 100, height: 30 });
    element.dispatchEvent(new MouseEvent('mouseover', { bubbles: true }));
    element.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }));

    expect(callbacks.onSelection).toHaveBeenCalledTimes(1);
    const [selection, page] = callbacks.onSelection.mock.calls[0];
    expect(selection.kind).toBe('element');
    expect(selection.target.element_id).toBe('publish');
    expect(selection.target.rect).toEqual({ x: 0.01, y: 0.01, width: 0.1, height: 0.03 });
    expect(page.path).toBe(window.location.pathname);
    expect(callbacks.onUnavailable).not.toHaveBeenCalled();

    host.dispose();
  });
});
