import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';

vi.mock('../api', () => ({
  api: {
    getAgents: vi.fn(),
    sendMessage: vi.fn(),
    disbandGroup: vi.fn(),
    uploadFile: vi.fn(),
    createMobileUploadSession: vi.fn(),
    getMobileUploadSession: vi.fn(),
  },
}));

vi.mock('./qr-code', () => ({
  default: function MockQRCode({ value }) {
    return <div data-testid="qr-code">{value}</div>;
  },
}));

import { api } from '../api';
import EmptyTaskComposer from './empty-task-composer';

const agents = [
  {
    uid: 21,
    username: 'code-agent',
    display_name: '代码审查助手',
    topic_id: 'p2p_1_21',
    is_bot: true,
  },
  {
    uid: 22,
    username: 'ops-agent',
    display_name: '运营数据助手',
    topic_id: 'p2p_1_22',
    is_bot: true,
  },
];

describe('EmptyTaskComposer', () => {
  let container;
  let root;

  beforeEach(() => {
    global.IS_REACT_ACT_ENVIRONMENT = true;
    vi.useFakeTimers();
    api.getAgents.mockReset().mockResolvedValue({ agents });
    api.sendMessage.mockReset().mockResolvedValue({ seq_id: 101 });
    api.disbandGroup.mockReset().mockResolvedValue({});
    api.uploadFile.mockReset();
    api.createMobileUploadSession.mockReset();
    api.getMobileUploadSession.mockReset();

    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(async () => {
    await act(async () => {
      root.unmount();
    });
    vi.clearAllTimers();
    vi.useRealTimers();
    container.remove();
    vi.clearAllMocks();
  });

  async function mountComposer(extraProps = {}) {
    const onResolveAgentTopic = extraProps.onResolveAgentTopic || vi.fn().mockResolvedValue({ topicId: 'p2p_1_21' });
    const onActivateTopic = extraProps.onActivateTopic || vi.fn().mockResolvedValue(undefined);

    await act(async () => {
      root.render(
        <EmptyTaskComposer
          onResolveAgentTopic={onResolveAgentTopic}
          onActivateTopic={onActivateTopic}
          {...extraProps}
        />,
      );
      await flushPromises();
    });

    return { onResolveAgentTopic, onActivateTopic };
  }

  it('renders a real textarea in the shared composer and keeps all upload actions under plus', async () => {
    await mountComposer();

    const composer = container.querySelector('.v3-composer[aria-label="新任务输入栏"]');
    const box = composer.querySelector('.v3-composer-box');
    const row = box.querySelector('.v3-composer-row');
    const textarea = row.querySelector('textarea.v3-composer-input');

    expect(composer.classList.contains('cc-empty-composer-wrap')).toBe(true);
    expect(textarea).not.toBeNull();
    expect(textarea.placeholder).toBe('输入指令，我帮您完成');

    await act(async () => {
      Simulate.click(row.querySelector('button.v3-composer-plus'));
    });

    const menu = row.querySelector('.v3-attachment-menu.is-open');
    expect(menu).not.toBeNull();
    expect(menu.textContent).toContain('上传图片');
    expect(menu.textContent).toContain('上传文件');
    expect(menu.textContent).toContain('手机扫码上传');
  });

  it('restores an unsent new-task draft from the shared store after remounting', async () => {
    const composerDraftStore = {
      inputDrafts: new Map(),
      structuredMentionDrafts: new Map(),
      attachmentDrafts: new Map(),
      persist: vi.fn(),
    };

    await mountComposer({ composerDraftStore, draftKey: 'new-task' });
    const textarea = container.querySelector('textarea.v3-composer-input');
    await typeInto(textarea, '保留这条尚未建立会话的草稿');

    expect(composerDraftStore.inputDrafts.get('new-task'))
      .toBe('保留这条尚未建立会话的草稿');
    expect(composerDraftStore.persist).toHaveBeenCalled();

    await act(async () => {
      root.unmount();
    });
    root = createRoot(container);
    await mountComposer({ composerDraftStore, draftKey: 'new-task' });

    expect(container.querySelector('textarea.v3-composer-input').value)
      .toBe('保留这条尚未建立会话的草稿');
  });

  it('shows voice input on the new task composer and inserts the final transcript', async () => {
    let callbacks;
    const session = {
      start: vi.fn().mockResolvedValue(undefined),
      stop: vi.fn().mockResolvedValue(undefined),
      cancel: vi.fn(),
    };
    const createVoiceSession = vi.fn((options) => {
      callbacks = options;
      return session;
    });
    await mountComposer({ voiceInputAvailable: true, createVoiceSession });

    const textarea = container.querySelector('textarea.v3-composer-input');
    await typeInto(textarea, '整理：');
    textarea.setSelectionRange(textarea.value.length, textarea.value.length);
    await act(async () => {
      Simulate.click(container.querySelector('button[aria-label="开始语音输入"]'));
      await flushPromises();
    });
    await act(async () => {
      callbacks.onFinal('今天的会议记录');
      vi.runOnlyPendingTimers();
    });

    expect(createVoiceSession).toHaveBeenCalledTimes(1);
    expect(session.start).toHaveBeenCalledTimes(1);
    expect(textarea.value).toBe('整理：今天的会议记录');
  });

  it('supports the same touch-hold voice overlay on the new task composer', async () => {
    const session = {
      start: vi.fn().mockResolvedValue(undefined),
      stop: vi.fn().mockResolvedValue(undefined),
      cancel: vi.fn(),
    };
    await mountComposer({
      voiceInputAvailable: true,
      createVoiceSession: () => session,
    });

    const voiceButton = container.querySelector('button[aria-label="开始语音输入"]');
    await act(async () => {
      voiceButton.dispatchEvent(new PointerEvent('pointerdown', {
        bubbles: true,
        pointerId: 17,
        pointerType: 'touch',
        clientY: 720,
      }));
      vi.advanceTimersByTime(300);
      await flushPromises();
    });

    expect(container.querySelector('.v3-voice-hold-overlay')).not.toBeNull();
    expect(container.querySelector('.v3-voice-hold-wave svg')).not.toBeNull();
    expect(session.start).toHaveBeenCalledTimes(1);

    await act(async () => {
      voiceButton.dispatchEvent(new PointerEvent('pointerup', {
        bubbles: true,
        pointerId: 17,
        pointerType: 'touch',
        clientY: 720,
      }));
      await flushPromises();
    });

    expect(session.stop).toHaveBeenCalledTimes(1);
  });

  it('keeps the Agent picker hidden while preserving automatic Agent selection', async () => {
    const { onResolveAgentTopic } = await mountComposer({ initialAgent: agents[1] });

    expect(container.querySelector('.v3-agent-picker-button')).toBeNull();
    expect(container.querySelector('.v3-agent-picker-menu')).toBeNull();
    expect(onResolveAgentTopic).not.toHaveBeenCalled();
    expect(api.sendMessage).not.toHaveBeenCalled();
  });

  it('reports the selected Agent before a new task is sent', async () => {
    const onSelectedAgentChange = vi.fn();
    await mountComposer({ onSelectedAgentChange });

    expect(onSelectedAgentChange).toHaveBeenLastCalledWith(
      expect.objectContaining({ uid: 21, display_name: '代码审查助手' }),
    );
  });

  it('creates the selected Agent task from the first instruction, sends, then activates it on Enter', async () => {
    const order = [];
    const resolvedTopic = {
      topicId: 'grp_401',
      groupId: 401,
      isGroup: true,
      name: '检查这段代码',
    };
    const onResolveAgentTopic = vi.fn().mockImplementation(async (agent, draft) => {
      order.push('resolve');
      expect(agent.uid).toBe(21);
      expect(draft).toEqual({ text: '检查这段代码', attachments: [] });
      return resolvedTopic;
    });
    api.sendMessage.mockImplementationOnce(async (topicId, payload) => {
      order.push('send');
      expect(topicId).toBe('grp_401');
      expect(payload).toBe('检查这段代码');
      return { seq_id: 102 };
    });
    const onActivateTopic = vi.fn().mockImplementation(async (topic) => {
      order.push('activate');
      expect(topic).toBe(resolvedTopic);
    });
    await mountComposer({ onResolveAgentTopic, onActivateTopic });

    const textarea = container.querySelector('textarea.v3-composer-input');
    await typeInto(textarea, '检查这段代码');
    await pressEnter(textarea);

    expect(order).toEqual(['resolve', 'send', 'activate']);
    expect(onResolveAgentTopic).toHaveBeenCalledTimes(1);
    expect(api.sendMessage).toHaveBeenCalledWith('grp_401', '检查这段代码');
    expect(onActivateTopic).toHaveBeenCalledWith(resolvedTopic);
  });

  it('does not submit Enter while a Chinese IME composition is active', async () => {
    const { onResolveAgentTopic, onActivateTopic } = await mountComposer();
    const textarea = container.querySelector('textarea.v3-composer-input');
    await typeInto(textarea, '正在输入中文');

    const composingEnter = new KeyboardEvent('keydown', {
      key: 'Enter',
      bubbles: true,
      cancelable: true,
    });
    Object.defineProperty(composingEnter, 'isComposing', { value: true });
    await act(async () => {
      textarea.dispatchEvent(composingEnter);
      await flushPromises();
    });

    expect(onResolveAgentTopic).not.toHaveBeenCalled();
    expect(api.sendMessage).not.toHaveBeenCalled();
    expect(onActivateTopic).not.toHaveBeenCalled();
    expect(textarea.value).toBe('正在输入中文');
  });

  it('does a final phone upload sync before creating the task and sends the uploaded file', async () => {
    const uploadedImage = {
      file_key: 'phone-cat.jpg',
      url: '/uploads/images/phone-cat.jpg',
      name: 'phone-cat.jpg',
      size: 2048,
      type: 'image',
      mime_type: 'image/jpeg',
    };
    api.createMobileUploadSession.mockResolvedValueOnce({
      session_id: 'draft-upload',
      upload_url: '/mobile-upload/draft-upload',
      api_upload_url: '/api/mobile-upload/sessions/draft-upload/files',
    });
    api.getMobileUploadSession
      .mockResolvedValueOnce({ session_id: 'draft-upload', files: [] })
      .mockResolvedValue({ session_id: 'draft-upload', files: [uploadedImage] });
    const resolvedTopic = { topicId: 'p2p_1_21', name: '代码审查助手' };
    const onResolveAgentTopic = vi.fn().mockImplementation(async (_agent, draft) => {
      expect(draft.text).toBe('分析手机上传的图片');
      expect(draft.attachments).toEqual([
        expect.objectContaining({
          type: 'image',
          name: 'phone-cat.jpg',
          content: expect.objectContaining({
            payload: expect.objectContaining({ file_key: 'phone-cat.jpg' }),
          }),
        }),
      ]);
      return resolvedTopic;
    });
    const onActivateTopic = vi.fn();
    await mountComposer({ onResolveAgentTopic, onActivateTopic });

    await act(async () => {
      Simulate.click(container.querySelector('button.v3-composer-plus'));
    });
    await act(async () => {
      Simulate.click(container.querySelector('button[aria-label="手机扫码上传"]'));
      await flushPromises();
    });

    expect(api.createMobileUploadSession).toHaveBeenCalledWith('');
    expect(api.getMobileUploadSession).toHaveBeenCalledWith('draft-upload');

    const textarea = container.querySelector('textarea.v3-composer-input');
    await typeInto(textarea, '分析手机上传的图片');
    await pressEnter(textarea);

    expect(api.getMobileUploadSession).toHaveBeenCalledTimes(2);
    expect(onResolveAgentTopic).toHaveBeenCalledTimes(1);
    expect(api.sendMessage).toHaveBeenCalledWith(
      'p2p_1_21',
      expect.objectContaining({
        type: 'text',
        content: '分析手机上传的图片',
        content_blocks: expect.arrayContaining([
          { type: 'text', text: '分析手机上传的图片' },
          expect.objectContaining({
            type: 'image',
            payload: expect.objectContaining({
              file_key: 'phone-cat.jpg',
              url: '/uploads/images/phone-cat.jpg',
              name: 'phone-cat.jpg',
            }),
          }),
        ]),
      }),
    );
    expect(onActivateTopic).toHaveBeenCalledWith(resolvedTopic);
  });

  it('keeps the draft and rolls back the newly created empty task when sending fails', async () => {
    api.sendMessage.mockRejectedValueOnce(new Error('network unavailable'));
    const onResolveAgentTopic = vi.fn().mockResolvedValue({ topicId: 'grp_402', groupId: 402, isGroup: true });
    const onActivateTopic = vi.fn();
    await mountComposer({ onResolveAgentTopic, onActivateTopic });

    const textarea = container.querySelector('textarea.v3-composer-input');
    await typeInto(textarea, '不要丢失这段输入');
    await pressEnter(textarea);

    expect(api.sendMessage).toHaveBeenCalledWith('grp_402', '不要丢失这段输入');
    expect(api.disbandGroup).toHaveBeenCalledWith(402);
    expect(onActivateTopic).not.toHaveBeenCalled();
    expect(textarea.value).toBe('不要丢失这段输入');
    expect(container.textContent).toContain('network unavailable');
  });

  it('keeps the original send error when empty-task rollback also fails', async () => {
    api.sendMessage.mockRejectedValueOnce(new Error('original send failure'));
    api.disbandGroup.mockRejectedValueOnce(new Error('rollback failure'));
    const onResolveAgentTopic = vi.fn().mockResolvedValue({ topicId: 'grp_403', groupId: 403, isGroup: true });
    const onActivateTopic = vi.fn();
    await mountComposer({ onResolveAgentTopic, onActivateTopic });

    const textarea = container.querySelector('textarea.v3-composer-input');
    await typeInto(textarea, '保留原始错误');
    await pressEnter(textarea);

    expect(api.disbandGroup).toHaveBeenCalledWith(403);
    expect(container.textContent).toContain('original send failure');
    expect(container.textContent).not.toContain('rollback failure');
    expect(textarea.value).toBe('保留原始错误');
  });

  it('keeps the first instruction when task creation fails', async () => {
    const onResolveAgentTopic = vi.fn().mockRejectedValueOnce(new Error('task creation unavailable'));
    const onActivateTopic = vi.fn();
    await mountComposer({ initialAgent: agents[1], onResolveAgentTopic, onActivateTopic });

    const textarea = container.querySelector('textarea.v3-composer-input');
    await typeInto(textarea, '稍后还要继续发送');
    await pressEnter(textarea);

    expect(onResolveAgentTopic).toHaveBeenCalledWith(
      agents[1],
      { text: '稍后还要继续发送', attachments: [] },
    );
    expect(api.sendMessage).not.toHaveBeenCalled();
    expect(onActivateTopic).not.toHaveBeenCalled();
    expect(textarea.value).toBe('稍后还要继续发送');
    expect(container.textContent).toContain('task creation unavailable');
  });
});

async function typeInto(textarea, value) {
  await act(async () => {
    textarea.value = value;
    Simulate.change(textarea, { target: { value } });
    await flushPromises();
  });
}

async function pressEnter(textarea) {
  await act(async () => {
    Simulate.keyDown(textarea, { key: 'Enter', shiftKey: false });
    await flushPromises();
  });
}

async function flushPromises(count = 8) {
  for (let index = 0; index < count; index += 1) {
    await Promise.resolve();
  }
}
