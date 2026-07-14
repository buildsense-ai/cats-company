import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';

vi.mock('../widgets/chat-message', () => ({
  __esModule: true,
  default: function MockChatMessage(props) {
    const fileBlock = props.message?.content_blocks?.find?.((block) => block.type === 'file');
    if (!fileBlock) return null;
    return (
      <button
        type="button"
        className="mock-open-preview"
        onClick={() => props.onPreviewFile?.(fileBlock.payload)}
      >
        open preview
      </button>
    );
  },
  FilePreviewPanel: function MockFilePreviewPanel({ file }) {
    return <aside className="mock-file-preview">{file?.name || 'preview'}</aside>;
  },
}));

vi.mock('../widgets/group-settings', () => ({
  default: function MockGroupSettings() {
    return null;
  },
}));

vi.mock('../widgets/avatar', () => ({
  default: function MockAvatar() {
    return null;
  },
}));

vi.mock('../api', () => ({
  api: {
    getMessages: vi.fn(),
    getFriends: vi.fn(),
    getAgents: vi.fn(),
    getAgentQuota: vi.fn(),
    getGroupInfo: vi.fn(),
    createChannelIdentityMobileLink: vi.fn(),
    sendMessage: vi.fn(),
    uploadFile: vi.fn(),
    createMobileUploadSession: vi.fn(),
    getMobileUploadSession: vi.fn(),
    getTutorialTasks: vi.fn(),
  },
  wsSendMessage: vi.fn(),
  wsSendStreamCancel: vi.fn(),
  wsSendTyping: vi.fn(),
  wsSendRead: vi.fn(),
  onWSMessage: vi.fn(() => vi.fn()),
  updateTopicSeq: vi.fn(),
}));

import MessagesView from './messages-view';
import { TUTORIAL_TASKS } from '../widgets/tutorial-tasks';
import { api, onWSMessage } from '../api';

const user = {
  uid: 1,
  username: 'me',
  display_name: 'Me',
  avatar_url: '',
  account_type: 'human',
};

function renderTopic(root, topic, extraProps = {}) {
  root.render(
    <MessagesView
      topic={topic}
      topicName={topic}
      user={user}
      isGroup={false}
      groupId={null}
      topicAvatarUrl=""
      onTopicUpdated={vi.fn()}
      {...extraProps}
    />
  );
}

async function mountTopic(root, topic, extraProps = {}) {
  await act(async () => {
    renderTopic(root, topic, extraProps);
    await Promise.resolve();
  });
}

function typeDraft(textarea, value) {
  textarea.value = value;
  Simulate.change(textarea, {
    target: {
      value,
      selectionStart: value.length,
    },
  });
}

