// A conversation can hold several attachments with the same name, because every save
// uploads a new immutable file and appends a message. Clicking an older card therefore
// opens older bytes while the newest version sits further down the thread. This module
// finds that newest one so the panel does not show a stale document as if it were current.

function blocksOf(message) {
  const blocks = message?.content_blocks;
  if (Array.isArray(blocks)) return blocks;
  const content = message?.content;
  if (content && typeof content === 'object' && Array.isArray(content.content_blocks)) {
    return content.content_blocks;
  }
  return [];
}

/**
 * The last attachment in the thread that has the same file name as `file`.
 *
 * Only messages after the one the file came from are considered, so opening the newest
 * card never jumps backwards. Returns null when nothing newer matches, which keeps the
 * caller's file untouched instead of guessing.
 */
export function latestNamedAttachment(messages, file) {
  const name = String(file?.name || '').trim();
  if (!name) return null;
  const currentId = Number(file?.annotation_source?.message_id) || 0;
  let best = null;
  for (const message of Array.isArray(messages) ? messages : []) {
    const id = Number(message?.id) || 0;
    if (currentId && (!id || id <= currentId)) continue;
    for (const block of blocksOf(message)) {
      if (block?.type !== 'file') continue;
      const payload = block.payload || {};
      if (!payload.url) continue;
      if (String(payload.name || '').trim() !== name) continue;
      best = { ...payload, messageId: id };
    }
  }
  return best;
}