import React, { useLayoutEffect, useMemo, useRef, useState, useSyncExternalStore } from 'react';

const segmenter = typeof Intl.Segmenter === 'function' ? new Intl.Segmenter(undefined, { granularity: 'grapheme' }) : null;
const characters = text => segmenter ? Array.from(segmenter.segment(text), part => part.segment) : Array.from(text);
const TAIL_CHARS = 6;
// Matches the `cc-reply-text-arrive` duration in catsco-liquid-messages.css.
// A character that is no longer the newest one is pushed past the end of its
// own animation, so it renders at its final opacity instead of replaying.
const ARRIVE_MS = 170;

// Every text fragment of every message asks this. One shared media query and
// one native listener serve all of them, instead of a query and a listener per
// fragment (two per history row). The query is re-created if `matchMedia`
// itself is replaced.
const reducedMotion = {
  matchMedia: undefined,
  media: null,
  listeners: new Set(),
  notify: () => reducedMotion.listeners.forEach((listener) => listener()),
};

function reducedMotionMedia() {
  const matchMedia = typeof window === 'undefined' ? undefined : window.matchMedia;
  if (reducedMotion.matchMedia !== matchMedia) {
    const listening = reducedMotion.listeners.size > 0;
    if (listening) reducedMotion.media?.removeEventListener?.('change', reducedMotion.notify);
    reducedMotion.matchMedia = matchMedia;
    reducedMotion.media = matchMedia?.call(window, '(prefers-reduced-motion: reduce)') ?? null;
    if (listening) reducedMotion.media?.addEventListener?.('change', reducedMotion.notify);
  }
  return reducedMotion.media;
}

function subscribeReducedMotion(listener) {
  const media = reducedMotionMedia();
  if (reducedMotion.listeners.size === 0) media?.addEventListener?.('change', reducedMotion.notify);
  reducedMotion.listeners.add(listener);
  return () => {
    reducedMotion.listeners.delete(listener);
    if (reducedMotion.listeners.size === 0) reducedMotion.media?.removeEventListener?.('change', reducedMotion.notify);
  };
}

const prefersReducedMotion = () => Boolean(reducedMotionMedia()?.matches);

function useReducedMotion() {
  return useSyncExternalStore(subscribeReducedMotion, prefersReducedMotion, () => false);
}

// Splitting a reply into graphemes is only needed while it animates. History
// replies never do, and holding one string per character for each of them was
// most of the timeline's retained memory, so the split happens on first use.
const SETTLED = -1;
function lazyGraphemes(text) {
  let parts = null;
  return {
    get parts() {
      if (!parts) parts = characters(text);
      return parts;
    },
    get length() {
      return this.parts.length;
    },
  };
}

// Arrival tokens belong only to live WebSocket messages, never loaded history.
// Remember consumed tokens so returning to a mounted/virtualized history does not replay.
const seenArrivals = new WeakSet();
const NORMAL_RATE = 24;

export function useReplyReveal(text, streaming, { arrival, immediate = false } = {}) {
  const reduced = useReducedMotion();
  const graphemes = useMemo(() => lazyGraphemes(text), [text]);
  // `SETTLED` stands for "every character of `current.text` is shown"; it is
  // resolved to a number only when an animation needs one.
  const [progress, setProgress] = useState(() => {
    const active = !reduced && !immediate && (streaming || (arrival && !seenArrivals.has(arrival)));
    return { count: active ? 0 : SETTLED, active: Boolean(active) };
  });
  const model = useRef({ text, graphemes, ...progress, arrival });
  useLayoutEffect(() => {
    const current = model.current;
    const fresh = arrival && arrival !== current.arrival && !seenArrivals.has(arrival);
    if (arrival) seenArrivals.add(arrival);
    if (reduced || immediate) {
      current.count = SETTLED;
      current.active = false;
    } else if (fresh && !text.startsWith(current.text)) {
      current.count = 0;
      current.active = true;
    } else if (!text.startsWith(current.text)) {
      // A correction is authoritative, not another copy of the reply to replay.
      current.count = SETTLED;
      current.active = false;
    } else if (streaming || fresh || current.active) {
      // A settled reply that keeps growing reveals only the new part.
      if (current.count === SETTLED) current.count = current.graphemes.length;
      current.active = Boolean(streaming || current.count < graphemes.length);
    } else current.count = SETTLED;
    Object.assign(current, { text, graphemes, arrival });
    setProgress({ count: current.count, active: current.active });
  }, [text, graphemes, streaming, arrival, immediate, reduced]);
  const animate = !reduced && !immediate && progress.active;
  useLayoutEffect(() => {
    if (!animate) return undefined;
    let frame;
    let last = performance.now();
    let credit = 0;
    const step = now => {
      const current = model.current;
      if (current.count === SETTLED) current.count = current.graphemes.length;
      const backlog = current.graphemes.length - current.count;
      const elapsed = Math.min(64, now - last);
      last = now;
      if (backlog > 0) {
        // Short replies keep an observable pace, even when delivered in one payload.
        // Only substantial backlogs accelerate, with a hard upper bound.
        const rate = Math.min(120, NORMAL_RATE + Math.max(0, backlog - 100) / 5);
        credit += elapsed * rate / 1000;
        const advance = Math.floor(credit);
        if (advance > 0) {
          credit -= advance;
          current.count = Math.min(current.graphemes.length, current.count + advance);
          current.active = Boolean(streaming || current.count < current.graphemes.length);
          setProgress({ count: current.count, active: current.active });
        }
      } else credit = 0;
      if (current.active) frame = requestAnimationFrame(step);
    };
    frame = requestAnimationFrame(step);
    return () => cancelAnimationFrame(frame);
  }, [animate, streaming]);
  // Read lazily: callers that do not animate never touch `parts` or `count`.
  return {
    get parts() { return graphemes.parts; },
    get count() {
      return animate && progress.count !== SETTLED ? Math.min(progress.count, graphemes.length) : graphemes.length;
    },
    animate,
  };
}

