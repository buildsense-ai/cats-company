// Slide-level operations: add, delete, duplicate, reorder.
//
// All four touch the same three places, and forgetting any one of them produces a file
// PowerPoint either ignores or offers to repair:
//
//   ppt/presentation.xml          the sldIdLst order and the sldId entries
//   ppt/_rels/presentation.xml.rels   one relationship per slide
//   [Content_Types].xml           one Override per slide part
//
// The presentation.xml edits are splices for the same reason every other part is: a
// re-serialized presentation part loses namespace prefixes and extension lists that
// Office wrote, and the repair prompt follows.

import { applyEdits, attr, escapeText, findChild, findChildren, findDescendant, findDescendants, scanXml } from '../ooxml/xml.js';
import {
  addRelationship, ensureContentTypeOverride, nextSlideNumber, removeContentTypeOverride, relsPathFor,
} from './insert.js';

const SLIDE_CONTENT_TYPE = 'application/vnd.openxmlformats-officedocument.presentationml.slide+xml';
const SLIDE_REL_TYPE = 'http://schemas.openxmlformats.org/officeDocument/2006/relationships/slide';
const LAYOUT_REL_TYPE = 'http://schemas.openxmlformats.org/officeDocument/2006/relationships/slideLayout';

/** Read the presentation part's sldIdLst as an ordered list of {id, rid, node}. */
export function readSlideOrder(presentationXml) {
  const tree = scanXml(presentationXml);
  const list = findDescendant(tree, 'p:sldIdLst');
  if (!list) return { tree, list: null, entries: [] };
  const entries = findChildren(list, 'p:sldId').map(node => ({
    id: Number(attr(node, 'id')),
    rid: attr(node, 'r:id') || attr(node, 'id'),
    node,
  }));
  return { tree, list, entries };
}

function nextSldIdValue(entries) {
  // p:sldId/@id must be unique and at least 256. Reusing an id makes Office treat the
  // second slide as a duplicate of the first.
  let max = 255;
  for (const entry of entries) if (Number.isFinite(entry.id) && entry.id > max) max = entry.id;
  return max + 1;
}

async function readPresentationXml(model) {
  const xml = await model.pkg.text('ppt/presentation.xml');
  if (!xml) throw new Error('这不是一个有效的 PPTX 文件');
  return xml;
}

function resolveLayoutPart(model, slideIndex) {
  const slide = model.slides[slideIndex];
  return slide?.layoutPart || null;
}

/**
 * Append or insert a new slide.
 *
 * The new slide starts with an empty shape tree and a relationship to the same layout the
 * copied slide used. An empty `p:spTree` with just nvGrpSpPr/grpSpPr is valid; omitting
 * either of those two makes Office refuse the part.
 */
