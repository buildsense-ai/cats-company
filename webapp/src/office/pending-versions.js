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
    return { source, latest: latestVersionFromUpload(latest) };
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

/** Drop the record once the file has travelled to the conversation. */
export function markVersionSynced(source, storage = 'localStorage') {
  const key = localVersionStorageKey(source);
  if (!key) return false;
  return removeStorageValue(key, storage);
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
  for (const entry of listPendingVersions(topicId, storage)) {
    const latest = entry.latest;
    if (!latest?.url || seen.has(latest.url)) continue;
    seen.add(latest.url);
    attachments.push({
      type: 'file',
      name: latest.name,
      content: {
        payload: {
          file_key: latest.file_key,
          url: latest.url,
          name: latest.name,
          size: latest.size,
          mime_type: latest.mime_type,
        },
      },
    });
  }
  return attachments;
}