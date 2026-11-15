// Geometry math for canvas interaction.
//
// Two coordinate systems meet here and the conversion is the only place they do:
//
//   CSS pixels  — what the pointer reports, depends on panel width
//   EMU         — what OOXML stores, a fixed unit (914400 per inch)
//
// The conversion factor is derived per interaction from the canvas's measured size, not
// cached. A cached factor goes stale the moment the user resizes the side panel, and a
// stale factor makes every drag land short or long by exactly that ratio.

export const MIN_SHAPE_EMU = 12700; // one point, the smallest size PowerPoint accepts

/** EMU per CSS pixel along one axis, measured from the live canvas box. */
export function emuPerPixel(renderedPx, slideEmu) {
  if (!Number.isFinite(renderedPx) || renderedPx <= 0) return 0;
  if (!Number.isFinite(slideEmu) || slideEmu <= 0) return 0;
  return slideEmu / renderedPx;
}

/**
 * Map a group child's box into slide coordinates.
 *
 * A child inside `p:grpSp` stores `a:off`/`a:ext` in the group's own child space, anchored at
 * `chOff` and scaled by `chExt`. Drawing those raw values puts every grouped shape as if the group
 * began at the slide origin, so grouped content jumps the moment the group is selected.
 *
 *   absolute = groupOff + (child - chOff) * (groupExt / chExt)
 */
export function absoluteFromRelative(childBox, groupBox, chOffset, chExtent) {
  if (!groupBox || !chOffset || !chExtent || chExtent.x <= 0 || chExtent.y <= 0) return childBox;
  const sx = groupBox.cx / chExtent.x;
  const sy = groupBox.cy / chExtent.y;
  return {
    x: Math.round(groupBox.x + (childBox.x - chOffset.x) * sx),
    y: Math.round(groupBox.y + (childBox.y - chOffset.y) * sy),
    cx: Math.round(childBox.cx * sx),
    cy: Math.round(childBox.cy * sy),
  };
}

/** Inverse of absoluteFromRelative, used when a drag on a grouped child is written back. */
export function relativeFromAbsolute(absBox, groupBox, chOffset, chExtent) {
  if (!groupBox || !chOffset || !chExtent || groupBox.cx <= 0 || groupBox.cy <= 0) return absBox;
  const sx = chExtent.x / groupBox.cx;
  const sy = chExtent.y / groupBox.cy;
  return {
    x: Math.round(chOffset.x + (absBox.x - groupBox.x) * sx),
    y: Math.round(chOffset.y + (absBox.y - groupBox.y) * sy),
    cx: Math.round(absBox.cx * sx),
    cy: Math.round(absBox.cy * sy),
  };
}

/**
 * Keep a shape inside the slide.
 *
 * PowerPoint permits off-slide content, and users occasionally want a deliberate bleed.
 * Dragging is different: a shape that leaves the canvas becomes unreachable, because the
 * canvas clips its overflow. So the box is clamped so at least a sliver stays visible.
 */
export function clampBoxToSlide(box, slideSize, { minVisible = 0 } = {}) {
  const cx = Math.max(Math.round(box.cx), MIN_SHAPE_EMU);
  const cy = Math.max(Math.round(box.cy), MIN_SHAPE_EMU);
  // Allow the box to hang off by its own size minus a sliver, so a user can drag a wide
  // shape partly out and still grab it again.
  const sliverX = Math.max(minVisible, Math.round(cx * 0.1));
  const sliverY = Math.max(minVisible, Math.round(cy * 0.1));
  return {
    x: Math.round(Math.min(Math.max(box.x, sliverX - cx), slideSize.cx - sliverX)),
    y: Math.round(Math.min(Math.max(box.y, sliverY - cy), slideSize.cy - sliverY)),
    cx,
    cy,
  };
}

/**
 * Apply a pointer delta to a box.
 *
 * `anchor` is captured at pointerdown so repeated moves accumulate from the original
 * box instead of from the previous frame. Accumulating from the previous frame makes
 * rounding error compound and the shape drifts away from the cursor.
 */
export function moveBox(anchor, deltaPx, factor) {
  return {
    x: Math.round(anchor.x + deltaPx.dx * factor.x),
    y: Math.round(anchor.y + deltaPx.dy * factor.y),
    cx: anchor.cx,
    cy: anchor.cy,
  };
}

export const RESIZE_HANDLES = ['nw', 'n', 'ne', 'e', 'se', 's', 'sw', 'w'];

/**
 * Resize from one handle.
 *
 * Dragging the west or north edge moves the origin as well as the size, which is the
 * detail that makes a resize feel wrong when it is missing: the shape grows in one
 * direction while its opposite edge stays pinned.
 */
