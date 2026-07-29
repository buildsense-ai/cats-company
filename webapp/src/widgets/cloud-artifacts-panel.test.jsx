import React, { act } from 'react';
import { createRoot } from 'react-dom/client';

vi.mock('../api', () => ({
  api: {
    getCloudArtifacts: vi.fn(),
    getAgentFiles: vi.fn(),
    deleteCloudArtifact: vi.fn(),
    restoreCloudArtifact: vi.fn(),
  },
}));

import { api } from '../api';
import CloudArtifactsPanel from './cloud-artifacts-panel';

const activeArtifact = {
  id: 'lesson-game',
  title: '课堂小游戏',
  kind: 'html',
  url: 'https://example.test/lesson-game/latest/',
  status: 'active',
  created_at: '2026-07-22T05:00:00.000Z',
  updated_at: '2026-07-22T06:00:00.000Z',
  publish_version: 2,
  agent_name: '豆包',
  source_title: '课堂任务',
  can_delete: true,
  can_restore: false,
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
  topic_id: 'grp_term_review',
  topic_name: '期末材料',
  created_at: '2026-07-29T02:20:00.000Z',
};

describe('CloudArtifactsPanel', () => {
  let container;
  let root;
  let onPreviewArtifact;
  let onPreviewFile;

  beforeEach(() => {
    api.getCloudArtifacts.mockReset().mockResolvedValue({ artifacts: [activeArtifact] });
    api.getAgentFiles.mockReset().mockResolvedValue({
      files: [historicalFile],
      has_more: false,
      next_before_id: 0,
    });
    api.deleteCloudArtifact.mockReset().mockResolvedValue({ ok: true, artifact: deletedArtifact });
    api.restoreCloudArtifact.mockReset().mockResolvedValue({ ok: true, artifact: activeArtifact });
    onPreviewArtifact = vi.fn();
    onPreviewFile = vi.fn();
    Object.defineProperty(navigator, 'clipboard', {
      configurable: true,
      value: { writeText: vi.fn().mockResolvedValue(undefined) },
    });
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(async () => {
    await act(async () => root.unmount());
    container.remove();
  });

  test('loads active metadata and previews the selected artifact in the parent workspace', async () => {
    await renderPanel();

    expect(api.getCloudArtifacts).toHaveBeenCalledWith(440, 'active');
    expect(container.textContent).toContain('课堂小游戏');
    expect(container.textContent).toContain('发布 v2');
    expect(container.textContent).toContain('豆包');
    expect(container.textContent).toContain('课堂任务');
    const artifactButton = container.querySelector('.cloud-artifact-main');
    expect(artifactButton?.tagName).toBe('BUTTON');

    await act(async () => {
      artifactButton.click();
    });
    expect(onPreviewArtifact).toHaveBeenCalledWith(activeArtifact);

    await act(async () => {
      container.querySelector('button[aria-label="复制 课堂小游戏 链接"]').click();
      await Promise.resolve();
    });
    expect(navigator.clipboard.writeText).toHaveBeenCalledWith('https://example.test/lesson-game/latest/');
  });

  test('cancels deletion without making a request', async () => {
    await renderPanel();

    await act(async () => {
      container.querySelector('button[aria-label="删除 课堂小游戏"]').click();
    });
    expect(container.textContent).toContain('链接会立即失效');

    await act(async () => {
      container.querySelector('.cloud-artifact-confirm-actions button:not(.danger)').click();
    });
    expect(api.deleteCloudArtifact).not.toHaveBeenCalled();
    expect(container.textContent).toContain('课堂小游戏');
  });

  test('deletes one exact artifact after confirmation', async () => {
    await renderPanel();

    await act(async () => {
      container.querySelector('button[aria-label="删除 课堂小游戏"]').click();
    });
    await act(async () => {
      container.querySelector('.cloud-artifact-confirm-actions button.danger').click();
      await Promise.resolve();
    });

    expect(api.deleteCloudArtifact).toHaveBeenCalledTimes(1);
    expect(api.deleteCloudArtifact).toHaveBeenCalledWith(440, 'lesson-game');
    expect(container.textContent).not.toContain('课堂小游戏');
  });

  test('deleting one similar ID leaves sibling entries visible', async () => {
    const siblings = [
      { ...activeArtifact, id: 'witch-poison-game', title: '版本一' },
      { ...activeArtifact, id: 'witch-poison-game-2', title: '版本二' },
      { ...activeArtifact, id: 'witch-poison-game-3', title: '版本三' },
    ];
    api.getCloudArtifacts.mockResolvedValueOnce({ artifacts: siblings });
    await renderPanel();

    await act(async () => {
      container.querySelector('button[aria-label="删除 版本二"]').click();
    });
    await act(async () => {
      container.querySelector('.cloud-artifact-confirm-actions button.danger').click();
      await Promise.resolve();
    });

    expect(api.deleteCloudArtifact).toHaveBeenCalledWith(440, 'witch-poison-game-2');
    expect(container.textContent).toContain('版本一');
    expect(container.textContent).not.toContain('版本二');
    expect(container.textContent).toContain('版本三');
  });

  test('keeps an artifact visible when deletion fails', async () => {
    api.deleteCloudArtifact.mockRejectedValueOnce(new Error('删除暂时失败'));
    await renderPanel();

    await act(async () => {
      container.querySelector('button[aria-label="删除 课堂小游戏"]').click();
    });
    await act(async () => {
      container.querySelector('.cloud-artifact-confirm-actions button.danger').click();
      await Promise.resolve();
    });

    expect(container.textContent).toContain('课堂小游戏');
    expect(container.textContent).toContain('删除暂时失败');
  });

  test('loads the recycle bin and restores an exact artifact', async () => {
    api.getCloudArtifacts
      .mockResolvedValueOnce({ artifacts: [activeArtifact] })
      .mockResolvedValueOnce({ artifacts: [deletedArtifact] });
    await renderPanel();

    await act(async () => {
      [...container.querySelectorAll('button[role="tab"]')]
        .find((button) => button.textContent === '回收站')
        .click();
      await Promise.resolve();
    });

    expect(api.getCloudArtifacts).toHaveBeenLastCalledWith(440, 'deleted');
    expect(container.querySelector('.cloud-artifact-main')?.tagName).toBe('DIV');
    expect(container.querySelector('button[aria-label="复制 课堂小游戏 链接"]')).toBeNull();

    await act(async () => {
      container.querySelector('button[aria-label="恢复 课堂小游戏"]').click();
      await Promise.resolve();
    });
    expect(api.restoreCloudArtifact).toHaveBeenCalledWith(440, 'lesson-game');
    expect(container.textContent).not.toContain('课堂小游戏');
  });

  test('indexes historical agent files and opens one in the parent preview', async () => {
    await renderPanel();

    await act(async () => {
      [...container.querySelectorAll('button[role="tab"]')]
        .find((button) => button.textContent === '文件')
        .click();
      await Promise.resolve();
    });

    expect(api.getAgentFiles).toHaveBeenCalledWith(440, { beforeId: 0, limit: 40 });
    expect(container.textContent).toContain('期末学情报告.pdf');
    expect(container.textContent).toContain('PDF');
    expect(container.textContent).toContain('711.3 KB');
    expect(container.textContent).toContain('期末材料');

    await act(async () => {
      container.querySelector('button[aria-label="预览文件 期末学情报告.pdf"]').click();
    });
    expect(onPreviewFile).toHaveBeenCalledWith(historicalFile);
  });

  test('loads older historical files with a stable message cursor', async () => {
    const olderFile = {
      ...historicalFile,
      id: '700:0',
      name: '复习清单.docx',
      url: '/uploads/files/review-list.docx',
      message_id: 700,
    };
    api.getAgentFiles
      .mockResolvedValueOnce({
        files: [historicalFile],
        has_more: true,
        next_before_id: 820,
      })
      .mockResolvedValueOnce({
        files: [olderFile],
        has_more: false,
        next_before_id: 0,
      });
    await renderPanel();

    await act(async () => {
      [...container.querySelectorAll('button[role="tab"]')]
        .find((button) => button.textContent === '文件')
        .click();
      await Promise.resolve();
    });
    await act(async () => {
      [...container.querySelectorAll('button')]
        .find((button) => button.textContent === '加载更多')
        .click();
      await Promise.resolve();
    });

    expect(api.getAgentFiles).toHaveBeenLastCalledWith(440, { beforeId: 820, limit: 40 });
    expect(container.textContent).toContain('期末学情报告.pdf');
    expect(container.textContent).toContain('复习清单.docx');
  });

  test('refreshes the current tab and shows an empty state', async () => {
    api.getCloudArtifacts.mockResolvedValue({ artifacts: [] });
    await renderPanel();
    expect(container.textContent).toContain('还没有已部署的网页');

    await act(async () => {
      container.querySelector('button[aria-label="刷新产物"]').click();
      await Promise.resolve();
    });
    expect(api.getCloudArtifacts).toHaveBeenCalledTimes(2);
  });

  async function renderPanel() {
    await act(async () => {
      root.render(
        <CloudArtifactsPanel
          agentUid={440}
          onClose={vi.fn()}
          onPreviewArtifact={onPreviewArtifact}
          onPreviewFile={onPreviewFile}
        />,
      );
      await Promise.resolve();
    });
  }
});
