import { describe, expect, it, vi } from 'vitest';
import {
  FILE_ANNOTATIONS_CONTRACT,
  FILE_ANNOTATION_MAX_ANNOTATIONS,
  captureFileAnnotationScreenshot,
  captureFileAnnotationScreenshots,
  cellSelectionTarget,
  canonicalFileResourceURL,
  fileAnnotationCommentPreview,
  fileAnnotationDraftKey,
  fileAnnotationHasScreenshot,
  fileAnnotationOwnedBlocks,
  fileAnnotationScreenshotFiles,
  fileAnnotationSourceMatches,
  fileAnnotationSourceMatchesFile,
  fileAnnotationTargetSummary,
  mediaContentBox,
  normalizeFileAnnotationRect,
  normalizedRectFromBox,
  normalizedSelection,
  normalizeFileAnnotations,
  normalizeFileAnnotationSource,
  normalizeFileAnnotationTarget,
  textSelectionTarget,
  validateFileAnnotation,
} from './file-annotations';

const SOURCE = {
  topic_id: 'p2p_7_9',
  message_id: 123,
  attachment_index: 0,
  name: 'image.png',
  url: '/uploads/images/key.png',
  file_key: 'key.png',
  mime_type: 'image/png',
  type: 'image',
  size: 123,
  version: 'a'.repeat(64),
};

function docFixture(overrides = {}) {
  return {
    contract_version: FILE_ANNOTATIONS_CONTRACT,
    source: SOURCE,
    annotations: [{
      id: 'fa_1', kind: 'image', body: '改成蓝色',
      target: { rect: { x: 0.2, y: 0.3, width: 0.1, height: 0.08 } },
    }],
    ...overrides,
  };
}

describe('file annotation source', () => {
  it('accepts optional file_key/mime_type and absolute CDN urls', () => {
    const minimal = normalizeFileAnnotationSource({ ...SOURCE, file_key: '', mime_type: '' });
    expect(minimal).not.toBeNull();
    expect(minimal.file_key).toBe('');
    expect(minimal.mime_type).toBe('');
    const absent = normalizeFileAnnotationSource({ ...SOURCE });
    expect(absent).not.toBeNull();
    const cdn = normalizeFileAnnotationSource({ ...SOURCE, url: 'https://media.catsco.cc/uploads/images/key.png' });
    expect(cdn).not.toBeNull();
  });
  it('rejects foreign urls, non-hex and short/long versions', () => {
    expect(normalizeFileAnnotationSource({ ...SOURCE, url: 'https://evil.test/x.png' })).toBeNull();
    expect(normalizeFileAnnotationSource({ ...SOURCE, version: 'abc' })).toBeNull();
    expect(normalizeFileAnnotationSource({ ...SOURCE, version: 'a'.repeat(63) })).toBeNull();
    expect(normalizeFileAnnotationSource({ ...SOURCE, version: 'a'.repeat(65) })).toBeNull();
    expect(normalizeFileAnnotationSource({ ...SOURCE, version: ` ${'a'.repeat(64)}` })).toBeNull();
  });
  it('normalizes version to lowercase hex', () => {
    expect(normalizeFileAnnotationSource({ ...SOURCE, version: 'A'.repeat(64) }).version).toBe('a'.repeat(64));
  });
  it('retains optional width/height when provided', () => {
    const sized = normalizeFileAnnotationSource({ ...SOURCE, width: 800, height: 600 });
    expect(sized).toMatchObject({ width: 800, height: 600 });
  });
  it('compares canonical source with url-canonical and optional-field tolerance', () => {
    expect(fileAnnotationSourceMatches(SOURCE, { ...SOURCE, url: 'https://media.catsco.cc/uploads/images/key.png', file_key: '', mime_type: '' })).toBe(true);
    expect(fileAnnotationSourceMatches(SOURCE, { ...SOURCE, version: 'b'.repeat(64) })).toBe(false);
    expect(fileAnnotationSourceMatchesFile(SOURCE, { name: 'image.png', url: 'https://media.catsco.cc/uploads/images/key.png', file_key: '', mime_type: '', type: 'image', size: 123 })).toBe(true);
    expect(fileAnnotationSourceMatchesFile(SOURCE, { name: 'other.png', url: '/uploads/images/key.png', file_key: 'key.png', mime_type: 'image/png', type: 'image', size: 123 })).toBe(false);
  });
  it('canonicalizes resource urls across hosts and query strings', () => {
    expect(canonicalFileResourceURL('https://media.catsco.cc/uploads/a.png?v=2', 'https://app.test')).toBe('/uploads/a.png?v=2');
    expect(canonicalFileResourceURL('/uploads/a.png', 'https://app.test')).toBe('/uploads/a.png');
  });
});

