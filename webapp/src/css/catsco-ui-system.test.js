import { existsSync, readFileSync, statSync } from 'node:fs';
import { resolve } from 'node:path';

const css = readFileSync(resolve(process.cwd(), 'src/css/catsco-ui-system.css'), 'utf8')
  .replace(/\r\n?/g, '\n');
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

  it('defines a dark green liquid theme over the supplied landscape background', () => {
    const liquidRule = ruleFor('html[data-theme="liquid"]');
    const liquidGlassRule = ruleFor('html[data-theme="liquid"] :is(\n  .v3-sidebar,\n  .v3-local-assistant-bar,\n  .v3-profile-footer,\n  .v3-composer-box,\n  .v3-agent-picker-menu,\n  .v3-attachment-menu,\n  .v3-friend-action-menu,\n  .v3-profile-popover,\n  .name-dialog,\n  .oc-modal,\n  .settings-panel,\n  .collaboration-manager\n)');
    const liquidButtonRule = ruleFor('html[data-theme="liquid"] :is(\n  .v3-sidebar-collapse-btn,\n  .v3-action-btn,\n  .v3-tool,\n  .cc-section-add,\n  .v3-profile-settings,\n  .oc-btn-default,\n  .oc-modal-close,\n  .oc-profile-editor-close\n)');
    const liquidButtonHoverRule = ruleFor('html[data-theme="liquid"] :is(\n  .v3-sidebar-collapse-btn,\n  .v3-action-btn,\n  .v3-tool,\n  .cc-section-add,\n  .v3-profile-settings,\n  .oc-btn-default,\n  .oc-modal-close,\n  .oc-profile-editor-close\n):hover:not(:disabled)');
    const liquidButtonActiveRule = ruleFor('html[data-theme="liquid"] :is(\n  .v3-sidebar-collapse-btn,\n  .v3-action-btn,\n  .v3-tool,\n  .cc-section-add,\n  .v3-profile-settings,\n  .oc-btn-default,\n  .oc-modal-close,\n  .oc-profile-editor-close\n):active:not(:disabled)');

    expect(liquidRule).toContain('--cc-accent: #159b78;');
    expect(liquidRule).toContain('--cc-main-bg: transparent;');
    expect(liquidRule).toContain('color-scheme: dark;');
    expect(liquidRule).toContain('--cc-text: #ffffff;');
    expect(liquidRule).toContain('--cc-text-secondary: #ffffff;');
    expect(liquidRule).toContain('--cc-muted: #ffffff;');
    expect(liquidRule).toContain('--cc-liquid-violet: #8272d9;');
    expect(liquidRule).toContain('--cc-liquid-blue: #5a91d8;');
    expect(liquidGlassRule).toContain('backdrop-filter: blur(12px) saturate(118%);');
    expect(ruleFor('html[data-theme="liquid"] .oc-modal.oc-profile-editor-modal'))
      .toContain('background: #1a1c1d !important;');
    expect(ruleFor('html[data-theme="liquid"] body')).toContain("url('/liquid-dark-background.png')");
    expect(ruleFor('html[data-theme="liquid"] body')).toContain('linear-gradient(rgba(1, 8, 7, 0.86), rgba(1, 8, 7, 0.86))');
    expect(ruleFor('html[data-theme="liquid"] .v3-main')).toContain('linear-gradient(rgba(1, 8, 7, 0.86), rgba(1, 8, 7, 0.86))');
    expect(ruleFor('html[data-theme="liquid"] .v3-main')).toContain("url('/liquid-dark-background.png') center / cover no-repeat");
    expect(ruleFor('html[data-theme="liquid"] .v3-message-workspace')).toContain('background: transparent;');
    expect(ruleFor('html[data-theme="liquid"] .v3-chat-column')).toContain('background: transparent;');
    expect(ruleFor('html[data-theme="liquid"] .v3-timeline')).toContain('background: transparent;');
    expect(ruleFor('html[data-theme="liquid"] .cc-empty-task')).toContain('background: transparent;');
    expect(ruleFor('html[data-theme="liquid"] .v3-sidebar'))
      .toContain('linear-gradient(180deg, #151b19 0%, #111714 58%, #0f1513 100%)');
    expect(ruleFor('html[data-theme="liquid"] .v3-sidebar'))
      .toContain('border-right-color: rgba(184, 229, 216, 0.18);');
    expect(ruleFor('html[data-theme="liquid"] .v3-profile-footer'))
      .toContain('background: rgba(14, 20, 18, 0.76);');
    expect(ruleFor('html[data-theme="liquid"] .v3-profile-footer'))
      .toContain('backdrop-filter: none;');
    expect(liquidRule).toContain('--cc-liquid-edge: rgba(184, 229, 216, 0.2);');
    expect(liquidButtonRule).toContain('border: 1px solid var(--cc-liquid-edge);');
    expect(liquidButtonRule).toContain('inset 0 1px 0 rgba(255, 255, 255, 0.14)');
    expect(liquidButtonRule).toContain('inset 0 6px 8px -7px rgba(255, 255, 255, 0.34)');
    expect(liquidButtonRule).toContain('inset 1px 0 0 rgba(55, 190, 153, 0.06)');
    expect(liquidButtonRule).toContain('inset -1px 0 0 rgba(130, 114, 217, 0.05)');
    expect(liquidButtonHoverRule).toContain('inset 0 7px 9px -7px rgba(255, 255, 255, 0.44)');
    expect(liquidButtonHoverRule).toContain('transform: translateY(-1px);');
    expect(liquidButtonActiveRule).toContain('transform: translateY(0) scale(0.98);');
    expect(ruleFor('html[data-theme="liquid"] .v3-local-assistant-bar')).toContain('background: transparent;');
    expect(ruleFor('html[data-theme="liquid"] .v3-local-assistant-bar')).toContain('border-bottom: 0;');
    expect(ruleFor('html[data-theme="liquid"] .v3-local-assistant-bar')).toContain('backdrop-filter: none;');
    expect(ruleFor('html[data-theme="liquid"] .v3-local-assistant-bar')).toContain('box-shadow: none;');
    expect(ruleFor('html[data-theme="liquid"] .cc-sidebar-search input')).toContain('background: transparent;');
    expect(ruleFor('html[data-theme="liquid"] .cc-sidebar-search input')).toContain('box-shadow: none;');
    expect(css).toContain('@keyframes cc-liquid-drift-a');
    expect(css).toContain('@keyframes cc-liquid-drift-b');
    expect(css).toContain('@keyframes cc-liquid-main-flow');
  });

  it('presents theme choices and member-code unlocking as compact settings controls', () => {
    expect(ruleFor('.oc-theme-picker')).toContain('display: grid;');
    expect(ruleFor('.oc-theme-option')).toContain('grid-template-columns: 42px minmax(0, 1fr) 22px;');
    expect(ruleFor('.oc-theme-preview-liquid')).toContain('rgba(130, 114, 217, 0.4)');
    expect(ruleFor('.oc-liquid-unlock-row')).toContain('grid-template-columns: minmax(0, 1fr) auto;');
  });

  it('renders the liquid send control as a simple single-layer circle', () => {
    const sendRule = ruleFor('html[data-theme="liquid"] .v3-send');
    const sendDecorationRule = ruleFor('html[data-theme="liquid"] .v3-send::before,\nhtml[data-theme="liquid"] .v3-send::after');
    const sendHoverRule = ruleFor('html[data-theme="liquid"] .v3-send:hover:not(:disabled)');
    const sendActiveRule = ruleFor('html[data-theme="liquid"] .v3-send:active:not(:disabled)');
    const sendDisabledRule = ruleFor('html[data-theme="liquid"] .v3-send:disabled');

    expect(sendRule).toContain('border: 1px solid var(--cc-liquid-edge);');
    expect(sendRule).toContain('background: rgba(21, 155, 120, 0.28);');
    expect(sendRule).toContain('color: #a6e7d3;');
    expect(sendRule).not.toMatch(/0 0 0 \d+px/);
    expect(sendRule).toContain('0 1px 3px rgba(0, 9, 7, 0.26)');
    expect(sendRule).toContain('inset 0 1px 0 rgba(255, 255, 255, 0.22)');
    expect(sendRule).toContain('inset 1px 0 0 rgba(55, 190, 153, 0.05)');
    expect(sendRule).toContain('inset -1px 0 0 rgba(130, 114, 217, 0.04)');
    expect(sendRule).not.toContain('radial-gradient');
    expect(sendDecorationRule).toContain('display: none;');
    expect(sendHoverRule).toContain('inset 0 1px 0 rgba(255, 255, 255, 0.28)');
    expect(sendActiveRule).toContain('transform: translateY(0) scale(0.96);');
    expect(sendDisabledRule).toContain('background: rgba(255, 255, 255, 0.05);');
    expect(sendDisabledRule).toContain('color: #78918a;');
    expect(sendDisabledRule).toContain('inset 0 1px 0 rgba(255, 255, 255, 0.08)');
    expect(sendDisabledRule).toContain('opacity: 1;');
  });

  it('renders the liquid attachment control as a simple neutral circle', () => {
    const attachmentRule = ruleFor('html[data-theme="liquid"] .v3-composer-plus');
    const attachmentDecorationRule = ruleFor('html[data-theme="liquid"] .v3-composer-plus::before,\nhtml[data-theme="liquid"] .v3-composer-plus::after');
    const attachmentHoverRule = ruleFor('html[data-theme="liquid"] .v3-composer-plus:hover:not(:disabled),\nhtml[data-theme="liquid"] .v3-composer-plus[aria-expanded="true"]');
    const attachmentActiveRule = ruleFor('html[data-theme="liquid"] .v3-composer-plus:active:not(:disabled)');

    expect(attachmentRule).toContain('border: 1px solid var(--cc-liquid-edge);');
    expect(attachmentRule).toContain('background: rgba(255, 255, 255, 0.07);');
    expect(attachmentRule).toContain('color: #dce8e4;');
    expect(attachmentRule).toContain('0 1px 3px rgba(0, 9, 7, 0.24)');
    expect(attachmentRule).toContain('inset 0 1px 0 rgba(255, 255, 255, 0.22)');
    expect(attachmentRule).not.toContain('gradient');
    expect(attachmentDecorationRule).toContain('display: none;');
    expect(attachmentHoverRule).toContain('inset 0 1px 0 rgba(255, 255, 255, 0.28)');
    expect(attachmentActiveRule).toContain('transform: translateY(0) scale(0.96);');
  });

  it('keeps the dark liquid composer legible with restrained focus depth', () => {
    const composerRule = ruleFor('html[data-theme="liquid"] .v3-composer-box');
    const composerFocusRule = ruleFor('html[data-theme="liquid"] .v3-composer-box:focus-within');
    const composerInputRule = ruleFor('html[data-theme="liquid"] .v3-composer-input');

    expect(composerRule).toContain('background: #242627;');
    expect(composerRule).toContain('0 0 0 1px rgba(0, 10, 8, 0.32)');
    expect(composerRule).toContain('0 3px 7px rgba(0, 9, 7, 0.24)');
    expect(composerRule).toContain('inset 0 8px 12px -10px rgba(255, 255, 255, 0.36)');
    expect(composerFocusRule).toContain('linear-gradient(#242627, #242627) padding-box');
    expect(composerFocusRule).toContain('rgba(90, 145, 216, 0.5) 38%');
    expect(composerFocusRule).toContain('rgba(130, 114, 217, 0.52) 68%');
    expect(composerFocusRule).toContain('border-box;');
    expect(composerFocusRule).toContain('0 0 0 1px rgba(0, 10, 8, 0.3)');
    expect(composerFocusRule).toContain('inset 0 9px 13px -10px rgba(255, 255, 255, 0.42)');
    expect(composerFocusRule).toContain('transform: translateY(-1px);');
    expect(composerInputRule).toContain('background: transparent;');
    expect(composerInputRule).toContain('box-shadow: none;');
  });

  it('scopes the unified liquid control states away from the light and dark themes', () => {
    const primaryRule = ruleFor('html[data-theme="liquid"] :is(\n  .oc-btn-primary,\n  .oc-auth-btn,\n  .v3-custom-model-save,\n  .relay-access-primary-btn,\n  .v3-agent-request-action.primary\n)');
    const neutralRule = ruleFor('html[data-theme="liquid"] :is(\n  .oc-btn-default,\n  .v3-btn-secondary,\n  .cc-agent-empty-action,\n  .v3-model-status-button,\n  .v3-agent-picker-button,\n  .oc-settings-small-btn,\n  .catsco-download-action,\n  .relay-access-copy-btn,\n  .relay-access-open-btn,\n  .relay-access-key-actions button,\n  .relay-access-secret-box button\n)');
    const settingsRule = ruleFor('html[data-theme="liquid"] :is(.oc-settings-list-item, .oc-settings-list-button, .cc-new-task-agent)');
    const dangerRule = ruleFor('html[data-theme="liquid"] :is(.oc-btn-danger, button.danger, .mobile-channel-unlink-btn)');
    const focusRule = ruleFor('html[data-theme="liquid"] button:focus-visible');

    expect(primaryRule).toContain('background: rgba(21, 155, 120, 0.78) !important;');
    expect(primaryRule).toContain('inset 0 7px 10px -8px rgba(255, 255, 255, 0.38)');
    expect(neutralRule).toContain('border: 1px solid var(--cc-liquid-edge) !important;');
    expect(neutralRule).toContain('background: rgba(255, 255, 255, 0.07) !important;');
    expect(settingsRule).toContain('background: rgba(255, 255, 255, 0.055) !important;');
    expect(dangerRule).toContain('background: rgba(164, 46, 52, 0.16) !important;');
    expect(focusRule).toContain('outline: 2px solid rgba(130, 114, 217, 0.58);');
    expect(primaryRule).toContain('html[data-theme="liquid"] :is(');
    expect(neutralRule).toContain('html[data-theme="liquid"] :is(');
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

  it('layers the authentication flow behind the interactive card', () => {
    const authRule = ruleFor('.oc-auth');
    const flowRule = ruleFor('.oc-auth-flow-background');
    const cardRule = ruleFor('.oc-auth-card');
    const logoRule = ruleFor('.oc-auth-logo');

    expect(authRule).toContain('isolation: isolate;');
    expect(flowRule).toContain('position: fixed;');
    expect(flowRule).toContain('z-index: 0;');
    expect(flowRule).toContain('pointer-events: none;');
    expect(cardRule).toContain('z-index: 1;');
    expect(logoRule).toContain('color: var(--cc-accent);');
    expect(logoRule).toContain('font-weight: 750;');
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

  it('keeps friend requests visible and gives approval a consistent green interaction', () => {
    const panelRule = ruleFor('.v3-agent-request-panel');
    const actionRule = ruleFor('.v3-agent-request-action');
    const approvalHoverRule = ruleFor('.v3-agent-request-action.primary:hover:not(:disabled),\n.v3-agent-request-action.primary:focus-visible');
    const approvalActiveRule = ruleFor('.v3-agent-request-action.primary:active:not(:disabled)');

    expect(panelRule).toContain('flex: 0 0 auto;');
    expect(actionRule).toContain('flex: 0 0 26px;');
    expect(actionRule).toContain('transition: background-color 140ms ease');
    expect(approvalHoverRule).toContain('background: var(--cc-accent-hover);');
    expect(approvalHoverRule).toContain('filter: none;');
    expect(approvalActiveRule).toContain('transform: scale(0.94);');
    expect(approvalActiveRule).toContain('color: #fff;');
  });

  it('keeps the contact request count compact beside the more button', () => {
    const badgeRule = ruleFor('.cc-section-request-badge');

    expect(badgeRule).toContain('min-width: 16px;');
    expect(badgeRule).toContain('height: 16px;');
    expect(badgeRule).toContain('padding: 0 4px;');
    expect(badgeRule).toContain('margin-right: 2px;');
    expect(badgeRule).toContain('font-size: 10px;');
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
