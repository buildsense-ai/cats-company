// Browser driver for the office-editor acceptance run.
//
// Why this exists instead of codex's browser tools: codex 0.154.0 registers native
// `browser_*` tools when the `browser_use` / `in_app_browser` features are on, and those
// tools are served by the OpenAI Responses hosted-tool path. Behind a third-party relay
// they list fine but every call returns `unsupported call`. The playwright MCP server is
// healthy over stdio (25 tools), yet its tools arrive under the same `browser_*` names and
// are shadowed by the native ones, so the same error appears. A CLI the agent can run
// through its shell tool has no such collision and is verifiable end to end.
//
// The Chrome profile is persistent, so a login done once survives later invocations. That
// is what makes a multi-step UI test possible when each step is a separate process.
//
// Requirements: playwright-core must be resolvable. Either install it in the webapp
// (`npm i -D playwright-core`), or install @playwright/mcp globally, which ships one.
//
// Usage (run from webapp/):
//   node scripts/office-browser-driver.mjs open <url>
//   node scripts/office-browser-driver.mjs login
//   node scripts/office-browser-driver.mjs text
//   node scripts/office-browser-driver.mjs inputs
//   node scripts/office-browser-driver.mjs click <selector>
//   node scripts/office-browser-driver.mjs fill <selector> <value...>
//   node scripts/office-browser-driver.mjs upload <path...>
//   node scripts/office-browser-driver.mjs compose [--new] <text> -- <file...>
//   node scripts/office-browser-driver.mjs scanfiles
//   node scripts/office-browser-driver.mjs eval "<js expression>"
//   node scripts/office-browser-driver.mjs shot <out.png>
//   node scripts/office-browser-driver.mjs url
//
// Environment:
//   BROWSER_HEADLESS=1        headless (default is headed, so a captcha is visible)
//   BROWSER_CREDENTIALS=<p>   credential file, first line account, second line password
//
// Every command prints machine-readable lines so the agent can assert on output rather
// than on a screenshot. Credentials are never echoed.

import fs from 'node:fs';
import path from 'node:path';
import { createRequire } from 'node:module';

const PROFILE = process.env.BROWSER_PROFILE || 'D:\\tmp\\office-verify\\chrome-profile';
const STATE = 'D:\\tmp\\office-verify\\browser-state.json';
const DEFAULT_CREDENTIALS = 'C:\\Users\\17499\\Desktop\\catsco账号密码.txt';
const CREDENTIALS = process.env.BROWSER_CREDENTIALS || DEFAULT_CREDENTIALS;
const LOGIN_PATH = '/login';
const DEFAULT_ORIGIN = 'http://localhost:5173';
const TIMEOUT = 30000;

function loadPlaywright() {
  const candidates = [
    path.join(process.cwd(), 'node_modules', 'playwright-core'),
    path.join(process.cwd(), 'node_modules', 'playwright'),
    path.join(process.env.APPDATA || '', 'npm', 'node_modules', '@playwright', 'mcp'),
  ];
  for (const base of candidates) {
    try {
      // `@playwright/mcp` re-exports playwright-core through its own dependency tree.
      const req = createRequire(path.join(base, 'noop.js'));
      return req('playwright-core').chromium || req('playwright').chromium;
    } catch { /* try the next candidate */ }
  }
  throw new Error('playwright-core not found. Run `npm i -D playwright-core` in webapp/, or `npm i -g @playwright/mcp`.');
}

const [command, ...args] = process.argv.slice(2);
if (!command) {
  console.error('usage: node scripts/office-browser-driver.mjs <command> [args]');
  process.exit(2);
}

function readState() {
  try { return JSON.parse(fs.readFileSync(STATE, 'utf8')); } catch { return {}; }
}
function writeState(patch) {
  try { fs.writeFileSync(STATE, JSON.stringify({ ...readState(), ...patch }, null, 2), 'utf8'); } catch { /* convenience only */ }
}

function readCredentials() {
  const raw = fs.readFileSync(CREDENTIALS, 'utf8');
  const lines = raw.split(/\r?\n/).map(line => line.trim()).filter(Boolean);
  if (lines.length < 2) throw new Error('credential file needs an account line and a password line');
  return { account: lines[0], password: lines[1] };
}

