import {
  buildFileVersionBindingRequest, buildOfficeUpdateMessage, officeBytesToFile, OFFICE_SYNC_NOTICE,
} from './save.js';
import { readStorageValue, writeStorageValue } from '../utils/storage-access';

export const OFFICE_VERSION_STORAGE_PREFIX = 'office.fileVersion.';
export const OFFICE_LOCAL_SAVE_NOTICE = '已保存最新版，仅本机可见；关掉重开会回到旧版';
export const OFFICE_SERVER_UNSUPPORTED_NOTICE = '已保存到本机；服务器版本记录未启用，关掉重开会回到旧版';
export const OFFICE_VERSION_UNSUPPORTED_CODE = 'office-version-unsupported';
export const OFFICE_UNSYNCED_NOTICE = '已保存最新版，但尚未发给小八；会话里仍是旧附件';
export { OFFICE_SYNC_NOTICE };

const OFFICE_EXT = /\.(docx|pptx)$/i;

function positiveInteger(value) {
  const n = Number(value);
  return Number.isInteger(n) && n > 0 ? n : null;
}

function nonNegativeInteger(value) {
  const n = Number(value);
  return Number.isInteger(n) && n >= 0 ? n : null;
}

function cleanString(value) {
  return typeof value === 'string' ? value.trim() : '';
}

