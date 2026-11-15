// Property panel: the editing surface.
//
// Deliberately NOT contenteditable on the canvas. Every user change goes through a field
// tied to one model property, and the canvas re-renders from the model. That keeps one
// code path for "user typed a value" and "AI changed a field", and it means no
// pixel-to-XML inference ever has to happen.
//
// Two draft shapes exist because text and style are written by different functions:
//   draft.paragraphs  full paragraph list, text edits
//   draft.styles      per-paragraph style patches, keyed by index
// Splitting them lets a text edit and a style edit on the same paragraph both survive
// until save, without one overwriting the other.

import React, { useState } from 'react';
import { SHAPE_KINDS } from '../pptx/model.js';

function Field({ label, value, onChange, type = 'text', hint, disabled, min, max, step }) {
  return (
    <label className="office-field">
      <span className="office-field-label">{label}</span>
      <input
        type={type}
        value={value ?? ''}
        disabled={disabled}
        min={min}
        max={max}
        step={step}
        onChange={event => onChange?.(event.target.value)}
      />
      {hint ? <span className="office-field-hint">{hint}</span> : null}
    </label>
  );
}

function Readonly({ label, value }) {
  return (
    <div className="office-field office-field-readonly">
      <span className="office-field-label">{label}</span>
      <span className="office-field-value">{value}</span>
    </div>
  );
}

const ALIGN_OPTIONS = [
  { value: 'l', label: '左对齐' },
  { value: 'ctr', label: '居中' },
  { value: 'r', label: '右对齐' },
  { value: 'just', label: '两端对齐' },
];

export function alignValue(value) {
  if (value === 'ctr' || value === 'r' || value === 'just') return value;
  return 'l';
}

function paragraphsOf(shape, draft) {
  return draft?.paragraphs || shape?.textBody?.paragraphs || [];
}

/** Merge draft style patches over the paragraph's parsed style for display. */
function styledParagraph(para, stylePatch) {
  if (!stylePatch) return para;
  const merged = { ...para };
  if (stylePatch.bold !== undefined) merged.bold = stylePatch.bold;
  if (stylePatch.italic !== undefined) merged.italic = stylePatch.italic;
  if (stylePatch.underline !== undefined) merged.underline = stylePatch.underline;
  if (stylePatch.size !== undefined) merged.size = stylePatch.size;
  if (stylePatch.color !== undefined) merged.color = stylePatch.color;
  if (stylePatch.align !== undefined) merged.align = stylePatch.align;
  return merged;
}

