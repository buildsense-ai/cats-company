import assert from 'node:assert/strict';
import test from 'node:test';
import {
  budgetBoundedTimeout, budgetExhaustedOr, cleanDocumentText, collectSheetValues, DEFAULT_TOTAL_READ_BUDGET_MS,
  fetchRangeValues, parseA1Range, planRowBands, PlaywrightShimoEngine, readBudgetFor, readStartedAt,
  ShimoWorkerError, UPSTREAM_MAX_CELLS_PER_REQUEST, validateShimoURL,
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

test('只有超过上游 5000 格硬上限才拒绝，4001~5000 列仍按 1 行 1 块读', () => {
  // 单行格数就超过硬上限的才注定 400：18278 列连 1 行都读不了
  assert.throws(() => planRowBands('A1:ZZZ1000'), error => error instanceof ShimoWorkerError && error.code === 'RANGE_TOO_LARGE');
  assert.throws(() => planRowBands('A1:ZZZ1'), error => error instanceof ShimoWorkerError && error.code === 'RANGE_TOO_LARGE');
  // 5001 列同样超过硬上限（拒绝条件用的是硬上限，不是 4000 的分块预算）
  assert.throws(() => planRowBands('A1:GJI1'), error => error instanceof ShimoWorkerError && error.code === 'RANGE_TOO_LARGE');

  // 4001~5000 列：单行格数 4001 <= 5000，上游会正常返回，不能拒
  const wide = planRowBands('A1:EWW1');
  assert.equal(wide.columns, 4001);
  assert.equal(wide.rows_per_band, 1);
  assert.deepEqual(wide.bands.map(band => band.range), ['A1:EWW1']);
  const wideTwoRows = planRowBands('A1:EWW2');
  assert.deepEqual(wideTwoRows.bands.map(band => band.range), ['A1:EWW1', 'A2:EWW2']);
  for (const band of wideTwoRows.bands) assert.ok(bandCells(band) <= UPSTREAM_MAX_CELLS_PER_REQUEST);

  // 刚好等于分块预算的一档仍按 1 行 1 块读
  assert.equal(planRowBands('A1:EWV1').columns, 4000);
  assert.equal(planRowBands('A1:EWV1').rows_per_band, 1);
});

test('空块不再提前停止，所有块都被读到，覆盖范围等于请求范围', async () => {
  const plan = planRowBands('A1:Z1000');
  let seen = 0;
  // 只有第一块有数据、后面全是空块：空块不能当成「后面没有数据」的证据（上游首尾都裁空白）
  const early = await collectSheetValues(plan.bands, async () => (seen++ === 0 ? [['row']] : []));
  assert.equal(early.stopped_early, false);
  assert.equal(early.timed_out, false);
  assert.deepEqual(early.values, [['row']]);
  assert.equal(early.requests, plan.bands.length);
  assert.equal(early.covered_through_row, plan.bands.at(-1).end_row);

  // 一个数据行都没读到同样读满所有块（数据可能从后面的行开始）
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

test('剩余时间预算会传给每一块请求，单块超时也会标记截断', async () => {
  const plan = planRowBands('A1:Z1000');
  const timeoutError = timeoutMs => Object.assign(
    new Error(`apiRequestContext.get: Request timed out after ${timeoutMs}ms`),
    { name: 'TimeoutError' },
  );

  // 正常情况：第一块拿到的是剩余预算（而不是无上限等待）
  const seenTimeouts = [];
  const complete = await collectSheetValues(plan.bands.slice(0, 1), async (band, options) => {
    seenTimeouts.push(options.timeoutMs);
    return [['row']];
  }, { budgetMs: 30_000, now: () => 0 });
  assert.deepEqual(seenTimeouts, [30_000]);
  assert.equal(complete.timed_out, false);

  // 唯一一块本身就超预算：不能当成成功返回
  await assert.rejects(
    () => collectSheetValues(plan.bands.slice(0, 1), async (band, options) => { throw timeoutError(options.timeoutMs); }, { budgetMs: 30_000, now: () => 0 }),
    error => error.name === 'TimeoutError',
  );

  // 已经读到数据之后某一块超时：返回已读到的部分，并标记截断
  const interrupted = await collectSheetValues(plan.bands.slice(0, 3), async (band, options) => {
    if (band.start_row > 1) throw timeoutError(options.timeoutMs);
    return [['row-1']];
  }, { budgetMs: 30_000, now: () => 0 });
  assert.deepEqual(interrupted.values, [['row-1']]);
  assert.equal(interrupted.timed_out, true);
  // 超时的那一块请求已经发出去了，也要计数（requests = 已发出的上游请求数）
  assert.equal(interrupted.requests, 2);
  assert.equal(interrupted.covered_through_row, plan.bands[0].end_row);

  // 非超时错误（例如权限、范围错误）必须原样抛出，不能降级成「截断」
  await assert.rejects(
    () => collectSheetValues(plan.bands.slice(0, 2), async (band) => {
      if (band.start_row > 1) throw new ShimoWorkerError('PERMISSION_DENIED', 'no', 403);
      return [['row-1']];
    }, { budgetMs: 30_000, now: () => 0 }),
    error => error instanceof ShimoWorkerError && error.code === 'PERMISSION_DENIED',
  );
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

test('分块读取按顺序拼接，数据全收且覆盖到最后一块', async () => {
  const plan = planRowBands('A1:Z1000');
  const seen = [];
  const bandsWithData = 3;
  const collected = await collectSheetValues(plan.bands, async band => {
    seen.push(band.range);
    return seen.length <= bandsWithData ? [[seen.length, 'x']] : [];
  });
  assert.deepEqual(collected.values, [[1, 'x'], [2, 'x'], [3, 'x']]);
  assert.equal(seen.length, plan.bands.length);
  assert.equal(collected.requests, seen.length);
  assert.equal(collected.covered_through_row, plan.bands.at(-1).end_row);
});

test('数据从中间行开始时不会被误判为空表', async () => {
  const plan = planRowBands('A1:Z1000');
  const found = [];
  const collected = await collectSheetValues(plan.bands, async band => {
    found.push(band.range);
    return found.length === 2 ? [['客户A']] : [];
  });
  assert.deepEqual(collected.values, [['客户A']]);
  assert.equal(collected.requests, plan.bands.length);
  assert.equal(collected.covered_through_row, plan.bands.at(-1).end_row);
});

test('中段整段空白、后面仍有数据时全部保留', async () => {
  const bands = Array.from({ length: 6 }, (_, index) => ({ start_row: index * 10 + 1, end_row: (index + 1) * 10 }));
  const collected = await collectSheetValues(bands, async band => (
    band.start_row === 11 || band.start_row === 21 ? [] : [[`data@${band.start_row}`]]
  ));
  assert.deepEqual(collected.values, [['data@1'], ['data@31'], ['data@41'], ['data@51']]);
  assert.equal(collected.requests, 6);
  assert.equal(collected.stopped_early, false);
  assert.equal(collected.timed_out, false);
  assert.equal(collected.covered_through_row, 60);
});

test('整次调用预算扣掉前置耗时，并受单块读取预算封顶', () => {
  const start = 1_000;
  // 前置步骤（页面加载 / 取正文）很快时，读取阶段拿满单块预算
  assert.equal(readBudgetFor(start, { totalBudgetMs: 65_000, readBudgetMs: 30_000, now: () => start + 5_000 }), 30_000);
  // 前置步骤吃掉 50s 后，读取阶段只能拿剩下的 15s
  assert.equal(readBudgetFor(start, { totalBudgetMs: 65_000, readBudgetMs: 30_000, now: () => start + 50_000 }), 15_000);
  // 前置耗时超过总预算时至少给 1ms，避免 0 或负数让请求立即被打断
  assert.equal(readBudgetFor(start, { totalBudgetMs: 65_000, readBudgetMs: 30_000, now: () => start + 70_000 }), 1);
  // 总预算必须留在 server 侧 75s 超时以内
  assert.ok(DEFAULT_TOTAL_READ_BUDGET_MS < 75_000, `total budget ${DEFAULT_TOTAL_READ_BUDGET_MS}ms 应该小于 75s`);
});

test('readStartedAt 优先采用调用方传入的请求到达时刻，缺省时才读时钟', () => {
  let clockCalls = 0;
  const clock = () => { clockCalls += 1; return 5_000; };
  // Worker 入口传入请求到达时刻：排队等待也要计入这次读取的预算。
  assert.equal(readStartedAt(1_700_000_000_000, clock), 1_700_000_000_000);
  assert.equal(clockCalls, 0);
  // 直接调用 reader（没有入口时间）时才回退到当前时刻。
  assert.equal(readStartedAt(undefined, clock), 5_000);
  assert.equal(readStartedAt(Number.NaN, clock), 5_000);
  assert.equal(clockCalls, 2);
});

test('budgetBoundedTimeout 用剩余预算封顶固定超时', () => {
  assert.equal(budgetBoundedTimeout(45_000, 60_000), 45_000);
  assert.equal(budgetBoundedTimeout(45_000, 5_000), 5_000);
  assert.equal(budgetBoundedTimeout(45_000, 0), 1);
  assert.equal(budgetBoundedTimeout(45_000, -10), 1);
});

test('页面加载步骤按剩余总预算收口，预算不足时提前按「忙」拒绝', async () => {
  const recorded = [];
  const fakePage = {
    async goto(_, { timeout }) { recorded.push(['goto', timeout]); },
    async waitForTimeout(ms) { recorded.push(['wait', ms]); },
    locator() {
      return { async innerText({ timeout }) { recorded.push(['innerText', timeout]); throw new Error('stop-after-page-load'); } };
    },
  };
  const clock = 1_000_000;
  const engine = new PlaywrightShimoEngine({ executablePath: 'test-browser', now: () => clock });

  engine.withContext = async (state, callback) => callback({ newPage: async () => fakePage });

  // 排队 + 页面加载已吃掉 61s：三个固定超时都必须退到剩余预算以内，而不是各按 45s/2.5s/15s 跑。
  await assert.rejects(
    () => engine.readSheet({}, 'https://shimo.im/sheets/Abc123/', '项目表', 'A1:C20', { budgetStartedAt: clock - 61_000 }),
    /stop-after-page-load/,
  );
  const gotoTimeout = recorded.find(entry => entry[0] === 'goto')[1];
  const renderWait = recorded.find(entry => entry[0] === 'wait')[1];
  const innerTextTimeout = recorded.find(entry => entry[0] === 'innerText')[1];
  assert.ok(gotoTimeout <= 4_000 && gotoTimeout > 0, `goto timeout ${gotoTimeout} 应被剩余预算封顶`);
  assert.ok(renderWait <= 4_000 && renderWait >= 0, `render wait ${renderWait} 应被剩余预算封顶`);
  assert.ok(innerTextTimeout <= 4_000 && innerTextTimeout > 0, `innerText timeout ${innerTextTimeout} 应被剩余预算封顶`);

  // 预算已经用光：不再启动浏览器，直接给出可重试的错误。
  const before = recorded.length;
  await assert.rejects(
    () => engine.readSheet({}, 'https://shimo.im/sheets/Abc123/', '项目表', 'A1:C20', { budgetStartedAt: clock - 70_000 }),
    error => error instanceof ShimoWorkerError && error.code === 'READ_BUDGET_EXHAUSTED' && error.status === 503,
  );
  assert.equal(recorded.length, before);
});

test('页面加载超时只有在预算耗尽时才归类为可重试错误', async () => {
  let clock = 1_000_000;
  const startedAt = clock - 61_000;
  const engine = new PlaywrightShimoEngine({ executablePath: 'test-browser', now: () => clock });
  engine.withContext = async (state, callback) => callback({ newPage: async () => ({
    async goto() { clock += 5_000; throw new Error('page load timed out'); },
    async waitForTimeout() {},
    locator() { return { async innerText() { throw new Error('unreachable'); } }; },
  }) });
  // 页面加载把最后的预算也用光：归类成可重试的 503，而不是 502 WORKER_ERROR。
  await assert.rejects(
    () => engine.readSheet({}, 'https://shimo.im/sheets/Abc123/', '项目表', 'A1:C20', { budgetStartedAt: startedAt }),
    error => error instanceof ShimoWorkerError && error.code === 'READ_BUDGET_EXHAUSTED' && error.status === 503,
  );

  // 预算还够时页面加载失败：保持原样，说明是上游页面问题而不是「忙」。
  clock = 1_000_000;
  const withTimeLeft = new PlaywrightShimoEngine({ executablePath: 'test-browser', now: () => clock });
  withTimeLeft.withContext = async (state, callback) => callback({ newPage: async () => ({
    async goto() { throw new Error('page load timed out'); },
    async waitForTimeout() {},
    locator() { return { async innerText() { return ''; } }; },
  }) });
  await assert.rejects(
    () => withTimeLeft.readSheet({}, 'https://shimo.im/sheets/Abc123/', '项目表', 'A1:C20', { budgetStartedAt: clock - 10_000 }),
    /page load timed out/,
  );
  assert.equal(budgetExhaustedOr(new Error('x'), () => 5_000).message, 'x');
});
