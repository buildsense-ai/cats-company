// Merge uncommitted Word paragraph and table-cell edits into the model used for rendering.
//
// Every edit is keyed by its block (and cell) identity rather than by the current selection,
// so a selection change cannot move text between paragraphs or cells.

import { BLOCK_KINDS, cmToTwips } from '../docx/model.js';

const RUN_STYLE_FIELDS = ['bold', 'italic', 'underline', 'size', 'color', 'font'];

export function wordCellDraftKey(blockId, row, col) {
  return `${blockId}:${row}:${col}`;
}

function stylePatch(style) {
  if (!style) return null;
  const patch = {};
  for (const field of RUN_STYLE_FIELDS) {
    if (style[field] !== undefined) patch[field] = style[field];
  }
  return Object.keys(patch).length > 0 ? patch : null;
}

function templateRun(runs) {
  return (runs || []).find(run => !run.break) || {};
}

function runsForText(text, runs, style) {
  const template = { ...templateRun(runs), ...(style || {}) };
  const next = [];
  String(text ?? '').split('\n').forEach((line, index) => {
    if (index > 0) next.push({ break: true });
    next.push({ ...template, text: line });
  });
  return next;
}

function patchParagraph(block, text, style, props) {
  let next = { ...block };
  const styles = stylePatch(style);

  if (text !== null && text !== undefined) {
    const value = String(text);
    if (value !== block.text) {
      next.text = value;
      next.runs = runsForText(value, block.runs, styles);
    } else if (styles) {
      next.runs = (block.runs || []).map(run => (run.break ? run : { ...run, ...styles }));
    }
  } else if (styles) {
    next.runs = (block.runs || []).map(run => (run.break ? run : { ...run, ...styles }));
  }

  if (props?.align !== undefined) next.align = props.align;
  if (props?.indLeftCm !== undefined) next.indLeftTwips = cmToTwips(props.indLeftCm);
  if (props?.indFirstCm !== undefined) {
    next.indFirstTwips = cmToTwips(props.indFirstCm);
    next.indHangingTwips = 0;
  }
  return next;
}

function patchTableCells(block, cells) {
  if (!block?.cells?.length || !cells) return block;
  return {
    ...block,
    cells: block.cells.map((cell) => {
      const text = cells[wordCellDraftKey(block.id, cell.row, cell.col)];
      return text === undefined ? cell : { ...cell, text: String(text) };
    }),
  };
}

export function applyWordDraft(model, draft) {
  if (!model) return model;
  const texts = draft?.texts || {};
  const styles = draft?.styles || {};
  const props = draft?.props || {};
  const cells = draft?.cells || {};
  if (Object.keys(texts).length === 0 && Object.keys(styles).length === 0
    && Object.keys(props).length === 0 && Object.keys(cells).length === 0) return model;
  return {
    ...model,
    blocks: (model.blocks || []).map((block) => {
      if (block.kind === BLOCK_KINDS.TABLE) return patchTableCells(block, cells);
      if (block.kind !== BLOCK_KINDS.PARAGRAPH) return block;
      const hasDraft = Object.prototype.hasOwnProperty.call(texts, block.id)
        || Object.prototype.hasOwnProperty.call(styles, block.id)
        || Object.prototype.hasOwnProperty.call(props, block.id);
      if (!hasDraft) return block;
      return patchParagraph(block, texts[block.id], styles[block.id], props[block.id]);
    }),
  };
}
