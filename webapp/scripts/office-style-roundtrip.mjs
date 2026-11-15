// Phase-2 round-trip: change text AND style in one save, on a real PowerPoint file.
//
// The style path is riskier than the text path: a misplaced `a:rPr`, a duplicate
// `solidFill` or a paragraph-level `b="1"` all produce files that our parser reads
// happily and PowerPoint rejects or renders unstyled. So this harness prints what the
// writer actually produced, and the PowerShell check afterwards reads the applied
// formatting back through Office's own object model.
//
// Usage: node scripts/office-style-roundtrip.mjs <in.pptx> <out.pptx>

import fs from 'node:fs';
import { parsePptx } from '../src/office/pptx/parse.js';
import { writePptx } from '../src/office/pptx/write.js';

const [inputPath, outputPath] = process.argv.slice(2);
if (!inputPath || !outputPath) {
  console.error('usage: node scripts/office-style-roundtrip.mjs <in.pptx> <out.pptx>');
  process.exit(2);
}

const bytes = fs.readFileSync(inputPath);
const model = await parsePptx(bytes);

// Pick a text shape that owns its geometry. Inherited placeholders are skipped on
// purpose: geometry is refused for them, and this harness is about text plus style.
let target = null;
let slideIndex = -1;
for (let index = 0; index < model.slides.length && !target; index += 1) {
  const found = model.slides[index].shapes.find(shape =>
    shape.kind === 'text' && shape.text && !shape.inherited);
  if (found) { target = found; slideIndex = index; }
}
if (!target) {
  for (let index = 0; index < model.slides.length && !target; index += 1) {
    const found = model.slides[index].shapes.find(shape => shape.kind === 'text' && shape.text);
    if (found) { target = found; slideIndex = index; }
  }
}
if (!target) {
  console.error('FAIL: no text shape found');
  process.exit(1);
}

const paragraphs = target.textBody.paragraphs;
const newText = '样式验证文字';

// Deliberately distinctive values: 33pt bold orange. If the write lands anywhere other
// than the intended run, reading these back through PowerPoint will not match.
const STYLE = { size: 3300, bold: true, color: '#C86400' };

const changes = [
  { slideIndex, elId: target.elId, op: 'text',
    payload: { paragraphs: [{ ...paragraphs[0], text: newText },
      ...paragraphs.slice(1)] } },
  { slideIndex, elId: target.elId, op: 'style',
    payload: { paragraphs: [{ index: 0, ...STYLE }] } },
];

const result = await writePptx(model, changes);
if (!result.changed) {
  console.error('FAIL: writer reported no change');
  process.exit(1);
}

fs.writeFileSync(outputPath, result.bytes);

const reparsed = await parsePptx(result.bytes);
const shape = reparsed.slides[slideIndex].shapes.find(item => item.elId === target.elId);
const para = shape?.textBody?.paragraphs?.[0];
const run = para?.runs?.[0];

console.log(`in         ${inputPath}`);
console.log(`out        ${outputPath}`);
console.log(`slide      ${slideIndex + 1}`);
console.log(`element    ${target.elId}`);
console.log(`parts      ${result.partsChanged} changed`);
console.log('');

const checks = [
  ['text', para?.text === newText, para?.text],
  ['size', para?.size === STYLE.size, para?.size],
  ['bold', para?.bold === true, para?.bold],
  ['color', String(para?.color || '').toUpperCase() === STYLE.color, para?.color],
  ['runs', (para?.runs?.length || 0) >= 1, para?.runs?.length],
];

let failed = 0;
for (const [name, ok, actual] of checks) {
  console.log(`${ok ? 'ok  ' : 'FAIL'}  ${name.padEnd(6)} ${JSON.stringify(actual)}`);
  if (!ok) failed += 1;
}

console.log('');
console.log(`expect     text="${newText}" size=${STYLE.size} bold=true color=${STYLE.color}`);
if (run) console.log(`run0       size=${run.size} bold=${run.bold} color=${run.color}`);

if (failed > 0) {
  console.error(`\nFAIL: ${failed} check(s) did not survive the write`);
  process.exit(1);
}
console.log('\nWRITE OK');
