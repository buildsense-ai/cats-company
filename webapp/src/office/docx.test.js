// DOCX parse and write.
//
// Sample text is ASCII throughout. The earlier version of this file used multi-byte strings and
// a tooling encoding round trip corrupted them, which produced a parse error that looked like a
// code bug. Keeping fixtures ASCII removes that whole class of false failure; the behaviours
// under test are encoding-independent.

import { describe, it, expect } from 'vitest';
import JSZip from 'jszip';
import { buildDocx, buildSampleDocx, paragraph, run, table } from './test-fixtures/build-docx.js';
import { parseDocx } from './docx/parse.js';
import {
  applyParagraphText, applyParagraphProperties, applyRunStyle, deleteParagraph,
  insertParagraph, validateDocumentPart,
} from './docx/write.js';
import { halfPointsToPt, ptToHalfPoints, twipsToCm, cmToTwips } from './docx/model.js';

describe('unit conversion', () => {
  it('converts half-points both ways without drift', () => {
    expect(halfPointsToPt(24)).toBe(12);
    expect(ptToHalfPoints(12)).toBe(24);
    expect(halfPointsToPt(ptToHalfPoints(10.5))).toBe(10.5);
  });

  it('converts twips both ways', () => {
    expect(cmToTwips(1)).toBe(567);
    expect(twipsToCm(567)).toBe(1);
  });
});

describe('docx parse', () => {
  it('reads blocks in document order with their kind', async () => {
    const model = await parseDocx(await buildSampleDocx());
    expect(model.family).toBe('docx');
    expect(model.blocks.map(block => block.kind)).toEqual([
      'paragraph', 'paragraph', 'paragraph', 'paragraph', 'table', 'paragraph',
    ]);
  });

  it('reads text, heading level and alignment', async () => {
    const model = await parseDocx(await buildSampleDocx());
    expect(model.blocks[0].text).toBe('Quarterly review');
    expect(model.blocks[0].heading).toBe(1);
    expect(model.blocks[1].text).toContain('Delivery cycle');
    expect(model.blocks[2].align).toBe('center');
  });

  it('reads per-run formatting rather than flattening the paragraph', async () => {
    const model = await parseDocx(await buildSampleDocx());
    // The third paragraph has two runs: plain and bold-orange. A parser that only kept the
    // paragraph text would lose the emphasis and the writer could never restore it.
    const runs = model.blocks[2].runs;
    expect(runs).toHaveLength(2);
    expect(runs[0].bold).toBe(false);
    expect(runs[1].bold).toBe(true);
    expect(runs[1].color).toBe('#C86400');
    expect(model.blocks[2].text).toBe('Key: cost down 15%');
  });

  it('reads a table as rows and cells', async () => {
    const model = await parseDocx(await buildSampleDocx());
    const tableBlock = model.blocks.find(block => block.kind === 'table');
    expect(tableBlock.rows).toBe(2);
    expect(tableBlock.cols).toBe(3);
    expect(tableBlock.cells).toHaveLength(6);
    expect(tableBlock.cells[0].text).toBe('Metric');
    expect(tableBlock.cells[5].text).toBe('2 weeks');
  });

  it('reads indentation in twips and reports it in cm', async () => {
    const model = await parseDocx(await buildSampleDocx());
    const last = model.blocks[model.blocks.length - 1];
    expect(last.indFirstTwips).toBe(420);
    expect(twipsToCm(last.indFirstTwips)).toBeCloseTo(0.74, 1);
  });

  it('flags a numbered paragraph so the writer can preserve the list', async () => {
    const model = await parseDocx(await buildSampleDocx());
    const numbered = model.blocks.find(block => block.hasNumbering);
    expect(numbered).toBeDefined();
    expect(numbered.text).toBe('List item one');
  });

  it('reads page setup from sectPr instead of assuming A4', async () => {
    const model = await parseDocx(await buildSampleDocx());
    expect(model.page.widthTwips).toBe(11906);
    expect(model.page.marginLeftTwips).toBe(1440);
  });

  it('produces ids that survive a re-parse of the same bytes', async () => {
    const bytes = await buildSampleDocx();
    const first = await parseDocx(bytes);
    const second = await parseDocx(bytes);
    expect(first.blocks.map(block => block.id)).toEqual(second.blocks.map(block => block.id));
    expect(new Set(first.blocks.map(block => block.id)).size).toBe(first.blocks.length);
  });

  it('builds an outline from headings only', async () => {
    const model = await parseDocx(await buildSampleDocx());
    expect(model.outline).toHaveLength(1);
    expect(model.outline[0].text).toBe('Quarterly review');
  });

  it('rejects a package that is not a docx', async () => {
    const zip = new JSZip();
    zip.file('hello.txt', 'not a document');
    const bytes = await zip.generateAsync({ type: 'uint8array' });
    await expect(parseDocx(bytes)).rejects.toThrow('DOCX');
  });
});

