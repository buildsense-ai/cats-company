import fs from 'node:fs';
import { chromium } from 'playwright-core';

export class ShimoWorkerError extends Error {
  constructor(code, message, status = 502, details = {}) {
    super(message);
    this.name = 'ShimoWorkerError';
    this.code = code;
    this.status = status;
    this.details = details;
  }
}

export function validateShimoURL(input, expected) {
  let url;
  try { url = new URL(String(input || '').trim()); } catch {
    throw new ShimoWorkerError('INVALID_URL', '请提供完整的石墨 HTTPS 链接。', 400);
  }
  if (url.protocol !== 'https:' || !['shimo.im', 'www.shimo.im'].includes(url.hostname)) {
    throw new ShimoWorkerError('INVALID_URL', '只允许读取 shimo.im 的 HTTPS 链接。', 400);
  }
  const sheetMatch = url.pathname.match(/^\/(?:sheets?|tables?)\/([A-Za-z0-9]+)/);
  const documentMatch = url.pathname.match(/^\/(?:docs?|docx)\/([A-Za-z0-9]+)/);
  if (expected === 'sheet' && !sheetMatch) throw new ShimoWorkerError('NOT_A_SHEET', '该链接不是石墨表格。', 400);
  if (expected === 'document' && !documentMatch) throw new ShimoWorkerError('NOT_A_DOCUMENT', '该链接不是石墨文档。', 400);
  url.hash = '';
  return { url: url.toString(), fileId: sheetMatch?.[1] || documentMatch?.[1] || '' };
}

const RANGE_PATTERN = /^([A-Z]{1,3})([1-9][0-9]{0,6}):([A-Z]{1,3})([1-9][0-9]{0,6})$/;
// 石墨表格接口单次最多返回 5000 个单元格（实测越界返回 HTTP 400：限制最多获取 5000 个单元格的数据）。
// 这里按「请求范围」预扣预算（含空白行也算），留出安全余量后自动分块。
export const UPSTREAM_MAX_CELLS_PER_REQUEST = 5000;
// 预算由硬上限推导，避免两个常量各自硬编码后漂移：5000 * 0.8 = 4000。
export const CHUNK_CELL_BUDGET = Math.floor(UPSTREAM_MAX_CELLS_PER_REQUEST * 0.8);
// 40 块 x 每块 <=4000 格；按实测（同一上下文内每块 100~350ms）总耗时约 5~15s，
// 远低于 server 侧 75s 的整次调用超时，同时避免 A1:Z1000000 这类超长范围把并发槽占满。
export const MAX_BANDS = 40;
// 单块读取阶段的兜底时间预算：即使块数没到上限，也要保证读取阶段不会把整次调用占满。
export const DEFAULT_READ_BUDGET_MS = 30_000;
// 整次调用预算：server 侧 HTTP 客户端 75s 超时（server/shimo_worker_backend.go），
// 这里按 65s 收口。计时从 Worker 收到请求开始（含并发限流器里的排队等待），页面加载
// （goto 最多 45s、等渲染 2.5s、取正文最多 15s）按剩余预算收口，剩下的留给响应序列化
// 和网络往返，保证「排队 + 前置 + 读取」不越过 server 超时。
export const DEFAULT_TOTAL_READ_BUDGET_MS = 65_000;
// 页面加载阶段的最小可用预算：剩余不足这个数时不再启动浏览器，直接按「忙、可重试」上报。
export const MIN_PAGE_BUDGET_MS = 3_000;

function columnIndex(label) {
  let index = 0;
  for (const character of String(label)) index = index * 26 + (character.charCodeAt(0) - 64);
  return index;
}

export function columnLabel(index) {
  let value = Number(index);
  let label = '';
  while (value > 0) {
    const remainder = (value - 1) % 26;
    label = String.fromCharCode(65 + remainder) + label;
    value = Math.floor((value - 1) / 26);
  }
  return label;
}

