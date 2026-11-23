import { createElement, forwardRef } from 'react';
import { Icon } from 'lucide-react';

const DESKTOP_STROKE_WIDTH = 1.7;
const COMPACT_STROKE_LIMIT = 2.1;
const COMPACT_STROKE_SCALE = 32;

// Each viewBox keeps the visible silhouette at the established desktop size.
function desktopIcon(name, viewBox, iconNode) {
  const viewBoxSize = Number(viewBox.split(' ')[2]);
  const Component = forwardRef(function DesktopIcon({ className, size = 24, strokeWidth, ...props }, ref) {
    // Give compact SVGs more weight so their strokes survive subpixel rendering.
    const defaultStroke = Math.max(
      DESKTOP_STROKE_WIDTH,
      Math.min(COMPACT_STROKE_LIMIT, COMPACT_STROKE_SCALE / (Number(size) || 24)),
    );
    return createElement(Icon, {
      ref,
      iconNode,
      viewBox,
      size,
      strokeWidth: (strokeWidth ?? defaultStroke) * viewBoxSize / 24,
      className: 'lucide-' + name + (className ? ' ' + className : ''),
      ...props,
    });
  });
  Component.displayName = name;
  return Component;
}

export const Search = desktopIcon('search', '1.5250 1.5250 20.7000 20.7000', [
  ['circle', { cx: '10.25', cy: '10.25', r: '6.25', key: '0' }],
  ['path', { d: 'm14.75 14.75 5 5', key: '1' }],
]);

export const SidebarClose = desktopIcon('sidebar-close', '1.5 1.5 21 21', [
  ['path', { d: 'M9 6h11M9 12h11M9 18h11', key: '0' }],
  ['circle', { cx: '4', cy: '6', r: '0.85', fill: 'currentColor', stroke: 'none', key: '1' }],
  ['circle', { cx: '4', cy: '12', r: '0.85', fill: 'currentColor', stroke: 'none', key: '2' }],
  ['circle', { cx: '4', cy: '18', r: '0.85', fill: 'currentColor', stroke: 'none', key: '3' }],
]);

export const SidebarOpen = desktopIcon('sidebar-open', '1.5 1.5 21 21', [
  ['path', { d: 'M4 6h16M4 12h16M4 18h16', key: '0' }],
]);

export const Plus = desktopIcon('plus', '0.2250 0.2250 23.5500 23.5500', [
  ['path', { d: 'M12 5v14m-7-7h14', key: '0' }],
]);

export const Folder = desktopIcon('folder', '1.9636 2.4636 20.0727 20.0727', [
  ['path', { d: 'M3.5 7.5a1.5 1.5 0 0 1 1.5-1.5h5l2 2h7a1.5 1.5 0 0 1 1.5 1.5v8a1.5 1.5 0 0 1-1.5 1.5H5a1.5 1.5 0 0 1-1.5-1.5z', key: '0' }],
]);

export const MoreHorizontal = desktopIcon('more-horizontal', '1.1333 1.1333 21.7333 21.7333', [
  ['path', { d: 'M5 12h.01m6.99 0h.01m6.99 0h.01', strokeWidth: '2.54', key: '0' }],
]);

export const AssistantBadge = desktopIcon('assistant-badge', '1.6364 1.6364 20.7273 20.7273', [
  ['rect', { x: '3.25', y: '5.5', width: '17.5', height: '13', rx: '2.25', key: '0' }],
  ['circle', { cx: '9', cy: '10.5', r: '1.55', key: '1' }],
  ['path', { d: 'M6.5 15c0-1.1 1.15-1.75 2.5-1.75s2.5.65 2.5 1.75m2.5-4h3.7M14 14h2.5', key: '2' }],
]);

export const AppsGrid = desktopIcon('apps-grid', '1.375 1.375 21.25 21.25', [
  ['rect', { x: '3', y: '3', width: '7', height: '7', rx: '1', key: '0' }],
  ['rect', { x: '14', y: '3', width: '7', height: '7', rx: '1', key: '1' }],
  ['rect', { x: '3', y: '14', width: '7', height: '7', rx: '1', key: '2' }],
  ['rect', { x: '14', y: '14', width: '7', height: '7', rx: '1', key: '3' }],
]);

export const Package = desktopIcon('package', '2.1818 2.2318 19.6364 19.6364', [
  ['path', { d: 'm12 3.75 7.25 4.1v8.35L12 20.35l-7.25-4.15V7.85L12 3.75Z', key: '0' }],
  ['path', { d: 'm5.05 8 6.95 4 6.95-4M12 12v8.05m-3.6-14.2 7.2 4.1', key: '1' }],
]);

export const Settings = desktopIcon('settings', '2.1818 2.2318 19.6364 19.6364', [
  ['path', { d: 'M10.3 3.75h3.4l.55 2.4 2 .98 2.25-.76 1.7 2.95-1.72 1.65v2.16l1.72 1.65-1.7 2.95-2.25-.76-2 .98-.55 2.4h-3.4l-.55-2.4-2-.98-2.25.76-1.7-2.95 1.72-1.65v-2.16L3.8 9.32l1.7-2.95 2.25.76 2-.98z', key: '0' }],
  ['circle', { cx: '12', cy: '12', r: '2.55', key: '1' }],
]);

