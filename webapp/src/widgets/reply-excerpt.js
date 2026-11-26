// Text offsets survive message rerenders and distinguish repeated sentences.
function describeRange(body, range, text) {
  const prefix = document.createRange();
  prefix.selectNodeContents(body);
  prefix.setEnd(range.startContainer, range.startOffset);
  const start = prefix.toString().length;
  const match = range.toString();
  return { text, start, end: start + match.length, match };
}

export function selectedReplyExcerpt(body) {
  const selection = window.getSelection();
  if (!body || !selection?.rangeCount || selection.isCollapsed) return null;
  const range = selection.getRangeAt(0);
  const text = selection.toString().trim();
  return text && body.contains(range.commonAncestorContainer) ? describeRange(body, range, text) : null;
}

export function fullReplyExcerpt(body) {
  if (!body) return null;
  const text = (body.innerText ?? body.textContent).trim();
  return text ? { text, start: 0, end: body.textContent.length } : null;
}

function excerptRange(body, excerpt) {
  const walker = document.createTreeWalker(body, NodeFilter.SHOW_TEXT);
  const range = document.createRange();
  let offset = 0;
  for (let node; (node = walker.nextNode());) {
    const next = offset + node.length;
    if (offset <= excerpt.start && excerpt.start < next) range.setStart(node, excerpt.start - offset);
    if (offset < excerpt.end && excerpt.end <= next) {
      range.setEnd(node, excerpt.end - offset);
      return excerpt.match && range.toString() !== excerpt.match ? null : range;
    }
    offset = next;
  }
  return null;
}

export function revealReplyExcerpt(timeline, message, excerpt) {
  const body = message.querySelector('.v3-message-content');
  const reduceMotion = window.matchMedia?.('(prefers-reduced-motion: reduce)')?.matches;
  const behavior = reduceMotion ? 'instant' : 'smooth';
  const source = excerpt || fullReplyExcerpt(body);
  const range = body && source && excerptRange(body, source);
  const first = range && Array.from(range.getClientRects?.() || []).find(rect => rect.width && rect.height);
  if (!first) {
    message.scrollIntoView({ behavior, block: 'center' });
    return;
  }

  const registry = window.CSS?.highlights;
  const name = 'cc-quoted-text';
  let highlight;
  let frame;
  let pulseStyle;
  let timer;
  function clear() {
    window.clearTimeout(timer);
    timeline.removeEventListener('scroll', waitForScroll);
    cancelAnimationFrame(frame);
    pulseStyle?.remove();
    if (highlight && registry.get(name) === highlight) registry.delete(name);
  }
  function show() {
    timeline.removeEventListener('scroll', waitForScroll);
    if (!body.isConnected || !registry || !window.Highlight) return;
    highlight = new Highlight(range);
    registry.set(name, highlight);
    if (reduceMotion) {
      timer = window.setTimeout(clear, 800);
      return;
    }
    // Set the mix on the highlight itself. Older Chromium does not inherit
    // animated custom properties from the originating text element here.
    pulseStyle = document.createElement('style');
    pulseStyle.textContent = '::highlight(cc-quoted-text) { --cc-quote-emphasis: 0%; }';
    document.head.append(pulseStyle);
    const rule = pulseStyle.sheet.cssRules[0].style;
    const start = performance.now();
    function pulse(now) {
      const elapsed = now - start;
      if (elapsed >= 800) {
        clear();
        return;
      }
      const strength = Math.max(0, Math.min(elapsed / 180, 1, (800 - elapsed) / 500));
      rule.setProperty('--cc-quote-emphasis', (strength * strength * (3 - 2 * strength) * 100) + '%');
      frame = requestAnimationFrame(pulse);
    }
    frame = requestAnimationFrame(pulse);
  }
  function waitForScroll() {
    window.clearTimeout(timer);
    timer = window.setTimeout(show, 120);
  }
  const viewport = timeline.getBoundingClientRect();
  if (first.top < viewport.top || first.bottom > viewport.bottom) {
    timeline.addEventListener('scroll', waitForScroll, { passive: true });
    timeline.scrollBy({ top: first.top - viewport.top - (viewport.height - first.height) / 2, behavior });
    waitForScroll();
  } else {
    show();
  }
  return clear;
}
