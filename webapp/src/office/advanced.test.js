// Group-child selection, Word cell editing, Word picture insertion.
//
// These three share a property: each one touches something the earlier phases deliberately left
// read-only, so every assertion here is about a boundary that just moved. The tests are grouped in
// one file because the failure mode is the same — a write that looks fine in memory and produces a
// file Office rejects — and one file makes that intent visible.

import { describe, it, expect } from 'vitest';
import JSZip from 'jszip';
import { buildPptx, titleSlideXml } from './test-fixtures/build-pptx.js';
import { buildDocx, buildSampleDocx, paragraph, run, table } from './test-fixtures/build-docx.js';
import { parsePptx } from './pptx/parse.js';
import { parseDocx } from './docx/parse.js';
import { applyGroupShapes } from './pptx/insert.js';
import { absoluteFromRelative, relativeFromAbsolute } from './pptx/geometry.js';
import { applyTableCellText, validateDocumentPart } from './docx/write.js';
import { applyInsertImage, nextDocPrId, readImageSize, usableWidthTwips } from './docx/insert.js';
import { BLOCK_KINDS } from './docx/model.js';

const PNG_1X1 = Uint8Array.from(Buffer.from(
  'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8DwHwAFAAH/q842iQAAAABJRU5ErkJggg==',
  'base64',
));
const PNG_2X1 = Uint8Array.from(Buffer.from(
  'iVBORw0KGgoAAAANSUhEUgAAAAIAAAABCAYAAAD0In+KAAAAAXNSR0IArs4c6QAAAARnQU1BAACxjwv8YQUAAAAJcEhZcwAADsMAAA7DAcdvqGQAAAAOSURBVBhXY/jPwABC/wEP+QP98+IdQAAAAABJRU5ErkJggg==',
  'base64',
));

describe('group child coordinates', () => {
  it('maps a child box from group space into slide space', () => {
    const groupBox = { x: 1000000, y: 500000, cx: 2000000, cy: 1000000 };
    const chOffset = { x: 0, y: 0 };
    const chExtent = { x: 2000000, y: 1000000 };
    const child = { x: 500000, y: 250000, cx: 400000, cy: 200000 };

    const abs = absoluteFromRelative(child, groupBox, chOffset, chExtent);
    // scale is 1 on both axes here, so the result is a pure translation by the group origin.
    expect(abs.x).toBe(1500000);
    expect(abs.y).toBe(750000);
    expect(abs.cx).toBe(400000);
  });

  it('applies the chExt scale rather than assuming 1:1', () => {
    const groupBox = { x: 0, y: 0, cx: 4000000, cy: 2000000 };
    const chOffset = { x: 0, y: 0 };
    // The child space is half the size of the group box, so children are drawn at 2x.
    const chExtent = { x: 2000000, y: 1000000 };
    const child = { x: 100000, y: 100000, cx: 500000, cy: 500000 };

    const abs = absoluteFromRelative(child, groupBox, chOffset, chExtent);
    expect(abs.x).toBe(200000);
    expect(abs.cx).toBe(1000000);
  });

  it('round-trips through the inverse without drift', () => {
    const groupBox = { x: 1200000, y: 800000, cx: 3000000, cy: 1500000 };
    const chOffset = { x: 100000, y: 50000 };
    const chExtent = { x: 1500000, y: 750000 };
    const child = { x: 400000, y: 200000, cx: 300000, cy: 150000 };

    const abs = absoluteFromRelative(child, groupBox, chOffset, chExtent);
    const back = relativeFromAbsolute(abs, groupBox, chOffset, chExtent);
    expect(back).toEqual(child);
  });

  it('passes the box through when the group has no child extent', () => {
    // A group with chExt 0 is malformed; returning the input beats dividing by zero and painting
    // every child at NaN.
    const child = { x: 1, y: 2, cx: 3, cy: 4 };
    expect(absoluteFromRelative(child, { x: 0, y: 0, cx: 10, cy: 10 }, null, null)).toBe(child);
  });
});

