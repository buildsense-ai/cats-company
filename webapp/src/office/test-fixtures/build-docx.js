// Fixture builder for DOCX tests.
//
// Same reasoning as the PPTX fixture: a real .docx is a zip, so a committed binary fixture is
// unreviewable and a corrupted one tells you nothing. Building the parts here means the XML is
// legible and each test can vary exactly one thing.
//
// All sample text is ASCII on purpose. The parse and write paths are encoding-independent, and
// a fixture full of multi-byte characters is one broken editor away from producing misleading
// failures in the tests instead of in the code.

import JSZip from 'jszip';

const W = 'http://schemas.openxmlformats.org/wordprocessingml/2006/main';
const R = 'http://schemas.openxmlformats.org/officeDocument/2006/relationships';

const DECL = '<?xml version="1.0" encoding="UTF-8" standalone="yes"?>';

export const CONTENT_TYPES = `${DECL}
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
<Default Extension="xml" ContentType="application/xml"/>
<Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>
</Types>`;

export const ROOT_RELS = `${DECL}
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>
</Relationships>`;

export const DOCUMENT_RELS = `${DECL}
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
</Relationships>`;

const SECT_PR = '<w:sectPr><w:pgSz w:w="11906" w:h="16838"/><w:pgMar w:top="1440" w:right="1440" w:bottom="1440" w:left="1440"/></w:sectPr>';

/** A run with optional formatting. */
export function run(text, style = {}) {
  const parts = [];
  if (style.bold) parts.push('<w:b/>');
  if (style.italic) parts.push('<w:i/>');
  if (style.underline) parts.push('<w:u w:val="single"/>');
  if (style.size) parts.push(`<w:sz w:val="${style.size * 2}"/>`);
  if (style.color) parts.push(`<w:color w:val="${style.color.replace('#', '').toUpperCase()}"/>`);
  if (style.font) parts.push(`<w:rFonts w:eastAsia="${style.font}" w:ascii="${style.font}"/>`);
  const rPr = parts.length ? `<w:rPr>${parts.join('')}</w:rPr>` : '';
  return `<w:r>${rPr}<w:t xml:space="preserve">${escapeXml(text)}</w:t></w:r>`;
}

/**
 * A paragraph. `runs` accepts run() output or plain strings.
 *
 * A string that already looks like a `w:r` element is spliced in as-is. Without that check a
 * caller writing `paragraph([run('x')])` would get its XML escaped and nested inside another
 * run, producing a document whose text is literally `<w:r>…` — which is exactly what the first
 * version of this fixture did, and it read as a parser bug.
 */
export function paragraph(runs, opts = {}) {
  const pPr = [];
  if (opts.style) pPr.push(`<w:pStyle w:val="${opts.style}"/>`);
  if (opts.align) pPr.push(`<w:jc w:val="${opts.align}"/>`);
  if (opts.indLeftTwips || opts.indFirstTwips) {
    const attrs = [];
    if (opts.indLeftTwips) attrs.push(`w:left="${opts.indLeftTwips}"`);
    if (opts.indFirstTwips) attrs.push(`w:firstLine="${opts.indFirstTwips}"`);
    pPr.push(`<w:ind ${attrs.join(' ')}/>`);
  }
  if (opts.spacingLine) pPr.push(`<w:spacing w:line="${opts.spacingLine}" w:lineRule="auto"/>`);
  if (opts.numbered) pPr.push('<w:numPr><w:ilvl w:val="0"/><w:numId w:val="1"/></w:numPr>');
  const pPrXml = pPr.length ? `<w:pPr>${pPr.join('')}</w:pPr>` : '';
  const body = (runs || []).map((item) => {
    if (typeof item !== 'string') return item;
    return item.startsWith('<w:r') ? item : run(item);
  }).join('');
  return `<w:p>${pPrXml}${body}</w:p>`;
}

/** A table with the given rows of plain-text cells. */
export function table(rows) {
  const cols = Math.max(...rows.map(row => row.length));
  const grid = Array.from({ length: cols }, () => '<w:gridCol w:w="2000"/>').join('');
  const trs = rows.map(row => {
    const cells = row.map(text =>
      `<w:tc><w:tcPr/><w:p>${run(text)}</w:p></w:tc>`).join('');
    return `<w:tr>${cells}</w:tr>`;
  }).join('');
  return `<w:tbl><w:tblPr><w:tblW w:w="0" w:type="auto"/></w:tblPr><w:tblGrid>${grid}</w:tblGrid>${trs}</w:tbl>`;
}

function escapeXml(value) {
  return String(value)
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;');
}

export function documentXml(bodyXml) {
  return `${DECL}
<w:document xmlns:w="${W}" xmlns:r="${R}"><w:body>${bodyXml}${SECT_PR}</w:body></w:document>`;
}

/** Assemble a .docx package from raw body XML. */
export async function buildDocx(bodyXml) {
  const zip = new JSZip();
  zip.file('[Content_Types].xml', CONTENT_TYPES);
  zip.file('_rels/.rels', ROOT_RELS);
  zip.file('word/document.xml', documentXml(bodyXml));
  zip.file('word/_rels/document.xml.rels', DOCUMENT_RELS);
  return zip.generateAsync({ type: 'uint8array' });
}

/**
 * A small realistic document: heading, body paragraph, a paragraph with two differently
 * formatted runs, a numbered list item, a table, and an indented closing line.
 */
export async function buildSampleDocx() {
  return buildDocx([
    paragraph([run('Quarterly review', { bold: true, size: 16 })], { style: 'Heading1' }),
    paragraph([run('Delivery cycle is down to two weeks this quarter.')]),
    paragraph([
      run('Key: '),
      run('cost down 15%', { bold: true, color: '#C86400' }),
    ], { align: 'center' }),
    paragraph(['List item one'], { numbered: true }),
    table([['Metric', 'Last', 'Now'], ['Delivery', '4 weeks', '2 weeks']]),
    paragraph([run('Closing line.')], { indFirstTwips: 420 }),
  ].join(''));
}

export { W, R, DECL, SECT_PR };