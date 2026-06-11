export const MAX_ATTACHMENT_SIZE_MB = 300;
export const MAX_ATTACHMENT_SIZE = MAX_ATTACHMENT_SIZE_MB * 1024 * 1024;

export const SUPPORTED_IMAGE_EXTENSIONS = new Set(['.jpg', '.jpeg', '.png', '.gif', '.webp']);
export const SUPPORTED_IMAGE_MIME_TYPES = new Set(['image/jpeg', 'image/png', 'image/gif', 'image/webp']);
export const IMAGE_UPLOAD_ACCEPT = '.jpg,.jpeg,.png,.gif,.webp,image/jpeg,image/png,image/gif,image/webp';

export function inferAttachmentType(file, requestedType) {
  if (requestedType) return requestedType;
  if (file?.type?.toLowerCase().startsWith('image/')) return 'image';
  const name = file?.name?.toLowerCase() || '';
  const extension = name.includes('.') ? name.slice(name.lastIndexOf('.')) : '';
  return SUPPORTED_IMAGE_EXTENSIONS.has(extension) ? 'image' : 'file';
}

export function validateImageUpload(file, options = {}) {
  const {
    maxSizeBytes = MAX_ATTACHMENT_SIZE,
    maxSizeMB = MAX_ATTACHMENT_SIZE_MB,
    missingMessage = '未找到可上传的文件。',
    unsupportedTypeMessage = '当前仅支持 JPG、PNG、GIF、WebP 图片。',
  } = options;

  if (!file) return missingMessage;
  if (file.size > maxSizeBytes) {
    return `文件过大：${(file.size / 1024 / 1024).toFixed(1)}MB。当前最多支持 ${maxSizeMB}MB。`;
  }

  const mimeType = file.type?.toLowerCase() || '';
  const name = file.name?.toLowerCase() || '';
  const extension = name.includes('.') ? name.slice(name.lastIndexOf('.')) : '';
  const mimeAllowed = !mimeType || SUPPORTED_IMAGE_MIME_TYPES.has(mimeType);
  const extensionAllowed = !extension || SUPPORTED_IMAGE_EXTENSIONS.has(extension);
  if (mimeAllowed && extensionAllowed) return '';
  return unsupportedTypeMessage;
}
