// Interactive canvas layer: drag to move, handles to resize, snap guides.
//
// This wraps the pure renderer instead of teaching it to drag. The renderer's job is to
// paint a slide faithfully; pointer interaction, snapping and draft state belong to a
// separate layer so a preview-only surface can keep using the renderer untouched.
//
// The coordinate story in one place: the pointer reports CSS pixels, the model stores
// EMU. The factor between them is measured from the live canvas rect on every gesture
// start, because the side panel is resizable and a cached factor silently makes every
// drag land short or long by exactly the ratio.

import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import SlideCanvas from '../pptx/render.jsx';
import {
  RESIZE_HANDLES, absoluteFromRelative, clampBoxToSlide, emuPerPixel, moveBox,
  relativeFromAbsolute, resizeBox, snapBox,
} from '../pptx/geometry.js';

// Snap distance in CSS pixels. Expressed on screen rather than in EMU so the feel is
// identical whether the panel is narrow or wide.
const SNAP_PX = 5;

function Handle({ position, onPointerDown }) {
  const base = {
    position: 'absolute',
    width: 8,
    height: 8,
    background: '#ffffff',
    border: '1.5px solid #1f5eff',
    borderRadius: 2,
    zIndex: 3,
  };
  const offsets = {
    nw: { left: -4, top: -4, cursor: 'nwse-resize' },
    n: { left: '50%', top: -4, marginLeft: -4, cursor: 'ns-resize' },
    ne: { right: -4, top: -4, cursor: 'nesw-resize' },
    e: { right: -4, top: '50%', marginTop: -4, cursor: 'ew-resize' },
    se: { right: -4, bottom: -4, cursor: 'nwse-resize' },
    s: { left: '50%', bottom: -4, marginLeft: -4, cursor: 'ns-resize' },
    sw: { left: -4, bottom: -4, cursor: 'nesw-resize' },
    w: { left: -4, top: '50%', marginTop: -4, cursor: 'ew-resize' },
  };
  return (
    <span
      role="presentation"
      data-office-handle={position}
      style={{ ...base, ...offsets[position] }}
      onPointerDown={event => onPointerDown(event, position)}
    />
  );
}

function Guide({ orientation, position }) {
  const style = orientation === 'v'
    ? { position: 'absolute', left: `${position}%`, top: 0, bottom: 0, width: 1, background: '#f0a020', zIndex: 4, pointerEvents: 'none' }
    : { position: 'absolute', top: `${position}%`, left: 0, right: 0, height: 1, background: '#f0a020', zIndex: 4, pointerEvents: 'none' };
  return <span style={style} />;
}

/**
 * Overlay that draws the selection frame and handles for the selected element.
 *
 * The frame is placed with the same percentage math the renderer uses, so it lines up
 * with the painted shape without measuring the DOM. Measuring would break for rotated
 * shapes, where the bounding box differs from the transform box.
 */
function SelectionOverlay({
  shape, box, slideSize, onDragStart, onHandleDown, showHandles,
}) {
  if (!shape || !box) return null;
  const left = (box.x / slideSize.cx) * 100;
  const top = (box.y / slideSize.cy) * 100;
  const width = (box.cx / slideSize.cx) * 100;
  const height = (box.cy / slideSize.cy) * 100;
  return (
    <div
      data-office-selection={shape.elId}
      style={{
        position: 'absolute',
        left: `${left}%`,
        top: `${top}%`,
        width: `${width}%`,
        height: `${height}%`,
        outline: '1.5px solid #1f5eff',
        outlineOffset: 1,
        cursor: shape.inherited ? 'not-allowed' : 'move',
        zIndex: 2,
      }}
      onPointerDown={event => onDragStart(event, shape.elId)}
    >
      {showHandles && !shape.inherited
        ? RESIZE_HANDLES.map(position => (
          <Handle key={position} position={position}
            onPointerDown={(event, pos) => onHandleDown(event, shape.elId, pos)} />
        ))
        : null}
    </div>
  );
}

