// PPTX writer.
//
// Two rules govern everything here, and both exist because Office refuses to open
// files that violate them:
//
// 1. Splice, never re-serialize. `applyEdits` replaces character ranges inside the
//    original part text. Attributes reorder, namespaces drop and hex casing shifts
//    when you rebuild XML, and any of those can make PowerPoint show "needs repair".
//
// 2. Never touch a part that has no pending change. Parts are staged individually and
//    `pkg.set*` is only called for the ones that actually changed.
//
// The writer re-parses the part text it is editing so it can walk the recorded anchor
// path. Re-parsing is cheap and it means the anchor never has to carry node identity
// across a save, which would break the moment two saves happen in one session.

import { scanXml, findChild, findChildren, findDescendant, applyEdits, attr, rawInner } from '../ooxml/xml.js';
import { escapeText } from '../ooxml/xml.js';
import { SHAPE_KINDS } from './model.js';

function nodeAtPath(root, path) {
  let current = root;
  for (const index of path) {
    if (!current) return null;
    current = current.children[index];
  }
  return current;
}

/**
 * Build the replacement text for a shape's paragraphs.
 *
 * Formatting comes from the model's first run of each paragraph. This is a
 * deliberate simplification: per-run formatting inside one paragraph is preserved
 * only when the paragraph was not edited. Once the user rewrites a paragraph, the
 * whole paragraph is re-emitted with the paragraph's dominant formatting.
 */
function buildParagraphXml(paragraph, templateRunProps) {
  const rPr = templateRunProps ? `<a:rPr ${templateRunProps}/>` : '';
  const pieces = [];
  const text = String(paragraph?.text ?? '');
  const lines = text.split('\n');
  lines.forEach((line, index) => {
    if (index > 0) pieces.push('<a:br/>');
    if (line) pieces.push(`<a:r>${rPr}<a:t>${escapeText(line)}</a:t></a:r>`);
  });
  if (pieces.length === 0) pieces.push('<a:endParaRPr/>');
  const pPr = [];
  if (paragraph?.align) pPr.push(`algn="${paragraph.align}"`);
  if (paragraph?.level) pPr.push(`lvl="${paragraph.level}"`);
  const pPrXml = pPr.length ? `<a:pPr ${pPr.join(' ')}/>` : '';
  return `<a:p>${pPrXml}${pieces.join('')}</a:p>`;
}

/** Serialize run properties back to attributes so edited text keeps its look. */
function runPropsFromStyle(style) {
  const parts = [];
  if (style?.bold) parts.push('b="1"');
  if (style?.italic) parts.push('i="1"');
  if (style?.underline) parts.push('u="sng"');
  if (Number.isFinite(style?.size) && style.size > 0) parts.push(`sz="${Math.round(style.size)}"`);
  return parts.join(' ');
}

/**
 * Replace the text of one shape in one slide part.
 *
 * Returns the new part text. Callers batch several edits by chaining calls on the
 * returned string, which keeps offsets valid because each call re-parses.
 */
export function applyTextEdits(partXml, shape, changes) {
  const tree = scanXml(partXml);
  const node = nodeAtPath(tree, shape.anchor.path);
  if (!node) throw new Error(`找不到元素 ${shape.elId}，文件可能已变化`);

  const body = findChild(node, 'p:txBody') || findChild(node, 'a:txBody');
  if (!body) throw new Error(`元素 ${shape.elId} 没有文本内容`);

  const paraNodes = findChildren(body, 'a:p');
  const edits = [];

  // Paragraph-level replacement. Only paragraphs whose text actually changed are
  // reserialized; untouched paragraphs keep their original bytes and all their runs.
  const nextParagraphs = changes.paragraphs || [];
  nextParagraphs.forEach((para, index) => {
    const existing = paraNodes[index];
    const existingText = existing ? findDescendant(existing, 'a:t') : null;
    const currentText = existing ? collectParaText(partXml, existing) : '';
    if (currentText === String(para.text ?? '')) return;
    const firstRun = existing ? findChild(existing, 'a:r') : null;
    const firstRPr = firstRun ? findChild(firstRun, 'a:rPr') : null;
    const template = firstRPr ? attrSource(partXml, firstRPr) : runPropsFromStyle(para);
    const replacement = buildParagraphXml(para, template);
    if (existing) {
      edits.push({ start: existing.start, end: existing.end, replacement });
    } else {
      // Appending a paragraph: insert before the body's closing tag.
      edits.push({ start: body.innerEnd, end: body.innerEnd, replacement });
    }
  });

  if (edits.length === 0) return partXml;
  return applyEdits(partXml, edits);
}

