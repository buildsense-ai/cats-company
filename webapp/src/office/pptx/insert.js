// Insert operations that touch more than one part of the package.
//
// Two of the four allowed primitives need package-level work, not just an XML splice:
//
//   picture — adds a media part, a relationship on the slide, and a content-type default
//   group   — removes N shape nodes and re-emits them inside a new grpSp
//
// Keeping them here rather than in write.js is deliberate: write.js edits one part's text
// and is pure string-in/string-out. These need the package, so mixing them would make the
// pure functions untestable without a zip.

import { applyEdits, escapeText, findChild, findDescendants, findDescendant, scanXml, attr } from '../ooxml/xml.js';
import { readRelationships } from '../ooxml/zip.js';

const REL_IMAGE = 'http://schemas.openxmlformats.org/officeDocument/2006/relationships/image';
const CONTENT_TYPES = '[Content_Types].xml';

const IMAGE_MIME = {
  png: 'image/png',
  jpg: 'image/jpeg',
  jpeg: 'image/jpeg',
  gif: 'image/gif',
  bmp: 'image/bmp',
  webp: 'image/webp',
  svg: 'image/svg+xml',
};

export function escapeXmlAttr(value) {
  return String(value).replace(/&/g, '&amp;').replace(/</g, '&lt;')
    .replace(/>/g, '&gt;').replace(/"/g, '&quot;');
}

/** Slide part names are `ppt/slides/slideN.xml`; find the highest N in use. */
export function nextSlideNumber(pkg) {
  let max = 0;
  for (const name of pkg.parts()) {
    const match = /^ppt\/slides\/slide(\d+)\.xml$/.exec(name);
    if (match) max = Math.max(max, Number(match[1]));
  }
  return max + 1;
}

/**
 * Media part names are `<family>/media/imageN.ext`; find the highest N in use.
 *
 * The directory prefix is part of the pattern rather than hardcoded to `ppt/media/`. Word stores
 * media under `word/media/`, so a ppt-only regex silently returned 1 for every docx and the second
 * inserted picture overwrote the first one's bytes while both shapes kept a valid relationship.
 */
export function nextMediaNumber(pkg) {
  let max = 0;
  for (const name of pkg.parts()) {
    const match = /^(?:ppt|word)\/media\/[a-zA-Z]*(\d+)\./.exec(name);
    if (match) max = Math.max(max, Number(match[1]));
  }
  return max + 1;
}

function relsPathFor(partName, isSlideLayout = false) {
  const clean = String(partName).replace(/^\/+/, '');
  const slash = clean.lastIndexOf('/');
  const dir = slash === -1 ? '' : clean.slice(0, slash);
  const base = slash === -1 ? clean : clean.slice(slash + 1);
  return `${dir ? `${dir}/` : ''}_rels/${base}.rels`;
}

const RELS_HEADER = '<?xml version="1.0" encoding="UTF-8" standalone="yes"?>\n'
  + '<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">';

/**
 * Append a relationship to a part.
 *
 * When the part has no rels file yet, one is created rather than skipped: a slide with an
 * image but no rels file is exactly the case where Office reports a broken part, and the
 * error message points at the image instead of the missing rels.
 */
export async function addRelationship(pkg, partName, { type, target, targetMode }) {
  const relPath = relsPathFor(partName);
  const existing = await pkg.text(relPath);

  if (!existing) {
    const id = 'rId1';
    const body = `${RELS_HEADER}<Relationship Id="${id}" Type="${type}" Target="${escapeXmlAttr(target)}"${targetMode ? ` TargetMode="${targetMode}"` : ''}/></Relationships>`;
    pkg.setText(relPath, body);
    return id;
  }

  const tree = scanXml(existing);
  const used = new Set(findDescendants(tree, 'Relationship').map(rel => attr(rel, 'Id')));
  let counter = 1;
  while (used.has(`rId${counter}`)) counter += 1;
  const id = `rId${counter}`;

  const insertAt = existing.lastIndexOf('</Relationships>');
  const node = `<Relationship Id="${id}" Type="${type}" Target="${escapeXmlAttr(target)}"${targetMode ? ` TargetMode="${targetMode}"` : ''}/>`;
  pkg.setText(relPath, existing.slice(0, insertAt) + node + existing.slice(insertAt));
  return id;
}

/** Ensure [Content_Types].xml declares a Default for the extension. */
export async function ensureContentTypeDefault(pkg, ext, mime) {
  const xml = await pkg.text(CONTENT_TYPES);
  if (!xml) return;
  const lower = ext.toLowerCase();
  const pattern = new RegExp(`<Default[^>]*Extension="${lower}"`, 'i');
  if (pattern.test(xml)) return;
  const insertAt = xml.lastIndexOf('</Types>');
  pkg.setText(CONTENT_TYPES, `${xml.slice(0, insertAt)}<Default Extension="${lower}" ContentType="${mime}"/>${xml.slice(insertAt)}`);
}

/** Ensure [Content_Types].xml has an Override for a specific part. */
export async function ensureContentTypeOverride(pkg, partName, mime) {
  const xml = await pkg.text(CONTENT_TYPES);
  if (!xml) return;
  const clean = `/${String(partName).replace(/^\/+/, '')}`;
  if (new RegExp(`PartName="${clean.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}"`, 'i').test(xml)) return;
  const insertAt = xml.lastIndexOf('</Types>');
  pkg.setText(CONTENT_TYPES, `${xml.slice(0, insertAt)}<Override PartName="${clean}" ContentType="${mime}"/>${xml.slice(insertAt)}`);
}

export async function removeContentTypeOverride(pkg, partName) {
  const xml = await pkg.text(CONTENT_TYPES);
  if (!xml) return;
  const clean = `/${String(partName).replace(/^\/+/, '')}`;
  const pattern = new RegExp(`<Override[^>]*PartName="${clean.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}"[^>]*/>`, 'i');
  if (!pattern.test(xml)) return;
  pkg.setText(CONTENT_TYPES, xml.replace(pattern, ''));
}

/**
 * Insert a picture.
 *
 * The bytes go into `ppt/media`, the slide gets an image relationship, and the shape uses
 * `r:embed` to point at that relationship id — never a file path. Writing a path instead
 * of a relationship id produces a file that opens but shows a blank box.
 */
export async function applyInsertPicture(model, slideIndex, spec) {
  const slide = model.slides[slideIndex];
  if (!slide) throw new Error('找不到要插入图片的页面');
  if (!spec?.bytes || spec.bytes.byteLength === 0) throw new Error('图片内容为空');

  const ext = String(spec.ext || 'png').toLowerCase();
  const mime = IMAGE_MIME[ext] || 'image/png';
  const mediaName = `ppt/media/image${nextMediaNumber(model.pkg)}.${ext}`;
  model.pkg.setBinary(mediaName, spec.bytes);
  await ensureContentTypeDefault(model.pkg, ext, mime);

  // Relative target from the slide part to the media part.
  const relId = await addRelationship(model.pkg, slide.part, {
    type: REL_IMAGE,
    target: `../media/${mediaName.split('/').pop()}`,
  });

  const partXml = await model.pkg.text(slide.part);
  const tree = scanXml(partXml);
  const spTree = findDescendant(tree, 'p:spTree');
  if (!spTree) throw new Error('幻灯片结构损坏，找不到画布');

  const id = spec.id || nextShapeIdIn(partXml);
  const name = escapeXmlAttr(spec.name || `图片 ${id}`);
  const box = spec.box;
  const x = Math.round(box.x);
  const y = Math.round(box.y);
  const cx = Math.max(1, Math.round(box.cx));
  const cy = Math.max(1, Math.round(box.cy));

  const xml = `<p:pic><p:nvPicPr><p:cNvPr id="${id}" name="${name}" descr="${escapeXmlAttr(spec.name || '')}"/><p:cNvPicPr><a:picLocks noChangeAspect="1"/></p:cNvPicPr><p:nvPr/></p:nvPicPr><p:blipFill><a:blip r:embed="${relId}"/><a:stretch><a:fillRect/></a:stretch></p:blipFill><p:spPr><a:xfrm><a:off x="${x}" y="${y}"/><a:ext cx="${cx}" cy="${cy}"/></a:xfrm><a:prstGeom prst="rect"><a:avLst/></a:prstGeom></p:spPr></p:pic>`;

  model.pkg.setText(slide.part, partXml.slice(0, spTree.innerEnd) + xml + partXml.slice(spTree.innerEnd));
  return { mediaName, relId, id };
}

function nextShapeIdIn(partXml) {
  const tree = scanXml(partXml);
  let max = 0;
  for (const node of findDescendants(tree, 'p:cNvPr')) {
    const id = Number(attr(node, 'id'));
    if (Number.isFinite(id) && id > max) max = id;
  }
  return Math.max(max + 1, 2);
}

/**
 * Group the given shapes.
 *
 * The group's `chOff`/`chExt` are set equal to its `off`/`ext`, which means children keep
 * their original absolute coordinates inside the group. That is valid OOXML and avoids a
 * coordinate rewrite on every child, which is where a grouping implementation usually
 * introduces a visible jump.
 */
export function applyGroupShapes(partXml, shapes) {
  if (!Array.isArray(shapes) || shapes.length < 2) {
    throw new Error('至少要选两个元素才能组合');
  }
  const tree = scanXml(partXml);
  const spTree = findDescendant(tree, 'p:spTree');
  if (!spTree) throw new Error('幻灯片结构损坏，找不到画布');

  const nodes = shapes.map((shape) => {
    let current = tree;
    for (const index of shape.anchor.path) {
      if (!current) return null;
      current = current.children[index];
    }
    return current;
  });
  if (nodes.some(node => !node)) throw new Error('有元素已经不在这一页上了');

  // Only top-level shapes can be grouped directly. A shape already inside a group has a
  // path through that group, and pulling it out silently would break the outer group.
  const notTopLevel = nodes.filter(node => node.parent !== spTree);
  if (notTopLevel.length > 0) throw new Error('组合里的元素不能再次组合，请先取消组合');

  const left = Math.min(...shapes.map(shape => shape.box.x));
  const top = Math.min(...shapes.map(shape => shape.box.y));
  const right = Math.max(...shapes.map(shape => shape.box.x + shape.box.cx));
  const bottom = Math.max(...shapes.map(shape => shape.box.y + shape.box.cy));
  const width = Math.max(1, right - left);
  const height = Math.max(1, bottom - top);

  // Take the topmost member's slot so the group keeps the stacking the user saw.
  const ordered = [...nodes].sort((a, b) => a.start - b.start);
  const firstStart = ordered[0].start;
  const pieces = ordered.map(node => partXml.slice(node.start, node.end));

  const groupId = nextShapeIdIn(partXml);
  const groupXml = `<p:grpSp><p:nvGrpSpPr><p:cNvPr id="${groupId}" name="组合 ${groupId}"/><p:cNvGrpSpPr/><p:nvPr/></p:nvGrpSpPr><p:grpSpPr><a:xfrm><a:off x="${left}" y="${top}"/><a:ext cx="${width}" cy="${height}"/><a:chOff x="${left}" y="${top}"/><a:chExt cx="${width}" cy="${height}"/></a:xfrm></p:grpSpPr>${pieces.join('')}</p:grpSp>`;

  // Remove the members from the end so earlier offsets stay valid, then insert the group.
  let out = partXml;
  for (const node of [...ordered].sort((a, b) => b.start - a.start)) {
    out = out.slice(0, node.start) + out.slice(node.end);
  }
  return out.slice(0, firstStart) + groupXml + out.slice(firstStart);
}

export { REL_IMAGE, IMAGE_MIME, CONTENT_TYPES, relsPathFor };