describe('grouped shapes stay addressable', () => {
  it('keeps every child in the parsed model after grouping', async () => {
    const model = await parsePptx(await buildPptx());
    const slide = model.slides[0];
    const xml = await model.pkg.text(slide.part);
    model.pkg.setText(slide.part, applyGroupShapes(xml, slide.shapes));

    const reparsed = await parsePptx(await model.pkg.save());
    const group = reparsed.slides[0].shapes[0];
    expect(group.kind).toBe('group');
    expect(group.children).toHaveLength(2);

    // Each child must carry its own id and be marked relative, which is what tells the renderer
    // and the writer not to treat its box as absolute.
    for (const child of group.children) {
      expect(child.elId).toBeTruthy();
      expect(child.positioning).toBe('relative');
      expect(child.parentGroupId).toBe(group.elId);
    }
  });

  it('gives each child a distinct anchor path so a click resolves to one node', async () => {
    const model = await parsePptx(await buildPptx());
    const slide = model.slides[0];
    const xml = await model.pkg.text(slide.part);
    model.pkg.setText(slide.part, applyGroupShapes(xml, slide.shapes));

    const reparsed = await parsePptx(await model.pkg.save());
    const children = reparsed.slides[0].shapes[0].children;
    const paths = children.map(child => child.anchor.path.join('.'));
    expect(new Set(paths).size).toBe(paths.length);
    // The group itself is a prefix of each child path, which is what makes the lookup a walk
    // rather than a search.
    const groupPath = reparsed.slides[0].shapes[0].anchor.path.join('.');
    for (const path of paths) expect(path.startsWith(`${groupPath}.`)).toBe(true);
  });
});

describe('docx table cell editing', () => {
  async function modelWithTable() {
    const model = await parseDocx(await buildSampleDocx());
    const block = model.blocks.find(item => item.kind === BLOCK_KINDS.TABLE);
    const xml = await model.pkg.text(model.part);
    return { model, block, xml };
  }

  it('replaces the text of one cell and leaves the others alone', async () => {
    const { model, block, xml } = await modelWithTable();
    const cell = block.cells[0];
    expect(cell.text).toBe('Metric');

    const next = applyTableCellText(xml, cell, 'Measure');
    expect(next).toContain('Measure');
    expect(next).not.toContain('>Metric<');
    // Neighbouring cells must be untouched, including their column positions.
    expect(next).toContain('>Last<');
    expect(next).toContain('>Now<');
  });

  it('round-trips through save and re-parse', async () => {
    const { model, block, xml } = await modelWithTable();
    const cell = block.cells.find(item => item.text === '2 weeks');
    model.pkg.setText(model.part, applyTableCellText(xml, cell, '1 week'));

    const reparsed = await parseDocx(await model.pkg.save());
    const tableBlock = reparsed.blocks.find(item => item.kind === BLOCK_KINDS.TABLE);
    expect(tableBlock.cells.some(item => item.text === '1 week')).toBe(true);
    expect(tableBlock.cells.some(item => item.text === '2 weeks')).toBe(false);
    // The table's shape must not change; rewriting a cell is not a structural edit.
    expect(tableBlock.rows).toBe(block.rows);
    expect(tableBlock.cols).toBe(block.cols);
  });

  it('writes multiple lines as one paragraph with breaks', async () => {
    const { model, block, xml } = await modelWithTable();
    const next = applyTableCellText(xml, block.cells[0], 'first\nsecond');
    expect(next).toContain('first');
    expect(next).toContain('second');
    // A cell must keep at least one paragraph, so the line break is a w:br rather than a new p.
    expect(validateDocumentPart(next)).toEqual([]);
  });

  it('escapes an ampersand so the cell cannot break the document', async () => {
    const { model, block, xml } = await modelWithTable();
    const next = applyTableCellText(xml, block.cells[0], 'a & b');
    expect(next).toContain('a &amp; b');
    expect(validateDocumentPart(next)).toEqual([]);
  });

  it('refuses a cell with no paragraph to write into', async () => {
    const { xml } = await modelWithTable();
    expect(() => applyTableCellText(xml, { row: 0, col: 0 }, 'x')).toThrow();
  });
});

