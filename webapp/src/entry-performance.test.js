import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { generateAuthCriticalCss } from '../scripts/generate-auth-critical-css.mjs';

const readSource = (path) => readFileSync(resolve(process.cwd(), path), 'utf8')
  .replace(/\r\n?/g, '\n');

const entrySource = readSource('src/index.jsx');
const authCss = readSource('src/css/auth-critical.css');
const workspaceStyles = readSource('src/views/workspace-styles.js');
const robots = readSource('public/robots.txt');
const viteConfig = readSource('vite.config.js');

describe('entry bundle split', () => {
  it('defers the authenticated workspace while preserving direct-route support', () => {
    expect(entrySource).toContain("const TinodeWeb = lazy(importWorkspace);");
    expect(entrySource).toContain("const PwaController = lazy(() => import('./components/pwa-controller'));");
    expect(entrySource).toContain("const preloadWorkspace = () => { void importWorkspace().catch(() => undefined); };");
    expect(entrySource).toContain('const [, startAuthTransition] = useTransition();');
    expect(entrySource).toContain('const nextAuth = {');
    expect(entrySource).toContain('if (loggedIn) {');
    expect(entrySource).toContain('startAuthTransition(() => {');
    expect(entrySource).toContain('setAuth(nextAuth);');
    expect(entrySource).toContain('function WorkspaceLoadingFallback()');
    expect(entrySource).toContain('<Suspense fallback={<WorkspaceLoadingFallback />}>');
    expect(entrySource).toContain('<WorkspaceLoadErrorBoundary>');
    expect(entrySource).toContain('function isWorkspaceChunkLoadError(error)');
    expect(entrySource).toContain('loggedIn: isRestorableSession(token),');
    expect(entrySource).toContain("browserLocation.pathname.startsWith('/mobile-upload/')");
    expect(entrySource).toContain("get('workflow_demo') === '1'");
    expect(entrySource).toContain('onAuthenticationIntent={preloadWorkspace}');
    expect(entrySource).toContain('const mountPwa = auth.loggedIn && shouldMountPwaForPathname(browserLocation.pathname);');
    expect(entrySource).toContain('<Suspense fallback={null}>');
    expect(entrySource).not.toContain("import TinodeWeb from './views/tinode-web';");
    expect(entrySource).not.toContain("import PwaController from './components/pwa-controller';");
  });

  it('precaches only the app shell and PWA runtime, not lazy workspace chunks', () => {
    expect(viteConfig).toContain("'assets/index-*.{js,css}'");
    expect(viteConfig).toContain("'assets/workbox-window.*.js'");
    expect(viteConfig).toContain("'index.html'");
    expect(viteConfig).toContain("'offline.html'");
    expect(viteConfig).toContain("'pwa-192x192.png'");
    expect(viteConfig).not.toContain("'assets/**/*.{js,css}'");
    expect(viteConfig).not.toContain("'pwa-*.png'");
  });

  it('preserves the workspace stylesheet cascade after lazy loading', () => {
    const importedStylesheets = [...workspaceStyles.matchAll(/^import '(.+\.css)';$/gm)]
      .map(([, path]) => path);

    expect(importedStylesheets).toEqual([
      '@fontsource-variable/inter/wght.css',
      '@fontsource-variable/noto-sans-sc/wght.css',
      '@fontsource-variable/jetbrains-mono/wght.css',
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
    expect(authCss).toContain('--oc-danger: #ef4444;');
    expect(authCss).toContain('--cc-scrollbar-size: var(--cc-scrollbar-panel-size);');
    expect(authCss).toContain('scrollbar-color: var(--cc-scrollbar-thumb) var(--cc-scrollbar-track);');
    expect(authCss).toContain('scrollbar-width: thin;');
    expect(authCss).toContain('html *');
    expect(authCss).toContain('::-webkit-scrollbar-thumb');
    expect(authCss).toContain('::-webkit-scrollbar-button');
    expect(authCss).toContain('display: none !important;');
    expect(authCss).toContain('::-webkit-scrollbar-corner');
    expect(authCss).toContain(':focus-visible');
    expect(authCss).toContain('html[data-theme="liquid"] :is(input, textarea, select)');
    expect(authCss).not.toContain('--cc-conversation-share-accent:');
    expect(authCss).toContain('html[data-theme="liquid"][data-liquid-variant="green"] {');
    expect(authCss).toContain('.oc-auth-card');
    expect(authCss).toContain('.oc-form-error');
    expect(authCss).toContain('.oc-settings-secondary');
    expect(authCss).toContain('.cc-workspace-loading');
    expect(authCss).toContain('.cc-workspace-loading-error');
    expect(authCss).not.toContain('.cc-toast');
    expect(authCss).not.toContain('.oc-modal.cc-confirm-dialog');
    expect(authCss).not.toContain('.oc-auth .oc-auth-btn');
    expect(authCss.length).toBeLessThan(14_500);
  });

  it('generates the auth shell stylesheet from the workspace visual sources', () => {
    expect(authCss).toBe(generateAuthCriticalCss({ write: false }));
    expect(authCss).toContain('Generated from the workspace CSS sources');
    expect(authCss).not.toContain('.relay-access-primary-btn');
    expect(authCss).not.toContain('.catsco-download-action');
  });

  it('allows crawlers to discover the public entrypoint', () => {
    expect(robots).toContain('User-agent: *');
    expect(robots).toContain('Allow: /');
    expect(robots).not.toContain('Disallow: /');
  });
});
