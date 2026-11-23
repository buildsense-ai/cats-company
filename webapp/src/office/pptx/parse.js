// PPTX reader.
//
// Each slide becomes a flat list of shapes with an `anchor` that points back at the
// exact XML node it came from. The anchor is what makes click-to-edit exact: it is
// `{part, path}` where path is the child-index chain from the slide root, so the
// writer can walk the same chain on a fresh parse of the same text and land on the
// same node without comparing names or values.

import { openPackage, readRelationships, OFFICE_MAIN_PARTS } from '../ooxml/zip.js';
import {
  scanXml, findChild, findChildren, findDescendant, findDescendants, attr, numberOf,
  rawInner, nodeText,
} from '../ooxml/xml.js';
import { SHAPE_KINDS, shapeId, shapeLabel, shapePreview } from './model.js';

const PRESENTATION_NS = 'http://schemas.openxmlformats.org/presentationml/2006/main';
const DRAWING_NS = 'http://schemas.openxmlformats.org/drawingml/2006/main';

/** Walk the child-index chain from root to node. Root itself is the empty path. */
function pathOf(node) {
  const path = [];
  let current = node;
  while (current && current.parent) {
    path.unshift(current.parent.children.indexOf(current));
    current = current.parent;
  }
  return path;
}

function nodeAt(root, path) {
  let current = root;
  for (const index of path) {
    if (!current) return null;
    current = current.children[index];
  }
  return current;
}

function colorFromNode(container) {
  if (!container) return null;
  const solidFill = findChild(container, 'a:solidFill');
  if (solidFill) {
    const srgb = findChild(solidFill, 'a:srgbClr');
    if (srgb) return `#${attr(srgb, 'val', '000000')}`;
    const scheme = findChild(solidFill, 'a:schemeClr');
    if (scheme) return schemeColor(attr(scheme, 'val', ''));
  }
  return null;
}

// Theme colors resolve through the theme part. Without this, text using
// `tx1`/`bg1`/`accent1` renders black on white and looks nothing like Office.
const SCHEME_MAP = {
  tx1: 'dk1', tx2: 'dk2', bg1: 'lt1', bg2: 'lt2',
};

function schemeColor(value) {
  const key = SCHEME_MAP[value] || value;
  return `var(--office-scheme-${key}, #000000)`;
}

function readThemeColors(themeXml) {
  const colors = Object.create(null);
  if (!themeXml) return colors;
  const tree = scanXml(themeXml);
  const scheme = findDescendant(tree, 'a:clrScheme');
  if (!scheme) return colors;
  for (const entry of scheme.children) {
    const name = entry.name.replace('a:', '');
    const srgb = findChild(entry, 'a:srgbClr');
    const sysClr = findChild(entry, 'a:sysClr');
    // `sysClr` carries a system name like "windowText" in `val` and the resolved hex in
    // `lastClr`. Reading `val` first yields CSS keywords that are not colors, which
    // renders every theme-driven text run as black.
    const hex = srgb ? attr(srgb, 'val') : (sysClr ? (attr(sysClr, 'lastClr') || '') : '');
    if (hex) colors[name] = hex.startsWith('#') ? hex : `#${hex}`;
  }
  return colors;
}

