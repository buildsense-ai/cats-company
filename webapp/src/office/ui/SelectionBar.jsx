// The small editing bar that appears beside the block you clicked.
//
// Replaces the tall property column as the primary way to edit text. That column pushed the
// document sideways, and floating it only moved the problem to a permanent corner. This bar
// sits next to the text it edits, opens on the text itself, and keeps a handful of character
// controls beside it — the shape a right-click toolbar has inside Word.
//
// Two postures. Floating: parked beside the selection, draggable by its grip. Docked: pushed
// against the right edge, where it becomes a column like a tools palette.

import React, { useCallback, useEffect, useRef, useState } from 'react';
import LocalDraftTextarea from './LocalDraftTextarea.jsx';
import { placeBarBeside, shouldDockBar, clampDragOffset, BAR_HEIGHT, BAR_WIDTH } from './floating-bar-placement.js';

const SWATCHES = ['#16181b', '#c0392b', '#1f5eff', '#0f8a4a', '#b8860b', '#7b2fbe'];

const ALIGN_OPTIONS = [
  { value: 'l', label: '左' },
  { value: 'ctr', label: '中' },
  { value: 'r', label: '右' },
  { value: 'just', label: '两端' },
];

function Toggle({ label, title, active, disabled, onClick }) {
  return (
    <button type="button" className={`office-bar-btn${active ? ' is-on' : ''}`}
      title={title} aria-pressed={active ? 'true' : 'false'}
      disabled={disabled} onClick={onClick}>
      {label}
    </button>
  );
}