export async function addSlide(model, { at = null, templateIndex = null } = {}) {
  const number = nextSlideNumber(model.pkg);
  const partName = `ppt/slides/slide${number}.xml`;

  const presentationXml = await readPresentationXml(model);
  const { entries } = readSlideOrder(presentationXml);
  const sourceIndex = templateIndex != null ? templateIndex : (model.slides[0]?.index ?? 0);
  const layoutPart = resolveLayoutPart(model, sourceIndex);

  const slideXml = '<?xml version="1.0" encoding="UTF-8" standalone="yes"?>\n'
    + '<p:sld xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" '
    + 'xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" '
    + 'xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main">'
    + '<p:cSld><p:spTree><p:nvGrpSpPr><p:cNvPr id="1" name=""/><p:cNvGrpSpPr/><p:nvPr/></p:nvGrpSpPr>'
    + '<p:grpSpPr><a:xfrm><a:off x="0" y="0"/><a:ext cx="0" cy="0"/><a:chOff x="0" y="0"/><a:chExt cx="0" cy="0"/></a:xfrm></p:grpSpPr>'
    + '</p:spTree></p:cSld><p:clrMapOvr><a:masterClrMapping/></p:clrMapOvr></p:sld>';
  model.pkg.setText(partName, slideXml);

  await ensureContentTypeOverride(model.pkg, partName, SLIDE_CONTENT_TYPE);

  // The slide needs a layout relationship; without one PowerPoint cannot pick a master and
  // shows the slide with no theme.
  if (layoutPart) {
    const depth = partName.split('/').length - 1;
    const target = `../${layoutPart.split('/').slice(1).join('/')}`;
    const relsBody = '<?xml version="1.0" encoding="UTF-8" standalone="yes"?>\n'
      + '<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">'
      + `<Relationship Id="rId1" Type="${LAYOUT_REL_TYPE}" Target="${target}"/></Relationships>`;
    model.pkg.setText(relsPathFor(partName), relsBody);
    void depth;
  }

  const relId = await addRelationship(model.pkg, 'ppt/presentation.xml', {
    type: SLIDE_REL_TYPE,
    target: partName.replace(/^ppt\//, ''),
  });

  const sldIdValue = nextSldIdValue(entries);
  const entryXml = `<p:sldId id="${sldIdValue}" r:id="${relId}"/>`;

  const currentXml = await readPresentationXml(model);
  const tree = scanXml(currentXml);
  const list = findDescendant(tree, 'p:sldIdLst');
  if (!list) throw new Error('演示文稿缺少幻灯片列表，无法新增页');

  const insertIndex = at == null ? entries.length : Math.max(0, Math.min(at, entries.length));
  if (insertIndex >= entries.length) {
    model.pkg.setText('ppt/presentation.xml',
      currentXml.slice(0, list.innerEnd) + entryXml + currentXml.slice(list.innerEnd));
  } else {
    const anchor = entries[insertIndex].node;
    model.pkg.setText('ppt/presentation.xml',
      currentXml.slice(0, anchor.start) + entryXml + currentXml.slice(anchor.start));
  }

  return { partName, sldIdValue, relId };
}

/** Remove every relationship whose resolved target is the given slide part. */
async function removeSlideRelationship(pkg, partName) {
  const relsPart = 'ppt/_rels/presentation.xml.rels';
  const xml = await pkg.text(relsPart);
  if (!xml) return null;
  const tree = scanXml(xml);
  const wanted = partName.replace(/^ppt\//, '');
  const target = findChildren(tree, 'Relationship').find(rel => {
    const value = attr(rel, 'Target');
    return value === wanted || value === `/${partName}`;
  });
  if (!target) return null;
  const id = attr(target, 'Id');
  pkg.setText(relsPart, xml.slice(0, target.start) + xml.slice(target.end));
  return id;
}

/**
 * Delete a slide.
 *
 * Refuses to delete the last slide: a presentation with no slides is technically writable
 * but every consumer of this editor expects at least one, and a blank document the user
 * cannot undo back from is worse than a disabled button.
 */
export async function deleteSlide(model, slideIndex) {
  if (model.slides.length <= 1) throw new Error('这是最后一页，不能再删');
  const slide = model.slides[slideIndex];
  if (!slide) throw new Error('找不到要删除的页面');

  const rId = await removeSlideRelationship(model.pkg, slide.part);

  const presentationXml = await readPresentationXml(model);
  const { entries } = readSlideOrder(presentationXml);
  const entry = entries.find(item => item.rid === rId) || entries[slideIndex];
  if (!entry) throw new Error('演示文稿里找不到这一页的记录');

  model.pkg.setText('ppt/presentation.xml',
    presentationXml.slice(0, entry.node.start) + presentationXml.slice(entry.node.end));

  await removeContentTypeOverride(model.pkg, slide.part);
  model.pkg.removePart?.(slide.part);
  return { partName: slide.part };
}

/**
 * Duplicate a slide.
 *
 * The copy keeps the same layout and keeps `r:embed` references to shared media, which
 * stay valid because they resolve relative to the slide's own folder.
 */
export async function duplicateSlide(model, slideIndex) {
  const slide = model.slides[slideIndex];
  if (!slide) throw new Error('找不到要复制的页面');
  const sourceXml = await model.pkg.text(slide.part);
  if (!sourceXml) throw new Error('这一页的内容读不出来');

  const number = nextSlideNumber(model.pkg);
  const partName = `ppt/slides/slide${number}.xml`;
  model.pkg.setText(partName, sourceXml);
  await ensureContentTypeOverride(model.pkg, partName, SLIDE_CONTENT_TYPE);

  // Copy the source slide's relationships verbatim: layout and media targets are relative
  // and remain correct from the new file's location in the same folder.
  const sourceRels = await model.pkg.text(relsPathFor(slide.part));
  if (sourceRels) model.pkg.setText(relsPathFor(partName), sourceRels);

  const relId = await addRelationship(model.pkg, 'ppt/presentation.xml', {
    type: SLIDE_REL_TYPE,
    target: partName.replace(/^ppt\//, ''),
  });

  const presentationXml = await readPresentationXml(model);
  const { entries } = readSlideOrder(presentationXml);
  const entryXml = `<p:sldId id="${nextSldIdValue(entries)}" r:id="${relId}"/>`;
  const anchor = entries[slideIndex];
  if (!anchor) throw new Error('演示文稿里找不到这一页的记录');

  model.pkg.setText('ppt/presentation.xml',
    presentationXml.slice(0, anchor.node.end) + entryXml + presentationXml.slice(anchor.node.end));

  return { partName, relId };
}

/**
 * Move a slide to a new position.
 *
 * Only the sldIdLst order changes; the parts and relationships stay as they are. That is
 * why reordering is the safest of the four operations and does not need a new part.
 */
export async function moveSlide(model, from, to) {
  const presentationXml = await readPresentationXml(model);
  const { list, entries } = readSlideOrder(presentationXml);
  if (from < 0 || from >= entries.length) throw new Error('要移动的页面不存在');
  if (!list) throw new Error('演示文稿缺少幻灯片列表');
  const target = Math.max(0, Math.min(to, entries.length - 1));
  if (target === from) return { moved: false };

  // Rebuild the list from the ordered node strings. Splicing the original document at an
  // entry's old offset is wrong after an earlier entry was removed: every offset after the
  // removal shifts left by that entry's length, so the insertion can land inside the next
  // `p:sldId` and make PowerPoint drop it.
  const ordered = entries.map(entry => presentationXml.slice(entry.node.start, entry.node.end));
  const [moving] = ordered.splice(from, 1);
  ordered.splice(target, 0, moving);
  const working = presentationXml.slice(0, list.innerStart)
    + ordered.join('')
    + presentationXml.slice(list.innerEnd);

  model.pkg.setText('ppt/presentation.xml', working);
  return { moved: true };
}

export { SLIDE_CONTENT_TYPE, SLIDE_REL_TYPE, nextSldIdValue };
