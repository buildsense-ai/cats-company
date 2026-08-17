import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

const readSource = (path) => readFileSync(resolve(process.cwd(), path), 'utf8')
  .replace(/\r\n?/g, '\n');

const entrySource = readSource('src/index.jsx');
const authCss = readSource('src/css/auth-critical.css');
const workspaceStyles = readSource('src/views/workspace-styles.js');
const robots = readSource('public/robots.txt');

describe('entry bundle split', () => {
  it('defers the authenticated workspace while preserving direct-route support', () => {
    expect(entrySource).toContain("const TinodeWeb = lazy(importWorkspace);");
    expect(entrySource).toContain("const preloadWorkspace = () => { void importWorkspace(); };");
    expect(entrySource).toContain('const [, startAuthTransition] = useTransition();');
    expect(entrySource).toContain('startAuthTransition(() => {');
    expect(entrySource).toContain('<Suspense fallback={null}>');
    expect(entrySource).toContain("browserLocation.pathname.startsWith('/mobile-upload/')");
    expect(entrySource).toContain("get('workflow_demo') === '1'");
    expect(entrySource).toContain('onAuthenticationIntent={preloadWorkspace}');
    expect(entrySource).not.toContain("import TinodeWeb from './views/tinode-web';");
  });

  it('preserves the workspace stylesheet cascade after lazy loading', () => {
    const importedStylesheets = [...workspaceStyles.matchAll(/^import '(.+\.css)';$/gm)]
      .map(([, path]) => path);

    expect(importedStylesheets).toEqual([
      '../css/catsco-topbar.css',
      '../css/catsco-secondary-headers.css',
      '../css/catsco-settings-controls.css',
      '../css/search-overlay.css',
      '../css/openchat-theme.css',
      '../css/catsco-ui-system.css',
      '../css/catsco-liquid-green.css',
    ]);
  });

  it('keeps the auth shell on the established visual tokens and overflow rules', () => {
    expect(authCss).toContain('--cc-accent: #29bc95;');
    expect(authCss).toContain('--cc-bg: #fcfcfc;');
    expect(authCss).toContain('--cc-scrollbar-page-size: 8px;');
    expect(authCss).toContain('scrollbar-color: var(--cc-scrollbar-thumb) var(--cc-scrollbar-track);');
    expect(authCss).toContain('::-webkit-scrollbar-thumb');
    expect(authCss).toContain('--cc-liquid-shadow:');
    expect(authCss).toContain('.oc-auth-card');
  });

  it('allows crawlers to discover the public entrypoint', () => {
    expect(robots).toContain('User-agent: *');
    expect(robots).toContain('Allow: /');
    expect(robots).not.toContain('Disallow: /');
  });
});
