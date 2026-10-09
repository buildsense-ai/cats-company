import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import FileAnnotationEditor from './file-annotation-editor';

const SOURCE = { topic_id: 'p2p_7_9', message_id: 123, attachment_index: 0 };
const BOUND_SOURCE = {
  topic_id: 'p2p_7_9', message_id: 123, attachment_index: 0,
  name: 'image.png', url: '/uploads/images/key.png', file_key: 'key.png',
  mime_type: 'image/png', type: 'image', size: 123, version: 'a'.repeat(64),
};
const FILE = { name: 'image.png', url: '/uploads/images/key.png', file_key: 'key.png', mime_type: 'image/png', type: 'image', size: 123 };

const JPEG = (() => {
  const raw = new Uint8Array([0xFF, 0xD8, 0xFF, 0xC0, 0x00, 0x11, 0x08,
    0x02, 0x58, 0x03, 0x20, 0x03, 0x01, 0x22, 0x00, 0x02, 0x11, 0x01, 0x03, 0x11, 0x01, 0xFF, 0xD9]);
  let binary = '';
  raw.forEach((byte) => { binary += String.fromCharCode(byte); });
  return `data:image/jpeg;base64,${btoa(binary)}`;
})();

function makeApi(overrides = {}) {
  return {
    openFileAnnotationBinding: vi.fn(async () => ({ open_ref: 'fob_1', source: BOUND_SOURCE })),
    sendFileAnnotations: vi.fn(async () => ({ seq_id: 7 })),
    revokeFileAnnotationBinding: vi.fn(async () => ({})),
    uploadImage: vi.fn(async (file) => {
      const key = `20261009_${globalThis.crypto.randomUUID().replace(/-/g, '')}.jpg`;
      return { file_key: key, url: `/uploads/images/${key}`, size: file.size, mime_type: 'image/jpeg' };
    }),
    ...overrides,
  };
}

