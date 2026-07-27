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
    expect(css).toMatch(
      /html\[data-theme="liquid"\]\[data-liquid-variant="green"\] \.catsco-brand-mark\s*\{[^}]*background: #29bc95;[^}]*mask: url\('\/catsco-brand-mark\.webp'\)[^}]*filter: none;/,
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
    expect(css).toContain("url('/liquid-dark-background.png')");
    expect(css).toContain('background: linear-gradient(180deg, #151b19 0%, #111714 58%, #0f1513 100%);');
    expect(css).toContain('background: rgba(21, 155, 120, 0.28);');
  });
});
