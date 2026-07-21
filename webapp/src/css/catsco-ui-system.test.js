import { existsSync, readFileSync, statSync } from 'node:fs';
import { resolve } from 'node:path';

const css = readFileSync(resolve(process.cwd(), 'src/css/catsco-ui-system.css'), 'utf8');
const brandAssetPath = resolve(process.cwd(), 'public/catsco-brand-mark.webp');

const ruleFor = (selector) => css.match(
  new RegExp(`(?:^|\\r?\\n)${selector.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}\\s*\\{[^}]*\\}`),
)?.[0] || '';

const readLosslessWebpDimensions = (buffer) => {
  if (
    buffer.subarray(0, 4).toString('ascii') !== 'RIFF'
    || buffer.subarray(8, 12).toString('ascii') !== 'WEBP'
    || buffer.subarray(12, 16).toString('ascii') !== 'VP8L'
    || buffer[20] !== 0x2f
  ) {
    throw new Error('Expected a lossless WebP brand asset');
  }
  const sizeBits = buffer.readUInt32LE(21);
  return {
    width: (sizeBits & 0x3fff) + 1,
    height: ((sizeBits >>> 14) & 0x3fff) + 1,
  };
};

describe('CatsCo shell styling', () => {
  it('uses the optimized formal brand asset wherever the shared mark is rendered', () => {
    const brandRule = ruleFor('.catsco-brand-mark');

    expect(existsSync(brandAssetPath)).toBe(true);
    expect(existsSync(resolve(process.cwd(), 'public/catsco-brand-asasda.png'))).toBe(false);
    expect(statSync(brandAssetPath).size).toBeLessThan(25_000);
    expect(readLosslessWebpDimensions(readFileSync(brandAssetPath))).toEqual({ width: 256, height: 96 });
    expect(brandRule).toContain('width: 48px;');
    expect(brandRule).toContain("url('/catsco-brand-mark.webp')");
    expect(brandRule).toContain('background');
    expect(brandRule).toContain('contain no-repeat');
    expect(brandRule).toContain('-webkit-mask: none;');
    expect(brandRule).toContain('mask: none;');
  });

  it('keeps sidebar chrome fixed while the navigation list owns overflow', () => {
    const headerRule = ruleFor('.v3-sidebar-header');
    const toolsRule = ruleFor('.cc-sidebar-tools');
    const listRule = ruleFor('.v3-chat-list');
    const footerRule = ruleFor('.v3-profile-footer');

    expect(headerRule).toContain('flex: 0 0 56px;');
    expect(toolsRule).toContain('flex: 0 0 auto;');
    expect(listRule).toContain('min-height: 0;');
    expect(listRule).toContain('flex: 1 1 auto;');
    expect(listRule).toContain('overflow-y: auto;');
    expect(footerRule).toContain('flex: 0 0 auto;');
  });

  it('uses a lighter heading weight on the empty task screen', () => {
    const markRule = ruleFor('.cc-empty-task-mark');

    expect(markRule).toContain('width: 128px;');
    expect(markRule).toContain('height: 56px;');
    expect(ruleFor('.cc-empty-task h1')).toContain('font-weight: 500;');
  });

  it('aligns peer messages and typing status to the unchanged composer rail', () => {
    expect(ruleFor('.v3-timeline')).toContain('padding: 18px 20px 140px;');
    expect(ruleFor('.v3-timeline-inner')).toContain('max-width: 760px;');
    expect(ruleFor('.v3-message.is-peer .v3-avatar-col')).toContain('margin-right: 10px;');
    expect(ruleFor('.v3-message.is-peer .v3-message-bubble')).toContain('padding: 8px 0 14px;');
    expect(ruleFor('.v3-message.is-peer .v3-message-footer')).toContain('padding: 0;');
    expect(ruleFor('.v3-peer-typing')).toContain('width: min(760px, 100%);');
    expect(ruleFor('.v3-peer-typing')).toContain('margin: 4px auto;');
    expect(ruleFor('.v3-peer-typing')).toContain('padding: 8px 0 14px;');
    expect(ruleFor('.v3-peer-typing-label')).toContain('font-weight: 400;');
    expect(ruleFor('.v3-peer-typing-label')).toContain('animation: cc-peer-typing-pulse 900ms ease-in-out infinite;');
    expect(ruleFor('.v3-peer-typing-label')).toContain('will-change: opacity;');
    expect(css).toContain('@keyframes cc-peer-typing-pulse');
    expect(css).toContain('animation-duration: 1800ms;');
    expect(ruleFor('.v3-composer-box')).toContain('width: min(760px, 100%);');
  });
});
