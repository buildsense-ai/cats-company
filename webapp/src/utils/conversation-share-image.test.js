import {
  conversationShareMessageKey,
  conversationShareText,
  renderConversationShareImage,
} from './conversation-share-image';

describe('conversation share image helpers', () => {
  it('extracts text and attachment labels from structured content blocks', () => {
    expect(conversationShareText({
      content_blocks: [
        { type: 'text', text: '先整理重点' },
        { type: 'assistant_text', text: '这是最终回答' },
        { type: 'file', payload: { name: 'brief.pdf' } },
        { type: 'image', payload: { name: 'cover.png' } },
      ],
    })).toBe('先整理重点\n这是最终回答\n[文件] brief.pdf\n[图片] cover.png');
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

  it('omits process narration from a share image', () => {
    expect(conversationShareText({
      content_blocks: [
        { type: 'text', text: '正在处理的内部说明', presentation_role: ' Process ' },
        { type: 'text', text: '可以分享的结论', presentation_role: 'result' },
        { type: 'file', payload: { name: 'brief.pdf' } },
      ],
    })).toBe('可以分享的结论\n[文件] brief.pdf');
  });

  it('falls back to legacy content without truncating messages', () => {
    expect(conversationShareText({ content: 'legacy message' })).toBe('legacy message');
    expect(conversationShareText({
      content: JSON.stringify({ type: 'image', payload: { name: 'legacy-cover.png' } }),
    })).toBe('[图片] legacy-cover.png');
    const longText = 'a'.repeat(40);
    expect(conversationShareText({ content: longText })).toBe(longText);
  });

  it('sanitizes private URLs before rendering a share image', () => {
    const text = conversationShareText({
      content: '附件 /uploads/files/private.pdf，公开文档 https://example.com/docs',
    });

    expect(text).not.toContain('/uploads/files/private.pdf');
    expect(text).toContain('https://example.com/docs');
  });

  it('prefers stable message identifiers for selection keys', () => {
    expect(conversationShareMessageKey({ id: 42 }, 3)).toBe('42');
    expect(conversationShareMessageKey({ seq_id: 8 }, 3)).toBe('8');
    expect(conversationShareMessageKey({}, 3)).toBe('share-message-3');
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
    expect(result.pages).toHaveLength(2);
    expect(result.pages.every((page) => page.height <= 9600)).toBe(true);
    expect(context.scale).toHaveBeenCalledWith(expect.any(Number), expect.any(Number));
    expect(context.scale.mock.calls.some(([outputScale]) => outputScale < 1.5)).toBe(true);
  });
});