function TextFields({ shape, draft, onDraftChange, onStyleChange, disabled }) {
  const paragraphs = paragraphsOf(shape, draft);
  const [activeIndex, setActiveIndex] = useState(0);
  const safeIndex = Math.min(activeIndex, Math.max(paragraphs.length - 1, 0));
  const active = styledParagraph(paragraphs[safeIndex] || {}, draft?.styles?.[safeIndex]);

  if (paragraphs.length === 0) {
    return (
      <div className="office-field-group">
        <div className="office-field-group-title">文字</div>
        <p className="office-field-empty">这个元素没有文字</p>
      </div>
    );
  }

  return (
    <div className="office-field-group">
      <div className="office-field-group-title">文字</div>
      {paragraphs.map((para, index) => (
        <label key={index} className="office-field">
          <span className="office-field-label">
            第 {index + 1} 段
            {para.level ? ` · 层级 ${para.level}` : ''}
            {para.text.length > 40 ? ` · ${para.text.length} 字` : ''}
          </span>
          <textarea
            rows={2}
            disabled={disabled}
            value={para.text}
            onFocus={() => setActiveIndex(index)}
            onChange={event => {
              const next = paragraphs.map((item, i) => (
                i === index ? { ...item, text: event.target.value } : item
              ));
              onDraftChange?.({ paragraphs: next });
            }}
          />
        </label>
      ))}

      <div className="office-field-group-title" style={{ marginTop: 12 }}>
        样式
        <span className="office-field-scope">作用于第 {safeIndex + 1} 段</span>
      </div>
      {paragraphs.length > 1 ? (
        <label className="office-field">
          <span className="office-field-label">选择段落</span>
          <select
            value={safeIndex}
            disabled={disabled}
            onChange={event => setActiveIndex(Number(event.target.value))}
          >
            {paragraphs.map((para, index) => (
              <option key={index} value={index}>
                第 {index + 1} 段{para.text ? `：${para.text.slice(0, 12)}` : '（空）'}
              </option>
            ))}
          </select>
        </label>
      ) : null}

      <div className="office-field-row">
        <Field label="字号 pt" type="number" min="1" max="400" disabled={disabled}
          value={active.size ? Math.round(active.size / 100) : ''}
          onChange={value => {
            const pt = Number(value);
            if (!Number.isFinite(pt) || pt <= 0) return;
            onStyleChange?.(safeIndex, { size: Math.round(pt * 100) });
          }} />
        <Field label="颜色" type="color" disabled={disabled}
          value={hexOnly(active.color) || '#16181b'}
          onChange={value => onStyleChange?.(safeIndex, { color: value })} />
      </div>

      <div className="office-field-row">
        <label className="office-field">
          <span className="office-field-label">对齐</span>
          <select
            value={alignValue(active.align)}
            disabled={disabled}
            onChange={event => onStyleChange?.(safeIndex, { align: event.target.value })}
          >
            {ALIGN_OPTIONS.map(option => (
              <option key={option.value} value={option.value}>{option.label}</option>
            ))}
          </select>
        </label>
        <div className="office-field">
          <span className="office-field-label">字形</span>
          <div className="office-toggle-row">
            <button type="button"
              className={`office-toggle${active.bold ? ' is-on' : ''}`}
              disabled={disabled}
              onClick={() => onStyleChange?.(safeIndex, { bold: !active.bold })}>
              粗
            </button>
            <button type="button"
              className={`office-toggle${active.italic ? ' is-on' : ''}`}
              disabled={disabled}
              onClick={() => onStyleChange?.(safeIndex, { italic: !active.italic })}>
              斜
            </button>
            <button type="button"
              className={`office-toggle${active.underline ? ' is-on' : ''}`}
              disabled={disabled}
              onClick={() => onStyleChange?.(safeIndex, { underline: !active.underline })}>
              下划线
            </button>
          </div>
        </div>
      </div>
    </div>
  );
}

/**
 * A color input only accepts #rrggbb. Theme-driven runs carry a `var(--office-scheme-…)`
 * reference at parse time, which has to collapse to something the picker understands.
 */
