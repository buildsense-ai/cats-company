import { describe, it, expect } from 'vitest';
import JSZip from 'jszip';
import { buildPptx, titleSlideXml, tableSlideXml } from './test-fixtures/build-pptx.js';
import { openPackage, detectFamily } from './ooxml/zip.js';
import { scanXml, findDescendants, applyEdits, escapeText } from './ooxml/xml.js';
import { parsePptx } from './pptx/parse.js';
import {
  applyTextEdits, applyGeometryEdit, applyDeleteShape, nextShapeId, insertTextBox, insertTable,
} from './pptx/write.js';

describe('ooxml xml locator', () => {
  it('records absolute ranges so a splice leaves the rest of the part untouched', () => {
    const xml = '<p:root><p:a x="1">hello</p:a><p:b>world</p:b></p:root>';
    const tree = scanXml(xml);
    const a = findDescendants(tree, 'p:a')[0];

    // The range must point at the node itself, and slicing it must reproduce the node.
    expect(xml.slice(a.start, a.end)).toBe('<p:a x="1">hello</p:a>');
    expect(xml.slice(a.innerStart, a.innerEnd)).toBe('hello');

    const next = applyEdits(xml, [{ start: a.innerStart, end: a.innerEnd, replacement: 'bye' }]);
    expect(next).toBe('<p:root><p:a x="1">bye</p:a><p:b>world</p:b></p:root>');
  });

  it('decodes attribute values and keeps self-closing tags distinct from open ones', () => {
    const xml = '<p:r><p:self a="x"/><p:open a="y">t</p:open></p:r>';
    const tree = scanXml(xml);
    const nodes = findDescendants(tree, 'p:self');
    expect(nodes).toHaveLength(1);
    expect(nodes[0].selfClosing).toBe(true);
    expect(nodes[0].attrs.a).toBe('x');
  });

  it('refuses overlapping edits instead of producing silently corrupted xml', () => {
    const xml = '<r><a>1</a><b>2</b></r>';
    expect(() => applyEdits(xml, [
      { start: 3, end: 8, replacement: 'X' },
      { start: 5, end: 10, replacement: 'Y' },
    ])).toThrow('overlapping xml edits');
  });

  it('escapes text so a user typo cannot break the xml', () => {
    expect(escapeText('a & b < c')).toBe('a &amp; b &lt; c');
  });
});

describe('ooxml package', () => {
  it('detects the family from parts rather than the file extension', async () => {
    const bytes = await buildPptx();
    const pkg = await openPackage(bytes);
    expect(detectFamily(pkg)).toBe('pptx');
  });

  it('returns byte-identical content for every part it did not edit', async () => {
    const bytes = await buildPptx();
    const pkg = await openPackage(bytes);
    const original = await pkg.text('ppt/slideLayouts/slideLayout1.xml');
    pkg.setText('ppt/slides/slide1.xml', '<p:sld/>');
    const saved = await pkg.save();

    const reopened = await openPackage(saved);
    expect(await reopened.text('ppt/slideLayouts/slideLayout1.xml')).toBe(original);
    expect(await reopened.text('ppt/slides/slide1.xml')).toBe('<p:sld/>');
  });
});

describe('pptx parse', () => {
  it('rejects a package that is not a pptx', async () => {
    const zip = await JSZip.loadAsync(await buildPptx());
    zip.remove('ppt/presentation.xml');
    const bytes = await zip.generateAsync({ type: 'uint8array' });
    await expect(parsePptx(bytes)).rejects.toThrow('这不是一个有效的 PPTX 文件');
  });

  it('reads slide size, theme colors and both shapes of a title slide', async () => {
    const model = await parsePptx(await buildPptx());
    expect(model.family).toBe('pptx');
    expect(model.slideSize).toEqual({ cx: 12192000, cy: 6858000 });
    expect(model.themeColors.accent1).toBe('#1F5EFF');
    expect(model.slides).toHaveLength(1);

    const shapes = model.slides[0].shapes;
    expect(shapes).toHaveLength(2);
    expect(shapes[0].role).toBe('title');
    expect(shapes[0].text).toBe('季度复盘');
    expect(shapes[0].box.cx).toBe(10515600);
    expect(shapes[1].text).toBe('第一点\n第二点');
    expect(shapes[1].textBody.paragraphs[0].color).toBe('#3D434D');
  });

  it('marks a placeholder without its own transform as inherited from the layout', async () => {
    const model = await parsePptx(await buildPptx());
    const title = model.slides[0].shapes[0];
    // The title carries no a:xfrm, so its box comes from the layout placeholder.
    expect(title.inherited).toBe(true);
    expect(title.inheritedBox).toBe(true);
    expect(title.box.cx).toBe(10515600);
  });

  it('gives every element a stable id that survives a re-parse', async () => {
    const bytes = await buildPptx();
    const first = await parsePptx(bytes);
    const second = await parsePptx(bytes);
    const ids = list => list.slides[0].shapes.map(shape => shape.elId);
    expect(ids(first)).toEqual(ids(second));
    expect(new Set(ids(first)).size).toBe(ids(first).length);
  });

  it('reads a table as rows, columns and cells', async () => {
    const model = await parsePptx(await buildPptx({ slides: [tableSlideXml({ rows: 2, cols: 3 })] }));
    const table = model.slides[0].shapes[0];
    expect(table.kind).toBe('table');
    expect(table.tableRows).toBe(2);
    expect(table.tableCols).toBe(3);
    expect(table.table.cells).toHaveLength(6);
    expect(table.table.cells[0].text).toBe('值');
  });

  it('resolves the layout and master chain for each slide', async () => {
    const model = await parsePptx(await buildPptx());
    const slide = model.slides[0];
    expect(slide.layoutPart).toBe('ppt/slideLayouts/slideLayout1.xml');
    expect(slide.masterPart).toBe('ppt/slideMasters/slideMaster1.xml');
    expect(slide.themePart).toBe('ppt/theme/theme1.xml');
  });
});

