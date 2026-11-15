// Round-trip verification harness.
//
// Reads a real .pptx, applies one text edit through the writer, saves the package, and
// writes it back out so PowerPoint can be asked whether it opens cleanly. This is the
// only check that matters for "did we corrupt the file": our own parser will happily
// re-read a file PowerPoint refuses to open.
//
// Usage: node scripts/office-roundtrip.mjs <in.pptx> <out.pptx> [--text "新文字"]

import fs from 'node:fs';
import { parsePptx } from '../src/office/pptx/parse.js';
import { applyTextEdits } from '../src/office/pptx/write.js';

const args = process.argv.slice(2);
const positional = args.filter(arg => !arg.startsWith('--'));
const [inputPath, outputPath] = positional;
const textIndex = args.indexOf('--text');
const replacement = textIndex >= 0 ? args[textIndex + 1] : '验证写入成功';

if (!inputPath || !outputPath) {
  console.error('usage: node scripts/office-roundtrip.mjs <in.pptx> <out.pptx> [--text "..."]');
  process.exit(2);
}

const bytes = fs.readFileSync(inputPath);
const model = await parsePptx(bytes);

// Pick the first shape that actually carries text and owns its own transform. Inherited
// placeholders are deliberately skipped because the writer refuses to move them, and the
// point of this harness is a clean write path rather than policy.
let target = null;
let slideIndex = -1;
for (let index = 0; index < model.slides.length && !target; index += 1) {
  const slide = model.slides[index];
  const candidate = slide.shapes.find(shape =>
    shape.kind === 'text' && shape.text && !shape.inherited);
  if (candidate) {
    target = candidate;
    slideIndex = index;
  }
}
if (!target) {
  // Fall back to an inherited text shape: the text itself is editable even when the
  // geometry belongs to the layout, which is the common case in a real deck.
  for (let index = 0; index < model.slides.length && !target; index += 1) {
    const slide = model.slides[index];
    const candidate = slide.shapes.find(shape => shape.kind === 'text' && shape.text);
    if (candidate) {
      target = candidate;
      slideIndex = index;
    }
  }
}
if (!target) {
  console.error('FAIL: no editable text shape found');
  process.exit(1);
}

const slide = model.slides[slideIndex];
const partXml = await model.pkg.text(slide.part);
const paragraphs = target.textBody.paragraphs.map((para, index) => (
  index === 0 ? { ...para, text: replacement } : para
));

const nextXml = applyTextEdits(partXml, target, { paragraphs });
if (nextXml === partXml) {
  console.error('FAIL: writer produced no change');
  process.exit(1);
}

// Byte-level proof that nothing outside the edited node moved.
//
// The diff is taken over the whole part, so what comes back is the minimal changed
// span. It is usually just the characters inside one text run, which is the best
// outcome: everything around it is byte-identical. An earlier version of this check
// demanded the span contain `a:t`, which fails exactly when the splice is tightest.
const before = partXml;
const after = nextXml;
const prefix = commonPrefix(before, after);
const suffix = commonSuffix(before, after, prefix);
const originalText = before.slice(prefix, before.length - suffix);
const replacedText = after.slice(prefix, after.length - suffix);
if (originalText.length > 400 || replacedText.length > 400) {
  console.error('FAIL: the changed region is too large to be a single text edit');
  console.error(`  changed ${originalText.length} -> ${replacedText.length} chars`);
  process.exit(1);
}

model.pkg.setText(slide.part, nextXml);
const saved = await model.pkg.save();
fs.writeFileSync(outputPath, saved);

// Re-open the saved bytes with our own parser as a first gate.
const reparsed = await parsePptx(saved);
const reparsedShape = reparsed.slides[slideIndex].shapes.find(shape => shape.elId === target.elId);
const reparsedText = reparsedShape?.textBody?.paragraphs?.[0]?.text;

console.log(`in         ${inputPath} (${bytes.byteLength} bytes)`);
console.log(`out        ${outputPath} (${saved.byteLength} bytes)`);
console.log(`slide      ${slideIndex + 1}  ${slide.part}`);
console.log(`element    ${target.elId}  "${target.text.replace(/\s+/g, ' ').slice(0, 30)}"`);
console.log(`edit       "${replacement}"`);
console.log(`changed    ${originalText.length} chars -> ${replacedText.length} chars`);
console.log(`untouched  ${prefix} leading + ${suffix} trailing chars identical`);
console.log(`reparse    ${reparsedText === replacement ? 'OK' : `MISMATCH (${reparsedText})`}`);
console.log(`parts      ${reparsed.pkg.parts().length} (was ${model.pkg.parts().length})`);

if (reparsedText !== replacement) {
  console.error('FAIL: re-parsed text does not match the written value');
  process.exit(1);
}
console.log('WRITE OK');

function commonPrefix(a, b) {
  const max = Math.min(a.length, b.length);
  let i = 0;
  while (i < max && a[i] === b[i]) i += 1;
  return i;
}

function commonSuffix(a, b, prefix) {
  const max = Math.min(a.length, b.length) - prefix;
  let i = 0;
  while (i < max && a[a.length - 1 - i] === b[b.length - 1 - i]) i += 1;
  return i;
}
