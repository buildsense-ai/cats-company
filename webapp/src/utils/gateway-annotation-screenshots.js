const SCREENSHOT_WARNING_TEXT = {
  'cross-origin-image': '部分图片来自其他站点，可能无法包含在截图中。',
  'cross-origin-background': '部分背景图片来自其他站点，可能无法包含在截图中。',
  'unloaded-image': '部分图片尚未加载完成，截图可能缺少它们。',
  'video': '视频内容无法包含在静态截图中。',
  'embedded-content': '内嵌框架或嵌入内容无法包含在截图中。',
  'embedded-frame': '内嵌框架或嵌入内容无法包含在截图中。',
  'webgl-canvas': '部分三维或 WebGL 画布内容可能无法包含在截图中。',
  'unreadable-canvas': '部分画布内容受保护，无法读取，已按空白或实际可见部分呈现。',
  'sensitive-content-masked': '密码等敏感输入内容已在截图中遮挡。',
};
function screenshotWarningText(code) {
  return SCREENSHOT_WARNING_TEXT[code] || `截图可能不完整：${String(code || '').slice(0, 60)}`;
}

// UI conversion only. The shared host owns screenshot protocol validation;
// repeat the fixed MIME/size bounds before turning app bytes into an owned upload.
export function screenshotWarnings(value) {
  if (!Array.isArray(value)) return [];
  return [...new Set(value.map(screenshotWarningText))];
}

export function screenshotFile(image) {
  if (!image || !['full', 'crop'].includes(image.role) || image.mime_type !== 'image/jpeg'
    || !Number.isInteger(image.width) || !Number.isInteger(image.height)
    || image.width < 1 || image.height < 1 || image.width > 2048 || image.height > 2048
    || typeof image.data_url !== 'string'
    || !/^data:image\/jpeg;base64,[A-Za-z0-9+/]+={0,2}$/.test(image.data_url)) {
    throw new Error('截图格式无效，请重新捕获');
  }
  const encoded = image.data_url.slice('data:image/jpeg;base64,'.length);
  if (encoded.length > Math.ceil(2 * 1024 * 1024 / 3) * 4) throw new Error('截图超过 2 MiB，请重新捕获');
  let decoded;
  try { decoded = atob(encoded); } catch { throw new Error('截图数据无效，请重新捕获'); }
  if (!decoded.length || decoded.length > 2 * 1024 * 1024) throw new Error('截图大小无效，请重新捕获');
  const bytes = Uint8Array.from(decoded, char => char.charCodeAt(0));
  return new File([bytes], `gateway-${image.role}.jpg`, { type: 'image/jpeg' });
}

export function ownedScreenshotBlock(upload, image) {
  if (typeof upload?.file_key !== 'string' || !upload.file_key
    || typeof upload?.url !== 'string' || !/^\/uploads\/images\/[A-Za-z0-9_./-]+$/.test(upload.url)
    || upload.url !== `/uploads/images/${upload.file_key}`
    || !Number.isInteger(upload.size) || upload.size <= 0 || upload.size > 2 * 1024 * 1024
    || upload.url.includes('..') || (upload.mime_type && upload.mime_type !== 'image/jpeg')) {
    throw new Error('截图上传未返回有效的自有图片，请重试');
  }
  return { type: 'image', payload: { file_key: upload.file_key, url: upload.url,
    name: `gateway-${image.role}.jpg`, screenshot_role: image.role,
    size: upload.size, mime_type: 'image/jpeg', width: image.width, height: image.height } };
}
