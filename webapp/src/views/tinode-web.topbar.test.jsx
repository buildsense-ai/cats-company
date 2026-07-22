import React, { act } from 'react';
import { createRoot } from 'react-dom/client';

import {
  canOpenCloudArtifacts,
  describeModelApplyError,
  describeModelConfigRequestError,
  LocalAssistantBar,
  resolveDisplayedActiveAgent,
} from './tinode-web';
import { api } from '../api';

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
      id: 'minimax-m3',
      label: 'MiniMax M3',
      description: '支持多模态与长上下文',
      quota: { model: 'minimax-m3', limit_cny: 100, remaining_cny: 75, percent: 25, status: 'normal' },
    },
    {
      id: 'deepseek-v4-flash',
      label: 'DeepSeek V4 Flash',
      description: '低额度 Flash，支持推理强度',
      quota: { model: 'deepseek-v4-flash', limit_cny: 50, remaining_cny: 5, percent: 90, status: 'high' },
      reasoning_efforts: ['high', 'max', 'disabled'],
      default_reasoning_effort: 'high',
    },
    {
      id: 'gpt-5.6-terra',
      label: 'GPT-5.6 Terra',
      description: 'OpenAI Responses，支持精细推理强度',
      reasoning_efforts: ['none', 'minimal', 'low', 'medium', 'high', 'xhigh'],
      default_reasoning_effort: 'medium',
    },
  ],
};

const relayState = {
  isBot: true,
  state: 'ready',
  summary: {
    source: 'relay', model: 'minimax-m3', limit_cny: 100, percent: 25, remaining_percent: 75, status: 'normal',
  },
};

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
  it('is available only in a private chat with a capable agent', () => {
    const doubao = { uid: 440, cloud_artifacts_enabled: true };
    expect(canOpenCloudArtifacts({ topicId: 'p2p_7_440', isGroup: false }, doubao)).toBe(true);
    expect(canOpenCloudArtifacts({ topicId: 'grp_8', isGroup: true }, doubao)).toBe(false);
    expect(canOpenCloudArtifacts({ topicId: 'p2p_7_441', isGroup: false }, { uid: 441 })).toBe(false);
    expect(canOpenCloudArtifacts(null, doubao)).toBe(false);
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

  it('renders the generated-artifacts button only when the parent enables it', async () => {
    const onOpenCloudArtifacts = vi.fn();
    await renderBar({ onOpenCloudArtifacts });
    const button = container.querySelector('button[aria-label="打开生成物"]');
    expect(button).toBeTruthy();
    await act(async () => button.click());
    expect(onOpenCloudArtifacts).toHaveBeenCalledTimes(1);

    await renderBar({ onOpenCloudArtifacts: undefined });
    expect(container.querySelector('button[aria-label="打开生成物"]')).toBeNull();
  });

  it('keeps the current model and quota together in the header', async () => {
    await renderBar();
    const status = container.querySelector('.v3-local-assistant-status');
    expect(status?.textContent).toBe('minimax-m3剩余 75%');
    expect(status?.getAttribute('aria-label')).toContain('minimax-m3');
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
    expect(container.querySelector('.v3-model-apply-state')?.textContent).toBe('暂时无法切换');
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
    const deepseek = [...container.querySelectorAll('.v3-model-menu-item')]
      .find((item) => item.textContent.includes('DeepSeek V4 Flash'));
    expect(m3?.textContent).toContain('剩余 75% · ¥75.00');
    expect(deepseek?.textContent).toContain('剩余 10% · ¥5.00');
    expect(deepseek?.querySelector('.v3-model-menu-quota.warning')).toBeTruthy();
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

  it('edits a cloud custom model without receiving or resending the stored API key', async () => {
    const customConfig = {
      ...baseConfig,
      desired: { kind: 'custom', model_id: 'private-model', reasoning_effort: 'high', revision: 4 },
      custom: {
        protocol: 'openai-responses',
        api_base: 'https://models.example.com/v1',
        model: 'private-model',
        api_key_configured: true,
        api_key_hint: '****cret',
        context_window_tokens: 256000,
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
    await act(async () => {
      container.querySelector('.v3-custom-model-editor').dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
      await Promise.resolve();
    });
    expect(update).toHaveBeenCalledWith(43, expect.objectContaining({
      kind: 'custom',
      model_id: 'custom',
      custom: expect.objectContaining({
        protocol: 'openai-responses', model: 'private-model', api_key: '', context_window_tokens: 256000,
      }),
    }));
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
    expect(container.querySelector('.v3-model-apply-state')?.textContent).toBe('切换中');

    await act(async () => vi.advanceTimersByTimeAsync(45000));
    expect(trigger.disabled).toBe(false);
    expect(container.querySelector('.v3-model-apply-state')?.textContent).toBe('待应用');
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
    expect(container.querySelector('.v3-model-apply-state')?.textContent).toBe('切换中');
  });

  it('classifies request and runtime apply failures for users', () => {
    expect(describeModelConfigRequestError({ code: 'NETWORK_ERROR' })).toContain('网络连接中断');
    expect(describeModelConfigRequestError({ status: 429 })).toContain('操作过于频繁');
    expect(describeModelConfigRequestError({ status: 503, message: 'custom model encryption unavailable' }))
      .toContain('安全密钥存储');
    expect(describeModelApplyError('401 Unauthorized: invalid api key')).toContain('鉴权失败');
    expect(describeModelApplyError('429 quota exceeded')).toContain('额度不足');
    expect(describeModelApplyError('fetch failed: connection timeout')).toContain('连接模型服务超时');
  });
});
