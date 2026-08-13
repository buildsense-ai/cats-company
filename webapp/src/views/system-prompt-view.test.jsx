import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';
import SystemPromptView, {
  MAX_SYSTEM_PROMPT_BYTES,
  normalizeAgentPrompt,
  normalizePromptApplication,
  normalizePromptBots,
  promptByteLength,
  resolvePromptApplicationState,
} from './system-prompt-view';
import { api } from '../api';
import { FeedbackProvider } from '../components/feedback-system';

vi.mock('../api', () => ({
  api: {
    getAgents: vi.fn(),
    getAgentPrompt: vi.fn(),
    getBotDefinitionPrompt: vi.fn(),
    updateBotDefinitionPrompt: vi.fn(),
    updateBotPromptVisibility: vi.fn(),
  },
}));

function viewerPrompt({
  uid = 42,
  relation = 'owner',
  canEdit = relation === 'owner',
  selected = 'default',
  content = selected === 'custom' ? 'Active custom prompt' : 'Bundled default prompt',
  contentAvailable = true,
  defaultContent = 'Bundled default prompt',
  defaultAvailable = true,
  visibility = 'owner',
  revision = 3,
  application,
} = {}) {
  return {
    uid,
    botId: String(uid),
    relation,
    can_edit: canEdit,
    prompt_visibility: visibility,
    selected,
    content,
    content_available: contentAvailable,
    default_content: defaultAvailable ? defaultContent : '',
    default_content_available: defaultAvailable,
    revision,
    ...(application ? { application } : {}),
    ...(defaultAvailable ? {
      default_snapshot: {
        contentHash: 'abc123',
        xiaobaVersion: '1.2.3',
        runtimeVersion: 'node-24',
        reportedAt: '2026-08-13T03:00:00Z',
      },
    } : {}),
  };
}

function ownerDefinition({
  revision = 3,
  selected = 'default',
  customSystemPrompt = 'Saved custom backup',
} = {}) {
  return {
    configured: true,
    revision,
    definition: {
      schema: 'xiaoba.bot-definition.v1',
      botId: '42',
      prompt: {
        selected,
        ...(customSystemPrompt ? { customSystemPrompt } : {}),
      },
    },
  };
}

async function settle() {
  await Promise.resolve();
  await Promise.resolve();
}

describe('SystemPromptView helpers', () => {
  it('counts UTF-8 bytes and keeps every bot in the Agent roster', () => {
    expect(promptByteLength('CatsCo')).toBe(6);
    expect(promptByteLength('小八')).toBe(6);
    expect(promptByteLength('a'.repeat(MAX_SYSTEM_PROMPT_BYTES))).toBe(MAX_SYSTEM_PROMPT_BYTES);
    expect(normalizePromptBots({
      agents: [
        { uid: 42, relation: 'owner', is_bot: true },
        { uid: 43, relation: 'friend', is_bot: true },
        { uid: 44, relation: 'friend', is_bot: false },
      ],
    }).map((agent) => agent.uid)).toEqual([42, 43]);
  });

  it('normalizes the server snake_case viewer contract', () => {
    expect(normalizeAgentPrompt(viewerPrompt({ relation: 'friend', visibility: 'friends' })))
      .toMatchObject({
        canEdit: false,
        content: 'Bundled default prompt',
        defaultContent: 'Bundled default prompt',
        promptVisibility: 'friends',
        relation: 'friend',
        selected: 'default',
      });
  });

  it('normalizes application status fields and falls back to runtime acknowledgements', () => {
    expect(normalizePromptApplication({
      revision: 4,
      application: {
        status: 'applied',
        desired_revision: 4,
        applied_revision: 4,
        applied_at: '2026-08-13T03:00:00Z',
        is_online: true,
      },
    })).toMatchObject({
      status: 'applied',
      desiredRevision: 4,
      appliedRevision: 4,
      isOnline: true,
    });
    expect(normalizePromptApplication({
      configured: true,
      revision: 7,
      runtime: {
        desiredRevision: 7,
        appliedRevision: 6,
        lastAttemptRevision: 7,
        lastAttemptAt: '2026-08-13T03:00:00Z',
      },
    })).toMatchObject({
      status: 'pending',
      desiredRevision: 7,
      appliedRevision: 6,
      lastAttemptRevision: 7,
    });
    expect(normalizePromptApplication({
      revision: 0,
      runtime: { lastError: 'stale legacy error' },
      is_online: true,
    })).toMatchObject({ status: 'saved', desiredRevision: 0 });
  });

  it('maps application states to user-facing status labels', () => {
    expect(resolvePromptApplicationState({
      application: { status: 'saved', desired_revision: 3 },
    })).toMatchObject({ kind: 'saved', label: '已保存到云端' });
    expect(resolvePromptApplicationState({
      application: { status: 'pending', desired_revision: 3 },
    })).toMatchObject({ kind: 'pending', label: '等待 Agent 应用' });
    expect(resolvePromptApplicationState({
      application: { status: 'applied', applied_revision: 3 },
    })).toMatchObject({ kind: 'applied', label: 'Agent 已应用 revision 3' });
    expect(resolvePromptApplicationState({
      application: { status: 'failed', desired_revision: 3 },
    })).toMatchObject({ kind: 'failed', label: '应用失败，请重启或检查 Agent' });
  });
});

