// File preview annotation contract helpers (see /tmp/catsco-file-annotations-brief.md).
//
// Pure validation + canvas capture for ordinary file previews (image/video/
// PDF/text/cells). No gateway dependency, no server authority: the canonical
// source always comes from the parent's binding response, and these helpers
// only normalize what the user selected.
//
// Text offsets are UTF-16 code units (the native JS string index), matching
// the Go server comparison. Do NOT convert to code points or bytes.

import { screenshotFile, ownedScreenshotBlock } from './gateway-annotation-screenshots';

export const FILE_ANNOTATIONS_CONTRACT = 'catsco.file-annotations.v1';
export const FILE_OPEN_BINDING_CONTRACT = 'catsco.file-open-binding.v1';
export const FILE_ANNOTATION_KINDS = ['image', 'video', 'pdf', 'text', 'cells'];

export const FILE_ANNOTATION_MAX_ANNOTATIONS = 20;
export const FILE_ANNOTATION_MAX_BODY = 2000;
export const FILE_ANNOTATION_MAX_ID = 128;
export const FILE_ANNOTATION_MAX_QUOTE = 4096;
export const FILE_ANNOTATION_MAX_SHEET = 128;
export const FILE_ANNOTATION_MAX_ROW = 200;
export const FILE_ANNOTATION_MAX_COL = 50;
export const FILE_ANNOTATION_MAX_BYTES = 16 * 1024;
export const FILE_ANNOTATION_CROP_PADDING = 16;
export const FILE_ANNOTATION_MAX_DIMENSION = 2048;
export const FILE_ANNOTATION_JPEG_QUALITY = 0.85;
// The binding version is the server SHA-256 over the canonical descriptor:
// exactly 64 hex characters, nothing else.
export const FILE_ANNOTATION_VERSION_PATTERN = /^[0-9a-fA-F]{64}$/;

const CONTROL_FORBIDDEN = /[\u0000-\u001f\u007f]/;

function fail(code, message) {
  const error = new Error(message);
  error.code = code;
  return error;
}

function plainObject(value) {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return false;
  const prototype = Object.getPrototypeOf(value);
  return prototype === Object.prototype || prototype === null;
}

function boundedString(value, maxLength, allowEmpty = false) {
  if (typeof value !== 'string') return null;
  if (value.length > maxLength) return null;
  if (!allowEmpty && value.length === 0) return null;
  return value;
}

function optionalString(value, maxLength) {
  // Optional canonical field: absent, null or empty string all normalize to ''.
  if (value === undefined || value === null || value === '') return '';
  return boundedString(value, maxLength, true);
}

function finiteNumber(value) {
  return typeof value === 'number' && Number.isFinite(value) ? value : null;
}

function positiveInteger(value, max) {
  const n = finiteNumber(value);
  return n !== null && Number.isInteger(n) && n >= 1 && (max === undefined || n <= max) ? n : null;
}

function nonNegativeInteger(value) {
  const n = finiteNumber(value);
  return n !== null && Number.isInteger(n) && n >= 0 ? n : null;
}

function isTaintError(error) {
  return Boolean(error) && /security|taint/i.test(`${error.name || ''} ${error.message || ''}`);
}

// ---------------------------------------------------------------------------
// Source descriptor
// ---------------------------------------------------------------------------

// Stored attachment URLs may be relative (/uploads/...) or absolute on the
// platform media host / CDN. Only http(s) and relative paths are accepted.
export function canonicalFileResourceURL(url, base = typeof window !== 'undefined' ? window.location.origin : '') {
  const raw = String(url || '').trim();
  if (!raw) return '';
  try {
    const resolved = new URL(raw, base || undefined);
    return `${resolved.pathname}${resolved.search}`.replace(/\/+$/, '') || '/';
  } catch {
    return raw.split('?')[0].replace(/\/+$/, '');
  }
}

function validResourceURL(url) {
  if (typeof url !== 'string' || !url) return false;
  if (url.startsWith('/')) return !url.startsWith('//');
  try {
    const resolved = new URL(url, 'https://app.catsco.cc');
    return resolved.protocol === 'https:' && !resolved.username && !resolved.password
      && !resolved.hash && !/\.\./.test(resolved.pathname)
      && resolved.hostname.endsWith('catsco.cc');
  } catch {
    return false;
  }
}