describe('paragraph text write', () => {
  it('replaces the text and keeps the paragraph properties', async () => {
    const model = await parseDocx(await buildSampleDocx());
    const block = model.blocks[2];
    const partXml = await model.pkg.text(model.part);

    const next = applyParagraphText(partXml, block, 'rewritten paragraph');
    expect(next).toContain('rewritten paragraph');
    expect(next).not.toContain('cost down 15%');
    // The centred alignment lives on pPr; replacing the words must not drop it.
    expect(next).toContain('<w:jc w:val="center"/>');
  });

  it('keeps the first run formatting on the rewritten paragraph', async () => {
    const model = await parseDocx(await buildSampleDocx());
    const block = model.blocks[2];
    const partXml = await model.pkg.text(model.part);

    const next = applyParagraphText(partXml, block, 'new words');
    // The paragraph's leading run was plain, so the replacement should inherit exactly that
    // and not invent bold from the second run.
    expect(next).toContain('<w:t xml:space="preserve">new words</w:t>');
    expect(next).toContain('<w:jc w:val="center"/>');
  });

  it('writes xml:space=preserve so leading spaces survive', async () => {
    const model = await parseDocx(await buildDocx(paragraph(['placeholder'])));
    const partXml = await model.pkg.text(model.part);
    const next = applyParagraphText(partXml, model.blocks[0], '  leading spaces');
    // Without xml:space Word trims the spaces on open, so the edit would silently lose them.
    expect(next).toContain('xml:space="preserve">  leading spaces');
  });

  it('creates a run when the paragraph was empty', async () => {
    const model = await parseDocx(await buildDocx(paragraph([])));
    const partXml = await model.pkg.text(model.part);
    expect(model.blocks[0].text).toBe('');
    const next = applyParagraphText(partXml, model.blocks[0], 'written into an empty paragraph');
    expect(next).toContain('written into an empty paragraph');
  });

  it('escapes a user typo so it cannot break the document', async () => {
    const model = await parseDocx(await buildDocx(paragraph(['placeholder'])));
    const partXml = await model.pkg.text(model.part);
    const next = applyParagraphText(partXml, model.blocks[0], 'a & b < c');
    expect(next).toContain('a &amp; b &lt; c');
  });
});

describe('run style write', () => {
  it('applies bold to every run in the paragraph', async () => {
    const model = await parseDocx(await buildSampleDocx());
    const block = model.blocks[2];
    const partXml = await model.pkg.text(model.part);

    const next = applyRunStyle(partXml, block, { bold: true });
    // Two runs in this paragraph; both need the flag or the paragraph ends up half bold.
    const boldCount = (next.match(/<w:b\/>/g) || []).length;
    expect(boldCount).toBeGreaterThanOrEqual(2);
  });

  it('applies several missing and existing properties to one rPr without overlapping', async () => {
    const model = await parseDocx(await buildSampleDocx());
    const block = model.blocks[2];
    const partXml = await model.pkg.text(model.part);

    // Before the fix this threw `overlapping xml edits`: italic/underline/color were inserted
    // at the same offset where the existing w:sz node was replaced.
    const next = applyRunStyle(partXml, block, {
      bold: true, italic: true, underline: true, size: 20, color: '#3366CC',
    });
    expect(next).toContain('<w:i/>');
    expect(next).toContain('<w:u w:val="single"/>');
    expect(next).toContain('<w:color w:val="3366CC"/>');
    expect(next).toContain('<w:sz w:val="40"/>');
  });

  it('writes size in half-points', async () => {
    const model = await parseDocx(await buildDocx(paragraph([run('text')])));
    const partXml = await model.pkg.text(model.part);
    const next = applyRunStyle(partXml, model.blocks[0], { size: 14 });
    expect(next).toContain('<w:sz w:val="28"/>');
  });

  it('writes colour as an uppercase hex with no hash', async () => {
    const model = await parseDocx(await buildDocx(paragraph([run('text')])));
    const partXml = await model.pkg.text(model.part);
    const next = applyRunStyle(partXml, model.blocks[0], { color: '#1f5eff' });
    expect(next).toContain('<w:color w:val="1F5EFF"/>');
  });

  it('creates rPr on a run that has none', async () => {
    const model = await parseDocx(await buildDocx(paragraph([run('plain text')])));
    const block = model.blocks[0];
    expect(block.runs[0].bold).toBe(false);
    const partXml = await model.pkg.text(model.part);
    const next = applyRunStyle(partXml, block, { italic: true });
    expect(next).toContain('<w:i/>');
  });
});