export function parseA1Range(cellRange) {
  const match = RANGE_PATTERN.exec(String(cellRange || '').trim().toUpperCase());
  if (!match) throw new ShimoWorkerError('INVALID_RANGE', '单元格范围应类似 A1:Z1000。', 400);
  const parsed = {
    startColumn: columnIndex(match[1]), startRow: Number(match[2]),
    endColumn: columnIndex(match[3]), endRow: Number(match[4]),
  };
  if (parsed.startColumn > parsed.endColumn || parsed.startRow > parsed.endRow) {
    throw new ShimoWorkerError('INVALID_RANGE', '单元格范围的起点应位于终点之前。', 400);
  }
  return parsed;
}

export function planRowBands(cellRange, {
  cellBudget = CHUNK_CELL_BUDGET,
  maxBands = MAX_BANDS,
  maxCellsPerRequest = UPSTREAM_MAX_CELLS_PER_REQUEST,
} = {}) {
  const { startColumn, startRow, endColumn, endRow } = parseA1Range(cellRange);
  const columns = endColumn - startColumn + 1;
  // 拒绝条件用上游硬上限（5000），不是分块预算（4000）：4001~5000 列的单行请求只有几千格，
  // 上游会正常返回，只有「单行格数也超过硬上限」时才是注定 400 的请求。
  if (columns > maxCellsPerRequest) {
    throw new ShimoWorkerError('RANGE_TOO_LARGE', `该范围包含 ${columns} 列，单次最多只能读取 ${maxCellsPerRequest} 个单元格，请按列拆分读取。`, 400);
  }
  // 列数超过分块预算时按「一行一块」读，每块格数 = 列数（仍然 <= 硬上限）。
  const rowsPerBand = Math.max(1, Math.floor(cellBudget / columns));
  const bands = [];
  let row = startRow;
  let truncated = false;
  while (row <= endRow) {
    if (bands.length >= maxBands) { truncated = true; break; }
    const lastRow = Math.min(row + rowsPerBand - 1, endRow);
    bands.push({
      range: columnLabel(startColumn) + row + ':' + columnLabel(endColumn) + lastRow,
      start_row: row,
      end_row: lastRow,
    });
    row = lastRow + 1;
  }
  return { bands, columns, rows: endRow - startRow + 1, rows_per_band: rowsPerBand, truncated };
}

// 逐块读取并拼成一个二维数组。上游会裁掉每块首尾的空白行，所以「空块」不能当成
// 「后面没有数据」的证据：表格中间可能存在整段空白、之后又有数据，一旦按连续空块提前
// 停止，那段数据就被静默丢掉了。因此这里读完计划里的所有块，覆盖范围就等于请求范围；
// 只有块数上限（plan.truncated）或时间预算才会让读取提前结束，并显式标记为截断。
function isTimeoutError(error) {
  return error?.name === 'TimeoutError' || /timed out/i.test(String(error?.message || ''));
}

export async function collectSheetValues(bands, fetchBand, {
  budgetMs = DEFAULT_READ_BUDGET_MS,
  now = () => Date.now(),
} = {}) {
  const startedAt = now();
  const values = [];
  let requests = 0;
  let timedOut = false;
  let coveredThroughRow = bands.length ? bands[0].start_row - 1 : 0;
  for (const band of bands) {
    const remainingMs = budgetMs - (now() - startedAt);
    if (requests > 0 && remainingMs <= 0) { timedOut = true; break; }
    // requests 记的是「已经发出去的上游请求数」：这一块即便超时/报错也已经发过了。
    requests += 1;
    let bandValues;
    try {
      // 剩余预算同时交给这一块请求本身：预算耗尽时中断请求，而不是无限等下去。
      bandValues = await fetchBand(band, { timeoutMs: Math.max(1, remainingMs) });
    } catch (error) {
      // 单块超时且此前已经读到数据：返回已读到的部分并标记截断；
      // 一行都没读到就按失败抛出，避免把「超时」当成「空表」。
      if (!isTimeoutError(error) || !values.length) throw error;
      timedOut = true;
      break;
    }
    coveredThroughRow = band.end_row;
    for (const row of bandValues) values.push(row);
  }
  // stopped_early 继续对外返回（连接器、skill 快照和文档都在用这个字段），
  // 但读取不再提前停止，所以它现在是常量 false。
  return { values, requests, covered_through_row: coveredThroughRow, stopped_early: false, timed_out: timedOut };
}

