import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

const read = (name) => readFileSync(resolve(process.cwd(), `src/css/${name}.css`), 'utf8');
const system = read('catsco-ui-system');
const green = read('catsco-liquid-green');
const skillHub = read('skillhub-view');
const artifacts = read('openchat-theme');
const secondary = read('catsco-secondary-surfaces');
const tokens = (source) => Object.fromEntries([...source.matchAll(/(--cc-[\w-]+):\s*([^;]+);/g)].map((match) => [match[1], match[2].trim()]));
const themeTokens = (name) => tokens(system.match(new RegExp(`html\\[data-theme="${name}"\\]\\s*\\{([^}]+)\\}`))[1]);
const root = tokens(system.match(/:root\s*\{([^}]+)\}/)[1]);
const variants = {
  light: root,
  dark: { ...root, ...themeTokens('dark') },
  liquid: { ...root, ...themeTokens('liquid') },
  green: { ...root, ...themeTokens('liquid'), ...tokens(green.match(/html[^}]+\{([^}]+)\}/)[1]) },
};

function luminance(hex) {
  const value = hex.replace('#', '');
  const channels = [0, 2, 4].map((offset) => parseInt(value.slice(offset, offset + 2), 16) / 255)
    .map((channel) => channel <= 0.04045 ? channel / 12.92 : ((channel + 0.055) / 1.055) ** 2.4);
  return channels[0] * 0.2126 + channels[1] * 0.7152 + channels[2] * 0.0722;
}
function contrast(first, second) {
  const values = [luminance(first), luminance(second)].sort((a, b) => b - a);
  return (values[0] + 0.05) / (values[1] + 0.05);
}

describe('theme surface and primary action contrast', () => {
  it.each(Object.entries(variants))('%s keeps popup hierarchy and solid actions readable', (_, theme) => {
    ['--cc-text', '--cc-text-secondary', '--cc-muted'].forEach((name) => {
      expect(contrast(theme[name], theme['--cc-history-panel-bg'])).toBeGreaterThanOrEqual(4.5);
    });
    ['--cc-action-bg', '--cc-action-bg-hover'].forEach((name) => {
      expect(contrast(theme[name], theme['--cc-on-action'])).toBeGreaterThanOrEqual(4.5);
    });
  });

  it('gives liquid green its own popup surface and three text levels', () => {
    expect(variants.green['--cc-history-panel-bg']).toBe('#222425');
    expect(new Set(['--cc-text', '--cc-text-secondary', '--cc-muted'].map(name => variants.green[name])).size).toBe(3);
  });

  it('uses semantic colors for artifact confirmations and primary SkillHub actions', () => {
    expect(artifacts).toMatch(/\.cloud-artifact-confirm\s*\{[^}]*background:\s*var\(--cc-history-panel-bg\)/);
    expect(artifacts).toMatch(/\.cloud-artifact-confirm h4\s*\{[^}]*color:\s*var\(--cc-text\)/);
    expect(skillHub).toMatch(/\.cc-skillhub-page button\.primary\s*\{[^}]*background:\s*var\(--cc-action-bg\);[^}]*color:\s*var\(--cc-on-action\)/);
  });

  it('lets mobile member sections fit content without forced blank height', () => {
    expect(system).toMatch(/\.oc-create-group-dialog \.oc-member-picker-shell\s*\{[^}]*min-height: 0;/);
    expect(system).not.toMatch(/min-height: 420px;/);
    expect(system).toMatch(/\.oc-create-group-dialog \.oc-collaboration-modal-body\s*\{[^}]*flex: 1 1 auto;[^}]*max-height: none;/);
  });

  it('reuses the accessible action pair on primary form buttons without recoloring send or danger', () => {
    const primaryRule = secondary.match(/:root body \.oc-btn-primary,[\s\S]*?\{([^}]+)\}/)[1];
    expect(primaryRule).toContain('background: var(--cc-action-bg) !important;');
    expect(primaryRule).toContain('color: var(--cc-on-action) !important;');
    expect(secondary).toContain(':root body .cc-confirm-submit:not(.is-danger)');
    expect(primaryRule).not.toContain('v3-send');
  });
});
