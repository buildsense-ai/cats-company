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
    expect(ruleFor('.v3-brand-title')).toContain('gap: 9px;');
    expect(ruleFor('.v3-brand-title')).toContain('font-size: 16px;');
    expect(ruleFor('.v3-brand-title')).toContain('font-weight: var(--cc-font-weight-bold);');
    expect(ruleFor('.v3-sidebar-header > .v3-brand-title')).toContain('gap: 4px;');
    expect(ruleFor('.v3-sidebar-header > .v3-brand-title')).toContain('font-size: 20px;');
    expect(ruleFor('.v3-sidebar-header > .v3-brand-title')).toContain('line-height: 22px;');
    expect(ruleFor('.v3-sidebar-header > .v3-brand-title'))
      .toContain('font-weight: var(--cc-font-weight-brand);');
    expect(ruleFor(':root')).toContain('--cc-brand-text-start: #29bc95;');
    expect(ruleFor(':root')).toContain('--cc-brand-text-end: #29bc95;');
    expect(ruleFor(':root')).toContain('--cc-accent: #29bc95;');
    expect(ruleFor(':root')).toContain('--oc-tab-active: #29bc95;');
    expect(ruleFor('html[data-theme="dark"]')).toContain('--cc-brand-text-start: #29bc95;');
    expect(ruleFor('html[data-theme="liquid"]')).toContain('--cc-brand-text-end: #7548cf;');
    expect(ruleFor('.v3-sidebar-header .catsco-brand-name')).toContain(
      'var(--cc-brand-text-start) 0%',
    );
    expect(ruleFor('.v3-sidebar-header .catsco-brand-name')).toContain('letter-spacing: 0.01em;');
    expect(ruleFor('.v3-sidebar-header .catsco-brand-name'))
      .toContain('text-shadow: none;');
    expect(css).toMatch(
      /@media \(forced-colors: active\)[\s\S]*?\.v3-sidebar-header \.catsco-brand-name\s*\{[^}]*-webkit-text-fill-color: currentColor;/,
    );
    expect(ruleFor('.v3-sidebar.collapsed .v3-sidebar-collapse-btn .catsco-brand-mark'))
      .toContain('width: 34px;');
    expect(ruleFor('html[data-theme="liquid"] .catsco-brand-mark'))
      .toContain('filter: hue-rotate(68deg) saturate(1.05) brightness(0.78);');
  });

  it('keeps sidebar chrome fixed while the navigation list owns overflow', () => {
    const headerRule = ruleFor('.v3-sidebar-header');
    const collapseButtonRule = ruleFor('.v3-sidebar-collapse-btn');
    const sidebarRule = ruleFor('.v3-sidebar');
    const toolsRule = ruleFor('.cc-sidebar-tools');
    const listRule = ruleFor('.v3-chat-list');
    const footerRule = ruleFor('.v3-profile-footer');

    expect(headerRule).toContain('flex: 0 0 56px;');
    expect(sidebarRule).toContain('font-family: var(--cc-font-sans);');
    expect(collapseButtonRule).toContain('width: 38px;');
    expect(collapseButtonRule).toContain('height: 38px;');
    expect(ruleFor('.v3-sidebar-collapse-btn > svg')).toContain('width: 20px;');
    expect(toolsRule).toContain('flex: 0 0 auto;');
    expect(listRule).toContain('min-height: 0;');
    expect(listRule).toContain('flex: 1 1 auto;');
    expect(listRule).toContain('overflow-y: auto;');
    expect(footerRule).toContain('flex: 0 0 auto;');
    expect(ruleFor('.v3-chat-item')).toContain('font-weight: 400;');
  });

  it('keeps sidebar metadata quiet and reveals its slim scrollbar on interaction', () => {
    const listRule = ruleFor('.v3-chat-list');
    const interactiveListRule = ruleFor('.v3-chat-list:hover,\n.v3-chat-list:focus-within');
    const scrollbarRule = ruleFor('.v3-chat-list::-webkit-scrollbar');
    const thumbRule = ruleFor('.v3-chat-list::-webkit-scrollbar-thumb');
    const interactiveThumbRule = ruleFor(
      '.v3-chat-list:hover::-webkit-scrollbar-thumb,\n.v3-chat-list:focus-within::-webkit-scrollbar-thumb,\n.v3-chat-list::-webkit-scrollbar-thumb:hover,\n.v3-chat-list::-webkit-scrollbar-thumb:active',
    );
    const collaborationRule = ruleFor('.cc-item-kind-agent');

    expect(ruleFor('.cc-chat-row-time')).toContain('var(--cc-muted) 76%');
    expect(collaborationRule).toContain('var(--cc-muted) 76%');
    expect(collaborationRule).toContain('var(--cc-muted) 7%');
    expect(listRule).toContain('var(--cc-muted) 18%');
    expect(interactiveListRule).toContain('var(--cc-muted) 42%');
    expect(scrollbarRule).toContain('width: 4px;');
    expect(scrollbarRule).toContain('height: 4px;');
    expect(thumbRule).toContain('border-radius: 999px;');
    expect(thumbRule).toContain('var(--cc-muted) 18%');
    expect(interactiveThumbRule).toContain('var(--cc-muted) 42%');
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

  it('defines a light blue-violet liquid theme with layered glass surfaces', () => {
    const liquidRule = ruleFor('html[data-theme="liquid"]');
    const liquidGlassRule = ruleFor('html[data-theme="liquid"] :is(\n  .v3-sidebar,\n  .v3-local-assistant-bar,\n  .v3-profile-footer,\n  .v3-composer-box,\n  .v3-agent-picker-menu,\n  .v3-attachment-menu,\n  .v3-friend-action-menu,\n  .v3-profile-popover,\n  .name-dialog,\n  .oc-modal,\n  .settings-panel,\n  .collaboration-manager\n)');
    const liquidButtonRule = ruleFor('html[data-theme="liquid"] :is(\n  .v3-sidebar-collapse-btn,\n  .v3-action-btn,\n  .v3-tool,\n  .cc-section-add,\n  .v3-profile-settings,\n  .oc-btn-default,\n  .oc-modal-close,\n  .oc-profile-editor-close\n)');
    const liquidButtonHoverRule = ruleFor('html[data-theme="liquid"] :is(\n  .v3-sidebar-collapse-btn,\n  .v3-action-btn,\n  .v3-tool,\n  .cc-section-add,\n  .v3-profile-settings,\n  .oc-btn-default,\n  .oc-modal-close,\n  .oc-profile-editor-close\n):hover:not(:disabled)');
    const liquidButtonActiveRule = ruleFor('html[data-theme="liquid"] :is(\n  .v3-sidebar-collapse-btn,\n  .v3-action-btn,\n  .v3-tool,\n  .cc-section-add,\n  .v3-profile-settings,\n  .oc-btn-default,\n  .oc-modal-close,\n  .oc-profile-editor-close\n):active:not(:disabled)');

    expect(liquidRule).toContain('--cc-accent: #5662d9;');
    expect(liquidRule).toContain('--cc-main-bg: transparent;');
    expect(liquidRule).toContain('color-scheme: light;');
    expect(liquidRule).toContain('--cc-text: #182033;');
    expect(liquidRule).toContain('--cc-text-secondary: #4f5b73;');
    expect(liquidRule).toContain('--cc-muted: #667085;');
    expect(liquidRule).toContain('--cc-liquid-violet: #7548cf;');
    expect(liquidRule).toContain('--cc-liquid-blue: #5662d9;');
    expect(liquidRule).toContain('--oc-green-light: #95aef4;');
    expect(liquidRule).toContain('--oc-tab-active: #5662d9;');
    expect(liquidGlassRule).toContain('backdrop-filter: blur(12px) saturate(118%);');
    expect(ruleFor('html[data-theme="liquid"] .oc-modal.oc-profile-editor-modal'))
      .toContain('background: rgba(255, 255, 255, 0.94) !important;');
    expect(ruleFor('html[data-theme="liquid"] body'))
      .toContain('radial-gradient(circle at 12% 4%, rgba(143, 181, 255, 0.3), transparent 32%)');
    expect(ruleFor('html[data-theme="liquid"] body'))
      .toContain('linear-gradient(135deg, #f9fbff 0%, #f4f7ff 52%, #f8f4ff 100%)');
    expect(ruleFor('html[data-theme="liquid"] body')).not.toContain('liquid-dark-background.png');
    expect(ruleFor('html[data-theme="liquid"] .v3-main'))
      .toContain('radial-gradient(circle at 86% 88%, rgba(180, 145, 255, 0.18), transparent 36%)');
    expect(ruleFor('html[data-theme="liquid"] .v3-main'))
      .toContain('linear-gradient(135deg, #f9fbff 0%, #f4f7ff 54%, #f8f4ff 100%)');
    expect(ruleFor('html[data-theme="liquid"] .v3-message-workspace')).toContain('background: transparent;');
    expect(ruleFor('html[data-theme="liquid"] .v3-chat-column')).toContain('background: transparent;');
    expect(ruleFor('html[data-theme="liquid"] .v3-timeline')).toContain('background: transparent;');
    expect(ruleFor('html[data-theme="liquid"] .cc-empty-task')).toContain('background: transparent;');
    expect(ruleFor('html[data-theme="liquid"] .v3-message.is-self .v3-message-bubble'))
      .toContain('background: rgba(255, 255, 255, 0.88);');
    expect(ruleFor('html[data-theme="liquid"] .v3-wpi-code-block.result pre'))
      .toContain('color: var(--cc-text-secondary);');
    expect(ruleFor('html[data-theme="liquid"] .v3-wpi-tool-header .oc-wpi-tool-input'))
      .toContain('opacity: 1 !important;');
    expect(ruleFor('html[data-theme="liquid"] .v3-sidebar'))
      .toContain('linear-gradient(180deg, rgba(255, 255, 255, 0.92) 0%, rgba(249, 251, 255, 0.88) 58%, rgba(246, 248, 255, 0.9) 100%)');
    expect(ruleFor('html[data-theme="liquid"] .v3-sidebar'))
      .toContain('border-right-color: rgba(73, 86, 168, 0.14);');
    expect(ruleFor('html[data-theme="liquid"] .v3-profile-footer'))
      .toContain('background: rgba(249, 251, 255, 0.9);');
    expect(ruleFor('html[data-theme="liquid"] .v3-profile-footer'))
      .toContain('backdrop-filter: none;');
    expect(liquidRule).toContain('--cc-liquid-edge: rgba(73, 86, 168, 0.18);');
    expect(liquidButtonRule).toContain('border: 1px solid var(--cc-liquid-edge);');
    expect(liquidButtonRule).toContain('inset 0 1px 0 rgba(255, 255, 255, 0.94)');
    expect(liquidButtonRule).toContain('inset 0 6px 8px -7px rgba(255, 255, 255, 0.92)');
    expect(liquidButtonRule).toContain('inset 1px 0 0 rgba(86, 98, 217, 0.07)');
    expect(liquidButtonRule).toContain('inset -1px 0 0 rgba(117, 72, 207, 0.06)');
    expect(liquidButtonHoverRule).toContain('inset 0 7px 9px -7px rgba(255, 255, 255, 0.96)');
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
    expect(ruleFor('.oc-theme-preview-liquid')).toContain('rgba(165, 128, 232, 0.68)');
    expect(ruleFor('.oc-liquid-unlock-row')).toContain('grid-template-columns: minmax(0, 1fr) auto;');
  });

  it('renders the liquid send control as a simple single-layer circle', () => {
    const sendRule = ruleFor('html[data-theme="liquid"] .v3-send');
    const sendDecorationRule = ruleFor('html[data-theme="liquid"] .v3-send::before,\nhtml[data-theme="liquid"] .v3-send::after');
    const sendHoverRule = ruleFor('html[data-theme="liquid"] .v3-send:hover:not(:disabled)');
    const sendActiveRule = ruleFor('html[data-theme="liquid"] .v3-send:active:not(:disabled)');
    const sendDisabledRule = ruleFor('html[data-theme="liquid"] .v3-send:disabled');

    expect(sendRule).toContain('border: 1px solid var(--cc-liquid-edge);');
    expect(sendRule).toContain('background: rgba(86, 98, 217, 0.16);');
    expect(sendRule).toContain('color: #4652c5;');
    expect(sendRule).not.toMatch(/0 0 0 \d+px/);
    expect(sendRule).toContain('0 2px 5px rgba(79, 91, 148, 0.12)');
    expect(sendRule).toContain('inset 0 1px 0 rgba(255, 255, 255, 0.96)');
    expect(sendRule).toContain('inset 1px 0 0 rgba(86, 98, 217, 0.07)');
    expect(sendRule).toContain('inset -1px 0 0 rgba(117, 72, 207, 0.05)');
    expect(sendRule).not.toContain('radial-gradient');
    expect(sendDecorationRule).toContain('display: none;');
    expect(sendHoverRule).toContain('inset 0 1px 0 rgba(255, 255, 255, 0.98)');
    expect(sendActiveRule).toContain('transform: translateY(0) scale(0.96);');
    expect(sendDisabledRule).toContain('background: rgba(255, 255, 255, 0.48);');
    expect(sendDisabledRule).toContain('color: #99a2b7;');
    expect(sendDisabledRule).toContain('inset 0 1px 0 rgba(255, 255, 255, 0.82)');
    expect(sendDisabledRule).toContain('opacity: 1;');
  });

  it('renders the liquid attachment control as a simple neutral circle', () => {
    const attachmentRule = ruleFor('html[data-theme="liquid"] .v3-composer-plus');
    const attachmentDecorationRule = ruleFor('html[data-theme="liquid"] .v3-composer-plus::before,\nhtml[data-theme="liquid"] .v3-composer-plus::after');
    const attachmentHoverRule = ruleFor('html[data-theme="liquid"] .v3-composer-plus:hover:not(:disabled),\nhtml[data-theme="liquid"] .v3-composer-plus[aria-expanded="true"]');
    const attachmentActiveRule = ruleFor('html[data-theme="liquid"] .v3-composer-plus:active:not(:disabled)');

    expect(attachmentRule).toContain('border: 1px solid var(--cc-liquid-edge);');
    expect(attachmentRule).toContain('background: rgba(255, 255, 255, 0.68);');
    expect(attachmentRule).toContain('color: #46506a;');
    expect(attachmentRule).toContain('0 2px 5px rgba(79, 91, 148, 0.11)');
    expect(attachmentRule).toContain('inset 0 1px 0 rgba(255, 255, 255, 0.96)');
    expect(attachmentRule).not.toContain('gradient');
    expect(attachmentDecorationRule).toContain('display: none;');
    expect(attachmentHoverRule).toContain('inset 0 1px 0 rgba(255, 255, 255, 0.98)');
    expect(attachmentActiveRule).toContain('transform: translateY(0) scale(0.96);');
  });

  it('keeps the light liquid composer legible with restrained focus depth', () => {
    const composerRule = ruleFor('html[data-theme="liquid"] .v3-composer-box');
    const composerFocusRule = ruleFor('html[data-theme="liquid"] .v3-composer-box:focus-within');
    const composerInputRule = ruleFor('html[data-theme="liquid"] .v3-composer-input');

    expect(composerRule).toContain('background: rgba(255, 255, 255, 0.84);');
    expect(composerRule).toContain('0 0 0 1px rgba(73, 86, 168, 0.05)');
    expect(composerRule).toContain('0 4px 12px rgba(79, 91, 148, 0.12)');
    expect(composerRule).toContain('inset 0 8px 12px -10px rgba(255, 255, 255, 0.96)');
    expect(composerFocusRule).toContain('linear-gradient(rgba(255, 255, 255, 0.9), rgba(255, 255, 255, 0.9)) padding-box');
    expect(composerFocusRule).toContain('rgba(108, 137, 232, 0.54) 38%');
    expect(composerFocusRule).toContain('rgba(117, 72, 207, 0.56) 68%');
    expect(composerFocusRule).toContain('border-box;');
    expect(composerFocusRule).toContain('0 0 0 1px rgba(73, 86, 168, 0.08)');
    expect(composerFocusRule).toContain('inset 0 9px 13px -10px rgba(255, 255, 255, 0.96)');
    expect(composerFocusRule).toContain('transform: translateY(-1px);');
    expect(composerInputRule).toContain('background: transparent;');
    expect(composerInputRule).toContain('box-shadow: none;');
  });

  it('keeps sidebar friend requests readable at the fixed sidebar width', () => {
    const requestRule = ruleFor('.cc-contact-requests .oc-friend-request');
    const infoRule = ruleFor('.cc-contact-requests .oc-friend-request-info');
    const actionsRule = ruleFor('.cc-contact-requests .oc-friend-request-actions');
    const buttonRule = ruleFor('.cc-contact-requests .oc-friend-request-actions .oc-btn');

    expect(requestRule).toContain('grid-template-columns: 40px minmax(0, 1fr);');
    expect(requestRule).toContain('"actions actions";');
    expect(infoRule).toContain('min-width: 0;');
    expect(actionsRule).toContain('grid-template-columns: repeat(2, minmax(0, 1fr));');
    expect(buttonRule).toContain('white-space: nowrap;');
  });

  it('scopes the unified liquid control states away from the light and dark themes', () => {
    const primaryRule = ruleFor('html[data-theme="liquid"] :is(\n  .oc-btn-primary,\n  .oc-auth-btn,\n  .v3-custom-model-save,\n  .relay-access-primary-btn,\n  .v3-agent-request-action.primary\n)');
    const neutralRule = ruleFor('html[data-theme="liquid"] :is(\n  .oc-btn-default,\n  .v3-btn-secondary,\n  .cc-agent-empty-action,\n  .v3-model-status-button,\n  .v3-agent-picker-button,\n  .oc-settings-small-btn,\n  .catsco-download-action,\n  .relay-access-copy-btn,\n  .relay-access-open-btn,\n  .relay-access-key-actions button,\n  .relay-access-secret-box button\n)');
    const settingsRule = ruleFor('html[data-theme="liquid"] :is(.oc-settings-list-item, .oc-settings-list-button, .cc-new-task-agent)');
    const dangerRule = ruleFor('html[data-theme="liquid"] :is(.oc-btn-danger, button.danger, .mobile-channel-unlink-btn)');
    const focusRule = ruleFor('html[data-theme="liquid"] button:focus-visible');

    expect(primaryRule).toContain('background: rgba(86, 98, 217, 0.94) !important;');
    expect(primaryRule).toContain('inset 0 7px 10px -8px rgba(255, 255, 255, 0.46)');
    expect(neutralRule).toContain('border: 1px solid var(--cc-liquid-edge) !important;');
    expect(neutralRule).toContain('background: rgba(255, 255, 255, 0.72) !important;');
    expect(settingsRule).toContain('background: rgba(255, 255, 255, 0.72) !important;');
    expect(dangerRule).toContain('background: rgba(218, 69, 79, 0.09) !important;');
    expect(focusRule).toContain('outline: 2px solid rgba(86, 98, 217, 0.62);');
    expect(primaryRule).toContain('html[data-theme="liquid"] :is(');
    expect(neutralRule).toContain('html[data-theme="liquid"] :is(');
    expect(ruleFor('html[data-theme="liquid"] :is(\n  .catsco-download-icon,\n  .relay-access-summary-icon\n)'))
      .toContain('background: rgba(86, 98, 217, 0.11);');
    expect(ruleFor('html[data-theme="liquid"] .relay-access-state:not(.inactive):not(.revoked)'))
      .toContain('background: rgba(86, 98, 217, 0.1);');
    expect(ruleFor('html[data-theme="liquid"] .relay-access-current-quota.active'))
      .toContain('background: rgba(86, 98, 217, 0.07);');
    expect(ruleFor('html[data-theme="liquid"] .relay-access-quota-bar i'))
      .toContain('linear-gradient(90deg, var(--cc-liquid-blue), var(--cc-liquid-violet))');
    expect(ruleFor('html[data-theme="liquid"] :is(\n  .v3-model-quota,\n  .v3-model-menu-item .v3-model-menu-quota\n):not(.warning):not(.danger):not(.muted)'))
      .toContain('color: var(--cc-liquid-blue);');
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
    expect(logoRule).toContain('font-weight: 700;');
  });

  it('centers the sidebar settings icon inside its hover surface', () => {
    const settingsRule = ruleFor('.v3-profile-settings');
    const iconRule = ruleFor('.v3-profile-settings > svg');

    expect(settingsRule).toContain('display: grid;');
    expect(settingsRule).toContain('place-items: center;');
    expect(settingsRule).toContain('width: 32px;');
    expect(settingsRule).toContain('height: 32px;');
    expect(settingsRule).toContain('padding: 0;');
    expect(settingsRule).toContain('border: 1px solid transparent;');
    expect(settingsRule).toContain('line-height: 0;');
    expect(iconRule).toContain('display: block;');
    expect(css).toContain('.v3-profile-footer:focus-visible .v3-profile-settings');
    expect(css).toContain('background: var(--cc-pressed);');
    expect(css).toContain('color: var(--cc-text);');
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

  it('keeps the contact request count at the right edge until the more button appears', () => {
    const badgeRule = ruleFor('.cc-section-request-badge');
    const menuRule = ruleFor('.cc-top-level-section > .cc-section-add');

    expect(badgeRule).toContain('min-width: 16px;');
    expect(badgeRule).toContain('height: 16px;');
    expect(badgeRule).toContain('padding: 0 4px;');
    expect(badgeRule).toContain('margin-left: auto;');
    expect(badgeRule).toContain('margin-right: 0;');
    expect(badgeRule).toContain('font-size: 10px;');
    expect(menuRule).toContain('position: absolute;');
    expect(menuRule).toContain('right: 4px;');
    expect(css).toContain('.cc-contacts-section:hover > .cc-section-request-badge');
    expect(css).toContain('transform: translateX(-30px);');
  });

  it('right-aligns every top-level action and keeps its chevron close to the title', () => {
    const sectionRule = ruleFor('.cc-top-level-section');
    const toggleRule = ruleFor('.cc-section-toggle');
    const actionRule = ruleFor('.cc-top-level-section > .cc-section-add');

    expect(sectionRule).toContain('position: relative;');
    expect(toggleRule).toContain('gap: 3px;');
    expect(actionRule).toContain('position: absolute;');
    expect(actionRule).toContain('right: 4px;');
  });

  it('aligns contact and project conversation actions with regular task actions', () => {
    const actionRule = ruleFor('.v3-chat-item .cc-chat-row-actions');
    const agentActionRule = ruleFor('.cc-agent-row-trailing .v3-agent-row-actions');
    const projectActionRule = ruleFor('.cc-project-menu-trigger');

    expect(actionRule).toContain('justify-content: flex-end;');
    expect(actionRule).toContain('right: -5px;');
    expect(agentActionRule).toContain('justify-content: flex-end;');
    expect(agentActionRule).toContain('gap: 2px;');
    expect(agentActionRule).toContain('right: -5px;');
    expect(projectActionRule).toContain('flex: 0 0 var(--cc-sidebar-action-size);');
    expect(css).not.toContain('.cc-history-item .cc-chat-row-actions');
  });

  it('makes top-level sidebar section titles distinct from expanded items', () => {
    expect(ruleFor('.cc-top-level-section')).toContain('font-weight: 600;');
    expect(ruleFor('.cc-history-section')).toContain('font-weight: 500;');
    expect(ruleFor('.v3-chat-item')).toContain('font-weight: 400;');
  });

  it('reveals top-level section actions without adding a row hover surface', () => {
    const actionRevealRule = ruleFor(
      `.v3-chat-section:hover > .cc-section-add,
.v3-chat-section:focus-within > .cc-section-add,
.cc-section-add:focus-visible`,
    );
    const nestedHoverRule = ruleFor('.v3-chat-section:not(.cc-top-level-section):hover');

    expect(actionRevealRule).toContain('opacity: 1;');
    expect(actionRevealRule).toContain('visibility: visible;');
    expect(nestedHoverRule).toContain('background: var(--cc-hover);');
    expect(css).not.toContain('.v3-chat-section:hover {\n  background: var(--cc-hover);');
  });

  it('stacks sidebar section titles while their content scrolls underneath', () => {
    const stickyRule = ruleFor(
      '.v3-chat-list > :is(.cc-contacts-section, .cc-project-section, .cc-conversation-section)',
    );
    const sectionGapRule = ruleFor('.v3-chat-list > .cc-section-after-expanded-content');
    const contactsRule = ruleFor('.v3-chat-list > .cc-contacts-section');
    const projectsRule = ruleFor('.v3-chat-list > .cc-project-section');
    const conversationsRule = ruleFor('.v3-chat-list > .cc-conversation-section');
    const compactProjectsRule = projectsRule.replace(/\s+/g, ' ');
    const compactConversationsRule = conversationsRule.replace(/\s+/g, ' ');

    expect(stickyRule).toContain('position: sticky;');
    expect(stickyRule).toContain('z-index: 12;');
    expect(stickyRule).toContain('border-radius: 0;');
    expect(stickyRule).toContain('background: var(--cc-bg);');
    expect(stickyRule).toContain(
      '0 calc(-1 * var(--cc-sidebar-row-gap)) 0 0 var(--cc-bg),',
    );
    expect(stickyRule).toContain(
      '0 var(--cc-sidebar-row-gap) 0 0 var(--cc-bg);',
    );
    expect(ruleFor('.v3-chat-list')).toContain('--cc-sidebar-group-gap: 12px;');
    expect(ruleFor('.v3-chat-list')).toContain('--cc-sidebar-list-padding-top: 8px;');
    expect(ruleFor('.v3-chat-list')).toContain('scrollbar-gutter: stable;');
    expect(sectionGapRule).toContain(
      'margin-top: calc(var(--cc-sidebar-group-gap) - var(--cc-sidebar-row-gap)) !important;',
    );
    expect(contactsRule).toContain('top: var(--cc-sidebar-row-gap);');
    expect(contactsRule).toContain('z-index: 14;');
    expect(contactsRule).toContain(
      '0 calc(-1 * var(--cc-sidebar-list-padding-top) - var(--cc-sidebar-row-gap)) 0 0 var(--cc-bg),',
    );
    expect(compactProjectsRule).toContain(
      'top: calc( var(--cc-sidebar-row-height) + var(--cc-sidebar-row-gap) + var(--cc-sidebar-row-gap) + var(--cc-sidebar-row-gap) );',
    );
    expect(projectsRule).toContain('z-index: 13;');
    expect(compactConversationsRule).toContain(
      'var(--cc-sidebar-row-height) + var(--cc-sidebar-row-height) + var(--cc-sidebar-row-gap) + var(--cc-sidebar-row-gap) + var(--cc-sidebar-row-gap) + var(--cc-sidebar-row-gap) + var(--cc-sidebar-row-gap)',
    );

    const liquidStickyRule = ruleFor(
      'html[data-theme="liquid"] .v3-chat-list > :is(.cc-contacts-section, .cc-project-section, .cc-conversation-section)',
    );
    expect(liquidStickyRule).toContain('background: rgb(249, 251, 255);');
    expect(liquidStickyRule).not.toContain('rgba(249, 251, 255, 0.96)');

    const liquidContactsRule = ruleFor(
      'html[data-theme="liquid"] .v3-chat-list > .cc-contacts-section',
    );
    expect(liquidContactsRule).toContain(
      '0 calc(-1 * var(--cc-sidebar-list-padding-top) - var(--cc-sidebar-row-gap)) 0 0 rgb(249, 251, 255),',
    );
  });

  it('uses the project row geometry for every sidebar hierarchy level', () => {
    const listRule = ruleFor('.v3-chat-list');
    const sectionRule = ruleFor('.v3-chat-section');
    const itemRule = ruleFor('.v3-chat-item');
    const historyRule = ruleFor('.cc-history-item');
    const projectRowRule = ruleFor('.cc-project-row');
    const projectItemRule = ruleFor('.cc-project-row > .cc-project-item');
    const projectTaskRule = ruleFor('.cc-project-task-item');
    const nestedItemRule = ruleFor('.cc-sidebar-item-level-2');
    const contactRule = ruleFor('.cc-contact-item');
    const compactContactRule = ruleFor(
      '.cc-contact-item[data-contact-kind="friend"],\n.cc-contact-item[data-contact-kind="agent"]',
    );

    expect(listRule).toContain('--cc-sidebar-row-height: 36px;');
    expect(listRule).toContain('--cc-sidebar-row-gap: 1px;');
    expect(listRule).toContain('--cc-sidebar-row-padding-y: 6px;');
    expect(listRule).toContain('--cc-sidebar-action-size: 28px;');
    expect(sectionRule).toContain('min-height: var(--cc-sidebar-row-height);');
    expect(sectionRule).toContain('line-height: 20px;');
    expect(itemRule).toContain('min-height: var(--cc-sidebar-row-height);');
    expect(itemRule).toContain('margin: var(--cc-sidebar-row-gap) 0;');
    expect(historyRule).toContain('min-height: var(--cc-sidebar-row-height);');
    expect(historyRule).toContain('padding-top: var(--cc-sidebar-row-padding-y);');
    expect(historyRule).toContain('padding-bottom: var(--cc-sidebar-row-padding-y);');
    expect(projectRowRule).toContain('position: relative;');
    expect(projectItemRule).toContain('margin: 0;');
    expect(projectTaskRule).toContain('width: 100%;');
    expect(nestedItemRule).toContain('padding-left: 28px;');
    expect(contactRule).toContain('min-height: var(--cc-sidebar-row-height);');
    expect(compactContactRule).toContain('min-height: var(--cc-sidebar-row-height);');
    expect(compactContactRule).toContain('padding-top: var(--cc-sidebar-row-padding-y);');
    expect(compactContactRule).toContain('padding-bottom: var(--cc-sidebar-row-padding-y);');
  });

  it('keeps the selected project-task checkmark visible against its accent fill', () => {
    expect(ruleFor('.cc-project-task-selection-indicator svg')).toContain('color: currentColor;');
    expect(ruleFor('.cc-project-task-option.is-selected .cc-project-task-selection-indicator svg')).toContain('color: #fff;');
  });

  it('keeps project selection controls inside their scroll rail', () => {
    const listRule = ruleFor('.cc-new-task-agent-list');
    const optionRule = ruleFor('.cc-new-task-agent');

    expect(listRule).toContain('min-width: 0;');
    expect(listRule).toContain('overflow-x: hidden;');
    expect(listRule).toContain('scrollbar-gutter: stable;');
    expect(optionRule).toContain('box-sizing: border-box;');
    expect(optionRule).toContain('min-width: 0;');
    expect(optionRule).toContain('overflow: hidden;');
  });

  it('keeps group member actions on one desktop row without overlapping members', () => {
    const memberRule = ruleFor('.oc-group-settings-modal .oc-settings-member-item');
    const actionsRule = ruleFor('.oc-group-settings-modal .oc-settings-member-actions');

    expect(memberRule).toContain('grid-template-columns: 32px minmax(0, 1fr) auto;');
    expect(memberRule).toContain('align-items: center;');
    expect(actionsRule).toContain('max-width: none;');
    expect(actionsRule).toContain('flex-wrap: nowrap;');
    expect(css).toContain('@media (max-width: 640px)');
    expect(css).toContain('grid-column: 2;');
  });

  it('shows the narrow-screen sidebar shadow only while the drawer is open', () => {
    expect(css).toContain('.v3-sidebar:not(.collapsed) {\n    position: fixed;');
    expect(css).toContain('flex-basis: min(86vw, 300px);\n    box-shadow: none;');
    expect(css).toContain('.v3-sidebar:not(.collapsed).open {\n    box-shadow: 16px 0 40px rgba(0, 0, 0, 0.34);');
  });

  it('zooms compact task artwork without resizing its button and shows a fixed label', () => {
    const buttonRule = ruleFor('.cc-compact-new-chat,\n.cc-compact-conversation');
    const avatarRule = ruleFor('.cc-compact-conversation .oc-avatar');
    const hintRule = ruleFor('.cc-compact-task-hint');

    expect(buttonRule).toContain('width: 42px;');
    expect(buttonRule).toContain('height: 42px;');
    expect(avatarRule).toContain('transition: transform 150ms ease;');
    expect(css).toContain('transform: scale(1.14);');
    expect(hintRule).toContain('position: fixed;');
    expect(hintRule).toContain('pointer-events: none;');
  });

  it('overlays compact task status on the avatar without changing rail geometry', () => {
    const compactButtonRule = ruleFor('.cc-compact-conversation');
    const dotRule = ruleFor('.cc-compact-task-status:not(.running)');
    const runningRule = ruleFor('.cc-compact-task-status.running');
    const spinnerRule = ruleFor('.cc-compact-task-status.running svg');

    expect(compactButtonRule).toContain('position: relative;');
    expect(css).toContain('.cc-compact-conversation {\n  overflow: hidden;');
    expect(dotRule).toContain('right: 3px;');
    expect(dotRule).toContain('bottom: 3px;');
    expect(dotRule).toContain('width: 9px;');
    expect(dotRule).toContain('box-shadow: 0 0 0 2px var(--cc-bg);');
    expect(runningRule).toContain('inset: 0;');
    expect(runningRule).toContain('justify-content: center;');
    expect(spinnerRule).toContain('animation: catsco-spin 0.9s linear infinite;');
    expect(spinnerRule).not.toContain('filter:');
    expect(ruleFor('.cc-compact-task-status.completed')).toContain('color: var(--v3-primary);');
    expect(ruleFor('.cc-compact-task-status.failed')).toContain('color: #ef5b5b;');
    expect(css).toContain('.cc-compact-task-status.cancelled,\n.cc-compact-task-status.stale');
    expect(css).toContain('.cc-task-row-status.running svg,\n  .cc-compact-task-status.running svg');
  });

  it('zooms the collapsed profile avatar without changing the footer frame', () => {
    const footerRule = ruleFor('.v3-sidebar.collapsed .v3-profile-footer');
    const avatarRule = ruleFor('.v3-sidebar.collapsed .v3-profile-avatar.oc-avatar');

    expect(footerRule).toContain('width: 100%;');
    expect(footerRule).toContain('min-height: 64px;');
    expect(avatarRule).toContain('transition: transform 150ms ease;');
    expect(css).toContain('.v3-sidebar.collapsed .v3-profile-footer:hover .v3-profile-avatar.oc-avatar');
    expect(css).toContain('transform: scale(1.14);');
  });

  it('positions the collapsed profile menu beside the rail without widening it', () => {
    const popoverRule = ruleFor('.v3-profile-popover');
    const compactRule = ruleFor('.v3-profile-popover.is-compact');

    expect(popoverRule).toContain('position: fixed;');
    expect(compactRule).toContain('left: calc(var(--cc-sidebar-collapsed) + 8px);');
    expect(compactRule).toContain('bottom: 12px;');
    expect(compactRule).toContain('width: min(220px, calc(100vw - var(--cc-sidebar-collapsed) - 20px));');
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
    expect(headingRule).toContain('align-items: flex-end;');
    expect(headingRule).toContain('justify-content: center;');
    expect(headingRule).toContain('gap: 16px;');
    expect(headingRule).toContain('margin-bottom: 24px;');
    expect(headingRule).not.toContain('transform:');
    expect(markRule).toContain('width: 112px;');
    expect(markRule).toContain('height: 48px;');
    expect(markRule).toContain('margin: 0;');
    expect(ruleFor('.cc-empty-task h1')).toContain('margin: 0;');
    expect(ruleFor('.cc-empty-task h1')).toContain('font-size: 28px;');
    expect(ruleFor('.cc-empty-task h1')).toContain('font-weight: 500;');
    expect(ruleFor('.cc-empty-task h1')).toContain('line-height: 36px;');
  });

  it('uses integer line heights for the primary desktop text rails', () => {
    expect(ruleFor('.cc-sidebar-search input')).toContain('font-size: 14px;');
    expect(ruleFor('.cc-sidebar-search input')).toContain('line-height: 20px;');
    expect(ruleFor('.v3-chat-section')).toContain('font-size: 14px;');
    expect(ruleFor('.v3-chat-section')).toContain('line-height: 20px;');
    expect(ruleFor('.v3-chat-item')).toContain('font-size: 13px;');
    expect(ruleFor('.v3-chat-item')).toContain('line-height: 19px;');
    expect(ruleFor('.v3-chat-item-identity')).toContain('font-size: 12px;');
    expect(ruleFor('.v3-chat-item-identity')).toContain('line-height: 18px;');
    expect(ruleFor('.v3-composer-input')).toContain('font-size: 15px;');
    expect(ruleFor('.v3-composer-input')).toContain('line-height: 22px;');
    expect(ruleFor('.v3-composer-hint')).toContain('line-height: 18px;');
  });

  it('aligns peer messages and typing status to the unchanged composer rail', () => {
    const stackedNoticeRule = ruleFor('.v3-composer-notices .v3-live-input-status:last-child');

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
    expect(stackedNoticeRule).toContain('width: 85%;');
    expect(stackedNoticeRule).toContain('margin: 0 auto -11px;');
    expect(stackedNoticeRule).toContain('border-radius: 22px 22px 0 0;');
    expect(stackedNoticeRule).toContain('padding: 8px 14px 11px;');
    expect(stackedNoticeRule).toContain('text-align: center;');
    expect(ruleFor('.v3-composer-notices + .v3-composer-box')).toContain('z-index: 1;');
    expect(ruleFor('.oc-reply-bar')).toContain('width: min(646px, calc(85% - 34px)) !important;');
    expect(ruleFor('.oc-reply-bar')).toContain('border-radius: 22px 22px 0 0 !important;');
    expect(ruleFor('.oc-reply-bar')).toContain('background: color-mix(in srgb, var(--cc-accent) 5%, var(--cc-panel)) !important;');
    expect(ruleFor('.oc-reply-bar')).toContain('font-size: 12px !important;');
    expect(css).toContain('.oc-reply-bar .oc-reply-bar-close {\n  width: 24px;\n  height: 18px;');
    expect(ruleFor('.oc-reply-bar + .v3-composer')).toContain('z-index: 1;');
    expect(ruleFor('.v3-attachment-notice')).toContain('justify-content: center;');
    expect(ruleFor('.v3-attachment-notice > span')).toContain('text-overflow: ellipsis;');
    expect(ruleFor('.v3-composer-attachment-tray')).toContain('overflow-x: auto;');
    expect(ruleFor('.v3-composer-attachment-chip')).toContain('height: 56px;');
    expect(ruleFor('.v3-composer-attachment-chip.is-image')).toContain('width: 56px;');
    expect(ruleFor('.v3-composer-attachment-remove')).toContain('position: absolute;');
    expect(ruleFor('.v3-composer-attachment-preview')).toContain('cursor: zoom-in;');
    expect(ruleFor('.v3-composer-image-preview-backdrop')).toContain('position: fixed;');
    expect(css).not.toContain('.v3-composer-attachments {');
  });

  it('uses a lighter self-message bubble in light mode and a deeper one in dark mode', () => {
    expect(ruleFor(':root')).toContain('--cc-self-message: #efeff0;');
    expect(ruleFor('html[data-theme="dark"]')).toContain('--cc-self-message: #303032;');
    expect(ruleFor('.v3-message.is-self .v3-message-bubble'))
      .toContain('background: var(--cc-self-message);');
  });

  it('renders sent files as a compact neutral card', () => {
    const cardRule = ruleFor('.v3-message .v3-attachment-card.v3-artifact-card');
    const iconRule = ruleFor('.v3-message .v3-attachment-icon');
    const actionRule = ruleFor('.v3-message .v3-artifact-action');

    expect(cardRule).toContain('width: min(390px, 100%);');
    expect(cardRule).toContain('background: var(--cc-panel);');
    expect(cardRule).toContain('border: 1px solid var(--cc-border);');
    expect(iconRule).toContain('width: 30px;');
    expect(iconRule).toContain('height: 30px;');
    expect(actionRule).toContain('height: 28px;');
    expect(actionRule).toContain('background: transparent;');
    expect(css).toContain('.v3-message.is-self.has-file-only .v3-message-bubble');
  });

  it('presents confirmations as a compact neutral dialog with a clear primary action', () => {
    const overlayRule = ruleFor('.cc-confirm-overlay');
    const dialogRule = ruleFor('.oc-modal.cc-confirm-dialog');
    const cancelRule = ruleFor('.cc-confirm-cancel');
    const submitRule = ruleFor('.cc-confirm-submit');
    const liquidDialogRule = ruleFor('html[data-theme="liquid"] .cc-confirm-dialog');

    expect(overlayRule).toContain('background: rgba(0, 0, 0, 0.72);');
    expect(dialogRule).toContain('width: min(420px, calc(100vw - 32px));');
    expect(dialogRule).toContain('border-radius: 16px !important;');
    expect(dialogRule).toContain('border: 1px solid var(--cc-border) !important;');
    expect(dialogRule).toContain('background: var(--cc-panel) !important;');
    expect(cancelRule).toContain('background: var(--cc-panel-raised);');
    expect(cancelRule).toContain('border: 1px solid var(--cc-border-strong);');
    expect(submitRule).toContain('background: var(--cc-accent);');
    expect(ruleFor('.cc-confirm-submit.is-danger'))
      .toContain('background: color-mix(in srgb, var(--cc-danger) 16%, var(--cc-panel));');
    expect(liquidDialogRule).toContain('border-color: rgba(112, 119, 139, 0.22) !important;');
    expect(liquidDialogRule).toContain('background: rgba(255, 255, 255, 0.96) !important;');
    expect(liquidDialogRule).toContain('saturate(100%)');
    expect(css).toContain('@keyframes cc-confirm-enter');
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
    expect(ruleFor(':root')).toContain('--cc-online-icon: #29bc95;');
    expect(ruleFor('html[data-theme="liquid"]')).toContain('--cc-online-icon: #5662d9;');
    expect(ruleFor('html[data-theme="liquid"]')).toContain('--cc-offline-icon: #98a2b7;');
    expect(ruleFor('.cc-contact-item .cc-agent-contact-icon.online')).toContain('stroke: var(--cc-online-icon);');
    expect(ruleFor('.cc-contact-item .cc-agent-contact-icon.online')).toContain('fill: none;');
    const liquidOfflineRule = ruleFor('html[data-theme="liquid"] :is(\n  .cc-friend-contact-icon.offline,\n  .cc-agent-contact-icon.offline,\n  .cc-task-agent-icon.offline\n)');
    expect(liquidOfflineRule).toContain('color: var(--cc-offline-icon);');
    expect(liquidOfflineRule).toContain('stroke: var(--cc-offline-icon);');
    expect(liquidOfflineRule).toContain('fill: none;');
    expect(liquidOfflineRule).toContain('opacity: 1;');
  });

  it('keeps the member search surface unified and clearly marks the active member type', () => {
    const searchInputRule = ruleFor('.oc-member-search input');
    const searchFocusRule = ruleFor('.oc-member-search input:focus,\n.oc-member-search input:focus-visible');
    const activeTypeRule = ruleFor('.oc-member-search .oc-segmented-control button.active');
    const groupNameFocusRule = ruleFor('.oc-create-group-dialog .oc-collaboration-input:focus-visible');

    expect(searchInputRule).toContain('background: transparent !important;');
    expect(searchInputRule).toContain('box-shadow: none !important;');
    expect(searchFocusRule).toContain('background: transparent !important;');
    expect(activeTypeRule).toContain('var(--cc-accent)');
    expect(activeTypeRule).toContain('color: #fff;');
    expect(groupNameFocusRule).toContain('outline: 0;');
    expect(groupNameFocusRule).toContain('box-shadow: none;');
  });

  it('centers the assistant role chevron against the select control itself', () => {
    expect(ruleFor('.cc-agent-role-field')).toContain('position: relative;');
    expect(ruleFor('.cc-agent-basic-card .cc-agent-role-select')).toContain('margin-bottom: 0;');
    expect(ruleFor('.cc-agent-role-chevron')).toContain('top: 50%;');
    expect(ruleFor('.cc-agent-role-chevron')).toContain('transform: translateY(-50%);');
  });

  it('uses the existing narrow preview breakpoint and keeps the file preview sheet inside short viewports', () => {
    expect(css).toContain('@media (max-width: 1024px) {');
    expect(css).not.toContain('@container catsco-main (max-width: 719px)');
    expect(css).toContain('grid-template-rows: minmax(0, 1fr);');
    expect(css).toContain('height: min(68svh, 620px, calc(100svh - max(76px, env(safe-area-inset-top)) - max(10px, env(safe-area-inset-bottom))));');
    expect(css).not.toContain('min-height: 380px;');
  });

  it('removes file preview sheet dismissal transitions for reduced motion', () => {
    expect(css).toContain(`@media (prefers-reduced-motion: reduce) {
    .v3-file-preview-backdrop,
    .v3-file-preview-panel,
    .v3-file-preview-panel.is-dismissing {
      transition: none;
    }`);
  });

});
