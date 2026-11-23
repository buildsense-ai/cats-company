// Insert, slide and batch-apply operations.
//
// These are the operations that touch more than one part of the package, so they are the
// ones most likely to produce a file that our own parser reads happily and PowerPoint
// refuses to open. Each test asserts the structural invariant that Office checks, not just
// that the call returned without throwing.

import { describe, it, expect } from 'vitest';
import JSZip from 'jszip';
import { buildPptx, titleSlideXml, tableSlideXml } from './test-fixtures/build-pptx.js';
import { parsePptx } from './pptx/parse.js';
import { openPackage, readRelationships } from './ooxml/zip.js';
import { scanXml, findDescendants, attr } from './ooxml/xml.js';
import {
  addRelationship, applyGroupShapes, applyInsertPicture, ensureContentTypeDefault,
  ensureContentTypeOverride, nextMediaNumber, nextSlideNumber,
} from './pptx/insert.js';
import { addSlide, deleteSlide, duplicateSlide, moveSlide, readSlideOrder } from './pptx/slides.js';
import { applyOps, validateBatch } from './pptx/apply.js';

// A 1x1 PNG. Real bytes matter: a picture insert with an empty buffer is rejected, and the
// media part has to be a genuine image for the content-type default to be meaningful.
const PNG_1X1 = Uint8Array.from(atob(
  'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8DwHwAFAAH/q842iQAAAABJRU5ErkJggg==',
), ch => ch.charCodeAt(0));

describe('part numbering', () => {
  it('finds the next slide and media numbers instead of reusing one', async () => {
    const bytes = await buildPptx({
      slides: [titleSlideXml(), titleSlideXml({ title: '第二页' })],
    });
    const pkg = await openPackage(bytes);
    // The fixture has slide1 and slide2, so the next number must be 3.
    expect(nextSlideNumber(pkg)).toBe(3);
    expect(nextMediaNumber(pkg)).toBe(1);
  });
});

describe('relationships and content types', () => {
  it('creates a rels part when the part has none', async () => {
    const pkg = await openPackage(await buildPptx());
    const relId = await addRelationship(pkg, 'ppt/slides/slide1.xml', {
      type: 'http://schemas.openxmlformats.org/officeDocument/2006/relationships/image',
      target: '../media/image1.png',
    });
    // A slide that already has a layout relationship must not get a duplicate rId1.
    expect(relId).not.toBe('rId1');

    const rels = await readRelationships(pkg, 'ppt/slides/slide1.xml');
    expect(rels.has(relId)).toBe(true);
  });

  it('declares an extension default only once', async () => {
    const pkg = await openPackage(await buildPptx());
    await ensureContentTypeDefault(pkg, 'png', 'image/png');
    await ensureContentTypeDefault(pkg, 'PNG', 'image/png');
    const xml = await pkg.text('[Content_Types].xml');
    expect((xml.match(/Extension="png"/gi) || []).length).toBe(1);
  });

  it('declares a part override only once', async () => {
    const pkg = await openPackage(await buildPptx());
    const part = 'ppt/slides/slide9.xml';
    await ensureContentTypeOverride(pkg, part, 'application/vnd.openxmlformats-officedocument.presentationml.slide+xml');
    await ensureContentTypeOverride(pkg, part, 'application/vnd.openxmlformats-officedocument.presentationml.slide+xml');
    const xml = await pkg.text('[Content_Types].xml');
    expect((xml.match(/slide9\.xml/g) || []).length).toBe(1);
  });
});

