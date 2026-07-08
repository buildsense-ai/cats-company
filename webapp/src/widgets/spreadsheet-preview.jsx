import React, { useEffect, useMemo, useState } from 'react';

export const SPREADSHEET_PREVIEW_MAX_ROWS = 200;
export const SPREADSHEET_PREVIEW_MAX_COLUMNS = 50;
export const SPREADSHEET_PREVIEW_MAX_BYTES = 12 * 1024 * 1024;

function columnLabel(index) {
  let label = '';
  let value = index + 1;
  while (value > 0) {
    value -= 1;
    label = String.fromCharCode(65 + (value % 26)) + label;
    value = Math.floor(value / 26);
  }
  return label;
}

function formatCellValue(value) {
  if (value === null || value === undefined) return '';
  if (value instanceof Date && !Number.isNaN(value.getTime())) {
    return value.toISOString().replace('T', ' ').slice(0, 16);
  }
  if (typeof value === 'boolean') return value ? 'TRUE' : 'FALSE';
  return String(value);
}

function normalizeRows(rows) {
  if (!Array.isArray(rows)) return [];
  return rows.map((row) => (Array.isArray(row) ? row : [row]));
}

function normalizeSheet(sheet, index) {
  const rawRows = normalizeRows(sheet?.data || sheet?.rows || []);
  const visibleColumns = rawRows.reduce((max, row) => Math.max(max, row.length), 0);
  const totalRows = Number.isFinite(sheet?.totalRows) ? sheet.totalRows : rawRows.length;
  const totalColumns = Number.isFinite(sheet?.totalColumns) ? sheet.totalColumns : visibleColumns;
  const columnCount = Math.min(Math.max(totalColumns, 1), SPREADSHEET_PREVIEW_MAX_COLUMNS);
  const rows = rawRows
    .slice(0, SPREADSHEET_PREVIEW_MAX_ROWS)
    .map((row) => row.slice(0, SPREADSHEET_PREVIEW_MAX_COLUMNS).map(formatCellValue));

  return {
    name: String(sheet?.sheet || sheet?.name || `Sheet ${index + 1}`),
    rows,
    columnCount,
    totalRows,
    totalColumns,
    truncatedRows: Boolean(sheet?.truncatedRows) || totalRows > SPREADSHEET_PREVIEW_MAX_ROWS,
    truncatedColumns: Boolean(sheet?.truncatedColumns) || totalColumns > SPREADSHEET_PREVIEW_MAX_COLUMNS,
  };
}

function normalizeWorkbook(rawSheets) {
  const sheets = Array.isArray(rawSheets) ? rawSheets : [];
  return sheets.map(normalizeSheet).filter((sheet) => sheet.totalRows > 0 || sheet.totalColumns > 0);
}

function parseCsv(text) {
  const rows = [];
  let row = [];
  let value = '';
  let inQuotes = false;
  let currentCellCount = 0;
  let totalRows = 0;
  let totalColumns = 0;
  let truncatedRows = false;
  let truncatedColumns = false;

  const pushCell = () => {
    if (currentCellCount < SPREADSHEET_PREVIEW_MAX_COLUMNS) {
      row.push(value);
    } else {
      truncatedColumns = true;
    }
    currentCellCount += 1;
    value = '';
  };

  const pushRow = () => {
    const rowHasContent = row.some((cell) => String(cell).trim() !== '');
    totalColumns = Math.max(totalColumns, currentCellCount);
    if (rowHasContent || rows.length === 0) {
      totalRows += 1;
      if (rows.length < SPREADSHEET_PREVIEW_MAX_ROWS) {
        rows.push(row);
      } else {
        truncatedRows = true;
      }
    }
    row = [];
    currentCellCount = 0;
  };

  for (let index = 0; index < text.length; index += 1) {
    const char = text[index];
    const next = text[index + 1];
    if (inQuotes) {
      if (char === '"' && next === '"') {
        value += '"';
        index += 1;
      } else if (char === '"') {
        inQuotes = false;
      } else {
        value += char;
      }
      continue;
    }

    if (char === '"') {
      inQuotes = true;
    } else if (char === ',') {
      pushCell();
    } else if (char === '\n') {
      pushCell();
      pushRow();
    } else if (char !== '\r') {
      value += char;
    }
  }

  pushCell();
  pushRow();
  return {
    rows,
    totalRows,
    totalColumns,
    truncatedRows,
    truncatedColumns,
  };
}

