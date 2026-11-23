// Single entry point for every write the editor can produce.
//
// Two families of operation live here and they are genuinely different:
//
//   part-string ops   text, style, geometry, z-order, delete, insert shape/table
//                     — pure string in, string out, via write.js
//   package ops       insert picture, group, add/duplicate/delete/move slide
//                     — they add or remove whole parts, add relationships, and edit
//                       [Content_Types].xml, so they need the package, not a string
//
// Both funnel through one function because the save has to happen exactly once. Saving
// twice produces a file whose second save silently drops the first batch's new parts.
//
// Ordering rule that matters: part-string ops read the part through `pkg.text()`, which
// returns staged text after a previous `setText`, so string edits compose correctly. A
// structural slide op invalidates the model's slide offsets, so it must run alone — that
// is enforced rather than documented, because a mixed batch would write to the wrong slide.

import { validateSlidePart, findShape, applyDeleteShape, applyGeometryEdit, applyStyleEdits,
  applyTextEdits, applyZOrderEdit, insertRectangle, insertTable, insertTextBox,
  materializeGeometry, applyLayoutGeometry } from './write.js';
import { applyGroupShapes, applyInsertPicture } from './insert.js';
import { addSlide, deleteSlide, duplicateSlide, moveSlide } from './slides.js';

const STRUCTURAL_OPS = new Set(['addSlide', 'deleteSlide', 'duplicateSlide', 'moveSlide']);

const PART_STRING_OPS = new Set([
  'text', 'style', 'geometry', 'zorder', 'delete', 'insertText', 'insertShape', 'insertTable',
  // Inherited-geometry branches. `only` copies the layout box onto this slide as a local
  // override; `all` edits the layout so every slide using it moves together. They are
  // separate operations because the blast radius is different, and decision D5 requires the
  // user to pick one explicitly rather than have a default applied silently.
  'materializeGeometry', 'layoutGeometry',
]);

export function isStructuralOp(op) {
  return STRUCTURAL_OPS.has(op);
}

/** Reject a batch that mixes structural and non-structural ops before anything is written. */
export function validateBatch(ops) {
  const list = ops || [];
  if (list.length === 0) return { structural: [] };
  const structural = list.filter(op => STRUCTURAL_OPS.has(op.op));
  if (structural.length > 0 && list.length > structural.length) {
    throw new Error('页面的增删和元素编辑不能放在同一次写入里，请先保存其中一项');
  }
  if (structural.length > 1) {
    throw new Error('一次只能做一项页面操作');
  }
  for (const op of list) {
    if (!STRUCTURAL_OPS.has(op.op) && !PART_STRING_OPS.has(op.op)
      && op.op !== 'insertPicture' && op.op !== 'group') {
      throw new Error(`不支持的写入类型：${op.op}`);
    }
  }
  return { structural };
}