// 整次调用的起点：Worker 入口传入请求到达时刻，让排队等待也计入同一次读取预算；
// 没有传入时回退到当前时刻（直接调用 reader 的场景）。
export function readStartedAt(budgetStartedAt, now = Date.now()) {
  return Number.isFinite(budgetStartedAt) ? budgetStartedAt : now();
}

// 固定超时也要服从总预算：剩余时间不足时改用剩余值，保证「排队 + 页面加载 + 读取」不越过上界。
export function budgetBoundedTimeout(defaultTimeoutMs, remainingMs) {
  return Math.max(1, Math.min(defaultTimeoutMs, Math.floor(remainingMs)));
}

// 页面加载阶段超时：若超时发生时预算已经耗尽，说明是「等太久、可以重试」，
// 归类成 READ_BUDGET_EXHAUSTED（503），而不是让 Playwright 的 TimeoutError 变成 502。
export function budgetExhaustedOr(error, budgetLeft) {
  if (error instanceof ShimoWorkerError) return error;
  return budgetLeft() <= 0
    ? new ShimoWorkerError('READ_BUDGET_EXHAUSTED', '这次读取等待时间过长，预算已用尽，请稍后重试。', 503)
    : error;
}

// 整次调用的剩余预算：排队等待、页面加载等前置步骤吃掉的时间都要从读取阶段扣掉，
// 这样「排队耗时 + 前置耗时 + 读取耗时」始终受总预算约束。
export function readBudgetFor(startedAt, {
  totalBudgetMs = DEFAULT_TOTAL_READ_BUDGET_MS,
  readBudgetMs = DEFAULT_READ_BUDGET_MS,
  now = () => Date.now(),
} = {}) {
  const remainingTotalMs = totalBudgetMs - (now() - startedAt);
  return Math.max(1, Math.min(readBudgetMs, remainingTotalMs));
}

export async function fetchRangeValues(context, fileId, sheetName, cellRange, { timeoutMs } = {}) {
  const apiRange = sheetName + '!' + cellRange;
  const endpoint = 'https://shimo.im/sdk/v2/api/files/' + fileId + '/sheets/values?range=' + encodeURIComponent(apiRange);
  const requestOptions = Number.isFinite(timeoutMs) && timeoutMs > 0 ? { timeout: timeoutMs } : undefined;
  const response = await context.request.get(endpoint, requestOptions);
  if (response.status() === 401) throw new ShimoWorkerError('LOGIN_REQUIRED', '石墨登录会话已失效。', 409);
  if (response.status() === 403) throw new ShimoWorkerError('PERMISSION_DENIED', '当前石墨账号没有读取权限。', 403);
  if (!response.ok()) {
    const body = await response.text().catch(() => '');
    if (response.status() === 400 && body.includes('限制最多获取')) {
      throw new ShimoWorkerError('RANGE_TOO_LARGE', '石墨表格单次最多读取 5000 个单元格，请缩小范围或分块读取。', 400);
    }
    throw new ShimoWorkerError('SHIMO_API_ERROR', '石墨表格接口返回 HTTP ' + response.status() + '。', 502);
  }
  const payload = await response.json();
  if (!Array.isArray(payload.values) || payload.values.some(row => !Array.isArray(row))) {
    throw new ShimoWorkerError('UPSTREAM_CHANGED', '石墨表格接口没有返回二维数组。', 502);
  }
  return payload.values;
}

