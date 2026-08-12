import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';
import SystemPromptView, {
  MAX_SYSTEM_PROMPT_BYTES,
  normalizePromptDefinition,
  promptByteLength,
  resolvePromptApplyState,
} from './system-prompt-view';
import { api } from '../api';
import { FeedbackProvider } from '../components/feedback-system';

vi.mock('../api', () => ({
  api: {
    getMyBots: vi.fn(),
    getBotDefinitionPrompt: vi.fn(),
    updateBotDefinitionPrompt: vi.fn(),
  },
}));

function definition({
  revision = 3,
  selected = 'default',
  customSystemPrompt = '',
  appliedRevision = revision,
  lastAttemptRevision = revision,
  lastError = '',
  appliedAt = '',
  appliedKind = '',
  appliedModelId = '',
} = {}) {
  return {
    configured: true,
    revision,
    definition: {
      schema: 'xiaoba.bot-definition.v1',
      botId: '42',
      model: { kind: 'catalog', modelId: 'minimax-m3' },
      prompt: {
        selected,
        ...(customSystemPrompt ? { customSystemPrompt } : {}),
      },
      skills: [],
    },
    runtime: {
      desiredRevision: revision,
      appliedRevision,
      lastAttemptRevision,
      lastError,
      appliedAt,
      appliedKind,
      appliedModelId,
    },
  };
}

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, reject, resolve };
}

async function settle() {
  await Promise.resolve();
  await Promise.resolve();
}

describe('SystemPromptView helpers', () => {
  it('counts UTF-8 bytes and normalizes missing prompt fields to default', () => {
    expect(promptByteLength('CatsCo')).toBe(6);
    expect(promptByteLength('小八')).toBe(6);
    expect(promptByteLength('a'.repeat(MAX_SYSTEM_PROMPT_BYTES))).toBe(MAX_SYSTEM_PROMPT_BYTES);
    expect(normalizePromptDefinition({ definition: {} })).toEqual({
      selected: 'default',
      customSystemPrompt: '',
    });
  });

  it('distinguishes pending, applied, and failed runtime revisions', () => {
    expect(resolvePromptApplyState(definition({ appliedRevision: 2, lastAttemptRevision: 2 })))
      .toMatchObject({ kind: 'pending', label: '待应用' });
    expect(resolvePromptApplyState(definition()))
      .toMatchObject({ kind: 'applied', label: '已生效' });
    expect(resolvePromptApplyState(definition({
      appliedRevision: 2,
      lastAttemptRevision: 3,
      lastError: 'Bot 配置应用失败',
    }))).toMatchObject({ kind: 'error', label: '应用失败' });
  });

  it('requires runtime acknowledgement evidence before treating revision zero as applied', () => {
    expect(resolvePromptApplyState(definition({ revision: 0 })))
      .toMatchObject({ kind: 'pending', label: '待应用' });
    expect(resolvePromptApplyState(definition({
      revision: 0,
      appliedAt: '2026-08-12T10:00:00Z',
    }))).toMatchObject({ kind: 'applied', label: '已生效' });
  });
});

