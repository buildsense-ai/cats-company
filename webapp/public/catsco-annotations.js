/*
 * CatsCo gateway annotation SDK.
 *
 * Gateway injects a same-origin external script (no inline config/secrets):
 *   <script src="/_catsco/runtime/annotations-v1.js"
 *     data-catsco-parent-origins='["https://app.catsco.cc"]'></script>
 * It waits for the first allowlisted window.parent connect. Advanced apps
 * may still call create({ parentOrigin, revision, getElementId }); injected
 * automatic/explicit use shares one instance. Plain manual use stays compatible.
 *
 * The SDK never talks to the network, never reads input values, and never
 * sends chat messages. It only reports what the user explicitly selected
 * (element / text / region) to the host over exact-origin postMessage, and
 * renders selection affordances while an explicit annotation mode is on.
 */
(function initCatsCoAnnotations() {
  'use strict';

  var runtimeScript = document.currentScript;
  var originsAttribute = runtimeScript && runtimeScript.getAttribute('data-catsco-parent-origins');
  // Repeated head/body injection must not install another module/dispatcher.
  if (window.CatsCoAnnotations && window.CatsCoAnnotations.runtimeVersion === 'annotations-v1') {
    window.CatsCoAnnotations.bootstrapAttribute(originsAttribute);
    return;
  }

  var BRIDGE_CONTRACT = 'catsco.gateway-annotation-bridge.v1';
  var TYPE_CONNECT = 'catsco.gateway.annotation.connect.v1';
  var TYPE_READY = 'catsco.gateway.annotation.ready.v1';
  var TYPE_MODE = 'catsco.gateway.annotation.mode.v1';
  var TYPE_TARGET = 'catsco.gateway.annotation.target.v1';
  var TYPE_PAGE = 'catsco.gateway.annotation.page.v1';
  var TYPE_SCREENSHOT_REQUEST = 'catsco.gateway.annotation.screenshot.request.v1';
  var TYPE_SCREENSHOT_RESULT = 'catsco.gateway.annotation.screenshot.result.v1';
  var TYPE_SCREENSHOT_CANCEL = 'catsco.gateway.annotation.screenshot.cancel.v1';

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

  function exactOrigin(value) {
    if (typeof value !== 'string' || !value || value.length > 2048) return null;
    try {
      var url = new URL(value);
      return (url.protocol === 'https:' || url.protocol === 'http:') && url.origin === value ? value : null;
    } catch (error) {
      return null;
    }
  }

  function handshakeId(value) {
    return typeof value === 'string' && value.length > 0 && value.length <= MAX_ID_CHARS
      && !CONTROL_FORBIDDEN.test(value) ? value : null;
  }

  function validConnect(payload) {
    return isPlainObject(payload) && payload.contract_version === BRIDGE_CONTRACT
      && payload.type === TYPE_CONNECT && handshakeId(payload.session_id) && handshakeId(payload.request_id);
  }

  var liveInstances = new Map();
  var automaticOrigins = null; // immutable once configured; never infer from the frame URL/referrer
  var singleton = null;
  var bootstrapListening = false;
  var bootstrapStopped = false; // dispose is permanent until an explicit create (or new document)

  function stopBootstrap() {
    window.removeEventListener('message', onBootstrapConnect);
    bootstrapListening = false;
  }

  function onBootstrapConnect(event) {
    if (!automaticOrigins || window.parent === window || event.source !== window.parent
      || automaticOrigins.indexOf(event.origin) < 0 || !validConnect(event.data)) return;
    var instance = createConfigured({ parentOrigin: event.origin });
    if (instance) {
      // The new listener was not present at the start of this dispatch. Do
      // not swallow the host's only connect or depend on a second retry.
      liveInstances.get(instance).onMessage(event);
    }
  }

  function bootstrapAttribute(attribute) {
    if (typeof attribute !== 'string' || attribute.length > 16384) return false;
    var origins;
    try { origins = JSON.parse(attribute); } catch (error) { return false; }
    if (!Array.isArray(origins) || !origins.length || origins.length > 32
      || origins.some(function (origin) { return !exactOrigin(origin); })) return false;
    origins = Array.from(new Set(origins)).sort();
    if (automaticOrigins && JSON.stringify(origins) !== JSON.stringify(automaticOrigins)) return false;
    if (!automaticOrigins) {
      // A manually installed instance wins over a later gateway script.
      // Ambiguous pre-existing instances cannot safely become a singleton.
      if (liveInstances.size > 1) return false;
      if (liveInstances.size === 1) {
        var entry = Array.from(liveInstances.entries())[0];
        if (origins.indexOf(entry[1].parentOrigin) < 0) return false;
        singleton = entry[0];
      }
      automaticOrigins = origins;
    }
    if (!singleton && !bootstrapListening && !bootstrapStopped && window.parent !== window) {
      window.addEventListener('message', onBootstrapConnect);
      bootstrapListening = true;
    }
    return true;
  }

  function createConfigured(config) {
    if (!isPlainObject(config) || !exactOrigin(config.parentOrigin)) return null;
    if (automaticOrigins) {
      if (automaticOrigins.indexOf(config.parentOrigin) < 0) return null;
      if (singleton) {
        var controller = liveInstances.get(singleton);
        if (controller.parentOrigin !== config.parentOrigin) return null;
        controller.configure(config);
        return singleton;
      }
      stopBootstrap();
      singleton = createInstance(config);
      return singleton;
    }
    return createInstance(config);
  }

  var RENDERER_URL = '/_catsco/runtime/html2canvas-pro-1.6.7.min.js';
  var RENDERER_INTEGRITY = 'sha384-CqHBfwlOY3BunFNI9xxzy+h/+/df5g0tI05vSbd8kIwTTPk23b5jYDpyrlxfukc+';
  var rendererPromise = null;
  var verifiedRenderer = null;

  function loadRenderer() {
    // Always load the pinned self-hosted bundle with SRI, even when the app
    // already defines window.html2canvas: an arbitrary app-provided renderer
    // is not the pinned 1.6.7 build and must not be trusted for evidence.
    if (!rendererPromise) {
      rendererPromise = new Promise(function (resolve, reject) {
        var script = document.createElement('script');
        script.id = 'catsco-annotation-renderer';
        script.src = new URL(RENDERER_URL, window.location.origin).href;
        script.integrity = RENDERER_INTEGRITY;
        script.crossOrigin = 'anonymous';
        // The self-hosted bundle overwrites window.html2canvas. Remember what
        // the application had so its own behaviour is restored afterwards.
        var hadGlobal = 'html2canvas' in window;
        var previousGlobal = window.html2canvas;
        function restoreAppGlobal() {
          if (hadGlobal) window.html2canvas = previousGlobal;
          else { try { delete window.html2canvas; } catch (error) { window.html2canvas = undefined; } }
        }
        var timer = setTimeout(function () { script.remove(); restoreAppGlobal(); reject(new Error('renderer-unavailable')); }, 10000);
        script.onload = function () {
          clearTimeout(timer);
          // The 1.6.7 browser bundle unwraps its UMD default export back to
          // window.html2canvas. Freeze that final, SRI-verified function.
          if (typeof window.html2canvas !== 'function') {
            script.remove(); restoreAppGlobal(); reject(new Error('renderer-unavailable')); return;
          }
          verifiedRenderer = window.html2canvas;
          restoreAppGlobal();
          resolve();
        };
        script.onerror = function () { clearTimeout(timer); script.remove(); restoreAppGlobal(); reject(new Error('renderer-unavailable')); };
        (document.head || document.documentElement).appendChild(script);
      }).catch(function (error) { rendererPromise = null; throw error; });
    }
    return rendererPromise;
  }

  function runtimeOverlay(element) {
    return element && element.nodeType === 1 && (element.id === 'catsco-annotation-style' || element.id === 'catsco-annotation-renderer'
      || (element.classList && (element.classList.contains('catsco-annotation-overlay')
        || element.classList.contains('catsco-annotation-badge'))));
  }

  function screenshotEnvironment() {
    return { width: window.innerWidth, height: window.innerHeight, scrollX: window.scrollX || 0,
      scrollY: window.scrollY || 0, dpr: window.devicePixelRatio || 1 };
  }

  function sameEnvironment(a, b) {
    return a.width === b.width && a.height === b.height && a.scrollX === b.scrollX
      && a.scrollY === b.scrollY && a.dpr === b.dpr;
  }

  function screenshotRisks() {
    var warnings = new Set();
    var nodes = Array.from(document.querySelectorAll('*'));
    // The renderer now clones open shadow roots. Count those nodes too.
    // Preserved roots are masked as a whole; flattened clones use ordinary
    // control/subtree masking. Stop at the same 10,000-node budget.
    for (var i = 0; i < nodes.length; i++) {
      if (nodes.length > 10000) throw new Error('page-too-large');
      if (nodes[i].shadowRoot) {
        var children = nodes[i].shadowRoot.querySelectorAll('*');
        if (nodes.length + children.length > 10000) throw new Error('page-too-large');
        Array.prototype.push.apply(nodes, children);
      }
    }
    function external(value) {
      try {
        var url = new URL(value, window.location.href);
        return !['data:', 'blob:'].includes(url.protocol) && url.origin !== window.location.origin;
      } catch (error) { return true; }
    }
    Array.from(nodes).forEach(function (node) {
      if (runtimeOverlay(node) || !normalizedViewportRect(node.getBoundingClientRect())) return;
      if (node.shadowRoot) {
        warnings.add('embedded-content'); warnings.add('sensitive-content-masked'); return;
      }
      if (isSensitiveSubtreeRoot(node) || ['INPUT', 'TEXTAREA', 'SELECT'].includes(node.tagName)) {
        warnings.add('sensitive-content-masked'); return;
      }
      var style = window.getComputedStyle(node);
      if (style.display === 'none' || style.visibility === 'hidden' || style.opacity === '0') return;
      if (node.tagName === 'IMG') {
        if (external(node.currentSrc || node.src)) warnings.add('cross-origin-image');
        if (!node.complete || !node.naturalWidth) warnings.add('unloaded-image');
      }
      if (node.tagName === 'VIDEO') warnings.add('video');
      if (node.tagName === 'IFRAME' || node.tagName === 'OBJECT' || node.tagName === 'EMBED') warnings.add('embedded-content');
      if (node.tagName === 'CANVAS') {
        try {
          node.toDataURL();
          if (!node.getContext('2d')) warnings.add('webgl-canvas');
        } catch (error) { warnings.add('unreadable-canvas'); }
      }
      var background = style.backgroundImage || '';
      var matches = background.match(/url\([^)]+\)/g) || [];
      matches.forEach(function (match) {
        if (external(match.slice(4, -1).replace(/^['"]|['"]$/g, ''))) warnings.add('cross-origin-background');
      });
    });
    return Array.from(warnings);
  }

  function maskScreenshotClone(clone) {
    // Snapshot every box BEFORE mutating the clone. Keep subtree content in
    // its layout (never textContent=''): auto-height blocks/textarea must not
    // collapse and move the selected target. html2canvas skips opacity-zero
    // roots entirely, including nested media and pseudo-elements.
    var nodes = Array.from(clone.querySelectorAll('*'));
    var masks = nodes.filter(function (node) {
      // A cloned shadow host hides the whole boundary, including sensitive
      // controls that document.querySelectorAll cannot see.
      return node.shadowRoot || isSensitiveSubtreeRoot(node) || ['INPUT', 'TEXTAREA', 'SELECT'].includes(node.tagName);
    }).map(function (node) { return { node: node, rect: node.getBoundingClientRect() }; });
    nodes.forEach(function (node) { if (runtimeOverlay(node)) node.remove(); });
    masks.forEach(function (entry) {
      var node = entry.node;
      if ('value' in node) node.value = '';
      node.removeAttribute('value'); node.removeAttribute('placeholder');
      if (entry.rect.width > 0 && entry.rect.height > 0) {
        node.style.setProperty('box-sizing', 'border-box', 'important');
        node.style.setProperty('width', entry.rect.width + 'px', 'important');
        node.style.setProperty('height', entry.rect.height + 'px', 'important');
        node.style.setProperty('min-width', entry.rect.width + 'px', 'important');
        node.style.setProperty('max-width', entry.rect.width + 'px', 'important');
        node.style.setProperty('min-height', entry.rect.height + 'px', 'important');
        node.style.setProperty('max-height', entry.rect.height + 'px', 'important');
      }
      node.style.setProperty('overflow', 'hidden', 'important');
      node.style.setProperty('opacity', '0', 'important');
      node.style.setProperty('visibility', 'hidden', 'important');
      node.style.setProperty('background-image', 'none', 'important');
    });
  }

  function imageEvidence(canvas, role) {
    var data = canvas.toDataURL('image/jpeg', 0.85);
    if (!/^data:image\/jpeg;base64,[A-Za-z0-9+/]+={0,2}$/.test(data)) throw new Error('encode-failed');
    var encoded = data.slice(23);
    var bytes = encoded.length * 3 / 4 - (encoded.endsWith('==') ? 2 : encoded.endsWith('=') ? 1 : 0);
    if (bytes > 2 * 1024 * 1024) throw new Error('image-too-large');
    return { role: role, mime_type: 'image/jpeg', data_url: data, width: canvas.width, height: canvas.height };
  }

  function screenshotPair(bitmap, rect, env) {
    var full = document.createElement('canvas');
    full.width = bitmap.width; full.height = bitmap.height;
    var context = full.getContext('2d');
    if (!context) throw new Error('canvas-unavailable');
    context.drawImage(bitmap, 0, 0);
    context.strokeStyle = '#ef4444'; context.lineWidth = Math.max(1, 2 * bitmap.width / env.width);
    var x = rect.x * bitmap.width, y = rect.y * bitmap.height;
    var width = rect.width * bitmap.width, height = rect.height * bitmap.height;
    var inset = context.lineWidth / 2;
    context.strokeRect(x + inset, y + inset, Math.max(0, width - 2 * inset), Math.max(0, height - 2 * inset));
    var crop = document.createElement('canvas');
    var left = Math.max(0, Math.floor(x - 16 * bitmap.width / env.width));
    var top = Math.max(0, Math.floor(y - 16 * bitmap.height / env.height));
    var right = Math.min(bitmap.width, Math.ceil(x + width + 16 * bitmap.width / env.width));
    var bottom = Math.min(bitmap.height, Math.ceil(y + height + 16 * bitmap.height / env.height));
    crop.width = Math.max(1, right - left); crop.height = Math.max(1, bottom - top);
    var cropContext = crop.getContext('2d');
    if (!cropContext) throw new Error('canvas-unavailable');
    // Same frozen bitmap, not two asynchronous renderings; only full has box.
    cropContext.drawImage(bitmap, left, top, crop.width, crop.height, 0, 0, crop.width, crop.height);
    return [imageEvidence(full, 'full'), imageEvidence(crop, 'crop')];
  }

  function clamp01(value) {
    if (!Number.isFinite(value)) return 0;
    return Math.min(1, Math.max(0, value));
  }

  function normalizedViewportRect(domRect) {
    var width = window.innerWidth || 1;
    var height = window.innerHeight || 1;
    // Intersect the DOM rect with the viewport FIRST, then derive the
    // normalized geometry: a partially visible element (e.g. left=900 in a
    // 1000px viewport with width=200) must yield x+w ≤ 1 instead of
    // reporting an out-of-viewport area the host schema would reject. A
    // rect with no visible area returns null so the caller can omit the
    // auxiliary evidence and keep the element/text anchor.
    var left = Number.isFinite(domRect.left) ? domRect.left : 0;
    var top = Number.isFinite(domRect.top) ? domRect.top : 0;
    var right = domRect.width === undefined ? 0 : left + domRect.width;
    var bottom = domRect.height === undefined ? 0 : top + domRect.height;
    var clampedLeft = Math.max(0, Math.min(left, width));
    var clampedTop = Math.max(0, Math.min(top, height));
    var clampedRight = Math.max(0, Math.min(right, width));
    var clampedBottom = Math.max(0, Math.min(bottom, height));
    if (clampedRight - clampedLeft <= 0 || clampedBottom - clampedTop <= 0) return null;
    return {
      x: clampedLeft / width,
      y: clampedTop / height,
      width: (clampedRight - clampedLeft) / width,
      height: (clampedBottom - clampedTop) / height,
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
    // treat them as sensitive unless the app explicitly opts in. The DOM
    // property is unreliable across engines (jsdom), so the attribute is
    // checked directly as well.
    if (element.isContentEditable) return true;
    var editableAttr = (element.getAttribute && element.getAttribute('contenteditable') || '').toLowerCase();
    if (editableAttr && editableAttr !== 'false') return true;
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

  function elementOfNode(node) {
    if (!node) return null;
    return node.nodeType === 1 ? node : node.parentElement;
  }

  function nextNodeInDocumentOrder(node, root) {
    if (node.firstChild) return node.firstChild;
    while (node && node !== root) {
      if (node.nextSibling) return node.nextSibling;
      node = node.parentNode;
    }
    return null;
  }

  var SELECTION_WALK_BUDGET = 500;

  // Fail-closed: the range is untrusted, so every element its boundaries
  // actually cover (start/end containers, descendants reached when the
  // containers are elements, and everything in between) must be outside
  // sensitive subtrees. Traversal is bounded; oversized subtrees are treated
  // as sensitive.
  function rangeTouchesSensitiveSubtree(range) {
    if (!range || typeof range.startContainer === 'undefined') return true;
    var startEl = elementOfNode(range.startContainer);
    var endEl = elementOfNode(range.endContainer);
    var commonEl = elementOfNode(range.commonAncestorContainer);
    if (isSensitiveSubtreeRoot(startEl) || isSensitiveSubtreeRoot(endEl) || isSensitiveSubtreeRoot(commonEl)) return true;
    if (!commonEl || typeof range.intersectsNode !== 'function') return true;
    var budget = SELECTION_WALK_BUDGET;
    var walker = document.createTreeWalker(commonEl, NodeFilter.SHOW_ELEMENT);
    while (walker.nextNode()) {
      if (budget-- <= 0) return true; // fail closed on oversized subtrees
      var node = walker.currentNode;
      var covered;
      try {
        covered = range.intersectsNode(node);
      } catch (error) {
        return true;
      }
      if (covered && isSensitiveSubtreeRoot(node)) return true;
    }
    return false;
  }

  function cssEscapeFragment(value) {
    // Only plain attribute-name fragments end up in selectors; keep them
    // bounded and free of quotes/backslashes so injection cannot occur.
    return String(value).replace(/[^A-Za-z0-9_.-]/g, '').slice(0, 64);
  }

  function sameTagOrdinal(node) {
    // :nth-of-type() counts among same-tag siblings, not all children.
    var parent = node.parentElement;
    if (!parent) return -1;
    var tag = node.tagName;
    var ordinal = 0;
    for (var i = 0; i < parent.children.length; i++) {
      if (parent.children[i] === node) return ordinal + 1;
      if (parent.children[i].tagName === tag) ordinal++;
    }
    return -1;
  }

  function selectorMatchesElement(selector, element) {
    if (!selector) return false;
    try {
      return document.querySelector(selector) === element;
    } catch (error) {
      return false;
    }
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
        // The sanitized fragment may coincide with another element's
        // attribute value; only accept the attribute anchor when it still
        // resolves to this exact node.
        var attributeSelector = '[data-catsco-annotation-id="' + cssEscapeFragment(annotationId) + '"]';
        if (selectorMatchesElement(attributeSelector, node)) {
          segments.unshift(attributeSelector);
          break;
        }
      }
      var ordinal = sameTagOrdinal(node);
      if (ordinal > 0) segment += ':nth-of-type(' + ordinal + ')';
      segments.unshift(segment);
      node = node.parentElement;
      depth += 1;
    }
    if (!segments.length) return null;
    var selector = segments.join(' > ');
    if (selector.length > MAX_SELECTOR_CHARS) selector = selector.slice(0, MAX_SELECTOR_CHARS);
    // A selector anchor that cannot actually match the element is worse than
    // none: element_id stays the anchor and the selector is dropped.
    return selectorMatchesElement(selector, element) ? selector : null;
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
    if (!selection || typeof selection.getRangeAt !== 'function' || selection.rangeCount === 0) return null;
    var range;
    try {
      range = selection.getRangeAt(0);
    } catch (error) {
      return null;
    }
    var text = boundedText(selection.toString(), MAX_TEXT_CHARS);
    if (!text || !/\S/.test(text)) return null;
    // The whole range is untrusted: start/end containers, the common
    // ancestor, and every node covered in between must be outside sensitive
    // subtrees (a selection may begin in plain text and end inside a
    // password/email field).
    if (rangeTouchesSensitiveSubtree(range)) return null;
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
        var visibleRangeRect = normalizedViewportRect(rect);
        if (visibleRangeRect) {
          target.rect = visibleRangeRect;
          target.coordinate_space = 'viewport';
          target.viewport = viewportEvidence();
        }
      }
    } catch (error) {
      // Selection evidence is best-effort; text alone already anchors it.
    }
    return target;
  }

  // ---------------------------------------------------------------------
  // Shared navigation dispatcher.
  //
  // history.pushState/replaceState are patched exactly once per document;
  // navigation events fan out to every live instance. Each instance's
  // dispose only removes itself from the set, so any dispose order keeps
  // surviving instances receiving page notifications; the native methods
  // are restored when the last instance goes away.
  var navigationInstances = new Set();
  var patchedHistory = null; // { pushState, replaceState } natives

  function notifyNavigation() {
    Array.from(navigationInstances).forEach(function dispatchToInstance(onUrlChanged) {
      try {
        onUrlChanged();
      } catch (error) {
        // One broken instance must not starve the others.
      }
    });
  }

  function installHistoryPatches() {
    if (navigationInstances.size > 0) return;
    var nativePushState = history.pushState;
    var nativeReplaceState = history.replaceState;
    patchedHistory = { pushState: nativePushState, replaceState: nativeReplaceState };
    history.pushState = function sharedPatchedPushState() {
      var result = nativePushState.apply(this, arguments);
      notifyNavigation();
      return result;
    };
    history.replaceState = function sharedPatchedReplaceState() {
      var result = nativeReplaceState.apply(this, arguments);
      notifyNavigation();
      return result;
    };
  }

  function uninstallHistoryPatches() {
    if (navigationInstances.size > 0 || !patchedHistory) return;
    history.pushState = patchedHistory.pushState;
    history.replaceState = patchedHistory.replaceState;
    patchedHistory = null;
  }

  function registerInstance(onUrlChanged) {
    // Install first: installHistoryPatches only patches when the set is
    // empty, so the first instance must install before being counted.
    installHistoryPatches();
    navigationInstances.add(onUrlChanged);
  }

  function unregisterInstance(onUrlChanged) {
    navigationInstances.delete(onUrlChanged);
    uninstallHistoryPatches();
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
    var parentOrigin = exactOrigin(config.parentOrigin);
    if (!parentOrigin) {
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
      connectRequestId: null, // host-minted handshake id echoed in ready
      mode: 'off',
      page: currentPage(revision),
      overlay: null,
      regionDrag: null,
      lastElement: null,
      selectGesture: null,
      selectionRect: null,
      suppressNextClick: false, // cleared by the trailing click or a fresh mousedown
      screenshotSelection: null,
      screenshotJob: null,
      rendering: false,
      documentEpoch: 0,
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
        request_id: state.connectRequestId,
        capabilities: CAPABILITIES,
        screenshot_supported: true,
        page: currentPage(revision) || { path: '/' },
      });
    }

    function sendSelection(selection) {
      var page = currentPage(revision);
      if (!state.session || !page) return false;
      if (state.mode === 'select') {
        // Inline host comments need a real visible bounding box; never
        // fabricate a corner/point and claim it came from the selected DOM.
        var target = selection.target;
        if (!target.rect || target.coordinate_space !== 'viewport' || !target.viewport) return false;
        showSelectionRect(target.rect);
      }
      cancelScreenshot('selection-changed');
      state.screenshotSelection = {
        id: selection.id, target: selection.target, page: page, environment: screenshotEnvironment(),
        epoch: state.documentEpoch, session: state.session, request: state.connectRequestId,
      };
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

    function cancelScreenshot(reason) {
      var job = state.screenshotJob;
      if (!job) return;
      state.screenshotJob = null;
      job.canceled = true;
      post({ type: TYPE_SCREENSHOT_RESULT, contract_version: BRIDGE_CONTRACT,
        session_id: job.session, request_id: job.request, selection_id: job.selection.id,
        page: job.selection.page, error: { code: reason || 'canceled' } });
    }

    function invalidateScreenshot() {
      state.documentEpoch++;
      state.screenshotSelection = null;
      cancelScreenshot('stale-document');
    }

    function onCaptureEnvironmentChanged() {
      invalidateScreenshot();
      teardownOverlay();
      clearSelection();
      if (state.mode !== 'off') activeOverlay().setBadge(modeHints[state.mode] || '');
      // Same path/revision still invalidates viewport geometry, including
      // an already completed screenshot. Host page handler revokes target.
      sendPageChanged();
    }

    function onScreenshotRequest(payload) {
      var selected = state.screenshotSelection;
      if (!selected || !handshakeId(payload.request_id) || payload.selection_id !== selected.id
        || !isPlainObject(payload.page) || payload.page.path !== selected.page.path
        || (payload.page.revision || '') !== (selected.page.revision || '')) return;
      // A new request never cancels a run that is already inside the
      // renderer: it is refused as busy so the in-flight pair still lands.
      if (state.rendering) {
        post({ type: TYPE_SCREENSHOT_RESULT, contract_version: BRIDGE_CONTRACT,
          session_id: state.session, request_id: payload.request_id, selection_id: selected.id,
          page: selected.page, error: { code: 'capture-busy' } });
        return;
      }
      cancelScreenshot('superseded');
      var job = { session: state.session, request: payload.request_id, selection: selected, canceled: false };
      state.screenshotJob = job;
      function current() {
        var page = currentPage(revision);
        return !state.disposed && !job.canceled && state.screenshotJob === job
          && state.session === job.session && selected === state.screenshotSelection
          && selected.epoch === state.documentEpoch && selected.request === state.connectRequestId
          && page && page.path === selected.page.path && (page.revision || '') === (selected.page.revision || '')
          && sameEnvironment(selected.environment, screenshotEnvironment());
      }
      function fail(code) {
        if (!current()) { if (state.screenshotJob === job) cancelScreenshot('stale-document'); return; }
        state.screenshotJob = null;
        post({ type: TYPE_SCREENSHOT_RESULT, contract_version: BRIDGE_CONTRACT,
          session_id: job.session, request_id: job.request, selection_id: selected.id, page: selected.page,
          error: { code: code } });
      }
      var env = selected.environment;
      var rect = selected.target.rect;
      if (!current()) { cancelScreenshot('stale-document'); return; }
      if (!rect || !Number.isFinite(env.width) || !Number.isFinite(env.height) || env.width < 1 || env.height < 1
        || env.width > 16384 || env.height > 16384) { fail('bad-geometry'); return; }
      var scale = Math.min(Math.max(1, Math.min(env.dpr, 2)), 2048 / Math.max(env.width, env.height));
      var warnings;
      try { warnings = screenshotRisks(); } catch (error) { fail('page-too-large'); return; }
      state.rendering = true;
      loadRenderer().then(function () {
        if (!current()) throw new Error('stale-document');
        var renderer = verifiedRenderer;
        // The verified bundle is called directly; the application global is
        // irrelevant here (and may be absent, which is the normal case).
        if (typeof renderer !== 'function') throw new Error('renderer-unavailable');
        return renderer(document.documentElement, {
          x: env.scrollX, y: env.scrollY, width: env.width, height: env.height,
          windowWidth: env.width, windowHeight: env.height, scrollX: env.scrollX, scrollY: env.scrollY,
          scale: scale, backgroundColor: '#ffffff', allowTaint: false, useCORS: false,
          logging: false, imageTimeout: 5000, removeContainer: true,
          ignoreElements: runtimeOverlay, onclone: maskScreenshotClone,
        });
      }).then(function (bitmap) {
        if (!current()) throw new Error('stale-document');
        var expectedWidth = Math.floor(env.width * scale), expectedHeight = Math.floor(env.height * scale);
        if (!bitmap || bitmap.width !== expectedWidth || bitmap.height !== expectedHeight
          || bitmap.width < 1 || bitmap.height < 1 || bitmap.width > 2048 || bitmap.height > 2048) throw new Error('bad-geometry');
        var screenshots = screenshotPair(bitmap, rect, env);
        if (!current()) throw new Error('stale-document');
        state.screenshotJob = null;
        post({ type: TYPE_SCREENSHOT_RESULT, contract_version: BRIDGE_CONTRACT,
          session_id: job.session, request_id: job.request, selection_id: selected.id, page: selected.page,
          screenshots: screenshots, warnings: warnings });
      }).catch(function (error) {
        var message = error && typeof error.message === 'string' ? error.message : '';
        var allowed = ['renderer-unavailable', 'stale-document', 'bad-geometry', 'image-too-large', 'encode-failed', 'canvas-unavailable'];
        // Send a bounded category, never raw parser messages or CSS values.
        var unsupportedStyle = /^Attempting to parse an unsupported (?:color|image) function\b/.test(message);
        fail(allowed.indexOf(message) >= 0 ? message : unsupportedStyle ? 'unsupported-style' : 'capture-failed');
      }).finally(function () { state.rendering = false; });
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
        var visibleRect = normalizedViewportRect(rect);
        // Rect is auxiliary evidence only: keep it when any part of the
        // element is visible, otherwise send the anchor alone.
        if (visibleRect) {
          target.rect = visibleRect;
          target.coordinate_space = 'viewport';
          target.viewport = viewportEvidence();
        }
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
      select: '点击元素或拖拽框选，然后在旁边填写批注；Esc 退出',
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
      if (state.selectGesture) state.suppressNextClick = true;
      state.selectGesture = null;
      state.selectionRect = null;
    }

    function clearSelection() {
      try {
        var selection = window.getSelection && window.getSelection();
        if (selection && typeof selection.removeAllRanges === 'function') selection.removeAllRanges();
      } catch (error) {
        // Some embedded runtimes do not expose an editable Selection.
      }
    }

    function handleMode(mode) {
      if (state.mode !== mode) { clearSelection(); invalidateScreenshot(); }
      state.mode = mode;
      teardownOverlay();
      if (mode !== 'off') {
        activeOverlay().setBadge(modeHints[mode] || '');
      }
    }

    function showSelectionRect(rect) {
      state.selectionRect = rect;
      activeOverlay().setHighlight({
        left: rect.x * window.innerWidth, top: rect.y * window.innerHeight,
        width: rect.width * window.innerWidth, height: rect.height * window.innerHeight,
      });
    }

    function consumeGestureEvent(event) {
      event.preventDefault();
      event.stopImmediatePropagation();
    }

    function onSelectStart(event) {
      // A new ordinary gesture cannot accidentally inherit a canceled
      // annotation's pending click suppression after mode has turned off.
      state.suppressNextClick = false;
      if (state.mode !== 'select' || event.button !== 0) return;
      consumeGestureEvent(event);
      state.selectionRect = null;
      state.selectGesture = {
        startX: event.clientX, startY: event.clientY, element: event.target,
        session: state.session, request: state.connectRequestId, page: currentPage(revision),
      };
      activeOverlay().setBadge(modeHints.select);
    }

    function onSelectMove(event) {
      if (state.mode !== 'select' || !state.selectGesture) return;
      consumeGestureEvent(event);
      var gesture = state.selectGesture;
      if (Math.max(Math.abs(event.clientX - gesture.startX), Math.abs(event.clientY - gesture.startY)) <= MIN_REGION_PX) return;
      activeOverlay().setHighlight({
        left: Math.min(gesture.startX, event.clientX), top: Math.min(gesture.startY, event.clientY),
        width: Math.abs(event.clientX - gesture.startX), height: Math.abs(event.clientY - gesture.startY),
      });
    }

    function onSelectEnd(event) {
      if (state.mode !== 'select' || event.button !== 0 || !state.selectGesture) return;
      consumeGestureEvent(event);
      var gesture = state.selectGesture;
      state.selectGesture = null;
      state.suppressNextClick = true;
      var page = currentPage(revision);
      if (!page || !gesture.page || gesture.session !== state.session || gesture.request !== state.connectRequestId
        || page.path !== gesture.page.path || (page.revision || '') !== (gesture.page.revision || '')) return;
      // Use the actual release coordinates, not a potentially lagging move.
      var dx = Math.abs(event.clientX - gesture.startX);
      var dy = Math.abs(event.clientY - gesture.startY);
      if (Math.max(dx, dy) > MIN_REGION_PX) {
        var rect = normalizedViewportRect({
          left: Math.min(gesture.startX, event.clientX), top: Math.min(gesture.startY, event.clientY),
          width: dx, height: dy,
        });
        if (rect) emitRegion(rect); // a zero-area drag is not an element click
        return;
      }
      var element = gesture.element;
      if (element instanceof Element && element.isConnected) emitElement(element);
    }

    function onPointerControl(event) {
      // Pointer events precede mouse events in browsers. Block app pointer
      // handlers as well, while mouse events resolve the one gesture.
      if (state.mode === 'select' || (event.type === 'pointerup' && state.suppressNextClick)) {
        event.stopImmediatePropagation();
      }
    }

    function onAuxClick(event) {
      if (state.mode === 'select' || state.suppressNextClick) consumeGestureEvent(event);
    }

    function onHover(event) {
      if (state.mode !== 'element' && state.mode !== 'select') return;
      if (state.mode === 'select' && (state.selectGesture || state.selectionRect)) return;
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
      if (state.suppressNextClick) {
        state.suppressNextClick = false;
        consumeGestureEvent(event);
        return;
      }
      if (state.mode !== 'element' && state.mode !== 'select') return;
      if (state.mode === 'select') consumeGestureEvent(event);
      else {
        event.preventDefault();
        event.stopPropagation();
      }
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
      // The release point is the mouseup event's own coordinates (the last
      // mousemove may lag behind it); clamp both ends into the viewport
      // before the size threshold and the normalized rect.
      var startX = Math.max(0, Math.min(state.regionDrag.startX, width));
      var startY = Math.max(0, Math.min(state.regionDrag.startY, height));
      var endX = Number.isFinite(event.clientX)
        ? Math.max(0, Math.min(event.clientX, width))
        : Math.max(0, Math.min(state.regionDrag.currentX ?? startX, width));
      var endY = Number.isFinite(event.clientY)
        ? Math.max(0, Math.min(event.clientY, height))
        : Math.max(0, Math.min(state.regionDrag.currentY ?? startY, height));
      var left = Math.min(startX, endX);
      var top = Math.min(startY, endY);
      var dragRect = {
        left: left,
        top: top,
        width: Math.abs(endX - startX),
        height: Math.abs(endY - startY),
      };
      state.regionDrag = null;
      state.overlay.setHighlight(null);
      if (dragRect.width < MIN_REGION_PX || dragRect.height < MIN_REGION_PX) {
        state.overlay.setBadge('拖拽范围太小，请框选一个更大的区域');
        return;
      }
      var normalized = normalizedViewportRect(dragRect);
      if (!normalized) {
        // Degenerate after viewport clamping (e.g. drag entirely outside).
        state.overlay.setBadge('拖拽范围太小，请框选一个更大的区域');
        return;
      }
      emitRegion(normalized);
    }

    function onKeydown(event) {
      if (event.key !== 'Escape') return;
      if (state.mode === 'select') {
        consumeGestureEvent(event);
        invalidateScreenshot();
        handleMode('off');
        post({
          type: TYPE_MODE, contract_version: BRIDGE_CONTRACT, session_id: state.session,
          mode: 'off', page: currentPage(revision),
        });
        return;
      }
      // Legacy explicit modes retain their previous Escape behavior.
      teardownOverlay();
      clearSelection();
      if (state.mode !== 'off') activeOverlay().setBadge(modeHints[state.mode] || '');
    }

    function onUrlChanged() {
      invalidateScreenshot();
      // SPA navigations inside the app invalidate the previous document:
      // any hover/drag/DOM selection is dropped and the host learns the new
      // page. A stale selection must not be submitted with a newer revision.
      teardownOverlay();
      clearSelection();
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
        // The host-minted request_id is echoed in ready so the host can
        // bind the handshake reply to the exact connect it sent.
        if (!validConnect(payload)) return;
        var changed = state.session !== payload.session_id || state.connectRequestId !== payload.request_id;
        if (changed) {
          invalidateScreenshot();
          // Reset BEFORE ready: a synchronous test/host can immediately
          // send mode from its ready callback, which must not be overwritten.
          handleMode('off');
          clearSelection();
        }
        state.session = payload.session_id;
        state.connectRequestId = payload.request_id;
        sendReady(state.session);
        return;
      }
      if (!state.session || payload.session_id !== state.session) return;
      if (payload.type === TYPE_SCREENSHOT_REQUEST) { onScreenshotRequest(payload); return; }
      if (payload.type === TYPE_SCREENSHOT_CANCEL) {
        if (state.screenshotJob && state.screenshotJob.request === payload.request_id) cancelScreenshot('canceled');
        return;
      }
      if (payload.type === TYPE_MODE) {
        if (typeof payload.mode === 'string' && modeHints[payload.mode] !== undefined) {
          handleMode(payload.mode);
        }
      }
    }

    function onPageHide() {
      invalidateScreenshot();
      handleMode('off');
      clearSelection();
      state.session = null;
      state.connectRequestId = null;
    }

    // Navigation notifications come from the module-level shared history
    // dispatcher; this instance only registers/unregisters its handler.
    registerInstance(onUrlChanged);

    document.addEventListener('pointerdown', onPointerControl, true);
    document.addEventListener('pointerup', onPointerControl, true);
    document.addEventListener('mousedown', onSelectStart, true);
    document.addEventListener('mousemove', onSelectMove, true);
    document.addEventListener('mouseup', onSelectEnd, true);
    document.addEventListener('dblclick', onAuxClick, true);
    document.addEventListener('auxclick', onAuxClick, true);
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
    window.addEventListener('pagehide', onPageHide);
    window.addEventListener('scroll', onCaptureEnvironmentChanged, true);
    window.addEventListener('resize', onCaptureEnvironmentChanged);

    var instance = {
      setRevision(nextRevision) {
        if (state.disposed) return;
        // A real revision change is a document change: run the same
        // invalidation as navigation so a region drag started under the old
        // revision cannot be released as a target stamped with the new one
        // (hover/selection state is dropped too, and the mode badge
        // re-arms). No-op when the effective page is unchanged.
        var previousPage = currentPage(revision);
        revision = nextRevision;
        var nextPage = currentPage(revision);
        var changed = !previousPage || !nextPage
          || previousPage.path !== nextPage.path
          || (previousPage.revision || '') !== (nextPage.revision || '');
        if (changed) {
          onUrlChanged();
        } else {
          sendPageChanged();
        }
      },
      mode() { return state.mode; },
      dispose() {
        if (state.disposed) return;
        invalidateScreenshot();
        state.disposed = true;
        state.mode = 'off';
        teardownOverlay();
        clearSelection();
        document.removeEventListener('pointerdown', onPointerControl, true);
        document.removeEventListener('pointerup', onPointerControl, true);
        document.removeEventListener('mousedown', onSelectStart, true);
        document.removeEventListener('mousemove', onSelectMove, true);
        document.removeEventListener('mouseup', onSelectEnd, true);
        document.removeEventListener('dblclick', onAuxClick, true);
        document.removeEventListener('auxclick', onAuxClick, true);
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
        window.removeEventListener('pagehide', onPageHide);
        window.removeEventListener('scroll', onCaptureEnvironmentChanged, true);
        window.removeEventListener('resize', onCaptureEnvironmentChanged);
        unregisterInstance(onUrlChanged);
        state.session = null;
        liveInstances.delete(instance);
        if (singleton === instance) {
          singleton = null;
          bootstrapStopped = true;
          stopBootstrap(); // disposing must not silently resurrect a channel
        }
      },
    };
    liveInstances.set(instance, {
      parentOrigin: parentOrigin,
      onMessage: onMessage,
      configure(config) {
        // Explicit configuration upgrades the automatic instance in place.
        if (Object.prototype.hasOwnProperty.call(config, 'getElementId')) {
          customGetElementId = typeof config.getElementId === 'function' ? config.getElementId : null;
        }
        if (Object.prototype.hasOwnProperty.call(config, 'revision')) instance.setRevision(config.revision);
      },
    });
    return instance;
  }

  window.CatsCoAnnotations = {
    create: createConfigured,
    bootstrapAttribute: bootstrapAttribute,
    dispose() {
      bootstrapStopped = true;
      stopBootstrap();
      Array.from(liveInstances.keys()).forEach(function (instance) { instance.dispose(); });
    },
    runtimeVersion: 'annotations-v1',
    bridgeContract: BRIDGE_CONTRACT,
    types: {
      connect: TYPE_CONNECT,
      ready: TYPE_READY,
      mode: TYPE_MODE,
      target: TYPE_TARGET,
      page: TYPE_PAGE,
      screenshotRequest: TYPE_SCREENSHOT_REQUEST,
      screenshotResult: TYPE_SCREENSHOT_RESULT,
      screenshotCancel: TYPE_SCREENSHOT_CANCEL,
    },
  };
  bootstrapAttribute(originsAttribute);
})();
