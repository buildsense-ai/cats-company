import { marked } from 'marked';

marked.setOptions({ breaks: false, gfm: true });

export function escapeHtml(text) {
  return String(text)
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;');
}

export function renderSafeMarkdown(text) {
  return sanitizeHtml(marked.parse(escapeHtml(text)));
}

export function markdownPreviewDocument(text) {
  return `<!doctype html>
<html>
<head>
  <meta charset="utf-8" />
  <base target="_blank" />
  <style>
    body {
      margin: 0;
      padding: 28px 34px;
      color: #e5e7eb;
      background: #111827;
      font: 14px/1.65 -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
    }
    a { color: #93c5fd; }
    pre, code {
      font-family: "SFMono-Regular", Consolas, "Liberation Mono", Menlo, monospace;
      background: rgba(255,255,255,0.08);
      border-radius: 6px;
    }
    pre { padding: 14px; overflow: auto; }
    code { padding: 2px 5px; }
    table { border-collapse: collapse; width: 100%; }
    th, td { border: 1px solid rgba(255,255,255,0.14); padding: 8px; }
  </style>
</head>
<body>${renderSafeMarkdown(text)}</body>
</html>`;
}

function sanitizeHtml(html) {
  if (typeof document === 'undefined') {
    return fallbackSanitizeHtml(html);
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
      node.setAttribute('target', '_blank');
      node.setAttribute('rel', 'noopener noreferrer');
    }
  });

  return template.innerHTML;
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
