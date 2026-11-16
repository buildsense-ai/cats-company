// DOCX renderer.
//
// Word has no coordinates in the file, so this is a flow renderer: blocks stack in document
// order inside a page-shaped column, and the browser does the line breaking. That is the
// honest approach — a hand-rolled layout engine would disagree with Word on every line and
// look wrong in a way no amount of tuning fixes.
//
// Each block carries `data-office-block` with its id, so a click resolves to one XML node.
// Runs carry `data-office-run` for the same reason at text granularity.

import React, { useMemo } from 'react';
import { BLOCK_KINDS, ALIGNMENTS } from './model.js';

const FONT_STACK = '"Noto Sans SC","PingFang SC","Microsoft YaHei","Hiragino Sans GB",-apple-system,sans-serif';

// A4 at 96 dpi is 794 x 1123 px, but the page is sized from the file's own pgSz so a Letter
// document or a landscape section is not squeezed into an A4 shape.
const TWIPS_PER_INCH = 1440;
const CSS_PX_PER_INCH = 96;

function twipsToPx(twips) {
  const n = Number(twips);
  return Number.isFinite(n) ? (n / TWIPS_PER_INCH) * CSS_PX_PER_INCH : 0;
}

function alignToCss(value) {
  if (value === 'center') return 'center';
  if (value === 'right') return 'right';
  if (value === 'both' || value === 'distribute') return 'justify';
  return 'left';
}

/** Heading size fallback when a style carries no explicit size. */
function headingFontSize(level) {
  const table = { 1: 26, 2: 22, 3: 19, 4: 17, 5: 15 };
  return table[level] || 14;
}

function RunSpan({ run, block }) {
  const style = {};
  // Word stores size per run; a run without one inherits the paragraph's mark. Falling back to
  // the block's first run keeps a mixed paragraph from collapsing to browser default 16px.
  const size = run.size || block.fallbackSize;
  if (size) style.fontSize = `${size}pt`;
  if (run.bold) style.fontWeight = 700;
  if (run.italic) style.fontStyle = 'italic';
  if (run.underline) style.textDecoration = 'underline';
  if (run.color) style.color = run.color;
  if (run.font) style.fontFamily = `"${run.font}",${FONT_STACK}`;
  if (block.heading && size) style.fontWeight = style.fontWeight || 700;

  const pieces = String(run.text || '').split('\n');
  return (
    <span data-office-run={run.path ? run.path.join('.') : undefined} style={style}>
      {pieces.map((piece, index) => (
        <React.Fragment key={index}>
          {index > 0 ? <br /> : null}
          {piece}
        </React.Fragment>
      ))}
    </span>
  );
}

function ParagraphBlock({ block, selected, onSelect }) {
  const indentLeft = twipsToPx(block.indLeftTwips);
  const indentFirst = twipsToPx(block.indFirstTwips);
  const indentHanging = twipsToPx(block.indHangingTwips);
  const spacing = {};
  if (block.beforeTwips) spacing.marginTop = `${twipsToPx(block.beforeTwips)}px`;
  if (block.afterTwips) spacing.marginBottom = `${twipsToPx(block.afterTwips)}px`;
  if (block.lineTwips) {
    // lineRule="auto" means 240ths of a line; exact/atLeast are absolute twips.
    spacing.lineHeight = block.lineRule === 'auto'
      ? String(block.lineTwips / 240)
      : `${twipsToPx(block.lineTwips)}px`;
  }

  const style = {
    margin: 0,
    ...spacing,
    textAlign: alignToCss(block.align),
    paddingLeft: indentLeft ? `${indentLeft}px` : undefined,
    // A hanging indent pulls the first line back; a first-line indent pushes it in. Word
    // stores exactly one, so only one should ever apply.
    textIndent: indentHanging ? `${-indentHanging}px` : (indentFirst ? `${indentFirst}px` : undefined),
    whiteSpace: 'pre-wrap',
    overflowWrap: 'break-word',
    outline: selected ? '2px solid #1f5eff' : undefined,
    outlineOffset: selected ? '2px' : undefined,
    cursor: 'pointer',
    borderRadius: 3,
  };

  if (block.heading) {
    const level = Math.min(4, block.heading);
    return (
      <div
        data-office-block={block.id}
        data-office-block-kind="paragraph"
        data-office-heading={block.heading}
        role="heading"
        aria-level={block.heading}
        onMouseDown={(event) => { event.stopPropagation(); onSelect?.(block.id, event); }}
        style={{ ...style, fontSize: `${headingFontSize(level)}pt`, fontWeight: 700,
          marginTop: spacing.marginTop || '0.7em', marginBottom: spacing.marginBottom || '0.35em' }}
      >
        {block.runs.length === 0
          ? <span style={{ color: '#c8ccd4' }}>（空标题）</span>
          : block.runs.map((run, index) => (
            <RunSpan key={index} run={run} block={{ ...block, fallbackSize: headingFontSize(level) }} />
          ))}
      </div>
    );
  }

  return (
    <p
      data-office-block={block.id}
      data-office-block-kind="paragraph"
      onMouseDown={(event) => { event.stopPropagation(); onSelect?.(block.id, event); }}
      style={style}
    >
      {block.runs.length === 0
        ? (block.empty ? <br /> : null)
        : block.runs.map((run, index) => <RunSpan key={index} run={run} block={block} />)}
    </p>
  );
}

