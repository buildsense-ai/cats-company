import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

const css = readFileSync(resolve(process.cwd(), 'src/css/catsco-secondary-surfaces.css'), 'utf8')
  .replace(/\r\n?/g, '\n');
const skillHubCss = readFileSync(resolve(process.cwd(), 'src/css/skillhub-view.css'), 'utf8')
  .replace(/\r\n?/g, '\n');
const workspaceStylesSource = readFileSync(resolve(process.cwd(), 'src/views/workspace-styles.js'), 'utf8')
  .replace(/\r\n?/g, '\n');
const tinodeWebSource = readFileSync(resolve(process.cwd(), 'src/views/tinode-web.jsx'), 'utf8')
  .replace(/\r\n?/g, '\n');
const desktopConnectSource = readFileSync(resolve(process.cwd(), 'src/widgets/desktop-connect-modal.jsx'), 'utf8')
  .replace(/\r\n?/g, '\n');

describe('secondary surface design contract', () => {
  it('loads the scoped layer after the shared settings controls', () => {
    const settingsIndex = workspaceStylesSource.indexOf("import '../css/catsco-settings-controls.css';");
    const secondaryIndex = workspaceStylesSource.indexOf("import '../css/catsco-secondary-surfaces.css';");

    expect(settingsIndex).toBeGreaterThanOrEqual(0);
    expect(secondaryIndex).toBeGreaterThan(settingsIndex);
  });

  it('uses surface contrast instead of borders for neutral actions', () => {
    expect(css).toContain('.cc-settings-secondary-surface :is(');
    expect(css).toContain('background: var(--cc-secondary-action-bg) !important;');
    expect(css).toContain('background: var(--cc-secondary-action-bg-hover) !important;');
    expect(css).toContain('background: var(--cc-secondary-action-bg-active) !important;');
    expect(css).toContain('border: 0 !important;');
  });

  it('keeps destructive actions semantic while sharing the neutral surface ramp', () => {
    expect(css).toContain('.relay-access-danger-action');
    expect(css).toContain('color: var(--cc-danger) !important;');
  });

  it('keeps input focus neutral and visibly contained', () => {
    expect(css).toContain('border-color: var(--cc-focus-border) !important;');
    expect(css).toContain('box-shadow: inset 0 0 0 1px var(--cc-focus-border) !important;');
  });

  it('removes raised liquid treatment inside account secondary surfaces', () => {
    expect(css).toContain("html[data-theme='liquid'] .cc-settings-secondary-surface");
    expect(css).toContain('box-shadow: none !important;');
    expect(css).toContain('transform: none !important;');
  });

  it('keeps profile logout and desktop downloads on the neutral interaction system', () => {
    const connectActionsSource = desktopConnectSource.match(
      /<div className="catsco-connect-actions">([\s\S]*?)<\/div>/,
    )?.[1] || '';

    expect(css).toMatch(/\.v3-profile-popover \.v3-popover-item\.danger\s*\{[^}]*margin-top: 0 !important;[^}]*border: 0 !important;[^}]*background: transparent !important;[^}]*color: var\(--cc-text-secondary\) !important;/s);
    expect(css).toMatch(/\.catsco-download-modal \.catsco-connect-actions\s*\{[^}]*grid-template-columns: repeat\(2, minmax\(0, 1fr\)\);/s);
    expect(connectActionsSource).not.toContain('<Laptop');
    expect(connectActionsSource).not.toContain('<Download');
    expect(css).toMatch(/\.catsco-download-modal \.catsco-connect-summary\s*\{[^}]*grid-template-columns: 32px minmax\(0, 1fr\);[^}]*align-items: center;/s);
    expect(css).toMatch(/\.catsco-download-modal \.catsco-download-more\s*\{[^}]*display: inline-flex;[^}]*align-items: center;[^}]*justify-content: center;[^}]*gap: 7px;/s);
    expect(css).toMatch(/\.catsco-download-modal \.catsco-download-card:not\(\.catsco-device-card\)\s*\{[^}]*grid-template-columns: 40px minmax\(0, 1fr\) 32px;/s);
    expect(css).toMatch(/\.catsco-download-modal \.catsco-download-icon\s*\{[^}]*background: transparent !important;[^}]*color: var\(--cc-text-secondary\) !important;/s);
    expect(css).toMatch(/\.cc-settings-secondary-surface\.catsco-download-modal \.catsco-download-card > \.catsco-download-action\s*\{[^}]*width: 32px !important;[^}]*height: 32px !important;[^}]*margin-right: -2px;[^}]*margin-left: auto;[^}]*border-radius: 8px;[^}]*background: transparent !important;/s);
    expect(css).toMatch(/\.catsco-download-modal \.catsco-download-card-primary,[^{]*\.catsco-download-card\.catsco-device-card\.is-preferred\s*\{[^}]*background: var\(--cc-selected\);[^}]*color: var\(--cc-text\);/s);
  });

  it('keeps the phone upload dialog opaque inside inherited empty-state layouts', () => {
    expect(css).toContain('background: var(--cc-main-bg);');
    expect(css).toContain('backdrop-filter: none;');
    expect(css).toContain('.v3-phone-upload-modal {');
    expect(css).toContain('background: var(--cc-panel) !important;');
    expect(css).toContain('text-align: left;');
    expect(css).toContain('.v3-phone-upload-body {');
  });

  it('keeps mobile interaction overrides scoped and touch friendly', () => {
    expect(css).toContain('@media (max-width: 768px)');
    expect(css).toContain('.v3-sidebar-desktop-collapse-btn {');
    expect(css).toMatch(/\.v3-sidebar-desktop-collapse-btn\s*\{[^}]*display:\s*none;/s);
    expect(css).toMatch(/\.v3-sidebar-header-search-btn,\s*\.v3-mobile-sidebar-toggle\s*\{[^}]*width:\s*44px;[^}]*height:\s*44px;/s);
    expect(css).toMatch(/\.v3-mobile-sidebar-toggle,\s*\.v3-mobile-sidebar-toggle:hover,\s*\.v3-mobile-sidebar-toggle:active\s*\{[^}]*top:\s*max\(10px, env\(safe-area-inset-top\)\);[^}]*border:\s*0;[^}]*background:\s*transparent;[^}]*box-shadow:\s*none;/s);
    expect(css).toMatch(/html\[data-theme='liquid'\] \.v3-mobile-sidebar-toggle,[^}]*background:\s*transparent;/s);
    expect(css).toMatch(/\.cc-sidebar-primary\s*\{[^}]*font-size:\s*16px;[^}]*font-weight:\s*500;[^}]*line-height:\s*23px;/s);
    expect(css).toMatch(/\.v3-chat-item\s*\{[^}]*font-size:\s*15px;[^}]*font-weight:\s*400;[^}]*line-height:\s*22px;/s);
    expect(css).toMatch(/\.v3-chat-item\.active\s*\{[^}]*font-weight:\s*500;/s);
    expect(css).toMatch(/\.v3-local-assistant-status \.v3-model-reasoning-strength\s*\{[^}]*display:\s*none;/s);
    expect(css).toMatch(/\.v3-profile-footer,\s*\.v3-profile-footer:hover,[^{]*html\[data-theme='liquid'\]\[data-liquid-variant='green'\] \.v3-profile-footer:hover\s*\{[^}]*border-top:\s*0;[^}]*background:\s*transparent;[^}]*backdrop-filter:\s*none;[^}]*box-shadow:\s*none;/s);
    expect(css).toContain('.v3-composer-row {');
    expect(css).toContain('gap: 6px;');
    expect(css).toContain('.v3-composer-row.is-empty:not(.has-stop) .v3-send');
    expect(css).toContain('.v3-composer-row.has-content .v3-voice-button');
    expect(css).toMatch(/\.v3-mobile-model-info\s*\{[^}]*display:\s*flex;/s);
    expect(css).toMatch(/\.v3-mobile-model-info\s*\{[^}]*min-height:\s*44px;/s);
    expect(css).toMatch(/\.v3-mobile-model-trigger\s*\{[^}]*touch-action:\s*manipulation;/s);
    expect(css).toMatch(/\.v3-mobile-model-visual\s*\{[^}]*width:\s*fit-content;[^}]*min-height:\s*32px;[^}]*transform:\s*translateY\(-8px\);/s);
    expect(css).toMatch(/\.v3-mobile-model-trigger\[aria-expanded='true'\] \.v3-mobile-model-visual\s*\{[^}]*background:\s*var\(--cc-hover\);/s);
    expect(css).toMatch(/\.v3-message-footer \.v3-action-btn\s*\{[^}]*width:\s*44px;[^}]*height:\s*44px;[^}]*touch-action:\s*manipulation;/s);
    expect(css).toMatch(/\.v3-message-footer \.v3-message-action-menu button\s*\{[^}]*min-height:\s*44px;[^}]*height:\s*44px;/s);
    expect(css).toContain('min-height: 96px;');
    expect(css).toContain('.v3-composer-model-info {');
    expect(css).toContain('display: flex;');
    expect(css).toContain('.v3-composer-model-quota.warning { color: var(--cc-warning-text); }');
    expect(css).toContain('.v3-composer-model-quota.danger { color: var(--cc-danger-text); }');
    expect(css).toContain('.v3-mobile-model-quota.warning { color: var(--cc-warning-text); }');
    expect(css).toContain('.v3-mobile-model-quota.danger { color: var(--cc-danger-text); }');
    expect(css).not.toContain('#f8d477');
    expect(css).not.toContain('#f2a0a0');
    expect(css).toContain('.v3-mobile-model-info {');
    expect(css).toMatch(/\.v3-shell-title,\s*\.v3-shell-title-input\s*\{/);
    expect(css).toContain('top: max(12px, env(safe-area-inset-top));');
    expect(css).toContain('max-width: min(40vw, 170px);');
    expect(css).toContain('width: fit-content;');
    expect(css).toContain('height: 24px;');
    expect(css).toContain('.v3-shell-title-button::before {');
    expect(css).toContain('inset: -10px -8px;');
    expect(css).toContain('--cc-mobile-sidebar-width: min(78vw, 320px);');
    expect(css).toContain('width: var(--cc-mobile-sidebar-width);');
    expect(css).toContain('transform: translate3d(var(--cc-mobile-sidebar-width), 0, 0);');
    expect(css).toContain('border-radius: 20px 0 0 20px;');
    expect(css).toContain('background: rgba(0, 0, 0, 0.3);');
    expect(css).not.toContain('scale(0.975)');
    expect(css).toContain('min-width: 0;');
    expect(css).toContain('max-width: min(40vw, 170px);');
    expect(css).toContain('field-sizing: content;');
    expect(css).toContain('font-size: 16px;');
    expect(css).toContain('font-size: 12px;');
    expect(css).toContain('align-items: flex-start;');
    expect(css).toContain('height: 44px;');
    expect(css).toContain('align-self: flex-start;');
    expect(css).toMatch(/\.v3-shell-actions\s*\{[^}]*top:\s*max\(10px, env\(safe-area-inset-top\)\);/s);
    expect(css).toContain('.v3-send {');
    expect(css).toContain('width: 46px;');
    expect(css).toContain('height: 46px;');
    expect(css).toContain('@media (max-width: 640px)');
    expect(css).toMatch(/\.v3-local-assistant-bar\s*\{\s*padding-inline:\s*max\(10px, env\(safe-area-inset-left\)\)\s+max\(10px, env\(safe-area-inset-right\)\);/s);
    expect(css).toContain('.relay-access-body {');
    expect(css).toContain('padding: 14px 16px calc(20px + env(safe-area-inset-bottom));');
    expect(css).toContain('min-height: 100dvh;');
    expect(css).toContain('background: var(--cc-panel, var(--v3-bg-app));');
    expect(css).toContain('@media (hover: none) and (pointer: coarse)');
    expect(css).toContain('.v3-tool-with-tooltip::after {');
  });

  it('uses an iPhone-scale mobile hierarchy for secondary surfaces and SkillHub', () => {
    expect(css).toMatch(/\.cc-settings-secondary-surface,\s*\.cc-new-task-dialog\s*\{[^}]*--cc-mobile-text-body:\s*16px;[^}]*--cc-mobile-text-secondary:\s*14px;[^}]*--cc-mobile-control-height:\s*48px;[^}]*--cc-mobile-row-height:\s*60px;/s);
    expect(css).toMatch(/\.oc-theme-option\s*\{[^}]*min-height:\s*78px;[^}]*border-radius:\s*0;/s);
    expect(css).toMatch(/\.oc-settings-list-button\s*\{[^}]*min-height:\s*78px;/s);
    expect(css).toMatch(/\.cc-new-task-agent\s*\{[^}]*min-height:\s*var\(--cc-mobile-row-height\);[^}]*font-size:\s*var\(--cc-mobile-text-body\);/s);
    expect(css).toMatch(/\.oc-profile-editor-overlay\s*\{[^}]*align-items:\s*flex-end\s*!important;[^}]*padding:\s*max\(62px, calc\(env\(safe-area-inset-top\) \+ 32px\)\) 0 0\s*!important;/s);
    expect(css).toMatch(/\.oc-modal\.oc-profile-editor-modal\s*\{[^}]*width:\s*100%\s*!important;[^}]*height:\s*100%\s*!important;[^}]*border-radius:\s*28px 28px 0 0\s*!important;/s);
    expect(css).toMatch(/\.oc-profile-mobile-group-card,\s*\.oc-profile-theme-section \.oc-theme-picker\s*\{[^}]*border-radius:\s*24px;/s);
    expect(css).toMatch(/\.oc-profile-mobile-home-card\s*\{[^}]*border-radius:\s*20px;[^}]*background:\s*var\(--cc-panel\);/s);
    expect(css).toMatch(/\.oc-profile-mobile-home-card > button,\s*\.oc-profile-mobile-logout\s*\{[^}]*min-height:\s*56px;/s);
    expect(css).toContain(".oc-profile-editor-modal[data-mobile-pane='home'] .oc-profile-mobile-home");
    expect(css).toContain(".oc-profile-editor-modal[data-mobile-pane='home'] .oc-profile-editor-actions");

    expect(skillHubCss).toContain('@media (max-width: 768px)');
    expect(skillHubCss).toMatch(/\.cc-skillhub-page\s*\{[^}]*--cc-mobile-text-body:\s*16px;[^}]*--cc-mobile-text-secondary:\s*14px;[^}]*--cc-mobile-control-height:\s*48px;[^}]*--cc-mobile-card-radius:\s*16px;/s);
    expect(skillHubCss).toMatch(/\.cc-skillhub-card,\s*\.cc-skillhub-local-card\s*\{[^}]*min-height:\s*188px;[^}]*padding:\s*var\(--cc-mobile-card-padding\);/s);
    expect(skillHubCss).toMatch(/\.cc-skillhub-added-actions button,[\s\S]*?min-height:\s*var\(--cc-mobile-control-height\);/s);
    expect(skillHubCss).toMatch(/\.cc-skillhub-action-menu button,[\s\S]*?min-height:\s*var\(--cc-mobile-control-height\);/s);
    expect(skillHubCss).toContain('.cc-skillhub-card-footer button');
  });

  it('keeps mobile sidebar controls distinct from desktop collapsing', () => {
    expect(tinodeWebSource).toContain('v3-sidebar-collapse-btn v3-sidebar-desktop-collapse-btn');
    expect(tinodeWebSource).toContain("aria-label={mobileSidebarOpen ? '关闭左侧栏' : '打开左侧栏'}");
    expect(tinodeWebSource).toContain('setMobileSidebarOpen((open) => !open);');
    expect(tinodeWebSource).toContain('? <PanelLeftClose size={18} aria-hidden="true" />');
    expect(tinodeWebSource).toContain(': <PanelLeftOpen size={18} aria-hidden="true" />}');
  });
});