function hexOnly(value) {
  if (!value) return '';
  const match = /^#([0-9a-fA-F]{6})$/.exec(value);
  if (match) return value;
  const tail = /,\s*(#[0-9a-fA-F]{6})\)$/.exec(value);
  return tail ? tail[1] : '';
}

function GeometryFields({ shape, draftBox, onBoxChange, onAlignToSlide, disabled }) {
  const box = draftBox || shape?.box || { x: 0, y: 0, cx: 0, cy: 0 };
  const toCm = emu => (emu / 360000).toFixed(2);
  const fromCm = value => Math.round((Number(value) || 0) * 360000);
  return (
    <div className="office-field-group">
      <div className="office-field-group-title">位置与尺寸（厘米）</div>
      {shape?.inherited ? (
        <p className="office-field-note">这个元素的位置继承自版式。改动会先问你是只改这一页还是所有使用该版式的页。</p>
      ) : null}
      <div className="office-field-row">
        <Field label="左" type="number" step="0.1" disabled={disabled}
          value={toCm(box.x)} onChange={v => onBoxChange?.({ ...box, x: fromCm(v) })} />
        <Field label="上" type="number" step="0.1" disabled={disabled}
          value={toCm(box.y)} onChange={v => onBoxChange?.({ ...box, y: fromCm(v) })} />
      </div>
      <div className="office-field-row">
        <Field label="宽" type="number" step="0.1" min="0.1" disabled={disabled}
          value={toCm(box.cx)} onChange={v => onBoxChange?.({ ...box, cx: Math.max(1, fromCm(v)) })} />
        <Field label="高" type="number" step="0.1" min="0.1" disabled={disabled}
          value={toCm(box.cy)} onChange={v => onBoxChange?.({ ...box, cy: Math.max(1, fromCm(v)) })} />
      </div>

      <div className="office-field">
        <span className="office-field-label">相对页面</span>
        <div className="office-toggle-row">
          <button type="button" className="office-toggle" disabled={disabled}
            onClick={() => onAlignToSlide?.('left')} title="靠左">左</button>
          <button type="button" className="office-toggle" disabled={disabled}
            onClick={() => onAlignToSlide?.('centerX')} title="水平居中">中</button>
          <button type="button" className="office-toggle" disabled={disabled}
            onClick={() => onAlignToSlide?.('right')} title="靠右">右</button>
          <button type="button" className="office-toggle" disabled={disabled}
            onClick={() => onAlignToSlide?.('top')} title="靠上">上</button>
          <button type="button" className="office-toggle" disabled={disabled}
            onClick={() => onAlignToSlide?.('centerY')} title="垂直居中">中</button>
          <button type="button" className="office-toggle" disabled={disabled}
            onClick={() => onAlignToSlide?.('bottom')} title="靠下">下</button>
        </div>
        <span className="office-field-hint">直接对齐到幻灯片边界或中线</span>
      </div>
    </div>
  );
}

function TableFields({ shape }) {
  const table = shape?.table;
  if (!table) return null;
  return (
    <div className="office-field-group">
      <div className="office-field-group-title">表格 {table.rows} × {table.cols}</div>
      <p className="office-field-note">单元格内容编辑在表格阶段开放，当前可改整体位置与尺寸</p>
      <div className="office-table-preview">
        {Array.from({ length: Math.min(table.rows, 6) }).map((_, row) => (
          <div key={row} className="office-table-row">
            {Array.from({ length: Math.min(table.cols, 8) }).map((__, col) => {
              const cell = table.cells.find(item => item.row === row && item.col === col);
              return <span key={col} className="office-table-cell">{cell?.text || ''}</span>;
            })}
          </div>
        ))}
      </div>
    </div>
  );
}

export default function PropertyPanel({
  shape, draft, draftBox, disabled = false, readOnly = false,
  onDraftChange, onStyleChange, onBoxChange, onAlignToSlide, onDelete,
}) {
  // `readOnly` remains a real posture, not dead code: Word documents reuse this panel
  // before their writer exists, and a preview-only surface must not accept input.
  const locked = disabled || readOnly;

  if (!shape) {
    return (
      <div className="office-props office-props-empty">
        <p>点画布上的元素，这里会显示它的属性。</p>
        <p className="office-props-tip">也可以在左侧列表里选。</p>
      </div>
    );
  }

  return (
    <div className="office-props" data-office-el={shape.elId}>
      <div className="office-props-head">
        <div>
          <div className="office-props-title">{shape.label}</div>
          <div className="office-props-path">{shape.anchor.part}</div>
        </div>
        <button type="button" className="office-props-delete" disabled={locked}
          onClick={() => onDelete?.(shape.elId)}>
          删除
        </button>
      </div>

      <Readonly label="元素 id" value={shape.elId} />

      {(shape.kind === SHAPE_KINDS.TEXT || shape.kind === SHAPE_KINDS.FRAME) ? (
        <TextFields
          shape={shape}
          draft={draft}
          onDraftChange={onDraftChange}
          onStyleChange={onStyleChange}
          disabled={locked}
        />
      ) : null}

      {shape.kind === SHAPE_KINDS.TABLE ? <TableFields shape={shape} /> : null}

      {shape.kind === SHAPE_KINDS.PICTURE ? (
        <div className="office-field-group">
          <div className="office-field-group-title">图片</div>
          <Readonly label="文件名" value={shape.imageName || '（未知）'} />
          <p className="office-field-note">换图在插入图片阶段开放</p>
        </div>
      ) : null}

      <GeometryFields shape={shape} draftBox={draftBox} onBoxChange={onBoxChange}
        onAlignToSlide={onAlignToSlide} disabled={locked} />
    </div>
  );
}

export { Field, Readonly };
