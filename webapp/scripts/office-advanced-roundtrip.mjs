// Round-trip for the three features that used to be read-only:
//   PPT  grouped child selection — geometry only, covered by unit tests
//   Word table cell text
//   Word picture insertion
//
// The Word side needs a real file: a picture Word cannot resolve opens as a broken-image
// placeholder instead of raising an error, so reading it back in Word is the only proof.
//
// Usage: node scripts/office-advanced-roundtrip.mjs <in.docx> <out.docx>

import fs from 'node:fs';
import JSZip from 'jszip';
import { parseDocx } from '../src/office/docx/parse.js';
import { applyTableCellText, validateDocumentPart } from '../src/office/docx/write.js';
import { applyInsertImage } from '../src/office/docx/insert.js';
import { BLOCK_KINDS } from '../src/office/docx/model.js';

const PNG_1X1 = Uint8Array.from(Buffer.from(
  'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8DwHwAFAAH/q842iQAAAABJRU5ErkJggg==',
  'base64',
));

const [inputPath, outputPath] = process.argv.slice(2);
if (!inputPath || !outputPath) {
  console.error('usage: node scripts/office-advanced-roundtrip.mjs <in.docx> <out.docx>');
  process.exit(2);
}

const bytes = fs.readFileSync(inputPath);
const model = await parseDocx(bytes);
const log = [];
const step = (name, detail) => log.push(`  ${name.padEnd(14)} ${detail}`);

// 1. Cell text, when the document has a table.
const tableBlock = model.blocks.find(block => block.kind === BLOCK_KINDS.TABLE);
let cellsEdited = false;
let cellBefore = '';
if (tableBlock) {
  const xml = await model.pkg.text(model.part);
  const targetCell = tableBlock.cells[0];
  cellBefore = targetCell.text;
  const edited = applyTableCellText(xml, targetCell, 'CELL-EDITED');
  const problems = validateDocumentPart(edited);
  if (problems.length > 0) {
    console.error(`FAIL: cell edit rejected — ${problems.join('; ')}`);
    process.exit(1);
  }
  model.pkg.setText(model.part, edited);
  cellsEdited = true;
  step('cell', `"${cellBefore}" -> "CELL-EDITED"`);
} else {
  step('cell', 'skipped (no table in this file)');
}

// 2. A picture, appended as its own paragraph before sectPr.
const picture = await applyInsertImage(model, {
  bytes: PNG_1X1, ext: 'png', name: 'roundtrip.png', widthCm: 6,
});
step('picture', `${picture.mediaName} rel=${picture.relId} ${picture.widthEmu} EMU wide`);

const finalXml = await model.pkg.text(model.part);
const problems = validateDocumentPart(finalXml);
if (problems.length > 0) {
  console.error(`FAIL: validation rejected the edit — ${problems.join('; ')}`);
  process.exit(1);
}

const saved = await model.pkg.save();
fs.writeFileSync(outputPath, saved);

// ---- re-parse and report ----
const reopened = await parseDocx(saved);
const reopenedTable = reopened.blocks.find(block => block.kind === BLOCK_KINDS.TABLE);
const savedXml = await reopened.pkg.text(reopened.part);
const drawingCount = (savedXml.match(/<w:drawing>/g) || []).length;
const zip = await JSZip.loadAsync(saved);
const mediaParts = Object.keys(zip.files).filter(name => /^word\/media\/.+\./.test(name)
  && !zip.files[name].dir);
const documentRelsXml = await zip.file('word/_rels/document.xml.rels')?.async('string') || '';
const pictureRel = picture.relId
  ? documentRelsXml.match(new RegExp(`<Relationship[^>]*Id="${picture.relId}"[^>]*>`))?.[0] || ''
  : '';
const pictureRelTarget = pictureRel.match(/Target="([^"]+)"/)?.[1] || '';
const expectedMediaTarget = `media/${picture.mediaName.split('/').pop()}`;
const pictureWidth = Number(savedXml.match(new RegExp(`<wp:extent[^>]*cx="(\\d+)"[^>]*>`))?.[1] || 0);

console.log(`in         ${inputPath} (${bytes.byteLength} bytes)`);
console.log(`out        ${outputPath} (${saved.byteLength} bytes)`);
console.log('');
console.log('steps');
log.forEach(line => console.log(line));
console.log('');

const checks = [
  ['block count', reopened.blocks.length >= model.blocks.length,
    `${model.blocks.length} -> ${reopened.blocks.length}`],
  ['drawing', drawingCount === 1, `${drawingCount} w:drawing`],
  ['media part', mediaParts.length === 1, mediaParts.join(',') || 'none'],
  ['picture rel', pictureRelTarget === expectedMediaTarget,
    `${picture.relId} -> ${pictureRelTarget || 'missing'}`],
  ['picture width', pictureWidth === picture.widthEmu, `${pictureWidth} EMU`],
  ['valid', problems.length === 0, problems.length ? problems.join(';') : 'clean'],
];
if (cellsEdited) {
  checks.splice(1, 0, ['cell text',
    reopenedTable?.cells.some(cell => cell.text === 'CELL-EDITED') === true,
    JSON.stringify(reopenedTable?.cells?.[0]?.text)]);
  checks.splice(2, 0, ['table shape',
    reopenedTable?.rows === tableBlock.rows && reopenedTable?.cols === tableBlock.cols,
    `${reopenedTable?.rows}x${reopenedTable?.cols}`]);
}

let failed = 0;
for (const [name, ok, detail] of checks) {
  console.log(`${ok ? 'ok  ' : 'FAIL'}  ${name.padEnd(13)} ${detail}`);
  if (!ok) failed += 1;
}

if (failed > 0) {
  console.error(`\nFAIL: ${failed} check(s) failed`);
  process.exit(1);
}
console.log('\nWRITE OK');
