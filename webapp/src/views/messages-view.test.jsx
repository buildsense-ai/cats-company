import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

vi.mock('../widgets/chat-message', () => ({
  __esModule: true,
  default: function MockChatMessage(props) {
    const fileBlock = props.message?.content_blocks?.find?.((block) => block.type === 'file');
    const textBlocks = props.message?.content_blocks?.filter?.((block) => block.type === 'text') || [];
    return (
      <div
        className="mock-chat-message"
        data-conversation-question={props.questionAnchorKey || undefined}
        data-message-id={props.message?.id}
        data-message-content={typeof props.message?.content === 'string' ? props.message.content : ''}
        data-consecutive={String(Boolean(props.isConsecutive))}
        data-known-artifact-count={String(props.knownArtifacts?.length || 0)}
        data-working-only={String(Boolean(props.workingOnly))}
        data-working-complete={String(Boolean(props.workingComplete))}
        data-working-count={String(props.workingMessages?.length || 0)}
        data-working-message-ids={(props.workingMessages || []).map((message) => message.id).join(',')}
        data-artifacts-first={String(Boolean(props.artifactsFirst))}
        data-content-block-count={String(props.message?.content_blocks?.length || 0)}
        data-text-block-roles={textBlocks.map((block) => block.presentation_role || 'body').join(',')}
        data-text-block-texts={textBlocks.map((block) => block.text || '').join('|')}
        data-sender-name={props.senderName || ''}
        data-sender-is-bot={String(Boolean(props.senderIsBot))}
      >
        {props.onReply && (
          <button
            type="button"
            className="mock-reply-message"
            data-message-id={props.message?.id}
            onClick={props.onReply}
          >
            reply
          </button>
        )}
        {props.onRegenerate && (
          <button
            type="button"
            className="mock-regenerate-message"
            data-message-id={props.message?.id}
            onClick={() => props.onRegenerate(props.message)}
          >
            regenerate
          </button>
        )}
        {props.onEdit && (
          <button
            type="button"
            className="mock-edit-message"
            data-message-id={props.message?.id}
            onClick={() => props.onEdit(props.message)}
          >
            edit
          </button>
        )}
        {fileBlock && (
          <button
            type="button"
            className="mock-open-preview"
            onClick={() => props.onPreviewFile?.(fileBlock.payload)}
          >
            open preview
          </button>
        )}
      </div>
    );
  },
  FilePreviewPanel: function MockFilePreviewPanel({ file, onBack, backgroundRef }) {
    return (
      <aside
        className="mock-file-preview"
        data-url={file?.url || ''}
        data-background-class={backgroundRef?.current?.className || ''}
      >
        {file?.name || 'preview'}
        {onBack && (
          <button type="button" aria-label="返回产物列表" onClick={onBack}>
            back
          </button>
        )}
      </aside>
    );
  },
  createCloudArtifactPreviewFile: (artifact) => ({
    name: artifact.title || artifact.id,
    url: artifact.url,
    mime_type: 'text/html',
    artifact_id: artifact.id,
  }),
  previewFileDescriptor: (file) => {
    const name = String(file?.name || file?.url || '').toLowerCase();
    const canPreview = /\.(?:csv|html?|json|md|pdf|txt|xlsx|xml)(?:[?#].*)?$/.test(name);
    return {
      url: file?.url || '',
      canPreview,
      downloadURL: file?.url || '',
    };
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
    getCloudArtifacts: vi.fn(),
    getAgentFiles: vi.fn(),
    deleteCloudArtifact: vi.fn(),
    restoreCloudArtifact: vi.fn(),
  },
  wsSendMessage: vi.fn(),
  wsSendStreamCancel: vi.fn(),
  wsSendTyping: vi.fn(),
  wsSendRead: vi.fn(),
  onWSMessage: vi.fn(() => vi.fn()),
  updateTopicSeq: vi.fn(),
  getApiBaseURL: () => window.location.origin,
}));

import MessagesView, {
  collectStructuredMentionTargets,
  reconcileStructuredMentionSelections,
} from './messages-view';
import { TUTORIAL_TASKS } from '../widgets/tutorial-tasks';
import { api, onWSMessage, wsSendStreamCancel } from '../api';
import { CHAT_ATTACHMENT_DRAG_TYPE, writeChatAttachmentDrag } from '../chat-attachment-drag';

const openchatThemeCss = readFileSync(
  resolve(process.cwd(), 'src/css/openchat-theme.css'),
  'utf8',
);

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

async function openPhoneUploadFromComposer(container) {
  const attachmentButton = container.querySelector('button[aria-label="添加文件或图片"]');
  expect(attachmentButton).not.toBeNull();

  await act(async () => {
    Simulate.click(attachmentButton);
    await Promise.resolve();
  });

  expect(attachmentButton.getAttribute('aria-expanded')).toBe('true');
  const phoneUploadButton = container.querySelector('button[aria-label="手机扫码上传"]');
  expect(phoneUploadButton).not.toBeNull();

  await act(async () => {
    Simulate.click(phoneUploadButton);
    await Promise.resolve();
  });

  return phoneUploadButton;
}

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, resolve, reject };
}

function composerAgentFixtures() {
  return {
    codeAgent: {
      uid: 2,
      username: 'code-agent',
      display_name: '代码审查助手',
      topic_id: 'p2p_1_2',
      is_bot: true,
    },
    opsAgent: {
      uid: 3,
      username: 'ops-agent',
      display_name: '运营数据助手',
      topic_id: 'p2p_1_3',
      is_bot: true,
    },
  };
}

function mockTutorialAgentPeer(peerId = 2) {
  api.getAgents.mockResolvedValue({
    agents: [{
      uid: peerId,
      username: 'tutorial-agent',
      display_name: 'Tutorial Agent',
      is_bot: true,
    }],
  });
}

async function flushPromises(count = 8) {
  for (let index = 0; index < count; index += 1) {
    await Promise.resolve();
  }
}

describe('structured composer mention provenance', () => {
  it('does not promote hand-typed uid-like text into structured targets', () => {
    expect(collectStructuredMentionTargets('@usr42 请处理', [])).toEqual([]);
  });

  it('keeps picker selections across edits outside the selected token', () => {
    const selection = [{ target: 'usr42', start: 0, end: 6 }];
    const afterAppending = reconcileStructuredMentionSelections('@usr42 ', '@usr42 处理', selection);
    const reconciled = reconcileStructuredMentionSelections('@usr42 处理', '请 @usr42 处理', afterAppending);
    expect(reconciled).toEqual([{ target: 'usr42', start: 2, end: 8 }]);
    expect(collectStructuredMentionTargets('请 @usr42 处理', reconciled)).toEqual(['usr42']);
  });

  it('keeps the picker-only all-bots target across surrounding edits', () => {
    const selection = [{ target: 'all', start: 0, end: 4 }];
    const afterAppending = reconcileStructuredMentionSelections('@所有人 ', '@所有人 一起处理', selection);
    const reconciled = reconcileStructuredMentionSelections('@所有人 一起处理', '请 @所有人 一起处理', afterAppending);
    expect(reconciled).toEqual([{ target: 'all', start: 2, end: 6 }]);
    expect(collectStructuredMentionTargets('请 @所有人 一起处理', reconciled)).toEqual(['all']);
    expect(collectStructuredMentionTargets('@所有人 一起处理', [])).toEqual([]);
  });

  it('drops picker provenance when the selected token is edited', () => {
    const selection = [{ target: 'usr42', start: 0, end: 6 }];
    expect(reconcileStructuredMentionSelections('@usr42 ', '@usr43 ', selection)).toEqual([]);
  });

  it('drops picker provenance when text is inserted against the token boundary', () => {
    const selection = [{ target: 'usr42', start: 0, end: 6 }];
    expect(reconcileStructuredMentionSelections('@usr42 ', '@usr42x ', selection)).toEqual([]);
    expect(collectStructuredMentionTargets('@usr42x ', selection)).toEqual([]);
  });

});

