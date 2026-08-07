import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';

vi.mock('../api', () => ({
  api: {
    createBot: vi.fn(),
    getAgents: vi.fn(),
    getFriends: vi.fn(),
    getMyBots: vi.fn(),
    setBotSkillsVisibility: vi.fn(),
    uploadFile: vi.fn(),
  },
  getWebSocketURL: vi.fn(() => 'wss://app.catsco.cc/v0/channels'),
  resolveMediaURL: vi.fn((url) => url),
}));

import { api } from '../api';
import AgentStoreModal from './agent-store-modal';

describe('AgentStoreModal', () => {
  let container;
  let root;

  beforeEach(() => {
    global.IS_REACT_ACT_ENVIRONMENT = true;
    api.createBot.mockReset().mockResolvedValue({ uid: 91 });
    api.getAgents.mockReset().mockResolvedValue({ agents: [] });
    api.getFriends.mockReset().mockResolvedValue({ friends: [] });
    api.getMyBots.mockReset().mockResolvedValue({ bots: [] });
    api.setBotSkillsVisibility.mockReset().mockResolvedValue({ skills_visibility: 'owner' });
    api.uploadFile.mockReset().mockResolvedValue({ url: '/uploads/avatar.png' });
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(async () => {
    await act(async () => {
      root.unmount();
    });
    container.remove();
  });

  test('allows creating an assistant without a usage description', async () => {
    await act(async () => {
      root.render(React.createElement(AgentStoreModal, {
        onClose: vi.fn(),
        user: { uid: 7 },
      }));
      await Promise.resolve();
    });

    const createTab = Array.from(container.querySelectorAll('button'))
      .find((button) => button.textContent.includes('创建新助手'));

    await act(async () => {
      Simulate.click(createTab);
    });

    const form = container.querySelector('.cc-agent-create-form');
    const nameInput = form.querySelector('input[type="text"]');
    const description = form.querySelector('textarea');
    const submit = form.querySelector('button[type="submit"]');
    const roleSelect = form.querySelector('.v3-custom-model-select-trigger[aria-label="助手定位"]');

    expect(description.required).toBe(false);
    expect(submit.disabled).toBe(false);
    expect(roleSelect).not.toBeNull();
    expect(roleSelect.closest('.cc-agent-role-select')).not.toBeNull();
    expect(roleSelect.querySelector('.v3-custom-model-select-chevron')).not.toBeNull();

    await act(async () => {
      Simulate.click(roleSelect);
    });

    const roleOptions = Array.from(document.body.querySelectorAll('.v3-custom-model-select-option'));
    expect(roleOptions.map((option) => option.textContent)).toEqual([
      '代码审查助手',
      '问题排查助手',
      '写作助手',
      '研究助手',
      '通用助手',
    ]);

    await act(async () => {
      Simulate.click(roleOptions[2]);
    });
    expect(roleSelect.dataset.value).toBe('writing');
    expect(roleSelect.textContent).toContain('写作助手');

    await act(async () => {
      Simulate.change(nameInput, { target: { value: '测试助手' } });
    });

    await act(async () => {
      Simulate.submit(form);
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(api.createBot).toHaveBeenCalledWith(
      expect.objectContaining({ display_name: '测试助手' }),
    );
  });

  test('opens a requested owned assistant directly in the management view', async () => {
    api.getMyBots.mockResolvedValue({
      bots: [{
        id: 42,
        uid: 42,
        username: 'dev-agent',
        display_name: 'Dev Agent',
        avatar_url: '/uploads/dev.png',
        relation: 'owner',
        is_owner: true,
        visibility: 'public',
      }],
    });

    await act(async () => {
      root.render(React.createElement(AgentStoreModal, {
        initialAgentId: 42,
        onClose: vi.fn(),
        user: { uid: 7 },
      }));
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(container.querySelector('.cc-agent-manager-body h2')?.textContent).toBe('管理助手');
    expect(container.querySelector('.cc-agent-manager-body input[type="text"]')?.value).toBe('Dev Agent');
    expect(
      Array.from(container.querySelectorAll('.cc-agent-manager-body .oc-form-group > label'))
        .map((label) => label.textContent),
    ).toEqual(['头像', '名称']);
    expect(container.textContent).not.toContain('还没有你创建的 AI 助手');
  });

  test('uses the stable hub height for live overview data and practical usage guidance', async () => {
    api.getMyBots.mockResolvedValue({
      bots: [
        {
          id: 42,
          username: 'review-agent',
          display_name: 'Review Agent',
          relation: 'owner',
          is_owner: true,
          visibility: 'public',
        },
        {
          id: 43,
          username: 'private-agent',
          display_name: 'Private Agent',
          relation: 'owner',
          is_owner: true,
          visibility: 'private',
          tenant_name: 'catsco-cloud',
        },
      ],
    });
    api.getAgents.mockResolvedValue({
      agents: [{
        uid: 42,
        username: 'review-agent',
        display_name: 'Review Agent',
        relation: 'owner',
        is_bot: true,
        is_online: true,
      }],
    });

    await act(async () => {
      root.render(React.createElement(AgentStoreModal, {
        onClose: vi.fn(),
        user: { uid: 7 },
      }));
      await Promise.resolve();
      await Promise.resolve();
    });

    const stats = Array.from(container.querySelectorAll('.cc-agent-overview-stats > div'))
      .map((item) => item.textContent);
    expect(stats).toEqual(['2全部助手', '1当前在线', '1公开可搜索', '1自托管']);
    expect(container.querySelector('.cc-agent-hub-grid')).not.toBeNull();
    expect(container.querySelectorAll('.cc-agent-hub-grid .v3-agent-card')).toHaveLength(2);
    expect(container.querySelector('.cc-agent-card-manage')?.textContent).toBe('管理');
    expect(container.querySelector('[aria-label="删除助手 Review Agent"]')?.textContent).toBe('');
    expect(container.querySelector('.cc-agent-usage-guide')?.textContent).toContain('管理');
    expect(container.querySelector('.cc-agent-usage-guide')?.textContent).toContain('入口码');
    expect(container.querySelector('.cc-agent-usage-guide')?.textContent).toContain('移动端使用');
    expect(container.querySelector('.cc-agent-usage-guide')?.textContent)
      .not.toContain('三个入口分别负责配置、分享与移动端连接');
  });

  test('does not apply a completed avatar upload to a different managed assistant', async () => {
    let resolveUpload;
    api.uploadFile.mockReturnValue(new Promise((resolve) => {
      resolveUpload = resolve;
    }));
    api.getMyBots.mockResolvedValue({
      bots: [
        {
          id: 42,
          username: 'alpha-agent',
          display_name: 'Alpha Agent',
          relation: 'owner',
          is_owner: true,
          avatar_url: '/uploads/alpha.png',
          visibility: 'public',
        },
        {
          id: 43,
          username: 'beta-agent',
          display_name: 'Beta Agent',
          relation: 'owner',
          is_owner: true,
          avatar_url: '/uploads/beta.png',
          visibility: 'public',
        },
      ],
    });

    await act(async () => {
      root.render(React.createElement(AgentStoreModal, {
        onClose: vi.fn(),
        user: { uid: 7 },
      }));
      await Promise.resolve();
      await Promise.resolve();
    });

    const alphaCard = Array.from(container.querySelectorAll('.v3-agent-card'))
      .find((card) => card.textContent.includes('Alpha Agent'));
    await act(async () => Simulate.click(alphaCard.querySelector('.cc-agent-card-manage')));

    const fileInput = container.querySelector('input[type="file"]');
    Object.defineProperty(fileInput, 'files', {
      configurable: true,
      value: [new File(['avatar'], 'alpha.png', { type: 'image/png' })],
    });
    await act(async () => {
      Simulate.change(fileInput);
      await Promise.resolve();
    });
    expect(api.uploadFile).toHaveBeenCalledTimes(1);

    await act(async () => Simulate.click(container.querySelector('.cc-agent-manager-tabs button')));
    const betaCard = Array.from(container.querySelectorAll('.v3-agent-card'))
      .find((card) => card.textContent.includes('Beta Agent'));
    await act(async () => Simulate.click(betaCard.querySelector('.cc-agent-card-manage')));

    await act(async () => {
      resolveUpload({ url: '/uploads/old-alpha-upload.png' });
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(container.querySelector('.cc-agent-manager-body input[type="text"]').value).toBe('Beta Agent');
    expect(container.querySelector('.cc-agent-manager-body .oc-avatar-img')?.getAttribute('src'))
      .toBe('/uploads/beta.png');
  });

  test('defaults skill visibility to owner and saves a selected audience', async () => {
    let resolveVisibility;
    api.setBotSkillsVisibility.mockReturnValue(new Promise((resolve) => {
      resolveVisibility = resolve;
    }));
    api.getMyBots.mockResolvedValue({
      bots: [{
        id: 42,
        uid: 42,
        username: 'dev-agent',
        display_name: 'Dev Agent',
        relation: 'owner',
        is_owner: true,
        visibility: 'public',
      }],
    });

    await act(async () => {
      root.render(React.createElement(AgentStoreModal, {
        initialAgentId: 42,
        onClose: vi.fn(),
        user: { uid: 7 },
      }));
      await Promise.resolve();
      await Promise.resolve();
    });

    const options = [...container.querySelectorAll('.cc-agent-permission-options > button')];
    expect(options.map((button) => button.textContent)).toEqual([
      '仅自己只有你能查看技能列表',
      'Agent 使用者已添加该 Agent 的用户可查看',
      '公开所有已登录用户都可查看',
    ]);
    expect(options[0].getAttribute('aria-pressed')).toBe('true');

    await act(async () => {
      Simulate.click(options[1]);
      await Promise.resolve();
    });
    expect(api.setBotSkillsVisibility).toHaveBeenCalledWith(42, 'authorized');
    expect(options.every((button) => button.disabled)).toBe(true);
    expect(container.querySelector('.cc-agent-permission-heading > span')?.textContent).toBe('保存中...');

    await act(async () => {
      resolveVisibility({ skills_visibility: 'authorized' });
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(options[1].getAttribute('aria-pressed')).toBe('true');
    expect(options.every((button) => !button.disabled)).toBe(true);
  });

  test('keeps the previous skill visibility and reports a save failure', async () => {
    api.setBotSkillsVisibility.mockRejectedValue(new Error('保存失败，请重试'));
    api.getMyBots.mockResolvedValue({
      bots: [{
        id: 42,
        uid: 42,
        username: 'dev-agent',
        display_name: 'Dev Agent',
        relation: 'owner',
        is_owner: true,
        visibility: 'public',
        skills_visibility: 'public',
      }],
    });

    await act(async () => {
      root.render(React.createElement(AgentStoreModal, {
        initialAgentId: 42,
        onClose: vi.fn(),
        user: { uid: 7 },
      }));
      await Promise.resolve();
      await Promise.resolve();
    });
    const options = [...container.querySelectorAll('.cc-agent-permission-options > button')];

    await act(async () => {
      Simulate.click(options[0]);
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(options[2].getAttribute('aria-pressed')).toBe('true');
    expect(container.querySelector('.cc-agent-inline-feedback')?.textContent).toContain('保存失败，请重试');
  });
});