describe('MessagesView composer draft isolation', () => {
  let container;
  let root;
  let wsHandler;

  beforeEach(() => {
    global.IS_REACT_ACT_ENVIRONMENT = true;
    localStorage.clear();
    api.getMessages.mockResolvedValue({ messages: [] });
    api.getFriends.mockResolvedValue({ friends: [] });
    api.getAgents.mockResolvedValue({ agents: [] });
    api.getAgentQuota.mockResolvedValue({ configured: false, shared: true });
    api.createChannelIdentityMobileLink.mockResolvedValue({ qr_value: 'https://app.catsco.cc/mobile-link' });
    api.getGroupInfo.mockResolvedValue({ members: [], group: null });
    api.sendMessage.mockResolvedValue({ seq_id: 100 });
    api.getTutorialTasks.mockResolvedValue({ tasks: [], limit: 6 });
    api.uploadFile.mockResolvedValue({
      file_key: '20260610_default.jpg',
      url: '/uploads/images/20260610_default.jpg',
      name: 'default.jpg',
      size: 12,
      mime_type: 'image/jpeg',
    });
    api.createMobileUploadSession.mockResolvedValue({
      session_id: 'abc123',
      upload_url: '/mobile-upload/abc123',
      api_upload_url: '/api/mobile-upload/sessions/abc123/files',
    });
    api.getMobileUploadSession.mockResolvedValue({ session_id: 'abc123', files: [] });
    wsHandler = null;
    onWSMessage.mockImplementation((handler) => {
      wsHandler = handler;
      return vi.fn();
    });

    window.HTMLElement.prototype.scrollIntoView = vi.fn();
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(async () => {
    await act(async () => {
      root.unmount();
    });
    container.remove();
    vi.clearAllMocks();
  });

  it('preserves unsent drafts per topic when switching topics', async () => {
    await mountTopic(root, 'p2p_1_2');

    const firstTextarea = container.querySelector('textarea.v3-composer-input');
    await act(async () => {
      typeDraft(firstTextarea, 'keep this draft');
    });

    expect(firstTextarea.value).toBe('keep this draft');

    await mountTopic(root, 'p2p_1_3');

    const secondTextarea = container.querySelector('textarea.v3-composer-input');
    expect(secondTextarea.value).toBe('');

    await act(async () => {
      typeDraft(secondTextarea, 'another draft');
    });

    await mountTopic(root, 'p2p_1_2');

    expect(container.querySelector('textarea.v3-composer-input').value).toBe('keep this draft');

    await mountTopic(root, 'p2p_1_3');

    expect(container.querySelector('textarea.v3-composer-input').value).toBe('another draft');
  });

  it('does not restore a failed old-topic draft after the user has switched topics', async () => {
    let rejectSend;
    api.sendMessage.mockImplementationOnce(() => new Promise((resolve, reject) => {
      rejectSend = reject;
    }));

    await mountTopic(root, 'p2p_1_2');

    const textarea = container.querySelector('textarea.v3-composer-input');
    await act(async () => {
      typeDraft(textarea, 'old topic draft');
    });

    await act(async () => {
      Simulate.click(container.querySelector('button.v3-send'));
    });

    expect(container.querySelector('textarea.v3-composer-input').value).toBe('');

    await mountTopic(root, 'p2p_1_3');

    await act(async () => {
      rejectSend(new Error('send failed'));
      await Promise.resolve();
    });

    expect(container.querySelector('textarea.v3-composer-input').value).toBe('');
  });

  it('grows the composer until it reaches the scroll cap', async () => {
    await mountTopic(root, 'p2p_1_2');

    const textarea = container.querySelector('textarea.v3-composer-input');
    let scrollHeight = 128;
    Object.defineProperty(textarea, 'scrollHeight', {
      configurable: true,
      get: () => scrollHeight,
    });

    await act(async () => {
      typeDraft(textarea, 'line 1\nline 2\nline 3');
    });

    expect(textarea.style.height).toBe('128px');
    expect(textarea.style.overflowY).toBe('hidden');

    scrollHeight = 260;
    await act(async () => {
      typeDraft(textarea, 'line 1\nline 2\nline 3\nline 4\nline 5\nline 6\nline 7\nline 8');
    });

    expect(textarea.style.height).toBe('220px');
    expect(textarea.style.overflowY).toBe('auto');
  });

  it('lets the file preview panel width be adjusted and persisted', async () => {
    Object.defineProperty(window, 'innerWidth', {
      configurable: true,
      value: 1440,
    });
    api.getMessages.mockResolvedValueOnce({
      messages: [{
        id: 30,
        from_uid: 2,
        content: '[文件] report.html',
        content_blocks: [{
          type: 'file',
          payload: {
            name: 'report.html',
            url: '/uploads/files/report.html',
            mime_type: 'text/html',
          },
        }],
        created_at: '2026-06-12T00:00:00Z',
      }],
    });

    await mountTopic(root, 'p2p_1_2');

    await act(async () => {
      Simulate.click(container.querySelector('.mock-open-preview'));
      await Promise.resolve();
    });

    const workspace = container.querySelector('.v3-message-workspace');
    const handle = container.querySelector('.v3-preview-resize-handle');
    expect(workspace.className).toContain('has-preview');
    expect(handle).not.toBeNull();

    await act(async () => {
      Simulate.pointerDown(handle, { clientX: 900, pointerId: 1 });
      window.dispatchEvent(new MouseEvent('pointermove', { clientX: 780 }));
      window.dispatchEvent(new MouseEvent('pointerup'));
      await Promise.resolve();
    });

    expect(workspace.style.getPropertyValue('--v3-file-preview-width')).toBe('760px');
    expect(localStorage.getItem('cc_file_preview_width_v1')).toBe('760');
  });

  it('shows an inline error when an unsupported image is selected', async () => {
    await mountTopic(root, 'p2p_1_2');

    const input = container.querySelector('input[accept*="image/jpeg"]');
    const invalidImage = new File(['<svg></svg>'], 'vector.svg', { type: 'image/svg+xml' });

    await act(async () => {
      Simulate.change(input, {
        target: {
          files: [invalidImage],
          value: 'C:\\fakepath\\vector.svg',
        },
      });
    });

    expect(api.uploadFile).not.toHaveBeenCalled();
    expect(container.textContent).toContain('当前仅支持 JPG、PNG、GIF、WebP 图片。');
  });

  it('shows upload success inline after adding an image attachment', async () => {
    api.uploadFile.mockResolvedValueOnce({
      file_key: '20260610_abc.jpg',
      url: '/uploads/images/20260610_abc.jpg',
      name: 'cat.jpg',
      size: 12,
      mime_type: 'image/jpeg',
    });

    await mountTopic(root, 'p2p_1_2');

    const input = container.querySelector('input[accept*="image/jpeg"]');
    const image = new File(['hello'], 'cat.jpg', { type: 'image/jpeg' });

    await act(async () => {
      Simulate.change(input, {
        target: {
          files: [image],
          value: 'C:\\fakepath\\cat.jpg',
        },
      });
      await Promise.resolve();
    });

    expect(api.uploadFile).toHaveBeenCalledTimes(1);
    expect(api.uploadFile).toHaveBeenCalledWith(image, 'image');
    expect(container.textContent).toContain('已添加图片：cat.jpg');
    expect(container.textContent).toContain('cat.jpg');
  });

  it('opens a phone upload QR dialog from the composer', async () => {
    await mountTopic(root, 'p2p_1_2');

    const phoneUploadButton = container.querySelector('button[aria-label="微信扫码上传"]');
    expect(phoneUploadButton).not.toBeNull();
    expect(phoneUploadButton.getAttribute('data-tooltip')).toBe('微信扫码上传');

    await act(async () => {
      Simulate.click(phoneUploadButton);
    });

    expect(container.textContent).toContain('手机扫码上传');
    expect(container.textContent).toContain('/mobile-upload/');
  });

  it('uses an absolute phone upload URL without prefixing the browser origin', async () => {
    api.createMobileUploadSession.mockResolvedValueOnce({
      session_id: 'lan123',
      upload_url: 'https://app.example.test/mobile-upload/lan123',
      api_upload_url: '/api/mobile-upload/sessions/lan123/files',
    });

    await mountTopic(root, 'p2p_1_2');

    await act(async () => {
      Simulate.click(container.querySelector('button[aria-label="微信扫码上传"]'));
    });

    expect(container.textContent).toContain('https://app.example.test/mobile-upload/lan123');
    expect(container.textContent).not.toContain('localhost:6061https://app.example.test');
  });

  it('keeps syncing phone uploads after the QR dialog is closed', async () => {
    vi.useFakeTimers();
    api.createMobileUploadSession.mockResolvedValueOnce({
      session_id: 'sync-after-close',
      upload_url: '/mobile-upload/sync-after-close',
      api_upload_url: '/api/mobile-upload/sessions/sync-after-close/files',
    });
    api.getMobileUploadSession
      .mockResolvedValueOnce({ session_id: 'sync-after-close', files: [] })
      .mockResolvedValueOnce({
        session_id: 'sync-after-close',
        files: Array.from({ length: 8 }, (_, index) => ({
          file_key: `image-${index + 1}.jpg`,
          url: `/uploads/images/image-${index + 1}.jpg`,
          name: `image-${index + 1}.jpg`,
          size: 1024,
          type: 'image',
          mime_type: 'image/jpeg',
        })),
      })
      .mockResolvedValueOnce({
        session_id: 'sync-after-close',
        files: Array.from({ length: 9 }, (_, index) => ({
          file_key: `image-${index + 1}.jpg`,
          url: `/uploads/images/image-${index + 1}.jpg`,
          name: `image-${index + 1}.jpg`,
          size: 1024,
          type: 'image',
          mime_type: 'image/jpeg',
        })),
      });

    await mountTopic(root, 'p2p_1_2');

    await act(async () => {
      Simulate.click(container.querySelector('button[aria-label="微信扫码上传"]'));
    });

    await act(async () => {
      vi.advanceTimersByTime(2000);
      await Promise.resolve();
    });

    expect(container.textContent).toContain('8 个附件待发送');

    await act(async () => {
      Simulate.click(container.querySelector('button[aria-label="关闭手机上传"]'));
    });

    await act(async () => {
      vi.advanceTimersByTime(2000);
      await Promise.resolve();
    });

    expect(container.textContent).toContain('9 个附件待发送');
    vi.useRealTimers();
  });

  it('shows tutorial task cards on an empty topic', async () => {
    await mountTopic(root, 'p2p_1_2');

    expect(container.textContent).toContain('试一个文件任务');
    expect(container.textContent).toContain('读图提取信息');
    expect(container.textContent).toContain('移动文件到桌面');
  });

  it('waits for history before showing tutorial task cards', async () => {
    let resolveHistory;
    api.getMessages.mockImplementationOnce(() => new Promise((resolve) => {
      resolveHistory = resolve;
    }));

    await act(async () => {
      renderTopic(root, 'p2p_1_2');
      await Promise.resolve();
    });

    expect(container.querySelector('.cc-tutorial-empty')).toBeNull();

    await act(async () => {
      resolveHistory({ messages: [] });
      await Promise.resolve();
    });

    expect(container.querySelector('.cc-tutorial-empty')).not.toBeNull();
  });

  it('downloads tutorial media and fills the selected prompt', async () => {
    await mountTopic(root, 'p2p_1_2', { localAssistantStatus: 'connected' });

    await act(async () => {
      Simulate.click(Array.from(container.querySelectorAll('.cc-tutorial-card')).find((el) => el.textContent.includes('读图提取信息')));
    });

    const downloadLink = container.querySelector('a[download="catsco-tutorial-sample.png"]');
    expect(downloadLink.getAttribute('href')).toBe('/demo-artifacts/catsco-tutorial-sample.png');

    await act(async () => {
      Simulate.click(downloadLink);
    });
    expect(container.textContent).toContain('已开始下载');

    await act(async () => {
      Simulate.click(Array.from(container.querySelectorAll('button')).find((el) => el.textContent.includes('填入任务')));
    });

    expect(container.querySelector('textarea.v3-composer-input').value).toBe(TUTORIAL_TASKS[0].prompt);
    expect(container.textContent).toContain('已填入示例任务，你可以直接发送。');
  });

  it('dismisses tutorial cards for the current topic and stores the choice', async () => {
    const onTutorialHint = vi.fn();
    await mountTopic(root, 'p2p_1_2', { onTutorialHint });

    await act(async () => {
      Simulate.click(Array.from(container.querySelectorAll('button')).find((el) => el.textContent.includes('暂时不用')));
    });

    expect(container.textContent).not.toContain('试一个文件任务');
    expect(localStorage.getItem('cc_tutorial_empty_dismissed:v1:1:p2p_1_2')).toBe('1');
    expect(onTutorialHint).toHaveBeenCalledTimes(1);
  });

  it('opens tutorial picker from the profile menu trigger token', async () => {
    await mountTopic(root, 'p2p_1_2', { tutorialOpenToken: 1 });

    expect(container.textContent).toContain('选择示例任务');
    expect(container.textContent).toContain('选择一个任务，下载示例文件');
  });

  it('shows mobile binding action for bot friends identified by bot flag', async () => {
    api.getFriends.mockResolvedValueOnce({
      friends: [
        {
          id: 2,
          username: 'dev-agent',
          display_name: 'Dev Agent',
          bot: true,
        },
      ],
    });

    await mountTopic(root, 'p2p_1_2');

    expect(container.querySelector('button[title="移动端使用"]')).toBeTruthy();
  });

  it('shows mobile binding action when agent roster identifies the peer as bot', async () => {
    api.getFriends.mockResolvedValueOnce({
      friends: [
        {
          id: 2,
          username: 'friend-agent',
          display_name: 'Friend Agent',
        },
      ],
    });
    api.getAgents.mockResolvedValueOnce({
      agents: [
        {
          uid: 2,
          username: 'friend-agent',
          display_name: 'Friend Agent',
          relation: 'friend',
          is_bot: true,
        },
      ],
    });

    await mountTopic(root, 'p2p_1_2');

    expect(container.querySelector('button[title="移动端使用"]')).toBeTruthy();
  });

  it('shows the owner shared quota for the active bot conversation', async () => {
    api.getAgents.mockResolvedValueOnce({
      agents: [{
        uid: 2,
        username: 'friend-agent',
        display_name: 'Friend Agent',
        relation: 'friend',
        is_bot: true,
      }],
    });
    api.getAgentQuota.mockResolvedValueOnce({
      configured: true,
      shared: true,
      summary: {
        source: 'relay',
        model: 'MiniMax-M3',
        remaining_percent: 72,
        status: 'normal',
      },
    });

    await mountTopic(root, 'p2p_1_2');
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });

    const quota = container.querySelector('.v3-agent-quota-pill');
    expect(api.getAgentQuota).toHaveBeenCalledWith(2);
    expect(quota?.textContent).toBe('M3 剩余 72%');
    expect(quota?.getAttribute('title')).toBe('使用该虚拟员工所属账号的共享额度');
  });

  it('shows the reported custom model name for the active bot conversation', async () => {
    api.getAgents.mockResolvedValueOnce({
      agents: [{
        uid: 2,
        username: 'friend-agent',
        display_name: 'Friend Agent',
        relation: 'friend',
        is_bot: true,
      }],
    });
    api.getAgentQuota.mockResolvedValueOnce({
      configured: true,
      shared: false,
      summary: {
        source: 'custom',
        model: 'gpt-5.6-terra',
        status: 'custom',
      },
    });

    await mountTopic(root, 'p2p_1_2');
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });

    const quota = container.querySelector('.v3-agent-quota-pill');
    expect(quota?.textContent).toBe('gpt-5.6-terra · 自备');
    expect(quota?.getAttribute('title')).toBe('gpt-5.6-terra；该虚拟员工使用自备模型，不消耗 CatsCo 共享额度');
  });

  it('clears peer typing immediately when a peer final reply arrives', async () => {
    await mountTopic(root, 'p2p_1_2');

    await act(async () => {
      wsHandler({
        info: {
          topic: 'p2p_1_2',
          what: 'kp',
          from: 'usr2',
        },
      });
    });

    expect(container.textContent).toContain('输入');

    await act(async () => {
      wsHandler({
        data: {
          seq_id: 22,
          seq: 22,
          topic: 'p2p_1_2',
          from: 'usr2',
          content: 'done',
          type: 'text',
          msg_type: 'text',
        },
      });
    });

    expect(container.textContent).not.toContain('输入');
  });
});