describe('SystemPromptView', () => {
  let container;
  let root;

  beforeEach(() => {
    Object.values(api).forEach((mock) => mock.mockReset());
    globalThis.localStorage?.clear();
    api.getAgents.mockResolvedValue({
      agents: [
        { uid: 42, display_name: 'Owner Bot', relation: 'owner', is_bot: true },
        { uid: 43, display_name: 'Friend Bot', relation: 'friend', is_bot: true },
      ],
    });
    api.getAgentPrompt.mockResolvedValue(viewerPrompt());
    api.getBotDefinitionPrompt.mockResolvedValue(ownerDefinition());
    api.updateBotDefinitionPrompt.mockResolvedValue({});
    api.updateBotPromptVisibility.mockResolvedValue({ prompt_visibility: 'friends' });
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(async () => {
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

  function findButton(label) {
    return [...container.querySelectorAll('button')]
      .find((button) => button.textContent.includes(label));
  }

  function modeButton(label) {
    return [...container.querySelectorAll('.cc-system-prompt-mode button')]
      .find((button) => button.textContent.includes(label));
  }

  it('lists owner and friend Agents and combines both owner prompt sources', async () => {
    await renderView();

    const options = [...container.querySelectorAll('.cc-system-prompt-agent-picker option')];
    expect(options.map((option) => option.textContent)).toEqual([
      'Owner Bot',
      'Friend Bot · 联系人',
    ]);
    expect(api.getAgentPrompt).toHaveBeenCalledWith('42');
    expect(api.getBotDefinitionPrompt).toHaveBeenCalledWith('42');
    expect(container.querySelector('#cc-system-prompt-text').value).toBe('Bundled default prompt');

    await act(async () => Simulate.click(modeButton('自定义提示词')));
    expect(container.querySelector('#cc-system-prompt-text').value).toBe('Saved custom backup');
  });

  it('lets an owner with active custom content preview the default snapshot', async () => {
    api.getAgentPrompt.mockResolvedValueOnce(viewerPrompt({ selected: 'custom' }));
    api.getBotDefinitionPrompt.mockResolvedValueOnce(ownerDefinition({
      selected: 'custom',
      customSystemPrompt: 'Active custom prompt',
    }));
    await renderView();

    expect(container.querySelector('#cc-system-prompt-text').value).toBe('Active custom prompt');
    await act(async () => Simulate.click(modeButton('默认提示词')));
    expect(container.querySelector('#cc-system-prompt-text').value).toBe('Bundled default prompt');
    expect(container.textContent).toContain('不包含日期、平台和设备等运行时上下文');
  });

  it('keeps a friend read-only and never requests or reveals the owner definition', async () => {
    api.getAgents.mockResolvedValueOnce({
      agents: [{ uid: 43, display_name: 'Friend Bot', relation: 'friend', is_bot: true }],
    });
    api.getAgentPrompt.mockResolvedValueOnce(viewerPrompt({
      uid: 43,
      relation: 'friend',
      selected: 'default',
      visibility: 'friends',
    }));
    api.getBotDefinitionPrompt.mockResolvedValueOnce(ownerDefinition({
      customSystemPrompt: 'INACTIVE OWNER BACKUP',
    }));
    await renderView();

    expect(api.getAgentPrompt).toHaveBeenCalledWith('43');
    expect(api.getBotDefinitionPrompt).not.toHaveBeenCalled();
    expect(container.querySelector('#cc-system-prompt-text').readOnly).toBe(true);
    expect([...container.querySelectorAll('.cc-system-prompt-mode button')]
      .every((button) => button.disabled)).toBe(true);
    expect(container.textContent).toContain('只有创建者可以修改');
    expect(container.textContent).not.toContain('INACTIVE OWNER BACKUP');
    expect(findButton('保存修改')).toBeUndefined();
    expect(container.querySelector('.cc-system-prompt-visibility')).toBeNull();
  });

  it('shows only the active custom prompt to a friend', async () => {
    api.getAgents.mockResolvedValueOnce({
      agents: [{ uid: 43, display_name: 'Friend Bot', relation: 'friend', is_bot: true }],
    });
    api.getAgentPrompt.mockResolvedValueOnce(viewerPrompt({
      uid: 43,
      relation: 'friend',
      selected: 'custom',
      content: 'Friend-visible active custom prompt',
      defaultAvailable: false,
      visibility: 'friends',
    }));
    await renderView();

    expect(container.querySelector('#cc-system-prompt-text').value)
      .toBe('Friend-visible active custom prompt');
    expect(modeButton('自定义提示词').getAttribute('aria-checked')).toBe('true');
    expect(api.getBotDefinitionPrompt).not.toHaveBeenCalled();
  });

  it('trusts server can_edit instead of the roster relation for editing access', async () => {
    api.getAgentPrompt.mockResolvedValueOnce(viewerPrompt({ canEdit: false }));
    await renderView();

    expect(api.getBotDefinitionPrompt).toHaveBeenCalledWith('42');
    expect([...container.querySelectorAll('.cc-system-prompt-mode button')]
      .every((button) => button.disabled)).toBe(true);
    expect(findButton('保存修改')).toBeUndefined();
  });

  it('shows an explicit state when the default snapshot has not synced', async () => {
    api.getAgentPrompt.mockResolvedValueOnce(viewerPrompt({
      content: '',
      contentAvailable: false,
      defaultAvailable: false,
    }));
    await renderView();

    expect(container.textContent).toContain('默认提示词尚未同步');
    expect(container.textContent).toContain('启动或升级该 Agent 的 XiaoBa');
    expect(container.querySelector('#cc-system-prompt-text')).toBeNull();
  });

  it.each([
    ['saved', '已保存到云端'],
    ['pending', '等待 Agent 应用'],
    ['applied', 'Agent 已应用 revision 3'],
    ['failed', '应用失败，请重启或检查 Agent'],
  ])('shows the %s Agent application state', async (status, label) => {
    api.getAgentPrompt.mockResolvedValueOnce(viewerPrompt({
      application: {
        status,
        desired_revision: 3,
        applied_revision: status === 'applied' ? 3 : 0,
      },
    }));
    await renderView();

    expect(container.querySelector('.cc-system-prompt-status')).not.toBeNull();
    expect(container.querySelector('.cc-system-prompt-status').textContent).toContain(label);
    if (status === 'failed') {
      expect(container.textContent).toContain('重启或检查该 Agent');
    }
  });

  it('prefers the viewer application projection over the owner runtime fallback', async () => {
    api.getAgentPrompt.mockResolvedValueOnce(viewerPrompt({
      revision: 4,
      application: {
        status: 'pending',
        desired_revision: 4,
        applied_revision: 3,
      },
    }));
    api.getBotDefinitionPrompt.mockResolvedValueOnce({
      ...ownerDefinition({ revision: 4 }),
      runtime: {
        desiredRevision: 4,
        appliedRevision: 4,
        appliedAt: '2026-08-13T03:00:00Z',
      },
    });
    await renderView();

    expect(container.querySelector('.cc-system-prompt-status').textContent)
      .toContain('等待 Agent 应用');
  });

  it('saves visibility independently from the prompt revision', async () => {
    await renderView();

    await act(async () => {
      Simulate.click(findButton('好友可查看'));
      await settle();
    });

    expect(api.updateBotPromptVisibility).toHaveBeenCalledWith('42', 'friends');
    expect(findButton('好友可查看').getAttribute('aria-pressed')).toBe('true');
    expect(api.updateBotDefinitionPrompt).not.toHaveBeenCalled();
  });

  it('retains the inactive custom backup when switching to default', async () => {
    api.getAgentPrompt
      .mockResolvedValueOnce(viewerPrompt({ selected: 'custom' }))
      .mockResolvedValueOnce(viewerPrompt({ selected: 'default', revision: 4 }));
    api.getBotDefinitionPrompt
      .mockResolvedValueOnce(ownerDefinition({
        selected: 'custom',
        customSystemPrompt: 'Keep this backup',
      }))
      .mockResolvedValueOnce(ownerDefinition({
        revision: 4,
        selected: 'default',
        customSystemPrompt: 'Keep this backup',
      }));
    await renderView();

    await act(async () => Simulate.click(modeButton('默认提示词')));
    await act(async () => {
      Simulate.click(findButton('保存修改'));
      await settle();
    });

    expect(api.updateBotDefinitionPrompt).toHaveBeenCalledWith('42', 3, {
      selected: 'default',
      customSystemPrompt: 'Keep this backup',
    });
    expect(container.querySelector('#cc-system-prompt-text').value).toBe('Bundled default prompt');
  });

  it('tracks edits to a custom backup after switching back to the active default', async () => {
    api.getAgentPrompt
      .mockResolvedValueOnce(viewerPrompt())
      .mockResolvedValueOnce(viewerPrompt({ revision: 4 }));
    api.getBotDefinitionPrompt
      .mockResolvedValueOnce(ownerDefinition({ customSystemPrompt: 'Original backup' }))
      .mockResolvedValueOnce(ownerDefinition({
        revision: 4,
        customSystemPrompt: 'Updated backup',
      }));
    await renderView();

    await act(async () => Simulate.click(modeButton('自定义提示词')));
    await act(async () => {
      Simulate.change(container.querySelector('#cc-system-prompt-text'), {
        target: { value: 'Updated backup' },
      });
      Simulate.click(modeButton('默认提示词'));
    });
    expect(findButton('保存修改').disabled).toBe(false);

    await act(async () => {
      Simulate.click(findButton('保存修改'));
      await settle();
    });
    expect(api.updateBotDefinitionPrompt).toHaveBeenCalledWith('42', 3, {
      selected: 'default',
      customSystemPrompt: 'Updated backup',
    });
  });

  it('validates and saves a custom prompt with the owner revision', async () => {
    api.getAgentPrompt
      .mockResolvedValueOnce(viewerPrompt())
      .mockResolvedValueOnce(viewerPrompt({
        selected: 'custom',
        content: 'Be precise.',
        revision: 4,
      }));
    api.getBotDefinitionPrompt
      .mockResolvedValueOnce(ownerDefinition({ customSystemPrompt: '' }))
      .mockResolvedValueOnce(ownerDefinition({
        revision: 4,
        selected: 'custom',
        customSystemPrompt: 'Be precise.',
      }));
    await renderView();

    await act(async () => {
      Simulate.click(modeButton('自定义提示词'));
      Simulate.change(container.querySelector('#cc-system-prompt-text'), {
        target: { value: 'Be precise.' },
      });
    });
    await act(async () => {
      Simulate.click(findButton('保存修改'));
      await settle();
    });

    expect(api.updateBotDefinitionPrompt).toHaveBeenCalledWith('42', 3, {
      selected: 'custom',
      customSystemPrompt: 'Be precise.',
    });
    expect(container.querySelector('#cc-system-prompt-text').value).toBe('Be precise.');
  });

  it('preserves a retryable local draft after a revision conflict', async () => {
    const conflict = Object.assign(new Error('conflict'), { status: 409 });
    api.updateBotDefinitionPrompt.mockRejectedValueOnce(conflict);
    api.getAgentPrompt
      .mockResolvedValueOnce(viewerPrompt())
      .mockResolvedValueOnce(viewerPrompt({ revision: 4 }));
    api.getBotDefinitionPrompt
      .mockResolvedValueOnce(ownerDefinition())
      .mockResolvedValueOnce(ownerDefinition({ revision: 4 }));
    await renderView();

    await act(async () => {
      Simulate.click(modeButton('自定义提示词'));
      Simulate.change(container.querySelector('#cc-system-prompt-text'), {
        target: { value: 'My local draft' },
      });
    });
    await act(async () => {
      Simulate.click(findButton('保存修改'));
      await settle();
    });

    expect(container.querySelector('#cc-system-prompt-text').value).toBe('My local draft');
    expect(container.textContent).toContain('检测到配置冲突');
    expect(container.textContent).toContain('云端 revision4');
    expect(findButton('保存修改').disabled).toBe(false);
  });

  it('explains when a friend-visible Agent has not shared its prompt', async () => {
    const forbidden = Object.assign(new Error('Agent owner has not shared this prompt'), {
      status: 403,
    });
    api.getAgents.mockResolvedValueOnce({
      agents: [{ uid: 43, display_name: 'Private Friend Bot', relation: 'friend', is_bot: true }],
    });
    api.getAgentPrompt.mockRejectedValueOnce(forbidden);
    await renderView();

    expect(container.textContent).toContain('系统提示词未向好友开放');
    expect(container.textContent).toContain('创建者尚未允许好友查看');
    expect(api.getBotDefinitionPrompt).not.toHaveBeenCalled();
  });
});