function readTextBody(node) {
  // Text can live in `p:txBody` (shapes) or `a:txBody` (graphic frames).
  const body = findChild(node, 'p:txBody') || findChild(node, 'a:txBody');
  if (!body) return null;
  const paragraphs = [];
  for (const para of findChildren(body, 'a:p')) {
    const pPr = findChild(para, 'a:pPr');
    const alignRaw = attr(pPr, 'algn', '');
    const runs = [];
    for (const child of para.children) {
      if (child.name === 'a:r') {
        const rPr = findChild(child, 'a:rPr');
        const t = findChild(child, 'a:t');
        const text = t ? nodeText(t) : '';
        if (!text) continue;
        runs.push({
          text,
          bold: attr(rPr, 'b') === '1',
          italic: attr(rPr, 'i') === '1',
          underline: attr(rPr, 'u', 'none') !== 'none',
          size: numberOf(attr(rPr, 'sz'), 0),
          color: colorFromNode(rPr),
          font: attr(findChild(rPr, 'a:latin'), 'typeface', ''),
        });
      } else if (child.name === 'a:br') {
        runs.push({ text: '\n', break: true, size: 0, bold: false, italic: false, underline: false, color: null, font: '' });
      } else if (child.name === 'a:fld') {
        const t = findChild(child, 'a:t');
        const text = t ? nodeText(t) : '';
        if (text) {
          const rPr = findChild(child, 'a:rPr');
          runs.push({ text, field: attr(child, 'type', ''), bold: attr(rPr, 'b') === '1',
            italic: attr(rPr, 'i') === '1', underline: false, size: numberOf(attr(rPr, 'sz'), 0),
            color: colorFromNode(rPr), font: attr(findChild(rPr, 'a:latin'), 'typeface', '') });
        }
      }
    }
    const text = runs.map(run => run.text).join('');
    const first = runs.find(run => !run.break && run.text);
    paragraphs.push({
      text,
      runs,
      align: alignRaw,
      size: first ? first.size : 0,
      bold: first ? first.bold : false,
      italic: first ? first.italic : false,
      color: first ? first.color : null,
      font: first ? first.font : '',
      level: numberOf(attr(pPr, 'lvl'), 0),
    });
  }
  const bodyPr = findChild(body, 'a:bodyPr');
  return {
    paragraphs,
    anchor: attr(bodyPr, 'anchor', 't'),
    wrap: attr(bodyPr, 'wrap', 'square'),
    lIns: numberOf(attr(bodyPr, 'lIns'), 91440),
    tIns: numberOf(attr(bodyPr, 'tIns'), 45720),
    rIns: numberOf(attr(bodyPr, 'rIns'), 91440),
    bIns: numberOf(attr(bodyPr, 'bIns'), 45720),
  };
}

function readTransform(node) {
  // Transform lives in a different place per shape kind. A plain shape has
  // `p:spPr/a:xfrm`; a graphicFrame (table, chart) has `p:xfrm` directly on the frame and
  // no spPr at all. Reading only the spPr path silently gives tables a 0,0 0x0 box.
  const spPr = findChild(node, 'p:spPr') || findChild(node, 'p:grpSpPr');
  const candidates = [
    spPr ? findChild(spPr, 'a:xfrm') : null,
    findChild(node, 'p:xfrm'),
    findChild(node, 'a:xfrm'),
  ];
  const transform = candidates.find(Boolean) || null;
  const off = transform ? findChild(transform, 'a:off') : null;
  const ext = transform ? findChild(transform, 'a:ext') : null;
  const childOff = transform ? findChild(transform, 'a:chOff') : null;
  const childExt = transform ? findChild(transform, 'a:chExt') : null;
  return {
    x: numberOf(attr(off, 'x'), 0),
    y: numberOf(attr(off, 'y'), 0),
    cx: numberOf(attr(ext, 'cx'), 0),
    cy: numberOf(attr(ext, 'cy'), 0),
    rot: numberOf(attr(transform, 'rot'), 0),
    flipH: attr(transform, 'flipH') === '1',
    flipV: attr(transform, 'flipV') === '1',
    childOffset: childOff ? { x: numberOf(attr(childOff, 'x'), 0), y: numberOf(attr(childOff, 'y'), 0) } : null,
    // `a:chExt` carries cx/cy, not x/y. Reading x/y yields 0 for every real file, and a
    // zero extent makes both geometry helpers bail out through their guard: a grouped
    // child then renders at its relative coordinates and a drag collapses to chOff.
    childExtent: childExt ? { x: numberOf(attr(childExt, 'cx'), 0), y: numberOf(attr(childExt, 'cy'), 0) } : null,
    hasTransform: Boolean(transform),
  };
}

function readTable(node) {
  const tbl = findDescendant(node, 'a:tbl');
  if (!tbl) return null;
  const rows = findChildren(tbl, 'a:tr');
  const cells = [];
  let cols = 0;
  rows.forEach((row, rowIndex) => {
    const cellNodes = findChildren(row, 'a:tc');
    cols = Math.max(cols, cellNodes.length);
    cellNodes.forEach((cell, colIndex) => {
      const body = readTextBody(cell);
      cells.push({
        row: rowIndex,
        col: colIndex,
        text: body ? body.paragraphs.map(p => p.text).join('\n') : '',
        _span: numberOf(attr(cell, 'gridSpan'), 1),
        _rowSpan: numberOf(attr(cell, 'rowSpan'), 1),
        _merged: attr(cell, 'hMerge') === '1' || attr(cell, 'vMerge') === '1',
      });
    });
  });
  return { rows: rows.length, cols, cells };
}

