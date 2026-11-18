import { describe, expect, it } from 'vitest';
import { latestNamedAttachment } from './latest-attachment.js';

function fileMessage(id, name, url) {
  return { id, content_blocks: [{ type: 'text', text: name }, { type: 'file', payload: { name, url } }] };
}

const OPENED = {
  name: 'deck.pptx',
  url: '/uploads/files/first.pptx',
  annotation_source: { topic_id: 'g1', message_id: 10, attachment_index: 0 },
};

describe('latest attachment by name', () => {
  it('picks the last attachment with the same name after the opened message', () => {
    const messages = [
      fileMessage(10, 'deck.pptx', '/uploads/files/first.pptx'),
      fileMessage(11, 'notes.docx', '/uploads/files/notes.docx'),
      fileMessage(12, 'deck.pptx', '/uploads/files/second.pptx'),
      fileMessage(13, 'deck.pptx', '/uploads/files/third.pptx'),
    ];
    const latest = latestNamedAttachment(messages, OPENED);
    expect(latest.url).toBe('/uploads/files/third.pptx');
    expect(latest.messageId).toBe(13);
  });

  it('ignores attachments at or before the opened message', () => {
    const messages = [
      fileMessage(9, 'deck.pptx', '/uploads/files/older.pptx'),
      fileMessage(10, 'deck.pptx', '/uploads/files/first.pptx'),
    ];
    expect(latestNamedAttachment(messages, OPENED)).toBeNull();
  });

  it('does not match a different name', () => {
    const messages = [fileMessage(12, 'other.pptx', '/uploads/files/other.pptx')];
    expect(latestNamedAttachment(messages, OPENED)).toBeNull();
  });

  it('returns null when the opened file has no name to match on', () => {
    expect(latestNamedAttachment([fileMessage(12, 'deck.pptx', '/uploads/files/x.pptx')], { url: '/a.pptx' }))
      .toBeNull();
  });

  it('reads blocks nested under content as well', () => {
    const messages = [{
      id: 20,
      content: { content_blocks: [{ type: 'file', payload: { name: 'deck.pptx', url: '/uploads/files/nested.pptx' } }] },
    }];
    expect(latestNamedAttachment(messages, OPENED).url).toBe('/uploads/files/nested.pptx');
  });

  it('skips a payload with no url instead of returning a dead attachment', () => {
    const messages = [
      { id: 21, content_blocks: [{ type: 'file', payload: { name: 'deck.pptx' } }] },
      fileMessage(22, 'deck.pptx', '/uploads/files/live.pptx'),
    ];
    expect(latestNamedAttachment(messages, OPENED).url).toBe('/uploads/files/live.pptx');
  });
});