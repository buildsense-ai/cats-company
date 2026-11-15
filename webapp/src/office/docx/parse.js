// DOCX reader.
//
// `word/document.xml` is the part that matters: its `w:body` holds an ordered list of
// paragraphs and tables, and that order IS the document. Nothing here reads position,
// because Word does not store any — layout is computed at open time from page size, styles
// and font metrics.
//
// The anchor rule matches the PPTX side: each block records the absolute child-index path
// from the document root, so a later write can re-walk the same chain on a fresh parse and
// land on the same node. No name matching, no text search.

import { openPackage, readRelationships, OFFICE_MAIN_PARTS } from '../ooxml/zip.js';
import {
  scanXml, findChild, findChildren, findDescendant, findDescendants, attr, nodeText,
} from '../ooxml/xml.js';
import {
  BLOCK_KINDS, blockId, blockLabel, blockPreview, halfPointsToPt, twipsToCm,
} from './model.js';

const W_NS = 'http://schemas.openxmlformats.org/wordprocessingml/2006/main';

/** Walk the child-index chain from root to node. */
function pathOf(node) {
  const path = [];
  let current = node;
  while (current && current.parent) {
    path.unshift(current.parent.children.indexOf(current));
    current = current.parent;
  }
  return path;
}

function colorOf(rPr) {
  if (!rPr) return null;
  const color = findChild(rPr, 'w:color');
  if (!color) return null;
  const value = attr(color, 'w:val', '');
  // `auto` means "let Word decide", which on a white page is black. Returning it as a hex
  // would paint the text black even inside a dark-styled block.
  if (!value || value === 'auto') return null;
  return value.startsWith('#') ? value : `#${value}`;
}

function fontOf(rPr) {
  if (!rPr) return null;
  const fonts = findChild(rPr, 'w:rFonts');
  if (!fonts) return null;
  return attr(fonts, 'w:eastAsia', '') || attr(fonts, 'w:ascii', '') || null;
}

/**
 * Read one run's formatting.
 *
 * A run with no `w:rPr` inherits everything from the paragraph and the document defaults. The
 * returned object still carries explicit `false` for the toggles rather than leaving them
 * undefined: every consumer — the renderer, the property panel, the writer's template lookup —
 * treats "absent" and "off" the same way, and an undefined that leaks into a UI checkbox reads
 * as "unknown" instead of "not bold".
 */
function runStyle(rPr) {
  const base = { bold: false, italic: false, underline: false, size: 0, color: null, font: null };
  if (!rPr) return base;

  // `w:sz` is a child ELEMENT carrying a `w:val`, not an attribute of `w:rPr`. Reading it as an
  // attribute silently yielded '' and every run reported size 0, which made a correct size write
  // look like a failed one.
  const sizeNode = findChild(rPr, 'w:sz');
  const size = sizeNode ? attr(sizeNode, 'w:val', '') : '';
  const boldNode = findChild(rPr, 'w:b');
  const italicNode = findChild(rPr, 'w:i');
  const underline = findChild(rPr, 'w:u');
  return {
    // `w:b w:val="0"` is the explicit "off" form and appears in real files after a user un-bolds
    // text, so the value is checked rather than the mere presence of the element.
    bold: boldNode ? attr(boldNode, 'w:val', 'true') !== '0' : false,
    italic: italicNode ? attr(italicNode, 'w:val', 'true') !== '0' : false,
    underline: underline ? attr(underline, 'w:val', 'single') !== 'none' : false,
    size: size ? halfPointsToPt(size) : 0,
    color: colorOf(rPr),
    font: fontOf(rPr),
  };
}

/**
 * Collect runs from a paragraph.
 *
 * Runs sit directly under `w:p`, but also inside `w:hyperlink`, `w:ins` and `w:sdt`. Walking
 * descendants finds all of them; the trade-off is that a hyperlink's runs are treated as
 * ordinary text, which is what the editor wants — the link target is not editable here, but
 * its words must be.
 */