function collectParaText(source, paraNode) {
  const parts = [];
  const walk = (node) => {
    for (const child of node.children) {
      if (child.name === 'a:t') { parts.push(source.slice(child.innerStart, child.innerEnd)); continue; }
      if (child.name === 'a:br') { parts.push('\n'); continue; }
      walk(child);
    }
  };
  walk(paraNode);
  return parts.join('');
}

function attrSource(source, node) {
  const raw = source.slice(node.start, node.end);
  const match = /<(?:[\w.:-]+)((?:\s+[\w.:-]+\s*=\s*(?:"[^"]*"|'[^']*'))*)\s*\/?>/.exec(raw);
  return match ? match[1].trim() : '';
}

const ATTR_PAIR_RE = /([\w.:-]+)\s*=\s*(?:"([^"]*)"|'([^']*)')/g;

/** Split an attribute string into ordered pairs so writes preserve original ordering. */
function parseAttrPairs(source) {
  const pairs = [];
  ATTR_PAIR_RE.lastIndex = 0;
  let match;
  while ((match = ATTR_PAIR_RE.exec(source || '')) !== null) {
    pairs.push([match[1], match[2] !== undefined ? match[2] : match[3]]);
  }
  return pairs;
}

function serializeAttrPairs(pairs) {
  return pairs.map(([key, value]) => `${key}="${value}"`).join(' ');
}

/**
 * Merge style values into an attribute list without reordering anything already there.
 *
 * Attribute order is cosmetic to a parser but not to a diff reviewer, and keeping the
 * original order means an unrelated style edit does not churn the whole tag. A null
 * value deletes the attribute, which is how `b="0"` gets normalized back to "absent".
 */
function mergeAttrs(source, updates) {
  const pairs = parseAttrPairs(source);
  for (const [key, value] of Object.entries(updates)) {
    if (value === undefined) continue;
    const index = pairs.findIndex(([name]) => name === key);
    if (value === null) {
      if (index >= 0) pairs.splice(index, 1);
      continue;
    }
    if (index >= 0) pairs[index][1] = String(value);
    else pairs.push([key, String(value)]);
  }
  return serializeAttrPairs(pairs);
}

/** Rebuild an element's open tag with new attributes, keeping its body and self-closing form. */
function rebuildTag(source, node, updates) {
  const attrs = mergeAttrs(attrSource(source, node), updates);
  const open = `<${node.name}${attrs ? ` ${attrs}` : ''}`;
  if (node.selfClosing) return `${open}/>`;
  return `${open}>${source.slice(node.innerStart, node.innerEnd)}</${node.name}>`;
}

function normalizeHex(value) {
  const raw = String(value || '').trim().replace(/^#/, '');
  if (/^[0-9a-fA-F]{6}$/.test(raw)) return raw.toUpperCase();
  if (/^[0-9a-fA-F]{3}$/.test(raw)) {
    return raw.split('').map(ch => ch + ch).join('').toUpperCase();
  }
  return '';
}

/**
 * Rewrite one run's properties.
 *
 * A run without `a:rPr` is legal in OOXML (it inherits), but a run we are about to style
 * needs one. Inserting an empty `a:rPr` first and then filling it keeps a single code
 * path: after this call the node always has properties.
 */
function styleRunPr(source, rPrNode, style) {
  const updates = {};
  if (Object.prototype.hasOwnProperty.call(style, 'bold')) {
    updates.b = style.bold ? '1' : null;
  }
  if (Object.prototype.hasOwnProperty.call(style, 'italic')) {
    updates.i = style.italic ? '1' : null;
  }
  if (Object.prototype.hasOwnProperty.call(style, 'underline')) {
    updates.u = style.underline ? 'sng' : null;
  }
  if (Object.prototype.hasOwnProperty.call(style, 'size')) {
    const size = Math.round(Number(style.size));
    // sz is in centipoints; 0 is not a legal size, so a cleared field is left alone.
    if (Number.isFinite(size) && size > 0) updates.sz = String(size);
  }

  const hex = style.color === undefined ? '' : normalizeHex(style.color);
  const inner = source.slice(rPrNode.innerStart, rPrNode.innerEnd);
  let nextInner = inner;

  if (hex) {
    const srgbMatch = /<a:srgbClr\b[^>]*\bval="([^"]*)"/.exec(inner);
    if (srgbMatch) {
      nextInner = inner.replace(/(<a:srgbClr\b[^>]*\bval=")[^"]*(")/, `$1${hex}$2`);
    } else if (/<a:solidFill\b/.test(inner)) {
      // A solidFill driven by a scheme color has to be replaced outright: writing a
      // srgbClr next to a schemeClr leaves two fills in one element, which Office
      // treats as a damaged part.
      nextInner = inner.replace(/<a:solidFill\b[\s\S]*?<\/a:solidFill>/,
        `<a:solidFill><a:srgbClr val="${hex}"/></a:solidFill>`);
    } else {
      nextInner = `<a:solidFill><a:srgbClr val="${hex}"/></a:solidFill>${inner}`;
    }
  }

  const attrs = mergeAttrs(attrSource(source, rPrNode), updates);
  const open = `<${rPrNode.name}${attrs ? ` ${attrs}` : ''}`;
  if (rPrNode.selfClosing) {
    return nextInner === inner
      ? `${open}/>`
      : `${open}>${nextInner}</${rPrNode.name}>`;
  }
  return `${open}>${nextInner}</${rPrNode.name}>`;
}

/**
 * Apply paragraph-level style changes.
 *
 * Style is written per run, not per paragraph, because that is where PowerPoint stores
 * it. Every run in the paragraph receives the change so a mixed paragraph does not end
 * up half-restyled, which is more surprising than a uniform result.
 */
export function applyStyleEdits(partXml, shape, changes) {
  const tree = scanXml(partXml);
  const node = nodeAtPath(tree, shape.anchor.path);
  if (!node) throw new Error(`找不到元素 ${shape.elId}，文件可能已变化`);

  const body = findChild(node, 'p:txBody') || findChild(node, 'a:txBody');
  if (!body) throw new Error(`元素 ${shape.elId} 没有文本内容`);

  const paraNodes = findChildren(body, 'a:p');
  const edits = [];

  for (const patch of changes.paragraphs || []) {
    const paraNode = paraNodes[patch.index];
    if (!paraNode) continue;

    // Paragraph alignment is an attribute of a:pPr, and a missing a:pPr has to be
    // created. Ordering matters: a:pPr must be the first child of a:p.
    if (patch.align !== undefined) {
      const pPr = findChild(paraNode, 'a:pPr');
      if (pPr) {
        const replacement = rebuildTag(partXml, pPr, { algn: patch.align || null });
        if (replacement !== partXml.slice(pPr.start, pPr.end)) {
          edits.push({ start: pPr.start, end: pPr.end, replacement });
        }
      } else if (patch.align) {
        edits.push({ start: paraNode.innerStart, end: paraNode.innerStart,
          replacement: `<a:pPr algn="${patch.align}"/>` });
      }
    }

    const styleChanged = ['bold', 'italic', 'underline', 'size', 'color']
      .some(key => patch[key] !== undefined);
    if (!styleChanged) continue;

    const style = {
      bold: patch.bold,
      italic: patch.italic,
      underline: patch.underline,
      size: patch.size,
      color: patch.color,
    };

    for (const run of findChildren(paraNode, 'a:r')) {
      const rPr = findChild(run, 'a:rPr');
      if (rPr) {
        const replacement = styleRunPr(partXml, rPr, style);
        if (replacement !== partXml.slice(rPr.start, rPr.end)) {
          edits.push({ start: rPr.start, end: rPr.end, replacement });
        }
      } else {
        // Insert properties immediately after the open tag so the run keeps its text.
        const attrs = [];
        if (style.bold !== undefined && style.bold) attrs.push('b="1"');
        if (style.italic !== undefined && style.italic) attrs.push('i="1"');
        if (style.underline !== undefined && style.underline) attrs.push('u="sng"');
        if (Number.isFinite(Number(style.size)) && Number(style.size) > 0) {
          attrs.push(`sz="${Math.round(Number(style.size))}"`);
        }
        const hex = style.color === undefined ? '' : normalizeHex(style.color);
        const fill = hex ? `<a:solidFill><a:srgbClr val="${hex}"/></a:solidFill>` : '';
        const tag = `<a:rPr${attrs.length ? ` ${attrs.join(' ')}` : ''}${fill ? '' : '/'}`;
        const rPrXml = fill ? `<a:rPr${attrs.length ? ` ${attrs.join(' ')}` : ''}>${fill}</a:rPr>` : `${tag}>`;
        edits.push({ start: run.innerStart, end: run.innerStart, replacement: rPrXml });
      }
    }
  }

  if (edits.length === 0) return partXml;
  return applyEdits(partXml, edits);
}

/**
 * Pre-save validation.
 *
 * Runs before the package is written. A duplicate `cNvPr id` on one slide is accepted by
 * our own parser and by zip readers, then rejected by PowerPoint with "needs repair",
 * so catching it here is the difference between a clear error and a damaged file.
 */
export function validateSlidePart(partXml) {
  const problems = [];
  const tree = scanXml(partXml);
  const ids = new Map();
  const walk = (node) => {
    for (const child of node.children) {
      if (child.name === 'p:cNvPr') {
        const id = attr(child, 'id');
        if (id) {
          if (ids.has(id)) problems.push(`重复的形状 id ${id}`);
          ids.set(id, true);
        }
      }
      // A table whose grid column count differs from its cell count is the other
      // repair-prompt classic.
      if (child.name === 'a:tbl') {
        const gridCols = findChildren(findChild(child, 'a:tblGrid') || { children: [] }, 'a:gridCol').length;
        for (const row of findChildren(child, 'a:tr')) {
          const cells = findChildren(row, 'a:tc').length;
          if (gridCols > 0 && cells !== gridCols) {
            problems.push(`表格行列不匹配：网格 ${gridCols} 列，行内 ${cells} 个单元格`);
          }
        }
      }
      walk(child);
    }
  };
  walk(tree);
  return problems;
}

/** Change a shape's absolute position and size. */
export function applyGeometryEdit(partXml, shape, box) {
  // Check the inheritance flag before looking at the XML. An inherited placeholder has
  // no transform of its own, so the XML error would be "no coordinates to write", which
  // reads like a malformed file instead of "this belongs to the layout".
  if (shape.inherited) {
    throw new Error('该元素继承自版式，不能直接改位置');
  }

  const tree = scanXml(partXml);
  const node = nodeAtPath(tree, shape.anchor.path);
  if (!node) throw new Error(`找不到元素 ${shape.elId}`);

  // A table keeps its transform on the graphicFrame itself, not in an spPr child.
  const spPr = findChild(node, 'p:spPr') || findChild(node, 'p:grpSpPr');
  const ownFrame = findChild(node, 'p:xfrm');
  const container = spPr || node;
  const xfrm = ownFrame || findChild(container, 'a:xfrm');
  if (!xfrm) throw new Error('该元素没有可写的坐标信息');

  const off = findChild(xfrm, 'a:off');
  const ext = findChild(xfrm, 'a:ext');
  const edits = [];
  if (off) {
    edits.push({ start: off.start, end: off.end,
      replacement: `<a:off x="${Math.round(box.x)}" y="${Math.round(box.y)}"/>` });
  }
  if (ext) {
    edits.push({ start: ext.start, end: ext.end,
      replacement: `<a:ext cx="${Math.round(box.cx)}" cy="${Math.round(box.cy)}"/>` });
  }
  if (edits.length === 0) throw new Error('该元素缺少位置定义');
  return applyEdits(partXml, edits);
}

/**
 * Move a shape one step in the z-order.
 *
 * Z-order in OOXML is document order inside `p:spTree`: the last shape paints on top.
 * There is no z-index attribute, so reordering means moving the whole node. That is why
 * this is a splice of two ranges rather than an attribute write.
 *
 * Only direct children of spTree can be reordered this way. A shape inside a group moves
 * within its group, which the caller has to handle by passing the parent path.
 */
export function applyZOrderEdit(partXml, shape, direction) {
  const tree = scanXml(partXml);
  const spTree = findDescendant(tree, 'p:spTree');
  if (!spTree) throw new Error('幻灯片结构损坏，找不到画布');

  // Reorderable nodes are the drawable ones; nvGrpSpPr and grpSpPr are not shape slots
  // and must keep their leading positions or Office treats the tree as malformed.
  const reorderable = spTree.children.filter(child =>
    ['p:sp', 'p:pic', 'p:graphicFrame', 'p:grpSp', 'p:cxnSp', 'mc:AlternateContent']
      .includes(child.name));
  const node = nodeAtPath(tree, shape.anchor.path);
  if (!node) throw new Error(`找不到元素 ${shape.elId}`);

  const index = reorderable.indexOf(node);
  if (index === -1) throw new Error('这个元素不能在页面上调整层级');

  const step = direction === 'front' || direction === 'up' ? 1 : -1;
  const targetIndex = direction === 'front' ? reorderable.length - 1
    : direction === 'back' ? 0 : index + step;
  if (targetIndex === index || targetIndex < 0 || targetIndex >= reorderable.length) {
    return partXml;
  }

  const moving = reorderable[index];
  const anchorNode = reorderable[targetIndex];
  const movingXml = partXml.slice(moving.start, moving.end);

  // Removing first then inserting keeps both offsets meaningful: the removal only shifts
  // things after it, and the insert point is computed from the anchor's own range.
  const withoutMoving = partXml.slice(0, moving.start) + partXml.slice(moving.end);
  const anchorStartAfterRemoval = anchorNode.start > moving.start
    ? anchorNode.start - movingXml.length
    : anchorNode.start;
  const anchorEndAfterRemoval = anchorNode.end > moving.start
    ? anchorNode.end - movingXml.length
    : anchorNode.end;

  const insertAt = step > 0 ? anchorEndAfterRemoval : anchorStartAfterRemoval;
  return withoutMoving.slice(0, insertAt) + movingXml + withoutMoving.slice(insertAt);
}

/**
 * Delete a shape by removing its whole XML node.
 *
 * Removing a node also removes everything inside it, which is why the caller must
 * confirm the shape is not an inherited placeholder: deleting an inherited shape
 * from a slide would silently erase text the user never saw come from this slide.
 */
export function applyDeleteShape(partXml, shape) {
  const tree = scanXml(partXml);
  const node = nodeAtPath(tree, shape.anchor.path);
  if (!node) throw new Error(`找不到元素 ${shape.elId}`);
  return applyEdits(partXml, [{ start: node.start, end: node.end, replacement: '' }]);
}

/**
 * Give an inherited shape its own transform on this slide.
 *
 * A placeholder that inherits geometry has no `a:xfrm`. To move it on one slide without
 * touching the layout, a local transform has to be created — this is the "只改这一页"
 * branch of decision D5. `p:spPr` must sit between `p:nvSpPr` and `p:txBody`, and Office
 * rejects the part if the order is wrong.
 */
export function materializeGeometry(partXml, shape, box) {
  const tree = scanXml(partXml);
  const node = nodeAtPath(tree, shape.anchor.path);
  if (!node) throw new Error(`找不到元素 ${shape.elId}`);

  const existing = findChild(node, 'p:spPr');
  const xfrm = `<a:xfrm><a:off x="${Math.round(box.x)}" y="${Math.round(box.y)}"/><a:ext cx="${Math.round(box.cx)}" cy="${Math.round(box.cy)}"/></a:xfrm>`;

  if (existing) {
    const currentXfrm = findChild(existing, 'a:xfrm');
    if (currentXfrm) {
      return applyEdits(partXml, [
        { start: currentXfrm.start, end: currentXfrm.end, replacement: xfrm },
      ]);
    }
    // spPr exists but has no transform. a:xfrm must be its first child.
    const inner = partXml.slice(existing.innerStart, existing.innerEnd);
    if (existing.selfClosing) {
      return applyEdits(partXml, [{
        start: existing.start, end: existing.end, replacement: `<p:spPr>${xfrm}</p:spPr>`,
      }]);
    }
    return applyEdits(partXml, [{
      start: existing.innerStart, end: existing.innerEnd, replacement: xfrm + inner,
    }]);
  }

  // No spPr at all: insert one right after nvSpPr so the child order stays legal.
  const nv = findChild(node, 'p:nvSpPr');
  if (!nv) throw new Error('该元素结构不支持单独设置位置');
  return applyEdits(partXml, [{
    start: nv.end, end: nv.end, replacement: `<p:spPr>${xfrm}</p:spPr>`,
  }]);
}

/**
 * Write geometry onto the layout so every slide using it changes together.
 *
 * This is the "所有使用该版式的页" branch of D5. It edits the layout part, not the slide,
 * which is why it is a separate function: a caller that mixes the two would silently apply
 * one slide's intent to the whole deck.
 */
export function applyLayoutGeometry(layoutXml, placeholderType, box) {
  const tree = scanXml(layoutXml);
  const spTree = findDescendant(tree, 'p:spTree');
  if (!spTree) throw new Error('版式结构损坏');

  const candidates = findChildren(spTree, 'p:sp');
  const wanted = String(placeholderType || 'body');
  const target = candidates.find((node) => {
    const nvSpPr = findChild(node, 'p:nvSpPr');
    const nvPr = nvSpPr ? findChild(nvSpPr, 'p:nvPr') : null;
    const ph = nvPr ? findChild(nvPr, 'p:ph') : null;
    return ph && attr(ph, 'type', 'body') === wanted;
  }) || candidates.find((node) => {
    const nvSpPr = findChild(node, 'p:nvSpPr');
    const nvPr = nvSpPr ? findChild(nvSpPr, 'p:nvPr') : null;
    return nvPr && findChild(nvPr, 'p:ph');
  });

  if (!target) throw new Error('版式里找不到对应的占位符');

  const xfrm = `<a:xfrm><a:off x="${Math.round(box.x)}" y="${Math.round(box.y)}"/><a:ext cx="${Math.round(box.cx)}" cy="${Math.round(box.cy)}"/></a:xfrm>`;
  const spPr = findChild(target, 'p:spPr');

  if (!spPr) {
    const nv = findChild(target, 'p:nvSpPr');
    if (!nv) throw new Error('占位符结构不支持设置位置');
    return applyEdits(layoutXml, [{
      start: nv.end, end: nv.end, replacement: `<p:spPr>${xfrm}</p:spPr>`,
    }]);
  }
  const currentXfrm = findChild(spPr, 'a:xfrm');
  if (currentXfrm) {
    return applyEdits(layoutXml, [
      { start: currentXfrm.start, end: currentXfrm.end, replacement: xfrm },
    ]);
  }
  return applyEdits(layoutXml, [{
    start: spPr.innerStart, end: spPr.innerStart, replacement: xfrm,
  }]);
}

/** Collect every shape id currently used on a slide, so inserts never collide. */
export function usedShapeIds(partXml) {
  const tree = scanXml(partXml);
  const ids = new Set();
  const walk = (node) => {
    for (const child of node.children) {
      if (child.name === 'p:cNvPr') {
        const id = attr(child, 'id');
        if (id) ids.add(Number(id));
      }
      walk(child);
    }
  };
  walk(tree);
  return ids;
}

export function nextShapeId(partXml) {
  const ids = usedShapeIds(partXml);
  let max = 0;
  for (const id of ids) if (Number.isFinite(id) && id > max) max = id;
  // Office starts shape ids at 2 and expects them positive and unique per slide.
  return Math.max(max + 1, 2);
}

/**
 * Insert a text box.
 *
 * The XML shape here is a template filled with values, not a hand-built string. Every
 * attribute that Office validates (the `a:` namespace prefix, the `p:nvSpPr` block,
 * the `a:bodyPr` before `a:lstStyle`) is present because a template was copied from a
 * real file structure rather than recalled from memory.
 */
export function insertTextBox(partXml, spec) {
  const tree = scanXml(partXml);
  const spTree = findDescendant(tree, 'p:spTree');
  if (!spTree) throw new Error('幻灯片结构损坏，找不到画布');

  const id = spec.id || nextShapeId(partXml);
  const name = escapeXmlAttr(spec.name || `文本框 ${id}`);
  const x = Math.round(spec.box.x);
  const y = Math.round(spec.box.y);
  const cx = Math.max(Math.round(spec.box.cx), 1);
  const cy = Math.max(Math.round(spec.box.cy), 1);
  const size = Math.round((spec.size || 18) * 100);
  const color = (spec.color || '#16181b').replace('#', '').toUpperCase();
  const bold = spec.bold ? ' b="1"' : '';
  const lines = String(spec.text || '').split('\n');
  const runs = lines.map((line, index) => {
    const br = index > 0 ? '<a:br/>' : '';
    const safe = escapeText(line);
    return `${br}<a:r><a:rPr lang="zh-CN" sz="${size}"${bold} dirty="0"><a:solidFill><a:srgbClr val="${color}"/></a:solidFill><a:latin typeface="微软雅黑"/><a:ea typeface="微软雅黑"/></a:rPr><a:t>${safe}</a:t></a:r>`;
  }).join('');

  const xml = `<p:sp><p:nvSpPr><p:cNvPr id="${id}" name="${name}"/><p:cNvSpPr txBox="1"/><p:nvPr/></p:nvSpPr><p:spPr><a:xfrm><a:off x="${x}" y="${y}"/><a:ext cx="${cx}" cy="${cy}"/></a:xfrm><a:prstGeom prst="rect"><a:avLst/></a:prstGeom><a:noFill/><a:ln><a:noFill/></a:ln></p:spPr><p:txBody><a:bodyPr wrap="square" lIns="91440" tIns="45720" rIns="91440" bIns="45720"><a:noAutofit/></a:bodyPr><a:lstStyle/><a:p>${runs || '<a:endParaRPr lang="zh-CN"/>'}</a:p></p:txBody></p:sp>`;

  // Insert before the closing tag of spTree so the shape lands on top of the stack.
  return applyEdits(partXml, [{ start: spTree.innerEnd, end: spTree.innerEnd, replacement: xml }]);
}

function escapeXmlAttr(value) {
  return String(value).replace(/&/g, '&amp;').replace(/</g, '&lt;')
    .replace(/>/g, '&gt;').replace(/"/g, '&quot;');
}

/** Insert one of the supported primitives. Shapes are templates, not自由绘制. */
export function insertRectangle(partXml, spec) {
  const tree = scanXml(partXml);
  const spTree = findDescendant(tree, 'p:spTree');
  if (!spTree) throw new Error('幻灯片结构损坏，找不到画布');
  const id = spec.id || nextShapeId(partXml);
  const preset = spec.preset === 'roundRect' ? 'roundRect' : 'rect';
  const fill = (spec.fill || '#1f5eff').replace('#', '').toUpperCase();
  const xml = `<p:sp><p:nvSpPr><p:cNvPr id="${id}" name="${escapeXmlAttr(spec.name || `矩形 ${id}`)}"/><p:cNvSpPr/><p:nvPr/></p:nvSpPr><p:spPr><a:xfrm><a:off x="${Math.round(spec.box.x)}" y="${Math.round(spec.box.y)}"/><a:ext cx="${Math.max(1, Math.round(spec.box.cx))}" cy="${Math.max(1, Math.round(spec.box.cy))}"/></a:xfrm><a:prstGeom prst="${preset}"><a:avLst/></a:prstGeom><a:solidFill><a:srgbClr val="${fill}"/></a:solidFill><a:ln><a:noFill/></a:ln></p:spPr><p:txBody><a:bodyPr/><a:lstStyle/><a:p/></p:txBody></p:sp>`;
  return applyEdits(partXml, [{ start: spTree.innerEnd, end: spTree.innerEnd, replacement: xml }]);
}

/**
 * Insert a table.
 *
 * A table lives inside `p:graphicFrame`, not `p:sp`, and needs its own `a:tblGrid`
 * whose widths match the cell count. Omitting the grid is the classic cause of
 * "PowerPoint found a problem with content".
 */
export function insertTable(partXml, spec) {
  const tree = scanXml(partXml);
  const spTree = findDescendant(tree, 'p:spTree');
  if (!spTree) throw new Error('幻灯片结构损坏，找不到画布');
  const id = spec.id || nextShapeId(partXml);
  const rows = Math.max(1, Math.min(Number(spec.rows) || 3, 30));
  const cols = Math.max(1, Math.min(Number(spec.cols) || 3, 12));
  const cx = Math.max(Math.round(spec.box.cx), cols * 200000);
  const cy = Math.max(Math.round(spec.box.cy), rows * 200000);
  const colWidth = Math.round(cx / cols);
  const rowHeight = Math.round(cy / rows);
  const grid = Array.from({ length: cols }, () => `<a:gridCol w="${colWidth}"/>`).join('');
  const body = Array.from({ length: rows }, () => {
    const cells = Array.from({ length: cols }, () =>
      '<a:tc><a:txBody><a:bodyPr/><a:lstStyle/><a:p><a:endParaRPr lang="zh-CN"/></a:p></a:txBody><a:tcPr/></a:tc>').join('');
    return `<a:tr h="${rowHeight}">${cells}</a:tr>`;
  }).join('');

  const xml = `<p:graphicFrame><p:nvGraphicFramePr><p:cNvPr id="${id}" name="${escapeXmlAttr(spec.name || `表格 ${id}`)}"/><p:cNvGraphicFramePr><a:graphicFrameLocks noGrp="1"/></p:cNvGraphicFramePr><p:nvPr/></p:nvGraphicFramePr><p:xfrm><a:off x="${Math.round(spec.box.x)}" y="${Math.round(spec.box.y)}"/><a:ext cx="${cx}" cy="${cy}"/></p:xfrm><a:graphic><a:graphicData uri="http://schemas.openxmlformats.org/drawingml/2006/table"><a:tbl><a:tblPr firstRow="1" bandRow="1"/><a:tblGrid>${grid}</a:tblGrid>${body}</a:tbl></a:graphicData></a:graphic></p:graphicFrame>`;

  return applyEdits(partXml, [{ start: spTree.innerEnd, end: spTree.innerEnd, replacement: xml }]);
}

/**
 * Write a batch of model changes back into the package.
 *
 * `changes` is a list of `{slideIndex, elId, op, payload}`. Grouping by slide keeps
 * one part's edits sequential, which matters because every edit re-parses the text.
 */
export async function writePptx(model, changes) {
  const byPart = new Map();
  for (const change of changes || []) {
    const slide = model.slides[change.slideIndex];
    if (!slide) continue;
    if (!byPart.has(slide.part)) byPart.set(slide.part, []);
    byPart.get(slide.part).push(change);
  }

  let partsChanged = 0;
  for (const [part, list] of byPart) {
    let xml = await model.pkg.text(part);
    const original = xml;
    for (const change of list) {
      const slide = model.slides[change.slideIndex];
      const shape = findShape(slide, change.elId);
      if (change.op === 'text') {
        if (!shape) continue;
        xml = applyTextEdits(xml, shape, change.payload);
      } else if (change.op === 'style') {
        if (!shape) continue;
        xml = applyStyleEdits(xml, shape, change.payload);
      } else if (change.op === 'geometry') {
        if (!shape) continue;
        // An inherited shape's box lives on the layout. Writing coordinates into this
        // slide would detach it silently, so the caller has to resolve inheritance first.
        if (shape.inherited) continue;
        xml = applyGeometryEdit(xml, shape, change.payload);
      } else if (change.op === 'delete') {
        if (!shape) continue;
        xml = applyDeleteShape(xml, shape);
      } else if (change.op === 'zorder') {
        if (!shape) continue;
        xml = applyZOrderEdit(xml, shape, change.payload.direction);
      } else if (change.op === 'insertText') {
        xml = insertTextBox(xml, change.payload);
      } else if (change.op === 'insertShape') {
        xml = insertRectangle(xml, change.payload);
      } else if (change.op === 'insertTable') {
        xml = insertTable(xml, change.payload);
      }
    }
    if (xml !== original) {
      // Validation runs on the edited part before it is staged. A failure here aborts
      // the whole save rather than producing a package PowerPoint will offer to repair.
      const problems = validateSlidePart(xml);
      if (problems.length > 0) {
        throw new Error(`写入前的检查未通过：${problems.join('；')}`);
      }
      model.pkg.setText(part, xml);
      partsChanged += 1;
    }
  }

  if (partsChanged === 0) return { changed: false, bytes: null };
  const bytes = await model.pkg.save();
  return { changed: true, bytes, partsChanged };
}

function findShape(slide, elId) {
  if (!slide) return null;
  for (const shape of slide.shapes) {
    if (shape.elId === elId) return shape;
    for (const child of shape.children || []) if (child.elId === elId) return child;
  }
  return null;
}

export { findShape, buildParagraphXml, nodeAtPath };