function shapeKind(node) {
  switch (node.name) {
    case 'p:sp': return SHAPE_KINDS.TEXT;
    case 'p:pic': return SHAPE_KINDS.PICTURE;
    case 'p:graphicFrame': return findDescendant(node, 'a:tbl') ? SHAPE_KINDS.TABLE : SHAPE_KINDS.FRAME;
    case 'p:grpSp': return SHAPE_KINDS.GROUP;
    case 'p:cxnSp': return SHAPE_KINDS.CONNECTOR;
    case 'mc:AlternateContent': return SHAPE_KINDS.UNKNOWN;
    default: return SHAPE_KINDS.UNKNOWN;
  }
}

function describeNode(node, part, ordinal, index) {
  const kind = shapeKind(node);
  const nvSpPr = findChild(node, 'p:nvSpPr') || findChild(node, 'p:nvPicPr')
    || findChild(node, 'p:nvGraphicFramePr') || findChild(node, 'p:nvGrpSpPr') || findChild(node, 'p:nvCxnSpPr');
  const cNvPr = nvSpPr ? findChild(nvSpPr, 'p:cNvPr') : null;
  const nvPr = nvSpPr ? findChild(nvSpPr, 'p:nvPr') : null;
  const ph = nvPr ? findChild(nvPr, 'p:ph') : null;

  const transform = readTransform(node);
  const textBody = kind === SHAPE_KINDS.TEXT || kind === SHAPE_KINDS.FRAME ? readTextBody(node) : null;
  const table = kind === SHAPE_KINDS.TABLE ? readTable(node) : null;

  // Pictures reference their bytes through a relationship id, not a path. Recording
  // the id here lets the renderer resolve media without re-walking the XML.
  let imageRelId = '';
  let imageName = '';
  if (kind === SHAPE_KINDS.PICTURE) {
    const blip = findDescendant(node, 'a:blip');
    imageRelId = attr(blip, 'r:embed', '');
    imageName = attr(cNvPr, 'descr', '') || attr(cNvPr, 'name', '');
  }

  const shape = {
    elId: shapeId(part, index),
    id: attr(cNvPr, 'id', String(ordinal)),
    name: attr(cNvPr, 'name', ''),
    kind,
    role: ph ? attr(ph, 'type', 'body') : '',
    part,
    anchor: { part, path: pathOf(node) },
    box: { x: transform.x, y: transform.y, cx: transform.cx, cy: transform.cy },
    rot: transform.rot,
    flipH: transform.flipH,
    flipV: transform.flipV,
    hasTransform: transform.hasTransform,
    childOffset: transform.childOffset,
    childExtent: transform.childExtent,
    textBody,
    table,
    tableRows: table ? table.rows : 0,
    tableCols: table ? table.cols : 0,
    text: textBody ? textBody.paragraphs.map(p => p.text).join('\n') : '',
    imageRelId,
    imageName,
    // Inherited means the shape has no transform and a placeholder reference: its
    // position and formatting come from the layout or master, not this slide.
    inherited: Boolean(ph) && !transform.hasTransform,
    placeholderType: ph ? attr(ph, 'type', 'body') : '',
    // Placeholder identity is `type` plus `idx`. Two placeholders can share a type and
    // differ only by idx (subTitle idx=1 next to a valueless one), so dropping idx makes
    // inheritance resolve to the wrong box or to nothing at all.
    placeholderIdx: ph ? attr(ph, 'idx', '0') : '',
    rawInner: rawInner('', node),
  };
  shape.label = shapeLabel(shape);
  shape.preview = shapePreview(shape);
  return shape;
}