describe('file annotation validation', () => {
  it('accepts a canonical document and rejects wrong contract/source', () => {
    expect(normalizeFileAnnotations(docFixture())).not.toBeNull();
    expect(normalizeFileAnnotations(docFixture({ contract_version: 'other' }))).toBeNull();
    expect(normalizeFileAnnotations(docFixture({ source: { ...SOURCE, version: '' } }))).toBeNull();
    expect(normalizeFileAnnotations(docFixture({ source: { ...SOURCE, url: 'https://evil.test/x.png' } }))).toBeNull();
    expect(normalizeFileAnnotations(docFixture({ source: { ...SOURCE, message_id: 0 } }))).toBeNull();
  });

  it('validates every kind target with finite bounds', () => {
    expect(normalizeFileAnnotationTarget('image', { rect: { x: 0, y: 0, width: 1, height: 1 } })).not.toBeNull();
    expect(normalizeFileAnnotationTarget('image', { rect: { x: 0.9, y: 0, width: 0.2, height: 1 } })).toBeNull();
    expect(normalizeFileAnnotationTarget('video', { rect: { x: 0, y: 0, width: 1, height: 1 }, time_seconds: 12.34 })).not.toBeNull();
    expect(normalizeFileAnnotationTarget('video', { rect: { x: 0, y: 0, width: 1, height: 1 }, time_seconds: -1 })).toBeNull();
    expect(normalizeFileAnnotationTarget('pdf', { rect: { x: 0, y: 0, width: 1, height: 1 }, page: 2 })).not.toBeNull();
    expect(normalizeFileAnnotationTarget('pdf', { rect: { x: 0, y: 0, width: 1, height: 1 }, page: 0 })).toBeNull();
    expect(normalizeFileAnnotationTarget('text', { start: 10, end: 25, quote: 'x', line_start: 2, line_end: 3 })).not.toBeNull();
    expect(normalizeFileAnnotationTarget('text', { start: 25, end: 10, quote: 'x', line_start: 2, line_end: 3 })).toBeNull();
    expect(normalizeFileAnnotationTarget('cells', { sheet: 'Sheet1', row_start: 1, row_end: 2, column_start: 1, column_end: 3, quote: 'v' })).not.toBeNull();
    expect(normalizeFileAnnotationTarget('cells', { sheet: 'Sheet1', row_start: 201, row_end: 2, column_start: 1, column_end: 3, quote: 'v' })).toBeNull();
    expect(normalizeFileAnnotationTarget('cells', { sheet: 'Sheet1', row_start: 1, row_end: 2, column_start: 51, column_end: 3, quote: 'v' })).toBeNull();
  });

  it('rejects empty bodies, duplicate ids, unknown kinds and oversized sets', () => {
    expect(normalizeFileAnnotations(docFixture({ annotations: [{ id: 'a', kind: 'image', body: ' ', target: { rect: { x: 0, y: 0, width: 1, height: 1 } } }] }))).toBeNull();
    const row = { id: 'a', kind: 'image', body: 'x', target: { rect: { x: 0, y: 0, width: 1, height: 1 } } };
    expect(normalizeFileAnnotations(docFixture({ annotations: [row, row] }))).toBeNull();
    expect(normalizeFileAnnotations(docFixture({ annotations: [{ ...row, kind: 'audio' }] }))).toBeNull();
    expect(normalizeFileAnnotations(docFixture({ annotations: Array.from({ length: FILE_ANNOTATION_MAX_ANNOTATIONS + 1 }, (_, i) => ({ ...row, id: `a${i}` })) }))).toBeNull();
  });

  it('summarizes targets and declares screenshot availability honestly', () => {
    expect(fileAnnotationTargetSummary('text', { start: 1, end: 2, quote: 'hello world', line_start: 1, line_end: 1 })).toBe('hello world');
    expect(fileAnnotationTargetSummary('cells', { sheet: 'S', row_start: 1, row_end: 2, column_start: 1, column_end: 3 })).toBe('S 1:1–2:3');
    expect(fileAnnotationTargetSummary('video', { rect: { x: 0.1, y: 0.2, width: 0.3, height: 0.4 }, time_seconds: 1.5 })).toContain('@ 1.50s');
    expect(fileAnnotationHasScreenshot('text')).toBe(false);
    expect(fileAnnotationHasScreenshot('cells')).toBe(false);
    expect(fileAnnotationHasScreenshot('image')).toBe(true);
  });

  it('normalizes rects to actual pixels and rejects zero-area', () => {
    expect(normalizeFileAnnotationRect({ x: 0, y: 0, width: 0.5, height: 0.5 })).toEqual({ x: 0, y: 0, width: 0.5, height: 0.5 });
    expect(normalizeFileAnnotationRect({ x: 0, y: 0, width: 0, height: 0.5 })).toBeNull();
    expect(normalizeFileAnnotationRect({ x: 0, y: 0, width: 1.5, height: 0.5 })).toBeNull();
    expect(normalizeFileAnnotationRect(null)).toBeNull();
  });

  it('exposes the normalized-rect alias used by the editor', () => {
    expect(normalizeFileAnnotationRect({ x: 0, y: 0, width: 0.25, height: 0.5 })).toEqual({ x: 0, y: 0, width: 0.25, height: 0.5 });
  });

  it('previews comments without truncation surprises', () => {
    expect(fileAnnotationCommentPreview('hello\nworld')).toBe('hello world');
    expect(fileAnnotationCommentPreview(`${'a'.repeat(200)}`)).toBe(`${'a'.repeat(120)}…`);
  });

  it('stabilizes draft keys for change detection', () => {
    expect(fileAnnotationDraftKey([{ id: 'fa_1', kind: 'text', body: 'x', target: { start: 0, end: 1 } }]))
      .toBe('[["fa_1","text","x",{"start":0,"end":1}]]');
  });
});

