import { getApiBaseURL } from './api';

export const CHAT_ATTACHMENT_DRAG_TYPE = 'application/x-catsco-chat-attachment';
export const CHAT_ATTACHMENT_DRAG_FALLBACK_TYPE = 'text/plain';

const CHAT_ATTACHMENT_DRAG_PREFIX = 'catsco-chat-attachment:';
const activeDrags = new Map();
let activeDragToken = '';
const MAX_ACTIVE_DRAGS = 128;
const DRAG_TOKEN_TTL_MS = 60_000;

export function canDragChatAttachment(block) {
  return Boolean(attachmentFromContentBlock(block));
}

export function writeChatAttachmentDrag(dataTransfer, block) {
  const attachment = attachmentFromContentBlock(block);
  if (!dataTransfer || !attachment) return false;

  const token = createDragToken();
  if (!token) return false;
  pruneActiveDrags();
  activeDrags.set(token, {
    attachment,
    expiresAt: Date.now() + DRAG_TOKEN_TTL_MS,
  });
  activeDragToken = token;
  window.setTimeout(() => {
    activeDrags.delete(token);
    if (activeDragToken === token) activeDragToken = '';
  }, DRAG_TOKEN_TTL_MS);
  const wroteCustomType = setDragData(dataTransfer, CHAT_ATTACHMENT_DRAG_TYPE, token);
  // Safari may suppress or reject custom MIME values for same-page image
  // drags. The fallback still contains only an opaque, short-lived token;
  // attachment metadata remains in activeDrags and receives the same checks.
  const wroteFallbackType = setDragData(
    dataTransfer,
    CHAT_ATTACHMENT_DRAG_FALLBACK_TYPE,
    `${CHAT_ATTACHMENT_DRAG_PREFIX}${token}`,
  );
  if (!wroteCustomType && !wroteFallbackType) {
    activeDrags.delete(token);
    if (activeDragToken === token) activeDragToken = '';
    return false;
  }
  dataTransfer.effectAllowed = 'copy';
  return true;
}

export function readChatAttachmentDrag(dataTransfer) {
  const token = readDragToken(dataTransfer) || activeDragToken;
  if (!token) return null;
  const entry = activeDrags.get(token) || null;
  activeDrags.delete(token);
  if (activeDragToken === token) activeDragToken = '';
  if (!entry || Date.now() >= entry.expiresAt) return null;
  return entry.attachment;
}

export function hasChatAttachmentDrag(dataTransfer) {
  if (dataTransferHasType(dataTransfer, CHAT_ATTACHMENT_DRAG_TYPE)) return true;
  const token = readDragToken(dataTransfer) || activeDragToken;
  const entry = token ? activeDrags.get(token) : null;
  return Boolean(entry && Date.now() < entry.expiresAt);
}

export function clearChatAttachmentDrag() {
  if (!activeDragToken) return;
  activeDrags.delete(activeDragToken);
  activeDragToken = '';
}

export function attachmentFromContentBlock(block) {
  if (!block || (block.type !== 'image' && block.type !== 'file')) return null;
  return normalizeAttachment({
    type: block.type,
    name: block.payload?.name,
    size: block.payload?.size,
    content: {
      type: block.type,
      payload: block.payload,
    },
  });
}

export function attachmentIdentity(value) {
  const type = value?.type;
  const payload = value?.content?.payload || value?.payload;
  const upload = trustedUploadDescriptor(type, payload);
  return upload ? `${type}:${upload.directory}:${upload.fileName}` : '';
}