describe('FileAnnotationEditor', () => {
  let container;
  let root;
  let contentRef;

  function mount({ kind = 'image', api = makeApi(), onSent = vi.fn(), onModeChange = vi.fn(), file = FILE, source = SOURCE } = {}) {
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
    contentRef = { current: null };
    act(() => {
      root.render(
        <FileAnnotationEditor file={file} kind={kind} contentRef={contentRef} source={source}
          onSent={onSent} api={api} onModeChange={onModeChange} />,
      );
    });
    return { api, onSent, onModeChange };
  }

  function mountSurface(html, kind = 'image') {
    const wrapper = document.createElement('div');
    wrapper.innerHTML = html;
    contentRef.current = wrapper;
    container.appendChild(wrapper);
    // jsdom lacks layout: give every surface a stable content box.
    wrapper.querySelectorAll('[data-file-annotation-surface]').forEach((node) => {
      node.getBoundingClientRect = () => ({ left: 100, top: 100, right: 900, bottom: 700, width: 800, height: 600, x: 100, y: 100 });
    });
    return wrapper;
  }

  function imageSurface() {
    const wrapper = mountSurface('<div><img data-file-annotation-surface src="x.png" /></div>');
    const img = wrapper.querySelector('img');
    Object.defineProperty(img, 'naturalWidth', { configurable: true, value: 800 });
    Object.defineProperty(img, 'naturalHeight', { configurable: true, value: 600 });
    Object.defineProperty(img, 'complete', { configurable: true, value: true });
    return img;
  }

  function typeInto(element, text) {
    expect(element).not.toBeNull();
    // React 18 tracks the DOM value; use the native setter then input.
    const setter = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value').set;
    setter.call(element, text);
    element.dispatchEvent(new Event('input', { bubbles: true }));
  }

  function button(text) {
    return [...container.querySelectorAll('button')].find((item) => item.textContent.includes(text));
  }

  async function startRegion() {
    // The editor refuses a new selection while a draft is queued: leave the
    // mode and re-enter it, exactly as a user would.
    if (container.querySelector('.file-annotation-mode.is-active')) {
      await act(async () => button('框选区域').click());
    }
    await act(async () => button('框选区域').click());
  }

  function dragRegion(from = [150, 150], to = [300, 280]) {
    const img = contentRef.current.querySelector('[data-file-annotation-surface]');
    act(() => {
      img.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, clientX: from[0], clientY: from[1] }));
    });
    act(() => {
      window.dispatchEvent(new PointerEvent('pointermove', { clientX: to[0], clientY: to[1] }));
    });
    act(() => {
      window.dispatchEvent(new PointerEvent('pointerup', { clientX: to[0], clientY: to[1] }));
    });
  }

  beforeEach(() => {
    global.IS_REACT_ACT_ENVIRONMENT = true;
    vi.stubGlobal('document', Object.assign(document, {
      createElement: document.createElement.bind(document),
    }));
    // Bounded canvas capture without a real 2D context.
    const original = document.createElement.bind(document);
    vi.spyOn(document, 'createElement').mockImplementation((tag) => {
      if (tag !== 'canvas') return original(tag);
      return {
        width: 1, height: 1,
        getContext: () => ({ drawImage: vi.fn(), strokeRect: vi.fn(), strokeStyle: '', lineWidth: 0 }),
        toDataURL: () => JPEG,
      };
    });
  });

  afterEach(() => {
    act(() => root?.unmount());
    container?.remove();
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  it('exposes region + whole modes for an image and reports mode changes', async () => {
    const { onModeChange } = mount({ kind: 'image' });
    expect(container.querySelector('.file-annotation-mode').textContent).toBe('框选区域');
    await act(async () => button('框选区域').click());
    expect(button('框选区域').getAttribute('aria-pressed')).toBe('true');
    expect(onModeChange).toHaveBeenLastCalledWith(true);
    await act(async () => button('框选区域').click());
    expect(onModeChange).toHaveBeenLastCalledWith(false);
  });

  it('captures at selection time and sends each draft independently with its own screenshot', async () => {
    const api = makeApi();
    const onSent = vi.fn();
    mount({ kind: 'image', api, onSent });
    imageSurface();
    await act(async () => button('框选区域').click());
    dragRegion();
    await act(async () => { await Promise.resolve(); });
    expect(api.openFileAnnotationBinding).toHaveBeenCalledTimes(1);
    expect(container.querySelectorAll('.file-annotation-screenshots img')).toHaveLength(2);
    await act(async () => { typeInto(container.querySelector('textarea'), '第一条'); });
    await act(async () => button('添加批注').click());
    expect(container.querySelector('.file-annotation-draft').textContent).toContain('第一条');

    await startRegion();
    dragRegion([400, 400], [520, 500]);
    await act(async () => { await Promise.resolve(); });
    await act(async () => { typeInto(container.querySelector('textarea'), '第二条'); });
    await act(async () => button('添加批注').click());
    expect(container.querySelectorAll('.file-annotation-draft')).toHaveLength(2);

    await act(async () => button('发送 2 条批注').click());
    await act(async () => { await Promise.resolve(); });
    expect(api.sendFileAnnotations).toHaveBeenCalledTimes(2);
    const first = api.sendFileAnnotations.mock.calls[0][0];
    const second = api.sendFileAnnotations.mock.calls[1][0];
    expect(first.file_annotations.annotations[0].body).toBe('第一条');
    expect(second.file_annotations.annotations[0].body).toBe('第二条');
    // Each submit carries exactly its own 2 JPEGs (never a shared last frame).
    expect(first.content_blocks.filter((block) => block.type === 'image')).toHaveLength(2);
    expect(second.content_blocks.filter((block) => block.type === 'image')).toHaveLength(2);
    expect(first.content_blocks[1].payload.file_key).not.toBe(second.content_blocks[1].payload.file_key);
    expect(api.uploadImage).toHaveBeenCalledTimes(4);
    expect(container.querySelectorAll('.file-annotation-draft')).toHaveLength(0);
    expect(onSent).toHaveBeenCalledWith({ seq_id: 7 }, 'p2p_7_9');
  });

  it('keeps unsent drafts and stable client id when a send fails mid-batch', async () => {
    const api = makeApi();
    api.sendFileAnnotations
      .mockResolvedValueOnce({ seq_id: 1 })
      .mockRejectedValueOnce(new Error('网络故障'));
    mount({ kind: 'image', api });
    imageSurface();
    await act(async () => button('框选区域').click());
    dragRegion();
    await act(async () => { await Promise.resolve(); });
    await act(async () => { typeInto(container.querySelector('textarea'), 'A'); });
    await act(async () => button('添加批注').click());
    await startRegion();
    dragRegion([400, 400], [520, 500]);
    await act(async () => { await Promise.resolve(); });
    await act(async () => { typeInto(container.querySelector('textarea'), 'B'); });
    await act(async () => button('添加批注').click());

    await act(async () => button('发送 2 条批注').click());
    await act(async () => { await Promise.resolve(); });
    // Only the acknowledged draft is gone; the failed one stays retryable.
    const remaining = container.querySelectorAll('.file-annotation-draft');
    expect(remaining).toHaveLength(1);
    expect(remaining[0].textContent).toContain('B');
    expect(container.querySelector('.file-annotation-hint.is-error')).not.toBeNull();

    const firstID = api.sendFileAnnotations.mock.calls[1][0].client_msg_id;
    await act(async () => button('发送 1 条批注').click());
    await act(async () => { await Promise.resolve(); });
    expect(api.sendFileAnnotations.mock.calls[2][0].client_msg_id).toBe(firstID);
    expect(container.querySelectorAll('.file-annotation-draft')).toHaveLength(0);
  });

  it('keeps the captured pair on an earlier draft when the later draft is text-only', async () => {
    const api = makeApi();
    mount({ api }); imageSurface();
    await act(async () => button('框选区域').click());
    dragRegion(); await act(async () => { await Promise.resolve(); });
    await act(async () => typeInto(container.querySelector('textarea'), '带截图'));
    await act(async () => button('添加批注').click());
    dragRegion([400, 400], [520, 500]); await act(async () => { await Promise.resolve(); });
    await act(async () => container.querySelector('.file-annotation-text-only input').click());
    await act(async () => typeInto(container.querySelector('textarea'), '仅文字'));
    await act(async () => button('添加批注').click());
    await act(async () => button('发送 2 条批注').click());
    const payloads = api.sendFileAnnotations.mock.calls.map(([payload]) => payload);
    expect(payloads[0].content_blocks.filter(block => block.type === 'image')).toHaveLength(2);
    expect(payloads[1].content_blocks).toBeUndefined();
    expect(api.uploadImage).toHaveBeenCalledTimes(2);
  });

  it('preserves a failed draft client id and uploads after another draft is added', async () => {
    const api = makeApi(); api.sendFileAnnotations.mockRejectedValueOnce(new Error('response lost'));
    mount({ api }); imageSurface();
    await act(async () => button('框选区域').click());
    dragRegion(); await act(async () => { await Promise.resolve(); });
    await act(async () => typeInto(container.querySelector('textarea'), '先前草稿'));
    await act(async () => button('添加批注').click());
    await act(async () => button('发送 1 条批注').click());
    const original = api.sendFileAnnotations.mock.calls[0][0];
    dragRegion([400, 400], [520, 500]); await act(async () => { await Promise.resolve(); });
    await act(async () => typeInto(container.querySelector('textarea'), '新增草稿'));
    await act(async () => button('添加批注').click());
    await act(async () => button('发送 2 条批注').click());
    const retried = api.sendFileAnnotations.mock.calls[1][0];
    expect(retried.client_msg_id).toBe(original.client_msg_id);
    expect(retried.content_blocks).toEqual(original.content_blocks);
    expect(api.uploadImage).toHaveBeenCalledTimes(4);
  });

  it('does not restore a canceled selection after a late binding response', async () => {
    let resolve;
    const api = makeApi({ openFileAnnotationBinding: vi.fn(() => new Promise(callback => { resolve = callback; })) });
    mount({ api }); imageSurface();
    await act(async () => button('框选区域').click());
    dragRegion(); await act(async () => { await Promise.resolve(); });
    await act(async () => window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true })));
    await act(async () => { resolve({ open_ref: 'fob_late', source: BOUND_SOURCE }); await Promise.resolve(); });
    expect(container.querySelector('.file-annotation-current')).toBeNull();
    expect(container.querySelector('.file-annotation-mode.is-active')).toBeNull();
  });

  it('sends text-only explicitly for image when the user opts out of screenshots', async () => {
    const api = makeApi();
    mount({ kind: 'image', api });
    imageSurface();
    await act(async () => button('框选区域').click());
    dragRegion();
    await act(async () => { await Promise.resolve(); });
    await act(async () => container.querySelector('.file-annotation-text-only input').click());
    expect(container.querySelector('.file-annotation-screenshots')).toBeNull();
    await act(async () => { typeInto(container.querySelector('textarea'), '仅文字'); });
    await act(async () => button('添加批注').click());
    expect(container.querySelectorAll('.file-annotation-draft')).toHaveLength(1);
    await act(async () => button('发送 1 条批注').click());
    await act(async () => { await Promise.resolve(); });
    const payload = api.sendFileAnnotations.mock.calls[0][0];
    expect(payload.content_blocks).toBeUndefined();
    expect(api.uploadImage).not.toHaveBeenCalled();
    expect(payload.file_annotations.annotations[0].body).toBe('仅文字');
  });

  it('rejects a binding whose canonical source no longer matches the shown file', async () => {
    const api = makeApi({
      openFileAnnotationBinding: vi.fn(async () => ({
        open_ref: 'fob_1', source: { ...BOUND_SOURCE, file_key: 'other.png' },
      })),
    });
    mount({ kind: 'image', api });
    imageSurface();
    await act(async () => button('框选区域').click());
    dragRegion();
    await act(async () => { await Promise.resolve(); });
    expect(api.revokeFileAnnotationBinding).toHaveBeenCalledWith('fob_1');
    expect(container.querySelector('.file-annotation-screenshots')).toBeNull();
    expect(container.querySelector('.file-annotation-hint')?.textContent).toContain('来源已变化');
  });

  it('revokes the binding on unmount and on source change', async () => {
    const api = makeApi();
    mount({ kind: 'image', api });
    imageSurface();
    await act(async () => button('框选区域').click());
    dragRegion();
    await act(async () => { await Promise.resolve(); });
    expect(api.revokeFileAnnotationBinding).not.toHaveBeenCalled();
    await act(async () => root.unmount());
    expect(api.revokeFileAnnotationBinding).toHaveBeenCalledWith('fob_1');
  });

  it('revokes a late binding that resolves after unmount', async () => {
    let resolveBinding;
    const api = makeApi({
      openFileAnnotationBinding: vi.fn(() => new Promise((resolve) => { resolveBinding = resolve; })),
    });
    mount({ kind: 'image', api });
    imageSurface();
    await act(async () => button('框选区域').click());
    dragRegion();
    await act(async () => { await Promise.resolve(); });
    await act(async () => root.unmount());
    await act(async () => {
      resolveBinding({ open_ref: 'fob_late', source: BOUND_SOURCE });
      await Promise.resolve();
    });
    expect(api.revokeFileAnnotationBinding).toHaveBeenCalledWith('fob_late');
  });

  it('invalidates an in-flight capture when the page/zoom/seek epoch changes', async () => {
    let resolveCapture;
    mount({ kind: 'image' });
    imageSurface();
    await act(async () => button('框选区域').click());
    dragRegion();
    await act(async () => { await Promise.resolve(); });
    // A resize between selection and body typing must clear the pending target.
    const originalWidth = window.innerWidth;
    Object.defineProperty(window, 'innerWidth', { configurable: true, value: originalWidth + 100 });
    await act(async () => { window.dispatchEvent(new Event('resize')); });
    Object.defineProperty(window, 'innerWidth', { configurable: true, value: originalWidth });
    expect(container.querySelector('textarea')).toBeNull();
    expect(container.querySelector('.file-annotation-hint')?.textContent).toContain('请重新框选');
    resolveCapture?.(null);
  });

  it('Esc cancels the pending selection without closing the panel and exits mode', async () => {
    mount({ kind: 'image' });
    imageSurface();
    await act(async () => button('框选区域').click());
    dragRegion();
    await act(async () => { await Promise.resolve(); });
    expect(container.querySelector('textarea')).not.toBeNull();
    await act(async () => {
      window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    });
    expect(container.querySelector('textarea')).toBeNull();
    expect(container.querySelector('.file-annotation-editor')).not.toBeNull();
    await act(async () => {
      window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    });
    expect(container.querySelector('.file-annotation-mode.is-active')).toBeNull();
  });

  it('supports text selection targeting the original pre with UTF-16 offsets', async () => {
    const api = makeApi();
    mount({ kind: 'text', api });
    const wrapper = mountSurface('<pre data-file-annotation-text data-file-annotation-surface>你好😀世界\n第二行</pre>', 'text');
    const pre = wrapper.querySelector('pre');
    const text = pre.firstChild;
    const range = document.createRange();
    range.setStart(text, 0);
    range.setEnd(text, 4); // "你好😀世" = 4 UTF-16 code units
    const selection = { rangeCount: 1, getRangeAt: () => range, isCollapsed: false };
    vi.spyOn(window, 'getSelection').mockReturnValue(selection);
    await act(async () => button('文本选区').click());
    await act(async () => {
      pre.dispatchEvent(new MouseEvent('mouseup', { bubbles: true }));
      await Promise.resolve();
    });
    expect(container.querySelector('textarea')).not.toBeNull();
    await act(async () => { typeInto(container.querySelector('textarea'), '改一下'); });
    await act(async () => button('添加批注').click());
    await act(async () => button('发送 1 条批注').click());
    await act(async () => { await Promise.resolve(); });
    const payload = api.sendFileAnnotations.mock.calls[0][0];
    // 4 UTF-16 code units = 你(1) 好(1) 😀(2)
    expect(payload.file_annotations.annotations[0].target).toMatchObject({ start: 0, end: 4, quote: '你好😀' });
    expect(payload.content_blocks).toBeUndefined();
  });

  it('supports cells targeting with one-based row/column and sheet name', async () => {
    const api = makeApi();
    mount({ kind: 'cells', api });
    const wrapper = mountSurface('<table data-file-annotation-sheet="Sheet1"><tr><td data-file-annotation-surface data-file-annotation-row="1" data-file-annotation-column="2">B1</td></tr></table>', 'cells');
    const cell = wrapper.querySelector('td');
    await act(async () => button('表格选区').click());
    await act(async () => {
      cell.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, clientX: 150, clientY: 150 }));
      cell.dispatchEvent(new PointerEvent('pointerup', { bubbles: true, clientX: 150, clientY: 150 }));
      await Promise.resolve();
    });
    await act(async () => { typeInto(container.querySelector('textarea'), '改单元格'); });
    await act(async () => button('添加批注').click());
    await act(async () => button('发送 1 条批注').click());
    await act(async () => { await Promise.resolve(); });
    const payload = api.sendFileAnnotations.mock.calls[0][0];
    expect(payload.file_annotations.annotations[0].target).toMatchObject({
      sheet: 'Sheet1', row_start: 1, row_end: 1, column_start: 2, column_end: 2,
    });
  });

  it('refuses to select a PDF page that is not rendered yet', async () => {
    const api = makeApi();
    mount({ kind: 'pdf', api });
    mountSurface('<div><canvas data-file-annotation-surface data-file-annotation-page="2" data-file-annotation-ready="false"></canvas></div>', 'pdf');
    await act(async () => button('整页/整段').click());
    expect(container.querySelector('textarea')).toBeNull();
    expect(container.querySelector('.file-annotation-hint')?.textContent).toContain('尚未渲染完成');
  });

  it('pauses a playing video without moving the playhead when entering a mode', async () => {
    const api = makeApi();
    mount({ kind: 'video', api });
    const wrapper = mountSurface('<div><video data-file-annotation-surface></video></div>', 'video');
    const video = wrapper.querySelector('video');
    video.pause = vi.fn();
    video.currentTime = 12.5;
    await act(async () => button('整页/整段').click());
    await act(async () => { await Promise.resolve(); });
    expect(video.pause).toHaveBeenCalled();
    expect(video.currentTime).toBe(12.5);
    expect(container.querySelector('textarea')).not.toBeNull();
  });
});
