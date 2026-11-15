// OOXML package access: one zip, many parts, and every untouched part must come
// back out byte-identical.
//
// jszip recompresses on write, so the zip container bytes differ from the file we
// read. That is fine and Office accepts it. What must NOT change is the inflated
// content of any part we did not edit, so this module stores the inflated bytes
// once and re-emits them as-is.

import JSZip from 'jszip';
import { scanXml, findDescendants, attr } from './xml.js';

export const OFFICE_MAIN_PARTS = {
  pptx: 'ppt/presentation.xml',
  docx: 'word/document.xml',
};

export class NotAnOfficePackage extends Error {
  constructor(message) {
    super(message);
    this.name = 'NotAnOfficePackage';
  }
}

// OOXML relationships use relative targets such as `../slideLayouts/slideLayout1.xml`.
// Left uncollapsed, those paths never match a package entry, so the layout, master and
// theme all silently resolve to null and every inherited shape loses its geometry.
// Collapsing `..` here is the fix, and it has to happen before any lookup.
function normalizePartName(name) {
  const raw = String(name || '').replace(/\\+/g, '/').replace(/^\/+/, '');
  const segments = [];
  for (const segment of raw.split('/')) {
    if (!segment || segment === '.') continue;
    if (segment === '..') { segments.pop(); continue; }
    segments.push(segment);
  }
  return segments.join('/');
}

export async function openPackage(input) {
  let zip;
  try {
    zip = await JSZip.loadAsync(input);
  } catch (error) {
    throw new NotAnOfficePackage(`无法解开文件包：${error?.message || error}`);
  }

  // `files` holds only the entries that came in with the zip. A part the editor creates —
  // a new slide, an inserted image — is NOT an entry here, so it lives in `added` instead.
  //
  // Getting this wrong is silent and total: everything reads back fine in memory, then
  // `save()` walks the original entry list and the new parts are simply absent from the
  // output. PowerPoint then opens a presentation that references slides and media files
  // which are not in the package.
  const files = new Map();
  zip.forEach((relativePath, entry) => {
    if (!entry.dir) files.set(normalizePartName(relativePath), entry);
  });

  // Text parts are cached as strings so repeated lookups (relationships, content types,
  // slide XML) do not re-inflate on every access. Binary parts stay binary.
  const textCache = new Map();
  const binaryCache = new Map();
  const edited = new Map();
  // Parts created this session. Values are strings for text parts, Uint8Array for binary.
  const added = new Map();
  const removed = new Set();

  const exists = key => !removed.has(key) && (files.has(key) || added.has(key));

  return {
    parts() {
      const original = [...files.keys()].filter(key => !removed.has(key));
      const created = [...added.keys()].filter(key => !removed.has(key));
      return [...original, ...created].sort();
    },
    has(name) {
      return exists(normalizePartName(name));
    },
    async text(name) {
      const key = normalizePartName(name);
      if (removed.has(key)) return null;
      if (edited.has(key)) return edited.get(key);
      if (textCache.has(key)) return textCache.get(key);
      if (added.has(key)) {
        const value = added.get(key);
        // A part added as binary but read as text would otherwise yield "[object …]".
        const asText = typeof value === 'string' ? value : new TextDecoder().decode(value);
        textCache.set(key, asText);
        return asText;
      }
      const target = files.get(key);
      if (!target) return null;
      const value = await target.async('string');
      textCache.set(key, value);
      return value;
    },
    async binary(name) {
      const key = normalizePartName(name);
      if (removed.has(key)) return null;
      if (binaryCache.has(key)) return binaryCache.get(key);
      if (added.has(key)) {
        const value = added.get(key);
        const bytes = typeof value === 'string' ? new TextEncoder().encode(value) : value;
        binaryCache.set(key, bytes);
        return bytes;
      }
      const target = files.get(key);
      if (!target) return null;
      const value = await target.async('uint8array');
      binaryCache.set(key, value);
      return value;
    },
    /** Stage an edited or newly created text part. Nothing is written until `save()`. */
    setText(name, value) {
      const key = normalizePartName(name);
      const text = String(value);
      removed.delete(key);
      if (files.has(key)) edited.set(key, text);
      else added.set(key, text);
      textCache.set(key, text);
      binaryCache.delete(key);
    },
    setBinary(name, value) {
      const key = normalizePartName(name);
      removed.delete(key);
      if (files.has(key)) {
        edited.delete(key);
        binaryCache.set(key, value);
      } else {
        added.set(key, value);
        binaryCache.set(key, value);
      }
    },
    /**
     * Drop a part entirely.
     *
     * Deleting a slide without deleting its part leaves an orphan XML file behind. That is
     * not fatal — Office ignores unreferenced parts — but it bloats the file and confuses
     * anyone reading it later, so the delete path removes it explicitly.
     */
    removePart(name) {
      const key = normalizePartName(name);
      const existed = exists(key);
      removed.add(key);
      textCache.delete(key);
      binaryCache.delete(key);
      edited.delete(key);
      added.delete(key);
      return existed;
    },
    editedParts() {
      return [...edited.keys()].sort();
    },
    addedParts() {
      return [...added.keys()].filter(key => !removed.has(key)).sort();
    },
    async save() {
      const output = new JSZip();
      // Preserve the original entry order; some validators compare it.
      for (const key of files.keys()) {
        if (removed.has(key)) continue;
        if (edited.has(key)) {
          output.file(key, edited.get(key));
        } else if (binaryCache.has(key)) {
          output.file(key, binaryCache.get(key));
        } else if (textCache.has(key)) {
          output.file(key, textCache.get(key));
        } else {
          output.file(key, await files.get(key).async('uint8array'));
        }
      }
      // New parts go last so the original order above stays untouched.
      for (const key of [...added.keys()].sort()) {
        if (removed.has(key)) continue;
        output.file(key, added.get(key));
      }
      return output.generateAsync({ type: 'uint8array', compression: 'DEFLATE',
        compressionOptions: { level: 6 } });
    },
  };
}

/**
 * Read the relationships of a part. OOXML resolves `r:id` through these, so a
 * slide's layout or an image's media file is only reachable this way.
 */
export async function readRelationships(pkg, partName) {
  const clean = normalizePartName(partName);
  const slash = clean.lastIndexOf('/');
  const dir = slash === -1 ? '' : clean.slice(0, slash);
  const base = slash === -1 ? clean : clean.slice(slash + 1);
  const relPath = `${dir ? `${dir}/` : ''}_rels/${base}.rels`;
  const xml = await pkg.text(relPath);
  if (!xml) return new Map();

  const tree = scanXml(xml);
  const map = new Map();
  for (const rel of findDescendants(tree, 'Relationship')) {
    const id = attr(rel, 'Id');
    const target = attr(rel, 'Target');
    if (!id || !target) continue;
    map.set(id, {
      id,
      type: attr(rel, 'Type'),
      target,
      targetMode: attr(rel, 'TargetMode'),
      // Relative targets resolve against the part's own directory.
      resolved: target.startsWith('/')
        ? target.slice(1)
        : normalizePartName(dir ? `${dir}/${target}` : target),
    });
  }
  return map;
}

/** Detect the document family from part names rather than trusting the extension. */
export function detectFamily(pkg) {
  if (pkg.has(OFFICE_MAIN_PARTS.pptx)) return 'pptx';
  if (pkg.has(OFFICE_MAIN_PARTS.docx)) return 'docx';
  return null;
}