export default function EditableCanvas({
  slide, slideSize, themeColors, selectedId, hoveredId, imageUrls,
  editedBoxes, onSelect, onHover, onGestureStart, onBoxPreview, onBoxCommit, readOnly = false,
}) {
  const canvasRef = useRef(null);
  const gestureRef = useRef(null);
  const [guide, setGuide] = useState(null);

  // Measured factor along each axis, recomputed per gesture. Null until a gesture runs.
  const factorOf = useCallback(() => {
    const rect = canvasRef.current?.getBoundingClientRect?.();
    if (!rect || rect.width <= 0 || rect.height <= 0) return null;
    return {
      x: emuPerPixel(rect.width, slideSize.cx),
      y: emuPerPixel(rect.height, slideSize.cy),
      rect,
    };
  }, [slideSize.cx, slideSize.cy]);

  // Selection can land on a group child now, so the lookup has to descend into `children`.
  // Returning only top-level shapes is what previously made a grouped element unselectable:
  // the click found an id, the lookup returned null, and the overlay silently disappeared.
  const slideShape = useCallback((elId) => {
    const walk = (shapes) => {
      for (const shape of shapes || []) {
        if (shape.elId === elId) return shape;
        const nested = walk(shape.children);
        if (nested) return nested;
      }
      return null;
    };
    return walk(slide?.shapes || []);
  }, [slide]);

  /** The group a child belongs to, needed to convert its box in both directions. */
  const parentGroupOf = useCallback((elId) => {
    const walk = (shapes) => {
      for (const shape of shapes || []) {
        if ((shape.children || []).some(child => child.elId === elId)) return shape;
        const nested = walk(shape.children);
        if (nested) return nested;
      }
      return null;
    };
    return walk(slide?.shapes || []);
  }, [slide]);

  /**
   * A shape's box in slide coordinates.
   *
   * A grouped child stores relative coordinates; the overlay and the drag math both work in slide
   * space, so the conversion happens here rather than at each call site.
   */
  const absoluteBoxOf = useCallback((shape) => {
    if (!shape) return null;
    if (shape.positioning !== 'relative') return { ...shape.box };
    const group = parentGroupOf(shape.elId);
    if (!group) return { ...shape.box };
    return absoluteFromRelative(shape.box, group.box, group.childOffset, group.childExtent);
  }, [parentGroupOf]);

  const baseBoxOf = useCallback((elId) => {
    const override = editedBoxes?.[elId];
    if (override) return override;
    const shape = slideShape(elId);
    return shape ? absoluteBoxOf(shape) : null;
  }, [absoluteBoxOf, editedBoxes, slideShape]);

  const finish = useCallback((event) => {
    const gesture = gestureRef.current;
    gestureRef.current = null;
    setGuide(null);
    if (!gesture) return;
    gesture.element?.releasePointerCapture?.(event.pointerId);
    if (!gesture.moved) return;

    // The drag worked in slide coordinates. A grouped child stores relative coordinates, so the
    // result is converted back before it reaches the draft; writing the absolute box would move
    // the shape by the group's offset a second time on the next render.
    const shape = slideShape(gesture.elId);
    let committed = gesture.lastBox;
    if (shape?.positioning === 'relative') {
      const group = parentGroupOf(gesture.elId);
      if (group) {
        committed = relativeFromAbsolute(committed, group.box, group.childOffset, group.childExtent);
      }
    }
    onBoxCommit?.(gesture.elId, committed);
  }, [onBoxCommit, parentGroupOf, slideShape]);

  // Pointer capture is used so a fast drag that leaves the canvas still tracks. The
  // listeners live on window as a fallback for environments where capture is a no-op.
  useEffect(() => {
    const onMove = (event) => {
      const gesture = gestureRef.current;
      if (!gesture) return;
      const dx = event.clientX - gesture.startX;
      const dy = event.clientY - gesture.startY;
      if (!gesture.moved && Math.abs(dx) < 2 && Math.abs(dy) < 2) return;
      gesture.moved = true;

      let next;
      if (gesture.mode === 'resize') {
        next = resizeBox(gesture.anchor, gesture.handle, { dx, dy }, gesture.factor);
      } else {
        next = moveBox(gesture.anchor, { dx, dy }, gesture.factor);
        const thresholdEmu = SNAP_PX * gesture.factor.x;
        const snapped = snapBox(next, slideSize, thresholdEmu);
        next = { x: snapped.x, y: snapped.y, cx: snapped.cx, cy: snapped.cy };
        gesture.snapX = snapped.snappedX || null;
        gesture.snapY = snapped.snappedY || null;
      }
      next = clampBoxToSlide(next, slideSize);
      gesture.lastBox = next;
      // Draft boxes for group children use the same relative coordinate space as the model.
      // The gesture itself works in slide space, so convert before publishing the preview.
      let previewBox = next;
      const shape = slideShape(gesture.elId);
      if (shape?.positioning === 'relative') {
        const group = parentGroupOf(gesture.elId);
        if (group) {
          previewBox = relativeFromAbsolute(next, group.box, group.childOffset, group.childExtent);
        }
      }
      onBoxPreview?.(gesture.elId, previewBox);
      if (gesture.mode === 'move') {
        setGuide(gesture.snapX || gesture.snapY
          ? { x: gesture.snapX, y: gesture.snapY }
          : null);
      }
    };
    const onUp = (event) => finish(event);
    window.addEventListener('pointermove', onMove);
    window.addEventListener('pointerup', onUp);
    window.addEventListener('pointercancel', onUp);
    return () => {
      window.removeEventListener('pointermove', onMove);
      window.removeEventListener('pointerup', onUp);
      window.removeEventListener('pointercancel', onUp);
    };
  }, [finish, onBoxPreview, parentGroupOf, slideShape, slideSize]);

  const beginGesture = useCallback((event, elId, mode, handle) => {
    if (readOnly) return;
    const shape = slideShape(elId);
    if (!shape) return;
    // An inherited shape's box lives on the layout. Dragging it here would write a
    // transform that detaches it from the layout for this slide only, silently, so the
    // gesture is refused and the panel explains why.
    if (shape.inherited) return;

    const factor = factorOf();
    if (!factor) return;
    const anchor = baseBoxOf(elId);
    if (!anchor) return;

    event.preventDefault();
    event.stopPropagation();
    const element = event.currentTarget;
    try { element.setPointerCapture?.(event.pointerId); } catch { /* synthetic event */ }

    onGestureStart?.(elId);
    gestureRef.current = {
      elId,
      mode,
      handle,
      anchor,
      factor,
      startX: event.clientX,
      startY: event.clientY,
      element,
      moved: false,
      lastBox: anchor,
      snapX: null,
      snapY: null,
    };
    onSelect?.(elId);
  }, [baseBoxOf, factorOf, onGestureStart, onSelect, readOnly, slideShape]);

  const handleDragStart = useCallback((event, elId) => {
    beginGesture(event, elId, 'move', null);
  }, [beginGesture]);

  const handleHandleDown = useCallback((event, elId, position) => {
    beginGesture(event, elId, 'resize', position);
  }, [beginGesture]);

  // Draft boxes are handed to the renderer so the canvas shows the move while it happens.
  // Rebuilding the slide object here keeps SlideCanvas free of drag concerns. Nested children are
  // covered too: a draft on a grouped element must show while dragging, not only after commit.
  const renderedSlide = useMemo(() => {
    if (!slide) return slide;
    if (!editedBoxes || Object.keys(editedBoxes).length === 0) return slide;
    const applyBoxes = (shapes) => shapes.map((shape) => {
      const next = { ...shape };
      if (editedBoxes[shape.elId]) {
        next.box = editedBoxes[shape.elId];
      }
      if (shape.children) next.children = applyBoxes(shape.children);
      return next;
    });
    return { ...slide, shapes: applyBoxes(slide.shapes) };
  }, [editedBoxes, slide]);

  const selectedShape = selectedId ? slideShape(selectedId) : null;
  // The overlay draws in slide coordinates, so a grouped child's relative box has to be resolved.
  const selectedBox = (() => {
    if (!selectedId || !selectedShape) return null;
    const override = editedBoxes?.[selectedId];
    if (!override) return absoluteBoxOf(selectedShape);
    if (selectedShape.positioning !== 'relative') return override;
    const group = parentGroupOf(selectedId);
    return group
      ? absoluteFromRelative(override, group.box, group.childOffset, group.childExtent)
      : override;
  })();

  const guidePercent = useMemo(() => {
    if (!guide) return null;
    const out = {};
    if (guide.x === 'left') out.x = 0;
    else if (guide.x === 'center') out.x = 50;
    else if (guide.x === 'right') out.x = 100;
    if (guide.y === 'top') out.y = 0;
    else if (guide.y === 'middle') out.y = 50;
    else if (guide.y === 'bottom') out.y = 100;
    return out;
  }, [guide]);

  return (
    <div ref={canvasRef} style={{ position: 'relative', width: '100%' }}>
      <SlideCanvas
        slide={renderedSlide}
        slideSize={slideSize}
        themeColors={themeColors}
        selectedId={selectedId}
        hoveredId={hoveredId}
        onSelect={onSelect}
        onHover={onHover}
        imageUrls={imageUrls}
      />
      {selectedShape ? (
        <SelectionOverlay
          shape={selectedShape}
          box={selectedBox}
          slideSize={slideSize}
          onDragStart={handleDragStart}
          onHandleDown={handleHandleDown}
          showHandles={!readOnly}
        />
      ) : null}
      {guidePercent?.x !== undefined ? <Guide orientation="v" position={guidePercent.x} /> : null}
      {guidePercent?.y !== undefined ? <Guide orientation="h" position={guidePercent.y} /> : null}
    </div>
  );
}

export { SNAP_PX };
