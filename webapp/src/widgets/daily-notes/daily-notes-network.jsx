import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Maximize2, Minus, Plus } from 'lucide-react';
import { NOTE_SPACING, regionAt } from './daily-notes-layout';

const MIN_ZOOM = 0.15;
const MAX_ZOOM = 4;
const MAX_LABELS = 60;
// Below this on-screen spacing between notes, each cluster draws as one disc.
const OVERVIEW_SPACING = 6;
// Zoomed out, only clusters this large on screen are named, at most this many.
const OVERVIEW_LABEL_RADIUS = 12;
const OVERVIEW_LABELS = 24;

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

// Hues for the clusters after the first, which takes the accent. Each is
// used only if it sits clear of the accent, so the brand hue stays unique.
const CLUSTER_HUES = [255, 62, 322, 205, 128, 28, 290, 95];

// OKLab hue and lightness of any CSS colour, normalised through the canvas.
function oklch(context, color) {
  context.fillStyle = '#000';
  context.fillStyle = color;
  const channels = context.fillStyle.startsWith('#')
    ? [1, 3, 5].map((i) => parseInt(context.fillStyle.slice(i, i + 2), 16))
    : context.fillStyle.match(/[\d.]+/g).slice(0, 3).map(Number);
  const [r, g, b] = channels.map((v) => {
    const c = v / 255;
    return c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
  });
  const l = Math.cbrt(0.4122214708 * r + 0.5363325363 * g + 0.0514459929 * b);
  const m = Math.cbrt(0.2119034982 * r + 0.6806995451 * g + 0.1073969566 * b);
  const s = Math.cbrt(0.0883024619 * r + 0.2817188376 * g + 0.6299787005 * b);
  const lightness = 0.2104542553 * l + 0.793617785 * m - 0.0040720468 * s;
  const a = 1.9779984951 * l - 2.428592205 * m + 0.4505937099 * s;
  const bb = 0.0259040371 * l + 0.7827717662 * m - 0.808675766 * s;
  return { lightness, hue: ((Math.atan2(bb, a) * 180) / Math.PI + 360) % 360 };
}

