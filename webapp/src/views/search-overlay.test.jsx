import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';
import { describe, expect, it, beforeEach, afterEach, vi } from 'vitest';
import SearchOverlay, { normalizeSearchResult } from './search-overlay';
import { api } from '../api';

vi.mock('../api', () => ({
  api: { getMessageSearch: vi.fn(), getConversations: vi.fn() },
}));

function flush() {
  return act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });
}

describe('SearchOverlay', () => {
  let root;
  let container;
  let onClose;
  let onSelectResult;

  beforeEach(() => {
    vi.useFakeTimers();
    api.getMessageSearch.mockReset();
    api.getConversations.mockReset().mockResolvedValue({ conversations: [] });
    onClose = vi.fn();
    onSelectResult = vi.fn();
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(async () => {
    await act(async () => root.unmount());
    container.remove();
    vi.useRealTimers();
  });

  const render = async (props = {}) => {
    await act(async () => root.render(<SearchOverlay open onClose={onClose} onSelectResult={onSelectResult} {...props} />));
  };

  it('normalizes the server artifact response contract', () => {
    const result = normalizeSearchResult({
      message_id: 12,
      topic_id: 'grp_8',
      topic_name: '项目群',
      content: '<img src=x onerror=alert(1)> Supabase',
      artifact_name: 'report.pdf',
      content_type: 'artifact',
    });
    expect(result).toMatchObject({ topicId: 'grp_8', messageId: 12, source: '项目群', category: 'artifact', attachmentName: 'report.pdf' });
  });

  it('highlights matching text with React fragments without rendering unsafe HTML', async () => {
    api.getMessageSearch.mockResolvedValue({
      results: [{
        message_id: 12,
        topic_id: 'grp_8',
        topic_name: '项目群',
        content: '<img src=x onerror=alert(1)> Supabase result',
      }],
    });
    await render();
    const input = container.querySelector('input');
    await act(async () => { Simulate.change(input, { target: { value: 'supabase' } }); });
    await act(async () => vi.advanceTimersByTime(300));
    await flush();

    expect(container.querySelector('img')).toBeNull();
    expect(container.querySelector('mark')?.textContent).toBe('Supabase');
    expect(container.querySelector('.cc-global-search-result-snippet')?.textContent).toContain('<img src=x onerror=alert(1)>');
  });

  it('does not search before two characters and debounces the request', async () => {
    api.getMessageSearch.mockResolvedValue({ results: [] });
    await render();
    const input = container.querySelector('input');
    await act(async () => { Simulate.change(input, { target: { value: 'a' } }); });
    await act(async () => { vi.advanceTimersByTime(500); });
    expect(api.getMessageSearch).not.toHaveBeenCalled();
    await act(async () => { Simulate.change(input, { target: { value: 'ab' } }); });
    expect(api.getMessageSearch).not.toHaveBeenCalled();
    await act(async () => { vi.advanceTimersByTime(299); });
    expect(api.getMessageSearch).not.toHaveBeenCalled();
    await act(async () => { vi.advanceTimersByTime(1); });
    expect(api.getMessageSearch).toHaveBeenCalledWith('ab', 'all', expect.objectContaining({ signal: expect.any(AbortSignal) }));
  });

  it('passes the selected category and selects a normalized result', async () => {
    api.getMessageSearch.mockResolvedValue({ results: [{ message_id: 7, topic_id: 'p2p_1_2', topic_name: '小明', content: 'hello' }] });
    await render();
    const input = container.querySelector('input');
    await act(async () => { Simulate.change(input, { target: { value: 'hello' } }); });
    await act(async () => vi.advanceTimersByTime(300));
    await flush();
    const artifactTab = [...container.querySelectorAll('[role="tab"]')].find((node) => node.textContent === '产物');
    await act(async () => artifactTab.click());
    await act(async () => vi.advanceTimersByTime(300));
    await flush();
    expect(api.getMessageSearch).toHaveBeenLastCalledWith('hello', 'artifact', expect.anything());
    const resultButton = container.querySelector('.cc-global-search-result');
    await act(async () => resultButton.click());
    expect(onSelectResult).toHaveBeenCalledWith(expect.objectContaining({ topicId: 'p2p_1_2', messageId: 7 }));
  });

  it('ignores a late response from an older query', async () => {
    let resolveOld;
    let resolveNew;
    api.getMessageSearch
      .mockImplementationOnce(() => new Promise((resolve) => { resolveOld = resolve; }))
      .mockImplementationOnce(() => new Promise((resolve) => { resolveNew = resolve; }));
    await render();
    const input = container.querySelector('input');
    await act(async () => { Simulate.change(input, { target: { value: 'old' } }); });
    await act(async () => vi.advanceTimersByTime(300));
    await act(async () => { Simulate.change(input, { target: { value: 'new' } }); });
    await act(async () => vi.advanceTimersByTime(300));
    await act(async () => { resolveOld({ results: [{ message_id: 1, topic_id: 'old', content: 'old result' }] }); await Promise.resolve(); });
    expect(container.textContent).not.toContain('old result');
    await act(async () => { resolveNew({ results: [{ message_id: 2, topic_id: 'new', content: 'new result' }] }); await Promise.resolve(); });
    expect(container.textContent).toContain('new result');
  });

  it('keeps global search within message and artifact scope', async () => {
    api.getMessageSearch.mockResolvedValue({ results: [] });
    api.getConversations.mockResolvedValue({
      conversations: [{ topic_id: 'grp_9', name: 'Supabase 项目群', is_group: true, group_id: 9 }],
    });
    await render();
    const input = container.querySelector('input');
    await act(async () => { Simulate.change(input, { target: { value: 'supabase' } }); });
    await act(async () => vi.advanceTimersByTime(300));
    await flush();

    expect(api.getConversations).not.toHaveBeenCalled();
    expect(container.textContent).not.toContain('Supabase 项目群');
  });

  it('navigates results with arrow keys and selects the focused result with Enter', async () => {
    api.getMessageSearch.mockResolvedValue({
      results: [
        { message_id: 7, topic_id: 'p2p_1_2', topic_name: '小明', content: 'first hello' },
        { message_id: 8, topic_id: 'p2p_1_3', topic_name: '小红', content: 'second hello' },
        { message_id: 9, topic_id: 'p2p_1_4', topic_name: '小蓝', content: 'third hello' },
      ],
    });
    await render();
    const input = container.querySelector('input');
    await act(async () => { Simulate.change(input, { target: { value: 'hello' } }); });
    await act(async () => vi.advanceTimersByTime(300));
    await flush();
    const resultButtons = [...container.querySelectorAll('.cc-global-search-result')];

    await act(async () => {
      document.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowUp', bubbles: true, cancelable: true }));
    });
    expect(document.activeElement).toBe(resultButtons[2]);

    await act(async () => {
      document.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true, cancelable: true }));
    });
    expect(document.activeElement).toBe(resultButtons[0]);

    await act(async () => {
      document.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true, cancelable: true }));
    });
    expect(document.activeElement).toBe(resultButtons[1]);

    await act(async () => {
      document.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowUp', bubbles: true, cancelable: true }));
      document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }));
    });
    expect(onSelectResult).toHaveBeenCalledWith(expect.objectContaining({ messageId: 7 }));
  });

  it('closes on Escape and returns null when closed', async () => {
    await render();
    await act(async () => {
      document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
    });
    expect(onClose).toHaveBeenCalledTimes(1);
    await render({ open: false });
    expect(container.innerHTML).toBe('');
  });
});
