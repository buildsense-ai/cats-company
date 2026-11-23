// DOCX writer.
//
// Same rule as the PPTX writer, and for the same reason: splice character ranges, never
// re-serialize a part. Word files carry style ids, rsid stamps, namespace prefixes and
// numbering references that no generic serializer reproduces, and any of those changing is
// enough for Word to open the document in a "repaired" state.
//
// What is different from PPTX: text lives in runs, and a paragraph's visible text is the
// concatenation of its runs. Rewriting a paragraph therefore means deciding what happens to
// the runs that were there. This writer keeps the first run's formatting as the template and
// emits one run per line, which is the behaviour a user expects when they retype a paragraph.

import { scanXml, findChild, findChildren, findDescendant, applyEdits, attr } from '../ooxml/xml.js';
import { escapeText } from '../ooxml/xml.js';
import { ptToHalfPoints, cmToTwips } from './model.js';

function nodeAtPath(root, path) {
  let current = root;
  for (const index of path) {
    if (!current) return null;
    current = current.children[index];
  }
  return current;
}

const ATTR_PAIR_RE = /([\w.:-]+)\s*=\s*(?:"([^"]*)"|'([^']*)')/g;

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

/** Merge attributes without reordering the ones already present. */
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

function attrSource(source, node) {
  const raw = source.slice(node.start, node.end);
  const match = /<[^>]*?((?:\s[\w.:-]+\s*=\s*(?:"[^"]*"|'[^']*'))*)\s*\/?>/.exec(raw);
  const attrs = match ? match[1].trim() : '';
  // Self-closing is a legitimate form for w:rPr in many real files; merge and re-emit without
  // the slash so the caller decides the shape.
  return attrs;
}

function rebuildTag(source, node, updates) {
  const attrs = mergeAttrs(attrSource(source, node), updates);
  const open = `<${node.name}${attrs ? ` ${attrs}` : ''}`;
  if (node.selfClosing) return `${open}/>`;
  return `${open}>${source.slice(node.innerStart, node.innerEnd)}</${node.name}>`;
}

/**
 * Build one `w:r` for a line of text.
 *
 * `xml:space="preserve"` is not optional: without it Word strips leading and trailing spaces,
 * so every edit of a paragraph with an indented first line silently loses the indent.
 */
function buildRun(text, rPrXml) {
  const inner = rPrXml || '';
  return `<w:r>${inner}<w:t xml:space="preserve">${escapeText(text)}</w:t></w:r>`;
}

/** Serialize run properties as they stand, so a text edit keeps the original look. */
function existingRunProps(source, runNode) {
  const rPr = findChild(runNode, 'w:rPr');
  if (!rPr) return '';
  return source.slice(rPr.start, rPr.end);
}

/**
 * Replace the text of one table cell.
 *
 * A cell holds one or more paragraphs, and Word treats an empty cell as a cell with one empty
 * paragraph rather than no paragraph. So the first paragraph is rewritten in place and any extra
 * paragraphs after it are removed, which keeps the "one logical line of text" model the panel
 * exposes. Writing zero paragraphs would produce a cell Word considers damaged.
 */
export function applyTableCellText(partXml, cell, text) {
  if (!cell || !cell.firstParagraphPath) throw new Error('这个单元格没有可写的位置');

  const tree = scanXml(partXml);
  const firstPara = nodeAtPath(tree, cell.firstParagraphPath);
  if (!firstPara) throw new Error('找不到单元格里的段落，文档可能已变化');

  // Every paragraph sibling inside the same cell, so extras can be trimmed in the same edit.
  const container = firstPara.parent;
  const siblings = container ? findChildren(container, 'w:p') : [firstPara];
  const trailing = siblings.slice(siblings.indexOf(firstPara) + 1);

  const runNodes = [];
  const collect = (current) => {
    for (const child of current.children) {
      if (child.name === 'w:r') runNodes.push(child);
      else if (child.name === 'w:hyperlink' || child.name === 'w:ins' || child.name === 'w:smartTag') collect(child);
    }
  };
  collect(firstPara);

  const template = runNodes.length > 0 ? existingRunProps(partXml, runNodes[0]) : '';
  const lines = String(text ?? '').split('\n');
  const replacement = lines.map(line => buildRun(line, template)).join('<w:r><w:br/></w:r>');

  const edits = [];
  if (runNodes.length > 0) {
    edits.push({ start: runNodes[0].start, end: runNodes[runNodes.length - 1].end,
      replacement });
  } else {
    const pPr = findChild(firstPara, 'w:pPr');
    const insertAt = pPr ? pPr.end : firstPara.innerStart;
    edits.push({ start: insertAt, end: insertAt, replacement });
  }
  for (const extra of trailing) {
    edits.push({ start: extra.start, end: extra.end, replacement: '' });
  }
  return applyEdits(partXml, edits);
}

/** Replace a paragraph's text.
 *
 * The paragraph's own properties (`w:pPr`) are left completely untouched, which is what keeps
 * numbering, style and section behaviour intact when only the words change.
 */
export function applyParagraphText(partXml, block, text) {
  const tree = scanXml(partXml);
  const node = nodeAtPath(tree, block.path);
  if (!node) throw new Error(`找不到段落 ${block.id}，文档可能已变化`);

  const runNodes = [];
  const collect = (current) => {
    for (const child of current.children) {
      if (child.name === 'w:r') runNodes.push(child);
      else if (child.name === 'w:hyperlink' || child.name === 'w:ins' || child.name === 'w:smartTag') collect(child);
    }
  };
  collect(node);

  const lines = String(text ?? '').split('\n');
  const template = runNodes.length > 0 ? existingRunProps(partXml, runNodes[0]) : '';
  const replacement = lines.map(line => buildRun(line, template)).join('<w:r><w:br/></w:r>');

  if (runNodes.length === 0) {
    // An empty paragraph has no run to replace. Insert after pPr, which must stay first.
    const pPr = findChild(node, 'w:pPr');
    const insertAt = pPr ? pPr.end : node.innerStart;
    return applyEdits(partXml, [{ start: insertAt, end: insertAt, replacement }]);
  }

  // Replace the span from the first run to the last, so interleaved bookmarks and proofing
  // marks between them do not survive as orphans around new text.
  const first = runNodes[0];
  const last = runNodes[runNodes.length - 1];
  return applyEdits(partXml, [{ start: first.start, end: last.end, replacement }]);
}

/**
 * Change one run property.
 *
 * Three distinct intents, kept explicit because collapsing them caused a real bug: passing
 * `null` for "bold on" made the writer delete the `<w:b/>` element, so setting bold turned bold
 * off. `remove` and "set no value" are different operations and must not share a sentinel.
 */
function setRunProperty(partXml, runNode, tag, mode, value) {
  const rPr = findChild(runNode, 'w:rPr');
  if (!rPr) return null;
  const target = findChild(rPr, tag);

  if (mode === 'remove') {
    return target ? { start: target.start, end: target.end, replacement: '' } : null;
  }

  const element = value === undefined ? `<${tag}/>` : `<${tag} w:val="${value}"/>`;
  if (target) {
    return { start: target.start, end: target.end, replacement: element };
  }

  // Insert in a stable order so the same edit twice produces the same bytes.
  const order = ['w:rFonts', 'w:b', 'w:i', 'w:u', 'w:color', 'w:sz'];
  const wanted = order.indexOf(tag);
  const later = wanted >= 0
    ? rPr.children.find(child => order.indexOf(child.name) > wanted)
    : null;
  const insertAt = later ? later.start : rPr.innerEnd;
  return { start: insertAt, end: insertAt, replacement: element };
}

/** Apply several properties to one `w:rPr` by rescanning after each splice. */
function applyRunPropsXml(rPrXml, style) {
  let current = rPrXml;
  const order = ['w:rFonts', 'w:b', 'w:i', 'w:u', 'w:color', 'w:sz'];
  for (const [tag, mode, value] of styleToEdits(style)) {
    let root = scanXml(current).children[0];
    if (!root) return current;
    if (root.selfClosing) {
      const openEnd = current.indexOf('>');
      const open = current.slice(0, openEnd).replace(/\/\s*$/, '');
      current = `${open}></${root.name}>`;
      root = scanXml(current).children[0];
    }

    const target = findChild(root, tag);
    if (mode === 'remove') {
      if (!target) continue;
      current = applyEdits(current, [{ start: target.start, end: target.end, replacement: '' }]);
      continue;
    }

    const element = value === undefined ? `<${tag}/>` : `<${tag} w:val="${value}"/>`;
    if (target) {
      current = applyEdits(current, [{ start: target.start, end: target.end, replacement: element }]);
      continue;
    }

    const wanted = order.indexOf(tag);
    const later = wanted >= 0
      ? root.children.find(child => order.indexOf(child.name) > wanted)
      : null;
    const insertAt = later ? later.start : root.innerEnd;
    current = applyEdits(current, [{ start: insertAt, end: insertAt, replacement: element }]);
  }
  return current;
}

/** Apply run-level formatting to every run in a paragraph. */
export function applyRunStyle(partXml, block, style) {
  const tree = scanXml(partXml);
  const node = nodeAtPath(tree, block.path);
  if (!node) throw new Error(`找不到段落 ${block.id}`);

  const runNodes = [];
  const collect = (current) => {
    for (const child of current.children) {
      if (child.name === 'w:r') runNodes.push(child);
      else if (child.name === 'w:hyperlink' || child.name === 'w:ins' || child.name === 'w:smartTag') collect(child);
    }
  };
  collect(node);
  if (runNodes.length === 0) return partXml;

  const edits = [];
  for (const runNode of runNodes) {
    const rPr = findChild(runNode, 'w:rPr');
    if (!rPr) {
      // A run without rPr inherits everything. Create one so the style has somewhere to live.
      const rPrXml = buildRunProps(style);
      if (rPrXml) {
        edits.push({ start: runNode.innerStart, end: runNode.innerStart,
          replacement: `<w:rPr>${rPrXml}</w:rPr>` });
      }
      continue;
    }
    const original = partXml.slice(rPr.start, rPr.end);
    const next = applyRunPropsXml(original, style);
    if (next !== original) {
      // One replacement per run. Applying each property against absolute offsets from the
      // original part put an insertion at `w:sz`'s start and the `w:sz` replacement at the same
      // range, which applyEdits correctly rejected as overlapping.
      edits.push({ start: rPr.start, end: rPr.end, replacement: next });
    }
  }

  if (edits.length === 0) return partXml;
  // Edits come from several runs; sort so applyEdits' end-to-start rule stays valid.
  return applyEdits(partXml, edits);
}

/**
 * Translate the panel's style object into property operations.
 *
 * Toggles carry an explicit on/off intent; `false` removes the element rather than writing
 * `w:val="0"`, which Word re-normalizes on save and would make an "unbold" look like a no-op.
 */
function styleToEdits(style) {
  const out = [];
  if (style.bold !== undefined) out.push(['w:b', style.bold ? 'set' : 'remove', undefined]);
  if (style.italic !== undefined) out.push(['w:i', style.italic ? 'set' : 'remove', undefined]);
  if (style.underline !== undefined) {
    out.push(['w:u', 'set', style.underline ? 'single' : 'none']);
  }
  if (style.size !== undefined) {
    const half = ptToHalfPoints(style.size);
    if (half > 0) out.push(['w:sz', 'set', String(half)]);
  }
  if (style.color !== undefined) {
    const hex = String(style.color || '').replace('#', '').toUpperCase();
    if (/^[0-9A-F]{6}$/.test(hex)) out.push(['w:color', 'set', hex]);
  }
  return out;
}

function buildRunProps(style) {
  const parts = [];
  if (style.bold) parts.push('<w:b/>');
  if (style.italic) parts.push('<w:i/>');
  if (style.underline !== undefined) {
    parts.push(`<w:u w:val="${style.underline ? 'single' : 'none'}"/>`);
  }
  const half = style.size !== undefined ? ptToHalfPoints(style.size) : 0;
  if (half > 0) parts.push(`<w:sz w:val="${half}"/>`);
  const hex = String(style.color || '').replace('#', '').toUpperCase();
  if (/^[0-9A-F]{6}$/.test(hex)) parts.push(`<w:color w:val="${hex}"/>`);
  return parts.join('');
}

/**
 * Change paragraph-level formatting.
 *
 * `w:pPr` must be the first child of `w:p`. A paragraph without one is legal in OOXML, so a
 * missing pPr is created rather than treated as an error — otherwise the most common case
 * (an untitled body paragraph) would be unformattable.
 */
export function applyParagraphProperties(partXml, block, props) {
  const tree = scanXml(partXml);
  const node = nodeAtPath(tree, block.path);
  if (!node) throw new Error(`找不到段落 ${block.id}`);

  const pPr = findChild(node, 'w:pPr');
  if (!pPr) {
    const parts = [];
    if (props.align) parts.push(`<w:jc w:val="${props.align}"/>`);
    if (props.indLeftCm !== undefined) {
      parts.push(`<w:ind w:left="${cmToTwips(props.indLeftCm)}"/>`);
    }
    if (parts.length === 0) return partXml;
    return applyEdits(partXml, [{
      start: node.innerStart, end: node.innerStart,
      replacement: `<w:pPr>${parts.join('')}</w:pPr>`,
    }]);
  }

  const edits = [];
  if (props.align !== undefined) {
    const jc = findChild(pPr, 'w:jc');
    if (jc) {
      const next = rebuildTag(partXml, jc, { 'w:val': props.align || null });
      if (next !== partXml.slice(jc.start, jc.end)) {
        edits.push({ start: jc.start, end: jc.end, replacement: next });
      }
    } else if (props.align) {
      edits.push({ start: pPr.innerEnd, end: pPr.innerEnd,
        replacement: `<w:jc w:val="${props.align}"/>` });
    }
  }
  if (props.indLeftCm !== undefined || props.indFirstCm !== undefined) {
    const ind = findChild(pPr, 'w:ind');
    const updates = {};
    if (props.indLeftCm !== undefined) updates['w:left'] = String(cmToTwips(props.indLeftCm));
    if (props.indFirstCm !== undefined) {
      updates['w:firstLine'] = props.indFirstCm > 0 ? String(cmToTwips(props.indFirstCm)) : null;
    }
    if (ind) {
      const next = rebuildTag(partXml, ind, updates);
      if (next !== partXml.slice(ind.start, ind.end)) {
        edits.push({ start: ind.start, end: ind.end, replacement: next });
      }
    } else {
      const attrs = Object.entries(updates).filter(([, value]) => value !== null)
        .map(([key, value]) => `${key}="${value}"`).join(' ');
      if (attrs) {
        edits.push({ start: pPr.innerEnd, end: pPr.innerEnd, replacement: `<w:ind ${attrs}/>` });
      }
    }
  }

  if (edits.length === 0) return partXml;
  return applyEdits(partXml, edits);
}

/**
 * Append a paragraph after an existing block.
 *
 * Cloned from the anchor's own `w:pPr` when available: a new paragraph in a numbered list
 * should continue the list, and inventing a bare `w:p` would silently break the numbering.
 */
export function insertParagraph(partXml, block, text) {
  const tree = scanXml(partXml);
  const node = nodeAtPath(tree, block?.path || []);
  const body = findDescendant(tree, 'w:body');
  if (!body) throw new Error('文档缺少正文');

  const pPr = node ? findChild(node, 'w:pPr') : null;
  const pPrXml = pPr ? partXml.slice(pPr.start, pPr.end) : '';
  const xml = `<w:p>${pPrXml}${buildRun(String(text || ''), '')}</w:p>`;

  if (node) {
    return applyEdits(partXml, [{ start: node.end, end: node.end, replacement: xml }]);
  }
  // No anchor: append before sectPr so the section properties stay last.
  const sectPr = findChild(body, 'w:sectPr');
  const insertAt = sectPr ? sectPr.start : body.innerEnd;
  return applyEdits(partXml, [{ start: insertAt, end: insertAt, replacement: xml }]);
}

/**
 * Delete a paragraph.
 *
 * Refuses to delete the last remaining body paragraph: a `w:body` whose only child is
 * `w:sectPr` opens in Word as a document with a missing text body.
 */
export function deleteParagraph(partXml, block, remainingParagraphs) {
  if (remainingParagraphs <= 1) throw new Error('这是文档最后一段，不能再删');
  const tree = scanXml(partXml);
  const node = nodeAtPath(tree, block.path);
  if (!node) throw new Error(`找不到段落 ${block.id}`);
  return applyEdits(partXml, [{ start: node.start, end: node.end, replacement: '' }]);
}

/**
 * Check the parts Word rejects silently.
 *
 * A paragraph whose `w:pPr` is not its first child, or an unbalanced `w:tbl`, opens with a
 * repair prompt. Our own parser is more forgiving than Word, so this runs before the save.
 */
export function validateDocumentPart(partXml) {
  const problems = [];
  const tree = scanXml(partXml);
  const body = findDescendant(tree, 'w:body');
  if (!body) {
    problems.push('文档缺少正文');
    return problems;
  }

  for (const paragraph of findChildren(body, 'w:p')) {
    if (paragraph.children.length === 0) continue;
    const first = paragraph.children[0];
    const pPrIndex = paragraph.children.findIndex(child => child.name === 'w:pPr');
    // pPr must lead. A pPr anywhere else is ignored by Word, so formatting appears to vanish.
    if (pPrIndex > 0 && first.name !== 'w:rPr') {
      problems.push(`段落 ${pathOf(paragraph).join('.')} 的属性不在首位`);
    }
  }

  for (const table of findChildren(body, 'w:tbl')) {
    const grid = findChild(table, 'w:tblGrid');
    const gridCount = grid ? findChildren(grid, 'w:gridCol').length : 0;
    for (const row of findChildren(table, 'w:tr')) {
      const cells = findChildren(row, 'w:tc');
      if (cells.length === 0) problems.push('表格里有一行没有单元格');
      if (gridCount > 0) {
        const spanSum = cells.reduce((sum, cell) => {
          const tcPr = findChild(cell, 'w:tcPr');
          const span = tcPr ? findChild(tcPr, 'w:gridSpan') : null;
          return sum + (span ? Number(attr(span, 'w:val', '1')) || 1 : 1);
        }, 0);
        // Word tolerates a short row but not a grid that disagrees with every row.
        if (spanSum > gridCount) problems.push('表格列数与网格定义不一致');
      }
    }
  }

  return problems;
}

function pathOf(node) {
  const path = [];
  let current = node;
  while (current && current.parent) {
    path.unshift(current.parent.children.indexOf(current));
    current = current.parent;
  }
  return path;
}

export { nodeAtPath, buildRun, buildRunProps, pathOf };
