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
      url: new URL('/uploads/files/report.pdf', window.location.href).toString(),
    });
    expect(share.mock.calls[0][0].url).not.toContain('download=1');
    expect(navigatorLike.clipboard.writeText).not.toHaveBeenCalled();
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
      new URL('/uploads/files/report.html', window.location.href).toString(),
    );
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
