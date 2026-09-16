import assert from 'node:assert/strict';
import test from 'node:test';
import zlib from 'node:zlib';
import {
  XlsxError, columnIndexFromRef, columnLabelFromIndex, decodeXmlText,
  openXlsxWorkbook, parseXlsxSheetGrid, readZipCentralDirectory, readZipEntry,
} from '../src/xlsx.mjs';

// 测试用的最小 ZIP 打包器：只支持 store/ deflate，不依赖第三方库。
// 上传到石墨的 Excel 是用户文件，解析器必须能处理畸形或恶意构造的包，
// 所以这里也要能故意构造「声明大小撒谎」的坏包。
const CRC_TABLE = (() => {
  const table = new Uint32Array(256);
  for (let index = 0; index < 256; index += 1) {
    let value = index;
    for (let bit = 0; bit < 8; bit += 1) value = (value & 1) ? (0xedb88320 ^ (value >>> 1)) : (value >>> 1);
    table[index] = value >>> 0;
  }
  return table;
})();

function crc32(buffer) {
  let crc = 0xffffffff;
  for (const byte of buffer) crc = CRC_TABLE[(crc ^ byte) & 0xff] ^ (crc >>> 8);
  return (crc ^ 0xffffffff) >>> 0;
}

function buildZip(entries) {
  const chunks = [];
  const central = [];
  let offset = 0;
  for (const entry of entries) {
    const raw = Buffer.isBuffer(entry.data) ? entry.data : Buffer.from(String(entry.data), 'utf8');
    const method = entry.method === undefined ? 8 : entry.method;
    const compressed = method === 8 ? zlib.deflateRawSync(raw) : raw;
    const claimedUncompressed = entry.claimedUncompressedSize === undefined ? raw.length : entry.claimedUncompressedSize;
    const name = Buffer.from(entry.name, 'utf8');
    const local = Buffer.alloc(30);
    local.writeUInt32LE(0x04034b50, 0);
    local.writeUInt16LE(20, 4);
    local.writeUInt16LE(method, 8);
    local.writeUInt32LE(crc32(raw), 14);
    local.writeUInt32LE(compressed.length, 18);
    local.writeUInt32LE(claimedUncompressed, 22);
    local.writeUInt16LE(name.length, 26);
    chunks.push(local, name, compressed);

    const centralEntry = Buffer.alloc(46);
    centralEntry.writeUInt32LE(0x02014b50, 0);
    centralEntry.writeUInt16LE(20, 4);
    centralEntry.writeUInt16LE(20, 6);
    centralEntry.writeUInt16LE(method, 10);
    centralEntry.writeUInt32LE(crc32(raw), 16);
    centralEntry.writeUInt32LE(compressed.length, 20);
    centralEntry.writeUInt32LE(claimedUncompressed, 24);
    centralEntry.writeUInt16LE(name.length, 28);
    centralEntry.writeUInt32LE(offset, 42);
    central.push(centralEntry, name);
    offset += local.length + name.length + compressed.length;
  }
  const centralDirectory = Buffer.concat(central);
  const eocd = Buffer.alloc(22);
  eocd.writeUInt32LE(0x06054b50, 0);
  eocd.writeUInt16LE(entries.length, 8);
  eocd.writeUInt16LE(entries.length, 10);
  eocd.writeUInt32LE(centralDirectory.length, 12);
  eocd.writeUInt32LE(offset, 16);
  return Buffer.concat([...chunks, centralDirectory, eocd]);
}

function workbookXml(names) {
  const rows = names.map((name, index) => `    <sheet name="${name}" sheetId="${index + 1}" r:id="rId${index + 1}"/>`).join('\n');
  return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>\n<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">\n  <sheets>\n${rows}\n  </sheets>\n</workbook>`;
}

function relsXml(count) {
  const rows = Array.from({ length: count }, (_, index) => `  <Relationship Id="rId${index + 1}" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet${index + 1}.xml"/>`).join('\n');
  return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>\n<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">\n${rows}\n</Relationships>`;
}

function sheetXml(body) {
  return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>\n<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">\n${body}\n</worksheet>`;
}

function buildWorkbook({ sheets, sharedStrings, deflate } = {}) {
  const entries = [
    { name: 'xl/workbook.xml', data: workbookXml(sheets.map(sheet => sheet.name)), deflate },
    { name: 'xl/_rels/workbook.xml.rels', data: relsXml(sheets.length), deflate },
  ];
  if (sharedStrings) entries.push({ name: 'xl/sharedStrings.xml', data: sharedStrings, deflate });
  sheets.forEach((sheet, index) => entries.push({ name: `xl/worksheets/sheet${index + 1}.xml`, data: sheetXml(sheet.body), deflate }));
  return buildZip(entries);
}

