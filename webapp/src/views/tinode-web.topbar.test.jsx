import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

import {
  canOpenCloudArtifacts,
  cloudArtifactNotificationToast,
  commitPreviewSession,
  describeModelApplyError,
  describeModelConfigRequestError,
  LocalAssistantBar,
  ProfilePopover,
  resolveInitialUser,
  resolveDisplayedActiveAgent,
} from './tinode-web';
import { api } from '../api';

const topbarCss = readFileSync(
  resolve(process.cwd(), 'src/css/catsco-topbar.css'),
  'utf8',
);

const baseConfig = {
  uid: 43,
  runtime_supported: true,
  configured: true,
  status: 'applied',
  desired: { kind: 'catalog', model_id: 'minimax-m3', reasoning_effort: '', revision: 2 },
  applied: { kind: 'catalog', model_id: 'minimax-m3', reasoning_effort: '', revision: 2 },
  custom_supported: true,
  models: [
    {
      id: 'minimax-m2.7',
      label: 'MiniMax M2.7',
      description: '标准额度，适合日常任务',
      context_window_tokens: 204800,
    },
    {
      id: 'minimax-m3',
      label: 'MiniMax M3',
      description: '支持多模态与长上下文',
      context_window_tokens: 1000000,
      quota: { model: 'minimax-m3', quota_configured: true, percent: 25, remaining_percent: 75, status: 'normal' },
    },
    {
      id: 'deepseek-v4-flash',
      label: 'DeepSeek V4 Flash',
      description: '低额度 Flash，支持推理强度与视觉理解',
      context_window_tokens: 1000000,
      quota: { model: 'deepseek-v4-flash', quota_configured: true, percent: 90, remaining_percent: 10, status: 'high' },
      reasoning_efforts: ['high', 'max', 'disabled'],
      default_reasoning_effort: 'high',
      vision: true,
    },
    {
      id: 'gpt-5.6-terra',
      label: 'GPT-5.6 Terra',
      description: 'OpenAI Responses，支持精细推理强度',
      context_window_tokens: 256000,
      reasoning_efforts: ['none', 'minimal', 'low', 'medium', 'high', 'xhigh'],
      default_reasoning_effort: 'medium',
    },
  ],
};

const relayState = {
  isBot: true,
  state: 'ready',
  summary: {
    source: 'relay', model: 'minimax-m3', quota_configured: true, percent: 25, remaining_percent: 75, status: 'normal',
  },
};

describe('preview user identity', () => {
  it('persists the preview profile before publishing its token', () => {
    const calls = [];
    const storedUser = commitPreviewSession({
      token: 'preview-session',
      id: 100,
      username: 'ui-reviewer',
    }, {
      writeProfile: (profile) => {
        calls.push(['profile', profile]);
        return profile;
      },
      setSessionToken: (token) => calls.push(['token', token]),
    });

    expect(storedUser).toMatchObject({ uid: 100, username: 'ui-reviewer' });
    expect(calls).toEqual([
      ['profile', expect.objectContaining({ uid: 100, username: 'ui-reviewer' })],
      ['token', 'preview-session'],
    ]);
  });

  it('restores the authenticated backend identity while previewing a theme', () => {
    expect(resolveInitialUser({
      themePreview: 'liquid',
      previewEnabled: true,
      token: 'existing-session',
      savedUser: {
        id: 38,
        username: 'cycren',
        display_name: 'Cycren',
        account_type: 'human',
      },
    })).toMatchObject({
      uid: 38,
      username: 'cycren',
      display_name: 'Cycren',
    });
  });

  it('uses the visual-only placeholder when no authenticated preview session exists', () => {
    expect(resolveInitialUser({
      themePreview: 'liquid',
      previewEnabled: true,
    })).toMatchObject({
      uid: 'theme-preview',
      username: 'preview',
    });
  });
});

describe('cloud artifact owner notifications', () => {
  it('maps the realtime event to the generic notification without artifact details', () => {
    expect(cloudArtifactNotificationToast({
      notification: {
        type: 'cloud_artifact_shared',
        message: '内部成果标题不应展示',
      },
    })).toEqual({
      tone: 'info',
      message: '有新文件在云端共享',
    });
    expect(cloudArtifactNotificationToast({ data: { type: 'file' } })).toBeNull();
  });
});

