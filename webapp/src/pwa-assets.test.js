import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { inflateSync } from 'node:zlib';
import { describe, expect, it } from 'vitest';

function paethPredictor(left, above, upperLeft) {
  const estimate = left + above - upperLeft;
  const leftDistance = Math.abs(estimate - left);
  const aboveDistance = Math.abs(estimate - above);
  const upperLeftDistance = Math.abs(estimate - upperLeft);
  if (leftDistance <= aboveDistance && leftDistance <= upperLeftDistance) return left;
  return aboveDistance <= upperLeftDistance ? above : upperLeft;
}

function decodeRGBA8PNG(file) {
  const png = readFileSync(file);
  expect(png.subarray(0, 8).toString('hex')).toBe('89504e470d0a1a0a');

  let width;
  let height;
  const idat = [];
  for (let offset = 8; offset < png.length;) {
    const length = png.readUInt32BE(offset);
    const type = png.subarray(offset + 4, offset + 8).toString('ascii');
    const data = png.subarray(offset + 8, offset + 8 + length);
    if (type === 'IHDR') {
      width = data.readUInt32BE(0);
      height = data.readUInt32BE(4);
      expect(data[8]).toBe(8);
      expect(data[9]).toBe(6);
      expect(data[12]).toBe(0);
    } else if (type === 'IDAT') {
      idat.push(data);
    }
    offset += length + 12;
  }

  const encoded = inflateSync(Buffer.concat(idat));
  const bytesPerPixel = 4;
  const stride = width * bytesPerPixel;
  const pixels = Buffer.alloc(stride * height);
  let sourceOffset = 0;
  let previous = Buffer.alloc(stride);

  for (let y = 0; y < height; y += 1) {
    const filter = encoded[sourceOffset];
    sourceOffset += 1;
    const row = Buffer.from(encoded.subarray(sourceOffset, sourceOffset + stride));
    sourceOffset += stride;
    for (let x = 0; x < stride; x += 1) {
      const left = x >= bytesPerPixel ? row[x - bytesPerPixel] : 0;
      const above = previous[x];
      const upperLeft = x >= bytesPerPixel ? previous[x - bytesPerPixel] : 0;
      if (filter === 1) row[x] = (row[x] + left) & 0xff;
      else if (filter === 2) row[x] = (row[x] + above) & 0xff;
      else if (filter === 3) row[x] = (row[x] + Math.floor((left + above) / 2)) & 0xff;
      else if (filter === 4) row[x] = (row[x] + paethPredictor(left, above, upperLeft)) & 0xff;
      else if (filter !== 0) throw new Error(`unsupported PNG filter ${filter}`);
    }
    row.copy(pixels, y * stride);
    previous = row;
  }
  return { width, height, pixels };
}

describe('PWA notification badge', () => {
  it('uses a transparent monochrome asset distinct from the launcher icon', () => {
    const badgePath = resolve(process.cwd(), 'public/pwa-notification-badge-96x96.png');
    const { width, height, pixels } = decodeRGBA8PNG(badgePath);
    const alphas = [];

    for (let offset = 0; offset < pixels.length; offset += 4) {
      const alpha = pixels[offset + 3];
      alphas.push(alpha);
    }
    expect([width, height]).toEqual([96, 96]);
    expect(Math.min(...alphas)).toBe(0);
    expect(Math.max(...alphas)).toBe(255);

    const serviceWorker = readFileSync(resolve(process.cwd(), 'src/sw.js'), 'utf8');
    const viteConfig = readFileSync(resolve(process.cwd(), 'vite.config.js'), 'utf8');
    expect(serviceWorker).toContain("badge: notification.badge || '/pwa-notification-badge-96x96.png'");
    expect(viteConfig).toContain("'pwa-notification-badge-96x96.png'");
  });
});