const chromium = loadPlaywright();
const headless = process.env.BROWSER_HEADLESS === '1';
const context = await chromium.launchPersistentContext(PROFILE, {
  channel: 'chrome',
  headless,
  viewport: { width: 1440, height: 900 },
  args: ['--no-first-run', '--no-default-browser-check'],
});
const page = context.pages()[0] || await context.newPage();
page.setDefaultTimeout(TIMEOUT);

const out = (label, value) => console.log(`${label} ${value}`);

try {
  // Every invocation is a fresh process that starts on about:blank. Without restoring the
  // last URL, follow-up commands inspect an empty page: `url` answered about:blank right
  // after a successful login, which reads as a failure in any multi-step test.
  if (command !== 'open' && !page.url().startsWith('http')) {
    const remembered = readState().url;
    if (remembered) {
      await page.goto(remembered, { waitUntil: 'domcontentloaded', timeout: TIMEOUT });
      await page.waitForTimeout(1500);
    }
  }

  if (command === 'open') {
    await page.goto(args[0], { waitUntil: 'domcontentloaded', timeout: TIMEOUT });
    await page.waitForTimeout(2000);
    writeState({ url: page.url() });
    out('URL', page.url());
    out('TITLE', await page.title());

  } else if (command === 'login') {
    // A persistent profile keeps the session, so a second run lands already signed in.
    // Treating that as a failure would hide a working login behind a selector timeout.
    if (!page.url().startsWith('http')) {
      await page.goto(readState().url || DEFAULT_ORIGIN, { waitUntil: 'domcontentloaded', timeout: TIMEOUT });
      await page.waitForTimeout(1500);
    }
    if (!page.url().includes(LOGIN_PATH) && page.url().startsWith('http')) {
      out('URL', page.url());
      out('LOGGED_IN', 'already');
      out('OK', '1');
      await context.close();
      process.exit(0);
    }

    // Sign in against the origin that was opened, not a hard-coded host. Pinning the
    // production host once reported a healthy login that had actually tested deployed
    // code while claiming to test the local build.
    const origin = new URL(readState().url || DEFAULT_ORIGIN).origin;
    await page.goto(`${origin}${LOGIN_PATH}`, { waitUntil: 'domcontentloaded', timeout: TIMEOUT });
    await page.waitForTimeout(2000);

    const { account, password } = readCredentials();
    const accountBox = page.locator('input[type="email"], input[name="email"], input[autocomplete="username"], input[type="text"]').first();
    await accountBox.waitFor({ state: 'visible', timeout: TIMEOUT });
    await accountBox.fill(account);
    await page.locator('input[type="password"]').first().fill(password);

    // The form renders a plain `button` (password visibility toggle) before the submit
    // button. A union selector ordered by DOM clicks the toggle, the form never submits,
    // and the only symptom is "still on /login".
    await page.locator('button[type="submit"]').first().click();
    await page.waitForTimeout(6000);
    writeState({ url: page.url() });
    out('URL', page.url());
    out('LOGGED_IN', page.url().includes(LOGIN_PATH) ? 'no' : 'yes');

  } else if (command === 'text') {
    const text = await page.evaluate(() => document.body.innerText.slice(0, 4000));
    out('TEXT', JSON.stringify(text));

  } else if (command === 'scanfiles') {
    const result = await page.evaluate(async () => {
      const mod = await import('/src/api.js');
      const conversations = (await mod.api.getConversations()).conversations || [];
      const matches = [];
      let cursor = 0;
      async function worker() {
        while (cursor < conversations.length) {
          const conversation = conversations[cursor++];
          try {
            const response = await mod.api.getMessages(conversation.id, 80, 0, true);
            for (const message of response.messages || []) {
              const files = [];
              if (message.type === 'file') {
                let value = message.content;
                if (typeof value === 'string') {
                  try { value = JSON.parse(value); } catch { /* keep raw */ }
                }
                files.push(value?.payload || value);
              }
              for (const block of Array.isArray(message.content_blocks) ? message.content_blocks : []) {
                if (block?.type === 'file' || block?.type === 'attachment') {
                  files.push(block.payload || block.file || block);
                }
                if (Array.isArray(block?.attachments)) {
                  files.push(...block.attachments.map(attachment => attachment.payload || attachment));
                }
              }
              for (const file of files) {
                const name = String(file?.name || file?.filename || '');
                if (!/\.(pptx|docx|ppt)$/i.test(name)) continue;
                matches.push({
                  conversationId: conversation.id,
                  conversationName: conversation.name,
                  messageId: message.id || message.message_id,
                  name,
                  size: file.size,
                  url: String(file.url || '').slice(0, 160),
                });
              }
            }
          } catch { /* one unavailable conversation must not block the scan */ }
        }
      }
      await Promise.all(Array.from({ length: 8 }, worker));
      return { scanned: conversations.length, matches };
    });
    out('SCAN', JSON.stringify(result));

  } else if (command === 'inputs') {
    const fields = await page.evaluate(() => Array.from(document.querySelectorAll('input,textarea,button'))
      .map(el => [el.tagName.toLowerCase(), el.getAttribute('type') || '', el.getAttribute('name') || '',
        el.getAttribute('placeholder') || '', (el.innerText || '').trim().slice(0, 24)].join('|')));
    out('FIELDS', JSON.stringify(fields));

  } else if (command === 'click') {
    await page.locator(args[0]).first().click();
    await page.waitForTimeout(2000);
    writeState({ url: page.url() });
    out('URL', page.url());

  } else if (command === 'fill') {
    await page.locator(args[0]).first().fill(args.slice(1).join(' '));
    out('FILLED', args[0]);

  } else if (command === 'upload') {
    // The composer keeps its real file input hidden behind a menu. Setting files on that input
    // exercises the same change handler a human click produces, without opening a native dialog.
    const input = page.locator('input[type="file"]').last();
    await input.setInputFiles(args);
    out('UPLOADED', JSON.stringify(args));

  } else if (command === 'compose') {
    // Upload and send must happen in one process: transient composer state is not persisted by
    // the app between driver invocations, and a reload between the two steps would drop the file.
    const newTask = args[0] === '--new';
    const bodyArgs = newTask ? args.slice(1) : args;
    const separator = bodyArgs.indexOf('--');
    const text = (separator >= 0 ? bodyArgs.slice(0, separator) : bodyArgs).join(' ');
    const files = separator >= 0 ? bodyArgs.slice(separator + 1) : [];
    if (newTask) {
      await page.getByRole('button', { name: '新建任务' }).first().click();
      await page.waitForTimeout(1500);
      // A new task first asks which agent should own it. The composer behind that picker still
      // belongs to the previous conversation, so choosing an agent is required before composing.
      await page.locator('button.cc-new-task-agent').first().click();
      await page.waitForTimeout(1500);
    }
    await page.locator('textarea').last().fill(text);
    if (files.length > 0) {
      await page.locator('input[type="file"]').last().setInputFiles(files);
      const send = page.locator('button.v3-send').last();
      const deadline = Date.now() + 60000;
      while (Date.now() < deadline && !(await send.isEnabled())) {
        await page.waitForTimeout(1000);
      }
    }
    const send = page.locator('button.v3-send').last();
    out('COMPOSER', JSON.stringify(await page.evaluate(() => ({
      tail: document.body.innerText.slice(-1200),
      attachments: Array.from(document.querySelectorAll('.v3-composer-attachment, .v3-attachment-chip, .v3-pending-attachment'))
        .map(node => (node.innerText || '').trim()).filter(Boolean),
      sendDisabled: document.querySelector('button.v3-send')?.disabled ?? null,
    }))));
    await send.click({ timeout: 15000 });
    await page.waitForTimeout(10000);
    writeState({ url: page.url() });
    out('URL', page.url());
    out('TEXT', JSON.stringify((await page.evaluate(() => document.body.innerText)).slice(-2500)));
    out('CARDS', JSON.stringify(await page.evaluate(() => Array.from(document.querySelectorAll('.v3-attachment-card'))
      .map(card => (card.innerText || '').trim().replace(/\s+/g, ' ').slice(0, 180)))));

  } else if (command === 'eval') {
    out('VALUE', JSON.stringify(await page.evaluate(args.join(' '))));

  } else if (command === 'shot') {
    const target = path.resolve(args[0] || 'D:\\tmp\\office-verify\\shot.png');
    fs.mkdirSync(path.dirname(target), { recursive: true });
    await page.screenshot({ path: target });
    out('SHOT', target);

  } else if (command === 'url') {
    out('URL', page.url());

  } else {
    console.error(`unknown command: ${command}`);
    process.exit(2);
  }

  out('OK', '1');
} catch (error) {
  // A failure must be distinguishable from a pass, so say so on its own line.
  console.error(`FAIL ${error.message.split('\n')[0]}`);
  process.exitCode = 1;
} finally {
  await context.close();
}
