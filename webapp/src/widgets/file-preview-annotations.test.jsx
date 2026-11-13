import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';

vi.mock('../api', () => ({ resolveMediaURL: value => value }));
vi.mock('./file-annotation-editor', () => ({
  default: ({ source, kind, onModeChange }) => (
    <button data-testid="file-editor" data-kind={kind} data-source={JSON.stringify(source)}
      onClick={() => onModeChange(true)}>开启文件批注</button>
  ),
}));
vi.mock('./mobile-pdf-preview', () => ({
  default: ({ annotationMode }) => <canvas data-testid="pdf-reader" data-mode={String(annotationMode)} />,
}));
vi.mock('./avatar', () => ({ default: () => <span /> }));
import ChatMessage, { FilePreviewPanel, fileAnnotationSourceForPreview, previewFileDescriptor } from './chat-message';

let container;
let root;
beforeEach(() => {
  container = document.createElement('div');
  document.body.append(container);
  root = createRoot(container);
});
afterEach(async () => {
  await act(async () => root.unmount());
  container.remove();
  vi.unstubAllGlobals();
});
const image = { name: 'diagram.png', url: '/uploads/images/diagram.png', mime_type: 'image/png' };
const video = { name: 'demo.mp4', url: '/uploads/files/demo.mp4', mime_type: 'video/mp4' };
const message = (payload = image) => ({ id: 42, topic_id: 'p2p_7_9', from_uid: 9,
  content_blocks: [{ type: 'text', text: 'Result' }, { type: 'tool_use', name: 'render' },
    { type: 'image', payload: image }, { type: 'file', payload: video }],
  content: '', msg_type: 'text', created_at: '2026-10-09T00:00:00Z' });