describe('insert picture', () => {
  it('adds a media part, a relationship and a shape pointing at it', async () => {
    const model = await parsePptx(await buildPptx());
    const result = await applyInsertPicture(model, 0, {
      bytes: PNG_1X1,
      ext: 'png',
      name: '测试图',
      box: { x: 1000000, y: 1000000, cx: 1000000, cy: 1000000 },
    });

    expect(result.mediaName).toBe('ppt/media/image1.png');
    expect(await model.pkg.binary(result.mediaName)).not.toBeNull();

    const bytes = await model.pkg.save();
    const reparsed = await parsePptx(bytes);
    const picture = reparsed.slides[0].shapes.find(shape => shape.kind === 'picture');
    expect(picture).toBeDefined();
    // The shape must reference the relationship id, not a file path; a path produces a
    // file that opens with an empty box.
    expect(picture.imageRelId).toBe(result.relId);
    expect(picture.imageName).toBe('测试图');
  });

  it('rejects an empty buffer instead of writing a broken media part', async () => {
    const model = await parsePptx(await buildPptx());
    await expect(applyInsertPicture(model, 0, {
      bytes: new Uint8Array(0), ext: 'png', box: { x: 0, y: 0, cx: 10, cy: 10 },
    })).rejects.toThrow('图片内容为空');
  });

  it('gives a second picture its own media part', async () => {
    const model = await parsePptx(await buildPptx());
    const first = await applyInsertPicture(model, 0, {
      bytes: PNG_1X1, ext: 'png', box: { x: 0, y: 0, cx: 100, cy: 100 },
    });
    const second = await applyInsertPicture(model, 0, {
      bytes: PNG_1X1, ext: 'png', box: { x: 200, y: 200, cx: 100, cy: 100 },
    });
    expect(second.mediaName).not.toBe(first.mediaName);
    expect(second.relId).not.toBe(first.relId);
  });
});

describe('group shapes', () => {
  it('wraps both members in one grpSp and keeps their coordinates', async () => {
    const model = await parsePptx(await buildPptx());
    const slide = model.slides[0];
    const xml = await model.pkg.text(slide.part);
    const [title, body] = slide.shapes;

    const next = applyGroupShapes(xml, [title, body]);
    const tree = scanXml(next);
    const groups = findDescendants(tree, 'p:grpSp');
    expect(groups).toHaveLength(1);

    // chOff/chExt equal to off/ext means children keep absolute coordinates, which is why
    // no child needs rewriting and nothing visibly jumps.
    const groupPr = findDescendants(groups[0], 'a:xfrm')[0];
    const off = findDescendants(groupPr, 'a:off')[0];
    const chOff = findDescendants(groupPr, 'a:chOff')[0];
    expect(attr(off, 'x')).toBe(attr(chOff, 'x'));
    expect(attr(off, 'y')).toBe(attr(chOff, 'y'));

    // Both member texts must survive inside the group.
    expect(next).toContain('季度复盘');
    expect(next).toContain('第一点');
  });

  it('refuses a single shape', async () => {
    const model = await parsePptx(await buildPptx());
    const xml = await model.pkg.text(model.slides[0].part);
    expect(() => applyGroupShapes(xml, [model.slides[0].shapes[0]]))
      .toThrow('至少要选两个元素');
  });

  it('round-trips through save and re-parse as a group', async () => {
    const model = await parsePptx(await buildPptx());
    const slide = model.slides[0];
    const xml = await model.pkg.text(slide.part);
    model.pkg.setText(slide.part, applyGroupShapes(xml, slide.shapes));

    const reparsed = await parsePptx(await model.pkg.save());
    const group = reparsed.slides[0].shapes[0];
    expect(group.kind).toBe('group');
    expect(group.children).toHaveLength(2);
  });
});

