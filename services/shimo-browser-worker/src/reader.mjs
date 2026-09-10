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
  constructor({ executablePath = browserExecutable(), timeoutMs = 45_000 } = {}) {
    this.executablePath = executablePath;
    this.timeoutMs = timeoutMs;
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

  async readSheet(storageState, inputURL, sheetName, cellRange) {
    const { url, fileId } = validateShimoURL(inputURL, 'sheet');
    return this.withContext(storageState, async context => {
      const page = await context.newPage();
      await page.goto(url, { waitUntil: 'domcontentloaded', timeout: this.timeoutMs });
      await page.waitForTimeout(2500);
      const bodyText = await page.locator('body').innerText({ timeout: 15_000 });
      assertPageAccess(await page.title(), bodyText);
      const apiRange = `${sheetName}!${cellRange}`;
      const endpoint = `https://shimo.im/sdk/v2/api/files/${fileId}/sheets/values?range=${encodeURIComponent(apiRange)}`;
      const response = await context.request.get(endpoint);
      if (response.status() === 401) throw new ShimoWorkerError('LOGIN_REQUIRED', '石墨登录会话已失效。', 409);
      if (response.status() === 403) throw new ShimoWorkerError('PERMISSION_DENIED', '当前石墨账号没有读取权限。', 403);
      if (!response.ok()) throw new ShimoWorkerError('SHIMO_API_ERROR', `石墨表格接口返回 HTTP ${response.status()}。`, 502);
      const payload = await response.json();
      if (!Array.isArray(payload.values) || payload.values.some(row => !Array.isArray(row))) {
        throw new ShimoWorkerError('UPSTREAM_CHANGED', '石墨表格接口没有返回二维数组。', 502);
      }
      return { extracted_at: new Date().toISOString(), values: payload.values };
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