describe('docx picture insertion', () => {
  it('reads a non-square PNG header and preserves its aspect ratio', async () => {
    expect(readImageSize(PNG_2X1, 'png')).toEqual({ width: 2, height: 1 });

    const model = await parseDocx(await buildDocx(paragraph(['before'])));
    const result = await applyInsertImage(model, {
      bytes: PNG_2X1, ext: 'png', name: 'wide.png', widthCm: 10,
    });

    // The old writer hard-coded height = width, which turned every photo into a square.
    expect(result.pixelWidth).toBe(2);
    expect(result.pixelHeight).toBe(1);
    expect(result.heightEmu).toBe(Math.round(result.widthEmu / 2));
  });

  it('adds a media part, a relationship and a drawing that points at it', async () => {
    const model = await parseDocx(await buildDocx(paragraph(['before'])));
    const result = await applyInsertImage(model, {
      bytes: PNG_1X1, ext: 'png', name: 'chart.png', widthCm: 10,
    });

    expect(result.mediaName).toBe('word/media/image1.png');
    expect(await model.pkg.binary(result.mediaName)).not.toBeNull();

    const xml = await model.pkg.text(model.part);
    // The drawing must reference the relationship id, never a file path: a path opens in Word as
    // a broken-image placeholder rather than an error.
    expect(xml).toContain(`r:embed="${result.relId}"`);
    expect(xml).toContain('wp:inline');
    expect(xml).toContain('<w:drawing>');
  });

  it('declares the extension so Word can resolve the media type', async () => {
    const model = await parseDocx(await buildDocx(paragraph(['x'])));
    await applyInsertImage(model, { bytes: PNG_1X1, ext: 'png', name: 'p.png' });
    const types = await model.pkg.text('[Content_Types].xml');
    expect(types).toMatch(/Extension="png"/i);
  });

  it('gives a second picture its own media part and docPr id', async () => {
    const model = await parseDocx(await buildDocx(paragraph(['x'])));
    const first = await applyInsertImage(model, { bytes: PNG_1X1, ext: 'png', name: 'a.png' });
    const second = await applyInsertImage(model, { bytes: PNG_1X1, ext: 'png', name: 'b.png' });

    expect(second.mediaName).not.toBe(first.mediaName);
    expect(second.relId).not.toBe(first.relId);
    // wp:docPr/@id must be unique document-wide; a duplicate makes Word drop one of the pictures.
    const ids = [...(await model.pkg.text(model.part)).matchAll(/wp:docPr id="(\d+)"/g)]
      .map(match => match[1]);
    expect(new Set(ids).size).toBe(ids.length);
  });

  it('fits the picture inside the text column instead of the requested width', async () => {
    const model = await parseDocx(await buildDocx(paragraph(['x'])));
    // A4 with 1 inch margins leaves about 6.27 inches of text width.
    const result = await applyInsertImage(model, {
      bytes: PNG_1X1, ext: 'png', name: 'wide.png', widthCm: 40,
    });
    const usableEmu = Math.round((usableWidthTwips({ children: [] }) / 1440) * 914400);
    expect(result.widthEmu).toBeLessThanOrEqual(usableEmu);
  });

  it('rejects empty bytes rather than writing an empty media part', async () => {
    const model = await parseDocx(await buildDocx(paragraph(['x'])));
    await expect(applyInsertImage(model, {
      bytes: new Uint8Array(0), ext: 'png', name: 'x.png',
    })).rejects.toThrow('图片内容为空');
  });

  it('keeps the document valid and re-parses after insertion', async () => {
    const model = await parseDocx(await buildDocx(paragraph(['before'])));
    await applyInsertImage(model, { bytes: PNG_1X1, ext: 'png', name: 'p.png' });

    const xml = await model.pkg.text(model.part);
    expect(validateDocumentPart(xml)).toEqual([]);

    const saved = await model.pkg.save();
    const zip = await JSZip.loadAsync(saved);
    expect(Object.keys(zip.files)).toContain('word/media/image1.png');

    const reparsed = await parseDocx(saved);
    // The original paragraph survives; the picture is appended as its own paragraph.
    expect(reparsed.blocks.some(block => block.text === 'before')).toBe(true);
    expect(reparsed.blocks.length).toBe(2);
  });

  it('places the picture before sectPr so the section stays last', async () => {
    const model = await parseDocx(await buildDocx(paragraph(['x'])));
    await applyInsertImage(model, { bytes: PNG_1X1, ext: 'png', name: 'p.png' });
    const xml = await model.pkg.text(model.part);
    expect(xml.indexOf('<w:drawing>')).toBeLessThan(xml.indexOf('<w:sectPr>'));
  });
});

describe('docPr id allocation', () => {
  it('starts at 1 for a document with no drawings', () => {
    expect(nextDocPrId('<w:document><w:body/></w:document>')).toBe(1);
  });

  it('continues past the highest existing id', () => {
    const xml = '<w:document><w:body><wp:docPr id="7"/><wp:docPr id="3"/></w:body></w:document>';
    expect(nextDocPrId(xml)).toBe(8);
  });
});