describe('paragraph properties write', () => {
  it('creates pPr when the paragraph has none', async () => {
    const model = await parseDocx(await buildDocx(paragraph([run('plain paragraph')])));
    const partXml = await model.pkg.text(model.part);
    const next = applyParagraphProperties(partXml, model.blocks[0], { align: 'right' });

    // pPr must be the first child of w:p or Word ignores the alignment.
    expect(next).toContain('<w:pPr><w:jc w:val="right"/></w:pPr>');
    expect(next.indexOf('<w:pPr>')).toBeLessThan(next.indexOf('<w:r>'));
  });

  it('changes alignment on a paragraph that already has properties', async () => {
    const model = await parseDocx(await buildSampleDocx());
    const block = model.blocks[2];
    const partXml = await model.pkg.text(model.part);
    const next = applyParagraphProperties(partXml, block, { align: 'left' });
    expect(next).toContain('<w:jc w:val="left"/>');
    expect(next).not.toContain('<w:jc w:val="center"/>');
  });

  it('writes indentation in twips', async () => {
    const model = await parseDocx(await buildDocx(paragraph([run('paragraph')])));
    const partXml = await model.pkg.text(model.part);
    const next = applyParagraphProperties(partXml, model.blocks[0], { indLeftCm: 1 });
    expect(next).toContain('w:left="567"');
  });
});

describe('insert and delete', () => {
  it('inserts a paragraph after the anchor and keeps its list style', async () => {
    const model = await parseDocx(await buildSampleDocx());
    const numbered = model.blocks.find(block => block.hasNumbering);
    const partXml = await model.pkg.text(model.part);
    const next = insertParagraph(partXml, numbered, 'new list item');

    expect(next).toContain('new list item');
    // Cloning the anchor's pPr is what keeps the new paragraph inside the same numbering.
    expect(next).toContain('<w:numPr>');
    const insertedIndex = next.indexOf('new list item');
    const anchorIndex = next.indexOf('List item one');
    expect(insertedIndex).toBeGreaterThan(anchorIndex);
  });

  it('refuses to delete the last paragraph', async () => {
    const model = await parseDocx(await buildDocx(paragraph(['only paragraph'])));
    const partXml = await model.pkg.text(model.part);
    expect(() => deleteParagraph(partXml, model.blocks[0], 1)).toThrow();
  });

  it('deletes a paragraph when others remain', async () => {
    const model = await parseDocx(await buildSampleDocx());
    const partXml = await model.pkg.text(model.part);
    const next = deleteParagraph(partXml, model.blocks[1], model.blocks.length - 1);
    expect(next).not.toContain('Delivery cycle');
    expect(next).toContain('Quarterly review');
  });
});

describe('validation', () => {
  it('passes a healthy document', async () => {
    const model = await parseDocx(await buildSampleDocx());
    const partXml = await model.pkg.text(model.part);
    expect(validateDocumentPart(partXml)).toEqual([]);
  });

  it('reports a paragraph whose pPr is not first', () => {
    const xml = '<?xml version="1.0"?><w:document xmlns:w="w"><w:body>'
      + '<w:p><w:r><w:t>x</w:t></w:r><w:pPr><w:jc w:val="center"/></w:pPr></w:p>'
      + '</w:body></w:document>';
    const problems = validateDocumentPart(xml);
    expect(problems.length).toBeGreaterThan(0);
  });

  it('reports a document with no body', () => {
    const problems = validateDocumentPart('<w:document xmlns:w="w"></w:document>');
    expect(problems).toContain('文档缺少正文');
  });
});

describe('round trip', () => {
  it('parses, edits text, saves and re-parses', async () => {
    const model = await parseDocx(await buildSampleDocx());
    const block = model.blocks[1];
    const partXml = await model.pkg.text(model.part);
    const edited = applyParagraphText(partXml, block, 'this version was edited');

    model.pkg.setText(model.part, edited);
    const saved = await model.pkg.save();

    const reopened = await parseDocx(saved);
    expect(reopened.blocks[1].text).toBe('this version was edited');
    // Everything else must be intact: same block count, same table.
    expect(reopened.blocks).toHaveLength(model.blocks.length);
    expect(reopened.blocks.find(item => item.kind === 'table').rows).toBe(2);
  });

  it('keeps parts it did not edit byte-identical', async () => {
    const model = await parseDocx(await buildSampleDocx());
    const before = await model.pkg.text('[Content_Types].xml');
    model.pkg.setText(model.part, applyParagraphText(
      await model.pkg.text(model.part), model.blocks[0], 'new heading'));
    const saved = await model.pkg.save();

    const zip = await JSZip.loadAsync(saved);
    const after = await zip.file('[Content_Types].xml').async('string');
    expect(after).toBe(before);
  });
});
