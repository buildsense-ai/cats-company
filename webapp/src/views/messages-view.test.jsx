import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

const { artifactRefreshPreviewObserved, feedbackConfirm, feedbackNotify } = vi.hoisted(() => ({
  artifactRefreshPreviewObserved: vi.fn(),
  feedbackConfirm: vi.fn(async () => true),
  feedbackNotify: vi.fn(),
}));

vi.mock('../components/feedback-system', () => ({
  useFeedback: () => ({ confirm: feedbackConfirm, notify: feedbackNotify }),
}));

vi.mock('../widgets/chat-message', () => ({
  __esModule: true,
  downloadableMediaURL: (url) => `${url}${url.includes('?') ? '&' : '?'}download=1`,
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
        data-sender-avatar={props.senderAvatarUrl || ''}
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
        {props.onCreateConversationShare && (
          <button
            type="button"
            className="mock-create-conversation-share"
            data-message-id={props.message?.id}
            onClick={props.onCreateConversationShare}
          >
            制作分享图
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
  FilePreviewPanel: function MockFilePreviewPanel({
    file,
    onBack,
    onClose,
    backgroundRef,
    onRemoteArtifactFrameChange,
    pendingRemoteArtifactFile,
    onRemoteArtifactRefreshReady,
    onRemoteArtifactRefreshFailed,
    onOpenRemoteArtifactFullscreen,
  }) {
    onRemoteArtifactFrameChange?.(file?.artifact_frame_binding || null);
    React.useEffect(() => {
      if (pendingRemoteArtifactFile) artifactRefreshPreviewObserved(pendingRemoteArtifactFile);
    }, [pendingRemoteArtifactFile]);
    return (
      <aside
        className="mock-file-preview"
        data-url={file?.url || ''}
        data-pending-url={pendingRemoteArtifactFile?.url || ''}
        data-background-class={backgroundRef?.current?.className || ''}
      >
        {file?.name || 'preview'}
        {onBack && (
          <button type="button" aria-label="返回云文件" onClick={onBack}>
            back
          </button>
        )}
        {pendingRemoteArtifactFile && (
          <>
            <button
              type="button"
              className="mock-ready-artifact-refresh"
              onClick={() => onRemoteArtifactRefreshReady?.(pendingRemoteArtifactFile)}
            >
              ready refresh
            </button>
            <button
              type="button"
              className="mock-fail-artifact-refresh"
              onClick={() => onRemoteArtifactRefreshFailed?.(pendingRemoteArtifactFile)}
            >
              fail refresh
            </button>
          </>
        )}
        {file?.artifact_id && (
          <button
            type="button"
            className="mock-open-artifact-fullscreen"
            onClick={() => onOpenRemoteArtifactFullscreen?.(file)}
          >
            open fullscreen
          </button>
        )}
        <button type="button" className="mock-close-preview" onClick={onClose}>close</button>
      </aside>
    );
  },
  createCloudArtifactPreviewFile: (artifact) => {
    const agentUID = Number(artifact.agent_uid || artifact.agentUid || artifact.artifact_agent_uid || 0);
    return {
      name: artifact.title || artifact.id,
      url: artifact.url,
      mime_type: 'text/html',
      artifact_id: artifact.id,
      publish_version: artifact.publish_version || null,
      artifact_agent_uid: agentUID || undefined,
      artifact_frame_binding: artifact.artifact_frame_binding
        ? { ...artifact.artifact_frame_binding, agentUid: agentUID || undefined }
        : null,
    };
  },
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

vi.mock('../utils/conversation-share-image', () => ({
  conversationShareMessageKey: (message, index = 0) => String(
    message?.id ?? message?.seq_id ?? `share-message-${index}`,
  ),
  conversationShareText: (message) => {
    const blocks = Array.isArray(message?.content_blocks) ? message.content_blocks : [];
    if (blocks.length > 0) {
      return blocks.map((block) => block?.text || block?.payload?.name || '').filter(Boolean).join('\n');
    }
    return typeof message?.content === 'string' ? message.content : message?.content?.text || '';
  },
  downloadConversationShareImage: vi.fn(async () => true),
  downloadConversationShareImages: vi.fn(async () => true),
  isMobileConversationShareBrowser: vi.fn(() => false),
  openConversationShareImageForManualSave: vi.fn(() => true),
  renderConversationShareImage: vi.fn(async () => ({
    dataUrl: 'data:image/png;base64,catsco-share',
    width: 1080,
    height: 1440,
  })),
}));

vi.mock('../api', () => ({
  api: {
    getMessages: vi.fn(),
    getFriends: vi.fn(),
    getAgents: vi.fn(),
    getAgentQuota: vi.fn(),
    getGroupInfo: vi.fn(),
    createChannelIdentityMobileLink: vi.fn(),
    createArtifactContextSnapshot: vi.fn(),
    invalidateArtifactContextSnapshot: vi.fn(),
    createArtifactTask: vi.fn(),
    getArtifactTask: vi.fn(),
    failArtifactTask: vi.fn(),
    sendMessage: vi.fn(),
    uploadFile: vi.fn(),
    createMobileUploadSession: vi.fn(),
    getMobileUploadSession: vi.fn(),
    getTutorialTasks: vi.fn(),
    getCloudWorkers: vi.fn(),
    getCloudArtifacts: vi.fn(),
    getAgentFiles: vi.fn(),
    getTopicFiles: vi.fn(),
    deleteCloudArtifact: vi.fn(),
    restoreCloudArtifact: vi.fn(),
  },
  wsSendMessage: vi.fn(),
  wsSendStreamCancel: vi.fn(),
  wsSendTyping: vi.fn(),
  wsSendRead: vi.fn(),
  wsSendArtifactResultReceipt: vi.fn(),
  onWSMessage: vi.fn(() => vi.fn()),
  updateTopicSeq: vi.fn(),
  getApiBaseURL: () => window.location.origin,
  resolveMediaURL: (url) => url,
}));

import MessagesView, {
  canonicalizeStructuredMentionText,
  collectStructuredMentionTargets,
  mergeOwnServerEcho,
  ImageGalleryPreview,
  mergeCloudWorkerSnapshots,
  reconcileRenderedGroupConsecutiveness,
  reconcileStructuredMentionSelections,
  shouldConvertPastedTextToDocument,
} from './messages-view';
import { TUTORIAL_TASKS } from '../widgets/tutorial-tasks';
import { api, onWSMessage, wsSendArtifactResultReceipt, wsSendStreamCancel } from '../api';
import { CHAT_ATTACHMENT_DRAG_FALLBACK_TYPE, CHAT_ATTACHMENT_DRAG_TYPE, writeChatAttachmentDrag } from '../chat-attachment-drag';
import {
  downloadConversationShareImage,
  downloadConversationShareImages,
  isMobileConversationShareBrowser,
  openConversationShareImageForManualSave,
  renderConversationShareImage,
} from '../utils/conversation-share-image';
import {
  ARTIFACT_PREVIEW_COORDINATION_CONTRACT,
  createArtifactPreviewMessage,
} from '../artifact-preview-coordinator';
import {
  createComposerDraftStore,
  writeComposerInputDraft,
} from '../utils/composer-draft-storage';

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
const ARTIFACT_REGISTRY_POLL_MS_FOR_TEST = 5000;
const artifactPreviewChannels = [];

describe('cloud-worker update snapshots', () => {
  it('preserves a known update while a refresh omits release metadata', () => {
    const previous = [{ uid: 42, latest_release: 'v1.5.3', update_available: true }];
    const next = [{ uid: 42, app_version: 'v1.5.2' }];
    expect(mergeCloudWorkerSnapshots(previous, next)).toEqual([{
      uid: 42,
      app_version: 'v1.5.2',
      latest_release: 'v1.5.3',
      update_available: true,
    }]);
  });

  it('uses a complete refresh snapshot when release metadata is present', () => {
    const previous = [{ uid: 42, latest_release: 'v1.5.3', update_available: true }];
    const next = [{ uid: 42, latest_release: 'v1.5.4', update_available: false }];
    expect(mergeCloudWorkerSnapshots(previous, next)).toEqual(next);
  });

  it('does not turn malformed worker entries into an update', () => {
    const previous = [{ uid: 42, latest_release: 'v1.5.3', update_available: true }];
    expect(mergeCloudWorkerSnapshots(previous, null)).toEqual([]);
    expect(mergeCloudWorkerSnapshots(previous, [{ username: 'missing-uid' }])).toEqual([
      { username: 'missing-uid' },
    ]);
  });
});

class MockArtifactPreviewChannel {
  constructor(name) {
    this.name = name;
    this.onmessage = null;
    this.posted = [];
    this.closed = false;
    this.onPost = null;
    artifactPreviewChannels.push(this);
  }

  postMessage(message) {
    this.posted.push(message);
    this.onPost?.(message);
  }

  receive(message) {
    this.onmessage?.({ data: message });
  }

  close() {
    this.closed = true;
  }
}

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

function pasteInto(textarea, { text = '', files = [] } = {}) {
  const event = new Event('paste', { bubbles: true, cancelable: true });
  const items = files.map((file) => ({ kind: 'file', getAsFile: () => file }));
  Object.defineProperty(event, 'clipboardData', {
    configurable: true,
    value: {
      files,
      items,
      getData: (type) => (type === 'text/plain' ? text : ''),
    },
  });
  textarea.dispatchEvent(event);
  return event;
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

function dispatchFrameMessage(source, origin, data) {
  const event = new Event('message');
  Object.defineProperties(event, {
    source: { value: source },
    origin: { value: origin },
    data: { value: data },
  });
  window.dispatchEvent(event);
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

  it('keeps the uid target while the selected token uses the Agent display name', () => {
    const selection = [{ target: 'usr42', label: '市场助手', start: 0, end: 5 }];
    const afterAppending = reconcileStructuredMentionSelections('@市场助手 ', '@市场助手 请处理', selection);
    const reconciled = reconcileStructuredMentionSelections('@市场助手 请处理', '请 @市场助手 请处理', afterAppending);

    expect(reconciled).toEqual([{
      target: 'usr42',
      label: '市场助手',
      start: 2,
      end: 7,
    }]);
    expect(collectStructuredMentionTargets('请 @市场助手 请处理', reconciled)).toEqual(['usr42']);
    expect(canonicalizeStructuredMentionText('请 @市场助手 请处理', reconciled)).toBe('请 @usr42 请处理');
  });

  it('matches a UID server echo to its display-name optimistic message', () => {
    const optimisticMessage = {
      id: 100,
      from_uid: 1,
      content: '@市场助手 请处理',
      _canonical_content: '@usr42 请处理',
      _pending: true,
    };
    const serverMessage = {
      id: 101,
      seq_id: 101,
      from_uid: 1,
      content: '@usr42 请处理',
    };

    expect(mergeOwnServerEcho([optimisticMessage], serverMessage, 1)).toEqual([serverMessage]);
    expect(mergeOwnServerEcho([optimisticMessage], serverMessage, 2)).toBeNull();
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

describe('ImageGalleryPreview', () => {
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
    document.querySelector('.oc-rich-image-gallery-preview')?.remove();
  });

  it('shows boundary controls, supports keyboard navigation, and restores the trigger focus', async () => {
    const trigger = document.createElement('button');
    trigger.type = 'button';
    document.body.appendChild(trigger);
    const triggerRef = { current: trigger };
    const items = [
      { id: 'one', payload: { url: '/uploads/images/one.png', name: 'one.png' } },
      { id: 'two', payload: { url: '/uploads/images/two.png', name: 'two.png' } },
      { id: 'three', payload: { url: '/uploads/images/three.png', name: 'three.png' } },
    ];
    let selectedIndex = 0;
    const render = () => root.render(
      <ImageGalleryPreview
        item={items[selectedIndex]}
        index={selectedIndex}
        items={items}
        triggerRef={triggerRef}
        onClose={() => root.render(null)}
        onChange={(nextIndex) => {
          selectedIndex = nextIndex;
          render();
        }}
      />,
    );

    await act(async () => render());
    expect(document.querySelector('[aria-label="上一张图片"]')).not.toBeNull();
    expect(document.querySelector('[aria-label="上一张图片"]')?.disabled).toBe(true);
    expect(document.querySelector('[aria-label="下一张图片"]')).not.toBeNull();
    expect(document.querySelector('[aria-label="下一张图片"]')?.disabled).toBe(false);
    const download = document.querySelector('a.oc-rich-media-preview-download');
    expect(download?.getAttribute('aria-label')).toBe('下载图片 one.png');
    expect(download?.getAttribute('href')).toBe('/uploads/images/one.png?download=1');
    expect(download?.getAttribute('download')).toBe('one.png');

    await act(async () => {
      document.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowRight', bubbles: true }));
    });
    expect(document.querySelector('.oc-rich-image-preview-media')?.getAttribute('src')).toBe('/uploads/images/two.png');
    expect(document.querySelector('a.oc-rich-media-preview-download')?.getAttribute('href'))
      .toBe('/uploads/images/two.png?download=1');
    expect(document.querySelector('[aria-label="上一张图片"]')).not.toBeNull();
    expect(document.querySelector('[aria-label="上一张图片"]')?.disabled).toBe(false);
    expect(document.querySelector('[aria-label="下一张图片"]')?.disabled).toBe(false);

    await act(async () => {
      document.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowRight', bubbles: true }));
    });
    expect(document.querySelector('.oc-rich-image-preview-media')?.getAttribute('src')).toBe('/uploads/images/three.png');
    expect(document.querySelector('[aria-label="上一张图片"]')?.disabled).toBe(false);
    expect(document.querySelector('[aria-label="下一张图片"]')?.disabled).toBe(true);

    await act(async () => {
      document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    });
    expect(document.querySelector('.oc-rich-image-gallery-preview')).toBeNull();
    expect(document.activeElement).toBe(trigger);
    trigger.remove();
  });
});

describe('long pasted text detection', () => {
  it('keeps ordinary multi-paragraph text inline', () => {
    expect(shouldConvertPastedTextToDocument('一段普通文字\n\n再补充一段。')).toBe(false);
  });

  it('recognizes very long text and substantial multi-line text', () => {
    expect(shouldConvertPastedTextToDocument('长'.repeat(4000))).toBe(true);
    expect(shouldConvertPastedTextToDocument(Array.from({ length: 60 }, () => '一行较长的内容'.repeat(6)).join('\n'))).toBe(true);
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
    sessionStorage.clear();
    artifactPreviewChannels.length = 0;
    vi.stubGlobal('BroadcastChannel', MockArtifactPreviewChannel);
    api.getMessages.mockResolvedValue({ messages: [] });
    api.getFriends.mockResolvedValue({ friends: [] });
    api.getAgents.mockResolvedValue({ agents: [] });
    api.getAgentQuota.mockResolvedValue({ configured: false, shared: true });
    api.createChannelIdentityMobileLink.mockResolvedValue({ qr_value: 'https://app.catsco.cc/mobile-link' });
    api.getGroupInfo.mockResolvedValue({ members: [], group: null });
    api.createArtifactContextSnapshot.mockResolvedValue({
      contract_version: 'catsco.artifact-context-ref.v1',
      context_ref: `acr_${'x'.repeat(43)}`,
      expires_at: '2026-08-14T12:05:00Z',
      revision: 1,
    });
    api.invalidateArtifactContextSnapshot.mockResolvedValue({ ok: true });
    api.createArtifactTask.mockResolvedValue({});
    api.getArtifactTask.mockResolvedValue({});
    api.failArtifactTask.mockResolvedValue({ ok: true });
    api.sendMessage.mockResolvedValue({ seq_id: 100 });
    api.getTutorialTasks.mockResolvedValue({ tasks: [], limit: 6 });
    api.getCloudWorkers.mockResolvedValue({ workers: [] });
    api.getCloudArtifacts.mockResolvedValue({ artifacts: [] });
    feedbackConfirm.mockReset();
    feedbackConfirm.mockResolvedValue(true);
    feedbackNotify.mockReset();
    api.getAgentFiles.mockResolvedValue({ files: [], has_more: false, next_before_id: 0 });
    api.getTopicFiles.mockResolvedValue({ files: [], has_more: false, next_before_id: 0 });
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
    isMobileConversationShareBrowser.mockReturnValue(false);
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
    vi.unstubAllGlobals();
    window.IntersectionObserver = originalIntersectionObserver;
    container.remove();
    vi.restoreAllMocks();
    vi.clearAllMocks();
  });

  it('keeps the message view mountable when browser storage is blocked', async () => {
    const originalStorageDescriptor = Object.getOwnPropertyDescriptor(globalThis, 'localStorage');
    Object.defineProperty(globalThis, 'localStorage', {
      configurable: true,
      get() {
        throw new DOMException('Storage access is blocked', 'SecurityError');
      },
    });

    try {
      await mountTopic(root, 'p2p_1_2');
      expect(container.querySelector('textarea')).toBeTruthy();
    } finally {
      Object.defineProperty(globalThis, 'localStorage', originalStorageDescriptor);
    }
  });

  it('loads around a search result, highlights its anchor, and returns to search', async () => {
    const onBackToSearch = vi.fn();
    api.getMessages.mockResolvedValueOnce({
      messages: [{
        id: 42,
        seq_id: 42,
        topic_id: 'p2p_1_2',
        from_uid: 2,
        type: 'text',
        content: 'target search result',
        created_at: '2026-07-30T12:00:00Z',
      }],
    });

    await mountTopic(root, 'p2p_1_2', {
      messageLocationRequest: { topicId: 'p2p_1_2', messageId: 42, requestId: 1 },
      onBackToSearch,
    });

    expect(api.getMessages).toHaveBeenCalledWith(
      'p2p_1_2',
      expect.any(Number),
      0,
      false,
      0,
      expect.objectContaining({ aroundId: 42, signal: expect.any(AbortSignal) }),
    );
    const anchor = container.querySelector('[data-search-message-id="42"]');
    expect(anchor).not.toBeNull();
    expect(anchor.classList.contains('cc-message-search-hit')).toBe(true);
    expect(anchor.scrollIntoView).toHaveBeenCalledWith({ behavior: 'smooth', block: 'center' });

    const backButton = [...container.querySelectorAll('button')]
      .find((button) => button.textContent.includes('返回搜索结果'));
    await act(async () => backButton.click());
    expect(onBackToSearch).toHaveBeenCalledTimes(1);
  });

  it('selects existing conversation messages and exports a branded sharing image', async () => {
    api.getMessages.mockResolvedValueOnce({
      messages: [
        {
          id: 101,
          seq_id: 101,
          topic_id: 'p2p_1_2',
          from_uid: 1,
          type: 'text',
          content: '请整理本周项目进度',
          created_at: '2026-08-13T02:00:00Z',
        },
        {
          id: 102,
          seq_id: 102,
          topic_id: 'p2p_1_2',
          from_uid: 2,
          type: 'text',
          content_blocks: [
            { type: 'text', text: '已经整理完成，附件如下。' },
            { type: 'file', payload: { name: 'weekly-brief.pdf' } },
          ],
          created_at: '2026-08-13T02:01:00Z',
        },
      ],
    });
    const previousTheme = document.documentElement.dataset.theme;
    const previousLiquidVariant = document.documentElement.dataset.liquidVariant;
    document.documentElement.dataset.theme = 'liquid';
    document.documentElement.dataset.liquidVariant = 'green';

    try {
      await mountTopic(root, 'p2p_1_2', {
        topicName: '项目周报',
      });
      await act(async () => { await flushPromises(); });

      const shareTrigger = container.querySelector('.mock-chat-message[data-message-id="102"] .mock-create-conversation-share');
      await act(async () => {
        shareTrigger.click();
        await Promise.resolve();
      });

      const toolbar = container.querySelector('[aria-label="对话分享图选择"]');
      expect(toolbar?.textContent).toContain('已选 1 条');
      expect(container.querySelectorAll('.cc-message-search-hit')).toHaveLength(0);

      const selectableCards = container.querySelectorAll('.cc-message-anchor.is-conversation-share-selectable');
      expect(selectableCards).toHaveLength(2);
      expect(selectableCards[1].classList.contains('is-conversation-share-selected')).toBe(true);
      const selectableToggles = container.querySelectorAll('button[aria-label^="选择消息"]');
      expect(selectableToggles).toHaveLength(1);

      const generateButton = [...toolbar.querySelectorAll('button')]
        .find((button) => button.textContent.includes('生成分享图'));
      generateButton.focus();
      await act(async () => {
        generateButton.click();
        await flushPromises();
      });
      expect(renderConversationShareImage).toHaveBeenCalledWith(expect.objectContaining({
        topicName: '项目周报',
        theme: 'liquid-green',
        items: [expect.objectContaining({
          senderName: '项目周报',
          message: expect.objectContaining({ id: 102 }),
        })],
      }));

      const preview = document.body.querySelector('[role="dialog"][aria-labelledby="conversation-share-preview-title"]');
      expect(preview?.querySelector('img')?.getAttribute('src')).toBe('data:image/png;base64,catsco-share');
      const closeButton = preview.querySelector('button[aria-label="关闭分享图预览"]');
      expect(document.activeElement).toBe(closeButton);
      const downloadButton = [...preview.querySelectorAll('button')]
        .find((button) => button.textContent.includes('下载 PNG'));
      await act(async () => {
        document.dispatchEvent(new KeyboardEvent('keydown', {
          key: 'Tab',
          shiftKey: true,
          bubbles: true,
          cancelable: true,
        }));
      });
      expect(document.activeElement).toBe(downloadButton);
      await act(async () => {
        downloadButton.click();
        await flushPromises();
      });
      expect(downloadConversationShareImage).toHaveBeenCalledWith('data:image/png;base64,catsco-share');
      await act(async () => {
        document.dispatchEvent(new KeyboardEvent('keydown', {
          key: 'Escape',
          bubbles: true,
          cancelable: true,
        }));
        await flushPromises();
      });
      expect(document.body.querySelector('[role="dialog"][aria-labelledby="conversation-share-preview-title"]')).toBeNull();
      expect(document.activeElement).toBe(generateButton);
    } finally {
      if (previousTheme) document.documentElement.dataset.theme = previousTheme;
      else delete document.documentElement.dataset.theme;
      if (previousLiquidVariant) document.documentElement.dataset.liquidVariant = previousLiquidVariant;
      else delete document.documentElement.dataset.liquidVariant;
    }
  });

  it('allows up to 50 messages and rejects the 51st selection', async () => {
    api.getMessages.mockResolvedValueOnce({
      messages: Array.from({ length: 51 }, (_, index) => ({
        id: index + 1,
        seq_id: index + 1,
        topic_id: 'p2p_1_2',
        from_uid: index % 2 === 0 ? 1 : 2,
        type: 'text',
        content: `消息 ${index + 1}`,
      })),
    });
    await mountTopic(root, 'p2p_1_2');
    await act(async () => { await flushPromises(); });

    await act(async () => {
      container.querySelector('.mock-chat-message[data-message-id="1"] .mock-create-conversation-share').click();
      await Promise.resolve();
    });

    const toolbar = container.querySelector('[aria-label="对话分享图选择"]');
    const toggles = container.querySelectorAll('.cc-conversation-share-message-toggle');
    expect(toggles).toHaveLength(51);

    await act(async () => {
      for (let index = 1; index < 50; index += 1) {
        toggles[index].click();
      }
    });
    expect(toolbar?.textContent).toContain('已选 50 条');

    await act(async () => toggles[50].click());
    expect(toolbar?.textContent).toContain('已选 50 条');
    expect(toolbar?.textContent).toContain('一次最多选择 50 条消息。');
  });

  it('previews and downloads every generated share-image page', async () => {
    api.getMessages.mockResolvedValueOnce({
      messages: [{
        id: 201,
        seq_id: 201,
        topic_id: 'p2p_1_2',
        from_uid: 2,
        type: 'text',
        content: '一条较长的消息',
      }],
    });
    renderConversationShareImage.mockResolvedValueOnce({
      dataUrl: 'data:image/png;base64,page-one',
      pages: [
        { dataUrl: 'data:image/png;base64,page-one', width: 720, height: 9600, page: 1, total: 2 },
        { dataUrl: 'data:image/png;base64,page-two', width: 720, height: 2200, page: 2, total: 2 },
      ],
    });

    await mountTopic(root, 'p2p_1_2');
    await act(async () => { await flushPromises(); });

    const shareTrigger = container.querySelector('.mock-create-conversation-share');
    await act(async () => shareTrigger.click());
    const toolbar = container.querySelector('[aria-label="对话分享图选择"]');
    const generateButton = [...toolbar.querySelectorAll('button')]
      .find((button) => button.textContent.includes('生成分享图'));
    await act(async () => {
      generateButton.click();
      await flushPromises();
    });

    const preview = document.body.querySelector('[role="dialog"][aria-labelledby="conversation-share-preview-title"]');
    expect(preview?.textContent).toContain('共 2 张');
    expect(preview?.querySelector('img')?.getAttribute('src')).toBe('data:image/png;base64,page-one');

    const nextButton = preview.querySelector('button[aria-label="查看下一张分享图"]');
    await act(async () => nextButton.click());
    expect(preview?.querySelector('img')?.getAttribute('src')).toBe('data:image/png;base64,page-two');

    const downloadAllButton = [...preview.querySelectorAll('button')]
      .find((button) => button.textContent.includes('下载全部图片（ZIP）'));
    expect(downloadAllButton?.textContent).toBe('下载全部图片（ZIP）');
    await act(async () => {
      downloadAllButton.click();
      await flushPromises();
    });
    expect(downloadConversationShareImages).toHaveBeenCalledWith([
      'data:image/png;base64,page-one',
      'data:image/png;base64,page-two',
    ]);
  });

  it('labels multi-page mobile sharing as a system share action', async () => {
    api.getMessages.mockResolvedValueOnce({
      messages: [{
        id: 202,
        seq_id: 202,
        topic_id: 'p2p_1_2',
        from_uid: 2,
        type: 'text',
        content: '两张分享图',
      }],
    });
    isMobileConversationShareBrowser.mockReturnValue(true);
    renderConversationShareImage.mockResolvedValueOnce({
      dataUrl: 'data:image/png;base64:page-one',
      pages: [
        { dataUrl: 'data:image/png;base64:page-one', width: 720, height: 1200, page: 1, total: 2 },
        { dataUrl: 'data:image/png;base64:page-two', width: 720, height: 1200, page: 2, total: 2 },
      ],
    });

    await mountTopic(root, 'p2p_1_2');
    await act(async () => { await flushPromises(); });
    await act(async () => container.querySelector('.mock-create-conversation-share').click());

    const toolbar = container.querySelector('[aria-label="对话分享图选择"]');
    const generateButton = [...toolbar.querySelectorAll('button')]
      .find((button) => button.textContent.includes('生成分享图'));
    await act(async () => {
      generateButton.click();
      await flushPromises();
    });

    const preview = document.body.querySelector('[role="dialog"][aria-labelledby="conversation-share-preview-title"]');
    expect([...preview.querySelectorAll('button')]
      .find((button) => button.textContent.includes('系统分享全部图片'))?.textContent)
      .toBe('系统分享全部图片');
  });

  it('shows a visible recovery message when image saving cannot start', async () => {
    api.getMessages.mockResolvedValueOnce({
      messages: [{
        id: 301,
        seq_id: 301,
        topic_id: 'p2p_1_2',
        from_uid: 2,
        type: 'text',
        content: '请保存这张分享图',
      }],
    });
    downloadConversationShareImage.mockResolvedValueOnce(false);

    await mountTopic(root, 'p2p_1_2');
    await act(async () => { await flushPromises(); });
    await act(async () => container.querySelector('.mock-create-conversation-share').click());

    const toolbar = container.querySelector('[aria-label="对话分享图选择"]');
    const generateButton = [...toolbar.querySelectorAll('button')]
      .find((button) => button.textContent.includes('生成分享图'));
    await act(async () => {
      generateButton.click();
      await flushPromises();
    });

    const preview = document.body.querySelector('[role="dialog"][aria-labelledby="conversation-share-preview-title"]');
    const downloadButton = [...preview.querySelectorAll('button')]
      .find((button) => button.textContent.includes('下载 PNG'));
    await act(async () => {
      downloadButton.click();
      await flushPromises();
    });

    expect(preview?.textContent).toContain('无法启动图片保存。请在新标签页中打开图片后，使用浏览器的保存功能。');
    const manualSaveButton = [...preview.querySelectorAll('button')]
      .find((button) => button.textContent.includes('在新标签页打开图片'));
    await act(async () => {
      manualSaveButton.click();
    });
    expect(openConversationShareImageForManualSave).toHaveBeenCalledWith('data:image/png;base64,catsco-share');
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

  it('restores an unsent draft after returning from SkillHub', async () => {
    const composerDraftStore = {
      inputDrafts: new Map(),
      structuredMentionDrafts: new Map(),
      attachmentDrafts: new Map(),
    };

    await mountTopic(root, 'p2p_1_2', { composerDraftStore });

    const textarea = container.querySelector('textarea.v3-composer-input');
    await act(async () => {
      typeDraft(textarea, 'keep this draft while browsing skills');
    });

    await act(async () => {
      root.render(<main data-testid="skillhub-view">SkillHub</main>);
      await Promise.resolve();
    });

    await mountTopic(root, 'p2p_1_2', { composerDraftStore });

    expect(container.querySelector('textarea.v3-composer-input').value)
      .toBe('keep this draft while browsing skills');
  });

  it('persists a real composer draft as soon as its text changes', async () => {
    const composerDraftStore = {
      inputDrafts: new Map(),
      structuredMentionDrafts: new Map(),
      attachmentDrafts: new Map(),
      persist: vi.fn(),
    };

    await mountTopic(root, 'p2p_1_2', { composerDraftStore });

    const textarea = container.querySelector('textarea.v3-composer-input');
    await act(async () => {
      typeDraft(textarea, 'persist this before opening SkillHub');
    });

    expect(composerDraftStore.inputDrafts.get('p2p_1_2'))
      .toBe('persist this before opening SkillHub');
    expect(composerDraftStore.persist).toHaveBeenCalled();
  });

  it('reflects a shared-store clear in a composer that is already mounted', async () => {
    const composerDraftStore = createComposerDraftStore('messages-subscription-test');
    await mountTopic(root, 'p2p_1_2', { composerDraftStore });
    const textarea = container.querySelector('textarea.v3-composer-input');

    await act(async () => {
      typeDraft(textarea, '待清除的对话草稿');
      await Promise.resolve();
    });
    expect(textarea.value).toBe('待清除的对话草稿');

    await act(async () => {
      writeComposerInputDraft(composerDraftStore, 'p2p_1_2', '');
      await Promise.resolve();
    });

    expect(textarea.value).toBe('');
  });

  it('does not let an old upload resurrect a sent conversation draft', async () => {
    const attachmentDrafts = new Map();
    const inputDrafts = new Map();
    const composerDraftStore = {
      inputDrafts,
      structuredMentionDrafts: new Map(),
      attachmentDrafts,
      persist: vi.fn(),
    };
    let resolveUpload;
    api.uploadFile.mockReturnValueOnce(new Promise((resolve) => {
      resolveUpload = resolve;
    }));

    const image = new File(['draft image'], 'draft.png', { type: 'image/png' });
    await mountTopic(root, 'p2p_1_2', { composerDraftStore });
    const imageInput = container.querySelector('input[accept*="image/jpeg"]');
    await act(async () => {
      Simulate.change(imageInput, {
        target: {
          files: [image],
          value: 'C:\\fakepath\\draft.png',
        },
      });
      await Promise.resolve();
    });
    await vi.waitFor(() => expect(api.uploadFile).toHaveBeenCalledTimes(1));

    await act(async () => {
      root.unmount();
    });
    root = createRoot(container);
    await mountTopic(root, 'p2p_1_2', { composerDraftStore });

    const textarea = container.querySelector('textarea.v3-composer-input');
    await act(async () => {
      typeDraft(textarea, '发送后不恢复旧附件');
      await Promise.resolve();
    });
    await act(async () => {
      Simulate.click(container.querySelector('button[aria-label="发送"]'));
      await flushPromises();
    });
    await vi.waitFor(() => expect(api.sendMessage).toHaveBeenCalledTimes(1));

    expect(inputDrafts.get('p2p_1_2')).toBeUndefined();
    expect(attachmentDrafts.get('p2p_1_2')).toBeUndefined();

    await act(async () => {
      resolveUpload({
        file_key: 'draft.png',
        url: '/uploads/images/draft.png',
        name: image.name,
        size: image.size,
        mime_type: image.type,
      });
      await flushPromises();
    });

    expect(inputDrafts.get('p2p_1_2')).toBeUndefined();
    expect(attachmentDrafts.get('p2p_1_2')).toBeUndefined();
  });

  it('removes the current conversation draft after a successful send', async () => {
    const composerDraftStore = createComposerDraftStore('sent-conversation-draft');
    await mountTopic(root, 'p2p_1_2', { composerDraftStore });
    await act(async () => {
      typeDraft(container.querySelector('textarea.v3-composer-input'), '发送后清空');
      await Promise.resolve();
    });
    await act(async () => {
      Simulate.click(container.querySelector('button[aria-label="发送"]'));
      await flushPromises();
    });
    await vi.waitFor(() => expect(api.sendMessage).toHaveBeenCalledTimes(1));

    expect(createComposerDraftStore('sent-conversation-draft').getInputDraft('p2p_1_2')).toBe('');
    expect(sessionStorage.getItem('catsco_composer_drafts:v1:sent-conversation-draft')).toBeNull();
    expect(localStorage.getItem('catsco_composer_drafts:v1:sent-conversation-draft')).toBeNull();
  });

  it('does not restore stale long-paste text after a newer send', async () => {
    const attachmentDrafts = new Map();
    const inputDrafts = new Map();
    const composerDraftStore = {
      inputDrafts,
      structuredMentionDrafts: new Map(),
      attachmentDrafts,
      persist: vi.fn(),
    };
    let rejectUpload;
    api.uploadFile.mockReturnValueOnce(new Promise((_resolve, reject) => {
      rejectUpload = reject;
    }));

    await mountTopic(root, 'p2p_1_2', { composerDraftStore });
    const oldTextarea = container.querySelector('textarea.v3-composer-input');
    pasteInto(oldTextarea, { text: '长文本'.repeat(1400) });
    await vi.waitFor(() => expect(api.uploadFile).toHaveBeenCalledTimes(1));

    await act(async () => {
      root.unmount();
    });
    root = createRoot(container);
    await mountTopic(root, 'p2p_1_2', { composerDraftStore });

    const textarea = container.querySelector('textarea.v3-composer-input');
    await act(async () => {
      typeDraft(textarea, '发送后不恢复旧粘贴文本');
      await Promise.resolve();
    });
    await act(async () => {
      Simulate.click(container.querySelector('button[aria-label="发送"]'));
      await flushPromises();
    });
    await vi.waitFor(() => expect(api.sendMessage).toHaveBeenCalledTimes(1));

    await act(async () => {
      rejectUpload(new Error('upload cancelled'));
      await flushPromises();
    });

    expect(inputDrafts.get('p2p_1_2')).toBeUndefined();
    expect(attachmentDrafts.get('p2p_1_2')).toBeUndefined();
  });

  it('keeps a newer conversation draft written while the previous send is in flight', async () => {
    const inputDrafts = new Map();
    const composerDraftStore = {
      inputDrafts,
      structuredMentionDrafts: new Map(),
      attachmentDrafts: new Map(),
      persist: vi.fn(),
    };
    let resolveSend;
    api.sendMessage.mockReturnValueOnce(new Promise((resolve) => {
      resolveSend = resolve;
    }));

    await mountTopic(root, 'p2p_1_2', { composerDraftStore });
    await act(async () => {
      typeDraft(container.querySelector('textarea.v3-composer-input'), '第一条消息');
      await Promise.resolve();
    });
    await act(async () => {
      Simulate.click(container.querySelector('button[aria-label="发送"]'));
      await Promise.resolve();
    });
    await vi.waitFor(() => expect(api.sendMessage).toHaveBeenCalledTimes(1));

    await act(async () => {
      root.unmount();
    });
    root = createRoot(container);
    await mountTopic(root, 'p2p_1_2', { composerDraftStore });
    await act(async () => {
      typeDraft(container.querySelector('textarea.v3-composer-input'), '发送期间的新消息');
      await Promise.resolve();
    });

    await act(async () => {
      resolveSend({ seq_id: 120 });
      await flushPromises();
    });

    expect(inputDrafts.get('p2p_1_2')).toBe('发送期间的新消息');
  });

  it('does not clear a newer draft written before an old send reaches its clear step', async () => {
    const inputDrafts = new Map();
    const composerDraftStore = {
      inputDrafts,
      structuredMentionDrafts: new Map(),
      attachmentDrafts: new Map(),
      persist: vi.fn(),
    };
    let resolveSend;
    let resolvePoll;
    api.createMobileUploadSession.mockResolvedValueOnce({
      session_id: 'send-delay',
      upload_url: '/mobile-upload/send-delay',
    });
    api.getMobileUploadSession.mockReturnValueOnce(new Promise((resolve) => {
      resolvePoll = resolve;
    }));
    api.sendMessage.mockReturnValueOnce(new Promise((resolve) => {
      resolveSend = resolve;
    }));

    await mountTopic(root, 'p2p_1_2', { composerDraftStore });
    await act(async () => {
      typeDraft(container.querySelector('textarea.v3-composer-input'), '旧消息');
      await Promise.resolve();
    });
    await openPhoneUploadFromComposer(container);
    await act(async () => {
      Simulate.click(container.querySelector('button[aria-label="发送"]'));
      await Promise.resolve();
    });
    await vi.waitFor(() => expect(api.getMobileUploadSession).toHaveBeenCalledTimes(1));
    await act(async () => {
      root.unmount();
    });
    root = createRoot(container);
    await mountTopic(root, 'p2p_1_2', { composerDraftStore });
    await act(async () => {
      typeDraft(container.querySelector('textarea.v3-composer-input'), '切换期间的新消息');
      await Promise.resolve();
    });
    await act(async () => {
      resolvePoll({ session_id: 'send-delay', files: [] });
      await Promise.resolve();
    });
    await vi.waitFor(() => expect(api.sendMessage).toHaveBeenCalledTimes(1));

    expect(inputDrafts.get('p2p_1_2')).toBe('切换期间的新消息');
    await act(async () => {
      resolveSend({ seq_id: 121 });
      await flushPromises();
    });
    expect(inputDrafts.get('p2p_1_2')).toBe('切换期间的新消息');
  });

  it('includes a phone upload that resolves while Send is waiting for the final poll', async () => {
    const attachmentDrafts = new Map();
    const composerDraftStore = {
      inputDrafts: new Map(),
      structuredMentionDrafts: new Map(),
      attachmentDrafts,
      phoneUploadSessions: new Map(),
      persist: vi.fn(),
    };
    const pendingPoll = deferred();
    api.createMobileUploadSession.mockResolvedValueOnce({
      session_id: 'send-file',
      upload_url: '/mobile-upload/send-file',
    });
    api.getMobileUploadSession.mockReturnValueOnce(pendingPoll.promise);

    await mountTopic(root, 'p2p_1_2', { composerDraftStore });
    await act(async () => {
      typeDraft(container.querySelector('textarea.v3-composer-input'), '请把手机文件一起发送');
      await Promise.resolve();
    });
    await openPhoneUploadFromComposer(container);
    await vi.waitFor(() => expect(api.getMobileUploadSession).toHaveBeenCalledTimes(1));

    await act(async () => {
      Simulate.click(container.querySelector('button[aria-label="发送"]'));
      await Promise.resolve();
    });
    expect(api.sendMessage).not.toHaveBeenCalled();

    await act(async () => {
      pendingPoll.resolve({
        session_id: 'send-file',
        files: [{
          file_key: 'phone-report.pdf',
          url: '/uploads/files/phone-report.pdf',
          name: 'phone-report.pdf',
          size: 42,
          type: 'file',
        }],
      });
      await flushPromises();
    });
    await vi.waitFor(() => expect(api.sendMessage).toHaveBeenCalledTimes(1));

    const payload = api.sendMessage.mock.calls[0][1];
    expect(payload.content_blocks).toEqual(expect.arrayContaining([
      expect.objectContaining({
        type: 'file',
        payload: expect.objectContaining({ file_key: 'phone-report.pdf' }),
      }),
    ]));
    expect(attachmentDrafts.get('p2p_1_2')).toBeUndefined();
  });

  it('keeps newer typing after a failed long paste completes across a remount', async () => {
    const inputDrafts = new Map();
    const composerDraftStore = {
      inputDrafts,
      structuredMentionDrafts: new Map(),
      attachmentDrafts: new Map(),
      phoneUploadSessions: new Map(),
      persist: vi.fn(),
    };
    const pendingUpload = deferred();
    api.uploadFile.mockReturnValueOnce(pendingUpload.promise);

    await mountTopic(root, 'p2p_1_2', { composerDraftStore });
    pasteInto(container.querySelector('textarea.v3-composer-input'), {
      text: '长文本'.repeat(1400),
    });
    await vi.waitFor(() => expect(api.uploadFile).toHaveBeenCalledTimes(1));

    await act(async () => root.unmount());
    root = createRoot(container);
    await mountTopic(root, 'p2p_1_2', { composerDraftStore });
    await act(async () => {
      typeDraft(container.querySelector('textarea.v3-composer-input'), '切换后继续输入的新草稿');
      await Promise.resolve();
    });

    await act(async () => {
      pendingUpload.reject(new Error('upload cancelled'));
      await flushPromises();
    });

    expect(inputDrafts.get('p2p_1_2')).toBe('切换后继续输入的新草稿');
    expect(container.querySelector('textarea.v3-composer-input').value)
      .toBe('切换后继续输入的新草稿');
  });

  it('resumes a persisted phone upload session after the composer remounts', async () => {
    const attachmentDrafts = new Map();
    const phoneUploadSessions = new Map();
    const composerDraftStore = {
      inputDrafts: new Map(),
      structuredMentionDrafts: new Map(),
      attachmentDrafts,
      phoneUploadSessions,
      persist: vi.fn(),
    };
    api.createMobileUploadSession.mockResolvedValueOnce({
      session_id: 'resume-file',
      upload_url: '/mobile-upload/resume-file',
    });

    await mountTopic(root, 'p2p_1_2', { composerDraftStore });
    await openPhoneUploadFromComposer(container);
    await vi.waitFor(() => expect(phoneUploadSessions.get('p2p_1_2')).toMatchObject({
      session_id: 'resume-file',
    }));

    await act(async () => root.unmount());
    root = createRoot(container);
    api.getMobileUploadSession.mockResolvedValueOnce({
      session_id: 'resume-file',
      files: [{
        file_key: 'resume-file.txt',
        url: '/uploads/files/resume-file.txt',
        name: 'resume-file.txt',
        size: 18,
        type: 'file',
      }],
    });
    await mountTopic(root, 'p2p_1_2', { composerDraftStore });
    await vi.waitFor(() => expect(attachmentDrafts.get('p2p_1_2')).toHaveLength(1));

    expect(attachmentDrafts.get('p2p_1_2')[0].content.payload.file_key)
      .toBe('resume-file.txt');
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

  it('resolves group sender identity when message uid is a string', async () => {
    api.getMessages.mockResolvedValueOnce({
      messages: [
        { id: 72, seq_id: 72, topic_id: 'grp_9', from_uid: '2', type: 'text', content: '来自助手的消息' },
      ],
    });
    api.getGroupInfo.mockResolvedValueOnce({
      group: { id: 9, name: '字符串 UID 群聊' },
      members: [{ user_id: 1, display_name: 'Me' }, {
        user_id: 2,
        display_name: 'Design Agent',
        avatar_url: '/uploads/design-agent.png',
        is_bot: true,
      }],
    });

    await mountTopic(root, 'grp_9', { isGroup: true, groupId: 9 });
    await act(async () => {
      await flushPromises();
    });

    const message = container.querySelector('.mock-chat-message[data-message-id="72"]');
    expect(message?.dataset.senderName).toBe('Design Agent');
    expect(message?.dataset.senderAvatar).toBe('/uploads/design-agent.png');
  });

  it('keeps sender metadata on the first visible reply when thinking is hidden', async () => {
    localStorage.setItem('cc_show_thinking', 'false');
    api.getMessages.mockResolvedValueOnce({
      messages: [
        { id: 73, seq_id: 73, topic_id: 'p2p_1_2', from_uid: 2, type: 'thinking', content: '内部过程', created_at: '2026-07-01T00:00:00Z' },
        { id: 74, seq_id: 74, topic_id: 'p2p_1_2', from_uid: 2, type: 'text', content: '最终回复', created_at: '2026-07-01T00:00:01Z' },
      ],
    });
    api.getFriends.mockResolvedValueOnce({
      friends: [{ id: 2, display_name: 'Agent', avatar_url: '/uploads/agent.png', is_bot: true }],
    });

    await mountTopic(root, 'p2p_1_2', { topicName: 'Agent', topicAvatarUrl: '/uploads/agent.png' });
    await act(async () => {
      await flushPromises();
    });

    const message = container.querySelector('.mock-chat-message[data-message-id="74"]');
    expect(message?.dataset.senderName).toBe('Agent');
    expect(message?.dataset.senderAvatar).toBe('/uploads/agent.png');
    expect(message?.dataset.consecutive).toBe('false');
  });

  it('keeps sender metadata when the first Agent narrative is folded into a tool trace', async () => {
    api.getMessages.mockResolvedValueOnce({
      messages: [
        {
          id: 741,
          topic_id: 'p2p_1_2',
          from_uid: 1,
          type: 'text',
          content: '整理近期公开的金融行业新闻资讯，生成一份市场洞察摘要。',
          created_at: '2026-08-18T09:00:00Z',
        },
        {
          id: 742,
          topic_id: 'p2p_1_2',
          from_uid: 2,
          role: 'assistant',
          type: 'text',
          content: '收到。我会先核实公开来源，再生成市场洞察摘要。',
          created_at: '2026-08-18T09:00:01Z',
        },
        {
          id: 743,
          topic_id: 'p2p_1_2',
          from_uid: 2,
          type: 'tool_use',
          content: 'execute_shell',
          metadata: { id: 'tool-743' },
          created_at: '2026-08-18T09:00:02Z',
        },
      ],
    });
    api.getFriends.mockResolvedValueOnce({
      friends: [{
        id: 2,
        display_name: '市场洞察助理',
        avatar_url: '/uploads/market-agent.png',
        is_bot: true,
      }],
    });

    await mountTopic(root, 'p2p_1_2', {
      topicName: '市场洞察助理',
      topicAvatarUrl: '/uploads/market-agent.png',
    });
    await act(async () => {
      await flushPromises();
    });

    const workingMessage = container.querySelector('[data-working-only="true"]');
    expect(workingMessage?.dataset.workingMessageIds).toBe('742,743');
    expect(workingMessage?.dataset.senderName).toBe('市场洞察助理');
    expect(workingMessage?.dataset.senderAvatar).toBe('/uploads/market-agent.png');
    expect(workingMessage?.dataset.consecutive).toBe('false');
  });

  it('shows Agent identity when a live working group follows a visible user message', () => {
    const userGroup = {
      type: 'text',
      message: { id: 751, from_uid: 1 },
      sourceMessages: [{ id: 751, from_uid: 1 }],
      sender: { name: 'Cycren', isBot: false },
      isConsecutive: false,
    };
    const workingGroup = {
      type: 'working',
      messages: [{ id: 752, from_uid: 2, type: 'tool_use', content: 'execute_shell' }],
      sender: { name: '市场洞察助理', isBot: true },
      isConsecutive: true,
    };
    const outputGroup = {
      type: 'text',
      message: { id: 753, from_uid: 2, role: 'assistant', type: 'text', content: '已完成。' },
      sourceMessages: [{ id: 753, from_uid: 2, role: 'assistant', type: 'text', content: '已完成。' }],
      sender: { name: '市场洞察助理', isBot: true },
      isConsecutive: true,
    };

    const reconciled = reconcileRenderedGroupConsecutiveness([
      userGroup,
      workingGroup,
      outputGroup,
    ]);

    expect(reconciled[1].isConsecutive).toBe(false);
    expect(reconciled[2].isConsecutive).toBe(true);
  });

  it('ignores a stale group profile response after switching conversations', async () => {
    const firstGroupProfile = deferred();
    const secondGroupProfile = deferred();
    api.getMessages
      .mockResolvedValueOnce({ messages: [] })
      .mockResolvedValueOnce({
        messages: [
          { id: 75, seq_id: 75, topic_id: 'grp_10', from_uid: 3, type: 'text', content: '当前群的消息' },
        ],
      });
    api.getGroupInfo
      .mockImplementationOnce(() => firstGroupProfile.promise)
      .mockImplementationOnce(() => secondGroupProfile.promise);

    await mountTopic(root, 'grp_9', { isGroup: true, groupId: 9 });
    await mountTopic(root, 'grp_10', { isGroup: true, groupId: 10 });

    await act(async () => {
      secondGroupProfile.resolve({
        group: { id: 10, name: '当前群' },
        members: [{ user_id: 1, display_name: 'Me' }, {
          user_id: 3,
          display_name: 'Current Agent',
          avatar_url: '/uploads/current-agent.png',
          is_bot: true,
        }],
      });
      await flushPromises();
    });

    await act(async () => {
      firstGroupProfile.resolve({
        group: { id: 9, name: '旧群' },
        members: [{ user_id: 1, display_name: 'Me' }, {
          user_id: 2,
          display_name: 'Stale Agent',
          avatar_url: '/uploads/stale-agent.png',
          is_bot: true,
        }],
      });
      await flushPromises();
    });

    const message = container.querySelector('.mock-chat-message[data-message-id="75"]');
    expect(message?.dataset.senderName).toBe('Current Agent');
    expect(message?.dataset.senderAvatar).toBe('/uploads/current-agent.png');
  });

  it('uses the live Agent roster when the peer profile request has no result', async () => {
    const rosterAgent = {
      uid: 2,
      username: 'roster-agent',
      display_name: 'Roster Agent',
      avatar_url: '/uploads/roster-agent.png',
      is_bot: true,
    };
    let agentRequestCount = 0;
    api.getAgents.mockImplementation(() => {
      agentRequestCount += 1;
      return Promise.resolve(agentRequestCount === 1 ? { agents: [rosterAgent] } : { agents: [] });
    });
    api.getFriends.mockResolvedValueOnce({ friends: [] });
    api.getMessages.mockResolvedValueOnce({
      messages: [{ id: 76, seq_id: 76, topic_id: 'p2p_1_2', from_uid: 2, type: 'text', content: 'Roster reply' }],
    });

    await mountTopic(root, 'p2p_1_2', { topicName: '', topicAvatarUrl: '' });
    await act(async () => {
      await flushPromises();
    });

    const message = container.querySelector('.mock-chat-message[data-message-id="76"]');
    expect(message?.dataset.senderName).toBe('Roster Agent');
    expect(message?.dataset.senderAvatar).toBe('/uploads/roster-agent.png');
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

  it('restores attachments from serialized content blocks', async () => {
    api.getMessages.mockResolvedValueOnce({
      messages: [{
        id: 691,
        seq_id: 691,
        topic_id: 'p2p_1_2',
        from_uid: 1,
        type: 'text',
        content: '描述这张 Safari 图片',
        content_blocks: JSON.stringify([
          { type: 'text', text: '描述这张 Safari 图片' },
          { type: 'image', payload: { file_key: 'safari.png', url: '/uploads/images/safari.png', name: 'safari.png', size: 16, mime_type: 'image/png' } },
        ]),
        created_at: '2026-06-09T00:00:00Z',
      }],
    });

    await mountTopic(root, 'p2p_1_2');
    await act(async () => {
      await flushPromises();
      Simulate.click(container.querySelector('.mock-edit-message[data-message-id="691"]'));
      await new Promise((resolve) => window.setTimeout(resolve, 0));
    });

    expect(container.querySelector('textarea.v3-composer-input').value).toBe('描述这张 Safari 图片');
    expect(container.querySelector('[aria-label="预览图片：safari.png"]')).not.toBeNull();
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

  it('clears a Safari attachment drag on window blur before another drop', async () => {
    await mountTopic(root, 'p2p_1_2');
    const values = new Map();
    const dataTransfer = {
      types: [],
      setData(type, value) {
        values.set(type, value);
        if (!this.types.includes(type)) this.types.push(type);
      },
      getData: () => '',
      dropEffect: 'none',
      effectAllowed: 'none',
    };
    writeChatAttachmentDrag(dataTransfer, {
      type: 'image',
      payload: { file_key: 'stale.png', url: '/uploads/images/stale.png', name: 'stale.png' },
    });
    dataTransfer.types = [CHAT_ATTACHMENT_DRAG_FALLBACK_TYPE];

    await act(async () => {
      window.dispatchEvent(new Event('blur'));
      Simulate.drop(container.querySelector('.v3-timeline'), { dataTransfer });
      await Promise.resolve();
    });

    expect(api.uploadFile).not.toHaveBeenCalled();
    expect(container.querySelectorAll('.v3-composer-attachment-chip')).toHaveLength(0);
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

  it('keeps the visible Artifact focus when regenerating a bot reply', async () => {
    const artifact = {
      id: 'lesson-game',
      title: '课堂小游戏',
      kind: 'html',
      url: 'https://artifacts.example.test/by-agent/440/lesson-game/latest/',
      status: 'active',
      publish_version: 2,
      can_delete: true,
    };
    api.getMessages.mockResolvedValueOnce({
      messages: [
        {
          id: 70,
          seq_id: 70,
          topic_id: 'p2p_1_440',
          from_uid: 1,
          type: 'text',
          msg_type: 'text',
          content: '调整这个页面',
          created_at: '2026-08-07T00:00:00Z',
        },
        {
          id: 71,
          seq_id: 71,
          topic_id: 'p2p_1_440',
          from_uid: 440,
          role: 'assistant',
          type: 'text',
          msg_type: 'text',
          content: '已经调整完成',
          created_at: '2026-08-07T00:01:00Z',
        },
      ],
    });
    api.getAgents.mockResolvedValue({
      agents: [{
        uid: 440,
        username: 'artifact-agent',
        display_name: 'Artifact Agent',
        is_bot: true,
        cloud_artifacts_enabled: true,
      }],
    });
    api.getCloudArtifacts.mockResolvedValue({ artifacts: [artifact] });

    await mountTopic(root, 'p2p_1_440', {
      cloudArtifactsRequest: { agentUid: 440, requestId: 1 },
    });
    await act(async () => {
      await flushPromises();
    });
    await act(async () => {
      const artifactsTab = [...container.querySelectorAll('button[role="tab"]')]
        .find((button) => button.textContent === '应用');
      expect(artifactsTab).not.toBeNull();
      Simulate.click(artifactsTab);
      await flushPromises();
    });
    await act(async () => {
      const previewButton = container.querySelector('button[aria-label="预览 课堂小游戏"]');
      expect(previewButton).not.toBeNull();
      Simulate.click(previewButton);
      await Promise.resolve();
    });

    const regenerateButton = container.querySelector('.mock-regenerate-message[data-message-id="71"]');
    expect(regenerateButton).not.toBeNull();
    await act(async () => {
      Simulate.click(regenerateButton);
      await flushPromises();
    });

    expect(api.createArtifactContextSnapshot).toHaveBeenCalledWith({
      topic_id: 'p2p_1_440',
      artifact_ref: {
        contract_version: 'catsco.artifact-ref.v1',
        id: 'lesson-game',
        displayed_version: 2,
        currently_visible: true,
      },
    }, { timeoutMs: 2200 });
    expect(api.sendMessage).toHaveBeenCalledWith('p2p_1_440', {
      type: 'text',
      content: '调整这个页面',
      metadata: {
        artifact_context_ref: `acr_${'x'.repeat(43)}`,
      },
    }, undefined);
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

  it('filters bot names and inserts the display-name mention with Enter', async () => {
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

    expect(textarea.value).toBe('@Wanyu ');
    expect(container.querySelector('.oc-mention-picker')).toBeNull();
    expect(api.sendMessage).not.toHaveBeenCalled();

    await act(async () => {
      typeDraft(textarea, '@Wanyu 请处理');
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
    expect(textarea.value).toBe('@Wanyu ');

    await act(async () => {
      typeDraft(textarea, '@Wanyux 请处理');
    });
    await act(async () => {
      Simulate.keyDown(textarea, { key: 'Enter', shiftKey: false });
      await flushPromises();
    });

    expect(api.sendMessage).toHaveBeenCalledWith('grp_80', '@Wanyux 请处理', undefined);
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
      typeDraft(textarea, '  @Wanyu ');
    });
    await act(async () => {
      typeDraft(textarea, '  @Wanyu 请处理  ');
    });

    await act(async () => {
      Simulate.keyDown(textarea, { key: 'Enter', shiftKey: false });
      await flushPromises();
    });

    expect(api.sendMessage).toHaveBeenNthCalledWith(1, 'grp_80', '@usr43 请处理', undefined, ['usr43']);
    expect(textarea.value).toBe('  @Wanyu 请处理  ');

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
    expect(textarea.value).toBe('@Wanyu ');

    await act(async () => {
      typeDraft(textarea, '请处理');
    });
    await act(async () => {
      typeDraft(textarea, '@Wanyu 请处理');
    });
    await act(async () => {
      Simulate.keyDown(textarea, { key: 'Enter', shiftKey: false });
      await flushPromises();
    });

    expect(api.sendMessage).toHaveBeenCalledWith('grp_80', '@Wanyu 请处理', undefined);
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

    expect(textarea.value).toBe('前@Saturday 后');
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
    expect(api.getTopicFiles).toHaveBeenCalledWith('p2p_1_440', {
      beforeId: 0,
      limit: 40,
    });
    expect(api.getAgentFiles).not.toHaveBeenCalled();

    await act(async () => {
      Simulate.click([...container.querySelectorAll('button[role="tab"]')]
        .find((button) => button.textContent === '应用'));
      await Promise.resolve();
      await Promise.resolve();
    });
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

  it('consumes a cloud artifacts request after opening it once', async () => {
    const onRequestConsumed = vi.fn();
    let switchTopic;
    function TopicHarness() {
      const [currentTopic, setCurrentTopic] = React.useState('p2p_1_440');
      const [request, setRequest] = React.useState({
        agentUid: 440,
        requestId: 1,
        topicId: 'p2p_1_440',
      });
      switchTopic = setCurrentTopic;
      const consumeRequest = React.useCallback((requestId) => {
        onRequestConsumed(requestId);
        setRequest((current) => (
          current?.requestId === requestId ? null : current
        ));
      }, []);

      return (
        <MessagesView
          topic={currentTopic}
          topicName={currentTopic}
          user={user}
          isGroup={false}
          groupId={null}
          topicAvatarUrl=""
          onTopicUpdated={vi.fn()}
          cloudArtifactsRequest={request}
          onCloudArtifactsRequestConsumed={consumeRequest}
        />
      );
    }

    await act(async () => {
      root.render(<TopicHarness />);
      await flushPromises();
    });

    expect(container.querySelector('.cloud-artifacts-panel')).not.toBeNull();
    expect(onRequestConsumed).toHaveBeenCalledTimes(1);
    expect(onRequestConsumed).toHaveBeenCalledWith(1);

    await act(async () => {
      Simulate.click(container.querySelector('button[aria-label="关闭云文件"]'));
      await flushPromises();
    });
    expect(container.querySelector('.cloud-artifacts-panel')).toBeNull();

    await act(async () => {
      switchTopic('p2p_1_2');
      await flushPromises();
    });
    expect(container.querySelector('.cloud-artifacts-panel')).toBeNull();

    await act(async () => {
      switchTopic('p2p_1_440');
      await flushPromises();
    });

    expect(container.querySelector('.cloud-artifacts-panel')).toBeNull();
    expect(onRequestConsumed).toHaveBeenCalledTimes(1);
  });

  it('refreshes the visible exact Artifact once when its published version increases', async () => {
    vi.useFakeTimers();
    const versionTwo = {
      id: 'lesson-game',
      title: '课堂小游戏',
      kind: 'html',
      url: 'https://artifacts.example.test/by-agent/440/lesson-game/latest/',
      status: 'active',
      publish_version: 2,
      can_delete: true,
    };
    let currentArtifact = versionTwo;
    api.getCloudArtifacts.mockImplementation(async () => ({ artifacts: [currentArtifact] }));
    api.getAgents.mockResolvedValue({
      agents: [{
        uid: 440,
        username: 'artifact-agent',
        display_name: 'Artifact Agent',
        is_bot: true,
        cloud_artifacts_enabled: true,
      }],
    });
    await mountTopic(root, 'p2p_1_440', {
      cloudArtifactsRequest: { agentUid: 440, requestId: 1 },
    });
    await act(async () => {
      await flushPromises();
    });
    await act(async () => {
      Simulate.click([...container.querySelectorAll('button[role="tab"]')]
        .find((button) => button.textContent === '应用'));
      await flushPromises();
    });
    await act(async () => {
      Simulate.click(container.querySelector('button[aria-label="预览 课堂小游戏"]'));
      await flushPromises();
    });

    expect(container.querySelector('.mock-file-preview')?.getAttribute('data-url')).toBe(versionTwo.url);
    currentArtifact = { ...versionTwo, publish_version: 3 };
    await act(async () => {
      await vi.advanceTimersByTimeAsync(ARTIFACT_REGISTRY_POLL_MS_FOR_TEST);
      await flushPromises();
    });

    const refreshedURL = 'https://artifacts.example.test/by-agent/440/lesson-game/v3/';
    expect(container.querySelector('.mock-file-preview')?.getAttribute('data-url')).toBe(versionTwo.url);
    expect(container.querySelector('.mock-file-preview')?.getAttribute('data-pending-url')).toBe(refreshedURL);
    expect(artifactRefreshPreviewObserved).toHaveBeenCalledTimes(1);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(ARTIFACT_REGISTRY_POLL_MS_FOR_TEST * 2);
      await flushPromises();
    });
    expect(container.querySelector('.mock-file-preview')?.getAttribute('data-url')).toBe(versionTwo.url);
    expect(artifactRefreshPreviewObserved).toHaveBeenCalledTimes(1);

    await act(async () => {
      Simulate.click(container.querySelector('.mock-ready-artifact-refresh'));
      await flushPromises();
    });
    expect(container.querySelector('.mock-file-preview')?.getAttribute('data-url')).toBe(refreshedURL);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(ARTIFACT_REGISTRY_POLL_MS_FOR_TEST * 2);
      await flushPromises();
    });
    expect(artifactRefreshPreviewObserved).toHaveBeenCalledTimes(1);
    await act(async () => {
      Simulate.click(container.querySelector('.mock-close-preview'));
      await flushPromises();
    });
    const callsAfterClose = api.getCloudArtifacts.mock.calls.length;
    await act(async () => {
      await vi.advanceTimersByTimeAsync(ARTIFACT_REGISTRY_POLL_MS_FOR_TEST * 2);
      await flushPromises();
    });
    expect(api.getCloudArtifacts).toHaveBeenCalledTimes(callsAfterClose);
    vi.useRealTimers();
  });

  it('waits to switch a verified Artifact update while the current page reports unsaved state', async () => {
    vi.useFakeTimers();
    const origin = 'https://artifacts.example.test';
    let dirty = true;
    let contextRequests = 0;
    const frameWindow = {
      postMessage(message, targetOrigin) {
        contextRequests += 1;
        expect(targetOrigin).toBe(origin);
        const event = new Event('message');
        Object.defineProperties(event, {
          source: { value: frameWindow },
          origin: { value: origin },
          data: {
            value: {
              type: 'catsco.artifact.context.response.v1',
              request_id: message.request_id,
              context: {
                contract_version: 'catsco.artifact-page-context.v1',
                observed_at: '2026-08-21T10:00:00Z',
                dirty,
                artifact_version: 2,
              },
            },
          },
        });
        window.dispatchEvent(event);
      },
    };
    const versionTwo = {
      id: 'lesson-game',
      agent_uid: '440',
      title: '课堂小游戏',
      kind: 'html',
      url: `${origin}/by-agent/440/lesson-game/latest/`,
      status: 'active',
      publish_version: 2,
      can_delete: true,
      artifact_frame_binding: {
        frame: { contentWindow: frameWindow },
        artifactId: 'lesson-game',
        agentUid: 440,
        url: `${origin}/by-agent/440/lesson-game/latest/`,
      },
    };
    let currentArtifact = versionTwo;
    api.getCloudArtifacts.mockImplementation(async () => ({ artifacts: [currentArtifact] }));
    api.getAgents.mockResolvedValue({
      agents: [{
        uid: 440,
        username: 'artifact-agent',
        display_name: 'Artifact Agent',
        is_bot: true,
        cloud_artifacts_enabled: true,
      }],
    });

    await mountTopic(root, 'p2p_1_440', {
      cloudArtifactsRequest: { agentUid: 440, requestId: 1 },
    });
    await act(async () => {
      await flushPromises();
    });
    await act(async () => {
      Simulate.click([...container.querySelectorAll('button[role="tab"]')]
        .find((button) => button.textContent === '应用'));
      await flushPromises();
    });
    await act(async () => {
      Simulate.click(container.querySelector('button[aria-label="预览 课堂小游戏"]'));
      await flushPromises();
    });

    currentArtifact = { ...versionTwo, publish_version: 3 };
    await act(async () => {
      await vi.advanceTimersByTimeAsync(ARTIFACT_REGISTRY_POLL_MS_FOR_TEST);
      await flushPromises();
    });
    await act(async () => {
      Simulate.click(container.querySelector('.mock-ready-artifact-refresh'));
      await flushPromises();
    });
    expect(container.querySelector('.mock-file-preview')?.getAttribute('data-url')).toBe(versionTwo.url);
    expect(container.querySelector('.mock-file-preview')?.getAttribute('data-pending-url')).toBe(
      'https://artifacts.example.test/by-agent/440/lesson-game/v3/',
    );
    expect(artifactRefreshPreviewObserved).toHaveBeenCalledTimes(1);

    for (let attempt = 0; attempt < 2; attempt += 1) {
      await act(async () => {
        await vi.advanceTimersByTimeAsync(ARTIFACT_REGISTRY_POLL_MS_FOR_TEST);
        await flushPromises();
      });
    }
    expect(container.querySelector('.mock-file-preview')?.getAttribute('data-url')).toBe(versionTwo.url);
    expect(container.querySelector('.mock-file-preview')?.getAttribute('data-pending-url')).toBe(
      'https://artifacts.example.test/by-agent/440/lesson-game/v3/',
    );
    expect(artifactRefreshPreviewObserved).toHaveBeenCalledTimes(1);
    expect(contextRequests).toBeGreaterThanOrEqual(3);

    dirty = false;
    await act(async () => {
      await vi.advanceTimersByTimeAsync(ARTIFACT_REGISTRY_POLL_MS_FOR_TEST);
      await flushPromises();
    });
    expect(container.querySelector('.mock-file-preview')?.getAttribute('data-url')).toBe(
      'https://artifacts.example.test/by-agent/440/lesson-game/v3/',
    );
    expect(container.querySelector('.mock-file-preview')?.getAttribute('data-pending-url')).toBe('');
    expect(artifactRefreshPreviewObserved).toHaveBeenCalledTimes(1);
    vi.useRealTimers();
  });

  it('keeps one verified Artifact candidate pending while the current page context is unavailable', async () => {
    vi.useFakeTimers();
    const origin = 'https://artifacts.example.test';
    let contextAvailable = false;
    const frameWindow = {
      postMessage(message, targetOrigin) {
        expect(targetOrigin).toBe(origin);
        if (!contextAvailable) return;
        const event = new Event('message');
        Object.defineProperties(event, {
          source: { value: frameWindow },
          origin: { value: origin },
          data: {
            value: {
              type: 'catsco.artifact.context.response.v1',
              request_id: message.request_id,
              context: {
                contract_version: 'catsco.artifact-page-context.v1',
                observed_at: '2026-08-21T10:00:00Z',
                dirty: false,
                artifact_version: 2,
              },
            },
          },
        });
        window.dispatchEvent(event);
      },
    };
    const versionTwo = {
      id: 'lesson-game',
      agent_uid: '440',
      title: '课堂小游戏',
      kind: 'html',
      url: `${origin}/by-agent/440/lesson-game/latest/`,
      status: 'active',
      publish_version: 2,
      can_delete: true,
      artifact_frame_binding: {
        frame: { contentWindow: frameWindow },
        artifactId: 'lesson-game',
        agentUid: 440,
        url: `${origin}/by-agent/440/lesson-game/latest/`,
      },
    };
    let currentArtifact = versionTwo;
    api.getCloudArtifacts.mockImplementation(async () => ({ artifacts: [currentArtifact] }));
    api.getAgents.mockResolvedValue({
      agents: [{
        uid: 440,
        username: 'artifact-agent',
        display_name: 'Artifact Agent',
        is_bot: true,
        cloud_artifacts_enabled: true,
      }],
    });

    await mountTopic(root, 'p2p_1_440', {
      cloudArtifactsRequest: { agentUid: 440, requestId: 1 },
    });
    await act(async () => {
      await flushPromises();
    });
    await act(async () => {
      Simulate.click([...container.querySelectorAll('button[role="tab"]')]
        .find((button) => button.textContent === '应用'));
      await flushPromises();
    });
    await act(async () => {
      Simulate.click(container.querySelector('button[aria-label="预览 课堂小游戏"]'));
      await flushPromises();
    });

    currentArtifact = { ...versionTwo, publish_version: 3 };
    await act(async () => {
      await vi.advanceTimersByTimeAsync(ARTIFACT_REGISTRY_POLL_MS_FOR_TEST);
      await flushPromises();
    });
    await act(async () => {
      Simulate.click(container.querySelector('.mock-ready-artifact-refresh'));
      await vi.advanceTimersByTimeAsync(300);
      await flushPromises();
    });
    expect(container.querySelector('.mock-file-preview')?.getAttribute('data-url')).toBe(versionTwo.url);
    expect(container.querySelector('.mock-file-preview')?.getAttribute('data-pending-url')).toBe(
      'https://artifacts.example.test/by-agent/440/lesson-game/v3/',
    );
    expect(artifactRefreshPreviewObserved).toHaveBeenCalledTimes(1);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(ARTIFACT_REGISTRY_POLL_MS_FOR_TEST + 300);
      await flushPromises();
    });
    expect(container.querySelector('.mock-file-preview')?.getAttribute('data-url')).toBe(versionTwo.url);
    expect(artifactRefreshPreviewObserved).toHaveBeenCalledTimes(1);

    contextAvailable = true;
    await act(async () => {
      await vi.advanceTimersByTimeAsync(ARTIFACT_REGISTRY_POLL_MS_FOR_TEST);
      await flushPromises();
    });
    expect(container.querySelector('.mock-file-preview')?.getAttribute('data-url')).toBe(
      'https://artifacts.example.test/by-agent/440/lesson-game/v3/',
    );
    expect(container.querySelector('.mock-file-preview')?.getAttribute('data-pending-url')).toBe('');
    expect(artifactRefreshPreviewObserved).toHaveBeenCalledTimes(1);
    vi.useRealTimers();
  });

  it('switches a verified Artifact update when a valid legacy page context omits dirty', async () => {
    vi.useFakeTimers();
    const origin = 'https://artifacts.example.test';
    const frameWindow = {
      postMessage(message, targetOrigin) {
        expect(targetOrigin).toBe(origin);
        const event = new Event('message');
        Object.defineProperties(event, {
          source: { value: frameWindow },
          origin: { value: origin },
          data: {
            value: {
              type: 'catsco.artifact.context.response.v1',
              request_id: message.request_id,
              context: {
                contract_version: 'catsco.artifact-page-context.v1',
                observed_at: '2026-08-21T10:00:00Z',
                artifact_version: 2,
              },
            },
          },
        });
        window.dispatchEvent(event);
      },
    };
    const versionTwo = {
      id: 'lesson-game',
      agent_uid: '440',
      title: '课堂小游戏',
      kind: 'html',
      url: `${origin}/by-agent/440/lesson-game/latest/`,
      status: 'active',
      publish_version: 2,
      can_delete: true,
      artifact_frame_binding: {
        frame: { contentWindow: frameWindow },
        artifactId: 'lesson-game',
        agentUid: 440,
        url: `${origin}/by-agent/440/lesson-game/latest/`,
      },
    };
    let currentArtifact = versionTwo;
    api.getCloudArtifacts.mockImplementation(async () => ({ artifacts: [currentArtifact] }));
    api.getAgents.mockResolvedValue({
      agents: [{
        uid: 440,
        username: 'artifact-agent',
        display_name: 'Artifact Agent',
        is_bot: true,
        cloud_artifacts_enabled: true,
      }],
    });

    await mountTopic(root, 'p2p_1_440', {
      cloudArtifactsRequest: { agentUid: 440, requestId: 1 },
    });
    await act(async () => {
      await flushPromises();
    });
    await act(async () => {
      Simulate.click([...container.querySelectorAll('button[role="tab"]')]
        .find((button) => button.textContent === '应用'));
      await flushPromises();
    });
    await act(async () => {
      Simulate.click(container.querySelector('button[aria-label="预览 课堂小游戏"]'));
      await flushPromises();
    });

    currentArtifact = { ...versionTwo, publish_version: 3 };
    await act(async () => {
      await vi.advanceTimersByTimeAsync(ARTIFACT_REGISTRY_POLL_MS_FOR_TEST);
      await flushPromises();
    });
    await act(async () => {
      Simulate.click(container.querySelector('.mock-ready-artifact-refresh'));
      await flushPromises();
    });
    expect(container.querySelector('.mock-file-preview')?.getAttribute('data-url')).toBe(
      'https://artifacts.example.test/by-agent/440/lesson-game/v3/',
    );
    expect(container.querySelector('.mock-file-preview')?.getAttribute('data-pending-url')).toBe('');
    vi.useRealTimers();
  });

  it('keeps the current Artifact visible after a failed candidate load and retries on the next poll', async () => {
    vi.useFakeTimers();
    const versionTwo = {
      id: 'lesson-game',
      title: '课堂小游戏',
      kind: 'html',
      url: 'https://artifacts.example.test/by-agent/440/lesson-game/latest/',
      status: 'active',
      publish_version: 2,
      can_delete: true,
    };
    let currentArtifact = versionTwo;
    api.getCloudArtifacts.mockImplementation(async () => ({ artifacts: [currentArtifact] }));
    api.getAgents.mockResolvedValue({
      agents: [{
        uid: 440,
        username: 'artifact-agent',
        display_name: 'Artifact Agent',
        is_bot: true,
        cloud_artifacts_enabled: true,
      }],
    });
    await mountTopic(root, 'p2p_1_440', {
      cloudArtifactsRequest: { agentUid: 440, requestId: 1 },
    });
    await act(async () => {
      await flushPromises();
    });
    await act(async () => {
      Simulate.click([...container.querySelectorAll('button[role="tab"]')]
        .find((button) => button.textContent === '应用'));
      await flushPromises();
    });
    await act(async () => {
      Simulate.click(container.querySelector('button[aria-label="预览 课堂小游戏"]'));
      await flushPromises();
    });

    currentArtifact = { ...versionTwo, publish_version: 3 };
    await act(async () => {
      await vi.advanceTimersByTimeAsync(ARTIFACT_REGISTRY_POLL_MS_FOR_TEST);
      await flushPromises();
    });
    expect(container.querySelector('.mock-file-preview')?.getAttribute('data-url')).toBe(versionTwo.url);
    expect(container.querySelector('.mock-file-preview')?.getAttribute('data-pending-url')).toBe(
      'https://artifacts.example.test/by-agent/440/lesson-game/v3/',
    );
    expect(artifactRefreshPreviewObserved).toHaveBeenCalledTimes(1);

    await act(async () => {
      Simulate.click(container.querySelector('.mock-fail-artifact-refresh'));
      await flushPromises();
    });
    expect(container.querySelector('.mock-file-preview')?.getAttribute('data-url')).toBe(versionTwo.url);
    expect(container.querySelector('.mock-file-preview')?.getAttribute('data-pending-url')).toBe('');

    await act(async () => {
      await vi.advanceTimersByTimeAsync(ARTIFACT_REGISTRY_POLL_MS_FOR_TEST);
      await flushPromises();
    });
    expect(artifactRefreshPreviewObserved).toHaveBeenCalledTimes(2);
    await act(async () => {
      Simulate.click(container.querySelector('.mock-ready-artifact-refresh'));
      await flushPromises();
    });
    expect(container.querySelector('.mock-file-preview')?.getAttribute('data-url')).toBe(
      'https://artifacts.example.test/by-agent/440/lesson-game/v3/',
    );
    vi.useRealTimers();
  });

  it('aborts an in-flight Artifact registry request when the preview closes', async () => {
    vi.useFakeTimers();
    const artifact = {
      id: 'lesson-game',
      agent_uid: '440',
      title: '课堂小游戏',
      kind: 'html',
      url: 'https://artifacts.example.test/by-agent/440/lesson-game/latest/',
      status: 'active',
      publish_version: 2,
      can_delete: true,
    };
    let holdPollingRequest = false;
    const pollingSignals = [];
    const aborted = vi.fn();
    api.getCloudArtifacts.mockImplementation((_agentUID, _status, options) => {
      if (!holdPollingRequest || !options?.signal) return Promise.resolve({ artifacts: [artifact] });
      pollingSignals.push(options.signal);
      return new Promise((_resolve, reject) => {
        options.signal.addEventListener('abort', () => {
          aborted();
          const error = new Error('aborted');
          error.code = 'REQUEST_ABORTED';
          reject(error);
        }, { once: true });
      });
    });
    api.getAgents.mockResolvedValue({
      agents: [{
        uid: 440,
        username: 'artifact-agent',
        display_name: 'Artifact Agent',
        is_bot: true,
        cloud_artifacts_enabled: true,
      }],
    });

    await mountTopic(root, 'p2p_1_440', {
      cloudArtifactsRequest: { agentUid: 440, requestId: 1 },
    });
    await act(async () => {
      await flushPromises();
    });
    await act(async () => {
      Simulate.click([...container.querySelectorAll('button[role="tab"]')]
        .find((button) => button.textContent === '应用'));
      await flushPromises();
    });
    await act(async () => {
      Simulate.click(container.querySelector('button[aria-label="预览 课堂小游戏"]'));
      await flushPromises();
    });

    holdPollingRequest = true;
    await act(async () => {
      await vi.advanceTimersByTimeAsync(ARTIFACT_REGISTRY_POLL_MS_FOR_TEST);
      await flushPromises();
    });
    const inFlightPollingSignal = pollingSignals[0];
    expect(inFlightPollingSignal).toBeInstanceOf(AbortSignal);
    expect(inFlightPollingSignal.aborted).toBe(false);

    await act(async () => {
      Simulate.click(container.querySelector('.mock-close-preview'));
      await flushPromises();
    });
    expect(inFlightPollingSignal.aborted).toBe(true);
    expect(aborted).toHaveBeenCalledTimes(1);
  });

  it('does not cross-refresh an open Artifact after the same topic changes Agent', async () => {
    const agentAArtifact = {
      id: 'lesson-game',
      agent_uid: '440',
      title: 'Agent A game',
      kind: 'html',
      url: 'https://artifacts.example.test/by-agent/440/lesson-game/latest/',
      status: 'active',
      publish_version: 2,
    };
    const agentBArtifact = {
      ...agentAArtifact,
      agent_uid: '441',
      title: 'Agent B game',
      url: 'https://artifacts.example.test/by-agent/441/lesson-game/latest/',
      publish_version: 3,
    };
    api.getAgents.mockResolvedValue({
      agents: [
        { uid: 440, display_name: 'Agent A', is_bot: true, cloud_artifacts_enabled: true },
        { uid: 441, display_name: 'Agent B', is_bot: true, cloud_artifacts_enabled: true },
      ],
    });
    api.getGroupInfo
      .mockResolvedValueOnce({
        group: { id: 90, name: 'Artifact task', is_agent_task: true },
        members: [
          { user_id: 1, display_name: 'Me', is_bot: false },
          { user_id: 440, display_name: 'Agent A', is_bot: true },
        ],
      })
      .mockResolvedValueOnce({
        group: { id: 90, name: 'Artifact task', is_agent_task: true },
        members: [
          { user_id: 1, display_name: 'Me', is_bot: false },
          { user_id: 441, display_name: 'Agent B', is_bot: true },
        ],
      });
    api.getCloudArtifacts.mockImplementation((agentUID) => Promise.resolve({
      artifacts: [Number(agentUID) === 441 ? agentBArtifact : agentAArtifact],
    }));

    await mountTopic(root, 'grp_90', {
      isGroup: true,
      groupId: 90,
      cloudArtifactsRequest: { agentUid: 440, requestId: 1 },
    });
    await act(async () => {
      await flushPromises();
    });
    await act(async () => {
      Simulate.click([...container.querySelectorAll('button[role="tab"]')]
        .find((button) => button.textContent === '应用'));
      await flushPromises();
    });
    await act(async () => {
      Simulate.click(container.querySelector('button[aria-label="预览 Agent A game"]'));
      await flushPromises();
    });
    expect(container.querySelector('.mock-file-preview')?.getAttribute('data-url')).toBe(agentAArtifact.url);

    await act(async () => {
      wsHandler({ pres: { topic: 'grp_90', what: 'members_invited' } });
      await flushPromises();
    });

    expect(api.getCloudArtifacts).toHaveBeenCalledWith(441, 'active', {
      signal: expect.any(AbortSignal),
    });
    expect(container.querySelector('.mock-file-preview')?.getAttribute('data-url')).toBe(agentAArtifact.url);
    expect(container.querySelector('.mock-file-preview')?.getAttribute('data-pending-url')).toBe('');
  });

  it('stores the visible Artifact snapshot separately and attaches only its opaque ref', async () => {
    const artifact = {
      id: 'lesson-game',
      agent_uid: '440',
      title: '课堂小游戏',
      kind: 'html',
      url: 'https://artifacts.example.test/by-agent/440/lesson-game/latest/',
      status: 'active',
      publish_version: 2,
      can_delete: true,
    };
    api.getCloudArtifacts.mockResolvedValue({ artifacts: [artifact] });
    api.getAgents.mockResolvedValue({
      agents: [{
        uid: 440,
        username: 'artifact-agent',
        display_name: 'Artifact Agent',
        is_bot: true,
        cloud_artifacts_enabled: true,
      }],
    });

    await mountTopic(root, 'p2p_1_440', {
      cloudArtifactsRequest: { agentUid: 440, requestId: 1 },
    });
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });
    await act(async () => {
      const artifactsTab = [...container.querySelectorAll('button[role="tab"]')]
        .find((button) => button.textContent === '应用');
      expect(artifactsTab).not.toBeNull();
      Simulate.click(artifactsTab);
      await Promise.resolve();
      await Promise.resolve();
    });
    await act(async () => {
      const previewButton = container.querySelector('button[aria-label="预览 课堂小游戏"]');
      expect(previewButton).not.toBeNull();
      Simulate.click(previewButton);
      await Promise.resolve();
    });

    const textarea = container.querySelector('textarea.v3-composer-input');
    await act(async () => {
      typeDraft(textarea, '把右边标题改短一点');
    });
    await act(async () => {
      Simulate.click(container.querySelector('button[aria-label="发送"]'));
      await flushPromises();
    });

    expect(api.createArtifactContextSnapshot).toHaveBeenCalledWith({
      topic_id: 'p2p_1_440',
      artifact_ref: {
        contract_version: 'catsco.artifact-ref.v1',
        id: 'lesson-game',
        displayed_version: 2,
        currently_visible: true,
      },
    }, { timeoutMs: 2200 });
    expect(api.sendMessage).toHaveBeenCalledWith('p2p_1_440', {
      type: 'text',
      content: '把右边标题改短一点',
      metadata: {
        artifact_context_ref: `acr_${'x'.repeat(43)}`,
      },
    }, undefined);
    expect(container.textContent).not.toContain('lesson-game');
    expect(container.textContent).not.toContain('当前 Artifact');
  });

  it('hands the exact Artifact version to a ready Viewer and reads fresh context from that tab', async () => {
    const artifact = {
      id: 'lesson-game',
      agent_uid: '440',
      title: '课堂小游戏',
      kind: 'html',
      url: 'https://artifacts.example.test/by-agent/440/lesson-game/latest/',
      status: 'active',
      publish_version: 2,
      can_delete: true,
    };
    api.getCloudArtifacts.mockResolvedValue({ artifacts: [artifact] });
    api.getAgents.mockResolvedValue({
      agents: [{
        uid: 440,
        username: 'artifact-agent',
        display_name: 'Artifact Agent',
        is_bot: true,
        cloud_artifacts_enabled: true,
      }],
    });
    const openedWindow = {
      closed: false,
      close: vi.fn(function close() { this.closed = true; }),
      focus: vi.fn(),
      opener: window,
    };
    const replacementWindow = {
      closed: false,
      close: vi.fn(function close() { this.closed = true; }),
      focus: vi.fn(),
      opener: window,
    };
    const openSpy = vi.spyOn(window, 'open')
      .mockReturnValueOnce(openedWindow)
      .mockReturnValueOnce(replacementWindow);

    await mountTopic(root, 'p2p_1_440', {
      cloudArtifactsRequest: { agentUid: 440, requestId: 1 },
    });
    await act(async () => flushPromises());
    await act(async () => {
      Simulate.click([...container.querySelectorAll('button[role="tab"]')]
        .find((button) => button.textContent === '应用'));
      await flushPromises();
    });
    await act(async () => {
      Simulate.click(container.querySelector('button[aria-label="预览 课堂小游戏"]'));
      await flushPromises();
    });

    await act(async () => {
      Simulate.click(container.querySelector('.mock-open-artifact-fullscreen'));
      await Promise.resolve();
    });
    await act(async () => {
      Simulate.click(container.querySelector('.mock-open-artifact-fullscreen'));
      await flushPromises();
    });

    expect(container.querySelector('.mock-file-preview')).not.toBeNull();
    expect(openSpy).toHaveBeenCalledTimes(2);
    expect(openedWindow.close).toHaveBeenCalledTimes(1);
    const viewerURL = new URL(openSpy.mock.calls[1][0]);
    expect(viewerURL.pathname).toBe('/artifact-viewer');
    expect(Object.fromEntries(viewerURL.searchParams)).toMatchObject({
      topic: 'p2p_1_440',
      agent: '440',
      artifact: 'lesson-game',
      version: '2',
    });
    expect(openSpy.mock.calls[0][0]).not.toContain('artifacts.example.test');

    const channel = artifactPreviewChannels[0];
    expect(channel).toBeTruthy();
    const identity = {
      topicId: 'p2p_1_440',
      agentUid: 440,
      artifactId: 'lesson-game',
      displayedVersion: 2,
    };
    await act(async () => {
      channel.receive(createArtifactPreviewMessage('viewer_ready', identity, {
        viewer_id: 'viewer_12345678',
        handoff_id: viewerURL.searchParams.get('handoff'),
        context_ref: `acr_${'i'.repeat(43)}`,
      }));
      await flushPromises();
    });
    expect(container.querySelector('.mock-file-preview')).toBeNull();
    expect(replacementWindow.close).not.toHaveBeenCalled();

    channel.onPost = (message) => {
      if (message.type !== 'context_request') return;
      channel.receive(createArtifactPreviewMessage('context_response', identity, {
        viewer_id: 'viewer_12345678',
        handoff_id: viewerURL.searchParams.get('handoff'),
        request_id: message.request_id,
        context_ref: `acr_${'v'.repeat(43)}`,
      }));
    };
    await act(async () => {
      typeDraft(container.querySelector('textarea.v3-composer-input'), '分析全屏页面');
      await flushPromises();
    });
    await act(async () => {
      Simulate.click(container.querySelector('button[aria-label="发送"]'));
      await flushPromises();
    });

    const contextRequest = channel.posted.find((message) => message.type === 'context_request');
    expect(contextRequest).toMatchObject({
      contract_version: ARTIFACT_PREVIEW_COORDINATION_CONTRACT,
      viewer_id: 'viewer_12345678',
      topic_id: 'p2p_1_440',
      artifact_id: 'lesson-game',
      displayed_version: 2,
    });
    expect(api.createArtifactContextSnapshot).not.toHaveBeenCalled();
    expect(api.sendMessage).toHaveBeenCalledWith('p2p_1_440', {
      type: 'text',
      content: '分析全屏页面',
      metadata: {
        artifact_context_ref: `acr_${'v'.repeat(43)}`,
      },
    }, undefined);
  });

  it('keeps the sidebar active when the browser blocks the Viewer tab', async () => {
    const artifact = {
      id: 'lesson-game',
      agent_uid: '440',
      title: '课堂小游戏',
      kind: 'html',
      url: 'https://artifacts.example.test/by-agent/440/lesson-game/latest/',
      status: 'active',
      publish_version: 2,
    };
    api.getCloudArtifacts.mockResolvedValue({ artifacts: [artifact] });
    api.getAgents.mockResolvedValue({
      agents: [{
        uid: 440,
        username: 'artifact-agent',
        display_name: 'Artifact Agent',
        is_bot: true,
        cloud_artifacts_enabled: true,
      }],
    });
    vi.spyOn(window, 'open').mockReturnValue(null);

    await mountTopic(root, 'p2p_1_440', {
      cloudArtifactsRequest: { agentUid: 440, requestId: 1 },
    });
    await act(async () => flushPromises());
    await act(async () => {
      Simulate.click([...container.querySelectorAll('button[role="tab"]')]
        .find((button) => button.textContent === '应用'));
      await flushPromises();
    });
    await act(async () => {
      Simulate.click(container.querySelector('button[aria-label="预览 课堂小游戏"]'));
      await flushPromises();
    });
    await act(async () => {
      Simulate.click(container.querySelector('.mock-open-artifact-fullscreen'));
      await flushPromises();
    });

    expect(container.querySelector('.mock-file-preview')).not.toBeNull();
    expect(feedbackNotify).toHaveBeenCalledWith(expect.objectContaining({
      tone: 'warning',
      message: expect.stringContaining('浏览器拦截'),
    }));
  });

  it('sends the ordinary message when snapshot creation fails', async () => {
    const artifact = {
      id: 'lesson-game',
      agent_uid: '440',
      title: '课堂小游戏',
      kind: 'html',
      url: 'https://artifacts.example.test/by-agent/440/lesson-game/latest/',
      status: 'active',
      publish_version: 2,
      can_delete: true,
    };
    api.getCloudArtifacts.mockResolvedValue({ artifacts: [artifact] });
    api.getAgents.mockResolvedValue({
      agents: [{
        uid: 440,
        username: 'artifact-agent',
        display_name: 'Artifact Agent',
        is_bot: true,
        cloud_artifacts_enabled: true,
      }],
    });
    api.createArtifactContextSnapshot.mockRejectedValueOnce(new Error('snapshot unavailable'));

    await mountTopic(root, 'p2p_1_440', {
      cloudArtifactsRequest: { agentUid: 440, requestId: 1 },
    });
    await act(async () => {
      await flushPromises();
    });
    await act(async () => {
      Simulate.click([...container.querySelectorAll('button[role="tab"]')]
        .find((button) => button.textContent === '应用'));
      await flushPromises();
    });
    await act(async () => {
      Simulate.click(container.querySelector('button[aria-label="预览 课堂小游戏"]'));
      await flushPromises();
    });
    await act(async () => {
      typeDraft(container.querySelector('textarea.v3-composer-input'), '正常发送');
      await flushPromises();
    });
    await act(async () => {
      Simulate.click(container.querySelector('button[aria-label="发送"]'));
      await flushPromises();
    });

    expect(api.createArtifactContextSnapshot).toHaveBeenCalledTimes(1);
    expect(api.sendMessage).toHaveBeenCalledWith('p2p_1_440', '正常发送', undefined);
  });

  it('invalidates a snapshot that finishes after the preview has closed', async () => {
    let finishSnapshot;
    api.createArtifactContextSnapshot.mockImplementationOnce(() => new Promise((resolve) => {
      finishSnapshot = resolve;
    }));
    const artifact = {
      id: 'lesson-game',
      agent_uid: '440',
      title: '课堂小游戏',
      kind: 'html',
      url: 'https://artifacts.example.test/by-agent/440/lesson-game/latest/',
      status: 'active',
      publish_version: 2,
      can_delete: true,
    };
    api.getCloudArtifacts.mockResolvedValue({ artifacts: [artifact] });
    api.getAgents.mockResolvedValue({
      agents: [{
        uid: 440,
        username: 'artifact-agent',
        display_name: 'Artifact Agent',
        is_bot: true,
        cloud_artifacts_enabled: true,
      }],
    });

    await mountTopic(root, 'p2p_1_440', {
      cloudArtifactsRequest: { agentUid: 440, requestId: 1 },
    });
    await act(async () => {
      await flushPromises();
    });
    await act(async () => {
      Simulate.click([...container.querySelectorAll('button[role="tab"]')]
        .find((button) => button.textContent === '应用'));
      await flushPromises();
    });
    await act(async () => {
      Simulate.click(container.querySelector('button[aria-label="预览 课堂小游戏"]'));
      await flushPromises();
    });
    await act(async () => {
      typeDraft(container.querySelector('textarea.v3-composer-input'), '不要绑定旧页面');
      await flushPromises();
    });
    await act(async () => {
      Simulate.click(container.querySelector('button[aria-label="发送"]'));
      await Promise.resolve();
    });
    expect(finishSnapshot).toEqual(expect.any(Function));

    await act(async () => {
      Simulate.click(container.querySelector('.mock-close-preview'));
      finishSnapshot({
        contract_version: 'catsco.artifact-context-ref.v1',
        context_ref: `acr_${'z'.repeat(43)}`,
      });
      await flushPromises();
    });

    expect(api.invalidateArtifactContextSnapshot).toHaveBeenCalledWith(
      `acr_${'z'.repeat(43)}`,
      { timeoutMs: 2200 },
    );
    expect(api.sendMessage).toHaveBeenCalledWith('p2p_1_440', '不要绑定旧页面', undefined);
  });

  it('stores the latest bounded iframe observation outside the chat message', async () => {
    const origin = 'https://artifacts.example.test';
    const frameWindow = {
      postMessage(message, targetOrigin) {
        expect(targetOrigin).toBe(origin);
        window.setTimeout(() => {
          const event = new Event('message');
          Object.defineProperties(event, {
            source: { value: frameWindow },
            origin: { value: origin },
            data: {
              value: {
                type: 'catsco.artifact.context.response.v1',
                request_id: message.request_id,
                context: {
                  contract_version: 'catsco.artifact-page-context.v1',
                  observed_at: '2026-08-07T12:00:00Z',
                  selected_text: '企业客户',
                  controls: [{ type: 'checkbox', name: 'feedback', value: 'f12', checked: true }],
                },
              },
            },
          });
          window.dispatchEvent(event);
        }, 0);
      },
    };
    const artifact = {
      id: 'lesson-game',
      agent_uid: '440',
      title: '课堂小游戏',
      kind: 'html',
      url: `${origin}/by-agent/440/lesson-game/latest/`,
      status: 'active',
      publish_version: 2,
      can_delete: true,
      artifact_frame_binding: {
        frame: { contentWindow: frameWindow },
        artifactId: 'lesson-game',
        agentUid: 440,
        url: `${origin}/by-agent/440/lesson-game/latest/`,
      },
    };
    api.getCloudArtifacts.mockResolvedValue({ artifacts: [artifact] });
    api.getAgents.mockResolvedValue({
      agents: [{
        uid: 440,
        username: 'artifact-agent',
        display_name: 'Artifact Agent',
        is_bot: true,
        cloud_artifacts_enabled: true,
      }],
    });

    await mountTopic(root, 'p2p_1_440', {
      cloudArtifactsRequest: { agentUid: 440, requestId: 1 },
    });
    await act(async () => {
      await flushPromises();
    });
    await act(async () => {
      const artifactsTab = [...container.querySelectorAll('button[role="tab"]')]
        .find((button) => button.textContent === '应用');
      expect(artifactsTab).not.toBeNull();
      Simulate.click(artifactsTab);
      await flushPromises();
    });
    await act(async () => {
      const previewButton = container.querySelector('button[aria-label="预览 课堂小游戏"]');
      expect(previewButton).not.toBeNull();
      Simulate.click(previewButton);
      await flushPromises();
    });

    const textarea = container.querySelector('textarea.v3-composer-input');
    await act(async () => {
      typeDraft(textarea, '分析这些');
      await flushPromises();
    });
    await act(async () => {
      Simulate.click(container.querySelector('button[aria-label="发送"]'));
      await new Promise((resolve) => setTimeout(resolve, 5));
      await flushPromises();
    });

    expect(api.createArtifactContextSnapshot).toHaveBeenCalledWith({
      topic_id: 'p2p_1_440',
      artifact_ref: {
        contract_version: 'catsco.artifact-ref.v1',
        id: 'lesson-game',
        displayed_version: 2,
        currently_visible: true,
      },
      page_context: {
        contract_version: 'catsco.artifact-page-context.v1',
        observed_at: '2026-08-07T12:00:00Z',
        selected_text: '企业客户',
        controls: [{ type: 'checkbox', name: 'feedback', value: 'f12', checked: true }],
      },
    }, { timeoutMs: 2200 });
    expect(api.sendMessage).toHaveBeenCalledWith('p2p_1_440', {
      type: 'text',
      content: '分析这些',
      metadata: {
        artifact_context_ref: `acr_${'x'.repeat(43)}`,
      },
    }, undefined);
  });

  it('routes a result only to the preview that owns the current context snapshot', async () => {
    const origin = 'https://artifacts.example.test';
    const resultId = `arr_${'r'.repeat(43)}`;
    let appliedResult = null;
    const frameWindow = {
      postMessage(message) {
        if (message.type === 'catsco.artifact.context.request.v1') {
          window.setTimeout(() => dispatchFrameMessage(frameWindow, origin, {
            type: 'catsco.artifact.context.response.v1',
            request_id: message.request_id,
            context: {
              contract_version: 'catsco.artifact-page-context.v1',
              observed_at: '2026-08-25T03:00:00Z',
              semantic_context: { view: 'risk-register', state_revision: '42' },
            },
          }), 0);
          return;
        }
        if (message.type === 'catsco.artifact.result.request.v1') {
          appliedResult = message.result;
          window.setTimeout(() => dispatchFrameMessage(frameWindow, origin, {
            type: 'catsco.artifact.result.response.v1',
            request_id: message.request_id,
            receipt: {
              contract_version: 'catsco.artifact-result-receipt.v1',
              result_id: resultId,
              status: 'applied',
              receipt: { created: 1, state_revision: '43' },
            },
          }), 0);
        }
      },
    };
    const artifact = {
      id: 'lesson-game',
      agent_uid: '440',
      title: '风险台账',
      kind: 'html',
      url: `${origin}/by-agent/440/lesson-game/latest/`,
      status: 'active',
      publish_version: 2,
      can_delete: true,
      artifact_frame_binding: {
        frame: { contentWindow: frameWindow },
        artifactId: 'lesson-game',
        agentUid: 440,
        url: `${origin}/by-agent/440/lesson-game/latest/`,
      },
    };
    api.getCloudArtifacts.mockResolvedValue({ artifacts: [artifact] });
    api.getAgents.mockResolvedValue({
      agents: [{ uid: 440, is_bot: true, cloud_artifacts_enabled: true }],
    });

    await mountTopic(root, 'p2p_1_440', {
      cloudArtifactsRequest: { agentUid: 440, requestId: 1 },
    });
    await act(async () => { await flushPromises(); });
    await act(async () => {
      Simulate.click([...container.querySelectorAll('button[role="tab"]')]
        .find((button) => button.textContent === '应用'));
      await flushPromises();
    });
    await act(async () => {
      Simulate.click(container.querySelector('button[aria-label="预览 风险台账"]'));
      await flushPromises();
    });
    await act(async () => {
      typeDraft(container.querySelector('textarea.v3-composer-input'), '把风险写进当前台账');
      await flushPromises();
    });
    await act(async () => {
      Simulate.click(container.querySelector('button[aria-label="发送"]'));
      await new Promise((resolve) => setTimeout(resolve, 5));
      await flushPromises();
    });

    const request = {
      type: 'request',
      origin_node_id: 'catsco-node-1',
      context_ref: `acr_${'x'.repeat(43)}`,
      writeback_ref: `awr_${'w'.repeat(43)}`,
      topic_id: 'p2p_1_440',
      agent_uid: '440',
      artifact_id: 'lesson-game',
      displayed_version: 2,
      sink_id: 'risk-items.upsert.v1',
      result_id: resultId,
      expected_state_revision: '42',
      payload: { items: [{ title: '延期风险' }] },
    };
    await act(async () => {
      wsHandler({ artifact_result: request });
      await new Promise((resolve) => setTimeout(resolve, 5));
      await flushPromises();
    });

    expect(appliedResult).toEqual({
      contract_version: 'catsco.artifact-result.v1',
      artifact_id: 'lesson-game',
      displayed_version: 2,
      sink_id: 'risk-items.upsert.v1',
      result_id: resultId,
      expected_state_revision: '42',
      payload: { items: [{ title: '延期风险' }] },
    });
    expect(wsSendArtifactResultReceipt).toHaveBeenCalledWith(expect.objectContaining({
      type: 'receipt',
      context_ref: `acr_${'x'.repeat(43)}`,
      result_id: resultId,
      receipt: expect.objectContaining({ status: 'applied' }),
    }));

    wsSendArtifactResultReceipt.mockClear();
    await act(async () => {
      wsHandler({
        artifact_result: {
          ...request,
          context_ref: `acr_${'z'.repeat(43)}`,
          result_id: `arr_${'q'.repeat(43)}`,
        },
      });
      await flushPromises();
    });
    expect(wsSendArtifactResultReceipt).not.toHaveBeenCalled();
  });

  it('drops a result response that arrives after the preview is closed', async () => {
    const origin = 'https://artifacts.example.test';
    const resultId = `arr_${'c'.repeat(43)}`;
    let respondToResult = null;
    const frameWindow = {
      postMessage(message, targetOrigin) {
        expect(targetOrigin).toBe(origin);
        if (message.type === 'catsco.artifact.context.request.v1') {
          dispatchFrameMessage(frameWindow, origin, {
            type: 'catsco.artifact.context.response.v1',
            request_id: message.request_id,
            context: {
              contract_version: 'catsco.artifact-page-context.v1',
              observed_at: '2026-08-25T03:00:00Z',
              selected_text: '待回写页面',
            },
          });
          return;
        }
        if (message.type === 'catsco.artifact.result.request.v1') {
          respondToResult = () => dispatchFrameMessage(frameWindow, origin, {
            type: 'catsco.artifact.result.response.v1',
            request_id: message.request_id,
            receipt: {
              contract_version: 'catsco.artifact-result-receipt.v1',
              result_id: resultId,
              status: 'applied',
            },
          });
        }
      },
    };
    const artifactURL = `${origin}/by-agent/440/close-race/latest/`;
    const artifact = {
      id: 'close-race',
      agent_uid: '440',
      title: '关闭竞态',
      kind: 'html',
      url: artifactURL,
      status: 'active',
      publish_version: 1,
      can_delete: true,
      artifact_frame_binding: {
        frame: { contentWindow: frameWindow },
        artifactId: 'close-race',
        agentUid: 440,
        url: artifactURL,
      },
    };
    api.getCloudArtifacts.mockResolvedValue({ artifacts: [artifact] });
    api.getAgents.mockResolvedValue({
      agents: [{ uid: 440, is_bot: true, cloud_artifacts_enabled: true }],
    });

    await mountTopic(root, 'p2p_1_440', {
      cloudArtifactsRequest: { agentUid: 440, requestId: 1 },
    });
    await act(async () => { await flushPromises(); });
    await act(async () => {
      Simulate.click([...container.querySelectorAll('button[role="tab"]')]
        .find((button) => button.textContent === '应用'));
      await flushPromises();
    });
    await act(async () => {
      Simulate.click(container.querySelector('button[aria-label="预览 关闭竞态"]'));
      await flushPromises();
    });
    await act(async () => {
      typeDraft(container.querySelector('textarea.v3-composer-input'), '写回关闭竞态');
      await flushPromises();
    });
    await act(async () => {
      Simulate.click(container.querySelector('button[aria-label="发送"]'));
      await flushPromises();
    });

    wsSendArtifactResultReceipt.mockClear();
    await act(async () => {
      wsHandler({
        artifact_result: {
          type: 'request',
          origin_node_id: 'catsco-node-1',
          context_ref: `acr_${'x'.repeat(43)}`,
          writeback_ref: `awr_${'w'.repeat(43)}`,
          topic_id: 'p2p_1_440',
          agent_uid: '440',
          artifact_id: 'close-race',
          displayed_version: 1,
          sink_id: 'items.upsert.v1',
          result_id: resultId,
          payload: { items: [{ title: '不应回写' }] },
        },
      });
      await flushPromises();
    });
    expect(respondToResult).toEqual(expect.any(Function));

    await act(async () => {
      Simulate.click(container.querySelector('button.mock-close-preview'));
      await flushPromises();
    });
    await act(async () => {
      respondToResult();
      await flushPromises();
    });

    expect(wsSendArtifactResultReceipt).not.toHaveBeenCalled();
  });

  it('returns a failed receipt when a same-origin opaque Artifact has no bridge support', async () => {
    vi.stubGlobal('MessageChannel', undefined);
    const artifactURL = new URL(
      '/artifacts/by-agent/440/legacy-game/latest/',
      window.location.origin,
    ).toString();
    const resultId = `arr_${'o'.repeat(43)}`;
    const frameWindow = { postMessage: vi.fn() };
    const artifact = {
      id: 'legacy-game',
      agent_uid: '440',
      title: '旧版小游戏',
      kind: 'html',
      url: artifactURL,
      status: 'active',
      publish_version: 1,
      can_delete: true,
      artifact_frame_binding: {
        frame: { contentWindow: frameWindow },
        artifactId: 'legacy-game',
        agentUid: 440,
        url: artifactURL,
        bridge: 'catsco.artifact-frame-bridge.v1',
        bridgeNonce: 'legacy-bridge-nonce',
        bridgeReady: true,
      },
    };
    api.getCloudArtifacts.mockResolvedValue({ artifacts: [artifact] });
    api.getAgents.mockResolvedValue({
      agents: [{ uid: 440, is_bot: true, cloud_artifacts_enabled: true }],
    });

    await mountTopic(root, 'p2p_1_440', {
      cloudArtifactsRequest: { agentUid: 440, requestId: 1 },
    });
    await act(async () => { await flushPromises(); });
    await act(async () => {
      Simulate.click([...container.querySelectorAll('button[role="tab"]')]
        .find((button) => button.textContent === '应用'));
      await flushPromises();
    });
    await act(async () => {
      Simulate.click(container.querySelector('button[aria-label="预览 旧版小游戏"]'));
      await flushPromises();
    });
    await act(async () => {
      typeDraft(container.querySelector('textarea.v3-composer-input'), '把结果写回旧版页面');
      await flushPromises();
    });
    await act(async () => {
      Simulate.click(container.querySelector('button[aria-label="发送"]'));
      await flushPromises();
    });

    const request = {
      type: 'request',
      origin_node_id: 'catsco-node-1',
      context_ref: `acr_${'x'.repeat(43)}`,
      writeback_ref: `awr_${'w'.repeat(43)}`,
      topic_id: 'p2p_1_440',
      agent_uid: '440',
      artifact_id: 'legacy-game',
      displayed_version: 1,
      sink_id: 'items.upsert.v1',
      result_id: resultId,
      payload: { items: [{ title: '不应发送到旧页面' }] },
    };
    await act(async () => {
      wsHandler({ artifact_result: request });
      await flushPromises();
    });

    expect(frameWindow.postMessage).toHaveBeenCalledWith(
      { type: 'catsco.artifact.host.connect.v1' },
      window.location.origin,
    );
    expect(frameWindow.postMessage.mock.calls.some(
      ([message]) => message?.type === 'catsco.artifact.result.request.v1',
    )).toBe(false);
    expect(wsSendArtifactResultReceipt).toHaveBeenCalledWith(expect.objectContaining({
      type: 'receipt',
      result_id: resultId,
      receipt: {
        contract_version: 'catsco.artifact-result-receipt.v1',
        result_id: resultId,
        status: 'failed',
        code: 'opaque_frame_bridge_required',
      },
    }));
  });

  it('turns a declared page action into one visible Agent turn and routes its result back', async () => {
    const origin = 'https://artifacts.example.test';
    const taskId = `atk_${'t'.repeat(43)}`;
    const taskRef = `atr_${'q'.repeat(43)}`;
    const resultId = `arr_${'r'.repeat(43)}`;
    const posted = [];
    let appliedResult = null;
    const frameWindow = {
      postMessage(message, targetOrigin) {
        expect(targetOrigin).toBe(origin);
        posted.push(message);
        if (message.type === 'catsco.artifact.context.request.v1') {
          window.setTimeout(() => dispatchFrameMessage(frameWindow, origin, {
            type: 'catsco.artifact.context.response.v1',
            request_id: message.request_id,
            context: {
              contract_version: 'catsco.artifact-page-context.v1',
              observed_at: '2026-08-26T03:00:00Z',
              semantic_context: { view: 'board', selected_task: 'task-7' },
            },
          }), 0);
        }
        if (message.type === 'catsco.artifact.result.request.v1') {
          appliedResult = message.result;
          window.setTimeout(() => dispatchFrameMessage(frameWindow, origin, {
            type: 'catsco.artifact.result.response.v1',
            request_id: message.request_id,
            receipt: {
              contract_version: 'catsco.artifact-result-receipt.v1',
              result_id: resultId,
              status: 'applied',
              receipt: { created: 1, state_revision: '8' },
            },
          }), 0);
        }
      },
    };
    const artifact = {
      id: 'task-board',
      agent_uid: '440',
      title: '任务看板',
      kind: 'mini_app',
      url: `${origin}/by-agent/440/task-board/latest/`,
      status: 'active',
      publish_version: 3,
      can_delete: true,
      artifact_frame_binding: {
        frame: { contentWindow: frameWindow },
        artifactId: 'task-board',
        agentUid: 440,
        url: `${origin}/by-agent/440/task-board/latest/`,
      },
    };
    api.getCloudArtifacts.mockResolvedValue({ artifacts: [artifact] });
    api.getAgents.mockResolvedValue({
      agents: [{ uid: 440, is_bot: true, cloud_artifacts_enabled: true }],
    });
    api.createArtifactTask.mockResolvedValue({
      contract_version: 'catsco.artifact-task-ref.v1',
      task_id: taskId,
      task_ref: taskRef,
      status: 'submitted',
      delivery_status: 'pending',
      visible_message: '来自「任务看板」：创建任务',
      expires_at: '2026-08-26T12:00:00Z',
    });
    const pendingTaskStatus = {
      contract_version: 'catsco.artifact-task-status.v1',
      task_id: taskId,
      status: 'submitted',
      delivery_status: 'pending',
      updated_at: '2026-08-26T03:00:00Z',
      expires_at: '2026-08-26T12:00:00Z',
    };
    const deliveredTaskStatus = {
      contract_version: 'catsco.artifact-task-status.v1',
      task_id: taskId,
      status: 'running',
      delivery_status: 'delivered',
      run_id: 'run-42',
      updated_at: '2026-08-26T03:00:01Z',
      expires_at: '2026-08-26T12:00:00Z',
    };
    let taskStatusReads = 0;
    api.getArtifactTask.mockImplementation(async () => {
      taskStatusReads += 1;
      return taskStatusReads <= 6 ? pendingTaskStatus : deliveredTaskStatus;
    });
    api.sendMessage
      .mockRejectedValueOnce(new TypeError('response connection closed'))
      .mockRejectedValueOnce(new TypeError('response connection still closed'));
    api.failArtifactTask.mockRejectedValueOnce(Object.assign(
      new Error('Artifact task delivery is still in progress'),
      {
        status: 409,
        data: { code: 'artifact_task_delivery_pending', delivery_status: 'pending' },
      },
    ));

    await mountTopic(root, 'p2p_1_440', {
      cloudArtifactsRequest: { agentUid: 440, requestId: 1 },
    });
    await act(async () => { await flushPromises(); });
    await act(async () => {
      Simulate.click([...container.querySelectorAll('button[role="tab"]')]
        .find((button) => button.textContent === '应用'));
      await flushPromises();
    });
    await act(async () => {
      Simulate.click(container.querySelector('button[aria-label="预览 任务看板"]'));
      await flushPromises();
    });
    expect(posted.some(message => message.type === 'catsco.artifact.host.connect.v1')).toBe(true);

    await act(async () => {
      dispatchFrameMessage({ postMessage() {} }, origin, {
        type: 'catsco.artifact.task.request.v1',
        request_id: 'task-request-bad-source',
        intent_id: 'tasks.create.v1',
        payload: { title: 'must not run' },
      });
      await flushPromises();
    });
    expect(api.createArtifactTask).not.toHaveBeenCalled();

    feedbackConfirm.mockResolvedValueOnce(false);
    await act(async () => {
      dispatchFrameMessage(frameWindow, origin, {
        type: 'catsco.artifact.task.request.v1',
        request_id: 'task-request-raw-bypass',
        intent_id: 'tasks.create.v1',
        payload: { title: 'must require Host confirmation' },
      });
      await flushPromises();
    });
    expect(feedbackConfirm).toHaveBeenCalledWith(expect.objectContaining({
      confirmLabel: '确认发送',
    }));
    expect(api.createArtifactTask).not.toHaveBeenCalled();
    expect(posted).toContainEqual(expect.objectContaining({
      type: 'catsco.artifact.task.rejected.v1',
      request_id: 'task-request-raw-bypass',
      code: 'task_request_cancelled',
    }));

    await act(async () => {
      dispatchFrameMessage(frameWindow, origin, {
        type: 'catsco.artifact.task.request.v1',
        request_id: 'task-request-42',
        intent_id: 'tasks.create.v1',
        payload: { title: '准备发布清单' },
      });
      await vi.waitFor(() => {
        expect(posted.some(message => message.type === 'catsco.artifact.task.status.v1'
          && message.task?.delivery_status === 'delivered')).toBe(true);
      }, { timeout: 3000 });
      await flushPromises();
    });

    expect(api.createArtifactTask).toHaveBeenCalledWith({
      topic_id: 'p2p_1_440',
      artifact_ref: {
        contract_version: 'catsco.artifact-ref.v1',
        id: 'task-board',
        displayed_version: 3,
        currently_visible: true,
      },
      intent_id: 'tasks.create.v1',
      payload: { title: '准备发布清单' },
      page_context: expect.objectContaining({
        semantic_context: { selected_task: 'task-7', view: 'board' },
      }),
    }, { timeoutMs: 5000 });
    expect(api.sendMessage).toHaveBeenCalledWith('p2p_1_440', {
      type: 'text',
      content: '来自「任务看板」：创建任务',
      client_msg_id: `artifact-task:${taskId}`,
      metadata: { artifact_task_ref: taskRef },
    });
    expect(api.sendMessage).toHaveBeenCalledTimes(2);
    expect(api.failArtifactTask).toHaveBeenCalledWith(taskId, { timeoutMs: 5000 });
    const accepted = posted.find(message => message.type === 'catsco.artifact.task.accepted.v1');
    expect(accepted).toMatchObject({
      request_id: 'task-request-42',
      task: { task_id: taskId, status: 'submitted', delivery_status: 'pending' },
    });
    expect(JSON.stringify(accepted)).not.toContain(taskRef);
    expect(posted).toContainEqual(expect.objectContaining({
      type: 'catsco.artifact.task.status.v1',
      task: expect.objectContaining({
        task_id: taskId,
        status: 'running',
        delivery_status: 'delivered',
      }),
    }));
    expect(posted).not.toContainEqual(expect.objectContaining({
      type: 'catsco.artifact.task.rejected.v1',
      request_id: 'task-request-42',
    }));

    await act(async () => {
      wsHandler({
        artifact_result: {
          type: 'request',
          origin_node_id: 'catsco-node-1',
          task_id: taskId,
          writeback_ref: `awr_${'w'.repeat(43)}`,
          topic_id: 'p2p_1_440',
          agent_uid: '440',
          artifact_id: 'task-board',
          displayed_version: 3,
          sink_id: 'tasks.upsert.v1',
          result_id: resultId,
          payload: { items: [{ title: '准备发布清单' }] },
        },
      });
      await new Promise((resolve) => setTimeout(resolve, 5));
      await flushPromises();
    });
    expect(appliedResult).toMatchObject({
      artifact_id: 'task-board',
      sink_id: 'tasks.upsert.v1',
      result_id: resultId,
    });
    expect(appliedResult.task_id).toBeUndefined();
    expect(wsSendArtifactResultReceipt).toHaveBeenCalledWith(expect.objectContaining({
      task_id: taskId,
      result_id: resultId,
      receipt: expect.objectContaining({ status: 'applied' }),
    }));
  });

  it('does not deliver a task turn when the preview changes after task creation started', async () => {
    const origin = 'https://artifacts.example.test';
    const taskId = `atk_${'s'.repeat(43)}`;
    const taskRef = `atr_${'p'.repeat(43)}`;
    let resolveCreate;
    const frameWindow = {
      postMessage(message, targetOrigin) {
        expect(targetOrigin).toBe(origin);
        if (message.type === 'catsco.artifact.context.request.v1') {
          window.setTimeout(() => dispatchFrameMessage(frameWindow, origin, {
            type: 'catsco.artifact.context.response.v1',
            request_id: message.request_id,
            context: {
              contract_version: 'catsco.artifact-page-context.v1',
              observed_at: '2026-08-26T03:00:00Z',
            },
          }), 0);
        }
      },
    };
    const artifact = {
      id: 'task-board',
      agent_uid: '440',
      title: '任务看板',
      kind: 'mini_app',
      url: `${origin}/by-agent/440/task-board/latest/`,
      status: 'active',
      publish_version: 3,
      can_delete: true,
      artifact_frame_binding: {
        frame: { contentWindow: frameWindow },
        artifactId: 'task-board',
        agentUid: 440,
        url: `${origin}/by-agent/440/task-board/latest/`,
      },
    };
    api.getCloudArtifacts.mockResolvedValue({ artifacts: [artifact] });
    api.getAgents.mockResolvedValue({
      agents: [{ uid: 440, is_bot: true, cloud_artifacts_enabled: true }],
    });
    api.createArtifactTask.mockReturnValue(new Promise((resolvePromise) => {
      resolveCreate = resolvePromise;
    }));

    await mountTopic(root, 'p2p_1_440', {
      cloudArtifactsRequest: { agentUid: 440, requestId: 1 },
    });
    await act(async () => { await flushPromises(); });
    await act(async () => {
      Simulate.click([...container.querySelectorAll('button[role="tab"]')]
        .find((button) => button.textContent === '应用'));
      await flushPromises();
    });
    await act(async () => {
      Simulate.click(container.querySelector('button[aria-label="预览 任务看板"]'));
      await flushPromises();
    });
    await act(async () => {
      dispatchFrameMessage(frameWindow, origin, {
        type: 'catsco.artifact.task.request.v1',
        request_id: 'task-request-stale-after-create',
        intent_id: 'tasks.create.v1',
        payload: { title: '不应发送' },
      });
      await new Promise((resolvePromise) => setTimeout(resolvePromise, 10));
      await flushPromises();
    });
    expect(api.createArtifactTask).toHaveBeenCalledTimes(1);

    await act(async () => {
      Simulate.click(container.querySelector('.mock-close-preview'));
      resolveCreate({
        contract_version: 'catsco.artifact-task-ref.v1',
        task_id: taskId,
        task_ref: taskRef,
        status: 'submitted',
        delivery_status: 'pending',
        visible_message: '来自「任务看板」：创建任务',
        expires_at: '2026-08-26T12:00:00Z',
      });
      await flushPromises();
    });

    expect(api.sendMessage).not.toHaveBeenCalled();
    expect(api.failArtifactTask).toHaveBeenCalledWith(taskId, { timeoutMs: 5000 });
  });

  it('drops a stale Artifact reference when the preview closes during page capture', async () => {
    const origin = 'https://artifacts.example.test';
    let respondToSnapshot = null;
    const frameWindow = {
      postMessage(message, targetOrigin) {
        expect(targetOrigin).toBe(origin);
        respondToSnapshot = () => {
          const event = new Event('message');
          Object.defineProperties(event, {
            source: { value: frameWindow },
            origin: { value: origin },
            data: {
              value: {
                type: 'catsco.artifact.context.response.v1',
                request_id: message.request_id,
                context: {
                  contract_version: 'catsco.artifact-page-context.v1',
                  observed_at: '2026-08-10T12:00:00Z',
                  selected_text: '旧页面状态',
                },
              },
            },
          });
          window.dispatchEvent(event);
        };
      },
    };
    const artifact = {
      id: 'lesson-game',
      agent_uid: '440',
      title: '课堂小游戏',
      kind: 'html',
      url: `${origin}/by-agent/440/lesson-game/latest/`,
      status: 'active',
      publish_version: 2,
      can_delete: true,
      artifact_frame_binding: {
        frame: { contentWindow: frameWindow },
        artifactId: 'lesson-game',
        agentUid: 440,
        url: `${origin}/by-agent/440/lesson-game/latest/`,
      },
    };
    api.getCloudArtifacts.mockResolvedValue({ artifacts: [artifact] });
    api.getAgents.mockResolvedValue({
      agents: [{
        uid: 440,
        username: 'artifact-agent',
        display_name: 'Artifact Agent',
        is_bot: true,
        cloud_artifacts_enabled: true,
      }],
    });

    await mountTopic(root, 'p2p_1_440', {
      cloudArtifactsRequest: { agentUid: 440, requestId: 1 },
    });
    await act(async () => {
      await flushPromises();
    });
    await act(async () => {
      Simulate.click([...container.querySelectorAll('button[role="tab"]')]
        .find((button) => button.textContent === '应用'));
      await flushPromises();
    });
    await act(async () => {
      Simulate.click(container.querySelector('button[aria-label="预览 课堂小游戏"]'));
      await flushPromises();
    });

    await act(async () => {
      typeDraft(container.querySelector('textarea.v3-composer-input'), '分析这些');
      await flushPromises();
    });
    await act(async () => {
      Simulate.click(container.querySelector('button[aria-label="发送"]'));
      await Promise.resolve();
    });
    expect(respondToSnapshot).toEqual(expect.any(Function));

    await act(async () => {
      Simulate.click(container.querySelector('.mock-close-preview'));
      respondToSnapshot();
      await flushPromises();
    });

    expect(api.sendMessage).toHaveBeenCalledWith('p2p_1_440', '分析这些', undefined);
  });

  it('does not rebind an in-flight message when the user switches to another Artifact', async () => {
    const origin = 'https://artifacts.example.test';
    let respondToFirstSnapshot = null;
    const firstFrameWindow = {
      postMessage(message) {
        respondToFirstSnapshot = () => {
          const event = new Event('message');
          Object.defineProperties(event, {
            source: { value: firstFrameWindow },
            origin: { value: origin },
            data: {
              value: {
                type: 'catsco.artifact.context.response.v1',
                request_id: message.request_id,
                context: {
                  contract_version: 'catsco.artifact-page-context.v1',
                  observed_at: '2026-08-10T12:00:00Z',
                  selected_text: 'Artifact A',
                },
              },
            },
          });
          window.dispatchEvent(event);
        };
      },
    };
    const artifacts = [
      {
        id: 'lesson-game',
        agent_uid: '440',
        title: '课堂小游戏',
        kind: 'html',
        url: `${origin}/by-agent/440/lesson-game/latest/`,
        status: 'active',
        publish_version: 2,
        can_delete: true,
        artifact_frame_binding: {
          frame: { contentWindow: firstFrameWindow },
          artifactId: 'lesson-game',
          agentUid: 440,
          url: `${origin}/by-agent/440/lesson-game/latest/`,
        },
      },
      {
        id: 'report-board',
        agent_uid: '440',
        title: '分析看板',
        kind: 'html',
        url: `${origin}/by-agent/440/report-board/latest/`,
        status: 'active',
        publish_version: 1,
        can_delete: true,
      },
    ];
    api.getCloudArtifacts.mockResolvedValue({ artifacts });
    api.getAgents.mockResolvedValue({
      agents: [{
        uid: 440,
        username: 'artifact-agent',
        display_name: 'Artifact Agent',
        is_bot: true,
        cloud_artifacts_enabled: true,
      }],
    });

    await mountTopic(root, 'p2p_1_440', {
      cloudArtifactsRequest: { agentUid: 440, requestId: 1 },
    });
    await act(async () => {
      await flushPromises();
    });
    await act(async () => {
      Simulate.click([...container.querySelectorAll('button[role="tab"]')]
        .find((button) => button.textContent === '应用'));
      await flushPromises();
    });
    await act(async () => {
      Simulate.click(container.querySelector('button[aria-label="预览 课堂小游戏"]'));
      await flushPromises();
    });

    await act(async () => {
      typeDraft(container.querySelector('textarea.v3-composer-input'), '把这里改一下');
      await flushPromises();
    });
    await act(async () => {
      Simulate.click(container.querySelector('button[aria-label="发送"]'));
      await Promise.resolve();
    });
    expect(respondToFirstSnapshot).toEqual(expect.any(Function));

    await act(async () => {
      Simulate.click(container.querySelector('button[aria-label="返回云文件"]'));
      await flushPromises();
    });
    await act(async () => {
      Simulate.click(container.querySelector('button[aria-label="预览 分析看板"]'));
      await flushPromises();
    });
    await act(async () => {
      respondToFirstSnapshot();
      await flushPromises();
    });

    expect(api.sendMessage).toHaveBeenCalledWith('p2p_1_440', '把这里改一下', undefined);
  });

  it('drops an in-flight Artifact snapshot when another topic uses the same Agent', async () => {
    const origin = 'https://artifacts.example.test';
    let respondToSnapshot = null;
    const frameWindow = {
      postMessage(message) {
        respondToSnapshot = () => {
          const event = new Event('message');
          Object.defineProperties(event, {
            source: { value: frameWindow },
            origin: { value: origin },
            data: {
              value: {
                type: 'catsco.artifact.context.response.v1',
                request_id: message.request_id,
                context: {
                  contract_version: 'catsco.artifact-page-context.v1',
                  observed_at: '2026-08-10T12:00:00Z',
                  selected_text: '旧 topic 的页面状态',
                },
              },
            },
          });
          window.dispatchEvent(event);
        };
      },
    };
    const artifact = {
      id: 'lesson-game',
      agent_uid: '440',
      title: '课堂小游戏',
      kind: 'html',
      url: `${origin}/by-agent/440/lesson-game/latest/`,
      status: 'active',
      publish_version: 2,
      can_delete: true,
      artifact_frame_binding: {
        frame: { contentWindow: frameWindow },
        artifactId: 'lesson-game',
        agentUid: 440,
        url: `${origin}/by-agent/440/lesson-game/latest/`,
      },
    };
    api.getCloudArtifacts.mockResolvedValue({ artifacts: [artifact] });
    api.getAgents.mockResolvedValue({
      agents: [{
        uid: 440,
        username: 'artifact-agent',
        display_name: 'Artifact Agent',
        is_bot: true,
        cloud_artifacts_enabled: true,
      }],
    });
    api.getGroupInfo.mockImplementation((groupId) => Promise.resolve({
      group: { id: groupId, name: `Artifact topic ${groupId}`, kind: 'agent_task', is_agent_task: true },
      members: [
        { user_id: 1, display_name: 'Me', is_bot: false },
        { user_id: 440, display_name: 'Artifact Agent', is_bot: true },
      ],
    }));

    const cloudArtifactsRequest = { agentUid: 440, requestId: 1 };
    let switchTopic = null;
    function SnapshotResponder({ enabled }) {
      React.useLayoutEffect(() => {
        if (enabled) respondToSnapshot?.();
      }, [enabled]);
      return null;
    }
    function TopicHarness() {
      const [current, setCurrent] = React.useState({ topic: 'grp_91', groupId: 91 });
      switchTopic = () => setCurrent({ topic: 'grp_92', groupId: 92 });
      return (
        <>
          <MessagesView
            topic={current.topic}
            topicName={current.topic}
            user={user}
            isGroup
            groupId={current.groupId}
            topicAvatarUrl=""
            onTopicUpdated={vi.fn()}
            cloudArtifactsRequest={cloudArtifactsRequest}
          />
          <SnapshotResponder enabled={current.topic === 'grp_92'} />
        </>
      );
    }

    await act(async () => {
      root.render(<TopicHarness />);
      await flushPromises();
    });
    await act(async () => {
      Simulate.click([...container.querySelectorAll('button[role="tab"]')]
        .find((button) => button.textContent === '应用'));
      await flushPromises();
    });
    await act(async () => {
      Simulate.click(container.querySelector('button[aria-label="预览 课堂小游戏"]'));
      await flushPromises();
    });
    await act(async () => {
      typeDraft(container.querySelector('textarea.v3-composer-input'), '分析这些');
      await flushPromises();
    });
    await act(async () => {
      Simulate.click(container.querySelector('button[aria-label="发送"]'));
      await Promise.resolve();
    });
    expect(respondToSnapshot).toEqual(expect.any(Function));

    await act(async () => {
      switchTopic();
      await flushPromises();
    });

    expect(api.sendMessage).toHaveBeenCalledWith('grp_91', '分析这些', undefined);
  });

  it('finds a conversation file from history and opens it in the existing file preview', async () => {
    const historicalFile = {
      id: '820:0',
      name: '期末学情报告.pdf',
      url: '/uploads/files/term-report.pdf',
      mime_type: 'application/pdf',
      size: 728341,
      topic_name: '期末材料',
    };
    api.getTopicFiles.mockResolvedValue({
      files: [historicalFile],
      has_more: false,
      next_before_id: 0,
    });

    await mountTopic(root, 'p2p_1_440', {
      cloudArtifactsRequest: { agentUid: 440, requestId: 1 },
    });
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(api.getTopicFiles).toHaveBeenCalledWith('p2p_1_440', {
      beforeId: 0,
      limit: 40,
    });
    expect(api.getAgentFiles).not.toHaveBeenCalled();
    await act(async () => {
      Simulate.click(container.querySelector('button[aria-label="预览文件 期末学情报告.pdf"]'));
      await Promise.resolve();
    });

    expect(container.querySelector('.cloud-artifacts-panel')).toBeNull();
    const preview = container.querySelector('.mock-file-preview');
    expect(preview?.textContent).toContain('期末学情报告.pdf');
    expect(preview?.getAttribute('data-url')).toBe(historicalFile.url);

    await act(async () => {
      Simulate.click(container.querySelector('button[aria-label="返回云文件"]'));
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(container.querySelector('.cloud-artifacts-panel')).not.toBeNull();
    expect([...container.querySelectorAll('button[role="tab"]')]
      .find((button) => button.textContent === '文件')
      ?.getAttribute('aria-selected')).toBe('true');
    expect(api.getTopicFiles).toHaveBeenCalledTimes(2);
    expect(api.getAgentFiles).not.toHaveBeenCalled();
  });

  it('scopes the file panel request to the current group conversation', async () => {
    await mountTopic(root, 'grp_80', {
      isGroup: true,
      groupId: 80,
      cloudArtifactsRequest: { agentUid: 440, requestId: 1 },
    });
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(api.getTopicFiles).toHaveBeenCalledWith('grp_80', {
      beforeId: 0,
      limit: 40,
    });
    expect(api.getAgentFiles).not.toHaveBeenCalled();
  });

  it('opens conversation files without an Agent and hides the results tab', async () => {
    await mountTopic(root, 'p2p_1_2', {
      cloudArtifactsRequest: { agentUid: 0, requestId: 1 },
    });
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(api.getTopicFiles).toHaveBeenCalledWith('p2p_1_2', {
      beforeId: 0,
      limit: 40,
    });
    expect(container.querySelector('.cloud-artifacts-panel')).not.toBeNull();
    expect([...container.querySelectorAll('button[role="tab"]')].map((button) => button.textContent))
      .toEqual(['文件']);
    expect(api.getCloudArtifacts).not.toHaveBeenCalled();
  });

  it('keeps a normal text paste in the composer without starting an upload', async () => {
    await mountTopic(root, 'p2p_1_2');
    const textarea = container.querySelector('textarea.v3-composer-input');

    const pasteEvent = pasteInto(textarea, { text: '这是普通长度的粘贴内容。' });
    await act(async () => {
      await flushPromises();
    });

    expect(pasteEvent.defaultPrevented).toBe(false);
    expect(api.uploadFile).not.toHaveBeenCalled();
  });

  it('turns a long text paste into a Markdown attachment and sends it through the file message path', async () => {
    api.uploadFile.mockImplementationOnce(async (file) => ({
      file_key: `long-paste/${file.name}`,
      url: `/uploads/files/${file.name}`,
      name: file.name,
      size: file.size,
      mime_type: file.type,
    }));
    await mountTopic(root, 'p2p_1_2');
    const textarea = container.querySelector('textarea.v3-composer-input');
    const pastedText = `产品需求说明\n\n${'这是一段需要作为文档发送的详细内容。'.repeat(260)}`;

    let pasteEvent;
    await act(async () => {
      pasteEvent = pasteInto(textarea, { text: pastedText });
      await flushPromises();
    });

    expect(pasteEvent.defaultPrevented).toBe(true);
    expect(textarea.value).toBe('');
    expect(api.uploadFile).toHaveBeenCalledTimes(1);
    const [uploadedFile, requestedType] = api.uploadFile.mock.calls[0];
    expect(requestedType).toBe('file');
    expect(uploadedFile.name).toMatch(/^粘贴内容-\d{8}-\d{6}\.md$/u);
    expect(uploadedFile.type).toBe('text/markdown;charset=utf-8');
    expect(container.querySelector('.v3-composer-attachment-chip.is-file')?.textContent)
      .toContain(uploadedFile.name);
    expect(container.textContent).toContain('长文本已整理为文档');

    await act(async () => {
      Simulate.click(container.querySelector('button[aria-label="发送"]'));
      await flushPromises();
    });

    const [, payload] = api.sendMessage.mock.calls.at(-1);
    expect(payload.type).toBe('text');
    expect(payload.content).toBe(`[文件] ${uploadedFile.name}`);
    expect(payload.content_blocks).toEqual([{
      type: 'file',
      payload: {
        file_key: `long-paste/${uploadedFile.name}`,
        url: `/uploads/files/${uploadedFile.name}`,
        name: uploadedFile.name,
        size: uploadedFile.size,
        mime_type: 'text/markdown;charset=utf-8',
      },
    }]);
  });

  it('restores the original long paste at the caret when document upload fails', async () => {
    api.uploadFile.mockRejectedValueOnce(new Error('network unavailable'));
    await mountTopic(root, 'p2p_1_2');
    const textarea = container.querySelector('textarea.v3-composer-input');
    const pastedText = '长文本'.repeat(1400);
    await act(async () => {
      typeDraft(textarea, '前后');
    });
    textarea.setSelectionRange(1, 1);

    await act(async () => {
      pasteInto(textarea, { text: pastedText });
      await flushPromises();
    });

    expect(textarea.value).toBe(`前${pastedText}后`);
    expect(container.querySelector('.v3-composer-attachment-chip')).toBeNull();
    expect(container.textContent).toContain('原文已恢复到输入框');
  });

  it('keeps clipboard files ahead of long clipboard text', async () => {
    const image = new File(['image'], 'clipboard.png', { type: 'image/png' });
    api.uploadFile.mockResolvedValueOnce({
      file_key: 'clipboard.png',
      url: '/uploads/images/clipboard.png',
      name: 'clipboard.png',
      size: image.size,
      mime_type: image.type,
    });
    await mountTopic(root, 'p2p_1_2');
    const textarea = container.querySelector('textarea.v3-composer-input');

    await act(async () => {
      pasteInto(textarea, { text: '不会被转换'.repeat(1000), files: [image] });
      await flushPromises();
    });

    expect(api.uploadFile).toHaveBeenCalledTimes(1);
    expect(api.uploadFile).toHaveBeenCalledWith(image, 'image');
    expect(container.querySelector('[aria-label="预览图片：clipboard.png"]')).not.toBeNull();
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

    expect(container.textContent).toContain('暂时无法获取聊天记录');
    const retryButton = Array.from(container.querySelectorAll('button'))
      .find((button) => button.textContent.includes('重新加载'));
    expect(retryButton).not.toBeNull();

    await act(async () => {
      Simulate.click(retryButton);
      await flushPromises();
    });

    expect(container.textContent).not.toContain('暂时无法获取聊天记录');
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

    expect(container.textContent).toContain('获取聊天记录超时，请重试');
    expect(Array.from(container.querySelectorAll('button'))
      .some((button) => button.textContent.includes('重新加载'))).toBe(true);
  });

  it('classifies a gateway failure while loading older history', async () => {
    const latest = Array.from({ length: 50 }, (_, index) => ({
      id: 101 + index,
      seq_id: 101 + index,
      topic_id: 'p2p_1_2',
      from_uid: index % 2 === 0 ? 1 : 2,
      type: 'text',
      content: `latest-${index}`,
    }));
    const unavailable = Object.assign(new Error('bad gateway'), { status: 502 });
    api.getMessages
      .mockResolvedValueOnce({ messages: latest, has_more: true, next_before_id: 101 })
      .mockRejectedValueOnce(unavailable);

    await mountTopic(root, 'p2p_1_2');
    await act(async () => {
      await flushPromises(16);
    });

    expect(container.textContent).toContain('服务暂时不可用。');
    expect(container.textContent).not.toContain('更早的聊天记录加载失败。');
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
    expect(container.textContent).not.toContain('暂时无法获取聊天记录');
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

  it('keeps cached history and identifies it when the service is unavailable', async () => {
    const unavailable = Object.assign(new Error('bad gateway'), { status: 502 });
    api.getMessages
      .mockResolvedValueOnce({
        messages: [{ id: 1, topic_id: 'p2p_1_2', from_uid: 2, type: 'text', content: 'cached topic A' }],
        has_more: false,
      })
      .mockResolvedValueOnce({
        messages: [{ id: 2, topic_id: 'p2p_1_3', from_uid: 3, type: 'text', content: 'topic B' }],
        has_more: false,
      })
      .mockRejectedValueOnce(unavailable);

    await mountTopic(root, 'p2p_1_2');
    await act(async () => { await flushPromises(); });
    await mountTopic(root, 'p2p_1_3');
    await act(async () => { await flushPromises(); });
    await mountTopic(root, 'p2p_1_2');
    await act(async () => { await flushPromises(); });

    expect(container.querySelector('[data-message-content="cached topic A"]')).not.toBeNull();
    expect(container.textContent).toContain('服务暂时不可用。当前显示');
    expect(container.textContent).toContain('加载的聊天记录');
    expect(container.textContent).not.toContain('后端');
  });

  it('treats a previously loaded empty conversation as a cached result', async () => {
    const unavailable = Object.assign(new Error('bad gateway'), { status: 502 });
    api.getMessages
      .mockResolvedValueOnce({ messages: [], has_more: false })
      .mockResolvedValueOnce({
        messages: [{ id: 2, topic_id: 'p2p_1_3', from_uid: 3, type: 'text', content: 'topic B' }],
        has_more: false,
      })
      .mockRejectedValueOnce(unavailable);

    await mountTopic(root, 'p2p_1_2');
    await act(async () => { await flushPromises(); });
    await mountTopic(root, 'p2p_1_3');
    await act(async () => { await flushPromises(); });
    await mountTopic(root, 'p2p_1_2');
    await act(async () => { await flushPromises(); });

    expect(container.textContent).toContain('服务暂时不可用。当前显示');
    expect(container.textContent).toContain('加载的聊天记录');
    expect(container.textContent).not.toContain('暂时无法获取聊天记录');
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
    expect(api.getCloudArtifacts).toHaveBeenCalledWith(440, 'active', {
      signal: expect.any(AbortSignal),
    });
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

    expect(api.getCloudArtifacts).toHaveBeenCalledWith(310, 'active', {
      signal: expect.any(AbortSignal),
    });
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

  it('does not announce an Artifact that was already present when history loaded', async () => {
    const artifactURL = 'https://artifacts.example.test/by-agent/440/history/latest/';
    api.getMessages.mockResolvedValue({
      messages: [{
        id: 700,
        from_uid: 440,
        content: `已发布：${artifactURL}`,
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
    api.getCloudArtifacts.mockResolvedValue({
      artifacts: [{ id: 'history', url: artifactURL }],
    });

    await mountTopic(root, 'p2p_1_440');
    await act(async () => {
      await flushPromises();
    });

    expect(feedbackNotify).not.toHaveBeenCalled();
  });

  it('announces a newly completed republish even when the latest URL is unchanged', async () => {
    const artifactURL = 'https://artifacts.example.test/by-agent/440/reused/latest/';
    api.getMessages.mockResolvedValue({
      messages: [{
        id: 700,
        from_uid: 440,
        content: `已发布：${artifactURL}`,
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
    api.getCloudArtifacts.mockResolvedValue({
      artifacts: [{ id: 'reused', url: artifactURL, publish_version: 2 }],
    });

    await mountTopic(root, 'p2p_1_440');
    await act(async () => {
      await flushPromises();
    });
    expect(feedbackNotify).not.toHaveBeenCalled();

    await act(async () => {
      wsHandler({
        data: {
          topic: 'p2p_1_440',
          from: 'usr440',
          seq_id: 704,
          type: 'text',
          content: `已重新发布：${artifactURL}`,
        },
      });
      await flushPromises();
    });

    expect(feedbackNotify).toHaveBeenCalledWith({
      tone: 'success',
      message: '已共享内容到云端',
    });
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
    expect(feedbackNotify).not.toHaveBeenCalled();

    api.getCloudArtifacts.mockResolvedValue({
      artifacts: [{
        id: 'game',
        title: '课堂小游戏',
        url: 'https://artifacts.example.test/by-agent/440/game/latest/',
      }],
    });

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
    expect(feedbackNotify).toHaveBeenCalledWith({
      tone: 'success',
      message: '已共享内容到云端',
    });
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

  it('keeps the current reading position when streaming and scrolling overlap an older-history load', async () => {
    const initialHistory = deferred();
    const olderHistory = deferred();
    api.getMessages
      .mockImplementationOnce(() => initialHistory.promise)
      .mockImplementationOnce(() => olderHistory.promise);

    await mountTopic(root, 'p2p_1_2');
    const timeline = container.querySelector('.v3-timeline');
    let scrollHeight = 1000;
    let scrollTop = 0;
    Object.defineProperty(timeline, 'scrollHeight', {
      configurable: true,
      get: () => (
        container.querySelector('[data-message-content="older message"]') ? 1100 : scrollHeight
      ),
    });
    Object.defineProperty(timeline, 'clientHeight', { configurable: true, value: 500 });
    Object.defineProperty(timeline, 'scrollTop', {
      configurable: true,
      get: () => scrollTop,
      set: (value) => {
        scrollTop = value;
      },
    });
    timeline.getBoundingClientRect = vi.fn(() => ({ top: 0, bottom: 500 }));
    const messageRectSpy = vi.spyOn(window.HTMLElement.prototype, 'getBoundingClientRect')
      .mockImplementation(function getBoundingClientRect() {
        if (this.dataset?.searchMessageId === '101') {
          if (container.querySelector('[data-message-content="older message"]')) {
            return { top: 180, bottom: 220 };
          }
          if (container.querySelector('.oc-history-load') && scrollTop >= 200) {
            return { top: -40, bottom: 0 };
          }
          if (container.querySelector('.oc-history-load')) {
            return { top: 120, bottom: 160 };
          }
          return { top: 100, bottom: 140 };
        }
        if (this.dataset?.searchMessageId === '102') {
          if (container.querySelector('[data-message-content="older message"]')) {
            return { top: 300, bottom: 340 };
          }
          if (container.querySelector('.oc-history-load') && scrollTop >= 200) {
            return { top: 140, bottom: 180 };
          }
          if (container.querySelector('.oc-history-load')) {
            return { top: 320, bottom: 360 };
          }
          return { top: 300, bottom: 340 };
        }
        return { top: 0, bottom: 0 };
      });

    await act(async () => {
      initialHistory.resolve({
        messages: [
          {
            id: 101,
            seq_id: 101,
            topic_id: 'p2p_1_2',
            from_uid: 2,
            type: 'text',
            content: 'earlier visible message',
          },
          {
            id: 102,
            seq_id: 102,
            topic_id: 'p2p_1_2',
            from_uid: 2,
            type: 'text',
            content: 'latest message',
          },
        ],
        has_more: true,
        next_before_id: 101,
      });
      await flushPromises();
    });
    expect(scrollTop).toBe(1000);

    scrollTop = 100;
    await act(async () => {
      Simulate.scroll(timeline);
      await Promise.resolve();
    });

    scrollHeight = 1020;
    await act(async () => {
      wsHandler({
        data: {
          topic: 'p2p_1_2',
          from: 'usr2',
          type: 'stream_delta',
          content: 'stream update',
          metadata: { stream_id: 'stream-before-history' },
        },
      });
      await flushPromises();
    });
    expect(scrollTop).toBe(100);

    scrollTop = 220;
    await act(async () => {
      Simulate.scroll(timeline);
      await Promise.resolve();
    });

    await act(async () => {
      olderHistory.resolve({
        messages: [{
          id: 100,
          seq_id: 100,
          topic_id: 'p2p_1_2',
          from_uid: 2,
          type: 'text',
          content: 'older message',
        }],
        has_more: false,
        next_before_id: 100,
      });
      await flushPromises();
    });
    messageRectSpy.mockRestore();
    expect(scrollTop).toBe(380);
  });

  it('keeps following when the reader returns to the live edge during an older-history load', async () => {
    const initialHistory = deferred();
    const olderHistory = deferred();
    api.getMessages
      .mockImplementationOnce(() => initialHistory.promise)
      .mockImplementationOnce(() => olderHistory.promise);

    await mountTopic(root, 'p2p_1_2');
    const timeline = container.querySelector('.v3-timeline');
    let scrollHeight = 1000;
    let scrollTop = 0;
    Object.defineProperty(timeline, 'scrollHeight', {
      configurable: true,
      get: () => scrollHeight,
    });
    Object.defineProperty(timeline, 'clientHeight', { configurable: true, value: 500 });
    Object.defineProperty(timeline, 'scrollTop', {
      configurable: true,
      get: () => scrollTop,
      set: (value) => {
        scrollTop = value;
      },
    });

    await act(async () => {
      initialHistory.resolve({
        messages: [{
          id: 101,
          seq_id: 101,
          topic_id: 'p2p_1_2',
          from_uid: 2,
          type: 'text',
          content: 'latest message',
        }],
        has_more: true,
        next_before_id: 101,
      });
      await flushPromises();
    });

    scrollTop = 100;
    await act(async () => {
      Simulate.scroll(timeline);
      await Promise.resolve();
    });

    scrollTop = 500;
    await act(async () => {
      Simulate.scroll(timeline);
      await Promise.resolve();
    });

    scrollHeight = 1100;
    await act(async () => {
      olderHistory.resolve({
        messages: [{
          id: 100,
          seq_id: 100,
          topic_id: 'p2p_1_2',
          from_uid: 2,
          type: 'text',
          content: 'older message',
        }],
        has_more: false,
        next_before_id: 100,
      });
      await flushPromises();
    });

    expect(scrollTop).toBe(1100);
  });

  it('keeps auto-follow enabled when an inner timeline scroller receives up-scroll gestures', async () => {
    await mountTopic(root, 'p2p_1_2');
    await act(async () => {
      await flushPromises();
    });
    const timeline = container.querySelector('.v3-timeline');
    let scrollHeight = 1000;
    let scrollTop = 500;
    Object.defineProperty(timeline, 'scrollHeight', {
      configurable: true,
      get: () => scrollHeight,
    });
    Object.defineProperty(timeline, 'clientHeight', { configurable: true, value: 500 });
    Object.defineProperty(timeline, 'scrollTop', {
      configurable: true,
      get: () => scrollTop,
      set: (value) => {
        scrollTop = value;
      },
    });

    await act(async () => {
      Simulate.scroll(timeline);
      await Promise.resolve();
    });

    const nestedScroller = document.createElement('div');
    nestedScroller.className = 'v3-working-steps';
    timeline.append(nestedScroller);
    await act(async () => {
      nestedScroller.dispatchEvent(new WheelEvent('wheel', { bubbles: true, deltaY: -56 }));
      const touchStart = new Event('touchstart', { bubbles: true });
      Object.defineProperty(touchStart, 'touches', { value: [{ clientY: 320 }] });
      nestedScroller.dispatchEvent(touchStart);
      const touchMove = new Event('touchmove', { bubbles: true });
      Object.defineProperty(touchMove, 'touches', { value: [{ clientY: 376 }] });
      nestedScroller.dispatchEvent(touchMove);
      await Promise.resolve();
    });

    scrollHeight = 1100;
    await act(async () => {
      wsHandler({
        data: {
          seq_id: 36,
          seq: 36,
          topic: 'p2p_1_2',
          from: 'usr2',
          content: 'fresh message',
          type: 'text',
          msg_type: 'text',
        },
      });
      await flushPromises();
    });

    expect(scrollTop).toBe(1100);
  });

  it('follows runtime-only updates while the reader remains at the bottom', async () => {
    await mountTopic(root, 'p2p_1_2');
    await act(async () => {
      await flushPromises();
    });
    const timeline = container.querySelector('.v3-timeline');
    let scrollHeight = 1000;
    let scrollTop = 500;
    Object.defineProperty(timeline, 'scrollHeight', {
      configurable: true,
      get: () => scrollHeight,
    });
    Object.defineProperty(timeline, 'clientHeight', { configurable: true, value: 500 });
    Object.defineProperty(timeline, 'scrollTop', {
      configurable: true,
      get: () => scrollTop,
      set: (value) => {
        scrollTop = value;
      },
    });

    await act(async () => {
      Simulate.scroll(timeline);
      await Promise.resolve();
    });

    scrollHeight = 1080;
    await act(async () => {
      wsHandler({
        data: {
          seq_id: 37,
          seq: 37,
          topic: 'p2p_1_2',
          from: 'usr2',
          content: {
            revision: 1,
            updatedAt: Date.now(),
            steps: [{ text: 'still working', status: 'in_progress' }],
          },
          type: 'runtime_plan',
          msg_type: 'runtime_plan',
        },
      });
      await flushPromises();
    });
    expect(scrollTop).toBe(1080);

    scrollHeight = 1110;
    await act(async () => {
      wsHandler({
        info: {
          topic: 'p2p_1_2',
          what: 'kp',
          from: 'usr2',
        },
      });
      await flushPromises();
    });
    expect(scrollTop).toBe(1110);
  });

  it('keeps auto-follow after an upward wheel gesture cannot move a short timeline', async () => {
    await mountTopic(root, 'p2p_1_2');
    await act(async () => {
      await flushPromises();
    });
    const timeline = container.querySelector('.v3-timeline');
    let scrollHeight = 500;
    let scrollTop = 0;
    Object.defineProperty(timeline, 'scrollHeight', {
      configurable: true,
      get: () => scrollHeight,
    });
    Object.defineProperty(timeline, 'clientHeight', { configurable: true, value: 500 });
    Object.defineProperty(timeline, 'scrollTop', {
      configurable: true,
      get: () => scrollTop,
      set: (value) => {
        scrollTop = value;
      },
    });

    await act(async () => {
      Simulate.scroll(timeline);
      Simulate.wheel(timeline, { deltaY: -56 });
      await Promise.resolve();
    });

    scrollHeight = 1000;
    await act(async () => {
      wsHandler({
        data: {
          seq_id: 38,
          seq: 38,
          topic: 'p2p_1_2',
          from: 'usr2',
          content: {
            revision: 1,
            updatedAt: Date.now(),
            steps: [{ text: 'short timeline update', status: 'in_progress' }],
          },
          type: 'runtime_plan',
          msg_type: 'runtime_plan',
        },
      });
      await flushPromises();
    });

    expect(scrollTop).toBe(1000);
  });

  it('keeps auto-follow after an upward touch gesture cannot move a short timeline', async () => {
    await mountTopic(root, 'p2p_1_2');
    await act(async () => {
      await flushPromises();
    });
    const timeline = container.querySelector('.v3-timeline');
    let scrollHeight = 500;
    let scrollTop = 0;
    Object.defineProperty(timeline, 'scrollHeight', {
      configurable: true,
      get: () => scrollHeight,
    });
    Object.defineProperty(timeline, 'clientHeight', { configurable: true, value: 500 });
    Object.defineProperty(timeline, 'scrollTop', {
      configurable: true,
      get: () => scrollTop,
      set: (value) => {
        scrollTop = value;
      },
    });

    await act(async () => {
      Simulate.scroll(timeline);
      Simulate.touchStart(timeline, { touches: [{ clientY: 320 }] });
      Simulate.touchMove(timeline, { touches: [{ clientY: 376 }] });
      await Promise.resolve();
    });

    scrollHeight = 1000;
    await act(async () => {
      wsHandler({
        data: {
          seq_id: 39,
          seq: 39,
          topic: 'p2p_1_2',
          from: 'usr2',
          content: {
            revision: 1,
            updatedAt: Date.now(),
            steps: [{ text: 'short timeline update', status: 'in_progress' }],
          },
          type: 'runtime_plan',
          msg_type: 'runtime_plan',
        },
      });
      await flushPromises();
    });

    expect(scrollTop).toBe(1000);
  });

  it('keeps a manually up-scrolled conversation fixed during a runtime-plan update', async () => {
    await mountTopic(root, 'p2p_1_2');
    const timeline = container.querySelector('.v3-timeline');
    Object.defineProperty(timeline, 'scrollHeight', { configurable: true, value: 1000 });
    Object.defineProperty(timeline, 'clientHeight', { configurable: true, value: 500 });

    timeline.scrollTop = 500;
    await act(async () => {
      Simulate.scroll(timeline);
      await Promise.resolve();
    });

    timeline.scrollTop = 444;
    await act(async () => {
      Simulate.wheel(timeline, { deltaY: -56 });
      Simulate.scroll(timeline);
      await Promise.resolve();
    });

    const scrollCallsBeforeUpdate = window.HTMLElement.prototype.scrollIntoView.mock.calls.length;
    await act(async () => {
      wsHandler({
        data: {
          seq_id: 28,
          seq: 28,
          topic: 'p2p_1_2',
          from: 'usr2',
          content: {
            revision: 1,
            updatedAt: Date.now(),
            steps: [{ text: '仍在加载', status: 'in_progress' }],
          },
          type: 'runtime_plan',
          msg_type: 'runtime_plan',
        },
      });
      await Promise.resolve();
    });

    expect(timeline.scrollTop).toBe(444);
    expect(window.HTMLElement.prototype.scrollIntoView)
      .toHaveBeenCalledTimes(scrollCallsBeforeUpdate);
  });

  it('does not resume auto-follow after browser anchoring adjusts a near-bottom reader', async () => {
    await mountTopic(root, 'p2p_1_2');
    await act(async () => {
      await flushPromises();
    });
    const timeline = container.querySelector('.v3-timeline');
    let scrollHeight = 1000;
    let scrollTop = 500;
    Object.defineProperty(timeline, 'scrollHeight', {
      configurable: true,
      get: () => scrollHeight,
    });
    Object.defineProperty(timeline, 'clientHeight', { configurable: true, value: 500 });
    Object.defineProperty(timeline, 'scrollTop', {
      configurable: true,
      get: () => scrollTop,
      set: (value) => {
        scrollTop = value;
      },
    });

    await act(async () => {
      Simulate.scroll(timeline);
      await Promise.resolve();
    });

    scrollTop = 444;
    await act(async () => {
      Simulate.wheel(timeline, { deltaY: -56 });
      Simulate.scroll(timeline);
      await Promise.resolve();
    });

    // Native scroll anchoring can compensate for new content above the reader
    // without representing an explicit request to return to the live edge.
    scrollHeight = 1100;
    scrollTop = 544;
    await act(async () => {
      Simulate.scroll(timeline);
      await Promise.resolve();
    });

    scrollHeight = 1180;
    await act(async () => {
      wsHandler({
        data: {
          seq_id: 29,
          seq: 29,
          topic: 'p2p_1_2',
          from: 'usr2',
          content: {
            revision: 1,
            updatedAt: Date.now(),
            steps: [{ text: '仍在加载', status: 'in_progress' }],
          },
          type: 'runtime_plan',
          msg_type: 'runtime_plan',
        },
      });
      await flushPromises();
    });

    expect(scrollTop).toBe(544);
  });

  it('stops auto-follow when a touch drag moves toward older messages', async () => {
    await mountTopic(root, 'p2p_1_2');
    const timeline = container.querySelector('.v3-timeline');
    Object.defineProperty(timeline, 'scrollHeight', { configurable: true, value: 1000 });
    Object.defineProperty(timeline, 'clientHeight', { configurable: true, value: 500 });

    timeline.scrollTop = 500;
    await act(async () => {
      Simulate.scroll(timeline);
      Simulate.touchStart(timeline, { touches: [{ clientY: 320 }] });
      timeline.scrollTop = 444;
      Simulate.scroll(timeline);
      Simulate.touchMove(timeline, { touches: [{ clientY: 376 }] });
      await Promise.resolve();
    });

    const scrollCallsBeforeUpdate = window.HTMLElement.prototype.scrollIntoView.mock.calls.length;
    await act(async () => {
      wsHandler({
        data: {
          seq_id: 30,
          seq: 30,
          topic: 'p2p_1_2',
          from: 'usr2',
          content: '正在处理',
          type: 'tool_use',
          msg_type: 'tool_use',
          metadata: { id: 'tool-30', input: { task: '加载进度' } },
        },
      });
      await Promise.resolve();
    });

    expect(timeline.scrollTop).toBe(444);
    expect(window.HTMLElement.prototype.scrollIntoView)
      .toHaveBeenCalledTimes(scrollCallsBeforeUpdate);
  });

  it('follows fresh messages within the timeline without scrolling the page', async () => {
    const initialHistory = deferred();
    api.getMessages.mockImplementationOnce(() => initialHistory.promise);

    await mountTopic(root, 'p2p_1_2');
    const timeline = container.querySelector('.v3-timeline');
    let scrollTop = 0;
    Object.defineProperty(timeline, 'scrollHeight', { configurable: true, value: 1000 });
    Object.defineProperty(timeline, 'clientHeight', { configurable: true, value: 500 });
    Object.defineProperty(timeline, 'scrollTop', {
      configurable: true,
      get: () => scrollTop,
      set: (value) => {
        scrollTop = value;
      },
    });

    await act(async () => {
      initialHistory.resolve({
        messages: [{
          id: 29,
          seq_id: 29,
          topic_id: 'p2p_1_2',
          from_uid: 2,
          type: 'text',
          content: '最新回复',
        }],
        has_more: false,
      });
      await flushPromises();
    });

    expect(scrollTop).toBe(1000);
    expect(window.HTMLElement.prototype.scrollIntoView).not.toHaveBeenCalled();
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

  it('keeps the composer border active from sending until the Agent final reply', async () => {
    api.getAgents.mockResolvedValueOnce({
      agents: [{
        uid: 2,
        id: 2,
        topic_id: 'p2p_1_2',
        display_name: 'Agent Two',
        is_bot: true,
        relation: 'friend',
      }],
    });
    await mountTopic(root, 'p2p_1_2');
    await act(async () => {
      await flushPromises();
    });

    const textarea = container.querySelector('textarea.v3-composer-input');
    await act(async () => {
      typeDraft(textarea, '开始处理这个任务');
    });
    await act(async () => {
      Simulate.keyDown(textarea, { key: 'Enter', shiftKey: false });
      await flushPromises();
    });

    const composerBox = container.querySelector('.v3-composer-box');
    expect(composerBox.classList.contains('is-agent-reply-active')).toBe(true);
    expect(composerBox.getAttribute('aria-busy')).toBe('true');

    await act(async () => {
      wsHandler({
        data: {
          seq_id: 101,
          seq: 101,
          topic: 'p2p_1_2',
          from: 'usr2',
          content: '任务已经完成',
          type: 'text',
          msg_type: 'text',
        },
      });
    });

    expect(composerBox.classList.contains('is-agent-reply-active')).toBe(false);
    expect(composerBox.getAttribute('aria-busy')).toBe('false');
  });
});
