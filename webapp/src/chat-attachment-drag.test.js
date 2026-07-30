import {
  CHAT_ATTACHMENT_DRAG_TYPE,
  attachmentFromContentBlock,
  attachmentIdentity,
  readChatAttachmentDrag,
  writeChatAttachmentDrag,
} from './chat-attachment-drag';

function dataTransferStub() {
  const values = new Map();
  return {
    types: [],
    effectAllowed: 'none',
    setData(type, value) {
      values.set(type, value);
      if (!this.types.includes(type)) this.types.push(type);
    },
    getData(type) {
      return values.get(type) || '';
    },
  };
}

describe('chat attachment drag protocol', () => {
  it('consumes a trusted attachment token only once', () => {
    const dataTransfer = dataTransferStub();
    const payload = {
      file_key: 'cat.png',
      url: '/uploads/images/cat.png',
      name: 'cat.png',
      size: 12,
    };

    expect(writeChatAttachmentDrag(dataTransfer, { type: 'image', payload })).toBe(true);
    const token = dataTransfer.getData(CHAT_ATTACHMENT_DRAG_TYPE);
    expect(token).toMatch(/^(?:[0-9a-f-]{36}|[0-9a-f]{48})$/i);
    expect(readChatAttachmentDrag(dataTransfer)).toMatchObject({
      type: 'image',
      name: 'cat.png',
      content: { payload: { file_key: 'cat.png' } },
    });
    expect(readChatAttachmentDrag(dataTransfer)).toBeNull();
  });

  it('rejects a forged token that was not created by this page', () => {
    const dataTransfer = dataTransferStub();
    dataTransfer.setData(CHAT_ATTACHMENT_DRAG_TYPE, '00000000-0000-4000-8000-000000000000');
    expect(readChatAttachmentDrag(dataTransfer)).toBeNull();
  });

  it('rejects attachments outside the trusted CatsCo upload origin', () => {
    expect(attachmentFromContentBlock({
      type: 'image',
      payload: {
        file_key: 'forged-key',
        url: 'https://example.com/remote.png',
        name: 'remote.png',
      },
    })).toBeNull();
  });

  it('rejects URL-only and same-origin key mismatches', () => {
    expect(attachmentFromContentBlock({
      type: 'image',
      payload: { url: '/uploads/images/cat.png', name: 'cat.png' },
    })).toBeNull();
    expect(attachmentFromContentBlock({
      type: 'image',
      payload: { file_key: 'other.png', url: '/uploads/images/cat.png', name: 'cat.png' },
    })).toBeNull();
  });

  it('rejects attachment types that do not match the managed upload directory', () => {
    expect(attachmentFromContentBlock({
      type: 'file',
      payload: { file_key: 'cat.png', url: '/uploads/images/cat.png', name: 'cat.png' },
    })).toBeNull();
    expect(attachmentFromContentBlock({
      type: 'image',
      payload: { file_key: 'report.pdf', url: '/uploads/files/report.pdf', name: 'report.pdf' },
    })).toBeNull();
  });

  it('uses the same identity for bare and directory-prefixed file keys', () => {
    const bareKey = {
      type: 'image',
      payload: { file_key: 'cat.png', url: '/uploads/images/cat.png' },
    };
    const prefixedKey = {
      type: 'image',
      payload: { file_key: 'images/cat.png', url: '/uploads/images/cat.png' },
    };

    expect(attachmentIdentity(bareKey)).toBe(attachmentIdentity(prefixedKey));
  });

  it('rejects feedback uploads as chat image attachments', () => {
    expect(attachmentFromContentBlock({
      type: 'image',
      payload: {
        file_key: 'feedback/screenshot.png',
        url: '/uploads/feedback/screenshot.png',
        name: 'screenshot.png',
      },
    })).toBeNull();
  });

  it('rejects a drag token after its absolute expiry time', () => {
    vi.useFakeTimers();
    try {
      const dataTransfer = dataTransferStub();
      const startedAt = Date.now();
      expect(writeChatAttachmentDrag(dataTransfer, {
        type: 'image',
        payload: {
          file_key: 'cat.png',
          url: '/uploads/images/cat.png',
          name: 'cat.png',
        },
      })).toBe(true);

      vi.setSystemTime(startedAt + 60_001);
      expect(readChatAttachmentDrag(dataTransfer)).toBeNull();
    } finally {
      vi.useRealTimers();
    }
  });
});