function normalizeAttachment(value) {
  const type = value?.type;
  const payload = value?.content?.payload;
  const upload = trustedUploadDescriptor(type, payload);
  if (!upload) return null;

  const name = String(value.name || payload.name || (type === 'image' ? '图片' : '文件')).slice(0, 255);
  const rawSize = Number(value.size || payload.size || 0);
  const normalizedPayload = {
    file_key: upload.fileKey,
    url: upload.url,
    name,
    size: Number.isFinite(rawSize) && rawSize > 0 ? rawSize : 0,
  };
  if (typeof payload.mime_type === 'string' && payload.mime_type.trim()) {
    normalizedPayload.mime_type = payload.mime_type.trim().slice(0, 255);
  }
  if (type === 'image') {
    normalizedPayload.thumbnail = upload.url;
    for (const dimension of ['width', 'height']) {
      const number = Number(payload[dimension]);
      if (Number.isFinite(number) && number > 0) normalizedPayload[dimension] = number;
    }
  }

  return {
    type,
    name,
    size: normalizedPayload.size,
    content: {
      type,
      payload: normalizedPayload,
    },
  };
}

function trustedUploadDescriptor(type, payload) {
  if ((type !== 'image' && type !== 'file') || !payload || typeof payload !== 'object') return null;
  const fileKey = typeof payload.file_key === 'string' ? payload.file_key.trim().replace(/^\/+/, '') : '';
  const rawURL = typeof payload.url === 'string' && payload.url.trim()
    ? payload.url.trim()
    : typeof payload.thumbnail === 'string' ? payload.thumbnail.trim() : '';
  if (!fileKey || !rawURL) return null;

  try {
    const apiBase = new URL(getApiBaseURL(), window.location.origin);
    const url = new URL(rawURL, apiBase);
    const match = url.pathname.match(/^\/uploads\/(images|files)\/([^/]+)$/);
    if (url.origin !== apiBase.origin || url.username || url.password || !match) return null;

    const [, directory, encodedFileName] = match;
    const fileName = decodeURIComponent(encodedFileName);
    if (!fileName || fileName.includes('/') || fileName.includes('\\')) return null;
    if (type === 'file' ? directory !== 'files' : directory !== 'images') return null;
    if (fileKey !== fileName && fileKey !== `${directory}/${fileName}`) return null;

    return {
      directory,
      fileName,
      fileKey,
      url: `${url.pathname}${url.search}`,
    };
  } catch {
    return null;
  }
}

function pruneActiveDrags() {
  while (activeDrags.size >= MAX_ACTIVE_DRAGS) {
    const oldestToken = activeDrags.keys().next().value;
    if (!oldestToken) break;
    activeDrags.delete(oldestToken);
  }
}

function createDragToken() {
  if (globalThis.crypto?.randomUUID) return globalThis.crypto.randomUUID();
  const bytes = new Uint8Array(24);
  globalThis.crypto?.getRandomValues?.(bytes);
  if (bytes.some((value) => value !== 0)) {
    return Array.from(bytes, (value) => value.toString(16).padStart(2, '0')).join('');
  }
  return '';
}

function setDragData(dataTransfer, type, value) {
  try {
    dataTransfer.setData(type, value);
    return true;
  } catch {
    return false;
  }
}

function readDragToken(dataTransfer) {
  if (!dataTransfer || typeof dataTransfer.getData !== 'function') return '';
  if (dataTransferHasType(dataTransfer, CHAT_ATTACHMENT_DRAG_TYPE)) {
    const token = dataTransfer.getData(CHAT_ATTACHMENT_DRAG_TYPE);
    if (token) return token;
  }
  if (!dataTransferHasType(dataTransfer, CHAT_ATTACHMENT_DRAG_FALLBACK_TYPE)) return '';
  const fallback = dataTransfer.getData(CHAT_ATTACHMENT_DRAG_FALLBACK_TYPE);
  return fallback.startsWith(CHAT_ATTACHMENT_DRAG_PREFIX)
    ? fallback.slice(CHAT_ATTACHMENT_DRAG_PREFIX.length)
    : '';
}

function dataTransferHasType(dataTransfer, type) {
  if (!dataTransfer?.types) return false;
  return Array.from(dataTransfer.types).includes(type);
}
