import React, { act } from 'react';
import { createRoot } from 'react-dom/client';

vi.mock('../api', () => ({
  api: {
    getCloudArtifacts: vi.fn(),
  },
}));

import { api } from '../api';
import CloudArtifactsModal from './cloud-artifacts-modal';

describe('CloudArtifactsModal', () => {
  let container;
  let root;

  beforeEach(() => {
    api.getCloudArtifacts.mockReset().mockResolvedValue({
      artifacts: [{
        id: 'lesson-game',
        title: '课堂小游戏',
        kind: 'html',
        url: 'https://example.test/lesson-game/latest/',
        updated_at: '2026-07-22T06:00:00.000Z',
      }],
    });
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

  test('loads and exposes open and copy actions', async () => {
    await act(async () => {
      root.render(<CloudArtifactsModal onClose={vi.fn()} />);
      await Promise.resolve();
    });

    expect(container.textContent).toContain('课堂小游戏');
    const artifactLink = container.querySelector('.cloud-artifact-main');
    expect(artifactLink?.href).toBe('https://example.test/lesson-game/latest/');
    expect(artifactLink?.target).toBe('_blank');

    await act(async () => {
      container.querySelector('button[aria-label="复制 课堂小游戏 链接"]').click();
      await Promise.resolve();
    });
    expect(navigator.clipboard.writeText).toHaveBeenCalledWith('https://example.test/lesson-game/latest/');
  });

  test('refreshes the index on demand', async () => {
    await act(async () => {
      root.render(<CloudArtifactsModal onClose={vi.fn()} />);
      await Promise.resolve();
    });
    await act(async () => {
      container.querySelector('button[aria-label="刷新云端产物"]').click();
      await Promise.resolve();
    });
    expect(api.getCloudArtifacts).toHaveBeenCalledTimes(2);
  });

  test('shows the empty state', async () => {
    api.getCloudArtifacts.mockResolvedValue({ artifacts: [] });

    await act(async () => {
      root.render(<CloudArtifactsModal onClose={vi.fn()} />);
      await Promise.resolve();
    });

    expect(container.textContent).toContain('还没有已部署的云端产物');
  });

  test('shows a read error and retries', async () => {
    api.getCloudArtifacts
      .mockRejectedValueOnce(new Error('索引暂时不可用'))
      .mockResolvedValueOnce({ artifacts: [] });

    await act(async () => {
      root.render(<CloudArtifactsModal onClose={vi.fn()} />);
      await Promise.resolve();
    });
    expect(container.textContent).toContain('索引暂时不可用');

    await act(async () => {
      container.querySelector('.cloud-artifacts-status.error button').click();
      await Promise.resolve();
    });
    expect(api.getCloudArtifacts).toHaveBeenCalledTimes(2);
    expect(container.textContent).toContain('还没有已部署的云端产物');
  });
});
