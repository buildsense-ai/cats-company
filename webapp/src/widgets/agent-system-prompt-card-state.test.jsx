import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';
import { api } from '../api';
import { writePersona } from '../utils/agent-persona';
import AgentSystemPromptCard from './agent-system-prompt-card';

vi.mock('../api', () => ({
  api: {
    getAgentPrompt: vi.fn(),
    getBotDefinitionPrompt: vi.fn(),
    updateBotDefinitionPrompt: vi.fn(),
  },
}));

const definition = (revision, text) => ({
  configured: true,
  revision,
  definition: { prompt: { selected: 'custom', customSystemPrompt: text } },
});

describe('assistant settings revisions and validation', () => {
  let root;
  let container;

  beforeEach(() => {
    global.IS_REACT_ACT_ENVIRONMENT = true;
    vi.resetAllMocks();
    api.getAgentPrompt.mockResolvedValue({ application: { status: 'applied' } });
    container = document.createElement('div');
    document.body.append(container);
    root = createRoot(container);
  });

  afterEach(async () => {
    await act(async () => root.unmount());
    container.remove();
    vi.restoreAllMocks();
    vi.useRealTimers();
  });

  async function renderCard() {
    await act(async () => root.render(<AgentSystemPromptCard agent={{ uid: 42, display_name: '小派' }} />));
  }

  const click = element => act(async () => Simulate.click(element));
  const open = () => click(container.querySelector('.cc-agent-behavior-mode button:last-child'));
  const submit = () => click(document.querySelector('.cc-agent-prompt-editor-actions .oc-btn-primary'));
  const description = () => document.querySelector('#cc-persona-description');

  async function createConflict() {
    api.getBotDefinitionPrompt.mockResolvedValueOnce(definition(3, '最初的文字'))
      .mockResolvedValueOnce(definition(4, '另一处的修改'));
    api.updateBotDefinitionPrompt.mockRejectedValueOnce({ status: 409 });
    await renderCard();
    await open();
    await act(async () => Simulate.change(description(), { target: { value: '我的草稿' } }));
    await submit();
    expect(description().value).toBe('我的草稿');
    expect(document.querySelector('.cc-inline-feedback-warning').textContent).toContain('草稿已保留');
  }

  it('keeps polling across an unchanged and a failed read, then stops when the agent applies the settings', async () => {
    vi.useFakeTimers();
    const pending = { application: { status: 'pending' } };
    api.getAgentPrompt.mockResolvedValueOnce(pending).mockResolvedValueOnce(pending)
      .mockRejectedValueOnce(new Error('暂时离线'));
    api.getBotDefinitionPrompt.mockResolvedValue(definition(4, '回复优先项：尽量简短'));
    await renderCard();
    expect(container.querySelector('.cc-agent-behavior-current').textContent).toContain('只讲重点');
    const tick = () => act(async () => vi.advanceTimersByTimeAsync(4000));
    await tick();
    await tick();
    expect(container.textContent).toContain('等待 Agent 应用');
    await tick();
    expect(container.textContent).toContain('已生效');
    await tick();
    expect(api.getAgentPrompt).toHaveBeenCalledTimes(4);
  });

  it('leaves the owner in control of which revision is overwritten after a conflict', async () => {
    await createConflict();
    const confirm = vi.spyOn(window, 'confirm').mockReturnValueOnce(false).mockReturnValueOnce(true);
    await submit();
    expect(api.updateBotDefinitionPrompt).toHaveBeenCalledTimes(1);
    api.updateBotDefinitionPrompt.mockResolvedValue(definition(5, '我的草稿'));
    await submit();
    expect(confirm).toHaveBeenCalledTimes(2);
    expect(api.updateBotDefinitionPrompt).toHaveBeenLastCalledWith('42', 4, {
      selected: 'custom', customSystemPrompt: '我的草稿',
    });
    expect(document.querySelector('.cc-agent-prompt-editor-dialog')).toBeNull();
  });

  it('opens the saved definition when the separate application status is offline', async () => {
    api.getAgentPrompt.mockRejectedValueOnce(new Error('暂时离线'));
    api.getBotDefinitionPrompt.mockResolvedValueOnce(definition(4, '先讲重点'));
    await renderCard();
    await open();
    expect(description().value).toBe('先讲重点');
  });

  it('remounts the editor from the newer settings only after the owner accepts replacing their draft', async () => {
    await createConflict();
    vi.spyOn(window, 'confirm').mockReturnValueOnce(false).mockReturnValueOnce(true);
    const reload = () => click(document.querySelector('.cc-persona-reload'));
    await reload();
    expect(description().value).toBe('我的草稿');
    await reload();
    expect(description().value).toBe('另一处的修改');
    expect(document.querySelector('.cc-persona-reload')).toBeNull();
    expect(document.querySelector('.cc-agent-prompt-editor-actions .oc-btn-primary').disabled).toBe(true);
    expect(api.updateBotDefinitionPrompt).toHaveBeenCalledTimes(1);
  });

  it('opens on replies, flags a saved incomplete rule and takes the owner to its instruction', async () => {
    vi.useFakeTimers();
    const text = writePersona({ description: '按步回答', boundaries: '', rules: [{ occasion: '开会', instruction: '' }] });
    api.getBotDefinitionPrompt.mockResolvedValue(definition(3, text));
    await renderCard();
    await open();
    expect(document.querySelector('#cc-persona-tab-reply').getAttribute('aria-selected')).toBe('true');
    expect(document.querySelector('.cc-agent-prompt-editor-actions .oc-btn-primary').disabled).toBe(true);
    await click(document.querySelector('.cc-persona-fix-rule'));
    await act(async () => vi.advanceTimersByTimeAsync(32));
    const instruction = document.querySelector('textarea[data-incomplete="true"]');
    expect(document.activeElement).toBe(instruction);
    await act(async () => Simulate.change(instruction, { target: { value: '提醒我记下行动项' } }));
    expect(document.querySelector('.cc-persona-fix-rule')).toBeNull();
    expect(document.querySelector('.cc-agent-prompt-editor-actions .oc-btn-primary').disabled).toBe(false);
  });
});
