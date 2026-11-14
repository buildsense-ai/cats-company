import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest';

const mocks = vi.hoisted(() => ({
  getMyBots: vi.fn(),
  listArtifactApps: vi.fn(),
  requestArtifactLaunch: vi.fn(),
  updateArtifactApp: vi.fn(),
  uploadFile: vi.fn(),
}));

vi.mock('../api', () => ({
  api: mocks,
}));

import AppsView, { isNewApplication } from './apps-view';

describe('AppsView', () => {
  let container;
  let root;

  beforeEach(() => {
    globalThis.localStorage?.clear();
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
    mocks.getMyBots.mockResolvedValue({
      bots: [{
        uid: 42,
        display_name: '中国传统文化分析顾问',
        role: '中国通信院',
        description: '面向外国友人，包含唐诗宋词元曲、四书五经。',
        relation: 'owner',
        is_owner: true,
      }, { uid: 43, display_name: '开发者', relation: 'friend' },
      { uid: 99, display_name: '陌生 Agent', relation: 'public' }],
    });
    mocks.listArtifactApps.mockImplementation(async (uid) => uid === '43' ? {
      apps: [{ id: 'friend-demo', title: '好友应用', url: 'https://artifact.catsco.cc/friend-demo/', updated_at: '2026-09-28T08:00:00Z' }],
    } : {
      apps: [{
        id: 'saturday-demo',
        can_manage: true,
        title: 'Saturday 演示应用',
        url: 'https://artifact.catsco.cc/saturday-demo/',
        updated_at: '2026-09-17T08:36:11.972Z',
      }],
    });
    mocks.requestArtifactLaunch.mockResolvedValue({
      launch_url: 'https://artifact.catsco.cc/_launch/saturday-demo?code=one-time',
    });
  });

  afterEach(() => {
    root.unmount();
    container.remove();
    vi.useRealTimers();
    vi.clearAllMocks();
  });

  const titles = () => [...container.querySelectorAll('.cc-app-card-heading-copy strong')].map((node) => node.textContent);

  test('keeps legacy Artifact URLs out of the new Applications page', () => {
    expect(isNewApplication({ url: 'https://artifact.catsco.cc/new-app/' })).toBe(true);
    expect(isNewApplication({ urls: ['https://artifact.catsco.cn/new-app/'] })).toBe(true);
    expect(isNewApplication({ url: 'https://agent-1071.artifacts.catsco.fun:19991/artifacts/old-app/latest/' })).toBe(false);
    expect(isNewApplication({ id: 'missing-url' })).toBe(false);
  });

  test('delays the loading motion for slow initial loading and replaces it with real cards', async () => {
    vi.useFakeTimers();
    let finish;
    const agents = { bots: [{ uid: 42, relation: 'owner' }] };
    mocks.getMyBots.mockImplementationOnce(() => new Promise((resolve) => { finish = resolve; }));
    await act(async () => { root.render(<AppsView user={{ uid: 7 }} />); });
    expect(container.querySelector('.cc-apps-page').getAttribute('aria-busy')).toBe('true');
    expect(container.querySelector('[data-loading-kind="apps"]')).toBeNull();
    await act(async () => { vi.advanceTimersByTime(149); });
    expect(container.querySelector('[data-loading-kind="apps"]')).toBeNull();
    await act(async () => { vi.advanceTimersByTime(1); });
    expect(container.querySelectorAll('.cc-load-stack-sheet')).toHaveLength(3);
    expect(container.querySelector('[data-loading-kind="apps"]').getAttribute('aria-hidden')).toBe('true');
    expect(container.querySelector('.cc-loading-state button')).toBeNull();
    await act(async () => { finish(agents); });
    expect(container.querySelector('[data-loading-kind="apps"]')).toBeNull();
    expect(titles()).toHaveLength(1);
    expect(container.querySelector('.cc-apps-page').getAttribute('aria-busy')).toBe('false');
  });

  test('shows each Agent application as soon as its request resolves', async () => {
    const finishes = {};
    mocks.listArtifactApps.mockImplementation((uid) => new Promise((resolve) => { finishes[uid] = resolve; }));
    await act(async () => { root.render(<AppsView user={{ uid: 7 }} />); });
    expect(titles()).toEqual([]);
    expect(container.textContent).toContain('正在读取 2 个');

    await act(async () => finishes['43']({ apps: [{ id: 'first', title: '先到的应用', url: 'https://artifact.catsco.cc/first/' }] }));
    expect(titles()).toEqual(['先到的应用']);
    expect(container.textContent).toContain('已加载 1 个 Agent，正在读取 1 个');

    await act(async () => {
      finishes['42']({ apps: [{ id: 'second', title: '后到的应用', url: 'https://artifact.catsco.cc/second/' }] });
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(titles()).toHaveLength(2);
    expect(titles()).toContain('先到的应用');
    expect(titles()).toContain('后到的应用');
    expect(container.querySelector('.cc-apps-loading-progress')).toBeNull();
  });

  test('never flashes the loading motion on a fast load', async () => {
    vi.useFakeTimers();
    await act(async () => { root.render(<AppsView user={{ uid: 7 }} />); });
    await act(async () => { vi.advanceTimersByTime(200); });
    expect(container.querySelector('[data-loading-kind="apps"]')).toBeNull();
    expect(titles()).toHaveLength(2);
  });

  test('keeps cards and count during refresh and updates without remounting the grid', async () => {
    vi.useFakeTimers();
    await act(async () => { root.render(<AppsView user={{ uid: 7 }} />); });
    const grid = container.querySelector('.cc-apps-results');
    const cards = titles();
    const finishes = [];
    mocks.listArtifactApps.mockImplementation(() => new Promise((resolve) => { finishes.push(resolve); }));
    await selectAgent('开发者');
    await act(async () => { container.querySelector('.cc-apps-refresh').click(); });
    await act(async () => { vi.advanceTimersByTime(200); });
    expect(titles()).toEqual([cards[0]]);
    expect(container.querySelector('.cc-apps-results')).toBe(grid);
    expect(container.querySelector('[data-loading-kind="apps"]')).toBeNull();
    expect(container.querySelector('.cc-apps-refresh .is-spinning')).toBeTruthy();
    await act(async () => { finishes.forEach((finish) => finish({ apps: [{ id: 'updated', title: 'Updated application', url: 'https://artifact.catsco.cc/updated/' }] })); });
    expect(titles()).toEqual(['Updated application']);
    expect(container.querySelector('.cc-apps-results')).toBe(grid);
    expect(container.querySelector('.cc-apps-refresh .is-spinning')).toBeNull();
  });

  test('replaces the initial loading motion with retry on failure and keeps existing cards on a failed refresh', async () => {
    vi.useFakeTimers();
    let reject;
    mocks.getMyBots.mockImplementationOnce(() => new Promise((_, fail) => { reject = fail; }));
    await act(async () => { root.render(<AppsView user={{ uid: 7 }} />); });
    await act(async () => { vi.advanceTimersByTime(150); });
    expect(container.querySelector('[data-loading-kind="apps"]')).toBeTruthy();
    await act(async () => { reject(new Error('offline')); });
    expect(container.querySelector('[data-loading-kind="apps"]')).toBeNull();
    expect(container.querySelector('[role="alert"]')).toBeTruthy();
    await act(async () => { container.querySelector('[role="alert"] button').click(); });
    const cards = titles();
    expect(cards).toHaveLength(2);
    mocks.getMyBots.mockRejectedValueOnce(new Error('offline'));
    await act(async () => { container.querySelector('.cc-apps-refresh').click(); });
    expect(titles()).toEqual(cards);
    expect(container.querySelector('[role="alert"]')).toBeTruthy();
  });

  async function selectAgent(label) {
    await act(async () => { container.querySelector('[aria-label="选择 Agent"]').click(); });
    await act(async () => {
      [...document.querySelectorAll('[role="option"]')].find((node) => node.textContent.startsWith(label)).click();
    });
  }

  test('defaults to 全部 and shows owned and friend apps newest first, never Agent cards', async () => {
    localStorage.setItem('catsco.skillhub.selectedBot.7', '42');
    await act(async () => {
      root.render(<AppsView user={{ uid: 7 }} />);
      await Promise.resolve();
    });

    expect(container.querySelector('.cc-agent-card')).toBeNull();
    expect(titles()).toEqual(['好友应用', 'Saturday 演示应用']);
    expect(container.querySelector('[aria-label="选择 Agent"]').textContent).toBe('全部 Agent');
    expect(mocks.listArtifactApps).toHaveBeenCalledWith('42');
    expect(mocks.listArtifactApps).toHaveBeenCalledWith('43');
    expect(mocks.listArtifactApps).not.toHaveBeenCalledWith('99');
    expect(container.querySelector('.cc-app-card-heading-copy > span').textContent).toBe('开发者');
  });

  test('filters manageable apps using server permissions and intersects with Agent selection', async () => {
    await act(async () => { root.render(<AppsView user={{ uid: 7 }} />); });
    expect(container.querySelectorAll('.cc-app-card-menu')).toHaveLength(1);
    await act(async () => { [...container.querySelectorAll('.cc-apps-scope button')].find((button) => button.textContent === '我可管理').click(); });
    expect(titles()).toEqual(['Saturday 演示应用']);
    await selectAgent('开发者');
    expect(titles()).toEqual([]);
    expect(container.textContent).toContain('当前范围内没有可管理的应用');
    await selectAgent('全部');
    expect(titles()).toEqual(['Saturday 演示应用']);
  });

  async function openEditor() {
    await act(async () => { root.render(<AppsView user={{ uid: 7 }} />); });
    await act(async () => { container.querySelector('.cc-app-card-menu summary').click(); });
    await act(async () => { container.querySelector('.cc-app-card-menu-items button').click(); });
    return document.querySelector('[role="dialog"]');
  }

  test('searches within the selected scope and clears back to the full list', async () => {
    await act(async () => { root.render(<AppsView user={{ uid: 7 }} />); });
    expect(container.querySelector('input[aria-label="搜索应用"]')).toBeNull();
    await act(async () => { container.querySelector('button[aria-label="搜索应用"]').click(); });
    const input = container.querySelector('input[aria-label="搜索应用"]');
    expect(document.activeElement).toBe(input);
    await act(async () => { Simulate.change(input, { target: { value: 'saturday' } }); });
    expect(titles()).toEqual(['Saturday 演示应用']);
    await selectAgent('开发者');
    expect(titles()).toEqual([]);
    expect(container.textContent).toContain('没有找到匹配的应用');
    await act(async () => { container.querySelector('[aria-label="清除应用搜索"]').click(); });
    expect(titles()).toEqual(['好友应用']);
    await act(async () => { Simulate.keyDown(input, { key: 'Escape' }); });
    expect(container.querySelector('input[aria-label="搜索应用"]')).toBeNull();
  });

  test('persists favorites per user and intersects favorites with the Agent filter', async () => {
    await act(async () => { root.render(<AppsView user={{ uid: 7 }} />); });
    await act(async () => { container.querySelector('.cc-app-card-favorite').click(); });
    const favoritesButton = () => [...container.querySelectorAll('.cc-apps-scope button')].find((button) => button.textContent === '收藏');
    await act(async () => { favoritesButton().click(); });
    expect(titles()).toEqual(['好友应用']);
    expect(mocks.requestArtifactLaunch).not.toHaveBeenCalled();
    await selectAgent('中国传统');
    expect(titles()).toEqual([]);
    await selectAgent('全部');
    await act(async () => { root.render(<AppsView key='remounted' user={{ uid: 7 }} />); });
    await act(async () => { favoritesButton().click(); });
    expect(titles()).toEqual(['好友应用']);
    await act(async () => { root.render(<AppsView user={{ uid: 8 }} />); });
    await act(async () => { favoritesButton().click(); });
    expect(titles()).toEqual([]);
    await act(async () => { root.render(<AppsView user={{ uid: 7 }} />); });
    await act(async () => { favoritesButton().click(); });
    expect(titles()).toEqual(['好友应用']);
    await act(async () => { container.querySelector('.cc-app-card-favorite').click(); });
    expect(titles()).toEqual([]);
    expect(JSON.parse(localStorage.getItem('catsco.apps.favorites.7'))).toEqual([]);
  });

  test('copies the gateway public address for sharing, never the conversation-bound viewer URL', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText } });

    await act(async () => { root.render(<AppsView user={{ uid: 7 }} />); });

    const shareButton = () => container.querySelector('button[aria-label^="复制 Saturday 演示应用"]');
    expect(shareButton()).not.toBeNull();
    await act(async () => { shareButton().click(); });

    // A recipient has no session, so the address must not carry this
    // conversation or the owning bot.
    expect(writeText).toHaveBeenCalledTimes(1);
    const copied = writeText.mock.calls.at(-1)[0];
    expect(copied).toBe('https://artifact.catsco.cc/saturday-demo/');
    expect(copied).not.toContain('artifact-viewer');
    expect(copied).not.toContain('topic');
    expect(copied).not.toContain('agent');

    // The card confirms in place instead of navigating anywhere.
    expect(container.querySelector('button[aria-label^="已复制 Saturday 演示应用"]')).not.toBeNull();
  });

  test('surfaces a copy failure with the address so the user can copy it manually', async () => {
    const writeText = vi.fn().mockRejectedValue(new Error('clipboard denied'));
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText } });

    await act(async () => { root.render(<AppsView user={{ uid: 7 }} />); });
    await act(async () => { container.querySelector('button[aria-label^="复制 Saturday 演示应用"]').click(); });

    expect(container.textContent).toContain('https://artifact.catsco.cc/saturday-demo/');
    expect(container.querySelector('button[aria-label^="已复制"]')).toBeNull();
  });

  test('saves shared metadata, uploads an icon and updates the card without launching the app', async () => {
    mocks.uploadFile.mockResolvedValue({ url: '/uploads/icon.png' });
    mocks.updateArtifactApp.mockResolvedValue({ id: 'saturday-demo', title: '订单工作台', description: '共享订单', icon_url: '/uploads/icon.png', can_manage: true });
    const dialog = await openEditor();
    await act(async () => {
      Simulate.change(dialog.querySelector('input:not([type="file"])'), { target: { value: '订单工作台' } });
      Simulate.change(dialog.querySelector('textarea'), { target: { value: '共享订单' } });
    });
    await act(async () => { Simulate.change(dialog.querySelector('input[type="file"]'), { target: { files: [new File(['image'], 'icon.png', { type: 'image/png' })], value: 'icon.png' } }); });
    await act(async () => { Simulate.submit(dialog.querySelector('form')); });
    expect(mocks.updateArtifactApp).toHaveBeenCalledWith('saturday-demo', { title: '订单工作台', description: '共享订单', icon_url: '/uploads/icon.png' });
    expect(document.querySelector('[role="dialog"]')).toBeNull();
    expect(titles()).toContain('订单工作台');
    expect(container.querySelector('.is-manageable .cc-app-card-avatar img').getAttribute('src')).toBe('/uploads/icon.png');
    expect(mocks.requestArtifactLaunch).not.toHaveBeenCalled();
  });

  test('keeps edits after a failed save and permits retry without optimistic success', async () => {
    mocks.updateArtifactApp.mockRejectedValueOnce(new Error('offline')).mockResolvedValueOnce({ title: 'New name', can_manage: true });
    const dialog = await openEditor();
    await act(async () => { Simulate.change(dialog.querySelector('input:not([type="file"])'), { target: { value: 'New name' } }); });
    await act(async () => { Simulate.submit(dialog.querySelector('form')); });
    expect(dialog.querySelector('[role="alert"]').textContent).toContain('修改内容已保留');
    expect(dialog.querySelector('input:not([type="file"])').value).toBe('New name');
    expect(titles()).toContain('Saturday 演示应用');
    await act(async () => { Simulate.submit(dialog.querySelector('form')); });
    expect(titles()).toContain('New name');
  });

  test('Escape closes the editor without saving and returns focus to its menu trigger', async () => {
    await openEditor();
    await act(async () => { document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true })); });
    expect(document.querySelector('[role="dialog"]')).toBeNull();
    expect(mocks.updateArtifactApp).not.toHaveBeenCalled();
    expect(document.activeElement).toBe(container.querySelector('.cc-app-card-menu summary'));
  });

  test('loads only the selected Agent applications and opens one inside the viewer', async () => {
    await act(async () => {
      root.render(<AppsView user={{ uid: 7 }} topicId="usr_123" />);
      await Promise.resolve();
    });

    await selectAgent('中国传统文化分析顾问');

    expect(mocks.listArtifactApps).toHaveBeenCalledWith('42');
    expect(titles()).toEqual(['Saturday 演示应用']);

    await act(async () => {
      container.querySelector('.cc-app-card-button').click();
      await Promise.resolve();
    });

    expect(mocks.requestArtifactLaunch).toHaveBeenCalledWith({
      app: 'saturday-demo',
      topic_id: 'usr_123',
    });
    expect(container.querySelector('.cc-apps-viewer-frame')?.getAttribute('src'))
      .toBe('https://artifact.catsco.cc/_launch/saturday-demo?code=one-time');
    expect(container.textContent).toContain('返回应用');
    await act(async () => { container.querySelector('[aria-label="返回应用列表"]').click(); });
    expect(titles()).toEqual(['Saturday 演示应用']);
    await selectAgent('全部');
    expect(titles()).toEqual(['好友应用', 'Saturday 演示应用']);
  });

  test('keeps available apps when one Agent fails, and retries the failed list', async () => {
    const original = mocks.listArtifactApps.getMockImplementation();
    mocks.listArtifactApps.mockImplementation(async (uid) => {
      if (uid === '43') throw new Error('offline');
      return original(uid);
    });
    await act(async () => { root.render(<AppsView user={{ uid: 7 }} />); });
    expect(titles()).toEqual(['Saturday 演示应用']);
    expect(container.querySelector('[role="alert"]')).toBeNull();
    await selectAgent('开发者');
    expect(titles()).toEqual([]);
    expect(container.querySelector('[role="alert"]').textContent).toContain('这个 Agent');
    mocks.listArtifactApps.mockImplementation(original);
    await act(async () => { container.querySelector('[role="alert"] button').click(); });
    expect(titles()).toEqual(['好友应用']);
    expect(container.querySelector('[role="alert"]')).toBeNull();
  });

  test('silently skips a legacy-only Agent when its new catalog is unavailable', async () => {
    const original = mocks.listArtifactApps.getMockImplementation();
    mocks.listArtifactApps.mockImplementation(async (uid) => {
      if (uid === '43') {
        const error = new Error('not found');
        error.status = 404;
        throw error;
      }
      return original(uid);
    });
    await act(async () => { root.render(<AppsView user={{ uid: 7 }} />); });
    expect(titles()).toEqual(['Saturday 演示应用']);
    expect(container.querySelector('[role="alert"]')).toBeNull();
  });

  test('removes stale cards when an Agent now reports that it only has the legacy catalogue', async () => {
    await act(async () => { root.render(<AppsView user={{ uid: 7 }} />); });
    const original = mocks.listArtifactApps.getMockImplementation();
    mocks.listArtifactApps.mockImplementation(async (uid) => {
      if (uid === '43') throw Object.assign(new Error('not found'), { status: 404 });
      return { apps: [] };
    });
    await act(async () => { container.querySelector('.cc-apps-refresh').click(); });
    expect(titles()).toEqual([]);
    expect(container.querySelector('[role="alert"]')).toBeNull();
    mocks.listArtifactApps.mockImplementation(original);
  });

  test('does not claim to show remaining apps when every Agent request fails', async () => {
    mocks.listArtifactApps.mockRejectedValue(new Error('backend unavailable'));
    await act(async () => { root.render(<AppsView user={{ uid: 7 }} />); });
    expect(titles()).toEqual([]);
    expect(container.querySelector('[role="alert"]').textContent).toContain('共享应用暂时无法加载');
    expect(container.textContent).not.toContain('已展示其余应用');
  });

  test('handles empty lists and missing timestamps while keeping dated apps first', async () => {
    mocks.listArtifactApps.mockImplementation(async (uid) => ({ apps: uid === '43' ? [] : [
      { id: 'undated', title: '无日期', url: 'https://artifact.catsco.cc/undated/' },
      { id: 'created', title: '新建应用', url: 'https://artifact.catsco.cc/created/', created_at: '2026-09-20T00:00:00Z' },
    ] }));
    await act(async () => { root.render(<AppsView user={{ uid: 7 }} />); });
    expect(titles()).toEqual(['新建应用', '无日期']);
    await selectAgent('开发者');
    expect(container.textContent).toContain('这个 Agent 还没有共享应用');
    expect(container.querySelector('[role="alert"]')).toBeNull();
  });

  test('ignores a previous user request that completes after switching accounts', async () => {
    let resolveOld;
    mocks.getMyBots.mockImplementationOnce(() => new Promise((resolve) => { resolveOld = resolve; }));
    await act(async () => { root.render(<AppsView user={{ uid: 7 }} />); });
    mocks.getMyBots.mockResolvedValue({ bots: [] });
    await act(async () => { root.render(<AppsView user={{ uid: 8 }} />); });
    await act(async () => { resolveOld({ bots: [{ uid: 42, relation: 'owner' }] }); });
    expect(titles()).toEqual([]);
    expect(mocks.listArtifactApps).not.toHaveBeenCalled();
  });

  test('keeps the latest app selection when an older launch finishes later', async () => {
    const finish = {};
    mocks.requestArtifactLaunch.mockImplementation(({ app }) => new Promise((resolve) => { finish[app] = resolve; }));
    await act(async () => { root.render(<AppsView user={{ uid: 7 }} />); });
    await act(async () => { container.querySelector('[aria-label="打开应用 Saturday 演示应用"]').click(); });
    await act(async () => { container.querySelector('[aria-label="打开应用 好友应用"]').click(); });
    await act(async () => { finish['friend-demo']({ launch_url: 'https://artifact.catsco.cc/friend-demo/?code=new' }); });
    expect(container.querySelector('iframe').title).toBe('好友应用');
    expect(container.querySelector('iframe').getAttribute('sandbox').split(' ')).toContain('allow-downloads');
    await act(async () => { finish['saturday-demo']({ launch_url: 'https://artifact.catsco.cc/saturday-demo/?code=old' }); });
    expect(container.querySelector('iframe').title).toBe('好友应用');
  });

  test('cancels pending launches when returning to the list or leaving the page', async () => {
    const openWindow = vi.spyOn(window, 'open').mockImplementation(() => null);
    try {
      await act(async () => { root.render(<AppsView user={{ uid: 7 }} />); });
      await act(async () => { container.querySelector('[aria-label="打开应用 好友应用"]').click(); });
      let finish;
      mocks.requestArtifactLaunch.mockImplementation(() => new Promise((resolve) => { finish = resolve; }));
      await act(async () => { container.querySelector('.cc-apps-viewer-external').click(); });
      await act(async () => { container.querySelector('[aria-label="返回应用列表"]').click(); });
      await act(async () => { finish({ launch_url: 'https://artifact.catsco.cc/friend-demo/?code=late' }); });
      expect(openWindow).not.toHaveBeenCalled();
      expect(container.querySelector('iframe')).toBeNull();
      await act(async () => { container.querySelector('[aria-label="在新页面打开 好友应用"]').click(); });
      await act(async () => { root.render(null); });
      await act(async () => { finish({ launch_url: 'https://artifact.catsco.cc/friend-demo/?code=unmounted' }); });
      expect(openWindow).not.toHaveBeenCalled();
    } finally {
      openWindow.mockRestore();
    }
  });

  test('restores cached cards, filters and the list scroll while refreshing in the background', async () => {
    const viewStateRef = { current: null };
    await act(async () => { root.render(<AppsView user={{ uid: 7 }} viewStateRef={viewStateRef} />); });
    await selectAgent('中国传统');
    await act(async () => { [...container.querySelectorAll('.cc-apps-scope button')].find((button) => button.textContent === '我可管理').click(); });
    await act(async () => { container.querySelector('button[aria-label="搜索应用"]').click(); });
    await act(async () => { Simulate.change(container.querySelector('[aria-label="搜索应用"]'), { target: { value: 'Saturday' } }); });
    const list = container.querySelector('[aria-label="共享应用"]');
    list.scrollTop = 360;
    await act(async () => { Simulate.scroll(list); });
    await act(async () => { root.render(null); });
    let rejectRefresh;
    mocks.getMyBots.mockImplementationOnce(() => new Promise((_, reject) => { rejectRefresh = reject; }));
    await act(async () => { root.render(<AppsView user={{ uid: 7 }} viewStateRef={viewStateRef} />); });
    expect(titles()).toEqual(['Saturday 演示应用']);
    expect(container.querySelector('[aria-label="搜索应用"]').value).toBe('Saturday');
    expect(container.querySelector('[aria-label="选择 Agent"]').textContent).toBe('中国传统文化分析顾问');
    expect(container.querySelector('.cc-apps-scope [aria-pressed="true"]').textContent).toBe('我可管理');
    expect(container.querySelector('[aria-label="共享应用"]').scrollTop).toBe(360);
    expect(container.querySelector('[data-loading-kind="apps"]')).toBeNull();
    expect(container.querySelector('[aria-label="共享应用"]').getAttribute('aria-busy')).toBe('true');
    await act(async () => { rejectRefresh(new Error('offline')); });
    expect(titles()).toEqual(['Saturday 演示应用']);
    expect(container.querySelector('[role="alert"]')).not.toBeNull();
  });

  test('returns to the same favorite list without caching viewer credentials or dialogs', async () => {
    const viewStateRef = { current: null };
    await act(async () => { root.render(<AppsView user={{ uid: 7 }} viewStateRef={viewStateRef} />); });
    await act(async () => { container.querySelector('[aria-label="收藏 好友应用"]').click(); });
    await act(async () => { [...container.querySelectorAll('.cc-apps-scope button')].find((button) => button.textContent === '收藏').click(); });
    container.querySelector('[aria-label="共享应用"]').scrollTop = 240;
    await act(async () => { container.querySelector('[aria-label="打开应用 好友应用"]').click(); });
    expect(container.querySelector('iframe')).not.toBeNull();
    expect(JSON.stringify(viewStateRef.current)).not.toContain('one-time');
    expect(viewStateRef.current).not.toHaveProperty('selectedApp');
    expect(viewStateRef.current).not.toHaveProperty('editingApp');
    await act(async () => { container.querySelector('[aria-label="返回应用列表"]').click(); });
    expect(container.querySelector('[aria-label="共享应用"]').scrollTop).toBe(240);
    await act(async () => { root.render(null); });
    mocks.getMyBots.mockImplementationOnce(() => new Promise(() => {}));
    await act(async () => { root.render(<AppsView user={{ uid: 7 }} viewStateRef={viewStateRef} />); });
    expect(container.querySelector('iframe')).toBeNull();
    expect(titles()).toEqual(['好友应用']);
    expect(container.querySelector('.cc-apps-scope [aria-pressed="true"]').textContent).toBe('收藏');
    expect(container.querySelector('[aria-label="共享应用"]').scrollTop).toBe(240);
  });

  test('never restores another account cache or a launch pending for that account', async () => {
    const viewStateRef = { current: null };
    await act(async () => { root.render(<AppsView user={{ uid: 7 }} viewStateRef={viewStateRef} />); });
    await act(async () => { container.querySelector('button[aria-label="搜索应用"]').click(); });
    await act(async () => { Simulate.change(container.querySelector('[aria-label="搜索应用"]'), { target: { value: 'Saturday' } }); });
    let finishLaunch;
    mocks.requestArtifactLaunch.mockImplementationOnce(() => new Promise((resolve) => { finishLaunch = resolve; }));
    await act(async () => { container.querySelector('.cc-app-card-button').click(); });
    mocks.getMyBots.mockImplementationOnce(() => new Promise(() => {}));
    await act(async () => { root.render(<AppsView user={{ uid: 8 }} viewStateRef={viewStateRef} />); });
    expect(titles()).toEqual([]);
    expect(container.querySelector('[aria-label="搜索应用"]').value).toBe('');
    expect(viewStateRef.current.uid).toBe('8');
    await act(async () => { finishLaunch({ launch_url: 'https://artifact.catsco.cc/saturday-demo/?code=old-user' }); });
    expect(container.querySelector('iframe')).toBeNull();
  });

  test('retains previously loaded Agent cards on transient refresh failures but removes revoked access', async () => {
    await act(async () => { root.render(<AppsView user={{ uid: 7 }} />); });
    mocks.listArtifactApps.mockImplementation(async (uid) => {
      if (uid === '43') throw new Error('offline');
      return { apps: [] };
    });
    await act(async () => { container.querySelector('.cc-apps-refresh').click(); });
    expect(titles()).toEqual(['好友应用']);
    expect(container.querySelector('[role="alert"]')).toBeNull();
    mocks.listArtifactApps.mockRejectedValue(Object.assign(new Error('no access'), { status: 403 }));
    await act(async () => { container.querySelector('.cc-apps-refresh').click(); });
    expect(titles()).toEqual([]);
  });

  test.each([401, 403])('clears cached catalogue data when the Agent roster rejects access (%s)', async (status) => {
    const viewStateRef = { current: null };
    await act(async () => { root.render(<AppsView user={{ uid: 7 }} viewStateRef={viewStateRef} />); });
    expect(titles()).toHaveLength(2);
    await selectAgent('开发者');
    mocks.getMyBots.mockRejectedValueOnce(Object.assign(new Error('no access'), { status }));
    await act(async () => { container.querySelector('.cc-apps-refresh').click(); });
    expect(titles()).toEqual([]);
    expect(container.querySelector('[aria-label="选择 Agent"]').textContent).toBe('全部 Agent');
    expect(viewStateRef.current.apps).toEqual([]);
    expect(viewStateRef.current.agents).toEqual([]);
    expect(container.querySelector('[role="alert"]')).not.toBeNull();
    await act(async () => { root.render(null); });
    mocks.getMyBots.mockImplementationOnce(() => new Promise(() => {}));
    await act(async () => { root.render(<AppsView user={{ uid: 7 }} viewStateRef={viewStateRef} />); });
    expect(titles()).toEqual([]);
  });
});