// Canonical source descriptor minted by the server binding. The UI never
// invents these fields; it only compares them for exact equality. file_key and
// mime_type are optional on real historical attachments and normalize to ''.
export function normalizeFileAnnotationSource(value) {
  if (!plainObject(value)) return null;
  const topicId = boundedString(value.topic_id, 256);
  const messageId = positiveInteger(value.message_id);
  const attachmentIndex = nonNegativeInteger(value.attachment_index);
  if (!topicId || messageId === null || attachmentIndex === null) return null;
  const name = boundedString(value.name, 256);
  const url = boundedString(value.url, 2048);
  const type = boundedString(value.type, 32);
  if (!name || !url || !type) return null;
  if (!validResourceURL(url)) return null;
  const fileKey = optionalString(value.file_key, 256);
  const mimeType = optionalString(value.mime_type, 128);
  const size = finiteNumber(value.size);
  if (size !== null && size < 0) return null;
  const width = positiveInteger(value.width);
  const height = positiveInteger(value.height);
  const rawVersion = boundedString(value.version, 72);
  // Strict: exactly 64 hex characters (case-insensitive, normalized lower).
  if (rawVersion === null || !FILE_ANNOTATION_VERSION_PATTERN.test(rawVersion)) return null;
  const source = {
    topic_id: topicId,
    message_id: messageId,
    attachment_index: attachmentIndex,
    name,
    url,
    file_key: fileKey,
    mime_type: mimeType,
    type,
    size: size === null ? 0 : size,
    version: rawVersion.toLowerCase(),
  };
  if (width !== null) source.width = width;
  if (height !== null) source.height = height;
  return source;
}

// Exact canonical-descriptor equality for binding comparisons. Absent and
// empty optional fields are equivalent; URLs compare canonically so a CDN
// absolute URL matches its relative form.
export function fileAnnotationSourceMatches(left, right) {
  return Boolean(left && right)
    && left.topic_id === right.topic_id
    && left.message_id === right.message_id
    && left.attachment_index === right.attachment_index
    && canonicalFileResourceURL(left.url) === canonicalFileResourceURL(right.url)
    && left.version === right.version;
}

// The server minted the binding from the ACTUAL persisted attachment. The UI
// must confirm the file it is still showing is that same resource before
// sending, otherwise a replaced attachment would be annotated silently.
export function fileAnnotationSourceMatchesFile(source, file) {
  if (!source || !file) return false;
  if (canonicalFileResourceURL(source.url) !== canonicalFileResourceURL(file.url)) return false;
  if (String(source.name || '') !== String(file.name || '')) return false;
  if (String(source.type || '') !== String(file.type || '')) return false;
  if (Number(source.size || 0) !== Number(file.size || 0)) return false;
  return true;
}

// ---------------------------------------------------------------------------
// Rect normalization
// ---------------------------------------------------------------------------

// Normalized 0..1 rect relative to the ACTUAL media pixels (never letterbox).
export function normalizeFileAnnotationRect(value) {
  if (!plainObject(value)) return null;
  const x = finiteNumber(value.x);
  const y = finiteNumber(value.y);
  const width = finiteNumber(value.width);
  const height = finiteNumber(value.height);
  if (x === null || y === null || width === null || height === null) return null;
  if (x < 0 || y < 0 || width <= 0 || height <= 0) return null;
  if (x + width > 1 || y + height > 1) return null;
  return { x, y, width, height };
}

export function normalizeFileAnnotationTarget(kind, value) {
  if (!plainObject(value)) return null;
  if (kind === 'image' || kind === 'video' || kind === 'pdf') {
    const rect = normalizeFileAnnotationRect(value.rect);
    if (rect === null) return null;
    const target = { rect };
    if (kind === 'video') {
      const time = finiteNumber(value.time_seconds);
      if (time === null || time < 0) return null;
      target.time_seconds = time;
    }
    if (kind === 'pdf') {
      const page = positiveInteger(value.page);
      if (page === null) return null;
      target.page = page;
    }
    return target;
  }
  if (kind === 'text') {
    const start = nonNegativeInteger(value.start);
    const end = nonNegativeInteger(value.end);
    if (start === null || end === null || end <= start) return null;
    const quote = boundedString(value.quote, FILE_ANNOTATION_MAX_QUOTE);
    if (quote === null || quote.length === 0) return null;
    const lineStart = positiveInteger(value.line_start);
    const lineEnd = positiveInteger(value.line_end);
    if (lineStart === null || lineEnd === null || lineEnd < lineStart) return null;
    return { start, end, quote, line_start: lineStart, line_end: lineEnd };
  }
  if (kind === 'cells') {
    const sheet = boundedString(value.sheet, FILE_ANNOTATION_MAX_SHEET);
    if (sheet === null || CONTROL_FORBIDDEN.test(sheet)) return null;
    const rowStart = positiveInteger(value.row_start, FILE_ANNOTATION_MAX_ROW);
    const rowEnd = positiveInteger(value.row_end, FILE_ANNOTATION_MAX_ROW);
    const colStart = positiveInteger(value.column_start, FILE_ANNOTATION_MAX_COL);
    const colEnd = positiveInteger(value.column_end, FILE_ANNOTATION_MAX_COL);
    if (rowStart === null || rowEnd === null || colStart === null || colEnd === null) return null;
    if (rowEnd < rowStart || colEnd < colStart) return null;
    const quote = boundedString(value.quote, FILE_ANNOTATION_MAX_QUOTE, true);
    if (quote === null) return null;
    return {
      sheet, row_start: rowStart, row_end: rowEnd,
      column_start: colStart, column_end: colEnd, quote,
    };
  }
  return null;
}