describe('file preview source and annotation integration', () => {
  it('uses the filtered attachment index, and refuses transient or unrelated message sources', () => {
    expect(fileAnnotationSourceForPreview(message(), video)).toEqual({ topic_id: 'p2p_7_9', message_id: 42, attachment_index: 1 });
    expect(fileAnnotationSourceForPreview(message(), image)).toEqual({ topic_id: 'p2p_7_9', message_id: 42, attachment_index: 0 });
    expect(fileAnnotationSourceForPreview({ ...message(), _pending: true }, image)).toBeNull();
    expect(fileAnnotationSourceForPreview({ ...message(), id: -1 }, image)).toBeNull();
    expect(fileAnnotationSourceForPreview(message(), { ...image, url: '/uploads/images/other.png' })).toBeNull();
  });
  it('resolves merged legacy files against the original delivery message', () => {
    const original = { id: 91, topic_id: 'p2p_7_9', msg_type: 'file',
      content: JSON.stringify(JSON.stringify({ type: 'file', payload: video })) };
    const final = { id: 99, topic_id: 'p2p_7_9', msg_type: 'text', content: 'Done' };
    const merged = { ...final, _display_source_messages: [original, final],
      content_blocks: [{ type: 'file', payload: { ...video } }] };
    expect(fileAnnotationSourceForPreview(merged, { ...video }, 'p2p_7_10'))
      .toEqual({ topic_id: 'p2p_7_9', message_id: 91, attachment_index: 0 });
  });
  it('uses exact payload ownership and refuses ambiguous clones or pending originals', () => {
    const firstPayload = { ...video };
    const secondPayload = { ...video };
    const first = { id: 41, topic_id: 'p2p_7_9', content_blocks: [{ type: 'file', payload: firstPayload }] };
    const second = { id: 42, topic_id: 'p2p_7_9', content_blocks: [{ type: 'file', payload: secondPayload }] };
    const merged = { id: 99, topic_id: 'p2p_7_9', _display_source_messages: [first, second],
      content_blocks: [{ type: 'file', payload: secondPayload }] };
    expect(fileAnnotationSourceForPreview(merged, secondPayload)).toEqual({ topic_id: 'p2p_7_9', message_id: 42, attachment_index: 0 });
    expect(fileAnnotationSourceForPreview(merged, { ...video })).toBeNull();
    expect(fileAnnotationSourceForPreview({ ...merged, _display_source_messages: [{ ...second, _pending: true }] }, secondPayload)).toBeNull();
    expect(fileAnnotationSourceForPreview(merged, { ...video, url: '/uploads/files/other.mp4' })).toBeNull();
  });
  it('supports legacy encoded payloads and uses the original topic over the current topic', () => {
    const legacy = { id: 91, topic_id: 'p2p_7_9', msg_type: 'file', content: JSON.stringify(JSON.stringify({ type: 'file', payload: video })) };
    expect(fileAnnotationSourceForPreview(legacy, video, 'p2p_7_10')).toEqual({ topic_id: 'p2p_7_9', message_id: 91, attachment_index: 0 });
  });
  it('opens image annotation with a source derived from its real message', async () => {
    const onPreviewFile = vi.fn();
    await act(async () => root.render(<ChatMessage message={message()} isSelf={false} onPreviewFile={onPreviewFile} />));
    const button = container.querySelector('[aria-label="批注图片 diagram.png"]');
    expect(button).not.toBeNull();
    await act(async () => button.click());
    expect(onPreviewFile).toHaveBeenCalledWith(expect.objectContaining({ type: 'image', annotation_source: {
      topic_id: 'p2p_7_9', message_id: 42, attachment_index: 0,
    } }));
  });
  it('opens video annotation without changing the existing player preview action', async () => {
    const onPreviewFile = vi.fn();
    await act(async () => root.render(<ChatMessage message={message()} isSelf={false} onPreviewFile={onPreviewFile} />));
    expect(container.querySelector('[aria-label="预览视频 demo.mp4"]')).not.toBeNull();
    await act(async () => container.querySelector('[aria-label="批注视频 demo.mp4"]').click());
    expect(onPreviewFile).toHaveBeenCalledWith(expect.objectContaining({ type: 'file', annotation_source: {
      topic_id: 'p2p_7_9', message_id: 42, attachment_index: 1,
    } }));
  });
  it.each(['report.pdf', 'source.ts', 'table.csv', 'photo.png'])('preserves the enclosing file kind for %s, not an inner payload claim', async name => {
    const payload = { name, url: `/uploads/files/${name}`, type: 'image' };
    const onPreviewFile = vi.fn();
    await act(async () => root.render(<ChatMessage message={{ ...message(), content_blocks: [{ type: 'file', payload }] }}
      isSelf={false} onPreviewFile={onPreviewFile} />));
    await act(async () => container.querySelector('button[title="预览文件"]').click());
    expect(onPreviewFile).toHaveBeenCalledWith(expect.objectContaining({ type: 'file', annotation_source: {
      topic_id: 'p2p_7_9', message_id: 42, attachment_index: 0,
    } }));
  });
  it('recognizes video and common code files in the side preview', () => {
    expect(previewFileDescriptor(video)).toMatchObject({ canPreview: true, isVideo: true });
    expect(previewFileDescriptor({ name: 'types.ts', url: '/uploads/files/types.ts' }).canPreview).toBe(true);
    expect(previewFileDescriptor({ name: 'schema.yaml', url: '/uploads/files/schema.yaml' }).canPreview).toBe(true);
  });
  it('switches a desktop PDF to the annotatable canvas reader', async () => {
    const source = { topic_id: 'p2p_7_9', message_id: 42, attachment_index: 0 };
    vi.stubGlobal('matchMedia', () => ({ matches: false, addEventListener() {}, removeEventListener() {} }));
    await act(async () => root.render(<FilePreviewPanel file={{ name: 'report.pdf', url: '/uploads/files/report.pdf', annotation_source: source }} annotationApi={{}} />));
    expect(container.querySelector('iframe[title="PDF Preview"]')).not.toBeNull();
    await act(async () => container.querySelector('[data-testid="file-editor"]').click());
    expect(container.querySelector('iframe[title="PDF Preview"]')).toBeNull();
    expect(container.querySelector('[data-testid="pdf-reader"]').dataset.mode).toBe('true');
  });
  it('keeps the rendered HTML iframe and its sandbox when annotation starts', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => ({ ok: true, headers: { get: () => null },
      text: async () => '<!doctype html><html><head><style>h1{color:red}</style></head><body><h1>报告</h1></body></html>' })));
    const file = { name: 'report.html', url: '/uploads/files/report.html',
      annotation_source: { topic_id: 'p2p_7_9', message_id: 42, attachment_index: 0 } };
    await act(async () => root.render(<FilePreviewPanel file={file} annotationApi={{}} />));
    const frame = container.querySelector('iframe[title="HTML Report Preview"]');
    expect(frame).not.toBeNull();
    const doc = frame.getAttribute('srcdoc');
    expect(doc).toContain('h1{color:red}');
    expect(frame.getAttribute('sandbox')).not.toContain('allow-same-origin');
    expect(container.querySelector('[data-testid="file-editor"]').dataset.kind).toBe('html');
    await act(async () => container.querySelector('[data-testid="file-editor"]').click());
    expect(container.querySelector('iframe[title="HTML Report Preview"]')).toBe(frame);
    expect(frame.getAttribute('srcdoc')).toBe(doc);
    expect(container.querySelector('pre[data-file-annotation-text]')).toBeNull();
  });
  it('renders the editor only when a persisted source and annotation API are available', async () => {
    await act(async () => root.render(<FilePreviewPanel file={image} annotationApi={{}} />));
    expect(container.querySelector('[data-testid="file-editor"]')).toBeNull();
    await act(async () => root.render(<FilePreviewPanel file={{ ...image, annotation_source: { topic_id: 'p2p_7_9', message_id: 42, attachment_index: 0 } }} annotationApi={{}} />));
    expect(container.querySelector('[data-testid="file-editor"]').dataset.kind).toBe('image');
    expect(container.querySelector('img').dataset.fileAnnotationSurface).toBe('image');
  });
});