function fileNameFromURL(value) {
  const raw = cleanString(value).split(/[?#]/)[0];
  if (!raw) return '';
  try {
    return decodeURIComponent(new URL(raw, 'https://app.catsco.cc').pathname.split('/').pop() || '');
  } catch {
    return raw.split('/').pop() || '';
  }
}

function fileExtension(file) {
  const name = cleanString(file?.name);
  const key = cleanString(file?.file_key);
  const explicit = /\.(docx|pptx)$/i.exec(name) || /\.(docx|pptx)$/i.exec(key);
  if (explicit) return explicit[1].toLowerCase();
  const mime = cleanString(file?.mime_type).toLowerCase();
  if (mime.includes('wordprocessingml')) return 'docx';
  if (mime.includes('presentationml')) return 'pptx';
  const url = cleanString(file?.url).split(/[?#]/)[0];
  const fromURL = /\.(docx|pptx)$/i.exec(url);
  return fromURL ? fromURL[1].toLowerCase() : '';
}

export function isOfficeVersionFile(file) {
  const ext = fileExtension(file);
  return Boolean(ext && OFFICE_EXT.test(`x.${ext}`));
}

export function officeVersionSource(file) {
  const annotation = file?.annotation_source || {};
  const stored = file?.office_version_source || {};
  const topicID = cleanString(annotation.topic_id || stored.topic_id || file?.topic_id);
  const messageID = positiveInteger(
    annotation.message_id ?? stored.source_message_id ?? stored.message_id ?? file?.source_message_id,
  );
  const attachmentIndex = nonNegativeInteger(
    annotation.attachment_index ?? stored.source_attachment_index
      ?? file?.source_attachment_index,
  );
  if (!topicID || messageID === null || attachmentIndex === null) return null;
  const sourceFileKey = cleanString(
    stored.source_file_key || annotation.file_key || file?.file_key || fileNameFromURL(file?.url),
  );
  return {
    topic_id: topicID,
    source_message_id: messageID,
    source_attachment_index: attachmentIndex,
    source_file_key: sourceFileKey,
  };
}

export function latestVersionFromUpload(uploaded) {
  const latest = {
    file_key: cleanString(uploaded?.file_key),
    url: cleanString(uploaded?.url),
    name: cleanString(uploaded?.name),
    size: Number(uploaded?.size) || 0,
    mime_type: cleanString(uploaded?.mime_type),
  };
  if (!latest.url) throw new Error('上传结果缺少地址');
  if (!latest.file_key) {
    try {
      latest.file_key = decodeURIComponent(
        new URL(latest.url, 'https://app.catsco.cc').pathname.split('/').pop() || '',
      );
    } catch {
      latest.file_key = latest.url.split(/[?#]/)[0].split('/').pop() || '';
    }
  }
  return latest;
}

export function localVersionStorageKey(source) {
  if (!source) return '';
  return `${OFFICE_VERSION_STORAGE_PREFIX}${encodeURIComponent(source.topic_id)}`
    + `.${source.source_message_id}.${source.source_attachment_index}`;
}

export function writeLocalVersionBinding(source, latest, storage = 'localStorage') {
  const key = localVersionStorageKey(source);
  if (!key) return false;
  return writeStorageValue(key, JSON.stringify({
    source,
    latest: latestVersionFromUpload(latest),
  }), storage);
}

export function readLocalVersionBinding(source, storage = 'localStorage') {
  const key = localVersionStorageKey(source);
  if (!key) return null;
  const raw = readStorageValue(key, storage);
  if (!raw) return null;
  try {
    const parsed = JSON.parse(raw);
    if (!parsed?.latest?.url) return null;
    return latestVersionFromUpload(parsed.latest);
  } catch {
    return null;
  }
}

function latestFromBinding(binding) {
  if (!binding) return null;
  const latest = binding.latest || binding;
  if (!latest.latest_url && !latest.url) return null;
  return latestVersionFromUpload({
    file_key: latest.latest_file_key ?? latest.file_key,
    url: latest.latest_url ?? latest.url,
    name: latest.latest_name ?? latest.name,
    size: latest.latest_size ?? latest.size,
    mime_type: latest.latest_mime_type ?? latest.mime_type,
  });
}

function sameLatest(a, b) {
  if (!a || !b) return false;
  if (a.file_key && b.file_key) return a.file_key === b.file_key;
  return a.url === b.url;
}

export function applyOfficeLatestVersion(file, source, latest) {
  if (!file || !latest) return file;
  const changed = !sameLatest(file, latest);
  return {
    ...file,
    name: latest.name || file.name,
    url: latest.url,
    file_key: latest.file_key,
    size: latest.size,
    mime_type: latest.mime_type || file.mime_type,
    office_version_source: source || file.office_version_source,
    office_latest: latest,
    office_version_unsynced: changed || Boolean(file.office_version_unsynced),
    annotation_source: changed ? undefined : file.annotation_source,
  };
}

export async function resolveLatestOfficeVersion(file, getBinding, storage = 'localStorage') {
  if (!file || !isOfficeVersionFile(file)) return file;
  const source = officeVersionSource(file);
  if (!source) return file;
  if (typeof getBinding === 'function') {
    try {
      const binding = await getBinding(source);
      const latest = latestFromBinding(binding);
      if (latest) {
        writeLocalVersionBinding(source, latest, storage);
        return applyOfficeLatestVersion(file, source, latest);
      }
    } catch {
      // A server outage falls through to the last successful local pointer below.
    }
  }
  const local = readLocalVersionBinding(source, storage);
  return local ? applyOfficeLatestVersion(file, source, local) : file;
}

export async function performQuietOfficeSave({
  file, bytes, fileName, family, topicId, uploadFile, upsertBinding, sendMessage,
  requirement = '', slideCount, changedParts, storage = 'localStorage',
}) {
  if (!bytes?.byteLength) throw new Error('没有可保存的内容');
  if (!topicId) throw new Error('缺少会话');
  const source = officeVersionSource(file);
  const wrapped = officeBytesToFile(bytes, fileName, family);
  const uploaded = await uploadFile(wrapped, 'file');
  const latest = latestVersionFromUpload(uploaded);

  // When the user typed something to send along, the message itself carries the new file,
  // so 小八 reads the edited version on its next turn without a separate pointer to keep in
  // sync. The source→latest pointer stays the path for a silent save with no message.
  const ask = typeof requirement === 'string' ? requirement.trim() : '';
  if (ask) {
    if (typeof sendMessage !== 'function') throw new Error('当前会话无法发送消息');
    const message = buildOfficeUpdateMessage({
      uploaded,
      originalName: fileName || file?.name,
      family,
      slideCount,
      changedParts,
      requirement: ask,
    });
    const result = await sendMessage(topicId, {
      type: 'text',
      content: message.content,
      content_blocks: message.content_blocks,
    });
    if (!result) throw new Error('消息未发送成功');
    if (source) writeLocalVersionBinding(source, latest, storage);
    return {
      source,
      latest,
      uploaded,
      binding: null,
      sent: result,
      messageSent: true,
      localOnly: false,
      notice: OFFICE_SYNC_NOTICE,
      nextFile: applyOfficeLatestVersion(file, source, latest),
    };
  }

  let binding = null;
  if (source) {
    const request = buildFileVersionBindingRequest(source, latest, topicId);
    binding = await upsertBinding(request);
    if (!binding) throw new Error('版本指针未保存');
    writeLocalVersionBinding(source, latest, storage);
  }
  return {
    source,
    latest,
    uploaded,
    binding,
    messageSent: false,
    localOnly: !source,
    notice: source ? OFFICE_UNSYNCED_NOTICE : OFFICE_LOCAL_SAVE_NOTICE,
    // Compare against the file the user actually opened. Replacing its key first would
    // make every quiet save look like "same version" and leave annotations pointing at
    // the immutable source attachment.
    nextFile: applyOfficeLatestVersion(file, source, latest),
  };
}

export async function performExplicitOfficeSync({
  file, latestVersion, family, topicId, sendMessage, slideCount, changes,
}) {
  if (!topicId) throw new Error('缺少会话');
  const latest = latestFromBinding({ latest: latestVersion });
  if (!latest) throw new Error('还没有可同步的最新版');
  const message = buildOfficeUpdateMessage({
    uploaded: latest,
    originalName: file?.name || latest.name,
    family,
    slideCount,
    changedParts: Array.isArray(changes) ? changes.length : 0,
  });
  const result = await sendMessage(topicId, {
    type: 'text',
    content: message.content,
    content_blocks: message.content_blocks,
  });
  return { latest, message, result };
}
