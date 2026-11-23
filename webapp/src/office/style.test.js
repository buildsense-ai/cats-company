// Style write-back and pre-save validation.
//
// These tests assert the two things that decide whether an edited file survives real
// PowerPoint: style lands on the run rather than the paragraph, and a structural mistake
// is refused before the package is written.

import { describe, it, expect } from 'vitest';
import { buildPptx, titleSlideXml } from './test-fixtures/build-pptx.js';
import { parsePptx } from './pptx/parse.js';
import {
  applyStyleEdits, validateSlidePart, writePptx, nextShapeId, insertTextBox, nodeAtPath,
} from './pptx/write.js';
import { scanXml, findDescendants, findChildren } from './ooxml/xml.js';

/**
 * Resolve the edited shape's own node in a freshly parsed part.
 *
 * Scanning the whole document for the first `a:rPr` finds the title's run, because the
 * title comes first in document order. Every style assertion has to be scoped to the
 * shape being edited or it silently checks a shape nobody touched.
 */
function shapeNode(partXml, shape) {
  const tree = scanXml(partXml);
  const node = nodeAtPath(tree, shape.anchor.path);
  expect(node, `anchor for ${shape.elId} must resolve`).not.toBeNull();
  return node;
}

function firstRunProps(partXml, shape) {
  const node = shapeNode(partXml, shape);
  return findDescendants(node, 'a:rPr')[0];
}

async function modelWithBody() {
  const model = await parsePptx(await buildPptx());
  const slide = model.slides[0];
  // Shapes[0] is the title placeholder and shapes[1] is a body text box with its own
  // geometry, which is the shape these tests edit.
  const body = slide.shapes[1];
  const partXml = await model.pkg.text(slide.part);
  return { model, slide, body, partXml };
}

describe('pptx style write', () => {
  it('writes bold onto the run, not onto the paragraph', async () => {
    const { body, partXml } = await modelWithBody();
    const next = applyStyleEdits(partXml, body, { paragraphs: [{ index: 0, bold: true }] });

    const rPr = firstRunProps(next, body);
    expect(rPr).toBeDefined();
    expect(rPr.attrs.b).toBe('1');
    // b="1" must not appear on a:pPr; PowerPoint reads paragraph-level b as a different
    // (and largely ignored) property, so the text would look unstyled.
    const node = shapeNode(next, body);
    const pPrs = findDescendants(node, 'a:pPr');
    expect(pPrs.every(item => item.attrs.b === undefined)).toBe(true);
  });

  it('clears bold by removing the attribute rather than writing b="0"', async () => {
    const { body, partXml } = await modelWithBody();
    const bolded = applyStyleEdits(partXml, body, { paragraphs: [{ index: 0, bold: true }] });
    const cleared = applyStyleEdits(bolded, body, { paragraphs: [{ index: 0, bold: false }] });

    const rPr = firstRunProps(cleared, body);
    // b="0" is technically valid but Office re-normalizes it; removing is the honest
    // representation of "not bold" and keeps the diff one attribute wide.
    expect(rPr.attrs.b).toBeUndefined();
  });

  it('writes size in centipoints and color as an uppercase srgb hex', async () => {
    const { body, partXml } = await modelWithBody();
    const next = applyStyleEdits(partXml, body, {
      paragraphs: [{ index: 0, size: 2400, color: '#1f5eff' }],
    });

    const rPr = firstRunProps(next, body);
    expect(rPr.attrs.sz).toBe('2400');
    const srgb = findDescendants(rPr, 'a:srgbClr')[0];
    expect(srgb.attrs.val).toBe('1F5EFF');
  });

  it('replaces a scheme color instead of adding a second solidFill', async () => {
    // A run filled by schemeClr plus an srgbClr sibling is a damaged part: Office reads
    // two fills in one rPr and offers to repair the file.
    const xml = '<p:sp xmlns:p="p" xmlns:a="a"><p:txBody><a:p><a:r><a:rPr sz="1800"><a:solidFill><a:schemeClr val="accent1"/></a:solidFill></a:rPr><a:t>x</a:t></a:r></a:p></p:txBody></p:sp>';
    const shape = { elId: 't#0', anchor: { part: 'p', path: [0] } };
    const next = applyStyleEdits(xml, shape, { paragraphs: [{ index: 0, color: '#112233' }] });

    expect(next).not.toContain('schemeClr');
    expect((next.match(/<a:solidFill>/g) || []).length).toBe(1);
    expect(next).toContain('<a:srgbClr val="112233"/>');
  });

  it('creates a:pPr when alignment is set on a paragraph that has none', async () => {
    const { body, partXml } = await modelWithBody();
    const next = applyStyleEdits(partXml, body, { paragraphs: [{ index: 0, align: 'ctr' }] });

    const node = shapeNode(next, body);
    const para = findDescendants(node, 'a:p')[0];
    // a:pPr must be the first child of a:p, otherwise Office ignores it.
    expect(para.children[0].name).toBe('a:pPr');
    expect(para.children[0].attrs.algn).toBe('ctr');
  });

  it('leaves every other run untouched when one paragraph is restyled', async () => {
    const { body, partXml } = await modelWithBody();
    const next = applyStyleEdits(partXml, body, { paragraphs: [{ index: 0, italic: true }] });
    // The title shape has its own run with sz="3200"; it must keep its exact attributes.
    expect(next).toContain('sz="3200" b="1"');
    expect(next).toContain('<a:t>季度复盘</a:t>');
    // And the title's run must still carry no italic, i.e. the edit stayed scoped.
    const node = shapeNode(next, body);
    const titlePara = findDescendants(scanXml(next), 'a:p')[0];
    expect(findDescendants(titlePara, 'a:rPr')[0].attrs.i).toBeUndefined();
    expect(node).toBeTruthy();
  });
});

