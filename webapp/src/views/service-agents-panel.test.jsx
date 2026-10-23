import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const mocks = vi.hoisted(() => ({
  getServiceAgentsCatalog: vi.fn(),
  addServiceAgentSubscription: vi.fn(),
  removeServiceAgentSubscription: vi.fn(),
  publishServiceAgent: vi.fn(),
  unpublishServiceAgent: vi.fn(),
  listServiceWorkerCredentials: vi.fn(),
  createServiceWorkerCredential: vi.fn(),
  revokeServiceWorkerCredential: vi.fn(),
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
    mocks.unpublishServiceAgent.mockResolvedValue({ slug: 'hello', state: 'inactive' });
    mocks.listServiceWorkerCredentials.mockResolvedValue({ credentials: [], prefix: 'cwk_' });
    mocks.createServiceWorkerCredential.mockResolvedValue({
      id: 11,
      token: `cwk_${'a'.repeat(64)}`,
      prefix: 'cwk_aaaaaaaa',
      label: '本机 worker',
      shown_once: true,
    });
    mocks.revokeServiceWorkerCredential.mockResolvedValue({ id: 11, revoked: true });
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
    // 空目录时除了刷新，还要直接给一个「发布我的 agent」入口。
    const placeholderButtons = [...placeholder.querySelectorAll('button')];
    expect(placeholderButtons.some((node) => node.textContent.includes('发布我的 agent'))).toBe(true);
    const refresh = placeholderButtons.find((node) => node.textContent.includes('刷新目录'));
    expect(refresh).toBeTruthy();
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

    const [nameInput, slugInput] = form.querySelectorAll('input:not([type])');
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
    expect(payload).toMatchObject({ slug: 'my-agent', name: '我的助手', visibility: 'public' });
    expect(payload.definition).toMatchObject({ name: '我的助手', runner: { type: 'worker' } });

    const banner = container.querySelector('.v3-service-agents-published');
    expect(banner?.textContent).toContain('catsco_my-agent');
    expect(banner?.textContent).toContain('service-run-worker -agents my-agent');
    // 首次发布自动带上连接密钥：用户只需复制一行，不用自己找凭据。
    expect(mocks.createServiceWorkerCredential).toHaveBeenCalledTimes(1);
    expect(banner?.textContent).toContain(`-token cwk_${'a'.repeat(64)}`);
  });

  it('可以给每个应用单独指定本机执行命令（多个 agent 共用一个 worker）', async () => {
    mocks.publishServiceAgent.mockResolvedValueOnce({ slug: 'claude-helper', version: 1, visibility: 'private' });
    await renderPanel();
    await act(async () => {
      container.querySelector('button[aria-label="发布应用"]').click();
      await Promise.resolve();
    });
    const form = container.querySelector('.v3-service-agents-publish');
    const [nameInput, slugInput, commandInput] = form.querySelectorAll('input:not([type])');
    const setValue = (node, value) => {
      const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value').set;
      setter.call(node, value);
      node.dispatchEvent(new Event('input', { bubbles: true }));
    };
    await act(async () => {
      setValue(nameInput, 'Claude 助手');
      setValue(slugInput, 'claude-helper');
      setValue(commandInput, 'claude -p {input_file}');
      await Promise.resolve();
    });

    await act(async () => {
      form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
      await Promise.resolve();
      await Promise.resolve();
    });

    const payload = mocks.publishServiceAgent.mock.calls[0][0];
    expect(payload.definition.runner).toMatchObject({
      type: 'worker',
      command: 'claude -p {input_file}',
    });
    // 命令随应用下发，worker 不再需要 -cmd
    const banner = container.querySelector('.v3-service-agents-published');
    expect(banner.textContent).toContain('service-run-worker -agents claude-helper');
    expect(banner.textContent).not.toContain('-cmd');
  });

  it('发布 MCP 服务：runner.type=mcp 带地址与工具名', async () => {
    mocks.publishServiceAgent.mockResolvedValueOnce({ slug: 'peer-mcp', version: 1, visibility: 'public' });
    await renderPanel();
    await act(async () => {
      container.querySelector('button[aria-label="发布应用"]').click();
      await Promise.resolve();
    });
    const form = container.querySelector('.v3-service-agents-publish');
    const setValue = (node, value) => {
      const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value').set;
      setter.call(node, value);
      node.dispatchEvent(new Event('input', { bubbles: true }));
    };
    const [nameInput, slugInput] = form.querySelectorAll('input:not([type])');
    await act(async () => {
      setValue(nameInput, '对端 MCP');
      setValue(slugInput, 'peer-mcp');
      await Promise.resolve();
    });

    // 切到「MCP 服务」
    const mcpRadio = [...form.querySelectorAll('input[type="radio"]')]
      .find((node) => node.closest('label')?.textContent.includes('MCP 服务'));
    expect(mcpRadio).toBeTruthy();
    await act(async () => {
      mcpRadio.click();
      await Promise.resolve();
    });

    const urlInput = form.querySelector('input[placeholder="https://your-host/mcp"]');
    const toolInput = form.querySelector('input[placeholder="run"]');
    expect(urlInput).toBeTruthy();
    expect(toolInput).toBeTruthy();
    await act(async () => {
      setValue(urlInput, 'https://peer.example/mcp');
      setValue(toolInput, 'do-work');
      await Promise.resolve();
    });

    await act(async () => {
      form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
      await Promise.resolve();
      await Promise.resolve();
    });

    const payload = mocks.publishServiceAgent.mock.calls[0][0];
    expect(payload.definition.runner).toMatchObject({
      type: 'mcp',
      url: 'https://peer.example/mcp',
      tool: 'do-work',
    });
    // MCP 应用不需要本机 worker，布局里不应出现 worker 启动命令
    const banner = container.querySelector('.v3-service-agents-published');
    expect(banner?.textContent).not.toContain('service-run-worker');
  });

  it('发布表单会校验调用名，非法时不发请求', async () => {
    await renderPanel();
    await act(async () => {
      container.querySelector('button[aria-label="发布应用"]').click();
      await Promise.resolve();
    });
    const form = container.querySelector('.v3-service-agents-publish');
    const [nameInput, slugInput] = form.querySelectorAll('input:not([type])');
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

  it('收回分享：二次确认后下架、从列表移除并给出可恢复的提示', async () => {
    mocks.getServiceAgentsCatalog.mockResolvedValueOnce({
      agents: [
        { slug: 'my-local', name: '我本机的 agent', version: 2, hidden: false, mine: true, visibility: 'public' },
        { slug: 'other-agent', name: '别人的应用', version: 1, hidden: false, mine: false },
      ],
    });
    mocks.unpublishServiceAgent.mockResolvedValue({ slug: 'my-local', state: 'inactive' });
    await renderPanel();

    // 只有自己发布的应用才有「收回」
    expect(container.querySelectorAll('.v3-service-agent-unpublish').length).toBe(1);
    expect(container.querySelector('button[aria-label="收回我本机的 agent的分享"]')).toBeTruthy();

    await act(async () => {
      container.querySelector('button[aria-label="收回我本机的 agent的分享"]').click();
      await Promise.resolve();
    });
    // 第一下只是二次确认，不能直接发请求
    expect(mocks.unpublishServiceAgent).not.toHaveBeenCalled();
    const confirm = [...container.querySelectorAll('.v3-service-agent-unpublish')]
      .find((node) => node.textContent.includes('确认收回'));
    expect(confirm).toBeTruthy();

    await act(async () => {
      confirm.click();
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(mocks.unpublishServiceAgent).toHaveBeenCalledWith('my-local');
    // 下架的行从列表消失，只留别人的
    expect(container.querySelectorAll('.v3-service-agent-row').length).toBe(1);
    expect(container.querySelector('.v3-service-agent-slug')?.textContent).toBe('catsco_other-agent');
    const notice = container.querySelector('.v3-service-agents-notice');
    expect(notice?.textContent).toContain('catsco_my-local');
    expect(notice?.textContent).toContain('别人的工具列表里会立即消失');
    expect(notice?.textContent).toContain('重新发布同一调用名即可恢复分享');
  });

  it('收回失败（不是发布者本人）时保留该行并说明原因', async () => {
    mocks.getServiceAgentsCatalog.mockResolvedValueOnce({
      agents: [{ slug: 'my-local', name: '我本机的 agent', version: 1, hidden: false, mine: true }],
    });
    const forbidden = new Error('只有发布者本人能收回该应用。');
    forbidden.status = 403;
    mocks.unpublishServiceAgent.mockRejectedValueOnce(forbidden);
    await renderPanel();

    await act(async () => {
      container.querySelector('button[aria-label="收回我本机的 agent的分享"]').click();
      await Promise.resolve();
    });
    const confirm = [...container.querySelectorAll('.v3-service-agent-unpublish')]
      .find((node) => node.textContent.includes('确认收回'));
    await act(async () => {
      confirm.click();
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(container.querySelector('[role="alert"]')?.textContent).toContain('只有发布者本人能收回该应用');
    expect(container.querySelector('.v3-service-agent-slug')?.textContent).toBe('catsco_my-local');
    expect(container.querySelector('.v3-service-agents-notice')).toBeNull();
  });

  it('生成本机 worker 凭据：一次性展示明文，撤销后从列表消失', async () => {
    await renderPanel();
    const generate = [...container.querySelectorAll('.v3-service-worker-credentials-head button')]
      .find((node) => node.textContent.includes('生成'));
    expect(generate).toBeTruthy();

    await act(async () => {
      generate.click();
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(mocks.createServiceWorkerCredential).toHaveBeenCalled();
    const issued = container.querySelector('.v3-service-worker-credentials-issued');
    expect(issued?.textContent).toContain('只显示这一次');
    // 明文出现在可复制的 code 里（仅这一次）
    expect(issued?.querySelector('code')?.textContent).toBe(`cwk_${'a'.repeat(64)}`);
    // 列表里只显示短前缀
    const row = container.querySelector('.v3-service-worker-credentials-list li');
    expect(row?.textContent).toContain('cwk_aaaaaaaa');
    expect(row?.textContent).not.toContain('a'.repeat(64));

    const revoke = [...container.querySelectorAll('.v3-service-worker-credentials-list button')]
      .find((node) => node.textContent.includes('撤销'));
    await act(async () => {
      revoke.click();
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(mocks.revokeServiceWorkerCredential).toHaveBeenCalledWith(11);
    expect(container.querySelector('.v3-service-worker-credentials-list')).toBeNull();
    expect(container.querySelector('.v3-service-worker-credentials-issued')).toBeNull();
  });
});