describe('text offsets (UTF-16)', () => {
  // Real DOM Range: jsdom implements Range, so offsets are measured the same
  // way the browser measures them (UTF-16 code units).
  function preWith(text) {
    const pre = document.createElement('pre');
    pre.textContent = text;
    return pre;
  }
  function selectRange(root, start, end) {
    const range = document.createRange();
    range.setStart(root.firstChild, start);
    range.setEnd(root.firstChild, end);
    return { rangeCount: 1, getRangeAt: () => range };
  }

  it('uses UTF-16 code units so emoji and CJK match the server', () => {
    const text = 'export const greeting = "你好🙂";\nconst other = 1;';
    const pre = preWith(text);
    const target = textSelectionTarget(pre, selectRange(pre, 25, 29));
    expect(target).toEqual({ start: 25, end: 29, quote: '你好🙂', line_start: 1, line_end: 1 });
  });
  it('measures a tail range to the end of the text', () => {
    const text = 'export const greeting = "你好🙂";\nconst other = 1;';
    const pre = preWith(text);
    const target = textSelectionTarget(pre, selectRange(pre, 30, text.length));
    expect(target).toEqual({ start: 30, end: text.length, quote: text.slice(30), line_start: 1, line_end: 2 });
  });
  it('computes line numbers from LF in UTF-16 offsets', () => {
    const pre = preWith('aa\nbb\ncc');
    const target = textSelectionTarget(pre, selectRange(pre, 3, 5));
    expect(target.quote).toBe('bb');
    expect(target.line_start).toBe(2);
    expect(target.line_end).toBe(2);
  });
  it('rejects collapsed and foreign selections', () => {
    const pre = preWith('abc');
    expect(() => textSelectionTarget(pre, selectRange(pre, 2, 2))).toThrow(/empty/);
    const foreign = preWith('abc');
    const range = document.createRange();
    range.setStart(foreign.firstChild, 0);
    range.setEnd(foreign.firstChild, 1);
    expect(() => textSelectionTarget(pre, { rangeCount: 1, getRangeAt: () => range })).toThrow(/outside/);
  });
  it('normalizes raw selection bounds strictly', () => {
    expect(normalizedSelection(25, 10, 30)).toEqual({ start: 10, end: 25 });
    expect(() => normalizedSelection(-1, 5, 10)).toThrow(/out of bounds/);
    expect(() => normalizedSelection(4, 4, 10)).toThrow(/empty/);
    expect(() => normalizedSelection(1.5, 4, 10)).toThrow(/integers/);
  });
});

