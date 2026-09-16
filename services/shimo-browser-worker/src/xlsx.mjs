import { inflateRawSync } from 'node:zlib';

// 上传到石墨的 Excel 文件没有单元格读取接口（石墨的 /sdk/v2/.../sheets/values 只服务
// 石墨原生表格），但链接页里的 /lizard-api/files/{id}/download 能把原文件整份取下来。
// 所以这条链路只能在 Worker 内解析 xlsx。这里刻意不引入第三方解析库：Worker 只额外
// 依赖 node:zlib，避免为一个只读场景把新的供应链风险带进生产容器。
//
// 解析范围限定在「工作簿里有哪些工作表」和「某张表某个矩形区域里的值 + 合并区域」，
// 不处理样式、公式原文、图表、批注和数据透视表。

const EOCD_SIGNATURE = 0x06054b50;
const CENTRAL_SIGNATURE = 0x02014b50;
const LOCAL_SIGNATURE = 0x04034b50;

export class XlsxError extends Error {
  constructor(code, message) {
    super(message);
    this.name = 'XlsxError';
    this.code = code;
  }
}

export function decodeXmlText(value) {
  return String(value)
    .replace(/&#x([0-9a-fA-F]+);/g, (_, hex) => safeCodePoint(parseInt(hex, 16)))
    .replace(/&#([0-9]+);/g, (_, dec) => safeCodePoint(Number(dec)))
    .replace(/&lt;/g, '<')
    .replace(/&gt;/g, '>')
    .replace(/&quot;/g, '"')
    .replace(/&apos;/g, "'")
    .replace(/&amp;/g, '&');
}

function safeCodePoint(code) {
  if (!Number.isFinite(code) || code < 0 || code > 0x10ffff) return '';
  try {
    return String.fromCodePoint(code);
  } catch {
    return '';
  }
}

// 从文件尾部向前找中央目录结束记录（EOCD）。ZIP 允许结尾带注释，所以不能只读最后 22 字节。
export function readZipCentralDirectory(buffer) {
  const earliest = Math.max(0, buffer.length - 65557);
  let eocd = -1;
  for (let offset = buffer.length - 22; offset >= earliest; offset -= 1) {
    if (buffer.readUInt32LE(offset) === EOCD_SIGNATURE) {
      eocd = offset;
      break;
    }
  }
  if (eocd < 0) throw new XlsxError('NOT_A_ZIP', '文件不是 ZIP 结构，无法按 Excel 工作簿解析。');
  const entryCount = buffer.readUInt16LE(eocd + 10);
  const centralOffset = buffer.readUInt32LE(eocd + 16);
  const entries = new Map();
  let cursor = centralOffset;
  for (let index = 0; index < entryCount; index += 1) {
    if (cursor + 46 > buffer.length || buffer.readUInt32LE(cursor) !== CENTRAL_SIGNATURE) {
      throw new XlsxError('BROKEN_ZIP', 'Excel 文件的目录结构损坏，无法解析。');
    }
    const method = buffer.readUInt16LE(cursor + 10);
    const compressedSize = buffer.readUInt32LE(cursor + 20);
    const uncompressedSize = buffer.readUInt32LE(cursor + 24);
    const nameLength = buffer.readUInt16LE(cursor + 28);
    const extraLength = buffer.readUInt16LE(cursor + 30);
    const commentLength = buffer.readUInt16LE(cursor + 32);
    const localOffset = buffer.readUInt32LE(cursor + 42);
    const name = buffer.toString('utf8', cursor + 46, cursor + 46 + nameLength);
    if (compressedSize === 0xffffffff || uncompressedSize === 0xffffffff) {
      throw new XlsxError('ZIP64_UNSUPPORTED', '这个 Excel 文件使用了 ZIP64 结构，当前版本还不支持。');
    }
    entries.set(name, { name, method, compressedSize, uncompressedSize, localOffset });
    cursor += 46 + nameLength + extraLength + commentLength;
  }
  return entries;
}

export function readZipEntry(buffer, entry, { maxBytes } = {}) {
  if (!entry) throw new XlsxError('MISSING_PART', 'Excel 文件里缺少所需的内部结构。');
  const offset = entry.localOffset;
  if (offset + 30 > buffer.length || buffer.readUInt32LE(offset) !== LOCAL_SIGNATURE) {
    throw new XlsxError('BROKEN_ZIP', 'Excel 文件的内部结构损坏，无法解析。');
  }
  const nameLength = buffer.readUInt16LE(offset + 26);
  const extraLength = buffer.readUInt16LE(offset + 28);
  const start = offset + 30 + nameLength + extraLength;
  // 中央目录声明的压缩长度可能和实际数据对不上：越界时直接按损坏拒绝，
  // 而不是让 subarray 静默截断、把「数据不足」留到解压阶段报成别的原因。
  if (start + entry.compressedSize > buffer.length) {
    throw new XlsxError('BROKEN_ZIP', 'Excel 文件的内部结构损坏，无法解析。');
  }
  const raw = buffer.subarray(start, start + entry.compressedSize);
  if (entry.method === 0) {
    if (maxBytes && raw.length > maxBytes) throw new XlsxError('SHEET_TOO_LARGE', '这张工作表的内容超出当前可解析的大小，请缩小读取范围。');
    return raw;
  }
  if (entry.method !== 8) throw new XlsxError('UNSUPPORTED_COMPRESSION', '这个 Excel 文件使用了不支持的压缩方式。');
  // 中央目录里的 uncompressedSize 同样是文件自己声明的，不能信：解压输出必须由解压器
  // 本身限流（maxOutputLength），否则一个声明很小的压缩包可以在内存里膨胀成任意大小。
  try {
    return inflateRawSync(raw, maxBytes && maxBytes > 0 ? { maxOutputLength: maxBytes } : undefined);
  } catch (error) {
    if (error && (error.code === 'ERR_BUFFER_TOO_LARGE' || error.code === 'ERR_OUT_OF_RANGE')) {
      throw new XlsxError('SHEET_TOO_LARGE', '这张工作表的内容超出当前可解析的大小，请缩小读取范围。');
    }
    throw new XlsxError('BROKEN_ZIP', 'Excel 文件里的压缩数据无法解压，文件可能已损坏。');
  }
}

function readZipEntryText(buffer, entry, { maxBytes = 64 * 1024 * 1024 } = {}) {
  if (entry && entry.uncompressedSize > maxBytes) {
    throw new XlsxError('SHEET_TOO_LARGE', '这张工作表的内容超出当前可解析的大小，请缩小读取范围。');
  }
  const bytes = readZipEntry(buffer, entry, { maxBytes });
  if (bytes.length > maxBytes) {
    throw new XlsxError('SHEET_TOO_LARGE', '这张工作表的内容超出当前可解析的大小，请缩小读取范围。');
  }
  return bytes.toString('utf8');
}

function normalizeRelationshipTarget(target) {
  const cleaned = String(target || '').trim().replace(/^\/+/, '').replace(/^\.\//, '');
  if (!cleaned) return '';
  if (cleaned.startsWith('xl/')) return cleaned;
  return `xl/${cleaned}`;
}

export function openXlsxWorkbook(buffer) {
  const entries = readZipCentralDirectory(buffer);
  const workbookEntry = entries.get('xl/workbook.xml');
  if (!workbookEntry) {
    throw new XlsxError('NOT_AN_XLSX', '这个文件不是 Excel 工作簿（没有找到 xl/workbook.xml）。');
  }
  const relationships = new Map();
  const relsEntry = entries.get('xl/_rels/workbook.xml.rels');
  if (relsEntry) {
    const relsXml = readZipEntryText(buffer, relsEntry, { maxBytes: 8 * 1024 * 1024 });
    const relPattern = /<Relationship\b([^>]*)\/?>/g;
    let match;
    while ((match = relPattern.exec(relsXml))) {
      const id = /\bId="([^"]*)"/.exec(match[1]);
      const target = /\bTarget="([^"]*)"/.exec(match[1]);
      if (id && target) relationships.set(id[1], normalizeRelationshipTarget(target[1]));
    }
  }
  const workbookXml = readZipEntryText(buffer, workbookEntry, { maxBytes: 8 * 1024 * 1024 });
  const sheets = [];
  const sheetPattern = /<sheet\b([^>]*)\/?>/g;
  let match;
  while ((match = sheetPattern.exec(workbookXml))) {
    const attributes = match[1];
    const nameMatch = /\bname="([^"]*)"/.exec(attributes);
    const stateMatch = /\bstate="([^"]*)"/.exec(attributes);
    const relMatch = /\br:id="([^"]*)"/.exec(attributes);
    const sheetIdMatch = /\bsheetId="([^"]*)"/.exec(attributes);
    if (!nameMatch) continue;
    const relationshipPath = relMatch ? relationships.get(relMatch[1]) : '';
    const fallbackPath = sheetIdMatch ? `xl/worksheets/sheet${sheetIdMatch[1]}.xml` : '';
    const path = relationshipPath || fallbackPath;
    if (!path || !entries.has(path)) continue;
    sheets.push({
      name: decodeXmlText(nameMatch[1]),
      index: sheets.length,
      path,
      hidden: stateMatch ? stateMatch[1] !== 'visible' : false,
    });
  }
  if (!sheets.length) throw new XlsxError('NO_SHEETS', '这个 Excel 工作簿里没有可读取的工作表。');
  return { entries, sheets };
}

