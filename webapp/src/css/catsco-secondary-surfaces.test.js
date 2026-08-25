import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

const css = readFileSync(resolve(process.cwd(), 'src/css/catsco-secondary-surfaces.css'), 'utf8')
  .replace(/\r\n?/g, '\n');
const indexSource = readFileSync(resolve(process.cwd(), 'src/index.jsx'), 'utf8')
  .replace(/\r\n?/g, '\n');

describe('secondary surface design contract', () => {
  it('loads the scoped layer after the shared settings controls', () => {
    const settingsIndex = indexSource.indexOf("import './css/catsco-settings-controls.css';");
    const secondaryIndex = indexSource.indexOf("import './css/catsco-secondary-surfaces.css';");

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

  it('keeps the phone upload dialog opaque inside inherited empty-state layouts', () => {
    expect(css).toContain('background: var(--cc-main-bg);');
    expect(css).toContain('backdrop-filter: none;');
    expect(css).toContain('.v3-phone-upload-modal {');
    expect(css).toContain('background: var(--cc-panel) !important;');
    expect(css).toContain('text-align: left;');
    expect(css).toContain('.v3-phone-upload-body {');
  });
});