describe('spreadsheet cells', () => {
  function td(row, column, text) {
    return { textContent: text, dataset: { fileAnnotationRow: String(row), fileAnnotationColumn: String(column) } };
  }
  it('normalizes a drag into an ordered one-based range', () => {
    const root = { dataset: { fileAnnotationSheet: 'Sheet1' } };
    const target = cellSelectionTarget(td(3, 4, 'd'), td(1, 2, 'b'), root);
    expect(target).toMatchObject({ sheet: 'Sheet1', row_start: 1, row_end: 3, column_start: 2, column_end: 4 });
    expect(target.quote).toContain('b');
  });
  it('rejects missing sheet and out-of-grid cells', () => {
    expect(() => cellSelectionTarget(td(1, 1, 'a'), td(1, 1, 'a'), { dataset: {} })).toThrow(/sheet/);
    expect(() => cellSelectionTarget(td(0, 1, 'a'), td(1, 1, 'a'), { dataset: { fileAnnotationSheet: 'S' } })).toThrow(/outside/);
    expect(() => cellSelectionTarget(td(1, 1, 'a'), td(201, 1, 'a'), { dataset: { fileAnnotationSheet: 'S' } })).toThrow(/exceeds/);
    expect(() => cellSelectionTarget(td(1, 1, 'a'), td(1, 51, 'a'), { dataset: { fileAnnotationSheet: 'S' } })).toThrow(/exceeds/);
  });
});

describe('media content box', () => {
  it('de-letters an object-fit: contain image', () => {
    const element = { tagName: 'IMG', naturalWidth: 800, naturalHeight: 400, complete: true,
      getBoundingClientRect: () => ({ left: 0, top: 0, width: 400, height: 400, right: 400, bottom: 400 }) };
    expect(mediaContentBox(element)).toMatchObject({ x: 0, y: 100, width: 400, height: 200, bitmapWidth: 800, bitmapHeight: 400 });
  });
  it('uses the element box for canvas and rejects unloaded media', () => {
    const canvas = { tagName: 'CANVAS', width: 800, height: 400,
      getBoundingClientRect: () => ({ left: 0, top: 0, width: 400, height: 200, right: 400, bottom: 200 }) };
    expect(mediaContentBox(canvas)).toMatchObject({ width: 400, height: 200, bitmapWidth: 800, bitmapHeight: 400 });
    const img = { tagName: 'IMG', naturalWidth: 0, naturalHeight: 0, complete: false,
      getBoundingClientRect: () => ({ left: 0, top: 0, width: 10, height: 10, right: 10, bottom: 10 }) };
    expect(mediaContentBox(img)).toBeNull();
    expect(mediaContentBox(null)).toBeNull();
  });
  it('intersects a CSS rect with the content box instead of clamping', () => {
    const box = { x: 100, y: 50, width: 400, height: 200, bitmapWidth: 800, bitmapHeight: 400 };
    expect(normalizedRectFromBox({ x: 100, y: 50, width: 200, height: 100 }, box)).toEqual({ x: 0, y: 0, width: 0.5, height: 0.5 });
    // Partially outside: clipped to the box, not clamped into a fake rect.
    const clipped = normalizedRectFromBox({ x: 350, y: 150, width: 200, height: 100 }, box);
    expect(clipped).toEqual({ x: 0.625, y: 0.5, width: 0.375, height: 0.5 });
    // Fully outside: no intersection, no target.
    expect(normalizedRectFromBox({ x: 600, y: 400, width: 50, height: 50 }, box)).toBeNull();
    expect(() => normalizedRectFromBox({ x: 0, y: 0, width: 10, height: 10 }, null)).toThrow(/content box/);
  });
});

