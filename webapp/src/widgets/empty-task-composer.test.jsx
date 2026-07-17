import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';

vi.mock('../api', () => ({
  api: {
    getAgents: vi.fn(),
    sendMessage: vi.fn(),
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

    const composer = container.querySelector('.v3-composer[aria-label="新对话输入栏"]');
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

  it('shows the selected Agent and lets the user choose another one', async () => {
    const { onResolveAgentTopic, onActivateTopic } = await mountComposer();

    const agentButton = container.querySelector('.v3-agent-picker-button');
    expect(agentButton?.textContent).toContain('代码审查助手');

    await act(async () => {
      Simulate.click(agentButton);
    });
    const options = container.querySelectorAll('.v3-agent-picker-menu [role="option"]');
    expect(options).toHaveLength(2);

    await act(async () => {
      Simulate.click(options[1]);
    });
    expect(container.querySelector('.v3-agent-picker-button')?.textContent).toContain('运营数据助手');
    expect(onResolveAgentTopic).not.toHaveBeenCalled();
    expect(onActivateTopic).not.toHaveBeenCalled();
    expect(api.sendMessage).not.toHaveBeenCalled();
  });

  it('resolves the selected Agent, sends to its topic, then activates it on Enter', async () => {
    const order = [];
    const resolvedTopic = { topicId: 'p2p_1_21', name: '代码审查助手' };
    const onResolveAgentTopic = vi.fn().mockImplementation(async (agent) => {
      order.push('resolve');
      expect(agent.uid).toBe(21);
      return resolvedTopic;
    });
    api.sendMessage.mockImplementationOnce(async (topicId, payload) => {
      order.push('send');
      expect(topicId).toBe('p2p_1_21');
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
    expect(api.sendMessage).toHaveBeenCalledWith('p2p_1_21', '检查这段代码');
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

  it('polls a phone upload draft and sends the uploaded file with the first message', async () => {
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
    const onResolveAgentTopic = vi.fn().mockResolvedValue(resolvedTopic);
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

    await act(async () => {
      vi.advanceTimersByTime(2000);
      await flushPromises();
    });
    expect(container.textContent).toContain('phone-cat.jpg');

    const textarea = container.querySelector('textarea.v3-composer-input');
    await typeInto(textarea, '分析手机上传的图片');
    await pressEnter(textarea);

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

  it('keeps the draft when sending fails', async () => {
    api.sendMessage.mockRejectedValueOnce(new Error('network unavailable'));
    const onResolveAgentTopic = vi.fn().mockResolvedValue({ topicId: 'p2p_1_21' });
    const onActivateTopic = vi.fn();
    await mountComposer({ onResolveAgentTopic, onActivateTopic });

    const textarea = container.querySelector('textarea.v3-composer-input');
    await typeInto(textarea, '不要丢失这段输入');
    await pressEnter(textarea);

    expect(api.sendMessage).toHaveBeenCalledWith('p2p_1_21', '不要丢失这段输入');
    expect(onActivateTopic).not.toHaveBeenCalled();
    expect(textarea.value).toBe('不要丢失这段输入');
    expect(container.textContent).toContain('network unavailable');
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
