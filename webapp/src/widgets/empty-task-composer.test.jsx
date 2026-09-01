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
import {
  createComposerDraftStore,
  writeComposerTaskContextDraft,
} from '../utils/composer-draft-storage';
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
    localStorage.clear();
    sessionStorage.clear();

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
    localStorage.clear();
    sessionStorage.clear();
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

  it('persists uploaded attachments through the shared draft store interface', async () => {
    const inputDrafts = new Map();
    const attachmentDrafts = new Map();
    const composerDraftStore = {
      getInputDraft: vi.fn((key) => inputDrafts.get(key) || ''),
      setInputDraft: vi.fn((key, value) => {
        if (value) inputDrafts.set(key, value);
        else inputDrafts.delete(key);
      }),
      getAttachmentDraft: vi.fn((key) => attachmentDrafts.get(key) || []),
      setAttachmentDraft: vi.fn((key, value) => {
        if (value.length > 0) attachmentDrafts.set(key, value);
        else attachmentDrafts.delete(key);
      }),
      persist: vi.fn(),
    };
    const file = new File(['draft attachment'], 'brief.pdf', { type: 'application/pdf' });
    api.uploadFile.mockResolvedValueOnce({
      file_key: 'brief.pdf',
      url: '/uploads/files/brief.pdf',
      name: 'brief.pdf',
      size: file.size,
      mime_type: 'application/pdf',
    });

    await mountComposer({ composerDraftStore });
    await act(async () => {
      Simulate.click(container.querySelector('button.v3-composer-plus'));
    });
    const fileInput = [...container.querySelectorAll('input[type="file"]')]
      .find((input) => !input.accept);
    Object.defineProperty(fileInput, 'files', { configurable: true, value: [file] });
    await act(async () => {
      Simulate.change(fileInput);
      await flushPromises();
    });

    expect(composerDraftStore.setAttachmentDraft).toHaveBeenCalledWith(
      'new-task',
      [expect.objectContaining({ type: 'file', name: 'brief.pdf' })],
    );
    expect(attachmentDrafts.get('new-task')).toEqual([
      expect.objectContaining({
        type: 'file',
        name: 'brief.pdf',
        content: expect.objectContaining({
          payload: expect.objectContaining({ file_key: 'brief.pdf' }),
        }),
      }),
    ]);
    expect(composerDraftStore.persist).toHaveBeenCalled();
  });

  it('clears the draft even when the composer blurs mid-flight', async () => {
    const composerDraftStore = createComposerDraftStore('midflight-blur');
    let resolveSend;
    api.sendMessage.mockReturnValueOnce(new Promise((resolve) => {
      resolveSend = resolve;
    }));

    await mountComposer({ composerDraftStore, initialAgent: agents[0] });
    const ta = container.querySelector('textarea.v3-composer-input');
    await typeInto(ta, '发送后应清除（失焦场景）');
    await pressEnter(ta);
    await vi.waitFor(() => expect(api.sendMessage).toHaveBeenCalledTimes(1));

    // 真实浏览器在提交时禁用输入框并触发失焦；这次失焦 flush 不得记为
    // payload 变更，否则发送后的清除会被跳过、已发送内容残留为草稿。
    await act(async () => {
      Simulate.blur(ta);
      await flushPromises();
    });

    await act(async () => {
      resolveSend({ seq_id: 130 });
      await flushPromises();
    });

    expect(composerDraftStore.getInputDraft('new-task')).toBe('');
    expect(sessionStorage.getItem('catsco_composer_drafts:v1:midflight-blur')).toBeNull();
    expect(localStorage.getItem('catsco_composer_drafts:v1:midflight-blur')).toBeNull();
  });

  it('keeps the draft cleared when a late blur fires after the send', async () => {
    const composerDraftStore = createComposerDraftStore('post-send-blur');
    const onResolveAgentTopic = vi.fn().mockResolvedValue({ topicId: 'p2p_1_21' });

    await mountComposer({
      composerDraftStore,
      initialAgent: agents[0],
      onResolveAgentTopic,
    });
    const ta = container.querySelector('textarea.v3-composer-input');
    await typeInto(ta, '发送后迟到的失焦不得复活草稿');
    await pressEnter(ta);
    await vi.waitFor(() => expect(onResolveAgentTopic).toHaveBeenCalledTimes(1));

    await act(async () => {
      Simulate.blur(ta);
      await flushPromises();
    });

    expect(composerDraftStore.getInputDraft('new-task')).toBe('');
    expect(sessionStorage.getItem('catsco_composer_drafts:v1:post-send-blur')).toBeNull();
    expect(localStorage.getItem('catsco_composer_drafts:v1:post-send-blur')).toBeNull();
  });

  it('persists removing a restored attachment across a fresh context', async () => {
    const composerDraftStore = createComposerDraftStore('attachment-removal-context');
    const attachment = {
      type: 'file',
      name: 'brief.pdf',
      content: {
        type: 'file',
        payload: { file_key: 'brief.pdf', url: '/uploads/brief.pdf', name: 'brief.pdf' },
      },
    };
    composerDraftStore.setAttachmentDraft('new-task', [attachment]);
    composerDraftStore.persist();

    await mountComposer({ composerDraftStore });
    await act(async () => {
      Simulate.click(container.querySelector('[aria-label="移除附件：brief.pdf"]'));
      await flushPromises();
    });

    // A new SkillHub document cannot see the old sessionStorage copy.
    sessionStorage.clear();
    const freshContext = createComposerDraftStore('attachment-removal-context');
    expect(freshContext.getAttachmentDraft('new-task')).toEqual([]);
  });

  it('keeps a phone-uploaded attachment removed when the session poll re-lists it', async () => {
    const attachmentDrafts = new Map();
    const phoneUploadSessions = new Map();
    const composerDraftStore = {
      inputDrafts: new Map(),
      structuredMentionDrafts: new Map(),
      attachmentDrafts,
      phoneUploadSessions,
      persist: vi.fn(),
    };
    const phoneFile = {
      file_key: 'phone-brief.pdf',
      url: '/uploads/files/phone-brief.pdf',
      name: 'phone-brief.pdf',
      size: 2048,
      type: 'file',
      mime_type: 'application/pdf',
    };
    attachmentDrafts.set('new-task', [{
      type: 'file',
      name: 'phone-brief.pdf',
      size: 2048,
      content: {
        type: 'file',
        payload: {
          file_key: 'phone-brief.pdf',
          url: '/uploads/files/phone-brief.pdf',
          name: 'phone-brief.pdf',
        },
      },
    }]);
    phoneUploadSessions.set('new-task', { session_id: 'draft-upload' });
    api.getMobileUploadSession.mockResolvedValue({
      session_id: 'draft-upload',
      files: [phoneFile],
    });

    await mountComposer({ composerDraftStore });
    await vi.waitFor(() => expect(api.getMobileUploadSession).toHaveBeenCalledWith('draft-upload'));

    await act(async () => {
      Simulate.click(container.querySelector('[aria-label="移除附件：phone-brief.pdf"]'));
      await flushPromises();
    });
    expect(attachmentDrafts.get('new-task') ?? []).toEqual([]);

    // The still-alive server session keeps listing the removed file; the next
    // poll tick must not re-attach it.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2000);
      await flushPromises();
    });

    expect(attachmentDrafts.get('new-task') ?? []).toEqual([]);
    expect(container.querySelector('[aria-label="移除附件：phone-brief.pdf"]')).toBeNull();
  });

  it('keeps a removed phone attachment removed after a fresh document restores the draft', async () => {
    const composerDraftStore = createComposerDraftStore('removal-handoff');
    composerDraftStore.setAttachmentDraft('new-task', [{
      type: 'file',
      name: 'phone-brief.pdf',
      size: 2048,
      content: {
        type: 'file',
        payload: {
          file_key: 'phone-brief.pdf',
          url: '/uploads/files/phone-brief.pdf',
          name: 'phone-brief.pdf',
        },
      },
    }]);
    composerDraftStore.setPhoneUploadSession('new-task', { session_id: 'draft-upload' });
    composerDraftStore.persist();
    api.getMobileUploadSession.mockResolvedValue({
      session_id: 'draft-upload',
      files: [{
        file_key: 'phone-brief.pdf',
        url: '/uploads/files/phone-brief.pdf',
        name: 'phone-brief.pdf',
        size: 2048,
        type: 'file',
        mime_type: 'application/pdf',
      }],
    });

    await mountComposer({ composerDraftStore });
    await vi.waitFor(() => expect(api.getMobileUploadSession).toHaveBeenCalledWith('draft-upload'));
    await act(async () => {
      Simulate.click(container.querySelector('[aria-label="移除附件：phone-brief.pdf"]'));
      await flushPromises();
    });
    expect(composerDraftStore.getAttachmentDraft('new-task')).toEqual([]);

    // A fresh SkillHub document rebuilds the store from persisted storage; the
    // ignored file key must survive the handoff so the poll cannot re-attach.
    sessionStorage.clear();
    const freshContext = createComposerDraftStore('removal-handoff');
    expect(freshContext.getPhoneUploadSession('new-task').ignored_file_keys)
      .toEqual(['phone-brief.pdf']);

    await act(async () => {
      root.unmount();
    });
    root = createRoot(container);
    await mountComposer({ composerDraftStore: freshContext });
    await vi.waitFor(() => expect(api.getMobileUploadSession).toHaveBeenCalledWith('draft-upload'));
    await flushPromises();

    expect(freshContext.getAttachmentDraft('new-task')).toEqual([]);
    expect(container.querySelector('[aria-label="移除附件：phone-brief.pdf"]')).toBeNull();
  });

  it('removes a just-uploaded attachment when X is clicked', async () => {
    const attachmentDrafts = new Map();
    const composerDraftStore = {
      inputDrafts: new Map(),
      structuredMentionDrafts: new Map(),
      attachmentDrafts,
      persist: vi.fn(),
    };
    const file = new File(['just uploaded'], 'fresh.pdf', { type: 'application/pdf' });
    api.uploadFile.mockResolvedValueOnce({
      file_key: 'fresh.pdf',
      url: '/uploads/files/fresh.pdf',
      name: 'fresh.pdf',
      size: file.size,
      mime_type: 'application/pdf',
    });

    await mountComposer({ composerDraftStore });
    await act(async () => {
      Simulate.click(container.querySelector('button.v3-composer-plus'));
    });
    const fileInput = [...container.querySelectorAll('input[type="file"]')]
      .find((input) => !input.accept);
    Object.defineProperty(fileInput, 'files', { configurable: true, value: [file] });
    await act(async () => {
      Simulate.change(fileInput);
      await flushPromises();
    });
    expect(attachmentDrafts.get('new-task')).toHaveLength(1);

    await act(async () => {
      Simulate.click(container.querySelector('[aria-label="移除附件：fresh.pdf"]'));
      await flushPromises();
    });

    expect(attachmentDrafts.get('new-task') ?? []).toEqual([]);
    expect(container.querySelector('[aria-label="移除附件：fresh.pdf"]')).toBeNull();
  });

  it('keeps a phone-uploaded attachment removed across later polls in the same document', async () => {
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
      session_id: 'live-upload',
      upload_url: '/mobile-upload/live-upload',
    });
    api.getMobileUploadSession.mockResolvedValue({
      session_id: 'live-upload',
      files: [{
        file_key: 'live-brief.pdf',
        url: '/uploads/files/live-brief.pdf',
        name: 'live-brief.pdf',
        size: 2048,
        type: 'file',
        mime_type: 'application/pdf',
      }],
    });

    await mountComposer({ composerDraftStore });
    await act(async () => {
      Simulate.click(container.querySelector('button.v3-composer-plus'));
      await Promise.resolve();
    });
    await act(async () => {
      Simulate.click(container.querySelector('button[aria-label="手机扫码上传"]'));
      await flushPromises();
    });
    await vi.waitFor(() => expect(attachmentDrafts.get('new-task')).toHaveLength(1));

    // The file was added through the poll path in THIS document, so the
    // phone-upload file-key ref knows it; removal must still stick.
    await act(async () => {
      Simulate.click(container.querySelector('[aria-label="移除附件：live-brief.pdf"]'));
      await flushPromises();
    });
    expect(attachmentDrafts.get('new-task') ?? []).toEqual([]);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(2000);
      await flushPromises();
    });
    expect(attachmentDrafts.get('new-task') ?? []).toEqual([]);
    expect(container.querySelector('[aria-label="移除附件：live-brief.pdf"]')).toBeNull();
  });

  it('keeps a restored attachment removable while another upload is in flight', async () => {
    const attachmentDrafts = new Map();
    const composerDraftStore = {
      inputDrafts: new Map(),
      structuredMentionDrafts: new Map(),
      attachmentDrafts,
      persist: vi.fn(),
    };
    attachmentDrafts.set('new-task', [{
      type: 'file',
      name: 'kept.pdf',
      content: {
        type: 'file',
        payload: { file_key: 'kept.pdf', url: '/uploads/files/kept.pdf', name: 'kept.pdf' },
      },
    }]);
    let resolveUpload;
    api.uploadFile.mockReturnValueOnce(new Promise((resolve) => {
      resolveUpload = resolve;
    }));

    await mountComposer({ composerDraftStore });
    await act(async () => {
      Simulate.click(container.querySelector('button.v3-composer-plus'));
    });
    const fileInput = [...container.querySelectorAll('input[type="file"]')]
      .find((input) => !input.accept);
    const file = new File(['second upload'], 'pending.pdf', { type: 'application/pdf' });
    Object.defineProperty(fileInput, 'files', { configurable: true, value: [file] });
    await act(async () => {
      Simulate.change(fileInput);
      await Promise.resolve();
    });
    // The batch upload is still in flight; the already-uploaded attachment
    // must stay removable.
    expect(container.querySelector('[aria-label="移除附件：kept.pdf"]')).not.toBeNull();
    expect(container.querySelector('[aria-label="移除附件：kept.pdf"]').disabled).toBe(false);

    await act(async () => {
      Simulate.click(container.querySelector('[aria-label="移除附件：kept.pdf"]'));
      await flushPromises();
    });
    expect(attachmentDrafts.get('new-task') ?? []).toEqual([]);

    await act(async () => {
      resolveUpload({
        file_key: 'pending.pdf',
        url: '/uploads/files/pending.pdf',
        name: 'pending.pdf',
        size: file.size,
        mime_type: 'application/pdf',
      });
      await flushPromises();
    });
    // The in-flight upload must not resurrect the removed attachment.
    expect(attachmentDrafts.get('new-task') ?? []).toEqual([]);

    await act(async () => {
      root.unmount();
    });
    root = createRoot(container);
    await mountComposer({ composerDraftStore });
    expect(attachmentDrafts.get('new-task') ?? []).toEqual([]);
  });


  it('clears the new-task draft from both storage copies after a successful send', async () => {
    const composerDraftStore = createComposerDraftStore('sent-draft-context');
    composerDraftStore.setAttachmentDraft('new-task', [{
      type: 'file',
      name: 'brief.pdf',
      content: { type: 'file', payload: { file_key: 'brief.pdf' } },
    }]);
    composerDraftStore.persist();

    const onResolveAgentTopic = vi.fn().mockResolvedValue({ topicId: 'p2p_1_21' });
    await mountComposer({
      composerDraftStore,
      initialAgent: agents[0],
      onResolveAgentTopic,
    });
    await typeInto(container.querySelector('textarea.v3-composer-input'), '发送这条任务');
    await pressEnter(container.querySelector('textarea.v3-composer-input'));

    expect(onResolveAgentTopic).toHaveBeenCalledWith(
      agents[0],
      expect.objectContaining({ text: '发送这条任务' }),
    );
    expect(sessionStorage.getItem('catsco_composer_drafts:v1:sent-draft-context')).toBeNull();
    expect(localStorage.getItem('catsco_composer_drafts:v1:sent-draft-context')).toBeNull();

    const freshContext = createComposerDraftStore('sent-draft-context');
    expect(freshContext.getInputDraft('new-task')).toBe('');
    expect(freshContext.getAttachmentDraft('new-task')).toEqual([]);
  });

  it('clears a sent draft when the Agent roster refreshes during the send', async () => {
    const composerDraftStore = createComposerDraftStore('sent-draft-roster-refresh');
    let resolveSend;
    api.sendMessage.mockReturnValueOnce(new Promise((resolve) => {
      resolveSend = resolve;
    }));

    await mountComposer({
      composerDraftStore,
      initialAgent: agents[0],
      onSelectedAgentChange: (agent) => {
        if (!agent) return;
        writeComposerTaskContextDraft(composerDraftStore, 'new-task', { agent });
        composerDraftStore.persist();
      },
    });
    await typeInto(container.querySelector('textarea.v3-composer-input'), '发送后应清除');
    await pressEnter(container.querySelector('textarea.v3-composer-input'));
    await vi.waitFor(() => expect(api.sendMessage).toHaveBeenCalledTimes(1));

    api.getAgents.mockResolvedValueOnce({ agents: [{ ...agents[0] }] });
    await act(async () => {
      window.dispatchEvent(new Event('cc:data-changed'));
      await flushPromises();
    });
    await act(async () => {
      resolveSend({ seq_id: 113 });
      await flushPromises();
    });

    expect(composerDraftStore.getInputDraft('new-task')).toBe('');
    expect(composerDraftStore.getAttachmentDraft('new-task')).toEqual([]);
    expect(composerDraftStore.getTaskContextDraft('new-task')).toBeNull();
    expect(sessionStorage.getItem('catsco_composer_drafts:v1:sent-draft-roster-refresh')).toBeNull();
    expect(localStorage.getItem('catsco_composer_drafts:v1:sent-draft-roster-refresh')).toBeNull();

    await typeInto(container.querySelector('textarea.v3-composer-input'), '新的未发送草稿');
    expect(composerDraftStore.getTaskContextDraft('new-task')).toEqual(
      expect.objectContaining({ agent: expect.objectContaining({ uid: 21 }) }),
    );
  });

  it('keeps a draft re-typed identically while the send is in flight', async () => {
    const composerDraftStore = createComposerDraftStore('retype-in-flight');
    let resolveSend;
    api.sendMessage.mockReturnValueOnce(new Promise((resolve) => {
      resolveSend = resolve;
    }));

    await mountComposer({
      composerDraftStore,
      initialAgent: agents[0],
      onResolveAgentTopic: vi.fn().mockResolvedValue({ topicId: 'p2p_1_22' }),
    });
    const textarea = container.querySelector('textarea.v3-composer-input');
    await typeInto(textarea, '重打同样的话');
    await pressEnter(textarea);
    await vi.waitFor(() => expect(api.sendMessage).toHaveBeenCalledTimes(1));

    await act(async () => {
      await typeInto(textarea, '重打同样的话');
      await flushPromises();
    });
    await act(async () => {
      resolveSend({ seq_id: 114 });
      await flushPromises();
    });

    expect(composerDraftStore.getInputDraft('new-task')).toBe('重打同样的话');
    expect(localStorage.getItem('catsco_composer_drafts:v1:retype-in-flight')).not.toBeNull();
  });

  it('retains the new-task draft in both storage copies when creation fails', async () => {
    const composerDraftStore = createComposerDraftStore('creation-failure');
    const onResolveAgentTopic = vi.fn().mockRejectedValue(new Error('创建失败'));
    await mountComposer({
      composerDraftStore,
      initialAgent: agents[0],
      onResolveAgentTopic,
    });
    const textarea = container.querySelector('textarea.v3-composer-input');
    await typeInto(textarea, '失败后保留草稿');
    await pressEnter(textarea);
    await act(async () => {
      await flushPromises();
    });

    expect(onResolveAgentTopic).toHaveBeenCalledTimes(1);
    expect(api.sendMessage).not.toHaveBeenCalled();
    expect(composerDraftStore.getInputDraft('new-task')).toBe('失败后保留草稿');
    expect(sessionStorage.getItem('catsco_composer_drafts:v1:creation-failure')).not.toBeNull();
    expect(localStorage.getItem('catsco_composer_drafts:v1:creation-failure')).not.toBeNull();
  });

  it('persists an attachment when navigation unmounts the composer during upload', async () => {
    const attachmentDrafts = new Map();
    const composerDraftStore = {
      inputDrafts: new Map(),
      structuredMentionDrafts: new Map(),
      attachmentDrafts,
      persist: vi.fn(),
    };
    let resolveUpload;
    api.uploadFile.mockReturnValueOnce(new Promise((resolve) => {
      resolveUpload = resolve;
    }));

    const file = new File(['draft attachment'], 'brief.pdf', { type: 'application/pdf' });
    await mountComposer({ composerDraftStore });
    await act(async () => {
      Simulate.click(container.querySelector('button.v3-composer-plus'));
    });
    const fileInput = [...container.querySelectorAll('input[type="file"]')]
      .find((input) => !input.accept);
    Object.defineProperty(fileInput, 'files', { configurable: true, value: [file] });
    await act(async () => {
      Simulate.change(fileInput);
      await Promise.resolve();
    });
    expect(api.uploadFile).toHaveBeenCalledTimes(1);

    await act(async () => {
      root.unmount();
    });
    await act(async () => {
      resolveUpload({
        file_key: 'brief.pdf',
        url: '/uploads/files/brief.pdf',
        name: 'brief.pdf',
        size: file.size,
        mime_type: 'application/pdf',
      });
      await flushPromises();
    });

    expect(attachmentDrafts.get('new-task')).toEqual([
      expect.objectContaining({
        type: 'file',
        name: 'brief.pdf',
        content: expect.objectContaining({
          payload: expect.objectContaining({ file_key: 'brief.pdf' }),
        }),
      }),
    ]);
    expect(composerDraftStore.persist).toHaveBeenCalled();
  });

  it('does not overwrite a newer text draft when an old upload finishes late', async () => {
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

    const file = new File(['draft attachment'], 'brief.pdf', { type: 'application/pdf' });
    await mountComposer({ composerDraftStore });
    await act(async () => {
      Simulate.click(container.querySelector('button.v3-composer-plus'));
    });
    const fileInput = [...container.querySelectorAll('input[type="file"]')]
      .find((input) => !input.accept);
    Object.defineProperty(fileInput, 'files', { configurable: true, value: [file] });
    await act(async () => {
      Simulate.change(fileInput);
      await Promise.resolve();
    });

    await act(async () => {
      root.unmount();
    });
    root = createRoot(container);
    await mountComposer({ composerDraftStore });
    await typeInto(
      container.querySelector('textarea.v3-composer-input'),
      '新的草稿内容',
    );

    await act(async () => {
      resolveUpload({
        file_key: 'brief.pdf',
        url: '/uploads/files/brief.pdf',
        name: 'brief.pdf',
        size: file.size,
        mime_type: 'application/pdf',
      });
      await flushPromises();
    });

    expect(inputDrafts.get('new-task')).toBe('新的草稿内容');
    expect(attachmentDrafts.get('new-task')).toEqual([
      expect.objectContaining({ name: 'brief.pdf' }),
    ]);
  });

  it('drops an old upload when a newer composer sends and clears the draft', async () => {
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

    const file = new File(['draft attachment'], 'brief.pdf', { type: 'application/pdf' });
    await mountComposer({ composerDraftStore });
    await act(async () => {
      Simulate.click(container.querySelector('button.v3-composer-plus'));
    });
    const fileInput = [...container.querySelectorAll('input[type="file"]')]
      .find((input) => !input.accept);
    Object.defineProperty(fileInput, 'files', { configurable: true, value: [file] });
    await act(async () => {
      Simulate.change(fileInput);
      await Promise.resolve();
    });

    await act(async () => {
      root.unmount();
    });
    root = createRoot(container);
    await mountComposer({ composerDraftStore });
    await typeInto(
      container.querySelector('textarea.v3-composer-input'),
      '先发送这条新草稿',
    );
    await pressEnter(container.querySelector('textarea.v3-composer-input'));

    expect(inputDrafts.get('new-task')).toBeUndefined();
    expect(attachmentDrafts.get('new-task')).toBeUndefined();

    await act(async () => {
      resolveUpload({
        file_key: 'brief.pdf',
        url: '/uploads/files/brief.pdf',
        name: 'brief.pdf',
        size: file.size,
        mime_type: 'application/pdf',
      });
      await flushPromises();
    });

    expect(inputDrafts.get('new-task')).toBeUndefined();
    expect(attachmentDrafts.get('new-task')).toBeUndefined();
  });

  it('clears a sent draft when the send resolves after navigation unmounts the composer', async () => {
    const attachmentDrafts = new Map([[
      'new-task',
      [{ type: 'file', name: 'brief.pdf' }],
    ]]);
    const inputDrafts = new Map();
    const composerDraftStore = {
      inputDrafts,
      structuredMentionDrafts: new Map(),
      attachmentDrafts,
      persist: vi.fn(),
    };
    let resolveSend;
    api.sendMessage.mockReturnValueOnce(new Promise((resolve) => {
      resolveSend = resolve;
    }));

    await mountComposer({ composerDraftStore });
    await typeInto(
      container.querySelector('textarea.v3-composer-input'),
      '发送后不应再次恢复',
    );
    await act(async () => {
      Simulate.keyDown(container.querySelector('textarea.v3-composer-input'), {
        key: 'Enter',
        shiftKey: false,
      });
      await Promise.resolve();
    });
    await vi.waitFor(() => expect(api.sendMessage).toHaveBeenCalledTimes(1));

    await act(async () => {
      root.unmount();
    });
    await act(async () => {
      resolveSend({ seq_id: 111 });
      await flushPromises();
    });

    expect(inputDrafts.get('new-task')).toBeUndefined();
    expect(attachmentDrafts.get('new-task')).toBeUndefined();
    expect(composerDraftStore.persist).toHaveBeenCalled();
  });

  it('keeps a newer draft written while the previous send is in flight', async () => {
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

    await mountComposer({ composerDraftStore });
    await typeInto(
      container.querySelector('textarea.v3-composer-input'),
      '第一条任务',
    );
    await pressEnter(container.querySelector('textarea.v3-composer-input'));
    await vi.waitFor(() => expect(api.sendMessage).toHaveBeenCalledTimes(1));

    await act(async () => {
      root.unmount();
    });
    root = createRoot(container);
    await mountComposer({ composerDraftStore });
    await typeInto(
      container.querySelector('textarea.v3-composer-input'),
      '发送期间新写的任务',
    );

    await act(async () => {
      resolveSend({ seq_id: 112 });
      await flushPromises();
    });

    expect(inputDrafts.get('new-task')).toBe('发送期间新写的任务');
  });

  it('persists phone-uploaded attachments when navigation unmounts the composer during polling', async () => {
    const attachmentDrafts = new Map();
    const composerDraftStore = {
      inputDrafts: new Map(),
      structuredMentionDrafts: new Map(),
      attachmentDrafts,
      persist: vi.fn(),
    };
    let resolvePoll;
    api.createMobileUploadSession.mockResolvedValueOnce({
      session_id: 'draft-upload',
      upload_url: '/mobile-upload/draft-upload',
    });
    api.getMobileUploadSession.mockReturnValueOnce(new Promise((resolve) => {
      resolvePoll = resolve;
    }));

    await mountComposer({ composerDraftStore });
    await act(async () => {
      Simulate.click(container.querySelector('button.v3-composer-plus'));
      await Promise.resolve();
    });
    await act(async () => {
      Simulate.click(container.querySelector('button[aria-label="手机扫码上传"]'));
      await Promise.resolve();
    });
    await vi.waitFor(() => expect(api.getMobileUploadSession).toHaveBeenCalledWith('draft-upload'));

    await act(async () => {
      root.unmount();
    });
    await act(async () => {
      resolvePoll({
        session_id: 'draft-upload',
        files: [{
          file_key: 'phone-brief.pdf',
          url: '/uploads/files/phone-brief.pdf',
          name: 'phone-brief.pdf',
          size: 2048,
          type: 'file',
          mime_type: 'application/pdf',
        }],
      });
      await flushPromises();
    });

    expect(attachmentDrafts.get('new-task')).toEqual([
      expect.objectContaining({
        type: 'file',
        name: 'phone-brief.pdf',
        content: expect.objectContaining({
          payload: expect.objectContaining({ file_key: 'phone-brief.pdf' }),
        }),
      }),
    ]);
    expect(composerDraftStore.persist).toHaveBeenCalled();
  });

  it('resumes a persisted phone upload session after the new-task composer remounts', async () => {
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
      session_id: 'new-task-resume',
      upload_url: '/mobile-upload/new-task-resume',
    });
    api.getMobileUploadSession.mockResolvedValue({
      session_id: 'new-task-resume',
      files: [],
    });

    await mountComposer({ composerDraftStore });
    await act(async () => {
      Simulate.click(container.querySelector('button.v3-composer-plus'));
      await Promise.resolve();
    });
    await act(async () => {
      Simulate.click(container.querySelector('button[aria-label="手机扫码上传"]'));
      await flushPromises();
    });
    await vi.waitFor(() => expect(phoneUploadSessions.get('new-task')).toMatchObject({
      session_id: 'new-task-resume',
    }));

    await act(async () => root.unmount());
    root = createRoot(container);
    api.getMobileUploadSession.mockResolvedValueOnce({
      session_id: 'new-task-resume',
      files: [{
        file_key: 'new-task-resume.pdf',
        url: '/uploads/files/new-task-resume.pdf',
        name: 'new-task-resume.pdf',
        size: 19,
        type: 'file',
        mime_type: 'application/pdf',
      }],
    });
    await mountComposer({ composerDraftStore });
    await vi.waitFor(() => expect(attachmentDrafts.get('new-task')).toHaveLength(1));

    expect(attachmentDrafts.get('new-task')[0].content.payload.file_key)
      .toBe('new-task-resume.pdf');
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