function normalizeFileAnnotationEntry(value) {
  if (!plainObject(value)) return null;
  const id = boundedString(value.id, FILE_ANNOTATION_MAX_ID);
  if (id === null || CONTROL_FORBIDDEN.test(id)) return null;
  if (!FILE_ANNOTATION_KINDS.includes(value.kind)) return null;
  const body = boundedString(value.body, FILE_ANNOTATION_MAX_BODY, true);
  if (body === null || !/\S/.test(body)) return null;
  const target = normalizeFileAnnotationTarget(value.kind, value.target);
  if (target === null) return null;
  return { id, kind: value.kind, body, target };
}

// Full document validation. Returns null when anything is non-canonical so
// the caller refuses instead of silently dropping user comments.
export function normalizeFileAnnotations(value) {
  if (!plainObject(value)) return null;
  if (value.contract_version !== FILE_ANNOTATIONS_CONTRACT) return null;
  const source = normalizeFileAnnotationSource(value.source);
  if (source === null) return null;
  if (!Array.isArray(value.annotations) || value.annotations.length > FILE_ANNOTATION_MAX_ANNOTATIONS) return null;
  const seen = new Set();
  const annotations = value.annotations.map((entry) => {
    const normalized = normalizeFileAnnotationEntry(entry);
    if (normalized === null || seen.has(normalized.id)) return null;
    seen.add(normalized.id);
    return normalized;
  });
  if (annotations.some((entry) => entry === null)) return null;
  const normalized = { contract_version: FILE_ANNOTATIONS_CONTRACT, source, annotations };
  let bytes;
  try {
    bytes = new TextEncoder().encode(JSON.stringify(normalized)).length;
  } catch {
    return null;
  }
  if (bytes > FILE_ANNOTATION_MAX_BYTES) return null;
  return normalized;
}

// ---------------------------------------------------------------------------
// Target summaries
// ---------------------------------------------------------------------------

export function fileAnnotationTargetRect(kind, target) {
  return target?.rect || null;
}

// Text and cells annotations carry no visual evidence; the UI must say so
// instead of pretending a screenshot exists.
export function fileAnnotationHasScreenshot(kind) {
  return kind === 'image' || kind === 'video' || kind === 'pdf';
}

export function fileAnnotationTargetSummary(kind, target) {
  if (!target) return '';
  if (kind === 'text') {
    const quote = String(target.quote || '');
    return quote.length > 48 ? `${quote.slice(0, 48)}…` : quote || `第 ${target.line_start}–${target.line_end} 行`;
  }
  if (kind === 'cells') {
    return `${target.sheet} ${target.row_start}:${target.column_start}–${target.row_end}:${target.column_end}`;
  }
  const rect = target.rect || {};
  const pct = (value) => `${Math.round((Number(value) || 0) * 100)}%`;
  const base = `区域 ${pct(rect.x)},${pct(rect.y)}`;
  if (kind === 'video') return `${base} @ ${Number(target.time_seconds || 0).toFixed(2)}s`;
  if (kind === 'pdf') return `${base} 第 ${target.page} 页`;
  return base;
}

export function fileAnnotationCommentPreview(body) {
  if (typeof body !== 'string') return '';
  const flat = body.replace(/\s+/g, ' ').trim();
  return flat.length > 120 ? `${flat.slice(0, 120)}…` : flat;
}

export function fileAnnotationDraftKey(drafts) {
  return JSON.stringify((drafts || []).map((draft) => [draft.id, draft.kind, draft.body, draft.target]));
}

// ---------------------------------------------------------------------------
// Text offsets (UTF-16 code units, matching the Go server)
// ---------------------------------------------------------------------------

