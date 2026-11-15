// Phase-0 fidelity probe.
//
// Renders a real PowerPoint file through our parser and prints what we actually got:
// slide count, per-shape kind and geometry, resolved theme colors, inheritance. The
// point is to compare against the file's own truth, not against a fixture we wrote.
//
// Usage: node scripts/office-probe.mjs <file.pptx> [--json]

import fs from 'node:fs';
import path from 'node:path';
import { parsePptx } from '../src/office/pptx/parse.js';

const args = process.argv.slice(2);
const file = args.find(arg => !arg.startsWith('--'));
const asJson = args.includes('--json');

if (!file) {
  console.error('usage: node scripts/office-probe.mjs <file.pptx> [--json]');
  process.exit(2);
}

const buffer = fs.readFileSync(file);
const started = Date.now();
const model = await parsePptx(buffer);
const parseMs = Date.now() - started;

function flatten(shapes, out = []) {
  for (const shape of shapes || []) {
    out.push(shape);
    if (shape.children) flatten(shape.children, out);
  }
  return out;
}

const report = {
  file: path.basename(file),
  bytes: buffer.byteLength,
  parseMs,
  slideSize: model.slideSize,
  themeColors: model.themeColors,
  parts: model.pkg.parts().length,
  slides: model.slides.map(slide => ({
    index: slide.index,
    part: slide.part,
    layout: slide.layoutPart,
    shapes: flatten(slide.shapes).map(shape => ({
      elId: shape.elId,
      kind: shape.kind,
      role: shape.role || undefined,
      label: shape.label,
      text: shape.text ? shape.text.replace(/\s+/g, ' ').slice(0, 40) : undefined,
      box: shape.box,
      inherited: shape.inherited || undefined,
      table: shape.table ? `${shape.tableRows}x${shape.tableCols}` : undefined,
      paragraphs: shape.textBody ? shape.textBody.paragraphs.length : undefined,
      runs: shape.textBody ? shape.textBody.paragraphs.reduce((sum, p) => sum + p.runs.length, 0) : undefined,
    })),
  })),
};

if (asJson) {
  console.log(JSON.stringify(report, null, 2));
  process.exit(0);
}

console.log(`file        ${report.file}`);
console.log(`bytes       ${report.bytes}`);
console.log(`parts       ${report.parts}`);
console.log(`parse       ${report.parseMs} ms`);
console.log(`slideSize   ${report.slideSize.cx} x ${report.slideSize.cy}`);
console.log(`theme       ${Object.entries(report.themeColors).map(([k, v]) => `${k}=${v}`).join(' ')}`);
console.log('');

for (const slide of report.slides) {
  console.log(`slide ${slide.index + 1}  ${slide.part}`);
  console.log(`  layout    ${slide.layout}`);
  for (const shape of slide.shapes) {
    const bits = [
      shape.kind.padEnd(14),
      shape.label.padEnd(10),
      `box ${Math.round(shape.box.x)},${Math.round(shape.box.y)} ${Math.round(shape.box.cx)}x${Math.round(shape.box.cy)}`,
    ];
    if (shape.inherited) bits.push('INHERITED');
    if (shape.paragraphs != null) bits.push(`paras=${shape.paragraphs} runs=${shape.runs}`);
    if (shape.table) bits.push(`table=${shape.table}`);
    console.log(`  - ${bits.join('  ')}`);
    if (shape.text) console.log(`      "${shape.text}"`);
  }
  console.log('');
}

// A quick guard against the most common silent failure: a parser that finds slides but
// no shapes, or shapes but no text. Both look like "it works" in a screenshot.
const totalShapes = report.slides.reduce((sum, s) => sum + s.shapes.length, 0);
const totalText = report.slides.reduce(
  (sum, s) => sum + s.shapes.filter(shape => shape.text).length, 0);
console.log(`total       ${totalShapes} shapes, ${totalText} with text`);

if (totalShapes === 0) {
  console.error('PROBE FAIL: no shapes parsed');
  process.exit(1);
}
