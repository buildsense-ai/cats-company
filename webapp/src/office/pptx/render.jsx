// Canvas renderer for the pptx model.
//
// The renderer is intentionally dependency-free: no pptx-preview, no docx-preview.
// Those libraries flatten a slide into HTML and lose the link between a rendered
// node and its XML node, which is exactly the link click-to-edit needs. Rendering
// from our own model keeps `data-office-el` on every box, so a click resolves to one
// element with no guessing.

import React, { useMemo } from 'react';
import { SHAPE_KINDS, emuToPercent } from './model.js';

const FONT_STACK = '"Noto Sans SC","PingFang SC","Microsoft YaHei","Hiragino Sans GB",-apple-system,sans-serif';

function boxStyle(shape, slideSize, scale = 1) {
  const x = emuToPercent(shape.box.x, slideSize.cx);
  const y = emuToPercent(shape.box.y, slideSize.cy);
  const w = emuToPercent(shape.box.cx, slideSize.cx);
  const h = emuToPercent(shape.box.cy, slideSize.cy);
  const style = {
    position: 'absolute',
    left: `${x * scale}%`,
    top: `${y * scale}%`,
    width: `${w * scale}%`,
    height: `${h * scale}%`,
  };
  // Rotation in OOXML is 1/60000 degree.
  if (shape.rot) style.transform = `rotate(${shape.rot / 60000}deg)`;
  if (shape.flipH || shape.flipV) {
    style.transform = `${style.transform || ''} scale(${shape.flipH ? -1 : 1},${shape.flipV ? -1 : 1})`.trim();
  }
  return style;
}

