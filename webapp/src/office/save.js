// Save linkage: turn edited bytes into a new attachment message.
//
// The server never mutates an attachment. Uploading again produces a new `file_key`, and the
// old attachment stays addressable, which is what makes "改错了还能退回上一版" true rather
// than aspirational. So saving is: upload → append a message that points at the new file.
//
// This module is pure on purpose. The upload and the send are side effects that need a live
// session, but the shape of the message is what actually decides whether 小八 reads the new
// version next turn, and that can be asserted without a server.

export const OFFICE_SAVE_NOTICE = '已保存为新版本。会话里出现的新附件就是这一版，小八读到的也是这一版';

/**
 * Normalise the name of the saved file.
 *
 * A file that came in as `报告.pptx` must go out as `报告.pptx`, not `报告-edited.pptx`,
 * because the next save from that attachment has to look like the same document. Only a
 * missing extension is repaired, and it is repaired from the family, never from a guess.
 */
export function officeSavedFileName(originalName, family = 'pptx') {
  const raw = String(originalName || '').trim();
  const fallback = family === 'docx' ? 'document.docx' : 'presentation.pptx';
  if (!raw) return fallback;
  const ext = family === 'docx' ? '.docx' : '.pptx';
  if (raw.toLowerCase().endsWith(ext)) return raw;
  // A wrong extension on the original is kept as-is minus its tail, then re-suffixed: the
  // bytes are what the family says they are, and a `.ppt` name on pptx bytes makes Office
  // pick the legacy reader and fail.
  const dot = raw.lastIndexOf('.');
  const base = dot > 0 ? raw.slice(0, dot) : raw;
  return `${base}${ext}`;
}

/**
 * Build the message that records an edited version.
 *
 * One `text` block carries the visible sentence and one `file` block carries the payload.
 * The text block is not decoration: the message list, the notification preview and the
 * conversation summary all read `content`, and a message with only a file block renders as
 * an empty bubble in several places.
 */
export function buildOfficeUpdateMessage({ uploaded, originalName, family = 'pptx', slideCount, changedParts }) {
  const upload = uploaded || {};
  const name = String(upload.name || officeSavedFileName(originalName, family)).trim();
  const payload = {
    file_key: String(upload.file_key || ''),
    url: String(upload.url || ''),
    name,
    size: Number(upload.size) || 0,
    mime_type: String(upload.mime_type || defaultMime(family)),
  };
  if (!payload.url) throw new Error('上传结果缺少地址，无法登记新版本');

  const label = family === 'docx' ? 'Word' : 'PPT';
  const pages = Number.isFinite(slideCount) && slideCount > 0 ? `，共 ${slideCount} 页` : '';
  const parts = Number.isFinite(changedParts) && changedParts > 0
    ? `，改了 ${changedParts} 处` : '';
  const text = `已按你的修改更新 ${name}（${label}${pages}${parts}）`;

  return {
    content: text,
    content_blocks: [
      { type: 'text', text },
      { type: 'file', payload },
    ],
    payload,
  };
}

function defaultMime(family) {
  return family === 'docx'
    ? 'application/vnd.openxmlformats-officedocument.wordprocessingml.document'
    : 'application/vnd.openxmlformats-officedocument.presentationml.presentation';
}

/** Wrap saved bytes in a File so the existing upload path accepts them unchanged. */
export function officeBytesToFile(bytes, name, family = 'pptx') {
  if (!bytes || !bytes.byteLength) throw new Error('没有可保存的内容');
  const fileName = officeSavedFileName(name, family);
  if (typeof File === 'function') {
    return new File([bytes], fileName, { type: defaultMime(family) });
  }
  // jsdom and some older runtimes have no File constructor; Blob carries the bytes and the
  // upload path only needs `name` to build its header, which the caller supplies separately.
  const blob = new Blob([bytes], { type: defaultMime(family) });
  blob.name = fileName;
  return blob;
}

/**
 * Collect the element ids that actually changed, for the human-readable summary.
 *
 * Counts operations, not edits: dragging a shape fifty times and writing it once is one
 * change, because that is what the user would say out loud.
 */
export function countChangedOps(changes) {
  if (!Array.isArray(changes)) return 0;
  return changes.filter(change => change && typeof change.op === 'string').length;
}
