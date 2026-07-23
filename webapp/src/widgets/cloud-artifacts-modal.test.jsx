import React, { act } from 'react';
import { createRoot } from 'react-dom/client';

vi.mock('../api', () => ({
  api: {
    getCloudArtifacts: vi.fn(),
    deleteCloudArtifact: vi.fn(),
    restoreCloudArtifact: vi.fn(),
  },
}));

import { api } from '../api';
import CloudArtifactsModal from './cloud-artifacts-modal';

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

describe('CloudArtifactsModal', () => {
  let container;
  let root;

  beforeEach(() => {
    api.getCloudArtifacts.mockReset().mockResolvedValue({ artifacts: [activeArtifact] });
    api.deleteCloudArtifact.mockReset().mockResolvedValue({ ok: true, artifact: deletedArtifact });
    api.restoreCloudArtifact.mockReset().mockResolvedValue({ ok: true, artifact: activeArtifact });
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

  test('loads active metadata and exposes open and copy actions', async () => {
    await renderModal();

    expect(api.getCloudArtifacts).toHaveBeenCalledWith(440, 'active');
    expect(container.textContent).toContain('课堂小游戏');
    expect(container.textContent).toContain('发布 v2');
    expect(container.textContent).toContain('豆包');
    expect(container.textContent).toContain('课堂任务');
    const artifactLink = container.querySelector('.cloud-artifact-main');
    expect(artifactLink?.href).toBe('https://example.test/lesson-game/latest/');
    expect(artifactLink?.target).toBe('_blank');

    await act(async () => {
      container.querySelector('button[aria-label="复制 课堂小游戏 链接"]').click();
      await Promise.resolve();
    });
    expect(navigator.clipboard.writeText).toHaveBeenCalledWith('https://example.test/lesson-game/latest/');
  });

  test('cancels deletion without making a request', async () => {
    await renderModal();

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
    await renderModal();

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
    await renderModal();

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
    await renderModal();

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
    await renderModal();

    await act(async () => {
      container.querySelector('button[role="tab"][aria-selected="false"]').click();
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

  test('refreshes the current tab and shows an empty state', async () => {
    api.getCloudArtifacts.mockResolvedValue({ artifacts: [] });
    await renderModal();
    expect(container.textContent).toContain('还没有已部署的云端产物');

    await act(async () => {
      container.querySelector('button[aria-label="刷新云端产物"]').click();
      await Promise.resolve();
    });
    expect(api.getCloudArtifacts).toHaveBeenCalledTimes(2);
  });

  async function renderModal() {
    await act(async () => {
      root.render(<CloudArtifactsModal agentUid={440} onClose={vi.fn()} />);
      await Promise.resolve();
    });
  }
});
