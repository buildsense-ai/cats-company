// Office preview container.
//
// Owns load → parse → render → select → edit → write. The invariant the whole editor rests
// on: an element id resolves to exactly one XML node, and nothing ever infers a target from
// pixels. Two consequences shape this file:
//
//   DRAFTED    text, style, geometry, z-order accumulate in one draft object and are written
//              on demand. Binding them to a history stack makes undo free, and splitting
//              them into parallel states would give four stacks that can disagree.
//
//   IMMEDIATE  inserts, grouping and page operations write once and re-parse. They cannot be
//              drafted: a new shape has no element id until it exists in the file, and a
//              fabricated id would break the invariant above. The cost is one extra save per
//              insert; the benefit is that what the user sees is always what the file holds.

import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { parsePptx } from '../pptx/parse.js';
import { findShape } from '../pptx/write.js';
import { applyOps } from '../pptx/apply.js';
import { readRelationships } from '../ooxml/zip.js';
import {
  absoluteFromRelative, alignBoxes, clampBoxToSlide, relativeFromAbsolute,
} from '../pptx/geometry.js';
import PropertyPanel from './PropertyPanel.jsx';
import EditableCanvas from './EditableCanvas.jsx';
import InsertMenu from './InsertMenu.jsx';
import InheritedConfirm from './InheritedConfirm.jsx';
import { applyDraftToSlide, officeDraftKey, parseOfficeDraftKey } from './draft-render.js';
import { paragraphsFromPlainText } from './inline-text.js';
import { OFFICE_SAVE_NOTICE } from '../save.js';
import { OFFICE_SYNC_NOTICE, OFFICE_SERVER_UNSUPPORTED_NOTICE } from '../version-binding.js';
import {
  officePanelWidthFromPointer, readOfficePanelWidth, saveOfficePanelWidth,
} from './office-panel-width.js';
import {
  canRedo, canUndo, createHistory, pushHistory, redoHistory, resetHistory, undoHistory,
} from './history.js';
import '../../styles/office-preview.css';

// The whole file is inflated in the browser, so the ceiling is memory rather than network.
// 60 MB of pptx is roughly 200 MB inflated with media, already near the practical limit of
// a side panel on a low-end laptop.
export const OFFICE_PREVIEW_MAX_BYTES = 60 * 1024 * 1024;

const EMPTY_DRAFT = { texts: {}, styles: {}, boxes: {}, zorders: {} };

// Default geometry for a newly inserted element, in EMU. One inch grid keeps the shapes at
// a sensible size on any slide format without measuring first.
const DEFAULT_INSERT = { cx: 2743200, cy: 914400 };
const CASCADE_STEP = 228600;

function flatten(shapes, out = []) {
  for (const shape of shapes || []) {
    out.push(shape);
    if (shape.children) flatten(shape.children, out);
  }
  return out;
}

function slideShape(model, slideIndex, elId) {
  const slide = model?.slides?.[slideIndex];
  if (!slide) return null;
  return findShape(slide, elId);
}

function parentGroupOf(slide, elId) {
  const walk = (shapes) => {
    for (const shape of shapes || []) {
      if ((shape.children || []).some(child => child.elId === elId)) return shape;
      const nested = walk(shape.children);
      if (nested) return nested;
    }
    return null;
  };
  return walk(slide?.shapes || []);
}

function absoluteShapeBox(slide, shape) {
  if (!shape) return null;
  if (shape.positioning !== 'relative') return shape.box;
  const group = parentGroupOf(slide, shape.elId);
  return group
    ? absoluteFromRelative(shape.box, group.box, group.childOffset, group.childExtent)
    : shape.box;
}

function draftIsEmpty(draft) {
  if (!draft) return true;
  return Object.keys(draft.texts || {}).length === 0
    && Object.keys(draft.styles || {}).length === 0
    && Object.keys(draft.boxes || {}).length === 0
    && Object.keys(draft.zorders || {}).length === 0;
}

async function resolveMediaUrls(model) {
  const urls = {};
  for (const slide of model.slides) {
    const rels = await readRelationships(model.pkg, slide.part);
    for (const shape of flatten(slide.shapes)) {
      if (!shape.imageRelId || urls[shape.imageRelId]) continue;
      const rel = rels.get(shape.imageRelId);
      if (!rel) continue;
      const bytes = await model.pkg.binary(rel.resolved);
      if (!bytes) continue;
      urls[shape.imageRelId] = URL.createObjectURL(
        new Blob([bytes], { type: mediaMime(rel.resolved) }),
      );
    }
  }
  return urls;
}

function mediaMime(path) {
  const ext = (/\.([a-z0-9]+)$/i.exec(path) || [])[1]?.toLowerCase();
  if (ext === 'jpg' || ext === 'jpeg') return 'image/jpeg';
  if (ext === 'gif') return 'image/gif';
  if (ext === 'svg') return 'image/svg+xml';
  if (ext === 'webp') return 'image/webp';
  return 'image/png';
}

