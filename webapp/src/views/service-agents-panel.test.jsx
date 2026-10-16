import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const mocks = vi.hoisted(() => ({
  getServiceAgentsCatalog: vi.fn(),
  addServiceAgentSubscription: vi.fn(),
  removeServiceAgentSubscription: vi.fn(),
}));

vi.mock('../api', () => ({
  api: mocks,
}));

import ServiceAgentsPanel from './service-agents-panel';

describe('ServiceAgentsPanel', () => {
  let container;
  let root;

  const catalog = () => ({
    agents: [
      { slug: 'hello', name: 'Hello 试点应用', version: 3, hidden: false },
      { slug: 'weekly-report', name: '周报助手', version: 4, hidden: true },
    ],
  });

  beforeEach(() => {
    global.IS_REACT_ACT_ENVIRONMENT = true;
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
    mocks.getServiceAgentsCatalog.mockResolvedValue(catalog());
    mocks.addServiceAgentSubscription.mockResolvedValue({ slug: 'weekly-report', hidden: false });
    mocks.removeServiceAgentSubscription.mockResolvedValue({ slug: 'hello', hidden: true });
  });

  afterEach(async () => {
    await act(async () => root.unmount());
    container.remove();
    vi.clearAllMocks();
  });

  const renderPanel = async (props = {}) => {
    await act(async () => {
      root.render(<ServiceAgentsPanel onClose={() => {}} {...props} />);
      await Promise.resolve();
      await Promise.resolve();
    });
  };

  it('loads the catalog and renders agent rows with subscription state', async () => {
    await renderPanel();
    const rows = container.querySelectorAll('.v3-service-agent-row');
    expect(rows.length).toBe(2);
    expect(container.textContent).toContain('Hello 试点应用');
    expect(container.textContent).toContain('catsco_hello');
    expect(container.textContent).toContain('v4');
    // 已添加的显示"删除"，已移除的显示"添加"
    expect(container.querySelector('button[aria-label="删除Hello 试点应用"]')).toBeTruthy();
    expect(container.querySelector('button[aria-label="添加周报助手"]')).toBeTruthy();
    expect(rows[1].className).toContain('is-hidden');
  });

  it('removes a subscription and flips the row state', async () => {
    await renderPanel();
    const remove = container.querySelector('button[aria-label="删除Hello 试点应用"]');
    await act(async () => {
      remove.click();
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(mocks.removeServiceAgentSubscription).toHaveBeenCalledWith('hello');
    expect(container.querySelector('button[aria-label="添加Hello 试点应用"]')).toBeTruthy();
    expect(container.querySelector('.v3-service-agent-row')?.className).toContain('is-hidden');
  });

  it('adds a subscription back and shows the failure message on error', async () => {
    await renderPanel();
    const add = container.querySelector('button[aria-label="添加周报助手"]');
    await act(async () => {
      add.click();
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(mocks.addServiceAgentSubscription).toHaveBeenCalledWith('weekly-report');
    expect(container.querySelector('button[aria-label="删除周报助手"]')).toBeTruthy();

    // 失败路径：恢复显示错误，状态不变
    mocks.removeServiceAgentSubscription.mockRejectedValueOnce(new Error('删除失败，请稍后重试。'));
    const remove = container.querySelector('button[aria-label="删除周报助手"]');
    await act(async () => {
      remove.click();
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(container.querySelector('[role="alert"]')?.textContent).toContain('删除失败');
    expect(container.querySelector('button[aria-label="删除周报助手"]')).toBeTruthy();
  });

  it('renders close button wired to onClose and refresh re-fetches', async () => {
    const onClose = vi.fn();
    await renderPanel({ onClose });
    const close = container.querySelector('button[aria-label="关闭 Agent 工具"]');
    await act(async () => close.click());
    expect(onClose).toHaveBeenCalledTimes(1);

    const refresh = container.querySelector('button[aria-label="刷新 Agent 工具"]');
    await act(async () => {
      refresh.click();
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(mocks.getServiceAgentsCatalog.mock.calls.length).toBeGreaterThanOrEqual(2);
  });
});