function clusterColors(context, colors) {
  const dark = oklch(context, colors.canvas).lightness < 0.5;
  const accent = oklch(context, colors.accent).hue;
  const hues = CLUSTER_HUES.filter((hue) => Math.min(Math.abs(hue - accent), 360 - Math.abs(hue - accent)) > 35);
  return [colors.accent, ...hues.map((hue) => `oklch(${dark ? 0.76 : 0.66} ${dark ? 0.11 : 0.12} ${hue})`)];
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
  const [hover, setHover] = useState({ id: '', region: -1 });
  const hoverId = hover.id;
  // Below the narrow breakpoint the reader sits under the graph, not beside it.
  const rightInset = size.width > 820 ? requestedInset : 0;

  // What a selected (or hovered) note or source lights up: the note, its
  // linked notes and its source regions; or every note citing the source.
  const focusOf = useCallback((id) => {
    const notes = new Set();
    const regions = new Set();
    const entry = model.entries.get(id);
    if (entry) {
      notes.add(id);
      for (const link of entry.links) notes.add(link.from === id ? link.to : link.from);
      for (const key of entry.sources) if (layout.regionIndex.has(key)) regions.add(layout.regionIndex.get(key));
    } else if (model.sources.has(id)) {
      for (const noteId of model.sources.get(id).entries) notes.add(noteId);
      if (layout.regionIndex.has(id)) regions.add(layout.regionIndex.get(id));
    }
    return { notes, regions };
  }, [layout, model]);
  const selection = useMemo(() => (selectedId ? focusOf(selectedId) : null), [focusOf, selectedId]);

  // Per region: whether it stays lit under the search and selection, and how
  // many of its notes match the search. Computed once per change, not per frame.
  const regionState = useMemo(() => layout.regions.map((region, i) => {
    let matched = 0;
    let selected = false;
    for (const n of region.members) {
      const id = layout.nodes[n].id;
      if (matches?.has(id)) matched += 1;
      if (selection?.notes.has(id)) selected = true;
    }
    return { matched, shown: (!matches || matched > 0) && (!selection || selected || selection.regions.has(i)) };
  }), [layout, matches, selection]);

  // Links inside a cluster fade as the cluster gets busier, so a cluster with
  // thousands of notes keeps its shape instead of turning into a mesh.
  const insideAlpha = useMemo(() => {
    const counts = new Array(layout.regions.length).fill(0);
    for (const { source, target } of layout.links) {
      const a = layout.nodes[source].region;
      if (a === layout.nodes[target].region) counts[a] += 1;
    }
    return counts.map((count) => 0.55 / Math.sqrt(Math.max(1, count / 40)));
  }, [layout]);

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
    const focus = hoverId && hoverId !== selectedId ? focusOf(hoverId) : null;
    const noteDim = (id) => !visible(id) || (selection && !selection.notes.has(id));
    const lit = (i) => selection?.regions.has(i) || focus?.regions.has(i) || hover.region === i;
    // Colour names the cluster: the largest takes the accent; packed
    // neighbours never share a colour; the shared "other" cluster is grey.
    const hues = clusterColors(context, colors);
    const tone = layout.regions.map((region) => (region.other ? colors.muted : hues[region.hue % hues.length]));
    const toScreen = (px, py) => [px * k + x, py * k + y];
    const onScreen = (sx, sy, r) => sx > -r - 160 && sy > -r - 20 && sx < size.width + r + 160 && sy < size.height + r + 20;
    // A curve is skipped when its bounding box, bulge included, is off screen.
    const curve = (ax, ay, bx, by) => {
      const [sx, sy] = toScreen(ax, ay);
      const [tx, ty] = toScreen(bx, by);
      const bulge = 0.16 * (Math.abs(tx - sx) + Math.abs(ty - sy));
      if (Math.max(sx, tx) < -bulge || Math.min(sx, tx) > size.width + bulge || Math.max(sy, ty) < -bulge || Math.min(sy, ty) > size.height + bulge) return;
      context.beginPath();
      context.moveTo(sx, sy);
      context.quadraticCurveTo((sx + tx) / 2 - (ty - sy) * 0.16, (sy + ty) / 2 + (tx - sx) * 0.16, tx, ty);
      context.stroke();
    };
    const overview = k * NOTE_SPACING < OVERVIEW_SPACING;
    const labels = [];
    context.lineCap = 'round';

    if (overview) {
      // Zoomed out, a cluster is one disc and the links between two clusters
      // are one line, so the drawing scales with clusters, not notes.
      for (const { a, b, count } of layout.regionLinks) {
        const on = lit(a) || lit(b);
        const shown = regionState[a].shown && regionState[b].shown;
        context.globalAlpha = on ? 0.6 : shown && !selection ? 0.16 : 0.04;
        context.strokeStyle = on ? colors.text : colors.muted;
        context.lineWidth = Math.min(7, 0.8 + Math.log2(count) * 1.1);
        curve(layout.regions[a].x, layout.regions[a].y, layout.regions[b].x, layout.regions[b].y);
      }
      for (const [i, region] of layout.regions.entries()) {
        const [sx, sy] = toScreen(region.x, region.y);
        const r = Math.max(2, region.r * k * 0.9);
        if (!onScreen(sx, sy, r)) continue;
        const newest = recency.get(layout.nodes[region.members[0]].id) ?? 1;
        context.beginPath();
        context.arc(sx, sy, r, 0, Math.PI * 2);
        context.globalAlpha = 1;
        context.fillStyle = colors.canvas;
        context.fill();
        // A light wash with a fine rim: clusters read as areas, not buttons.
        const weight = (regionState[i].shown ? 1 : 0.15) * (region.other ? 0.6 : 1);
        context.globalAlpha = weight * (0.14 + 0.36 * newest);
        context.fillStyle = tone[i];
        context.fill();
        context.globalAlpha = weight * (0.45 + 0.4 * newest);
        context.strokeStyle = tone[i];
        context.lineWidth = 1;
        context.stroke();
        if (lit(i)) {
          context.globalAlpha = 1;
          context.strokeStyle = colors.text;
          context.lineWidth = 1.5;
          context.beginPath();
          context.arc(sx, sy, r + 3, 0, Math.PI * 2);
          context.stroke();
        }
      }
      // The selected or hovered note is still marked inside its disc.
      for (const id of [selectedId, hoverId]) {
        const node = layout.nodes[layout.index.get(id)];
        if (!node) continue;
        const [sx, sy] = toScreen(node.x, node.y);
        context.globalAlpha = 1;
        context.fillStyle = colors.text;
        context.beginPath();
        context.arc(sx, sy, 3.5, 0, Math.PI * 2);
        context.fill();
        labels.push({ title: titleOf(id), sx, at: [sy + 7], priority: id === selectedId ? 0 : 1, strong: true });
      }
    } else {
      // A cluster in focus gets a soft ground, which also shows the click target.
      for (const [i, region] of layout.regions.entries()) {
        if (!lit(i)) continue;
        const [sx, sy] = toScreen(region.x, region.y);
        context.globalAlpha = 0.09;
        context.fillStyle = tone[i];
        context.beginPath();
        context.arc(sx, sy, (region.r + 6) * k, 0, Math.PI * 2);
        context.fill();
      }
      // Links bow gently; within a cluster they take its colour, between
      // clusters they recede to a hairline so they connect without covering.
      const selectedNote = layout.index.get(selectedId);
      const hoveredNote = layout.index.get(hoverId);
      const across = layout.regions.length > 40 ? 0.07 : 0.16;
      for (const link of layout.links) {
        const a = layout.nodes[link.source];
        const b = layout.nodes[link.target];
        const on = link.source === selectedNote || link.target === selectedNote || link.source === hoveredNote || link.target === hoveredNote;
        const dim = (matches && !(visible(a.id) && visible(b.id))) || (selection && !on);
        const inside = a.region === b.region;
        context.globalAlpha = on ? 0.95 : dim ? 0.05 : inside ? insideAlpha[a.region] : across;
        context.strokeStyle = link.kind === 'corrects' ? colors.danger : inside ? tone[a.region] : on ? colors.text : colors.muted;
        context.lineWidth = on ? 1.8 : 1.1;
        curve(a.x, a.y, b.x, b.y);
      }
      // Citations of another session run dashed to that cluster's edge.
      context.setLineDash([2, 4]);
      for (const { source, region: regionIndex } of layout.bridges) {
        const node = layout.nodes[source];
        const region = layout.regions[regionIndex];
        const on = source === selectedNote || source === hoveredNote || region.key === selectedId || region.key === hoverId;
        if (!on && (selection || (matches && !visible(node.id)))) continue;
        const dx = node.x - region.x;
        const dy = node.y - region.y;
        const d = Math.hypot(dx, dy) || 1;
        context.globalAlpha = on ? 0.85 : across * 1.6;
        context.strokeStyle = on ? colors.text : colors.muted;
        context.lineWidth = 1;
        curve(node.x, node.y, region.x + (dx / d) * region.r, region.y + (dy / d) * region.r);
      }
      context.setLineDash([]);

      const scale = Math.min(1.6, Math.max(0.55, k));
      for (const [i, region] of layout.regions.entries()) {
        const [rx, ry] = toScreen(region.x, region.y);
        if (!onScreen(rx, ry, region.r * k)) continue;
        for (const n of region.members) {
          const node = layout.nodes[n];
          const [sx, sy] = toScreen(node.x, node.y);
          const r = Math.max(2, node.r * scale);
          if (!onScreen(sx, sy, r)) continue;
          const dim = noteDim(node.id);
          context.globalAlpha = dim ? 0.12 : 1;
          context.beginPath();
          context.arc(sx, sy, r, 0, Math.PI * 2);
          context.fillStyle = colors.canvas;
          context.fill();
          // The shared "other" cluster is quieter than the named ones.
          context.globalAlpha = (dim ? 0.12 : 1) * (region.other ? 0.55 : 1) * (0.32 + 0.68 * (recency.get(node.id) ?? 1));
          context.fillStyle = tone[i];
          context.fill();
          if (node.id === selectedId || node.id === hoverId) {
            context.globalAlpha = 1;
            context.strokeStyle = colors.text;
            context.lineWidth = node.id === selectedId ? 2 : 1.2;
            context.beginPath();
            context.arc(sx, sy, r + 3.5, 0, Math.PI * 2);
            context.stroke();
          }
          if (dim) continue;
          // Note titles appear once zoomed in, or when the note is in focus.
          const priority = node.id === selectedId ? 0 : node.id === hoverId ? 1 : selection?.notes.has(node.id) ? 2 : matches?.has(node.id) ? 3 : 10 - Math.min(4, node.degree);
          if (priority < 4 || k >= 1.5) labels.push({ title: titleOf(node.id), sx, at: [sy + r + 3], priority: priority + 2, strong: priority < 2 });
        }
      }
    }

    // Cluster names lead at the overview; the largest claim space first.
    // While searching, the count reads matches / notes.
    for (const [i, region] of layout.regions.entries()) {
      if (!regionState[i].shown) continue;
      const [sx, sy] = toScreen(region.x, region.y);
      const r = region.r * k * (overview ? 0.9 : 1);
      const on = lit(i);
      if (!onScreen(sx, sy, r) || (!on && r < (overview ? OVERVIEW_LABEL_RADIUS : 7))) continue;
      const count = matches ? `${regionState[i].matched}/${region.count}` : String(region.count);
      // Above the cluster, or below it when a neighbour's name is there. A
      // cluster zoomed in past the top edge keeps its name pinned there.
      const cx = (size.width - rightInset) / 2;
      const pinned = sy - r < 0 && Math.hypot(cx - sx, size.height / 2 - sy) < r;
      if (pinned) labels.push({ title: region.label, count, color: tone[i], sx: cx, at: [10], priority: -1, strong: true });
      else labels.push({ title: region.label, count, color: tone[i], sx, at: [sy - r - (overview ? 20 : 16), sy + r + (overview ? 3 : -2)], priority: on ? 0 : 1 + 1 / region.count, strong: on });
    }

    // Labels sit centred on their anchor, placed by priority and skipped where
    // they would overlap, so titles stay legible at any zoom.
    context.globalAlpha = 1;
    context.textAlign = 'left';
    context.textBaseline = 'top';
    labels.sort((a, b) => a.priority - b.priority);
    let clusterBudget = overview ? OVERVIEW_LABELS : MAX_LABELS;
    const placed = [];
    const free = (box) => !placed.some((b) => box.x < b.x + b.w && b.x < box.x + box.w && box.y < b.y + b.h && b.y < box.y + box.h);
    for (const { title, count, color, sx, at, strong } of labels) {
      if (placed.length >= MAX_LABELS) break;
      const cluster = count !== undefined;
      if (cluster && !strong && clusterBudget <= 0) continue;
      const nameFont = `${strong || cluster ? 600 : 500} ${strong ? 12.5 : cluster ? 12 : 11.5}px ${colors.font}`;
      context.font = nameFont;
      const label = clipLabel(context, title, strong ? 280 : 170);
      const nameWidth = context.measureText(label).width;
      context.font = `500 11px ${colors.font}`;
      const countWidth = cluster ? context.measureText(count).width + 6 : 0;
      const lead = cluster ? 13 : 0;
      const width = lead + nameWidth + countWidth + 10;
      const boxes = at.map((y) => ({ x: sx - width / 2, y, w: width, h: 18 }));
      const box = strong ? boxes[0] : boxes.find(free);
      if (!box) continue;
      placed.push(box);
      if (cluster) clusterBudget -= 1;
      context.globalAlpha = cluster ? 0.92 : 0.88;
      context.fillStyle = colors.canvas;
      context.fillRect(box.x, box.y, box.w, box.h);
      context.globalAlpha = 1;
      if (cluster) {
        context.fillStyle = color;
        context.beginPath();
        context.arc(box.x + 9, box.y + 9, 3.5, 0, Math.PI * 2);
        context.fill();
      }
      context.font = nameFont;
      context.fillStyle = colors.text;
      context.fillText(label, box.x + 5 + lead, box.y + 2.5);
      if (cluster) {
        context.font = `500 11px ${colors.font}`;
        context.fillStyle = colors.muted;
        context.fillText(count, box.x + 5 + lead + nameWidth + 6, box.y + 3.5);
      }
    }
  }, [layout, matches, selectedId, selection, regionState, insideAlpha, focusOf, hover, hoverId, size, rightInset, recency, titleOf]);

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

  // Colours come from theme variables, so a theme switch repaints the canvas.
  useEffect(() => {
    const observer = new MutationObserver(schedule);
    observer.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme', 'data-liquid-variant'] });
    return () => observer.disconnect();
  }, [schedule]);

  // A note chosen from the reader or the list is brought into view.
  useEffect(() => {
    const node = layout.nodes[layout.index.get(selectedId)] || layout.regions[layout.regionIndex.get(selectedId)];
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

  // The note under the pointer and the region around it. Zoomed out, only
  // the region counts. Only that region's notes are searched.
  const targetAt = (clientX, clientY) => {
    const rect = canvasRef.current.getBoundingClientRect();
    const { x, y, k } = viewRef.current;
    const px = (clientX - rect.left - x) / k;
    const py = (clientY - rect.top - y) / k;
    const region = regionAt(layout, px, py);
    if (region < 0) return { id: '', region: -1, overview: false };
    const overview = k * NOTE_SPACING < OVERVIEW_SPACING;
    const scale = Math.min(1.6, Math.max(0.55, k));
    let best = '';
    let bestDistance = Infinity;
    for (const n of overview ? [] : layout.regions[region].members) {
      const node = layout.nodes[n];
      const d = Math.hypot(node.x - px, node.y - py);
      if (d < bestDistance && d <= (Math.max(2, node.r * scale) + 5) / k) {
        best = node.id;
        bestDistance = d;
      }
    }
    return { id: best || layout.regions[region].key, region, overview };
  };

  // Clicking a cluster at the overview zooms in until its notes are readable.
  const zoomToRegion = (index) => {
    const region = layout.regions[index];
    const width = Math.max(200, size.width - rightInset);
    const k = Math.max((OVERVIEW_SPACING * 1.6) / NOTE_SPACING, Math.min(1.4, (0.7 * Math.min(width, size.height)) / (2 * region.r)));
    viewRef.current = { k, x: width / 2 - region.x * k, y: size.height / 2 - region.y * k };
    setHover({ id: '', region: -1 });
    schedule();
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
        aria-label={`笔记图谱：${model.entries.size} 条笔记，按来源会话分为 ${layout.regions.length} 组。可在列表视图中逐条阅读。`}
        data-cursor={hover.id || hover.region >= 0 ? 'pointer' : 'grab'}
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
          const target = targetAt(event.clientX, event.clientY);
          // Notes are clickable everywhere; regions only as an overview disc
          // or as a named source.
          const next = { id: target.id, region: target.overview || target.id ? target.region : -1 };
          setHover((current) => (current.id === next.id && current.region === next.region ? current : next));
        }}
        onPointerUp={(event) => {
          const drag = dragRef.current;
          dragRef.current = null;
          if (!drag || drag.moved) return;
          const target = targetAt(event.clientX, event.clientY);
          if (target.overview && target.region >= 0) zoomToRegion(target.region);
          else onSelect(target.id);
        }}
        onPointerLeave={() => setHover({ id: '', region: -1 })}
      />
      <div className="cc-daily-notes-network-legend" aria-hidden="true">
        <span><i className="is-entry" /><i className="is-entry is-second" />笔记（同色同来源，越淡越旧）</span>
        <span><i className="is-link" />笔记关系</span>
        <span><i className="is-correct" />更正</span>
        <span><i className="is-bridge" />引用其他会话</span>
      </div>
      <div className="cc-daily-notes-network-controls" style={{ right: rightInset + 12 }}>
        <button type="button" className="oc-btn oc-btn-default" aria-label="放大" title="放大" onClick={() => zoomAt(1.25)}><Plus size={15} aria-hidden="true" /></button>
        <button type="button" className="oc-btn oc-btn-default" aria-label="缩小" title="缩小" onClick={() => zoomAt(0.8)}><Minus size={15} aria-hidden="true" /></button>
        <button type="button" className="oc-btn oc-btn-default" aria-label="显示全部" title="显示全部" onClick={fit}><Maximize2 size={15} aria-hidden="true" /></button>
      </div>
    </div>
  );
}
