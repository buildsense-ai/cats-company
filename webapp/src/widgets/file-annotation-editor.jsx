import React, { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react';
import { Crosshair, ImageOff, MessageSquareText, Pencil, Plus, Send, Trash2, X, Scan } from 'lucide-react';
import { videoTimeLabel } from './video-file-preview';
import {
  FILE_ANNOTATIONS_CONTRACT,
  captureFilePreviewScreenshots,
  fileAnnotationHasScreenshot,
  fileAnnotationOwnedBlocks,
  fileAnnotationScreenshotFiles,
  fileAnnotationSourceMatches,
  fileAnnotationSourceMatchesFile,
  fileAnnotationTargetSummary,
  mediaContentBox,
  normalizeFileAnnotations,
  normalizedRectFromBox,
  textSelectionTarget,
  cellSelectionTarget,
} from '../utils/file-annotations';

import { htmlPreviewTarget } from '../utils/html-preview-bridge';

const MODE_LABELS = { region: '框选区域', text: '文本选区', cells: '表格选区', whole: '整页/整段' };
const REGION_KINDS = { image: true, video: true, pdf: true, html: true };
const SURFACE_SELECTOR = '[data-file-annotation-surface]';
const CAPTURE_ERRORS = {
  tainted: '浏览器无法读取当前内容的图片数据。请重新打开文件后重试。',
  unavailable: '当前内容尚未就绪或不可见，请等待加载完成后重试。',
  'bad-geometry': '所选区域无效，请重新选择。',
  'encode-failed': '截图图片生成失败，请重试当前选区。',
  'image-too-large': '截图超过大小限制，请缩小选区后重试。',
};

// B's capturer returns { screenshots, warnings }; the upload helpers consume
// { images, warnings }. Normalize once, here, so the editor owns the shape.
function captureImages(result) {
  const images = result?.images || result?.screenshots || [];
  return { images, warnings: result?.warnings || [], visiblePreview: Boolean(result?.visiblePreview) };
}

// File preview annotations. Owns its own mode + draft list; the parent file
// panel stays open. The media surface is the actual img/video/canvas element,
// text comes from a Selection inside the original <pre>, cells from a td.
export default function FileAnnotationEditor({
  file,
  kind,
  contentRef,
  source,
  onSent,
  api,
  onModeChange,
}) {
  const [mode, setMode] = useState('off');
  const [drafts, setDrafts] = useState([]);
  const [current, setCurrent] = useState(null);
  const [body, setBody] = useState('');
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [capturing, setCapturing] = useState(false);
  const [sending, setSending] = useState(false);
  const [screenshots, setScreenshots] = useState(null);
  const [textOnly, setTextOnly] = useState(false);
  const [region, setRegion] = useState(null);
  const [, updateLayout] = useState(0);

  const epochRef = useRef(0);
  const captureControllerRef = useRef(null);
  const layoutReadyRef = useRef(null);
  const bindingRef = useRef(null);
  const sourceRef = useRef(null);
  const bindingPromiseRef = useRef(null);
  const bindingGenerationRef = useRef(0);
  const sendLockRef = useRef(false);
  const editingDraftIDRef = useRef(null);
  const sentRef = useRef(new Set());
  const clientIDsRef = useRef(new Map());
  const panelRef = useRef(null);
  const outlineRef = useRef(null);
  const dragRef = useRef(null);
  const inputRef = useRef(null);
  const mountedRef = useRef(true);

  const isRegion = REGION_KINDS[kind] === true && mode === 'region';
  const visualKind = fileAnnotationHasScreenshot(kind);
  const needsScreenshot = visualKind && !textOnly;

  useEffect(() => {
    mountedRef.current = true;
    return () => { mountedRef.current = false; captureControllerRef.current?.abort(); };
  }, []);

  const revokeBinding = useCallback(() => {
    const binding = bindingRef.current;
    bindingGenerationRef.current += 1;
    bindingPromiseRef.current = null;
    bindingRef.current = null;
    if (binding?.open_ref) api.revokeFileAnnotationBinding(binding.open_ref).catch(() => {});
  }, [api]);

  // A new source identity invalidates everything captured for the old file.
  useEffect(() => {
    epochRef.current += 1;
    captureControllerRef.current?.abort();
    revokeBinding();
    sourceRef.current = null;
    editingDraftIDRef.current = null;
    setBody('');
    setDrafts([]);
    setCurrent(null);
    setScreenshots(null);
    setRegion(null);
    setTextOnly(false);
    sentRef.current = new Set();
    clientIDsRef.current.clear();
  }, [source?.topic_id, source?.message_id, source?.attachment_index, file?.name, file?.url, file?.file_key, file?.mime_type, file?.size, revokeBinding]);

  useEffect(() => () => { revokeBinding(); }, [revokeBinding]);

  useEffect(() => {
    onModeChange?.(mode !== 'off');
    return () => onModeChange?.(false);
  }, [mode, onModeChange]);

  const invalidateCapture = useCallback((message) => {
    epochRef.current += 1;
    captureControllerRef.current?.abort();
    dragRef.current = null;
    setCapturing(false);
    setScreenshots(null);
    setRegion(null);
    setCurrent(previous => kind === 'video' && previous
      ? { ...previous, stale: true, capture: null, failure: null } : null);
    if (message) setNotice(message);
  }, [kind]);

  // Page / seek / zoom / resize all make an in-flight capture stale. Native
  // `seeked` does not bubble, so it is bound on the media element itself.
  useEffect(() => {
    if (mode === 'off') return undefined;
    const root = contentRef?.current;
    const surface = root?.querySelector(SURFACE_SELECTOR);
    let viewportWidth = window.innerWidth;
    let viewportHeight = window.innerHeight;
    const editingComment = () => document.activeElement?.closest?.('.file-annotation-popover');
    const onViewport = (event) => {
      if (event?.type === 'scroll' && event.target?.closest?.('.file-annotation-editor')) return;
      if (kind === 'video') { updateLayout(value => value + 1); return; }
      if (event?.type === 'resize') {
        const width = window.innerWidth, height = window.innerHeight;
        if (width === viewportWidth && height === viewportHeight) return;
        const keyboardResize = width === viewportWidth && editingComment();
        viewportWidth = width; viewportHeight = height;
        if (keyboardResize) { updateLayout(value => value + 1); return; }
      }
      invalidateCapture('页面或缩放已变化，请重新框选');
    };
    const onSeek = (event) => {
      if (kind === 'video' && ['seeked', 'loadedmetadata'].includes(event.type)) {
        updateLayout(value => value + 1);
        return;
      }
      invalidateCapture('播放位置已变化，评论已保留。请暂停后重新选择当前帧。');
    };
    const onViewChange = event => {
      if (event.detail?.reason === 'resize') { updateLayout(value => value + 1); return; }
      invalidateCapture(surface?.dataset.fileAnnotationError || '页面视图已变化，请重新框选');
    };
    window.addEventListener('resize', onViewport);
    window.addEventListener('scroll', onViewport, true);
    root?.addEventListener('seeked', onSeek, true);
    root?.addEventListener('loadedmetadata', onSeek, true);
    if (kind === 'video') {
      root?.addEventListener('seeking', onSeek, true);
      root?.addEventListener('play', onSeek, true);
    }
    root?.addEventListener('file-annotation-viewchange', onViewChange, true);
    let observer = null;
    if (typeof ResizeObserver === 'function' && root) {
      // Opening the inline composer changes preview bounds, not source content.
      // Page, zoom and DOM changes have their own invalidation events below.
      observer = new ResizeObserver(() => updateLayout(value => value + 1));
      observer.observe(root);
      if (surface) observer.observe(surface);
    }
    let pageObserver = null;
    if (root && typeof MutationObserver === 'function') {
      pageObserver = new MutationObserver(onViewport);
      if (kind === 'pdf') {
        pageObserver.observe(root, { subtree: true, attributes: true, attributeFilter: ['width', 'height', 'data-file-annotation-page', 'data-file-annotation-ready'] });
      } else if (kind === 'text' || kind === 'cells') {
        pageObserver.observe(root, { subtree: true, childList: true, characterData: true,
          attributes: true, attributeFilter: ['data-file-annotation-sheet'] });
      }
    }
    return () => {
      window.removeEventListener('resize', onViewport);
      window.removeEventListener('scroll', onViewport, true);
      root?.removeEventListener('seeked', onSeek, true);
      root?.removeEventListener('loadedmetadata', onSeek, true);
      root?.removeEventListener('seeking', onSeek, true);
      root?.removeEventListener('play', onSeek, true);
      root?.removeEventListener('file-annotation-viewchange', onViewChange, true);
      observer?.disconnect();
      pageObserver?.disconnect();
    };
  }, [contentRef, invalidateCapture, kind, mode]);

  const openBinding = useCallback(async () => {
    if (bindingRef.current) return bindingRef.current;
    if (bindingPromiseRef.current) return bindingPromiseRef.current;
    const generation = bindingGenerationRef.current;
    const pending = (async () => {
      const binding = await api.openFileAnnotationBinding(source);
      const ref = binding?.open_ref;
      if (!ref) throw new Error('未获得文件批注绑定，请重试');
      if (!mountedRef.current || generation !== bindingGenerationRef.current) {
        api.revokeFileAnnotationBinding(ref).catch(() => {});
        throw new Error('已离开原文件预览');
      }
      if (!fileAnnotationSourceMatchesFile(binding.source, file)
        || (binding.source.file_key && file.file_key && binding.source.file_key !== file.file_key)
        || (binding.source.mime_type && file.mime_type && binding.source.mime_type !== file.mime_type)
        || binding.source.topic_id !== source?.topic_id
        || binding.source.message_id !== source?.message_id
        || binding.source.attachment_index !== source?.attachment_index) {
        api.revokeFileAnnotationBinding(ref).catch(() => {});
        throw new Error('文件来源已变化，请重新打开文件');
      }
      bindingRef.current = binding;
      sourceRef.current = binding.source;
      return binding;
    })();
    bindingPromiseRef.current = pending;
    try { return await pending; }
    finally { if (bindingPromiseRef.current === pending) bindingPromiseRef.current = null; }
  }, [api, file, source]);

  useLayoutEffect(() => {
    const complete = layoutReadyRef.current;
    layoutReadyRef.current = null;
    complete?.();
  });

  // Copy the selected frame before waiting for the binding request. Evidence
  // is accepted only after the original file's binding has been validated.
  const captureTarget = useCallback(async (target, epoch, signal) => {
    setCapturing(true);
    setError('');
    setNotice('');
    setScreenshots(null);
    let stage = 'binding';
    try {
      // Text and cell anchors survive reflow. Let the inline composer commit
      // before the asynchronous DOM renderer records the preview dimensions.
      if (kind === 'text' || kind === 'cells') {
        await new Promise((resolve, reject) => {
          const onAbort = () => { layoutReadyRef.current = null; reject(new DOMException('Capture canceled', 'AbortError')); };
          layoutReadyRef.current = () => { signal.removeEventListener('abort', onAbort); resolve(); };
          if (signal.aborted) onAbort(); else signal.addEventListener('abort', onAbort, { once: true });
        });
        if (!mountedRef.current || epoch !== epochRef.current) return null;
      }
      const surface = contentRef?.current?.querySelector(SURFACE_SELECTOR);
      const pending = captureFilePreviewScreenshots(surface, kind, target, { signal })
        .then(result => ({ result }), failure => ({ failure }));
      await openBinding();
      if (!mountedRef.current || epoch !== epochRef.current) return null;
      stage = 'capture';
      const captured = await pending;
      if (captured.failure) throw captured.failure;
      const result = captureImages(captured.result);
      if (!mountedRef.current || epoch !== epochRef.current) return null;
      if (result.images.length !== 2) throw new Error('当前内容无法截图，请重试');
      setScreenshots(result);
      return { capture: result };
    } catch (failure) {
      if (mountedRef.current && epoch === epochRef.current) {
        const message = stage === 'binding'
          ? `文件来源验证失败：${failure?.message || '请重新打开原文件'}`
          : CAPTURE_ERRORS[failure?.code]
            || (['stale', 'renderer-unavailable', 'capture-timeout'].includes(failure?.code)
              ? failure.message : '截图未能完成，请重试当前选区。');
        return { failure: { stage, message } };
      }
      return null;
    } finally {
      if (mountedRef.current && epoch === epochRef.current) setCapturing(false);
    }
  }, [contentRef, kind, openBinding]);

  const acceptTarget = useCallback(async (target, anchor = null, withScreenshot = needsScreenshot) => {
    const epoch = ++epochRef.current;
    captureControllerRef.current?.abort();
    const controller = new AbortController();
    captureControllerRef.current = controller;
    setCurrent({ target, anchor, capture: null });
    setScreenshots(null);
    const result = withScreenshot ? await captureTarget(target, epoch, controller.signal) : null;
    if (!mountedRef.current || epochRef.current !== epoch) return false;
    setCurrent({ target, anchor, capture: result?.capture || null, failure: result?.failure });
    setRegion(null);
    return true;
  }, [captureTarget, needsScreenshot]);

  useLayoutEffect(() => {
    // Opening the mobile keyboard during DOM rendering changes the visible
    // preview bounds. Focus once its evidence (or error) is available.
    if (current && !capturing) inputRef.current?.focus({ preventScroll: true });
  }, [current?.target, capturing]);

  const addDraft = useCallback(() => {
    const trimmed = body.trim();
    if (!trimmed || Array.from(trimmed).length > 2000 || !current || current.stale || (needsScreenshot && !current.capture)) return;
    const editingID = editingDraftIDRef.current;
    const draft = {
      id: editingID || `fa_${globalThis.crypto.randomUUID()}`,
      kind,
      body: trimmed,
      target: current.target,
      anchor: current.anchor,
      capture: current.capture || null,
      textOnly: textOnly || !current.capture,
      uploads: null,
    };
    setDrafts(previous => editingID
      ? previous.map(row => row.id === draft.id ? draft : row) : [...previous, draft]);
    editingDraftIDRef.current = null;
    setTextOnly(false);
    setCurrent(null);
    setBody('');
    setScreenshots(null);
    setNotice('');
  }, [body, current, kind, needsScreenshot, textOnly]);

  const editDraft = useCallback((draft) => {
    epochRef.current += 1; captureControllerRef.current?.abort();
    setCapturing(false); setNotice(''); setError('');
    setCurrent({ target: draft.target, anchor: draft.anchor, capture: draft.capture || null });
    setScreenshots(draft.capture || null);
    setBody(draft.body);
    setTextOnly(draft.textOnly);
    editingDraftIDRef.current = draft.id;
    inputRef.current?.focus({ preventScroll: true });
  }, []);

  const removeDraft = useCallback((id) => {
    if (editingDraftIDRef.current === id) {
      editingDraftIDRef.current = null; setCurrent(null); setScreenshots(null); setBody('');
    }
    setDrafts((previous) => previous.filter((item) => item.id !== id));
  }, []);

  const clearDrafts = useCallback(() => {
    epochRef.current += 1; captureControllerRef.current?.abort();
    editingDraftIDRef.current = null;
    setCapturing(false); setBody('');
    setDrafts([]);
    setCurrent(null);
    setScreenshots(null);
    setNotice('');
  }, []);

  const changeMode = useCallback((next) => {
    if (next === mode) return;
    if (next !== 'off' && current) {
      epochRef.current += 1; captureControllerRef.current?.abort();
      setCurrent(null); setScreenshots(null); setCapturing(false);
    }
    if (next !== 'off' && kind === 'video') contentRef?.current?.querySelector('video')?.pause();
    if (next === 'off') {
      editingDraftIDRef.current = null;
      setBody('');
      epochRef.current += 1;
      captureControllerRef.current?.abort();
      setCurrent(null);
      setScreenshots(null);
      setCapturing(false);
      setRegion(null);
      setTextOnly(false);
    }
    setMode(next);
    setError('');
    setNotice('');
  }, [contentRef, current, kind, mode]);

  // Escape cancels the pending selection only; the file panel keeps its own
  // Escape handling for actually closing.
  useEffect(() => {
    if (mode === 'off') return undefined;
    const onKeyDown = (event) => {
      if (event.key !== 'Escape' || event.defaultPrevented || event.isComposing || event.keyCode === 229) return;
      event.preventDefault();
      event.stopPropagation();
      editingDraftIDRef.current = null;
      setBody('');
      epochRef.current += 1;
      captureControllerRef.current?.abort();
      setRegion(null);
      setCurrent(null);
      setScreenshots(null);
      setCapturing(false);
      setNotice('');
      if (!current) setMode('off');
    };
    window.addEventListener('keydown', onKeyDown, true);
    return () => window.removeEventListener('keydown', onKeyDown, true);
  }, [current, mode]);

  // Per-draft direct send: each visual draft carries its OWN captured pair, so
  // no draft ever borrows another draft's evidence. Failures keep unsent items.
  const send = useCallback(async () => {
    if (sendLockRef.current || drafts.length === 0) return;
    sendLockRef.current = true;
    setSending(true);
    setError('');
    const queue = drafts.filter((draft) => !sentRef.current.has(draft.id));
    try {
      for (const draft of queue) {
        if (!mountedRef.current) return;
        const binding = await openBinding();
        let contentBlocks;
        if (visualKind && !draft.textOnly) {
          if (!draft.capture?.images?.length) throw new Error('该批注缺少截图，请删除后重新添加');
          // Owned uploads are cached per draft: a retry after a lost response
          // must not upload different bytes under the same client id.
          if (!draft.uploads) {
            const files = fileAnnotationScreenshotFiles(draft.capture);
            const uploaded = [];
            for (const item of files) uploaded.push(await api.uploadImage(item));
            if (!mountedRef.current) return;
            const blocks = fileAnnotationOwnedBlocks(uploaded, draft.capture.images);
            if (blocks.length !== files.length) throw new Error('截图上传校验失败，请重试');
            draft.uploads = blocks;
          }
          contentBlocks = [{ type: 'text', text: draft.body }, ...draft.uploads];
        }
        const fileAnnotations = normalizeFileAnnotations({
          contract_version: FILE_ANNOTATIONS_CONTRACT,
          source: sourceRef.current || binding.source,
          annotations: [{ id: draft.id, kind: draft.kind, body: draft.body, target: draft.target }],
        });
        if (!fileAnnotations) throw new Error('批注内容无效，请检查后重试');
        const envelope = {
          open_ref: binding.open_ref,
          file_annotations: fileAnnotations,
          ...(contentBlocks ? { content_blocks: contentBlocks } : {}),
        };
        const key = JSON.stringify({ draft: draft.id, body: draft.body, target: draft.target, capture: Boolean(draft.capture) });
        const ids = clientIDsRef.current;
        if (!ids.has(key)) {
          if (ids.size >= 100) ids.delete(ids.keys().next().value);
          ids.set(key, `fa_${globalThis.crypto.randomUUID()}`);
        }
        const result = await api.sendFileAnnotations({ ...envelope, client_msg_id: ids.get(key) });
        if (!mountedRef.current) return;
        // Mark acknowledged before touching state so a lost response cannot
        // cause a second durable copy of the same draft.
        sentRef.current.add(draft.id);
        ids.delete(key);
        // Selection/addition stays available during send. Consume only this
        // acknowledged row, never replace newer drafts with the send snapshot.
        setDrafts((previous) => previous.filter((item) => item.id !== draft.id));
        onSent?.(result, sourceRef.current?.topic_id);
        window.dispatchEvent(new Event('cc:data-changed'));
      }
      // The pending selection and its evidence may have been created while
      // this request was in flight; they are not part of the sent queue.
    } catch (failure) {
      if (mountedRef.current) {
        setError(failure?.message || '发送失败，未发送的批注已保留，可重试');
      }
    } finally {
      sendLockRef.current = false;
      if (mountedRef.current) setSending(false);
    }
  }, [api, drafts, onSent, openBinding, visualKind]);

  // --- selection handlers -------------------------------------------------

  const cellFromPoint = useCallback((event) => {
    const cell = event.target?.closest?.('[data-file-annotation-row]');
    if (!cell) return null;
    // The sheet root carries the sheet name; body/wrapper does not.
    const root = contentRef?.current?.querySelector('[data-file-annotation-sheet]')
      || cell.closest('[data-file-annotation-sheet]');
    return root ? { cell, root } : null;
  }, [contentRef]);

  const onPointerDown = useCallback((event) => {
    if (dragRef.current || (event.button != null && event.button !== 0)) return;
    if (mode === 'cells') {
      const hit = cellFromPoint(event);
      if (hit) {
        event.preventDefault();
        dragRef.current = { cellStart: hit.cell, root: hit.root, pointerId: event.pointerId };
      }
      return;
    }
    if (!isRegion) return;
    const surface = contentRef?.current?.querySelector(SURFACE_SELECTOR);
    if (!surface || (event.target !== surface && !surface.contains(event.target))) return;
    if (kind === 'video') surface.pause?.();
    const box = mediaContentBox(surface);
    if (!box) return;
    if (kind === 'html' && surface.dataset.fileAnnotationReady !== 'true') {
      setNotice(surface.dataset.fileAnnotationError || 'HTML 页面尚未就绪，请稍候再框选');
      return;
    }
    if (kind === 'pdf' && surface.dataset.fileAnnotationReady === 'false') {
      setNotice('当前页尚未渲染完成，请稍候再框选');
      return;
    }
    event.preventDefault();
    dragRef.current = { startX: event.clientX, startY: event.clientY, box, moved: false,
      pointerId: event.pointerId, surface, timeSeconds: kind === 'video' ? Number(surface.currentTime) : undefined };
    try { surface.setPointerCapture?.(event.pointerId); } catch { /* synthetic or released pointer */ }
  }, [cellFromPoint, contentRef, isRegion, kind, mode]);

  const onPointerMove = useCallback((event) => {
    const drag = dragRef.current;
    if (!drag || (drag.pointerId != null && event.pointerId != null && drag.pointerId !== event.pointerId)) return;
    const moved = drag.moved || Math.abs(event.clientX - drag.startX) > 4 || Math.abs(event.clientY - drag.startY) > 4;
    drag.moved = moved;
    drag.lastX = event.clientX;
    drag.lastY = event.clientY;
    if (!moved) return;
    const x = Math.min(drag.startX, event.clientX);
    const y = Math.min(drag.startY, event.clientY);
    setRegion({
      left: x, top: y,
      width: Math.abs(event.clientX - drag.startX),
      height: Math.abs(event.clientY - drag.startY),
    });
  }, []);

  const onPointerUp = useCallback(async (event) => {
    const drag = dragRef.current;
    if (drag?.pointerId != null && event.pointerId != null && drag.pointerId !== event.pointerId) return;
    dragRef.current = null;
    setRegion(null);
    if (!drag) return;
    try { drag.surface?.releasePointerCapture?.(drag.pointerId); } catch { /* capture already lost */ }
    // Cells: a real press-drag release across td cells selects the range.
    if (drag.cellStart) {
      const hit = cellFromPoint(event);
      const last = hit?.cell || drag.cellStart;
      let target;
      try {
        target = cellSelectionTarget(drag.cellStart, last, drag.root);
      } catch {
        target = null;
      }
      if (target) {
        const first = drag.cellStart.getBoundingClientRect();
        const end = last.getBoundingClientRect();
        await acceptTarget(target, { left: Math.min(first.left, end.left), right: Math.max(first.right, end.right), top: Math.min(first.top, end.top), bottom: Math.max(first.bottom, end.bottom) });
      }
      return;
    }
    const endX = Number.isFinite(event.clientX) ? event.clientX : drag.lastX ?? drag.startX;
    const endY = Number.isFinite(event.clientY) ? event.clientY : drag.lastY ?? drag.startY;
    const rect = {
      x: Math.min(drag.startX, endX),
      y: Math.min(drag.startY, endY),
      width: Math.abs(endX - drag.startX),
      height: Math.abs(endY - drag.startY),
    };
    if (rect.width < 4 || rect.height < 4) return;
    let normalized;
    try {
      normalized = normalizedRectFromBox(rect, drag.box);
    } catch {
      setNotice('选区无效，请重新框选');
      return;
    }
    if (!normalized || normalized.width < 0.005 || normalized.height < 0.005) return;
    let target = { rect: normalized };
    if (kind === 'html') {
      try { target = htmlPreviewTarget(contentRef?.current?.querySelector(SURFACE_SELECTOR), normalized); }
      catch (error) { setNotice(error.message); return; }
    }
    if (kind === 'pdf') {
      const page = Number(contentRef?.current?.querySelector(SURFACE_SELECTOR)?.dataset.fileAnnotationPage || 0);
      if (!page) { setNotice('当前页信息缺失，请稍候再框选'); return; }
      target.page = page;
    }
    if (kind === 'video') {
      const video = contentRef?.current?.querySelector(`${SURFACE_SELECTOR}`);
      if (video && Number.isFinite(video.currentTime)) {
        if (Math.abs(Number(video.currentTime) - drag.timeSeconds) > 0.05) {
          setNotice('视频画面已变化，请暂停后重新框选'); return;
        }
        target.time_seconds = drag.timeSeconds;
        video.pause?.();
      }
    }
    await acceptTarget(target);
  }, [acceptTarget, cellFromPoint, contentRef, kind]);

  useEffect(() => {
    if (!isRegion && mode !== 'cells') return undefined;
    window.addEventListener('pointermove', onPointerMove);
    window.addEventListener('pointerup', onPointerUp);
    const cancel = (event) => {
      const drag = dragRef.current;
      if (drag?.pointerId != null && event.pointerId != null && drag.pointerId !== event.pointerId) return;
      dragRef.current = null; setRegion(null);
      try { drag?.surface?.releasePointerCapture?.(drag.pointerId); } catch { /* capture already lost */ }
    };
    window.addEventListener('pointercancel', cancel);
    window.addEventListener('lostpointercapture', cancel);
    window.addEventListener('blur', cancel);
    return () => {
      window.removeEventListener('pointermove', onPointerMove);
      window.removeEventListener('pointerup', onPointerUp);
      window.removeEventListener('pointercancel', cancel);
      window.removeEventListener('lostpointercapture', cancel);
      window.removeEventListener('blur', cancel);
    };
  }, [isRegion, mode, onPointerMove, onPointerUp]);

  const selectText = useCallback(() => {
    if (mode !== 'text') return;
    const pre = contentRef?.current?.querySelector('[data-file-annotation-text]');
    if (!pre) return;
    let target;
    try {
      const selection = window.getSelection();
      target = textSelectionTarget(pre, selection);
      if (target) {
        const box = selection.getRangeAt(0).getBoundingClientRect?.();
        void acceptTarget(target, box ? { left: box.left, right: box.right, top: box.top, bottom: box.bottom } : null);
      }
    } catch {
      target = null;
    }
  }, [acceptTarget, contentRef, mode]);

  const selectCells = useCallback((event) => {
    const hit = cellFromPoint(event);
    if (!hit) return;
    let target;
    try {
      target = cellSelectionTarget(hit.cell, hit.cell, hit.root);
    } catch {
      target = null;
    }
    if (target) void acceptTarget(target);
  }, [acceptTarget, cellFromPoint]);

  const selectWhole = useCallback(() => {
    if (kind === 'pdf') {
      const canvas = contentRef?.current?.querySelector(SURFACE_SELECTOR);
      const page = Number(canvas?.dataset.fileAnnotationPage || 0);
      if (!page || canvas?.dataset.fileAnnotationReady === 'false') {
        setNotice('当前页尚未渲染完成，请稍候');
        return;
      }
      return acceptTarget({ rect: { x: 0, y: 0, width: 1, height: 1 }, page });
    } else if (kind === 'video') {
      const video = contentRef?.current?.querySelector(SURFACE_SELECTOR);
      if (!video) return;
      video.pause?.();
      return acceptTarget({ rect: { x: 0, y: 0, width: 1, height: 1 }, time_seconds: Number(video.currentTime || 0) });
    } else if (kind === 'html') {
      try { return acceptTarget(htmlPreviewTarget(contentRef?.current?.querySelector(SURFACE_SELECTOR), { x: 0, y: 0, width: 1, height: 1 })); }
      catch (error) { setNotice(error.message); return false; }
    } else if (kind === 'image') {
      return acceptTarget({ rect: { x: 0, y: 0, width: 1, height: 1 } });
    }
  }, [acceptTarget, cellFromPoint, contentRef, kind]);

  useLayoutEffect(() => {
    if (mode === 'off') return undefined;
    const root = contentRef?.current;
    if (!root) return undefined;
    const selectCellWithKey = (event) => {
      if (mode === 'cells' && (event.key === 'Enter' || event.key === ' ')) {
        event.preventDefault();
        selectCells(event);
      }
    };
    const framePointer = (event) => {
      const frame = root.querySelector(SURFACE_SELECTOR);
      if (kind !== 'html' || event.target !== frame) return;
      const data = event.detail, box = frame.getBoundingClientRect();
      const pointer = { target: frame, clientX: box.left + data.x, clientY: box.top + data.y, preventDefault() {} };
      if (data.event === 'pointerdown') onPointerDown(pointer);
      else if (data.event === 'pointermove') onPointerMove(pointer);
      else if (data.event === 'pointerup') void onPointerUp(pointer);
      else { dragRef.current = null; setRegion(null); }
    };
    root.dataset.fileAnnotationMode = mode;
    root.addEventListener('file-annotation-pointer', framePointer);
    root.addEventListener('pointerdown', onPointerDown);
    if (mode === 'text') root.addEventListener('mouseup', selectText);
    root.addEventListener('keydown', selectCellWithKey);
    const frameEscape = event => {
      if (kind === 'html' && event.key === 'Escape') invalidateCapture();
    };
    root.addEventListener('keydown', frameEscape);
    return () => {
      root.removeEventListener('keydown', frameEscape);
      delete root.dataset.fileAnnotationMode;
      root.removeEventListener('file-annotation-pointer', framePointer);
      root.removeEventListener('pointerdown', onPointerDown);
      root.removeEventListener('mouseup', selectText);
      root.removeEventListener('keydown', selectCellWithKey);
    };
  }, [contentRef, invalidateCapture, kind, mode, onPointerDown, onPointerMove, onPointerUp, selectCells, selectText]);

  useEffect(() => {
    if (mode !== 'whole') return undefined;
    if (kind !== 'pdf' && kind !== 'html') { void selectWhole(); return undefined; }
    const root = contentRef?.current;
    let active = true, timer;
    setNotice(kind === 'html' ? '正在加载 HTML 页面…' : '正在加载当前 PDF 页面…');
    const tryReadyPage = () => {
      clearTimeout(timer);
      if (root?.querySelector(SURFACE_SELECTOR)?.dataset.fileAnnotationReady !== 'true') return;
      // Initial fit-width measurement can immediately restart a first render.
      // Wait for layout to settle, and keep watching if that capture goes stale.
      timer = setTimeout(async () => {
        if (!active) return;
        const accepted = await selectWhole();
        if (accepted && active) { observer.disconnect(); setNotice(''); }
      }, 80);
    };
    const observer = new MutationObserver(tryReadyPage);
    if (root) observer.observe(root, { subtree: true, childList: true, attributes: true,
      attributeFilter: ['width', 'height', 'data-file-annotation-ready'] });
    tryReadyPage();
    return () => { active = false; clearTimeout(timer); observer.disconnect(); };
  }, [contentRef, kind, mode, selectWhole]);

  const availableModes = kind === 'text' ? ['text'] : kind === 'cells' ? ['cells'] : ['region', 'whole'];

  const showOutline = Boolean((region || current?.target?.rect) && !current?.stale);
  useLayoutEffect(() => {
    const outline = outlineRef.current;
    if (!outline) return undefined;
    const position = () => {
      const surface = contentRef?.current?.querySelector(SURFACE_SELECTOR);
      if (!surface) return;
      if (kind === 'video' && current && Math.abs(Number(surface.currentTime) - current.target.time_seconds) > 0.05) {
        outline.hidden = true; return;
      }
      let shown = region;
      if (!shown && current?.target?.rect) {
        const box = mediaContentBox(surface), rect = current.target.rect;
        if (box) shown = { left: box.x + rect.x * box.width, top: box.y + rect.y * box.height,
          width: rect.width * box.width, height: rect.height * box.height };
      }
      if (!shown) { outline.hidden = true; return; }
      const viewport = contentRef?.current?.getBoundingClientRect();
      if (viewport?.width > 0 && viewport.height > 0) {
        const left = Math.max(shown.left, viewport.left), top = Math.max(shown.top, viewport.top);
        const right = Math.min(shown.left + shown.width, viewport.left + viewport.width);
        const bottom = Math.min(shown.top + shown.height, viewport.top + viewport.height);
        shown = { left, top, width: right - left, height: bottom - top };
        if (shown.width <= 0 || shown.height <= 0) { outline.hidden = true; return; }
      }
      const parent = outline.offsetParent || document.documentElement;
      const panel = parent.getBoundingClientRect();
      Object.assign(outline.style, { left: (shown.left - panel.left - parent.clientLeft + parent.scrollLeft) + 'px',
        top: (shown.top - panel.top - parent.clientTop + parent.scrollTop) + 'px',
        width: shown.width + 'px', height: shown.height + 'px' });
      outline.hidden = false;
    };
    position();
    if (typeof ResizeObserver === 'undefined') return undefined;
    const observer = new ResizeObserver(position);
    if (contentRef?.current) observer.observe(contentRef.current);
    return () => observer.disconnect();
  });

  const cancelSelection = () => {
    epochRef.current += 1;
    captureControllerRef.current?.abort();
    editingDraftIDRef.current = null;
    setCapturing(false); setCurrent(null); setScreenshots(null); setBody(''); setNotice('');
  };
  const targetLabel = target => kind === 'video'
    ? videoTimeLabel(target.time_seconds) : fileAnnotationTargetSummary(kind, target);

  return (
    <div className="file-annotation-editor" ref={panelRef} data-kind={kind} data-mode={mode}>
      <div className="file-annotation-toolbar" role="group" aria-label="文件批注">
        <span className="file-annotation-toolbar-title"><MessageSquareText size={16} /><strong>批注</strong>
          {drafts.length > 0 && <span className="file-annotation-count">{drafts.length}</span>}
        </span>
        <div className="file-annotation-modes">
          {availableModes.map(item => <button key={item} type="button"
            className={`file-annotation-mode${mode === item ? ' is-active' : ''}`}
            aria-pressed={mode === item}
            onClick={() => {
              if (item === 'whole' && mode === 'whole') {
                if (!current || current.stale) void selectWhole();
                else setNotice('请先完成或取消当前选区');
              } else changeMode(mode === item ? 'off' : item);
            }}>
            {item === 'region' ? <Crosshair size={15} /> : <Scan size={15} />}
            {kind === 'video' && item === 'whole' ? '批注当前帧'
              : kind === 'html' && item === 'whole' ? '当前可见页' : MODE_LABELS[item]}
          </button>)}
        </div>
        {mode !== 'off' && <button className="file-annotation-icon-button" type="button" aria-label="退出批注"
          title="退出批注" onClick={() => changeMode('off')}><X size={16} /></button>}
        {!current && mode !== 'off' && <p className={`file-annotation-hint${error ? ' is-error' : ''}`} role={error ? 'alert' : 'status'}>
          {error || notice || (kind === 'video' ? '调整播放进度，暂停后框选区域或批注当前帧。'
            : kind === 'text' ? '选中原文中的文字，即可添加批注。'
              : kind === 'cells' ? '点击或拖选单元格，即可添加批注。' : '在画面上拖动选择区域。Esc 取消选区。')}
        </p>}
      </div>

      {!current && kind === 'html' && mode !== 'off' && <div className="file-annotation-empty">
        <Crosshair size={24} aria-hidden="true" /><strong>在页面中选择区域</strong>
        <p>可拖动框选，也可批注整个当前可见页面。</p>
      </div>}
      {current && <section className="file-annotation-current file-annotation-popover" role="dialog" aria-label="批注内容">
        <div className="file-annotation-composer-heading">
          <strong className="file-annotation-popover-target">{kind === 'video' ? '视频帧 · ' : ''}{targetLabel(current.target)}</strong>
          <button type="button" className="file-annotation-icon-button" aria-label="取消选区" title="取消选区" onClick={cancelSelection}><X size={16} /></button>
        </div>
        <textarea ref={inputRef} aria-label="批注内容" placeholder="描述这里需要怎样修改…"
          value={body} onChange={event => setBody(event.currentTarget.value)}
          onKeyDown={event => {
            if (event.nativeEvent.isComposing || event.keyCode === 229) return;
            if (event.key === 'Enter' && (event.ctrlKey || event.metaKey)) { event.preventDefault(); addDraft(); }
          }} />
        {current.stale && <p className="file-annotation-popover-error" role="alert">{notice || '画面已变化，评论已保留。请重新选择当前帧。'}</p>}
        {needsScreenshot && capturing && <div className="file-annotation-capture-pending" role="status">
          <span /> <span /> <p>{kind === 'video' ? '正在读取所选视频帧…' : '正在生成截图…'}</p>
        </div>}
        {needsScreenshot && screenshots?.images?.length > 0 && <div className="file-annotation-screenshots" aria-label="批注截图预览">
          {screenshots.images.map(image => <figure key={image.role}>
            <img src={image.data_url} alt={image.role === 'full' ? (kind === 'video' ? '所选视频帧' : '完整内容截图') : '选区截图'} />
            <figcaption>{image.role === 'full' ? (kind === 'video' ? '所选视频帧' : screenshots.visiblePreview ? '当前可见内容' : '完整内容') : '选区细节'}</figcaption>
          </figure>)}
          {screenshots.warnings?.length > 0 && <p role="status">{screenshots.warnings.join('；')}</p>}
        </div>}
        {current.failure && !textOnly && <div role="alert" className="file-annotation-popover-error">
          <p>{current.failure.message}</p>
          <button type="button" className="file-annotation-retry" disabled={capturing}
            onClick={() => void acceptTarget(current.target, current.anchor)}>重试当前选区</button>
        </div>}
        <div className="file-annotation-composer-options">
          {visualKind && <label className="file-annotation-text-only">
            <input type="checkbox" checked={textOnly} disabled={current.stale}
              onChange={event => {
                const checked = event.currentTarget.checked;
                setTextOnly(checked);
                if (checked) {
                  epochRef.current += 1; captureControllerRef.current?.abort(); setCapturing(false);
                } else void acceptTarget(current.target, current.anchor, true);
              }} /><ImageOff size={14} />仅发送文字与定位
          </label>}
          <span className={Array.from(body.trim()).length > 2000 ? 'is-error' : ''}>{Array.from(body.trim()).length} / 2000</span>
        </div>
        <div className="file-annotation-popover-actions">
          <span className="file-annotation-shortcut">⌘ / Ctrl + Enter 添加</span>
          <button type="button" onClick={cancelSelection}>取消选区</button>
          <button type="button" disabled={current.stale || !body.trim() || Array.from(body.trim()).length > 2000 || capturing || (needsScreenshot && !current.capture)} onClick={addDraft}>
            <Plus size={15} />{editingDraftIDRef.current ? '保存批注' : '添加批注'}
          </button>
        </div>
      </section>}

      {drafts.length > 0 && <div className="file-annotation-drafts" role="group" aria-label="待发送批注">
        {drafts.map(draft => <div className="file-annotation-draft" key={draft.id}>
          {draft.capture?.images?.[1] && <img className="file-annotation-draft-thumbnail" src={draft.capture.images[1].data_url} alt="" />}
          <div className="file-annotation-draft-content"><small>{targetLabel(draft.target)}</small>
            <span className="file-annotation-draft-label" title={draft.body}>{draft.body}</span></div>
          <button type="button" className="file-annotation-icon-button" disabled={sending} aria-label="编辑批注" title="编辑批注" onClick={() => editDraft(draft)}><Pencil size={15} /></button>
          <button type="button" className="file-annotation-icon-button" disabled={sending} aria-label="删除批注" title="删除批注" onClick={() => removeDraft(draft.id)}><Trash2 size={15} /></button>
        </div>)}
      </div>}

      {drafts.length > 0 && <div className="file-annotation-send-bar">
        <button type="button" className="file-annotation-clear" disabled={sending} onClick={clearDrafts}>清除全部</button>
        <span className="spacer" />
        <button type="button" disabled={sending || Boolean(editingDraftIDRef.current)} onClick={() => void send()}>
          <Send size={15} />{sending ? '发送中…' : `发送 ${drafts.length} 条批注`}
        </button>
      </div>}
      {error && (current || mode === 'off') && <p className="file-annotation-error" role="alert">{error}</p>}
      {(current || drafts.length > 0) && <p className="file-annotation-destination">{source?.topic_name ? `发送至「${source.topic_name}」` : '发送至文件所在会话'}</p>}
      {mode === 'off' && <button type="button" className="file-annotation-keyboard-target"
        onClick={() => changeMode(kind === 'text' ? 'text' : kind === 'cells' ? 'cells' : 'whole')}>
        {kind === 'video' ? '用键盘批注当前帧' : kind === 'html' ? '添加当前可见页批注（键盘）' : '添加整段/整页批注（键盘）'}
      </button>}
      {showOutline && <div ref={outlineRef} className="file-annotation-region-outline" aria-hidden="true" />}
    </div>
  );
}
