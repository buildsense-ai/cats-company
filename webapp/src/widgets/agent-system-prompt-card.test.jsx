import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';

vi.mock('../api', () => ({
  api: {
    getBotDefinitionPrompt: vi.fn(),
    updateBotDefinitionPrompt: vi.fn(),
  },
}));

import { api } from '../api';
import { FeedbackProvider } from '../components/feedback-system';
import AgentSystemPromptCard from './agent-system-prompt-card';

function definition({
  revision = 3,
  selected = 'default',
  customSystemPrompt = '',
  appliedRevision = revision,
  lastAttemptRevision = revision,
} = {}) {
  return {
    configured: true,
    revision,
    definition: {
      prompt: {
        selected,
        ...(customSystemPrompt ? { customSystemPrompt } : {}),
      },
    },
    runtime: {
      appliedRevision,
      lastAttemptRevision,
      appliedAt: appliedRevision === revision ? '2026-08-13T08:00:00Z' : '',
    },
  };
}

async function settle() {
  await Promise.resolve();
  await Promise.resolve();
}

describe('AgentSystemPromptCard', () => {
  let container;
  let root;

  beforeEach(() => {
    global.IS_REACT_ACT_ENVIRONMENT = true;
    api.getBotDefinitionPrompt.mockReset().mockResolvedValue(definition());
    api.updateBotDefinitionPrompt.mockReset();
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(async () => {
    await act(async () => root.unmount());
    document.body.querySelectorAll('.cc-agent-prompt-editor-overlay').forEach((node) => node.remove());
    document.body.querySelectorAll('.cc-confirm-overlay').forEach((node) => node.remove());
    container.remove();
  });

  async function renderCard(agent = { uid: 42, display_name: 'Dev Agent' }) {
    await act(async () => {
      root.render(
        <FeedbackProvider>
          <AgentSystemPromptCard agent={agent} />
        </FeedbackProvider>,
      );
      await settle();
    });
  }

  test('loads the managed Agent prompt into one compact behavior card', async () => {
    await renderCard();

    expect(api.getBotDefinitionPrompt).toHaveBeenCalledWith('42');
    expect(container.querySelector('.cc-agent-behavior-card')?.textContent).toContain('行为设定');
    expect(container.textContent).toContain('使用 XiaoBa 默认提示词');
    expect(container.textContent).toContain('已生效');
    expect(container.querySelector('select')).toBeNull();
    expect(document.body.querySelector('.cc-agent-prompt-editor-overlay')).toBeNull();
  });

  test('finishes loading when React strict mode replays mount effects', async () => {
    await act(async () => {
      root.render(
        <React.StrictMode>
          <FeedbackProvider>
            <AgentSystemPromptCard agent={{ uid: 42, display_name: 'Dev Agent' }} />
          </FeedbackProvider>
        </React.StrictMode>,
      );
      await settle();
      await settle();
    });

    expect(container.textContent).not.toContain('正在读取行为设定');
    expect(container.textContent).toContain('使用 XiaoBa 默认提示词');
  });

  test('opens a portal editor and saves a custom prompt for the fixed Agent', async () => {
    api.updateBotDefinitionPrompt.mockResolvedValue(definition({
      revision: 4,
      selected: 'custom',
      customSystemPrompt: 'Review changes carefully.',
      appliedRevision: 3,
      lastAttemptRevision: 3,
    }));
    await renderCard();

    const customButton = Array.from(container.querySelectorAll('.cc-agent-behavior-mode button'))
      .find((button) => button.textContent.includes('自定义'));
    await act(async () => Simulate.click(customButton));

    const overlay = document.body.querySelector('.cc-agent-prompt-editor-overlay');
    const textarea = overlay.querySelector('textarea');
    expect(overlay).not.toBeNull();
    expect(container.querySelector('.cc-agent-prompt-editor-overlay')).toBeNull();

    await act(async () => {
      Simulate.change(textarea, { target: { value: 'Review changes carefully.' } });
    });
    const saveButton = Array.from(overlay.querySelectorAll('button'))
      .find((button) => button.textContent.includes('保存修改'));
    await act(async () => {
      Simulate.click(saveButton);
      await settle();
    });

    expect(api.updateBotDefinitionPrompt).toHaveBeenCalledWith('42', 3, {
      selected: 'custom',
      customSystemPrompt: 'Review changes carefully.',
    });
    expect(document.body.querySelector('.cc-agent-prompt-editor-overlay')).toBeNull();
    expect(container.textContent).toContain('已设置自定义提示词');
    expect(container.textContent).toContain('编辑内容');
  });

  test('closes an untouched editor without asking to discard changes', async () => {
    await renderCard();

    const customButton = Array.from(container.querySelectorAll('.cc-agent-behavior-mode button'))
      .find((button) => button.textContent.includes('自定义'));
    await act(async () => Simulate.click(customButton));
    const overlay = document.body.querySelector('.cc-agent-prompt-editor-overlay');
    const cancelButton = Array.from(overlay.querySelectorAll('button'))
      .find((button) => button.textContent.includes('取消'));

    await act(async () => {
      Simulate.click(cancelButton);
      await settle();
    });

    expect(document.body.querySelector('.cc-agent-prompt-editor-overlay')).toBeNull();
    expect(document.body.querySelector('[role="alertdialog"]')).toBeNull();
  });

  test('restores the default prompt directly from the compact card', async () => {
    api.getBotDefinitionPrompt.mockResolvedValue(definition({
      selected: 'custom',
      customSystemPrompt: 'Keep this for later.',
    }));
    api.updateBotDefinitionPrompt.mockResolvedValue(definition({
      revision: 4,
      selected: 'default',
      customSystemPrompt: 'Keep this for later.',
      appliedRevision: 3,
      lastAttemptRevision: 3,
    }));
    await renderCard();

    const defaultButton = Array.from(container.querySelectorAll('.cc-agent-behavior-mode button'))
      .find((button) => button.textContent.includes('使用默认'));
    await act(async () => {
      Simulate.click(defaultButton);
      await settle();
    });

    expect(api.updateBotDefinitionPrompt).toHaveBeenCalledWith('42', 3, {
      selected: 'default',
      customSystemPrompt: 'Keep this for later.',
    });
    expect(container.textContent).toContain('使用 XiaoBa 默认提示词');
  });
});
