import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest';

const wikiMocks = vi.hoisted(() => ({
  getKnowledgeWikiManifest: vi.fn(),
  getKnowledgeWikiWebSocketTicket: vi.fn(),
  connectWS: vi.fn(),
  disconnectWS: vi.fn(),
  requestSkillHubDeviceTool: vi.fn(),
}));

vi.mock('../api', async (importOriginal) => {
  const actual = await importOriginal();
  return {
    ...actual,
    api: {
      ...actual.api,
      getKnowledgeWikiManifest: wikiMocks.getKnowledgeWikiManifest,
      getKnowledgeWikiWebSocketTicket: wikiMocks.getKnowledgeWikiWebSocketTicket,
    },
    connectWS: wikiMocks.connectWS,
    disconnectWS: wikiMocks.disconnectWS,
    requestSkillHubDeviceTool: wikiMocks.requestSkillHubDeviceTool,
  };
});

import TinodeWeb from './tinode-web';
import { knowledgeWikiAgentID, knowledgeWikiSortedItems } from './knowledge-wiki-view';

const wikiCss = readFileSync(resolve(process.cwd(), 'src/views/knowledge-wiki-view.css'), 'utf8');

let container;
let root;

beforeEach(() => {
  wikiMocks.getKnowledgeWikiManifest.mockReset();
  wikiMocks.getKnowledgeWikiWebSocketTicket.mockReset();
  wikiMocks.connectWS.mockReset();
  wikiMocks.disconnectWS.mockReset();
  wikiMocks.requestSkillHubDeviceTool.mockReset();
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
});

afterEach(async () => {
  await act(async () => root.unmount());
  container.remove();
});

function renderWikiRoute(pathname) {
  return act(async () => {
    root.render(<TinodeWeb location={{ pathname, search: '', hash: '' }} />);
  });
}

function primeWikiSession() {
  wikiMocks.getKnowledgeWikiManifest.mockResolvedValue({
    online: true,
    owner_user_id: 406,
    target_device_id: 'device-1',
    agent_name: '测试知识库',
  });
  wikiMocks.getKnowledgeWikiWebSocketTicket.mockResolvedValue({ token: 'wiki-ticket' });
  wikiMocks.connectWS.mockImplementation((onMessage) => {
    onMessage?.({ _type: 'ws_open' });
    return true;
  });
}

describe('knowledgeWikiAgentID', () => {
  test('decodes the uid and normalizes trailing slashes like the route gate', () => {
    expect(knowledgeWikiAgentID('/wiki/agents/982')).toBe('982');
    expect(knowledgeWikiAgentID('/wiki/agents/982/')).toBe('982');
    expect(knowledgeWikiAgentID('/wiki/agents/982//')).toBe('982');
    expect(knowledgeWikiAgentID('/wiki/agents/%E6%B5%8B%E8%AF%95')).toBe('测试');
  });

  test('returns an empty uid instead of throwing on malformed escapes', () => {
    expect(knowledgeWikiAgentID('/wiki/agents/%E0%A4')).toBe('');
    expect(knowledgeWikiAgentID('/wiki/agents')).toBe('');
    expect(knowledgeWikiAgentID('/wiki/agents/')).toBe('');
    expect(knowledgeWikiAgentID('/wiki')).toBe('');
  });
});