describe('pre-save validation', () => {
  it('passes a healthy part', async () => {
    const { partXml } = await modelWithBody();
    expect(validateSlidePart(partXml)).toEqual([]);
  });

  it('reports a duplicate shape id', () => {
    const xml = '<p:sld xmlns:p="p"><p:cNvPr id="2"/><p:cNvPr id="2"/></p:sld>';
    const problems = validateSlidePart(xml);
    expect(problems.some(text => text.includes('重复的形状 id 2'))).toBe(true);
  });

  it('reports a table whose cell count does not match its grid', () => {
    const xml = '<p:sld xmlns:p="p" xmlns:a="a"><a:tbl><a:tblGrid><a:gridCol w="1"/><a:gridCol w="1"/></a:tblGrid><a:tr h="1"><a:tc/><a:tc/></a:tr><a:tr h="1"><a:tc/></a:tr></a:tbl></p:sld>';
    const problems = validateSlidePart(xml);
    expect(problems.some(text => text.includes('表格行列不匹配'))).toBe(true);
  });

  it('holds the insert path to the same rule: a second insert gets a fresh id', async () => {
    const { partXml } = await modelWithBody();
    const firstId = nextShapeId(partXml);
    const one = insertTextBox(partXml, { text: 'a', box: { x: 0, y: 0, cx: 100, cy: 100 } });
    const two = insertTextBox(one, { text: 'b', box: { x: 0, y: 0, cx: 100, cy: 100 } });
    expect(one).toContain(`id="${firstId}"`);
    expect(two).toContain(`id="${firstId + 1}"`);
    expect(validateSlidePart(two)).toEqual([]);
  });
});

describe('writePptx integration', () => {
  it('applies text and style changes and re-parses both', async () => {
    const { model, body, slide } = await modelWithBody();
    const result = await writePptx(model, [
      { slideIndex: 0, elId: body.elId, op: 'text',
        payload: { paragraphs: [{ ...body.textBody.paragraphs[0], text: '新文字' }] } },
      { slideIndex: 0, elId: body.elId, op: 'style',
        payload: { paragraphs: [{ index: 0, bold: true, size: 2000, color: '#C86400' }] } },
    ]);

    expect(result.changed).toBe(true);
    const reparsed = await parsePptx(result.bytes);
    const shape = reparsed.slides[0].shapes.find(item => item.elId === body.elId);
    expect(shape.textBody.paragraphs[0].text).toBe('新文字');

    const repartXml = await reparsed.pkg.text(slide.part);
    const tree = scanXml(repartXml);
    const rPr = findDescendants(tree, 'a:rPr').find(node => node.attrs.sz === '2000');
    expect(rPr).toBeDefined();
    expect(rPr.attrs.b).toBe('1');
  });

  it('reports no change when the model edit is a no-op', async () => {
    const { model } = await modelWithBody();
    const result = await writePptx(model, []);
    expect(result.changed).toBe(false);
    expect(result.bytes).toBeNull();
  });

  it('refuses geometry writes on inherited shapes instead of detaching them', async () => {
    const { model } = await modelWithBody();
    const title = model.slides[0].shapes[0];
    expect(title.inherited).toBe(true);

    const result = await writePptx(model, [{
      slideIndex: 0, elId: title.elId, op: 'geometry',
      payload: { x: 1, y: 1, cx: 1, cy: 1 },
    }]);
    // The change is skipped, so nothing is written and no package is produced.
    expect(result.changed).toBe(false);
  });

  it('keeps untouched slides byte-identical', async () => {
    const model = await parsePptx(await buildPptx({
      slides: [titleSlideXml({ title: '第一页' }), titleSlideXml({ title: '第二页' })],
    }));
    const secondPart = model.slides[1].part;
    const before = await model.pkg.text(secondPart);

    const body = model.slides[0].shapes[1];
    const result = await writePptx(model, [{
      slideIndex: 0, elId: body.elId, op: 'text',
      payload: { paragraphs: [{ ...body.textBody.paragraphs[0], text: '只改第一页' }] },
    }]);
    expect(result.changed).toBe(true);

    const reopened = await parsePptx(result.bytes);
    expect(await reopened.pkg.text(secondPart)).toBe(before);
  });
});