describe('MessagesView composer draft isolation', () => {
  let container;
  let root;
  let wsHandler;
  let originalIntersectionObserver;

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
    api.getCloudArtifacts.mockResolvedValue({ artifacts: [] });
    api.getAgentFiles.mockResolvedValue({ files: [], has_more: false, next_before_id: 0 });
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
    originalIntersectionObserver = window.IntersectionObserver;
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(async () => {
    await act(async () => {
      root.unmount();
    });
    vi.useRealTimers();
    window.IntersectionObserver = originalIntersectionObserver;
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

  it('adapts the composer placeholder to agent groups, agent chats, and human chats', async () => {
    api.getGroupInfo.mockResolvedValueOnce({
      members: [
        { user_id: 1, display_name: 'Me', account_type: 'human' },
        { user_id: 5, display_name: 'Teammate', account_type: 'human' },
        { user_id: 2, display_name: 'Design Agent', account_type: 'bot', is_bot: true },
      ],
      group: { id: 9, name: 'Mixed group', has_bot: true },
    });

    await mountTopic(root, 'grp_9', { isGroup: true, groupId: 9 });
    await act(async () => {
      await flushPromises();
    });
    expect(container.querySelector('textarea.v3-composer-input').placeholder)
      .toBe('输入消息，@机器人即可回复');

    api.getGroupInfo.mockResolvedValueOnce({
      members: [
        { user_id: 1, display_name: 'Me', account_type: 'human' },
        { user_id: 2, display_name: 'Design Agent', account_type: 'bot', is_bot: true },
      ],
      group: { id: 10, name: 'Agent task', has_bot: true, is_agent_task: true },
    });
    await mountTopic(root, 'grp_10', { isGroup: true, groupId: 10 });
    await act(async () => {
      await flushPromises();
    });
    expect(container.querySelector('textarea.v3-composer-input').placeholder)
      .toBe('输入指令，我帮您完成');

    api.getAgents.mockResolvedValueOnce({
      agents: [{ uid: 3, username: 'agent', display_name: 'Agent', is_bot: true }],
    });
    await mountTopic(root, 'p2p_1_3', { isGroup: false, groupId: null });
    await act(async () => {
      await flushPromises();
    });
    expect(container.querySelector('textarea.v3-composer-input').placeholder)
      .toBe('输入指令，我帮您完成');

    api.getFriends.mockResolvedValueOnce({
      friends: [{ id: 4, username: 'friend', display_name: 'Friend', account_type: 'human' }],
    });
    api.getAgents.mockResolvedValueOnce({ agents: [] });
    await mountTopic(root, 'p2p_1_4', { isGroup: false, groupId: null });
    await act(async () => {
      await flushPromises();
    });
    expect(container.querySelector('textarea.v3-composer-input').placeholder)
      .toBe('输入消息');
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

    expect(textarea.style.height).toBe('200px');
    expect(textarea.style.overflowY).toBe('auto');
  });

  it('sends an ordinary friend message while the local assistant is disconnected', async () => {
    const onOpenDesktopConnect = vi.fn();
    api.getFriends.mockResolvedValueOnce({
      friends: [{ id: 2, username: 'alice', display_name: 'Alice', account_type: 'human' }],
    });

    await mountTopic(root, 'p2p_1_2', {
      localAssistantStatus: 'disconnected',
      onOpenDesktopConnect,
    });

    const textarea = container.querySelector('textarea.v3-composer-input');
    await act(async () => {
      typeDraft(textarea, '普通好友消息');
    });
    await act(async () => {
      Simulate.click(container.querySelector('button.v3-send'));
      await Promise.resolve();
    });

    expect(api.sendMessage).toHaveBeenCalledWith('p2p_1_2', '普通好友消息', undefined);
    expect(onOpenDesktopConnect).not.toHaveBeenCalled();
  });

  it('places a previous user instruction back into the composer for editing', async () => {
    api.getMessages.mockResolvedValueOnce({
      messages: [{
        id: 68,
        seq_id: 68,
        topic_id: 'p2p_1_2',
        from_uid: 1,
        type: 'text',
        content: 'Please review this instruction again.',
        created_at: '2026-06-09T00:00:00Z',
      }],
    });

    await mountTopic(root, 'p2p_1_2');
    await act(async () => {
      await flushPromises();
    });

    const editButton = container.querySelector('.mock-edit-message[data-message-id="68"]');
    expect(editButton).not.toBeNull();
    await act(async () => {
      Simulate.click(editButton);
      await new Promise((resolve) => window.setTimeout(resolve, 0));
    });

    const textarea = container.querySelector('textarea.v3-composer-input');
    expect(textarea.value).toBe('Please review this instruction again.');
    expect(document.activeElement).toBe(textarea);
    expect(container.querySelector('.v3-attachment-notice')?.textContent)
      .toContain('已将原指令放回输入框');

    await act(async () => {
      typeDraft(textarea, '');
    });

    expect(textarea.value).toBe('');
    expect(container.querySelector('.v3-attachment-notice')).toBeNull();
  });

  it('restores text and attachments when preparing a previous message to resend', async () => {
    api.getMessages.mockResolvedValueOnce({
      messages: [{
        id: 69,
        seq_id: 69,
        topic_id: 'p2p_1_2',
        from_uid: 1,
        type: 'text',
        content: '描述这张图',
        content_blocks: [
          { type: 'text', text: '描述这张图' },
          { type: 'image', payload: { file_key: 'cat.png', url: '/uploads/images/cat.png', name: 'cat.png', size: 12, mime_type: 'image/png' } },
        ],
        created_at: '2026-06-09T00:00:00Z',
      }],
    });

    await mountTopic(root, 'p2p_1_2');
    await act(async () => {
      await flushPromises();
      Simulate.click(container.querySelector('.mock-edit-message[data-message-id="69"]'));
      await new Promise((resolve) => window.setTimeout(resolve, 0));
    });

    expect(container.querySelector('textarea.v3-composer-input').value).toBe('描述这张图');
    expect(container.querySelectorAll('.v3-composer-attachment-chip')).toHaveLength(1);
    expect(container.querySelector('[aria-label="预览图片：cat.png"]')).not.toBeNull();
    expect(container.textContent).toContain('原文字和 1 个附件');
  });

  it('keeps legacy message content when attachment blocks have no text block', async () => {
    api.getMessages.mockResolvedValueOnce({
      messages: [{
        id: 70,
        seq_id: 70,
        topic_id: 'p2p_1_2',
        from_uid: 1,
        type: 'text',
        content: '旧格式正文仍要保留',
        content_blocks: [
          { type: 'image', payload: { file_key: 'legacy.png', url: '/uploads/images/legacy.png', name: 'legacy.png', size: 12 } },
        ],
        created_at: '2026-06-09T00:00:00Z',
      }],
    });

    await mountTopic(root, 'p2p_1_2');
    await act(async () => {
      await flushPromises();
      Simulate.click(container.querySelector('.mock-edit-message[data-message-id="70"]'));
      await new Promise((resolve) => window.setTimeout(resolve, 0));
    });

    expect(container.querySelector('textarea.v3-composer-input').value).toBe('旧格式正文仍要保留');
    expect(container.querySelector('[aria-label="预览图片：legacy.png"]')).not.toBeNull();
  });

  it('accepts a chat image drag without uploading the file again', async () => {
    await mountTopic(root, 'p2p_1_2');
    const attachment = {
      type: 'image',
      name: 'cat.png',
      size: 12,
      content: {
        type: 'image',
        payload: { file_key: 'cat.png', url: '/uploads/images/cat.png', name: 'cat.png', size: 12, mime_type: 'image/png' },
      },
    };
    const values = new Map();
    const dataTransfer = {
      types: [],
      setData: (type, value) => {
        values.set(type, value);
        if (!dataTransfer.types.includes(type)) dataTransfer.types.push(type);
      },
      getData: (type) => values.get(type) || '',
      dropEffect: 'none',
      effectAllowed: 'none',
    };
    writeChatAttachmentDrag(dataTransfer, { type: attachment.type, payload: attachment.content.payload });
    const timeline = container.querySelector('.v3-timeline');

    await act(async () => {
      Simulate.dragEnter(timeline, { dataTransfer });
      Simulate.drop(timeline, { dataTransfer });
      await Promise.resolve();
    });

    expect(api.uploadFile).not.toHaveBeenCalled();
    expect(container.querySelectorAll('.v3-composer-attachment-chip')).toHaveLength(1);
    expect(container.querySelector('[aria-label="预览图片：cat.png"]')).not.toBeNull();
    expect(container.textContent).toContain('已添加图片：cat.png');

    writeChatAttachmentDrag(dataTransfer, { type: attachment.type, payload: attachment.content.payload });
    await act(async () => {
      Simulate.drop(timeline, { dataTransfer });
      await Promise.resolve();
    });

    expect(container.querySelectorAll('.v3-composer-attachment-chip')).toHaveLength(1);
    expect(container.textContent).toContain('cat.png 已在待发送附件中');
  });

  it('accepts a chat file drag without uploading the file again', async () => {
    await mountTopic(root, 'p2p_1_2');
    const values = new Map();
    const dataTransfer = {
      types: [],
      setData: (type, value) => {
        values.set(type, value);
        if (!dataTransfer.types.includes(type)) dataTransfer.types.push(type);
      },
      getData: (type) => values.get(type) || '',
      dropEffect: 'none',
      effectAllowed: 'none',
    };
    writeChatAttachmentDrag(dataTransfer, {
      type: 'file',
      payload: {
        file_key: 'report.pdf',
        url: '/uploads/files/report.pdf',
        name: 'report.pdf',
        size: 24,
        mime_type: 'application/pdf',
      },
    });

    await act(async () => {
      Simulate.drop(container.querySelector('.v3-timeline'), { dataTransfer });
      await Promise.resolve();
    });

    expect(api.uploadFile).not.toHaveBeenCalled();
    expect(container.querySelectorAll('.v3-composer-attachment-chip')).toHaveLength(1);
    expect(container.querySelector('.v3-composer-attachment-chip.is-file[title="report.pdf"]')).not.toBeNull();
    expect(container.textContent).toContain('已添加文件：report.pdf');
  });

  it('rejects a forged chat attachment token without adding a draft', async () => {
    await mountTopic(root, 'p2p_1_2');
    const dataTransfer = {
      types: [CHAT_ATTACHMENT_DRAG_TYPE],
      getData: () => '00000000-0000-4000-8000-000000000000',
      files: [],
      items: [],
      dropEffect: 'none',
    };

    await act(async () => {
      Simulate.drop(container.querySelector('.v3-timeline'), { dataTransfer });
      await Promise.resolve();
    });

    expect(api.uploadFile).not.toHaveBeenCalled();
    expect(container.textContent).not.toContain('个附件待发送');
    expect(container.textContent).toContain('这次拖入没有识别到可上传的文件');
  });

  it('keeps the full reply preview available for single-line CSS truncation and clears it explicitly', async () => {
    const longReply = '这是一段明显超过旧版六十字硬截断限制的回复内容，用来确保预览栏保留完整原文，并交给界面根据实际可用宽度显示省略号，而不是提前丢失后半段文字。';
    api.getMessages.mockResolvedValueOnce({
      messages: [{
        id: 69,
        seq_id: 69,
        topic_id: 'p2p_1_2',
        from_uid: 1,
        type: 'text',
        content: longReply,
        created_at: '2026-06-09T00:01:00Z',
      }],
    });

    await mountTopic(root, 'p2p_1_2');
    await act(async () => {
      await flushPromises();
    });

    const replyButton = container.querySelector('.mock-reply-message[data-message-id="69"]');
    expect(replyButton).not.toBeNull();
    await act(async () => {
      Simulate.click(replyButton);
    });

    const replyBar = container.querySelector('.oc-reply-bar');
    const closeButton = replyBar?.querySelector('.oc-reply-bar-close');
    const composerBox = container.querySelector('.v3-composer-box');
    expect(replyBar?.querySelector('.oc-reply-bar-text')?.textContent).toBe(longReply);
    expect(composerBox?.contains(replyBar)).toBe(true);
    expect(replyBar?.closest('.v3-composer-context')).not.toBeNull();
    expect(closeButton?.getAttribute('type')).toBe('button');
    expect(closeButton?.getAttribute('aria-label')).toBe('取消回复');

    await act(async () => {
      Simulate.click(closeButton);
    });
    expect(container.querySelector('.oc-reply-bar')).toBeNull();
  });

  it('lets the reply preview inherit the composer width at every viewport', () => {
    expect(openchatThemeCss).toContain('width: 100% !important;');
    expect(openchatThemeCss).not.toContain('width: min(760px, calc(100% - 40px)) !important;');
    expect(openchatThemeCss).toMatch(
      /\.oc-reply-bar-content \{[^}]*overflow: hidden;[^}]*white-space: nowrap;[^}]*text-overflow: ellipsis;/s,
    );
  });

  it('keeps historical file metadata within two complete rows on narrow screens', () => {
    expect(openchatThemeCss).toMatch(
      /\.cloud-artifact-copy p \{[^}]*column-gap: 10px;[^}]*row-gap: 2px;[^}]*max-height: 34px;[^}]*line-height: 16px;/s,
    );
    expect(openchatThemeCss).toMatch(
      /@media \(max-width: 480px\) \{[\s\S]*?\.cloud-file-item \.cloud-artifact-copy p \{[^}]*grid-template-columns: minmax\(0, 1fr\) auto;[^}]*grid-template-rows: repeat\(2, 16px\);/,
    );
    expect(openchatThemeCss).toMatch(
      /@media \(max-width: 340px\) \{[\s\S]*?\.cloud-file-meta-time \{\s*display: none;/,
    );
  });

  it('merges adjacent assistant text chunks into one visual reply', async () => {
    mockTutorialAgentPeer();
    api.getMessages.mockResolvedValueOnce({
      messages: [
        {
          id: 70,
          seq_id: 70,
          topic_id: 'p2p_1_2',
          from_uid: 1,
          type: 'text',
          content: 'Explain providers.',
          created_at: '2026-07-20T09:23:00Z',
        },
        {
          id: 71,
          seq_id: 71,
          topic_id: 'p2p_1_2',
          from_uid: 2,
          role: 'assistant',
          type: 'text',
          content: 'A provider supplies the service.',
          created_at: '2026-07-20T09:24:00Z',
        },
        {
          id: 72,
          seq_id: 72,
          topic_id: 'p2p_1_2',
          from_uid: 2,
          role: 'assistant',
          type: 'text',
          content: 'The agent coordinates the work.',
          created_at: '2026-07-20T09:24:12Z',
        },
        {
          id: 73,
          seq_id: 73,
          topic_id: 'p2p_1_2',
          from_uid: 2,
          role: 'assistant',
          type: 'text',
          content: 'The provider performs it.',
          created_at: '2026-07-20T09:24:24Z',
        },
      ],
    });

    await mountTopic(root, 'p2p_1_2');
    await act(async () => {
      await flushPromises();
    });

    const renderedMessages = container.querySelectorAll('.mock-chat-message');
    expect(renderedMessages).toHaveLength(2);
    const assistantReply = container.querySelector('.mock-chat-message[data-message-id="73"]');
    expect(assistantReply).not.toBeNull();
    expect(assistantReply.getAttribute('data-message-content')).toBe(
      'A provider supplies the service. The agent coordinates the work. The provider performs it.',
    );
    expect(container.querySelectorAll('.mock-regenerate-message')).toHaveLength(1);
  });

  it('preserves explicit paragraph and Markdown boundaries while merging one assistant turn', async () => {
    mockTutorialAgentPeer();
    api.getMessages.mockResolvedValueOnce({
      messages: [
        {
          id: 74,
          topic_id: 'p2p_1_2',
          from_uid: 1,
          type: 'text',
          content: '整理结果。',
          created_at: '2026-07-20T09:25:00Z',
        },
        {
          id: 75,
          topic_id: 'p2p_1_2',
          from_uid: 2,
          role: 'assistant',
          type: 'text',
          content: '第一段。\n\n第二段。',
          created_at: '2026-07-20T09:25:10Z',
        },
        {
          id: 76,
          topic_id: 'p2p_1_2',
          from_uid: 2,
          role: 'assistant',
          type: 'text',
          content: '- 保留列表一\n- 保留列表二',
          created_at: '2026-07-20T09:25:20Z',
        },
        {
          id: 77,
          topic_id: 'p2p_1_2',
          from_uid: 2,
          role: 'assistant',
          type: 'text',
          content: '列表后的说明。',
          created_at: '2026-07-20T09:25:30Z',
        },
      ],
    });

    await mountTopic(root, 'p2p_1_2');
    await act(async () => {
      await flushPromises();
    });

    const assistantReply = container.querySelector('.mock-chat-message[data-message-id="77"]');
    expect(assistantReply?.getAttribute('data-message-content')).toBe(
      '第一段。\n\n第二段。\n\n- 保留列表一\n- 保留列表二\n\n列表后的说明。',
    );
  });

  it('keeps plan updates from the same Agent turn in one working group across assistant text', async () => {
    mockTutorialAgentPeer();
    api.getMessages.mockResolvedValueOnce({
      messages: [
        {
          id: 90,
          seq_id: 90,
          topic_id: 'p2p_1_2',
          from_uid: 1,
          type: 'text',
          content: '实现并测试这项功能',
          created_at: '2026-07-20T10:00:00Z',
        },
        {
          id: 91,
          seq_id: 91,
          topic_id: 'p2p_1_2',
          from_uid: 2,
          type: 'tool_use',
          content: 'update_plan',
          metadata: {
            id: 'plan-1',
            turn_id: 'retry-1',
            input: {
              steps: [
                { status: 'in_progress', step: '实现功能' },
                { status: 'pending', step: '运行测试' },
              ],
            },
          },
          created_at: '2026-07-20T10:00:01Z',
        },
        {
          id: 92,
          seq_id: 92,
          topic_id: 'p2p_1_2',
          from_uid: 2,
          type: 'tool_result',
          content: '计划已更新：0/2 已完成',
          metadata: { tool_use_id: 'plan-1', turn_id: 'retry-1' },
          created_at: '2026-07-20T10:00:02Z',
        },
        {
          id: 93,
          seq_id: 93,
          topic_id: 'p2p_1_2',
          from_uid: 2,
          role: 'assistant',
          type: 'text',
          content: '功能和测试已经完成。',
          created_at: '2026-07-20T10:00:03Z',
        },
        {
          id: 94,
          seq_id: 94,
          topic_id: 'p2p_1_2',
          from_uid: 2,
          type: 'tool_use',
          content: 'update_plan',
          metadata: {
            id: 'plan-2',
            turn_id: 'retry-2',
            input: {
              steps: [
                { status: 'completed', step: '实现功能' },
                { status: 'completed', step: '运行测试' },
              ],
            },
          },
          created_at: '2026-07-20T10:00:04Z',
        },
        {
          id: 95,
          seq_id: 95,
          topic_id: 'p2p_1_2',
          from_uid: 2,
          type: 'tool_result',
          content: '计划已更新：2/2 已完成',
          metadata: { tool_use_id: 'plan-2', turn_id: 'retry-2' },
          created_at: '2026-07-20T10:00:05Z',
        },
      ],
    });

    await mountTopic(root, 'p2p_1_2');
    await act(async () => {
      await flushPromises();
    });

    const workingGroups = container.querySelectorAll('.oc-working-group');
    expect(workingGroups).toHaveLength(1);
    const workingMessage = workingGroups[0].querySelector('[data-working-only="true"]');
    expect(workingMessage?.dataset.workingCount).toBe('4');
    expect(workingMessage?.dataset.workingMessageIds).toBe('91,92,94,95');
    expect(container.querySelector('.mock-chat-message[data-message-id="93"]')).not.toBeNull();
  });

  it('orders one Agent turn as working trace, delivery files, then the final result', async () => {
    mockTutorialAgentPeer();
    api.getMessages.mockResolvedValueOnce({
      messages: [
        {
          id: 100,
          topic_id: 'p2p_1_2',
          from_uid: 1,
          type: 'text',
          content: '完成并打包游戏',
          created_at: '2026-07-20T11:00:00Z',
        },
        {
          id: 101,
          topic_id: 'p2p_1_2',
          from_uid: 2,
          type: 'tool_use',
          content: 'update_plan',
          metadata: {
            id: 'plan-1',
            input: {
              steps: [
                { status: 'in_progress', step: '实现游戏' },
                { status: 'pending', step: '打包交付' },
              ],
            },
          },
          created_at: '2026-07-20T11:00:01Z',
        },
        {
          id: 102,
          topic_id: 'p2p_1_2',
          from_uid: 2,
          type: 'tool_result',
          content: '计划已更新：0/2 已完成',
          metadata: { tool_use_id: 'plan-1' },
          created_at: '2026-07-20T11:00:02Z',
        },
        {
          id: 103,
          topic_id: 'p2p_1_2',
          from_uid: 2,
          role: 'assistant',
          type: 'text',
          content: 'AI文本:检查和压缩包验收都通过。',
          created_at: '2026-07-20T11:00:03Z',
        },
        {
          id: 104,
          topic_id: 'p2p_1_2',
          from_uid: 2,
          role: 'assistant',
          type: 'text',
          content: JSON.stringify({
            type: 'file',
            payload: {
              name: 'game.zip',
              url: '/uploads/files/game.zip',
              size: 4096,
              mime_type: 'application/zip',
            },
          }),
          created_at: '2026-07-20T11:00:04Z',
        },
        {
          id: 105,
          topic_id: 'p2p_1_2',
          from_uid: 2,
          role: 'assistant',
          type: 'text',
          content: '更新版现在发送，旧存档仍可继续使用。',
          created_at: '2026-07-20T11:00:05Z',
        },
        {
          id: 106,
          topic_id: 'p2p_1_2',
          from_uid: 2,
          type: 'tool_use',
          content: 'update_plan',
          metadata: {
            id: 'plan-2',
            input: {
              steps: [
                { status: 'completed', step: '实现游戏' },
                { status: 'completed', step: '打包交付' },
              ],
            },
          },
          created_at: '2026-07-20T11:00:06Z',
        },
        {
          id: 107,
          topic_id: 'p2p_1_2',
          from_uid: 2,
          type: 'tool_result',
          content: '计划已更新：2/2 已完成',
          metadata: { tool_use_id: 'plan-2' },
          created_at: '2026-07-20T11:00:07Z',
        },
      ],
    });

    await mountTopic(root, 'p2p_1_2');
    await act(async () => {
      await flushPromises();
    });

    const orderedIDs = Array.from(container.querySelectorAll('.mock-chat-message'))
      .map((message) => message.dataset.messageId);
    expect(orderedIDs).toEqual(['100', '101', '105']);
    const workingGroups = container.querySelectorAll('.oc-working-group');
    expect(workingGroups).toHaveLength(1);
    const workingMessage = workingGroups[0].querySelector('[data-working-only="true"]');
    expect(workingMessage?.dataset.workingMessageIds).toBe('101,102,103,106,107');
    expect(workingMessage?.dataset.workingComplete).toBe('true');
    const mergedOutput = container.querySelector('[data-message-id="105"]');
    expect(mergedOutput?.dataset.artifactsFirst).toBe('true');
    expect(mergedOutput?.dataset.consecutive).toBe('true');
    expect(mergedOutput?.dataset.contentBlockCount).toBe('2');
    expect(mergedOutput?.dataset.textBlockRoles).toBe('result');
    expect(mergedOutput?.dataset.textBlockTexts).toBe('更新版现在发送，旧存档仍可继续使用。');
    expect(mergedOutput?.dataset.messageContent).toBe(
      '更新版现在发送，旧存档仍可继续使用。',
    );

  });

  it('marks a tool trace complete when the same turn has a final reply without a plan', async () => {
    mockTutorialAgentPeer();
    api.getMessages.mockResolvedValueOnce({
      messages: [
        {
          id: 120,
          topic_id: 'p2p_1_2',
          from_uid: 1,
          type: 'text',
          content: 'Run the check',
          created_at: '2026-07-20T11:30:00Z',
        },
        {
          id: 121,
          topic_id: 'p2p_1_2',
          from_uid: 2,
          type: 'tool_use',
          content: 'execute_shell',
          metadata: { id: 'shell-1', turn_id: 'turn-no-plan' },
          created_at: '2026-07-20T11:30:01Z',
        },
        {
          id: 122,
          topic_id: 'p2p_1_2',
          from_uid: 2,
          type: 'tool_result',
          content: 'All checks passed',
          metadata: { tool_use_id: 'shell-1', turn_id: 'turn-no-plan' },
          created_at: '2026-07-20T11:30:02Z',
        },
        {
          id: 123,
          topic_id: 'p2p_1_2',
          from_uid: 2,
          role: 'assistant',
          type: 'text',
          content: 'The check passed.',
          metadata: { turn_id: 'turn-no-plan' },
          created_at: '2026-07-20T11:30:03Z',
        },
      ],
    });

    await mountTopic(root, 'p2p_1_2');
    await act(async () => {
      await flushPromises();
    });

    const workingMessage = container.querySelector('[data-working-only="true"]');
    expect(workingMessage?.dataset.workingMessageIds).toBe('121,122');
    expect(workingMessage?.dataset.workingComplete).toBe('true');
    expect(container.querySelector('[data-message-id="123"]')?.dataset.messageContent)
      .toBe('The check passed.');
  });

  it('keeps a group Agent turn unified while group member details are unavailable', async () => {
    api.getAgents.mockResolvedValueOnce({
      agents: [{
        uid: 535,
        username: 'iteration-agent',
        display_name: '自迭代测试',
        avatar_url: '/avatars/iteration-agent.png',
        is_bot: true,
      }],
    });
    api.getGroupInfo.mockRejectedValueOnce(new Error('group details unavailable'));
    api.getMessages.mockResolvedValueOnce({
      messages: [
        {
          id: 130,
          topic_id: 'grp_53',
          from_uid: 1,
          type: 'text',
          content: '制作最近选美赛事图文简报',
          created_at: '2026-07-20T12:00:00Z',
        },
        {
          id: 131,
          topic_id: 'grp_53',
          from_uid: 535,
          type: 'tool_use',
          content: 'execute_shell',
          metadata: { id: 'tool-131' },
          created_at: '2026-07-20T12:00:01Z',
        },
        {
          id: 132,
          topic_id: 'grp_53',
          from_uid: 535,
          type: 'tool_result',
          content: 'search complete',
          metadata: { tool_use_id: 'tool-131' },
          created_at: '2026-07-20T12:00:02Z',
        },
        {
          id: 133,
          topic_id: 'grp_53',
          from_uid: 535,
          type: 'text',
          content: '已确认当前最近日期的赛事。',
          created_at: '2026-07-20T12:00:03Z',
        },
        {
          id: 134,
          topic_id: 'grp_53',
          from_uid: 535,
          type: 'tool_use',
          content: 'read_file',
          metadata: { id: 'tool-134' },
          created_at: '2026-07-20T12:00:04Z',
        },
        {
          id: 135,
          topic_id: 'grp_53',
          from_uid: 535,
          type: 'tool_result',
          content: 'file ready',
          metadata: { tool_use_id: 'tool-134' },
          created_at: '2026-07-20T12:00:05Z',
        },
        {
          id: 136,
          topic_id: 'grp_53',
          from_uid: 535,
          type: 'text',
          content: {
            type: 'file',
            payload: {
              name: '最近的选美大赛图文简报.pdf',
              url: '/uploads/files/pageant.pdf',
              size: 6_300_000,
              mime_type: 'application/pdf',
            },
          },
          created_at: '2026-07-20T12:00:06Z',
        },
        {
          id: 137,
          topic_id: 'grp_53',
          from_uid: 535,
          type: 'text',
          content: '图文简报已发。',
          created_at: '2026-07-20T12:00:07Z',
        },
      ],
    });

    await mountTopic(root, 'grp_53', { isGroup: true, groupId: 53 });
    await act(async () => {
      await flushPromises();
    });

    const renderedMessages = Array.from(container.querySelectorAll('.mock-chat-message'));
    expect(renderedMessages.map((message) => message.dataset.messageId))
      .toEqual(['130', '131', '137']);
    const workingMessage = container.querySelector('[data-working-only="true"]');
    expect(workingMessage?.dataset.workingMessageIds).toBe('131,132,133,134,135');
    expect(workingMessage?.dataset.senderName).toBe('自迭代测试');
    expect(workingMessage?.dataset.senderIsBot).toBe('true');
    const mergedOutput = container.querySelector('[data-message-id="137"]');
    expect(mergedOutput?.dataset.artifactsFirst).toBe('true');
    expect(mergedOutput?.dataset.textBlockRoles).toBe('result');
    expect(mergedOutput?.dataset.messageContent).toBe('图文简报已发。');
    expect(mergedOutput?.dataset.senderName).toBe('自迭代测试');
  });

  it('uses the latest delivery event as the single reply timestamp source', async () => {
    mockTutorialAgentPeer();
    api.getMessages.mockResolvedValueOnce({
      messages: [
        {
          id: 120,
          topic_id: 'p2p_1_2',
          from_uid: 1,
          type: 'text',
          content: '导出文件',
          created_at: '2026-07-20T11:02:00Z',
        },
        {
          id: 121,
          topic_id: 'p2p_1_2',
          from_uid: 2,
          type: 'tool_use',
          content: 'write_file',
          metadata: { id: 'write-121' },
          created_at: '2026-07-20T11:02:01Z',
        },
        {
          id: 122,
          topic_id: 'p2p_1_2',
          from_uid: 2,
          role: 'assistant',
          type: 'text',
          content: '文件已经生成。',
          created_at: '2026-07-20T11:02:02Z',
        },
        {
          id: 123,
          topic_id: 'p2p_1_2',
          from_uid: 2,
          role: 'assistant',
          type: 'text',
          content: {
            type: 'file',
            payload: {
              name: 'report.zip',
              url: '/uploads/files/report.zip',
              size: 2048,
              mime_type: 'application/zip',
            },
          },
          created_at: '2026-07-20T11:02:03Z',
        },
      ],
    });

    await mountTopic(root, 'p2p_1_2');
    await act(async () => {
      await flushPromises();
    });

    const orderedIDs = Array.from(container.querySelectorAll('.mock-chat-message'))
      .map((message) => message.dataset.messageId);
    expect(orderedIDs).toEqual(['120', '121', '123']);
    const mergedOutput = container.querySelector('[data-message-id="123"]');
    expect(mergedOutput?.dataset.artifactsFirst).toBe('true');
    expect(mergedOutput?.dataset.messageContent).toBe('文件已经生成。');
    expect(mergedOutput?.dataset.contentBlockCount).toBe('2');
    expect(mergedOutput?.dataset.textBlockRoles).toBe('result');
  });

  it('keeps consecutive Agent runs with different turn IDs as separate replies', async () => {
    mockTutorialAgentPeer();
    api.getMessages.mockResolvedValueOnce({
      messages: [
        {
          id: 108,
          topic_id: 'p2p_1_2',
          from_uid: 2,
          type: 'tool_use',
          content: 'execute_shell',
          metadata: { id: 'tool-a', turn_id: 'turn-a', input: { command: 'first' } },
          created_at: '2026-07-20T11:01:00Z',
        },
        {
          id: 109,
          topic_id: 'p2p_1_2',
          from_uid: 2,
          role: 'assistant',
          type: 'text',
          content: 'First run finished.',
          metadata: { turn_id: 'turn-a' },
          created_at: '2026-07-20T11:01:01Z',
        },
        {
          id: 110,
          topic_id: 'p2p_1_2',
          from_uid: 2,
          type: 'tool_use',
          content: 'execute_shell',
          metadata: { id: 'tool-b', turn_id: 'turn-b', input: { command: 'second' } },
          created_at: '2026-07-20T11:01:02Z',
        },
        {
          id: 111,
          topic_id: 'p2p_1_2',
          from_uid: 2,
          role: 'assistant',
          type: 'text',
          content: 'Second run finished.',
          metadata: { turn_id: 'turn-b' },
          created_at: '2026-07-20T11:01:03Z',
        },
      ],
    });

    await mountTopic(root, 'p2p_1_2');
    await act(async () => {
      await flushPromises();
    });

    expect(container.querySelectorAll('.oc-working-group')).toHaveLength(2);
    expect(Array.from(container.querySelectorAll('.mock-chat-message')).map(
      (message) => message.dataset.messageId,
    )).toEqual(['108', '109', '110', '111']);
    expect(container.querySelector('[data-message-id="109"]')?.dataset.messageContent)
      .toBe('First run finished.');
    expect(container.querySelector('[data-message-id="111"]')?.dataset.messageContent)
      .toBe('Second run finished.');
  });

  it('keeps separate assistant replies apart outside the fallback merge window', async () => {
    mockTutorialAgentPeer();
    api.getMessages.mockResolvedValueOnce({
      messages: [
        {
          id: 71,
          topic_id: 'p2p_1_2',
          from_uid: 2,
          role: 'assistant',
          type: 'text',
          content: 'First independent reply.',
          created_at: '2026-07-20T09:24:00Z',
        },
        {
          id: 72,
          topic_id: 'p2p_1_2',
          from_uid: 2,
          role: 'assistant',
          type: 'text',
          content: 'Second independent reply.',
          created_at: '2026-07-20T09:26:00Z',
        },
      ],
    });

    await mountTopic(root, 'p2p_1_2');
    await act(async () => {
      await flushPromises();
    });

    expect(container.querySelectorAll('.mock-chat-message')).toHaveLength(2);
  });

  it('does not merge adjacent messages from a human contact', async () => {
    api.getFriends.mockResolvedValueOnce({
      friends: [{ id: 2, username: 'alice', display_name: 'Alice', account_type: 'human' }],
    });
    api.getMessages.mockResolvedValueOnce({
      messages: [
        {
          id: 81,
          topic_id: 'p2p_1_2',
          from_uid: 2,
          type: 'text',
          content: 'First human message.',
          created_at: '2026-07-20T09:24:00Z',
        },
        {
          id: 82,
          topic_id: 'p2p_1_2',
          from_uid: 2,
          type: 'text',
          content: 'Second human message.',
          created_at: '2026-07-20T09:24:10Z',
        },
      ],
    });

    await mountTopic(root, 'p2p_1_2');
    await act(async () => {
      await flushPromises();
    });

    expect(container.querySelectorAll('.mock-chat-message')).toHaveLength(2);
  });

  it('renders a question navigator and scrolls to the selected user instruction', async () => {
    api.getMessages.mockResolvedValueOnce({
      messages: [
        { id: 101, seq_id: 101, topic_id: 'p2p_1_2', from_uid: 1, type: 'text', content: 'First question' },
        { id: 102, seq_id: 102, topic_id: 'p2p_1_2', from_uid: 2, type: 'text', content: 'First answer' },
        { id: 103, seq_id: 103, topic_id: 'p2p_1_2', from_uid: 1, type: 'text', content: 'Second question' },
        { id: 104, seq_id: 104, topic_id: 'p2p_1_2', from_uid: 2, type: 'text', content: 'Second answer' },
        { id: 105, seq_id: 105, topic_id: 'p2p_1_2', from_uid: 1, type: 'text', content: 'Third question' },
      ],
    });

    await mountTopic(root, 'p2p_1_2');
    await act(async () => {
      await flushPromises();
    });

    const navigator = container.querySelector('[aria-label="对话问题导航"]');
    expect(navigator).not.toBeNull();
    const questionButtons = navigator.querySelectorAll('.cc-question-navigator-item');
    expect(questionButtons).toHaveLength(3);
    expect(questionButtons[1].getAttribute('title')).toContain('Second question');
    const questionListButtons = navigator.querySelectorAll('.cc-question-list-item');
    expect(questionListButtons).toHaveLength(3);
    expect(questionListButtons[1].textContent).toContain('Second question');
    expect(navigator.querySelector('.cc-question-navigator-heading')).toBeNull();
    expect(navigator.querySelector('.cc-question-navigator-dots').nextElementSibling)
      .toBe(navigator.querySelector('.cc-question-navigator-panel'));

    const secondQuestion = container.querySelector('[data-conversation-question="103"]');
    secondQuestion.scrollIntoView = vi.fn();
    await act(async () => {
      Simulate.click(questionListButtons[1]);
    });

    expect(secondQuestion.scrollIntoView).toHaveBeenCalledWith({ behavior: 'smooth', block: 'start' });
    expect(questionButtons[1].getAttribute('aria-current')).toBe('true');
    expect(questionListButtons[1].getAttribute('aria-current')).toBe('true');

    const timeline = container.querySelector('.v3-timeline');
    const firstQuestion = container.querySelector('[data-conversation-question="101"]');
    const thirdQuestion = container.querySelector('[data-conversation-question="105"]');
    firstQuestion.scrollIntoView = vi.fn();
    timeline.getBoundingClientRect = vi.fn(() => ({ top: 0, height: 800 }));
    firstQuestion.getBoundingClientRect = vi.fn(() => ({ top: -140 }));
    secondQuestion.getBoundingClientRect = vi.fn(() => ({ top: 200 }));
    thirdQuestion.getBoundingClientRect = vi.fn(() => ({ top: 600 }));
    Object.defineProperties(timeline, {
      scrollHeight: { configurable: true, value: 1040 },
      clientHeight: { configurable: true, value: 800 },
      scrollTop: { configurable: true, writable: true, value: 240 },
    });

    await act(async () => {
      Simulate.click(questionButtons[0]);
      Simulate.scroll(timeline);
    });

    expect(firstQuestion.scrollIntoView).toHaveBeenCalledWith({ behavior: 'smooth', block: 'start' });
    expect(questionButtons[0].getAttribute('aria-current')).toBe('true');
    expect(questionButtons[2].hasAttribute('aria-current')).toBe(false);

    await act(async () => {
      Simulate.wheel(timeline);
      Simulate.scroll(timeline);
    });
    expect(questionButtons[2].getAttribute('aria-current')).toBe('true');

    Object.defineProperties(timeline, {
      scrollHeight: { configurable: true, value: 800 },
      clientHeight: { configurable: true, value: 800 },
      scrollTop: { configurable: true, writable: true, value: 0 },
    });
    firstQuestion.getBoundingClientRect = vi.fn(() => ({ top: 100 }));
    secondQuestion.getBoundingClientRect = vi.fn(() => ({ top: 360 }));
    thirdQuestion.getBoundingClientRect = vi.fn(() => ({ top: 640 }));

    await act(async () => {
      Simulate.click(questionButtons[0]);
      Simulate.scroll(timeline);
    });
    expect(questionButtons[0].getAttribute('aria-current')).toBe('true');
  });

  it('tracks the reading position with one IntersectionObserver instead of scanning every anchor on scroll', async () => {
    let observerCallback;
    const observe = vi.fn();
    const disconnect = vi.fn();
    window.IntersectionObserver = vi.fn(function IntersectionObserverMock(callback) {
      observerCallback = callback;
      return { observe, disconnect };
    });
    api.getMessages.mockResolvedValueOnce({
      messages: [
        { id: 201, seq_id: 201, topic_id: 'p2p_1_2', from_uid: 1, type: 'text', content: 'First observed question' },
        { id: 202, seq_id: 202, topic_id: 'p2p_1_2', from_uid: 2, type: 'text', content: 'First observed answer' },
        { id: 203, seq_id: 203, topic_id: 'p2p_1_2', from_uid: 1, type: 'text', content: 'Second observed question' },
      ],
    });

    await mountTopic(root, 'p2p_1_2');
    await act(async () => {
      await flushPromises();
    });

    const firstQuestion = container.querySelector('[data-conversation-question="201"]');
    const secondQuestion = container.querySelector('[data-conversation-question="203"]');
    const navigator = container.querySelector('[aria-label="对话问题导航"]');
    const buttons = navigator.querySelectorAll('.cc-question-navigator-item');
    expect(window.IntersectionObserver).toHaveBeenCalledTimes(1);
    expect(observe).toHaveBeenCalledWith(firstQuestion);
    expect(observe).toHaveBeenCalledWith(secondQuestion);

    await act(async () => {
      observerCallback([
        { target: firstQuestion, isIntersecting: false, boundingClientRect: { top: -40 } },
        { target: secondQuestion, isIntersecting: true, boundingClientRect: { top: 180 } },
      ]);
    });

    expect(buttons[1].getAttribute('aria-current')).toBe('true');
  });

  it('indexes older questions only on intent and fetches one nearby page for a jump', async () => {
    const originalScrollHeight = Object.getOwnPropertyDescriptor(
      window.HTMLElement.prototype,
      'scrollHeight',
    );
    const originalClientHeight = Object.getOwnPropertyDescriptor(
      window.HTMLElement.prototype,
      'clientHeight',
    );
    const originalScrollTop = Object.getOwnPropertyDescriptor(
      window.HTMLElement.prototype,
      'scrollTop',
    );
    Object.defineProperty(window.HTMLElement.prototype, 'scrollHeight', {
      configurable: true,
      get() {
        return this.classList?.contains('v3-timeline') ? 1000 : 0;
      },
    });
    Object.defineProperty(window.HTMLElement.prototype, 'clientHeight', {
      configurable: true,
      get() {
        return this.classList?.contains('v3-timeline') ? 500 : 0;
      },
    });
    Object.defineProperty(window.HTMLElement.prototype, 'scrollTop', {
      configurable: true,
      get() {
        return this.classList?.contains('v3-timeline') ? 500 : 0;
      },
      set() {},
    });
    const latestMessages = Array.from({ length: 50 }, (_, index) => ({
      id: 101 + index,
      seq_id: 101 + index,
      topic_id: 'p2p_1_2',
      from_uid: index === 0 || index === 49 ? 1 : 2,
      type: 'text',
      content: index === 0
        ? 'Recent question one'
        : (index === 49 ? 'Recent question two' : `Recent answer ${index}`),
    }));
    const olderMessages = [
      {
        id: 1,
        seq_id: 1,
        topic_id: 'p2p_1_2',
        from_uid: 1,
        type: 'text',
        content: 'Oldest question',
      },
      {
        id: 2,
        seq_id: 2,
        topic_id: 'p2p_1_2',
        from_uid: 2,
        type: 'text',
        content: 'Oldest answer',
      },
    ];
    api.getMessages
      .mockResolvedValueOnce({
        messages: latestMessages,
        has_more: true,
        next_before_id: 101,
      })
      .mockResolvedValueOnce({
        messages: olderMessages,
        has_more: false,
        next_before_id: 1,
      })
      .mockResolvedValueOnce({
        messages: olderMessages,
        has_more: false,
        next_before_id: 1,
      });

    try {
      await mountTopic(root, 'p2p_1_2');
      await act(async () => {
        await flushPromises(16);
      });

      expect(api.getMessages).toHaveBeenCalledWith(
        'p2p_1_2',
        50,
        0,
        true,
        0,
        expect.objectContaining({ signal: expect.any(AbortSignal), timeoutMs: 15000 }),
      );
      expect(api.getMessages.mock.calls.some(
        ([targetTopic, limit, offset, latest, beforeId]) => (
          targetTopic === 'p2p_1_2'
          && limit === 500
          && offset === 50
          && latest === true
          && beforeId === 101
        ),
      )).toBe(false);

      const navigator = container.querySelector('.cc-question-navigator');
      expect(navigator).not.toBeNull();
      await act(async () => {
        Simulate.mouseEnter(navigator);
        await flushPromises();
      });
      expect(api.getMessages).toHaveBeenCalledWith(
        'p2p_1_2',
        500,
        50,
        true,
        101,
        expect.objectContaining({ signal: expect.any(AbortSignal), timeoutMs: 15000 }),
      );

      const questionListButtons = Array.from(
        navigator.querySelectorAll('.cc-question-list-item'),
      );
      const oldestQuestionButton = questionListButtons.find(
        (button) => button.textContent.includes('Oldest question'),
      );
      expect(oldestQuestionButton).not.toBeNull();
      expect(container.querySelector('[data-conversation-question="1"]')).toBeNull();

      await act(async () => {
        Simulate.click(oldestQuestionButton);
        await flushPromises();
      });

      expect(api.getMessages).toHaveBeenCalledWith(
        'p2p_1_2',
        50,
        0,
        true,
        2,
        expect.objectContaining({ signal: expect.any(AbortSignal), timeoutMs: 15000 }),
      );
      const oldestQuestion = container.querySelector('[data-conversation-question="1"]');
      expect(oldestQuestion).not.toBeNull();
      expect(window.HTMLElement.prototype.scrollIntoView)
        .toHaveBeenCalledWith({ behavior: 'smooth', block: 'start' });

      const indexRequestCount = api.getMessages.mock.calls
        .filter(([, limit]) => limit === 500)
        .length;
      api.getMessages
        .mockResolvedValueOnce({
          messages: [{ id: 201, topic_id: 'p2p_1_3', from_uid: 3, type: 'text', content: 'Topic B' }],
          has_more: false,
        })
        .mockResolvedValueOnce({
          messages: latestMessages,
          has_more: true,
          next_before_id: 101,
        });
      await mountTopic(root, 'p2p_1_3');
      await act(async () => {
        await flushPromises();
      });
      await mountTopic(root, 'p2p_1_2');
      await act(async () => {
        await flushPromises();
      });

      const restoredNavigator = container.querySelector('.cc-question-navigator');
      expect(restoredNavigator.textContent).toContain('Oldest question');
      await act(async () => {
        Simulate.mouseEnter(restoredNavigator);
        await flushPromises();
      });
      expect(api.getMessages.mock.calls.filter(([, limit]) => limit === 500)).toHaveLength(
        indexRequestCount,
      );
    } finally {
      if (originalScrollHeight) {
        Object.defineProperty(window.HTMLElement.prototype, 'scrollHeight', originalScrollHeight);
      } else {
        delete window.HTMLElement.prototype.scrollHeight;
      }
      if (originalClientHeight) {
        Object.defineProperty(window.HTMLElement.prototype, 'clientHeight', originalClientHeight);
      } else {
        delete window.HTMLElement.prototype.clientHeight;
      }
      if (originalScrollTop) {
        Object.defineProperty(window.HTMLElement.prototype, 'scrollTop', originalScrollTop);
      } else {
        delete window.HTMLElement.prototype.scrollTop;
      }
    }
  });

  it('regenerates a bot reply by resending the preceding user task', async () => {
    api.getMessages.mockResolvedValueOnce({
      messages: [
        {
          id: 70,
          seq_id: 70,
          topic_id: 'p2p_1_2',
          from_uid: 1,
          type: 'text',
          msg_type: 'text',
          content: '检查这段代码',
          created_at: '2026-06-09T00:00:00Z',
        },
        {
          id: 71,
          seq_id: 71,
          topic_id: 'p2p_1_2',
          from_uid: 2,
          role: 'assistant',
          type: 'text',
          msg_type: 'text',
          content: '这是第一次检查结果',
          created_at: '2026-06-09T00:01:00Z',
        },
      ],
    });

    await mountTopic(root, 'p2p_1_2');
    await act(async () => {
      await flushPromises();
    });

    const regenerateButton = container.querySelector('.mock-regenerate-message[data-message-id="71"]');
    expect(regenerateButton).not.toBeNull();
    await act(async () => {
      Simulate.click(regenerateButton);
      await flushPromises();
    });

    expect(api.sendMessage).toHaveBeenCalledWith('p2p_1_2', '检查这段代码', undefined);
  });

  it('does not expose regenerate for bot replies in a standard group', async () => {
    api.getMessages.mockResolvedValueOnce({
      messages: [
        { id: 70, seq_id: 70, topic_id: 'grp_9', from_uid: 1, type: 'text', content: '检查这段代码' },
        { id: 71, seq_id: 71, topic_id: 'grp_9', from_uid: 2, type: 'text', content: '第一个 Bot 的回复' },
      ],
    });
    api.getGroupInfo.mockResolvedValueOnce({
      group: { id: 9, name: '多 Bot 群', kind: 'standard' },
      members: [
        { user_id: 1, display_name: 'Me', is_bot: false },
        { user_id: 2, display_name: 'Bot A', is_bot: true },
        { user_id: 3, display_name: 'Bot B', is_bot: true },
      ],
    });

    await mountTopic(root, 'grp_9', { isGroup: true, groupId: 9 });
    await act(async () => {
      await flushPromises();
    });

    expect(container.querySelector('.mock-regenerate-message[data-message-id="71"]')).toBeNull();
  });

  it('keeps regenerate available for a single-Agent task', async () => {
    api.getMessages.mockResolvedValueOnce({
      messages: [
        { id: 80, seq_id: 80, topic_id: 'grp_10', from_uid: 1, type: 'text', content: '生成发布说明' },
        { id: 81, seq_id: 81, topic_id: 'grp_10', from_uid: 2, type: 'text', content: '初版发布说明' },
      ],
    });
    api.getGroupInfo.mockResolvedValueOnce({
      group: { id: 10, name: '发布说明任务', kind: 'agent_task', is_agent_task: true },
      members: [
        { user_id: 1, display_name: 'Me', is_bot: false },
        { user_id: 2, display_name: 'Writer', is_bot: true },
      ],
    });

    await mountTopic(root, 'grp_10', { isGroup: true, groupId: 10 });
    await act(async () => {
      await flushPromises();
    });

    expect(container.querySelector('.mock-regenerate-message[data-message-id="81"]')).not.toBeNull();
  });

  it('does not send from the chat composer for an IME Enter reported as keyCode 229', async () => {
    await mountTopic(root, 'p2p_1_2');
    const textarea = container.querySelector('textarea.v3-composer-input');

    await act(async () => {
      typeDraft(textarea, '正在输入中文');
      Simulate.keyDown(textarea, { key: 'Enter', keyCode: 229, which: 229, shiftKey: false });
      await flushPromises();
    });

    expect(api.sendMessage).not.toHaveBeenCalled();
    expect(textarea.value).toBe('正在输入中文');
  });

  it('keeps the composer usable and sends a follow-up while the agent is working', async () => {
    api.getMessages.mockResolvedValueOnce({
      messages: [
        { id: 40, from_uid: 1, type: 'text', content: '先分析一下', created_at: '2026-07-17T01:00:00Z' },
        { id: 41, from_uid: 2, type: 'thinking', content: '正在分析', created_at: '2026-07-17T01:00:01Z' },
      ],
    });

    await mountTopic(root, 'p2p_1_2');
    await act(async () => {
      wsHandler({ info: { topic: 'p2p_1_2', what: 'kp', from: 'usr2' } });
    });

    const textarea = container.querySelector('textarea.v3-composer-input');
    expect(textarea.disabled).toBe(false);
    expect(container.querySelector('button[aria-label="停止当前工作"]')).not.toBeNull();

    await act(async () => {
      typeDraft(textarea, '再补充一个条件');
    });
    expect(container.querySelector('button[aria-label="停止当前工作"]')).toBeNull();
    expect(container.querySelector('button[aria-label="发送"]')).not.toBeNull();

    await act(async () => {
      Simulate.click(container.querySelector('button[aria-label="发送"]'));
      await flushPromises();
    });

    expect(api.sendMessage).toHaveBeenCalledWith('p2p_1_2', '再补充一个条件', undefined);
    expect(container.querySelector('button[aria-label="停止当前工作"]')).not.toBeNull();
  });

  it('returns the composer to send mode after a stop request is delivered', async () => {
    api.getMessages.mockResolvedValueOnce({
      messages: [
        { id: 50, from_uid: 1, type: 'text', content: '执行长任务', created_at: '2026-07-17T02:00:00Z' },
        { id: 51, from_uid: 2, type: 'tool_use', content: '执行工具', created_at: '2026-07-17T02:00:01Z' },
      ],
    });
    wsSendStreamCancel.mockResolvedValueOnce(1);

    await mountTopic(root, 'p2p_1_2');
    await act(async () => {
      wsHandler({ info: { topic: 'p2p_1_2', what: 'kp', from: 'usr2' } });
    });

    await act(async () => {
      Simulate.click(container.querySelector('button[aria-label="停止当前工作"]'));
      await flushPromises();
    });

    expect(wsSendStreamCancel).toHaveBeenCalledWith('p2p_1_2', 2);
    expect(container.querySelector('button[aria-label="停止当前工作"]')).toBeNull();
    expect(container.querySelector('button[aria-label="发送"]')).not.toBeNull();
    expect(container.querySelector('textarea.v3-composer-input').disabled).toBe(false);
  });

  it('does not let a group member stop an agent response requested by someone else', async () => {
    api.getGroupInfo.mockResolvedValueOnce({
      group: { id: 80, name: 'Agent Room', has_bot: true },
      members: [
        { user_id: 1, display_name: 'Me', is_bot: false },
        { user_id: 7, display_name: 'Alice', is_bot: false },
        { user_id: 42, display_name: 'Saturday', is_bot: true },
      ],
    });
    api.getMessages.mockResolvedValueOnce({
      messages: [
        { id: 52, from_uid: 7, type: 'text', content: '@Saturday 帮我分析', created_at: '2026-07-17T02:00:00Z' },
        { id: 53, from_uid: 42, type: 'thinking', content: '正在分析', created_at: '2026-07-17T02:00:01Z' },
      ],
    });

    await mountTopic(root, 'grp_80', { isGroup: true, groupId: 80 });
    await act(async () => {
      await flushPromises();
      wsHandler({ info: { topic: 'grp_80', what: 'kp', from: 'usr42' } });
    });

    expect(container.querySelector('button[aria-label="停止当前工作"]')).toBeNull();
    expect(container.querySelector('.v3-live-input-status')?.textContent).toContain('CatsCo 正在回复其他成员');
    expect(wsSendStreamCancel).not.toHaveBeenCalled();
  });

  it('lets the requesting group member stop their own agent response', async () => {
    api.getGroupInfo.mockResolvedValueOnce({
      group: { id: 81, name: 'Agent Room', has_bot: true },
      members: [
        { user_id: 1, display_name: 'Me', is_bot: false },
        { user_id: 7, display_name: 'Alice', is_bot: false },
        { user_id: 42, display_name: 'Saturday', is_bot: true },
      ],
    });
    api.getMessages.mockResolvedValueOnce({
      messages: [
        { id: 54, from_uid: 1, type: 'text', content: '@Saturday 帮我分析', created_at: '2026-07-17T02:10:00Z' },
        { id: 55, from_uid: 42, type: 'thinking', content: '正在分析', created_at: '2026-07-17T02:10:01Z' },
      ],
    });

    await mountTopic(root, 'grp_81', { isGroup: true, groupId: 81 });
    await act(async () => {
      await flushPromises();
      wsHandler({ info: { topic: 'grp_81', what: 'kp', from: 'usr42' } });
    });

    expect(container.querySelector('button[aria-label="停止当前工作"]')).not.toBeNull();
    expect(container.querySelector('.v3-live-input-status')?.textContent).toContain('可点击红色按钮停止');
  });

  it('keeps stop available in a one-user one-agent task when history has no initiator message', async () => {
    api.getGroupInfo.mockResolvedValueOnce({
      group: { id: 82, name: 'Solo Agent Task', has_bot: true, is_agent_task: true },
      members: [
        { user_id: 1, display_name: 'Me', is_bot: false },
        { user_id: 42, display_name: 'Saturday', is_bot: true },
      ],
    });
    api.getMessages.mockResolvedValueOnce({
      messages: [
        { id: 56, from_uid: 42, type: 'thinking', content: '正在处理', created_at: '2026-07-17T02:20:01Z' },
      ],
    });

    await mountTopic(root, 'grp_82', { isGroup: true, groupId: 82 });
    await act(async () => {
      await flushPromises();
      wsHandler({ info: { topic: 'grp_82', what: 'kp', from: 'usr42' } });
    });

    expect(container.querySelector('button[aria-label="停止当前工作"]')).not.toBeNull();
    expect(container.querySelector('.v3-live-input-status')?.textContent).toContain('可点击红色按钮停止');
  });

  it('removes stop access when a third member joins an active two-member task', async () => {
    api.getGroupInfo
      .mockResolvedValueOnce({
        group: { id: 83, name: 'Solo Agent Task', has_bot: true, is_agent_task: true },
        members: [
          { user_id: 1, display_name: 'Me', is_bot: false },
          { user_id: 42, display_name: 'Saturday', is_bot: true },
        ],
      })
      .mockResolvedValueOnce({
        group: { id: 83, name: 'Shared Agent Task', has_bot: true, is_agent_task: true },
        members: [
          { user_id: 1, display_name: 'Me', is_bot: false },
          { user_id: 7, display_name: 'Alice', is_bot: false },
          { user_id: 42, display_name: 'Saturday', is_bot: true },
        ],
      });
    api.getMessages.mockResolvedValueOnce({
      messages: [
        { id: 57, from_uid: 42, type: 'thinking', content: '正在处理', created_at: '2026-07-17T02:30:01Z' },
      ],
    });

    await mountTopic(root, 'grp_83', { isGroup: true, groupId: 83 });
    await act(async () => {
      await flushPromises();
      wsHandler({ info: { topic: 'grp_83', what: 'kp', from: 'usr42' } });
    });

    expect(container.querySelector('button[aria-label="停止当前工作"]')).not.toBeNull();
    expect(container.querySelector('.v3-live-input-status')?.textContent).toContain('可点击红色按钮停止');

    await act(async () => {
      wsHandler({ pres: { topic: 'grp_83', what: 'members_invited' } });
      await flushPromises();
    });

    expect(container.querySelector('button[aria-label="停止当前工作"]')).toBeNull();
    expect(container.querySelector('.v3-live-input-status')?.textContent).toContain('CatsCo 正在回复其他成员');
  });

  it('keeps the stop action available when cancel delivery fails', async () => {
    api.getMessages.mockResolvedValueOnce({
      messages: [
        { id: 60, from_uid: 1, type: 'text', content: '执行长任务', created_at: '2026-07-17T03:00:00Z' },
        { id: 61, from_uid: 2, type: 'thinking', content: '处理中', created_at: '2026-07-17T03:00:01Z' },
      ],
    });
    wsSendStreamCancel.mockRejectedValueOnce(new Error('socket closed'));

    await mountTopic(root, 'p2p_1_2');
    await act(async () => {
      wsHandler({ info: { topic: 'p2p_1_2', what: 'kp', from: 'usr2' } });
    });

    await act(async () => {
      Simulate.click(container.querySelector('button[aria-label="停止当前工作"]'));
      await flushPromises();
    });

    const stopButton = container.querySelector('button[aria-label="停止当前工作"]');
    expect(stopButton).not.toBeNull();
    expect(stopButton.disabled).toBe(false);
  });

  it('drops a stale stop state after the bot activity heartbeat expires', async () => {
    api.getMessages.mockResolvedValueOnce({
      messages: [
        { id: 70, from_uid: 1, type: 'text', content: '执行长任务', created_at: '2026-07-17T04:00:00Z' },
        { id: 71, from_uid: 2, type: 'tool_use', content: '处理中', created_at: '2026-07-17T04:00:01Z' },
      ],
    });
    await mountTopic(root, 'p2p_1_2');
    vi.useFakeTimers();

    await act(async () => {
      wsHandler({ info: { topic: 'p2p_1_2', what: 'kp', from: 'usr2' } });
    });
    expect(container.querySelector('button[aria-label="停止当前工作"]')).not.toBeNull();

    await act(async () => {
      vi.advanceTimersByTime(10_001);
    });
    expect(container.querySelector('button[aria-label="停止当前工作"]')).toBeNull();
    expect(container.querySelector('button[aria-label="发送"]')).not.toBeNull();
  });

  it('lists all bots plus the all-bots option from the current group after typing @', async () => {
    api.getGroupInfo.mockResolvedValueOnce({
      group: { id: 80, name: 'Agent Room' },
      members: [
        { user_id: 1, display_name: 'Me', is_bot: false },
        { user_id: 7, display_name: 'Alice', username: 'alice', is_bot: false },
        { user_id: 42, display_name: 'Saturday', username: 'bot-saturday', is_bot: true },
        { user_id: 43, display_name: 'Wanyu', username: 'catsco-agent-worker1', is_bot: true },
      ],
    });

    await mountTopic(root, 'grp_80', { isGroup: true, groupId: 80 });
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });

    const textarea = container.querySelector('textarea.v3-composer-input');
    await act(async () => {
      typeDraft(textarea, '@');
    });

    const options = [...container.querySelectorAll('.oc-mention-item')];
    expect(options).toHaveLength(3);
    expect(options.map((option) => option.textContent)).toEqual([
      '所有人全部机器人',
      'Saturday@usr42',
      'Wanyu@usr43',
    ]);
    expect(container.querySelector('.oc-mention-picker')?.textContent).not.toContain('Alice');
  });

  it('refreshes mentionable bots after the current group membership changes', async () => {
    api.getGroupInfo
      .mockResolvedValueOnce({
        group: { id: 80, name: 'Agent Room' },
        members: [
          { user_id: 1, display_name: 'Me', is_bot: false },
          { user_id: 42, display_name: 'Saturday', username: 'bot-saturday', is_bot: true },
        ],
      })
      .mockResolvedValueOnce({
        group: { id: 80, name: 'Agent Room' },
        members: [
          { user_id: 1, display_name: 'Me', is_bot: false },
          { user_id: 42, display_name: 'Saturday', username: 'bot-saturday', is_bot: true },
          { user_id: 43, display_name: 'Wanyu', username: 'catsco-agent-worker1', is_bot: true },
        ],
      });

    await mountTopic(root, 'grp_80', { isGroup: true, groupId: 80 });
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });

    await act(async () => {
      wsHandler({ pres: { topic: 'grp_80', src: 'grp_80', what: 'members_invited' } });
      await Promise.resolve();
      await Promise.resolve();
    });

    const textarea = container.querySelector('textarea.v3-composer-input');
    await act(async () => {
      typeDraft(textarea, '@');
    });

    const options = [...container.querySelectorAll('.oc-mention-item')];
    expect(api.getGroupInfo).toHaveBeenCalledTimes(2);
    expect(options.map((option) => option.textContent)).toEqual([
      '所有人全部机器人',
      'Saturday@usr42',
      'Wanyu@usr43',
    ]);
  });

  it('inserts and sends the structured all-bots mention from the picker', async () => {
    api.getGroupInfo.mockResolvedValueOnce({
      group: { id: 80, name: 'Agent Room' },
      members: [
        { user_id: 42, display_name: 'Saturday', username: 'bot-saturday', is_bot: true },
        { user_id: 43, display_name: 'Wanyu', username: 'catsco-agent-worker1', is_bot: true },
      ],
    });

    await mountTopic(root, 'grp_80', { isGroup: true, groupId: 80 });
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });

    const textarea = container.querySelector('textarea.v3-composer-input');
    await act(async () => {
      typeDraft(textarea, '@所有');
    });
    expect(container.querySelectorAll('.oc-mention-item')).toHaveLength(1);
    expect(container.querySelector('.oc-mention-item')?.textContent).toBe('所有人全部机器人');

    await act(async () => {
      Simulate.keyDown(textarea, { key: 'Enter', shiftKey: false });
      await Promise.resolve();
    });
    expect(textarea.value).toBe('@所有人 ');

    await act(async () => {
      typeDraft(textarea, '@所有人 一起处理');
    });
    await act(async () => {
      Simulate.keyDown(textarea, { key: 'Enter', shiftKey: false });
      await flushPromises();
    });

    expect(api.sendMessage).toHaveBeenCalledWith(
      'grp_80',
      '@所有人 一起处理',
      undefined,
      ['all'],
    );
  });

  it('does not send structured mentions for hand-typed uid-like text', async () => {
    await mountTopic(root, 'grp_80', { isGroup: true, groupId: 80 });

    const textarea = container.querySelector('textarea.v3-composer-input');
    await act(async () => {
      typeDraft(textarea, '@usr43 请处理');
    });
    await act(async () => {
      Simulate.keyDown(textarea, { key: 'Enter', shiftKey: false });
      await flushPromises();
    });

    expect(api.sendMessage).toHaveBeenCalledWith('grp_80', '@usr43 请处理', undefined);
  });

  it('filters bot names and inserts the canonical uid mention with Enter', async () => {
    api.getGroupInfo.mockResolvedValueOnce({
      group: { id: 80, name: 'Agent Room' },
      members: [
        { user_id: 42, display_name: 'Saturday', username: 'bot-saturday', is_bot: true },
        { user_id: 43, display_name: 'Wanyu', username: 'catsco-agent-worker1', is_bot: true },
      ],
    });

    await mountTopic(root, 'grp_80', { isGroup: true, groupId: 80 });
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });

    const textarea = container.querySelector('textarea.v3-composer-input');
    await act(async () => {
      typeDraft(textarea, '@wan');
    });
    expect(container.querySelectorAll('.oc-mention-item')).toHaveLength(1);

    await act(async () => {
      Simulate.keyDown(textarea, { key: 'Enter', shiftKey: false });
      await Promise.resolve();
    });

    expect(textarea.value).toBe('@usr43 ');
    expect(container.querySelector('.oc-mention-picker')).toBeNull();
    expect(api.sendMessage).not.toHaveBeenCalled();

    await act(async () => {
      typeDraft(textarea, '@usr43 请处理');
    });
    await act(async () => {
      Simulate.keyDown(textarea, { key: 'Enter', shiftKey: false });
      await flushPromises();
    });

    expect(api.sendMessage).toHaveBeenCalledWith('grp_80', '@usr43 请处理', undefined, ['usr43']);
  });

  it('does not send structured mentions after typing against the picker token boundary', async () => {
    api.getGroupInfo.mockResolvedValueOnce({
      group: { id: 80, name: 'Agent Room' },
      members: [
        { user_id: 43, display_name: 'Wanyu', username: 'catsco-agent-worker1', is_bot: true },
      ],
    });

    await mountTopic(root, 'grp_80', { isGroup: true, groupId: 80 });
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });

    const textarea = container.querySelector('textarea.v3-composer-input');
    await act(async () => {
      typeDraft(textarea, '@wan');
    });
    await act(async () => {
      Simulate.keyDown(textarea, { key: 'Enter', shiftKey: false });
      await Promise.resolve();
    });
    expect(textarea.value).toBe('@usr43 ');

    await act(async () => {
      typeDraft(textarea, '@usr43x 请处理');
    });
    await act(async () => {
      Simulate.keyDown(textarea, { key: 'Enter', shiftKey: false });
      await flushPromises();
    });

    expect(api.sendMessage).toHaveBeenCalledWith('grp_80', '@usr43x 请处理', undefined);
  });

  it('restores picker provenance and original text after a send failure', async () => {
    api.getGroupInfo.mockResolvedValueOnce({
      group: { id: 80, name: 'Agent Room' },
      members: [
        { user_id: 43, display_name: 'Wanyu', username: 'catsco-agent-worker1', is_bot: true },
      ],
    });
    api.sendMessage.mockRejectedValueOnce(new Error('send failed'));

    await mountTopic(root, 'grp_80', { isGroup: true, groupId: 80 });
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });

    const textarea = container.querySelector('textarea.v3-composer-input');
    await act(async () => {
      typeDraft(textarea, '@wan');
    });
    await act(async () => {
      Simulate.keyDown(textarea, { key: 'Enter', shiftKey: false });
      await Promise.resolve();
    });
    await act(async () => {
      typeDraft(textarea, '  @usr43 ');
    });
    await act(async () => {
      typeDraft(textarea, '  @usr43 请处理  ');
    });

    await act(async () => {
      Simulate.keyDown(textarea, { key: 'Enter', shiftKey: false });
      await flushPromises();
    });

    expect(api.sendMessage).toHaveBeenNthCalledWith(1, 'grp_80', '@usr43 请处理', undefined, ['usr43']);
    expect(textarea.value).toBe('  @usr43 请处理  ');

    await act(async () => {
      Simulate.keyDown(textarea, { key: 'Enter', shiftKey: false });
      await flushPromises();
    });

    expect(api.sendMessage).toHaveBeenNthCalledWith(2, 'grp_80', '@usr43 请处理', undefined, ['usr43']);
  });

  it('drops structured mention provenance after the picker token is removed', async () => {
    api.getGroupInfo.mockResolvedValueOnce({
      group: { id: 80, name: 'Agent Room' },
      members: [
        { user_id: 43, display_name: 'Wanyu', username: 'catsco-agent-worker1', is_bot: true },
      ],
    });

    await mountTopic(root, 'grp_80', { isGroup: true, groupId: 80 });
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });

    const textarea = container.querySelector('textarea.v3-composer-input');
    await act(async () => {
      typeDraft(textarea, '@wan');
    });
    await act(async () => {
      Simulate.keyDown(textarea, { key: 'Enter', shiftKey: false });
      await Promise.resolve();
    });
    expect(textarea.value).toBe('@usr43 ');

    await act(async () => {
      typeDraft(textarea, '请处理');
    });
    await act(async () => {
      typeDraft(textarea, '@usr43 请处理');
    });
    await act(async () => {
      Simulate.keyDown(textarea, { key: 'Enter', shiftKey: false });
      await flushPromises();
    });

    expect(api.sendMessage).toHaveBeenCalledWith('grp_80', '@usr43 请处理', undefined);
  });

  it('opens the bot picker from the toolbar and inserts at the cursor', async () => {
    api.getGroupInfo.mockResolvedValueOnce({
      group: { id: 80, name: 'Agent Room' },
      members: [
        { user_id: 42, display_name: 'Saturday', username: 'bot-saturday', is_bot: true },
      ],
    });

    await mountTopic(root, 'grp_80', { isGroup: true, groupId: 80 });
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });

    const textarea = container.querySelector('textarea.v3-composer-input');
    await act(async () => {
      typeDraft(textarea, '前后');
    });
    await act(async () => {
      textarea.setSelectionRange(1, 1);
      Simulate.click(container.querySelector('button[aria-label="@机器人"]'));
      await new Promise((resolve) => setTimeout(resolve, 0));
    });

    expect(textarea.value).toBe('前@后');
    const option = [...container.querySelectorAll('.oc-mention-item')]
      .find((item) => item.textContent.includes('Saturday'));
    expect(option).toBeTruthy();

    await act(async () => {
      Simulate.mouseDown(option);
      await new Promise((resolve) => setTimeout(resolve, 0));
    });

    expect(textarea.value).toBe('前@usr42 后');
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

    await mountTopic(root, 'p2p_1_2', {
      topBar: <header className="mock-top-bar">Conversation actions</header>,
    });

    await act(async () => {
      Simulate.click(container.querySelector('.mock-open-preview'));
      await Promise.resolve();
    });

    const workspace = container.querySelector('.v3-message-workspace');
    const chatColumn = container.querySelector('.v3-chat-column');
    const handle = container.querySelector('.v3-preview-resize-handle');
    const preview = container.querySelector('.mock-file-preview');
    expect(workspace.className).toContain('has-preview');
    expect(chatColumn.querySelector(':scope > .mock-top-bar')).not.toBeNull();
    expect(workspace.querySelector(':scope > .mock-top-bar')).toBeNull();
    expect(preview.getAttribute('data-background-class')).toContain('v3-chat-column');
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

  it('opens cloud artifact management in the preview area and previews a selected artifact there', async () => {
    const artifact = {
      id: 'lesson-game',
      title: '课堂小游戏',
      kind: 'html',
      url: 'https://artifacts.example.test/by-agent/440/lesson-game/latest/',
      status: 'active',
      publish_version: 2,
      can_delete: true,
    };
    api.getCloudArtifacts.mockResolvedValue({ artifacts: [artifact] });

    await mountTopic(root, 'p2p_1_440', {
      cloudArtifactsRequest: { agentUid: 440, requestId: 1 },
    });
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });

    const workspace = container.querySelector('.v3-message-workspace');
    expect(workspace.className).toContain('has-preview');
    expect(container.querySelector('.cloud-artifacts-panel')).not.toBeNull();
    expect(api.getCloudArtifacts).toHaveBeenCalledWith(440, 'active');

    await act(async () => {
      Simulate.click(container.querySelector('button[aria-label="预览 课堂小游戏"]'));
      await Promise.resolve();
    });

    expect(container.querySelector('.cloud-artifacts-panel')).toBeNull();
    const preview = container.querySelector('.mock-file-preview');
    expect(preview?.textContent).toContain('课堂小游戏');
    expect(preview?.getAttribute('data-url')).toBe(artifact.url);
  });

  it('finds an agent file from history and opens it in the existing file preview', async () => {
    const historicalFile = {
      id: '820:0',
      name: '期末学情报告.pdf',
      url: '/uploads/files/term-report.pdf',
      mime_type: 'application/pdf',
      size: 728341,
      topic_name: '期末材料',
    };
    api.getAgentFiles.mockResolvedValue({
      files: [historicalFile],
      has_more: false,
      next_before_id: 0,
    });

    await mountTopic(root, 'p2p_1_440', {
      cloudArtifactsRequest: { agentUid: 440, requestId: 1 },
    });
    await act(async () => {
      Simulate.click([...container.querySelectorAll('button[role="tab"]')]
        .find((button) => button.textContent === '文件'));
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(api.getAgentFiles).toHaveBeenCalledWith(440, { beforeId: 0, limit: 40 });
    await act(async () => {
      Simulate.click(container.querySelector('button[aria-label="预览文件 期末学情报告.pdf"]'));
      await Promise.resolve();
    });

    expect(container.querySelector('.cloud-artifacts-panel')).toBeNull();
    const preview = container.querySelector('.mock-file-preview');
    expect(preview?.textContent).toContain('期末学情报告.pdf');
    expect(preview?.getAttribute('data-url')).toBe(historicalFile.url);

    await act(async () => {
      Simulate.click(container.querySelector('button[aria-label="返回产物列表"]'));
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(container.querySelector('.cloud-artifacts-panel')).not.toBeNull();
    expect([...container.querySelectorAll('button[role="tab"]')]
      .find((button) => button.textContent === '文件')
      ?.getAttribute('aria-selected')).toBe('true');
    expect(api.getAgentFiles).toHaveBeenCalledTimes(2);
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
    expect(container.querySelectorAll('.v3-composer-box .v3-composer-attachment-chip')).toHaveLength(1);
  });

  it('continues a multi-image upload after one file fails and keeps successful images removable', async () => {
    api.uploadFile
      .mockRejectedValueOnce(new Error('first upload failed'))
      .mockResolvedValueOnce({
        file_key: '20260610_dog.jpg',
        url: '/uploads/images/20260610_dog.jpg',
        name: 'dog.jpg',
        size: 14,
        mime_type: 'image/jpeg',
      });

    await mountTopic(root, 'p2p_1_2');

    const input = container.querySelector('input[accept*="image/jpeg"]');
    const firstImage = new File(['first'], 'cat.jpg', { type: 'image/jpeg' });
    const secondImage = new File(['second'], 'dog.jpg', { type: 'image/jpeg' });

    await act(async () => {
      Simulate.change(input, {
        target: {
          files: [firstImage, secondImage],
          value: 'C:\\fakepath\\dog.jpg',
        },
      });
      await flushPromises();
    });

    expect(api.uploadFile).toHaveBeenCalledTimes(2);
    expect(container.textContent).toContain('已添加 1 个附件，另有 1 个上传失败');
    expect(container.querySelectorAll('.v3-composer-attachment-chip')).toHaveLength(1);
    expect(container.querySelector('.v3-composer-attachment-chip img')?.getAttribute('src'))
      .toBe('/uploads/images/20260610_dog.jpg');

    await act(async () => {
      Simulate.click(container.querySelector('button[aria-label="移除附件：dog.jpg"]'));
    });
    expect(container.querySelector('.v3-composer-attachment-chip')).toBeNull();
  });

  it('opens a phone upload QR dialog from the composer', async () => {
    await mountTopic(root, 'p2p_1_2');

    const phoneUploadButton = await openPhoneUploadFromComposer(container);
    expect(phoneUploadButton.getAttribute('data-tooltip')).toBe('手机扫码上传');

    expect(container.textContent).toContain('手机扫码上传');
    expect(container.textContent).toContain('/mobile-upload/');
  });

  it('closes the phone upload dialog with Escape or a backdrop press', async () => {
    await mountTopic(root, 'p2p_1_2');
    await openPhoneUploadFromComposer(container);

    expect(container.querySelector('.v3-phone-upload-backdrop')).not.toBeNull();
    await act(async () => {
      document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
      await Promise.resolve();
    });
    expect(container.querySelector('.v3-phone-upload-backdrop')).toBeNull();

    await openPhoneUploadFromComposer(container);
    const backdrop = container.querySelector('.v3-phone-upload-backdrop');
    expect(backdrop).not.toBeNull();
    await act(async () => {
      Simulate.mouseDown(backdrop);
      await Promise.resolve();
    });
    expect(container.querySelector('.v3-phone-upload-backdrop')).toBeNull();
  });

  it('uses an absolute phone upload URL without prefixing the browser origin', async () => {
    api.createMobileUploadSession.mockResolvedValueOnce({
      session_id: 'lan123',
      upload_url: 'https://app.example.test/mobile-upload/lan123',
      api_upload_url: '/api/mobile-upload/sessions/lan123/files',
    });

    await mountTopic(root, 'p2p_1_2');
    await openPhoneUploadFromComposer(container);

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
    await openPhoneUploadFromComposer(container);

    await act(async () => {
      vi.advanceTimersByTime(2000);
      await Promise.resolve();
    });

    expect(container.textContent).toContain('手机已上传 8 个附件');
    expect(container.querySelectorAll('.v3-attachment-notice')).toHaveLength(1);
    expect(container.querySelector('.v3-composer-attachments')).toBeNull();

    await act(async () => {
      Simulate.click(container.querySelector('button[aria-label="关闭手机上传"]'));
    });

    await act(async () => {
      vi.advanceTimersByTime(2000);
      await Promise.resolve();
    });

    expect(container.textContent).toContain('手机已上传 9 个附件');
    vi.useRealTimers();
  });

  it('shows tutorial task cards on an empty topic', async () => {
    mockTutorialAgentPeer();
    await mountTopic(root, 'p2p_1_2');
    await act(async () => {
      await flushPromises();
    });

    expect(container.textContent).toContain('试一个文件任务');
    expect(container.textContent).toContain('读图提取信息');
    expect(container.textContent).toContain('移动文件到桌面');
  });

  it('waits for history before showing tutorial task cards', async () => {
    mockTutorialAgentPeer();
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
      await flushPromises();
    });

    expect(container.querySelector('.cc-tutorial-empty')).not.toBeNull();
  });

  it('shows an actionable state when initial history loading fails', async () => {
    api.getMessages.mockRejectedValueOnce(new Error('network unavailable'));

    await mountTopic(root, 'p2p_1_2');
    await act(async () => {
      await flushPromises();
    });

    expect(container.textContent).toContain('聊天记录加载失败');
    const retryButton = Array.from(container.querySelectorAll('button'))
      .find((button) => button.textContent.includes('重新加载'));
    expect(retryButton).not.toBeNull();

    await act(async () => {
      Simulate.click(retryButton);
      await flushPromises();
    });

    expect(container.textContent).not.toContain('聊天记录加载失败');
  });

  it('uses a stable before cursor when loading older history', async () => {
    const latest = Array.from({ length: 50 }, (_, index) => ({
      id: 101 + index,
      seq_id: 101 + index,
      topic_id: 'p2p_1_2',
      from_uid: index % 2 === 0 ? 1 : 2,
      type: 'text',
      content: `latest-${index}`,
    }));
    api.getMessages.mockImplementation((topic, limit, offset, latestPage, beforeId) => {
      if (limit === 500) {
        return Promise.resolve({ messages: [], has_more: false, next_before_id: 0 });
      }
      if (beforeId === 101) {
        return Promise.resolve({
        messages: [{ id: 100, seq_id: 100, topic_id: 'p2p_1_2', from_uid: 2, type: 'text', content: 'older' }],
        has_more: false,
        next_before_id: 100,
      });
      }
      return Promise.resolve({ messages: latest, has_more: true, next_before_id: 101 });
    });

    await mountTopic(root, 'p2p_1_2');
    await act(async () => {
      await flushPromises();
    });

    expect(api.getMessages).toHaveBeenCalledWith(
      'p2p_1_2',
      50,
      0,
      true,
      0,
      expect.objectContaining({ signal: expect.any(AbortSignal), timeoutMs: 15000 }),
    );
    expect(api.getMessages).toHaveBeenCalledWith(
      'p2p_1_2',
      50,
      50,
      true,
      101,
      expect.objectContaining({ signal: expect.any(AbortSignal), timeoutMs: 15000 }),
    );
    expect(container.querySelector('[data-message-content="older"]')).not.toBeNull();
  });

  it('shows a specific retry state when history loading times out', async () => {
    const timeoutError = new Error('timeout');
    timeoutError.code = 'REQUEST_TIMEOUT';
    api.getMessages.mockRejectedValueOnce(timeoutError);

    await mountTopic(root, 'p2p_1_2');
    await act(async () => {
      await flushPromises();
    });

    expect(container.textContent).toContain('聊天记录加载超时，请重试');
    expect(Array.from(container.querySelectorAll('button'))
      .some((button) => button.textContent.includes('重新加载'))).toBe(true);
  });

  it('cancels the previous topic history request when switching topics', async () => {
    const firstHistory = deferred();
    let firstOptions;
    api.getMessages
      .mockImplementationOnce((topic, limit, offset, latest, beforeId, options) => {
        firstOptions = options;
        return firstHistory.promise;
      })
      .mockResolvedValueOnce({
        messages: [{ id: 2, topic_id: 'p2p_1_3', from_uid: 3, type: 'text', content: 'topic B' }],
        has_more: false,
      });

    await mountTopic(root, 'p2p_1_2');
    expect(firstOptions.signal.aborted).toBe(false);

    await mountTopic(root, 'p2p_1_3');
    await act(async () => {
      await flushPromises();
    });

    expect(firstOptions.signal.aborted).toBe(true);
    expect(container.querySelector('[data-message-content="topic B"]')).not.toBeNull();
    expect(container.textContent).not.toContain('聊天记录加载失败');
  });

  it('cancels an in-flight question index request when switching topics', async () => {
    const initialHistory = deferred();
    const questionIndex = deferred();
    let questionIndexOptions;
    api.getMessages
      .mockImplementationOnce(() => initialHistory.promise)
      .mockImplementationOnce((topic, limit, offset, latest, beforeId, options) => {
        questionIndexOptions = options;
        return questionIndex.promise;
      })
      .mockResolvedValueOnce({
        messages: [{ id: 200, topic_id: 'p2p_1_3', from_uid: 3, type: 'text', content: 'topic B' }],
        has_more: false,
      });

    await mountTopic(root, 'p2p_1_2');
    const timeline = container.querySelector('.v3-timeline');
    Object.defineProperty(timeline, 'scrollHeight', { configurable: true, value: 1000 });
    Object.defineProperty(timeline, 'clientHeight', { configurable: true, value: 500 });
    timeline.scrollTop = 500;
    await act(async () => {
      initialHistory.resolve({
        messages: [{
          id: 100,
          seq_id: 100,
          topic_id: 'p2p_1_2',
          from_uid: 1,
          type: 'text',
          content: 'latest question',
        }],
        has_more: true,
        next_before_id: 100,
      });
      await flushPromises();
    });
    const navigator = container.querySelector('.cc-question-navigator');
    expect(navigator).not.toBeNull();

    await act(async () => {
      Simulate.mouseEnter(navigator);
      await Promise.resolve();
    });
    expect(questionIndexOptions.signal.aborted).toBe(false);

    await mountTopic(root, 'p2p_1_3');
    await act(async () => {
      await flushPromises();
    });
    expect(questionIndexOptions.signal.aborted).toBe(true);
    expect(container.querySelector('[data-message-content="topic B"]')).not.toBeNull();
  });

  it('loads past tall working-only pages until ordinary chat content appears', async () => {
    const initialHistory = deferred();
    const workingPage = (id) => ({
      messages: [{
        id,
        seq_id: id,
        topic_id: 'p2p_1_2',
        from_uid: 2,
        type: 'tool_result',
        content: `working-${id}`,
      }],
      has_more: true,
      next_before_id: id,
    });
    api.getMessages
      .mockImplementationOnce(() => initialHistory.promise)
      .mockResolvedValueOnce(workingPage(98))
      .mockResolvedValueOnce(workingPage(97))
      .mockResolvedValueOnce({
        messages: [{
          id: 96,
          seq_id: 96,
          topic_id: 'p2p_1_2',
          from_uid: 1,
          type: 'text',
          content: 'ordinary question',
        }],
        has_more: true,
        next_before_id: 96,
      });

    await mountTopic(root, 'p2p_1_2');
    const timeline = container.querySelector('.v3-timeline');
    Object.defineProperty(timeline, 'scrollHeight', { configurable: true, value: 1200 });
    Object.defineProperty(timeline, 'clientHeight', { configurable: true, value: 500 });
    timeline.scrollTop = 500;

    await act(async () => {
      initialHistory.resolve(workingPage(99));
      await flushPromises(24);
    });

    expect(api.getMessages).toHaveBeenCalledTimes(4);
    expect(container.querySelector('[data-message-content="ordinary question"]')).not.toBeNull();
  });

  it('caps automatic history loading and lets the user continue explicitly', async () => {
    const initialHistory = deferred();
    let page = 100;
    api.getMessages.mockImplementation(() => Promise.resolve({
      messages: [{
        id: page,
        seq_id: page--,
        topic_id: 'p2p_1_2',
        from_uid: 2,
        type: 'tool_result',
        content: 'working only',
      }],
      has_more: true,
      next_before_id: page,
    }));
    api.getMessages.mockImplementationOnce(() => initialHistory.promise);

    await mountTopic(root, 'p2p_1_2');
    await act(async () => {
      initialHistory.resolve({
        messages: [{
          id: page,
          seq_id: page--,
          topic_id: 'p2p_1_2',
          from_uid: 2,
          type: 'tool_result',
          content: 'latest working',
        }],
        has_more: true,
        next_before_id: page,
      });
      await flushPromises(40);
    });

    expect(api.getMessages).toHaveBeenCalledTimes(7);
    expect(container.textContent).toContain('已暂停自动加载');
    const continueButton = Array.from(container.querySelectorAll('button'))
      .find((button) => button.textContent.includes('继续加载'));
    expect(continueButton).not.toBeNull();

    await act(async () => {
      Simulate.click(continueButton);
      await flushPromises(40);
    });
    expect(api.getMessages).toHaveBeenCalledTimes(14);
    expect(container.textContent).toContain('已暂停自动加载');
  });

  it('shows cached history immediately when returning to a topic', async () => {
    const refreshed = deferred();
    api.getMessages
      .mockResolvedValueOnce({
        messages: [{ id: 1, topic_id: 'p2p_1_2', from_uid: 2, type: 'text', content: 'cached topic A' }],
        has_more: false,
      })
      .mockResolvedValueOnce({
        messages: [{ id: 2, topic_id: 'p2p_1_3', from_uid: 3, type: 'text', content: 'topic B' }],
        has_more: false,
      })
      .mockImplementationOnce(() => refreshed.promise);

    await mountTopic(root, 'p2p_1_2');
    await act(async () => {
      await flushPromises();
    });
    await mountTopic(root, 'p2p_1_3');
    await act(async () => {
      await flushPromises();
    });

    await act(async () => {
      renderTopic(root, 'p2p_1_2');
      await Promise.resolve();
    });

    expect(container.querySelector('[data-message-content="cached topic A"]')).not.toBeNull();
    expect(container.textContent).not.toContain('正在加载聊天记录');

    await act(async () => {
      refreshed.resolve({
        messages: [{ id: 3, topic_id: 'p2p_1_2', from_uid: 2, type: 'text', content: 'fresh topic A' }],
        has_more: false,
      });
      await flushPromises();
    });
    expect(container.querySelector('[data-message-content="fresh topic A"]')).not.toBeNull();
  });

  it('resumes older history loading after a cached topic refresh finishes at the top', async () => {
    const initialTopicA = deferred();
    const refreshedTopicA = deferred();
    const latest = Array.from({ length: 50 }, (_, index) => ({
      id: 101 + index,
      seq_id: 101 + index,
      topic_id: 'p2p_1_2',
      from_uid: index % 2 === 0 ? 1 : 2,
      type: 'text',
      content: `latest-${index}`,
    }));
    let topicALatestRequests = 0;
    api.getMessages.mockImplementation((topic, limit, offset, latestPage, beforeId) => {
      if (limit === 500) {
        return Promise.resolve({ messages: [], has_more: false, next_before_id: 0 });
      }
      if (topic === 'p2p_1_2' && beforeId === 101) {
        return Promise.resolve({
          messages: [{ id: 100, seq_id: 100, topic_id: 'p2p_1_2', from_uid: 2, type: 'text', content: 'older after refresh' }],
          has_more: false,
          next_before_id: 100,
        });
      }
      if (topic === 'p2p_1_3') {
        return Promise.resolve({
        messages: [{ id: 201, topic_id: 'p2p_1_3', from_uid: 3, type: 'text', content: 'topic B' }],
        has_more: false,
        });
      }
      topicALatestRequests += 1;
      return topicALatestRequests === 1 ? initialTopicA.promise : refreshedTopicA.promise;
    });

    await mountTopic(root, 'p2p_1_2');
    const timeline = container.querySelector('.v3-timeline');
    Object.defineProperty(timeline, 'scrollHeight', { configurable: true, value: 1000 });
    Object.defineProperty(timeline, 'clientHeight', { configurable: true, value: 500 });
    timeline.scrollTop = 500;
    await act(async () => {
      initialTopicA.resolve({ messages: latest, has_more: true, next_before_id: 101 });
      await flushPromises();
    });
    await mountTopic(root, 'p2p_1_3');
    await act(async () => {
      await flushPromises();
    });
    await act(async () => {
      renderTopic(root, 'p2p_1_2');
      await Promise.resolve();
    });

    timeline.scrollTop = 0;
    await act(async () => {
      Simulate.scroll(timeline);
      await Promise.resolve();
    });
    expect(api.getMessages.mock.calls.some(
      ([targetTopic, limit, offset, latest, beforeId]) => (
        targetTopic === 'p2p_1_2'
        && limit === 50
        && offset === 50
        && latest === true
        && beforeId === 101
      ),
    )).toBe(false);

    await act(async () => {
      refreshedTopicA.resolve({ messages: latest, has_more: true, next_before_id: 101 });
      await flushPromises();
    });

    expect(api.getMessages).toHaveBeenCalledWith(
      'p2p_1_2',
      50,
      50,
      true,
      101,
      expect.objectContaining({ signal: expect.any(AbortSignal), timeoutMs: 15000 }),
    );
    expect(container.querySelector('[data-message-content="older after refresh"]')).not.toBeNull();
  });

  it('downloads tutorial media and fills the selected prompt', async () => {
    mockTutorialAgentPeer();
    await mountTopic(root, 'p2p_1_2', { localAssistantStatus: 'connected' });
    await act(async () => {
      await flushPromises();
    });

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
    mockTutorialAgentPeer();
    await mountTopic(root, 'p2p_1_2');
    await act(async () => {
      await flushPromises();
    });

    await act(async () => {
      Simulate.click(Array.from(container.querySelectorAll('button')).find((el) => el.textContent.includes('暂时不用')));
    });

    expect(container.textContent).not.toContain('试一个文件任务');
    expect(localStorage.getItem('cc_tutorial_empty_dismissed:v1:1:p2p_1_2')).toBe('1');
  });

  it('does not show tutorial task cards in an empty human friend conversation', async () => {
    api.getFriends.mockResolvedValue({
      friends: [{
        id: 2,
        username: 'human-friend',
        display_name: 'Human Friend',
        bot: false,
      }],
    });

    await mountTopic(root, 'p2p_1_2');
    await act(async () => {
      await flushPromises();
    });

    expect(container.querySelector('.cc-tutorial-empty')).toBeNull();
  });

  it('shows tutorial task cards in an empty Agent group task but not a human group', async () => {
    api.getGroupInfo.mockResolvedValueOnce({
      group: { id: 9, name: 'Agent task', kind: 'agent_task', is_agent_task: true },
      members: [
        { user_id: 1, display_name: 'Me', is_bot: false },
        { user_id: 2, display_name: 'Tutorial Agent', is_bot: true },
      ],
    });

    await mountTopic(root, 'grp_9', { isGroup: true, groupId: 9 });
    await act(async () => {
      await flushPromises();
    });

    expect(container.querySelector('.cc-tutorial-empty')).not.toBeNull();

    api.getGroupInfo.mockResolvedValueOnce({
      group: { id: 10, name: 'Human group', kind: 'standard' },
      members: [
        { user_id: 1, display_name: 'Me', is_bot: false },
        { user_id: 3, display_name: 'Human Friend', is_bot: false },
      ],
    });

    await mountTopic(root, 'grp_10', { isGroup: true, groupId: 10 });
    await act(async () => {
      await flushPromises();
    });

    expect(container.querySelector('.cc-tutorial-empty')).toBeNull();
  });

  it('does not repeat the migrated mobile binding action for bot friends', async () => {
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

    expect(container.querySelector('button[title="移动端使用"]')).toBeNull();
  });

  it('does not repeat the migrated mobile binding action for roster agents', async () => {
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

    expect(container.querySelector('button[title="移动端使用"]')).toBeNull();
  });

  it('does not repeat migrated mobile and management actions in a group header', async () => {
    api.getGroupInfo.mockResolvedValueOnce({
      group: { id: 9, name: '前端验收群' },
      members: [{ user_id: 1, display_name: 'Me', is_bot: false }],
    });

    await mountTopic(root, 'grp_9', { isGroup: true, groupId: 9 });
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(container.querySelector('.v3-conversation-actions')).toBeNull();
    expect(container.querySelector('button[title="移动端使用"]')).toBeNull();
    expect(container.querySelector('button[title="群设置"]')).toBeNull();
  });

  it('does not render an Agent selector in an active conversation composer', async () => {
    await mountTopic(root, 'p2p_1_2');

    expect(container.querySelector('.v3-agent-picker')).toBeNull();
    expect(container.querySelector('button[aria-label^="选择 Agent"]')).toBeNull();
  });

  it('reports the owner shared quota to the active conversation header', async () => {
    const onAgentModelChange = vi.fn();
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
        reasoning_effort: 'high',
        remaining_percent: 72,
        status: 'normal',
      },
    });

    await mountTopic(root, 'p2p_1_2', { onAgentModelChange });
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(api.getAgentQuota).toHaveBeenCalledWith(2);
    expect(onAgentModelChange).toHaveBeenLastCalledWith({
      isBot: true,
      state: 'ready',
      summary: {
        source: 'relay',
        model: 'MiniMax-M3',
        reasoning_effort: 'high',
        remaining_percent: 72,
        status: 'normal',
      },
    });
    expect(container.querySelector('.v3-agent-quota-pill')).toBeNull();
  });

  it('reports a custom model source to the active conversation header', async () => {
    const onAgentModelChange = vi.fn();
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

    await mountTopic(root, 'p2p_1_2', { onAgentModelChange });
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(onAgentModelChange).toHaveBeenLastCalledWith({
      isBot: true,
      state: 'ready',
      summary: {
        source: 'custom',
        model: 'gpt-5.6-terra',
        status: 'custom',
      },
    });
    expect(container.querySelector('.v3-agent-quota-pill')).toBeNull();
  });

  it('hides the model in a direct human conversation', async () => {
    const onAgentModelChange = vi.fn();
    api.getFriends.mockResolvedValueOnce({
      friends: [{ id: 2, username: 'alice', display_name: 'Alice', account_type: 'human' }],
    });

    await mountTopic(root, 'p2p_1_2', { onAgentModelChange });
    await act(async () => {
      await flushPromises();
    });

    expect(api.getAgentQuota).not.toHaveBeenCalled();
    expect(onAgentModelChange).toHaveBeenLastCalledWith({
      isBot: false,
      state: 'hidden',
      summary: null,
    });
  });

  it('reports the only Agent model and quota for a single-Agent task', async () => {
    const onAgentModelChange = vi.fn();
    api.getGroupInfo.mockResolvedValueOnce({
      group: { id: 9, name: '单 Agent 任务', is_agent_task: true },
      members: [
        { user_id: 1, display_name: 'Me', is_bot: false },
        { user_id: 405, display_name: 'Wanyu', is_bot: true },
      ],
    });
    api.getAgentQuota.mockResolvedValueOnce({
      configured: true,
      shared: true,
      summary: {
        source: 'relay',
        model: 'gpt-5.6-terra',
        reasoning_effort: 'xhigh',
        remaining_percent: 81,
        status: 'normal',
      },
    });

    await mountTopic(root, 'grp_9', {
      isGroup: true,
      groupId: 9,
      onAgentModelChange,
    });
    await act(async () => {
      await flushPromises();
    });

    expect(api.getAgentQuota).toHaveBeenCalledWith(405);
    expect(onAgentModelChange).toHaveBeenLastCalledWith({
      isBot: true,
      state: 'ready',
      summary: {
        source: 'relay',
        model: 'gpt-5.6-terra',
        reasoning_effort: 'xhigh',
        remaining_percent: 81,
        status: 'normal',
      },
    });
  });

  it('reports artifact capability for a single-Agent task', async () => {
    const onActiveAgentChange = vi.fn();
    api.getAgents.mockResolvedValue({
      agents: [{
        uid: 440,
        username: 'doubao',
        display_name: '豆包',
        relation: 'friend',
        is_bot: true,
        cloud_artifacts_enabled: true,
      }],
    });
    api.getGroupInfo.mockResolvedValueOnce({
      group: { id: 9, name: '豆包任务', kind: 'agent_task', is_agent_task: true },
      members: [
        { user_id: 1, display_name: 'Me', is_bot: false },
        { user_id: 440, display_name: '豆包', is_bot: true },
      ],
    });

    await mountTopic(root, 'grp_9', {
      isGroup: true,
      groupId: 9,
      onActiveAgentChange,
    });
    await act(async () => {
      await flushPromises();
    });

    expect(onActiveAgentChange).toHaveBeenLastCalledWith({
      uid: 440,
      relation: 'friend',
      isOwner: false,
      cloud_artifacts_enabled: true,
    });
    expect(api.getCloudArtifacts).toHaveBeenCalledWith(440, 'active');
  });

  it('hides the model for a multi-Agent task', async () => {
    const onAgentModelChange = vi.fn();
    api.getGroupInfo.mockResolvedValueOnce({
      group: { id: 10, name: '多 Agent 任务', is_agent_task: true },
      members: [
        { user_id: 1, display_name: 'Me', is_bot: false },
        { user_id: 405, display_name: 'Wanyu', is_bot: true },
        { user_id: 407, display_name: 'Saturday', is_bot: true },
      ],
    });

    await mountTopic(root, 'grp_10', {
      isGroup: true,
      groupId: 10,
      onAgentModelChange,
    });
    await act(async () => {
      await flushPromises();
    });

    expect(api.getAgentQuota).not.toHaveBeenCalled();
    expect(onAgentModelChange).toHaveBeenLastCalledWith({
      isBot: false,
      state: 'hidden',
      summary: null,
    });
  });

  it('hides the model for a regular group even when one bot is present', async () => {
    const onAgentModelChange = vi.fn();
    api.getGroupInfo.mockResolvedValueOnce({
      group: { id: 11, name: '普通群', kind: 'standard', has_bot: true },
      members: [
        { user_id: 1, display_name: 'Me', is_bot: false },
        { user_id: 405, display_name: 'Wanyu', is_bot: true },
      ],
    });

    await mountTopic(root, 'grp_11', {
      isGroup: true,
      groupId: 11,
      onAgentModelChange,
    });
    await act(async () => {
      await flushPromises();
    });

    expect(api.getAgentQuota).not.toHaveBeenCalled();
    expect(onAgentModelChange).toHaveBeenLastCalledWith({
      isBot: false,
      state: 'hidden',
      summary: null,
    });
  });

  it('reports artifact capability for a regular two-person group with Doubao', async () => {
    const onActiveAgentChange = vi.fn();
    api.getAgents.mockResolvedValue({
      agents: [{
        uid: 440,
        username: 'doubao',
        display_name: '豆包',
        relation: 'friend',
        is_bot: true,
        cloud_artifacts_enabled: true,
      }],
    });
    api.getGroupInfo.mockResolvedValueOnce({
      group: { id: 15, name: '我和豆包', kind: 'standard', has_bot: true },
      members: [
        { user_id: 1, display_name: 'Me', is_bot: false },
        { user_id: 440, display_name: '豆包', is_bot: true },
      ],
    });

    await mountTopic(root, 'grp_15', {
      isGroup: true,
      groupId: 15,
      onActiveAgentChange,
    });
    await act(async () => {
      await flushPromises();
    });

    expect(onActiveAgentChange).toHaveBeenLastCalledWith({
      uid: 440,
      relation: 'friend',
      isOwner: false,
      cloud_artifacts_enabled: true,
    });
  });

  it('ignores a late group-info response after switching to another Artifact-enabled group', async () => {
    const onActiveAgentChange = vi.fn();
    const firstGroup = deferred();
    const secondGroup = deferred();
    api.getAgents.mockResolvedValue({
      agents: [
        {
          uid: 440,
          username: 'doubao',
          display_name: '豆包',
          relation: 'friend',
          is_bot: true,
          cloud_artifacts_enabled: true,
        },
        {
          uid: 310,
          username: 'hakimi',
          display_name: '哈基米',
          relation: 'friend',
          is_bot: true,
          cloud_artifacts_enabled: true,
        },
      ],
    });
    api.getGroupInfo.mockImplementation((requestedGroupID) => (
      requestedGroupID === 21 ? firstGroup.promise : secondGroup.promise
    ));

    await mountTopic(root, 'grp_21', {
      isGroup: true,
      groupId: 21,
      onActiveAgentChange,
    });
    await act(async () => {
      renderTopic(root, 'grp_22', {
        isGroup: true,
        groupId: 22,
        onActiveAgentChange,
      });
      await flushPromises();
    });

    await act(async () => {
      secondGroup.resolve({
        group: { id: 22, name: '我和哈基米', kind: 'agent_task', is_agent_task: true },
        members: [
          { user_id: 1, display_name: 'Me', is_bot: false },
          { user_id: 310, display_name: '哈基米', is_bot: true },
        ],
      });
      await flushPromises();
    });

    expect(api.getCloudArtifacts).toHaveBeenCalledWith(310, 'active');
    expect(onActiveAgentChange).toHaveBeenLastCalledWith(expect.objectContaining({ uid: 310 }));

    await act(async () => {
      firstGroup.resolve({
        group: { id: 21, name: '我和豆包', kind: 'agent_task', is_agent_task: true },
        members: [
          { user_id: 1, display_name: 'Me', is_bot: false },
          { user_id: 440, display_name: '豆包', is_bot: true },
        ],
      });
      await flushPromises();
    });

    expect(api.getCloudArtifacts).not.toHaveBeenCalledWith(440, 'active');
    expect(onActiveAgentChange).toHaveBeenLastCalledWith(expect.objectContaining({ uid: 310 }));
  });

  it('reloads group ownership when groupId changes under the same topic', async () => {
    const onActiveAgentChange = vi.fn();
    const staleGroup = deferred();
    api.getAgents.mockResolvedValue({
      agents: [
        {
          uid: 440,
          username: 'doubao',
          relation: 'friend',
          is_bot: true,
          cloud_artifacts_enabled: true,
        },
        {
          uid: 310,
          username: 'hakimi',
          relation: 'friend',
          is_bot: true,
          cloud_artifacts_enabled: true,
        },
      ],
    });
    api.getGroupInfo.mockImplementation((requestedGroupID) => (
      requestedGroupID === 31
        ? staleGroup.promise
        : Promise.resolve({
          group: { id: 32, name: '已修正群', kind: 'agent_task', is_agent_task: true },
          members: [
            { user_id: 1, is_bot: false },
            { user_id: 310, is_bot: true },
          ],
        })
    ));

    await mountTopic(root, 'grp_pending', {
      isGroup: true,
      groupId: 31,
      onActiveAgentChange,
    });
    await act(async () => {
      renderTopic(root, 'grp_pending', {
        isGroup: true,
        groupId: 32,
        onActiveAgentChange,
      });
      await flushPromises();
    });

    expect(api.getGroupInfo).toHaveBeenCalledWith(32);
    expect(onActiveAgentChange).toHaveBeenLastCalledWith(expect.objectContaining({ uid: 310 }));

    await act(async () => {
      staleGroup.resolve({
        group: { id: 31, name: '过期资料', kind: 'agent_task', is_agent_task: true },
        members: [
          { user_id: 1, is_bot: false },
          { user_id: 440, is_bot: true },
        ],
      });
      await flushPromises();
    });

    expect(onActiveAgentChange).toHaveBeenLastCalledWith(expect.objectContaining({ uid: 310 }));
    expect(api.getCloudArtifacts).not.toHaveBeenCalledWith(440, 'active');
  });

  it('refreshes the active Artifact registry after a delete or restore event', async () => {
    let currentArtifacts = [{
      id: 'lesson-game',
      title: '课堂小游戏',
      url: 'https://artifacts.example.test/by-agent/440/lesson-game/latest/',
    }];
    api.getMessages.mockResolvedValue({
      messages: [{
        id: 701,
        from_uid: 440,
        content: '已发布课堂小游戏',
        created_at: '2026-07-27T00:00:00Z',
      }],
    });
    api.getFriends.mockResolvedValue({ friends: [] });
    api.getAgents.mockResolvedValue({
      agents: [{
        uid: 440,
        topic_id: 'p2p_1_440',
        username: 'doubao',
        display_name: '豆包',
        relation: 'friend',
        is_bot: true,
        account_type: 'bot',
        cloud_artifacts_enabled: true,
      }],
    });
    api.getCloudArtifacts.mockImplementation(() => Promise.resolve({ artifacts: currentArtifacts }));

    await mountTopic(root, 'p2p_1_440');
    await act(async () => {
      await flushPromises();
    });

    expect(container.querySelector('.mock-chat-message')?.dataset.knownArtifactCount).toBe('1');
    const callsBeforeChange = api.getCloudArtifacts.mock.calls.length;
    currentArtifacts = [];

    await act(async () => {
      window.dispatchEvent(new CustomEvent('cc:cloud-artifacts-changed', {
        detail: { agentUid: 440 },
      }));
      await flushPromises();
    });

    expect(api.getCloudArtifacts.mock.calls.length).toBeGreaterThan(callsBeforeChange);
    expect(container.querySelector('.mock-chat-message')?.dataset.knownArtifactCount).toBe('0');
  });

  it('lets only the latest Artifact registry request update the active Agent state', async () => {
    const firstRegistry = deferred();
    const refreshedRegistry = deferred();
    api.getMessages.mockResolvedValue({
      messages: [{
        id: 702,
        from_uid: 440,
        content: '等待产物列表',
        created_at: '2026-07-27T00:00:00Z',
      }],
    });
    api.getFriends.mockResolvedValue({ friends: [] });
    api.getAgents.mockResolvedValue({
      agents: [{
        uid: 440,
        topic_id: 'p2p_1_440',
        username: 'doubao',
        relation: 'friend',
        is_bot: true,
        account_type: 'bot',
        cloud_artifacts_enabled: true,
      }],
    });
    api.getCloudArtifacts
      .mockImplementationOnce(() => firstRegistry.promise)
      .mockImplementationOnce(() => refreshedRegistry.promise);

    await mountTopic(root, 'p2p_1_440');
    await act(async () => {
      await flushPromises();
    });
    expect(api.getCloudArtifacts).toHaveBeenCalledTimes(1);

    await act(async () => {
      window.dispatchEvent(new CustomEvent('cc:cloud-artifacts-changed', {
        detail: { agentUid: 440 },
      }));
      await flushPromises();
    });
    expect(api.getCloudArtifacts).toHaveBeenCalledTimes(2);

    await act(async () => {
      refreshedRegistry.resolve({
        artifacts: [{
          id: 'latest',
          url: 'https://artifacts.example.test/by-agent/440/latest/latest/',
        }],
      });
      await flushPromises();
    });
    expect(container.querySelector('.mock-chat-message')?.dataset.knownArtifactCount).toBe('1');

    await act(async () => {
      firstRegistry.resolve({ artifacts: [] });
      await flushPromises();
    });
    expect(container.querySelector('.mock-chat-message')?.dataset.knownArtifactCount).toBe('1');
  });

  it('waits for a streamed Artifact URL message to finish before refreshing the registry', async () => {
    api.getMessages.mockResolvedValue({ messages: [] });
    api.getFriends.mockResolvedValue({ friends: [] });
    api.getAgents.mockResolvedValue({
      agents: [{
        uid: 440,
        topic_id: 'p2p_1_440',
        username: 'doubao',
        relation: 'friend',
        is_bot: true,
        account_type: 'bot',
        cloud_artifacts_enabled: true,
      }],
    });

    await mountTopic(root, 'p2p_1_440');
    await act(async () => {
      await flushPromises();
    });
    const callsBeforeStream = api.getCloudArtifacts.mock.calls.length;

    await act(async () => {
      wsHandler({
        data: {
          topic: 'p2p_1_440',
          from: 'usr440',
          type: 'stream_delta',
          content: '已发布：https://artifacts.',
          metadata: { stream_id: 'artifact-stream-1' },
        },
      });
      wsHandler({
        data: {
          topic: 'p2p_1_440',
          from: 'usr440',
          type: 'stream_delta',
          content: 'example.test/by-agent/440/game/latest/',
          metadata: { stream_id: 'artifact-stream-1' },
        },
      });
      await flushPromises();
    });
    expect(api.getCloudArtifacts).toHaveBeenCalledTimes(callsBeforeStream);

    await act(async () => {
      wsHandler({
        data: {
          topic: 'p2p_1_440',
          from: 'usr440',
          seq_id: 703,
          type: 'text',
          content: '已发布：https://artifacts.example.test/by-agent/440/game/latest/',
          metadata: { stream_id: 'artifact-stream-1' },
        },
      });
      await flushPromises();
    });
    expect(api.getCloudArtifacts).toHaveBeenCalledTimes(callsBeforeStream + 1);
  });

  it('recognizes the only task Agent from the Agent roster when member disclosure is absent', async () => {
    const onAgentModelChange = vi.fn();
    api.getAgents.mockResolvedValueOnce({
      agents: [{ uid: 405, display_name: 'Wanyu', is_bot: true }],
    });
    api.getGroupInfo.mockResolvedValueOnce({
      group: { id: 12, name: '旧任务', kind: 'agent_task' },
      members: [
        { user_id: 1, display_name: 'Me', is_bot: false },
        { user_id: 405, display_name: 'Wanyu' },
      ],
    });
    api.getAgentQuota.mockResolvedValueOnce({
      summary: {
        source: 'relay',
        model: 'gpt-5.6-terra',
        remaining_percent: 65,
        status: 'normal',
      },
    });

    await mountTopic(root, 'grp_12', {
      isGroup: true,
      groupId: 12,
      onAgentModelChange,
    });
    await act(async () => {
      await flushPromises();
    });

    expect(api.getAgentQuota).toHaveBeenCalledWith(405);
    expect(onAgentModelChange).toHaveBeenLastCalledWith(expect.objectContaining({
      isBot: true,
      state: 'ready',
      summary: expect.objectContaining({ model: 'gpt-5.6-terra' }),
    }));
  });

  it('keeps a late single-Agent quota response from replacing a multi-Agent hidden state', async () => {
    const onAgentModelChange = vi.fn();
    const slowQuota = deferred();
    api.getGroupInfo.mockImplementation((groupId) => Promise.resolve(groupId === 13 ? {
      group: { id: 13, name: '单 Agent', kind: 'agent_task' },
      members: [
        { user_id: 1, is_bot: false },
        { user_id: 405, is_bot: true },
      ],
    } : {
      group: { id: 14, name: '多 Agent', kind: 'agent_task' },
      members: [
        { user_id: 1, is_bot: false },
        { user_id: 405, is_bot: true },
        { user_id: 407, is_bot: true },
      ],
    }));
    api.getAgentQuota.mockReturnValueOnce(slowQuota.promise);

    await mountTopic(root, 'grp_13', {
      isGroup: true,
      groupId: 13,
      onAgentModelChange,
    });
    await act(async () => {
      await flushPromises();
    });
    expect(api.getAgentQuota).toHaveBeenCalledWith(405);

    await act(async () => {
      renderTopic(root, 'grp_14', {
        isGroup: true,
        groupId: 14,
        onAgentModelChange,
      });
      await flushPromises();
    });
    expect(onAgentModelChange).toHaveBeenLastCalledWith({
      isBot: false,
      state: 'hidden',
      summary: null,
    });

    await act(async () => {
      slowQuota.resolve({
        summary: { source: 'relay', model: 'stale-model', remaining_percent: 99 },
      });
      await flushPromises();
    });
    expect(onAgentModelChange).toHaveBeenLastCalledWith({
      isBot: false,
      state: 'hidden',
      summary: null,
    });
  });

  it('keeps out-of-order quota responses scoped while switching between single-Agent tasks', async () => {
    const onAgentModelChange = vi.fn();
    const firstQuota = deferred();
    const secondQuota = deferred();
    api.getGroupInfo.mockImplementation((groupId) => Promise.resolve({
      group: { id: groupId, name: `任务 ${groupId}`, kind: 'agent_task' },
      members: [
        { user_id: 1, is_bot: false },
        { user_id: groupId === 15 ? 405 : 407, is_bot: true },
      ],
    }));
    api.getAgentQuota.mockImplementation((uid) => (uid === 405 ? firstQuota.promise : secondQuota.promise));

    await mountTopic(root, 'grp_15', {
      isGroup: true,
      groupId: 15,
      onAgentModelChange,
    });
    await act(async () => {
      await flushPromises();
    });
    await act(async () => {
      renderTopic(root, 'grp_16', {
        isGroup: true,
        groupId: 16,
        onAgentModelChange,
      });
      await flushPromises();
    });

    await act(async () => {
      secondQuota.resolve({
        summary: { source: 'relay', model: 'MiniMax-M3', remaining_percent: 74 },
      });
      await flushPromises();
    });
    expect(onAgentModelChange).toHaveBeenLastCalledWith(expect.objectContaining({
      state: 'ready',
      summary: expect.objectContaining({ model: 'MiniMax-M3' }),
    }));

    await act(async () => {
      firstQuota.resolve({
        summary: { source: 'relay', model: 'stale-model', remaining_percent: 95 },
      });
      await flushPromises();
    });
    expect(onAgentModelChange).toHaveBeenLastCalledWith(expect.objectContaining({
      state: 'ready',
      summary: expect.objectContaining({ model: 'MiniMax-M3' }),
    }));
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
    const typingStatus = container.querySelector('.v3-peer-typing');
    expect(typingStatus?.getAttribute('role')).toBe('status');
    expect(typingStatus?.querySelector('.v3-peer-typing-label')).not.toBeNull();
    expect(typingStatus?.querySelector('.v3-avatar-col')).toBeNull();

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
    expect(container.querySelector('.v3-peer-typing')).toBeNull();
  });

  it('hides the transient runtime plan once the same plan is persisted in working messages', async () => {
    await mountTopic(root, 'p2p_1_2');

    const steps = [
      { text: '梳理 Boss 逻辑', status: 'in_progress' },
      { text: '试玩并打包', status: 'pending' },
    ];
    await act(async () => {
      wsHandler({
        data: {
          seq_id: 24,
          seq: 24,
          topic: 'p2p_1_2',
          from: 'usr2',
          content: { revision: 1, updatedAt: Date.now(), steps },
          type: 'runtime_plan',
          msg_type: 'runtime_plan',
        },
      });
    });
    expect(container.querySelector('.v3-runtime-plan-card')).not.toBeNull();

    await act(async () => {
      wsHandler({
        data: {
          seq_id: 25,
          seq: 25,
          topic: 'p2p_1_2',
          from: 'usr2',
          content: 'update_plan',
          type: 'tool_use',
          msg_type: 'tool_use',
          metadata: {
            id: 'plan-25',
            input: {
              steps: steps.map((step) => ({
                step: step.text,
                status: step.status,
              })),
            },
          },
        },
      });
    });

    expect(container.querySelector('.v3-runtime-plan-card')).toBeNull();
    expect(container.querySelector('[data-working-only="true"]')).not.toBeNull();
  });

  it('keeps the runtime plan visible when persisted steps have older statuses', async () => {
    await mountTopic(root, 'p2p_1_2');

    const runtimeSteps = [
      { text: '梳理 Boss 逻辑', status: 'in_progress' },
      { text: '试玩并打包', status: 'pending' },
    ];
    await act(async () => {
      wsHandler({
        data: {
          seq_id: 26,
          seq: 26,
          topic: 'p2p_1_2',
          from: 'usr2',
          content: { revision: 2, updatedAt: Date.now(), steps: runtimeSteps },
          type: 'runtime_plan',
          msg_type: 'runtime_plan',
        },
      });
      wsHandler({
        data: {
          seq_id: 27,
          seq: 27,
          topic: 'p2p_1_2',
          from: 'usr2',
          content: 'update_plan',
          type: 'tool_use',
          msg_type: 'tool_use',
          metadata: {
            id: 'plan-27',
            input: {
              steps: runtimeSteps.map((step) => ({
                step: step.text,
                status: 'pending',
              })),
            },
          },
        },
      });
    });

    expect(container.querySelector('.v3-runtime-plan-card')).not.toBeNull();
    expect(container.querySelector('[data-working-only="true"]')).not.toBeNull();
  });

  it('expands a runtime plan without crashing the conversation view', async () => {
    await mountTopic(root, 'p2p_1_2');

    await act(async () => {
      wsHandler({
        data: {
          seq_id: 23,
          seq: 23,
          topic: 'p2p_1_2',
          from: 'usr2',
          content: {
            revision: 1,
            updatedAt: Date.now(),
            steps: [
              { text: '定位白屏原因', status: 'completed' },
              { text: '验证计划展开', status: 'in_progress' },
            ],
          },
          type: 'runtime_plan',
          msg_type: 'runtime_plan',
        },
      });
    });

    const toggle = container.querySelector('.v3-runtime-plan-toggle');
    expect(toggle).not.toBeNull();
    expect(toggle.getAttribute('aria-expanded')).toBe('false');
    expect(toggle.getAttribute('aria-controls')).toMatch(/^runtime-plan-steps-/);
    expect(container.querySelector('.v3-runtime-plan-steps')).toBeNull();

    await act(async () => {
      Simulate.click(toggle);
    });

    const stepsRegion = container.querySelector('.v3-runtime-plan-steps');
    expect(toggle.getAttribute('aria-expanded')).toBe('true');
    expect(stepsRegion?.id).toBe(toggle.getAttribute('aria-controls'));
    expect(stepsRegion?.getAttribute('role')).toBe('region');
    expect(stepsRegion?.textContent).toContain('验证计划展开');
  });
});
