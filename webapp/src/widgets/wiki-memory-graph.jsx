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
  return { zoom, offsetX: (minX + maxX) / 2, offsetY: (minY + maxY) / 2 };
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
  const fit = useMemo(() => layout && fitLayout(layout, size), [layout, size]);
  const regionKey = cameraOverride?.regionKey;
  const anchor = layout?.regions[layout.regionIndex.get(regionKey)];
  const anchorLost = regionKey !== undefined && !anchor;
  if (anchorLost) setCamera(null);

  // An opened cluster can move between layouts. Pan and zoom keep its relative offsets.
  const view = anchorLost ? fit : cameraOverride ?? fit;
  const camera = view && {
    zoom: view.zoom,
    centerX: view.offsetX + (anchor?.x ?? 0),
    centerY: view.offsetY + (anchor?.y ?? 0),
  };
  const detailZoom = Math.max(NODE_ZOOM, (fit?.zoom ?? 0) * 1.14);
  const zoomAt = useCallback((factor, x = size.width / 2, y = size.height / 2) => {
    if (!fit || !size.width) return;
    setCamera(previous => {
      const current = previous ?? fit;
      const zoom = Math.max(fit.zoom * 0.65, Math.min(MAX_ZOOM, current.zoom * factor));
      return {
        ...current,
        zoom,
        offsetX: current.offsetX + (x - size.width / 2) * (1 / current.zoom - 1 / zoom),
        offsetY: current.offsetY + (y - size.height / 2) * (1 / current.zoom - 1 / zoom),
      };
    });
  }, [fit, size]);

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

  const overview = !camera || camera.zoom < (anchor ? NODE_ZOOM : detailZoom);
  const circles = layout && camera && size.width ? layout.regions.flatMap(region => {
    const center = pointOnScreen(region, camera, size);
    const radius = region.r * camera.zoom;
    return inView(center, radius + 24, size) ? [{ region, ...center, radius }] : [];
  }) : [];
  const points = overview ? [] : circles.flatMap(({ region }) => region.members.flatMap((index, order) => {
    const node = layout.nodes[index];
    const item = items.get(node.id);
    const point = pointOnScreen(node, camera, size);
    if (!item || !inView(point, 12, size)) return [];
    const strength = 1 - 0.42 * order / Math.max(1, region.count - 1);
    return [{ item, hue: HUES[region.hue % HUES.length], strength, ...point }];
  }));
  const labels = camera?.zoom >= TITLE_ZOOM ? titleLabels(points, size) : [];

  function focusRegion(region) {
    setCamera({
      regionKey: region.key, offsetX: 0, offsetY: 0,
      zoom: Math.max(detailZoom * 1.16, Math.min(2.5, (Math.min(size.width, size.height) - 66) / (2 * region.r))),
    });
  }

  function startPan(event) {
    if (event.button !== 0 || !view) return;
    dragged.current = false;
    drag.current = { pointer: event.pointerId, x: event.clientX, y: event.clientY, view };
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
    setCamera({ ...start.view, offsetX: start.view.offsetX - dx / start.view.zoom,
      offsetY: start.view.offsetY - dy / start.view.zoom });
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
        const hue = HUES[region.hue % HUES.length];
        if (overview) {
          const discRadius = radius * 0.9;
          return <button key={region.key} type="button" className="cc-wiki-map-region"
            style={{ left: x, top: y, width: 2 * discRadius, height: 2 * discRadius, '--wiki-hue': hue }}
            aria-label={`${title}，${region.count} 篇 Wiki`} title={title} onClick={() => focusRegion(region)}>
            {discRadius >= 18 && <span className="cc-wiki-map-caption" style={{ maxWidth: Math.min(145, discRadius * 1.66) }}>
              <strong>{title}</strong>
              {discRadius >= 26 && <small>{region.count} 篇</small>}
            </span>}
          </button>;
        }
        let labelTop = y - radius - 11;
        // Keep the name of the cluster under the viewport center in frame while zooming.
        if (labelTop < 12 && Math.hypot(size.width / 2 - x, size.height / 2 - y) < radius) labelTop = 12;
        if (radius < 24 || x < 36 || x > size.width - 36 || labelTop < 12 || labelTop > size.height - 28) return null;
        return <span key={region.key} className="cc-wiki-detail-name" title={title}
          style={{ left: x, top: labelTop, '--wiki-hue': hue }}>{title}</span>;
      })}
      {points.map(({ item, x, y, hue, strength }) => <button key={item.id} type="button" className="cc-wiki-node"
        style={{ left: x, top: y, '--wiki-hue': hue, '--wiki-node-strength': strength,
          '--wiki-node-size': `${Math.min(12, 5 + camera.zoom * 1.7)}px` }} title={item.title} aria-label={item.title}
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
