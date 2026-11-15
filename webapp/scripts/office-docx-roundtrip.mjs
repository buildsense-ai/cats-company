// DOCX round-trip on a real Word file.
//
// Reads a .docx written by Word itself, edits one paragraph's text and one paragraph's run
// formatting, saves, and reports what survived. Afterwards the PowerShell check opens the result
// in Word: that is the only assertion that proves the file is not damaged, because our own
// parser is more forgiving than Word is.
//
// Usage: node scripts/office-docx-roundtrip.mjs <in.docx> <out.docx>

import fs from 'node:fs';
import { parseDocx } from '../src/office/docx/parse.js';
import {
  applyParagraphProperties, applyParagraphText, applyRunStyle, validateDocumentPart,
} from '../src/office/docx/write.js';
import { BLOCK_KINDS } from '../src/office/docx/model.js';

const [inputPath, outputPath] = process.argv.slice(2);
if (!inputPath || !outputPath) {
  console.error('usage: node scripts/office-docx-roundtrip.mjs <in.docx> <out.docx>');
  process.exit(2);
}

const bytes = fs.readFileSync(inputPath);
const model = await parseDocx(bytes);

const paragraphs = model.blocks.filter(block => block.kind === BLOCK_KINDS.PARAGRAPH);
if (paragraphs.length === 0) {
  console.error('FAIL: no paragraph found');
  process.exit(1);
}

const textTarget = paragraphs.find(block => block.text && !block.hasNumbering) || paragraphs[0];
// Apply both edits to the same paragraph. A split target would let the harness report a green
// text check and a green style check even if a real combined text-and-style edit lost one of them.
// The target must actually contain a run; a paragraph with no run has nowhere for run formatting
// to go, and picking it would report a silent no-op as a failed write.
const styleTarget = textTarget;

const NEW_TEXT = 'Edited by round-trip harness';
const NEW_STYLE = { bold: true, size: 18, color: '#C86400' };

let xml = await model.pkg.text(model.part);
const beforeLength = xml.length;

xml = applyParagraphText(xml, textTarget, NEW_TEXT);
xml = applyRunStyle(xml, styleTarget, NEW_STYLE);
xml = applyParagraphProperties(xml, styleTarget, { align: 'center' });

const problems = validateDocumentPart(xml);
if (problems.length > 0) {
  console.error(`FAIL: validation rejected the edit — ${problems.join('; ')}`);
  process.exit(1);
}

model.pkg.setText(model.part, xml);
const saved = await model.pkg.save();
fs.writeFileSync(outputPath, saved);

const reopened = await parseDocx(saved);
const editedBlock = reopened.blocks.find(block => block.id === textTarget.id);
const styledBlock = reopened.blocks.find(block => block.id === styleTarget.id);

console.log(`in         ${inputPath} (${bytes.byteLength} bytes)`);
console.log(`out        ${outputPath} (${saved.byteLength} bytes)`);
console.log(`blocks     ${model.blocks.length} -> ${reopened.blocks.length}`);
console.log('');

const checks = [
  ['block count', reopened.blocks.length === model.blocks.length,
    `${model.blocks.length} -> ${reopened.blocks.length}`],
  ['text', editedBlock?.text === NEW_TEXT, JSON.stringify(editedBlock?.text)],
  ['size', styledBlock?.runs?.[0]?.size === NEW_STYLE.size, styledBlock?.runs?.[0]?.size],
  ['bold', styledBlock?.runs?.[0]?.bold === true, styledBlock?.runs?.[0]?.bold],
  ['color', String(styledBlock?.runs?.[0]?.color || '').toUpperCase() === NEW_STYLE.color,
    styledBlock?.runs?.[0]?.color],
  ['align', styledBlock?.align === 'center', styledBlock?.align],
];

let failed = 0;
for (const [name, ok, actual] of checks) {
  console.log(`${ok ? 'ok  ' : 'FAIL'}  ${name.padEnd(12)} ${JSON.stringify(actual)}`);
  if (!ok) failed += 1;
}

console.log('');
console.log(`xml grew   ${beforeLength} -> ${xml.length} chars`);
if (failed > 0) {
  console.error(`\nFAIL: ${failed} check(s) did not survive the write`);
  process.exit(1);
}
console.log('WRITE OK');
