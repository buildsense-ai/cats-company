import {
  conversationShareMessageKey,
  conversationShareText,
  downloadConversationShareImage,
  downloadConversationShareImages,
  renderConversationShareImage,
} from './conversation-share-image';

describe('conversation share image helpers', () => {
  it('extracts text and attachment labels from structured content blocks', () => {
    expect(conversationShareText({
      content_blocks: [
        { type: 'text', text: '先整理重点' },
        { type: 'file', payload: { name: 'brief.pdf' } },
        { type: 'image', payload: { name: 'cover.png' } },
      ],
    })).toBe('先整理重点\n[文件] brief.pdf\n[图片] cover.png');
  });

  it('keeps full long text and attachment labels', () => {
    const longText = '0123456789abcdefghijklmnopqrstuvwxyz'.repeat(4);
    expect(conversationShareText({
      content_blocks: [
        { type: 'text', text: longText },
        { type: 'file', payload: { name: 'brief.pdf' } },
        { type: 'image', payload: { name: 'cover.png' } },
      ],
    })).toBe(`${longText}\n[文件] brief.pdf\n[图片] cover.png`);
  });

  it('falls back to legacy content without truncating messages', () => {
    expect(conversationShareText({ content: 'legacy message' })).toBe('legacy message');
    expect(conversationShareText({
      content: JSON.stringify({ type: 'image', payload: { name: 'legacy-cover.png' } }),
    })).toBe('[图片] legacy-cover.png');
    const longText = 'a'.repeat(40);
    expect(conversationShareText({ content: longText })).toBe(longText);
  });

  it('prefers stable message identifiers for selection keys', () => {
    expect(conversationShareMessageKey({ id: 42 }, 3)).toBe('42');
    expect(conversationShareMessageKey({ seq_id: 8 }, 3)).toBe('8');
    expect(conversationShareMessageKey({}, 3)).toBe('share-message-3');
  });

  it('uses a Blob URL to start a desktop PNG download', async () => {
    vi.useFakeTimers();
    const createObjectURL = vi.spyOn(URL, 'createObjectURL').mockReturnValue('blob:catsco-share');
    const revokeObjectURL = vi.spyOn(URL, 'revokeObjectURL').mockImplementation(() => {});
    let clickedHref = '';
    let clickedFilename = '';
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function recordClick() {
      clickedHref = this.href;
      clickedFilename = this.download;
    });

    await expect(downloadConversationShareImage('data:image/png;base64,aGVsbG8=', 'share.png')).resolves.toBe(true);

    const blob = createObjectURL.mock.calls[0][0];
    expect(blob).toBeInstanceOf(Blob);
    expect(blob.type).toBe('image/png');
    expect(clickedHref).toBe('blob:catsco-share');
    expect(clickedFilename).toBe('share.png');
    vi.advanceTimersByTime(60_000);
    expect(revokeObjectURL).toHaveBeenCalledWith('blob:catsco-share');

    click.mockRestore();
    createObjectURL.mockRestore();
    revokeObjectURL.mockRestore();
    vi.useRealTimers();
  });

  it('uses the native file share sheet on mobile browsers', async () => {
    const canShare = vi.fn(() => true);
    const share = vi.fn(() => Promise.resolve());
    vi.stubGlobal('navigator', {
      userAgent: 'Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X)',
      maxTouchPoints: 0,
      canShare,
      share,
    });

    try {
      await expect(downloadConversationShareImage('data:image/png;base64,aGVsbG8=', 'share.png')).resolves.toBe(true);
      expect(canShare).toHaveBeenCalledWith(expect.objectContaining({
        files: [expect.any(File)],
      }));
      expect(share).toHaveBeenCalledWith(expect.objectContaining({
        title: 'CatsCo 对话分享图',
        files: [expect.any(File)],
      }));
      const [file] = share.mock.calls[0][0].files;
      expect(file.name).toBe('share.png');
      expect(file.type).toBe('image/png');
    } finally {
      vi.unstubAllGlobals();
    }
  });

  it('opens a saveable image tab when native file sharing rejects asynchronously', async () => {
    vi.useFakeTimers();
    const createObjectURL = vi.spyOn(URL, 'createObjectURL').mockReturnValue('blob:catsco-share');
    const revokeObjectURL = vi.spyOn(URL, 'revokeObjectURL').mockImplementation(() => {});
    const open = vi.spyOn(window, 'open').mockReturnValue({});
    const share = vi.fn(() => Promise.reject(new DOMException('share blocked', 'NotAllowedError')));
    vi.stubGlobal('navigator', {
      userAgent: 'Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X)',
      maxTouchPoints: 0,
      canShare: vi.fn(() => true),
      share,
    });

    try {
      await expect(downloadConversationShareImage('data:image/png;base64,aGVsbG8=', 'share.png')).resolves.toBe(true);
      expect(share).toHaveBeenCalledTimes(1);
      expect(open).toHaveBeenCalledWith('blob:catsco-share', '_blank');
      vi.advanceTimersByTime(300_000);
      expect(revokeObjectURL).toHaveBeenCalledWith('blob:catsco-share');
    } finally {
      vi.unstubAllGlobals();
      open.mockRestore();
      createObjectURL.mockRestore();
      revokeObjectURL.mockRestore();
      vi.useRealTimers();
    }
  });

  it('opens a saveable image tab when mobile file sharing is unavailable', async () => {
    vi.useFakeTimers();
    const createObjectURL = vi.spyOn(URL, 'createObjectURL').mockReturnValue('blob:catsco-share');
    const revokeObjectURL = vi.spyOn(URL, 'revokeObjectURL').mockImplementation(() => {});
    const open = vi.spyOn(window, 'open').mockReturnValue({});
    vi.stubGlobal('navigator', {
      userAgent: 'Mozilla/5.0 (iPad; CPU OS 18_0 like Mac OS X)',
      maxTouchPoints: 0,
      canShare: vi.fn(() => false),
      share: vi.fn(),
    });

    try {
      await expect(downloadConversationShareImage('data:image/png;base64,aGVsbG8=', 'share.png')).resolves.toBe(true);
      expect(open).toHaveBeenCalledWith('blob:catsco-share', '_blank');
      vi.advanceTimersByTime(300_000);
      expect(revokeObjectURL).toHaveBeenCalledWith('blob:catsco-share');
    } finally {
      vi.unstubAllGlobals();
      open.mockRestore();
      createObjectURL.mockRestore();
      revokeObjectURL.mockRestore();
      vi.useRealTimers();
    }
  });

  it('shares every generated page together on mobile when the browser supports it', async () => {
    const canShare = vi.fn(() => true);
    const share = vi.fn(() => Promise.resolve());
    vi.stubGlobal('navigator', {
      userAgent: 'Mozilla/5.0 (Linux; Android 15)',
      maxTouchPoints: 0,
      canShare,
      share,
    });

    try {
      await expect(downloadConversationShareImages([
        'data:image/png;base64,b25l',
        'data:image/png;base64,dHdv',
      ])).resolves.toBe(true);
      expect(share).toHaveBeenCalledTimes(1);
      expect(share.mock.calls[0][0].files.map((file) => file.name)).toEqual([
        'catsco-conversation-share-01.png',
        'catsco-conversation-share-02.png',
      ]);
    } finally {
      vi.unstubAllGlobals();
    }
  });

  it('does not burst-download mobile pages after native sharing fails', async () => {
    const share = vi.fn(() => Promise.reject(new DOMException('share blocked', 'NotAllowedError')));
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {});
    vi.stubGlobal('navigator', {
      userAgent: 'Mozilla/5.0 (Linux; Android 15)',
      maxTouchPoints: 0,
      canShare: vi.fn(() => true),
      share,
    });

    try {
      await expect(downloadConversationShareImages([
        'data:image/png;base64,b25l',
        'data:image/png;base64,dHdv',
      ])).resolves.toBe(false);
      expect(share).toHaveBeenCalledTimes(1);
      expect(click).not.toHaveBeenCalled();
    } finally {
      vi.unstubAllGlobals();
      click.mockRestore();
    }
  });

  it('downloads all generated pages as one ZIP on desktop', async () => {
    vi.useFakeTimers();
    const createObjectURL = vi.spyOn(URL, 'createObjectURL').mockReturnValue('blob:catsco-share-zip');
    const revokeObjectURL = vi.spyOn(URL, 'revokeObjectURL').mockImplementation(() => {});
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {});
    vi.stubGlobal('navigator', {
      userAgent: 'Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 Chrome/140.0.0.0 Safari/537.36',
      maxTouchPoints: 0,
    });

    try {
      const download = downloadConversationShareImages([
        'data:image/png;base64,b25l',
        'data:image/png;base64,dHdv',
      ]);
      expect(click).toHaveBeenCalledTimes(1);
      await expect(download).resolves.toBe(true);

      const [zipBlob] = createObjectURL.mock.calls[0];
      expect(zipBlob).toBeInstanceOf(Blob);
      expect(zipBlob.type).toBe('application/zip');
      const zipText = new TextDecoder().decode(new Uint8Array(await zipBlob.arrayBuffer()));
      expect(zipText).toContain('catsco-conversation-share-01.png');
      expect(zipText).toContain('catsco-conversation-share-02.png');
      const downloadLink = click.mock.instances[0];
      expect(downloadLink.download).toBe('catsco-conversation-share.zip');
    } finally {
      vi.unstubAllGlobals();
      click.mockRestore();
      createObjectURL.mockRestore();
      revokeObjectURL.mockRestore();
      vi.useRealTimers();
    }
  });

  it('preserves a custom download prefix in the desktop ZIP filename', async () => {
    const createObjectURL = vi.spyOn(URL, 'createObjectURL').mockReturnValue('blob:catsco-share-zip');
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {});
    vi.stubGlobal('navigator', {
      userAgent: 'Mozilla/5.0 (X11; Linux x86_64)',
      maxTouchPoints: 0,
    });

    try {
      await expect(downloadConversationShareImages([
        'data:image/png;base64,b25l',
        'data:image/png;base64,dHdv',
      ], '对话分享')).resolves.toBe(true);
      expect(click.mock.instances[0].download).toBe('对话分享.zip');
    } finally {
      vi.unstubAllGlobals();
      click.mockRestore();
      createObjectURL.mockRestore();
    }
  });

  it('renders a branded PNG payload with a scale-aware canvas', async () => {
    const originalCreateElement = document.createElement.bind(document);
    const fillStyles = [];
    const strokeStyles = [];
    const context = {
      arcTo: vi.fn(),
      beginPath: vi.fn(),
      closePath: vi.fn(),
      fill: vi.fn(),
      fillRect: vi.fn(),
      fillText: vi.fn(),
      measureText: vi.fn((value) => ({ width: String(value).length * 10 })),
      moveTo: vi.fn(),
      lineTo: vi.fn(),
      restore: vi.fn(),
      save: vi.fn(),
      scale: vi.fn(),
      stroke: vi.fn(),
    };
    Object.defineProperty(context, 'fillStyle', {
      get: () => fillStyles.at(-1),
      set: (value) => fillStyles.push(value),
    });
    Object.defineProperty(context, 'strokeStyle', {
      get: () => strokeStyles.at(-1),
      set: (value) => strokeStyles.push(value),
    });
    const canvas = {
      getContext: vi.fn(() => context),
      toDataURL: vi.fn(() => 'data:image/png;base64,share'),
      style: {},
    };
    vi.spyOn(document, 'createElement').mockImplementation((tagName, options) => (
      tagName === 'canvas' ? canvas : originalCreateElement(tagName, options)
    ));

    await expect(renderConversationShareImage({
      logoUrl: '',
      items: [{
        message: { id: 1, content: 'hello', created_at: '2026-08-13T03:00:00Z' },
        senderName: 'Me',
        isSelf: true,
      }],
      topicName: '一个非常非常非常非常非常非常非常非常非常非常非常长的会话标题',
      theme: 'liquid-green',
      width: 400,
      scale: 2,
    })).resolves.toMatchObject({
      dataUrl: 'data:image/png;base64,share',
      width: 800,
    });
    expect(canvas.getContext).toHaveBeenCalledWith('2d');
    expect(canvas.toDataURL).toHaveBeenCalledWith('image/png');
    expect(context.scale).toHaveBeenCalledWith(2, 2);
    expect(fillStyles).toContain('#151718');
    expect(strokeStyles).toContain('#3ab292');
    expect(context.fillText).toHaveBeenCalledWith(expect.stringMatching(/…$/), 344, 94);
    expect(context.fillText).toHaveBeenCalledWith(expect.stringMatching(/^由/), 56, expect.any(Number));
    expect(context.fillText).toHaveBeenCalledWith('CatsCo', 194, 429);
    expect(context.fillText).toHaveBeenCalledWith('app.catsco.cc', 194, 454);
    expect(context.fillRect).toHaveBeenCalledWith(228, 384, 4, 4);
    expect(context.fillRect).not.toHaveBeenCalledWith(212, 368, 4, 4);

    await renderConversationShareImage({
      logoUrl: '',
      items: [{
        message: { id: 2, content: 'hello', created_at: '2026-08-13T03:00:00Z' },
        senderName: 'Me',
        isSelf: true,
      }],
      topicName: '对话',
      width: 720,
      scale: 2,
    });
    expect(context.fillText).toHaveBeenCalledWith('对话已整理为可分享图片', 56, 429);
    expect(context.fillText).toHaveBeenCalledWith('1 条消息 · 保留发送者、时间与附件标签', 56, 454);
    expect(context.fillText).toHaveBeenCalledWith('CatsCo', 514, 429);
    expect(context.fillText).toHaveBeenCalledWith('app.catsco.cc', 514, 454);
    expect(context.fillRect).toHaveBeenCalledWith(548, 384, 4, 4);
    expect(context.fillRect).not.toHaveBeenCalledWith(532, 368, 4, 4);
  });

  it('paginates one long message without truncating its final content', async () => {
    const originalCreateElement = document.createElement.bind(document);
    const context = {
      arcTo: vi.fn(),
      beginPath: vi.fn(),
      closePath: vi.fn(),
      fill: vi.fn(),
      fillRect: vi.fn(),
      fillText: vi.fn(),
      measureText: vi.fn((value) => ({ width: String(value).length * 10 })),
      moveTo: vi.fn(),
      lineTo: vi.fn(),
      restore: vi.fn(),
      save: vi.fn(),
      scale: vi.fn(),
      stroke: vi.fn(),
    };
    const canvas = {
      getContext: vi.fn(() => context),
      toDataURL: vi.fn(() => 'data:image/png;base64:single-long-share'),
      style: {},
    };
    vi.spyOn(document, 'createElement').mockImplementation((tagName, options) => (
      tagName === 'canvas' ? canvas : originalCreateElement(tagName, options)
    ));
    const longText = `${'a'.repeat(30_000)}END-OF-MESSAGE`;

    const result = await renderConversationShareImage({
      logoUrl: '',
      items: [{
        message: { id: 1, content: longText, created_at: '2026-08-13T03:00:00Z' },
        senderName: 'Me',
        isSelf: true,
      }],
    });

    expect(result.pages.length).toBeGreaterThan(1);
    expect(context.fillText.mock.calls.some(([text]) => String(text).includes('END-OF-MESSAGE'))).toBe(true);
  });

  it('paginates a long 50-message share instead of rejecting the selection', async () => {
    const originalCreateElement = document.createElement.bind(document);
    const context = {
      arcTo: vi.fn(),
      beginPath: vi.fn(),
      closePath: vi.fn(),
      fill: vi.fn(),
      fillRect: vi.fn(),
      fillText: vi.fn(),
      measureText: vi.fn((value) => ({ width: String(value).length * 10 })),
      moveTo: vi.fn(),
      lineTo: vi.fn(),
      restore: vi.fn(),
      save: vi.fn(),
      scale: vi.fn(),
      stroke: vi.fn(),
    };
    const canvas = {
      getContext: vi.fn(() => context),
      toDataURL: vi.fn(() => 'data:image/png;base64:long-share'),
      style: {},
    };
    vi.spyOn(document, 'createElement').mockImplementation((tagName, options) => (
      tagName === 'canvas' ? canvas : originalCreateElement(tagName, options)
    ));

    const result = await renderConversationShareImage({
      logoUrl: '',
      items: Array.from({ length: 50 }, (_, index) => ({
        message: { id: index + 1, content: '消息内容'.repeat(63) },
        senderName: index % 2 === 0 ? 'Me' : 'CatsCo',
        isSelf: index % 2 === 0,
      })),
    });

    expect(result.dataUrl).toBe('data:image/png;base64:long-share');
    expect(result.pages).toHaveLength(3);
    expect(result.pages.every((page) => page.height <= 7680)).toBe(true);
    expect(context.scale).toHaveBeenCalledWith(expect.any(Number), expect.any(Number));
    expect(context.scale.mock.calls.every(([outputScale]) => outputScale >= 1.5)).toBe(true);
  });
});
