import React, { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react';
import {
  FILE_ANNOTATIONS_CONTRACT,
  captureFileAnnotationScreenshots,
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

const MODE_LABELS = { region: '框选区域', text: '文本选区', cells: '表格选区', whole: '整页/整段' };
const REGION_KINDS = { image: true, video: true, pdf: true };
const SURFACE_SELECTOR = '[data-file-annotation-surface]';

// B's capturer returns { screenshots, warnings }; the upload helpers consume
// { images, warnings }. Normalize once, here, so the editor owns the shape.
function captureImages(result) {
  const images = result?.images || result?.screenshots || [];
  return { images, warnings: result?.warnings || [] };
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
  const bindingRef = useRef(null);
  const sourceRef = useRef(null);
  const bindingPromiseRef = useRef(null);
  const bindingGenerationRef = useRef(0);
  const sendLockRef = useRef(false);
  const sentRef = useRef(new Set());
  const clientIDsRef = useRef(new Map());
  const panelRef = useRef(null);
  const dragRef = useRef(null);
  const inputRef = useRef(null);
  const mountedRef = useRef(true);

  const isRegion = REGION_KINDS[kind] === true && mode === 'region';
  const visualKind = fileAnnotationHasScreenshot(kind);
  const needsScreenshot = visualKind && !textOnly;

  useEffect(() => {
    mountedRef.current = true;
    return () => { mountedRef.current = false; };
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
    revokeBinding();
    sourceRef.current = null;
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
    setCapturing(false);
    setScreenshots(null);
    setRegion(null);
    setCurrent(null);
    if (message) setNotice(message);
  }, []);

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
      if (event?.type === 'resize') {
        const width = window.innerWidth, height = window.innerHeight;
        if (width === viewportWidth && height === viewportHeight) return;
        const keyboardResize = width === viewportWidth && editingComment();
        viewportWidth = width; viewportHeight = height;
        if (keyboardResize) { updateLayout(value => value + 1); return; }
      }
      invalidateCapture('页面或缩放已变化，请重新框选');
    };
    const onSeek = () => invalidateCapture('播放位置已变化，请重新框选');
    window.addEventListener('resize', onViewport);
    window.addEventListener('scroll', onViewport, true);
    root?.addEventListener('seeked', onSeek, true);
    root?.addEventListener('loadedmetadata', onSeek, true);
    let observer = null;
    if (typeof ResizeObserver === 'function' && root) {
      const sizes = new Map([root, surface].filter(Boolean).map((node) => {
        const box = node.getBoundingClientRect();
        return [node, [box.width, box.height]];
      }));
      observer = new ResizeObserver((entries) => {
        let changed = false;
        for (const entry of entries) {
          const box = entry.target.getBoundingClientRect();
          const previous = sizes.get(entry.target);
          if (entry.target === surface && previous
            && (Math.abs(previous[0] - box.width) > 1 || Math.abs(previous[1] - box.height) > 1)) changed = true;
          sizes.set(entry.target, [box.width, box.height]);
        }
        if (changed && !editingComment()) onViewport();
        else updateLayout(value => value + 1);
      });
      observer.observe(root);
      if (surface) observer.observe(surface);
    }
    let pageObserver = null;
    if (kind === 'pdf' && root && typeof MutationObserver === 'function') {
      pageObserver = new MutationObserver(onViewport);
      pageObserver.observe(root, { subtree: true, attributes: true, attributeFilter: ['width', 'height', 'data-file-annotation-page', 'data-file-annotation-ready'] });
    }
    return () => {
      window.removeEventListener('resize', onViewport);
      window.removeEventListener('scroll', onViewport, true);
      root?.removeEventListener('seeked', onSeek, true);
      root?.removeEventListener('loadedmetadata', onSeek, true);
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

  // Capture happens at SELECTION time, for that one target only.
  const captureTarget = useCallback(async (target, epoch) => {
    setCapturing(true);
    setError('');
    setScreenshots(null);
    try {
      await openBinding();
      if (!mountedRef.current || epoch !== epochRef.current) return null;
      if (!needsScreenshot) return null;
      const surface = contentRef?.current?.querySelector(SURFACE_SELECTOR);
      const result = captureImages(await captureFileAnnotationScreenshots(surface, target));
      if (!mountedRef.current || epoch !== epochRef.current) return null;
      if (result.images.length !== 2) throw new Error('当前内容无法截图，请重新框选或选择仅发送文字与定位');
      setScreenshots(result);
      setCapturing(false);
      return result;
    } catch (failure) {
      if (mountedRef.current && epoch === epochRef.current) {
        setCapturing(false);
        setNotice(failure?.code === 'unavailable'
          ? '当前内容无法截图，可选择仅发送文字与定位'
          : (failure?.message || '截图失败，请重试或选择仅发送文字'));
      }
      return null;
    }
  }, [contentRef, needsScreenshot, openBinding]);

  const acceptTarget = useCallback(async (target, anchor = null) => {
    const epoch = ++epochRef.current;
    const capture = needsScreenshot ? await captureTarget(target, epoch) : null;
    if (!mountedRef.current || epochRef.current !== epoch) return;
    setCurrent({ target, anchor, capture, captureFailed: needsScreenshot && !capture });
    setRegion(null);
    inputRef.current?.focus();
  }, [captureTarget, needsScreenshot]);

  const addDraft = useCallback(() => {
    const trimmed = body.trim();
    if (!trimmed || trimmed.length > 2000 || !current || (needsScreenshot && !current.capture)) return;
    setDrafts((previous) => [...previous, {
      id: `fa_${globalThis.crypto.randomUUID()}`,
      kind,
      body: trimmed,
      target: current.target,
      anchor: current.anchor,
      capture: current.capture || null,
      textOnly: textOnly || !current.capture,
      uploads: null,
    }]);
    setTextOnly(false);
    setCurrent(null);
    setBody('');
    setScreenshots(null);
    setNotice('');
  }, [body, current, kind, needsScreenshot, textOnly]);

  const editDraft = useCallback((draft) => {
    setCurrent({ target: draft.target, anchor: draft.anchor, capture: draft.capture || null });
    setScreenshots(draft.capture || null);
    setBody(draft.body);
    setTextOnly(draft.textOnly);
    setDrafts((previous) => previous.filter((item) => item.id !== draft.id));
    inputRef.current?.focus();
  }, []);

  const removeDraft = useCallback((id) => {
    setDrafts((previous) => previous.filter((item) => item.id !== id));
  }, []);

  const clearDrafts = useCallback(() => {
    setDrafts([]);
    setCurrent(null);
    setScreenshots(null);
    setNotice('');
  }, []);

  const changeMode = useCallback((next) => {
    if (next === mode) return;
    if (next !== 'off' && current) { setNotice('请先完成或取消当前选区'); return; }
    if (next !== 'off' && kind === 'video') contentRef?.current?.querySelector('video')?.pause();
    if (next === 'off') {
      epochRef.current += 1;
      setCurrent(null);
      setScreenshots(null);
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
      if (event.key !== 'Escape' || event.defaultPrevented || event.isComposing) return;
      event.preventDefault();
      event.stopPropagation();
      epochRef.current += 1;
      setRegion(null);
      setCurrent(null);
      setScreenshots(null);
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
    if (mode === 'cells') {
      const hit = cellFromPoint(event);
      if (hit) {
        event.preventDefault();
        dragRef.current = { cellStart: hit.cell, root: hit.root };
      }
      return;
    }
    if (!isRegion) return;
    const surface = contentRef?.current?.querySelector(SURFACE_SELECTOR);
    const box = mediaContentBox(surface);
    if (!box) return;
    if (kind === 'pdf' && surface.dataset.fileAnnotationReady === 'false') {
      setNotice('当前页尚未渲染完成，请稍候再框选');
      return;
    }
    event.preventDefault();
    dragRef.current = { startX: event.clientX, startY: event.clientY, box, moved: false };
  }, [cellFromPoint, contentRef, isRegion, kind, mode]);

  const onPointerMove = useCallback((event) => {
    const drag = dragRef.current;
    if (!drag) return;
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
    dragRef.current = null;
    setRegion(null);
    if (!drag) return;
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
    if (!drag.moved) return;
    const rect = {
      x: Math.min(drag.startX, drag.lastX),
      y: Math.min(drag.startY, drag.lastY),
      width: Math.abs(drag.lastX - drag.startX),
      height: Math.abs(drag.lastY - drag.startY),
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
    const target = { rect: normalized };
    if (kind === 'pdf') {
      const page = Number(contentRef?.current?.querySelector(SURFACE_SELECTOR)?.dataset.fileAnnotationPage || 0);
      if (!page) { setNotice('当前页信息缺失，请稍候再框选'); return; }
      target.page = page;
    }
    if (kind === 'video') {
      const video = contentRef?.current?.querySelector(`${SURFACE_SELECTOR}`);
      if (video && Number.isFinite(video.currentTime)) {
        target.time_seconds = Number(video.currentTime);
        video.pause?.();
      }
    }
    await acceptTarget(target);
  }, [acceptTarget, cellFromPoint, contentRef, kind]);

  useEffect(() => {
    if (!isRegion && mode !== 'cells') return undefined;
    window.addEventListener('pointermove', onPointerMove);
    window.addEventListener('pointerup', onPointerUp);
    const cancel = () => { dragRef.current = null; setRegion(null); };
    window.addEventListener('pointercancel', cancel);
    return () => {
      window.removeEventListener('pointermove', onPointerMove);
      window.removeEventListener('pointerup', onPointerUp);
      window.removeEventListener('pointercancel', cancel);
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
      void acceptTarget({ rect: { x: 0, y: 0, width: 1, height: 1 }, page });
    } else if (kind === 'video') {
      const video = contentRef?.current?.querySelector(SURFACE_SELECTOR);
      if (!video) return;
      video.pause?.();
      void acceptTarget({ rect: { x: 0, y: 0, width: 1, height: 1 }, time_seconds: Number(video.currentTime || 0) });
    } else if (kind === 'image') {
      void acceptTarget({ rect: { x: 0, y: 0, width: 1, height: 1 } });
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
    root.dataset.fileAnnotationMode = mode;
    root.addEventListener('pointerdown', onPointerDown);
    if (mode === 'text') root.addEventListener('mouseup', selectText);
    root.addEventListener('keydown', selectCellWithKey);
    return () => {
      delete root.dataset.fileAnnotationMode;
      root.removeEventListener('pointerdown', onPointerDown);
      root.removeEventListener('mouseup', selectText);
      root.removeEventListener('keydown', selectCellWithKey);
    };
  }, [contentRef, mode, onPointerDown, selectCells, selectText]);

  useEffect(() => {
    if (mode === 'whole') void selectWhole();
  }, [mode, selectWhole]);

  const availableModes = kind === 'text' ? ['text'] : kind === 'cells' ? ['cells'] : ['region', 'whole'];

  const regionStyle = (() => {
    let shown = region;
    if (!shown && current?.target.rect) {
      const box = mediaContentBox(contentRef?.current?.querySelector(SURFACE_SELECTOR));
      if (box) {
        const rect = current.target.rect;
        shown = { left: (box.left ?? box.x) + rect.x * box.width, top: (box.top ?? box.y) + rect.y * box.height, width: rect.width * box.width, height: rect.height * box.height };
      }
    }
    if (!shown || !panelRef.current) return null;
    const panel = (contentRef?.current?.closest('.v3-file-preview-panel') || panelRef.current).getBoundingClientRect();
    return {
      left: shown.left - panel.left,
      top: shown.top - panel.top,
      width: shown.width,
      height: shown.height,
    };
  })();

  const popoverStyle = (() => {
    const surface = contentRef?.current?.querySelector(SURFACE_SELECTOR);
    const panel = contentRef?.current?.closest('.v3-file-preview-panel') || panelRef.current;
    if (!current || !surface || !panel) return { display: 'none' };
    const box = mediaContentBox(surface) || surface.getBoundingClientRect();
    const panelRect = panel.getBoundingClientRect();
    const width = Math.min(320, Math.max(180, panelRect.width - 24));
    const rect = current.target.rect;
    const anchorLeft = current.anchor ? current.anchor.left - panelRect.left : (box.left ?? box.x) + (rect?.x || 0) * box.width - panelRect.left;
    const anchorRight = current.anchor ? current.anchor.right - panelRect.left : (box.left ?? box.x) + (rect ? rect.x + rect.width : 1) * box.width - panelRect.left;
    let left = anchorRight + 8;
    if (left + width > panelRect.width - 12) left = anchorLeft - width - 8;
    left = Math.max(12, Math.min(left, panelRect.width - width - 12));
    const maxHeight = Math.max(160, Math.min(360, panelRect.height - 24));
    const top = Math.max(12, Math.min(current.anchor ? current.anchor.top - panelRect.top : (box.top ?? box.y) + (rect?.y || 0) * box.height - panelRect.top, panelRect.height - maxHeight - 12));
    return { left, top, width, maxHeight };
  })();

  return (
    <div className="file-annotation-editor" ref={panelRef} data-kind={kind} data-mode={mode}>
      <div className="file-annotation-toolbar" role="group" aria-label="文件批注">
        <span className="file-annotation-toolbar-title">
          {file?.name || '文件'} · {source?.topic_name ? `发送至「${source.topic_name}」` : '发送至文件所在会话'}
        </span>
        <div className="file-annotation-modes">
          {availableModes.map((item) => (
            <button
              key={item}
              type="button"
              className={`file-annotation-mode${mode === item ? ' is-active' : ''}`}
              aria-pressed={mode === item}
              onClick={() => changeMode(mode === item ? 'off' : item)}
            >
              {MODE_LABELS[item]}
            </button>
          ))}
        </div>
        {mode !== 'off' && (
          <p className={`file-annotation-hint${error ? ' is-error' : ''}`} role={error ? 'alert' : 'status'}>
            {error || notice || (kind === 'text' ? '在原文中选中要批注的文本。'
              : kind === 'cells' ? '点击要批注的单元格。'
                : '在内容上拖出要批注的区域；Esc 取消当前选区。')}
          </p>
        )}
      </div>

      {current && (
        <div className="file-annotation-current file-annotation-popover" style={popoverStyle} role="dialog" aria-label="批注内容">
          <span className="file-annotation-popover-target">
            当前批注：{fileAnnotationTargetSummary(kind, current.target)}
          </span>
          {visualKind && (
            <label className="file-annotation-text-only">
              <input type="checkbox" checked={textOnly} disabled={capturing}
                onChange={(event) => setTextOnly(event.currentTarget.checked)} />
              不含截图，仅发送文字与定位
            </label>
          )}
          {needsScreenshot && capturing && <p className="file-annotation-hint" role="status">正在捕获当前内容…</p>}
          {needsScreenshot && screenshots?.images?.length > 0 && (
            <div className="file-annotation-screenshots" aria-label="批注截图预览">
              {screenshots.images.map((image) => (
                <figure key={image.role}>
                  <img src={image.data_url} alt={image.role === 'full' ? '完整内容截图' : '选区截图'} />
                  <figcaption>{image.role === 'full' ? '完整内容' : '选区及周边'}</figcaption>
                </figure>
              ))}
              {screenshots.warnings?.length > 0 && <p role="status">部分视觉证据：{screenshots.warnings.join('；')}</p>}
            </div>
          )}
          {visualKind && !needsScreenshot && <p className="file-annotation-popover-note">将仅发送文字与定位，不含截图。</p>}
          {current.captureFailed && !textOnly && <p role="status">截图失败；请重新框选，或勾选仅发送文字与定位。</p>}
          <textarea ref={inputRef} aria-label="批注内容" maxLength={2000} placeholder="写下要传达给 Agent 的批注内容…"
            value={body} onChange={(event) => setBody(event.currentTarget.value)} />
          <div className="file-annotation-popover-actions">
            <button type="button" onClick={() => { setCurrent(null); setScreenshots(null); setNotice(''); }}>取消选区</button>
            <button type="button" disabled={!body.trim() || capturing || (needsScreenshot && !current.capture)} onClick={addDraft}>添加批注</button>
          </div>
        </div>
      )}

      {drafts.length > 0 && (
        <div className="file-annotation-drafts" role="group" aria-label="待发送批注">
          {drafts.map((draft) => (
            <div className="file-annotation-draft" key={draft.id}>
              <span className="file-annotation-draft-label" title={draft.body}>
                {fileAnnotationTargetSummary(kind, draft.target)}：{draft.body}
              </span>
              <button type="button" disabled={sending} aria-label="编辑批注" onClick={() => editDraft(draft)}>编辑</button>
              <button type="button" disabled={sending} aria-label="删除批注" onClick={() => removeDraft(draft.id)}>删除</button>
            </div>
          ))}
        </div>
      )}

      {drafts.length > 0 && (
        <div className="file-annotation-send-bar">
          <button type="button" disabled={sending} onClick={clearDrafts}>清除全部</button>
          <span className="spacer" />
          <button type="button" disabled={sending} onClick={() => void send()}>
            {sending ? '发送中…' : `发送 ${drafts.length} 条批注`}
          </button>
        </div>
      )}

      {mode === 'off' && (
        <button type="button" className="file-annotation-keyboard-target"
          onClick={() => changeMode(kind === 'text' ? 'text' : kind === 'cells' ? 'cells' : 'whole')}>
          添加整段/整页批注（键盘）
        </button>
      )}

      {regionStyle && <div className="file-annotation-region-outline" style={regionStyle} aria-hidden="true" />}
    </div>
  );
}
