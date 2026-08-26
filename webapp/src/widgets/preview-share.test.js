import { sharePreviewLink } from './preview-share';

describe('sharePreviewLink', () => {
  it('shares the inline preview URL instead of the download URL', async () => {
    const share = vi.fn().mockResolvedValue(undefined);
    const navigatorLike = {
      share,
      clipboard: { writeText: vi.fn() },
    };

    const result = await sharePreviewLink({
      url: '/uploads/files/report.pdf',
      name: 'report.pdf',
      navigatorLike,
    });

    expect(result).toMatchObject({ status: 'shared', method: 'native' });
    expect(share).toHaveBeenCalledWith({
      title: 'report.pdf',
      url: new URL('/uploads/files/report.pdf?preview=1&name=report.pdf', window.location.href).toString(),
    });
    expect(share.mock.calls[0][0].url).not.toContain('download=1');
    expect(navigatorLike.clipboard.writeText).not.toHaveBeenCalled();
  });

  it('shares a video metadata preview URL instead of the download URL', async () => {
    const share = vi.fn().mockResolvedValue(undefined);

    const result = await sharePreviewLink({
      url: '/uploads/files/demo.mp4?download=1',
      name: 'demo.mp4',
      navigatorLike: { share },
    });

    expect(result).toMatchObject({ status: 'shared', method: 'native' });
    expect(share).toHaveBeenCalledWith({
      title: 'demo.mp4',
      url: new URL('/uploads/files/demo.mp4?preview=1&name=demo.mp4', window.location.href).toString(),
    });
  });

  it('copies the inline preview URL when native sharing is unavailable', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);

    const result = await sharePreviewLink({
      url: '/uploads/files/report.html?download=1',
      name: 'report.html',
      navigatorLike: { clipboard: { writeText } },
    });

    expect(result).toMatchObject({ status: 'copied', method: 'clipboard' });
    expect(writeText).toHaveBeenCalledWith(
      new URL('/uploads/files/report.html?preview=1&name=report.html', window.location.href).toString(),
    );
  });

  it('does not advertise unsupported XHTML uploads as metadata previews', async () => {
    const share = vi.fn().mockResolvedValue(undefined);

    await sharePreviewLink({
      url: '/uploads/files/report.xhtml',
      name: 'report.xhtml',
      navigatorLike: { share },
    });

    expect(share).toHaveBeenCalledWith({
      title: 'report.xhtml',
      url: new URL('/uploads/files/report.xhtml', window.location.href).toString(),
    });
  });

  it('does not fall back to copying when the user cancels native sharing', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    const share = vi.fn().mockRejectedValue(Object.assign(new Error('cancelled'), { name: 'AbortError' }));

    const result = await sharePreviewLink({
      url: '/uploads/files/report.html',
      name: 'report.html',
      navigatorLike: { share, clipboard: { writeText } },
    });

    expect(result).toMatchObject({ status: 'cancelled' });
    expect(writeText).not.toHaveBeenCalled();
  });

  it('does not fall back to copying when native sharing fails', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    const nativeError = Object.assign(new Error('permission denied'), { name: 'NotAllowedError' });
    const share = vi.fn().mockRejectedValue(nativeError);

    const result = await sharePreviewLink({
      url: '/uploads/files/report.html',
      name: 'report.html',
      navigatorLike: { share, clipboard: { writeText } },
    });

    expect(result).toMatchObject({ status: 'error', reason: 'native-share', error: nativeError });
    expect(writeText).not.toHaveBeenCalled();
  });
});
