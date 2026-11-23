// Word picture insertion.
//
// A Word picture needs four things that a paragraph edit does not:
//
//   1. the image bytes as a part under `word/media/`
//   2. a relationship on `word/document.xml.rels` pointing at those bytes
//   3. a `Default` entry in `[Content_Types].xml` for the file extension
//   4. a `w:drawing` inside a `w:r`, referencing the relationship id
//
// Miss any one and Word opens the document with a broken-image placeholder instead of
// complaining, so the failure looks like "the picture did not appear" rather than an error.

import { applyEdits, attr, escapeText, findChild, findDescendants, findDescendant, scanXml } from '../ooxml/xml.js';
import { addRelationship, ensureContentTypeDefault, nextMediaNumber } from '../pptx/insert.js';

const IMAGE_MIME = {
  png: 'image/png',
  jpg: 'image/jpeg',
  jpeg: 'image/jpeg',
  gif: 'image/gif',
  bmp: 'image/bmp',
  webp: 'image/webp',
  svg: 'image/svg+xml',
};

// English Metric Units: 914400 per inch. Word's drawing extent is in EMU, not twips.
const EMU_PER_INCH = 914400;
const EMU_PER_CM = 360000;

/** Read the page's usable width in twips, so an inserted picture fits the text column. */
function usableWidthTwips(body) {
  const sectPr = findChild(body, 'w:sectPr');
  if (!sectPr) return 9026;
  const pgSz = findChild(sectPr, 'w:pgSz');
  const pgMar = findChild(sectPr, 'w:pgMar');
  const width = Number(attr(pgSz, 'w:w', '11906')) || 11906;
  const left = Number(attr(pgMar, 'w:left', '1440')) || 1440;
  const right = Number(attr(pgMar, 'w:right', '1440')) || 1440;
  return Math.max(1440, width - left - right);
}

function readU16BE(bytes, offset) {
  return (bytes[offset] << 8) | bytes[offset + 1];
}

function readU16LE(bytes, offset) {
  return bytes[offset] | (bytes[offset + 1] << 8);
}

function readU24LE(bytes, offset) {
  return bytes[offset] | (bytes[offset + 1] << 8) | (bytes[offset + 2] << 16);
}

function readU32BE(bytes, offset) {
  return ((bytes[offset] << 24) | (bytes[offset + 1] << 16)
    | (bytes[offset + 2] << 8) | bytes[offset + 3]) >>> 0;
}

/**
 * Read the intrinsic pixel size of the formats the picker accepts.
 *
 * The writer used to force every picture to a square. That is only correct for the 1x1 fixture
 * used by the early round-trip test; a 200x100 photo came back as a 200x200 square in Word.
 * Keeping the ratio here means both the UI and the script harness get the same answer without
 * depending on browser-only image APIs.
 */