function revokeAll(urls) {
  for (const value of Object.values(urls || {})) {
    if (String(value).startsWith('blob:')) URL.revokeObjectURL(value);
  }
}

/** Next free canvas slot, so repeated inserts do not land on top of each other. */
function cascadeBox(model, count) {
  const slide = model?.slideSize || { cx: 12192000, cy: 6858000 };
  const offset = (count % 6) * CASCADE_STEP;
  return {
    x: Math.round((slide.cx - DEFAULT_INSERT.cx) / 2) + offset,
    y: Math.round((slide.cy - DEFAULT_INSERT.cy) / 2) + offset,
    cx: DEFAULT_INSERT.cx,
    cy: DEFAULT_INSERT.cy,
  };
}

export default function OfficePreview({
  file, url, family = 'pptx', onSave, onSync, latestVersion,
}) {
  const [status, setStatus] = useState('loading');
  const [model, setModel] = useState(null);
  const [error, setError] = useState('');
  const [slideIndex, setSlideIndex] = useState(0);
  const [selectedId, setSelectedId] = useState(null);
  const [pickedIds, setPickedIds] = useState([]);
  const [hoveredId, setHoveredId] = useState(null);
  const [imageUrls, setImageUrls] = useState({});
  const [saving, setSaving] = useState(false);
  const [notice, setNotice] = useState('');
  const [editing, setEditing] = useState(false);
  const [askOpen, setAskOpen] = useState(false);
  const [askText, setAskText] = useState('');
  const [panelWidth, setPanelWidth] = useState(() => readOfficePanelWidth());
  const [pendingInherited, setPendingInherited] = useState(null);
  const [insertCount, setInsertCount] = useState(0);
  // Bytes that exist locally but have not been published to the conversation yet. Kept in a
  // ref because nothing renders from it, plus a boolean so the save button can reflect it.
  const pendingBytesRef = useRef(null);
  const [hasPending, setHasPending] = useState(false);
  const historyRef = useRef(createHistory(EMPTY_DRAFT));
  const gestureBaseRef = useRef(null);
  const [, setHistoryVersion] = useState(0);
  const imageUrlsRef = useRef({});
  const abortRef = useRef(null);

  const draft = historyRef.current.present;
  // "Dirty" means there is something the conversation does not have yet: either an unwritten
  // draft or structural bytes that were applied locally but not published.
  const dirty = !draftIsEmpty(draft) || hasPending;

  const bumpHistory = useCallback(() => setHistoryVersion(value => value + 1), []);

  const commitDraft = useCallback((updater) => {
    historyRef.current = pushHistory(historyRef.current, updater(historyRef.current.present));
    bumpHistory();
  }, [bumpHistory]);

  const resetDraft = useCallback(() => {
    historyRef.current = createHistory(EMPTY_DRAFT);
    bumpHistory();
  }, [bumpHistory]);

  useEffect(() => {
    setEditing(false);
  }, [url]);

  const startPanelResize = useCallback((event) => {
    if (typeof window === 'undefined' || window.innerWidth < 900) return;
    const startX = event.clientX;
    const startWidth = panelWidth;
    const move = (moveEvent) => {
      setPanelWidth(officePanelWidthFromPointer(startWidth, startX, moveEvent.clientX));
    };
    const finish = () => {
      window.removeEventListener('pointermove', move);
      window.removeEventListener('pointerup', finish);
      window.removeEventListener('pointercancel', finish);
      setPanelWidth((value) => saveOfficePanelWidth(value));
    };
    window.addEventListener('pointermove', move);
    window.addEventListener('pointerup', finish);
    window.addEventListener('pointercancel', finish);
  }, [panelWidth]);

  /** Swap in a freshly parsed model and refresh media URLs. Old blob URLs are revoked. */
  const adoptModel = useCallback(async (nextModel) => {
    const urls = await resolveMediaUrls(nextModel);
    revokeAll(imageUrlsRef.current);
    imageUrlsRef.current = urls;
    setImageUrls(urls);
    setModel(nextModel);
  }, []);

  /**
   * Hand saved bytes to the conversation.
   *
   * Upload and message creation live in the parent, because they need the live session and
   * the topic. This function exists so every publish path reports the same way and clears the
   * pending marker only after the parent confirms the message landed.
   */
  const publishBytes = useCallback(async (bytes, changes, requirement = '') => {
    if (!bytes || !onSave) return false;
    const result = await onSave({
      bytes,
      fileName: file?.name || 'presentation.pptx',
      slideCount: model?.slides?.length,
      changes: changes || [],
      requirement,
    });
    return result || false;
  }, [file?.name, model, onSave]);

  useEffect(() => {
    let cancelled = false;
    abortRef.current?.abort();
    const controller = new AbortController();
    abortRef.current = controller;

    setStatus('loading');
    setError('');
    setModel(null);
    setSelectedId(null);
    setPickedIds([]);
    setSlideIndex(0);
    setNotice('');
    setInsertCount(0);
    resetDraft();

    if (family !== 'pptx') {
      setStatus('unsupported');
      return () => { cancelled = true; };
    }
    if (!url) {
      setStatus('error');
      setError('缺少文件地址');
      return () => { cancelled = true; };
    }
    if (file?.size && file.size > OFFICE_PREVIEW_MAX_BYTES) {
      setStatus('error');
      setError(`文件较大（${Math.round(file.size / 1048576)} MB），当前最多预览 ${OFFICE_PREVIEW_MAX_BYTES / 1048576} MB，请下载后查看。`);
      return () => { cancelled = true; };
    }

    const load = async () => {
      try {
        const response = await fetch(url, { signal: controller.signal });
        if (!response.ok) throw new Error(`下载失败 HTTP ${response.status}`);
        const buffer = await response.arrayBuffer();
        if (cancelled) return;
        const parsed = await parsePptx(buffer);
        if (cancelled) return;
        const urls = await resolveMediaUrls(parsed);
        if (cancelled) return;
        revokeAll(imageUrlsRef.current);
        imageUrlsRef.current = urls;
        setImageUrls(urls);
        setModel(parsed);
        setStatus('ready');
        const firstText = parsed.slides[0]?.shapes?.find(shape => shape.text);
        const initial = (firstText || parsed.slides[0]?.shapes?.[0])?.elId || null;
        setSelectedId(initial);
        setPickedIds(initial ? [initial] : []);
      } catch (err) {
        if (cancelled || err?.name === 'AbortError') return;
        setStatus('error');
        setError(err?.message || '无法解析这个文件');
      }
    };
    load();

    return () => {
      cancelled = true;
      controller.abort();
    };
  }, [url, family, file?.size, resetDraft]);

  // Blob URLs are the classic side-panel leak. Revoke on unmount.
  useEffect(() => () => revokeAll(imageUrlsRef.current), []);

  const slide = model?.slides?.[slideIndex] || null;
  const slideCount = model?.slides?.length || 1;
  const selectedShape = useMemo(
    () => (model && selectedId ? slideShape(model, slideIndex, selectedId) : null),
    [model, slideIndex, selectedId],
  );
  const renderedSlide = useMemo(
    () => applyDraftToSlide(slide, slideIndex, draft),
    [draft, slide, slideIndex],
  );

  // Changing pages only changes what is selected. Drafts remain keyed to their own page and
  // element and are rendered again when the user returns.
  useEffect(() => {
    setSelectedId(null);
    setPickedIds([]);
  }, [slideIndex]);

  /* ---------- selection ---------- */

  const handleSelect = useCallback((elId, event) => {
    if (!elId) {
      setSelectedId(null);
      setPickedIds([]);
      return;
    }
    const additive = Boolean(event && (event.shiftKey || event.metaKey || event.ctrlKey));
    setSelectedId(elId);
    setPickedIds((current) => {
      if (!additive) return [elId];
      return current.includes(elId)
        ? current.filter(id => id !== elId)
        : [...current, elId];
    });
  }, []);

  const handleSelectMany = useCallback((elId) => {
    setSelectedId(elId);
    setPickedIds((current) => (current.includes(elId)
      ? current.filter(id => id !== elId)
      : [...current, elId]));
  }, []);

  /* ---------- draft edits ---------- */

  const handleTextChange = useCallback((next) => {
    if (!selectedShape) return;
    const key = officeDraftKey(slideIndex, selectedShape.elId);
    commitDraft(current => ({
      ...current,
      texts: { ...(current.texts || {}), [key]: next.paragraphs },
    }));
  }, [commitDraft, selectedShape, slideIndex]);

  // Text typed straight onto the canvas. It writes the same draft slot the panel writes,
  // so the two entry points cannot diverge and neither silently overwrites the other.
  const handleInlineText = useCallback((elId, value) => {
    if (!elId) return;
    const shape = slideShape(model, slideIndex, elId);
    if (!shape) return;
    const key = officeDraftKey(slideIndex, elId);
    const paragraphs = paragraphsFromPlainText(shape.textBody?.paragraphs, value);
    commitDraft(current => ({
      ...current,
      texts: { ...(current.texts || {}), [key]: paragraphs },
    }));
  }, [commitDraft, model, slideIndex]);

  const handleStyleChange = useCallback((index, patch) => {
    if (!selectedShape) return;
    const key = officeDraftKey(slideIndex, selectedShape.elId);
    commitDraft((current) => {
      const styles = { ...(current.styles || {}) };
      const elementStyles = { ...(styles[key] || {}) };
      elementStyles[index] = { ...(elementStyles[index] || {}), ...patch };
      styles[key] = elementStyles;
      return { ...current, styles };
    });
  }, [commitDraft, selectedShape, slideIndex]);

  // A drag emits dozens of boxes; only the released position is a history entry.
  const handleGestureStart = useCallback(() => {
    // Preview frames replace the visible state, but the undo base has to stay at the value
    // before pointerdown. Recording the already-previewed box would make undo a visual no-op.
    gestureBaseRef.current = historyRef.current;
  }, []);

  const handleBoxPreview = useCallback((elId, box) => {
    const key = officeDraftKey(slideIndex, elId);
    historyRef.current = resetHistory(historyRef.current, {
      ...historyRef.current.present,
      boxes: { ...(historyRef.current.present.boxes || {}), [key]: box },
    });
    bumpHistory();
  }, [bumpHistory, slideIndex]);

  const handleBoxCommit = useCallback((elId, box) => {
    const key = officeDraftKey(slideIndex, elId);
    const base = gestureBaseRef.current || historyRef.current;
    gestureBaseRef.current = null;
    historyRef.current = pushHistory(base, {
      ...base.present,
      boxes: { ...(base.present.boxes || {}), [key]: box },
    });
    bumpHistory();
  }, [bumpHistory, slideIndex]);

  const handleBoxField = useCallback((box) => {
    if (!selectedShape || !model) return;
    const clamped = clampBoxToSlide(box, model.slideSize);
    // An inherited shape has no transform of its own. Writing a box would either fail or
    // detach it from the layout silently, so the user is asked which scope they mean
    // (decision D5) and the answer decides which operation runs.
    if (selectedShape.inherited) {
      setPendingInherited({ kind: 'geometry', elId: selectedShape.elId, box: clamped });
      return;
    }
    const slide = model.slides[slideIndex];
    const group = selectedShape.positioning === 'relative'
      ? parentGroupOf(slide, selectedShape.elId) : null;
    handleBoxCommit(selectedShape.elId, group
      ? relativeFromAbsolute(clamped, group.box, group.childOffset, group.childExtent)
      : clamped);
  }, [handleBoxCommit, model, selectedShape, slideIndex]);

  const handleAlignToSlide = useCallback((edge) => {
    if (!selectedShape || !model) return;
    const box = draft.boxes?.[officeDraftKey(slideIndex, selectedShape.elId)]
      || absoluteShapeBox(model.slides[slideIndex], selectedShape);
    const { cx: slideCx, cy: slideCy } = model.slideSize;
    const next = { ...box };
    if (edge === 'left') next.x = 0;
    if (edge === 'right') next.x = slideCx - box.cx;
    if (edge === 'centerX') next.x = Math.round((slideCx - box.cx) / 2);
    if (edge === 'top') next.y = 0;
    if (edge === 'bottom') next.y = slideCy - box.cy;
    if (edge === 'centerY') next.y = Math.round((slideCy - box.cy) / 2);
    if (selectedShape.inherited) {
      setPendingInherited({ kind: 'geometry', elId: selectedShape.elId, box: next });
      return;
    }
    const group = selectedShape.positioning === 'relative'
      ? parentGroupOf(model.slides[slideIndex], selectedShape.elId) : null;
    handleBoxCommit(selectedShape.elId, group
      ? relativeFromAbsolute(next, group.box, group.childOffset, group.childExtent)
      : next);
  }, [draft.boxes, handleBoxCommit, model, selectedShape, slideIndex]);

  const handleAlign = useCallback((edge) => {
    if (!selectedShape) return;
    const box = draft.boxes?.[officeDraftKey(slideIndex, selectedShape.elId)] || selectedShape.box;
    const [aligned] = alignBoxes([{ ...box, elId: selectedShape.elId }], edge);
    handleBoxCommit(selectedShape.elId, aligned);
  }, [draft.boxes, handleBoxCommit, selectedShape, slideIndex]);

  const handleZOrder = useCallback((direction) => {
    if (!selectedShape) return;
    const key = officeDraftKey(slideIndex, selectedShape.elId);
    commitDraft(current => ({
      ...current,
      zorders: { ...(current.zorders || {}), [key]: direction },
    }));
  }, [commitDraft, selectedShape, slideIndex]);

  const handleUndo = useCallback(() => {
    historyRef.current = undoHistory(historyRef.current);
    bumpHistory();
  }, [bumpHistory]);

  const handleRedo = useCallback(() => {
    historyRef.current = redoHistory(historyRef.current);
    bumpHistory();
  }, [bumpHistory]);

  /* ---------- immediate writes ---------- */

  const buildDraftOps = useCallback((sourceModel, currentDraft) => {
    const ops = [];
    for (const [key, paragraphs] of Object.entries(currentDraft?.texts || {})) {
      const target = parseOfficeDraftKey(key);
      if (!target || !slideShape(sourceModel, target.slideIndex, target.elId)) continue;
      ops.push({ op: 'text', slideIndex: target.slideIndex, elId: target.elId,
        payload: { paragraphs } });
    }
    for (const [key, styles] of Object.entries(currentDraft?.styles || {})) {
      const target = parseOfficeDraftKey(key);
      if (!target || !slideShape(sourceModel, target.slideIndex, target.elId)) continue;
      const paragraphs = Object.entries(styles).map(([index, style]) => ({
        index: Number(index), ...style,
      }));
      if (paragraphs.length > 0) {
        ops.push({ op: 'style', slideIndex: target.slideIndex, elId: target.elId,
          payload: { paragraphs } });
      }
    }
    for (const [key, box] of Object.entries(currentDraft?.boxes || {})) {
      const target = parseOfficeDraftKey(key);
      const shape = target && slideShape(sourceModel, target.slideIndex, target.elId);
      // Inherited geometry never reaches here: the field handler routes it through the
      // confirmation dialog instead of putting it in the draft.
      if (!shape || shape.inherited) continue;
      ops.push({ op: 'geometry', slideIndex: target.slideIndex, elId: target.elId,
        payload: box });
    }
    for (const [key, direction] of Object.entries(currentDraft?.zorders || {})) {
      const target = parseOfficeDraftKey(key);
      const shape = target && slideShape(sourceModel, target.slideIndex, target.elId);
      if (!shape || shape.inherited) continue;
      ops.push({ op: 'zorder', slideIndex: target.slideIndex, elId: target.elId,
        payload: { direction } });
    }
    return ops;
  }, []);

  /**
   * Write operations that change the package structure, then re-parse.
   *
   * These cannot be drafted: a new shape has no element id until it exists in the file, and a
   * fabricated id would break the invariant that an id resolves to exactly one XML node. So
   * they hit the bytes immediately.
   *
   * They do NOT publish to the conversation by default. Publishing means uploading a new
   * attachment and appending a message, and doing that on every insert would post five
   * messages for five shapes. Instead the resulting bytes sit in `pendingBytes` and go out
   * when the user asks to save, in one message that describes the whole edit.
   */
  const runImmediate = useCallback(async (ops, successNote, { publish = false } = {}) => {
    if (!model || saving) return null;
    setSaving(true);
    setNotice('');
    try {
      let workingModel = model;
      let bytes = null;
      const draftOps = buildDraftOps(model, draft);
      if (draftOps.length > 0) {
        const flushed = await applyOps(model, draftOps);
        if (flushed.changed) {
          workingModel = await parsePptx(flushed.bytes);
          await adoptModel(workingModel);
          resetDraft();
          bytes = flushed.bytes;
          pendingBytesRef.current = bytes;
          setHasPending(true);
        }
      }

      const result = await applyOps(workingModel, ops);
      if (result.changed) {
        const reparsed = await parsePptx(result.bytes);
        await adoptModel(reparsed);
        resetDraft();
        bytes = result.bytes;
        pendingBytesRef.current = bytes;
        setHasPending(true);
      } else if (!bytes) {
        setNotice('没有产生改动');
        return null;
      }

      if (publish) {
        const published = await publishBytes(bytes, [...draftOps, ...ops]);
        if (published) pendingBytesRef.current = null;
        setHasPending(Boolean(pendingBytesRef.current));
      }
      if (successNote) setNotice(successNote);
      return bytes;
    } catch (err) {
      setNotice(`操作失败：${err.message}`);
      return null;
    } finally {
      setSaving(false);
    }
  }, [adoptModel, buildDraftOps, draft, model, publishBytes, resetDraft, saving]);

  /* ---------- insert ---------- */

  const handleInsert = useCallback(async (kind) => {
    if (!model) return;
    const box = cascadeBox(model, insertCount);
    setInsertCount(value => value + 1);

    if (kind === 'text') {
      await runImmediate([{
        op: 'insertText',
        slideIndex,
        payload: { text: '双击右侧面板改这段文字', box, size: 18 },
      }], '已插入文本框');
      return;
    }
    if (kind === 'table') {
      await runImmediate([{
        op: 'insertTable',
        slideIndex,
        payload: { rows: 3, cols: 3, box: { ...box, cy: DEFAULT_INSERT.cy * 2 } },
      }], '已插入表格');
      return;
    }
    // Rectangles and the line primitive are the same insert with a different preset. A line
    // is a very flat rectangle: simpler than a connector and it still drags and resizes.
    const preset = kind === 'roundRect' ? 'roundRect' : 'rect';
    const lineBox = kind === 'line'
      ? { x: box.x, y: box.y + Math.round(box.cy / 2), cx: box.cx, cy: 12700 * 2 }
      : box;
    await runImmediate([{
      op: 'insertShape',
      slideIndex,
      payload: { preset, box: lineBox, fill: kind === 'line' ? '#8b919c' : '#1f5eff' },
    }], kind === 'line' ? '已插入直线' : '已插入形状');
  }, [insertCount, model, runImmediate, slideIndex]);

  const handleInsertPicture = useCallback(async (picked) => {
    if (!model || !picked) return;
    try {
      const bytes = new Uint8Array(await picked.arrayBuffer());
      const ext = (picked.name.split('.').pop() || 'png').toLowerCase();
      const box = cascadeBox(model, insertCount);
      setInsertCount(value => value + 1);
      // Keep the aspect ratio so a photo is not squashed on insert.
      const sized = { ...box, cy: Math.round(box.cx * 0.62) };
      await runImmediate([{
        op: 'insertPicture',
        slideIndex,
        payload: { bytes, ext, name: picked.name, box: sized },
      }], '已插入图片');
    } catch (err) {
      setNotice(`插入图片失败：${err.message}`);
    }
  }, [insertCount, model, runImmediate, slideIndex]);

  /* ---------- group ---------- */

  const handleGroup = useCallback(async () => {
    if (!model) return;
    const ids = pickedIds.filter(id => slideShape(model, slideIndex, id));
    if (ids.length < 2) {
      setNotice('按住 Shift 再点，选中至少两个元素才能组合');
      return;
    }
    await runImmediate([{ op: 'group', slideIndex, payload: { elIds: ids } }], '已组合');
    setPickedIds([]);
    setSelectedId(null);
  }, [model, pickedIds, runImmediate, slideIndex]);

  /* ---------- delete element ---------- */

  const handleDeleteElement = useCallback(async () => {
    if (!model || !selectedShape) return;
    await runImmediate([{ op: 'delete', slideIndex, elId: selectedShape.elId }], '已删除元素');
    setSelectedId(null);
    setPickedIds([]);
  }, [model, runImmediate, selectedShape, slideIndex]);

  /* ---------- pages ---------- */

  const handleAddSlide = useCallback(async () => {
    await runImmediate([{ op: 'addSlide', payload: { at: slideIndex + 1, templateIndex: slideIndex } }],
      '已新增一页');
    setSlideIndex(index => index + 1);
  }, [runImmediate, slideIndex]);

  const handleDuplicateSlide = useCallback(async () => {
    await runImmediate([{ op: 'duplicateSlide', slideIndex }], '已复制这一页');
    setSlideIndex(index => index + 1);
  }, [runImmediate, slideIndex]);

  const handleDeleteSlide = useCallback(async () => {
    if (slideCount <= 1) {
      setNotice('这是最后一页，不能再删');
      return;
    }
    await runImmediate([{ op: 'deleteSlide', slideIndex }], '已删除这一页');
    setSlideIndex(index => Math.max(0, Math.min(index, slideCount - 2)));
  }, [runImmediate, slideCount, slideIndex]);

  const handleMoveSlide = useCallback(async (delta) => {
    const to = slideIndex + delta;
    if (to < 0 || to >= slideCount) return;
    await runImmediate([{ op: 'moveSlide', slideIndex, payload: { from: slideIndex, to } }],
      delta < 0 ? '已上移一页' : '已下移一页');
    setSlideIndex(to);
  }, [runImmediate, slideCount, slideIndex]);

  /* ---------- quiet save and explicit sync ---------- */

  /** Save every draft on every page, then publish only through the quiet save path. */
  const handleSaveDraft = useCallback(async (requirement = '') => {
    if (!model || saving) return;
    const draftOps = buildDraftOps(model, draft);
    if (draftOps.length === 0 && !pendingBytesRef.current) {
      setNotice('没有可写入的改动');
      return;
    }
    setSaving(true);
    setNotice('');
    try {
      let bytes = pendingBytesRef.current;
      if (draftOps.length > 0) {
        const result = await applyOps(model, draftOps);
        if (result.changed) {
          bytes = result.bytes;
          pendingBytesRef.current = bytes;
          setHasPending(true);
          await adoptModel(await parsePptx(result.bytes));
        } else if (!bytes) {
          resetDraft();
          setNotice('没有可写入的改动');
          return;
        }
      }
      if (!bytes) {
        setNotice('没有可写入的改动');
        return;
      }
      const published = await publishBytes(bytes, draftOps);
      if (published?.unsupported) {
        // The upload landed; only the source→latest pointer is missing because the deployed
        // backend has no route for it. Saying "retry" here would be a lie, so name the cause.
        pendingBytesRef.current = null;
        setHasPending(false);
        resetDraft();
        setNotice(OFFICE_SERVER_UNSUPPORTED_NOTICE);
      } else if (published) {
        pendingBytesRef.current = null;
        setHasPending(false);
        // Only a confirmed upload + binding may discard drafts. A failed publish leaves both
        // the draft and the pending bytes available for an exact retry.
        resetDraft();
        setNotice(published.notice || OFFICE_SAVE_NOTICE);
      } else {
        setNotice('已写入本地，但最新版未保存成功，请重试保存');
      }
    } catch (err) {
      setNotice(`保存失败：${err.message}`);
    } finally {
      setSaving(false);
    }
  }, [adoptModel, buildDraftOps, draft, model, publishBytes, resetDraft, saving]);


  /* ---------- inherited geometry ---------- */

  const handleInheritedChoice = useCallback(async (choice) => {
    const request = pendingInherited;
    setPendingInherited(null);
    if (!request || !model) return;
    if (request.kind !== 'geometry') return;

    if (choice === 'all') {
      // Changing the layout moves every slide that uses it. That is exactly what the user
      // chose, and it is why this is a separate operation rather than a flag on the write.
      const shape = slideShape(model, slideIndex, request.elId);
      const slide = model.slides[slideIndex];
      if (!shape || !slide?.layoutPart) {
        setNotice('这一页没有关联版式，无法改到所有页');
        return;
      }
      await runImmediate([{
        op: 'layoutGeometry',
        slideIndex,
        elId: request.elId,
        payload: { placeholderType: shape.placeholderType, box: request.box },
      }], '已改动版式，所有使用该版式的页都会更新');
      return;
    }

    await runImmediate([{
      op: 'materializeGeometry',
      slideIndex,
      elId: request.elId,
      payload: request.box,
    }], '只改了这一页');
  }, [model, pendingInherited, runImmediate, slideIndex]);

  /* ---------- render ---------- */

  if (status === 'loading') {
    return <div className="office-preview-state">正在解析文档…</div>;
  }
  if (status === 'unsupported') {
    return <div className="office-preview-state">Word 文档的预览正在开发中，当前请下载后查看。</div>;
  }
  if (status === 'error') {
    return <div className="office-preview-state is-error">{error}</div>;
  }
  if (!model) return null;

  const canGoBack = canUndo(historyRef.current);
  const canGoForward = canRedo(historyRef.current);
  const selectedBox = (() => {
    if (!selectedId || !selectedShape) return null;
    const box = draft.boxes?.[officeDraftKey(slideIndex, selectedId)] || selectedShape.box;
    if (selectedShape.positioning !== 'relative') return box;
    const group = parentGroupOf(slide, selectedId);
    return group
      ? absoluteFromRelative(box, group.box, group.childOffset, group.childExtent)
      : box;
  })();
  const selectedDraftKey = selectedId ? officeDraftKey(slideIndex, selectedId) : '';
  const selectedParagraphs = selectedDraftKey ? draft.texts?.[selectedDraftKey] : null;
  const selectedStyles = selectedDraftKey ? draft.styles?.[selectedDraftKey] : null;
  const editedBoxes = (() => {
    const boxes = {};
    for (const [key, box] of Object.entries(draft.boxes || {})) {
      const target = parseOfficeDraftKey(key);
      if (target?.slideIndex === slideIndex) boxes[target.elId] = box;
    }
    return boxes;
  })();
  const canGroup = pickedIds.length >= 2;

  return (
    <div className="office-preview" data-office-mode={editing ? 'edit' : 'readonly'}
      style={{ '--office-panel-width': `${panelWidth}px` }}>
      <div className="office-preview-toolbar">
        <button type="button" className="office-page-btn" disabled={slideIndex === 0}
          onClick={() => setSlideIndex(index => Math.max(0, index - 1))} aria-label="上一页">‹</button>
        <span className="office-page-label">{slideIndex + 1} / {slideCount}</span>
        <button type="button" className="office-page-btn" disabled={slideIndex >= slideCount - 1}
          onClick={() => setSlideIndex(index => Math.min(slideCount - 1, index + 1))} aria-label="下一页">›</button>

        <span className="office-toolbar-divider" />

        <button type="button" className="office-page-btn" data-office-edit-toggle=""
          disabled={saving} onClick={() => setEditing(value => !value)}>
          {editing ? '完成' : '编辑'}
        </button>

        <InsertMenu disabled={saving || !editing} onInsert={handleInsert}
          onPicture={handleInsertPicture} />

        <button type="button" className="office-page-btn" disabled={!editing || !canGroup || saving}
          onClick={handleGroup} title="按住 Shift 选两个以上元素">组合</button>
        <button type="button" className="office-page-btn"
          disabled={!editing || !selectedShape || saving} onClick={handleDeleteElement} title="删除选中元素">
          删除元素
        </button>

        <span className="office-toolbar-divider" />

        <button type="button" className="office-page-btn" disabled={!editing || saving}
          onClick={handleAddSlide} title="在当前页后新增一页">加页</button>
        <button type="button" className="office-page-btn" disabled={!editing || saving}
          onClick={handleDuplicateSlide} title="复制当前页">复制页</button>
        <button type="button" className="office-page-btn" disabled={!editing || saving}
          onClick={handleDeleteSlide} title="删除当前页">删页</button>
        <button type="button" className="office-page-btn" disabled={!editing || saving || slideIndex === 0}
          onClick={() => handleMoveSlide(-1)} title="当前页上移">页上移</button>
        <button type="button" className="office-page-btn" disabled={!editing || saving || slideIndex >= slideCount - 1}
          onClick={() => handleMoveSlide(1)} title="当前页下移">页下移</button>

        <span className="office-toolbar-divider" />

        <button type="button" className="office-page-btn" disabled={!editing || !canGoBack}
          onClick={handleUndo} title="撤销（Ctrl+Z）">撤销</button>
        <button type="button" className="office-page-btn" disabled={!editing || !canGoForward}
          onClick={handleRedo} title="重做（Ctrl+Shift+Z）">重做</button>

        <button type="button" className="office-page-btn"
          disabled={!editing || !selectedShape || selectedShape.inherited}
          onClick={() => handleZOrder('front')} title="置于顶层">置顶</button>
        <button type="button" className="office-page-btn"
          disabled={!editing || !selectedShape || selectedShape.inherited}
          onClick={() => handleZOrder('up')} title="上移一层">上移</button>
        <button type="button" className="office-page-btn"
          disabled={!editing || !selectedShape || selectedShape.inherited}
          onClick={() => handleZOrder('down')} title="下移一层">下移</button>
        <button type="button" className="office-page-btn"
          disabled={!editing || !selectedShape || selectedShape.inherited}
          onClick={() => handleZOrder('back')} title="置于底层">置底</button>

        <span className="office-toolbar-spacer" />
        {dirty ? <span className="office-dirty">已修改</span> : null}
        <button type="button" className="office-save-btn" disabled={saving}
          onClick={() => { setAskText(''); setAskOpen(true); }}>
          {saving ? '写入中…' : '写入文档'}
        </button>
      </div>

      {askOpen ? (
        <div className="office-ask-backdrop" data-office-ask="">
          <div className="office-ask" role="dialog" aria-modal="true" aria-label="写入文档">
            <div className="office-ask-title">还有要一起发给小八的新要求吗？</div>
            <p className="office-ask-hint">
              写了就和小八说一声，它会拿着这一版继续改。留空则只保存，会话里不发消息。
            </p>
            <textarea className="office-ask-input" data-office-ask-input="" rows={4}
              autoFocus value={askText} placeholder="例如：把第 3 页的标题改短一点"
              onChange={event => setAskText(event.target.value)} />
            <div className="office-ask-actions">
              <button type="button" className="office-page-btn"
                onClick={() => setAskOpen(false)}>取消</button>
              <button type="button" className="office-save-btn" data-office-ask-confirm=""
                onClick={() => {
                  const requirement = askText.trim();
                  setAskOpen(false);
                  handleSaveDraft(requirement);
                }}>
                {askText.trim() ? '写入并发送' : '仅写入'}
              </button>
            </div>
          </div>
        </div>
      ) : null}

      {notice ? <div className="office-preview-notice">{notice}</div> : null}

      <div className="office-preview-body" data-office-side={editing ? 'on' : 'off'}>
        <div className="office-preview-stage">
          <EditableCanvas
            slide={renderedSlide}
            slideSize={model.slideSize}
            themeColors={model.themeColors}
            selectedId={selectedId}
            hoveredId={hoveredId}
            imageUrls={imageUrls}
            editedBoxes={editedBoxes}
            onSelect={handleSelect}
            onHover={setHoveredId}
            onGestureStart={handleGestureStart}
            onBoxPreview={handleBoxPreview}
            onBoxCommit={handleBoxCommit}
            onTextCommit={handleInlineText}
            readOnly={!editing}
          />
        </div>
        {editing ? (
          <>
            <div className="office-panel-resizer" data-office-panel-resizer=""
              role="separator" aria-orientation="vertical" tabIndex={0}
              onPointerDown={startPanelResize}
              onKeyDown={(event) => {
                if (event.key !== 'ArrowLeft' && event.key !== 'ArrowRight') return;
                event.preventDefault();
                const delta = event.key === 'ArrowLeft' ? 16 : -16;
                setPanelWidth((value) => saveOfficePanelWidth(value + delta));
              }} />
            <div className="office-preview-side">
              <PropertyPanel
                shape={selectedShape}
                paragraphs={selectedParagraphs}
                styles={selectedStyles}
                draftBox={selectedBox}
                onDraftChange={handleTextChange}
                onStyleChange={handleStyleChange}
                onBoxChange={handleBoxField}
                onAlign={handleAlign}
                onAlignToSlide={handleAlignToSlide}
                disabled={saving}
                readOnly={!editing}
              />
            </div>
          </>
        ) : null}
      </div>

      {pendingInherited ? (
        <InheritedConfirm
          request={pendingInherited}
          shape={selectedShape}
          onCancel={() => setPendingInherited(null)}
          onConfirm={handleInheritedChoice}
        />
      ) : null}
    </div>
  );
}

export { flatten, cascadeBox };