// Normalize a raw start/end pair against the source text bounds.
// Offsets are UTF-16 code units (JS string indices) — the same unit the
// server compares against, so no conversion is applied.
export function normalizedSelection(start, end, bounds) {
  const length = typeof bounds === 'number' ? bounds : bounds && bounds.length;
  if (!Number.isInteger(length) || length < 0) throw fail('bad-geometry', 'text bounds invalid');
  if (!Number.isInteger(start) || !Number.isInteger(end)) throw fail('bad-geometry', 'text offsets must be integers');
  const from = Math.min(start, end);
  const to = Math.max(start, end);
  if (from < 0 || to > length) throw fail('bad-geometry', 'text selection out of bounds');
  if (to === from) throw fail('bad-geometry', 'text selection is empty');
  return { start: from, end: to };
}

function utf16Offset(range, root, which) {
  // Prefix length: select the whole root, then cut the probe at the range
  // boundary. setEnd for both start and end — setStart would return the
  // remaining text length, not the offset.
  const probe = range.cloneRange();
  probe.selectNodeContents(root);
  if (which === 'start') probe.setEnd(range.startContainer, range.startOffset);
  else probe.setEnd(range.endContainer, range.endOffset);
  return probe.toString().length;
}

function utf16LineAt(text, offset) {
  let line = 1;
  const end = Math.min(offset, text.length);
  for (let index = 0; index < end; index += 1) {
    if (text.charCodeAt(index) === 10) line += 1;
  }
  return line;
}

// Build a text target from a window.Selection fully contained in `root`
// (the original plaintext <pre>). Offsets are UTF-16 code units into
// root.textContent — identical to what the server validates against.
export function textSelectionTarget(root, selection) {
  if (!root || !selection || typeof selection !== 'object' || selection.rangeCount === 0) {
    throw fail('unavailable', 'no text selection');
  }
  const range = selection.getRangeAt(0);
  if (!range || !root.contains(range.startContainer) || !root.contains(range.endContainer)) {
    throw fail('unavailable', 'selection is outside the annotatable text');
  }
  if (range.collapsed) throw fail('unavailable', 'text selection is empty');
  const text = root.textContent || '';
  const start = utf16Offset(range, root, 'start');
  const end = utf16Offset(range, root, 'end');
  const normalized = normalizedSelection(start, end, text.length);
  const quote = text.slice(normalized.start, normalized.end);
  if (!quote) throw fail('unavailable', 'text selection is empty');
  return { start: normalized.start, end: normalized.end, quote,
    line_start: utf16LineAt(text, normalized.start), line_end: utf16LineAt(text, normalized.end) };
}

// ---------------------------------------------------------------------------
// Spreadsheet cells
// ---------------------------------------------------------------------------

function cellCell(element) {
  const row = Number(element && element.dataset && element.dataset.fileAnnotationRow);
  const column = Number(element && element.dataset && element.dataset.fileAnnotationColumn);
  if (!Number.isInteger(row) || !Number.isInteger(column) || row < 1 || column < 1) return null;
  return { row, column, text: (element.textContent || '').trim() };
}

// Build a cells target from a pointer drag between two <td> elements inside
// `root` (the spreadsheet root carrying data-file-annotation-sheet).
export function cellSelectionTarget(first, last, root) {
  const sheet = root && root.dataset && typeof root.dataset.fileAnnotationSheet === 'string'
    ? root.dataset.fileAnnotationSheet : '';
  if (!sheet) throw fail('unavailable', 'spreadsheet sheet is unavailable');
  const a = cellCell(first);
  const b = cellCell(last);
  if (!a || !b) throw fail('unavailable', 'cell selection is outside the annotatable grid');
  const rowStart = Math.min(a.row, b.row);
  const rowEnd = Math.max(a.row, b.row);
  const columnStart = Math.min(a.column, b.column);
  const columnEnd = Math.max(a.column, b.column);
  if (rowEnd > 200 || columnEnd > 50) throw fail('bad-geometry', 'cell range exceeds the preview grid');
  const quote = `${a.text}${a === b ? '' : ` … ${b.text}`}`.slice(0, FILE_ANNOTATION_MAX_QUOTE);
  return { sheet, row_start: rowStart, row_end: rowEnd,
    column_start: columnStart, column_end: columnEnd, quote };
}

// ---------------------------------------------------------------------------
// Media content box (de-letterboxed)
// ---------------------------------------------------------------------------

