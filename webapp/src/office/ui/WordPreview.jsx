// Word preview container.
//
// Kept separate from OfficePreview on purpose. A PPT slide is a canvas of positioned boxes and a
// Word document is a flow of blocks; putting both behind one component would mean every piece
// of state — draft, selection, undo, insert — carrying an "if this is the other format" branch.
// Two components that each do one thing are shorter than one component that does both.
//
// The editing model is narrower than the PPT side and honest about it: text, run formatting and
// paragraph properties. No dragging, because Word stores no coordinates.

import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { parseDocx } from '../docx/parse.js';
import DocxCanvas from '../docx/render.jsx';
import {
  applyParagraphProperties, applyParagraphText, applyRunStyle, applyTableCellText,
  deleteParagraph, insertParagraph, validateDocumentPart,
} from '../docx/write.js';
import { applyInsertImage } from '../docx/insert.js';
import { BLOCK_KINDS, ALIGNMENTS, blockPreview, cmToTwips, twipsToCm } from '../docx/model.js';
import { applyWordDraft } from './word-draft-render.js';
import { OFFICE_SAVE_NOTICE } from '../save.js';
import '../../styles/office-preview.css';

export const DOCX_PREVIEW_MAX_BYTES = 60 * 1024 * 1024;

const ALIGN_LABELS = [
  { value: 'left', label: '左' },
  { value: 'center', label: '中' },
  { value: 'right', label: '右' },
  { value: 'both', label: '两端' },
];

function applyDraftsToXml(xml, { block, cell, text, style, props, cellText }) {
  const before = xml;
  if (cellText !== null && cell) {
    xml = applyTableCellText(xml, cell, cellText);
  }
  if (block && block.kind !== BLOCK_KINDS.TABLE) {
    // Each writer scans the XML it is handed, but the earlier draft still uses the block's path
    // rather than offsets from the previous pass. Reusing only the stable path avoids applying
    // run/style edits to byte ranges that shifted when the paragraph text was replaced.
    const target = { id: block.id, path: block.path };
    if (text !== null && text !== block.text) {
      xml = applyParagraphText(xml, target, text);
    }
    if (Object.keys(style).length > 0) {
      xml = applyRunStyle(xml, target, style);
    }
    if (Object.keys(props).length > 0) {
      xml = applyParagraphProperties(xml, target, props);
    }
  }
  return { xml, changed: xml !== before };
}

function BlockList({ blocks, selectedId, onSelect }) {
  return (
    <div className="office-el-list">
      <div className="office-el-list-head">
        <span>共 {blocks.length} 个段落与表格</span>
      </div>
      <ul>
        {blocks.map((block) => (
          <li
            key={block.id}
            data-office-el-row={block.id}
            className={`office-el-row${selectedId === block.id ? ' is-selected' : ''}`}
            onClick={() => onSelect(block.id)}
          >
            <span className="office-el-glyph">{block.kind === BLOCK_KINDS.TABLE ? '▦' : '¶'}</span>
            <span className="office-el-label">{block.label}</span>
            {block.preview ? <span className="office-el-preview">{block.preview}</span> : null}
          </li>
        ))}
        {blocks.length === 0 ? <li className="office-el-empty">这个文档里没有段落</li> : null}
      </ul>
    </div>
  );
}

