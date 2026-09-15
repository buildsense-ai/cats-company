import assert from 'node:assert/strict';
import test from 'node:test';
import {
  cleanDocumentText, collectSheetValues, parseA1Range, planRowBands, ShimoWorkerError, validateShimoURL,
} from '../src/reader.mjs';

test('Shimo URL validation pins HTTPS and the expected file kind', () => {
  assert.equal(validateShimoURL('https://shimo.im/sheets/Abc123/?share=1#x', 'sheet').fileId, 'Abc123');
  assert.throws(() => validateShimoURL('https://example.com/sheets/Abc123/', 'sheet'), error => error instanceof ShimoWorkerError && error.code === 'INVALID_URL');
  assert.throws(() => validateShimoURL('https://shimo.im/docs/Abc123/', 'sheet'), error => error instanceof ShimoWorkerError && error.code === 'NOT_A_SHEET');
});

test('document cleanup removes duplicated table of contents and page chrome', () => {
  const raw = [
    '文档将自动保存', '注册', '登录', '分享', '目录',
    '正文标题', '1. 第一节', '', '正文标题', '', '第一段', '',
    '1. 第一节', '', '章节内容', '提交反馈以帮助我们改进',
  ].join('\n');
  assert.equal(cleanDocumentText(raw), '正文标题\n\n第一段\n\n1. 第一节\n\n章节内容');
});

const BAND_CELL_BUDGET = 4000;
const bandCells = band => {
  const { startColumn, startRow, endColumn, endRow } = parseA1Range(band.range);
  return (endColumn - startColumn + 1) * (endRow - startRow + 1);
};

test('大范围读取会拆成每块不超过 4000 个单元格的行块', () => {
  const plan = planRowBands('A1:Z1000');
  assert.equal(plan.columns, 26);
  assert.equal(plan.rows_per_band, 153);
  assert.equal(plan.truncated, false);
  assert.equal(plan.bands[0].range, 'A1:Z153');
  assert.equal(plan.bands.at(-1).end_row, 1000);
  assert.equal(plan.bands.length, 7);
  for (const band of plan.bands) assert.ok(bandCells(band) <= BAND_CELL_BUDGET);
});

test('列数不同时行块随之变化，且小范围仍只发一次请求', () => {
  assert.deepEqual(planRowBands('A1:F50').bands.map(band => band.range), ['A1:F50']);
  const wide = planRowBands('A1:AZ100');
  assert.equal(wide.columns, 52);
  assert.deepEqual(wide.bands.map(band => band.range), ['A1:AZ76', 'A77:AZ100']);
  const huge = planRowBands('A1:ZZ1000');
  for (const band of huge.bands) assert.ok(bandCells(band) <= BAND_CELL_BUDGET);
});

test('行块数量超过上限时标记为截断', () => {
  const plan = planRowBands('A1:Z100000');
  assert.equal(plan.truncated, true);
  assert.equal(plan.bands.length, 80);
});

test('非法或反向的范围会被拒绝', () => {
  for (const bad of ['A1', 'A0:Z10', '1:10', 'Z10:A1', 'A10:A1']) {
    assert.throws(() => parseA1Range(bad), error => error instanceof ShimoWorkerError && error.code === 'INVALID_RANGE');
  }
});

test('分块读取按顺序拼接，并在连续空块后停止', async () => {
  const plan = planRowBands('A1:Z1000');
  const seen = [];
  const bandsWithData = 3;
  const collected = await collectSheetValues(plan.bands, async band => {
    seen.push(band.range);
    return seen.length <= bandsWithData ? [[seen.length, 'x']] : [];
  });
  assert.deepEqual(collected.values, [[1, 'x'], [2, 'x'], [3, 'x']]);
  assert.equal(bandsWithData + 2, seen.length);
  assert.equal(collected.requests, seen.length);
});

test('数据从中间行开始时不会被误判为空表', async () => {
  const plan = planRowBands('A1:Z1000');
  const found = [];
  const collected = await collectSheetValues(plan.bands, async band => {
    found.push(band.range);
    return found.length === 2 ? [['客户A']] : [];
  });
  assert.deepEqual(collected.values, [['客户A']]);
  assert.equal(collected.requests, 4);
});