// Actual media content box in CSS pixels. For <img>/<video> the letterbox
// introduced by object-fit: contain is removed, so normalized rects are
// relative to the real pixels. For <canvas> (PDF pages) the element box is
// the content. Returns null when the media has no usable dimensions.
export function mediaContentBox(element) {
  if (!element || typeof element !== 'object' || typeof element.getBoundingClientRect !== 'function') return null;
  const rect = element.getBoundingClientRect();
  if (!rect || !Number.isFinite(rect.width) || !Number.isFinite(rect.height) || rect.width <= 0 || rect.height <= 0) return null;
  const tag = typeof element.tagName === 'string' ? element.tagName.toUpperCase() : '';
  if (tag === 'CANVAS') {
    const bw = Number(element.width);
    const bh = Number(element.height);
    if (!Number.isFinite(bw) || !Number.isFinite(bh) || bw <= 0 || bh <= 0) return null;
    return { x: rect.left, y: rect.top, width: rect.width, height: rect.height, bitmapWidth: bw, bitmapHeight: bh };
  }
  const naturalWidth = Number(element.naturalWidth || element.videoWidth);
  const naturalHeight = Number(element.naturalHeight || element.videoHeight);
  if (!Number.isFinite(naturalWidth) || !Number.isFinite(naturalHeight) || naturalWidth <= 0 || naturalHeight <= 0) return null;
  const scale = Math.min(rect.width / naturalWidth, rect.height / naturalHeight);
  const width = naturalWidth * scale;
  const height = naturalHeight * scale;
  return { x: rect.left + (rect.width - width) / 2, y: rect.top + (rect.height - height) / 2,
    width, height, bitmapWidth: naturalWidth, bitmapHeight: naturalHeight };
}

// Convert a CSS-pixel rect over the content box into a normalized 0..1 rect.
// The rect is intersected with the actual content box: a selection that
// starts or ends outside the box is clipped, never clamped into a fake
// in-bounds rect. Returns null when there is no intersection.
export function normalizedRectFromBox(rect, box) {
  if (!box || !Number.isFinite(box.width) || !Number.isFinite(box.height) || box.width <= 0 || box.height <= 0) {
    throw fail('bad-geometry', 'media content box is unavailable');
  }
  if (!rect || !Number.isFinite(rect.x) || !Number.isFinite(rect.y)
    || !Number.isFinite(rect.width) || !Number.isFinite(rect.height)) throw fail('bad-geometry', 'selection rect is invalid');
  const left = Math.max(rect.x, box.x);
  const top = Math.max(rect.y, box.y);
  const right = Math.min(rect.x + rect.width, box.x + box.width);
  const bottom = Math.min(rect.y + rect.height, box.y + box.height);
  if (right <= left || bottom <= top) return null;
  return {
    x: (left - box.x) / box.width,
    y: (top - box.y) / box.height,
    width: (right - left) / box.width,
    height: (bottom - top) / box.height,
  };
}

// ---------------------------------------------------------------------------
// Canvas capture (bounded)
// ---------------------------------------------------------------------------

function encodeJPEG(element, quality) {
  try {
    const url = element.toDataURL('image/jpeg', quality);
    if (typeof url !== 'string' || !url.startsWith('data:image/jpeg;base64,')) return null;
    return url;
  } catch (error) {
    if (isTaintError(error)) return { taint: true };
    return null;
  }
}

function mediaReady(element) {
  const tag = typeof element.tagName === 'string' ? element.tagName.toUpperCase() : '';
  if (tag === 'IMG') return element.complete !== false && Number(element.naturalWidth) > 0;
  if (tag === 'VIDEO') return Number(element.readyState) >= 2;
  return true;
}

// Seek a video to the exact annotated time and wait for the frame. Requires
// a paused (or pauseable) element; the caller captures the frozen frame.
function seekVideoFrame(element, timeSeconds, signal, timeoutMs = 5000) {
  return new Promise((resolve, reject) => {
    let settled = false;
    const cleanup = () => {
      clearTimeout(timer);
      element.removeEventListener('seeked', onSeeked);
      element.removeEventListener('error', onError);
      if (signal) signal.removeEventListener('abort', onAbort);
    };
    const done = (fn, value) => { if (settled) return; settled = true; cleanup(); fn(value); };
    const onAbort = () => done(reject, fail('aborted', 'capture aborted'));
    const onSeeked = () => {
      if (Math.abs(Number(element.currentTime) - timeSeconds) > 0.05) {
        done(reject, fail('bad-geometry', 'video frame is not at the annotated time'));
        return;
      }
      done(resolve);
    };
    const onError = () => done(reject, fail('unavailable', 'video seek failed'));
    const timer = setTimeout(() => done(reject, fail('unavailable', 'video seek timed out')), timeoutMs);
    if (signal) {
      if (signal.aborted) { done(reject, fail('aborted', 'capture aborted')); return; }
      signal.addEventListener('abort', onAbort, { once: true });
    }
    element.addEventListener('seeked', onSeeked, { once: true });
    element.addEventListener('error', onError, { once: true });
    try {
      if (element.pause) element.pause();
      element.currentTime = timeSeconds;
    } catch (error) {
      done(reject, fail('unavailable', 'video cannot be seeked'));
    }
  });
}

