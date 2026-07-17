import { marked } from 'marked';

marked.setOptions({ breaks: false, gfm: true });

const MARKDOWN_INLINE_OR_BLOCK_PATTERN = /(\*\*|__|`|#{1,6}\s|^\s*[-*+]\s|^\s*\d+\.\s|\[.*\]\(.*\))/m;
const TEXT_TABLE_SEPARATOR_PATTERN = /(?:\t+| {2,}|\u3000+)/g;
const TEXT_TABLE_ALIGNMENT_PATTERN = /(?:\t+| {2,}|\u3000+)/;

export function hasMarkdownTable(text) {
  const lines = String(text || '').split(/\r?\n/);
  for (let i = 0; i < lines.length - 1; i += 1) {
    const header = lines[i].trim();
    const separator = lines[i + 1].trim();
    if (!header.includes('|') || !separator.includes('|')) continue;
    if (/^\|?\s*:?-{2,}:?\s*(\|\s*:?-{2,}:?\s*)+\|?$/.test(separator)) {
      return true;
    }
  }
  return false;
}

export function hasPlainTextTable(text) {
  return false;
}

export function hasRenderableTable(text) {
  return hasMarkdownTable(text);
}

export function hasPlainTextTableLikeBlock(text) {
  const lines = String(text || '').split(/\r?\n/);
  let inFence = false;
  let block = [];

  const flush = () => {
    const matched = isPlainTextTableLikeBlock(block);
    block = [];
    return matched;
  };

  for (let i = 0; i < lines.length; i += 1) {
    const line = lines[i];
    if (/^\s*(```|~~~)/.test(line)) {
      if (!inFence && flush()) return true;
      inFence = !inFence;
      continue;
    }
    if (inFence || isIndentedCodeLine(line)) {
      if (!inFence && flush()) return true;
      continue;
    }
    if (!line.trim()) {
      if (flush()) return true;
      continue;
    }
    block.push(line);
  }

  return flush();
}

export function shouldRenderMarkdown(text, options = {}) {
  const value = String(text || '');
  return MARKDOWN_INLINE_OR_BLOCK_PATTERN.test(value) ||
    hasMarkdownTable(value);
}

export function escapeHtml(text) {
  return String(text)
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;');
}

export function renderSafeMarkdown(text, options = {}) {
  return sanitizeHtml(marked.parse(escapeHtml(text)));
}

export function normalizePlainTextTables(text) {
  return String(text || '');
}

export function markdownPreviewDocument(text) {
  return `<!doctype html>
<html>
<head>
  <meta charset="utf-8" />
  <base href="about:srcdoc" target="_blank" />
  <style>
    html { scroll-behavior: smooth; }
    body {
      margin: 0;
      padding: 28px 34px;
      color: #e5e7eb;
      background: #111827;
      font: 14px/1.65 -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
    }
    a { color: #93c5fd; }
    h1, h2, h3, h4, h5, h6 { scroll-margin-top: 20px; }
    pre, code {
      font-family: "SFMono-Regular", Consolas, "Liberation Mono", Menlo, monospace;
      background: rgba(255,255,255,0.08);
      border-radius: 6px;
    }
    pre { padding: 14px; overflow: auto; }
    code { padding: 2px 5px; }
    .oc-markdown-table { max-width: 100%; margin: 14px 0; overflow-x: auto; }
    .oc-markdown-table table { min-width: 720px; margin: 0; }
    table { border-collapse: collapse; width: 100%; }
    th, td { border: 1px solid rgba(255,255,255,0.14); padding: 8px; }
  </style>
</head>
<body>${renderSafeMarkdown(text)}</body>
</html>`;
}

function sanitizeHtml(html) {
  if (typeof document === 'undefined') {
    return wrapTablesInHtml(fallbackSanitizeHtml(html));
  }

  const template = document.createElement('template');
  template.innerHTML = html;
  const nodes = template.content.querySelectorAll('*');

  nodes.forEach((node) => {
    Array.from(node.attributes).forEach((attr) => {
      const name = attr.name.toLowerCase();
      const value = attr.value || '';
      if (name.startsWith('on') || name === 'style') {
        node.removeAttribute(attr.name);
        return;
      }
      if ((name === 'href' || name === 'src' || name === 'xlink:href') && !isSafeUrl(value, name)) {
        node.removeAttribute(attr.name);
      }
    });

    if (node.tagName === 'A') {
      const href = node.getAttribute('href') || '';
      if (isHashLink(href)) {
        node.setAttribute('target', '_self');
        node.removeAttribute('rel');
      } else {
        node.setAttribute('target', '_blank');
        node.setAttribute('rel', 'noopener noreferrer');
      }
    }
  });

  addHeadingAnchors(template.content);
  wrapMarkdownTables(template.content);

  return template.innerHTML;
}

function wrapTablesInHtml(html) {
  return String(html).replace(/<table(\s|>)[\s\S]*?<\/table>/gi, (tableHtml) => (
    `<div class="oc-markdown-table">${tableHtml}</div>`
  ));
}

function wrapMarkdownTables(root) {
  root.querySelectorAll('table').forEach((table) => {
    if (table.parentElement?.classList.contains('oc-markdown-table')) return;
    const wrapper = document.createElement('div');
    wrapper.className = 'oc-markdown-table';
    table.parentNode.insertBefore(wrapper, table);
    wrapper.appendChild(table);
  });
}