function readRuns(paragraph) {
  const runs = [];
  for (const node of findDescendants(paragraph, 'w:r')) {
    const rPr = findChild(node, 'w:rPr');
    const style = runStyle(rPr);
    const pieces = [];
    for (const child of node.children) {
      if (child.name === 'w:t') pieces.push(nodeText(child));
      else if (child.name === 'w:tab') pieces.push('\t');
      else if (child.name === 'w:br') pieces.push('\n');
    }
    const text = pieces.join('');
    if (!text) continue;
    runs.push({ text, ...style, path: pathOf(node) });
  }
  return runs;
}

function readParagraphSpacing(pPr) {
  const spacing = pPr ? findChild(pPr, 'w:spacing') : null;
  if (!spacing) return {};
  return {
    lineTwips: attr(spacing, 'w:line', '') ? Number(attr(spacing, 'w:line', '0')) : 0,
    lineRule: attr(spacing, 'w:lineRule', 'auto'),
    beforeTwips: Number(attr(spacing, 'w:before', '0')) || 0,
    afterTwips: Number(attr(spacing, 'w:after', '0')) || 0,
  };
}

function readIndent(pPr) {
  const ind = pPr ? findChild(pPr, 'w:ind') : null;
  if (!ind) return {};
  return {
    indLeftTwips: Number(attr(ind, 'w:left', '0')) || 0,
    indFirstTwips: Number(attr(ind, 'w:firstLine', '0')) || 0,
    indHangingTwips: Number(attr(ind, 'w:hanging', '0')) || 0,
  };
}

function describeParagraph(node, part) {
  const pPr = findChild(node, 'w:pPr');
  const styleNode = pPr ? findChild(pPr, 'w:pStyle') : null;
  const styleId = styleNode ? attr(styleNode, 'w:val', '') : '';
  const jc = pPr ? findChild(pPr, 'w:jc') : null;
  const runs = readRuns(node);
  const text = runs.map(run => run.text).join('');

  // `Heading1`..`Heading9` is the built-in naming; a custom style that merely looks like a
  // heading is not treated as one, because guessing would change the outline level on save.
  const headingMatch = /^Heading(\d)$/i.exec(styleId);

  const block = {
    kind: BLOCK_KINDS.PARAGRAPH,
    id: blockId(part, pathOf(node)),
    path: pathOf(node),
    text,
    runs,
    styleId,
    heading: headingMatch ? Number(headingMatch[1]) : 0,
    align: jc ? attr(jc, 'w:val', '') : '',
    empty: !text.trim(),
    ...readIndent(pPr),
    ...readParagraphSpacing(pPr),
    hasNumbering: Boolean(pPr && findChild(pPr, 'w:numPr')),
  };
  block.label = blockLabel(block);
  block.preview = blockPreview(block);
  return block;
}

/** Read a table's rows and cells. Merged cells appear once and are flagged, not duplicated. */
function describeTable(node, part) {
  const grid = findChild(node, 'w:tblGrid');
  const gridCols = grid ? findChildren(grid, 'w:gridCol') : [];
  const rowNodes = findChildren(node, 'w:tr');
  const rows = rowNodes.length;
  const cols = gridCols.length || Math.max(0, ...rowNodes.map(row =>
    findChildren(row, 'w:tc').length));

  const cells = [];
  rowNodes.forEach((row, rowIndex) => {
    let colIndex = 0;
    for (const cell of findChildren(row, 'w:tc')) {
      const tcPr = findChild(cell, 'w:tcPr');
      // `attr` needs a node, so the absent case is handled before the call rather than by
      // passing an empty object. Passing `{}` used to throw on the property read.
      const gridSpan = tcPr ? findChild(tcPr, 'w:gridSpan') : null;
      const span = gridSpan ? Number(attr(gridSpan, 'w:val', '1')) : 1;
      const spanValue = Number.isFinite(span) && span > 0 ? span : 1;
      const paragraphs = findChildren(cell, 'w:p');
      const text = paragraphs
        .map(para => readRuns(para).map(run => run.text).join(''))
        .join('\n');
      const vMerge = tcPr ? findChild(tcPr, 'w:vMerge') : null;
      cells.push({
        row: rowIndex,
        col: colIndex,
        span: spanValue,
        text,
        firstParagraphPath: paragraphs[0] ? pathOf(paragraphs[0]) : null,
        merged: Boolean(vMerge),
        mergeContinue: Boolean(vMerge) && !attr(vMerge, 'w:val', ''),
      });
      colIndex += spanValue;
    }
  });

  const block = {
    kind: BLOCK_KINDS.TABLE,
    id: blockId(part, pathOf(node)),
    path: pathOf(node),
    rows,
    cols,
    cells,
    label: '',
    preview: '',
  };
  block.label = blockLabel(block);
  block.preview = blockPreview(block);
  return block;
}