describe('file annotation screenshot capture', () => {
  afterEach(() => vi.unstubAllGlobals());

  function mockCanvas(width, height) {
    const context = { drawImage: vi.fn(), strokeStyle: '', lineWidth: 0, strokeRect: vi.fn() };
    return { width, height, getContext: vi.fn(() => context),
      toDataURL: vi.fn((mime, quality) => `data:${mime};base64,${'A'.repeat(64)}`) };
  }

  function stubCanvasFactory() {
    const createElement = vi.fn((tag) => (tag === 'canvas' ? mockCanvas(1, 1) : null));
    vi.stubGlobal('document', { ...document, createElement });
    return createElement;
  }

  function fakeCanvas(width = 800, height = 600) {
    return mockCanvas(width, height);
  }

  it('derives full and padded crop from one bitmap and reports failures', () => {
    stubCanvasFactory();
    const source = fakeCanvas();
    const result = captureFileAnnotationScreenshot({
      canvas: source,
      rect: { x: 0.2, y: 0.3, width: 0.1, height: 0.08 },
    });
    expect(result.error).toBeUndefined();
    expect(result.images).toHaveLength(2);
    expect(result.images[0]).toMatchObject({ role: 'full', width: 800, height: 600 });
    expect(result.images[1]).toMatchObject({ role: 'crop', width: 112, height: 80 });
    const broken = { width: 10, height: 10, getContext: () => null };
    expect(captureFileAnnotationScreenshot({ canvas: broken, rect: { x: 0, y: 0, width: 1, height: 1 } })).toEqual({ error: 'canvas-unavailable' });
    vi.unstubAllGlobals();
    expect(captureFileAnnotationScreenshot({ canvas: null, rect: { x: 0, y: 0, width: 1, height: 1 } })).toEqual({ error: 'canvas-unavailable' });
  });

  it('bounds large sources to 2048 instead of allocating full-pixel canvases', () => {
    stubCanvasFactory();
    const result = captureFileAnnotationScreenshot({
      canvas: fakeCanvas(4000, 3000),
      rect: { x: 0, y: 0, width: 1, height: 1 },
    });
    expect(result.images[0].width).toBe(2048);
    expect(result.images[0].height).toBe(1536);
  });

  it('converts captured images to owned upload blocks', () => {
    stubCanvasFactory();
    const result = captureFileAnnotationScreenshot({
      canvas: fakeCanvas(),
      rect: { x: 0.2, y: 0.3, width: 0.1, height: 0.08 },
    });
    const files = fileAnnotationScreenshotFiles(result);
    expect(files).toHaveLength(2);
    expect(files[0].name).toBe('gateway-full.jpg');
    const uploads = files.map((file) => ({ file_key: file.name, url: `/uploads/images/${file.name}`, size: file.size, mime_type: 'image/jpeg' }));
    const blocks = fileAnnotationOwnedBlocks(uploads, result.images);
    expect(blocks).toHaveLength(2);
    expect(blocks[0].payload.screenshot_role).toBe('full');
    expect(blocks[1].payload.screenshot_role).toBe('crop');
    expect(fileAnnotationScreenshotFiles({})).toEqual([]);
    expect(fileAnnotationOwnedBlocks(null, null)).toEqual([]);
  });
});

describe('direct media capture (image/video/canvas element)', () => {
  function element({ tag = 'IMG', naturalWidth = 800, naturalHeight = 400, readyState = 4, complete = true,
    currentTime = 0, width = 800, height = 400, rect = { left: 0, top: 0, width: 400, height: 200 } } = {}) {
    const el = { tagName: tag, naturalWidth, naturalHeight, readyState, complete, currentTime, width, height,
      getBoundingClientRect: () => ({ ...rect, right: rect.left + rect.width, bottom: rect.top + rect.height }) };
    return el;
  }

  function withDocument(run) {
    const created = [];
    const original = global.document;
    global.document = { createElement: (tag) => {
      const canvas = { width: 0, height: 0, getContext: () => ({ drawImage: () => {}, strokeStyle: '', lineWidth: 0, strokeRect: () => {} }),
        toDataURL: (mime) => `data:${mime};base64,${'A'.repeat(64)}` };
      created.push(canvas);
      return canvas;
    } };
    try { return run(created); } finally { global.document = original; }
  }

  it('captures an image element directly into a bounded canvas', () => {
    withDocument(() => {
      const result = captureFileAnnotationScreenshots(element(), { rect: { x: 0.1, y: 0.1, width: 0.2, height: 0.2 } });
      expect(result.screenshots.map((entry) => entry.role)).toEqual(['full', 'crop']);
      expect(result.screenshots[0]).toMatchObject({ mime_type: 'image/jpeg', width: 800, height: 400 });
      expect(result.screenshots[1].width).toBe(224);
      expect(result.screenshots[1].height).toBe(144);
    });
  });

  it('draws a canvas element (PDF page) at its own resolution', () => {
    withDocument(() => {
      const pdf = element({ tag: 'CANVAS', naturalWidth: 0, naturalHeight: 0, width: 1200, height: 900,
        rect: { left: 0, top: 0, width: 600, height: 450 } });
      const result = captureFileAnnotationScreenshots(pdf, { rect: { x: 0, y: 0, width: 1, height: 1 } });
      expect(result.screenshots[0]).toMatchObject({ width: 1200, height: 900 });
    });
  });

  it('respects AbortSignal and fails explicitly for tainted/unready media', () => {
    withDocument(() => {
      const aborted = { aborted: true };
      expect(() => captureFileAnnotationScreenshots(element(), { rect: { x: 0, y: 0, width: 1, height: 1 } }, { signal: aborted })).toThrow(/aborted/);
      const original = global.document;
      global.document = { createElement: () => ({ width: 0, height: 0,
        getContext: () => ({ drawImage: () => { const e = new Error('SecurityError'); e.name = 'SecurityError'; throw e; } }),
        toDataURL: () => '' }) };
      expect(() => captureFileAnnotationScreenshots(element(), { rect: { x: 0, y: 0, width: 1, height: 1 } })).toThrow(/cross-origin/);
      global.document = original;
      expect(() => captureFileAnnotationScreenshots(element({ complete: false, naturalWidth: 0 }), { rect: { x: 0, y: 0, width: 1, height: 1 } })).toThrow(/not loaded/);
      expect(() => captureFileAnnotationScreenshots(element({ readyState: 0, tag: 'VIDEO' }), { rect: { x: 0, y: 0, width: 1, height: 1 } })).toThrow(/not ready/);
      expect(() => captureFileAnnotationScreenshots(element(), {})).toThrow(/rect/);
    });
  });

  it('requires the video to be paused at the annotated time', () => {
    withDocument(() => {
      const video = element({ tag: 'VIDEO', currentTime: 1.5, readyState: 4 });
      const result = captureFileAnnotationScreenshots(video, { rect: { x: 0, y: 0, width: 1, height: 1 }, time_seconds: 1.5 });
      expect(result.screenshots).toHaveLength(2);
      expect(() => captureFileAnnotationScreenshots(video, { rect: { x: 0, y: 0, width: 1, height: 1 }, time_seconds: 2.5 }))
        .toThrow(/not at the annotated time/);
    });
  });
});