function fallbackSanitizeHtml(html) {
  return String(html)
    .replace(/\s+on[a-z]+\s*=\s*(['"]).*?\1/gi, '')
    .replace(/\s+(href|src|xlink:href)\s*=\s*(['"])\s*javascript:.*?\2/gi, '');
}

function isSafeUrl(value, attrName) {
  const trimmed = String(value || '').trim();
  if (!trimmed) return true;
  if (trimmed.startsWith('#') || trimmed.startsWith('/') || trimmed.startsWith('./') || trimmed.startsWith('../')) {
    return true;
  }

  try {
    const parsed = new URL(trimmed, window.location.origin);
    const protocol = parsed.protocol.toLowerCase();
    if (attrName === 'src') {
      return protocol === 'http:' || protocol === 'https:' || protocol === 'data:';
    }
    return ['http:', 'https:', 'mailto:', 'tel:'].includes(protocol);
  } catch (err) {
    return false;
  }
}

function isHashLink(value) {
  return /^#[^#]/.test(String(value || '').trim());
}

function addHeadingAnchors(root) {
  const usedIds = new Set(
    Array.from(root.querySelectorAll('[id]'))
      .map((node) => String(node.id || '').trim())
      .filter(Boolean),
  );

  root.querySelectorAll('h1, h2, h3, h4, h5, h6').forEach((heading) => {
    if (heading.id) {
      usedIds.add(heading.id);
      return;
    }
    heading.id = uniqueHeadingId(slugifyHeading(heading.textContent), usedIds);
  });
}

function uniqueHeadingId(baseId, usedIds) {
  const base = baseId || 'section';
  let nextId = base;
  let suffix = 2;
  while (usedIds.has(nextId)) {
    nextId = `${base}-${suffix}`;
    suffix += 1;
  }
  usedIds.add(nextId);
  return nextId;
}

function slugifyHeading(text) {
  const normalized = String(text || '')
    .trim()
    .toLowerCase()
    .normalize('NFKD')
    .replace(/[\u0300-\u036f]/g, '')
    .replace(/[^\p{L}\p{N}\s_-]/gu, '')
    .replace(/[\s_]+/g, '-')
    .replace(/-+/g, '-')
    .replace(/^-|-$/g, '');
  return normalized || 'section';
}

function splitAlignedColumns(line) {
  const value = String(line || '').replace(/\s+$/, '');
  if (!value.trim() || value.includes('|')) return null;

  const columns = [];
  let segmentStart = 0;
  let match;
  TEXT_TABLE_SEPARATOR_PATTERN.lastIndex = 0;

  while ((match = TEXT_TABLE_SEPARATOR_PATTERN.exec(value))) {
    addAlignedColumn(columns, value, segmentStart, match.index);
    segmentStart = match.index + match[0].length;
  }
  addAlignedColumn(columns, value, segmentStart, value.length);

  if (columns.length < 2) return null;
  return {
    cells: columns.map((column) => column.text),
  };
}

function addAlignedColumn(columns, line, start, end) {
  const rawSegment = line.slice(start, end);
  const text = rawSegment.trim();
  if (!text) return;
  const leading = rawSegment.match(/^\s*/)?.[0].length || 0;
  columns.push({ start: start + leading, text });
}

function isIndentedCodeLine(line) {
  return /^(?: {4,}|\t)/.test(String(line || ''));
}

function isLikelyNonTableLine(line) {
  const value = String(line || '').trim();
  return /^[-*+]\s{2,}/.test(value) ||
    /^\d+[.)]\s{2,}/.test(value) ||
    /^[^:\s]{1,32}:\s{2,}/.test(value);
}

function isLikelyLogRow(cells) {
  const firstCell = String(cells?.[0] || '').trim().toUpperCase();
  return /^(TRACE|DEBUG|INFO|WARN|WARNING|ERROR|FATAL)$/.test(firstCell);
}

function isPlainTextTableLikeBlock(lines) {
  const rows = (lines || [])
    .map((line) => String(line || '').replace(/\s+$/, ''))
    .filter((line) => line.trim());
  if (rows.length < 3) return false;
  if (rows.some((line) => isLikelyNonTableLine(line))) return false;
  if (rows.filter((line) => isLikelyLogRow(splitAlignedColumns(line)?.cells || [line])).length >= 2) return false;

  const alignedRows = rows.filter((line) => (splitAlignedColumns(line)?.cells.length || 0) >= 3).length;
  if (alignedRows >= 2) return true;

  const firstLine = rows[0].trim();
  const hasTableHeader = firstLine.startsWith('#') ||
    /(序号|编号|名称|数量|状态|备注|说明|类型|负责人|部门|文件夹|文件|路径|目录|项目|页面|场景|典型|指标|结果|file|folder|path|project|page)/i.test(firstLine);
  if (!hasTableHeader) return false;

  const numberedRows = rows.slice(1).filter((line) => {
    const value = line.trim();
    if (!/^\d+/.test(value)) return false;
    if (/^\d+[.)]\s/.test(value)) return false;
    return /[-_./\\+]|[\p{Script=Han}]/u.test(value);
  }).length;

  return numberedRows >= 2;
}