function BlockPanel({
  block, text, onTextChange, style, onStyleChange, onAlign, onIndent, onDelete,
  activeCell, onCellSelect, onCellTextChange, disabled, props,
}) {
  if (!block) {
    return (
      <div className="office-props office-props-empty">
        <p>点文档里的段落，这里会显示它的属性。</p>
        <p className="office-props-tip">也可以在左侧列表里选。</p>
      </div>
    );
  }
  if (block.kind === BLOCK_KINDS.TABLE) {
    // Cell text is edited one cell at a time. Whole-table editing would mean a grid of inputs and
    // a merge model, and the common case here is fixing one number in one cell.
    const cell = activeCell || block.cells[0] || null;
    return (
      <div className="office-props" data-office-el={block.id}>
        <div className="office-props-head">
          <div>
            <div className="office-props-title">{block.label}</div>
            <div className="office-props-path">点表格里的单元格选中它</div>
          </div>
        </div>
        <div className="office-field-group">
          <div className="office-field-group-title">单元格</div>
          <div className="office-field-row">
            <label className="office-field">
              <span className="office-field-label">行</span>
              <input type="number" min="1" max={block.rows} disabled={disabled}
                value={(cell?.row ?? 0) + 1}
                onChange={event => {
                  const row = Math.max(0, Math.min(block.rows - 1, Number(event.target.value) - 1));
                  const target = block.cells.find(item => item.row === row && item.col === (cell?.col ?? 0));
                  if (target) onCellSelect?.(target);
                }} />
            </label>
            <label className="office-field">
              <span className="office-field-label">列</span>
              <input type="number" min="1" max={block.cols} disabled={disabled}
                value={(cell?.col ?? 0) + 1}
                onChange={event => {
                  const col = Math.max(0, Math.min(block.cols - 1, Number(event.target.value) - 1));
                  const target = block.cells.find(item => item.row === (cell?.row ?? 0) && item.col === col);
                  if (target) onCellSelect?.(target);
                }} />
            </label>
          </div>
          <label className="office-field">
            <span className="office-field-label">内容</span>
            <textarea rows={3} disabled={disabled || !cell}
              value={cell?.text ?? ''}
              onChange={event => onCellTextChange?.(event.target.value)} />
          </label>
          {cell?.mergeContinue ? (
            <p className="office-field-note">这个单元格是纵向合并的延续格，改动不会显示在页面上。</p>
          ) : null}
        </div>
      </div>
    );
  }

  const sizePt = style.size ? Math.round(style.size) : Math.round(block.runs[0]?.size || 0);
  return (
    <div className="office-props" data-office-el={block.id}>
      <div className="office-props-head">
        <div>
          <div className="office-props-title">{block.label}</div>
          <div className="office-props-path">第 {block.path[block.path.length - 1] + 1} 个子节点</div>
        </div>
        <button type="button" className="office-props-delete" disabled={disabled}
          onClick={() => onDelete?.(block.id)}>
          删除
        </button>
      </div>

      <div className="office-field-group">
        <div className="office-field-group-title">文字</div>
        <label className="office-field">
          <span className="office-field-label">段落内容</span>
          <textarea
            rows={4}
            disabled={disabled}
            value={text ?? block.text}
            onChange={event => onTextChange?.(event.target.value)}
          />
        </label>
      </div>

      <div className="office-field-group">
        <div className="office-field-group-title">段落</div>
        <div className="office-field">
          <span className="office-field-label">对齐</span>
          <div className="office-toggle-row">
            {ALIGN_LABELS.map(option => (
              <button key={option.value} type="button"
                className={`office-toggle${(style.align || block.align) === option.value ? ' is-on' : ''}`}
                disabled={disabled}
                onClick={() => onAlign?.(option.value)}>
                {option.label}
              </button>
            ))}
          </div>
        </div>
        <div className="office-field-row">
          <label className="office-field">
            <span className="office-field-label">左缩进 cm</span>
            <input type="number" step="0.1" min="0" disabled={disabled}
              value={(props?.indLeftCm ?? twipsToCm(block.indLeftTwips || 0)).toFixed(2)}
              onChange={event => onIndent?.({ leftCm: Number(event.target.value) || 0 })} />
          </label>
          <label className="office-field">
            <span className="office-field-label">首行缩进 cm</span>
            <input type="number" step="0.1" min="0" disabled={disabled}
              value={(props?.indFirstCm ?? twipsToCm(block.indFirstTwips || 0)).toFixed(2)}
              onChange={event => onIndent?.({ firstCm: Number(event.target.value) || 0 })} />
          </label>
        </div>
      </div>

      <div className="office-field-group">
        <div className="office-field-group-title">字形</div>
        <div className="office-field-row">
          <label className="office-field">
            <span className="office-field-label">字号 pt</span>
            <input type="number" min="1" max="200" disabled={disabled}
              value={sizePt || ''}
              onChange={event => {
                const pt = Number(event.target.value);
                if (Number.isFinite(pt) && pt > 0) onStyleChange?.({ size: pt });
              }} />
          </label>
          <label className="office-field">
            <span className="office-field-label">颜色</span>
            <input type="color" disabled={disabled}
              value={/^#[0-9a-fA-F]{6}$/.test(style.color || '')
                ? style.color
                : (block.runs[0]?.color || '#1c2026')}
              onChange={event => onStyleChange?.({ color: event.target.value })} />
          </label>
        </div>
        <div className="office-field">
          <span className="office-field-label">样式</span>
          <div className="office-toggle-row">
            <button type="button"
              className={`office-toggle${style.bold ? ' is-on' : ''}`} disabled={disabled}
              onClick={() => onStyleChange?.({ bold: !style.bold })}>粗</button>
            <button type="button"
              className={`office-toggle${style.italic ? ' is-on' : ''}`} disabled={disabled}
              onClick={() => onStyleChange?.({ italic: !style.italic })}>斜</button>
            <button type="button"
              className={`office-toggle${style.underline ? ' is-on' : ''}`} disabled={disabled}
              onClick={() => onStyleChange?.({ underline: !style.underline })}>下划线</button>
          </div>
        </div>
      </div>
    </div>
  );
}