describe('knowledge wiki routing without a local session token', () => {
  test('renders the handoff view for a valid agent uid', async () => {
    wikiMocks.getKnowledgeWikiManifest.mockResolvedValue({
      online: true,
      owner_user_id: 406,
      target_device_id: 'device-1',
      agent_name: '测试知识库',
    });
    wikiMocks.getKnowledgeWikiWebSocketTicket.mockResolvedValue({ token: 'wiki-ticket' });
    wikiMocks.connectWS.mockImplementation((onMessage) => {
      onMessage?.({ _type: 'ws_open' });
      return true;
    });
    wikiMocks.requestSkillHubDeviceTool.mockResolvedValue({
      items: [],
      total: 3,
      review_pending: 1,
      nextOffset: null,
    });

    await renderWikiRoute('/wiki/agents/982');

    await vi.waitFor(() => {
      expect(container.textContent).toContain('测试知识库');
    });
    expect(container.querySelector('.cc-knowledge-wiki')).toBeTruthy();
    expect(wikiMocks.getKnowledgeWikiManifest).toHaveBeenCalledWith('982');
    expect(wikiMocks.requestSkillHubDeviceTool).toHaveBeenCalledWith(expect.objectContaining({
      toolName: 'knowledge.document.list',
    }));
    expect(container.textContent).toContain('3');
  });

  test('falls back to the invalid-entry state for malformed uids instead of throwing', async () => {
    await renderWikiRoute('/wiki/agents/%E0%A4');

    expect(container.querySelector('.cc-knowledge-wiki')).toBeTruthy();
    expect(container.textContent).toContain('知识库入口无效');
    expect(wikiMocks.getKnowledgeWikiManifest).not.toHaveBeenCalled();
  });

  test('maps an expired handoff session to the re-entry message', async () => {
    wikiMocks.getKnowledgeWikiManifest.mockRejectedValue(
      Object.assign(new Error('unauthorized'), { status: 401 }),
    );

    await renderWikiRoute('/wiki/agents/982');

    await vi.waitFor(() => {
      expect(container.textContent).toContain('知识库入口无效或已过期，请重新从 AI 助手管理进入。');
    });
    expect(container.textContent).not.toContain('unauthorized');
  });
});

describe('knowledgeWikiSortedItems', () => {
  const items = [
    { id: 'KB-b', title: 'older', updatedAt: '2026-09-01T00:00:00Z' },
    { id: 'KB-a', title: 'newer', updatedAt: '2026-09-16T00:00:00Z' },
    { id: 'KB-c', title: 'undated' },
  ];

  test('orders by updatedAt with the newest first by default', () => {
    expect(knowledgeWikiSortedItems(items).map((item) => item.id)).toEqual(['KB-a', 'KB-b', 'KB-c']);
    expect(knowledgeWikiSortedItems(items, { sortDesc: false }).map((item) => item.id)).toEqual(['KB-c', 'KB-b', 'KB-a']);
  });

  test('does not mutate the input array', () => {
    const source = items.slice();
    knowledgeWikiSortedItems(source, { sortDesc: false });
    expect(source.map((item) => item.id)).toEqual(['KB-b', 'KB-a', 'KB-c']);
  });
});

