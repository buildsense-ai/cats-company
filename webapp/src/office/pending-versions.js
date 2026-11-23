// Versions the user saved but never sent to the agent.
//
// A save uploads the edited bytes and remembers the new file as "latest for this source".
// Nothing is posted to the conversation, so the agent still sees the old attachment. The
// user's next message is the natural moment to hand the newest file over: they are already
// talking about it, and attaching it then costs no extra message.
//
// This module only reads and clears those local records; it never uploads.

import {
  OFFICE_VERSION_STORAGE_PREFIX, latestVersionFromUpload, localVersionStorageKey,
} from './version-binding.js';
import { readStorageValue, removeStorageValue, storageKeys } from '../utils/storage-access';

function parseEntry(raw) {
  if (!raw) return null;
  try {
    const parsed = JSON.parse(raw);
    const source = parsed?.source;
    const latest = parsed?.latest;
    if (!source || !latest?.url) return null;
    return {
      source,
      latest: latestVersionFromUpload(latest),
      savedAt: Number(parsed?.savedAt) || 0,
    };
  } catch {
    return null;
  }
}

/** Every unsent saved version belonging to one conversation, newest last. */
export function listPendingVersions(topicId, storage = 'localStorage') {
  const topic = String(topicId || '');
  if (!topic) return [];
  const found = [];
  for (const key of storageKeys(storage)) {
    if (!key.startsWith(OFFICE_VERSION_STORAGE_PREFIX)) continue;
    const entry = parseEntry(readStorageValue(key, storage));
    if (!entry) continue;
    if (String(entry.source.topic_id || '') !== topic) continue;
    found.push({ ...entry, key });
  }
  return found;
}

/**
 * Name a document as the user would name it.
 *
 * Copy suffixes are stripped because a document that was saved, downloaded and re-edited
 * comes back as "报告(1).docx"; treating that as a different document is how one file turned
 * into four attachments on a single message.
 */
function normalizedDocumentName(entry) {
  const raw = String(entry?.latest?.name || entry?.source?.source_file_key || '');
  const base = raw.split(/[?#]/)[0].split(/[\\/]/).pop() || '';
  // Strip the extension first. A copy suffix sits before it ("报告(1).docx"), so a rule that
  // anchors on the end of the string only works once the extension is gone.
  return base
    .replace(/\.(docx|pptx)$/i, '')
    .replace(/[\s_-]*(?:副本|copy)(?:\s*[（(]\d+[)）])?/gi, '')
    .replace(/\s*[（(]\d+[)）]$/, '')
    .trim()
    .toLowerCase();
}

/**
 * Identity of the document behind a record.
 *
 * Falls back to the storage key when no name survives, so two nameless records are never
 * merged into one and silently lose a file.
 */
function documentIdentity(entry) {
  const name = normalizedDocumentName(entry);
  const topic = String(entry?.source?.topic_id || '');
  if (name) return `${topic}|${name}`;
  return `${topic}|${localVersionStorageKey(entry?.source)}`;
}

/**
 * Keep only the newest save per document.
 *
 * `superseded` still has to be cleared after a send, otherwise those records keep their
 * place in storage and reappear as extra attachments on a later message.
 */
export function collapsePendingVersions(entries) {
  const byDocument = new Map();
  const superseded = [];
  for (const entry of Array.isArray(entries) ? entries : []) {
    const id = documentIdentity(entry);
    const kept = byDocument.get(id);
    if (!kept) {
      byDocument.set(id, entry);
      continue;
    }
    const newer = (entry.savedAt || 0) >= (kept.savedAt || 0) ? entry : kept;
    superseded.push(newer === entry ? kept : entry);
    byDocument.set(id, newer);
  }
  return { latest: [...byDocument.values()], superseded };
}

/** The records a message would actually carry: at most one per document. */
export function listPendingVersionDocuments(topicId, storage = 'localStorage') {
  return collapsePendingVersions(listPendingVersions(topicId, storage));
}

/** Drop the record once the file has travelled to the conversation. */
export function markVersionSynced(source, storage = 'localStorage') {
  const key = localVersionStorageKey(source);
  if (!key) return false;
  return removeStorageValue(key, storage);
}

/** Clear the carried records plus the older saves they replaced. */
export function markVersionsSynced(entries, storage = 'localStorage') {
  let cleared = 0;
  for (const entry of Array.isArray(entries) ? entries : []) {
    if (markVersionSynced(entry?.source, storage)) cleared += 1;
  }
  return cleared;
}

/**
 * Attachment drafts for the composer.
 *
 * The composer expects `{ type, name, content: { payload } }`, which is the shape
 * `buildAtomicContentBlocks` reads. A file the user already attached by hand is skipped so
 * the same bytes are not sent twice.
 */
export function pendingVersionAttachments(topicId, existing = [], storage = 'localStorage') {
  const seen = new Set(
    (existing || [])
      .map(item => String(item?.content?.payload?.url || item?.url || ''))
      .filter(Boolean),
  );
  const attachments = [];
  const { latest } = listPendingVersionDocuments(topicId, storage);
  for (const entry of latest) {
    const file = entry.latest;
    if (!file?.url || seen.has(file.url)) continue;
    seen.add(file.url);
    attachments.push({
      type: 'file',
      name: file.name,
      content: {
        payload: {
          file_key: file.file_key,
          url: file.url,
          name: file.name,
          size: file.size,
          mime_type: file.mime_type,
        },
      },
    });
  }
  return attachments;
}