function collectShapes(spTree, part, out, parentId = '') {
  const shapes = findChildren(spTree, 'p:sp')
    .concat(findChildren(spTree, 'p:pic'))
    .concat(findChildren(spTree, 'p:graphicFrame'))
    .concat(findChildren(spTree, 'p:grpSp'))
    .concat(findChildren(spTree, 'p:cxnSp'))
    .concat(findChildren(spTree, 'mc:AlternateContent'));
  // Preserve document order rather than grouping by node name; z-order is what the
  // user sees, and the element list must match the canvas stacking.
  shapes.sort((a, b) => a.start - b.start);

  for (const node of shapes) {
    const index = out.length;
    const shape = describeNode(node, part, index, index);
    // A group's children are numbered inside their own slice, so `slides-slide1#0` would
    // name both the first top-level shape and the first child of every group on that
    // slide. Every write resolves a target by elId, and the lookup stops at the first
    // match, so the collision silently redirected a grouped-child edit onto an unrelated
    // top-level shape. Qualifying the id with its parent keeps it unique per slide.
    if (parentId) shape.elId = `${parentId}/${index}`;
    out.push(shape);
    if (shape.kind === SHAPE_KINDS.GROUP) {
      const inner = findChild(node, 'p:grpSpPr');
      const innerTree = { children: findChildren(node, 'p:sp').concat(findChildren(node, 'p:pic'))
        .concat(findChildren(node, 'p:graphicFrame')).concat(findChildren(node, 'p:grpSp')) };
      shape.children = [];
      collectShapes(innerTree, part, shape.children, shape.elId);
      // A group's children live in the group's own coordinate space; mark them so
      // neither the renderer nor the writer treats their boxes as absolute.
      for (const child of shape.children) {
        child.parentGroupId = shape.elId;
        child.positioning = 'relative';
      }
      if (!inner) { /* grpSpPr is optional for our purposes */ }
    }
  }
}

function parseSpTreePart(xml, part, slideSize) {
  const tree = scanXml(xml);
  const spTree = findDescendant(tree, 'p:spTree');
  const shapes = [];
  if (spTree) collectShapes(spTree, part, shapes);
  return { tree, spTree, shapes };
}

/**
 * Resolve a slide's layout and master so inherited placeholders can be positioned
 * and themed. Inheritance is the single most common source of "why did editing one
 * slide change three others", so the chain is recorded explicitly.
 */
async function readSlideChain(pkg, slidePart) {
  const rels = await readRelationships(pkg, slidePart);
  let layoutPart = null;
  let masterPart = null;
  for (const rel of rels.values()) {
    if (rel.type && rel.type.endsWith('/slideLayout')) layoutPart = rel.resolved;
  }
  if (layoutPart) {
    const layoutRels = await readRelationships(pkg, layoutPart);
    for (const rel of layoutRels.values()) {
      if (rel.type && rel.type.endsWith('/slideMaster')) masterPart = rel.resolved;
    }
  }
  let themePart = null;
  if (masterPart) {
    const masterRels = await readRelationships(pkg, masterPart);
    for (const rel of masterRels.values()) {
      if (rel.type && rel.type.endsWith('/theme')) themePart = rel.resolved;
    }
  }
  return { layoutPart, masterPart, themePart };
}

/** Placeholders on a layout/master give inherited shapes their real position. */
function placeholderMap(xml) {
  const map = new Map();
  if (!xml) return map;
  const tree = scanXml(xml);
  const spTree = findDescendant(tree, 'p:spTree');
  if (!spTree) return map;
  const shapes = findChildren(spTree, 'p:sp');
  for (const node of shapes) {
    const nvSpPr = findChild(node, 'p:nvSpPr');
    const nvPr = nvSpPr ? findChild(nvSpPr, 'p:nvPr') : null;
    const ph = nvPr ? findChild(nvPr, 'p:ph') : null;
    if (!ph) continue;
    const type = attr(ph, 'type', 'body');
    const idx = attr(ph, 'idx', '0');
    const transform = readTransform(node);
    const record = {
      box: { x: transform.x, y: transform.y, cx: transform.cx, cy: transform.cy },
      textBody: readTextBody(node),
      type,
      idx,
    };
    map.set(`${type}|${idx}`, record);
    // Also index by type alone. A slide placeholder often omits idx while the layout one
    // declares it, and without this fallback that shape renders at 0,0 with zero size.
    if (!map.has(`type:${type}`)) map.set(`type:${type}`, record);
  }
  return map;
}

function firstPlaceholderOfType(map, type) {
  if (!map || !type) return null;
  return map.get(`type:${type}`) || null;
}

/**
 * Parse a .pptx into a render-and-edit model.
 *
 * Returns the package alongside the model so the writer can reuse already-inflated
 * parts instead of re-reading them.
 */