export function readImageSize(bytes, ext = '') {
  if (!bytes || bytes.byteLength < 10) return null;
  const suffix = String(ext || '').toLowerCase().replace(/^\./, '');

  if (bytes[0] === 0x89 && bytes[1] === 0x50 && bytes[2] === 0x4e && bytes[3] === 0x47
      && bytes[4] === 0x0d && bytes[5] === 0x0a && bytes[6] === 0x1a && bytes[7] === 0x0a) {
    return { width: readU32BE(bytes, 16), height: readU32BE(bytes, 20) };
  }

  const gifHeader = String.fromCharCode(...bytes.slice(0, 6));
  if (gifHeader === 'GIF87a' || gifHeader === 'GIF89a') {
    return { width: readU16LE(bytes, 6), height: readU16LE(bytes, 8) };
  }

  if (bytes[0] === 0xff && bytes[1] === 0xd8) {
    let offset = 2;
    while (offset + 9 < bytes.byteLength) {
      while (offset < bytes.byteLength && bytes[offset] !== 0xff) offset += 1;
      while (offset < bytes.byteLength && bytes[offset] === 0xff) offset += 1;
      if (offset >= bytes.byteLength) break;
      const marker = bytes[offset];
      offset += 1;
      if (marker === 0xd8 || marker === 0xd9 || marker === 0x01
          || (marker >= 0xd0 && marker <= 0xd7)) continue;
      if (offset + 1 >= bytes.byteLength) break;
      const length = readU16BE(bytes, offset);
      if (length < 2 || offset + length > bytes.byteLength) break;
      const isStartOfFrame = marker >= 0xc0 && marker <= 0xcf
        && marker !== 0xc4 && marker !== 0xc8 && marker !== 0xcc;
      if (isStartOfFrame && offset + 7 < bytes.byteLength) {
        return { width: readU16BE(bytes, offset + 5), height: readU16BE(bytes, offset + 3) };
      }
      offset += length;
    }
  }

  if (bytes[0] === 0x42 && bytes[1] === 0x4d) {
    const width = bytes[18] | (bytes[19] << 8) | (bytes[20] << 16) | (bytes[21] << 24);
    const height = bytes[22] | (bytes[23] << 8) | (bytes[24] << 16) | (bytes[25] << 24);
    return { width: Math.abs(width), height: Math.abs(height) };
  }

  if (String.fromCharCode(...bytes.slice(0, 4)) === 'RIFF'
      && String.fromCharCode(...bytes.slice(8, 12)) === 'WEBP') {
    const chunk = String.fromCharCode(...bytes.slice(12, 16));
    const data = 20;
    if (chunk === 'VP8X' && data + 9 < bytes.byteLength) {
      return { width: readU24LE(bytes, data + 4) + 1, height: readU24LE(bytes, data + 7) + 1 };
    }
    if (chunk === 'VP8 ' && data + 9 < bytes.byteLength) {
      return { width: readU16LE(bytes, data + 6) & 0x3fff, height: readU16LE(bytes, data + 8) & 0x3fff };
    }
    if (chunk === 'VP8L' && data + 4 < bytes.byteLength) {
      const bits = bytes[data + 1] | (bytes[data + 2] << 8)
        | (bytes[data + 3] << 16) | (bytes[data + 4] << 24);
      return { width: ((bits >>> 1) & 0x3fff) + 1, height: ((bits >>> 15) & 0x3fff) + 1 };
    }
  }

  if (suffix === 'svg') {
    const text = new TextDecoder().decode(bytes);
    const width = Number((text.match(/\bwidth=["']([\d.]+)/i) || [])[1]);
    const height = Number((text.match(/\bheight=["']([\d.]+)/i) || [])[1]);
    if (width > 0 && height > 0) return { width, height };
    const box = (text.match(/\bviewBox=["']\s*[-\d.]+\s+[-\d.]+\s+([\d.]+)\s+([\d.]+)/i) || []);
    if (Number(box[1]) > 0 && Number(box[2]) > 0) {
      return { width: Number(box[1]), height: Number(box[2]) };
    }
  }

  return null;
}

/**
 * Build the `w:drawing` element.
 *
 * `pic:pic` carries the fill and the geometry; `wp:inline` is what makes it sit in the text flow
 * rather than float. A floating anchor needs positioning relative to a paragraph or page, which is
 * a different feature — inserting inline is the honest default for "put a picture here".
 */
function buildDrawingXml(relId, { widthEmu, heightEmu, name, docPrId }) {
  return '<w:drawing>'
    + `<wp:inline distT="0" distB="0" distL="0" distR="0" xmlns:wp="http://schemas.openxmlformats.org/drawingml/2006/wordprocessingDrawing">`
    + `<wp:extent cx="${widthEmu}" cy="${heightEmu}"/>`
    + `<wp:effectExtent l="0" t="0" r="0" b="0"/>`
    + `<wp:docPr id="${docPrId}" name="${escapeXmlAttr(name)}"/>`
    + '<wp:cNvGraphicFramePr>'
    + '<a:graphicFrameLocks xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" noChangeAspect="1"/>'
    + '</wp:cNvGraphicFramePr>'
    + '<a:graphic xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main">'
    + '<a:graphicData uri="http://schemas.openxmlformats.org/drawingml/2006/picture">'
    + '<pic:pic xmlns:pic="http://schemas.openxmlformats.org/drawingml/2006/picture">'
    + `<pic:nvPicPr><pic:cNvPr id="${docPrId}" name="${escapeXmlAttr(name)}"/><pic:cNvPicPr/></pic:nvPicPr>`
    + `<pic:blipFill><a:blip r:embed="${relId}" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"/>`
    + '<a:stretch><a:fillRect/></a:stretch></pic:blipFill>'
    + '<pic:spPr>'
    + `<a:xfrm><a:off x="0" y="0"/><a:ext cx="${widthEmu}" cy="${heightEmu}"/></a:xfrm>`
    + '<a:prstGeom prst="rect"><a:avLst/></a:prstGeom>'
    + '</pic:spPr>'
    + '</pic:pic></a:graphicData></a:graphic></wp:inline></w:drawing>';
}

function escapeXmlAttr(value) {
  return String(value).replace(/&/g, '&amp;').replace(/</g, '&lt;')
    .replace(/>/g, '&gt;').replace(/"/g, '&quot;');
}

/** Next free `wp:docPr/@id`; Word requires it unique across the whole document. */
function nextDocPrId(partXml) {
  let max = 0;
  for (const node of findDescendants(scanXml(partXml), 'wp:docPr')) {
    const id = Number(attr(node, 'id'));
    if (Number.isFinite(id) && id > max) max = id;
  }
  return max + 1;
}

/**
 * Insert a picture into the paragraph after the given block.
 *
 * Returns the package mutation plus the new part text, so the caller can save once. The picture
 * lands in its own paragraph rather than appended to existing text: an inline image inside a line
 * of prose changes that line's height and looks like a layout bug to the user.
 */
export async function applyInsertImage(model, { bytes, ext, name, widthCm = 12 }) {
  if (!bytes || bytes.byteLength === 0) throw new Error('图片内容为空');

  const suffix = String(ext || 'png').toLowerCase().replace(/^\./, '');
  const pixelSize = readImageSize(bytes, suffix);
  const mime = IMAGE_MIME[suffix] || 'image/png';
  const mediaName = `word/media/image${nextMediaNumber(model.pkg)}.${suffix === 'jpeg' ? 'jpg' : suffix}`;
  model.pkg.setBinary(mediaName, bytes);
  await ensureContentTypeDefault(model.pkg, suffix === 'jpeg' ? 'jpg' : suffix, mime);

  // Target is relative to `word/`, where document.xml lives.
  const relId = await addRelationship(model.pkg, model.part, {
    type: 'http://schemas.openxmlformats.org/officeDocument/2006/relationships/image',
    target: `media/${mediaName.split('/').pop()}`,
  });

  const partXml = await model.pkg.text(model.part);
  const tree = scanXml(partXml);
  const body = findDescendant(tree, 'w:body');
  if (!body) throw new Error('文档缺少正文');

  // Fit the picture inside the text column so a wide photo does not run off the page.
  const usableTwips = usableWidthTwips(body);
  const maxWidthEmu = Math.round((usableTwips / 1440) * EMU_PER_INCH);
  const requestedEmu = Math.round(Math.max(1, Number(widthCm) || 12) * EMU_PER_CM);
  const widthEmu = Math.min(requestedEmu, maxWidthEmu);
  // Preserve the source ratio whenever the header tells us what it is. Square is only the
  // fallback for an unrecognised format, not the default for every picture.
  const heightEmu = pixelSize?.width > 0 && pixelSize?.height > 0
    ? Math.max(1, Math.round(widthEmu * (pixelSize.height / pixelSize.width)))
    : widthEmu;

  const drawing = buildDrawingXml(relId, {
    widthEmu, heightEmu, name: name || `image${suffix}`, docPrId: nextDocPrId(partXml),
  });
  const paragraphXml = `<w:p><w:r>${drawing}</w:r></w:p>`;

  // Append at the end of the body, before sectPr, which must stay last.
  const sectPr = findChild(body, 'w:sectPr');
  const insertAt = sectPr ? sectPr.start : body.innerEnd;
  model.pkg.setText(model.part, applyEdits(partXml, [
    { start: insertAt, end: insertAt, replacement: paragraphXml },
  ]));

  return { mediaName, relId, widthEmu, heightEmu, pixelWidth: pixelSize?.width || 0, pixelHeight: pixelSize?.height || 0 };
}

export { buildDrawingXml, nextDocPrId, usableWidthTwips, IMAGE_MIME };
