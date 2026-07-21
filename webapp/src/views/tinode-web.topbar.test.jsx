import React, { act } from 'react';
import { createRoot } from 'react-dom/client';

import {
  describeModelApplyError,
  describeModelConfigRequestError,
  LocalAssistantBar,
} from './tinode-web';
import { api } from '../api';

const modelConfig = {
  uid: 43,
  configured: true,
  status: 'applied',
  desired: { model_id: 'minimax-m3', reasoning_effort: '', revision: 2 },
  applied: { model_id: 'minimax-m3', reasoning_effort: '', revision: 2 },
  models: [
    { id: 'minimax-m3', label: 'MiniMax M3', description: '支持多模态与长上下文' },
    {
      id: 'deepseek-v4-flash',
      label: 'DeepSeek V4 Flash',
      description: '低额度 Flash，支持推理强度',
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

describe('LocalAssistantBar model quota', () => {
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

  it('keeps the active model and remaining quota in one header status group', async () => {
    await act(async () => {
      root.render(
        <LocalAssistantBar
          currentModelName="MiniMax-M2.7"
          agentQuota={{
            source: 'relay',
            model: 'gpt-5.6-terra',
            remaining_percent: 72,
            status: 'normal',
          }}
          theme="dark"
          onToggleTheme={vi.fn()}
          onDownload={vi.fn()}
          title="XiaoBa"
        />,
      );
    });

    const status = container.querySelector('.v3-local-assistant-status');
    expect(status?.textContent).toBe('gpt-5.6-terra剩余 72%');
    expect(status?.getAttribute('aria-label')).toBe('当前使用的模型：gpt-5.6-terra，剩余 72%');
    expect(status?.querySelector('.v3-model-quota')?.textContent).toBe('剩余 72%');
  });

  it('shows a custom model as the model name without duplicating it in quota text', async () => {
    await act(async () => {
      root.render(
        <LocalAssistantBar
          currentModelName="MiniMax-M2.7"
          agentQuota={{ source: 'custom', model: 'gpt-5.6-sol', status: 'custom' }}
          theme="light"
          onToggleTheme={vi.fn()}
          onDownload={vi.fn()}
          title="XiaoBa"
        />,
      );
    });

    expect(container.querySelector('.v3-current-model-name')?.textContent).toBe('gpt-5.6-sol');
    expect(container.querySelector('.v3-model-quota')?.textContent).toBe('自备模型');
  });

  it('does not imply a single active model in a group conversation', async () => {
    await act(async () => {
      root.render(
        <LocalAssistantBar
          currentModelName="MiniMax-M2.7"
          agentQuota={null}
          showModelStatus={false}
          theme="light"
          onToggleTheme={vi.fn()}
          onDownload={vi.fn()}
          title="多 Agent 项目群"
        />,
      );
    });

    expect(container.querySelector('.v3-local-assistant-status')).toBeNull();
    expect(container.textContent).toContain('多 Agent 项目群');
  });

  it('lets only the owner open the model menu and select a GPT-5.6 reasoning effort', async () => {
    vi.spyOn(api, 'getBotModelConfig').mockResolvedValue(modelConfig);
    vi.spyOn(api, 'updateBotModelConfig').mockResolvedValue({
      ...modelConfig,
      status: 'pending',
      desired: { model_id: 'gpt-5.6-terra', reasoning_effort: 'xhigh', revision: 3 },
    });

    await act(async () => {
      root.render(
        <LocalAssistantBar
          currentModelName="MiniMax-M3"
          agentQuota={{ source: 'relay', model: 'MiniMax-M3', remaining_percent: 72, status: 'normal' }}
          activeAgent={{ uid: 43, isOwner: true, relation: 'owner' }}
          theme="dark"
          onToggleTheme={vi.fn()}
          onDownload={vi.fn()}
          title="Owned Agent"
        />,
      );
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(api.getBotModelConfig).toHaveBeenCalledWith(43);
    const trigger = container.querySelector('.v3-model-status-button');
    await act(async () => trigger.click());
    const terra = [...container.querySelectorAll('.v3-model-menu-item')]
      .find((button) => button.textContent.includes('GPT-5.6 Terra'));
    await act(async () => terra.click());
    const xhigh = [...container.querySelectorAll('.v3-model-reasoning-item')]
      .find((button) => button.textContent.includes('xhigh'));
    expect(xhigh).toBeTruthy();

    await act(async () => {
      xhigh.click();
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(api.updateBotModelConfig).toHaveBeenCalledWith(43, {
      model_id: 'gpt-5.6-terra',
      reasoning_effort: 'xhigh',
    });
  });

  it('keeps a friend bot model status display-only', async () => {
    const getConfig = vi.spyOn(api, 'getBotModelConfig').mockResolvedValue(modelConfig);
    await act(async () => {
      root.render(
        <LocalAssistantBar
          currentModelName="MiniMax-M3"
          agentQuota={{ source: 'relay', model: 'MiniMax-M3', remaining_percent: 72, status: 'normal' }}
          activeAgent={{ uid: 43, isOwner: false, relation: 'friend' }}
          theme="light"
          onToggleTheme={vi.fn()}
          onDownload={vi.fn()}
          title="Friend Agent"
        />,
      );
      await Promise.resolve();
    });

    expect(container.querySelector('.v3-local-assistant-status')).toBeTruthy();
    expect(container.querySelector('.v3-model-status-button')).toBeNull();
    expect(getConfig).not.toHaveBeenCalled();
  });

  it('locks repeated choices while the device is applying a model and unlocks after acknowledgement', async () => {
    vi.useFakeTimers();
    const appliedConfig = {
      ...modelConfig,
      status: 'applied',
      desired: { model_id: 'gpt-5.6-terra', reasoning_effort: 'xhigh', revision: 3 },
      applied: { model_id: 'gpt-5.6-terra', reasoning_effort: 'xhigh', revision: 3 },
    };
    vi.spyOn(api, 'getBotModelConfig')
      .mockResolvedValueOnce(modelConfig)
      .mockResolvedValue(appliedConfig);
    let resolveUpdate;
    const update = vi.spyOn(api, 'updateBotModelConfig').mockImplementation(() => new Promise((resolve) => {
      resolveUpdate = resolve;
    }));

    await act(async () => {
      root.render(
        <LocalAssistantBar
          currentModelName="MiniMax-M3"
          agentQuota={{ source: 'relay', model: 'MiniMax-M3', remaining_percent: 72, status: 'normal' }}
          activeAgent={{ uid: 43, isOwner: true, relation: 'owner' }}
          theme="dark"
          onToggleTheme={vi.fn()}
          onDownload={vi.fn()}
          title="Owned Agent"
        />,
      );
      await Promise.resolve();
      await Promise.resolve();
    });

    const trigger = container.querySelector('.v3-model-status-button');
    await act(async () => trigger.click());
    const terra = [...container.querySelectorAll('.v3-model-menu-item')]
      .find((button) => button.textContent.includes('GPT-5.6 Terra'));
    await act(async () => terra.click());
    const xhigh = [...container.querySelectorAll('.v3-model-reasoning-item')]
      .find((button) => button.textContent.includes('xhigh'));
    await act(async () => xhigh.click());

    expect(trigger.disabled).toBe(true);
    expect(trigger.getAttribute('aria-busy')).toBe('true');
    expect(container.querySelector('.v3-model-switch-spinner')).toBeTruthy();
    expect(container.querySelector('.v3-model-apply-state')?.textContent).toBe('保存中');
    xhigh.click();
    expect(update).toHaveBeenCalledTimes(1);

    await act(async () => {
      resolveUpdate({
        ...modelConfig,
        status: 'pending',
        desired: { model_id: 'gpt-5.6-terra', reasoning_effort: 'xhigh', revision: 3 },
      });
      await Promise.resolve();
    });
    expect(trigger.disabled).toBe(true);
    expect(container.querySelector('.v3-model-apply-state')?.textContent).toBe('切换中');

    await act(async () => {
      await vi.advanceTimersByTimeAsync(2000);
    });
    expect(trigger.disabled).toBe(false);
    expect(trigger.getAttribute('aria-busy')).toBe('false');
    expect(container.querySelector('.v3-model-switch-spinner')).toBeNull();
    expect(container.querySelector('.v3-current-model-name')?.textContent).toBe('GPT-5.6 Terra');
  });

  it('shows an actionable owner error instead of the backend error text', async () => {
    vi.spyOn(api, 'getBotModelConfig').mockResolvedValue(modelConfig);
    vi.spyOn(api, 'updateBotModelConfig').mockRejectedValue(
      Object.assign(new Error('not your bot'), { status: 403 }),
    );
    await act(async () => {
      root.render(
        <LocalAssistantBar
          currentModelName="MiniMax-M3"
          agentQuota={{ source: 'relay', model: 'MiniMax-M3', remaining_percent: 72, status: 'normal' }}
          activeAgent={{ uid: 43, isOwner: true, relation: 'owner' }}
          theme="light"
          onToggleTheme={vi.fn()}
          onDownload={vi.fn()}
          title="Owned Agent"
        />,
      );
      await Promise.resolve();
      await Promise.resolve();
    });

    await act(async () => container.querySelector('.v3-model-status-button').click());
    const local = [...container.querySelectorAll('.v3-model-menu-item')]
      .find((button) => button.textContent.includes('设备本地配置'));
    await act(async () => {
      local.click();
      await Promise.resolve();
      await Promise.resolve();
    });
    const feedback = container.querySelector('.v3-model-menu-feedback.error');
    expect(feedback?.textContent).toContain('只有机器人创建者可以切换模型');
    expect(feedback?.textContent).not.toContain('not your bot');
  });

  it('does not carry an unfinished save lock into another bot conversation', async () => {
    vi.spyOn(api, 'getBotModelConfig').mockImplementation(async (uid) => ({
      ...modelConfig,
      uid,
    }));
    let resolveUpdate;
    vi.spyOn(api, 'updateBotModelConfig').mockImplementation(() => new Promise((resolve) => {
      resolveUpdate = resolve;
    }));
    const renderBar = (uid) => (
      <LocalAssistantBar
        currentModelName="MiniMax-M3"
        agentQuota={{ source: 'relay', model: 'MiniMax-M3', remaining_percent: 72, status: 'normal' }}
        activeAgent={{ uid, isOwner: true, relation: 'owner' }}
        theme="light"
        onToggleTheme={vi.fn()}
        onDownload={vi.fn()}
        title={`Owned Agent ${uid}`}
      />
    );

    await act(async () => {
      root.render(renderBar(43));
      await Promise.resolve();
      await Promise.resolve();
    });
    let trigger = container.querySelector('.v3-model-status-button');
    await act(async () => trigger.click());
    const terra = [...container.querySelectorAll('.v3-model-menu-item')]
      .find((button) => button.textContent.includes('GPT-5.6 Terra'));
    await act(async () => terra.click());
    const xhigh = [...container.querySelectorAll('.v3-model-reasoning-item')]
      .find((button) => button.textContent.includes('xhigh'));
    await act(async () => xhigh.click());
    expect(trigger.disabled).toBe(true);

    await act(async () => {
      root.render(renderBar(44));
      await Promise.resolve();
      await Promise.resolve();
    });
    trigger = container.querySelector('.v3-model-status-button');
    expect(api.getBotModelConfig).toHaveBeenCalledWith(44);
    expect(trigger.disabled).toBe(false);
    expect(container.querySelector('.v3-model-switch-spinner')).toBeNull();

    await act(async () => {
      resolveUpdate({
        ...modelConfig,
        uid: 43,
        status: 'pending',
        desired: { model_id: 'gpt-5.6-terra', reasoning_effort: 'xhigh', revision: 3 },
      });
      await Promise.resolve();
    });
    expect(container.querySelector('.v3-model-status-button').disabled).toBe(false);
  });

  it('stays retryable when a new save fails after the device wait timed out', async () => {
    vi.useFakeTimers();
    const pendingConfig = {
      ...modelConfig,
      status: 'pending',
      desired: { model_id: 'gpt-5.6-terra', reasoning_effort: 'high', revision: 3 },
    };
    vi.spyOn(api, 'getBotModelConfig').mockResolvedValue(pendingConfig);
    vi.spyOn(api, 'updateBotModelConfig').mockRejectedValue(
      Object.assign(new Error('service unavailable'), { status: 503 }),
    );
    await act(async () => {
      root.render(
        <LocalAssistantBar
          currentModelName="MiniMax-M3"
          agentQuota={{ source: 'relay', model: 'MiniMax-M3', remaining_percent: 72, status: 'normal' }}
          activeAgent={{ uid: 43, isOwner: true, relation: 'owner' }}
          theme="light"
          onToggleTheme={vi.fn()}
          onDownload={vi.fn()}
          title="Owned Agent"
        />,
      );
      await Promise.resolve();
      await Promise.resolve();
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(45000);
    });

    const trigger = container.querySelector('.v3-model-status-button');
    expect(trigger.disabled).toBe(false);
    expect(container.querySelector('.v3-model-apply-state')?.textContent).toBe('待应用');
    await act(async () => trigger.click());
    const local = [...container.querySelectorAll('.v3-model-menu-item')]
      .find((button) => button.textContent.includes('设备本地配置'));
    await act(async () => {
      local.click();
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(trigger.disabled).toBe(false);
    expect(container.querySelector('.v3-model-menu-feedback.error')?.textContent)
      .toContain('模型配置服务暂时不可用');
  });

  it('classifies request and runtime apply failures for users', () => {
    expect(describeModelConfigRequestError({ code: 'NETWORK_ERROR' }))
      .toContain('网络连接中断');
    expect(describeModelConfigRequestError({ status: 429 }))
      .toContain('操作过于频繁');
    expect(describeModelApplyError('401 Unauthorized: invalid api key'))
      .toContain('鉴权失败');
    expect(describeModelApplyError('429 quota exceeded'))
      .toContain('额度不足');
    expect(describeModelApplyError('fetch failed: connection timeout'))
      .toContain('连接模型服务超时');
  });
});
