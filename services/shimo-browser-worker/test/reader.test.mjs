import assert from 'node:assert/strict';
import test from 'node:test';
import {
  budgetBoundedTimeout, budgetExhaustedOr, cleanDocumentText, collectSheetValues, DEFAULT_TOTAL_READ_BUDGET_MS,
  downloadUploadedFileBytes, fetchRangeValues, htmlShellVerdict, loadUploadedWorkbook, looksLikeAccessDeniedPage,
  looksLikeLoginPromptPage, looksLikeUnreadableDocument, parseA1Range, planRowBands,
  PlaywrightShimoEngine, readBudgetFor, readStartedAt, ShimoWorkerError, sliceXlsxGrid, UNREADABLE_DOCUMENT_MESSAGE,
  upstreamExcerpt, UPSTREAM_MAX_CELLS_PER_REQUEST, validateShimoURL,
} from '../src/reader.mjs';

test('Shimo URL validation pins HTTPS and the expected file kind', () => {
  assert.equal(validateShimoURL('https://shimo.im/sheets/Abc123/?share=1#x', 'sheet').fileId, 'Abc123');
  assert.throws(() => validateShimoURL('https://example.com/sheets/Abc123/', 'sheet'), error => error instanceof ShimoWorkerError && error.code === 'INVALID_URL');
  assert.throws(() => validateShimoURL('https://shimo.im/docs/Abc123/', 'sheet'), error => error instanceof ShimoWorkerError && error.code === 'NOT_A_SHEET');
});

test('上传到石墨的 Excel（/file/）与原生表格（/sheets/）在同一入口按 kind 分流', () => {
  // 石墨里这两种链接在用户看来都是「表格」：/sheets/ 是原生表格（有单元格接口），
  // /file/ 是用户上传的 .xlsx（只能整份下载后本地解析）。validateShimoURL 必须把
  // 两者都放行并带上 kind，读取层才能各走各的路径。
  const uploaded = validateShimoURL('https://shimo.im/file/L9kBBwK8jlsVp8kK/?from=link#frag', 'sheet');
  assert.equal(uploaded.kind, 'uploaded_file');
  assert.equal(uploaded.fileId, 'L9kBBwK8jlsVp8kK');
  assert.equal(uploaded.url, 'https://shimo.im/file/L9kBBwK8jlsVp8kK/?from=link');
  const native = validateShimoURL('https://shimo.im/sheets/Abc123/', 'sheet');
  assert.equal(native.kind, 'sheet');
  assert.throws(() => validateShimoURL('https://shimo.im/file/Abc123/', 'document'), error => error instanceof ShimoWorkerError && error.code === 'NOT_A_DOCUMENT');
});

test('sliceXlsxGrid 按请求窗口裁剪尾部空行空列，并保留合并区域的绝对坐标', () => {
  const grid = {
    cells: new Map([['1:1', '客户'], ['1:2', 1000], ['4:3', '尾']]),
    merged: [
      { startRow: 1, startColumn: 1, endRow: 2, endColumn: 1 },
      { startRow: 99, startColumn: 1, endRow: 100, endColumn: 1 },
    ],
  };
  const sliced = sliceXlsxGrid(grid, 'A1:D10');
  // 返回的是矩形矩阵（每行列数一致），与原生表格路径的 values 形态相同：
  // 窗口内最靠右的有值列决定列数，窗口内最靠下的有值行决定行数。
  assert.deepEqual(sliced.values, [['客户', 1000, ''], ['', '', ''], ['', '', ''], ['', '', '尾']]);
  // 与窗口相交的合并区域保留原表坐标；窗口外的不返回。
  assert.deepEqual(sliced.merged_ranges, ['A1:A2']);
});

test('sliceXlsxGrid 遇到空窗口时返回空 values', () => {
  const sliced = sliceXlsxGrid({ cells: new Map(), merged: [] }, 'A5:C9');
  assert.deepEqual(sliced.values, []);
  assert.deepEqual(sliced.merged_ranges, []);
});