export function resizeBox(anchor, handle, deltaPx, factor) {
  const dx = deltaPx.dx * factor.x;
  const dy = deltaPx.dy * factor.y;
  let { x, y, cx, cy } = anchor;

  if (handle.includes('w')) {
    const nextX = anchor.x + dx;
    const nextCx = anchor.cx - dx;
    if (nextCx >= MIN_SHAPE_EMU) { x = nextX; cx = nextCx; }
  }
  if (handle.includes('e')) {
    const nextCx = anchor.cx + dx;
    if (nextCx >= MIN_SHAPE_EMU) cx = nextCx;
  }
  if (handle.includes('n')) {
    const nextY = anchor.y + dy;
    const nextCy = anchor.cy - dy;
    if (nextCy >= MIN_SHAPE_EMU) { y = nextY; cy = nextCy; }
  }
  if (handle.includes('s')) {
    const nextCy = anchor.cy + dy;
    if (nextCy >= MIN_SHAPE_EMU) cy = nextCy;
  }

  return { x: Math.round(x), y: Math.round(y), cx: Math.round(cx), cy: Math.round(cy) };
}

/**
 * Snap a box's edges to the slide's centre and margins.
 *
 * Threshold is expressed in EMU but derived from on-screen pixels by the caller, so the
 * snap feels the same regardless of zoom. Without a pixel-derived threshold a snap either
 * never triggers or grabs shapes from far away.
 */
export function snapBox(box, slideSize, thresholdEmu) {
  if (!Number.isFinite(thresholdEmu) || thresholdEmu <= 0) return box;
  const result = { ...box };

  const candidatesX = [
    { value: 0, edge: 'left' },
    { value: Math.round((slideSize.cx - box.cx) / 2), edge: 'center' },
    { value: slideSize.cx - box.cx, edge: 'right' },
  ];
  for (const candidate of candidatesX) {
    if (Math.abs(result.x - candidate.value) <= thresholdEmu) {
      result.x = candidate.value;
      result.snappedX = candidate.edge;
      break;
    }
  }

  const candidatesY = [
    { value: 0, edge: 'top' },
    { value: Math.round((slideSize.cy - box.cy) / 2), edge: 'middle' },
    { value: slideSize.cy - box.cy, edge: 'bottom' },
  ];
  for (const candidate of candidatesY) {
    if (Math.abs(result.y - candidate.value) <= thresholdEmu) {
      result.y = candidate.value;
      result.snappedY = candidate.edge;
      break;
    }
  }

  return result;
}

/**
 * Distribute boxes evenly along one axis, keeping the outermost two fixed.
 *
 * Input boxes must carry an `elId` so the caller can map results back onto the model.
 * Returns a new array in the same order as the input.
 */
export function distributeBoxes(boxes, axis = 'x') {
  if (!Array.isArray(boxes) || boxes.length < 3) return boxes;
  const key = axis === 'y' ? 'y' : 'x';
  const sizeKey = axis === 'y' ? 'cy' : 'cx';
  const sorted = [...boxes].sort((a, b) => a[key] - b[key]);
  const first = sorted[0];
  const last = sorted[sorted.length - 1];
  const totalSpan = (last[key] + last[sizeKey]) - first[key];
  const totalSize = sorted.reduce((sum, box) => sum + box[sizeKey], 0);
  // Space between neighbours, not the space each box gets: gaps are what "distribute"
  // means in every drawing tool, and using an even stride instead leaves uneven gaps
  // when the shapes differ in size.
  const gap = (totalSpan - totalSize) / (sorted.length - 1);

  const placed = new Map();
  let cursor = first[key];
  for (const box of sorted) {
    placed.set(box.elId, { ...box, [key]: Math.round(cursor) });
    cursor += box[sizeKey] + gap;
  }
  return boxes.map(box => placed.get(box.elId) || box);
}

/** Align boxes to one edge of their common bounding box. */
export function alignBoxes(boxes, edge) {
  if (!Array.isArray(boxes) || boxes.length < 1) return boxes;
  if (boxes.length === 1) return boxes;
  const left = Math.min(...boxes.map(box => box.x));
  const right = Math.max(...boxes.map(box => box.x + box.cx));
  const top = Math.min(...boxes.map(box => box.y));
  const bottom = Math.max(...boxes.map(box => box.y + box.cy));
  const centerX = Math.round((left + right) / 2);
  const centerY = Math.round((top + bottom) / 2);

  return boxes.map((box) => {
    switch (edge) {
      case 'left': return { ...box, x: left };
      case 'right': return { ...box, x: right - box.cx };
      case 'centerX': return { ...box, x: centerX - Math.round(box.cx / 2) };
      case 'top': return { ...box, y: top };
      case 'bottom': return { ...box, y: bottom - box.cy };
      case 'centerY': return { ...box, y: centerY - Math.round(box.cy / 2) };
      default: return box;
    }
  });
}
