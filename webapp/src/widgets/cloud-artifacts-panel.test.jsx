import React, { act } from 'react';
import { createRoot } from 'react-dom/client';

vi.mock('../api', () => ({
  resolveMediaURL: vi.fn((url) => url),
  api: {
    getCloudArtifacts: vi.fn(),
    getAgentFiles: vi.fn(),
    getTopicFiles: vi.fn(),
    publishCloudArtifact: vi.fn(),
    uploadFile: vi.fn(),
    deleteCloudArtifact: vi.fn(),
    restoreCloudArtifact: vi.fn(),
    getCloudArtifactTags: vi.fn(),
    setCloudArtifactTags: vi.fn(),
    deleteCloudArtifactTag: vi.fn(),
  },
}));

import { api } from '../api';
import { FeedbackProvider } from '../components/feedback-system';
import CloudArtifactsPanel from './cloud-artifacts-panel';

const activeArtifact = {
  id: 'lesson-game',
  title: '课堂小游戏',
  kind: 'html',
  url: 'https://example.test/lesson-game/latest/',
  status: 'active',
  updated_at: '2026-07-22T06:00:00.000Z',
  publish_version: 2,
  source_title: '课堂任务',
  source_topic_id: 'p2p_7_440',
  creator_type: 'user',
  creator_uid: '8',
  creator_name: '成员甲',
  uploader_uid: '8',
  uploader_name: '成员甲',
  can_delete: true,
};

const deletedArtifact = {
  ...activeArtifact,
  status: 'deleted',
  deleted_at: '2026-07-22T07:00:00.000Z',
  can_delete: false,
  can_restore: true,
};

const historicalFile = {
  id: '820:0',
  name: '期末学情报告.pdf',
  url: '/uploads/files/term-report.pdf',
  file_key: 'term-report.pdf',
  mime_type: 'application/pdf',
  size: 728341,
  message_id: 820,
  topic_id: 'p2p_7_440',
  topic_name: '期末材料',
  created_at: '2026-07-29T02:20:00.000Z',
};

const historicalImage = {
  id: '821:0',
  type: 'image',
  name: '课堂照片.jpg',
  url: '/uploads/images/classroom.jpg',
  thumbnail: '/uploads/images/classroom-thumb.jpg',
  mime_type: 'image/jpeg',
  size: 182341,
  message_id: 821,
  topic_id: 'p2p_7_440',
  topic_name: '期末材料',
  created_at: '2026-07-29T03:20:00.000Z',
};

function TestPanel({
  initialTab = 'active',
  topicId = 'p2p_7_440',
  agentUid = 440,
  onPreviewArtifact,
  onPreviewFile,
}) {
  const [tab, setTab] = React.useState(initialTab);
  return (
    <FeedbackProvider>
      <CloudArtifactsPanel
        agentUid={agentUid}
        topicId={topicId}
        tab={tab}
        onTabChange={setTab}
        onClose={vi.fn()}
        onPreviewArtifact={onPreviewArtifact}
        onPreviewFile={onPreviewFile}
      />
    </FeedbackProvider>
  );
}

