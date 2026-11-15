// Element list for the office canvas.
//
// The list exists because a slide can hold dozens of shapes and a flat property panel
// would become a wall. Four behaviours keep it manageable:
//
//   - collapsed by default: one line per element, label plus short preview
//   - selection follows the canvas: clicking a shape scrolls its row into view
//   - hover is bidirectional: hovering a row outlines the shape on the canvas, so the
//     user knows what they are about to select before they commit
//   - multi-select with shift: grouping needs two or more shapes, and a modifier click is
//     the cheapest way to express that without adding a mode
//
// `pickedIds` is deliberately separate from `selectedId`. `selectedId` drives the property
// panel (exactly one shape); `pickedIds` drives grouping (one or more). Merging them would
// force the panel to handle a multi-shape state it has no fields for.

import React, { useEffect, useRef } from 'react';
import { SHAPE_KINDS } from '../pptx/model.js';

const KIND_GLYPH = {
  [SHAPE_KINDS.TEXT]: 'T',
  [SHAPE_KINDS.PICTURE]: '▣',
  [SHAPE_KINDS.TABLE]: '▦',
  [SHAPE_KINDS.GROUP]: '❏',
  [SHAPE_KINDS.CONNECTOR]: '╱',
  [SHAPE_KINDS.FRAME]: '◫',
  [SHAPE_KINDS.UNKNOWN]: '?',
};

function Row({ shape, depth, selectedId, pickedIds, onSelect, onSelectMany, onHover }) {
  const ref = useRef(null);
  const isSelected = selectedId === shape.elId;
  const isPicked = Boolean(pickedIds && pickedIds.includes(shape.elId));

  useEffect(() => {
    if (!isSelected) return;
    // `block: 'nearest'` keeps the canvas from jumping when the user picks a shape that is
    // already visible, which would be disorienting during rapid clicking.
    ref.current?.scrollIntoView?.({ block: 'nearest' });
  }, [isSelected]);

  const classes = ['office-el-row'];
  if (isSelected) classes.push('is-selected');
  if (isPicked && !isSelected) classes.push('is-picked');
  if (shape.inherited) classes.push('is-inherited');

  return (
    <>
      <li
        ref={ref}
        data-office-el-row={shape.elId}
        className={classes.join(' ')}
        style={{ paddingLeft: 8 + depth * 14 }}
        onClick={(event) => {
          if (event.shiftKey || event.metaKey || event.ctrlKey) {
            onSelectMany?.(shape.elId);
            return;
          }
          onSelect?.(shape.elId, event);
        }}
        onMouseEnter={() => onHover?.(shape.elId)}
        onMouseLeave={() => onHover?.(null)}
      >
        <span className="office-el-glyph" aria-hidden="true">{KIND_GLYPH[shape.kind] || '?'}</span>
        <span className="office-el-label">{shape.label}</span>
        {shape.preview ? <span className="office-el-preview">{shape.preview}</span> : null}
        {shape.inherited ? <span className="office-el-badge" title="来自版式或母版">继承</span> : null}
      </li>
      {(shape.children || []).map(child => (
        <Row key={child.elId} shape={child} depth={depth + 1} selectedId={selectedId}
          pickedIds={pickedIds} onSelect={onSelect} onSelectMany={onSelectMany} onHover={onHover} />
      ))}
    </>
  );
}

export default function ElementList({
  shapes, selectedId, pickedIds, onSelect, onSelectMany, onHover, slideIndex = 0, slideCount = 1,
}) {
  const pickedCount = pickedIds?.length || 0;
  return (
    <div className="office-el-list">
      <div className="office-el-list-head">
        <span>第 {slideIndex + 1} 页 · {shapes?.length || 0} 个元素</span>
        {pickedCount > 1
          ? <span className="office-el-list-picked">已选 {pickedCount}</span>
          : (slideCount > 1 ? <span className="office-el-list-page">共 {slideCount} 页</span> : null)}
      </div>
      <ul>
        {(shapes || []).map(shape => (
          <Row key={shape.elId} shape={shape} depth={0} selectedId={selectedId}
            pickedIds={pickedIds} onSelect={onSelect} onSelectMany={onSelectMany} onHover={onHover} />
        ))}
        {(!shapes || shapes.length === 0) && <li className="office-el-empty">这一页没有可编辑元素</li>}
      </ul>
    </div>
  );
}
