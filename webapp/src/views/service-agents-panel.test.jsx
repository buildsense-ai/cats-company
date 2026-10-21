import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const mocks = vi.hoisted(() => ({
  getServiceAgentsCatalog: vi.fn(),
  addServiceAgentSubscription: vi.fn(),
  removeServiceAgentSubscription: vi.fn(),
  publishServiceAgent: vi.fn(),
  getMyBots: vi.fn(),
  getBotDefinitionPrompt: vi.fn(),
  updateBotDefinitionServiceAgents: vi.fn(),
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
    mocks.getMyBots.mockResolvedValue({ bots: [{ uid: 63, name: '我的助手' }] });
    mocks.publishServiceAgent.mockResolvedValue({ slug: 'my-agent', version: 1, visibility: 'private' });
    mocks.getBotDefinitionPrompt.mockResolvedValue({ configured: true, revision: 5, definition: { botId: '63' } });
    mocks.updateBotDefinitionServiceAgents.mockResolvedValue({
      configured: true,
      revision: 6,
      definition: { botId: '63', serviceAgents: ['hello'] },
    });
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

  it('scopes tools per Agent: 未配置=全部可见，切换后按清单保存', async () => {
    await renderPanel();
    const select = container.querySelector('#v3-service-agents-scope-select');
    expect(select).toBeTruthy();
    expect(select.options.length).toBe(2); // 账号 + 我的助手

    await act(async () => {
      select.value = '63';
      select.dispatchEvent(new Event('change', { bubbles: true }));
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(mocks.getBotDefinitionPrompt).toHaveBeenCalledWith('63');
    // 未配置 serviceAgents → 账号可见的 hello 视为已选；账号已移除的 weekly-report 不可选
    expect(container.querySelector('button[aria-label="删除Hello 试点应用"]')).toBeTruthy();
    const weekly = container.querySelector('button[aria-label="添加周报助手"]');
    expect(weekly).toBeTruthy();
    expect(weekly.disabled).toBe(true);

    // 从该 Agent 移除 hello → 保存空清单
    mocks.updateBotDefinitionServiceAgents.mockResolvedValueOnce({
      configured: true,
      revision: 6,
      definition: { botId: '63', serviceAgents: [] },
    });
    const remove = container.querySelector('button[aria-label="删除Hello 试点应用"]');
    await act(async () => {
      remove.click();
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(mocks.updateBotDefinitionServiceAgents).toHaveBeenCalledWith('63', 5, []);
    expect(container.querySelector('button[aria-label="添加Hello 试点应用"]')).toBeTruthy();
  });

  it('空目录时给出引导式空态而不是一句"暂无可用工具"', async () => {
    mocks.getServiceAgentsCatalog.mockResolvedValue({ agents: [] });
    await renderPanel();
    expect(container.querySelector('.v3-service-agents-list')).toBeNull();
    const placeholder = container.querySelector('.v3-service-agents-placeholder');
    expect(placeholder).toBeTruthy();
    expect(placeholder.textContent).toContain('还没有可用的 Agent 工具');
    expect(placeholder.textContent).toContain('catsco_');
    const refresh = placeholder.querySelector('button');
    expect(refresh.textContent).toContain('刷新目录');
    await act(async () => {
      refresh.click();
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(mocks.getServiceAgentsCatalog.mock.calls.length).toBeGreaterThanOrEqual(2);
  });

  it('加载中显示骨架屏；失败时给出重试并可从错误态恢复', async () => {
    let resolveCatalog;
    mocks.getServiceAgentsCatalog.mockImplementation(() => new Promise((resolve) => {
      resolveCatalog = resolve;
    }));
    await renderPanel();
    expect(container.querySelector('.v3-service-agents-skeleton')).toBeTruthy();

    await act(async () => {
      resolveCatalog(Promise.reject(new Error('目录加载失败')));
      await Promise.resolve();
      await Promise.resolve();
    });
    const alert = container.querySelector('[role="alert"]');
    expect(alert?.textContent).toContain('目录加载失败');
    const retry = alert.querySelector('button');
    expect(retry.textContent).toContain('重试');

    mocks.getServiceAgentsCatalog.mockResolvedValue(catalog());
    await act(async () => {
      retry.click();
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(container.querySelectorAll('.v3-service-agent-row').length).toBe(2);
    expect(container.querySelector('[role="alert"]')).toBeNull();
  });

  it('没有 Agent 时配置范围降级为静态说明，不渲染只有一个选项的下拉', async () => {
    mocks.getMyBots.mockResolvedValue({ bots: [] });
    await renderPanel();
    expect(container.querySelector('#v3-service-agents-scope-select')).toBeNull();
    const staticScope = container.querySelector('.v3-service-agents-scope-control.is-static');
    expect(staticScope).toBeTruthy();
    expect(staticScope.textContent).toContain('账号（所有 Agent 共用）');
  });

  it('从面板直接发布应用：填名称自动生成调用名，提交后给出 catsco_<slug>', async () => {
    await renderPanel();
    const openBtn = container.querySelector('button[aria-label="发布应用"]');
    expect(openBtn).toBeTruthy();
    await act(async () => {
      openBtn.click();
      await Promise.resolve();
    });
    const form = container.querySelector('.v3-service-agents-publish');
    expect(form).toBeTruthy();

    const [nameInput, slugInput] = form.querySelectorAll('input');
    await act(async () => {
      const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value').set;
      setter.call(nameInput, '我的助手');
      nameInput.dispatchEvent(new Event('input', { bubbles: true }));
      await Promise.resolve();
    });
    // 中文名无法自动转写：slug 留空，由用户手填英文调用名（表单会校验）。
    expect(slugInput.value).toBe('');
    await act(async () => {
      const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value').set;
      setter.call(slugInput, 'my-agent');
      slugInput.dispatchEvent(new Event('input', { bubbles: true }));
      await Promise.resolve();
    });

    await act(async () => {
      form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(mocks.publishServiceAgent).toHaveBeenCalledTimes(1);
    const payload = mocks.publishServiceAgent.mock.calls[0][0];
    expect(payload).toMatchObject({ slug: 'my-agent', name: '我的助手', visibility: 'private' });
    expect(payload.definition).toMatchObject({ name: '我的助手', runner: { type: 'worker' } });

    const banner = container.querySelector('.v3-service-agents-published');
    expect(banner?.textContent).toContain('catsco_my-agent');
    expect(banner?.textContent).toContain('service-run-worker -agents my-agent');
  });

  it('发布表单会校验调用名，非法时不发请求', async () => {
    await renderPanel();
    await act(async () => {
      container.querySelector('button[aria-label="发布应用"]').click();
      await Promise.resolve();
    });
    const form = container.querySelector('.v3-service-agents-publish');
    const [nameInput, slugInput] = form.querySelectorAll('input');
    const setValue = (node, value) => {
      const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value').set;
      setter.call(node, value);
      node.dispatchEvent(new Event('input', { bubbles: true }));
    };
    await act(async () => {
      setValue(nameInput, 'My Agent');
      setValue(slugInput, 'BAD SLUG!');
      await Promise.resolve();
    });
    await act(async () => {
      form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
      await Promise.resolve();
    });
    expect(mocks.publishServiceAgent).not.toHaveBeenCalled();
    expect(container.querySelector('.v3-service-agents-publish [role="alert"]')?.textContent).toContain('调用名');
  });

  it('展示归属徽标与工具计数', async () => {    mocks.getServiceAgentsCatalog.mockResolvedValue({
      agents: [
        { slug: 'mine-app', name: '我的应用', version: 1, hidden: false, mine: true, visibility: 'private' },
      ],
    });
    await renderPanel();
    expect(container.querySelector('.v3-service-agents-count')?.textContent).toBe('1');
    expect(container.querySelector('.v3-service-agent-badge')?.textContent).toBe('我的');
    expect(container.querySelector('.v3-service-agent-badge.is-private')?.textContent).toBe('私有');
  });
});
