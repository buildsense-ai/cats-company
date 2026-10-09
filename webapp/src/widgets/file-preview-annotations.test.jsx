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
    expect(onPreviewFile).toHaveBeenCalledWith(expect.objectContaining({ annotation_source: {
      topic_id: 'p2p_7_9', message_id: 42, attachment_index: 1,
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
  it('renders the editor only when a persisted source and annotation API are available', async () => {
    await act(async () => root.render(<FilePreviewPanel file={image} annotationApi={{}} />));
    expect(container.querySelector('[data-testid="file-editor"]')).toBeNull();
    await act(async () => root.render(<FilePreviewPanel file={{ ...image, annotation_source: { topic_id: 'p2p_7_9', message_id: 42, attachment_index: 0 } }} annotationApi={{}} />));
    expect(container.querySelector('[data-testid="file-editor"]').dataset.kind).toBe('image');
    expect(container.querySelector('img').dataset.fileAnnotationSurface).toBe('image');
  });
});
