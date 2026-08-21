import { sanitizeConversationShareText } from './conversation-share-text';

describe('sanitizeConversationShareText', () => {
  it('removes private upload and capability URLs while preserving public links', () => {
    const text = sanitizeConversationShareText('附件 /uploads/files/private.pdf，备用链接 https://app.example.test/?next=%2Fuploads%2Fimages%2Fsecret.png，公开文档 https://example.com/docs');

    expect(text).not.toContain('/uploads/files/private.pdf');
    expect(text).not.toContain('/uploads/images/secret.png');
    expect(text).toContain('https://example.com/docs');
  });

  it('normalizes encoded separators and dot segments before classifying a URL', () => {
    expect(sanitizeConversationShareText('https://app.example.test/?redirect=%2Fapi%2Ffoo%2F..%2Fshared-conversations%2Ftoken')).toBe('');
    expect(sanitizeConversationShareText('查看 https://example.com/docs')).toBe('查看 https://example.com/docs');
  });
});
