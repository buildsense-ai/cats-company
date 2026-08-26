import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { generateAuthCriticalCss } from '../scripts/generate-auth-critical-css.mjs';

const readSource = (path) => readFileSync(resolve(process.cwd(), path), 'utf8')
  .replace(/\r\n?/g, '\n');

const entrySource = readSource('src/index.jsx');
const authCss = readSource('src/css/auth-critical.css');
const workspaceStyles = readSource('src/views/workspace-styles.js');
const authSessionSource = readSource('src/auth-session.js');
const authGatewaySource = readSource('src/views/auth-gateway.jsx');
const passwordResetSource = readSource('src/widgets/password-reset-form.jsx');
const pushCleanupSource = readSource('src/components/push-cleanup-controller.jsx');
const robots = readSource('public/robots.txt');
const viteConfig = readSource('vite.config.js');

describe('entry bundle split', () => {
  it('defers the authenticated workspace while preserving direct-route support', () => {
    expect(entrySource).toContain("const TinodeWeb = lazy(importWorkspace);");
    expect(entrySource).toContain("const PwaController = lazy(() => import('./components/pwa-controller'));");
    expect(entrySource).toContain("import PwaUpdateController from './components/pwa-update-controller';");
    expect(entrySource).toContain("const preloadWorkspace = () => { void importWorkspace().catch(() => undefined); };");
    expect(entrySource).toContain("from './auth-session';");
    expect(entrySource).toContain('  isTokenExpired,');
    expect(entrySource).toContain('const nextAuth = {');
    expect(entrySource).toContain('const [preserveAuthShell, setPreserveAuthShell] = useState(false);');
    expect(entrySource).toContain('if (loggedIn && !auth.loggedIn) setPreserveAuthShell(true);');
    expect(entrySource).toContain('const showAuthGateway = !shouldLoadWorkspace || preserveAuthShell;');
    expect(entrySource).toContain('fallback={showAuthGateway ? null : <WorkspaceLoadingFallback />}');
    expect(entrySource).toContain('preservePreviousScreen={preserveAuthShell}');
    expect(entrySource).toContain('function WorkspaceLoadingFallback()');
    expect(entrySource).toContain('preservePreviousScreen={preserveAuthShell}');
    expect(entrySource).toContain('onRecoverableError={handleWorkspaceLoadError}');
    expect(entrySource).toContain('function isWorkspaceChunkLoadError(error)');
    expect(entrySource).toContain('loggedIn: hasUsableSessionToken(token),');
    expect(entrySource).toContain("browserLocation.pathname.startsWith('/mobile-upload/')");
    expect(entrySource).toContain("get('workflow_demo') === '1'");
    expect(entrySource).toContain('onAuthenticationIntent={preloadWorkspace}');
    expect(entrySource).toContain('const shouldMountPwaController = auth.loggedIn');
    expect(entrySource).toContain('&& shouldLoadWorkspace');
    expect(entrySource).toContain('&& shouldMountPwaForPathname(browserLocation.pathname);');
    expect(entrySource).toContain('registerPwaServiceWorker();');
    expect(entrySource).toContain('<Suspense fallback={null}>');
    expect(entrySource).toContain('<PwaLoadErrorBoundary>');
    expect(entrySource).not.toContain("import TinodeWeb from './views/tinode-web';");
    expect(entrySource).not.toContain("import PwaController from './components/pwa-controller';");
    expect(entrySource).not.toContain("from './api'");
  });

  it('keeps navigation HTML out of precache while retaining hashed entry assets and offline fallback', () => {
    expect(viteConfig).toContain("'assets/index-*.{js,css}'");
    expect(viteConfig).toContain("'assets/workbox-window.*.js'");
    expect(viteConfig).toContain("'offline.html'");
    expect(viteConfig).not.toContain("'index.html'");
    expect(viteConfig).not.toContain("'assets/**/*.{js,css}'");
  });

  it('preserves the workspace stylesheet cascade after lazy loading', () => {
    const importedStylesheets = [...workspaceStyles.matchAll(/^import '(.+\.css)';$/gm)]
      .map(([, path]) => path);

    expect(importedStylesheets).toEqual([
      '@fontsource-variable/jetbrains-mono/wght.css',
      '../css/openchat-theme.css',
      '../css/catsco-ui-system.css',
      '../css/catsco-liquid-green.css',
      '../css/catsco-topbar.css',
      '../css/catsco-secondary-headers.css',
      '../css/catsco-settings-controls.css',
      '../css/catsco-secondary-surfaces.css',
      '../css/search-overlay.css',
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
    expect(authCss).toContain('html[data-theme="liquid"] body {');
    expect(authCss).toContain("url('/liquid-dark-background.png')");
    expect(authCss).toContain('@keyframes cc-liquid-drift-a');
    expect(authCss).toContain('@keyframes cc-liquid-drift-b');
    expect(authCss).toContain('@keyframes cc-liquid-main-flow');
    expect(authCss).toContain('input[type="email"]');
    expect(authCss).toContain('input[type="password"]');
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
    expect(authCss.length).toBeLessThan(18_000);
  });

  it('generates the auth shell stylesheet from the workspace visual sources', () => {
    expect(authCss).toBe(generateAuthCriticalCss({ write: false }));
    expect(authCss).toContain('Generated from the workspace CSS sources');
    expect(authCss).not.toContain('.relay-access-primary-btn');
    expect(authCss).not.toContain('.catsco-download-action');
  });

  it('keeps workspace-only API routes out of the authentication dependency path', () => {
    expect(authSessionSource).toContain("'/api/auth/login'");
    expect(authSessionSource).not.toContain('/api/messages');
    expect(authSessionSource).not.toContain('/api/stt/sessions');
    expect(authSessionSource).not.toContain('/api/agents');
    expect(authSessionSource).not.toContain('/api/admin/relay');
    expect(authGatewaySource).toContain("from '../auth-session'");
    expect(authGatewaySource).not.toContain("from '../api'");
    expect(passwordResetSource).toContain("from '../auth-session'");
    expect(passwordResetSource).not.toContain("from '../api'");
    expect(pushCleanupSource).toContain("from '../auth-session'");
    expect(pushCleanupSource).not.toContain("from '../api'");
  });

  it('allows crawlers to discover the public entrypoint', () => {
    expect(robots).toContain('User-agent: *');
    expect(robots).toContain('Allow: /');
    expect(robots).not.toContain('Disallow: /');
  });
});