// Capture full + crop JPEG evidence for an image/video/PDF-canvas element.
// Draws the ACTUAL element into a bounded canvas (<=2048 on the longest edge)
// — never an original-pixel bitmap. `target.rect` is normalized 0..1 over the
// actual media pixels. Returns { screenshots, warnings } or throws a coded
// error ('tainted' | 'unavailable' | 'aborted' | 'bad-geometry' |
// 'encode-failed' | 'image-too-large'). No getDisplayMedia, no permissions.
export function captureFileAnnotationScreenshots(element, target, options = {}) {
  const signal = options.signal;
  if (signal && signal.aborted) throw fail('aborted', 'capture aborted');
  const box = mediaContentBox(element);
  if (!box) throw fail('unavailable', 'media is not loaded or has no content');
  if (!mediaReady(element)) throw fail('unavailable', 'media is not ready to capture');
  if (!normalizeFileAnnotationRect(target && target.rect)) throw fail('bad-geometry', 'target rect is invalid');

  const tag = typeof element.tagName === 'string' ? element.tagName.toUpperCase() : '';
  const rect = target.rect;

  // Videos must be captured at the exact annotated paused frame.
  if (tag === 'VIDEO' && Number.isFinite(target.time_seconds)) {
    if (Math.abs(Number(element.currentTime) - target.time_seconds) > 0.05) {
      throw fail('unavailable', 'video frame is not at the annotated time; use seekVideoFrame first');
    }
  }

  // Bound the longest edge at 2048 instead of allocating a full-resolution
  // canvas for large sources.
  const scale = Math.min(1, FILE_ANNOTATION_MAX_DIMENSION / Math.max(box.bitmapWidth, box.bitmapHeight));
  const outWidth = Math.max(1, Math.round(box.bitmapWidth * scale));
  const outHeight = Math.max(1, Math.round(box.bitmapHeight * scale));

  const full = document.createElement('canvas');
  full.width = outWidth;
  full.height = outHeight;
  const context = full.getContext && full.getContext('2d');
  if (!context) throw fail('unavailable', 'canvas is unavailable');
  try {
    context.drawImage(element, 0, 0, outWidth, outHeight);
  } catch (error) {
    if (isTaintError(error)) throw fail('tainted', 'media is cross-origin and cannot be captured');
    throw fail('unavailable', 'media cannot be drawn');
  }

  // Red selection box on the full image only.
  const rx = rect.x * outWidth;
  const ry = rect.y * outHeight;
  const rw = rect.width * outWidth;
  const rh = rect.height * outHeight;
  context.strokeStyle = '#ef4444';
  context.lineWidth = Math.max(4, Math.min(8, Math.round(3 * outWidth / box.width)));
  context.strokeRect(rx, ry, rw, rh);

  if (signal && signal.aborted) throw fail('aborted', 'capture aborted');

  // Crop: outward floor/ceil with 16 CSS px padding, clamped to the bitmap.
  // Same per-axis multiply-then-sum order as the gateway SDK host math.
  const padX = FILE_ANNOTATION_CROP_PADDING * outWidth / box.width;
  const padY = FILE_ANNOTATION_CROP_PADDING * outHeight / box.height;
  const cropLeft = Math.max(0, Math.floor(rx - padX));
  const cropTop = Math.max(0, Math.floor(ry - padY));
  const cropRight = Math.min(outWidth, Math.ceil(rect.x * outWidth + rect.width * outWidth + padX));
  const cropBottom = Math.min(outHeight, Math.ceil(rect.y * outHeight + rect.height * outHeight + padY));
  const cropWidth = Math.max(1, cropRight - cropLeft);
  const cropHeight = Math.max(1, cropBottom - cropTop);

  const crop = document.createElement('canvas');
  crop.width = cropWidth;
  crop.height = cropHeight;
  const cropContext = crop.getContext && crop.getContext('2d');
  if (!cropContext) throw fail('unavailable', 'canvas is unavailable');
  cropContext.drawImage(full, cropLeft, cropTop, cropWidth, cropHeight, 0, 0, cropWidth, cropHeight);

  const encoded = [encodeJPEG(full, FILE_ANNOTATION_JPEG_QUALITY), encodeJPEG(crop, FILE_ANNOTATION_JPEG_QUALITY)];
  if (encoded.some((entry) => entry && entry.taint)) throw fail('tainted', 'media is cross-origin and cannot be captured');
  if (encoded.some((entry) => !entry)) throw fail('encode-failed', 'canvas encoding failed');
  if (encoded.some((url) => Math.ceil((url.length - 22) * 3 / 4) > 2 * 1024 * 1024)) {
    const retried = [encodeJPEG(full, 0.6), encodeJPEG(crop, 0.6)];
    if (retried.some((entry) => entry && entry.taint)) throw fail('tainted', 'media is cross-origin and cannot be captured');
    if (retried.some((url) => !url)) throw fail('encode-failed', 'canvas encoding failed');
    if (retried.some((url) => Math.ceil((url.length - 22) * 3 / 4) > 2 * 1024 * 1024)) {
      throw fail('image-too-large', 'image exceeds 2 MiB');
    }
    return {
      screenshots: [
        { role: 'full', mime_type: 'image/jpeg', width: outWidth, height: outHeight, data_url: retried[0] },
        { role: 'crop', mime_type: 'image/jpeg', width: cropWidth, height: cropHeight, data_url: retried[1] },
      ],
      warnings: [],
    };
  }

  return {
    screenshots: [
      { role: 'full', mime_type: 'image/jpeg', width: outWidth, height: outHeight, data_url: encoded[0] },
      { role: 'crop', mime_type: 'image/jpeg', width: cropWidth, height: cropHeight, data_url: encoded[1] },
    ],
    warnings: [],
  };
}

