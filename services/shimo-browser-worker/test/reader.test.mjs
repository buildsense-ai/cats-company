import assert from 'node:assert/strict';
import test from 'node:test';
import {
  cleanDocumentText, collectSheetValues, fetchRangeValues, parseA1Range, planRowBands, ShimoWorkerError, validateShimoURL,
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
  assert.equal(plan.bands.length, 40);
});

test('列数超过单块预算时直接拒绝，而不是发一堆注定 400 的请求', () => {
  assert.throws(() => planRowBands('A1:ZZZ1000'), error => error instanceof ShimoWorkerError && error.code === 'RANGE_TOO_LARGE');
  // 单行也超预算时同样拒绝：整行就是 18278 格，远超 5000 硬上限
  assert.throws(() => planRowBands('A1:ZZZ1'), error => error instanceof ShimoWorkerError && error.code === 'RANGE_TOO_LARGE');
  // 列数刚好等于预算时仍可按 1 行 1 块读
  assert.equal(planRowBands('A1:EWV1').columns, 4000);
  assert.throws(() => planRowBands('A1:EWW1'), error => error instanceof ShimoWorkerError && error.code === 'RANGE_TOO_LARGE');
});

test('提前停止与超时都会对外暴露，不会静默丢数据', async () => {
  const plan = planRowBands('A1:Z1000');
  let seen = 0;
  const early = await collectSheetValues(plan.bands, async () => (seen++ === 0 ? [['row']] : []));
  assert.equal(early.stopped_early, true);
  assert.equal(early.timed_out, false);
  assert.deepEqual(early.values, [['row']]);

  // 一个数据行都没读到时不提前结束（数据可能从后面的行开始）
  const empty = await collectSheetValues(plan.bands, async () => []);
  assert.equal(empty.stopped_early, false);
  assert.equal(empty.requests, plan.bands.length);
  assert.deepEqual(empty.values, []);

  let clock = 0;
  const timed = await collectSheetValues(plan.bands, async () => { clock += 20_000; return [['row']]; }, {
    budgetMs: 30_000, now: () => clock,
  });
  assert.equal(timed.timed_out, true);
  assert.equal(timed.requests, 2);
  assert.equal(timed.covered_through_row, plan.bands[1].end_row);

  const complete = await collectSheetValues(plan.bands.slice(0, 1), async () => [['row']]);
  assert.equal(complete.stopped_early, false);
  assert.equal(complete.timed_out, false);
  assert.equal(complete.covered_through_row, plan.bands[0].end_row);
});

test('中间出现空块时，后面的行仍按顺序拼接', async () => {
  const bands = [
    { start_row: 1, end_row: 10 },
    { start_row: 11, end_row: 20 },
    { start_row: 21, end_row: 30 },
  ];
  const collected = await collectSheetValues(bands, async band => {
    if (band.start_row === 11) return [];
    return [[`row-${band.start_row}`], [`row-${band.end_row}`]];
  });
  assert.deepEqual(collected.values, [['row-1'], ['row-10'], ['row-21'], ['row-30']]);
  assert.equal(collected.requests, 3);
  assert.equal(collected.stopped_early, false);
  assert.equal(collected.covered_through_row, 30);
});

test('石墨接口的错误码会映射成明确的 worker 错误', async () => {
  const contextFor = (status, body) => ({
    request: {
      get: async () => ({
        status: () => status,
        ok: () => status >= 200 && status < 300,
        text: async () => body,
        json: async () => JSON.parse(body),
      }),
    },
  });
  const call = context => fetchRangeValues(context, 'FileId', '工作表1', 'A1:Z10');

  assert.deepEqual(await call(contextFor(200, '{"values":[["a"],["b"]]}')), [['a'], ['b']]);
  const expectCode = async (status, body, code) => {
    await assert.rejects(() => call(contextFor(status, body)), error => error instanceof ShimoWorkerError && error.code === code);
  };
  await expectCode(401, '{}', 'LOGIN_REQUIRED');
  await expectCode(403, '{}', 'PERMISSION_DENIED');
  await expectCode(400, '{"error":"限制最多获取 5000 个单元格的数据"}', 'RANGE_TOO_LARGE');
  await expectCode(400, '{"error":"请求参数错误"}', 'SHIMO_API_ERROR');
  await expectCode(200, '{"values":"not-a-matrix"}', 'UPSTREAM_CHANGED');
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