export function FadingText({ text, enabled, reveal, tail = false }) {
  const own = useReplyReveal(text, enabled && !reveal);
  const state = reveal || own;
  if (!state.animate && !tail) return text;
  const visible = state.parts.slice(0, state.count);
  const start = Math.max(0, visible.length - TAIL_CHARS);
  return <>{visible.slice(0, start).join('')}{visible.slice(start).map((char, index) => {
    const position = start + index;
    // Only the newest character arrives. Everything before it is pushed past
    // the end of the arrival animation so it renders settled: replaying the
    // fade on every reveal tick is what made the whole reply shimmer.
    return <span className="cc-reply-new-text" key={position}
      style={position < visible.length - 1 ? { animationDelay: `-${ARRIVE_MS}ms` } : undefined}>{char}</span>;
  })}</>;
}

function htmlText(html) {
  const template = document.createElement('template');
  template.innerHTML = html;
  return template.content.textContent || '';
}

function progressiveHtml(html, visibleCount) {
  const template = document.createElement('template');
  template.innerHTML = html;
  const walker = document.createTreeWalker(template.content, NodeFilter.SHOW_TEXT);
  const nodes = [];
  let offset = 0;
  while (walker.nextNode()) {
    const node = walker.currentNode;
    const parts = characters(node.nodeValue);
    nodes.push({ node, parts, offset });
    offset += parts.length;
  }
  // Which visible character is the newest one overall. Only it may carry a
  // running arrival animation; every earlier character is already settled.
  const newest = visibleCount - 1;
  for (const { node, parts, offset: start } of nodes) {
    const end = Math.max(0, Math.min(parts.length, visibleCount - start));
    const stable = Math.max(0, Math.min(end, visibleCount - TAIL_CHARS - start));
    const fragment = document.createDocumentFragment();
    fragment.appendChild(document.createTextNode(parts.slice(0, stable).join('')));
    for (let i = stable; i < end; i += 1) {
      const span = document.createElement('span');
      span.className = 'cc-reply-new-text';
      // Re-rendering the sanitized HTML replaces every span, which restarts its
      // animation. Push settled characters past the end of the fade so they are
      // painted at full opacity immediately instead of shimmering again.
      if (start + i < newest) span.style.animationDelay = `-${ARRIVE_MS}ms`;
      span.textContent = parts[i];
      fragment.appendChild(span);
    }
    node.replaceWith(fragment);
  }
  // Future paragraphs/list items must not reserve empty rows in the growing bubble.
  for (const element of Array.from(template.content.querySelectorAll('*')).reverse()) {
    if (!element.textContent && !element.querySelector('img, hr')) element.remove();
  }
  return template.innerHTML;
}

export function FadingMarkdown({ html, enabled, receivedVisible = false, visibleCount = null, className }) {
  const text = useMemo(() => htmlText(html), [html]);
  const reveal = useReplyReveal(text, enabled && !receivedVisible);
  const { animate } = reveal;
  const count = animate ? reveal.count : null;
  // `visibleCount` lets the caller own how much is drawn while the HTML itself
  // comes from the complete reply. The render branch then never changes as the
  // reply arrives; only the number of characters inside it grows.
  const decorated = useMemo(() => {
    if (visibleCount != null && enabled) {
      return progressiveHtml(html, Math.max(0, Math.min(visibleCount, characters(text).length)));
    }
    return receivedVisible && enabled ? progressiveHtml(html, characters(text).length)
      : animate ? progressiveHtml(html, count) : html;
  }, [animate, count, enabled, receivedVisible, text, html, visibleCount]);
  return <div className={className} dangerouslySetInnerHTML={{ __html: decorated }} />;
}

export function LiquidReplySurface({ children, enabled, streaming, waiting, contentKey, className = '', ...props }) {
  const ref = useRef(null);
  useLayoutEffect(() => {
    const element = ref.current;
    if (!element || !enabled) return undefined;
    const notify = () => element.dispatchEvent(new Event('cc:reply-layout', { bubbles: true }));
    const observer = typeof ResizeObserver === 'function' ? new ResizeObserver(notify) : null;
    observer?.observe(element);
    notify();
    return () => observer?.disconnect();
  }, [enabled]);
  return <div ref={ref} className={`v3-message-text${className ? ` ${className}` : ''}`} {...props}>{children}</div>;
}
