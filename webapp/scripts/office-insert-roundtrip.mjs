// Phase-4 round-trip: insert primitives, group two shapes, add and duplicate a slide, on a
// real PowerPoint file.
//
// This is the highest-risk harness in the set. Every operation here creates or removes whole
// parts of the package, and the failure mode is not an exception — it is a file that our own
// parser reads back correctly while PowerPoint offers to repair it. So the harness prints the
// structural facts afterwards (part counts, ids, relationship targets) and the PowerShell
// check that follows is what actually decides whether the file is good.
//
// Usage: node scripts/office-insert-roundtrip.mjs <in.pptx> <out.pptx>

import fs from 'node:fs';
import { parsePptx } from '../src/office/pptx/parse.js';
import { applyOps } from '../src/office/pptx/apply.js';
import { readSlideOrder } from '../src/office/pptx/slides.js';
import { readRelationships } from '../src/office/ooxml/zip.js';

const [inputPath, outputPath] = process.argv.slice(2);
if (!inputPath || !outputPath) {
  console.error('usage: node scripts/office-insert-roundtrip.mjs <in.pptx> <out.pptx>');
  process.exit(2);
}

// A real 1x1 PNG. Fake bytes would be written into ppt/media and the content type would
// claim image/png for something that is not an image, which Office rejects.
const PNG_1X1 = Uint8Array.from(Buffer.from(
  'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8DwHwAFAAH/q842iQAAAABJRU5ErkJggg==',
  'base64',
));

const bytes = fs.readFileSync(inputPath);
let model = await parsePptx(bytes);

const log = [];
function step(name, detail) {
  log.push(`  ${name.padEnd(16)} ${detail}`);
}

// Slide size drives where the inserted boxes land; putting them on top of existing content
// would make the visual check ambiguous.
const { cx: slideW, cy: slideH } = model.slideSize;
const base = { x: Math.round(slideW * 0.08), y: Math.round(slideH * 0.62),
  cx: Math.round(slideW * 0.24), cy: Math.round(slideH * 0.1) };

const partsBefore = model.pkg.parts().length;

// 1. Four primitives plus a picture, each written immediately because a new shape has no
//    element id until it exists in the file.
const ops = [
  { op: 'insertText', slideIndex: 0, payload: { text: '插入的文本框', box: base, size: 18 } },
  { op: 'insertShape', slideIndex: 0,
    payload: { preset: 'rect', box: { ...base, x: base.x + Math.round(slideW * 0.27) } } },
  { op: 'insertShape', slideIndex: 0,
    payload: { preset: 'roundRect', box: { ...base, x: base.x + Math.round(slideW * 0.54) } } },
  { op: 'insertTable', slideIndex: 0,
    payload: { rows: 3, cols: 3, box: { x: base.x, y: base.y + Math.round(slideH * 0.16),
      cx: Math.round(slideW * 0.3), cy: Math.round(slideH * 0.2) } } },
  { op: 'insertPicture', slideIndex: 0,
    payload: { bytes: PNG_1X1, ext: 'png', name: 'probe.png',
      box: { ...base, x: base.x + Math.round(slideW * 0.7), cx: Math.round(slideW * 0.15),
        cy: Math.round(slideW * 0.15) } } },
];

let result = await applyOps(model, ops);
if (!result.changed) {
  console.error('FAIL: no insert produced a change');
  process.exit(1);
}
step('insert', `${result.applied} ops, ${model.pkg.parts().length} parts`);
model = await parsePptx(result.bytes);

// 2. Group the first two shapes of the slide after the inserts. Grouping is a separate write
//    because it removes N nodes and re-emits them inside a new grpSp.
const slideZero = model.slides[0];
const groupable = slideZero.shapes.filter(shape => !shape.inherited).slice(0, 2);
if (groupable.length === 2) {
  result = await applyOps(model, [{
    op: 'group', slideIndex: 0, payload: { elIds: groupable.map(shape => shape.elId) },
  }]);
  step('group', groupable.map(shape => shape.elId).join(' + '));
  model = await parsePptx(result.bytes);
} else {
  step('group', 'SKIPPED (fewer than two non-inherited shapes)');
}

// 3. Duplicate, then add a fresh slide after it. Both touch presentation.xml, the rels part
//    and content types, so they run as separate structural writes.
result = await applyOps(model, [{ op: 'duplicateSlide', slideIndex: 0 }]);
step('duplicate', `${model.pkg.parts().length} parts`);
model = await parsePptx(result.bytes);