async function applyPartStringOp(model, op) {
  const slide = model.slides[op.slideIndex];
  if (!slide) throw new Error(`找不到第 ${op.slideIndex + 1} 页`);

  // This operation changes the layout part rather than the slide. Resolve the target
  // through the slide so callers keep using the same {slideIndex, elId} shape, then
  // stage the text on the layout part. Falling through to the slide writer below used
  // to report no change even though the batch validator accepted the operation.
  if (op.op === 'layoutGeometry') {
    if (!slide.layoutPart) throw new Error('这一页没有关联版式，无法改到所有页');
    const shape = op.elId ? findShape(slide, op.elId) : null;
    if (!shape) return null;
    const original = await model.pkg.text(slide.layoutPart);
    const xml = applyLayoutGeometry(
      original,
      op.payload?.placeholderType || shape.placeholderType,
      op.payload?.box,
    );
    if (xml === original) return null;
    const problems = validateSlidePart(xml);
    if (problems.length > 0) {
      throw new Error(`写入前的检查未通过：${problems.join('；')}`);
    }
    model.pkg.setText(slide.layoutPart, xml);
    return { part: slide.layoutPart };
  }

  let xml = await model.pkg.text(slide.part);
  const original = xml;
  const shape = op.elId ? findShape(slide, op.elId) : null;

  switch (op.op) {
    case 'text':
      if (!shape) return null;
      xml = applyTextEdits(xml, shape, op.payload);
      break;
    case 'style':
      if (!shape) return null;
      xml = applyStyleEdits(xml, shape, op.payload);
      break;
    case 'geometry':
      if (!shape) return null;
      // An inherited shape's box lives on the layout. Writing coordinates here would
      // detach it from the layout for this slide only, which the user never asked for.
      if (shape.inherited) return { skipped: 'inherited-geometry' };
      xml = applyGeometryEdit(xml, shape, op.payload);
      break;
    case 'materializeGeometry': {
      if (!shape) return null;
      const box = op.payload;
      xml = materializeGeometry(xml, shape, box);
      break;
    }
    case 'zorder':
      if (!shape) return null;
      xml = applyZOrderEdit(xml, shape, op.payload.direction);
      break;
    case 'delete':
      if (!shape) return null;
      xml = applyDeleteShape(xml, shape);
      break;
    case 'insertText':
      xml = insertTextBox(xml, op.payload);
      break;
    case 'insertShape':
      xml = insertRectangle(xml, op.payload);
      break;
    case 'insertTable':
      xml = insertTable(xml, op.payload);
      break;
    default:
      return null;
  }

  if (xml === original) return null;
  const problems = validateSlidePart(xml);
  if (problems.length > 0) {
    throw new Error(`写入前的检查未通过：${problems.join('；')}`);
  }
  model.pkg.setText(slide.part, xml);
  return { part: slide.part };
}

/**
 * Apply a batch and save once.
 *
 * Returns `{ changed, bytes, applied, skipped }`. `skipped` carries reasons rather than
 * silently dropping operations, so the caller can tell the user "the position of an
 * inherited shape was not written" instead of reporting a clean save.
 */
export async function applyOps(model, ops) {
  const list = ops || [];
  const { structural } = validateBatch(list);
  const skipped = [];
  let applied = 0;

  if (structural.length === 1) {
    const op = structural[0];
    if (op.op === 'addSlide') await addSlide(model, op.payload || {});
    else if (op.op === 'deleteSlide') await deleteSlide(model, op.slideIndex);
    else if (op.op === 'duplicateSlide') await duplicateSlide(model, op.slideIndex);
    else if (op.op === 'moveSlide') await moveSlide(model, op.payload.from, op.payload.to);
    const bytes = await model.pkg.save();
    return { changed: true, bytes, applied: 1, skipped };
  }

  for (const op of list) {
    if (op.op === 'insertPicture') {
      await applyInsertPicture(model, op.slideIndex, op.payload);
      applied += 1;
      continue;
    }
    if (op.op === 'group') {
      const slide = model.slides[op.slideIndex];
      if (!slide) throw new Error(`找不到第 ${op.slideIndex + 1} 页`);
      const xml = await model.pkg.text(slide.part);
      const shapes = (op.payload.elIds || [])
        .map(elId => findShape(slide, elId))
        .filter(Boolean);
      if (shapes.length < 2) throw new Error('至少要选两个元素才能组合');
      const next = applyGroupShapes(xml, shapes);
      const problems = validateSlidePart(next);
      if (problems.length > 0) throw new Error(`组合后检查未通过：${problems.join('；')}`);
      model.pkg.setText(slide.part, next);
      applied += 1;
      continue;
    }

    const result = await applyPartStringOp(model, op);
    if (result?.skipped) {
      skipped.push({ op: op.op, elId: op.elId, reason: result.skipped });
    } else if (result) {
      applied += 1;
    }
  }

  if (applied === 0) return { changed: false, bytes: null, applied: 0, skipped };
  const bytes = await model.pkg.save();
  return { changed: true, bytes, applied, skipped };
}

export { applyPartStringOp, STRUCTURAL_OPS, PART_STRING_OPS };
