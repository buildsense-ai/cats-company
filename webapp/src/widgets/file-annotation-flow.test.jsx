import React, { act, useState } from 'react';
import { createRoot } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

vi.mock('../api', () => ({ resolveMediaURL: value => value }));
vi.mock('./avatar', () => ({ default: () => <span /> }));
vi.mock('./mobile-pdf-preview', () => ({ default: () => <canvas /> }));
import ChatMessage, { FilePreviewPanel } from './chat-message';

// Exercise the real message -> preview -> editor chain. In particular, do not
// fabricate payload.type or replace SpreadsheetPreview with annotated fake DOM.
function FileAnnotationFlow({ message, api }) {
  const [file, setFile] = useState(null);
  return <>
    <ChatMessage message={message} isSelf={false} onPreviewFile={setFile} />
    <FilePreviewPanel file={file} annotationApi={api} onClose={() => setFile(null)} />
  </>;
}

let container;
let root;
beforeEach(() => {
  container = document.createElement('div');
  document.body.append(container);
  root = createRoot(container);
  vi.stubGlobal('matchMedia', () => ({ matches: false, addEventListener() {}, removeEventListener() {} }));
});
afterEach(async () => {
  await act(async () => root.unmount());
  container.remove();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

function button(text) {
  const found = [...container.querySelectorAll('button')].find(item => item.textContent.includes(text));
  expect(found).toBeDefined();
  return found;
}

function typeComment(text) {
  const input = container.querySelector('.file-annotation-popover textarea');
  expect(input).not.toBeNull();
  Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value').set.call(input, text);
  input.dispatchEvent(new Event('input', { bubbles: true }));
}

async function openFile({ name, mime, text, legacy = false }) {
  const payload = { name, url: `/uploads/files/${name}`, mime_type: mime, size: new TextEncoder().encode(text).length };
  const source = { ...payload, type: 'file', topic_id: 'p2p_7_9', message_id: 42, attachment_index: 0, version: 'a'.repeat(64) };
  const api = {
    openFileAnnotationBinding: vi.fn(async () => ({ open_ref: 'fob_test', source })),
    revokeFileAnnotationBinding: vi.fn(async () => ({})),
    sendFileAnnotations: vi.fn(async () => ({ seq_id: 43 })),
    uploadImage: vi.fn(),
  };
  const message = { id: 42, topic_id: 'p2p_7_9', from_uid: 9, created_at: '2026-10-10T00:00:00Z',
    ...(legacy
      ? { msg_type: 'file', content: JSON.stringify({ type: 'file', payload }) }
      : { msg_type: 'text', content: '', content_blocks: [{ type: 'text', text: 'Source' }, { type: 'file', payload }] }),
  };
  vi.stubGlobal('fetch', vi.fn(async () => new Response(text, { status: 200 })));
  await act(async () => root.render(<FileAnnotationFlow message={message} api={api} />));
  await act(async () => container.querySelector('button[title="预览文件"]').click());
  return { api, source };
}

async function submitComment(text) {
  const dialog = container.querySelector('.file-annotation-popover');
  expect(dialog).not.toBeNull();
  expect(getComputedStyle(dialog).display).not.toBe('none');
  await act(async () => typeComment(text));
  await act(async () => button('添加批注').click());
  await act(async () => button('发送 1 条批注').click());
}

describe('file annotation production component flow', () => {
  it.each([false, true])('sends a chat-opened source file without an inner payload type (legacy=%s)', async legacy => {
    const text = '你好😀 world\nsecond line';
    const { api, source } = await openFile({ name: 'note.ts', mime: 'text/plain', text, legacy });
    await act(async () => button('文本选区').click());
    const pre = container.querySelector('[data-file-annotation-text]');
    expect(pre.textContent).toBe(text);
    const range = document.createRange();
    range.setStart(pre.firstChild, 0); range.setEnd(pre.firstChild, 4);
    vi.spyOn(window, 'getSelection').mockReturnValue({ rangeCount: 1, getRangeAt: () => range });
    await act(async () => pre.dispatchEvent(new MouseEvent('mouseup', { bubbles: true })));
    await submitComment('Change this greeting');
    expect(api.revokeFileAnnotationBinding).not.toHaveBeenCalled();
    expect(api.sendFileAnnotations).toHaveBeenCalledTimes(1);
    expect(api.sendFileAnnotations.mock.calls[0][0]).toMatchObject({
      file_annotations: { source, annotations: [{ kind: 'text', body: 'Change this greeting',
        target: { start: 0, end: 4, quote: '你好😀' } }] },
    });
    expect(api.uploadImage).not.toHaveBeenCalled();
  });

  it('shows and sends a cell-range comment from the real CSV preview', async () => {
    const { api, source } = await openFile({ name: 'table.csv', mime: 'text/csv', text: 'name,value\nA,1\nB,2' });
    await act(async () => button('表格选区').click());
    const first = container.querySelector('[data-file-annotation-row="2"][data-file-annotation-column="1"]');
    const last = container.querySelector('[data-file-annotation-row="3"][data-file-annotation-column="2"]');
    expect(first).not.toBeNull();
    expect(last).not.toBeNull();
    await act(async () => {
      first.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, clientX: 100, clientY: 100 }));
      last.dispatchEvent(new PointerEvent('pointerup', { bubbles: true, clientX: 200, clientY: 200 }));
    });
    await submitComment('Recheck these rows');
    expect(api.sendFileAnnotations).toHaveBeenCalledTimes(1);
    expect(api.sendFileAnnotations.mock.calls[0][0]).toMatchObject({
      file_annotations: { source, annotations: [{ kind: 'cells', body: 'Recheck these rows',
        target: { sheet: 'CSV', row_start: 2, row_end: 3, column_start: 1, column_end: 2 } }] },
    });
    expect(api.uploadImage).not.toHaveBeenCalled();
  });
});