describe('SystemPromptView', () => {
  let container;
  let root;

  beforeEach(() => {
    Object.values(api).forEach((mock) => mock.mockReset());
    globalThis.localStorage?.clear();
    api.getMyBots.mockResolvedValue({
      bots: [
        { uid: 42, display_name: 'Owner Bot', relation: 'owner' },
        { uid: 43, display_name: 'Friend Bot', relation: 'friend' },
      ],
    });
    api.getBotDefinitionPrompt.mockResolvedValue(definition());
    api.updateBotDefinitionPrompt.mockResolvedValue(definition({
      revision: 4,
      selected: 'custom',
      customSystemPrompt: 'Be precise.',
      appliedRevision: 3,
      lastAttemptRevision: 3,
    }));
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(async () => {
    vi.useRealTimers();
    await act(async () => root.unmount());
    container.remove();
  });

  async function renderView(props = {}) {
    await act(async () => {
      root.render(
        <FeedbackProvider>
          <SystemPromptView user={{ uid: 7 }} {...props} />
        </FeedbackProvider>,
      );
      await settle();
    });
  }

  function modeButton(label) {
    return [...container.querySelectorAll('.cc-system-prompt-mode button')]
      .find((button) => button.textContent.includes(label));
  }

  it('shows only owner Bots and loads the canonical prompt revision', async () => {
    await renderView();

    const options = [...container.querySelectorAll('.cc-system-prompt-agent-picker option')];
    expect(options.map((option) => option.textContent)).toEqual(['Owner Bot']);
    expect(api.getBotDefinitionPrompt).toHaveBeenCalledWith('42');
    expect(container.textContent).toContain('云端 revision3');
    expect(container.textContent).toContain('已生效');
  });

  it('recovers from an initial prompt load failure through the retry action', async () => {
    api.getBotDefinitionPrompt
      .mockRejectedValueOnce(new Error('network unavailable'))
      .mockResolvedValueOnce(definition());
    await renderView();

    expect(container.textContent).toContain('无法读取 Agent 配置');
    expect(container.textContent).toContain('network unavailable');
    expect(container.querySelector('#cc-system-prompt-text')).toBeNull();

    await act(async () => {
      Simulate.click([...container.querySelectorAll('button')]
        .find((button) => button.textContent.includes('重试')));
      await settle();
    });

    expect(api.getBotDefinitionPrompt).toHaveBeenCalledTimes(2);
    expect(container.textContent).toContain('云端 revision3');
    expect(container.querySelector('#cc-system-prompt-text')).not.toBeNull();
    expect(container.textContent).not.toContain('network unavailable');
  });

  it('keeps the loaded editor visible when a manual refresh fails', async () => {
    api.getBotDefinitionPrompt
      .mockResolvedValueOnce(definition({
        selected: 'custom',
        customSystemPrompt: 'Keep this editor visible.',
      }))
      .mockRejectedValueOnce(new Error('refresh unavailable'));
    await renderView();

    await act(async () => {
      Simulate.click([...container.querySelectorAll('button')]
        .find((button) => button.textContent.includes('刷新')));
      await settle();
    });

    expect(container.textContent).toContain('refresh unavailable');
    expect(container.textContent).toContain('云端 revision3');
    expect(container.querySelector('#cc-system-prompt-text').value)
      .toBe('Keep this editor visible.');
  });

  it('saves custom content with the current BotDefinition revision', async () => {
    await renderView();

    await act(async () => {
      Simulate.click(modeButton('自定义提示词'));
      Simulate.change(container.querySelector('#cc-system-prompt-text'), {
        target: { value: 'Be precise.' },
      });
    });
    await act(async () => {
      Simulate.click([...container.querySelectorAll('button')]
        .find((button) => button.textContent.includes('保存修改')));
      await settle();
    });

    expect(api.updateBotDefinitionPrompt).toHaveBeenCalledWith('42', 3, {
      selected: 'custom',
      customSystemPrompt: 'Be precise.',
    });
    expect(container.textContent).toContain('待应用');
  });

  it('retains the custom text when saving the default mode', async () => {
    api.getBotDefinitionPrompt.mockResolvedValueOnce(definition({
      selected: 'custom',
      customSystemPrompt: 'Keep this for later.',
    }));
    api.updateBotDefinitionPrompt.mockResolvedValueOnce(definition({
      revision: 4,
      selected: 'default',
      customSystemPrompt: 'Keep this for later.',
      appliedRevision: 3,
      lastAttemptRevision: 3,
    }));
    await renderView();

    await act(async () => Simulate.click(modeButton('默认提示词')));
    await act(async () => {
      Simulate.click([...container.querySelectorAll('button')]
        .find((button) => button.textContent.includes('保存修改')));
      await settle();
    });

    expect(api.updateBotDefinitionPrompt).toHaveBeenCalledWith('42', 3, {
      selected: 'default',
      customSystemPrompt: 'Keep this for later.',
    });
  });

  it('omits a whitespace-only custom backup when saving the default mode', async () => {
    await renderView();

    await act(async () => {
      Simulate.click(modeButton('自定义提示词'));
      Simulate.change(container.querySelector('#cc-system-prompt-text'), {
        target: { value: '   \n' },
      });
      Simulate.click(modeButton('默认提示词'));
    });
    await act(async () => {
      Simulate.click([...container.querySelectorAll('button')]
        .find((button) => button.textContent.includes('保存修改')));
      await settle();
    });

    expect(api.updateBotDefinitionPrompt).toHaveBeenCalledWith('42', 3, {
      selected: 'default',
    });
  });

  it('tracks edits to the inactive custom backup before switching modes', async () => {
    api.getBotDefinitionPrompt.mockResolvedValueOnce(definition({
      selected: 'default',
      customSystemPrompt: 'Original backup',
    }));
    await renderView();

    await act(async () => Simulate.click(modeButton('自定义提示词')));
    await act(async () => {
      Simulate.change(container.querySelector('#cc-system-prompt-text'), {
        target: { value: 'Updated backup' },
      });
      Simulate.click(modeButton('默认提示词'));
    });

    const saveButton = [...container.querySelectorAll('button')]
      .find((button) => button.textContent.includes('保存修改'));
    expect(saveButton.disabled).toBe(false);
    await act(async () => {
      Simulate.click(saveButton);
      await settle();
    });
    expect(api.updateBotDefinitionPrompt).toHaveBeenCalledWith('42', 3, {
      selected: 'default',
      customSystemPrompt: 'Updated backup',
    });
  });

  it('keeps the local draft after a 409 and retries with the refreshed revision', async () => {
    const conflict = Object.assign(new Error('conflict'), { status: 409 });
    api.updateBotDefinitionPrompt
      .mockRejectedValueOnce(conflict)
      .mockResolvedValueOnce(definition({
        revision: 5,
        selected: 'custom',
        customSystemPrompt: 'My draft',
        appliedRevision: 4,
        lastAttemptRevision: 4,
      }));
    api.getBotDefinitionPrompt
      .mockResolvedValueOnce(definition())
      .mockResolvedValueOnce(definition({
        revision: 4,
        selected: 'custom',
        customSystemPrompt: 'Changed elsewhere',
      }));
    await renderView();

    await act(async () => {
      Simulate.click(modeButton('自定义提示词'));
      Simulate.change(container.querySelector('#cc-system-prompt-text'), {
        target: { value: 'My draft' },
      });
    });
    await act(async () => {
      Simulate.click([...container.querySelectorAll('button')]
        .find((button) => button.textContent.includes('保存修改')));
      await settle();
    });

    expect(container.querySelector('#cc-system-prompt-text').value).toBe('My draft');
    expect(container.textContent).toContain('检测到配置冲突');
    expect(container.textContent).toContain('云端 revision4');

    await act(async () => {
      Simulate.click([...container.querySelectorAll('button')]
        .find((button) => button.textContent.includes('保存修改')));
      await settle();
    });
    expect(api.updateBotDefinitionPrompt).toHaveBeenLastCalledWith('42', 4, {
      selected: 'custom',
      customSystemPrompt: 'My draft',
    });
  });

  it('blocks empty and oversized custom prompts before sending a request', async () => {
    await renderView();

    await act(async () => Simulate.click(modeButton('自定义提示词')));
    let saveButton = [...container.querySelectorAll('button')]
      .find((button) => button.textContent.includes('保存修改'));
    expect(saveButton.disabled).toBe(true);
    expect(container.textContent).toContain('自定义模式下提示词不能为空');

    await act(async () => {
      Simulate.change(container.querySelector('#cc-system-prompt-text'), {
        target: { value: 'a'.repeat(MAX_SYSTEM_PROMPT_BYTES + 1) },
      });
    });
    saveButton = [...container.querySelectorAll('button')]
      .find((button) => button.textContent.includes('保存修改'));
    expect(saveButton.disabled).toBe(true);
    expect(container.textContent).toContain('内容超过后端允许的 1 MiB 限制');
    expect(api.updateBotDefinitionPrompt).not.toHaveBeenCalled();
  });

  it('locks the Agent picker and all editing controls while a save is in flight', async () => {
    const pendingSave = deferred();
    const onSavingChange = vi.fn();
    api.updateBotDefinitionPrompt.mockReturnValueOnce(pendingSave.promise);
    await renderView({ onSavingChange });

    await act(async () => {
      Simulate.click(modeButton('自定义提示词'));
      Simulate.change(container.querySelector('#cc-system-prompt-text'), {
        target: { value: 'Wait for this save.' },
      });
    });
    await act(async () => {
      Simulate.click([...container.querySelectorAll('button')]
        .find((button) => button.textContent.includes('保存修改')));
      await settle();
    });

    expect(container.querySelector('.cc-system-prompt-agent-picker select').disabled).toBe(true);
    expect([...container.querySelectorAll('.cc-system-prompt-mode button')]
      .every((button) => button.disabled)).toBe(true);
    expect(container.querySelector('#cc-system-prompt-text').disabled).toBe(true);
    expect(onSavingChange).toHaveBeenLastCalledWith(true);

    await act(async () => {
      pendingSave.resolve(definition({
        revision: 4,
        selected: 'custom',
        customSystemPrompt: 'Wait for this save.',
        appliedRevision: 3,
        lastAttemptRevision: 3,
      }));
      await settle();
    });
    expect(container.querySelector('.cc-system-prompt-agent-picker select').disabled).toBe(false);
    expect([...container.querySelectorAll('.cc-system-prompt-mode button')]
      .every((button) => !button.disabled)).toBe(true);
    expect(container.querySelector('#cc-system-prompt-text').disabled).toBe(false);
    expect(onSavingChange).toHaveBeenLastCalledWith(false);
  });

  it('ignores a pending status response that started before a successful save', async () => {
    vi.useFakeTimers();
    const stalePoll = deferred();
    api.getBotDefinitionPrompt
      .mockResolvedValueOnce(definition({ appliedRevision: 2, lastAttemptRevision: 2 }))
      .mockReturnValueOnce(stalePoll.promise);
    await renderView();

    await act(async () => {
      vi.advanceTimersByTime(4000);
      await settle();
      Simulate.click(modeButton('自定义提示词'));
      Simulate.change(container.querySelector('#cc-system-prompt-text'), {
        target: { value: 'Newest prompt' },
      });
    });
    await act(async () => {
      Simulate.click([...container.querySelectorAll('button')]
        .find((button) => button.textContent.includes('保存修改')));
      await settle();
    });
    expect(container.textContent).toContain('云端 revision4');

    await act(async () => {
      stalePoll.resolve(definition({ appliedRevision: 3, lastAttemptRevision: 3 }));
      await settle();
    });

    expect(container.textContent).toContain('云端 revision4');
    expect(container.textContent).toContain('待应用');
    vi.useRealTimers();
  });

  it('preserves a retryable draft when the conflict refresh fails', async () => {
    const conflict = Object.assign(new Error('conflict'), { status: 409 });
    api.updateBotDefinitionPrompt.mockRejectedValueOnce(conflict);
    api.getBotDefinitionPrompt
      .mockResolvedValueOnce(definition())
      .mockRejectedValueOnce(new Error('refresh unavailable'));
    await renderView();

    await act(async () => {
      Simulate.click(modeButton('自定义提示词'));
      Simulate.change(container.querySelector('#cc-system-prompt-text'), {
        target: { value: 'Keep my retryable draft' },
      });
    });
    await act(async () => {
      Simulate.click([...container.querySelectorAll('button')]
        .find((button) => button.textContent.includes('保存修改')));
      await settle();
    });

    expect(container.querySelector('#cc-system-prompt-text').value).toBe('Keep my retryable draft');
    expect(container.textContent).toContain('云端 revision3');
    expect(container.textContent).toContain('检测到配置冲突');
    expect([...container.querySelectorAll('button')]
      .find((button) => button.textContent.includes('保存修改')).disabled).toBe(false);
  });

  it('reports dirty state and keeps an unconfigured Bot read-only', async () => {
    const onDirtyChange = vi.fn();
    api.getBotDefinitionPrompt.mockResolvedValueOnce({ configured: false, revision: 0 });
    await renderView({ onDirtyChange });

    expect(container.textContent).toContain('Agent 配置尚未初始化');
    expect(container.querySelector('#cc-system-prompt-text')).toBeNull();
    expect(api.updateBotDefinitionPrompt).not.toHaveBeenCalled();

    api.getBotDefinitionPrompt.mockResolvedValueOnce(definition());
    await act(async () => {
      Simulate.click([...container.querySelectorAll('button')]
        .find((button) => button.textContent.includes('刷新')));
      await settle();
    });
    await act(async () => {
      Simulate.click(modeButton('自定义提示词'));
      Simulate.change(container.querySelector('#cc-system-prompt-text'), {
        target: { value: 'Draft' },
      });
    });
    expect(onDirtyChange).toHaveBeenLastCalledWith(true);
  });
});