describe('model reasoning menu placement', () => {
  it('keeps reasoning choices attached to the right side of their model at every viewport size', () => {
    expect(topbarCss).toMatch(
      /\.v3-model-reasoning-menu\s*\{[^}]*left:\s*calc\(100% - 2px\);/s,
    );
    expect(topbarCss).not.toMatch(
      /\.v3-model-reasoning-menu\s*\{[^}]*position:\s*static;/s,
    );
    expect(topbarCss).not.toContain('.v3-model-menu:not(.custom-open)');
  });

  it('keeps comfortable space below the custom model entry', () => {
    expect(topbarCss).toMatch(
      /\.v3-model-menu,\s*\.v3-model-reasoning-menu\s*\{[^}]*padding:\s*6px;/s,
    );
    expect(topbarCss).toMatch(
      /\.v3-model-menu\s*\{[^}]*overflow:\s*visible;/s,
    );
    expect(topbarCss).toMatch(
      /\.v3-model-menu\.custom-open\s*\{[^}]*max-height:/s,
    );
  });
});

describe('narrow conversation top bar', () => {
  it('keeps actions visible in the mobile layout', () => {
    expect(topbarCss).toMatch(
      /\.v3-shell-actions\s*\{[^}]*display:\s*flex;[^}]*align-items:\s*center;[^}]*flex:\s*0\s+0\s+auto;/s,
    );
    expect(topbarCss).toMatch(
      /@media\s*\(max-width:\s*768px\)[\s\S]*?\.v3-shell-actions\s*\{[^}]*min-width:\s*max-content;/,
    );
    expect(topbarCss).toMatch(
      /@media\s*\(max-width:\s*520px\)[\s\S]*?\.v3-shell-actions\s*\{[^}]*overflow:\s*visible;/,
    );
  });

  it('limits narrow-pane action hiding to desktop previews', () => {
    expect(topbarCss).toMatch(
      /\.v3-chat-column\s*\{[^}]*container-name:\s*catsco-chat-column;[^}]*container-type:\s*inline-size;/s,
    );
    expect(topbarCss).toMatch(
      /@media\s*\(min-width:\s*769px\)[\s\S]*?@container catsco-chat-column \(max-width: 820px\)[\s\S]*?\.v3-local-assistant-bar > :is\(\.v3-model-select, \.v3-shell-actions\)\s*\{[^}]*display:\s*none;/,
    );
    expect(topbarCss).toMatch(
      /@media\s*\(min-width:\s*769px\)[\s\S]*?@container catsco-chat-column \(max-width: 820px\)[\s\S]*?\.v3-local-assistant-bar > :is\(\.v3-shell-title, \.v3-shell-title-input\)\s*\{[^}]*max-width:\s*100%;[^}]*min-width:\s*0;/,
    );
  });
});

describe('resolveDisplayedActiveAgent', () => {
  it('exposes an owned draft agent to the model selector before the task is created', () => {
    expect(resolveDisplayedActiveAgent('', null, {
      agent: { uid: 110, relation: 'owner', display_name: 'XiaoBa' },
    })).toMatchObject({ uid: 110, relation: 'owner', isOwner: true });
  });

  it('keeps friend draft agents read-only', () => {
    expect(resolveDisplayedActiveAgent('', null, {
      agent: { id: 407, relation: 'friend' },
    })).toMatchObject({ uid: 407, relation: 'friend', isOwner: false });
  });

  it('exposes the selected empty-task Agent before a task exists', () => {
    const agent = resolveDisplayedActiveAgent('', null, null, {
      uid: 440,
      relation: 'owner',
      display_name: 'Doubao',
    });

    expect(agent).toMatchObject({ uid: 440, relation: 'owner', isOwner: true });
    expect(canOpenCloudArtifacts(null, agent)).toBe(true);
  });

  it('uses the active conversation agent instead of a stale draft', () => {
    const activeAgent = { uid: 63, relation: 'owner', isOwner: true };
    expect(resolveDisplayedActiveAgent(
      'p2p_38_63',
      { topicId: 'p2p_38_63', agent: activeAgent },
      { agent: { uid: 110, relation: 'owner' } },
    )).toBe(activeAgent);
  });
});

