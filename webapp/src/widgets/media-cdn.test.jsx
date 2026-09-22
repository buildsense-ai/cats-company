import { describe, expect, test, vi } from 'vitest';

// 模拟生产构建下的媒体域解析：/uploads 相对路径 → CDN 域名（i.catsco.cc）。
// 消费方（预览信任判定 / 下载参数）必须正确识别该 origin。
vi.mock('../api', async (importOriginal) => {
  const actual = await importOriginal();
  return {
    ...actual,
    resolveMediaURL: (url) => (
      url && String(url).startsWith('/uploads/')
        ? `https://i.catsco.cc${url}`
        : actual.resolveMediaURL(url)
    ),
    getApiBaseURL: () => window.location.origin,
  };
});

import { createCloudArtifactPreviewFile, downloadableMediaURL, previewFileDescriptor } from './chat-message';

describe('media acceleration consumption', () => {
  test('file preview stays trusted when uploads resolve to the media domain', () => {
    const descriptor = previewFileDescriptor({
      url: '/uploads/files/20260101_abcdefabcdefabcdefabcdefabcdef12.pdf',
      name: 'doc.pdf',
      size: 1024,
      type: 'file',
    });

    expect(descriptor.url).toBe('https://i.catsco.cc/uploads/files/20260101_abcdefabcdefabcdefabcdefabcdef12.pdf');
    expect(descriptor.canPreview).toBe(true);
  });

  test('download URL appends download=1 on the media domain', () => {
    const fileURL = 'https://i.catsco.cc/uploads/files/20260101_abcdefabcdefabcdefabcdefabcdef12.pdf';

    expect(downloadableMediaURL(fileURL)).toBe(`${fileURL}?download=1`);
    expect(downloadableMediaURL('https://i.catsco.cc/uploads/images/20260101_abcdefabcdefabcdefabcdefabcdef12.jpg'))
      .toBe('https://i.catsco.cc/uploads/images/20260101_abcdefabcdefabcdefabcdefabcdef12.jpg?download=1');
  });

  test('rejects untrusted hosts and non-upload paths for download', () => {
    const foreign = 'https://evil.example/uploads/files/20260101_abcdefabcdefabcdefabcdefabcdef12.pdf';

    expect(downloadableMediaURL(foreign)).toBe(foreign);
  });

  test('download works for feedback uploads too', () => {
    const feedbackURL = 'https://i.catsco.cc/uploads/feedback/20260101_abcdefabcdefabcdefabcdefabcdef12.jpg';

    expect(downloadableMediaURL(feedbackURL)).toBe(`${feedbackURL}?download=1`);
  });

  test('preview trust rejects crafted hosts', () => {
    const file = (url) => ({ url, name: 'doc.pdf', size: 1024, type: 'file' });

    expect(previewFileDescriptor(file('https://evilcatsco.cc/uploads/files/20260101_abcdefabcdefabcdefabcdefabcdef12.pdf')).canPreview).toBe(false);
    expect(previewFileDescriptor(file('https://i.catsco.cc.evil.example/uploads/files/20260101_abcdefabcdefabcdefabcdefabcdef12.pdf')).canPreview).toBe(false);
    expect(previewFileDescriptor(file('https://evil.example/uploads/files/20260101_abcdefabcdefabcdefabcdefabcdef12.pdf')).canPreview).toBe(false);
  });

  test('managed artifact preview keeps the app origin while media base is active', () => {
    const artifact = createCloudArtifactPreviewFile({
      id: 'lesson-game',
      title: 'game',
      url: '/uploads/files/20260101_abcdefabcdefabcdefabcdefabcdef12.html',
      publish_version: 1,
      agent_uid: 42,
    });
    const descriptor = previewFileDescriptor(artifact);

    expect(descriptor.url).toContain('/uploads/files/');
    expect(descriptor.url).not.toContain('i.catsco.cc');
  });
});
