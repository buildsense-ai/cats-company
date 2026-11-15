// Merge uncommitted Word paragraph and table-cell edits into the model used for rendering.
//
// Word's canvas is a flow renderer, so it has no geometry overlay to carry a partial edit.
// Keeping this merge pure mirrors the PPTX draft renderer and lets the canvas show exactly
// what the next save will write without mutating the parsed document.

import { BLOCK_KINDS, cmToTwips } from '../docx/model.js';

const RUN_STYLE_FIELDS = ['bold', 'italic', 'underline', 'size', 'color', 'font'];

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

function patchParagraph(block, draft) {
  let next = { ...block };
  const style = stylePatch(draft?.style);

  if (draft?.text !== null && draft?.text !== undefined) {
    const text = String(draft.text);
    if (text !== block.text) {
      next.text = text;
      next.runs = runsForText(text, block.runs, style);
    } else if (style) {
      next.runs = (block.runs || []).map(run => (run.break ? run : { ...run, ...style }));
    }
  } else if (style) {
    next.runs = (block.runs || []).map(run => (run.break ? run : { ...run, ...style }));
  }

  if (draft?.props?.align !== undefined) next.align = draft.props.align;
  if (draft?.props?.indLeftCm !== undefined) {
    next.indLeftTwips = cmToTwips(draft.props.indLeftCm);
  }
  if (draft?.props?.indFirstCm !== undefined) {
    next.indFirstTwips = cmToTwips(draft.props.indFirstCm);
    next.indHangingTwips = 0;
  }
  return next;
}

function patchTableCell(block, draft) {
  const cellDraft = draft?.cell;
  if (!cellDraft || cellDraft.row === undefined || cellDraft.col === undefined) return block;
  return {
    ...block,
    cells: (block.cells || []).map(cell => (
      cell.row === cellDraft.row && cell.col === cellDraft.col
        ? { ...cell, text: String(cellDraft.text ?? '') }
        : cell
    )),
  };
}

export function applyWordDraft(model, selectedId, draft) {
  if (!model || !selectedId) return model;
  return {
    ...model,
    blocks: (model.blocks || []).map(block => {
      if (block.id !== selectedId) return block;
      if (block.kind === BLOCK_KINDS.TABLE) return patchTableCell(block, draft);
      if (block.kind === BLOCK_KINDS.PARAGRAPH) return patchParagraph(block, draft);
      return block;
    }),
  };
}
