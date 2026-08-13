import React, { useCallback, useEffect, useRef, useState } from 'react';
import { RefreshCw, X } from 'lucide-react';
import { api } from '../api';

export const RELAY_ADMIN_WIDTH_STORAGE_KEY = 'cc_relay_admin_width_v1';
export const RELAY_ADMIN_WIDTH_MIN = 360;
export const RELAY_ADMIN_WIDTH_DEFAULT = 620;
export const RELAY_ADMIN_WIDTH_MAX = 980;

export function clampRelayAdminWidth(width) {
  const viewport = typeof window !== 'undefined' ? window.innerWidth : 1440;
  const viewportMax = Number.isFinite(viewport)
    ? Math.max(RELAY_ADMIN_WIDTH_MIN, viewport - 520)
    : RELAY_ADMIN_WIDTH_MAX;
  const maxWidth = Math.min(RELAY_ADMIN_WIDTH_MAX, viewportMax);
  const numericWidth = Number(width);
  if (!Number.isFinite(numericWidth)) return RELAY_ADMIN_WIDTH_DEFAULT;
  return Math.min(Math.max(numericWidth, RELAY_ADMIN_WIDTH_MIN), maxWidth);
}

export function loadRelayAdminWidth() {
  if (typeof window === 'undefined' || !window.localStorage) return RELAY_ADMIN_WIDTH_DEFAULT;
  return clampRelayAdminWidth(
    Number(window.localStorage.getItem(RELAY_ADMIN_WIDTH_STORAGE_KEY)) || RELAY_ADMIN_WIDTH_DEFAULT,
  );
}

export function saveRelayAdminWidth(width) {
  if (typeof window === 'undefined' || !window.localStorage) return;
  window.localStorage.setItem(RELAY_ADMIN_WIDTH_STORAGE_KEY, String(Math.round(width)));
}

export default function RelayAdminPanel({ onClose }) {
  const [width, setWidth] = useState(() => loadRelayAdminWidth());
  const [reloadKey, setReloadKey] = useState(0);
  const widthRef = useRef(width);

  useEffect(() => {
    widthRef.current = width;
  }, [width]);

  const updateWidth = useCallback((nextWidth) => {
    const clamped = clampRelayAdminWidth(nextWidth);
    widthRef.current = clamped;
    setWidth(clamped);
    saveRelayAdminWidth(clamped);
  }, []);

  const handleResizePointerDown = useCallback((event) => {
    event.preventDefault();
    event.currentTarget.setPointerCapture?.(event.pointerId);
    const startX = event.clientX;
    const startWidth = widthRef.current;

    const handlePointerMove = (moveEvent) => {
      updateWidth(startWidth + (startX - moveEvent.clientX));
    };
    const handlePointerUp = () => {
      window.removeEventListener('pointermove', handlePointerMove);
      window.removeEventListener('pointerup', handlePointerUp);
    };

    window.addEventListener('pointermove', handlePointerMove);
    window.addEventListener('pointerup', handlePointerUp);
  }, [updateWidth]);

  const handleResizeKeyDown = useCallback((event) => {
    if (event.key === 'ArrowLeft') {
      event.preventDefault();
      updateWidth(widthRef.current + 40);
    } else if (event.key === 'ArrowRight') {
      event.preventDefault();
      updateWidth(widthRef.current - 40);
    } else if (event.key === 'Home') {
      event.preventDefault();
      updateWidth(RELAY_ADMIN_WIDTH_MIN);
    } else if (event.key === 'End') {
      event.preventDefault();
      updateWidth(RELAY_ADMIN_WIDTH_MAX);
    }
  }, [updateWidth]);

  return (
    <aside
      className="v3-relay-admin-panel"
      aria-label="模型用量管理"
      style={{ '--v3-relay-admin-width': `${width}px` }}
    >
      <div
        className="v3-relay-admin-resize-handle"
        role="separator"
        aria-label="调整模型用量面板宽度"
        aria-orientation="vertical"
        tabIndex={0}
        onPointerDown={handleResizePointerDown}
        onKeyDown={handleResizeKeyDown}
        title="拖动调整面板宽度"
      />
      <div className="v3-relay-admin-header">
        <span>模型用量管理</span>
        <div className="v3-relay-admin-header-actions">
          <button
            type="button"
            onClick={() => setReloadKey((value) => value + 1)}
            aria-label="重新载入模型用量管理"
            title="重新载入"
          >
            <RefreshCw size={16} />
          </button>
          <button type="button" onClick={onClose} aria-label="关闭模型用量管理" title="关闭">
            <X size={16} />
          </button>
        </div>
      </div>
      {/* The relay page needs scripts and same-origin fetches (path-rewritten
          through the guarded proxy). allow-same-origin + allow-scripts means the
          sandbox is a thin layer only; the real boundary is the server-side
          proxy (uid whitelist + path whitelist + scoped cookie + rate limit),
          and only whitelisted uid can open this panel at all. */}
      <iframe
        key={reloadKey}
        title="模型用量管理"
        src={api.relayAdminProxyURL('/local/usage-admin')}
        sandbox="allow-scripts allow-same-origin allow-forms allow-modals"
      />
    </aside>
  );
}
