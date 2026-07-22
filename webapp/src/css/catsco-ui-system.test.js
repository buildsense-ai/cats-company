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
    expect(ruleFor('.v3-sidebar.collapsed .v3-sidebar-collapse-btn .catsco-brand-mark'))
      .toContain('width: 34px;');
  });

  it('keeps sidebar chrome fixed while the navigation list owns overflow', () => {
    const headerRule = ruleFor('.v3-sidebar-header');
    const collapseButtonRule = ruleFor('.v3-sidebar-collapse-btn');
    const sidebarRule = ruleFor('.v3-sidebar');
    const toolsRule = ruleFor('.cc-sidebar-tools');
    const listRule = ruleFor('.v3-chat-list');
    const footerRule = ruleFor('.v3-profile-footer');

    expect(headerRule).toContain('flex: 0 0 56px;');
    expect(sidebarRule).toContain('font-family: Inter, "Segoe UI", "Microsoft YaHei UI", "Microsoft YaHei", DengXian, sans-serif;');
    expect(collapseButtonRule).toContain('width: 38px;');
    expect(collapseButtonRule).toContain('height: 38px;');
    expect(ruleFor('.v3-sidebar-collapse-btn > svg')).toContain('width: 20px;');
    expect(toolsRule).toContain('flex: 0 0 auto;');
    expect(listRule).toContain('min-height: 0;');
    expect(listRule).toContain('flex: 1 1 auto;');
    expect(listRule).toContain('overflow-y: auto;');
    expect(footerRule).toContain('flex: 0 0 auto;');
    expect(ruleFor('.v3-chat-item')).toContain('font-weight: 535;');
  });

  it('uses the requested chat surfaces without changing sidebar or border colors', () => {
    expect(ruleFor(':root')).toContain('--cc-main-bg: #f8f8f8;');
    expect(ruleFor(':root')).toContain('--cc-main-header-bg: #f8f8f8;');
    expect(ruleFor('html[data-theme="dark"]')).toContain('--cc-main-bg: #0f0f0f;');
    expect(ruleFor('html[data-theme="dark"]')).toContain('--cc-main-header-bg: #0f0f0f;');
    expect(css).toMatch(/\.v3-main,\s*\.v3-message-workspace,\s*\.v3-chat-column\s*\{[^}]*background: var\(--cc-main-bg\);/);
    expect(ruleFor('.v3-local-assistant-bar')).toContain('background: var(--cc-main-header-bg);');
    expect(ruleFor('.v3-timeline')).toContain('background: var(--cc-main-bg);');
    expect(ruleFor('.v3-sidebar')).toContain('background: var(--cc-bg);');
    expect(ruleFor('.v3-local-assistant-bar')).toContain('border-bottom: 0;');
  });

  it('aligns registration verification controls to the shared field grid', () => {
    const rowRule = ruleFor('.oc-auth-code-row');
    const inputRule = ruleFor('.oc-auth-code-row .oc-auth-input');
    const buttonRule = ruleFor('.oc-auth-code-row .oc-auth-btn');

    expect(rowRule).toContain('align-items: stretch;');
    expect(rowRule).toContain('margin-bottom: 12px;');
    expect(inputRule).toContain('margin: 0;');
    expect(buttonRule).toContain('height: 46px;');
    expect(buttonRule).toContain('margin: 0;');
  });

  it('centers the sidebar settings icon inside its hover surface', () => {
    const settingsRule = ruleFor('.v3-profile-settings');
    const iconRule = ruleFor('.v3-profile-settings > svg');

    expect(settingsRule).toContain('display: grid;');
    expect(settingsRule).toContain('place-items: center;');
    expect(settingsRule).toContain('width: 32px;');
    expect(settingsRule).toContain('height: 32px;');
    expect(settingsRule).toContain('padding: 0;');
    expect(settingsRule).toContain('line-height: 0;');
    expect(iconRule).toContain('display: block;');
  });

  it('tightens feedback upload copy and separates profile theme text', () => {
    const uploadRule = ruleFor('.oc-feedback-upload-button');
    const themeCopyRule = ruleFor('.oc-settings-theme-button .oc-settings-list-text');
    const themeLineRule = ruleFor('.oc-settings-theme-button .oc-settings-list-text > span');

    expect(uploadRule).toContain('gap: 5px;');
    expect(themeCopyRule).toContain('display: grid;');
    expect(themeCopyRule).toContain('gap: 2px;');
    expect(themeLineRule).toContain('display: block;');
    expect(themeLineRule).toContain('line-height: 1.35;');
  });

  it('places the empty-task brand mark beside the lighter greeting', () => {
    const headingRule = ruleFor('.cc-empty-task-heading');
    const markRule = ruleFor('.cc-empty-task-mark');
    const emptyTaskRule = ruleFor('.cc-empty-task');

    expect(ruleFor(':root')).toContain('--cc-empty-task-bg: #f8f8f8;');
    expect(ruleFor('html[data-theme="dark"]')).toContain('--cc-empty-task-bg: #0f0f0f;');
    expect(emptyTaskRule).toContain('background: var(--cc-empty-task-bg);');
    expect(headingRule).toContain('display: flex;');
    expect(headingRule).toContain('align-items: center;');
    expect(headingRule).toContain('justify-content: center;');
    expect(headingRule).toContain('gap: 18px;');
    expect(headingRule).toContain('margin-bottom: 28px;');
    expect(headingRule).toContain('transform: translateY(8px);');
    expect(markRule).toContain('width: 128px;');
    expect(markRule).toContain('height: 56px;');
    expect(markRule).toContain('margin: 0;');
    expect(ruleFor('.cc-empty-task h1')).toContain('margin: 0;');
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
    expect(ruleFor('.v3-peer-typing-label')).toContain('animation: cc-peer-typing-pulse 1200ms ease-in-out infinite;');
    expect(ruleFor('.v3-peer-typing-label')).toContain('will-change: opacity;');
    expect(css).toContain('@keyframes cc-peer-typing-pulse');
    expect(css).toContain('opacity: 0.52;');
    expect(css).toContain('opacity: 0.88;');
    expect(css).toContain('animation-duration: 2400ms;');
    expect(ruleFor('.v3-composer-box')).toContain('width: min(760px, 100%);');
  });

  it('uses a lighter self-message bubble in light mode and a deeper one in dark mode', () => {
    expect(ruleFor(':root')).toContain('--cc-self-message: #efeff0;');
    expect(ruleFor('html[data-theme="dark"]')).toContain('--cc-self-message: #303032;');
    expect(ruleFor('.v3-message.is-self .v3-message-bubble'))
      .toContain('background: var(--cc-self-message);');
  });

  it('uses the same solid online color for friend and task icons while keeping agents outlined', () => {
    const friendRule = ruleFor('.cc-contact-item .cc-friend-contact-icon.online');
    const taskRule = ruleFor('.cc-task-agent-icon.online');

    [friendRule, taskRule].forEach((rule) => {
      expect(rule).toContain('color: var(--cc-online-icon);');
      expect(rule).toContain('stroke: var(--cc-online-icon);');
      expect(rule).toContain('fill: var(--cc-online-icon);');
    });
    expect(friendRule).toContain('fill-opacity: 1;');
    expect(taskRule).toContain('fill-opacity: 1;');
    expect(taskRule).toContain('opacity: 1;');
    expect(ruleFor(':root')).toContain('--cc-online-icon: #5ea693;');
    expect(ruleFor('.cc-contact-item .cc-agent-contact-icon.online')).toContain('stroke: var(--cc-online-icon);');
    expect(ruleFor('.cc-contact-item .cc-agent-contact-icon.online')).toContain('fill: none;');
  });
});