export default function WordPreview({ file, url, onSave }) {
  const [status, setStatus] = useState('loading');
  const [model, setModel] = useState(null);
  const [error, setError] = useState('');
  const [selectedId, setSelectedId] = useState(null);
  const [saving, setSaving] = useState(false);
  const [notice, setNotice] = useState('');
  const [draftText, setDraftText] = useState(null);
  const [draftStyle, setDraftStyle] = useState({});
  const [draftProps, setDraftProps] = useState({});
  // Cell editing is its own draft: it targets a cell inside a table block, and the paragraph draft
  // has no meaning for a table. Keeping them apart also means selecting a different cell does not
  // resurrect a pending paragraph edit.
  const [activeCell, setActiveCell] = useState(null);
  const [draftCellText, setDraftCellText] = useState(null);
  const pendingBytesRef = useRef(null);
  const [hasPending, setHasPending] = useState(false);
  const imageInputRef = useRef(null);
  const abortRef = useRef(null);

  const dirty = draftText !== null || Object.keys(draftStyle).length > 0
    || Object.keys(draftProps).length > 0 || draftCellText !== null || hasPending;

  const adopt = useCallback((next) => {
    setModel(next);
    setDraftText(null);
    setDraftStyle({});
    setDraftProps({});
    setDraftCellText(null);
  }, []);

  useEffect(() => {
    let cancelled = false;
    abortRef.current?.abort();
    const controller = new AbortController();
    abortRef.current = controller;

    setStatus('loading');
    setError('');
    setModel(null);
    setSelectedId(null);
    setNotice('');
    setDraftText(null);
    setDraftStyle({});
    setDraftProps({});

    if (!url) {
      setStatus('error');
      setError('缺少文件地址');
      return () => { cancelled = true; };
    }
    if (file?.size && file.size > DOCX_PREVIEW_MAX_BYTES) {
      setStatus('error');
      setError(`文件较大（${Math.round(file.size / 1048576)} MB），当前最多预览 ${DOCX_PREVIEW_MAX_BYTES / 1048576} MB，请下载后查看。`);
      return () => { cancelled = true; };
    }

    const load = async () => {
      try {
        const response = await fetch(url, { signal: controller.signal });
        if (!response.ok) throw new Error(`下载失败 HTTP ${response.status}`);
        const buffer = await response.arrayBuffer();
        if (cancelled) return;
        const parsed = await parseDocx(buffer);
        if (cancelled) return;
        setModel(parsed);
        setStatus('ready');
        const first = parsed.blocks.find(block => block.kind === BLOCK_KINDS.PARAGRAPH && block.text);
        setSelectedId((first || parsed.blocks[0])?.id || null);
      } catch (err) {
        if (cancelled || err?.name === 'AbortError') return;
        setStatus('error');
        setError(err?.message || '无法解析这个文件');
      }
    };
    load();

    return () => {
      cancelled = true;
      controller.abort();
    };
  }, [url, file?.size]);

  const selectedBlock = useMemo(
    () => (model?.blocks || []).find(block => block.id === selectedId) || null,
    [model, selectedId],
  );

  // Selection change drops the drafts: they are keyed to one block, and applying them to the
  // next selection would write one paragraph's text onto another.
  useEffect(() => {
    setDraftText(null);
    setDraftStyle({});
    setDraftProps({});
    setNotice('');
  }, [selectedId]);

  const effectiveStyle = useMemo(() => {
    const first = selectedBlock?.runs?.[0] || {};
    return {
      bold: draftStyle.bold !== undefined ? draftStyle.bold : Boolean(first.bold),
      italic: draftStyle.italic !== undefined ? draftStyle.italic : Boolean(first.italic),
      underline: draftStyle.underline !== undefined ? draftStyle.underline : Boolean(first.underline),
      size: draftStyle.size !== undefined ? draftStyle.size : (first.size || 0),
      color: draftStyle.color !== undefined ? draftStyle.color : (first.color || ''),
      align: draftProps.align !== undefined ? draftProps.align : (selectedBlock?.align || 'left'),
    };
  }, [draftProps.align, draftStyle, selectedBlock]);

  const effectiveProps = useMemo(() => ({
    indLeftCm: draftProps.indLeftCm !== undefined
      ? draftProps.indLeftCm
      : twipsToCm(selectedBlock?.indLeftTwips || 0),
    indFirstCm: draftProps.indFirstCm !== undefined
      ? draftProps.indFirstCm
      : twipsToCm(selectedBlock?.indFirstTwips || 0),
  }), [draftProps.indFirstCm, draftProps.indLeftCm, selectedBlock]);

  const renderedModel = useMemo(() => applyWordDraft(model, selectedId, {
    text: draftText,
    style: draftStyle,
    props: draftProps,
    cell: draftCellText !== null && activeCell && selectedBlock?.kind === BLOCK_KINDS.TABLE
      ? { row: activeCell.row, col: activeCell.col, text: draftCellText }
      : null,
  }), [activeCell, draftCellText, draftProps, draftStyle, draftText, model, selectedBlock, selectedId]);

  const applyToBytes = useCallback(async () => {
    if (!model) return null;
    const xml = await model.pkg.text(model.part);
    const prepared = applyDraftsToXml(xml, {
      block: selectedBlock,
      cell: activeCell,
      text: draftText,
      style: draftStyle,
      props: draftProps,
      cellText: draftCellText,
    });
    if (!prepared.changed) return null;

    const problems = validateDocumentPart(prepared.xml);
    if (problems.length > 0) throw new Error(`写入前的检查未通过：${problems.join('；')}`);
    return { xml: prepared.xml };
  }, [activeCell, draftCellText, draftProps, draftStyle, draftText, model, selectedBlock]);

  /**
   * Persist the visible drafts onto a model and return the reparsed result.
   *
   * Structural operations (insert/delete/image) used to run against the last saved XML and then
   * adopt that reparsed model, which silently reverted any text/style/cell change the user had
   * not saved yet. Applying the drafts to the intermediate model keeps both kinds of edit.
   */
  const persistDrafts = useCallback(async (sourceModel, block, cell) => {
    const xml = await sourceModel.pkg.text(sourceModel.part);
    const prepared = applyDraftsToXml(xml, {
      block,
      cell,
      text: draftText,
      style: draftStyle,
      props: draftProps,
      cellText: draftCellText,
    });
    if (!prepared.changed) return { model: sourceModel, bytes: null };
    const problems = validateDocumentPart(prepared.xml);
    if (problems.length > 0) throw new Error(`写入前的检查未通过：${problems.join('；')}`);
    sourceModel.pkg.setText(sourceModel.part, prepared.xml);
    const bytes = await sourceModel.pkg.save();
    return { model: await parseDocx(bytes), bytes };
  }, [draftCellText, draftProps, draftStyle, draftText]);

  /** Write locally and re-parse, so the canvas shows exactly what the bytes now say. */
  const commit = useCallback(async (mutate, successNote) => {
    if (!model || saving) return;
    setSaving(true);
    setNotice('');
    try {
      let xml = await model.pkg.text(model.part);
      const next = await mutate(xml);
      if (!next || next === xml) {
        setNotice('没有产生改动');
        return;
      }
      const problems = validateDocumentPart(next);
      if (problems.length > 0) throw new Error(`写入前的检查未通过：${problems.join('；')}`);

      model.pkg.setText(model.part, next);
      const structuralBytes = await model.pkg.save();
      let reparsed = await parseDocx(structuralBytes);
      // Block ids are path-derived and survive an insertion/deletion elsewhere. Re-finding the
      // edited block in the structural result lets drafts apply against positions that are valid
      // in the new XML instead of offsets from the pre-mutation model.
      const draftBlock = selectedId
        ? reparsed.blocks.find(block => block.id === selectedId) || null
        : null;
      const draftCell = draftBlock?.kind === BLOCK_KINDS.TABLE ? activeCell : null;
      const flushed = await persistDrafts(reparsed, draftBlock, draftCell);
      const bytes = flushed.bytes || structuralBytes;
      if (flushed.bytes) reparsed = flushed.model;
      pendingBytesRef.current = bytes;
      setHasPending(true);
      adopt(reparsed);
      if (successNote) setNotice(successNote);
      return bytes;
    } catch (err) {
      setNotice(`操作失败：${err.message}`);
      return null;
    } finally {
      setSaving(false);
    }
  }, [activeCell, adopt, model, persistDrafts, saving, selectedId]);

  const handleSave = useCallback(async () => {
    if (!model || saving) return;
    if (!dirty) {
      setNotice('没有可写入的改动');
      return;
    }
    setSaving(true);
    setNotice('');
    try {
      const prepared = await applyToBytes();
      let bytes = pendingBytesRef.current;
      if (prepared) {
        const problems = validateDocumentPart(prepared.xml);
        if (problems.length > 0) throw new Error(`写入前的检查未通过：${problems.join('；')}`);

        model.pkg.setText(model.part, prepared.xml);
        bytes = await model.pkg.save();
        adopt(await parseDocx(bytes));
      }
      if (!bytes) {
        setNotice('没有可写入的改动');
        return;
      }
      const published = await onSave?.({
        bytes,
        fileName: file?.name || 'document.docx',
        slideCount: undefined,
        changes: [{ op: 'docx-edit' }],
      });
      if (onSave && !published) {
        setNotice('已写入本地，但没能存进会话，请重试保存');
        return;
      }
      pendingBytesRef.current = null;
      setHasPending(false);
      setNotice(OFFICE_SAVE_NOTICE);
    } catch (err) {
      setNotice(`写入失败：${err.message}`);
    } finally {
      setSaving(false);
    }
  }, [adopt, applyToBytes, dirty, file?.name, model, onSave, saving]);

  const handleInsertParagraph = useCallback(async () => {
    const anchor = selectedBlock;
    const bytes = await commit(
      xml => insertParagraph(xml, anchor, '新段落'),
      '已在下方插入一段',
    );
    if (bytes) {
      // Land the selection on the new paragraph so the user can type immediately.
      const reparsed = await parseDocx(bytes);
      const anchorIndex = reparsed.blocks.findIndex(block => block.id === anchor?.id);
      const next = reparsed.blocks[anchorIndex + 1];
      if (next) setSelectedId(next.id);
      adopt(reparsed);
    }
  }, [adopt, commit, selectedBlock]);

  const handleDeleteBlock = useCallback(async () => {
    if (!selectedBlock || !model) return;
    const paragraphs = model.blocks.filter(block => block.kind === BLOCK_KINDS.PARAGRAPH);
    if (paragraphs.length <= 1) {
      setNotice('这是文档最后一段，不能再删');
      return;
    }
    await commit(xml => deleteParagraph(xml, selectedBlock, paragraphs.length), '已删除该段');
    setSelectedId(null);
  }, [commit, model, selectedBlock]);

  /** Selecting a cell inside a table block. The table itself stays selected as the block. */
  const handleCellSelect = useCallback((cell) => {
    setActiveCell(cell);
    setDraftCellText(null);
    setNotice(`已选中第 ${cell.row + 1} 行第 ${cell.col + 1} 列`);
  }, []);

  /**
   * Insert a picture.
   *
   * A picture needs the package (media part + relationship + content type), so it cannot go
   * through the paragraph path. It writes immediately and re-parses, the same way the PPT inserts
   * do, because the result only becomes addressable once it exists in the file.
   */
  const handleInsertImage = useCallback(async (picked) => {
    if (!model || !picked || saving) return;
    setSaving(true);
    setNotice('');
    try {
      const bytes = new Uint8Array(await picked.arrayBuffer());
      const ext = (picked.name.split('.').pop() || 'png').toLowerCase();
      // Keep edits that are still drafts, then add the picture to that intermediate model.
      // Otherwise inserting the picture would visibly revert the paragraph or cell underneath it.
      const draftBlock = selectedId
        ? model.blocks.find(block => block.id === selectedId) || null
        : null;
      const draftCell = draftBlock?.kind === BLOCK_KINDS.TABLE ? activeCell : null;
      const flushed = await persistDrafts(model, draftBlock, draftCell);
      const target = flushed.model;
      await applyInsertImage(target, { bytes, ext, name: picked.name, widthCm: 12 });

      const problems = validateDocumentPart(await target.pkg.text(target.part));
      if (problems.length > 0) throw new Error(`写入前的检查未通过：${problems.join('；')}`);

      const saved = await target.pkg.save();
      const reparsed = await parseDocx(saved);
      pendingBytesRef.current = saved;
      setHasPending(true);
      adopt(reparsed);
      setNotice('已插入图片，按页面宽度缩放。需要精确尺寸请用 Word 调整');
    } catch (err) {
      setNotice(`插入图片失败：${err.message}`);
    } finally {
      setSaving(false);
    }
  }, [activeCell, adopt, model, persistDrafts, saving, selectedId]);

  if (status === 'loading') {
    return <div className="office-preview-state">正在解析文档…</div>;
  }
  if (status === 'error') {
    return <div className="office-preview-state is-error">{error}</div>;
  }
  if (!model) return null;

  return (
    <div className="office-preview">
      <div className="office-preview-toolbar">
        <span className="office-page-label">
          {model.blocks.length} 块 · {model.outline.length} 个标题
        </span>
        <span className="office-toolbar-divider" />
        <button type="button" className="office-page-btn" disabled={saving}
          onClick={handleInsertParagraph}>插入段落</button>
        <button type="button" className="office-page-btn" disabled={saving || !selectedBlock}
          onClick={handleDeleteBlock}>删除段落</button>
        <button type="button" className="office-page-btn" disabled={saving}
          onClick={() => imageInputRef.current?.click?.()}>插入图片</button>
        <input
          ref={imageInputRef}
          type="file"
          accept="image/png,image/jpeg,image/gif,image/webp"
          style={{ display: 'none' }}
          onChange={(event) => {
            const picked = event.target.files?.[0];
            // Reset so choosing the same file twice still fires a change event.
            event.target.value = '';
            if (picked) handleInsertImage(picked);
          }}
        />
        <span className="office-toolbar-spacer" />
        {dirty ? <span className="office-dirty">已修改</span> : null}
        <button type="button" className="office-save-btn" disabled={saving}
          onClick={handleSave}>
          {saving ? '写入中…' : '写入文档'}
        </button>
      </div>

      {notice ? <div className="office-preview-notice">{notice}</div> : null}

      <div className="office-preview-body">
        <div className="office-preview-stage office-docx-stage">
          <DocxCanvas
            model={renderedModel}
            selectedId={selectedId}
            hoveredId={null}
            onSelect={setSelectedId}
            onHover={() => {}}
            activeCell={activeCell}
            onCellSelect={handleCellSelect}
          />
        </div>
        <div className="office-preview-side">
          <BlockList blocks={renderedModel.blocks} selectedId={selectedId} onSelect={setSelectedId} />
          <BlockPanel
            block={selectedBlock}
            text={draftText}
            style={effectiveStyle}
            props={effectiveProps}
            onTextChange={setDraftText}
            onStyleChange={(patch) => setDraftStyle(current => ({ ...current, ...patch }))}
            onAlign={(align) => setDraftProps(current => ({ ...current, align }))}
            onIndent={({ leftCm, firstCm }) => setDraftProps((current) => {
              const next = { ...current };
              if (leftCm !== undefined) next.indLeftCm = leftCm;
              if (firstCm !== undefined) next.indFirstCm = firstCm;
              return next;
            })}
            onDelete={handleDeleteBlock}
            activeCell={activeCell}
            onCellSelect={handleCellSelect}
            onCellTextChange={setDraftCellText}
            disabled={saving}
          />
        </div>
      </div>
    </div>
  );
}

export { ALIGN_LABELS, cmToTwips, ALIGNMENTS };