describe('CloudArtifactsPanel', () => {
  let container;
  let root;
  let onPreviewArtifact;
  let onPreviewFile;

  beforeEach(() => {
    api.getCloudArtifacts.mockReset().mockResolvedValue({
      artifacts: [activeArtifact],
      viewer_relation: 'owner',
      visibility: 'agent_users',
    });
    api.getAgentFiles.mockReset().mockResolvedValue({
      files: [historicalFile],
      has_more: false,
      next_before_id: 0,
    });
    api.getTopicFiles.mockReset().mockResolvedValue({
      files: [historicalFile],
      has_more: false,
      next_before_id: 0,
    });
    api.deleteCloudArtifact.mockReset().mockResolvedValue({ ok: true, artifact: deletedArtifact });
    api.restoreCloudArtifact.mockReset().mockResolvedValue({ ok: true, artifact: activeArtifact });
    api.publishCloudArtifact.mockReset().mockResolvedValue({ ok: true, artifact: activeArtifact });
    api.uploadFile.mockReset().mockResolvedValue({ url: '/uploads/files/result.html' });
    api.getCloudArtifactTags.mockReset().mockResolvedValue({ tags: [] });
    api.setCloudArtifactTags.mockReset().mockImplementation(async (_agentUid, _artifactId, tags) => ({ tags }));
    api.deleteCloudArtifactTag.mockReset().mockResolvedValue({ ok: true });
    onPreviewArtifact = vi.fn();
    onPreviewFile = vi.fn();
    Object.defineProperty(navigator, 'clipboard', {
      configurable: true,
      value: { writeText: vi.fn().mockResolvedValue(undefined) },
    });
    Object.defineProperty(navigator, 'standalone', { configurable: true, value: false });
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(async () => {
    await act(async () => root.unmount());
    container.remove();
  });

  test('shows only files and results, with the result visibility explanation', async () => {
    await renderPanel();

    expect([...container.querySelectorAll('button[role="tab"]')].map((button) => button.textContent))
      .toEqual(['文件', '应用']);
    expect(container.textContent).toContain('共享成果');
    expect(container.querySelector('.cloud-artifacts-role-badge')?.textContent).toBe('所有者');
    expect(container.textContent).toContain('成员可查看 · 你可管理全部成果');
    expect(container.textContent).not.toContain('已添加该 Agent');
    expect(container.querySelector('button[aria-label="筛选成果范围"]')?.textContent).toContain('当前任务');
    expect(container.textContent).toContain('成员甲');
    expect(container.querySelector('.cloud-artifact-kind-icon.application .lucide-cloud')).not.toBeNull();
    expect(container.textContent).not.toContain('Agent 用户可见');
    expect(container.textContent).not.toContain('技能');
  });

  test('does not present an Agent as the uploader of a legacy result', async () => {
    api.getCloudArtifacts.mockResolvedValueOnce({
      artifacts: [{
        ...activeArtifact,
        creator_type: '',
        creator_uid: '',
        creator_name: '',
        uploader_uid: '',
        uploader_name: '',
        agent_name: '豆包',
      }],
      viewer_relation: 'owner',
    });
    await renderPanel();

    expect(container.textContent).toContain('上传用户未知');
    expect(container.textContent).not.toContain('豆包 生成');
    expect(container.textContent).not.toContain('Agent 用户可见');
  });

  test('shows the Agent creator name when no uploading account is present', async () => {
    api.getCloudArtifacts.mockResolvedValueOnce({
      artifacts: [{
        ...activeArtifact,
        creator_type: 'agent',
        creator_name: '自迭代',
        uploader_uid: '',
        uploader_name: '',
        agent_name: '自迭代',
      }],
      viewer_relation: 'owner',
    });
    await renderPanel();

    expect(container.textContent).not.toContain('Cycren');
    expect(container.textContent).toContain('自迭代 生成');
    expect(container.textContent).not.toContain('上传用户未知');
  });

  test('falls back to the Agent name when creator name is missing', async () => {
    api.getCloudArtifacts.mockResolvedValueOnce({
      artifacts: [{
        ...activeArtifact,
        creator_type: 'agent',
        creator_name: '',
        uploader_uid: '',
        uploader_name: '',
        agent_name: '豆包',
      }],
      viewer_relation: 'owner',
    });
    await renderPanel();

    expect(container.textContent).toContain('豆包 生成');
    expect(container.textContent).not.toContain('上传用户未知');
  });

  test('keeps Agent provenance when both Agent names are unavailable', async () => {
    api.getCloudArtifacts.mockResolvedValueOnce({
      artifacts: [{
        ...activeArtifact,
        creator_type: 'agent',
        creator_name: '',
        uploader_name: '旧版成员',
        agent_name: '',
      }],
      viewer_relation: 'owner',
    });
    await renderPanel();

    expect(container.textContent).toContain('Agent 生成');
    expect(container.textContent).not.toContain('旧版成员');
    expect(container.textContent).not.toContain('上传用户未知');
  });

  test('shows Agent provenance ahead of a legacy uploading account name', async () => {
    api.getCloudArtifacts.mockResolvedValueOnce({
      artifacts: [{
        ...activeArtifact,
        creator_type: 'agent',
        creator_name: '自迭代',
        agent_name: '自迭代',
        uploader_name: 'Cycren',
      }],
      viewer_relation: 'owner',
    });
    await renderPanel();

    expect(container.textContent).toContain('自迭代 生成');
    expect(container.textContent).not.toContain('Cycren');
  });

  test('prefers canonical user provenance over a legacy uploading account name', async () => {
    api.getCloudArtifacts.mockResolvedValueOnce({
      artifacts: [{
        ...activeArtifact,
        creator_type: 'user',
        creator_name: '规范成员',
        uploader_name: '旧版成员',
      }],
      viewer_relation: 'owner',
    });
    await renderPanel();

    expect(container.textContent).toContain('规范成员');
    expect(container.textContent).not.toContain('旧版成员');
  });

  test('uses the API unknown label for historical results with no provenance', async () => {
    api.getCloudArtifacts.mockResolvedValueOnce({
      artifacts: [{
        ...activeArtifact,
        creator_type: 'unknown',
        creator_uid: '',
        creator_name: '',
        uploader_uid: '',
        uploader_name: '旧版上传账号',
        agent_name: '',
      }],
      viewer_relation: 'owner',
    });
    await renderPanel();

    expect(container.textContent).toContain('来源未知');
    expect(container.textContent).not.toContain('上传用户未知');
    expect(container.textContent).not.toContain('旧版上传账号');
  });

  test('filters results by the current task and can show all Agent results', async () => {
    const otherTaskArtifact = {
      ...activeArtifact,
      id: 'other-task-result',
      title: '其他任务成果',
      source_topic_id: 'grp_80',
    };
    api.getCloudArtifacts.mockResolvedValueOnce({
      artifacts: [activeArtifact, otherTaskArtifact],
      viewer_relation: 'owner',
    });
    await renderPanel();

    expect(container.textContent).toContain('课堂小游戏');
    expect(container.textContent).not.toContain('其他任务成果');

    const scopeTrigger = container.querySelector('button[aria-label="筛选成果范围"]');
    scopeTrigger.getBoundingClientRect = () => ({
      bottom: 72, height: 32, left: 240, right: 336, top: 40, width: 96,
      x: 240, y: 40, toJSON: () => ({}),
    });
    await act(async () => {
      scopeTrigger.click();
    });
    expect(document.querySelector('.cloud-artifacts-scope-options')?.style.width).toBe('96px');
    await act(async () => {
      document.querySelector('.cloud-artifacts-scope-options button:not(:disabled):last-child').click();
    });

    expect(container.textContent).toContain('课堂小游戏');
    expect(container.textContent).toContain('其他任务成果');
  });

  test('supports keyboard selection and Escape without closing the cloud panel', async () => {
    api.getCloudArtifacts.mockResolvedValueOnce({
      artifacts: [
        activeArtifact,
        { ...activeArtifact, id: 'other-task-result', source_topic_id: 'grp_80' },
      ],
      viewer_relation: 'owner',
    });
    await renderPanel();

    const scopeTrigger = container.querySelector('button[aria-label="筛选成果范围"]');
    scopeTrigger.getBoundingClientRect = () => ({
      bottom: 72, height: 32, left: 240, right: 336, top: 40, width: 96,
      x: 240, y: 40, toJSON: () => ({}),
    });
    await act(async () => {
      scopeTrigger.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true }));
    });
    expect(scopeTrigger.getAttribute('aria-expanded')).toBe('true');

    let listbox = document.querySelector('.cloud-artifacts-scope-options');
    await act(async () => {
      listbox.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }));
    });
    expect(scopeTrigger.textContent).toContain('全部');

    await act(async () => {
      scopeTrigger.click();
    });
    listbox = document.querySelector('.cloud-artifacts-scope-options');
    await act(async () => {
      listbox.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    });
    expect(scopeTrigger.getAttribute('aria-expanded')).toBe('false');
    expect(container.querySelector('.cloud-artifacts-panel')).not.toBeNull();
  });

  test('keeps legacy results without a task source visible in the current-task view', async () => {
    api.getCloudArtifacts.mockResolvedValueOnce({
      artifacts: [{ ...activeArtifact, source_topic_id: undefined }],
      viewer_relation: 'owner',
    });
    await renderPanel();

    expect(container.textContent).toContain('课堂小游戏');
    expect(container.querySelector('button[aria-label="筛选成果范围"]')?.textContent).toContain('全部');
    await act(async () => {
      container.querySelector('button[aria-label="筛选成果范围"]').click();
    });
    expect(document.querySelector('.cloud-artifacts-scope-options button')?.disabled).toBe(true);
  });

  test('loads conversation files without an Agent sender filter and opens the preview', async () => {
    await renderPanel({ initialTab: 'files' });

    expect(api.getTopicFiles).toHaveBeenCalledWith('p2p_7_440', {
      beforeId: 0,
      limit: 40,
    });
    expect(api.getAgentFiles).not.toHaveBeenCalled();
    expect(container.textContent).toContain('期末学情报告.pdf');
    expect(container.textContent).toContain('711.3 KB');

    await act(async () => {
      container.querySelector('button[aria-label="预览文件 期末学情报告.pdf"]').click();
    });
    expect(onPreviewFile).toHaveBeenCalledWith(historicalFile);
  });

  test('loads older conversation files with the stable cursor', async () => {
    api.getTopicFiles
      .mockResolvedValueOnce({
        files: [historicalFile],
        has_more: true,
        next_before_id: 820,
        next_before_created_at: historicalFile.created_at,
      })
      .mockResolvedValueOnce({
        files: [{ ...historicalFile, id: '700:0', message_id: 700, name: '复习清单.docx' }],
        has_more: false,
        next_before_id: 0,
      });
    await renderPanel({ initialTab: 'files' });

    await act(async () => {
      [...container.querySelectorAll('button')]
        .find((button) => button.textContent === '加载更多')
        .click();
      await Promise.resolve();
    });

    expect(api.getTopicFiles).toHaveBeenLastCalledWith('p2p_7_440', {
      beforeId: 820,
      beforeCreatedAt: historicalFile.created_at,
      limit: 40,
    });
    expect(api.getAgentFiles).not.toHaveBeenCalled();
    expect(container.textContent).toContain('复习清单.docx');
  });

  test('shows images with thumbnails and keeps files sorted newest first', async () => {
    const olderFile = {
      ...historicalFile,
      id: '819:0',
      name: '较早报告.pdf',
      created_at: '2026-07-29T01:20:00.000Z',
    };
    api.getTopicFiles.mockResolvedValueOnce({
      files: [olderFile, historicalFile, historicalImage],
      has_more: false,
      next_before_id: 0,
    });
    await renderPanel({ initialTab: 'files' });

    const names = [...container.querySelectorAll('.cloud-file-item h4')].map((node) => node.textContent);
    expect(names).toEqual(['课堂照片.jpg', '期末学情报告.pdf', '较早报告.pdf']);
    expect(container.querySelector('.cloud-file-item img')?.getAttribute('src'))
      .toBe('/uploads/images/classroom-thumb.jpg');
    expect(container.querySelector('.cloud-file-item .cloud-file-meta-type')?.textContent).toBe('图片');

    await act(async () => {
      container.querySelector('button[aria-label="预览图片 课堂照片.jpg"]').click();
    });
    expect(onPreviewFile).toHaveBeenCalledWith(historicalImage);
  });

  test('previews and copies an active result', async () => {
    await renderPanel();

    await act(async () => {
      container.querySelector('button[aria-label="预览 课堂小游戏"]').click();
    });
    expect(onPreviewArtifact).toHaveBeenCalledWith(activeArtifact);

    await act(async () => {
      container.querySelector('button[aria-label="复制 课堂小游戏 链接"]').click();
      await Promise.resolve();
    });
    expect(navigator.clipboard.writeText).toHaveBeenCalledWith(activeArtifact.url);
  });

  test('lets the Agent owner delete and restore a result', async () => {
    api.getCloudArtifacts
      .mockResolvedValueOnce({
        artifacts: [{ ...activeArtifact, can_delete: false }],
        viewer_relation: 'owner',
      })
      .mockResolvedValueOnce({
        artifacts: [{ ...deletedArtifact, can_restore: false }],
        viewer_relation: 'owner',
      });
    await renderPanel();

    await act(async () => {
      container.querySelector('button[aria-label="下架 课堂小游戏"]').click();
    });
    await act(async () => {
      container.querySelector('.cloud-artifact-confirm-actions button.danger').click();
      await Promise.resolve();
    });
    expect(api.deleteCloudArtifact).toHaveBeenCalledWith(440, 'lesson-game');
    expect(document.body.querySelector('.cc-toast')?.textContent).toContain('已下架共享成果');

    await act(async () => {
      container.querySelector('button[aria-label="打开回收站"]').click();
      await Promise.resolve();
    });
    await act(async () => {
      container.querySelector('button[aria-label="恢复 课堂小游戏"]').click();
      await Promise.resolve();
    });
    expect(api.restoreCloudArtifact).toHaveBeenCalledWith(440, 'lesson-game');
    expect([...document.body.querySelectorAll('.cc-toast')].some(
      (toast) => toast.textContent.includes('已恢复共享成果'),
    )).toBe(true);
  });

  test('keeps a friend viewer read-only', async () => {
    api.getCloudArtifacts.mockResolvedValueOnce({
      artifacts: [{ ...activeArtifact, can_delete: false }],
      viewer_relation: 'friend',
      visibility: 'agent_users',
    });
    await renderPanel();

    expect(container.querySelector('button[aria-label="打开回收站"]')).toBeNull();
    expect(container.querySelector('button[aria-label="下架 课堂小游戏"]')).toBeNull();
    expect(container.querySelector('button[aria-label="复制 课堂小游戏 链接"]')).not.toBeNull();
  });

  test('lets a member publish immediately when the artifact service advertises the capability', async () => {
    const publishedArtifact = {
      ...activeArtifact,
      id: 'member-result',
      title: '课堂网页',
      uploader_name: '成员甲',
      uploaded_by_me: true,
      can_delete: true,
    };
    api.getCloudArtifacts.mockResolvedValueOnce({
      artifacts: [],
      viewer_relation: 'friend',
      visibility: 'agent_users',
      can_publish: true,
      publish_mode: 'immediate',
    });
    api.publishCloudArtifact.mockResolvedValueOnce({ ok: true, artifact: publishedArtifact });
    await renderPanel();

    const file = new File(['<h1>result</h1>'], '课堂网页.html', { type: 'text/html' });
    const input = container.querySelector('input[type="file"]');
    Object.defineProperty(input, 'files', { configurable: true, value: [file] });
    await act(async () => {
      input.dispatchEvent(new Event('change', { bubbles: true }));
      await Promise.resolve();
    });

    expect(api.uploadFile).toHaveBeenCalledWith(file, 'file');
    expect(api.publishCloudArtifact).toHaveBeenCalledWith(440, {
      title: '课堂网页',
      kind: 'html',
      url: 'http://localhost:3000/uploads/files/result.html',
      source_topic_id: 'p2p_7_440',
    });
    expect(container.textContent).toContain('课堂网页');
    expect(container.textContent).toContain('成员甲');
    expect(container.textContent).not.toContain('我上传');
    expect(container.querySelector('.cloud-artifacts-role-badge')?.textContent).toBe('成员');
    expect(container.textContent).toContain('你可以查看和上传成果');
    expect(container.querySelector('button[aria-label="下架 课堂网页"]')).not.toBeNull();
    expect(container.textContent).not.toContain('待审核');
    expect(document.body.querySelector('.cc-toast')?.textContent).toContain('已共享内容到云端');
  });

  test('keeps the upload control hidden for a legacy artifact service', async () => {
    api.getCloudArtifacts.mockResolvedValueOnce({
      artifacts: [activeArtifact],
      viewer_relation: 'friend',
      visibility: 'agent_users',
    });
    await renderPanel();

    expect(container.querySelector('button[aria-label="上传成果"]')).toBeNull();
    expect(container.querySelector('.cloud-artifacts-role-badge')?.textContent).toBe('成员');
    expect(container.textContent).toContain('你可以查看成果');
  });

  test('shows only the file tab when the current conversation has no Agent', async () => {
    await renderPanel({ initialTab: 'files', agentUid: 0 });

    expect([...container.querySelectorAll('button[role="tab"]')].map((button) => button.textContent))
      .toEqual(['文件']);
    expect(api.getTopicFiles).toHaveBeenCalledWith('p2p_7_440', {
      beforeId: 0,
      limit: 40,
    });
    expect(api.getAgentFiles).not.toHaveBeenCalled();
    expect(api.getCloudArtifacts).not.toHaveBeenCalled();
    expect(container.querySelector('button[aria-label="筛选成果范围"]')).toBeNull();
  });

  test('opens all Agent results when no conversation exists', async () => {
    const otherTaskArtifact = {
      ...activeArtifact,
      id: 'other-task-result',
      title: '其他任务成果',
      source_topic_id: 'grp_80',
    };
    api.getCloudArtifacts.mockResolvedValueOnce({
      artifacts: [activeArtifact, otherTaskArtifact],
      viewer_relation: 'owner',
    });

    await renderPanel({ topicId: '', initialTab: 'active' });

    expect(container.textContent).toContain('课堂小游戏');
    expect(container.textContent).toContain('其他任务成果');
    expect(container.querySelector('button[aria-label="筛选成果范围"]')?.textContent)
      .toContain('全部');
    expect(container.querySelector('button[role="tab"][disabled]')?.textContent).toBe('文件');
    expect(api.getAgentFiles).not.toHaveBeenCalled();
  });

  test('shows a useful empty state and retry action', async () => {
    api.getCloudArtifacts.mockResolvedValueOnce({ artifacts: [], viewer_relation: 'owner' });
    await renderPanel();
    expect(container.textContent).toContain('当前任务还没有共享成果');

    api.getCloudArtifacts.mockRejectedValueOnce(new Error('成果服务暂时不可用'));
    await act(async () => {
      container.querySelector('button[aria-label="刷新当前栏目"]').click();
      await Promise.resolve();
    });
    expect(container.querySelector('[role="alert"]')?.textContent).toContain('成果服务暂时不可用');
    expect([...container.querySelectorAll('button')].some((button) => button.textContent === '重试')).toBe(true);
  });

  async function renderPanel({
    initialTab = 'active',
    topicId = 'p2p_7_440',
    agentUid = 440,
  } = {}) {
    await act(async () => {
      root.render(
        <TestPanel
          initialTab={initialTab}
          topicId={topicId}
          agentUid={agentUid}
          onPreviewArtifact={onPreviewArtifact}
          onPreviewFile={onPreviewFile}
        />,
      );
      await Promise.resolve();
    });
  }

  async function flush() {
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });
  }

  test('filters shared artifacts by tag chips with AND semantics', async () => {
    api.getCloudArtifacts.mockResolvedValue({
      artifacts: [
        { ...activeArtifact, tags: ['游戏', '演示'] },
        { ...activeArtifact, id: 'lesson-poster', title: '课堂海报', tags: ['游戏'] },
      ],
      viewer_relation: 'owner',
    });
    api.getCloudArtifactTags.mockResolvedValue({
      tags: [{ tag: '游戏', count: 2 }, { tag: '演示', count: 1 }],
    });
    await renderPanel();

    const chips = [...container.querySelectorAll('.cloud-artifact-tag-chip')];
    expect(chips.map((chip) => chip.textContent)).toEqual(['游戏2', '演示1']);
    expect(container.textContent).toContain('课堂海报');

    await act(async () => {
      chips.find((chip) => chip.textContent === '游戏2').click();
    });
    await flush();
    expect([...container.querySelectorAll('.cloud-artifact-item')]).toHaveLength(2);

    await act(async () => {
      [...container.querySelectorAll('.cloud-artifact-tag-chip')]
        .find((chip) => chip.textContent === '演示1')
        .click();
    });
    await flush();
    const visible = [...container.querySelectorAll('.cloud-artifact-item')];
    expect(visible).toHaveLength(1);
    expect(visible[0].textContent).toContain('课堂小游戏');
    expect(container.textContent).toContain('清空筛选');

    await act(async () => {
      container.querySelector('.cloud-artifact-tag-clear').click();
    });
    await flush();
    expect([...container.querySelectorAll('.cloud-artifact-item')]).toHaveLength(2);
  });

  test('owner adds and removes tags from the inline editor', async () => {
    api.getCloudArtifacts.mockResolvedValue({
      artifacts: [{ ...activeArtifact, tags: ['游戏'] }],
      viewer_relation: 'owner',
    });
    api.getCloudArtifactTags.mockResolvedValue({ tags: [{ tag: '游戏', count: 1 }] });
    await renderPanel();

    await act(async () => {
      container.querySelector('button[aria-label="编辑 课堂小游戏 的标签"]').click();
    });
    await flush();

    const input = container.querySelector('.cloud-artifact-tag-editor input');
    expect(input).not.toBeNull();
    await act(async () => {
      const valueSetter = Object.getOwnPropertyDescriptor(
        window.HTMLInputElement.prototype,
        'value',
      ).set;
      valueSetter.call(input, '演示');
      input.dispatchEvent(new Event('input', { bubbles: true }));
    });
    await act(async () => {
      [...container.querySelectorAll('.cloud-artifact-tag-editor > button')]
        .find((button) => button.textContent === '添加')
        .click();
    });
    await flush();

    expect(api.setCloudArtifactTags).toHaveBeenCalledWith(440, 'lesson-game', ['游戏', '演示']);
    const removeButtons = [...container.querySelectorAll('button[aria-label="移除标签 游戏"]')];
    expect(removeButtons).toHaveLength(1);

    await act(async () => {
      removeButtons[0].click();
    });
    await flush();
    expect(api.setCloudArtifactTags).toHaveBeenLastCalledWith(440, 'lesson-game', ['演示']);
  });

  test('friend can edit tags', async () => {
    api.getCloudArtifacts.mockResolvedValue({
      artifacts: [{ ...activeArtifact, tags: ['游戏'] }],
      viewer_relation: 'friend',
    });
    api.getCloudArtifactTags.mockResolvedValue({ tags: [{ tag: '游戏', count: 1 }] });
    await renderPanel();

    const editButton = container.querySelector('button[aria-label="编辑 课堂小游戏 的标签"]');
    expect(editButton).not.toBeNull();

    await act(async () => {
      editButton.click();
    });
    await flush();

    const input = container.querySelector('.cloud-artifact-tag-editor input');
    expect(input).not.toBeNull();
    await act(async () => {
      const valueSetter = Object.getOwnPropertyDescriptor(
        window.HTMLInputElement.prototype,
        'value',
      ).set;
      valueSetter.call(input, '演示');
      input.dispatchEvent(new Event('input', { bubbles: true }));
    });
    await act(async () => {
      [...container.querySelectorAll('.cloud-artifact-tag-editor > button')]
        .find((button) => button.textContent === '添加')
        .click();
    });
    await flush();

    expect(api.setCloudArtifactTags).toHaveBeenCalledWith(440, 'lesson-game', ['游戏', '演示']);
    const removeButtons = [...container.querySelectorAll('button[aria-label="移除标签 游戏"]')];
    expect(removeButtons).toHaveLength(1);

    await act(async () => {
      removeButtons[0].click();
    });
    await flush();
    expect(api.setCloudArtifactTags).toHaveBeenLastCalledWith(440, 'lesson-game', ['演示']);
  });
});
