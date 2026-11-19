import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Maximize2, Minus, Plus } from 'lucide-react';
import { createDailyNotesLayout } from './daily-notes/daily-notes-layout';
import './wiki-memory-graph.css';

const NODE_ZOOM = 1.1;
const TITLE_ZOOM = 3.4;
const MAX_ZOOM = 12;
const LABEL_WIDTH = 130;
const LABEL_HEIGHT = 34;
const HUES = [161, 245, 50, 310, 205, 120, 25];

function useLayout(groups) {
  const [layout, setLayout] = useState(null);
  useEffect(() => {
    const entries = new Map();
    const sources = new Map();
    for (const [id, items] of groups) {
      sources.set(id, { label: id, entries: new Set(items.map(item => item.id)) });
      for (const item of items) entries.set(item.id, { id: item.id, sources: [id] });
    }
    const model = { entries, sources, days: [{ entries: [...entries.values()] }], relations: [] };
    const next = createDailyNotesLayout(model, { minRegionSize: 1 });
    let frame;
    const advance = () => {
      if (next.advance(performance.now() + 5)) setLayout(next);
      else frame = requestAnimationFrame(advance);
    };
    advance();
    return () => cancelAnimationFrame(frame);
  }, [groups]);
  return layout;
}

function fitLayout(layout, size) {
  const { minX, maxX, minY, maxY } = layout.bounds;
  const zoom = Math.min(1.65, Math.max(1, size.width - 52) / Math.max(1, maxX - minX),
    Math.max(1, size.height - 48) / Math.max(1, maxY - minY));
  return { zoom, centerX: (minX + maxX) / 2, centerY: (minY + maxY) / 2 };
}

function pointOnScreen(point, camera, size) {
  return {
    x: size.width / 2 + (point.x - camera.centerX) * camera.zoom,
    y: size.height / 2 + (point.y - camera.centerY) * camera.zoom,
  };
}

function inView(point, radius, size) {
  return point.x + radius >= 0 && point.y + radius >= 0 &&
    point.x - radius <= size.width && point.y - radius <= size.height;
}

function overlaps(a, b) {
  return a.x < b.x + b.width && b.x < a.x + a.width &&
    a.y < b.y + b.height && b.y < a.y + a.height;
}

function titleLabels(points, size) {
  const labels = [];
  const occupied = [];
  for (const { item, x, y } of points) {
    const left = x + LABEL_WIDTH + 12 < size.width ? x + 10 : x - LABEL_WIDTH - 10;
    const box = { x: left, y: y - 14, width: LABEL_WIDTH, height: LABEL_HEIGHT };
    if (left < 4 || box.y < 4 || box.y + box.height > size.height - 4) continue;
    if (occupied.some(other => overlaps(box, other))) continue;
    if (points.some(point => point.item.id !== item.id &&
      overlaps(box, { x: point.x - 5, y: point.y - 5, width: 10, height: 10 }))) continue;
    occupied.push(box);
    labels.push({ id: item.id, title: item.title, ...box });
    if (labels.length === 32) break;
  }
  return labels;
}