function resolveColor(value, themeColors) {
  if (!value) return null;
  const match = /^var\(--office-scheme-([\w-]+),\s*(#[0-9a-fA-F]{3,8})\)$/.exec(value);
  if (!match) return value;
  return themeColors?.[match[1]] || match[2];
}

function paragraphStyle(para, themeColors) {
  const style = { margin: 0, whiteSpace: 'pre-wrap', wordBreak: 'break-word' };
  if (para.align === 'ctr') style.textAlign = 'center';
  else if (para.align === 'r') style.textAlign = 'right';
  else if (para.align === 'just') style.textAlign = 'justify';
  if (para.level) style.paddingLeft = `${para.level * 14}px`;
  return style;
}

function runStyle(run, themeColors) {
  const style = {};
  // OOXML text size is centipoints; 0 means inherit.
  if (run.size) style.fontSize = `${run.size / 100}pt`;
  if (run.bold) style.fontWeight = 700;
  if (run.italic) style.fontStyle = 'italic';
  if (run.underline) style.textDecoration = 'underline';
  const color = resolveColor(run.color, themeColors);
  if (color) style.color = color;
  if (run.font) style.fontFamily = `"${run.font}",${FONT_STACK}`;
  return style;
}

function TextBody({ body, themeColors }) {
  if (!body) return null;
  const padding = {
    paddingTop: `${(body.tIns / 12700) * 1.2}px`,
    paddingBottom: `${(body.bIns / 12700) * 1.2}px`,
    paddingLeft: `${(body.lIns / 12700) * 1.2}px`,
    paddingRight: `${(body.rIns / 12700) * 1.2}px`,
  };
  const justify = body.anchor === 'ctr' ? 'center' : body.anchor === 'b' ? 'flex-end' : 'flex-start';
  return (
    <div style={{ width: '100%', height: '100%', display: 'flex', flexDirection: 'column',
      justifyContent: justify, overflow: 'hidden', boxSizing: 'border-box', ...padding }}>
      {body.paragraphs.map((para, index) => (
        <p key={index} style={paragraphStyle(para, themeColors)}>
          {para.runs.length === 0 ? <br /> : para.runs.map((run, runIndex) => (
            run.break
              ? <br key={runIndex} />
              : <span key={runIndex} style={runStyle(run, themeColors)}>{run.text}</span>
          ))}
        </p>
      ))}
    </div>
  );
}

function TableShape({ shape, themeColors }) {
  const { table } = shape;
  if (!table || !table.cells.length) return null;
  const grid = [];
  for (let row = 0; row < table.rows; row += 1) {
    const line = [];
    for (let col = 0; col < table.cols; col += 1) {
      line.push(table.cells.find(cell => cell.row === row && cell.col === col) || null);
    }
    grid.push(line);
  }
  return (
    <table style={{ width: '100%', height: '100%', borderCollapse: 'collapse', tableLayout: 'fixed',
      fontSize: '9pt', fontFamily: FONT_STACK }}>
      <tbody>
        {grid.map((row, rowIndex) => (
          <tr key={rowIndex}>
            {row.map((cell, colIndex) => (
              <td key={colIndex} style={{ border: '1px solid #c9ccd2', padding: '2px 4px',
                verticalAlign: 'top', whiteSpace: 'pre-wrap', wordBreak: 'break-word' }}>
                {cell ? cell.text : ''}
              </td>
            ))}
          </tr>
        ))}
      </tbody>
    </table>
  );
}

/**
 * Resolve a group child's box into the containing group's local coordinates.
 *
 * A child inside `p:grpSp` stores its `a:off`/`a:ext` in the group's own child coordinate space,
 * anchored at `chOff` and scaled by `chExt`. Drawing that box directly would place every grouped
 * shape as if the group started at the origin, which is why grouped content used to jump when a
 * group was selected. The child element is already nested in the group's DOM box, so its
 * left/top are measured from the group origin: local = (child - chOff) * s.
 */
function relativeChildBox(child, group) {
  const groupBox = group?.box;
  const chOffset = group?.childOffset;
  const chExtent = group?.childExtent;
  if (!groupBox || !chOffset || !chExtent || chExtent.x <= 0 || chExtent.y <= 0) {
    return child.box;
  }
  const scaleX = groupBox.cx / chExtent.x;
  const scaleY = groupBox.cy / chExtent.y;
  return {
    x: Math.round((child.box.x - chOffset.x) * scaleX),
    y: Math.round((child.box.y - chOffset.y) * scaleY),
    cx: Math.round(child.box.cx * scaleX),
    cy: Math.round(child.box.cy * scaleY),
  };
}

function ShapeNode({
  shape, slideSize, themeColors, selected, hovered, onSelect, onHover, imageUrls,
  boxOverride, parentGroup,
}) {
  const isSelected = selected === shape.elId;
  const isHovered = hovered === shape.elId;
  const outline = isSelected
    ? '2px solid #1f5eff'
    : isHovered ? '1.5px dashed #9db4e8' : '1.5px solid transparent';

  // `boxOverride` wins over the shape's own box: a group child is drawn at its resolved absolute
  // position while still carrying the relative coordinates its anchor needs for writing.
  const resolved = boxOverride || shape.box;
  const drawable = boxOverride ? { ...shape, box: resolved } : shape;
  // A child is positioned inside its group's DOM box, not inside the slide. Percentages
  // therefore have to use the parent group's dimensions as their containing block.
  const styleSize = parentGroup?.box || slideSize;

  const common = {
    'data-office-el': shape.elId,
    'data-office-kind': shape.kind,
    'data-office-inherited': shape.inherited ? '1' : '0',
    // The event is forwarded so the caller can read shiftKey for multi-select. Grouping
    // needs two or more shapes, and a modifier on the click is the only gesture that
    // costs nothing in the UI.
    onMouseDown: event => { event.stopPropagation(); onSelect?.(shape.elId, event); },
    onMouseEnter: () => onHover?.(shape.elId),
    onMouseLeave: () => onHover?.(null),
    title: shape.inherited ? `${shape.label}（继承自版式）` : shape.label,
  };

  if (shape.kind === SHAPE_KINDS.GROUP) {
    return (
      <div {...common} style={{ ...boxStyle(drawable, styleSize), outline, outlineOffset: '1px' }}>
        {(shape.children || []).map(child => (
          <ShapeNode key={child.elId} shape={child} slideSize={slideSize} themeColors={themeColors}
            selected={selected} hovered={hovered} onSelect={onSelect} onHover={onHover}
            imageUrls={imageUrls}
            boxOverride={relativeChildBox(child, drawable)}
            parentGroup={drawable} />
        ))}
      </div>
    );
  }

  if (shape.kind === SHAPE_KINDS.PICTURE) {
    const url = shape.imageRelId ? imageUrls?.[shape.imageRelId] : null;
    return (
      <div {...common} style={{ ...boxStyle(drawable, styleSize), outline, outlineOffset: '1px',
        background: url ? 'transparent' : '#e8eaf0', overflow: 'hidden' }}>
        {url
          ? <img src={url} alt={shape.label} style={{ width: '100%', height: '100%', objectFit: 'contain' }} draggable={false} />
          : <div style={{ width: '100%', height: '100%', display: 'flex', alignItems: 'center',
              justifyContent: 'center', color: '#9aa0ab', fontSize: 11 }}>图片</div>}
      </div>
    );
  }

  if (shape.kind === SHAPE_KINDS.TABLE) {
    return (
      <div {...common} style={{ ...boxStyle(drawable, styleSize), outline, outlineOffset: '1px', overflow: 'hidden' }}>
        <TableShape shape={shape} themeColors={themeColors} />
      </div>
    );
  }

  if (shape.kind === SHAPE_KINDS.CONNECTOR) {
    return (
      <div {...common} style={{ ...boxStyle(drawable, styleSize), outline, outlineOffset: '1px' }}>
        <div style={{ width: '100%', height: '100%', display: 'flex', alignItems: 'center' }}>
          <div style={{ width: '100%', height: 1.5, background: '#8b919c' }} />
        </div>
      </div>
    );
  }

  // Text, frame and anything we do not model deeply.
  return (
    <div {...common} style={{ ...boxStyle(drawable, styleSize), outline, outlineOffset: '1px', overflow: 'hidden' }}>
      <TextBody body={shape.textBody} themeColors={themeColors} />
    </div>
  );
}

/**
 * One slide as an interactive surface.
 *
 * Selection is reported by element id. The parent uses that id to scroll the element
 * list and to load the property panel, which is the whole point of the design: the
 * canvas never has to infer what the user clicked.
 */
export default function SlideCanvas({
  slide, slideSize, themeColors, selectedId, hoveredId, onSelect, onHover, imageUrls,
}) {
  const themeStyle = useMemo(() => {
    const vars = {};
    if (themeColors) {
      for (const [name, value] of Object.entries(themeColors)) {
        vars[`--office-scheme-${name}`] = value;
      }
    }
    return vars;
  }, [themeColors]);

  if (!slide) return null;

  return (
    <div
      className="office-slide-canvas"
      data-office-slide={slide.index}
      onMouseDown={event => { if (event.target === event.currentTarget) onSelect?.(null); }}
      style={{
        position: 'relative',
        width: '100%',
        aspectRatio: `${slideSize.cx} / ${slideSize.cy}`,
        background: slide.background?.fill || '#ffffff',
        overflow: 'hidden',
        fontFamily: FONT_STACK,
        ...themeStyle,
      }}
    >
      {slide.shapes.map(shape => (
        <ShapeNode key={shape.elId} shape={shape} slideSize={slideSize} themeColors={themeColors}
          selected={selectedId} hovered={hoveredId} onSelect={onSelect} onHover={onHover}
          imageUrls={imageUrls} />
      ))}
    </div>
  );
}

export { boxStyle, resolveColor };