describe('validateFileAnnotation', () => {
  const base = { id: 'fa_1', kind: 'image', body: 'look here' };
  it('accepts a valid image annotation and returns it unchanged', () => {
    const annotation = { ...base, target: { rect: { x: 0, y: 0, width: 0.5, height: 0.5 } } };
    expect(validateFileAnnotation(annotation)).toBe(annotation);
  });
  it('validates every kind target strictly', () => {
    const cases = [
      [{ kind: 'video', target: { rect: { x: 0, y: 0, width: 1, height: 1 }, time_seconds: 1.5 } }, null],
      [{ kind: 'video', target: { rect: { x: 0, y: 0, width: 1, height: 1 }, time_seconds: -1 } }, /time_seconds/],
      [{ kind: 'pdf', target: { rect: { x: 0, y: 0, width: 1, height: 1 }, page: 2 } }, null],
      [{ kind: 'pdf', target: { rect: { x: 0, y: 0, width: 1, height: 1 }, page: 0 } }, /page/],
      [{ kind: 'text', target: { start: 0, end: 3, quote: 'abc', line_start: 1, line_end: 1 } }, null],
      [{ kind: 'text', target: { start: 3, end: 3, quote: '', line_start: 1, line_end: 1 } }, /offsets/],
      [{ kind: 'cells', target: { sheet: 'S', row_start: 1, row_end: 2, column_start: 1, column_end: 3, quote: 'a' } }, null],
      [{ kind: 'cells', target: { sheet: 'S', row_start: 2, row_end: 1, column_start: 1, column_end: 3, quote: 'a' } }, /inverted/],
      [{ kind: 'cells', target: { sheet: 'S', row_start: 1, row_end: 201, column_start: 1, column_end: 3, quote: 'a' } }, /row/],
    ];
    for (const [partial, error] of cases) {
      const annotation = { ...base, ...partial };
      if (error) expect(() => validateFileAnnotation(annotation)).toThrow(error);
      else expect(validateFileAnnotation(annotation)).toBe(annotation);
    }
  });
  it('rejects bad ids, bodies, kinds and rects', () => {
    const rect = { x: 0, y: 0, width: 1, height: 1 };
    expect(() => validateFileAnnotation({ ...base, id: 'nope', target: { rect } })).toThrow(/id/);
    expect(() => validateFileAnnotation({ ...base, body: '  ', target: { rect } })).toThrow(/empty/);
    expect(() => validateFileAnnotation({ ...base, kind: 'nope', target: { rect } })).toThrow(/kind/);
    expect(() => validateFileAnnotation({ ...base, target: { rect: { x: 0, y: 0, width: 2, height: 1 } } })).toThrow(/rect/);
    expect(() => validateFileAnnotation(null)).toThrow(/object/);
  });
});