// Capture from an already-rendered source canvas (the annotatable PDF page /
// media surface). Bounded to 2048; never allocates an original-size canvas.
export function captureFileAnnotationScreenshot({
  canvas,
  rect,
  padding = FILE_ANNOTATION_CROP_PADDING,
  maxDimension = FILE_ANNOTATION_MAX_DIMENSION,
  quality = FILE_ANNOTATION_JPEG_QUALITY,
} = {}) {
  if (!canvas || typeof canvas.getContext !== 'function' || !canvas.width || !canvas.height) {
    return { error: 'canvas-unavailable' };
  }
  // A canvas whose 2D context cannot be created is reported, never faked.
  if (canvas.getContext('2d') === null) return { error: 'canvas-unavailable' };
  const sourceWidth = canvas.width;
  const sourceHeight = canvas.height;
  const scale = Math.min(1, maxDimension / Math.max(sourceWidth, sourceHeight));
  const outWidth = Math.max(1, Math.round(sourceWidth * scale));
  const outHeight = Math.max(1, Math.round(sourceHeight * scale));

  let full;
  try {
    full = document.createElement('canvas');
    full.width = outWidth;
    full.height = outHeight;
    const context = full.getContext('2d');
    if (!context) return { error: 'canvas-unavailable' };
    context.drawImage(canvas, 0, 0, outWidth, outHeight);
    const rx = rect.x * outWidth;
    const ry = rect.y * outHeight;
    const rw = Math.max(1, rect.width * outWidth);
    const rh = Math.max(1, rect.height * outHeight);
    context.strokeStyle = '#ef4444';
    context.lineWidth = 4;
    context.strokeRect(rx, ry, rw, rh);
  } catch (error) {
    return { error: isTaintError(error) ? 'tainted' : 'canvas-unavailable' };
  }

  const padX = padding * scale;
  const padY = padding * scale;
  const cropLeft = Math.max(0, Math.floor(rect.x * outWidth - padX));
  const cropTop = Math.max(0, Math.floor(rect.y * outHeight - padY));
  const cropRight = Math.min(outWidth, Math.ceil(rect.x * outWidth + rect.width * outWidth + padX));
  const cropBottom = Math.min(outHeight, Math.ceil(rect.y * outHeight + rect.height * outHeight + padY));
  const cropWidth = Math.max(1, cropRight - cropLeft);
  const cropHeight = Math.max(1, cropBottom - cropTop);

  let crop;
  try {
    crop = document.createElement('canvas');
    crop.width = cropWidth;
    crop.height = cropHeight;
    const context = crop.getContext('2d');
    if (!context) return { error: 'canvas-unavailable' };
    context.drawImage(full, cropLeft, cropTop, cropWidth, cropHeight, 0, 0, cropWidth, cropHeight);
  } catch (error) {
    return { error: isTaintError(error) ? 'tainted' : 'canvas-unavailable' };
  }

  const encode = (element) => encodeJPEG(element, quality);
  const fullURL = encode(full);
  const cropURL = encode(crop);
  if (fullURL && fullURL.taint) return { error: 'tainted' };
  if (cropURL && cropURL.taint) return { error: 'tainted' };
  if (!fullURL || !cropURL) return { error: 'encode-failed' };

  return {
    images: [
      { role: 'full', mime_type: 'image/jpeg', data_url: fullURL, width: outWidth, height: outHeight },
      { role: 'crop', mime_type: 'image/jpeg', data_url: cropURL, width: cropWidth, height: cropHeight },
    ],
    warnings: [],
  };
}

