import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Maximize2, Minus, Plus } from 'lucide-react';

const MIN_ZOOM = 0.15;
const MAX_ZOOM = 4;
const MAX_LABELS = 60;
const HUB_LABELS = 10;

function palette(element) {
  const style = getComputedStyle(element);
  const read = (name, fallback) => style.getPropertyValue(name).trim() || fallback;
  return {
    canvas: read('--cc-panel', '#fff'),
    text: read('--cc-text', '#1f1f1f'),
    muted: read('--cc-muted', '#747479'),
    rule: read('--cc-border', '#ececef'),
    strong: read('--cc-border-strong', '#dedee2'),
    accent: read('--cc-accent', '#29bc95'),
    danger: read('--cc-danger', '#e05757'),
    font: read('--cc-font-sans', 'system-ui, sans-serif'),
  };
}

function clipLabel(context, text, width) {
  if (context.measureText(text).width <= width) return text;
  let end = text.length;
  while (end > 1 && context.measureText(`${text.slice(0, end)}…`).width > width) end -= 1;
  return `${text.slice(0, end)}…`;
}

// rightInset is the width covered by the floating reader, so fitting and
// centring use the part of the canvas that is actually visible.
export default function DailyNotesNetwork({ layout, model, matches, selectedId, onSelect, rightInset: requestedInset = 0 }) {
  const wrapRef = useRef(null);
  const canvasRef = useRef(null);
  const viewRef = useRef({ x: 0, y: 0, k: 1 });
  const fittedRef = useRef(false);
  const dragRef = useRef(null);
  const frameRef = useRef(0);
  const [size, setSize] = useState({ width: 0, height: 0 });
  const [hoverId, setHoverId] = useState('');
  // Below the narrow breakpoint the reader sits under the graph, not beside it.
  const rightInset = size.width > 820 ? requestedInset : 0;

  const neighbors = useMemo(() => {
    const out = new Set();
    if (!selectedId) return out;
    for (const link of layout.links) {
      const a = layout.nodes[link.source].id;
      const b = layout.nodes[link.target].id;
      if (a === selectedId) out.add(b);
      if (b === selectedId) out.add(a);
    }
    return out;
  }, [layout, selectedId]);

  // Older days fade toward the canvas, so recent work stands out without a
  // time axis in either theme.
  const recency = useMemo(() => {
    const out = new Map();
    const last = Math.max(1, model.days.length - 1);
    model.days.forEach(({ entries }, rank) => {
      for (const entry of entries) out.set(entry.id, 1 - rank / last);
    });
    return out;
  }, [model]);

  // The most-linked nodes are named even at the overview zoom.
  const hubs = useMemo(() => new Set([...layout.nodes].sort((a, b) => b.degree - a.degree).slice(0, HUB_LABELS).map((node) => node.id)), [layout]);

  const titleOf = useCallback((id) => model.entries.get(id)?.title || model.sources.get(id)?.label || '', [model]);

  const draw = useCallback(() => {
    frameRef.current = 0;
    const canvas = canvasRef.current;
    if (!canvas || !size.width || !size.height) return;
    const ratio = window.devicePixelRatio || 1;
    const context = canvas.getContext('2d');
    const colors = palette(canvas);
    const { x, y, k } = viewRef.current;
    context.setTransform(ratio, 0, 0, ratio, 0, 0);
    context.globalAlpha = 1;
    context.fillStyle = colors.canvas;
    context.fillRect(0, 0, size.width, size.height);

    const visible = (id) => !matches || matches.has(id) || id === selectedId;
    const focus = (id) => id === selectedId || neighbors.has(id) || id === hoverId;
    context.lineCap = 'round';
    for (const link of layout.links) {
      const a = layout.nodes[link.source];
      const b = layout.nodes[link.target];
      const lit = (selectedId && (a.id === selectedId || b.id === selectedId)) || (hoverId && (a.id === hoverId || b.id === hoverId));
      const dim = (matches && !(visible(a.id) && visible(b.id))) || (selectedId && !lit);
      context.globalAlpha = lit ? 0.9 : dim ? 0.06 : link.kind === 'cites' ? 0.28 : 0.6;
      context.strokeStyle = link.kind === 'corrects' ? colors.danger : link.kind === 'cites' ? colors.strong : colors.accent;
      context.lineWidth = (link.kind === 'cites' ? 1 : 1.5) * (lit ? 1.5 : 1);
      context.beginPath();
      context.moveTo(a.x * k + x, a.y * k + y);
      context.lineTo(b.x * k + x, b.y * k + y);
      context.stroke();
    }

    const labels = [];
    const scale = Math.min(1.6, Math.max(0.55, k));
    for (const node of layout.nodes) {
      const sx = node.x * k + x;
      const sy = node.y * k + y;
      const r = Math.max(2.5, node.r * scale);
      if (sx < -r - 160 || sy < -r - 20 || sx > size.width + r + 160 || sy > size.height + r + 20) continue;
      const dim = !visible(node.id) || (selectedId && !focus(node.id));
      context.globalAlpha = dim ? 0.14 : 1;
      context.beginPath();
      context.arc(sx, sy, r, 0, Math.PI * 2);
      if (node.type === 'entry') {
        context.fillStyle = colors.canvas;
        context.fill();
        context.globalAlpha = (dim ? 0.14 : 1) * (0.4 + 0.6 * (recency.get(node.id) ?? 1));
        context.fillStyle = colors.accent;
        context.fill();
      } else {
        context.fillStyle = colors.canvas;
        context.fill();
        context.strokeStyle = colors.muted;
        context.lineWidth = 1.5;
        context.stroke();
      }
      if (node.id === selectedId || node.id === hoverId) {
        context.globalAlpha = 1;
        context.strokeStyle = colors.text;
        context.lineWidth = node.id === selectedId ? 2 : 1.2;
        context.beginPath();
        context.arc(sx, sy, r + 3.5, 0, Math.PI * 2);
        context.stroke();
      }
      if (dim) continue;
      // Hubs are named first; every other title appears as the reader zooms in.
      const priority = node.id === selectedId ? 0 : node.id === hoverId ? 1 : neighbors.has(node.id) ? 2 : matches?.has(node.id) ? 3 : 10 - Math.min(6, node.degree / 2);
      const earned = priority < 4 || hubs.has(node.id) || node.r * k >= 9 || k >= 1.4;
      if (earned) labels.push({ node, sx, sy, r, priority, strong: priority < 2 });
    }

    // Labels sit centred under their node, placed by priority and skipped where
    // they would overlap, so titles stay legible at any zoom.
    context.globalAlpha = 1;
    context.textAlign = 'center';
    context.textBaseline = 'top';
    labels.sort((a, b) => a.priority - b.priority);
    const placed = [];
    for (const { node, sx, sy, r, strong } of labels) {
      if (placed.length >= MAX_LABELS) break;
      context.font = `${strong ? 600 : 500} ${strong ? 12.5 : 11.5}px ${colors.font}`;
      const label = clipLabel(context, titleOf(node.id), strong ? 280 : 150);
      const width = context.measureText(label).width + 8;
      const box = { x: sx - width / 2, y: sy + r + 3, w: width, h: 17 };
      if (!strong && placed.some((b) => box.x < b.x + b.w && b.x < box.x + box.w && box.y < b.y + b.h && b.y < box.y + box.h)) continue;
      placed.push(box);
      context.globalAlpha = 0.88;
      context.fillStyle = colors.canvas;
      context.fillRect(box.x, box.y, box.w, box.h);
      context.globalAlpha = 1;
      context.fillStyle = node.type === 'entry' || strong ? colors.text : colors.muted;
      context.fillText(label, sx, box.y + 2);
    }
  }, [layout, matches, selectedId, neighbors, hoverId, size, recency, titleOf, hubs]);

  const schedule = useCallback(() => {
    if (!frameRef.current) frameRef.current = requestAnimationFrame(draw);
  }, [draw]);

  const fit = useCallback(() => {
    const { minX, maxX, minY, maxY } = layout.bounds;
    if (!size.width || !layout.nodes.length) return;
    const pad = 40;
    const width = Math.max(200, size.width - rightInset);
    const k = Math.max(MIN_ZOOM, Math.min(1.6, (width - pad * 2) / Math.max(1, maxX - minX), (size.height - pad * 2) / Math.max(1, maxY - minY)));
    viewRef.current = { k, x: width / 2 - ((minX + maxX) / 2) * k, y: size.height / 2 - ((minY + maxY) / 2) * k };
    schedule();
  }, [layout, size, rightInset, schedule]);

  useEffect(() => {
    const element = wrapRef.current;
    if (!element) return undefined;
    const observer = new ResizeObserver(([entry]) => {
      const { width, height } = entry.contentRect;
      setSize({ width: Math.round(width), height: Math.round(height) });
    });
    observer.observe(element);
    return () => observer.disconnect();
  }, []);

  useEffect(() => {
    const canvas = canvasRef.current;
    if (!canvas) return;
    const ratio = window.devicePixelRatio || 1;
    canvas.width = Math.round(size.width * ratio);
    canvas.height = Math.round(size.height * ratio);
    canvas.style.width = `${size.width}px`;
    canvas.style.height = `${size.height}px`;
  }, [size]);

  // Fit when the first pages arrive and again once loading finishes; later
  // changes keep the reader's view.
  const nodeCount = layout.nodes.length;
  useEffect(() => {
    if (!size.width || !nodeCount) return;
    if (!fittedRef.current || fittedRef.current === 'partial') {
      fittedRef.current = layout.complete ? true : 'partial';
      fit();
    }
  }, [fit, nodeCount, layout.complete, size.width]);

  useEffect(() => {
    schedule();
  }, [schedule]);

  // A note chosen from the reader or the list is brought into view.
  useEffect(() => {
    const node = layout.nodes[layout.index.get(selectedId)];
    if (!node || !size.height) return;
    const view = viewRef.current;
    const width = Math.max(200, size.width - rightInset);
    const sy = node.y * view.k + view.y;
    const sx = node.x * view.k + view.x;
    if (sy < 30 || sy > size.height - 60 || sx < 30 || sx > width - 30) {
      viewRef.current = { ...view, x: width / 2 - node.x * view.k, y: size.height / 2 - node.y * view.k };
      schedule();
    }
  }, [selectedId, layout, size, rightInset, schedule]);

  useEffect(() => () => cancelAnimationFrame(frameRef.current), []);

  const nodeAt = (clientX, clientY) => {
    const rect = canvasRef.current.getBoundingClientRect();
    const { x, y, k } = viewRef.current;
    const px = (clientX - rect.left - x) / k;
    const py = (clientY - rect.top - y) / k;
    const scale = Math.min(1.6, Math.max(0.55, k));
    let best = null;
    let bestDistance = Infinity;
    for (const node of layout.nodes) {
      const d = Math.hypot(node.x - px, node.y - py);
      if (d < bestDistance && d <= (Math.max(2.5, node.r * scale) + 6) / k) {
        best = node;
        bestDistance = d;
      }
    }
    return best;
  };

  const zoomAt = (factor, cx = (size.width - rightInset) / 2, cy = size.height / 2) => {
    const view = viewRef.current;
    const k = Math.max(MIN_ZOOM, Math.min(MAX_ZOOM, view.k * factor));
    viewRef.current = { k, x: cx - ((cx - view.x) / view.k) * k, y: cy - ((cy - view.y) / view.k) * k };
    schedule();
  };

  useEffect(() => {
    const canvas = canvasRef.current;
    if (!canvas) return undefined;
    const onWheel = (event) => {
      event.preventDefault();
      const rect = canvas.getBoundingClientRect();
      // The wheel zooms around the pointer, as in other graph tools; a
      // sideways trackpad swipe pans.
      if (!event.ctrlKey && Math.abs(event.deltaX) > Math.abs(event.deltaY)) {
        viewRef.current = { ...viewRef.current, x: viewRef.current.x - event.deltaX };
        schedule();
        return;
      }
      zoomAt(Math.exp(-event.deltaY * (event.ctrlKey ? 0.01 : 0.002)), event.clientX - rect.left, event.clientY - rect.top);
    };
    canvas.addEventListener('wheel', onWheel, { passive: false });
    return () => canvas.removeEventListener('wheel', onWheel);
  });

  return (
    <div className="cc-daily-notes-network" ref={wrapRef}>
      <canvas
        ref={canvasRef}
        role="img"
        aria-label={`笔记图谱：${model.entries.size} 条笔记，${model.sources.size} 个来源会话。可在列表视图中逐条阅读。`}
        data-cursor={hoverId ? 'pointer' : 'grab'}
        onPointerDown={(event) => {
          event.currentTarget.setPointerCapture(event.pointerId);
          dragRef.current = { x: event.clientX, y: event.clientY, view: viewRef.current, moved: false };
        }}
        onPointerMove={(event) => {
          const drag = dragRef.current;
          if (drag) {
            const dx = event.clientX - drag.x;
            const dy = event.clientY - drag.y;
            if (Math.abs(dx) + Math.abs(dy) > 3) drag.moved = true;
            viewRef.current = { ...drag.view, x: drag.view.x + dx, y: drag.view.y + dy };
            schedule();
            return;
          }
          setHoverId(nodeAt(event.clientX, event.clientY)?.id || '');
        }}
        onPointerUp={(event) => {
          const drag = dragRef.current;
          dragRef.current = null;
          if (drag && !drag.moved) onSelect(nodeAt(event.clientX, event.clientY)?.id || '');
        }}
        onPointerLeave={() => setHoverId('')}
      />
      <div className="cc-daily-notes-network-legend" aria-hidden="true">
        <span><i className="is-entry" />笔记（越淡越旧）</span>
        <span><i className="is-source" />来源会话</span>
        <span><i className="is-link" />笔记关系</span>
        <span><i className="is-correct" />更正</span>
      </div>
      <div className="cc-daily-notes-network-controls" style={{ right: rightInset + 12 }}>
        <button type="button" className="oc-btn oc-btn-default" aria-label="放大" title="放大" onClick={() => zoomAt(1.25)}><Plus size={15} aria-hidden="true" /></button>
        <button type="button" className="oc-btn oc-btn-default" aria-label="缩小" title="缩小" onClick={() => zoomAt(0.8)}><Minus size={15} aria-hidden="true" /></button>
        <button type="button" className="oc-btn oc-btn-default" aria-label="显示全部" title="显示全部" onClick={fit}><Maximize2 size={15} aria-hidden="true" /></button>
      </div>
    </div>
  );
}
