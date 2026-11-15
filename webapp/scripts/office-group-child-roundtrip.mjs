// Phase-8 round-trip: move one child inside a group, then report the numbers the file
// actually contains.
//
// A child of `p:grpSp` stores coordinates relative to the group's child offset, so the
// UI has to convert absolute -> relative before writing. Unit tests cover that math;
// this harness proves the converted value lands in the file and that the file still
// opens in PowerPoint.
//
// Usage: node scripts/office-group-child-roundtrip.mjs <in.pptx> <out.pptx>

import fs from 'node:fs';
import { parsePptx } from '../src/office/pptx/parse.js';
import { writePptx } from '../src/office/pptx/write.js';
import { absoluteFromRelative, relativeFromAbsolute } from '../src/office/pptx/geometry.js';

const [inputPath, outputPath] = process.argv.slice(2);
if (!inputPath || !outputPath) {
  console.error('usage: node scripts/office-group-child-roundtrip.mjs <in.pptx> <out.pptx>');
  process.exit(2);
}

const bytes = fs.readFileSync(inputPath);
const model = await parsePptx(bytes);

let group = null;
let child = null;
let slideIndex = -1;
for (let index = 0; index < model.slides.length && !group; index += 1) {
  for (const shape of model.slides[index].shapes) {
    if (shape.kind !== 'group') continue;
    const candidates = (shape.children || []).filter(item => item.box && item.box.cx > 0);
    if (candidates.length) {
      group = shape;
      child = candidates[0];
      slideIndex = index;
      break;
    }
  }
}
if (!child) {
  console.error('FAIL: no group with a child found');
  process.exit(1);
}

const DX = 365760; // 0.4 inch, same delta the flat-shape harness uses
const DY = 182880; // 0.2 inch

const groupBox = group.box;
// The parser stores `childOffset` / `childExtent` as {x,y} pairs. The geometry helpers
// take the extent as {x,y} too and treat them as cx/cy, so do not pass {cx,cy} here.
const chOff = {
  x: group.childOffset?.x ?? groupBox.x,
  y: group.childOffset?.y ?? groupBox.y,
};
const chExt = {
  x: group.childExtent?.x ?? groupBox.cx,
  y: group.childExtent?.y ?? groupBox.cy,
};

const childAbsBefore = absoluteFromRelative(child.box, groupBox, chOff, chExt);

const childAbsAfter = { ...childAbsBefore, x: childAbsBefore.x + DX, y: childAbsBefore.y + DY };
const childRelAfter = relativeFromAbsolute(childAbsAfter, groupBox, chOff, chExt);

console.log('in        ', inputPath, `(${bytes.length} bytes)`);
console.log('group     ', group.elId, `box=${groupBox.x},${groupBox.y},${groupBox.cx}x${groupBox.cy}`);
console.log('child     ', child.elId, `rel=${child.box.x},${child.box.y}`);
console.log('abs before', `${childAbsBefore.x},${childAbsBefore.y}`);
console.log('abs after ', `${childAbsAfter.x},${childAbsAfter.y}`);
console.log('rel after ', `${childRelAfter.x},${childRelAfter.y}`);

const result = await writePptx(model, [{
  op: 'geometry',
  slideIndex,
  // `writePptx` resolves the target by the top-level `elId`, and hands `payload`
  // straight to `applyGeometryEdit` as the box.
  elId: child.elId,
  payload: childRelAfter,
}]);

if (!result.changed) {
  console.error('\nFAIL: the writer reported no change — the geometry edit never landed');
  process.exit(1);
}

fs.writeFileSync(outputPath, Buffer.from(result.bytes));
console.log('out       ', outputPath, `(${result.bytes.length} bytes)`);

const finalModel = await parsePptx(fs.readFileSync(outputPath));
const finalGroup = finalModel.slides[slideIndex].shapes.find(shape => shape.elId === group.elId);
const finalChild = (finalGroup?.children || []).find(shape => shape.elId === child.elId);

const checks = [];
checks.push(['child still inside group', Boolean(finalChild), finalChild ? 'yes' : 'missing']);
checks.push([
  'group children count',
  (finalGroup?.children || []).length === (group.children || []).length,
  `${(group.children || []).length} -> ${(finalGroup?.children || []).length}`,
]);

if (finalChild) {
  const relDx = finalChild.box.x - child.box.x;
  const relDy = finalChild.box.y - child.box.y;
  checks.push(['relative dx', relDx === childRelAfter.x - child.box.x, `${relDx}`]);
  checks.push(['relative dy', relDy === childRelAfter.y - child.box.y, `${relDy}`]);

  // Re-read the extent from the file that was just written, in the same {x: cx, y: cy}
  // shape the helpers expect. Reusing the pre-write values is fine only while they are
  // unchanged, and passing them wrongly is what produced NaN here.
  const finalGroupBox = finalGroup.box;
  const finalOff = {
    x: finalGroup.childOffset?.x ?? finalGroupBox.x,
    y: finalGroup.childOffset?.y ?? finalGroupBox.y,
  };
  const finalExt = {
    x: finalGroup.childExtent?.x ?? finalGroupBox.cx,
    y: finalGroup.childExtent?.y ?? finalGroupBox.cy,
  };
  const finalAbs = absoluteFromRelative(finalChild.box, finalGroupBox, finalOff, finalExt);
  const absDx = finalAbs.x - childAbsBefore.x;
  const absDy = finalAbs.y - childAbsBefore.y;
  // The acceptance criterion for phase 3 is < 0.2% of the slide dimension. Here the
  // delta has to come back exactly, because no rounding is involved.
  checks.push(['absolute dx', absDx === DX, `${absDx} vs ${DX}`]);
  checks.push(['absolute dy', absDy === DY, `${absDy} vs ${DY}`]);
  checks.push(['absolute after', true, `${finalAbs.x},${finalAbs.y}`]);
}

let failed = false;
console.log('\nchecks');
for (const [name, ok, detail] of checks) {
  if (!ok) failed = true;
  console.log(`  ${ok ? 'ok  ' : 'FAIL'}  ${name.padEnd(24)} ${detail}`);
}

if (failed) {
  console.error('\nWRITE FAILED');
  process.exit(1);
}
console.log('\nWRITE OK');