export function fileAnnotationScreenshotFiles(result) {
  if (!result || !Array.isArray(result.images)) return [];
  return result.images.map((image) => screenshotFile(image));
}

export function fileAnnotationOwnedBlocks(uploads, images) {
  if (!Array.isArray(uploads) || !Array.isArray(images)) return [];
  return uploads.map((upload, index) => ownedScreenshotBlock(upload, images[index]));
}

// ---------------------------------------------------------------------------
// Strict single-annotation validation (throws coded errors)
// ---------------------------------------------------------------------------

// Strict validation of one file annotation against the contract. Returns the
// annotation unchanged when valid; throws Error with a stable `.code`
// ('bad-geometry') otherwise. Use normalizeFileAnnotations for documents.
export function validateFileAnnotation(annotation) {
  if (!annotation || typeof annotation !== 'object' || Array.isArray(annotation)) throw fail('bad-geometry', 'annotation must be an object');
  if (typeof annotation.id !== 'string' || !/^fa_[A-Za-z0-9_-]{1,128}$/.test(annotation.id)) {
    throw fail('bad-geometry', 'annotation id must be fa_<token>');
  }
  if (CONTROL_FORBIDDEN.test(annotation.id)) throw fail('bad-geometry', 'annotation id contains control characters');
  if (!FILE_ANNOTATION_KINDS.includes(annotation.kind)) throw fail('bad-geometry', 'annotation kind is unsupported');
  if (typeof annotation.body !== 'string' || !annotation.body.trim()) throw fail('bad-geometry', 'annotation comment is empty');
  if (annotation.body.length > FILE_ANNOTATION_MAX_BODY) throw fail('bad-geometry', 'annotation comment is too long');
  const target = annotation.target;
  if (!target || typeof target !== 'object' || Array.isArray(target)) throw fail('bad-geometry', 'annotation target is invalid');

  if (annotation.kind === 'image') {
    if (!normalizeFileAnnotationRect(target.rect)) throw fail('bad-geometry', 'image rect must be normalized 0..1');
  } else if (annotation.kind === 'video') {
    if (!normalizeFileAnnotationRect(target.rect)) throw fail('bad-geometry', 'video rect must be normalized 0..1');
    if (!Number.isFinite(target.time_seconds) || target.time_seconds < 0) throw fail('bad-geometry', 'video time_seconds must be a finite positive number');
  } else if (annotation.kind === 'pdf') {
    if (!normalizeFileAnnotationRect(target.rect)) throw fail('bad-geometry', 'pdf rect must be normalized 0..1');
    if (!positiveInteger(target.page)) throw fail('bad-geometry', 'pdf page must be a positive integer');
  } else if (annotation.kind === 'text') {
    if (!Number.isInteger(target.start) || !Number.isInteger(target.end) || target.start < 0 || target.end <= target.start) {
      throw fail('bad-geometry', 'text offsets must satisfy 0 <= start < end');
    }
    if (typeof target.quote !== 'string' || !target.quote || target.quote.length > FILE_ANNOTATION_MAX_QUOTE) throw fail('bad-geometry', 'text quote is invalid');
    if (!positiveInteger(target.line_start) || !positiveInteger(target.line_end) || target.line_start > target.line_end) {
      throw fail('bad-geometry', 'text line bounds are invalid');
    }
  } else if (annotation.kind === 'cells') {
    const sheet = boundedString(target.sheet, FILE_ANNOTATION_MAX_SHEET);
    if (sheet === null || CONTROL_FORBIDDEN.test(sheet)) throw fail('bad-geometry', 'cells sheet is invalid');
    for (const row of [target.row_start, target.row_end]) {
      if (!positiveInteger(row, FILE_ANNOTATION_MAX_ROW)) throw fail('bad-geometry', 'cells row out of range');
    }
    for (const column of [target.column_start, target.column_end]) {
      if (!positiveInteger(column, FILE_ANNOTATION_MAX_COL)) throw fail('bad-geometry', 'cells column out of range');
    }
    if (target.row_start > target.row_end || target.column_start > target.column_end) throw fail('bad-geometry', 'cells range is inverted');
    if (typeof target.quote !== 'string' || target.quote.length > FILE_ANNOTATION_MAX_QUOTE) throw fail('bad-geometry', 'cells quote is invalid');
  }
  return annotation;
}