test('下载返回 HTML 外壳时区分登录失效与无权限，二进制内容不误判', () => {
  assert.equal(htmlShellVerdict('<html><body>请先登录</body></html>', 'text/html; charset=utf-8'), 'login');
  assert.equal(htmlShellVerdict('<!doctype html><html>你没有权限，请申请访问权限</html>', 'text/html'), 'denied');
  assert.equal(htmlShellVerdict('<!doctype html><html>其他错误页面</html>', 'text/html'), 'denied');
  assert.equal(htmlShellVerdict('PK\u0003\u0004binary', 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet'), null);
});

test('上传件不是工作簿时翻译成可解释的 UNSUPPORTED_FILE_TYPE', () => {
  assert.throws(
    () => loadUploadedWorkbook(Buffer.from('这不是 Excel 文件')),
    error => error instanceof ShimoWorkerError && error.code === 'UNSUPPORTED_FILE_TYPE',
  );
});

function fakeDownloadPage(result) {
  return { evaluate: async () => result };
}

test('下载上传件：401 归为登录失效、403/404 归为看不见文档、超限归为 FILE_TOO_LARGE', async () => {
  await assert.rejects(
    downloadUploadedFileBytes(fakeDownloadPage({ ok: false, status: 401 }), 'Abc123'),
    error => error instanceof ShimoWorkerError && error.code === 'LOGIN_REQUIRED',
  );
  await assert.rejects(
    downloadUploadedFileBytes(fakeDownloadPage({ ok: false, status: 404 }), 'Abc123'),
    error => error instanceof ShimoWorkerError && error.code === 'DOCUMENT_NOT_ACCESSIBLE',
  );
  await assert.rejects(
    downloadUploadedFileBytes(fakeDownloadPage({ ok: false, tooLarge: true, size: 40 * 1024 * 1024 }), 'Abc123'),
    error => error instanceof ShimoWorkerError && error.code === 'FILE_TOO_LARGE' && error.status === 413,
  );
  await assert.rejects(
    downloadUploadedFileBytes(fakeDownloadPage({ ok: false, status: 0, error: 'socket hang up' }), 'Abc123'),
    error => error instanceof ShimoWorkerError && error.code === 'SHIMO_API_ERROR',
  );
});

test('下载上传件成功后返回原始字节与响应头信息', async () => {
  const payload = Buffer.from('PK\u0003\u0004fake-xlsx');
  const download = await downloadUploadedFileBytes(fakeDownloadPage({
    ok: true, base64: payload.toString('base64'), size: payload.length,
    contentType: 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet', head: 'PK\u0003\u0004',
  }), 'Abc123');
  assert.equal(download.bytes.toString(), 'PK\u0003\u0004fake-xlsx');
  assert.equal(download.contentType, 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet');
});

test('listSheets 对 /file/ 链接走上传件路径，不打开原生表格页面', async () => {
  const engine = new PlaywrightShimoEngine();
  let calls = 0;
  engine.listUploadedFileSheets = async (storageState, url, fileId) => {
    calls += 1;
    assert.equal(fileId, 'Abc123');
    return { sheets: [{ name: 'S1', index: 0 }], source_kind: 'uploaded_excel' };
  };
  const result = await engine.listSheets({ cookies: [] }, 'https://shimo.im/file/Abc123/');
  assert.equal(calls, 1);
  assert.equal(result.source_kind, 'uploaded_excel');
});

test('readSheet 对 /file/ 链接走上传件路径，工作表名与范围原样传入', async () => {
  const engine = new PlaywrightShimoEngine();
  let captured = null;
  engine.readUploadedFileSheet = async (storageState, url, fileId, sheetName, cellRange, meta) => {
    captured = { url, fileId, sheetName, cellRange, hasStartedAt: Number.isFinite(meta?.startedAt) };
    return { values: [['ok']], source_kind: 'uploaded_excel' };
  };
  const result = await engine.readSheet({}, 'https://shimo.im/file/Abc123/', '开票', 'A1:Z10', { budgetStartedAt: Date.now() - 5 });
  assert.equal(captured.fileId, 'Abc123');
  assert.equal(captured.sheetName, '开票');
  assert.equal(captured.cellRange, 'A1:Z10');
  assert.equal(captured.hasStartedAt, true);
  assert.deepEqual(result, { values: [['ok']], source_kind: 'uploaded_excel' });
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
      if (band.start_row > 1) throw new ShimoWorkerError('DOCUMENT_NOT_ACCESSIBLE', 'no', 403);
      return [['row-1']];
    }, { budgetMs: 30_000, now: () => 0 }),
    error => error instanceof ShimoWorkerError && error.code === 'DOCUMENT_NOT_ACCESSIBLE',
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
  await expectCode(403, '{}', 'DOCUMENT_NOT_ACCESSIBLE');
  await expectCode(400, '{"error":"限制最多获取 5000 个单元格的数据"}', 'RANGE_TOO_LARGE');
  await expectCode(400, '{"error":"请求参数错误"}', 'SHIMO_API_ERROR');
  await expectCode(200, '{"values":"not-a-matrix"}', 'UPSTREAM_CHANGED');
});

const contextReturning = (status, body) => ({
  request: {
    get: async () => ({
      status: () => status,
      ok: () => status >= 200 && status < 300,
      text: async () => body,
      json: async () => JSON.parse(body),
    }),
  },
});

test('账号看不到的文档要报成 DOCUMENT_NOT_ACCESSIBLE，而不是笼统的接口错误', async () => {
  // 2026-09-15 生产实测：同事的石墨账号没有该表格权限时，石墨 SDK 返回的就是这条通用上游错误。
  // 旧实现把它当成「接口 HTTP 500」上报，于是 David 只能去猜「文档类型不支持 / 工作表名写错」。
  const upstream = '{"code":3,"statusText":"GetFileByProviderID fail, err: rpc error: code = InvalidArgument'
    + ' desc = get remote file by provider id failed, reason: sdk.provider.v1.GET_REMOTE_FILE_INVALID_ARGUMENT"}';
  await assert.rejects(
    () => fetchRangeValues(contextReturning(500, upstream), 'FileId', '工作表1', 'A1:Z10'),
    error => error instanceof ShimoWorkerError
      && error.code === 'DOCUMENT_NOT_ACCESSIBLE'
      && error.status === 403
      && error.message === UNREADABLE_DOCUMENT_MESSAGE,
  );
  // 石墨不区分「没有权限」和「文档不存在」，所以文案必须同时说清两种可能，并要求换账号而不是重试。
  assert.match(UNREADABLE_DOCUMENT_MESSAGE, /没有查看权限/);
  assert.match(UNREADABLE_DOCUMENT_MESSAGE, /已被删除|链接已失效/);
  assert.match(UNREADABLE_DOCUMENT_MESSAGE, /重试无效/);
  // 其他上游失败仍按可重试的接口错误上报，但带上游摘录，方便定位。
  await assert.rejects(
    () => fetchRangeValues(contextReturning(502, '<html>bad gateway</html>'), 'FileId', '工作表1', 'A1:Z10'),
    error => error instanceof ShimoWorkerError
      && error.code === 'SHIMO_API_ERROR'
      && error.message.includes('bad gateway'),
  );
});

test('页面文案判定只认错误外壳自己的文案，正常表格里的短语不会命中', () => {
  // 生产实测：账号看不到文档时，石墨给的是英文 404 外壳。
  assert.equal(looksLikeAccessDeniedPage('404 not found', '404\nThe page you visited does not exist\nBack to Desktop'), true);
  assert.equal(looksLikeAccessDeniedPage('无权限', '申请访问权限'), true);
  assert.equal(looksLikeAccessDeniedPage('项目表', '日期 业务员 客户 建库类型'), false);
  // 整页 innerText 里也包含单元格内容：这些普通业务短语不能单独当作证据。
  assert.equal(looksLikeAccessDeniedPage('报错记录', '2026-09-01 config file does not exist'), false);
  assert.equal(looksLikeAccessDeniedPage('项目表', '文档不存在 已被删除 链接已失效'), false);
  assert.equal(looksLikeAccessDeniedPage('', ''), false);
  // 2026-09-15 生产实测：登录会话失效时标题是「No permission」、正文是英文登录提示。
  // 只认中文提示，这一页就会被 /no permission/ 误判成「账号看不到这份文档」。
  assert.equal(looksLikeLoginPromptPage("You haven't logged in yet\nPlease sign in before trying to access\nSign in"), true);
  assert.equal(looksLikeLoginPromptPage('您还没有登录 请登录后尝试访问'), true);
  assert.equal(looksLikeLoginPromptPage('No permission\nRequest access'), false);
  assert.equal(looksLikeLoginPromptPage('日期 业务员 客户 建库类型'), false);
  assert.equal(looksLikeLoginPromptPage(''), false);
  const upstream = 'GetFileByProviderID fail: GET_REMOTE_FILE_INVALID_ARGUMENT';
  assert.equal(looksLikeUnreadableDocument(500, upstream), true);
  assert.equal(looksLikeUnreadableDocument(400, upstream), false);
  assert.equal(looksLikeUnreadableDocument(500, ''), false);
  assert.equal(upstreamExcerpt('x'.repeat(400)).length, 161);
});

const fakeSheetPage = ({ title, bodyText, tabs = [] }) => ({
  async goto() {},
  async waitForTimeout() {},
  async title() { return title; },
  locator(selector) {
    if (selector === 'body') return { async innerText() { return bodyText; } };
    return {
      first() {
        return { async waitFor() { if (tabs.length === 0) throw new Error('no sheet tab'); } };
      },
      async evaluateAll() {
        return tabs.map((name, index) => ({ name, index, active: index === 0 }));
      },
    };
  },
});

const engineFor = (page, request) => {
  const engine = new PlaywrightShimoEngine({ executablePath: 'test-browser' });
  engine.withContext = async (state, callback) => callback({
    newPage: async () => page,
    request: request || contextReturning(500, '').request,
  });
  return engine;
};

test('页面上有表格标签时，正文里的普通文案不会让读取变成「看不到文档」', async () => {
  const url = 'https://shimo.im/sheets/Abc123/';
  // 标题恰好叫「404 not found」的正常表格：有表格标签就不该被判成无权限页。
  const listed = await engineFor(fakeSheetPage({
    title: '404 not found',
    bodyText: '日期 说明 2026-09-01 config file does not exist',
    tabs: ['工作表1', '报错记录'],
  })).listSheets({}, url);
  assert.deepEqual(listed.sheets.map(item => item.name), ['工作表1', '报错记录']);

  // readSheet 同理：单元格里写着「does not exist」，读取必须照常进行。
  const values = JSON.stringify({ values: [['config file does not exist']] });
  const read = await engineFor(
    fakeSheetPage({ title: '报错记录', bodyText: '日期 说明\n2026-09-01 config file does not exist', tabs: ['工作表1'] }),
    { get: async () => ({ status: () => 200, ok: () => true, text: async () => values, json: async () => JSON.parse(values) }) },
  ).readSheet({}, url, '工作表1', 'A1:C10');
  assert.deepEqual(read.values, [['config file does not exist']]);
});

test('登录会话失效的页面报 LOGIN_REQUIRED，不会被当成「账号看不到这份文档」', async () => {
  const url = 'https://shimo.im/sheets/Abc123/';
  // 真实形态：会话过期后打开表格，石墨给的是标题「No permission」+ 英文登录提示。
  const expired = fakeSheetPage({
    title: 'No permission',
    bodyText: "You haven't logged in yet\nPlease sign in before trying to access\nSign in",
  });
  await assert.rejects(
    () => engineFor(expired).listSheets({}, url),
    error => error instanceof ShimoWorkerError && error.code === 'LOGIN_REQUIRED' && error.status === 409,
  );
  await assert.rejects(
    () => engineFor(expired).readSheet({}, url, '工作表1', 'A1:C10'),
    error => error instanceof ShimoWorkerError && error.code === 'LOGIN_REQUIRED' && error.status === 409,
  );
  // 同样的标题、但正文没有登录提示：仍然是「账号看不到这份文档」，不能被新规则吞掉。
  const forbidden = fakeSheetPage({ title: 'No permission', bodyText: 'No permission\nRequest access' });
  await assert.rejects(
    () => engineFor(forbidden).listSheets({}, url),
    error => error instanceof ShimoWorkerError && error.code === 'DOCUMENT_NOT_ACCESSIBLE' && error.status === 403,
  );
});

test('没有表格标签时才用页面文案判定：错误外壳报看不到文档，普通页面报 PAGE_UNEXPECTED', async () => {
  const url = 'https://shimo.im/sheets/Abc123/';
  const shell = fakeSheetPage({ title: '404 not found', bodyText: '404 The page you visited does not exist Back to Desktop' });
  // 同事这次的真实形态：连接成功、但账号看不到这份文档，石墨给的是英文 404 外壳。
  await assert.rejects(
    () => engineFor(shell).listSheets({}, url),
    error => error instanceof ShimoWorkerError && error.code === 'DOCUMENT_NOT_ACCESSIBLE' && error.status === 403,
  );
  // readSheet 在读之前就该停下，而不是拿一句笼统的接口错误去让调用方猜。
  await assert.rejects(
    () => engineFor(shell).readSheet({}, url, '工作表1', 'A1:C10'),
    error => error instanceof ShimoWorkerError && error.code === 'DOCUMENT_NOT_ACCESSIBLE',
  );
  // 没有表格标签、页面文案也不像错误外壳：报可诊断的页面标题，而不是「看不到文档」。
  const unexpected = await engineFor(fakeSheetPage({ title: '石墨文档', bodyText: '正在加载' }))
    .listSheets({}, url).then(() => null, error => error);
  assert.equal(unexpected?.code, 'PAGE_UNEXPECTED');
  assert.match(unexpected.message, /石墨文档/);
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
