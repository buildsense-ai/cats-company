import { describe, expect, test } from 'vitest';
import { formatUploadErrorMessage } from './upload-rules';

describe('formatUploadErrorMessage', () => {
  test('uses the server-reported limit for oversized uploads', () => {
    expect(formatUploadErrorMessage({
      code: 'upload_too_large',
      status: 413,
      message: 'file too large; maximum supported size is 300MB',
      data: { code: 'upload_too_large', error: 'file too large; maximum supported size is 300MB', max_size_mb: 300 },
    })).toBe('上传失败：文件超过 300MB 限制。');
  });

  test('follows the server-reported limit instead of a hardcoded default', () => {
    expect(formatUploadErrorMessage({
      status: 413,
      message: 'file too large',
      data: { max_size_mb: 500 },
    })).toBe('上传失败：文件超过 500MB 限制。');
  });

  test('describes a gateway rejection without claiming the product limit', () => {
    expect(formatUploadErrorMessage({
      code: 'upload_too_large',
      status: 413,
      message: 'Payload Too Large',
    })).toBe('上传失败：文件被服务器网关拒绝，可能超出上传链路限制，请压缩或拆分后重试。');
    expect(formatUploadErrorMessage({ message: '413 Request Entity Too Large' }))
      .toBe('上传失败：文件被服务器网关拒绝，可能超出上传链路限制，请压缩或拆分后重试。');
  });

  test('keeps the other upload failure mappings', () => {
    expect(formatUploadErrorMessage({ message: 'file too large; maximum supported size is 300MB' }))
      .toBe('上传失败：文件过大，超出服务器允许的大小，请压缩后重试。');
    expect(formatUploadErrorMessage({ message: 'invalid image type' }))
      .toBe('上传失败：当前仅支持 JPG、PNG、GIF、WebP 图片。');
    expect(formatUploadErrorMessage({ message: 'file type not allowed' }))
      .toBe('上传失败：该文件类型暂不支持。');
    expect(formatUploadErrorMessage({ message: 'Unexpected token < in JSON at position 0' }))
      .toBe('上传失败：服务器返回了无法识别的响应。');
    expect(formatUploadErrorMessage({ message: '上传连接中断，请检查网络后重试。' }))
      .toBe('上传失败：上传连接中断，请检查网络后重试。');
  });
});
