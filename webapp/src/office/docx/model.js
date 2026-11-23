// Word model, shared by the parser, the renderer and the writer.
//
// Word is a flow, not a canvas. There are no coordinates to click and no boxes to drag: a
// document is an ordered list of blocks, and each block flows after the one above it. That
// difference drives every decision here:
//
//   selection   a paragraph or a run of text, addressed by document order
//   editing     text and paragraph style, never position
//   layout      produced by the renderer, not stored in the file
//
// The identity rule matches the PPTX side: an id is derived from the XML child-index path at
// parse time, so a click resolves to exactly one node without any inference.

export const BLOCK_KINDS = {
  PARAGRAPH: 'paragraph',
  TABLE: 'table',
  UNKNOWN: 'unknown',
};

// OOXML stores font size in half-points and indentation in twips (1/20 pt, 1440 per inch).
export const HALF_POINTS_PER_PT = 2;
export const TWIPS_PER_CM = 567;

export function halfPointsToPt(value) {
  const n = Number(value);
  return Number.isFinite(n) ? n / HALF_POINTS_PER_PT : 0;
}

export function ptToHalfPoints(value) {
  const n = Number(value);
  return Number.isFinite(n) ? Math.round(n * HALF_POINTS_PER_PT) : 0;
}

export function twipsToCm(value) {
  const n = Number(value);
  return Number.isFinite(n) ? n / TWIPS_PER_CM : 0;
}

export function cmToTwips(value) {
  const n = Number(value);
  return Number.isFinite(n) ? Math.round(n * TWIPS_PER_CM) : 0;
}

/**
 * Stable block id.
 *
 * Derived from the part name plus the child-index path, so re-parsing the same file yields
 * the same ids and a save can re-find the node it edited.
 */
export function blockId(part, path) {
  const base = String(part || '').replace(/^word\//, '').replace(/\.xml$/, '').replace(/[^\w]+/g, '-');
  return `${base}#${path.join('.')}`;
}

/** Justification values Word accepts. Anything else is left as-is. */
export const ALIGNMENTS = ['left', 'center', 'right', 'both', 'distribute'];

/** Human label for the block list. */
export function blockLabel(block) {
  if (!block) return '内容';
  if (block.kind === BLOCK_KINDS.TABLE) {
    return `表格 ${block.rows || ''}×${block.cols || ''}`.trim();
  }
  if (block.heading) return `标题 ${block.heading}`;
  if (block.styleId === 'ListParagraph') return '列表项';
  if (!block.text) return '空段落';
  return '段落';
}

/** One-line preview for the collapsed block list. */
export function blockPreview(block) {
  if (!block) return '';
  if (block.kind === BLOCK_KINDS.TABLE) return `${block.rows} 行 ${block.cols} 列`;
  const flat = String(block.text || '').replace(/\s+/g, ' ').trim();
  return flat.length > 26 ? `${flat.slice(0, 26)}…` : flat;
}