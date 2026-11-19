import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import WikiMemoryPanel from './wiki-memory-panel';
import { api } from '../api';

vi.mock('../api', () => ({ api: { getWikiMemoryCatalog: vi.fn(), getWikiMemoryDocument: vi.fn() } }));

const wiki = (id, day, topic = 'grp_7') => ({
  id,
  title: `Wiki ${id}`,
  conversation_ids: [topic],
  created_at: `2026-11-${day}T00:00:00Z`,
  revision: 'v1',
});
const page = (items, cursor = null) => ({ items, conversations: [{ id: 'grp_7', title: '设计讨论' }], next_cursor: cursor });

describe('chat wiki memories', () => {
  let container;
  let root;
  let reachListEnd;

  beforeEach(() => {
    vi.resetAllMocks();
    vi.stubGlobal('IntersectionObserver', class {
      constructor(callback) { reachListEnd = () => callback([{ isIntersecting: true }]); }
      observe() {}
      disconnect() {}
    });
    localStorage.clear();
    globalThis.IS_REACT_ACT_ENVIRONMENT = true;
    container = document.createElement('div');
    document.body.append(container);
    root = createRoot(container);
    api.getWikiMemoryDocument.mockImplementation(async (_, id) => ({ id, revision: 'v1', markdown: '## Actual **Markdown**' }));
  });

  afterEach(async () => {
    await act(async () => root.unmount());
    vi.unstubAllGlobals();
    container.remove();
  });

  async function render(key = 'first') {
    await act(async () => root.render(<WikiMemoryPanel key={key} viewerUid="2" topicId="grp_7" agents={[{ uid: '50', name: 'Helper' }]} onClose={() => {}} />));
  }

  async function click(button) {
    expect(button).toBeTruthy();
    await act(async () => button.click());
  }

  const rows = () => Array.from(container.querySelectorAll('.cc-wiki-workspace:not([hidden]) .cc-wiki-catalog:not([hidden]) button.cc-wiki-row'));

  it('opens the current conversation in newest-first order and fetches Markdown only when expanded', async () => {
    api.getWikiMemoryCatalog.mockResolvedValue(page([wiki('older', 11), wiki('newer', 12)]));
    await render();
    expect(rows().map(el => el.querySelector('span').textContent)).toEqual(['Wiki newer', 'Wiki older']);
    expect(api.getWikiMemoryCatalog.mock.calls[0][1].topicId).toBe('grp_7');
    expect(api.getWikiMemoryDocument).not.toHaveBeenCalled();
    await click(rows()[0]);
    expect(container.querySelector('article h2 strong')?.textContent).toBe('Markdown');
    expect(rows()).toHaveLength(0);
    expect(container.querySelector('.cc-wiki-reader h3').textContent).toBe('Wiki newer');
    await click(container.querySelector('.cc-wiki-reader-toolbar button'));
    await click(rows()[0]);
    expect(api.getWikiMemoryDocument).toHaveBeenCalledTimes(2);
  });

  it('switches between graph and a single newest-first list without loading Wiki bodies', async () => {
    api.getWikiMemoryCatalog.mockResolvedValue(page([wiki('older', 11), wiki('newer', 12)]));
    await render();
    await click(Array.from(container.querySelectorAll('nav button')).find(button => button.textContent === '助手记忆'));
    expect(api.getWikiMemoryCatalog.mock.calls[1][1].topicId).toBeUndefined();
    await click(Array.from(container.querySelectorAll('.cc-wiki-view-bar button')).find(button => button.textContent === '列表'));
    expect(rows().map(row => row.querySelector('.cc-wiki-row-text > span').textContent)).toEqual(['Wiki newer', 'Wiki older']);
    expect(rows()[0].textContent).toContain('设计讨论');
    expect(api.getWikiMemoryCatalog).toHaveBeenCalledTimes(2);
    expect(api.getWikiMemoryDocument).not.toHaveBeenCalled();
  });

  it('loads the next list page at the bottom and keeps newer rows first', async () => {
    api.getWikiMemoryCatalog.mockResolvedValueOnce(page([]));
    await render('initial-empty');
    api.getWikiMemoryCatalog.mockResolvedValueOnce(page([wiki('a', 12)], 'older-page'));
    let resolveOlder;
    api.getWikiMemoryCatalog.mockReturnValueOnce(new Promise(resolve => { resolveOlder = resolve; }));
    await render('after-a');
    expect(rows()).toHaveLength(1);
    expect(rows()[0].querySelector('small')?.textContent).toBe('新增');
    expect(api.getWikiMemoryCatalog).toHaveBeenCalledTimes(2);
    await act(async () => reachListEnd());
    expect(api.getWikiMemoryCatalog.mock.calls.at(-1)[1].cursor).toBe('older-page');
    await act(async () => resolveOlder(page([wiki('b', 11), wiki('a', 12)])));
    expect(rows().map(row => row.querySelector('span').textContent)).toEqual(['Wiki a', 'Wiki b']);
    expect(api.getWikiMemoryDocument).not.toHaveBeenCalled();
    api.getWikiMemoryCatalog.mockResolvedValueOnce(page([wiki('a', 12), wiki('b', 11)]));
    await render('reopen');
    expect(rows()[0].querySelector('small')).toBeNull();
  });

  it('fills the entire graph without a scroll or a click', async () => {
    api.getWikiMemoryCatalog
      .mockResolvedValueOnce(page([]))
      .mockResolvedValueOnce(page([wiki('a', 12)], 'next'))
      .mockResolvedValueOnce(page([wiki('b', 11)]));
    await render();
    await click(Array.from(container.querySelectorAll('nav button')).find(button => button.textContent === '助手记忆'));
    expect(container.querySelector('.cc-wiki-catalog-count').textContent).toContain('2 篇');
    expect(api.getWikiMemoryCatalog.mock.calls.at(-1)[1].cursor).toBe('next');
    expect(api.getWikiMemoryDocument).not.toHaveBeenCalled();
  });

  it('discards displayed body and rechecks the active scope when the browser regains focus', async () => {
    api.getWikiMemoryCatalog.mockResolvedValueOnce(page([wiki('visible', 12)]));
    await render();
    await click(rows()[0]);
    expect(container.querySelector('article')).toBeTruthy();
    api.getWikiMemoryCatalog.mockResolvedValueOnce(page([]));
    await act(async () => window.dispatchEvent(new Event('focus')));
    expect(rows()).toHaveLength(0);
    expect(container.querySelector('article')).toBeNull();
    expect(api.getWikiMemoryCatalog).toHaveBeenCalledTimes(2);
  });
});
