// Measure where a block landed on screen.
//
// The bar has to sit beside what the user clicked, and only the browser knows where that is:
// the slide is scaled to the window, the preview column is resizable, and a Word paragraph
// has no model coordinates at all. Measuring the element keeps one code path for both
// previews and stays correct through window resizes, which percentages of a model box would
// not.

import { useCallback, useEffect, useRef, useState } from 'react';

/** Rect of `selector` inside `host`, in host-relative pixels. Null when not found. */
export function measureInside(host, selector) {
  if (!host || !selector) return null;
  const rectOfHost = host.getBoundingClientRect?.();
  if (!rectOfHost) return null;
  let node = null;
  try {
    node = host.querySelector(selector);
  } catch {
    // An id with quotes would make an invalid selector. Not worth crashing the editor over.
    return null;
  }
  if (!node) return null;
  const rect = node.getBoundingClientRect?.();
  if (!rect) return null;
  return {
    left: rect.left - rectOfHost.left,
    top: rect.top - rectOfHost.top,
    width: rect.width,
    height: rect.height,
  };
}

export function hostSize(host) {
  const rect = host?.getBoundingClientRect?.();
  if (!rect) return { width: 0, height: 0 };
  return { width: rect.width, height: rect.height };
}

/** Attribute selector for an element id, quoted so `#` and `/` in ids stay literal. */
export function byElementId(elId) {
  if (!elId) return '';
  return `[data-office-el="${String(elId).replace(/"/g, '\\"')}"]`;
}

export function byBlockId(blockId) {
  if (!blockId) return '';
  return `[data-office-block="${String(blockId).replace(/"/g, '\\"')}"]`;
}

/**
 * Track the on-screen box of whatever is selected, plus the host's own size.
 *
 * Re-measures on selection change, on host resize, and once more after a frame so a canvas
 * that painted a moment late still lands the bar on the right block. A stale anchor is the
 * failure mode that matters: the bar would then point at a block the user is not editing.
 */
export function useBarAnchor(hostRef, selector, revision = 0) {
  const [state, setState] = useState({ anchor: null, size: { width: 0, height: 0 } });
  const frame = useRef(0);

  const measure = useCallback(() => {
    const host = hostRef?.current;
    if (!host) {
      setState({ anchor: null, size: { width: 0, height: 0 } });
      return;
    }
    const next = {
      anchor: selector ? measureInside(host, selector) : null,
      size: hostSize(host),
    };
    setState((previous) => {
      const sameAnchor = JSON.stringify(previous.anchor) === JSON.stringify(next.anchor);
      const sameSize = previous.size.width === next.size.width
        && previous.size.height === next.size.height;
      return sameAnchor && sameSize ? previous : next;
    });
  }, [hostRef, selector]);

  useEffect(() => {
    measure();
    // One extra pass on the next frame: the first paint of a freshly selected block has no
    // layout yet in some browsers, and measuring then yields a zero-sized box.
    frame.current = typeof requestAnimationFrame === 'function'
      ? requestAnimationFrame(measure)
      : 0;
    const host = hostRef?.current;
    const observer = typeof ResizeObserver === 'function' && host
      ? new ResizeObserver(measure)
      : null;
    if (observer && host) observer.observe(host);
    if (typeof window !== 'undefined') window.addEventListener('resize', measure);
    return () => {
      if (frame.current) cancelAnimationFrame(frame.current);
      frame.current = 0;
      observer?.disconnect();
      if (typeof window !== 'undefined') window.removeEventListener('resize', measure);
    };
  }, [measure, revision]);

  return { ...state, measure };
}