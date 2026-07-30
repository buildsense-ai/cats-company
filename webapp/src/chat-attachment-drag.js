import { getApiBaseURL } from './api';

export const CHAT_ATTACHMENT_DRAG_TYPE = 'application/x-catsco-chat-attachment';

const activeDrags = new Map();
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
  window.setTimeout(() => activeDrags.delete(token), DRAG_TOKEN_TTL_MS);
  dataTransfer.setData(CHAT_ATTACHMENT_DRAG_TYPE, token);
  dataTransfer.effectAllowed = 'copy';
  return true;
}

export function readChatAttachmentDrag(dataTransfer) {
  if (!dataTransferHasType(dataTransfer, CHAT_ATTACHMENT_DRAG_TYPE)) return null;
  const token = dataTransfer.getData(CHAT_ATTACHMENT_DRAG_TYPE);
  const entry = activeDrags.get(token) || null;
  activeDrags.delete(token);
  if (!entry || Date.now() >= entry.expiresAt) return null;
  return entry.attachment;
}

export function hasChatAttachmentDrag(dataTransfer) {
  return dataTransferHasType(dataTransfer, CHAT_ATTACHMENT_DRAG_TYPE);
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

function dataTransferHasType(dataTransfer, type) {
  if (!dataTransfer?.types) return false;
  return Array.from(dataTransfer.types).includes(type);
}
