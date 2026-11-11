import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Maximize2, Minus, Plus } from 'lucide-react';
import { formatDay } from './daily-notes-model';
import { DAY_SPACING } from './daily-notes-layout';

const MIN_ZOOM = 0.15;
const MAX_ZOOM = 3;

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
    accentText: read('--cc-accent-text', '#147c65'),
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

export default function DailyNotesNetwork({ layout, model, matches, selectedId, onSelect }) {
  const wrapRef = useRef(null);
  const canvasRef = useRef(null);
  const viewRef = useRef({ x: 0, y: 0, k: 1 });
  const fittedRef = useRef(false);
  const dragRef = useRef(null);
  const frameRef = useRef(0);
  const [size, setSize] = useState({ width: 0, height: 0 });
  const [hoverId, setHoverId] = useState('');

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

  const draw = useCallback(() => {
    frameRef.current = 0;
    const canvas = canvasRef.current;
    if (!canvas || !size.width || !size.height) return;
    const ratio = window.devicePixelRatio || 1;
    const context = canvas.getContext('2d');
    const colors = palette(canvas);
    const { x, y, k } = viewRef.current;
    context.setTransform(ratio, 0, 0, ratio, 0, 0);
    context.fillStyle = colors.canvas;
    context.fillRect(0, 0, size.width, size.height);

    // Day rows: the vertical axis is time, newest at the top.
    context.font = `500 11px ${colors.font}`;
    context.textAlign = 'left';
    context.textBaseline = 'bottom';
    const step = Math.max(1, Math.ceil(28 / (DAY_SPACING * k)));
    layout.days.forEach(({ day, y: dayY }, i) => {
      const sy = (dayY - DAY_SPACING / 2) * k + y;
      if (sy < -20 || sy > size.height + 20) return;
      context.strokeStyle = colors.rule;
      context.lineWidth = 1;
      context.beginPath();
      context.moveTo(0, sy);
      context.lineTo(size.width, sy);
      context.stroke();
      if (i % step === 0) {
        context.fillStyle = colors.muted;
        context.fillText(formatDay(day).split(' · ')[0], 10, sy + 16);
      }
    });

    const visible = (id) => !matches || matches.has(id) || id === selectedId;
    const focus = (id) => id === selectedId || neighbors.has(id) || id === hoverId;
    context.lineCap = 'round';
    for (const link of layout.links) {
      const a = layout.nodes[link.source];
      const b = layout.nodes[link.target];
      const lit = selectedId && (a.id === selectedId || b.id === selectedId);
      const dim = (matches && !(visible(a.id) && visible(b.id))) || (selectedId && !lit);
      context.globalAlpha = dim ? 0.08 : link.kind === 'cites' ? 0.35 : 0.75;
      context.strokeStyle = link.kind === 'corrects' ? colors.danger : link.kind === 'cites' ? colors.strong : colors.accent;
      context.lineWidth = (link.kind === 'cites' ? 1 : 1.6) * (lit ? 1.6 : 1);
      context.beginPath();
      context.moveTo(a.x * k + x, a.y * k + y);
      context.lineTo(b.x * k + x, b.y * k + y);
      context.stroke();
    }

    const labels = [];
    for (const node of layout.nodes) {
      const sx = node.x * k + x;
      const sy = node.y * k + y;
      const r = Math.max(2.5, node.r * Math.min(1.4, Math.max(0.6, k)));
      if (sx < -r || sy < -r || sx > size.width + r || sy > size.height + r) continue;
      const dim = !visible(node.id) || (selectedId && !focus(node.id));
      context.globalAlpha = dim ? 0.15 : 1;
      context.beginPath();
      context.arc(sx, sy, r, 0, Math.PI * 2);
      if (node.type === 'entry') {
        context.fillStyle = colors.accent;
        context.fill();
      } else {
        context.fillStyle = colors.canvas;
        context.fill();
        context.strokeStyle = colors.muted;
        context.lineWidth = 1.4;
        context.stroke();
      }
      if (node.id === selectedId) {
        context.globalAlpha = 1;
        context.strokeStyle = colors.text;
        context.lineWidth = 2;
        context.beginPath();
        context.arc(sx, sy, r + 4, 0, Math.PI * 2);
        context.stroke();
      }
      if (!dim && (node.type === 'entry' || focus(node.id))) {
        const priority = node.id === selectedId ? 0 : node.id === hoverId ? 1 : neighbors.has(node.id) ? 2 : matches?.has(node.id) ? 3 : 4 + 1 / node.r;
        labels.push({ node, sx, sy, r, priority, strong: priority < 2 });
      }
    }

    // Labels are placed by priority and skipped where they would overlap,
    // so titles stay legible at any zoom.
    context.globalAlpha = 1;
    context.textAlign = 'left';
    context.textBaseline = 'middle';
    labels.sort((a, b) => a.priority - b.priority);
    const placed = [];
    for (const { node, sx, sy, r, strong } of labels.slice(0, 160)) {
      if (placed.length >= 48) break;
      const text = node.type === 'entry'
        ? model.entries.get(node.id)?.title || ''
        : model.sources.get(node.id)?.label || '';
      context.font = `${strong ? 600 : 500} 12px ${colors.font}`;
      const overlaps = (box) => placed.some((b) => box.x < b.x + b.w && b.x < box.x + box.w && box.y < b.y + b.h && b.y < box.y + box.h);
      // Right of the node first, then left; a label with no free side is skipped.
      let label = '';
      let box = null;
      for (const side of size.width - sx - r - 20 >= 90 ? ['right', 'left'] : ['left']) {
        const room = side === 'right' ? size.width - sx - r - 20 : sx - r - 20;
        if (room < 48) continue;
        const candidate = clipLabel(context, text, Math.min(strong ? 300 : 170, room));
        const width = context.measureText(candidate).width + 8;
        const next = { x: side === 'right' ? sx + r + 4 : sx - r - 4 - width, y: sy - 9, w: width, h: 18 };
        if (strong || !overlaps(next)) { label = candidate; box = next; break; }
      }
      if (!box) continue;
      placed.push(box);
      context.fillStyle = colors.canvas;
      context.globalAlpha = 0.9;
      context.fillRect(box.x, box.y, box.w, box.h);
      context.globalAlpha = 1;
      context.fillStyle = node.type === 'entry' ? colors.text : colors.muted;
      context.fillText(label, box.x + 4, sy);
    }
  }, [layout, model, matches, selectedId, neighbors, hoverId, size]);

  const schedule = useCallback(() => {
    if (!frameRef.current) frameRef.current = requestAnimationFrame(draw);
  }, [draw]);

  // Fit the width and start at the newest day; the timeline scrolls down.
  const fit = useCallback(() => {
    const { minX, maxX } = layout.bounds;
    if (!size.width || !layout.nodes.length) return;
    const pad = 56;
    const k = Math.max(MIN_ZOOM, Math.min(1, (size.width - pad * 2) / Math.max(1, maxX - minX), size.height / (DAY_SPACING * 5.5)));
    viewRef.current = {
      k,
      x: size.width / 2 - ((minX + maxX) / 2) * k + 20,
      y: (DAY_SPACING / 2) * k + 8,
    };
    schedule();
  }, [layout, size, schedule]);

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

  // Fit once when the first pages arrive; later pages keep the reader's view.
  useEffect(() => {
    if (!fittedRef.current && size.width && layout.nodes.length) {
      fittedRef.current = true;
      fit();
    }
  }, [fit, layout, size]);

  useEffect(() => {
    schedule();
  }, [schedule]);

  // A note chosen from the reader or the list is brought into view.
  useEffect(() => {
    const node = layout.nodes[layout.index.get(selectedId)];
    if (!node || !size.height) return;
    const view = viewRef.current;
    const sy = node.y * view.k + view.y;
    const sx = node.x * view.k + view.x;
    if (sy < 40 || sy > size.height - 40 || sx < 20 || sx > size.width - 20) {
      viewRef.current = { ...view, x: size.width / 2 - node.x * view.k, y: size.height / 3 - node.y * view.k };
      schedule();
    }
  }, [selectedId, layout, size, schedule]);

  useEffect(() => () => cancelAnimationFrame(frameRef.current), []);

  const nodeAt = (clientX, clientY) => {
    const rect = canvasRef.current.getBoundingClientRect();
    const { x, y, k } = viewRef.current;
    const px = (clientX - rect.left - x) / k;
    const py = (clientY - rect.top - y) / k;
    let best = null;
    let bestDistance = Infinity;
    for (const node of layout.nodes) {
      const d = Math.hypot(node.x - px, node.y - py);
      if (d < bestDistance && d <= node.r + 6 / k) {
        best = node;
        bestDistance = d;
      }
    }
    return best;
  };

  const zoomAt = (factor, cx = size.width / 2, cy = size.height / 2) => {
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
      // Pinch and Ctrl+wheel zoom; a plain wheel scrolls the timeline.
      if (event.ctrlKey || event.metaKey) {
        zoomAt(Math.exp(-event.deltaY * 0.01), event.clientX - rect.left, event.clientY - rect.top);
      } else {
        viewRef.current = { ...viewRef.current, x: viewRef.current.x - event.deltaX, y: viewRef.current.y - event.deltaY };
        schedule();
      }
    };
    canvas.addEventListener('wheel', onWheel, { passive: false });
    return () => canvas.removeEventListener('wheel', onWheel);
  });

  const hoverLabel = hoverId
    ? model.entries.get(hoverId)?.title || model.sources.get(hoverId)?.label || ''
    : '';

  return (
    <div className="cc-daily-notes-network" ref={wrapRef}>
      <canvas
        ref={canvasRef}
        role="img"
        aria-label={`笔记网络：${model.entries.size} 条笔记，${layout.days.length} 天。可在列表视图中逐条阅读。`}
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
          const node = nodeAt(event.clientX, event.clientY);
          setHoverId(node?.id || '');
        }}
        onPointerUp={(event) => {
          const drag = dragRef.current;
          dragRef.current = null;
          if (drag && !drag.moved) {
            const node = nodeAt(event.clientX, event.clientY);
            if (node?.type === 'entry') onSelect(node.id);
          }
        }}
        onPointerLeave={() => setHoverId('')}
      />
      <div className="cc-daily-notes-network-legend" aria-hidden="true">
        <span><i className="is-entry" />笔记</span>
        <span><i className="is-source" />来源会话</span>
        <span><i className="is-link" />笔记关系</span>
        <span><i className="is-correct" />更正</span>
      </div>
      {hoverLabel && <div className="cc-daily-notes-network-hover" aria-hidden="true">{hoverLabel}</div>}
      <div className="cc-daily-notes-network-controls">
        <button type="button" className="oc-btn oc-btn-default" aria-label="放大" title="放大" onClick={() => zoomAt(1.25)}><Plus size={15} aria-hidden="true" /></button>
        <button type="button" className="oc-btn oc-btn-default" aria-label="缩小" title="缩小" onClick={() => zoomAt(0.8)}><Minus size={15} aria-hidden="true" /></button>
        <button type="button" className="oc-btn oc-btn-default" aria-label="回到最新一天" title="回到最新一天" onClick={fit}><Maximize2 size={15} aria-hidden="true" /></button>
      </div>
    </div>
  );
}
