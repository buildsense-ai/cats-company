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
    deleteCloudArtifactTagEverywhere: vi.fn(),
    renameCloudArtifactTag: vi.fn(),
  },
}));

vi.mock('./chat-message', () => ({
  createCloudArtifactPreviewFile: vi.fn((artifact) => ({
    name: artifact.title,
    url: artifact.url,
    mime_type: 'text/html',
    artifact_id: artifact.id,
    artifact_agent_uid: Number(artifact.agent_uid || 0),
    publish_version: artifact.publish_version || null,
  })),
  FilePreviewPanel: ({ file, onBack, onClose, onOpenRemoteArtifactFullscreen }) => (
    <div className="stub-file-preview">
      <span className="stub-file-preview-name">{file?.name}</span>
      <button type="button" className="stub-back" onClick={() => onBack?.()}>返回</button>
      <button type="button" className="stub-close" onClick={() => onClose?.()}>关闭</button>
      <button
        type="button"
        className="stub-open-fullscreen"
        onClick={() => onOpenRemoteArtifactFullscreen?.(file)}
      >
        在新标签页打开
      </button>
    </div>
  ),
}));

import { api } from '../api';
import { FeedbackProvider } from '../components/feedback-system';
import StandaloneCloudArtifactsPanel from './standalone-cloud-artifacts-panel';

const activeArtifact = {
  id: 'lesson-game',
  title: '课堂小游戏',
  kind: 'html',
  url: 'https://example.test/lesson-game/latest/',
  status: 'active',
  updated_at: '2026-07-22T06:00:00.000Z',
  publish_version: 2,
  creator_type: 'user',
  creator_uid: '8',
  creator_name: '成员甲',
  uploader_uid: '8',
  uploader_name: '成员甲',
  can_delete: true,
};

function TestPanel({ onOpenArtifact, onClose = vi.fn() }) {
  const [tab, setTab] = React.useState('active');
  return (
    <FeedbackProvider>
      <StandaloneCloudArtifactsPanel
        agentUid={440}
        topicId=""
        initialTab="active"
        tab={tab}
        onTabChange={setTab}
        onClose={onClose}
        onOpenArtifact={onOpenArtifact}
      />
    </FeedbackProvider>
  );
}

describe('StandaloneCloudArtifactsPanel', () => {
  let container;
  let root;
  let onOpenArtifact;
  let onClose;

  beforeEach(() => {
    api.getCloudArtifacts.mockReset().mockResolvedValue({
      artifacts: [activeArtifact],
      viewer_relation: 'owner',
      visibility: 'agent_users',
    });
    api.getAgentFiles.mockReset().mockResolvedValue({ files: [], has_more: false, next_before_id: 0 });
    api.getTopicFiles.mockReset().mockResolvedValue({ files: [], has_more: false, next_before_id: 0 });
    api.getCloudArtifactTags.mockReset().mockResolvedValue({ tags: [] });
    onOpenArtifact = vi.fn();
    onClose = vi.fn();
    window.open = vi.fn();
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(async () => {
    await act(async () => root.unmount());
    container.remove();
  });

  async function renderPanel() {
    await act(async () => {
      root.render(<TestPanel onOpenArtifact={onOpenArtifact} onClose={onClose} />);
      await Promise.resolve();
    });
  }

  async function flush() {
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });
  }

  test('previews a shared artifact inside the panel instead of a new tab', async () => {
    await renderPanel();
    await flush();

    const row = container.querySelector('.cloud-artifact-main');
    expect(row).not.toBeNull();

    await act(async () => { row.click(); });

    expect(container.querySelector('.stub-file-preview')).not.toBeNull();
    expect(container.querySelector('.stub-file-preview-name')?.textContent).toBe(activeArtifact.title);
    expect(container.querySelector('.cloud-artifact-main')).toBeNull();
    expect(window.open).not.toHaveBeenCalled();
  });

  test('keeps the new-tab action on the preview panel only', async () => {
    await renderPanel();
    await flush();
    await act(async () => { container.querySelector('.cloud-artifact-main').click(); });

    await act(async () => { container.querySelector('.stub-open-fullscreen').click(); });

    expect(onOpenArtifact).toHaveBeenCalledTimes(1);
    expect(onOpenArtifact).toHaveBeenCalledWith(expect.objectContaining({ url: activeArtifact.url }));
  });

  test('returns to the shared artifact list from the preview', async () => {
    await renderPanel();
    await flush();
    await act(async () => { container.querySelector('.cloud-artifact-main').click(); });

    await act(async () => { container.querySelector('.stub-back').click(); });

    expect(container.querySelector('.stub-file-preview')).toBeNull();
    expect(container.querySelector('.cloud-artifact-main')).not.toBeNull();
    expect(onClose).not.toHaveBeenCalled();
  });

  test('closes the whole panel from the preview', async () => {
    await renderPanel();
    await flush();
    await act(async () => { container.querySelector('.cloud-artifact-main').click(); });

    await act(async () => { container.querySelector('.stub-close').click(); });

    expect(onClose).toHaveBeenCalledTimes(1);
  });
});
