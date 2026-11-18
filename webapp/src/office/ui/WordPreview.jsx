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
import { applyWordDraft, wordCellDraftKey } from './word-draft-render.js';
import { OFFICE_SAVE_NOTICE } from '../save.js';
import { OFFICE_SYNC_NOTICE, OFFICE_SERVER_UNSUPPORTED_NOTICE } from '../version-binding.js';
import LocalDraftTextarea from './LocalDraftTextarea.jsx';
import {
  officePanelWidthFromPointer, readOfficePanelWidth, saveOfficePanelWidth,
} from './office-panel-width.js';
import '../../styles/office-preview.css';

export const DOCX_PREVIEW_MAX_BYTES = 60 * 1024 * 1024;

const EMPTY_WORD_DRAFT = { texts: {}, styles: {}, props: {}, cells: {} };

const ALIGN_LABELS = [
  { value: 'left', label: '左' },
  { value: 'center', label: '中' },
  { value: 'right', label: '右' },
  { value: 'both', label: '两端' },
];

function applyDraftsToXml(xml, model, draft) {
  const before = xml;
  const texts = draft?.texts || {};
  const styles = draft?.styles || {};
  const props = draft?.props || {};
  const cells = draft?.cells || {};
  for (const block of model?.blocks || []) {
    if (block.kind === BLOCK_KINDS.TABLE) {
      for (const cell of block.cells || []) {
        const key = wordCellDraftKey(block.id, cell.row, cell.col);
        if (Object.prototype.hasOwnProperty.call(cells, key)) {
          xml = applyTableCellText(xml, cell, cells[key]);
        }
      }
      continue;
    }
    if (block.kind !== BLOCK_KINDS.PARAGRAPH) continue;
    const target = { id: block.id, path: block.path };
    const hasText = Object.prototype.hasOwnProperty.call(texts, block.id);
    const hasStyle = Object.prototype.hasOwnProperty.call(styles, block.id);
    const hasProps = Object.prototype.hasOwnProperty.call(props, block.id);
    if (hasText && texts[block.id] !== block.text) {
      xml = applyParagraphText(xml, target, texts[block.id]);
    }
    if (hasStyle && Object.keys(styles[block.id] || {}).length > 0) {
      xml = applyRunStyle(xml, target, styles[block.id]);
    }
    if (hasProps && Object.keys(props[block.id] || {}).length > 0) {
      xml = applyParagraphProperties(xml, target, props[block.id]);
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
  activeCell, onCellSelect, onCellTextChange, cellText, disabled, props,
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
    const cell = activeCell?.blockId === block.id ? activeCell : (block.cells?.[0] || null);
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
            <LocalDraftTextarea key={cell ? `${block.id}:${cell.row}:${cell.col}` : block.id}
              rows={3} disabled={disabled || !cell}
              value={cellText ?? cell?.text ?? ''}
              onCommit={(value) => onCellTextChange?.(value)} />
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
          <LocalDraftTextarea
            key={block.id}
            rows={4}
            disabled={disabled}
            value={text ?? block.text}
            onCommit={(value) => onTextChange?.(value)}
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

export default function WordPreview({
  file, url, onSave, onSync, latestVersion,
}) {
  const [status, setStatus] = useState('loading');
  const [model, setModel] = useState(null);
  const [error, setError] = useState('');
  const [selectedId, setSelectedId] = useState(null);
  const [saving, setSaving] = useState(false);
  const [notice, setNotice] = useState('');
  const [draft, setDraft] = useState(EMPTY_WORD_DRAFT);
  const [activeCell, setActiveCell] = useState(null);
  const [editing, setEditing] = useState(false);
  const [askOpen, setAskOpen] = useState(false);
  const [askText, setAskText] = useState('');
  const [panelWidth, setPanelWidth] = useState(() => readOfficePanelWidth());
  const pendingBytesRef = useRef(null);
  const [hasPending, setHasPending] = useState(false);
  const imageInputRef = useRef(null);
  const abortRef = useRef(null);

  const dirty = Object.keys(draft.texts).length > 0 || Object.keys(draft.styles).length > 0
    || Object.keys(draft.props).length > 0 || Object.keys(draft.cells).length > 0 || hasPending;

  const adopt = useCallback((next) => {
    setModel(next);
    setDraft(EMPTY_WORD_DRAFT);
  }, []);

  const resetDraft = useCallback(() => setDraft(EMPTY_WORD_DRAFT), []);

  useEffect(() => {
    setEditing(false);
  }, [url]);

  const startPanelResize = useCallback((event) => {
    if (typeof window === 'undefined' || window.innerWidth < 900) return;
    const startX = event.clientX;
    const startWidth = panelWidth;
    const move = (moveEvent) => {
      setPanelWidth(officePanelWidthFromPointer(startWidth, startX, moveEvent.clientX));
    };
    const finish = () => {
      window.removeEventListener('pointermove', move);
      window.removeEventListener('pointerup', finish);
      window.removeEventListener('pointercancel', finish);
      setPanelWidth((value) => saveOfficePanelWidth(value));
    };
    window.addEventListener('pointermove', move);
    window.addEventListener('pointerup', finish);
    window.addEventListener('pointercancel', finish);
  }, [panelWidth]);

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
    setDraft(EMPTY_WORD_DRAFT);
    setActiveCell(null);

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

  // Selection only changes which element the panel edits. Drafts are keyed by block/cell and
  // remain visible when the user switches away and back.
  useEffect(() => {
    setNotice('');
  }, [selectedId]);

  const selectedText = selectedId ? draft.texts[selectedId] : undefined;
  const selectedStyle = selectedId ? draft.styles[selectedId] : undefined;
  const selectedProps = selectedId ? draft.props[selectedId] : undefined;
  const selectedTableCell = useMemo(() => {
    if (selectedBlock?.kind !== BLOCK_KINDS.TABLE) return null;
    if (activeCell?.blockId === selectedBlock.id
      && (selectedBlock.cells || []).some(cell => cell.row === activeCell.row && cell.col === activeCell.col)) {
      return activeCell;
    }
    return selectedBlock.cells?.[0] || null;
  }, [activeCell, selectedBlock]);
  const selectedCellKey = selectedTableCell
    ? wordCellDraftKey(selectedTableCell.blockId || selectedBlock.id,
      selectedTableCell.row, selectedTableCell.col)
    : '';
  const selectedCellText = selectedCellKey
    && Object.prototype.hasOwnProperty.call(draft.cells, selectedCellKey)
    ? draft.cells[selectedCellKey]
    : undefined;

  const effectiveStyle = useMemo(() => {
    const first = selectedBlock?.runs?.[0] || {};
    return {
      bold: selectedStyle?.bold !== undefined ? selectedStyle.bold : Boolean(first.bold),
      italic: selectedStyle?.italic !== undefined ? selectedStyle.italic : Boolean(first.italic),
      underline: selectedStyle?.underline !== undefined ? selectedStyle.underline : Boolean(first.underline),
      size: selectedStyle?.size !== undefined ? selectedStyle.size : (first.size || 0),
      color: selectedStyle?.color !== undefined ? selectedStyle.color : (first.color || ''),
      align: selectedProps?.align !== undefined ? selectedProps.align : (selectedBlock?.align || 'left'),
    };
  }, [selectedBlock, selectedProps?.align, selectedStyle]);

  const effectiveProps = useMemo(() => ({
    indLeftCm: selectedProps?.indLeftCm !== undefined
      ? selectedProps.indLeftCm
      : twipsToCm(selectedBlock?.indLeftTwips || 0),
    indFirstCm: selectedProps?.indFirstCm !== undefined
      ? selectedProps.indFirstCm
      : twipsToCm(selectedBlock?.indFirstTwips || 0),
  }), [selectedBlock, selectedProps?.indFirstCm, selectedProps?.indLeftCm]);

  const renderedModel = useMemo(() => applyWordDraft(model, draft), [draft, model]);

  const applyToBytes = useCallback(async () => {
    if (!model) return null;
    const xml = await model.pkg.text(model.part);
    const prepared = applyDraftsToXml(xml, model, draft);
    if (!prepared.changed) return null;

    const problems = validateDocumentPart(prepared.xml);
    if (problems.length > 0) throw new Error(`写入前的检查未通过：${problems.join('；')}`);
    return { xml: prepared.xml };
  }, [draft, model]);

  /**
   * Persist the visible drafts onto a model and return the reparsed result.
   *
   * Structural operations (insert/delete/image) used to run against the last saved XML and then
   * adopt that reparsed model, which silently reverted any text/style/cell change the user had
   * not saved yet. Applying the drafts to the intermediate model keeps both kinds of edit.
   */
  const persistDrafts = useCallback(async (sourceModel) => {
    const xml = await sourceModel.pkg.text(sourceModel.part);
    const prepared = applyDraftsToXml(xml, sourceModel, draft);
    if (!prepared.changed) return { model: sourceModel, bytes: null };
    const problems = validateDocumentPart(prepared.xml);
    if (problems.length > 0) throw new Error(`写入前的检查未通过：${problems.join('；')}`);
    sourceModel.pkg.setText(sourceModel.part, prepared.xml);
    const bytes = await sourceModel.pkg.save();
    return { model: await parseDocx(bytes), bytes };
  }, [draft]);

  /** Write locally and re-parse, so the canvas shows exactly what the bytes now say. */
  const commit = useCallback(async (mutate, successNote) => {
    if (!model || saving) return;
    setSaving(true);
    setNotice('');
    try {
      // Structural operations cannot share a batch with text drafts. Flush every draft to a
      // fresh model first, then mutate that exact model and re-parse once.
      const flushed = await persistDrafts(model);
      const target = flushed.model;
      const xml = await target.pkg.text(target.part);
      const next = await mutate(xml);
      if (!next || next === xml) {
        if (flushed.bytes) {
          pendingBytesRef.current = flushed.bytes;
          setHasPending(true);
          adopt(target);
          if (successNote) setNotice(successNote);
          return flushed.bytes;
        }
        setNotice('没有产生改动');
        return;
      }
      const problems = validateDocumentPart(next);
      if (problems.length > 0) throw new Error(`写入前的检查未通过：${problems.join('；')}`);

      target.pkg.setText(target.part, next);
      const bytes = await target.pkg.save();
      const reparsed = await parseDocx(bytes);
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
  }, [adopt, model, persistDrafts, saving]);

  const handleSave = useCallback(async (requirement = '') => {
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
      let reparsed = null;
      if (prepared) {
        const problems = validateDocumentPart(prepared.xml);
        if (problems.length > 0) throw new Error(`写入前的检查未通过：${problems.join('；')}`);

        model.pkg.setText(model.part, prepared.xml);
        bytes = await model.pkg.save();
        reparsed = await parseDocx(bytes);
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
        requirement,
      });
      if (onSave && !published) {
        setNotice('已写入本地，但最新版未保存成功，请重试保存');
        return;
      }
      if (published?.unsupported) {
        // The upload landed; the source→latest pointer is missing only because the deployed
        // backend has no route for it. Prompting a retry here would never succeed.
        if (reparsed) adopt(reparsed);
        pendingBytesRef.current = null;
        setHasPending(false);
        setNotice(OFFICE_SERVER_UNSUPPORTED_NOTICE);
        return;
      }
      if (reparsed) adopt(reparsed);
      pendingBytesRef.current = null;
      setHasPending(false);
      setNotice((published && published.notice) || OFFICE_SAVE_NOTICE);
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
  const handleCellSelect = useCallback((cell, blockId) => {
    const block = model?.blocks?.find(item => item.id === blockId)
      || model?.blocks?.find(item => item.id === selectedId && item.kind === BLOCK_KINDS.TABLE
        && (item.cells || []).some(entry => entry.row === cell.row && entry.col === cell.col));
    if (!block) return;
    setSelectedId(block.id);
    setActiveCell({ blockId: block.id, row: cell.row, col: cell.col });
    setNotice(`已选中第 ${cell.row + 1} 行第 ${cell.col + 1} 列`);
  }, [model, selectedId]);

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
      const flushed = await persistDrafts(model);
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
  }, [adopt, model, persistDrafts, saving]);

  if (status === 'loading') {
    return <div className="office-preview-state">正在解析文档…</div>;
  }
  if (status === 'error') {
    return <div className="office-preview-state is-error">{error}</div>;
  }
  if (!model) return null;

  return (
    <div className="office-preview" data-office-mode={editing ? 'edit' : 'readonly'}
      style={{ '--office-panel-width': `${panelWidth}px` }}>
      <div className="office-preview-toolbar">
        <span className="office-page-label">
          {model.blocks.length} 块 · {model.outline.length} 个标题
        </span>
        <span className="office-toolbar-divider" />
        <button type="button" className="office-page-btn" data-office-edit-toggle=""
          disabled={saving} onClick={() => setEditing(value => !value)}>
          {editing ? '完成' : '编辑'}
        </button>
        <button type="button" className="office-page-btn" disabled={!editing || saving}
          onClick={handleInsertParagraph}>插入段落</button>
        <button type="button" className="office-page-btn" disabled={!editing || saving || !selectedBlock}
          onClick={handleDeleteBlock}>删除段落</button>
        <button type="button" className="office-page-btn" disabled={!editing || saving}
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
          onClick={() => { setAskText(''); setAskOpen(true); }}>
          {saving ? '写入中…' : '写入文档'}
        </button>
      </div>

      {askOpen ? (
        <div className="office-ask-backdrop" data-office-ask="">
          <div className="office-ask" role="dialog" aria-modal="true" aria-label="写入文档">
            <div className="office-ask-title">还有要一起发给小八的新要求吗？</div>
            <p className="office-ask-hint">
              写了就和小八说一声，它会拿着这一版继续改。留空则只保存，会话里不发消息。
            </p>
            <textarea className="office-ask-input" data-office-ask-input="" rows={4}
              autoFocus value={askText} placeholder="例如：把第二段的语气改正式一点"
              onChange={event => setAskText(event.target.value)} />
            <div className="office-ask-actions">
              <button type="button" className="office-page-btn"
                onClick={() => setAskOpen(false)}>取消</button>
              <button type="button" className="office-save-btn" data-office-ask-confirm=""
                onClick={() => {
                  const requirement = askText.trim();
                  setAskOpen(false);
                  handleSave(requirement);
                }}>
                {askText.trim() ? '写入并发送' : '仅写入'}
              </button>
            </div>
          </div>
        </div>
      ) : null}

      {notice ? <div className="office-preview-notice">{notice}</div> : null}

      <div className="office-preview-body" data-office-side={editing ? 'on' : 'off'}>
        <div className="office-preview-stage office-docx-stage">
          <DocxCanvas
            model={renderedModel}
            selectedId={selectedId}
            hoveredId={null}
            onSelect={setSelectedId}
            onHover={() => {}}
            activeCell={activeCell}
            onCellSelect={editing ? handleCellSelect : undefined}
          />
        </div>
        {editing ? (
          <>
            <div className="office-panel-resizer" data-office-panel-resizer=""
              role="separator" aria-orientation="vertical" tabIndex={0}
              onPointerDown={startPanelResize}
              onKeyDown={(event) => {
                if (event.key !== 'ArrowLeft' && event.key !== 'ArrowRight') return;
                event.preventDefault();
                const delta = event.key === 'ArrowLeft' ? 16 : -16;
                setPanelWidth((value) => saveOfficePanelWidth(value + delta));
              }} />
            <div className="office-preview-side">
              <BlockPanel
            block={selectedBlock}
            text={selectedText}
            style={effectiveStyle}
            props={effectiveProps}
            onTextChange={(value) => {
              if (!selectedId) return;
              setDraft(current => ({ ...current,
                texts: { ...current.texts, [selectedId]: value } }));
            }}
            onStyleChange={(patch) => {
              if (!selectedId) return;
              setDraft(current => ({ ...current, styles: {
                ...current.styles,
                [selectedId]: { ...(current.styles[selectedId] || {}), ...patch },
              } }));
            }}
            onAlign={(align) => {
              if (!selectedId) return;
              setDraft(current => ({ ...current, props: {
                ...current.props,
                [selectedId]: { ...(current.props[selectedId] || {}), align },
              } }));
            }}
            onIndent={({ leftCm, firstCm }) => {
              if (!selectedId) return;
              setDraft(current => {
                const next = { ...(current.props[selectedId] || {}) };
                if (leftCm !== undefined) next.indLeftCm = leftCm;
                if (firstCm !== undefined) next.indFirstCm = firstCm;
                return { ...current, props: { ...current.props, [selectedId]: next } };
              });
            }}
            onDelete={handleDeleteBlock}
            activeCell={selectedTableCell}
            onCellSelect={handleCellSelect}
            cellText={selectedCellText}
            onCellTextChange={(value) => {
              if (!selectedCellKey) return;
              setDraft(current => ({ ...current,
                cells: { ...current.cells, [selectedCellKey]: value } }));
            }}
            disabled={saving || !editing}
              />
            </div>
          </>
        ) : null}
      </div>
    </div>
  );
}

export { ALIGN_LABELS, cmToTwips, ALIGNMENTS };