export default function SelectionBar({
  anchor, containerSize, value, onTextCommit, style, onStyle, onDelete,
  disabled, docked, onDock, onClose, hasText, label, children,
  // Word stores alignment as left/center/right/both, PPT as l/ctr/r/just. The bar does not
  // get to pick one vocabulary: the caller owns the format it writes back.
  alignOptions = ALIGN_OPTIONS,
}) {
  const [offset, setOffset] = useState({ x: 0, y: 0 });
  const [dragging, setDragging] = useState(false);
  const barRef = useRef(null);

  // A drag stores an offset from the computed spot. Clearing it when the selection moves
  // keeps the bar beside whatever was clicked next instead of leaving it behind.
  useEffect(() => {
    setOffset({ x: 0, y: 0 });
  }, [anchor?.left, anchor?.top, anchor?.width, anchor?.height]);

  const position = placeBarBeside({
    anchor,
    container: containerSize,
    size: { width: BAR_WIDTH, height: BAR_HEIGHT },
  });

  const startDrag = useCallback((event) => {
    if (docked) return;
    if (!event || (event.button !== undefined && event.button !== 0)) return;
    event.preventDefault();
    event.stopPropagation();
    const startX = event.clientX;
    const startY = event.clientY;
    const base = offset;
    setDragging(true);
    const move = (moveEvent) => {
      // Clamped against the container on every move: the fitted position is clamped, but the
      // drag offset used to be free, which let the bar be pulled past the right edge where it
      // was half outside the preview and impossible to grab back.
      const host = barRef.current?.parentElement;
      const rect = host?.getBoundingClientRect?.();
      setOffset(clampDragOffset({
        offset: {
          x: base.x + (moveEvent.clientX - startX),
          y: base.y + (moveEvent.clientY - startY),
        },
        position,
        size: { width: BAR_WIDTH, height: Math.max(BAR_HEIGHT, barRef.current?.offsetHeight || 0) },
        container: rect ? { width: rect.width, height: rect.height } : containerSize,
      }));
    };
    const finish = (upEvent) => {
      window.removeEventListener('pointermove', move);
      window.removeEventListener('pointerup', finish);
      window.removeEventListener('pointercancel', finish);
      setDragging(false);
      // Dropping at the edge means "get this out of the way", which is exactly what docking
      // is for. Only a drop counts, so a small drag near the edge does not dock by accident.
      const host = barRef.current?.parentElement;
      const rect = host?.getBoundingClientRect?.();
      if (upEvent && rect && onDock && shouldDockBar({
        pointerX: upEvent.clientX - rect.left,
        containerWidth: rect.width,
      })) {
        setOffset({ x: 0, y: 0 });
        onDock(true);
      }
    };
    window.addEventListener('pointermove', move);
    window.addEventListener('pointerup', finish);
    window.addEventListener('pointercancel', finish);
  }, [docked, offset, onDock]);

  const active = style || {};

  return (
    <div
      ref={barRef}
      className={`office-bar${docked ? ' is-docked' : ''}${dragging ? ' is-dragging' : ''}`}
      data-office-bar=""
      data-office-bar-docked={docked ? '1' : '0'}
      style={docked ? undefined : { left: position.left + offset.x, top: position.top + offset.y }}
      onPointerDown={event => event.stopPropagation()}
    >
      <div className="office-bar-grip" data-office-bar-grip=""
        title={docked ? '已停靠在右侧，点 ⇤ 回到选区旁' : '按住拖动，拖到最右侧自动停靠'}
        onPointerDown={startDrag}>
        <span className="office-bar-dots" aria-hidden="true">⋮⋮</span>
        <span className="office-bar-name">{label || (hasText ? '文本' : '元素')}</span>
        <span className="office-bar-spacer" />
        {onDock ? (
          <button type="button" className="office-bar-icon"
            title={docked ? '回到选区旁' : '停靠到右侧'}
            onClick={() => onDock(!docked)}>
            {docked ? '⇤' : '⇥'}
          </button>
        ) : null}
        {onClose ? (
          <button type="button" className="office-bar-icon" title="关闭"
            onClick={() => onClose()}>×</button>
        ) : null}
      </div>

      {hasText ? (
        <>
          <LocalDraftTextarea
            className="office-bar-text"
            rows={docked ? 3 : 2}
            disabled={disabled}
            value={value}
            onCommit={onTextCommit}
          />
          <div className="office-bar-row">
            <Toggle label="B" title="加粗" active={active.bold} disabled={disabled}
              onClick={() => onStyle?.({ bold: !active.bold })} />
            <Toggle label="I" title="斜体" active={active.italic} disabled={disabled}
              onClick={() => onStyle?.({ italic: !active.italic })} />
            <Toggle label="U" title="下划线" active={active.underline} disabled={disabled}
              onClick={() => onStyle?.({ underline: !active.underline })} />
            <span className="office-bar-sep" />
            {SWATCHES.map(color => (
              <button key={color} type="button" className="office-bar-swatch"
                data-office-bar-color={color}
                title={`文字颜色 ${color}`}
                aria-label={`文字颜色 ${color}`}
                disabled={disabled}
                style={{ background: color }}
                onClick={() => onStyle?.({ color })} />
            ))}
            <span className="office-bar-sep" />
            <select className="office-bar-select" value={active.align || alignOptions[0].value}
              disabled={disabled} title="对齐" aria-label="对齐"
              onChange={event => onStyle?.({ align: event.target.value })}>
              {alignOptions.map(option => (
                <option key={option.value} value={option.value}>{option.label}</option>
              ))}
            </select>
          </div>
        </>
      ) : (
        <p className="office-bar-note">这个元素没有文字。</p>
      )}

      {onDelete ? (
        <div className="office-bar-foot">
          <button type="button" className="office-bar-danger" disabled={disabled}
            onClick={() => onDelete()}>
            删除
          </button>
        </div>
      ) : null}

      {/* Docked is the only posture with room for the full property panel. Keeping it here
          rather than in a second card is what makes the dock read as one column. A block with
          no text gets the panel either way: its only controls are geometry and table cells,
          and leaving them behind the dock would make those blocks uneditable while floating. */}
      {children && (!hasText || docked) ? <div className="office-bar-extra">{children}</div> : null}
    </div>
  );
}

export { SWATCHES, ALIGN_OPTIONS };
