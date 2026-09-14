import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { SpreadsheetPreview } from './spreadsheet-preview';

function csvBuffer(text) {
  const bytes = Buffer.from(text, 'utf8');
  return bytes.buffer.slice(bytes.byteOffset, bytes.byteOffset + bytes.byteLength);
}

async function flushAsync(times = 6) {
  for (let index = 0; index < times; index += 1) await Promise.resolve();
}

describe('SpreadsheetPreview CSV presentation', () => {
  let container;
  let root;

  beforeEach(() => {
    global.IS_REACT_ACT_ENVIRONMENT = true;
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
  });

  it('promotes the first CSV row to field headers and keeps source row numbers', async () => {
    await act(async () => {
      root.render(<SpreadsheetPreview buffer={csvBuffer('name,count,city\nAlice,12,Paris\nBob,7,Tokyo')} kind="csv" />);
      await flushAsync();
    });

    const headers = [...container.querySelectorAll('.v3-spreadsheet-column-name')].map((node) => node.textContent);
    expect(headers).toEqual(['name', 'count', 'city']);
    expect([...container.querySelectorAll('.v3-spreadsheet-row-number')].map((node) => node.textContent)).toEqual(['2', '3']);
    expect(container.querySelector('tbody').textContent).not.toContain('name');
    expect(container.querySelector('td.is-numeric').textContent).toBe('12');
    expect(container.querySelectorAll('td.is-numeric')).toHaveLength(2);
    expect(container.querySelector('.v3-spreadsheet-header-note').textContent).toBe('首行为字段名');
  });

  it('treats only valid decimal values as numeric cells', async () => {
    await act(async () => {
      root.render(<SpreadsheetPreview buffer={csvBuffer('value\n"1,2"\n"1,234"\n0x10')} kind="csv" />);
      await flushAsync();
    });

    expect([...container.querySelectorAll('td')].map((node) => [node.textContent, node.className])).toEqual([
      ['1,2', ''],
      ['1,234', 'is-numeric'],
      ['0x10', ''],
    ]);
  });

  it('falls back to column letters for blank CSV field names', async () => {
    await act(async () => {
      root.render(<SpreadsheetPreview buffer={csvBuffer('name,,city\nAlice,12,Paris')} kind="csv" />);
      await flushAsync();
    });

    expect([...container.querySelectorAll('.v3-spreadsheet-column-name')].map((node) => node.textContent)).toEqual(['name', 'B', 'city']);
  });

  it('keeps data beyond the preview column limit in totals and treats an empty CSV as empty', async () => {
    const wideHeader = Array.from({ length: 51 }, (_, index) => `field${index + 1}`).join(',');
    const wideRow = Array.from({ length: 51 }, (_, index) => String(index + 1)).join(',');
    await act(async () => {
      root.render(<SpreadsheetPreview buffer={csvBuffer(`${wideHeader}\n${wideRow}`)} kind="csv" />);
      await flushAsync();
    });

    expect(container.querySelector('.v3-spreadsheet-summary').textContent).toContain('2 行 · 51 列');
    expect(container.querySelector('.v3-spreadsheet-summary').textContent).toContain('仅预览前 200 行、50 列');

    await act(async () => {
      root.render(<SpreadsheetPreview buffer={csvBuffer('')} kind="csv" />);
      await flushAsync();
    });
    expect(container.textContent).toContain('没有可预览的表格内容。');
    expect(container.querySelector('.v3-spreadsheet-header-note')).toBeNull();
  });
});