function readPageSetup(body) {
  const sectPr = findChild(body, 'w:sectPr');
  if (!sectPr) return { widthTwips: 11906, heightTwips: 16838, orientation: 'portrait' };
  const pgSz = findChild(sectPr, 'w:pgSz');
  const margins = findChild(sectPr, 'w:pgMar');
  return {
    widthTwips: Number(attr(pgSz, 'w:w', '11906')) || 11906,
    heightTwips: Number(attr(pgSz, 'w:h', '16838')) || 16838,
    orientation: attr(pgSz, 'w:orient', 'portrait'),
    marginTopTwips: Number(attr(margins, 'w:top', '1440')) || 1440,
    marginBottomTwips: Number(attr(margins, 'w:bottom', '1440')) || 1440,
    marginLeftTwips: Number(attr(margins, 'w:left', '1440')) || 1440,
    marginRightTwips: Number(attr(margins, 'w:right', '1440')) || 1440,
  };
}

/** Resolve which relationships a paragraph's images use, for rendering only. */
async function readImageRelationships(pkg, part) {
  const rels = await readRelationships(pkg, part);
  const out = new Map();
  for (const rel of rels.values()) {
    if (rel.type && rel.type.endsWith('/image')) out.set(rel.id, rel.resolved);
  }
  return out;
}

/**
 * Parse a .docx into a render-and-edit model.
 *
 * Blocks keep document order. An unknown child of `w:body` (a `w:sdt`, an `mc:AlternateContent`)
 * is skipped rather than flattened: inventing an editing surface for a structure we do not
 * model would let the user "edit" something the writer cannot faithfully write back.
 */
export async function parseDocx(input) {
  const pkg = await openPackage(input);
  const documentXml = await pkg.text(OFFICE_MAIN_PARTS.docx);
  if (!documentXml) throw new Error('这不是一个有效的 DOCX 文件');

  const tree = scanXml(documentXml);
  const body = findDescendant(tree, 'w:body');
  if (!body) throw new Error('文档缺少正文');

  const part = OFFICE_MAIN_PARTS.docx;
  const blocks = [];
  let imageCount = 0;

  for (const child of body.children) {
    if (child.name === 'w:p') {
      blocks.push(describeParagraph(child, part));
    } else if (child.name === 'w:tbl') {
      blocks.push(describeTable(child, part));
    } else if (child.name === 'w:sectPr') {
      continue;
    }
    if (child.name === 'w:p') {
      imageCount += findDescendants(child, 'w:drawing').length;
    }
  }

  const imageRels = await readImageRelationships(pkg, part);

  return {
    family: 'docx',
    pkg,
    part,
    page: readPageSetup(body),
    blocks,
    imageRels,
    imageCount,
    // The outline is what the block list groups by; deriving it here keeps the renderer free
    // of document-structure logic.
    outline: blocks.filter(block => block.kind === BLOCK_KINDS.PARAGRAPH && block.heading > 0),
    textLength: blocks.reduce((sum, block) => (
      block.kind === BLOCK_KINDS.PARAGRAPH ? sum + block.text.length : sum), 0),
  };
}

export {
  pathOf, readRuns, readParagraphSpacing, readIndent, readPageSetup, W_NS, halfPointsToPt, twipsToCm,
};