function TableBlock({ block, selected, activeCell, onSelect, onCellSelect }) {
  // Lay the cells on the declared column count. A span covers its extra slots so the rows keep
  // their intended width instead of collapsing.
  const rows = [];
  for (let index = 0; index < block.rows; index += 1) {
    const rowCells = block.cells.filter(cell => cell.row === index);
    rows.push(rowCells);
  }

  return (
    <div
      data-office-block={block.id}
      data-office-block-kind="table"
      onMouseDown={(event) => { event.stopPropagation(); onSelect?.(block.id, event); }}
      style={{
        margin: '0.6em 0',
        outline: selected ? '2px solid #1f5eff' : undefined,
        outlineOffset: selected ? '2px' : undefined,
        cursor: 'pointer',
      }}
    >
      <table style={{
        width: '100%', borderCollapse: 'collapse', tableLayout: 'fixed',
        fontSize: '10.5pt', fontFamily: FONT_STACK,
      }}>
        <tbody>
          {rows.map((cells, rowIndex) => (
            <tr key={rowIndex}>
              {cells.map(cell => {
                const isActive = activeCell
                  && activeCell.row === cell.row && activeCell.col === cell.col;
                return (
                  <td
                    key={`${cell.row}-${cell.col}`}
                    colSpan={cell.span > 1 ? cell.span : undefined}
                    data-office-cell={`${cell.row}.${cell.col}`}
                    onMouseDown={(event) => {
                      // Stop here so clicking a cell selects the cell, not the whole table. The
                      // table stays selected as the enclosing block; the cell is the edit target.
                      event.stopPropagation();
                      onSelect?.(block.id, event);
                      onCellSelect?.(cell, block.id);
                    }}
                    style={{
                      border: '1px solid #b9bec8',
                      padding: '4px 6px',
                      verticalAlign: 'top',
                      whiteSpace: 'pre-wrap',
                      overflowWrap: 'break-word',
                      // A continuation cell of a vertical merge holds no content of its own.
                      background: cell.mergeContinue ? '#f4f5f7' : undefined,
                      outline: isActive ? '2px solid #1f5eff' : undefined,
                      outlineOffset: isActive ? '-2px' : undefined,
                    }}
                  >
                    {cell.text}
                  </td>
                );
              })}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

/**
 * One Word page column.
 *
 * The width and padding come from the file's `sectPr`, so a landscape or narrow-margin
 * document is drawn at its own shape. Height is left to content: paginating here would mean
 * reimplementing Word's line breaking, and a wrong page break is worse than no page break.
 */
export default function DocxCanvas({
  model, selectedId, hoveredId, onSelect, onHover, activeCell, onCellSelect,
}) {
  const page = model?.page || {};
  const pageStyle = useMemo(() => {
    const width = twipsToPx(page.widthTwips) || 794;
    return {
      width: `${width}px`,
      maxWidth: '100%',
      minHeight: '420px',
      background: '#ffffff',
      boxShadow: '0 2px 14px rgba(16, 20, 28, .14)',
      paddingTop: `${twipsToPx(page.marginTopTwips) || 96}px`,
      paddingBottom: `${twipsToPx(page.marginBottomTwips) || 96}px`,
      paddingLeft: `${twipsToPx(page.marginLeftTwips) || 96}px`,
      paddingRight: `${twipsToPx(page.marginRightTwips) || 96}px`,
      boxSizing: 'border-box',
      fontFamily: FONT_STACK,
      fontSize: '11pt',
      lineHeight: 1.65,
      color: '#1c2026',
    };
  }, [page]);

  if (!model) return null;

  return (
    <div
      className="office-docx-canvas"
      onMouseDown={(event) => { if (event.target === event.currentTarget) onSelect?.(null); }}
      style={pageStyle}
    >
      {model.blocks.map((block) => {
        const selected = selectedId === block.id;
        const onMouseEnter = () => onHover?.(block.id);
        const onMouseLeave = () => onHover?.(null);
        return (
          <div key={block.id} onMouseEnter={onMouseEnter} onMouseLeave={onMouseLeave}
            style={{ outlineColor: hoveredId === block.id && !selected ? '#9db4e8' : undefined }}>
            {block.kind === BLOCK_KINDS.TABLE
              ? <TableBlock block={block} selected={selected} activeCell={activeCell}
                onSelect={onSelect} onCellSelect={onCellSelect} />
              : <ParagraphBlock block={block} selected={selected} onSelect={onSelect} />}
          </div>
        );
      })}
      {model.blocks.length === 0 ? (
        <p style={{ color: '#9aa0ab', fontSize: '11pt' }}>这个文档里没有可编辑的内容。</p>
      ) : null}
    </div>
  );
}

export { twipsToPx, alignToCss, headingFontSize };