describe('pptx write', () => {
  it('rewrites only the edited paragraph and keeps the other paragraphs byte-identical', async () => {
    const bytes = await buildPptx();
    const model = await parsePptx(bytes);
    const body = model.slides[0].shapes[1];
    const partXml = await model.pkg.text(model.slides[0].part);

    const next = applyTextEdits(partXml, body, {
      paragraphs: [{ ...body.textBody.paragraphs[0], text: '改过的正文' }],
    });

    expect(next).toContain('<a:t>改过的正文</a:t>');
    // Title paragraph is untouched, including its run properties.
    expect(next).toContain('<a:t>季度复盘</a:t>');
    expect(next).toContain('sz="3200" b="1"');
    // Everything outside the edited paragraph keeps its original order.
    expect(next.indexOf('<p:cSld>')).toBe(partXml.indexOf('<p:cSld>'));
  });

  it('writes geometry only for shapes that own a transform', async () => {
    const bytes = await buildPptx();
    const model = await parsePptx(bytes);
    const body = model.slides[0].shapes[1];
    const partXml = await model.pkg.text(model.slides[0].part);

    const next = applyGeometryEdit(partXml, body, { x: 1000000, y: 2000000, cx: 3000000, cy: 4000000 });
    expect(next).toContain('<a:off x="1000000" y="2000000"/>');
    expect(next).toContain('<a:ext cx="3000000" cy="4000000"/>');

    const title = model.slides[0].shapes[0];
    expect(() => applyGeometryEdit(partXml, title, { x: 1, y: 1, cx: 1, cy: 1 }))
      .toThrow('继承自版式');
  });

  it('deletes a shape by removing exactly its node', async () => {
    const bytes = await buildPptx();
    const model = await parsePptx(bytes);
    const body = model.slides[0].shapes[1];
    const partXml = await model.pkg.text(model.slides[0].part);

    const next = applyDeleteShape(partXml, body);
    expect(next).not.toContain('正文 2');
    expect(next).toContain('标题 1');
    expect(next.length).toBeLessThan(partXml.length);
  });

  it('keeps shape ids unique when inserting', async () => {
    const bytes = await buildPptx();
    const model = await parsePptx(bytes);
    const partXml = await model.pkg.text(model.slides[0].part);

    // The fixture uses ids 1, 2 and 3, so the next id must be 4, not a duplicate.
    expect(nextShapeId(partXml)).toBe(4);

    const withBox = insertTextBox(partXml, {
      id: 4, text: '新增', box: { x: 100, y: 200, cx: 3000, cy: 4000 }, size: 18,
    });
    expect(withBox).toContain('id="4"');
    expect(nextShapeId(withBox)).toBe(5);

    const again = insertTextBox(withBox, {
      text: '第二个', box: { x: 100, y: 200, cx: 3000, cy: 4000 },
    });
    // The second insert must pick its own id instead of reusing 4.
    expect(again).toContain('id="5"');
  });

  it('emits a table grid whose width count matches the column count', async () => {
    const bytes = await buildPptx();
    const model = await parsePptx(bytes);
    const partXml = await model.pkg.text(model.slides[0].part);

    const next = insertTable(partXml, { rows: 2, cols: 4, box: { x: 0, y: 0, cx: 8000000, cy: 2000000 } });
    const gridCols = (next.match(/<a:gridCol /g) || []).length;
    const rows = (next.match(/<a:tr /g) || []).length;
    expect(gridCols).toBe(4);
    expect(rows).toBe(2);
    // Every row must carry exactly `cols` cells or PowerPoint reports the file as
    // damaged when it opens.
    for (const row of next.split('<a:tr ').slice(1)) {
      const cells = (row.split('</a:tr>')[0].match(/<a:tc>/g) || []).length;
      expect(cells).toBe(4);
    }
  });

  it('round-trips through a real zip and re-parses with the edit applied', async () => {
    const bytes = await buildPptx();
    const model = await parsePptx(bytes);
    const body = model.slides[0].shapes[1];

    const partXml = await model.pkg.text(model.slides[0].part);
    model.pkg.setText(model.slides[0].part, applyTextEdits(partXml, body, {
      paragraphs: [{ ...body.textBody.paragraphs[0], text: '保存后应该出现' }],
    }));
    const saved = await model.pkg.save();

    const reopened = await JSZip.loadAsync(saved);
    expect(Object.keys(reopened.files)).toContain('ppt/slides/slide1.xml');

    const reparsed = await parsePptx(saved);
    expect(reparsed.slides[0].shapes[1].text).toBe('保存后应该出现');
  });
});
