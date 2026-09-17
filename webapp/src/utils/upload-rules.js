export const MAX_ATTACHMENT_SIZE_MB = 300;
export const MAX_ATTACHMENT_SIZE = MAX_ATTACHMENT_SIZE_MB * 1024 * 1024;

export const SUPPORTED_IMAGE_EXTENSIONS = new Set(['.jpg', '.jpeg', '.png', '.gif', '.webp']);
export const SUPPORTED_IMAGE_MIME_TYPES = new Set(['image/jpeg', 'image/png', 'image/gif', 'image/webp']);
export const IMAGE_UPLOAD_ACCEPT = '.jpg,.jpeg,.png,.gif,.webp,image/jpeg,image/png,image/gif,image/webp';
export const VIDEO_UPLOAD_ACCEPT = '.mp4,.webm,.ogg,.ogv,.m4v,.mov,video/mp4,video/webm,video/ogg,video/x-m4v,video/quicktime';

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

// Turn an upload failure into a user-facing message. A 413 does not imply the
// 300MB product limit: an intermediate gateway can reject a much smaller body
// before the upload API sees it, so prefer the server-reported limit and
// otherwise describe a gateway rejection instead of guessing.
export function formatUploadErrorMessage(error) {
  const limitMB = Number(error?.data?.max_size_mb);
  if (Number.isFinite(limitMB) && limitMB > 0) {
    return `上传失败：文件超过 ${limitMB}MB 限制。`;
  }
  const message = String(error?.message || '上传失败');
  if (message.includes('413') || message.includes('Payload Too Large')) {
    return '上传失败：文件被服务器网关拒绝，可能超出上传链路限制，请压缩或拆分后重试。';
  }
  if (message.includes('too large')) {
    return '上传失败：文件过大，超出服务器允许的大小，请压缩后重试。';
  }
  if (message.includes('invalid image type')) {
    return '上传失败：当前仅支持 JPG、PNG、GIF、WebP 图片。';
  }
  if (message.includes('file type not allowed')) {
    return '上传失败：该文件类型暂不支持。';
  }
  if (message.includes('Unexpected token') || message.includes('invalid server response') || message.includes('JSON')) {
    return '上传失败：服务器返回了无法识别的响应。';
  }
  return `上传失败：${message}`;
}