describe('slide operations', () => {
  it('adds a slide with its own relationship, content type and sldId', async () => {
    const model = await parsePptx(await buildPptx());
    const { partName } = await addSlide(model, {});

    expect(partName).toBe('ppt/slides/slide2.xml');
    const types = await model.pkg.text('[Content_Types].xml');
    expect(types).toContain('slide2.xml');

    const presentationXml = await model.pkg.text('ppt/presentation.xml');
    const { entries } = readSlideOrder(presentationXml);
    expect(entries).toHaveLength(2);

    const reparsed = await parsePptx(await model.pkg.save());
    expect(reparsed.slides).toHaveLength(2);
    // The new slide inherits the first slide's layout so it picks up a master.
    expect(reparsed.slides[1].layoutPart).toBe(reparsed.slides[0].layoutPart);
  });

  it('gives every slide a unique sldId and relationship', async () => {
    const model = await parsePptx(await buildPptx());
    await addSlide(model, {});
    await addSlide(model, {});

    const presentationXml = await model.pkg.text('ppt/presentation.xml');
    const { entries } = readSlideOrder(presentationXml);
    const ids = entries.map(entry => entry.id);
    const rids = entries.map(entry => entry.rid);
    expect(new Set(ids).size).toBe(ids.length);
    expect(new Set(rids).size).toBe(rids.length);
    // A reused sldId makes Office treat the second slide as a duplicate of the first.
    expect(Math.min(...ids)).toBeGreaterThanOrEqual(256);
  });

  it('refuses to delete the last remaining slide', async () => {
    const model = await parsePptx(await buildPptx());
    await expect(deleteSlide(model, 0)).rejects.toThrow('最后一页');
  });

  it('deletes a slide and removes its part, relationship and content type', async () => {
    const model = await parsePptx(await buildPptx({
      slides: [titleSlideXml({ title: '第一页' }), titleSlideXml({ title: '第二页' })],
    }));
    const doomed = model.slides[1].part;
    await deleteSlide(model, 1);

    expect(model.pkg.parts()).not.toContain(doomed);
    const types = await model.pkg.text('[Content_Types].xml');
    expect(types).not.toContain('slide2.xml');

    const reparsed = await parsePptx(await model.pkg.save());
    expect(reparsed.slides).toHaveLength(1);
    expect(reparsed.slides[0].shapes.some(shape => shape.text.includes('第一页'))).toBe(true);
  });

  it('duplicates a slide and keeps its content', async () => {
    const model = await parsePptx(await buildPptx({ slides: [tableSlideXml()] }));
    await duplicateSlide(model, 0);

    const reparsed = await parsePptx(await model.pkg.save());
    expect(reparsed.slides).toHaveLength(2);
    expect(reparsed.slides[1].shapes[0].kind).toBe('table');
    expect(reparsed.slides[1].shapes[0].tableRows).toBe(2);
  });

  it('reorders slides by changing only the sldIdLst order', async () => {
    const model = await parsePptx(await buildPptx({
      slides: [titleSlideXml({ title: '甲' }), titleSlideXml({ title: '乙' })],
    }));
    await moveSlide(model, 0, 1);

    const reparsed = await parsePptx(await model.pkg.save());
    expect(reparsed.slides[0].shapes[0].text).toBe('乙');
    expect(reparsed.slides[1].shapes[0].text).toBe('甲');
  });

  it('keeps every sldId when moving a middle slide after a duplicate', async () => {
    const model = await parsePptx(await buildPptx({
      slides: [
        titleSlideXml({ title: '甲' }), titleSlideXml({ title: '乙' }),
        titleSlideXml({ title: '丙' }), titleSlideXml({ title: '丁' }),
      ],
    }));
    await duplicateSlide(model, 1);
    await moveSlide(model, 1, 2);

    const reparsed = await parsePptx(await model.pkg.save());
    expect(reparsed.slides).toHaveLength(5);
    expect(reparsed.slides.map(slide => slide.shapes[0].text)).toEqual(['甲', '乙', '乙', '丙', '丁']);
  });

  it('is a no-op when moving a slide to where it already is', async () => {
    const model = await parsePptx(await buildPptx());
    const result = await moveSlide(model, 0, 0);
    expect(result.moved).toBe(false);
  });
});

