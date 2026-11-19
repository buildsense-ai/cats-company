// Where the small text bar sits next to the block being edited.
//
// The bar is positioned inside the stage, in stage pixels measured from real rectangles, not
// from the document model. That keeps one code path for both previews: a slide shape has
// model coordinates, a Word paragraph only has a DOM box, and only the browser knows where
// either one actually landed after scaling.
//
// The bar prefers the right side of the block, flips left when there is no room, and is
// clamped to the stage so it can never be dragged somewhere unreachable.

export const BAR_GAP = 10;
export const BAR_MARGIN = 8;
export const BAR_WIDTH = 330;
export const BAR_HEIGHT = 148;

/** Dropping the bar this close to the right edge docks it as a column. */
export const FLOATING_BAR_DOCK_THRESHOLD = 40;

function finite(value, fallback) {
  const n = Number(value);
  return Number.isFinite(n) ? n : fallback;
}

function clamp(value, min, max) {
  if (max < min) return min;
  return Math.min(Math.max(value, min), max);
}

/**
 * Place the bar beside a block.
 *
 * `anchor` and `container` are rectangles in the same space, `size` is the bar's own box.
 * Returns the bar's top-left plus the side it landed on, so the caller can flip a border
 * without recomputing anything.
 */
export function placeBarBeside({
  anchor, container, size, gap = BAR_GAP, margin = BAR_MARGIN,
}) {
  const stage = {
    width: Math.max(finite(container?.width, 0), 0),
    height: Math.max(finite(container?.height, 0), 0),
  };
  const bar = {
    width: Math.max(finite(size?.width, BAR_WIDTH), 0),
    height: Math.max(finite(size?.height, BAR_HEIGHT), 0),
  };
  // Before the stage has been measured there is no honest answer, and guessing would put the
  // bar in a corner of a zero-sized box. Zero it out; the first measurement corrects it.
  if (stage.width <= 0 || stage.height <= 0) return { left: 0, top: 0, side: 'below' };

  const box = {
    left: clamp(finite(anchor?.left, 0), 0, stage.width),
    top: clamp(finite(anchor?.top, 0), 0, stage.height),
    width: Math.max(finite(anchor?.width, 0), 0),
    height: Math.max(finite(anchor?.height, 0), 0),
  };
  const rightEdge = Math.min(box.left + box.width, stage.width);
  const rightRoom = stage.width - rightEdge - gap - margin;
  const leftRoom = box.left - gap - margin;

  let side;
  let left;
  if (rightRoom >= bar.width) {
    side = 'right';
    left = rightEdge + gap;
  } else if (leftRoom >= bar.width) {
    side = 'left';
    left = box.left - gap - bar.width;
  } else {
    // Neither side fits: sit just under the block. Overlapping the canvas still beats a bar
    // that covers the whole selection or leaves the stage.
    side = 'below';
    left = box.left;
  }

  const maxLeft = Math.max(stage.width - bar.width - margin, margin);
  const maxTop = Math.max(stage.height - bar.height - margin, margin);
  // Vertically the bar starts at the block's top edge, which keeps the grip next to the text
  // the user clicked, and only slides up when that would run past the bottom.
  return {
    left: Math.round(clamp(left, margin, maxLeft)),
    top: Math.round(clamp(box.top, margin, maxTop)),
    side,
  };
}

/** True when a drop point is close enough to the right edge to dock the bar. */
export function shouldDockBar({ pointerX, containerWidth, threshold = FLOATING_BAR_DOCK_THRESHOLD }) {
  const width = finite(containerWidth, 0);
  if (width <= 0) return false;
  return finite(pointerX, width) >= width - Math.max(finite(threshold, 0), 0);
}

/**
 * Keep a dragged bar inside its container.
 *
 * The initial position is clamped, but the drag offset used to be free. That let the bar be
 * pulled past the right edge, where it was half outside the preview and could not be grabbed
 * back — a real browser check showed it 153px out of bounds mid-drag.
 *
 * Teleports are not rejected: a wide `allowed` range still lets the user park the bar
 * completely left of the block, which is the point of dragging it.
 */
export function clampDragOffset({ offset, position, size, container, margin = BAR_MARGIN }) {
  const width = finite(container?.width, 0);
  const height = finite(container?.height, 0);
  const barWidth = Math.max(finite(size?.width, 0), 0);
  const barHeight = Math.max(finite(size?.height, 0), 0);
  const x = finite(offset?.x, 0);
  const y = finite(offset?.y, 0);
  if (width <= 0 || height <= 0) return { x, y };

  // The offset is relative to the computed spot, so the legal range is what keeps
  // (position + offset) inside the margin box.
  const minX = margin - finite(position?.left, 0);
  const maxX = width - barWidth - margin - finite(position?.left, 0);
  const minY = margin - finite(position?.top, 0);
  const maxY = height - barHeight - margin - finite(position?.top, 0);

  return {
    x: Math.round(clamp(x, Math.min(minX, maxX), Math.max(minX, maxX))),
    y: Math.round(clamp(y, Math.min(minY, maxY), Math.max(minY, maxY))),
  };
}

/** Whether the bar is pinned against the right edge, remembered per browser. */
export const BAR_DOCKED_KEY = 'office.barDocked';

export function readBarDocked(storage = 'localStorage') {
  try {
    const raw = storage === 'sessionStorage'
      ? globalThis.sessionStorage?.getItem(BAR_DOCKED_KEY)
      : globalThis.localStorage?.getItem(BAR_DOCKED_KEY);
    return raw === '1';
  } catch {
    return false;
  }
}

export function saveBarDocked(value, storage = 'localStorage') {
  try {
    const target = storage === 'sessionStorage' ? globalThis.sessionStorage : globalThis.localStorage;
    target?.setItem(BAR_DOCKED_KEY, value ? '1' : '0');
  } catch {
    // A blocked storage write only costs the preference next reload.
  }
  return Boolean(value);
}