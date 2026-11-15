// Phase-3 round-trip: move and resize a shape on a real PowerPoint file, then report
// what the saved file actually contains.
//
// The judgement here is numeric, not visual: phase 3's acceptance criterion is a position
// offset under 0.2% of the slide dimension. So the harness moves a shape by a known EMU
// delta and prints the delta that survived the write, which is what PowerPoint will read.

import fs from 'node:fs';
import { parsePptx } from '../src/office/pptx/parse.js';
import { writePptx } from '../src/office/pptx/write.js';

const [inputPath, outputPath] = process.argv.slice(2);
if (!inputPath || !outputPath) {
  console.error('usage: node scripts/office-geometry-roundtrip.mjs <in.pptx> <out.pptx>');
  process.exit(2);
}

const bytes = fs.readFileSync(inputPath);
const model = await parsePptx(bytes);

// Only shapes that own their transform can be moved: an inherited placeholder's box lives
// on the layout, and the writer refuses that on purpose.
let target = null;
let slideIndex = -1;
for (let index = 0; index < model.slides.length && !target; index += 1) {
  const found = model.slides[index].shapes.find(shape => !shape.inherited && shape.box.cx > 0);
  if (found) { target = found; slideIndex = index; }
}
if (!target) {
  console.error('FAIL: no movable shape found');
  process.exit(1);
}

// A delta large enough to be unambiguous but small enough to stay on the slide.
const DX = 365760; // 0.4 inch
const DY = 182880; // 0.2 inch
const before = { ...target.box };
const after = {
  x: before.x + DX,
  y: before.y + DY,
  cx: before.cx + 91440, // also resize, so both code paths are exercised
  cy: before.cy + 45720,
};

const result = await writePptx(model, [
  { slideIndex, elId: target.elId, op: 'geometry', payload: after },
]);
if (!result.changed) {
  console.error('FAIL: writer reported no change');
  process.exit(1);
}
fs.writeFileSync(outputPath, result.bytes);

const reparsed = await parsePptx(result.bytes);
const shape = reparsed.slides[slideIndex].shapes.find(item => item.elId === target.elId);
const saved = shape?.box;

console.log(`in         ${inputPath}`);
console.log(`out        ${outputPath}`);
console.log(`slide      ${slideIndex + 1}`);
console.log(`element    ${target.elId}  (${shape?.label || '?'})`);
console.log('');
console.log(`before     x=${before.x} y=${before.y} cx=${before.cx} cy=${before.cy}`);
console.log(`requested  x=${after.x} y=${after.y} cx=${after.cx} cy=${after.cy}`);
console.log(`saved      x=${saved?.x} y=${saved?.y} cx=${saved?.cx} cy=${saved?.cy}`);
console.log('');

const checks = [
  ['x', saved?.x === after.x, saved?.x, after.x],
  ['y', saved?.y === after.y, saved?.y, after.y],
  ['cx', saved?.cx === after.cx, saved?.cx, after.cx],
  ['cy', saved?.cy === after.cy, saved?.cy, after.cy],
];

// The acceptance criterion is expressed as a fraction of the slide, because 0.2% means
// different things in EMU on a 16:9 and a 4:3 deck.
const toleranceX = model.slideSize.cx * 0.002;
const toleranceY = model.slideSize.cy * 0.002;

let failed = 0;
for (const [name, exact, actual, expected] of checks) {
  const drift = Number.isFinite(actual) && Number.isFinite(expected) ? actual - expected : Number.NaN;
  const tolerance = name === 'x' || name === 'cx' ? toleranceX : toleranceY;
  const within = Math.abs(drift) <= tolerance;
  console.log(`${exact ? 'ok  ' : within ? 'warn' : 'FAIL'}  ${name.padEnd(3)} saved=${actual} expected=${expected} drift=${drift} tolerance=${Math.round(tolerance)}`);
  if (!exact && !within) failed += 1;
}

console.log('');
console.log(`tolerance  ${Math.round(toleranceX)} EMU on x (0.2% of ${model.slideSize.cx}), ${Math.round(toleranceY)} EMU on y`);
if (failed > 0) {
  console.error(`\nFAIL: ${failed} field(s) drifted beyond tolerance`);
  process.exit(1);
}
console.log('\nGEOMETRY OK');
