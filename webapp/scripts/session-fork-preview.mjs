// Browser-only contract fixture. Every API/WS request is intercepted; no live server or model is used.
import assert from 'node:assert/strict';
import { createRequire } from 'node:module';
import { mkdir } from 'node:fs/promises';
import { resolve } from 'node:path';

const require = createRequire(import.meta.url);
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const base = process.env.FORK_PREVIEW_URL || 'http://127.0.0.1:5173';
const output = resolve('.session-fork-preview');
await mkdir(output, { recursive: true });
const browser = await chromium.launch({ headless: true, ...(process.env.FORK_PREVIEW_BROWSER ? { executablePath: process.env.FORK_PREVIEW_BROWSER } : {}) });
const user = { uid: 1, username: 'preview', display_name: 'Preview', account_type: 'human', created_at: '2025-01-01T00:00:00Z' };
const agent = { uid: 2, display_name: 'Research Agent', username: 'research', is_bot: true, is_owner: true, relation: 'owner', topic_id: 'p2p_1_2', online: true };
const source = { id: 'p2p_1_2', name: 'Original research', type: 'p2p', is_bot: true, peer_uid: 2, updated_at: '2026-10-19T04:00:00Z' };
const target = { id: 'grp_99', name: 'Research copy', type: 'group', is_group: true, group_id: 99, kind: 'agent_task', is_agent_task: true, has_bot: true, member_count: 2, updated_at: '2026-10-19T05:00:00Z' };
try {
  for (const viewport of [{ width: 1365, height: 900 }, { width: 390, height: 844 }, { width: 320, height: 740 }]) {
    const context = await browser.newContext({ viewport, serviceWorkers: 'block' });
    let fork = null;
    let ready = false;
    let uncertain = true;
    const posts = [];
    const errors = [];
    const unhandledWrites = [];
    await context.routeWebSocket('**/*', socket => socket.close());
    await context.route('**/*', async route => {
      const request = route.request();
      const url = new URL(request.url());
      if (url.origin !== base) return route.abort();
      if (!url.pathname.startsWith('/api/') && !url.pathname.startsWith('/local') && !url.pathname.startsWith('/artifact-gateway')) return route.continue();
      const json = (body, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
      if (url.pathname === '/api/conversations/fork') {
        if (request.method() === 'POST') {
          posts.push(request.postDataJSON());
          fork = { ...request.postDataJSON(), target_topic_id: 'grp_99', state: 'pending' };
          if (uncertain) { uncertain = false; return route.abort('failed'); }
          return json(fork);
        }
        if (!fork || url.searchParams.get('topic_id') && url.searchParams.get('topic_id') !== 'grp_99') return json({ error: 'session_fork_not_found' }, 404);
        return json({ ...fork, state: ready ? 'ready' : 'pending' });
      }
      if (request.method() !== 'GET' && url.pathname !== '/api/push/subscriptions') unhandledWrites.push(url.pathname);
      if (url.pathname === '/api/me') return json(user);
      if (url.pathname === '/api/agents') return json({ agents: [agent] });
      if (url.pathname === '/api/friends') return json({ friends: [] });
      if (url.pathname === '/api/conversations') return json({ conversations: fork ? [target, source] : [source] });
      if (url.pathname === '/api/groups') return json({ groups: fork ? [{ ...target, id: 99, owner_id: 1, agent_ids: [2] }] : [] });
      if (url.pathname === '/api/groups/info') return json({ group: { ...target, id: 99, owner_id: 1 }, members: [{ user_id: 1, role: 'owner' }, { user_id: 2, is_bot: true }] });
      if (url.pathname === '/api/messages') return json({ messages: [{
        id: url.searchParams.get('topic_id') === 'grp_99' ? 101 : 1, seq_id: 1,
        topic_id: url.searchParams.get('topic_id'), from_uid: 1, type: 'text', content: 'Compare the two research directions.', created_at: '2026-10-19T04:00:00Z',
      }] });
      if (url.pathname === '/api/projects') return json({ projects: [] });
      if (url.pathname === '/api/agents/quota') return json({ configured: false, shared: true });
      return json({ agents: [], bots: [], groups: [], friends: [], workers: [], apps: [], artifacts: [], files: [], tasks: [], models: [], requests: [], allowed: false, enabled: false });
    });
    await context.addInitScript(({ user, source }) => {
      localStorage.setItem('oc_token', `fixture.${btoa(JSON.stringify({ userId: 1, exp: 2100000000 }))}.fixture`);
      localStorage.setItem('oc_user', JSON.stringify(user));
      if (!localStorage.getItem('v3_last_topic:1')) localStorage.setItem('v3_last_topic:1', JSON.stringify({ topicId: source.id, name: source.name, isBot: true, friendId: 2 }));
    }, { user, source });
    const page = await context.newPage();
    page.on('pageerror', error => errors.push(error.message));
    await page.goto(base);
    await page.getByRole('button', { name: '会话操作', exact: true }).click();
    await page.getByRole('menuitem', { name: '复制为新会话', exact: true }).waitFor();
    assert.match(await page.getByRole('menu', { name: '会话操作' }).innerText(), /不是 Git worktree/);
    await page.screenshot({ path: resolve(output, `menu-${viewport.width}.png`) });
    await page.getByRole('menuitem', { name: '复制为新会话', exact: true }).click();
    await page.getByRole('alert').filter({ hasText: '复制结果未确认' }).waitFor();
    await page.getByRole('button', { name: '重试复制', exact: true }).click();
    await page.getByRole('status').filter({ hasText: '正在初始化新会话' }).waitFor();
    assert.equal(posts.length, 2);
    assert.deepEqual(posts[0], posts[1]);
    // Open through the ordinary persisted-topic path while the target is pending.
    await page.evaluate(() => localStorage.setItem('v3_last_topic:1', JSON.stringify({ topicId: 'grp_99', groupId: 99, isGroup: true, name: 'Research copy' })));
    await page.reload();
    await page.getByRole('status').filter({ hasText: '暂不能发送消息' }).waitFor();
    assert.equal(await page.locator('textarea').isDisabled(), true);
    // A copy reads as an ordinary conversation: no 「复制自 grp_…」 banner. The
    // provenance record still disables the composer above, and the copy's name
    // is what tells the user where it came from.
    await page.screenshot({ path: resolve(output, `pending-${viewport.width}.png`) });
    ready = true;
    await page.waitForFunction(() => document.querySelector('textarea')?.disabled === false);
    assert.equal(await page.getByRole('note', { name: '会话来源' }).count(), 0);
    assert.ok((await page.locator('.v3-timeline').innerText()).includes('Compare the two research directions.'));
    await page.screenshot({ path: resolve(output, `ready-${viewport.width}.png`) });
    await page.evaluate(() => localStorage.setItem('v3_last_topic:1', JSON.stringify({ topicId: 'p2p_1_2', name: 'Original research', isBot: true, friendId: 2 })));
    await page.reload();
    await page.getByRole('button', { name: '打开新会话', exact: true }).click();
    assert.equal(await page.getByRole('note', { name: '会话来源' }).count(), 0);
    assert.match(await page.locator('.v3-local-assistant-bar').innerText(), /Research copy/);
    if (viewport.width > 1000) assert.match(await page.locator('.v3-sidebar').innerText(), /Research copy/);
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth), false);
    assert.deepEqual(errors, []);
    assert.deepEqual(unhandledWrites, []);
    console.log(JSON.stringify({ viewport, duplicateRequestId: posts[0].request_id, pendingComposerBlocked: true, readyComposerEnabled: true, provenance: true, copiedHistory: true, noPageErrors: true }));
    await context.close();
  }
} finally { await browser.close(); }