export async function parsePptx(input) {
  const pkg = await openPackage(input);

  const presentationXml = await pkg.text(OFFICE_MAIN_PARTS.pptx);
  if (!presentationXml) throw new Error('这不是一个有效的 PPTX 文件');

  const presentationTree = scanXml(presentationXml);
  const sldSz = findDescendant(presentationTree, 'p:sldSz');
  const slideSize = {
    cx: numberOf(attr(sldSz, 'cx'), 12192000),
    cy: numberOf(attr(sldSz, 'cy'), 6858000),
  };

  const presRels = await readRelationships(pkg, OFFICE_MAIN_PARTS.pptx);
  const slidePartNames = [];
  const sldIdLst = findDescendant(presentationTree, 'p:sldIdLst');
  if (sldIdLst) {
    for (const sldId of findChildren(sldIdLst, 'p:sldId')) {
      const rid = attr(sldId, 'r:id') || attr(sldId, 'id');
      const rel = presRels.get(rid);
      if (rel && rel.resolved) slidePartNames.push(rel.resolved);
    }
  }
  if (slidePartNames.length === 0) {
    // Fall back to directory order so a malformed presentation list still opens.
    const guessed = pkg.parts().filter(name => /^ppt\/slides\/slide\d+\.xml$/.test(name));
    guessed.sort((a, b) => {
      const na = Number((a.match(/slide(\d+)\.xml$/) || [])[1] || 0);
      const nb = Number((b.match(/slide(\d+)\.xml$/) || [])[1] || 0);
      return na - nb;
    });
    slidePartNames.push(...guessed);
  }

  let themeColors = Object.create(null);
  const slides = [];

  for (let index = 0; index < slidePartNames.length; index += 1) {
    const part = slidePartNames[index];
    const xml = await pkg.text(part);
    if (!xml) continue;
    const chain = await readSlideChain(pkg, part);

    if (Object.keys(themeColors).length === 0 && chain.themePart) {
      themeColors = readThemeColors(await pkg.text(chain.themePart));
    }

    const layoutXml = chain.layoutPart ? await pkg.text(chain.layoutPart) : null;
    const masterXml = chain.masterPart ? await pkg.text(chain.masterPart) : null;
    const fromLayout = placeholderMap(layoutXml);
    const fromMaster = placeholderMap(masterXml);

    const { tree, spTree, shapes } = parseSpTreePart(xml, part, slideSize);

    // Fill in inherited geometry so the canvas can draw something sensible, while
    // keeping `inherited` true so the writer refuses to touch the layout silently.
    for (const shape of shapes) {
      if (!shape.inherited) continue;
      const key = `${shape.placeholderType}|${shape.placeholderIdx || '0'}`;
      const byIdx = fromLayout.get(key) || fromMaster.get(key);
      // A placeholder may omit idx while its layout counterpart declares one; matching
      // on type alone is the fallback so the box is still found rather than left at 0x0.
      const byType = byIdx || null;
      const source = byType || firstPlaceholderOfType(fromLayout, shape.placeholderType)
        || firstPlaceholderOfType(fromMaster, shape.placeholderType);
      if (source) {
        shape.box = { ...source.box };
        shape.inheritedBox = true;
        if (!shape.textBody && source.textBody) shape.textBody = source.textBody;
      }
    }

    const backgroundXml = findDescendant(tree, 'p:bg');
    slides.push({
      index,
      part,
      layoutPart: chain.layoutPart,
      masterPart: chain.masterPart,
      themePart: chain.themePart,
      shapes,
      background: backgroundXml ? readBackground(backgroundXml) : null,
      hasLayoutOverrides: shapes.some(shape => shape.inherited && shape.inheritedBox),
    });
  }

  return {
    family: 'pptx',
    pkg,
    slideSize,
    themeColors,
    slides,
    title: slides.length ? (slides[0].shapes.find(s => s.role === 'title' || s.role === 'ctrTitle')?.text || '') : '',
  };
}

function readBackground(node) {
  const bgPr = findChild(node, 'p:bgPr');
  if (!bgPr) return null;
  const color = colorFromNode(bgPr);
  return color ? { fill: color } : null;
}

export { nodeAt, pathOf, placeholderMap };