describe('cloud artifact action visibility', () => {
  it('is available for a conversation or a selected draft Agent', () => {
    const doubao = { uid: 440, cloud_artifacts_enabled: true };
    expect(canOpenCloudArtifacts({ topicId: 'p2p_7_440', isGroup: false }, doubao)).toBe(true);
    expect(canOpenCloudArtifacts({ topicId: 'grp_8', isGroup: true }, doubao)).toBe(true);
    expect(canOpenCloudArtifacts({ topicId: 'p2p_7_441', isGroup: false }, { uid: 441 })).toBe(true);
    expect(canOpenCloudArtifacts({ topicId: 'p2p_7_441', isGroup: false }, null)).toBe(true);
    expect(canOpenCloudArtifacts(null, doubao)).toBe(true);
    expect(canOpenCloudArtifacts(null, null)).toBe(false);
  });
});

describe('ProfilePopover', () => {
  it('escapes the collapsed sidebar clipping context and marks the compact flyout', async () => {
    const sidebar = document.createElement('aside');
    document.body.appendChild(sidebar);
    const root = createRoot(sidebar);

    await act(async () => {
      root.render(
        <ProfilePopover compact>
          <button type="button">设置与资料</button>
        </ProfilePopover>,
      );
    });

    const popover = document.body.querySelector('.v3-profile-popover.is-compact');
    expect(popover).toBeTruthy();
    expect(popover?.textContent).toContain('设置与资料');
    expect(sidebar.querySelector('.v3-profile-popover')).toBeNull();

    await act(async () => root.unmount());
    sidebar.remove();
  });

  it('delegates the visible logout action to authenticated-session cleanup', async () => {
    const sidebar = document.createElement('aside');
    document.body.appendChild(sidebar);
    const root = createRoot(sidebar);
    const onLogout = vi.fn();

    await act(async () => {
      root.render(
        <ProfilePopover onLogout={onLogout}>
          <button type="button">设置与资料</button>
        </ProfilePopover>,
      );
    });

    const popover = document.body.querySelector('[role="menu"][aria-label="账号菜单"]');
    const logout = popover?.querySelector('button[role="menuitem"][aria-label="退出登录"]');
    expect(popover).toBeTruthy();
    expect(logout).toBeTruthy();

    await act(async () => logout.click());
    expect(onLogout).toHaveBeenCalledTimes(1);

    await act(async () => root.unmount());
    sidebar.remove();
  });
});