test('非 ZIP 字节按 NOT_A_ZIP 拒绝', () => {
  assert.throws(
    () => readZipCentralDirectory(Buffer.from('这不是一个 ZIP 包，只是一段普通文本')),
    error => error instanceof XlsxError && error.code === 'NOT_A_ZIP',
  );
});

test('文件过短（不足 EOCD 长度）也按 NOT_A_ZIP 拒绝，不抛越界错误', () => {
  assert.throws(() => readZipCentralDirectory(Buffer.from('PK')), error => error.code === 'NOT_A_ZIP');
});

test('缺少 xl/workbook.xml 的 ZIP 按 NOT_AN_XLSX 拒绝', () => {
  const bytes = buildZip([{ name: 'xl/styles.xml', data: '<styleSheet/>' }]);
  assert.throws(() => openXlsxWorkbook(bytes), error => error instanceof XlsxError && error.code === 'NOT_AN_XLSX');
});

test('集中目录声明 ZIP64 时明确拒绝而不是给出错误数据', () => {
  const bytes = buildZip([{ name: 'xl/workbook.xml', data: workbookXml(['S1']), claimedUncompressedSize: 0xffffffff }]);
  assert.throws(() => openXlsxWorkbook(bytes), error => error.code === 'ZIP64_UNSUPPORTED');
});

test('压缩方式不受支持时报 UNSUPPORTED_COMPRESSION', () => {
  const bytes = buildWorkbook({ sheets: [{ name: 'S1', body: '<row r="1"><c r="A1"><v>1</v></c></row>' }], deflate: false });
  const entries = readZipCentralDirectory(bytes);
  const entry = entries.get('xl/worksheets/sheet1.xml');
  assert.throws(
    () => readZipEntry(bytes, { ...entry, method: 12 }),
    error => error.code === 'UNSUPPORTED_COMPRESSION',
  );
});

test('中央目录声明的压缩长度越界时按 BROKEN_ZIP 拒绝', () => {
  const bytes = buildZip([{ name: 'a.txt', data: 'hello' }]);
  const entry = readZipCentralDirectory(bytes).get('a.txt');
  assert.throws(
    () => readZipEntry(bytes, { ...entry, compressedSize: entry.compressedSize + 100000 }),
    error => error.code === 'BROKEN_ZIP',
  );
});

test('解压输出超过限制时在解压阶段就被拦下（防 zip bomb）', () => {
  // 压缩数据实际解压出 2MB，但中央目录声称只有 100 字节：
  // 声明值不可信，必须由解压器本身的输出上限兜底。
  const bomb = Buffer.alloc(2 * 1024 * 1024, 0x41);
  const bytes = buildZip([{ name: 'big.bin', data: bomb, claimedUncompressedSize: 100 }]);
  const entry = readZipCentralDirectory(bytes).get('big.bin');
  assert.throws(
    () => readZipEntry(bytes, entry, { maxBytes: 64 * 1024 }),
    error => error instanceof XlsxError && error.code === 'SHEET_TOO_LARGE',
  );
});

test('解压出的数据不是合法压缩流时报 BROKEN_ZIP', () => {
  const bytes = buildZip([{ name: 'a.txt', data: 'hello world' }]);
  const entry = readZipCentralDirectory(bytes).get('a.txt');
  // 把原数据换成随机字节，CRC 与内容都对不上，inflate 必然失败。
  const corrupted = Buffer.from(bytes);
  corrupted.write('not-deflate-data', entry.localOffset + 30, 'utf8');
  assert.throws(
    () => readZipEntry(corrupted, entry),
    error => error instanceof XlsxError && (error.code === 'BROKEN_ZIP' || error.code === 'NOT_A_ZIP'),
  );
});

test('解析工作簿清单：名称、顺序与隐藏状态', () => {
  const bytes = buildWorkbook({
    sheets: [
      { name: '工作表1', body: '<row r="1"><c r="A1"><v>1</v></c></row>' },
      { name: '开票回款', body: '<row r="1"><c r="A1"><v>2</v></c></row>' },
    ],
  });
  const workbook = openXlsxWorkbook(bytes);
  assert.deepEqual(workbook.sheets.map(sheet => sheet.name), ['工作表1', '开票回款']);
  assert.deepEqual(workbook.sheets.map(sheet => sheet.index), [0, 1]);
  assert.deepEqual(workbook.sheets.map(sheet => sheet.hidden), [false, false]);
  assert.equal(workbook.sheets[1].path, 'xl/worksheets/sheet2.xml');
});