export function browserExecutable() {
  const configured = String(process.env.CHROMIUM_EXECUTABLE_PATH || '').trim();
  const candidates = [configured, '/usr/bin/chromium', '/usr/bin/google-chrome',
    'C:/Program Files (x86)/Microsoft/Edge/Application/msedge.exe',
    'C:/Program Files/Microsoft/Edge/Application/msedge.exe',
    'C:/Program Files/Google/Chrome/Application/chrome.exe'].filter(Boolean);
  const executable = candidates.find(candidate => fs.existsSync(candidate));
  if (!executable) throw new ShimoWorkerError('BROWSER_NOT_FOUND', '浏览器 Worker 没有找到 Chromium。', 503);
  return executable;
}

export function cleanDocumentText(bodyText) {
  const lines = String(bodyText || '').replace(/\r/g, '').replace(/[ \t]+\n/g, '\n').split('\n');
  const directoryIndex = lines.findIndex(line => line.trim() === '目录');
  let content = lines;
  if (directoryIndex >= 0) {
    const afterDirectory = lines.slice(directoryIndex + 1);
    const firstTitleIndex = afterDirectory.findIndex(line => line.trim());
    if (firstTitleIndex >= 0) {
      const firstTitle = afterDirectory[firstTitleIndex].trim();
      const repeatedTitleIndex = afterDirectory.findIndex((line, index) => index > firstTitleIndex && line.trim() === firstTitle);
      content = repeatedTitleIndex >= 0 ? afterDirectory.slice(repeatedTitleIndex) : afterDirectory.slice(firstTitleIndex);
    }
  } else {
    const firstLikelyContent = lines.findIndex(line => !['文档将自动保存', '注册', '登录', '分享'].includes(line.trim()));
    if (firstLikelyContent >= 0) content = lines.slice(firstLikelyContent);
  }
  const feedback = content.findIndex(line => line.includes('提交反馈以帮助我们改进'));
  if (feedback >= 0) content = content.slice(0, feedback);
  while (content.length && !content[0].trim()) content.shift();
  while (content.length && !content.at(-1).trim()) content.pop();
  return content.join('\n').replace(/\n{3,}/g, '\n\n').trim();
}

function assertPageAccess(title, bodyText) {
  if (bodyText.includes('您还没有登录') || bodyText.includes('请登录后尝试访问')) {
    throw new ShimoWorkerError('LOGIN_REQUIRED', '石墨登录会话已失效。', 409);
  }
  if (title === '无权限' || bodyText.includes('申请访问权限')) {
    throw new ShimoWorkerError('PERMISSION_DENIED', '当前石墨账号没有该文件的访问权限。', 403);
  }
}

export class PlaywrightShimoEngine {
  constructor({
    executablePath = browserExecutable(),
    timeoutMs = 45_000,
    totalBudgetMs = DEFAULT_TOTAL_READ_BUDGET_MS,
    readBudgetMs = DEFAULT_READ_BUDGET_MS,
    now = () => Date.now(),
  } = {}) {
    this.executablePath = executablePath;
    this.timeoutMs = timeoutMs;
    this.totalBudgetMs = totalBudgetMs;
    this.readBudgetMs = readBudgetMs;
    this.now = now;
  }

  async withContext(storageState, callback) {
    const browser = await chromium.launch({ executablePath: this.executablePath, headless: true });
    try {
      const context = await browser.newContext({ storageState, viewport: { width: 1400, height: 1000 } });
      return await callback(context);
    } finally {
      await browser.close();
    }
  }

