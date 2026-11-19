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

  beforeEach(() => {
    vi.resetAllMocks();
    localStorage.clear();
    globalThis.IS_REACT_ACT_ENVIRONMENT = true;
    container = document.createElement('div');
    document.body.append(container);
    root = createRoot(container);
    api.getWikiMemoryDocument.mockImplementation(async (_, id) => ({ id, revision: 'v1', markdown: '## Actual **Markdown**' }));
  });

  afterEach(async () => {
    await act(async () => root.unmount());
    container.remove();
  });

  async function render(key = 'first') {
    await act(async () => root.render(<WikiMemoryPanel key={key} viewerUid="2" topicId="grp_7" agents={[{ uid: '50', name: 'Helper' }]} onClose={() => {}} />));
  }

  async function click(button) {
    expect(button).toBeTruthy();
    await act(async () => button.click());
  }

  const rows = () => Array.from(container.querySelectorAll('.cc-wiki-catalog:not([hidden]) button.cc-wiki-row'));

  it('opens the current conversation in newest-first order and fetches Markdown only when expanded', async () => {
    api.getWikiMemoryCatalog.mockResolvedValue(page([wiki('older', 11), wiki('newer', 12)]));
    await render();
    expect(rows().map(el => el.querySelector('span').textContent)).toEqual(['Wiki newer', 'Wiki older']);
    expect(api.getWikiMemoryCatalog.mock.calls[0][1].topicId).toBe('grp_7');
    expect(api.getWikiMemoryDocument).not.toHaveBeenCalled();
    await click(rows()[0]);
    expect(container.querySelector('article h2 strong')?.textContent).toBe('Markdown');
    await click(rows()[0]);
    await click(rows()[0]);
    expect(api.getWikiMemoryDocument).toHaveBeenCalledTimes(1);
  });

  it('loads the all-conversation catalog once and shares wiki documents between graph and folded lists', async () => {
    api.getWikiMemoryCatalog.mockResolvedValue(page([wiki('older', 11), wiki('newer', 12)]));
    await render();
    await click(Array.from(container.querySelectorAll('nav button')).find(button => button.textContent === '助手记忆'));
    expect(api.getWikiMemoryCatalog.mock.calls[1][1].topicId).toBeUndefined();
    expect(container.querySelector('.cc-wiki-cluster-label').textContent).toBe('设计讨论');
    expect(container.querySelectorAll('.cc-wiki-node')).toHaveLength(2);
    await click(container.querySelector('.cc-wiki-node[aria-label="Wiki newer"]'));
    await click(container.querySelector('button[aria-label="列表"]'));
    const group = container.querySelector('.cc-wiki-groups details');
    expect(group.open).toBe(true);
    expect(group.querySelector('article').textContent).toContain('Markdown');
    expect(api.getWikiMemoryCatalog).toHaveBeenCalledTimes(2);
    expect(api.getWikiMemoryDocument).toHaveBeenCalledTimes(1);
  });

  it('marks only new rows, consumes only displayed pages, and keeps an empty initial baseline', async () => {
    api.getWikiMemoryCatalog.mockResolvedValueOnce(page([]));
    await render('initial-empty');
    api.getWikiMemoryCatalog.mockResolvedValueOnce(page([wiki('a', 12)], 'older-page'));
    await render('after-a');
    expect(rows()[0].querySelector('small')?.textContent).toBe('新增');
    api.getWikiMemoryCatalog.mockResolvedValueOnce(page([wiki('a', 12)], 'older-page'));
    await render('reopen');
    expect(rows()[0].querySelector('small')).toBeNull();
    api.getWikiMemoryCatalog.mockResolvedValueOnce(page([wiki('b', 11)]));
    await click(container.querySelector('button.cc-wiki-more'));
    expect(rows()[1].querySelector('small')?.textContent).toBe('新增');
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
