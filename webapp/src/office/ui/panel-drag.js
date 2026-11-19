// Where the floating edit panel sits, relative to its default corner.
//
// A panel that always pins to the same corner is not really floating: it still covers the
// part of the document the user is working on. The offset below is applied as a translate,
// so the panel keeps its layout size while the user parks it wherever the document is
// empty. It is remembered per browser so the position survives closing the panel.

import { readStorageValue, writeStorageValue } from '../../utils/storage-access';

export const OFFICE_PANEL_OFFSET_KEY = 'office.panelOffset';

// Bounds are generous but finite. Without them a stray drag can park the panel outside the
// panel body, where there is no way to grab it back.
export const PANEL_OFFSET_BOUNDS = {
  minX: -900, maxX: 160, minY: -80, maxY: 240,
};

export function clampPanelOffset(offset) {
  const x = Number(offset?.x);
  const y = Number(offset?.y);
  return {
    x: Math.min(PANEL_OFFSET_BOUNDS.maxX,
      Math.max(PANEL_OFFSET_BOUNDS.minX, Number.isFinite(x) ? Math.round(x) : 0)),
    y: Math.min(PANEL_OFFSET_BOUNDS.maxY,
      Math.max(PANEL_OFFSET_BOUNDS.minY, Number.isFinite(y) ? Math.round(y) : 0)),
  };
}

export function readOfficePanelOffset(storage = 'localStorage') {
  const raw = readStorageValue(OFFICE_PANEL_OFFSET_KEY, storage);
  if (!raw) return { x: 0, y: 0 };
  try {
    return clampPanelOffset(JSON.parse(raw));
  } catch {
    return { x: 0, y: 0 };
  }
}

export function saveOfficePanelOffset(offset, storage = 'localStorage') {
  const clamped = clampPanelOffset(offset);
  writeStorageValue(OFFICE_PANEL_OFFSET_KEY, JSON.stringify(clamped), storage);
  return clamped;
}

/** `translate(...)` for the panel and the resizer, which must move together. */
export function panelOffsetTransform(offset) {
  const { x, y } = clampPanelOffset(offset);
  return `translate(${x}px, ${y}px)`;
}