function readSharedStrings(buffer, entries) {
  const entry = entries.get('xl/sharedStrings.xml');
  if (!entry) return [];
  const xml = readZipEntryText(buffer, entry);
  const values = [];
  const itemPattern = /<si\b([^>]*)\/>|<si\b[^>]*>([\s\S]*?)<\/si>/g;
  let match;
  while ((match = itemPattern.exec(xml))) {
    const inner = match[2] || '';
    let text = '';
    const textPattern = /<t\b[^>]*>([\s\S]*?)<\/t>/g;
    let textMatch;
    while ((textMatch = textPattern.exec(inner))) text += decodeXmlText(textMatch[1]);
    values.push(text);
  }
  return values;
}

export function columnIndexFromRef(reference) {
  const letters = /^([A-Za-z]{1,3})/.exec(String(reference || '').trim());
  if (!letters) return 0;
  let index = 0;
  for (const character of letters[1].toUpperCase()) index = index * 26 + (character.charCodeAt(0) - 64);
  return index;
}

function rowIndexFromRef(reference) {
  const digits = /(\d{1,7})\s*$/.exec(String(reference || '').trim());
  return digits ? Number(digits[1]) : 0;
}

function cellValue(cellAttributes, content, sharedStrings) {
  const typeMatch = /\bt="([^"]*)"/.exec(cellAttributes);
  const type = typeMatch ? typeMatch[1] : 'n';
  if (type === 'inlineStr') {
    const inline = /<is\b[^>]*>([\s\S]*?)<\/is>/.exec(content);
    if (!inline) return '';
    let text = '';
    const textPattern = /<t\b[^>]*>([\s\S]*?)<\/t>/g;
    let match;
    while ((match = textPattern.exec(inline[1]))) text += decodeXmlText(match[1]);
    return text;
  }
  const valueMatch = /<v\b[^>]*>([\s\S]*?)<\/v>/.exec(content);
  if (!valueMatch) return '';
  const raw = decodeXmlText(valueMatch[1]);
  if (type === 's') {
    const index = Number(raw);
    return Number.isInteger(index) && index >= 0 && index < sharedStrings.length ? sharedStrings[index] : '';
  }
  if (type === 'b') return raw === '1' || raw.toLowerCase() === 'true';
  if (type === 'e') return raw;
  if (type === 'str' || type === 'd') return raw;
  const numeric = Number(raw);
  // 数值保留原始类型；日期是否转换交给下游（与原生表格路径的约定一致）。
  return Number.isFinite(numeric) && raw.trim() !== '' ? numeric : raw;
}