export const History = desktopIcon('history', '1.5000 1.5000 21.0000 21.0000', [
  ['circle', { cx: '12', cy: '12', r: '8', key: '0' }],
  ['path', { d: 'M12 7.75v4.5l3 1.75', key: '1' }],
]);

export const RefreshCw = desktopIcon('refresh-cw', '1.5000 1.5000 21.0000 21.0000', [
  ['path', { d: 'M18.75 8.5A7.5 7.5 0 0 0 5.5 7.65L4 9.25m0-4.5v4.5h4.5m-3.25 6.25a7.5 7.5 0 0 0 13.25.85L20 14.75m0 4.5v-4.5h-4.5', key: '0' }],
]);

export const Laptop = desktopIcon('laptop', '1.4423 1.5173 21.1154 21.1154', [
  ['rect', { x: '5', y: '5.75', width: '14', height: '10.5', rx: '1.5', key: '0' }],
  ['path', { d: 'm5 16.25-1.4 2.15h16.8L19 16.25', key: '1' }],
]);

export const Cloud = desktopIcon('cloud', '1.7455 1.6455 20.5091 20.5091', [
  ['path', { d: 'M7 17.75h10a3.65 3.65 0 0 0 .4-7.28A5.55 5.55 0 0 0 6.65 10a3.9 3.9 0 0 0 .35 7.75Z', key: '0' }],
]);

export const Mic = desktopIcon('mic', '2.5364 2.5614 18.9273 18.9273', [
  ['rect', { x: '9.25', y: '4', width: '5.5', height: '10', rx: '2.75', key: '0' }],
  ['path', { d: 'M6.5 11.6v.4a5.5 5.5 0 0 0 11 0v-.4M12 17.5V20m-2.5 0h5', key: '1' }],
]);

export const ArrowUp = desktopIcon('arrow-up', '0.2250 0.2250 23.5500 23.5500', [
  ['path', { d: 'M12 19V5m-6.5 6.5L12 5l6.5 6.5', key: '0' }],
]);

export const Stop = desktopIcon('stop', '3.975 3.975 16.050 16.050', [
  ['rect', { x: '5.3', y: '5.3', width: '13.4', height: '13.4', rx: '2.2', fill: 'currentColor', stroke: 'none', key: '0' }],
]);

export const Copy = desktopIcon('copy', '2.5091 2.5091 18.9818 18.9818', [
  ['rect', { x: '8', y: '8', width: '12', height: '12', rx: '2', key: '0' }],
  ['path', { d: 'M6 16H5.5A1.5 1.5 0 0 1 4 14.5v-9A1.5 1.5 0 0 1 5.5 4h9A1.5 1.5 0 0 1 16 5.5V6', key: '1' }],
]);

export const CornerUpLeft = desktopIcon('corner-up-left', '1.6667 1.1667 20.6667 20.6667', [
  ['path', { d: 'm9 5-4 4 4 4M5 9h9.25a4.75 4.75 0 0 1 4.75 4.75V18', key: '0' }],
]);

export const Pin = desktopIcon('pin', '2.8364 2.8364 18.3273 18.3273', [
  ['path', { d: 'M9 4.25h6l-.42 5.25 2.25 2.3v2.2H7.17v-2.2l2.25-2.3L9 4.25ZM12 14v5.75', key: '0' }],
]);

export const Star = desktopIcon('star', '2.6182 2.1432 18.7636 18.7636', [
  ['path', { d: 'm12 4 2.45 4.95 5.47.8-3.96 3.85.94 5.45L12 16.48l-4.9 2.57.94-5.45-3.96-3.85 5.47-.8L12 4Z', key: '0' }],
]);

export const FileText = desktopIcon('file-text', '2.3727 2.2477 19.2545 19.2545', [
  ['path', { d: 'M7 3.75h6.9l4.35 4.4v10.6A1.5 1.5 0 0 1 16.75 20h-9.5a1.5 1.5 0 0 1-1.5-1.5V5.25A1.5 1.5 0 0 1 7.25 3.75Z', key: '0' }],
  ['path', { d: 'M13.75 4v4.5H18m-8.25 3.75h4.5m-4.5 3h4.5', key: '1' }],
]);

export const Download = desktopIcon('download', '2.0100 1.6350 19.9800 19.9800', [
  ['path', { d: 'M12 4v10m-3.5-3.5L12 14l3.5-3.5M4.5 15.25v2.25a1.75 1.75 0 0 0 1.75 1.75h11.5a1.75 1.75 0 0 0 1.75-1.75v-2.25', key: '0' }],
]);

export const ExternalLink = desktopIcon('external-link', '2.7100 2.2100 19.0800 19.0800', [
  ['path', { d: 'M14 4.5h5.5V10m0-5.5-8.25 8.25', key: '0' }],
  ['path', { d: 'M11 5H6.75A1.75 1.75 0 0 0 5 6.75v10.5C5 18.22 5.78 19 6.75 19h10.5c.97 0 1.75-.78 1.75-1.75V13', key: '1' }],
]);

export const X = desktopIcon('x', '0.2571 0.2571 23.4857 23.4857', [
  ['path', { d: 'm6 6 12 12M18 6 6 18', key: '0' }],
]);
