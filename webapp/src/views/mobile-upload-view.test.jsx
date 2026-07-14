import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';

vi.mock('../api', () => ({
  api: {
    getMobileUploadSession: vi.fn(),
    uploadMobileSessionFile: vi.fn(),
  },
}));

import MobileUploadView from './mobile-upload-view';
import { api } from '../api';

describe('MobileUploadView', () => {
  let container;
  let root;

  beforeEach(() => {
    global.IS_REACT_ACT_ENVIRONMENT = true;
    api.getMobileUploadSession.mockResolvedValue({ files: [] });
    api.uploadMobileSessionFile.mockImplementation((sessionId, file) => Promise.resolve({
      file_key: file.name,
      name: file.name,
      size: file.size,
      type: file.type?.startsWith('image/') ? 'image' : 'file',
    }));
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

  it('shows cumulative upload count across multiple file selections', async () => {
    await act(async () => {
      root.render(<MobileUploadView sessionId="session-1" />);
      await Promise.resolve();
    });

    const input = container.querySelector('input[type="file"]');
    const firstBatch = [
      new File(['a'], 'paper-01.jpg', { type: 'image/jpeg' }),
      new File(['b'], 'paper-02.jpg', { type: 'image/jpeg' }),
    ];
    const secondBatch = [
      new File(['c'], 'paper-03.jpg', { type: 'image/jpeg' }),
    ];

    await act(async () => {
      Simulate.change(input, { target: { files: firstBatch, value: 'C:\\fakepath\\paper-01.jpg' } });
      await Promise.resolve();
      await Promise.resolve();
    });

    await act(async () => {
      Simulate.change(input, { target: { files: secondBatch, value: 'C:\\fakepath\\paper-03.jpg' } });
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(api.uploadMobileSessionFile).toHaveBeenCalledTimes(3);
    expect(container.querySelector('.mobile-upload-counter').getAttribute('aria-label')).toBe('已上传 3 个文件');
    expect(container.textContent).toContain('继续选择图片或文件');
  });
});