function decodeUtf8(buffer) {
  if (typeof TextDecoder !== 'undefined') {
    return new TextDecoder('utf-8').decode(buffer);
  }
  const bytes = Array.from(new Uint8Array(buffer || new ArrayBuffer(0)));
  try {
    return decodeURIComponent(bytes.map((byte) => `%${byte.toString(16).padStart(2, '0')}`).join(''));
  } catch (err) {
    let text = '';
    for (let index = 0; index < bytes.length; index += 8192) {
      text += String.fromCharCode(...bytes.slice(index, index + 8192));
    }
    return text;
  }
}

function parseCsvBuffer(buffer) {
  const text = decodeUtf8(buffer);
  const result = parseCsv(text);
  return [{
    sheet: 'CSV',
    data: result.rows,
    totalRows: result.totalRows,
    totalColumns: result.totalColumns,
    truncatedRows: result.truncatedRows,
    truncatedColumns: result.truncatedColumns,
  }];
}

async function parseSpreadsheetBuffer(buffer, kind) {
  if (kind === 'csv') return parseCsvBuffer(buffer);
  const module = await import('read-excel-file/browser');
  const readExcelFile = module.default || module.readSheet;
  if (!readExcelFile) throw new Error('缺少 Excel 解析器');
  return readExcelFile(buffer);
}

export function SpreadsheetPreview({ buffer, kind }) {
  const [sheets, setSheets] = useState([]);
  const [activeIndex, setActiveIndex] = useState(0);
  const [loading, setLoading] = useState(Boolean(buffer));
  const [error, setError] = useState('');

  useEffect(() => {
    let cancelled = false;
    setSheets([]);
    setActiveIndex(0);
    setError('');
    if (!buffer) {
      setLoading(false);
      return () => {
        cancelled = true;
      };
    }

    const load = async () => {
      setLoading(true);
      try {
        const rawSheets = await parseSpreadsheetBuffer(buffer, kind);
        if (cancelled) return;
        const normalized = normalizeWorkbook(rawSheets);
        if (normalized.length === 0) {
          setError('没有可预览的表格内容。');
        } else {
          setSheets(normalized);
        }
      } catch (err) {
        if (!cancelled) setError(`表格预览失败：${err.message}`);
      } finally {
        if (!cancelled) setLoading(false);
      }
    };
    load();

    return () => {
      cancelled = true;
    };
  }, [buffer, kind]);

  const activeSheet = sheets[Math.min(activeIndex, Math.max(sheets.length - 1, 0))] || null;
  const columns = useMemo(
    () => Array.from({ length: activeSheet?.columnCount || 0 }, (_, index) => columnLabel(index)),
    [activeSheet?.columnCount],
  );

  if (loading) {
    return <div className="v3-file-preview-state">正在解析表格...</div>;
  }
  if (error) {
    return <div className="v3-file-preview-state error">{error}</div>;
  }
  if (!activeSheet) {
    return <div className="v3-file-preview-state">暂无可预览内容。</div>;
  }

  return (
    <div className="v3-spreadsheet-preview">
      {sheets.length > 1 && (
        <div className="v3-spreadsheet-tabs" role="tablist" aria-label="工作表">
          {sheets.map((sheet, index) => (
            <button
              key={`${sheet.name}-${index}`}
              type="button"
              role="tab"
              aria-selected={index === activeIndex}
              className={index === activeIndex ? 'active' : ''}
              onClick={() => setActiveIndex(index)}
              title={sheet.name}
            >
              {sheet.name}
            </button>
          ))}
        </div>
      )}
      <div className="v3-spreadsheet-summary">
        <strong>{activeSheet.name}</strong>
        <span>{activeSheet.totalRows || 0} 行 · {activeSheet.totalColumns || 0} 列</span>
        {(activeSheet.truncatedRows || activeSheet.truncatedColumns) && (
          <em>仅预览前 {SPREADSHEET_PREVIEW_MAX_ROWS} 行、{SPREADSHEET_PREVIEW_MAX_COLUMNS} 列</em>
        )}
      </div>
      <div className="v3-spreadsheet-grid-wrap">
        <table className="v3-spreadsheet-grid">
          <thead>
            <tr>
              <th className="v3-spreadsheet-corner" aria-label="行号" />
              {columns.map((column) => (
                <th key={column}>{column}</th>
              ))}
            </tr>
          </thead>
          <tbody>
            {activeSheet.rows.map((row, rowIndex) => (
              <tr key={rowIndex}>
                <th className="v3-spreadsheet-row-number">{rowIndex + 1}</th>
                {columns.map((column, columnIndex) => {
                  const cell = row[columnIndex] || '';
                  return (
                    <td key={`${rowIndex}-${column}`} title={cell}>
                      {cell}
                    </td>
                  );
                })}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}