// 行、单元格都按「先按 r 属性定位，缺省时按上一个位置顺延」处理：某些写入器
// （尤其 WPS）会省略 r，只靠顺序表达位置。
export function parseXlsxSheetGrid(buffer, workbook, sheetEntry, { maxBytes } = {}) {
  const xml = readZipEntryText(buffer, sheetEntry, maxBytes ? { maxBytes } : undefined);
  const sharedStrings = readSharedStrings(buffer, workbook.entries);
  const cells = new Map();
  let maxRow = 0;
  let maxColumn = 0;
  const rowPattern = /<row\b([^>]*?)(?:\/>|>([\s\S]*?)<\/row>)/g;
  let rowMatch;
  let implicitRow = 0;
  while ((rowMatch = rowPattern.exec(xml))) {
    const rowAttributes = rowMatch[1] || '';
    const rowRef = /\br="(\d{1,7})"/.exec(rowAttributes);
    const rowNumber = rowRef ? Number(rowRef[1]) : implicitRow + 1;
    implicitRow = rowNumber;
    const rowContent = rowMatch[2];
    if (rowContent === undefined) continue;
    let implicitColumn = 0;
    const cellPattern = /<c\b([^>]*?)(?:\/>|>([\s\S]*?)<\/c>)/g;
    let cellMatch;
    while ((cellMatch = cellPattern.exec(rowContent))) {
      const attributes = cellMatch[1] || '';
      const content = cellMatch[2] || '';
      const reference = /\br="([A-Za-z]{1,3}\d{1,7})"/.exec(attributes);
      const column = reference ? columnIndexFromRef(reference[1]) : implicitColumn + 1;
      implicitColumn = column;
      const row = reference ? rowIndexFromRef(reference[1]) || rowNumber : rowNumber;
      if (!column || !row) continue;
      const value = cellValue(attributes, content, sharedStrings);
      if (value === '' || value === undefined) continue;
      cells.set(`${row}:${column}`, value);
      if (row > maxRow) maxRow = row;
      if (column > maxColumn) maxColumn = column;
    }
  }
  const merged = [];
  const mergePattern = /<mergeCell\b[^>]*\bref="([^"]*)"/g;
  let mergeMatch;
  while ((mergeMatch = mergePattern.exec(xml))) {
    const range = mergeMatch[1];
    const [start, end] = String(range).split(':');
    if (!start || !end) continue;
    merged.push({
      startRow: rowIndexFromRef(start), startColumn: columnIndexFromRef(start),
      endRow: rowIndexFromRef(end), endColumn: columnIndexFromRef(end),
    });
  }
  return { cells, merged, maxRow, maxColumn };
}

export function columnLabelFromIndex(index) {
  let value = Number(index);
  let label = '';
  while (value > 0) {
    const remainder = (value - 1) % 26;
    label = String.fromCharCode(65 + remainder) + label;
    value = Math.floor((value - 1) / 26);
  }
  return label;
}