describe('knowledge wiki search and filters', () => {
  test('searches through the runtime with a debounced query', async () => {
    primeWikiSession();
    wikiMocks.requestSkillHubDeviceTool.mockResolvedValue({
      items: [{ id: 'KB-1', title: '模拟台账', summary: '', category: 'projects', updatedAt: '2026-09-14T00:00:00Z', revision: 'r1', review_status: 'unreviewed' }],
      total: 1,
      review_pending: 1,
      nextOffset: null,
    });

    await renderWikiRoute('/wiki/agents/982');
    await vi.waitFor(() => {
      expect(container.querySelector('input[type="search"]')).toBeTruthy();
    });
    expect(container.querySelector('input[type="search"]').maxLength).toBe(200);
    const callsAfterLoad = wikiMocks.requestSkillHubDeviceTool.mock.calls.length;

    vi.useFakeTimers();
    try {
      const input = container.querySelector('input[type="search"]');
      const valueSetter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value').set;
      await act(async () => {
        valueSetter.call(input, '台账');
        input.dispatchEvent(new Event('input', { bubbles: true }));
      });
      await act(async () => {
        await vi.advanceTimersByTimeAsync(400);
      });

      const lastCall = wikiMocks.requestSkillHubDeviceTool.mock.calls.at(-1)?.[0];
      expect(wikiMocks.requestSkillHubDeviceTool.mock.calls.length).toBeGreaterThan(callsAfterLoad);
      expect(lastCall).toEqual(expect.objectContaining({
        toolName: 'knowledge.document.list',
        payload: expect.objectContaining({ query: '台账' }),
      }));
      expect(container.textContent).toContain('篇匹配');
    } finally {
      vi.useRealTimers();
    }
  });

  test('toggles the time ordering between newest-first and oldest-first', async () => {
    primeWikiSession();
    const now = Date.now();
    wikiMocks.requestSkillHubDeviceTool.mockResolvedValue({
      items: [
        { id: 'KB-old', title: '远古文档', summary: '', category: 'legacy', updatedAt: new Date(now - 100 * 24 * 60 * 60 * 1000).toISOString(), revision: 'r1', review_status: 'unreviewed' },
        { id: 'KB-new', title: '新鲜文档', summary: '', category: 'recent', updatedAt: new Date(now - 24 * 60 * 60 * 1000).toISOString(), revision: 'r2', review_status: 'reviewed' },
      ],
      total: 2,
      review_pending: 1,
      nextOffset: null,
    });

    await renderWikiRoute('/wiki/agents/982');
    await vi.waitFor(() => {
      expect(container.textContent).toContain('新鲜文档');
    });

    const titles = () => Array.from(container.querySelectorAll('.cc-knowledge-wiki-card h2')).map((node) => node.textContent);
    expect(titles()).toEqual(['新鲜文档', '远古文档']);
    expect(container.querySelector('.cc-knowledge-wiki-sort').textContent).toBe('最新优先');

    await act(async () => {
      Simulate.click(container.querySelector('.cc-knowledge-wiki-sort'));
    });
    expect(titles()).toEqual(['远古文档', '新鲜文档']);
    expect(container.querySelector('.cc-knowledge-wiki-sort').textContent).toBe('最早优先');
  });

  test('restores the default view and refetches the full list', async () => {
    primeWikiSession();
    const now = Date.now();
    const freshItem = { id: 'KB-new', title: '新鲜文档', summary: '', category: 'recent', updatedAt: new Date(now - 24 * 60 * 60 * 1000).toISOString(), revision: 'r2', review_status: 'reviewed' };
    const oldItem = { id: 'KB-old', title: '远古文档', summary: '', category: 'legacy', updatedAt: new Date(now - 100 * 24 * 60 * 60 * 1000).toISOString(), revision: 'r1', review_status: 'unreviewed' };
    const ledgerItem = { id: 'KB-1', title: '模拟台账', summary: '', category: 'projects', updatedAt: '2026-09-14T00:00:00Z', revision: 'r1', review_status: 'unreviewed' };
    wikiMocks.requestSkillHubDeviceTool.mockImplementation(async (request) => (
      request?.payload?.query === '台账'
        ? { items: [ledgerItem], total: 1, review_pending: 1, nextOffset: null }
        : { items: [oldItem, freshItem], total: 2, review_pending: 1, nextOffset: null }
    ));

    await renderWikiRoute('/wiki/agents/982');
    await vi.waitFor(() => {
      expect(container.textContent).toContain('新鲜文档');
    });

    await act(async () => {
      Simulate.click(container.querySelector('.cc-knowledge-wiki-sort'));
    });
    expect(container.querySelector('.cc-knowledge-wiki-sort').textContent).toContain('最早优先');

    vi.useFakeTimers();
    try {
      const input = container.querySelector('input[type="search"]');
      const valueSetter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value').set;
      await act(async () => {
        valueSetter.call(input, '台账');
        input.dispatchEvent(new Event('input', { bubbles: true }));
      });
      await act(async () => {
        await vi.advanceTimersByTimeAsync(400);
      });
    } finally {
      vi.useRealTimers();
    }

    expect(container.textContent).toContain('篇匹配');
    expect(container.textContent).toContain('模拟台账');
    expect(container.textContent).not.toContain('新鲜文档');

    const reset = container.querySelector('.cc-knowledge-wiki-reset');
    expect(reset).toBeTruthy();
    await act(async () => {
      Simulate.click(reset);
      await Promise.resolve();
    });

    await vi.waitFor(() => {
      expect(container.textContent).not.toContain('篇匹配');
    });
    expect(container.querySelector('input[type="search"]').value).toBe('');
    expect(container.querySelector('.cc-knowledge-wiki-sort').textContent).toContain('最新优先');
    expect(container.querySelector('.cc-knowledge-wiki-reset')).toBeFalsy();
    const titles = Array.from(container.querySelectorAll('.cc-knowledge-wiki-card h2')).map((node) => node.textContent);
    expect(titles).toEqual(['新鲜文档', '远古文档']);
  });

  test('pages through the list and continues past the loaded window with load-more', async () => {
    primeWikiSession();
    wikiMocks.requestSkillHubDeviceTool.mockImplementation(async ({ payload }) => ({
      items: [{ id: `KB-${payload.offset}`, title: `条目${payload.offset}`, summary: '', category: 'test', updatedAt: '2026-09-01T00:00:00Z', revision: 'r1', review_status: 'unreviewed' }],
      total: 999,
      review_pending: 0,
      nextOffset: payload.offset + 30,
    }));

    await renderWikiRoute('/wiki/agents/982');
    await vi.waitFor(() => {
      expect(container.textContent).toContain('条目0');
    });

    const offsets = () => wikiMocks.requestSkillHubDeviceTool.mock.calls.map((call) => call[0].payload.offset);
    expect(offsets()).toEqual([0, 30, 60, 90, 120, 150, 180, 210, 240, 270]);

    const more = container.querySelector('.cc-knowledge-wiki-more');
    expect(more).toBeTruthy();
    await act(async () => {
      Simulate.click(more);
      await Promise.resolve();
    });
    await vi.waitFor(() => {
      expect(offsets().at(-1)).toBe(300);
    });
    expect(container.textContent).toContain('条目300');
  });

  test('keeps the loaded list and offers an inline retry when a search fails', async () => {
    primeWikiSession();
    const item = { id: 'KB-1', title: '新鲜文档', summary: '', category: 'recent', updatedAt: '2026-09-14T00:00:00Z', revision: 'r1', review_status: 'reviewed' };
    wikiMocks.requestSkillHubDeviceTool.mockResolvedValue({ items: [item], total: 1, review_pending: 0, nextOffset: null });

    await renderWikiRoute('/wiki/agents/982');
    await vi.waitFor(() => {
      expect(container.textContent).toContain('新鲜文档');
    });

    wikiMocks.requestSkillHubDeviceTool.mockRejectedValue(Object.assign(
      new Error('知识库暂时无法读取，请刷新或通过 Agent 检查索引。'),
      { code: 'WIKI_BUSY' },
    ));
    const input = container.querySelector('input[type="search"]');
    const valueSetter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value').set;
    await act(async () => {
      valueSetter.call(input, '台账');
      input.dispatchEvent(new Event('input', { bubbles: true }));
    });
    await act(async () => {
      Simulate.keyDown(input, { key: 'Enter' });
      await Promise.resolve();
    });

    await vi.waitFor(() => {
      expect(container.textContent).toContain('知识库正在读取中，请稍后重试。');
    });
    expect(container.textContent).toContain('新鲜文档');
    expect(container.querySelector('.cc-knowledge-wiki-search-error button')).toBeTruthy();

    wikiMocks.requestSkillHubDeviceTool.mockResolvedValue({ items: [item], total: 1, review_pending: 0, nextOffset: null });
    await act(async () => {
      Simulate.click(container.querySelector('.cc-knowledge-wiki-search-error button'));
      await Promise.resolve();
    });
    await vi.waitFor(() => {
      expect(container.querySelector('.cc-knowledge-wiki-search-error')).toBeFalsy();
    });
  });
});

describe('knowledge wiki card layout', () => {
  test('keeps the text block shrinkable and breaks long tokens', () => {
    // The chip previously overflowed the card's right edge on cards whose text
    // contains long unbreakable tokens (e.g. paths like index/search/read).
    expect(wikiCss).toMatch(/\.cc-knowledge-wiki-card > div \{[^}]*flex: 1 1 auto[^}]*min-width: 0/);
    expect((wikiCss.match(/overflow-wrap: anywhere/g) || []).length).toBeGreaterThanOrEqual(2);
  });

  test('truncates the category chip instead of letting it leave the card', () => {
    const chipRule = wikiCss.match(/\.cc-knowledge-wiki-card > span \{[^}]*\}/)?.[0] || '';
    expect(chipRule).toContain('flex: none');
    expect(chipRule).toContain('white-space: nowrap');
    expect(chipRule).toContain('max-width: 100%');
    expect(chipRule).toContain('overflow: hidden');
    expect(chipRule).toContain('text-overflow: ellipsis');
  });
});