export default function WikiMemoryGraph({ groups, conversations, onOpen, isNew }) {
  const viewport = useRef(null);
  const drag = useRef(null);
  const dragged = useRef(false);
  const [size, setSize] = useState({ width: 0, height: 0 });
  const [cameraOverride, setCamera] = useState(null);
  const [panning, setPanning] = useState(false);
  const layout = useLayout(groups);
  const items = useMemo(() => new Map([...groups.values()].flat().map(item => [item.id, item])), [groups]);
  const fit = layout && fitLayout(layout, size);
  const camera = cameraOverride || fit;
  const detailZoom = Math.max(NODE_ZOOM, (fit?.zoom ?? 0) * 1.14);
  const zoomAt = useCallback((factor, x = size.width / 2, y = size.height / 2) => {
    if (!layout || !size.width) return;
    setCamera(previous => {
      const current = previous || fitLayout(layout, size);
      const zoom = Math.max(fitLayout(layout, size).zoom * 0.65, Math.min(MAX_ZOOM, current.zoom * factor));
      return {
        zoom,
        centerX: current.centerX + (x - size.width / 2) * (1 / current.zoom - 1 / zoom),
        centerY: current.centerY + (y - size.height / 2) * (1 / current.zoom - 1 / zoom),
      };
    });
  }, [layout, size]);

  useEffect(() => {
    const target = viewport.current;
    const measure = () => {
      const { width, height } = target.getBoundingClientRect();
      if (width && height) setSize(previous => previous.width === width && previous.height === height ? previous : { width, height });
    };
    measure();
    if (typeof ResizeObserver === 'undefined') return undefined;
    const observer = new ResizeObserver(measure);
    observer.observe(target);
    return () => observer.disconnect();
  }, []);

  useEffect(() => {
    const target = viewport.current;
    const onWheel = event => {
      event.preventDefault();
      const rect = target.getBoundingClientRect();
      const pixels = event.deltaY * (event.deltaMode === 1 ? 16 : event.deltaMode === 2 ? size.height : 1);
      zoomAt(Math.exp(-pixels * 0.0024), event.clientX - rect.left, event.clientY - rect.top);
    };
    target.addEventListener('wheel', onWheel, { passive: false });
    return () => target.removeEventListener('wheel', onWheel);
  }, [size.height, zoomAt]);

  const overview = !camera || camera.zoom < detailZoom;
  const circles = layout && camera && size.width ? layout.regions.flatMap(region => {
    const center = pointOnScreen(region, camera, size);
    const radius = region.r * camera.zoom;
    return inView(center, radius + 24, size) ? [{ region, ...center, radius }] : [];
  }) : [];
  const points = overview ? [] : circles.flatMap(({ region }) => region.members.flatMap(index => {
    const node = layout.nodes[index];
    const item = items.get(node.id);
    const point = pointOnScreen(node, camera, size);
    return item && inView(point, 12, size) ? [{ item, hue: HUES[region.hue % HUES.length], ...point }] : [];
  }));
  const labels = camera?.zoom >= TITLE_ZOOM ? titleLabels(points, size) : [];

  function focusRegion(region) {
    setCamera({
      centerX: region.x, centerY: region.y,
      zoom: Math.max(detailZoom * 1.16, Math.min(2.5, (Math.min(size.width, size.height) - 66) / (2 * region.r))),
    });
  }

  function startPan(event) {
    if (event.button !== 0 || !camera) return;
    dragged.current = false;
    drag.current = { pointer: event.pointerId, x: event.clientX, y: event.clientY, camera };
  }

  function movePan(event) {
    const start = drag.current;
    if (!start || start.pointer !== event.pointerId) return;
    const dx = event.clientX - start.x;
    const dy = event.clientY - start.y;
    if (!dragged.current && Math.hypot(dx, dy) < 5) return;
    if (!dragged.current) {
      dragged.current = true;
      event.currentTarget.setPointerCapture(event.pointerId);
      setPanning(true);
    }
    setCamera({ ...start.camera, centerX: start.camera.centerX - dx / start.camera.zoom,
      centerY: start.camera.centerY - dy / start.camera.zoom });
  }

  function stopPan(event) {
    if (drag.current?.pointer !== event.pointerId) return;
    drag.current = null;
    setPanning(false);
  }

  return <div className="cc-wiki-graph">
    <div className="cc-wiki-map" ref={viewport} role="group" aria-label="按会话聚类的记忆图谱"
      data-overview={overview} data-panning={panning} onPointerDown={startPan} onPointerMove={movePan}
      onPointerUp={stopPan} onPointerCancel={stopPan} onLostPointerCapture={stopPan}
      onPointerLeave={event => { if (!dragged.current) stopPan(event); }}
      onClickCapture={event => {
        if (!dragged.current || event.detail === 0) return;
        event.stopPropagation();
        dragged.current = false;
      }}>
      {circles.map(({ region, x, y, radius }) => {
        const title = conversations[region.key] || '其他来源';
        const style = { left: x, top: y, width: 2 * radius, height: 2 * radius,
          '--wiki-hue': HUES[region.hue % HUES.length] };
        const showCaption = overview ? radius >= 19 : radius >= 28;
        const caption = showCaption && <span className="cc-wiki-map-caption" style={{ maxWidth: Math.min(145, Math.max(42, radius * 1.66)) }}>
          <strong>{title}</strong>
          {overview && radius >= 27 && <small>{region.count} 篇</small>}
        </span>;
        return overview ?
          <button key={region.key} type="button" className="cc-wiki-map-region" style={style}
            aria-label={`${title}，${region.count} 篇 Wiki`} title={title} onClick={() => focusRegion(region)}>{caption}</button> :
          <div key={region.key} className="cc-wiki-map-region" style={style} aria-hidden="true">{caption}</div>;
      })}
      {points.map(({ item, x, y, hue }) => <button key={item.id} type="button" className="cc-wiki-node"
        style={{ left: x, top: y, '--wiki-hue': hue }} title={item.title} aria-label={item.title}
        data-new={isNew(item) || undefined} onClick={() => onOpen(item)} />)}
      {labels.map(label => <span key={label.id} className="cc-wiki-map-node-title" aria-hidden="true"
        style={{ left: label.x, top: label.y, width: label.width }}>{label.title}</span>)}
    </div>
    <div className="cc-wiki-map-controls" role="group" aria-label="图谱缩放">
      <button type="button" className="cc-wiki-icon" title="缩小" aria-label="缩小" onClick={() => zoomAt(0.76)}><Minus size={16} /></button>
      <button type="button" className="cc-wiki-icon" title="放大" aria-label="放大" onClick={() => zoomAt(1.32)}><Plus size={16} /></button>
      <button type="button" className="cc-wiki-icon" title="显示全部" aria-label="显示全部" onClick={() => setCamera(null)}><Maximize2 size={15} /></button>
    </div>
  </div>;
}
