// Shape model shared by the parser, the renderer and the writer.
//
// The central rule: a shape's identity is established while reading the file, not
// guessed from the rendered pixels. `elId` is derived from the XML path, so a click
// on the canvas resolves to exactly one node in exactly one part. There is no
// reverse lookup step anywhere in the pipeline.

export const EMU_PER_INCH = 914400;
export const EMU_PER_POINT = 12700;

export const SHAPE_KINDS = {
  TEXT: 'text',
  PICTURE: 'picture',
  TABLE: 'table',
  GROUP: 'group',
  CONNECTOR: 'connector',
  FRAME: 'frame',
  UNKNOWN: 'unknown',
};

/** Unit conversion. Everything stored in the model stays in EMU to avoid drift. */
export function emuToPercent(emu, total) {
  if (!Number.isFinite(emu) || !Number.isFinite(total) || total <= 0) return 0;
  return (emu / total) * 100;
}

export function percentToEmu(percent, total) {
  if (!Number.isFinite(percent) || !Number.isFinite(total)) return 0;
  return Math.round((percent / 100) * total);
}

export function emuToPt(emu) {
  return Number.isFinite(emu) ? emu / EMU_PER_POINT : 0;
}

export function ptToEmu(pt) {
  return Number.isFinite(pt) ? Math.round(pt * EMU_PER_POINT) : 0;
}

/** Transform an inner absolute box into its group's child coordinate space. */
export function toRelativeBox(box, groupBox, groupChildOffset) {
  if (!groupBox || groupBox.cx <= 0 || groupBox.cy <= 0) return box;
  const scaleX = box.cx / groupBox.cx;
  const scaleY = box.cy / groupBox.cy;
  const offsetX = Number.isFinite(groupChildOffset?.x) ? groupChildOffset.x : 0;
  const offsetY = Number.isFinite(groupChildOffset?.y) ? groupChildOffset.y : 0;
  return {
    x: Math.round((box.x - offsetX) * scaleX),
    y: Math.round((box.y - offsetY) * scaleY),
    cx: Math.round(box.cx),
    cy: Math.round(box.cy),
  };
}

/** Compose a group's child transform onto a child box to get absolute position. */
export function fromRelativeBox(box, groupBox, groupChildOffset) {
  if (!groupBox || groupBox.cx <= 0 || groupBox.cy <= 0) return box;
  const invX = box.cx > 0 ? groupBox.cx / box.cx : 1;
  const invY = box.cy > 0 ? groupBox.cy / box.cy : 1;
  const offsetX = Number.isFinite(groupChildOffset?.x) ? groupChildOffset.x : 0;
  const offsetY = Number.isFinite(groupChildOffset?.y) ? groupChildOffset.y : 0;
  return {
    x: Math.round(box.x / invX + offsetX),
    y: Math.round(box.y / invY + offsetY),
    cx: Math.round(groupBox.cx),
    cy: Math.round(groupBox.cy),
  };
}

/**
 * Build a stable element id.
 *
 * `ordinal` is the index of the shape inside its parent's children, which is also
 * the position the writer uses to re-find the node. Including the parent path makes
 * ids unique per part and stable across re-parses of the same file.
 */
export function shapeId(part, ordinal) {
  const base = String(part || '').replace(/^ppt\//, '').replace(/\.xml$/, '').replace(/[^\w]+/g, '-');
  return `${base}#${ordinal}`;
}

/** Human label for the element list. Falls back to the Office-assigned name. */
export function shapeLabel(node) {
  if (!node) return '元素';
  if (node.role === 'title') return '标题';
  if (node.role === 'subtitle') return '副标题';
  switch (node.kind) {
    case SHAPE_KINDS.PICTURE: return '图片';
    case SHAPE_KINDS.TABLE: return `表格 ${node.tableRows || ''}×${node.tableCols || ''}`.trim();
    case SHAPE_KINDS.GROUP: return '组合';
    case SHAPE_KINDS.CONNECTOR: return '线条';
    case SHAPE_KINDS.FRAME: return '内容框';
    case SHAPE_KINDS.TEXT: return node.text ? '文本框' : '形状';
    default: return node.name || '元素';
  }
}

/** One-line preview used by the collapsed element list. */
export function shapePreview(node) {
  if (!node) return '';
  if (node.text) {
    const flat = String(node.text).replace(/\s+/g, ' ').trim();
    return flat.length > 22 ? `${flat.slice(0, 22)}…` : flat;
  }
  if (node.kind === SHAPE_KINDS.TABLE) return `${node.tableRows} 行 ${node.tableCols} 列`;
  if (node.kind === SHAPE_KINDS.PICTURE) return node.imageName || '图片';
  return '';
}