  async listSheets(storageState, inputURL) {
    const { url } = validateShimoURL(inputURL, 'sheet');
    return this.withContext(storageState, async context => {
      const page = await context.newPage();
      await page.goto(url, { waitUntil: 'domcontentloaded', timeout: this.timeoutMs });
      await page.waitForTimeout(2500);
      const bodyText = await page.locator('body').innerText({ timeout: 15_000 });
      assertPageAccess(await page.title(), bodyText);
      await page.locator('.sm-sheet-tab-item').first().waitFor({ state: 'attached', timeout: 15_000 });
      const raw = await page.locator('.sm-sheet-tab-item').evaluateAll(nodes => nodes.map((node, index) => ({
        name: node.querySelector('.sm-sheet-tab-name')?.getAttribute('title')
          || node.querySelector('.sm-sheet-tab-name')?.textContent?.trim()
          || node.textContent?.trim() || '',
        index,
        active: node.classList.contains('active-tab'),
      })));
      const seen = new Set();
      return { extracted_at: new Date().toISOString(), sheets: raw.filter(item => item.name && !seen.has(item.name) && seen.add(item.name)) };
    });
  }

  async readSheet(storageState, inputURL, sheetName, cellRange, { budgetStartedAt } = {}) {
    // 预算从 Worker 收到这次请求的时刻开始算（含限流器排队），而不是拿到并发槽位才开始，
    // 否则「排队 + 读取」会整体越过 server 侧 75s 超时。
    const startedAt = readStartedAt(budgetStartedAt, this.now);
    const { url, fileId } = validateShimoURL(inputURL, 'sheet');
    const plan = planRowBands(cellRange);
    // 页面加载的固定超时（45s + 2.5s + 15s）也吃同一条总预算：剩余不足时不再启动浏览器，
    // 直接按「忙、可重试」上报，避免 server 已经超时、worker 才开始读。
    const budgetLeft = () => this.totalBudgetMs - Math.max(0, this.now() - startedAt);
    if (budgetLeft() < MIN_PAGE_BUDGET_MS) {
      throw new ShimoWorkerError('READ_BUDGET_EXHAUSTED', '这次读取等待时间过长，预算已用尽，请稍后重试。', 503);
    }
    return this.withContext(storageState, async context => {
      const page = await context.newPage();
      let bodyText;
      try {
        await page.goto(url, { waitUntil: 'domcontentloaded', timeout: budgetBoundedTimeout(this.timeoutMs, budgetLeft()) });
        await page.waitForTimeout(Math.min(2500, Math.max(0, Math.floor(budgetLeft()))));
        bodyText = await page.locator('body').innerText({ timeout: budgetBoundedTimeout(15_000, budgetLeft()) });
      } catch (error) {
        throw budgetExhaustedOr(error, budgetLeft);
      }
      assertPageAccess(await page.title(), bodyText);
      const collected = await collectSheetValues(
        plan.bands,
        (band, { timeoutMs } = {}) => fetchRangeValues(context, fileId, sheetName, band.range, { timeoutMs }),
        { budgetMs: readBudgetFor(startedAt, { totalBudgetMs: this.totalBudgetMs, readBudgetMs: this.readBudgetMs }) },
      );
      return {
        extracted_at: new Date().toISOString(),
        values: collected.values,
        requested_range: cellRange,
        requests: collected.requests,
        covered_through_row: collected.covered_through_row,
        stopped_early: collected.stopped_early,
        truncated: plan.truncated || collected.timed_out,
      };
    });
  }

  async readDocument(storageState, inputURL, maxChars) {
    const { url } = validateShimoURL(inputURL, 'document');
    return this.withContext(storageState, async context => {
      const page = await context.newPage();
      await page.goto(url, { waitUntil: 'domcontentloaded', timeout: this.timeoutMs });
      await page.waitForTimeout(2500);
      const bodyText = await page.locator('body').innerText({ timeout: 15_000 });
      assertPageAccess(await page.title(), bodyText);
      const fullText = cleanDocumentText(bodyText);
      if (!fullText) throw new ShimoWorkerError('EMPTY_CONTENT', '没有提取到可读正文。', 502);
      const characters = [...fullText].length;
      return { extracted_at: new Date().toISOString(), text: [...fullText].slice(0, maxChars).join(''), truncated: characters > maxChars, characters };
    });
  }
}
