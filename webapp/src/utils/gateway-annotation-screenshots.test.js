import { describe, expect, test } from 'vitest';
import { screenshotFile, ownedScreenshotBlock, screenshotWarnings } from './gateway-annotation-screenshots';
const image = { role: 'full', mime_type: 'image/jpeg', data_url: 'data:image/jpeg;base64,/9j/2Q==', width: 800, height: 600 };
describe('UI screenshot owned upload conversion', () => {
  test('converts bounded app bytes to image File and frozen A image schema', () => {
    const file = screenshotFile(image);
    expect(file.name).toBe('gateway-full.jpg');
    expect(file.type).toBe('image/jpeg'); expect(file.size).toBe(4);
    expect(ownedScreenshotBlock({ file_key: 'full.jpg', url: '/uploads/images/full.jpg', size: 4, mime_type: 'image/jpeg' }, image)).toEqual({
      type: 'image', payload: { url: '/uploads/images/full.jpg', file_key: 'full.jpg', name: 'gateway-full.jpg',
        screenshot_role: 'full', size: 4, mime_type: 'image/jpeg', width: 800, height: 600 },
    });
  });
  test('rejects external URLs, mismatched key, nonowned/overbudget upload responses', () => {
    for (const patch of [{ url: 'https://other.test/full.jpg' }, { url: '/uploads/images/other.jpg' },
      { file_key: '' }, { size: 0 }, { size: 2 * 1024 * 1024 + 1 }, { mime_type: 'image/png' }]) {
      expect(() => ownedScreenshotBlock({ file_key: 'full.jpg', url: '/uploads/images/full.jpg', size: 4, ...patch }, image)).toThrow();
    }
  });
  test('rejects nonJPEG, invalid geometry/base64, oversized data before upload', () => {
    for (const patch of [{ mime_type: 'image/png' }, { width: 2049 }, { height: 0 }, { role: 'other' },
      { data_url: 'https://app.test/image.jpg' }, { data_url: 'data:image/jpeg;base64,!' },
      { data_url: `data:image/jpeg;base64,${'A'.repeat(3 * 1024 * 1024)}` }]) {
      expect(() => screenshotFile({ ...image, ...patch })).toThrow();
    }
  });
  test('translates SDK warning codes into deduplicated Chinese text', () => {
    expect(screenshotWarnings(['cross-origin-image', 'sensitive-content-masked', 'cross-origin-image'])).toHaveLength(2);
    expect(screenshotWarnings(['sensitive-content-masked'])[0]).toContain('敏感输入');
    expect(screenshotWarnings(['cross-origin-image'])[0]).not.toContain('cross-origin-image');
    expect(screenshotWarnings(null)).toEqual([]);
  });
});
