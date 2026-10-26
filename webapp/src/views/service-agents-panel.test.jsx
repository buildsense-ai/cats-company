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

  /**
   * 发布表单里的「能做什么」。公开应用必填：调用方是另一个 Agent，
   * 它只看到这段文字，没描述等于不知道能不能用。
   */
  const fillDescription = async (form, value = '把中文技术文档润色成对外英文稿，保留代码块。input.text 是正文。') => {
    const textarea = form.querySelector('textarea');
    await act(async () => {
      const setter = Object.getOwnPropertyDescriptor(window.HTMLTextAreaElement.prototype, 'value').set;
      setter.call(textarea, value);
      textarea.dispatchEvent(new Event('input', { bubbles: true }));
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
    await fillDescription(form);

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
    // 凭证对普通用户不可理解，界面上一律不出现：不生成密钥、不给启动命令。
    expect(mocks.createServiceWorkerCredential).not.toHaveBeenCalled();
    expect(banner?.textContent).not.toContain('service-run-worker');
    expect(banner?.textContent).not.toContain('-token');
    expect(banner?.textContent).not.toContain('cwk_');
    // 只告诉用户：在要跑这个应用的那台电脑上登录桌面端就行。
    expect(banner?.textContent).toContain('小八');
    expect(banner?.textContent).toContain('自动接单');
  });

  // 「密钥 / 命令」这类东西用户完全看不懂，也就无法判断对错，因此从面板里彻底拿掉；
  // 老用户即使后台还有旧密钥，界面也不该再甩给他一条要粘贴的命令。
  // 编辑已有应用：发布是全量覆盖，所以必须带原 definition 合并 ——
  // 只改描述不能把执行命令 / 价格抹掉，否则应用当场跑不起来（run 报 no_command）。
  it('编辑已有应用：回填表单，保存时保留原执行方式与价格', async () => {
    mocks.getServiceAgentsCatalog.mockResolvedValue({
      agents: [
        {
          slug: 'my-clock',
          name: '我的时钟',
          version: 2,
          hidden: false,
          mine: true,
          visibility: 'private',
          description: '读我电脑的当前时间。',
          definition: {
            name: '我的时钟',
            description: '读我电脑的当前时间。',
            runner: { type: 'worker', command: 'node clock.js' },
            pricing: { unit: 'second', per_second: 0.001, currency: 'CNY' },
          },
        },
      ],
    });
    mocks.publishServiceAgent.mockResolvedValueOnce({ slug: 'my-clock', version: 3, visibility: 'private' });
    await renderPanel();

    // 行里能看见描述（这段就是调用方 Agent 看到的字）
    expect(container.querySelector('.v3-service-agent-desc')?.textContent).toContain('读我电脑的当前时间');

    await act(async () => {
      container.querySelector('button[aria-label="编辑我的时钟"]').click();
      await Promise.resolve();
    });
    const form = container.querySelector('.v3-service-agents-publish');
    expect(form?.textContent).toContain('编辑应用');
    const nameInput = form.querySelector('input:not([type])');
    const slugInput = form.querySelectorAll('input:not([type])')[1];
    expect(nameInput.value).toBe('我的时钟');
    expect(slugInput.value).toBe('my-clock');
    // 调用名是分享出去的名字，编辑时不能改（改了就是另一个应用）
    expect(slugInput.readOnly).toBe(true);
    expect(form.querySelector('textarea').value).toBe('读我电脑的当前时间。');

    const setValue = async (node, value) => {
      await act(async () => {
        const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value').set;
        setter.call(node, value);
        node.dispatchEvent(new Event('input', { bubbles: true }));
        await Promise.resolve();
      });
    };
    // 定位「本机执行命令」：占位符与命令输入框都带 {input_file} 提示
    const commandInput = [...form.querySelectorAll('input')]
      .find((node) => node.placeholder === 'claude -p {input_file}');
    expect(commandInput.value).toBe('node clock.js');
    await fillDescription(form, '读我电脑的当前时间；问“现在几点”时用。');
    await setValue(commandInput, 'node clock2.js');

    await act(async () => {
      form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(mocks.publishServiceAgent).toHaveBeenCalledTimes(1);
    const payload = mocks.publishServiceAgent.mock.calls[0][0];
    expect(payload.slug).toBe('my-clock');
    expect(payload.visibility).toBe('private');
    expect(payload.definition.description).toBe('读我电脑的当前时间；问“现在几点”时用。');
    // 关键：执行方式保留、命令按编辑后的值、价格没被抹掉
    expect(payload.definition.runner).toEqual({ type: 'worker', command: 'node clock2.js' });
    expect(payload.definition.pricing).toEqual({ unit: 'second', per_second: 0.001, currency: 'CNY' });
  });

  it('自己的应用没写描述时提示「别人基本不会用它」', async () => {
    mocks.getServiceAgentsCatalog.mockResolvedValue({
      agents: [{ slug: 'my-clock', name: '我的时钟', version: 1, hidden: false, mine: true }],
    });
    await renderPanel();
    expect(container.querySelector('.v3-service-agent-warn')?.textContent).toContain('没写「能做什么」');
  });

  it('别人的应用既不给编辑按钮，也不回传 definition', async () => {
    mocks.getServiceAgentsCatalog.mockResolvedValue({
      agents: [{ slug: 'peer', name: '别人的', version: 1, hidden: false, mine: false, description: '别人写的' }],
    });
    await renderPanel();
    expect(container.querySelector('button[aria-label="编辑别人的"]')).toBeNull();
    expect(container.querySelector('.v3-service-agent-warn')).toBeNull();
    expect(container.querySelector('.v3-service-agent-desc')?.textContent).toContain('别人写的');
  });

  it('发布后不给命令也不给密钥，只提示在桌面端登录即可', async () => {
    mocks.publishServiceAgent.mockResolvedValueOnce({ slug: 'legacy', version: 1, visibility: 'public' });
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
      setValue(nameInput, 'Legacy');
      setValue(slugInput, 'legacy');
      await Promise.resolve();
    });
    await fillDescription(form);
    await act(async () => {
      form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(mocks.createServiceWorkerCredential).not.toHaveBeenCalled();
    const banner = container.querySelector('.v3-service-agents-published');
    expect(banner.textContent).toContain('catsco_legacy');
    expect(banner.textContent).not.toContain('service-run-worker');
    expect(banner.textContent).not.toContain('-token');
    expect(banner.textContent).not.toContain('cwk_');
    expect(banner.textContent).not.toContain('连接密钥');
    // 整个面板（不只是横幅）都不该出现任何密钥痕迹
    expect(container.textContent).not.toContain('cwk_');
    expect(container.textContent).not.toContain('连接密钥');
  });


  // 按调用次数收费与真实开销无关（一次可能只是打招呼，也可能跑十分钟），
  // 所以只允许按实际用量计价：token 或运行时长。
  it('定价只按实际用量算，不按调用次数', async () => {
    mocks.publishServiceAgent.mockResolvedValueOnce({ slug: 'priced', version: 1, visibility: 'public' });
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
      setValue(nameInput, 'Priced');
      setValue(slugInput, 'priced');
      await Promise.resolve();
    });
    await fillDescription(form);

    // 默认按 token 用量
    const priceInput = [...form.querySelectorAll('input:not([type])')]
      .find((node) => node.getAttribute('inputmode') === 'decimal');
    await act(async () => {
      setValue(priceInput, '2');
      await Promise.resolve();
    });
    await act(async () => {
      form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
      await Promise.resolve();
      await Promise.resolve();
    });
    let payload = mocks.publishServiceAgent.mock.calls.at(-1)[0];
    expect(payload.definition.pricing).toEqual({ unit: 'token', per_1k_tokens: 2, currency: 'CNY' });
    expect(payload.definition.pricing.per_run).toBeUndefined();

    // 切到按运行时长
    mocks.publishServiceAgent.mockResolvedValueOnce({ slug: 'priced2', version: 1, visibility: 'public' });
    await act(async () => {
      container.querySelector('button[aria-label="发布应用"]').click();
      await Promise.resolve();
    });
    const form2 = container.querySelector('.v3-service-agents-publish');
    const seconds = [...form2.querySelectorAll('input[type="radio"]')]
      .find((node) => node.nextSibling?.textContent?.includes('按运行时长')
        || node.parentElement?.textContent?.includes('按运行时长'));
    await act(async () => {
      seconds.click();
      await Promise.resolve();
    });
    const [name2, slug2] = form2.querySelectorAll('input:not([type])');
    const price2 = [...form2.querySelectorAll('input:not([type])')]
      .find((node) => node.getAttribute('inputmode') === 'decimal');
    await act(async () => {
      setValue(name2, 'Priced Two');
      setValue(slug2, 'priced-2');
      setValue(price2, '0.01');
      await Promise.resolve();
    });
    await fillDescription(form2);
    await act(async () => {
      form2.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
      await Promise.resolve();
      await Promise.resolve();
    });
    payload = mocks.publishServiceAgent.mock.calls.at(-1)[0];
    expect(payload.definition.pricing).toEqual({ unit: 'second', per_second: 0.01, currency: 'CNY' });
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
    await fillDescription(form);

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
    // 命令随应用下发，worker 不再需要 -cmd；也不再把启动命令甩给用户
    const banner = container.querySelector('.v3-service-agents-published');
    expect(banner.textContent).not.toContain('service-run-worker');
    expect(banner.textContent).not.toContain('-cmd');
    expect(banner.textContent).toContain('catsco_claude-helper');
  });

  // 面板只提供「我的环境主动接入」一种方式：平台直连 http/mcp 要求公网可达地址，
  // 普通用户基本没有 —— 摆出来只会让人以为必须开公网、还得自己兜鉴权。
  it('发布表单不再提供平台直连（HTTP/MCP）选项', async () => {
    await renderPanel();
    await act(async () => {
      container.querySelector('button[aria-label="发布应用"]').click();
      await Promise.resolve();
    });
    const form = container.querySelector('.v3-service-agents-publish');
    const runnerRadios = [...form.querySelectorAll('input[type="radio"]')]
      .filter((node) => node.name === 'runner');
    expect(runnerRadios).toHaveLength(0);
    expect(form.textContent).not.toContain('HTTP 服务');
    expect(form.textContent).not.toContain('MCP 服务');
    // 但要讲清「电脑、云服务器都行」，因为没有公网也能用
    expect(form.textContent).toContain('云服务器');
    expect(form.textContent).toContain('不需要公网 IP');
  });

  // 旧应用可能还是直连型。面板不再提供这种接入方式，但保存时绝不能把它改成
  // worker —— 命令一下子没了，应用下次被调用就 no_command 跑不起来。
  it('编辑旧应用的直连配置时原样保留 runner', async () => {
    mocks.getServiceAgentsCatalog.mockResolvedValue({
      agents: [
        {
          slug: 'peer-mcp',
          name: '对端 MCP',
          version: 1,
          hidden: false,
          mine: true,
          visibility: 'public',
          description: '对端 MCP 服务。',
          definition: {
            name: '对端 MCP',
            description: '对端 MCP 服务。',
            runner: { type: 'mcp', url: 'https://peer.example/mcp', tool: 'do-work', timeout_seconds: 60 },
          },
        },
      ],
    });
    mocks.publishServiceAgent.mockResolvedValueOnce({ slug: 'peer-mcp', version: 2, visibility: 'public' });
    await renderPanel();

    await act(async () => {
      container.querySelector('button[aria-label="编辑对端 MCP"]').click();
      await Promise.resolve();
    });
    const form = container.querySelector('.v3-service-agents-publish');
    // 明确告诉用户这条链路面板改不了、也不会被改
    expect(form.querySelector('.v3-service-agents-locked')?.textContent).toContain('连接方式保持原样');
    // 直连型没有「执行命令」可填
    expect(form.querySelector('input[placeholder="claude -p {input_file}"]')).toBeNull();

    await fillDescription(form, '对端 MCP 服务，问 wiki 用。');
    await act(async () => {
      form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
      await Promise.resolve();
      await Promise.resolve();
    });

    const payload = mocks.publishServiceAgent.mock.calls[0][0];
    expect(payload.definition.description).toBe('对端 MCP 服务，问 wiki 用。');
    expect(payload.definition.runner).toEqual({
      type: 'mcp',
      url: 'https://peer.example/mcp',
      tool: 'do-work',
      timeout_seconds: 60,
    });
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
    await fillDescription(form);
    await act(async () => {
      form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
      await Promise.resolve();
    });
    expect(mocks.publishServiceAgent).not.toHaveBeenCalled();
    expect(container.querySelector('.v3-service-agents-publish [role="alert"]')?.textContent).toContain('调用名');
  });

  // 公开分享却不写描述 = 分享出去也没人会用：调用方是另一个 Agent，
  // 它看不到包内容、也没有你的对话上下文，只能靠这段文字判断要不要调。
  it('公开应用不写「能做什么」就不发请求；私有应用允许留空', async () => {
    mocks.publishServiceAgent.mockResolvedValue({ slug: 'no-desc', version: 1, visibility: 'public' });
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
      setValue(nameInput, 'No Desc');
      setValue(slugInput, 'no-desc');
      await Promise.resolve();
    });

    // 默认公开 + 空描述 → 拦下，不发请求
    await act(async () => {
      form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
      await Promise.resolve();
    });
    expect(mocks.publishServiceAgent).not.toHaveBeenCalled();
    expect(form.querySelector('[role="alert"]')?.textContent).toContain('能做什么');

    // 同样的内容，选了「仅自己」→ 允许发布
    const privateRadio = [...form.querySelectorAll('input[type="radio"]')]
      .find((node) => node.parentElement?.textContent?.includes('仅自己'));
    await act(async () => {
      privateRadio.click();
      await Promise.resolve();
    });
    await act(async () => {
      form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(mocks.publishServiceAgent).toHaveBeenCalledTimes(1);
    expect(mocks.publishServiceAgent.mock.calls[0][0].definition.description).toBeUndefined();
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
});

