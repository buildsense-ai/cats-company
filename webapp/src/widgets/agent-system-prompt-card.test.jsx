import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';

vi.mock('../api', () => ({
  api: {
    getAgentPrompt: vi.fn(),
    getBotDefinitionPrompt: vi.fn(),
    updateBotDefinitionPrompt: vi.fn(),
    previewBotPrompt: vi.fn(),
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

async function revealField(field) {
  const disclosure = field.closest('details');
  if (disclosure && !disclosure.open) {
    await act(async () => disclosure.querySelector('summary').click());
    expect(disclosure.open).toBe(true);
  }
}

describe('AgentSystemPromptCard', () => {
  let container;
  let root;

  beforeEach(() => {
    global.IS_REACT_ACT_ENVIRONMENT = true;
    api.getAgentPrompt.mockReset().mockResolvedValue({
      application: {
        status: 'applied',
        desired_revision: 3,
        applied_revision: 3,
        applied_at: '2026-08-13T08:00:00Z',
      },
    });
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
    expect(container.querySelector('.cc-agent-behavior-card')?.textContent).toContain('助手设定');
    expect(container.textContent).toContain('默认设定');
    expect(container.querySelector('.cc-agent-prompt-status').textContent).toContain('已生效');
    expect(container.querySelector('.cc-agent-prompt-status').title).toContain('Agent 已应用 revision 3');
    expect(container.querySelector('select')).toBeNull();
    expect(document.body.querySelector('.cc-agent-prompt-editor-overlay')).toBeNull();
  });

  test('prefers the application projection over an applied runtime fallback', async () => {
    api.getAgentPrompt.mockResolvedValue({
      application: {
        status: 'pending',
        desired_revision: 4,
        applied_revision: 3,
      },
    });
    api.getBotDefinitionPrompt.mockResolvedValue(definition({
      revision: 4,
      appliedRevision: 4,
      lastAttemptRevision: 4,
    }));

    await renderCard();

    expect(container.textContent).toContain('等待 Agent 应用');
    expect(container.textContent).not.toContain('Agent 已应用 revision 4');
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
    expect(container.textContent).toContain('默认设定');
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
    const textarea = overlay.querySelector('#cc-persona-description');
    expect(overlay).not.toBeNull();
    expect(container.querySelector('.cc-agent-prompt-editor-overlay')).toBeNull();
    expect(overlay.textContent).not.toContain('自定义系统提示词不能为空');
    expect(overlay.textContent).not.toContain('有未保存的修改');
    expect(overlay.textContent).toContain('选一项或写一句即可保存');

    await revealField(textarea);
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
    expect(container.textContent).toContain('自定义设定');
    expect(container.textContent).toContain('编辑设定');
  });

  test('offers inspiration separately when the owner has already written a description', async () => {
    const original = '跟我讨论画稿时，先说光影。\n其他事情尽量简短。';
    api.getBotDefinitionPrompt.mockResolvedValue(definition({ selected: 'custom', customSystemPrompt: original }));
    await renderCard();
    await act(async () => Simulate.click(container.querySelector('.cc-agent-behavior-mode button:last-child')));
    const dialog = document.querySelector('.cc-agent-prompt-editor-dialog');
    const click = async selector => act(async () => Simulate.click(dialog.querySelector(selector)));
    const description = dialog.querySelector('#cc-persona-description');
    expect(dialog.querySelector('.cc-persona-roll').textContent).toContain('补全设定');
    await click('.cc-persona-roll');
    const firstIdea = dialog.querySelector('.cc-persona-idea p').textContent;
    await click('.cc-persona-roll');
    const secondIdea = dialog.querySelector('.cc-persona-idea p').textContent;
    expect(firstIdea).not.toBe(secondIdea);
    expect(description.value).toBe(original);
    await click('.cc-persona-idea button:last-child');
    expect(description.value.startsWith(original + '\n')).toBe(true);
    expect(description.value.trim().length).toBeGreaterThan(original.length);
    await click('#cc-persona-tab-rules');
    const bounds = dialog.querySelector('#cc-persona-boundaries');
    await act(async () => Simulate.change(bounds, { target: { value: '报价之前找我确认' } }));
    await click('#cc-persona-tab-person');
    await click('.cc-persona-undo');
    expect(dialog.querySelector('#cc-persona-description').value).toBe(original);
    await click('#cc-persona-tab-rules');
    expect(dialog.querySelector('#cc-persona-boundaries').value).toBe('报价之前找我确认');
    expect(api.updateBotDefinitionPrompt).not.toHaveBeenCalled();
  });

  test('compares the same question without saving and retries only the missing answer', async () => {
    api.previewBotPrompt.mockReset().mockResolvedValueOnce({ answer: '原来的答复' }).mockRejectedValueOnce(new Error('模型繁忙'))
      .mockResolvedValueOnce({ answer: '草稿的答复' });
    await renderCard();
    await act(async () => container.querySelector('.cc-agent-behavior-mode button:last-child').click());
    const trial = document.querySelector('.cc-persona-trial');
    await act(async () => trial.querySelector('summary').click());
    await act(async () => document.querySelector('.cc-persona-missing input').click());
    await act(async () => Simulate.change(trial.querySelector('textarea'), { target: { value: '约朋友怎么说？' } }));
    expect(api.previewBotPrompt).not.toHaveBeenCalled();
    await act(async () => trial.querySelector('button').click());
    expect(trial.textContent).toContain('原来的答复');
    expect(trial.querySelector('[role=alert]').textContent).toContain('模型繁忙');
    await act(async () => trial.querySelector('button').click());
    const calls = api.previewBotPrompt.mock.calls;
    expect(calls.map(call => call[2])).toEqual(Array(3).fill('约朋友怎么说？'));
    expect(calls[0][1]).toBe('');
    expect(calls[1][1]).toContain('信息不足时：先问我');
    expect(calls[2][1]).toBe(calls[1][1]);
    expect(trial.textContent).toContain('草稿的答复');
    await act(async () => document.querySelector('#cc-persona-tab-person').click());
    await act(async () => document.querySelector('#cc-persona-tab-reply').click());
    const reopened = document.querySelector('.cc-persona-trial');
    expect(reopened.querySelector('textarea').value).toBe('约朋友怎么说？');
    expect(reopened.textContent).toContain('草稿的答复');
    expect(reopened.textContent).toContain('原来的答复');
    expect(api.previewBotPrompt).toHaveBeenCalledTimes(3);
    expect(api.updateBotDefinitionPrompt).not.toHaveBeenCalled();
  });

  test('drafts into an empty description only on request and lets the owner reroll or undo', async () => {
    await renderCard();
    await act(async () => Simulate.click(container.querySelector('.cc-agent-behavior-mode button:last-child')));
    const dialog = document.querySelector('.cc-agent-prompt-editor-dialog');
    const click = async selector => act(async () => Simulate.click(dialog.querySelector(selector)));
    const description = () => dialog.querySelector('#cc-persona-description').value;
    expect(description()).toBe('');
    expect(dialog.querySelector('#cc-persona-tab-reply').getAttribute('aria-selected')).toBe('true');
    await click('#cc-persona-tab-person');
    await click('.cc-persona-roll');
    const first = description();
    expect(first).toBeTruthy();
    expect(dialog.querySelector('.cc-persona-choices button[aria-pressed="true"]')).not.toBeNull();
    await click('#cc-persona-tab-reply');
    expect(dialog.querySelectorAll('.cc-reply-preferences:not(.cc-persona-missing) input:checked')).toHaveLength(1);
    expect(dialog.querySelectorAll('.cc-persona-missing input:checked')).toHaveLength(1);
    await click('.cc-persona-roll');
    expect(description()).not.toBe(first);
    expect(dialog.querySelectorAll('.cc-reply-preferences:not(.cc-persona-missing) input:checked')).toHaveLength(1);
    await click('.cc-persona-undo');
    expect(description()).toBe('');
    expect(dialog.querySelector('.cc-persona-choices button[aria-pressed="true"]')).toBeNull();
    expect(dialog.querySelector('footer .oc-btn-primary').disabled).toBe(true);
  });

  test('saves any combination of traits and habits without typing and restores their selected states', async () => {
    api.updateBotDefinitionPrompt.mockImplementation(async (_, revision, prompt) =>
      definition({ revision: revision + 1, ...prompt }));
    await renderCard();
    const click = async button => act(async () => Simulate.click(button));
    const open = () => click(container.querySelector('.cc-agent-behavior-mode button:last-child'));
    const choice = label => [...document.querySelectorAll('.cc-persona-choices button')]
      .find(button => button.textContent === label);
    await open();
    await click(document.querySelector('#cc-persona-tab-person'));
    expect(choice('坦率').getAttribute('aria-pressed')).toBe('false');
    await click(choice('坦率'));
    await click(choice('幽默'));
    await click(choice('幽默'));
    await click(choice('音乐'));
    await revealField(choice('165 cm'));
    await click(choice('165 cm'));
    await click(choice('175 cm'));
    expect(choice('165 cm').getAttribute('aria-pressed')).toBe('false');
    await click(document.querySelector('#cc-persona-tab-reply'));
    await revealField(choice('分步说明'));
    await click(choice('分步说明'));
    const save = document.querySelector('.cc-agent-prompt-editor-dialog footer .oc-btn-primary');
    expect(save.disabled).toBe(false);
    const expected = ['性格：坦率', '兴趣爱好：音乐', '虚拟身高：175 cm', '回答结构：分步说明'].join('\n');
    expect(document.querySelector('#cc-persona-description').value).toBe(expected);
    await click(save);
    expect(api.updateBotDefinitionPrompt).toHaveBeenCalledWith('42', 3, {
      selected: 'custom', customSystemPrompt: expected,
    });
    await open();
    await click(document.querySelector('#cc-persona-tab-person'));
    expect(choice('坦率').getAttribute('aria-pressed')).toBe('true');
    expect(choice('175 cm').getAttribute('aria-pressed')).toBe('true');
    expect(choice('幽默').getAttribute('aria-pressed')).toBe('false');
    await click(document.querySelector('#cc-persona-tab-reply'));
    expect(choice('分步说明').getAttribute('aria-pressed')).toBe('true');
    const input = document.querySelector('#cc-persona-description');
    await revealField(input);
    await revealField(choice('分步说明'));
    const rewritten = '讨论剧本时可以坦率，写给客户时更委婉。';
    await act(async () => Simulate.change(input, { target: { value: rewritten + '\r\n回答结构：分步说明' } }));
    await click(choice('分步说明'));
    expect(input.value).toBe(rewritten);
  });

  test('selects one reply priority and replaces a legacy combination only after a deliberate choice', async () => {
    api.updateBotDefinitionPrompt.mockImplementation(async (_, revision, prompt) => definition({ revision: revision + 1, ...prompt }));
    const original = '性格：耐心\n回答结构：先说结论\n回复篇幅：详细展开\n互动习惯：有疑问先问我\n回答结构：分步说明';
    api.getBotDefinitionPrompt.mockResolvedValue(definition({ selected: 'custom', customSystemPrompt: original }));
    await renderCard();
    const click = async button => act(async () => Simulate.click(button));
    const open = () => click(container.querySelector('.cc-agent-behavior-mode button:last-child'));
    await open();
    const radio = value => document.querySelector(`.cc-reply-preferences input[value="${value}"]`);
    const choose = async value => act(async () => radio(value).click());
    const checked = () => [...document.querySelectorAll('.cc-reply-preferences:not(.cc-persona-missing) input:checked')].map(el => el.value);
    const example = () => document.querySelector('.cc-persona-example [aria-live]').textContent;
    expect(checked()).toEqual(['先说结论']);
    expect(radio('先问我').checked).toBe(true);
    expect(document.querySelector('#cc-persona-description').value).toBe(original);
    expect(document.querySelector('footer .oc-btn-primary:enabled')).toBeNull();
    const examples = new Set();
    await choose('先给一版，标出待定内容');
    for (const value of ['先说结论', '给出判断依据', '给出下一步', '尽量简短']) {
      await choose(value);
      expect(checked()).toEqual([value]);
      examples.add(example());
    }
    expect(examples.size).toBe(4);
    const detail = label => [...document.querySelectorAll('.cc-persona-choices button')].find(el => el.textContent === label);
    expect(detail('给出判断依据')).toBeUndefined();
    expect(detail('尽量简短')).toBeUndefined();
    await choose('给出下一步');
    await revealField(detail('适中'));
    await click(detail('适中'));
    expect(checked()).toEqual(['给出下一步']);
    await click(document.querySelector('.cc-agent-prompt-editor-dialog footer .oc-btn-primary'));
    const prompt = api.updateBotDefinitionPrompt.mock.calls[0][2].customSystemPrompt;
    expect(prompt).toContain('性格：耐心');
    expect(prompt).toContain('回答结构：分步说明');
    expect(prompt).toContain('回复篇幅：适中');
    expect(prompt).not.toContain('详细展开');
    await open();
    expect(checked()).toEqual(['给出下一步']);
    expect(radio('先给一版，标出待定内容').checked).toBe(true);
  });

  test('switching example questions is a preview action; tone and priority can be combined', async () => {
    await renderCard();
    const click = async button => act(async () => button.click());
    await click(container.querySelector('.cc-agent-behavior-mode button:last-child'));
    const dialog = document.querySelector('.cc-agent-prompt-editor-dialog');
    const find = label => [...dialog.querySelectorAll('button')].find(button => button.textContent.trim() === label);
    const sample = () => dialog.querySelector('.cc-persona-example [aria-live]').textContent;
    const first = sample();
    await click(find('换个问题'));
    expect(sample()).not.toBe(first);
    expect(dialog.querySelector('footer .oc-btn-primary').disabled).toBe(true);
    const radio = dialog.querySelector('.cc-reply-preferences input[value="先说结论"]');
    await click(radio);
    const direct = sample();
    await revealField(find('轻松俏皮'));
    await click(find('轻松俏皮'));
    expect(sample()).not.toBe(direct);
    expect(radio.checked).toBe(true);
    expect(api.updateBotDefinitionPrompt).not.toHaveBeenCalled();
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

  test('saves user-defined situations and boundaries and reopens them as editable fields', async () => {
    api.updateBotDefinitionPrompt.mockImplementation(async (_, revision, prompt) =>
      definition({ revision: revision + 1, ...prompt }));
    await renderCard();
    const click = async button => act(async () => Simulate.click(button));
    const change = async (field, value) => act(async () => Simulate.change(field, { target: { value } }));
    const open = () => click(container.querySelector('.cc-agent-behavior-mode button:last-child'));
    await open();
    const dialog = document.querySelector('.cc-agent-prompt-editor-dialog');
    await revealField(dialog.querySelector('#cc-persona-description'));
    await change(dialog.querySelector('#cc-persona-description'), '一起做手帐，我喜欢冷笑话。');
    await click(dialog.querySelector('#cc-persona-tab-rules'));
    await click(dialog.querySelector('.cc-persona-add'));
    await change(dialog.querySelector('.cc-persona-rule input'), '写完周记');
    const save = dialog.querySelector('footer .oc-btn-primary');
    expect(save.disabled).toBe(true);
    await click(dialog.querySelector('#cc-persona-tab-person'));
    const completeRule = [...dialog.querySelectorAll('footer button')].find(button => button.textContent.includes('补全场景约定'));
    await click(completeRule);
    await act(async () => new Promise(resolve => window.requestAnimationFrame(resolve)));
    expect(dialog.querySelector('#cc-persona-tab-rules').getAttribute('aria-selected')).toBe('true');
    expect(document.activeElement).toBe(dialog.querySelector('.cc-persona-rule textarea'));
    await change(dialog.querySelector('.cc-persona-rule textarea'), '最后问我「最想留下哪一天？」');
    await change(dialog.querySelector('#cc-persona-boundaries'), '不要分享手帐。');
    await click(dialog.querySelector('.cc-persona-add'));
    const rows = dialog.querySelectorAll('.cc-persona-rule');
    await change(rows[1].querySelector('textarea'), '总是保留原文。');
    await click(save);
    const saved = JSON.parse(api.updateBotDefinitionPrompt.mock.calls[0][2].customSystemPrompt);
    expect(saved.场景约定).toEqual([{ 场景: '写完周记', 要求: '最后问我「最想留下哪一天？」' }, { 场景: '', 要求: '总是保留原文。' }]);
    await open();
    const reopened = document.querySelector('.cc-agent-prompt-editor-dialog');
    expect(reopened.querySelector('#cc-persona-description').value).toBe('一起做手帐，我喜欢冷笑话。');
    await click(reopened.querySelector('#cc-persona-tab-rules'));
    expect(reopened.querySelector('#cc-persona-boundaries').value).toBe('不要分享手帐。');
    await click(reopened.querySelector('.cc-persona-remove'));
    expect(reopened.querySelectorAll('.cc-persona-rule')).toHaveLength(1);
    expect(reopened.querySelector('.cc-persona-rule textarea').value).toBe('总是保留原文。');
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
    expect(container.textContent).toContain('默认设定');
  });
});
