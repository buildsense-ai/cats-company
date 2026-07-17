import React, { useEffect, useRef } from 'react';

export const APP_SIDEBAR_WIDTH_STORAGE_KEY = 'cc_app_sidebar_width_v1';
export const DEFAULT_APP_SIDEBAR_WIDTH = 260;
export const MIN_APP_SIDEBAR_WIDTH = 220;
export const MAX_APP_SIDEBAR_WIDTH = 480;

export function clampSidebarWidth(
  value,
  minWidth = MIN_APP_SIDEBAR_WIDTH,
  maxWidth = MAX_APP_SIDEBAR_WIDTH,
) {
  const numericValue = Number(value);
  if (!Number.isFinite(numericValue)) return DEFAULT_APP_SIDEBAR_WIDTH;
  return Math.round(Math.min(Math.max(numericValue, minWidth), maxWidth));
}

export function getSidebarMaxWidth(viewportWidth) {
  const numericViewport = Number(viewportWidth);
  if (!Number.isFinite(numericViewport)) return MAX_APP_SIDEBAR_WIDTH;
  return Math.max(
    MIN_APP_SIDEBAR_WIDTH,
    Math.min(MAX_APP_SIDEBAR_WIDTH, Math.floor(numericViewport - 480)),
  );
}

export function loadSidebarWidth(storage = globalThis.localStorage) {
  if (!storage) return DEFAULT_APP_SIDEBAR_WIDTH;
  try {
    const stored = storage.getItem(APP_SIDEBAR_WIDTH_STORAGE_KEY);
    return stored == null
      ? DEFAULT_APP_SIDEBAR_WIDTH
      : clampSidebarWidth(stored);
  } catch (error) {
    console.warn('Failed to restore sidebar width:', error);
    return DEFAULT_APP_SIDEBAR_WIDTH;
  }
}

export function saveSidebarWidth(width, storage = globalThis.localStorage) {
  if (!storage) return;
  try {
    storage.setItem(APP_SIDEBAR_WIDTH_STORAGE_KEY, String(clampSidebarWidth(width)));
  } catch (error) {
    console.warn('Failed to save sidebar width:', error);
  }
}

function releaseCapturedPointer(target, pointerId) {
  try {
    target?.releasePointerCapture?.(pointerId);
  } catch {
    // The browser may already have released capture during cancellation/unmount.
  }
}

export default function SidebarResizeHandle({
  width,
  maxWidth = MAX_APP_SIDEBAR_WIDTH,
  disabled = false,
  onWidthChange,
  onWidthCommit,
  onResizeChange,
}) {
  const dragStateRef = useRef(null);

  useEffect(() => {
    if (!disabled || !dragStateRef.current) return;
    const dragState = dragStateRef.current;
    dragStateRef.current = null;
    releaseCapturedPointer(dragState.target, dragState.pointerId);
    onWidthChange?.(width);
    onResizeChange?.(false);
  }, [disabled, onResizeChange, onWidthChange, width]);

  useEffect(() => () => {
    const dragState = dragStateRef.current;
    dragStateRef.current = null;
    releaseCapturedPointer(dragState?.target, dragState?.pointerId);
    onResizeChange?.(false);
  }, [onResizeChange]);

  if (disabled) return null;

  const applyWidth = (nextWidth, commit = false) => {
    const clampedWidth = clampSidebarWidth(
      nextWidth,
      MIN_APP_SIDEBAR_WIDTH,
      maxWidth,
    );
    onWidthChange?.(clampedWidth);
    if (commit) onWidthCommit?.(clampedWidth);
    return clampedWidth;
  };

  const handlePointerDown = (event) => {
    if (event.button !== 0) return;
    event.preventDefault();
    const pointerId = event.pointerId;
    dragStateRef.current = {
      pointerId,
      startX: event.clientX,
      startWidth: width,
      currentWidth: width,
      target: event.currentTarget,
    };
    event.currentTarget.setPointerCapture?.(pointerId);
    onResizeChange?.(true);
  };

  const handlePointerMove = (event) => {
    const dragState = dragStateRef.current;
    if (!dragState || dragState.pointerId !== event.pointerId) return;
    const nextWidth = applyWidth(
      dragState.startWidth + event.clientX - dragState.startX,
    );
    dragState.currentWidth = nextWidth;
    event.currentTarget.setAttribute('aria-valuenow', String(nextWidth));
    event.currentTarget.setAttribute('aria-valuetext', `${nextWidth} 像素`);
  };

  const finishPointerResize = (event) => {
    const dragState = dragStateRef.current;
    if (!dragState || dragState.pointerId !== event.pointerId) return;
    dragStateRef.current = null;
    releaseCapturedPointer(dragState.target, event.pointerId);
    onWidthCommit?.(dragState.currentWidth);
    onResizeChange?.(false);
  };

  const handleKeyDown = (event) => {
    const step = event.shiftKey ? 32 : 12;
    let nextWidth = width;
    if (event.key === 'ArrowLeft') nextWidth -= step;
    else if (event.key === 'ArrowRight') nextWidth += step;
    else if (event.key === 'Home') nextWidth = MIN_APP_SIDEBAR_WIDTH;
    else if (event.key === 'End') nextWidth = maxWidth;
    else return;

    event.preventDefault();
    applyWidth(nextWidth, true);
  };

  return (
    <div
      className="v3-sidebar-resize-handle"
      role="separator"
      aria-label="调整功能栏宽度"
      aria-orientation="vertical"
      aria-controls="catsco-function-sidebar"
      aria-valuemin={MIN_APP_SIDEBAR_WIDTH}
      aria-valuemax={maxWidth}
      aria-valuenow={width}
      aria-valuetext={`${width} 像素`}
      tabIndex={0}
      title="拖动调整功能栏宽度"
      onDoubleClick={() => applyWidth(DEFAULT_APP_SIDEBAR_WIDTH, true)}
      onKeyDown={handleKeyDown}
      onPointerDown={handlePointerDown}
      onPointerMove={handlePointerMove}
      onPointerUp={finishPointerResize}
      onPointerCancel={finishPointerResize}
      onLostPointerCapture={finishPointerResize}
    />
  );
}
