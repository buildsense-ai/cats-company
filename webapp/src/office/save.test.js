// Save linkage.
//
// The claim under test: saving produces a message that 小八 will read as the current version,
// and the original attachment is left untouched. Both are structural facts about the payload,
// so they can be asserted without a server.

import { describe, it, expect } from 'vitest';
import {
  buildOfficeUpdateMessage, countChangedOps, officeBytesToFile, officeSavedFileName,
  OFFICE_SAVE_NOTICE,
} from './save.js';

it('keeps the save notice explicit about the new attachment and what 小八 reads', () => {
  expect(OFFICE_SAVE_NOTICE).toBe(
    '已保存为新版本。会话里出现的新附件就是这一版，小八读到的也是这一版',
  );
});

const UPLOAD = {
  file_key: 'abc123.pptx',
  url: '/uploads/files/abc123.pptx',
  name: '季度复盘.pptx',
  size: 48123,
  mime_type: 'application/vnd.openxmlformats-officedocument.presentationml.presentation',
};

describe('saved file name', () => {
  it('keeps the original name so the next save looks like the same document', () => {
    expect(officeSavedFileName('季度复盘.pptx', 'pptx')).toBe('季度复盘.pptx');
  });

  it('repairs a missing extension from the family rather than guessing', () => {
    expect(officeSavedFileName('季度复盘', 'pptx')).toBe('季度复盘.pptx');
    expect(officeSavedFileName('备忘', 'docx')).toBe('备忘.docx');
  });

  it('replaces a legacy extension, because pptx bytes under a .ppt name make Office fail', () => {
    expect(officeSavedFileName('旧版.ppt', 'pptx')).toBe('旧版.pptx');
    expect(officeSavedFileName('旧版.doc', 'docx')).toBe('旧版.docx');
  });

  it('falls back to a neutral name when nothing usable is supplied', () => {
    expect(officeSavedFileName('', 'pptx')).toBe('presentation.pptx');
    expect(officeSavedFileName(null, 'docx')).toBe('document.docx');
  });
});

describe('update message', () => {
  it('carries both a text block and a file block', () => {
    const message = buildOfficeUpdateMessage({
      uploaded: UPLOAD, originalName: '季度复盘.pptx', slideCount: 4, changedParts: 3,
    });
    // A message with only a file block renders as an empty bubble in the list and the
    // notification preview, both of which read `content`.
    expect(message.content_blocks).toHaveLength(2);
    expect(message.content_blocks[0].type).toBe('text');
    expect(message.content_blocks[1].type).toBe('file');
    expect(message.content).toContain('季度复盘.pptx');
    expect(message.content).toContain('4 页');
    expect(message.content).toContain('3 处');
  });

  it('points at the newly uploaded file, never the original', () => {
    const message = buildOfficeUpdateMessage({ uploaded: UPLOAD, originalName: '季度复盘.pptx' });
    expect(message.payload.file_key).toBe('abc123.pptx');
    expect(message.payload.url).toBe('/uploads/files/abc123.pptx');
    expect(message.content_blocks[1].payload).toBe(message.payload);
  });

  it('omits the page and change counts when they are unknown, instead of printing zero', () => {
    const message = buildOfficeUpdateMessage({ uploaded: UPLOAD, originalName: 'x.pptx' });
    expect(message.content).not.toContain('0 页');
    expect(message.content).not.toContain('0 处');
  });

  it('refuses an upload result with no url rather than recording a dead attachment', () => {
    expect(() => buildOfficeUpdateMessage({
      uploaded: { ...UPLOAD, url: '' }, originalName: 'x.pptx',
    })).toThrow('上传结果缺少地址');
  });

  it('supplies the office mime type when the upload result omits it', () => {
    const message = buildOfficeUpdateMessage({
      uploaded: { ...UPLOAD, mime_type: '' }, originalName: 'x.pptx',
    });
    expect(message.payload.mime_type).toBe(
      'application/vnd.openxmlformats-officedocument.presentationml.presentation',
    );
  });

  it('labels a Word document as Word, not as PPT', () => {
    const message = buildOfficeUpdateMessage({
      uploaded: { ...UPLOAD, mime_type: '' }, originalName: '备忘.docx', family: 'docx',
    });
    expect(message.content).toContain('Word');
    expect(message.payload.mime_type).toContain('wordprocessingml');
  });
});

describe('bytes to file', () => {
  it('wraps bytes with the repaired name and office mime type', () => {
    const bytes = new Uint8Array([1, 2, 3]);
    const file = officeBytesToFile(bytes, '季度复盘', 'pptx');
    expect(file.name).toBe('季度复盘.pptx');
    expect(file.size).toBe(3);
    expect(file.type).toBe(
      'application/vnd.openxmlformats-officedocument.presentationml.presentation',
    );
  });

  it('rejects empty bytes instead of uploading an empty file', () => {
    expect(() => officeBytesToFile(new Uint8Array(0), 'x.pptx')).toThrow('没有可保存的内容');
    expect(() => officeBytesToFile(null, 'x.pptx')).toThrow('没有可保存的内容');
  });
});

describe('change counting', () => {
  it('counts operations, not individual pointer moves', () => {
    // Dragging a shape fifty times and writing it once is one change, which is what the
    // user would say out loud.
    expect(countChangedOps([
      { op: 'geometry', elId: 'a' },
      { op: 'text', elId: 'b' },
    ])).toBe(2);
    expect(countChangedOps([{ op: 'geometry', elId: 'a' }])).toBe(1);
  });

  it('ignores malformed entries rather than reporting a phantom change', () => {
    expect(countChangedOps([null, {}, { op: 'text' }])).toBe(1);
    expect(countChangedOps(null)).toBe(0);
  });
});
