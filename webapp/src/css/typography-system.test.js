import { readFileSync, readdirSync } from 'node:fs';
import { resolve } from 'node:path';

const readSource = (path) => readFileSync(resolve(process.cwd(), path), 'utf8')
  .replace(/\r\n?/g, '\n');

const indexSource = readSource('src/index.jsx');
const workspaceStylesSource = readSource('src/views/workspace-styles.js');
const catscoCss = readSource('src/css/catsco-ui-system.css');
const openchatCss = readSource('src/css/openchat-theme.css');
const markdownSource = readSource('src/widgets/markdown-utils.js');
const agentStoreSource = readSource('src/widgets/agent-store-modal.jsx');
const allCss = readdirSync(resolve(process.cwd(), 'src/css'))
  .filter((name) => name.endsWith('.css'))
  .map((name) => readSource(`src/css/${name}`))
  .join('\n');

describe('CatsCo typography system', () => {
  it('loads auth-facing font families before the workspace and defers only monospace', () => {
    expect(indexSource).toContain("import '@fontsource-variable/inter/wght.css';");
    expect(indexSource).toContain("import '@fontsource-variable/noto-sans-sc/wght.css';");
    expect(indexSource).not.toContain("import '@fontsource-variable/jetbrains-mono/wght.css';");
    expect(workspaceStylesSource).not.toContain("import '@fontsource-variable/inter/wght.css';");
    expect(workspaceStylesSource).not.toContain("import '@fontsource-variable/noto-sans-sc/wght.css';");
    expect(workspaceStylesSource).toContain("import '@fontsource-variable/jetbrains-mono/wght.css';");
  });

  it('uses Inter with Noto Sans SC fallback across the interface', () => {
    expect(catscoCss).toContain('--cc-font-sans: "Inter Variable", "Noto Sans SC Variable"');
    expect(catscoCss).toContain('font-family: var(--cc-font-sans);');
    expect(openchatCss).toContain('--oc-font-family: var(--cc-font-sans);');
  });

  it('stabilizes font shaping and rendering across browsers', () => {
    expect(catscoCss).toContain('font-kerning: normal;');
    expect(catscoCss).toContain('font-optical-sizing: auto;');
    expect(catscoCss).toContain('font-synthesis: none;');
    expect(catscoCss).toContain('text-rendering: optimizeLegibility;');
    expect(catscoCss).toContain('-webkit-font-smoothing: antialiased;');
    expect(catscoCss).toContain('-moz-osx-font-smoothing: grayscale;');
  });

  it('routes code surfaces through JetBrains Mono', () => {
    expect(catscoCss).toContain('--cc-font-mono: "JetBrains Mono Variable", "JetBrains Mono"');
    expect(openchatCss).not.toMatch(/font-family:\s*(?:ui-monospace|monospace|["']SF ?Mono)/);
    expect(markdownSource).toContain('font-family: "JetBrains Mono Variable", "JetBrains Mono"');
    expect(agentStoreSource).not.toContain("fontFamily: 'monospace'");
  });

  it('limits explicit numeric font weights to the four product tiers', () => {
    const explicitWeights = [...allCss.matchAll(/font-weight:\s*(\d{3})/g)]
      .map((match) => Number(match[1]));
    expect(new Set(explicitWeights)).toEqual(new Set([400, 500, 600, 700]));
    expect(catscoCss).toContain('--cc-font-weight-regular: 400;');
    expect(catscoCss).toContain('--cc-font-weight-medium: 500;');
    expect(catscoCss).toContain('--cc-font-weight-brand: 550;');
    expect(catscoCss).toContain('--cc-font-weight-semibold: 600;');
    expect(catscoCss).toContain('--cc-font-weight-bold: 700;');
  });
});