describe('LocalAssistantBar model selector', () => {
  let container;
  let root;

  beforeEach(() => {
    global.IS_REACT_ACT_ENVIRONMENT = true;
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(async () => {
    await act(async () => root.unmount());
    container.remove();
    vi.useRealTimers();
    vi.restoreAllMocks();
  });

  const renderBar = async (props = {}) => {
    await act(async () => {
      root.render(
        <LocalAssistantBar
          currentModelName="MiniMax-M2.7"
          agentModelState={relayState}
          onDownload={vi.fn()}
          title="XiaoBa"
          {...props}
        />,
      );
      await Promise.resolve();
      await Promise.resolve();
    });
  };

  it('shows the relay admin button when the parent grants access', async () => {
    await renderBar({ relayAdminAllowed: true });
    const button = container.querySelector('button[aria-label="模型用量"]');
    expect(button).toBeTruthy();
  });

  it('opens the relay admin panel when the button is clicked', async () => {
    const onOpenRelayAdmin = vi.fn();
    await renderBar({ relayAdminAllowed: true, onOpenRelayAdmin });
    const button = container.querySelector('button[aria-label="模型用量"]');
    await act(async () => button.click());
    expect(onOpenRelayAdmin).toHaveBeenCalledTimes(1);
  });

  it('hides the relay admin button when access is denied', async () => {
    await renderBar({ relayAdminAllowed: false });
    expect(container.querySelector('button[aria-label="模型用量"]')).toBeNull();
  });

  it('opens the unified CatsCo desktop modal from the top-right desktop button', async () => {
    const onDownload = vi.fn();
    await renderBar({ onDownload });
    const button = container.querySelector('button[aria-label="打开桌面端"]');
    expect(button).toBeTruthy();
    expect(button.getAttribute('title')).toBe('桌面端');
    await act(async () => button.click());
    expect(onDownload).toHaveBeenCalledTimes(1);
  });

  it('always renders the cloud button and enables it when an Agent resource handler is available', async () => {
    const onOpenCloudArtifacts = vi.fn();
    await renderBar({ onOpenCloudArtifacts });
    const button = container.querySelector('button[aria-label="打开产物"]');
    expect(button).toBeTruthy();
    expect(button.disabled).toBe(false);
    await act(async () => button.click());
    expect(onOpenCloudArtifacts).toHaveBeenCalledTimes(1);

    await renderBar({ onOpenCloudArtifacts: undefined });
    const unavailableButton = container.querySelector('button[aria-label="产物暂不可用"]');
    expect(unavailableButton).toBeTruthy();
    expect(unavailableButton.disabled).toBe(true);
  });

  it('keeps conversation-share image generation out of the top bar', async () => {
    await renderBar({ onCreateConversationShare: vi.fn() });
    expect(container.querySelector('button[aria-label="制作对话分享图"]')).toBeNull();
  });

  it('keeps the current model and quota together in the header', async () => {
    await renderBar();
    const status = container.querySelector('.v3-local-assistant-status');
    expect(status?.textContent).toBe('minimax-m3剩余 75%');
    expect(status?.getAttribute('aria-label')).toContain('minimax-m3');
  });

  it('keeps catalog context details out of the compact header', async () => {
    vi.spyOn(api, 'getBotModelConfig').mockResolvedValue(baseConfig);
    await renderBar({ activeAgent: { uid: 43, isOwner: true, relation: 'owner' } });
    const status = container.querySelector('.v3-local-assistant-status');
    expect(status?.textContent).toContain('minimax-m3');
    expect(status?.textContent).not.toContain('上下文');
    expect(status?.textContent).toContain('剩余 75%');
    expect(status?.title).toContain('上下文 1M');
  });

  it('shows only the custom model strength level in the compact header', async () => {
    vi.spyOn(api, 'getBotModelConfig').mockResolvedValue({
      ...baseConfig,
      desired: { kind: 'custom', model_id: 'custom', revision: 7 },
      applied: { kind: 'custom', model_id: 'custom', revision: 7 },
      custom: {
        protocol: 'anthropic',
        api_base: 'https://models.example.com',
        model: 'private-model',
        api_key_configured: true,
        api_key_hint: '****cret',
        context_window_tokens: 128000,
        reasoning_effort: 'high',
      },
    });
    await renderBar({ activeAgent: { uid: 43, isOwner: true, relation: 'owner' } });
    const status = container.querySelector('.v3-local-assistant-status');
    expect(status?.textContent).toContain('private-model');
    expect(status?.textContent).toContain('high');
    expect(status?.textContent).not.toContain('强度 high');
    expect(status?.textContent).not.toContain('上下文');
    expect(status?.textContent).toContain('自备模型');
    expect(status?.title).toContain('上下文 128K');
    expect(status?.getAttribute('aria-label')).toContain('推理强度 high');
  });

  it('shows the applied cloud model instead of a stale local quota snapshot', async () => {
    vi.spyOn(api, 'getBotModelConfig').mockResolvedValue({
      ...baseConfig,
      desired: { kind: 'catalog', model_id: 'gpt-5.6-sol', reasoning_effort: 'high', revision: 5 },
      applied: { kind: 'catalog', model_id: 'gpt-5.6-sol', reasoning_effort: 'high', revision: 5 },
      models: [
        ...baseConfig.models,
        { id: 'gpt-5.6-sol', label: 'GPT-5.6 Sol', reasoning_efforts: ['medium', 'high'] },
      ],
    });
    await renderBar({
      activeAgent: { uid: 43, isOwner: true, relation: 'owner' },
      agentModelState: {
        isBot: true,
        state: 'ready',
        summary: { source: 'custom', model: 'gpt-5.6-terra' },
      },
    });

    const status = container.querySelector('.v3-local-assistant-status');
    expect(status?.textContent).toContain('gpt-5.6-sol');
    expect(status?.textContent).toContain('high');
    expect(status?.textContent).not.toContain('强度 high');
    expect(status?.textContent).not.toContain('gpt-5.6-terra');
    expect(status?.getAttribute('aria-label')).toContain('推理强度 high');
  });

  it('does not expose a switcher for friend bots or group conversations', async () => {
    const getConfig = vi.spyOn(api, 'getBotModelConfig').mockResolvedValue(baseConfig);
    await renderBar({ activeAgent: { uid: 43, isOwner: false, relation: 'friend' } });
    expect(container.querySelector('.v3-model-status-button')).toBeNull();
    expect(getConfig).not.toHaveBeenCalled();

    await renderBar({ agentModelState: { isBot: false, state: 'hidden', summary: null }, activeAgent: null, title: '多 Agent 群聊' });
    expect(container.querySelector('.v3-local-assistant-status')).toBeNull();
  });

  it('keeps the switcher hidden when the owner is outside the rollout', async () => {
    const getConfig = vi.spyOn(api, 'getBotModelConfig').mockResolvedValue({
      ...baseConfig,
      management_enabled: false,
    });
    await renderBar({ activeAgent: { uid: 43, isOwner: true, relation: 'owner' } });
    expect(getConfig).toHaveBeenCalledWith(43, { includeUsage: false });
    expect(container.querySelector('.v3-model-status-button')).toBeNull();
    expect(container.querySelector('.v3-local-assistant-status')?.textContent).toContain('minimax-m3');
  });

  it('shows a clear unavailable state for an old CatsCo runtime', async () => {
    const getConfig = vi.spyOn(api, 'getBotModelConfig').mockResolvedValue({
      ...baseConfig,
      runtime_supported: false,
      runtime_unavailable_reason: '当前 CatsCo 版本暂不支持云端切换，请更新桌面端后再试',
      status: 'pending',
    });
    await renderBar({ activeAgent: { uid: 43, isOwner: true, relation: 'owner' } });
    expect(getConfig).toHaveBeenCalledWith(43, { includeUsage: false });
    expect(container.querySelector('.v3-model-status-button')).toBeNull();
    expect(container.querySelector('.v3-model-apply-state')).toBeNull();
    expect(container.querySelector('.v3-local-assistant-status')?.getAttribute('aria-label')).toContain('暂时无法切换');
    expect(container.querySelector('.v3-local-assistant-status')?.title).toContain('请更新桌面端');
  });

  it('loads quota once when the owner opens the list and shows it per model', async () => {
    const getConfig = vi.spyOn(api, 'getBotModelConfig').mockResolvedValue(baseConfig);
    await renderBar({ activeAgent: { uid: 43, isOwner: true, relation: 'owner' } });
    expect(getConfig).toHaveBeenCalledWith(43, { includeUsage: false });

    await act(async () => {
      container.querySelector('.v3-model-status-button').click();
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(getConfig).toHaveBeenCalledWith(43, { includeUsage: true });
    const m3 = [...container.querySelectorAll('.v3-model-menu-item')]
      .find((item) => item.textContent.includes('MiniMax M3'));
    const m27 = [...container.querySelectorAll('.v3-model-menu-item')]
      .find((item) => item.textContent.includes('MiniMax M2.7'));
    const deepseek = [...container.querySelectorAll('.v3-model-menu-item')]
      .find((item) => item.textContent.includes('DeepSeek V4 Flash'));
    const terra = [...container.querySelectorAll('.v3-model-menu-item')]
      .find((item) => item.textContent.includes('GPT-5.6 Terra'));
    expect(m27?.textContent).toContain('标准额度，适合日常任务');
    expect(m3?.textContent).toContain('支持多模态与长上下文');
    expect(deepseek?.textContent).toContain('低额度 Flash，支持推理强度');
    expect(terra?.textContent).toContain('OpenAI Responses，支持精细推理强度');
    expect(m27?.textContent).toContain('上下文 204.8K');
    expect(m3?.textContent).toContain('上下文 1M');
    expect(deepseek?.textContent).toContain('上下文 1M');
    expect(terra?.textContent).toContain('上下文 256K');
    expect(m3?.textContent).toContain('剩余 75%');
    expect(deepseek?.textContent).toContain('剩余 10%');
    expect(container.textContent).not.toContain('¥');
    expect(container.textContent).not.toContain('CNY');
    expect(deepseek?.querySelector('.v3-model-menu-quota.warning')).toBeTruthy();
  });

  it('keeps a revoked current model visible but prevents selecting it again', async () => {
    const revokedConfig = {
      ...baseConfig,
      desired: { kind: 'catalog', model_id: 'gpt-5.6-terra', reasoning_effort: 'medium', revision: 3 },
      applied: { kind: 'catalog', model_id: 'gpt-5.6-terra', reasoning_effort: 'medium', revision: 3 },
      models: [
        baseConfig.models[1],
        {
          ...baseConfig.models[3],
          available: false,
          unavailable_reason: '当前套餐已不包含该模型，切换后不可再选',
        },
      ],
    };
    vi.spyOn(api, 'getBotModelConfig').mockResolvedValue(revokedConfig);
    const update = vi.spyOn(api, 'updateBotModelConfig');
    await renderBar({ activeAgent: { uid: 43, isOwner: true, relation: 'owner' } });

    await act(async () => container.querySelector('.v3-model-status-button').click());
    const terra = [...container.querySelectorAll('.v3-model-menu-item')]
      .find((item) => item.textContent.includes('GPT-5.6 Terra'));
    expect(terra).toBeTruthy();
    expect(terra.disabled).toBe(true);
    expect(terra.classList.contains('unavailable')).toBe(true);
    expect(terra.textContent).toContain('当前套餐已不包含该模型');
    await act(async () => terra.click());
    expect(update).not.toHaveBeenCalled();
  });

  it('selects official reasoning strength with an explicit catalog payload', async () => {
    vi.spyOn(api, 'getBotModelConfig').mockResolvedValue(baseConfig);
    const update = vi.spyOn(api, 'updateBotModelConfig').mockResolvedValue({
      ...baseConfig,
      status: 'pending',
      desired: { kind: 'catalog', model_id: 'gpt-5.6-terra', reasoning_effort: 'xhigh', revision: 3 },
    });
    await renderBar({ activeAgent: { uid: 43, isOwner: true, relation: 'owner' } });
    await act(async () => container.querySelector('.v3-model-status-button').click());
    const terra = [...container.querySelectorAll('.v3-model-menu-item')]
      .find((item) => item.textContent.includes('GPT-5.6 Terra'));
    await act(async () => terra.click());
    const xhigh = [...container.querySelectorAll('.v3-model-reasoning-item')]
      .find((item) => item.textContent.includes('xhigh'));
    await act(async () => {
      xhigh.click();
      await Promise.resolve();
    });
    expect(update).toHaveBeenCalledWith(43, {
      kind: 'catalog', model_id: 'gpt-5.6-terra', reasoning_effort: 'xhigh',
    });
  });

  it('shows vision beside reasoning strength as a read-only automatic capability', async () => {
    vi.spyOn(api, 'getBotModelConfig').mockResolvedValue(baseConfig);
    await renderBar({ activeAgent: { uid: 43, isOwner: true, relation: 'owner' } });
    await act(async () => container.querySelector('.v3-model-status-button').click());
    const deepseek = [...container.querySelectorAll('.v3-model-menu-item')]
      .find((item) => item.textContent.includes('DeepSeek V4 Flash'));
    await act(async () => deepseek.click());
    expect(container.textContent).toContain('推理强度');
    expect(container.textContent).toContain('视觉');
    expect(container.textContent).toContain('图片输入自动启用');
    const vision = [...container.querySelectorAll('.v3-model-reasoning-item')]
      .find((item) => item.textContent.includes('图片输入自动启用'));
    expect(vision?.getAttribute('role')).toBe('note');
    expect(vision?.classList.contains('is-readonly')).toBe(true);
    expect(vision?.querySelector('button')).toBeNull();
  });

  it('edits a cloud custom model without receiving or resending the stored API key', async () => {
    const customConfig = {
      ...baseConfig,
      desired: { kind: 'custom', model_id: 'gpt-5.6-sol', reasoning_effort: 'high', revision: 4 },
      custom: {
        protocol: 'openai-responses',
        api_base: 'https://models.example.com/v1',
        model: 'gpt-5.6-sol',
        api_key_configured: true,
        api_key_hint: '****cret',
        context_window_tokens: 1000000,
        reasoning_effort: 'high',
      },
    };
    vi.spyOn(api, 'getBotModelConfig').mockResolvedValue(customConfig);
    const update = vi.spyOn(api, 'updateBotModelConfig').mockResolvedValue({ ...customConfig, status: 'pending' });
    await renderBar({ activeAgent: { uid: 43, isOwner: true, relation: 'owner' } });
    await act(async () => container.querySelector('.v3-model-status-button').click());
    const customEntry = [...container.querySelectorAll('.v3-model-menu-item')]
      .find((item) => item.textContent.includes('自定义模型'));
    await act(async () => customEntry.click());

    expect(container.textContent).not.toContain('sk-super-secret');
    const keyInput = container.querySelector('input[type="password"]');
    expect(keyInput.value).toBe('');
    expect(keyInput.placeholder).toContain('****cret');
    const backButton = container.querySelector('.v3-custom-model-heading button');
    expect(backButton.getAttribute('aria-label')).toBe('返回模型列表');
    expect(backButton.textContent).toBe('');
    const protocolSelect = container.querySelector('.v3-custom-model-select-trigger[aria-label="API 协议"]');
    expect(protocolSelect.closest('.v3-custom-model-select-wrap')).not.toBeNull();
    expect(protocolSelect.querySelector('.v3-custom-model-select-chevron')).not.toBeNull();
    expect(container.querySelector('.v3-custom-model-select-trigger[aria-label="上下文大小"]')?.textContent).toBe('1M');
    expect(container.textContent).not.toContain('最大输出 Token');
    expect(container.textContent).not.toContain('温度');
    await act(async () => {
      container.querySelector('.v3-custom-model-editor').dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
      await Promise.resolve();
    });
    expect(update).toHaveBeenCalledWith(43, expect.objectContaining({
      kind: 'custom',
      model_id: 'custom',
      custom: expect.objectContaining({
        protocol: 'openai-responses', model: 'gpt-5.6-sol', api_key: '',
      }),
    }));
  });

  it('keeps the saved custom editor populated while a relay catalog model is active', async () => {
    const catalogWithSavedCustom = {
      ...baseConfig,
      status: 'applied',
      desired: { kind: 'catalog', model_id: 'gpt-5.6-terra', reasoning_effort: 'high', revision: 5 },
      applied: { kind: 'catalog', model_id: 'gpt-5.6-terra', reasoning_effort: 'high', revision: 5 },
      custom: {
        protocol: 'openai-responses',
        api_base: 'https://models.example.com/v1',
        model: 'private-model',
        api_key_configured: true,
        api_key_hint: '****cret',
        reasoning_effort: 'xhigh',
      },
    };
    vi.spyOn(api, 'getBotModelConfig').mockResolvedValue(catalogWithSavedCustom);
    const update = vi.spyOn(api, 'updateBotModelConfig').mockResolvedValue({
      ...catalogWithSavedCustom,
      status: 'pending',
      desired: { kind: 'custom', model_id: 'private-model', reasoning_effort: 'xhigh', revision: 6 },
    });
    await renderBar({ activeAgent: { uid: 43, isOwner: true, relation: 'owner' } });
    await act(async () => container.querySelector('.v3-model-status-button').click());
    const customEntry = [...container.querySelectorAll('.v3-model-menu-item')]
      .find((item) => item.textContent.includes('自定义模型'));
    expect(customEntry.textContent).toContain('private-model');
    expect(customEntry.textContent).toContain('****cret');
    await act(async () => customEntry.click());

    const textInputs = [...container.querySelectorAll('.v3-custom-model-editor input')]
      .filter((input) => input.type !== 'password' && input.type !== 'number');
    expect(textInputs.map((input) => input.value)).toEqual([
      'https://models.example.com/v1',
      'private-model',
    ]);
    expect(container.querySelector('input[type="password"]').value).toBe('');
    expect(container.querySelector('input[type="password"]').placeholder).toContain('****cret');
    await act(async () => {
      container.querySelector('.v3-custom-model-editor')
        .dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
      await Promise.resolve();
    });
    expect(update).toHaveBeenCalledWith(43, expect.objectContaining({
      kind: 'custom',
      custom: expect.objectContaining({
        protocol: 'openai-responses',
        api_base: 'https://models.example.com/v1',
        model: 'private-model',
        api_key: '',
        reasoning_effort: 'xhigh',
      }),
    }));
  });

  it('keeps the legacy context window editable while never resending max_tokens', async () => {
    const legacyConfig = {
      ...baseConfig,
      desired: { kind: 'custom', model_id: 'legacy-model', reasoning_effort: '', revision: 4 },
      custom: {
        protocol: 'openai-chat',
        api_base: 'https://models.example.com/v1',
        model: 'legacy-model',
        api_key_configured: true,
        context_window_tokens: 272000,
        max_tokens: 8192,
      },
    };
    vi.spyOn(api, 'getBotModelConfig').mockResolvedValue(legacyConfig);
    const update = vi.spyOn(api, 'updateBotModelConfig').mockResolvedValue({ ...legacyConfig, status: 'pending' });
    await renderBar({ activeAgent: { uid: 43, isOwner: true, relation: 'owner' } });
    await act(async () => container.querySelector('.v3-model-status-button').click());
    const customEntry = [...container.querySelectorAll('.v3-model-menu-item')]
      .find((item) => item.textContent.includes('自定义模型'));
    await act(async () => customEntry.click());

    expect(container.querySelector('.v3-custom-model-select-trigger[aria-label="上下文大小"]')?.textContent).toBe('272K');
    expect(container.textContent).not.toContain('最大输出 Token');
    await act(async () => {
      container.querySelector('.v3-custom-model-editor').dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
      await Promise.resolve();
    });
    const payload = update.mock.calls[0][1];
    expect(payload.custom).toHaveProperty('context_window_tokens', 272000);
    expect(payload.custom).not.toHaveProperty('max_tokens');
  });

  it('locks repeated model changes while a saved revision is waiting for the bot', async () => {
    vi.useFakeTimers();
    const pending = {
      ...baseConfig,
      status: 'pending',
      desired: { kind: 'catalog', model_id: 'minimax-m3', reasoning_effort: '', revision: 3 },
    };
    vi.spyOn(api, 'getBotModelConfig').mockResolvedValue(pending);
    await renderBar({ activeAgent: { uid: 43, isOwner: true, relation: 'owner' } });
    const trigger = container.querySelector('.v3-model-status-button');
    expect(trigger.disabled).toBe(true);
    expect(trigger.getAttribute('aria-busy')).toBe('true');
    expect(container.querySelector('.v3-model-apply-state')).toBeNull();
    expect(trigger.getAttribute('aria-label')).toContain('切换中');

    await act(async () => vi.advanceTimersByTimeAsync(45000));
    expect(trigger.disabled).toBe(false);
    expect(trigger.getAttribute('aria-label')).toContain('待应用');
  });

  it('keeps return-to-local locked until the bot acknowledges the handoff', async () => {
    vi.useFakeTimers();
    vi.spyOn(api, 'getBotModelConfig').mockResolvedValue({
      ...baseConfig,
      configured: false,
      status: 'pending',
      desired: { kind: 'local', model_id: 'local', reasoning_effort: '', revision: 5 },
    });
    await renderBar({ activeAgent: { uid: 43, isOwner: true, relation: 'owner' } });

    const trigger = container.querySelector('.v3-model-status-button');
    expect(trigger.disabled).toBe(true);
    expect(trigger.getAttribute('aria-busy')).toBe('true');
    expect(container.querySelector('.v3-model-apply-state')).toBeNull();
    expect(trigger.getAttribute('aria-label')).toContain('切换中');
  });

  it('classifies request and runtime apply failures for users', () => {
    expect(describeModelConfigRequestError({ code: 'NETWORK_ERROR' })).toContain('网络连接中断');
    expect(describeModelConfigRequestError({ status: 429 })).toContain('操作过于频繁');
    expect(describeModelConfigRequestError({ status: 403, data: { code: 'model_not_in_plan' } }))
      .toContain('当前套餐未包含');
    expect(describeModelConfigRequestError({ status: 503, data: { code: 'model_entitlement_unavailable' } }))
      .toContain('套餐额度暂时无法确认');
    expect(describeModelConfigRequestError({
      status: 503,
      message: '服务暂时不可用，请稍后重试',
      data: { error: 'custom model encryption unavailable' },
    }))
      .toContain('安全密钥存储');
    expect(describeModelApplyError('401 Unauthorized: invalid api key')).toContain('鉴权失败');
    expect(describeModelApplyError('429 quota exceeded')).toContain('额度不足');
    expect(describeModelApplyError('fetch failed: connection timeout')).toContain('连接模型服务超时');
  });
});
