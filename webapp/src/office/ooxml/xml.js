// Lightweight XML locator for OOXML parts.
//
// This is deliberately NOT a full XML DOM. It only does one job: find a node and
// remember the exact character range it occupies in the original string, so a
// later edit can splice that range and leave every other byte untouched.
//
// Re-serializing a whole part is the thing that breaks Office files. Files written
// by PowerPoint carry attribute order, namespace declarations, ignorable extensions
// and rounding that no generic serializer reproduces. Splicing does not care.

const TAG_RE = /<(\/?)([A-Za-z_][\w.:-]*)((?:\s+[\w.:-]+\s*=\s*(?:"[^"]*"|'[^']*'))*)\s*(\/?)>/g;
const ATTR_RE = /([\w.:-]+)\s*=\s*(?:"([^"]*)"|'([^']*)')/g;

function parseAttrs(source) {
  const attrs = Object.create(null);
  if (!source) return attrs;
  ATTR_RE.lastIndex = 0;
  let m;
  while ((m = ATTR_RE.exec(source)) !== null) {
    attrs[m[1]] = m[2] !== undefined ? m[2] : m[3];
  }
  return attrs;
}

function decodeEntities(value) {
  if (!value || value.indexOf('&') === -1) return value;
  return value
    .replace(/&lt;/g, '<')
    .replace(/&gt;/g, '>')
    .replace(/&quot;/g, '"')
    .replace(/&apos;/g, "'")
    .replace(/&#(\d+);/g, (_, code) => String.fromCodePoint(Number(code)))
    .replace(/&#x([0-9a-fA-F]+);/g, (_, code) => String.fromCodePoint(parseInt(code, 16)))
    .replace(/&amp;/g, '&');
}

export function escapeText(value) {
  return String(value)
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;');
}

export function escapeAttr(value) {
  return escapeText(value).replace(/"/g, '&quot;');
}

/**
 * Parse a string into a locator tree.
 *
 * Every node keeps `start`/`end` as character offsets into the original string:
 * `start` is the `<` of the open tag, `end` is one past the final `>`. Children
 * ranges are absolute, not relative, so a replacement never has to translate.
 */
export function scanXml(xml) {
  const source = String(xml ?? '');
  const root = { name: '#document', attrs: Object.create(null), start: 0, end: source.length,
    innerStart: 0, innerEnd: source.length, children: [], parent: null, selfClosing: false,
    textCache: '' };
  const stack = [root];

  TAG_RE.lastIndex = 0;
  let match;
  while ((match = TAG_RE.exec(source)) !== null) {
    const [full, closing, name, attrSource, selfClose] = match;
    const start = match.index;
    const end = start + full.length;
    const parent = stack[stack.length - 1];

    if (closing === '/') {
      // Pop until the matching open tag. Mismatched nesting in a hostile file
      // should not throw; the deeper nodes just stay unclosed and are dropped.
      for (let i = stack.length - 1; i > 0; i -= 1) {
        if (stack[i].name === name) {
          const node = stack[i];
          node.innerEnd = start;
          node.end = end;
          node.textCache = decodeEntities(source.slice(node.innerStart, node.innerEnd));
          stack.length = i;
          break;
        }
      }
      continue;
    }

    const node = {
      name,
      attrs: parseAttrs(attrSource),
      start,
      end: selfClose ? end : source.length,
      innerStart: end,
      innerEnd: selfClose ? end : source.length,
      children: [],
      parent,
      selfClosing: Boolean(selfClose),
      textCache: '',
    };
    parent.children.push(node);
    if (!selfClose) stack.push(node);
  }

  return root;
}

export function findChild(node, name) {
  if (!node) return null;
  for (const child of node.children) if (child.name === name) return child;
  return null;
}

export function findChildren(node, name) {
  if (!node) return [];
  return node.children.filter(child => child.name === name);
}

export function findDescendants(node, name, out = []) {
  if (!node) return out;
  for (const child of node.children) {
    if (child.name === name) out.push(child);
    findDescendants(child, name, out);
  }
  return out;
}

export function findDescendant(node, name) {
  return findDescendants(node, name, [])[0] || null;
}

/** Resolve a colon-separated child path, e.g. `p:spPr/a:xfrm/a:off`. */
export function findPath(node, path) {
  let current = node;
  for (const step of String(path).split('/')) {
    if (!step) continue;
    current = findChild(current, step);
    if (!current) return null;
  }
  return current;
}

/**
 * Decoded own-text of a node.
 *
 * `nodeText` walks descendants, which is right for a shape body but wrong for a leaf
 * like `a:t`: an `a:t` has no `a:t` descendants, so a descendant walk returns '' for
 * every text run. Storing the decoded inner text at scan time makes leaves readable
 * without re-slicing the source.
 */
export function nodeText(node) {
  if (!node) return '';
  if (node.name === 'a:t' || node.children.length === 0) {
    return decodeEntities(rawInner('', node, node.textCache));
  }
  const pieces = findDescendants(node, 'a:t');
  return pieces.map(piece => piece.textCache ?? '').join('');
}

export function rawInner(source, node, cached) {
  if (!node) return '';
  if (cached !== undefined && (!source || source.length === 0)) return cached;
  return source.slice(node.innerStart, node.innerEnd);
}

export function rawSlice(source, node) {
  if (!node) return '';
  return source.slice(node.start, node.end);
}

export function attr(node, name, fallback = '') {
  // `!node.attrs` covers a caller passing a placeholder like `{}` or `null` for an optional
  // child. Without this guard the property read throws, and the failure surfaces far from the
  // missing-attribute logic that caused it.
  if (!node || !node.attrs) return fallback;
  const value = node.attrs[name];
  return value === undefined ? fallback : value;
}

export function numberOf(value, fallback = 0) {
  const parsed = Number(value);
  return Number.isFinite(parsed) ? parsed : fallback;
}

/** Splice a single range. Everything outside [start, end) survives byte for byte. */
export function replaceRange(source, start, end, replacement) {
  return source.slice(0, start) + replacement + source.slice(end);
}

/**
 * Apply several edits at once. Ranges are applied from the end so earlier offsets
 * stay valid and callers never have to sort or compensate.
 */
export function applyEdits(source, edits) {
  const ordered = [...(edits || [])]
    .filter(edit => edit && Number.isFinite(edit.start) && Number.isFinite(edit.end) && edit.end >= edit.start)
    .sort((a, b) => b.start - a.start);
  let output = source;
  let guard = Infinity;
  for (const edit of ordered) {
    if (edit.end > guard) {
      throw new Error('overlapping xml edits');
    }
    output = replaceRange(output, edit.start, edit.end, edit.replacement ?? '');
    guard = edit.start;
  }
  return output;
}
