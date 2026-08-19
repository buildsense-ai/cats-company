import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

const css = readFileSync(resolve(process.cwd(), 'src/css/catsco-liquid-green.css'), 'utf8')
  .replace(/\r\n?/g, '\n');

describe('restored green liquid theme', () => {
  it('keeps the original dark green material as an independent liquid variant', () => {
    expect(css).toContain('html[data-theme="liquid"][data-liquid-variant="green"]');
    expect(css).toContain('color-scheme: dark;');
    expect(css).toContain('--cc-accent: #29bc95;');
    expect(css).toContain('--cc-brand-text-start: #29bc95;');
    expect(css).toContain('--cc-brand-text-end: #29bc95;');
    expect(css).toContain('--cc-online-icon: #29bc95;');
    expect(css).toContain('--cc-offline-icon: #7f8b88;');
    expect(css).toContain('--cc-liquid-blue: #29bc95;');
    expect(css).toContain('--cc-liquid-violet: #29bc95;');
    expect(css).toContain('--cc-border: rgba(184, 229, 216, 0.12);');
    expect(css).toContain('--cc-border-strong: rgba(198, 240, 228, 0.22);');
    expect(css).toContain('--cc-control-surface: #252829;');
    expect(css).toContain('--cc-input-surface: rgba(255, 255, 255, 0.07);');
    expect(css).toContain('--cc-liquid-edge: rgba(184, 229, 216, 0.12);');
    expect(css).toContain('--cc-focus-ring: #69d7b7;');
    expect(css).toMatch(
      /html\[data-theme="liquid"\]\[data-liquid-variant="green"\] \.catsco-brand-mark\s*\{[^}]*background: #29bc95;[^}]*mask: url\('\/catsco-brand-mark\.webp'\)[^}]*filter: none;/,
    );
    expect(css).toMatch(
      /html\[data-theme="liquid"\]\[data-liquid-variant="green"\] \.v3-sidebar\.collapsed \.v3-sidebar-collapse-btn,[\s\S]*?background: transparent;[\s\S]*?box-shadow: none;/,
    );
    expect(css).toMatch(
      /html\[data-theme="liquid"\]\[data-liquid-variant="green"\] \.relay-access-current-quota\.active\s*\{[^}]*background: rgba\(41, 188, 149, 0\.08\);/,
    );
    expect(css).toMatch(/\.relay-access-quota-bar i\s*\{[^}]*background: #29bc95;/);
    expect(css).toMatch(
      /\.catsco-download-release-list\s+\.catsco-download-card > \.catsco-download-action\s*\{[^}]*width: 36px;[^}]*height: 36px;[^}]*border-radius: 10px;/,
    );
    expect(css).toMatch(
      /html\[data-theme="liquid"\]\[data-liquid-variant="green"\] \.v3-send\s*\{[^}]*color: #29bc95;/,
    );
    expect(css).toMatch(
      /html\[data-theme="liquid"\]\[data-liquid-variant="green"\] \.v3-composer-input::placeholder\s*\{[^}]*color: var\(--cc-placeholder\);/,
    );
    expect(css).toMatch(
      /html\[data-theme="liquid"\]\[data-liquid-variant="green"\] \.v3-custom-model-select-options\s*\{[^}]*background-color: #222425;[^}]*color: #ffffff;/,
    );
    expect(css).toMatch(
      /html\[data-theme="liquid"\]\[data-liquid-variant="green"\] \.cc-global-search-field input\s*\{[^}]*border: 0;[^}]*background: transparent;[^}]*box-shadow: none;/,
    );
    expect(css).toMatch(
      /html\[data-theme="liquid"\]\[data-liquid-variant="green"\] \.cc-global-search-field:focus-within\s*\{[^}]*border-color: rgba\(255, 255, 255, 0\.34\);/,
    );
    expect(css).toMatch(
      /html\[data-theme="liquid"\]\[data-liquid-variant="green"\] \.oc-feedback-message-field\s*\{[^}]*background: var\(--cc-input-surface\);/,
    );
    expect(css).toMatch(
      /html\[data-theme="liquid"\]\[data-liquid-variant="green"\] \.oc-feedback-message-field textarea\s*\{[^}]*background: transparent;[^}]*box-shadow: none;/,
    );
    expect(css).toMatch(
      /html\[data-theme="liquid"\]\[data-liquid-variant="green"\] :is\(\.cc-sidebar-primary, \.cc-sidebar-search\)\s*\{[^}]*inset 0 1px 0 rgba\(255, 255, 255, 0\.14\);?[^}]*\}/,
    );
    expect(css).not.toMatch(
      /html\[data-theme="liquid"\]\[data-liquid-variant="green"\] :is\(\.cc-sidebar-primary, \.cc-sidebar-search\)\s*\{[^}]*0 4px 12px/,
    );
    expect(css).not.toMatch(
      /html\[data-theme="liquid"\]\[data-liquid-variant="green"\] :is\(\.cc-sidebar-primary:hover, \.cc-sidebar-search:hover, \.cc-sidebar-search:focus-within\)\s*\{[^}]*0 5px 13px/,
    );
    expect(css).toContain(':is(input, textarea, select, .v3-custom-model-select-trigger)');
    expect(css).toContain("url('/liquid-dark-background.png')");
    expect(css).toContain('background: linear-gradient(180deg, #151b19 0%, #111714 58%, #0f1513 100%);');
    expect(css).toContain('background: rgba(21, 155, 120, 0.28);');
    expect(css).toMatch(
      /\.v3-wpi-plan\s*\{[^}]*border-color: rgba\(41, 188, 149, 0\.2\);[^}]*background: rgba\(8, 28, 24, 0\.82\);[^}]*box-shadow: inset 0 1px 0 rgba\(222, 255, 246, 0\.05\);/,
    );
    expect(css).toMatch(
      /\.v3-wpi-plan-step\.completed\s*\{[^}]*color: rgba\(211, 232, 225, 0\.76\);/,
    );
    expect(css).not.toMatch(/\.v3-wpi-plan\s*\{[^}]*background: #000;/);
  });
});
