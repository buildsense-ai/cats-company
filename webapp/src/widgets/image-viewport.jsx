import React, { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react';
import { Expand, Maximize2, Minimize2, Minus, Plus, Scan } from 'lucide-react';
import { imageFitScale, imagePointAt, imageScrollAt, imageViewGeometry } from '../utils/image-viewport';
import '../styles/image-viewport.css';
import LoadingState from './loading-state';

const editable = target => target?.closest?.('input, textarea, select, [contenteditable="true"]');
const midpoint = points => ({ x: (points[0].x + points[1].x) / 2, y: (points[0].y + points[1].y) / 2 });
const distance = points => Math.hypot(points[0].x - points[1].x, points[0].y - points[1].y);

export default function ImageViewport({ src, alt = '图片预览', annotation = false, wheelZoom = false,
  dark = false, imageClassName = '', annotationSurface = '', onError, onExpandedChange }) {
  const rootRef = useRef(null), scrollerRef = useRef(null), imgRef = useRef(null);
  const pointers = useRef(new Map()), gesture = useRef(null), space = useRef(false), hovering = useRef(false);
  const pendingAnchor = useRef(null), previousTap = useRef(null), state = useRef(null);
  const [image, setImage] = useState({ width: 0, height: 0 });
  const [viewport, setViewport] = useState({ width: 0, height: 0 });
  const [scale, setScale] = useState(1), [fit, setFit] = useState(true);
  const [failed, setFailed] = useState(false), [expanded, setExpanded] = useState(false), [panning, setPanning] = useState(false);
  state.current = { image, viewport, scale, fit, annotation };
  const ready = image.width > 0 && image.height > 0 && !failed;
  const fitScale = imageFitScale(image, viewport);
  const geometry = imageViewGeometry(image, viewport, scale);

  const changed = useCallback((reason) => {
    rootRef.current?.dispatchEvent(new CustomEvent('file-annotation-viewchange', {
      bubbles: true, detail: { reason: reason === 'resize' ? 'resize' : 'view' },
    }));
  }, []);
  const anchorFor = event => {
    const box = scrollerRef.current.getBoundingClientRect();
    return { x: event.clientX - box.left, y: event.clientY - box.top };
  };
  const zoomTo = useCallback((requested, anchor, fitting = false) => {
    const node = scrollerRef.current, current = state.current;
    if (!node || !current.image.width) return;
    const focus = anchor || { x: node.clientWidth / 2, y: node.clientHeight / 2 };
    const point = imagePointAt(current.image, current.viewport, current.scale,
      { left: node.scrollLeft, top: node.scrollTop }, focus);
    const next = Math.max(Math.min(0.05, imageFitScale(current.image, current.viewport)), Math.min(8, requested));
    pendingAnchor.current = { point, anchor: focus };
    setFit(fitting); setScale(next); changed();
  }, [changed]);
  const reset = useCallback(() => zoomTo(imageFitScale(state.current.image, state.current.viewport), undefined, true), [zoomTo]);

  useEffect(() => {
    setImage({ width: 0, height: 0 }); setFailed(false); setFit(true); setScale(1);
    pointers.current.clear(); gesture.current = null; previousTap.current = null;
    const node = scrollerRef.current; if (node) { node.scrollLeft = 0; node.scrollTop = 0; }
    const img = imgRef.current;
    if (img?.complete && img.naturalWidth) setImage({ width: img.naturalWidth, height: img.naturalHeight });
  }, [src]);

  useLayoutEffect(() => {
    const node = scrollerRef.current;
    const measure = () => {
      const next = { width: node.clientWidth, height: node.clientHeight };
      const current = state.current;
      if (current.viewport.width === next.width && current.viewport.height === next.height) return;
      if (current.image.width && !current.fit) {
        pendingAnchor.current = { point: imagePointAt(current.image, current.viewport, current.scale,
          { left: node.scrollLeft, top: node.scrollTop }, { x: current.viewport.width / 2, y: current.viewport.height / 2 }),
        anchor: { x: next.width / 2, y: next.height / 2 } };
      }
      setViewport(next); changed('resize');
    };
    measure();
    const observer = typeof ResizeObserver === 'function' ? new ResizeObserver(measure) : null;
    observer?.observe(node); window.addEventListener('resize', measure);
    return () => { observer?.disconnect(); window.removeEventListener('resize', measure); };
  }, [changed]);
  useLayoutEffect(() => {
    if (fit && ready) setScale(imageFitScale(image, viewport));
  }, [fit, ready, image, viewport]);
  useLayoutEffect(() => {
    const node = scrollerRef.current, pending = pendingAnchor.current;
    if (pending && ready) {
      const scroll = imageScrollAt(image, viewport, scale, pending.point, pending.anchor);
      node.scrollLeft = scroll.left; node.scrollTop = scroll.top; pendingAnchor.current = null;
    }
  }, [image, viewport, scale, ready]);

  useEffect(() => {
    const node = scrollerRef.current;
    const wheel = event => {
      if ((!wheelZoom && !event.ctrlKey && !event.metaKey) || !state.current.image.width) return;
      event.preventDefault(); zoomTo(state.current.scale * Math.exp(-Math.max(-100, Math.min(100, event.deltaY)) * 0.008), anchorFor(event));
    };
    node.addEventListener('wheel', wheel, { passive: false });
    const keydown = event => {
      if (editable(event.target)) return;
      const focused = rootRef.current?.contains(document.activeElement);
      if (event.code === 'Space' && (focused || hovering.current)) { space.current = true; event.preventDefault(); }
      if (!focused) return;
      if (event.key === '+' || event.key === '=') { event.preventDefault(); zoomTo(state.current.scale * 1.25); }
      else if (event.key === '-') { event.preventDefault(); zoomTo(state.current.scale / 1.25); }
      else if (event.key === '0' && !event.ctrlKey && !event.metaKey) { event.preventDefault(); reset(); }
    };
    const keyup = event => { if (event.code === 'Space') space.current = false; };
    const blur = () => { space.current = false; pointers.current.clear(); gesture.current = null; setPanning(false); };
    window.addEventListener('keydown', keydown); window.addEventListener('keyup', keyup); window.addEventListener('blur', blur);
    return () => { node.removeEventListener('wheel', wheel); window.removeEventListener('keydown', keydown);
      window.removeEventListener('keyup', keyup); window.removeEventListener('blur', blur); };
  }, [reset, wheelZoom, zoomTo]);

  const toggleExpanded = async () => {
    const next = !expanded;
    const target = rootRef.current.closest('.v3-file-preview-panel') || rootRef.current;
    if (document.fullscreenElement) await document.exitFullscreen?.();
    else if (next) { try { await target.requestFullscreen?.(); } catch { /* The in-page expanded view remains available. */ } }
    setExpanded(next); onExpandedChange?.(next); changed();
  };
  useEffect(() => {
    const fullscreenChanged = () => {
      if (!document.fullscreenElement) { setExpanded(false); onExpandedChange?.(false); }
    };
    document.addEventListener('fullscreenchange', fullscreenChanged);
    return () => document.removeEventListener('fullscreenchange', fullscreenChanged);
  }, [onExpandedChange]);

  const down = event => {
    if (event.button !== 0 && event.pointerType !== 'touch') return;
    if (!ready) return;
    const node = scrollerRef.current, point = anchorFor(event);
    pointers.current.set(event.pointerId, point);
    if (pointers.current.size > 1) {
      const points = [...pointers.current.values()].slice(0, 2), center = midpoint(points);
      gesture.current = { type: 'pinch', distance: Math.max(1, distance(points)), scale: state.current.scale,
        point: imagePointAt(image, viewport, scale, { left: node.scrollLeft, top: node.scrollTop }, center) };
      changed();
    } else if (!annotation || space.current || event.button === 1) {
      gesture.current = { type: 'pan', start: point, left: node.scrollLeft, top: node.scrollTop, moved: false };
    }
    if (gesture.current) {
      event.preventDefault(); setPanning(true);
      for (const id of pointers.current.keys()) { try { node.setPointerCapture?.(id); } catch { /* Synthetic or cancelled pointers. */ } }
    }
  };
  const move = event => {
    if (!pointers.current.has(event.pointerId)) return;
    const point = anchorFor(event); pointers.current.set(event.pointerId, point);
    const current = gesture.current, node = scrollerRef.current;
    if (!current) return;
    event.preventDefault();
    if (current.type === 'pinch' && pointers.current.size >= 2) {
      const points = [...pointers.current.values()].slice(0, 2), center = midpoint(points);
      const next = Math.max(Math.min(0.05, fitScale), Math.min(8, current.scale * distance(points) / current.distance));
      pendingAnchor.current = { point: current.point, anchor: center };
      setFit(false); setScale(next); changed();
    } else if (current.type === 'pan') {
      const dx = point.x - current.start.x, dy = point.y - current.start.y;
      if (Math.abs(dx) + Math.abs(dy) < 3 && !current.moved) return;
      current.moved = true; previousTap.current = null;
      const left = node.scrollLeft, top = node.scrollTop;
      node.scrollLeft = current.left - dx; node.scrollTop = current.top - dy;
      if (left !== node.scrollLeft || top !== node.scrollTop) changed();
    }
  };
  const end = event => {
    const current = gesture.current, point = pointers.current.get(event.pointerId);
    pointers.current.delete(event.pointerId);
    if (!current) {
      if (!pointers.current.size) setPanning(false);
      return;
    }
    event.preventDefault();
    if (current.type === 'pan' && !current.moved && event.pointerType === 'touch' && !annotation && event.type === 'pointerup') {
      const last = previousTap.current, now = performance.now();
      if (last && now - last.time < 300 && Math.hypot(point.x - last.point.x, point.y - last.point.y) < 24) {
        zoomTo(scale > fitScale * 1.1 ? fitScale : Math.min(8, Math.max(1, fitScale * 2)), point, scale > fitScale * 1.1);
        previousTap.current = null;
      } else previousTap.current = { time: now, point };
    }
    if (pointers.current.size === 1 && current.type === 'pinch') {
      const remaining = [...pointers.current.values()][0];
      gesture.current = { type: 'pan', start: remaining, left: scrollerRef.current.scrollLeft, top: scrollerRef.current.scrollTop, moved: true };
    } else if (!pointers.current.size) { gesture.current = null; setPanning(false); }
  };

  return <div ref={rootRef} className={`image-viewport${dark ? ' is-dark' : ''}${expanded ? ' is-expanded' : ''}`}
    onPointerEnter={() => { hovering.current = true; }} onPointerLeave={() => { hovering.current = false; }}>
    <div className="image-viewport-toolbar" role="group" aria-label="图片缩放控件" data-image-view-control="">
      <button type="button" aria-label="缩小图片" title="缩小图片" disabled={!ready || scale <= Math.min(0.05, fitScale)} onClick={() => zoomTo(scale / 1.25)}><Minus size={17} /></button>
      <output aria-live="polite" className="image-viewport-percentage">{ready ? `${Math.round(scale * 1000) / 10}%` : '—'}</output>
      <button type="button" aria-label="放大图片" title="放大图片" disabled={!ready || scale >= 8} onClick={() => zoomTo(scale * 1.25)}><Plus size={17} /></button>
      <button type="button" aria-label="图片适应窗口" title="适应窗口" disabled={!ready} onClick={reset}><Scan size={17} /><span>适应</span></button>
      <button type="button" aria-label="图片原尺寸" title="原尺寸（100%）" disabled={!ready} onClick={() => zoomTo(1)}><Maximize2 size={17} /><span>原尺寸</span></button>
      <button type="button" aria-label={expanded ? '退出图片全屏' : '图片全屏'} title={expanded ? '退出全屏' : '全屏'} onClick={toggleExpanded}>{expanded ? <Minimize2 size={17} /> : <Expand size={17} />}</button>
    </div>
    <div ref={scrollerRef} data-loading-region="" className={`image-viewport-scroller${panning ? ' is-panning' : ''}${annotation ? ' is-annotating' : ''}`}
      role="region" aria-label="图片查看区域" tabIndex={0}
      onClick={event => event.stopPropagation()}
      onPointerDownCapture={down} onPointerMoveCapture={move} onPointerUpCapture={end} onPointerCancelCapture={end}
      onDoubleClick={event => { if (!annotation && event.pointerType !== 'touch') zoomTo(scale > fitScale * 1.1 ? fitScale : Math.max(1, fitScale * 2), anchorFor(event), scale > fitScale * 1.1); }}>
      <div data-loading-content="" className="image-viewport-stage" style={{ width: geometry.stageWidth || '100%', height: geometry.stageHeight || '100%' }}>
        <img ref={imgRef} src={src} alt={alt} draggable={false} className={imageClassName}
          data-file-annotation-surface={annotationSurface || undefined}
          style={{ width: ready ? geometry.width : undefined, height: ready ? geometry.height : undefined, maxWidth: ready ? 'none' : '100%', maxHeight: ready ? 'none' : '100%' }}
          onLoad={event => { setImage({ width: event.currentTarget.naturalWidth, height: event.currentTarget.naturalHeight }); setFailed(false); }}
          onError={event => { setFailed(true); onError?.(event); }} />
      </div>
      {!ready && (failed
        ? <p className="image-viewport-status" role="alert">图片加载失败，请下载原文件查看。</p>
        : <LoadingState kind="image" label="正在加载图片" className="image-viewport-loading" />)}
    </div>
  </div>;
}