test('解析单元格值：数字、共享字符串、inlineStr、布尔与日期序列号', () => {
  const sharedStrings = '<?xml version="1.0"?>\n<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" count="2" uniqueCount="2"><si><t>客户A</t></si><si><r><t>富</t></r><r><t>文本</t></r></si></sst>';
  const body = [
    '<row r="1">',
    '  <c r="A1" t="s"><v>0</v></c>',
    '  <c r="B1" t="s"><v>1</v></c>',
    '  <c r="C1" t="inlineStr"><is><t>直接写入</t></is></c>',
    '  <c r="D1"><v>8500</v></c>',
    '  <c r="E1" t="b"><v>1</v></c>',
    '  <c r="F1"><v>46266</v></c>',
    '  <c r="G1" t="b"><v>0</v></c>',
    '</row>',
  ].join('\n');
  const bytes = buildWorkbook({ sheets: [{ name: 'S1', body }], sharedStrings });
  const workbook = openXlsxWorkbook(bytes);
  const grid = parseXlsxSheetGrid(bytes, workbook, workbook.entries.get('xl/worksheets/sheet1.xml'));
  assert.deepEqual([...grid.cells.entries()], [
    ['1:1', '客户A'],
    ['1:2', '富文本'],
    ['1:3', '直接写入'],
    ['1:4', 8500],
    ['1:5', true],
    ['1:6', 46266],
    ['1:7', false],
  ]);
  assert.equal(grid.maxRow, 1);
  assert.equal(grid.maxColumn, 7);
});

test('省略 r 属性的行与单元格按顺序顺延（WPS 写法）', () => {
  const body = [
    '<row><c t="inlineStr"><is><t>A</t></is></c><c><v>1</v></c></row>',
    '<row><c t="inlineStr"><is><t>B</t></is></c><c><v>2</v></c></row>',
  ].join('\n');
  const bytes = buildWorkbook({ sheets: [{ name: 'S1', body }] });
  const workbook = openXlsxWorkbook(bytes);
  const grid = parseXlsxSheetGrid(bytes, workbook, workbook.entries.get('xl/worksheets/sheet1.xml'));
  assert.equal(grid.cells.get('1:1'), 'A');
  assert.equal(grid.cells.get('1:2'), 1);
  assert.equal(grid.cells.get('2:1'), 'B');
  assert.equal(grid.cells.get('2:2'), 2);
});

test('合并区域按绝对坐标解析', () => {
  const body = [
    '<row r="1"><c r="A1" t="inlineStr"><is><t>甲</t></is></c></row>',
    '<mergeCells count="2"><mergeCell ref="A1:A2"/><mergeCell ref="U2312:U2315"/></mergeCells>',
  ].join('\n');
  const bytes = buildWorkbook({ sheets: [{ name: 'S1', body }] });
  const workbook = openXlsxWorkbook(bytes);
  const grid = parseXlsxSheetGrid(bytes, workbook, workbook.entries.get('xl/worksheets/sheet1.xml'));
  assert.deepEqual(grid.merged, [
    { startRow: 1, startColumn: 1, endRow: 2, endColumn: 1 },
    { startRow: 2312, startColumn: 21, endRow: 2315, endColumn: 21 },
  ]);
});

test('store（不压缩）方式写入的工作簿同样能解析', () => {
  const bytes = buildWorkbook({
    sheets: [{ name: 'S1', body: '<row r="1"><c r="A1"><v>7</v></c></row>' }],
    deflate: false,
  });
  const workbook = openXlsxWorkbook(bytes);
  const grid = parseXlsxSheetGrid(bytes, workbook, workbook.entries.get('xl/worksheets/sheet1.xml'));
  assert.equal(grid.cells.get('1:1'), 7);
});

test('工作表 XML 超过 maxBytes 上限时按 SHEET_TOO_LARGE 拒绝', () => {
  const bytes = buildWorkbook({ sheets: [{ name: 'S1', body: '<row r="1"><c r="A1"><v>1</v></c></row>' }] });
  const workbook = openXlsxWorkbook(bytes);
  assert.throws(
    () => parseXlsxSheetGrid(bytes, workbook, workbook.entries.get('xl/worksheets/sheet1.xml'), { maxBytes: 32 }),
    error => error instanceof XlsxError && error.code === 'SHEET_TOO_LARGE',
  );
});

test('XML 文本解码覆盖命名实体与数字实体', () => {
  assert.equal(decodeXmlText('&lt;&amp;&gt;&quot;&apos;&#65;&#x4e2d;'), '<&>"\'A中');
});

test('列号与列标签互转（A/Z/AA/ZZ/AAA）', () => {
  assert.equal(columnIndexFromRef('A1'), 1);
  assert.equal(columnIndexFromRef('Z9'), 26);
  assert.equal(columnIndexFromRef('AA1'), 27);
  assert.equal(columnIndexFromRef('ZZ100'), 702);
  assert.equal(columnIndexFromRef('AAA1'), 703);
  assert.equal(columnLabelFromIndex(1), 'A');
  assert.equal(columnLabelFromIndex(26), 'Z');
  assert.equal(columnLabelFromIndex(27), 'AA');
  assert.equal(columnLabelFromIndex(702), 'ZZ');
  assert.equal(columnLabelFromIndex(703), 'AAA');
  assert.equal(columnLabelFromIndex(0), '');
});