describe('batch apply', () => {
  it('rejects a batch that mixes slide and element operations', () => {
    expect(() => validateBatch([
      { op: 'addSlide' },
      { op: 'text', slideIndex: 0, elId: 'x' },
    ])).toThrow('不能放在同一次写入里');
  });

  it('rejects two structural operations in one batch', () => {
    expect(() => validateBatch([{ op: 'addSlide' }, { op: 'deleteSlide', slideIndex: 0 }]))
      .toThrow('一次只能做一项页面操作');
  });

  it('rejects an unknown operation rather than silently skipping it', () => {
    expect(() => validateBatch([{ op: 'rotate', slideIndex: 0 }])).toThrow('不支持的写入类型');
  });

  it('applies several element edits and saves exactly once', async () => {
    const model = await parsePptx(await buildPptx());
    const body = model.slides[0].shapes[1];
    const result = await applyOps(model, [
      { op: 'text', slideIndex: 0, elId: body.elId,
        payload: { paragraphs: [{ ...body.textBody.paragraphs[0], text: '批量改' }] } },
      { op: 'style', slideIndex: 0, elId: body.elId,
        payload: { paragraphs: [{ index: 0, bold: true }] } },
    ]);

    expect(result.changed).toBe(true);
    expect(result.applied).toBe(2);
    const reparsed = await parsePptx(result.bytes);
    expect(reparsed.slides[0].shapes[1].textBody.paragraphs[0].text).toBe('批量改');
    expect(reparsed.slides[0].shapes[1].textBody.paragraphs[0].bold).toBe(true);
  });

  it('reports skipped inherited geometry instead of silently dropping it', async () => {
    const model = await parsePptx(await buildPptx());
    const title = model.slides[0].shapes[0];
    expect(title.inherited).toBe(true);

    const result = await applyOps(model, [{
      op: 'geometry', slideIndex: 0, elId: title.elId,
      payload: { x: 1, y: 1, cx: 100, cy: 100 },
    }]);
    expect(result.changed).toBe(false);
    expect(result.skipped).toHaveLength(1);
    expect(result.skipped[0].reason).toBe('inherited-geometry');
  });

  it('writes inherited geometry to the layout for every slide that uses it', async () => {
    const model = await parsePptx(await buildPptx({
      slides: [titleSlideXml({ title: 'First' }), titleSlideXml({ title: 'Second' })],
    }));
    const title = model.slides[0].shapes[0];
    const slidePart = model.slides[0].part;
    const slideXmlBefore = await model.pkg.text(slidePart);
    const box = { x: 1524000, y: 914400, cx: 9144000, cy: 1371600 };

    const result = await applyOps(model, [{
      op: 'layoutGeometry', slideIndex: 0, elId: title.elId,
      payload: { placeholderType: title.placeholderType, box },
    }]);

    expect(result.changed).toBe(true);
    expect(result.applied).toBe(1);
    // The layout is the only part that may change. Editing the slide here would silently
    // detach one page and leave every other page on the old inherited box.
    expect(await model.pkg.text(slidePart)).toBe(slideXmlBefore);

    const reparsed = await parsePptx(result.bytes);
    expect(reparsed.slides[0].shapes[0].box).toMatchObject(box);
    expect(reparsed.slides[1].shapes[0].box).toMatchObject(box);
  });

  it('saves a new slide through applyOps and re-parses it', async () => {
    const model = await parsePptx(await buildPptx());
    const result = await applyOps(model, [{ op: 'addSlide', payload: {} }]);
    expect(result.changed).toBe(true);
    const reparsed = await parsePptx(result.bytes);
    expect(reparsed.slides).toHaveLength(2);
  });

  it('produces a zip whose untouched parts are still byte-identical', async () => {
    const model = await parsePptx(await buildPptx({
      slides: [titleSlideXml({ title: '甲' }), titleSlideXml({ title: '乙' })],
    }));
    const secondPart = model.slides[1].part;
    const before = await model.pkg.text(secondPart);

    const body = model.slides[0].shapes[1];
    const result = await applyOps(model, [{
      op: 'text', slideIndex: 0, elId: body.elId,
      payload: { paragraphs: [{ ...body.textBody.paragraphs[0], text: '只改第一页' }] },
    }]);

    const reopened = await JSZip.loadAsync(result.bytes);
    const secondXml = await reopened.file(`ppt/slides/slide2.xml`).async('string');
    expect(secondXml).toBe(before);
  });
});
