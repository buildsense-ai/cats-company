/*
 * CatsCo gateway annotation SDK.
 *
 * Loaded by a gateway application page served inside the CatsCo host iframe:
 *
 *   <script src="/catsco-annotations.js"></script>
 *   <script>
 *     window.CatsCoAnnotations.create({ parentOrigin: 'https://app.catsco.cc' });
 *   </script>
 *
 * The SDK never talks to the network, never reads input values, and never
 * sends chat messages. It only reports what the user explicitly selected
 * (element / text / region) to the host over exact-origin postMessage, and
 * renders selection affordances while an explicit annotation mode is on.
 */
(function initCatsCoAnnotations() {
  'use strict';

  var BRIDGE_CONTRACT = 'catsco.gateway-annotation-bridge.v1';
  var TYPE_CONNECT = 'catsco.gateway.annotation.connect.v1';
  var TYPE_READY = 'catsco.gateway.annotation.ready.v1';
  var TYPE_MODE = 'catsco.gateway.annotation.mode.v1';
  var TYPE_TARGET = 'catsco.gateway.annotation.target.v1';
  var TYPE_PAGE = 'catsco.gateway.annotation.page.v1';

  var CAPABILITIES = ['element', 'text', 'region'];
  var MAX_ID_CHARS = 128;
  var MAX_LABEL_CHARS = 256;
  var MAX_TEXT_CHARS = 2000;
  var MAX_AFFIX_CHARS = 256;
  var MAX_SELECTOR_CHARS = 512;
  var MAX_PATH_CHARS = 1024;
  var CONTROL_FORBIDDEN = /[\u0000-\u001f\u007f]/;
  var SENSITIVE_NAME = /pass(word|wd)?|pwd|token|secret|credential|api[-_]?key|auth|card|cvv|cvc|otp|captcha/i;
  var SENSITIVE_INPUT_TYPES = {
    password: true, hidden: true, email: true, tel: true, number: true,
    search: true, file: true, month: true, week: true, time: true,
    date: true, 'datetime-local': true,
  };
  var MIN_REGION_PX = 6; // a drag shorter than this is a click, not a region

  function isPlainObject(value) {
    return !!value && typeof value === 'object' && !Array.isArray(value)
      && (Object.getPrototypeOf(value) === Object.prototype || Object.getPrototypeOf(value) === null);
  }

  function boundedText(value, max) {
    if (typeof value !== 'string') return '';
    var text = value.length > max ? value.slice(0, max) : value;
    return CONTROL_FORBIDDEN.test(text.replace(/[\r\n\t]/g, '')) ? '' : text;
  }

  function safeId(value) {
    var id = typeof value === 'string' ? value.slice(0, MAX_ID_CHARS) : '';
    return id && !CONTROL_FORBIDDEN.test(id) ? id : null;
  }

  function clamp01(value) {
    if (!Number.isFinite(value)) return 0;
    return Math.min(1, Math.max(0, value));
  }

  function normalizedViewportRect(domRect) {
    var width = window.innerWidth || 1;
    var height = window.innerHeight || 1;
    return {
      x: clamp01(domRect.left / width),
      y: clamp01(domRect.top / height),
      width: clamp01(domRect.width / width),
      height: clamp01(domRect.height / height),
    };
  }

  function viewportEvidence() {
    return {
      width: window.innerWidth,
      height: window.innerHeight,
      scroll_x: Math.max(0, Math.round(window.scrollX || 0)),
      scroll_y: Math.max(0, Math.round(window.scrollY || 0)),
    };
  }

  function currentPage(revision) {
    var path = window.location && typeof window.location.pathname === 'string'
      ? window.location.pathname : '/';
    if (!path.startsWith('/') || path.length > MAX_PATH_CHARS || CONTROL_FORBIDDEN.test(path)) return null;
    var page = { path: path };
    var safeRevision = safeId(revision || '');
    if (safeRevision) page.revision = safeRevision;
    return page;
  }

  function isSensitiveControl(element) {
    if (!element || element.nodeType !== 1) return false;
    var tag = element.tagName;
    if (tag === 'INPUT') {
      var type = (element.getAttribute('type') || 'text').toLowerCase();
      if (SENSITIVE_INPUT_TYPES[type]) return true;
      var identity = (element.name || '') + ' ' + (element.id || '') + ' ' + (element.getAttribute('autocomplete') || '');
      return SENSITIVE_NAME.test(identity);
    }
    if (tag === 'TEXTAREA' || tag === 'SELECT') {
      var identity2 = (element.name || '') + ' ' + (element.id || '');
      return SENSITIVE_NAME.test(identity2);
    }
    // Contenteditable regions may hold drafts, secrets, or compose boxes;
    // treat them as sensitive unless the app explicitly opts in.
    if (element.isContentEditable) return true;
    return false;
  }

  function isSensitiveSubtreeRoot(element) {
    // Ignore selection targets that live inside password fields, hidden
    // inputs, or elements explicitly marked by the application.
    for (var node = element; node && node.nodeType === 1; node = node.parentElement) {
      if (node.hasAttribute && node.hasAttribute('data-catsco-annotation-sensitive')) return true;
      if (isSensitiveControl(node)) return true;
    }
    return false;
  }

  function cssEscapeFragment(value) {
    // Only plain attribute-name fragments end up in selectors; keep them
    // bounded and free of quotes/backslashes so injection cannot occur.
    return String(value).replace(/[^A-Za-z0-9_.-]/g, '').slice(0, 64);
  }

  function selectorFor(element) {
    if (!element || element.nodeType !== 1 || element === document.documentElement) return null;
    var segments = [];
    var node = element;
    var depth = 0;
    while (node && node.nodeType === 1 && node !== document.body && node !== document.documentElement && depth < 6) {
      var segment = node.tagName.toLowerCase();
      var elementId = node.id ? cssEscapeFragment(node.id) : '';
      if (elementId && document.getElementById(elementId) === node) {
        segments.unshift('#' + elementId);
        break;
      }
      var annotationId = node.getAttribute && node.getAttribute('data-catsco-annotation-id');
      if (annotationId && safeId(annotationId)) {
        segments.unshift('[data-catsco-annotation-id="' + cssEscapeFragment(annotationId) + '"]');
        break;
      }
      var parent = node.parentElement;
      if (parent) {
        var sameTag = parent.children ? Array.prototype.indexOf.call(parent.children, node) : -1;
        if (sameTag >= 0) segment += ':nth-of-type(' + (sameTag + 1) + ')';
      }
      segments.unshift(segment);
      node = parent;
      depth += 1;
    }
    if (!segments.length) return null;
    var selector = segments.join(' > ');
    return selector.length <= MAX_SELECTOR_CHARS ? selector : selector.slice(0, MAX_SELECTOR_CHARS);
  }

  function elementLabel(element) {
    if (!element || element.nodeType !== 1) return '';
    var tag = element.tagName.toLowerCase();
    var descriptor = element.id ? tag + '#' + cssEscapeFragment(element.id) : tag;
    if (descriptor.length > MAX_LABEL_CHARS) descriptor = descriptor.slice(0, MAX_LABEL_CHARS);
    return descriptor;
  }

  function textLabel(text) {
    var firstLine = String(text).split('\n')[0].trim();
    return firstLine.length > 80 ? firstLine.slice(0, 77) + '…' : firstLine;
  }

  function regionLabel(rect) {
    var percent = function (v) { return Math.round((clamp01(v) * 100)); };
    return '区域 ' + percent(rect.x + rect.width / 2) + '%, ' + percent(rect.y + rect.height / 2) + '%';
  }

  function extractTextTarget(selection) {
    var text = boundedText(selection.toString(), MAX_TEXT_CHARS);
    if (!text || !/\S/.test(text) || isSensitiveSubtreeRoot(selection.anchorNode && selection.anchorNode.parentElement)) return null;
    var target = { text: text };
    try {
      var range = selection.getRangeAt(0);
      var startContainer = range.startContainer;
      var endContainer = range.endContainer;
      if (startContainer && startContainer.nodeType === 3) {
        var prefix = startContainer.nodeValue.slice(Math.max(0, range.startOffset - MAX_AFFIX_CHARS), range.startOffset);
        if (prefix) target.prefix = boundedText(prefix, MAX_AFFIX_CHARS);
      }
      if (endContainer && endContainer.nodeType === 3) {
        var suffix = endContainer.nodeValue.slice(range.endOffset, range.endOffset + MAX_AFFIX_CHARS);
        if (suffix) target.suffix = boundedText(suffix, MAX_AFFIX_CHARS);
      }
      var rect = range.getBoundingClientRect();
      if (rect && rect.width > 0 && rect.height > 0) {
        target.rect = normalizedViewportRect(rect);
        target.coordinate_space = 'viewport';
        target.viewport = viewportEvidence();
      }
    } catch (error) {
      // Selection evidence is best-effort; text alone already anchors it.
    }
    return target;
  }

  function ensureStyleContainer() {
    var style = document.getElementById('catsco-annotation-style');
    if (style) return style;
    style = document.createElement('style');
    style.id = 'catsco-annotation-style';
    style.textContent = [
      '.catsco-annotation-overlay{position:fixed;inset:0;z-index:2147483646;pointer-events:none;}',
      '.catsco-annotation-highlight{position:fixed;pointer-events:none;border:2px solid #6366f1;',
      'background:rgba(99,102,241,0.15);border-radius:3px;z-index:2147483647;}',
      '.catsco-annotation-region{position:fixed;pointer-events:none;border:2px dashed #6366f1;',
      'background:rgba(99,102,241,0.12);z-index:2147483647;}',
      '.catsco-annotation-badge{position:fixed;top:8px;left:50%;transform:translateX(-50%);',
      'padding:4px 12px;border-radius:999px;background:#312e81;color:#fff;font:12px/1.6 sans-serif;',
      'z-index:2147483647;pointer-events:none;}',
    ].join('');
    (document.head || document.documentElement).appendChild(style);
    return style;
  }

  function createOverlay() {
    ensureStyleContainer();
    var overlay = document.createElement('div');
    overlay.className = 'catsco-annotation-overlay';
    var highlight = document.createElement('div');
    highlight.className = 'catsco-annotation-highlight';
    highlight.style.display = 'none';
    var badge = document.createElement('div');
    badge.className = 'catsco-annotation-badge';
    overlay.appendChild(highlight);
    document.body.appendChild(overlay);
    document.body.appendChild(badge);
    return {
      highlight: highlight,
      badge: badge,
      overlay: overlay,
      setHighlight(rect) {
        if (!rect) {
          highlight.style.display = 'none';
          return;
        }
        highlight.style.display = 'block';
        highlight.style.left = rect.left + 'px';
        highlight.style.top = rect.top + 'px';
        highlight.style.width = rect.width + 'px';
        highlight.style.height = rect.height + 'px';
      },
      setBadge(text) {
        if (!text) {
          badge.style.display = 'none';
          return;
        }
        badge.style.display = 'block';
        badge.textContent = text;
      },
      remove() {
        overlay.remove();
        badge.remove();
      },
    };
  }

  function createInstance(config) {
    var parentOrigin = typeof config.parentOrigin === 'string' ? config.parentOrigin : '';
    if (!parentOrigin || parentOrigin === '*' || parentOrigin === 'null') {
      // Refuse wildcard origins: the host is pinned to one exact origin.
      if (typeof console !== 'undefined' && console.warn) {
        console.warn('[CatsCoAnnotations] create() requires an exact parentOrigin string.');
      }
      return null;
    }
    var customGetElementId = typeof config.getElementId === 'function' ? config.getElementId : null;
    var revision = config.revision;

    var state = {
      disposed: false,
      session: null, // opaque host-minted token from connect.v1
      mode: 'off',
      page: currentPage(revision),
      overlay: null,
      regionDrag: null,
      lastElement: null,
    };

    function post(message) {
      if (state.disposed || !window.parent || window.parent === window) return false;
      try {
        window.parent.postMessage(message, parentOrigin);
        return true;
      } catch (error) {
        return false;
      }
    }

    function sessionId() { return state.session; }

    function sendReady(session) {
      post({
        type: TYPE_READY,
        contract_version: BRIDGE_CONTRACT,
        session_id: session,
        capabilities: CAPABILITIES,
        page: currentPage(revision) || { path: '/' },
      });
    }

    function sendSelection(selection) {
      var page = currentPage(revision);
      if (!state.session || !page) return false;
      return post({
        type: TYPE_TARGET,
        contract_version: BRIDGE_CONTRACT,
        session_id: state.session,
        page: page,
        selection: selection,
      });
    }

    function sendPageChanged() {
      if (!state.session) return;
      var page = currentPage(revision);
      if (!page) return;
      post({
        type: TYPE_PAGE,
        contract_version: BRIDGE_CONTRACT,
        session_id: state.session,
        page: page,
      });
    }

    function elementTarget(element) {
      var target = {};
      var elementId = null;
      if (customGetElementId) {
        try {
          elementId = safeId(customGetElementId(element));
        } catch (error) {
          elementId = null;
        }
      }
      if (!elementId) {
        elementId = safeId(element.getAttribute && element.getAttribute('data-catsco-annotation-id'))
          ?? safeId(element.id);
      }
      if (elementId) target.element_id = elementId;
      var selector = selectorFor(element);
      if (selector) target.selector = selector;
      var rect = element.getBoundingClientRect();
      if (rect && rect.width > 0 && rect.height > 0) {
        target.rect = normalizedViewportRect(rect);
        target.coordinate_space = 'viewport';
        target.viewport = viewportEvidence();
      }
      if (!target.element_id && !target.selector) return null;
      return target;
    }

    function emitElement(element) {
      if (isSensitiveSubtreeRoot(element)) return false;
      var target = elementTarget(element);
      if (!target) return false;
      return sendSelection({
        id: 'anno-' + Date.now().toString(36) + '-' + Math.random().toString(36).slice(2, 8),
        kind: 'element',
        label: elementLabel(element),
        target: target,
      });
    }

    function emitText() {
      var selection = window.getSelection && window.getSelection();
      if (!selection || selection.isCollapsed || selection.rangeCount === 0) return false;
      var target = extractTextTarget(selection);
      if (!target) return false;
      var ok = sendSelection({
        id: 'anno-' + Date.now().toString(36) + '-' + Math.random().toString(36).slice(2, 8),
        kind: 'text',
        label: textLabel(target.text),
        target: target,
      });
      if (ok && selection.removeAllRanges) selection.removeAllRanges();
      return ok;
    }

    function emitRegion(rect) {
      return sendSelection({
        id: 'anno-' + Date.now().toString(36) + '-' + Math.random().toString(36).slice(2, 8),
        kind: 'region',
        label: regionLabel(rect),
        target: {
          rect: rect,
          coordinate_space: 'viewport',
          viewport: viewportEvidence(),
        },
      });
    }

    var modeHints = {
      element: '元素标注：点击要标注的元素',
      text: '文本标注：选中要标注的文本',
      region: '区域标注：拖拽框选一个区域',
      off: '',
    };

    function activeOverlay() {
      if (!state.overlay) state.overlay = createOverlay();
      return state.overlay;
    }

    function teardownOverlay() {
      if (state.overlay) {
        state.overlay.remove();
        state.overlay = null;
      }
      state.lastElement = null;
      state.regionDrag = null;
    }

    function handleMode(mode) {
      state.mode = mode;
      teardownOverlay();
      if (mode !== 'off') {
        activeOverlay().setBadge(modeHints[mode] || '');
      }
    }

    function onHover(event) {
      if (state.mode !== 'element') return;
      var element = event.target;
      if (!(element instanceof Element) || isSensitiveSubtreeRoot(element)) {
        state.overlay.setHighlight(null);
        state.lastElement = null;
        return;
      }
      state.lastElement = element;
      state.overlay.setHighlight(element.getBoundingClientRect());
    }

    function onClick(event) {
      if (state.mode !== 'element') return;
      event.preventDefault();
      event.stopPropagation();
      if (!event.target || !(event.target instanceof Element)) return;
      if (isSensitiveSubtreeRoot(event.target)) {
        state.overlay.setBadge('敏感控件不可标注');
        return;
      }
      emitElement(event.target);
    }

    function onMouseUpText(event) {
      if (state.mode !== 'text') return;
      var selection = window.getSelection && window.getSelection();
      if (!selection || selection.isCollapsed || selection.rangeCount === 0) return;
      if (isSensitiveSubtreeRoot(selection.anchorNode && selection.anchorNode.parentElement)) return;
      emitText();
    }

    function onRegionStart(event) {
      if (state.mode !== 'region' || event.button !== 0) return;
      event.preventDefault();
      event.stopPropagation();
      state.regionDrag = { startX: event.clientX, startY: event.clientY };
      state.overlay.setBadge('区域标注：拖拽框选一个区域');
    }

    function onRegionMove(event) {
      if (state.mode !== 'region' || !state.regionDrag) return;
      event.preventDefault();
      event.stopPropagation();
      state.regionDrag.currentX = event.clientX;
      state.regionDrag.currentY = event.clientY;
      var width = window.innerWidth || 1;
      var height = window.innerHeight || 1;
      var left = Math.min(state.regionDrag.startX, state.regionDrag.currentX);
      var top = Math.min(state.regionDrag.startY, state.regionDrag.currentY);
      var rectWidth = Math.abs(state.regionDrag.currentX - state.regionDrag.startX);
      var rectHeight = Math.abs(state.regionDrag.currentY - state.regionDrag.startY);
      state.overlay.setHighlight({
        left: left,
        top: top,
        width: rectWidth,
        height: rectHeight,
      });
    }

    function onRegionEnd(event) {
      if (state.mode !== 'region' || !state.regionDrag) return;
      event.preventDefault();
      event.stopPropagation();
      var width = window.innerWidth || 1;
      var height = window.innerHeight || 1;
      var left = Math.min(state.regionDrag.startX, state.regionDrag.currentX ?? state.regionDrag.startX);
      var top = Math.min(state.regionDrag.startY, state.regionDrag.currentY ?? state.regionDrag.startY);
      var rectWidth = Math.abs((state.regionDrag.currentX ?? state.regionDrag.startX) - state.regionDrag.startX);
      var rectHeight = Math.abs((state.regionDrag.currentY ?? state.regionDrag.startY) - state.regionDrag.startY);
      state.regionDrag = null;
      state.overlay.setHighlight(null);
      if (rectWidth < MIN_REGION_PX || rectHeight < MIN_REGION_PX) {
        state.overlay.setBadge('拖拽范围太小，请框选一个更大的区域');
        return;
      }
      emitRegion({
        x: clamp01(left / width),
        y: clamp01(top / height),
        width: clamp01(rectWidth / width),
        height: clamp01(rectHeight / height),
      });
    }

    function onKeydown(event) {
      if (event.key !== 'Escape') return;
      // Escape inside the frame pauses affordances visually; the host owns
      // real mode switches (mode.v1) so nothing crosses the wire here.
      teardownOverlay();
      if (state.mode !== 'off') activeOverlay().setBadge(modeHints[state.mode] || '');
    }

    function onUrlChanged() {
      // SPA navigations inside the app invalidate the previous document:
      // any hover/drag state is dropped and the host learns the new page.
      teardownOverlay();
      if (state.mode !== 'off') activeOverlay().setBadge(modeHints[state.mode] || '');
      sendPageChanged();
    }

    function onMessage(event) {
      if (state.disposed) return;
      if (event.origin !== parentOrigin) return;
      if (event.source !== window.parent) return;
      var payload = event.data;
      if (!isPlainObject(payload)) return;
      if (payload.contract_version !== BRIDGE_CONTRACT) return;
      if (payload.type === TYPE_CONNECT) {
        // A connect re-binds the frame to a fresh session; older state dies.
        state.session = safeId(payload.session_id);
        if (state.session) {
          sendReady(state.session);
          handleMode(state.mode === 'off' ? 'off' : state.mode);
        }
        return;
      }
      if (!state.session || payload.session_id !== state.session) return;
      if (payload.type === TYPE_MODE) {
        if (typeof payload.mode === 'string' && modeHints[payload.mode] !== undefined) {
          handleMode(payload.mode);
        }
      }
    }

    // Listen to history pushState/replaceState, popstate and hashchange so
    // SPA routing still announces page changes.
    var originalPushState = history.pushState;
    var originalReplaceState = history.replaceState;
    history.pushState = function patchedPushState() {
      var result = originalPushState.apply(this, arguments);
      onUrlChanged();
      return result;
    };
    history.replaceState = function patchedReplaceState() {
      var result = originalReplaceState.apply(this, arguments);
      onUrlChanged();
      return result;
    };

    document.addEventListener('mouseover', onHover, true);
    document.addEventListener('click', onClick, true);
    document.addEventListener('mousedown', onRegionStart, true);
    document.addEventListener('mousemove', onRegionMove, true);
    document.addEventListener('mouseup', onRegionEnd, true);
    document.addEventListener('mouseup', onMouseUpText);
    document.addEventListener('keydown', onKeydown);
    window.addEventListener('message', onMessage);
    window.addEventListener('popstate', onUrlChanged);
    window.addEventListener('hashchange', onUrlChanged);
    window.addEventListener('pagehide', teardownOverlay);

    return {
      setRevision(nextRevision) {
        revision = nextRevision;
        sendPageChanged();
      },
      mode() { return state.mode; },
      dispose() {
        if (state.disposed) return;
        state.disposed = true;
        teardownOverlay();
        document.removeEventListener('mouseover', onHover, true);
        document.removeEventListener('click', onClick, true);
        document.removeEventListener('mousedown', onRegionStart, true);
        document.removeEventListener('mousemove', onRegionMove, true);
        document.removeEventListener('mouseup', onRegionEnd, true);
        document.removeEventListener('mouseup', onMouseUpText);
        document.removeEventListener('keydown', onKeydown);
        window.removeEventListener('message', onMessage);
        window.removeEventListener('popstate', onUrlChanged);
        window.removeEventListener('hashchange', onUrlChanged);
        window.removeEventListener('pagehide', teardownOverlay);
        history.pushState = originalPushState;
        history.replaceState = originalReplaceState;
        state.session = null;
      },
    };
  }

  window.CatsCoAnnotations = {
    create: createInstance,
    bridgeContract: BRIDGE_CONTRACT,
    types: {
      connect: TYPE_CONNECT,
      ready: TYPE_READY,
      mode: TYPE_MODE,
      target: TYPE_TARGET,
      page: TYPE_PAGE,
    },
  };
})();