result = await applyOps(model, [{ op: 'addSlide', payload: { at: 2, templateIndex: 0 } }]);
step('addSlide', `${model.pkg.parts().length} parts`);
model = await parsePptx(result.bytes);

// 4. Reorder the last slide to the front, which only changes the sldIdLst order.
const count = model.slides.length;
result = await applyOps(model, [{
  op: 'moveSlide', slideIndex: count - 1, payload: { from: count - 1, to: 0 },
}]);
step('moveSlide', `slide ${count} -> 1`);
model = await parsePptx(result.bytes);

fs.writeFileSync(outputPath, result.bytes);

// ---- structural report ----
const final = await parsePptx(fs.readFileSync(outputPath));
const presentationXml = await final.pkg.text('ppt/presentation.xml');
const { entries } = readSlideOrder(presentationXml);
const presRels = await readRelationships(final.pkg, 'ppt/presentation.xml');
const contentTypes = await final.pkg.text('[Content_Types].xml');
const slideOverrides = (contentTypes.match(/presentationml\.slide\+xml/g) || []).length;

console.log(`in         ${inputPath}`);
console.log(`out        ${outputPath}  (${fs.statSync(outputPath).size} bytes)`);
console.log('');
console.log('steps');
log.forEach(line => console.log(line));
console.log('');
console.log(`slides     ${final.slides.length}`);
console.log(`parts      ${partsBefore} -> ${final.pkg.parts().length}`);

const checks = [];

// Every sldId needs a resolvable relationship, or that slide is a dangling reference.
const missingRels = entries.filter(entry => !presRels.has(entry.rid));
checks.push(['sldId rels', missingRels.length === 0,
  missingRels.length === 0 ? `${entries.length} entries all resolve`
    : `${missingRels.length} dangling`]);

// sldId/@id must be unique and >= 256.
const ids = entries.map(entry => entry.id);
checks.push(['sldId unique', new Set(ids).size === ids.length,
  ids.join(',')]);

// One Override per slide part, no more and no less.
checks.push(['content types', slideOverrides === final.slides.length,
  `${slideOverrides} slide overrides for ${final.slides.length} slides`]);

// Shape ids must be unique per slide; a duplicate is the classic repair prompt.
let duplicateShapeIds = 0;
for (const slide of final.slides) {
  const seen = new Set();
  const walk = (shapes) => {
    for (const shape of shapes || []) {
      if (seen.has(shape.id)) duplicateShapeIds += 1;
      seen.add(shape.id);
      if (shape.children) walk(shape.children);
    }
  };
  walk(slide.shapes);
}
checks.push(['shape ids', duplicateShapeIds === 0,
  duplicateShapeIds === 0 ? 'unique per slide' : `${duplicateShapeIds} duplicates`]);

// The group must still carry both members.
const group = final.slides.find(slide => slide.shapes.some(shape => shape.kind === 'group'));
const grouped = group?.shapes.find(shape => shape.kind === 'group');
checks.push(['group intact', !grouped || (grouped.children || []).length === 2,
  grouped ? `${(grouped.children || []).length} children` : 'no group (skipped)']);

// The picture's relationship must resolve to bytes that exist.
const picSlide = final.slides.find(slide => slide.shapes.some(shape => shape.kind === 'picture'));
const picture = picSlide?.shapes.find(shape => shape.kind === 'picture');
let pictureOk = true;
if (picture) {
  const rels = await readRelationships(final.pkg, picSlide.part);
  const rel = rels.get(picture.imageRelId);
  const mediaBytes = rel ? await final.pkg.binary(rel.resolved) : null;
  pictureOk = Boolean(mediaBytes && mediaBytes.byteLength > 0);
  checks.push(['picture media', pictureOk,
    rel ? `${rel.resolved} ${mediaBytes ? mediaBytes.byteLength : 0} bytes` : 'relationship missing']);
}

console.log('');
let failed = 0;
for (const [name, ok, detail] of checks) {
  console.log(`${ok ? 'ok  ' : 'FAIL'}  ${name.padEnd(14)} ${detail}`);
  if (!ok) failed += 1;
}

if (failed > 0) {
  console.error(`\nFAIL: ${failed} structural check(s) failed`);
  process.exit(1);
}
console.log('\nSTRUCTURE OK');
