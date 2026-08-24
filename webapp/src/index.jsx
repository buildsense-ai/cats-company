import React, { lazy, Suspense, useCallback, useEffect, useLayoutEffect, useState } from 'react';
import ReactDOM from 'react-dom/client';
import '@fontsource-variable/inter/wght.css';
import '@fontsource-variable/noto-sans-sc/wght.css';
import AuthGateway from './views/auth-gateway';
import PushCleanupController from './components/push-cleanup-controller';
import PwaUpdateController from './components/pwa-update-controller';
import {
  getAuthRevision,
  getPushPromptOwner,
  getToken,
  isTokenExpired,
  setToken,
} from './auth-session';
import { FeedbackProvider } from './components/feedback-system';
import { registerPwaServiceWorker } from './pwa-registration';
import { applyDocumentTheme, THEME_STORAGE_KEY } from './utils/theme-access';
import { shouldMountPwaForPathname } from './utils/auth-routes';
import { readStorageValue } from './utils/storage-access';
import { clearStoredUserProfile, readStoredUserProfile } from './utils/user-profile';
import { startPwaInstallLifecycle } from './utils/pwa-install';
import './css/auth-critical.css';

const importWorkspace = () => import('./views/tinode-web');
const TinodeWeb = lazy(importWorkspace);
const PwaController = lazy(() => import('./components/pwa-controller'));
const preloadWorkspace = () => { void importWorkspace().catch(() => undefined); };
const WORKSPACE_CHUNK_ERROR_PATTERN = /(?:chunkloaderror|loading chunk|failed to fetch dynamically imported module|importing a module script failed|dynamically imported module|unable to preload css)/i;
const requestedThemePreview = import.meta.env.DEV
  ? new URLSearchParams(window.location.search).get('theme_preview')
  : '';
const developmentWorkspacePreview = import.meta.env.DEV && (
  import.meta.env.VITE_DEV_BYPASS_AUTH === 'true'
  || ['light', 'dark', 'liquid', 'liquid-green'].includes(requestedThemePreview)
);

applyDocumentTheme(readStorageValue(THEME_STORAGE_KEY));
startPwaInstallLifecycle();

function readBrowserLocation() {
  return {
    pathname: window.location.pathname,
    search: window.location.search,
    hash: window.location.hash,
  };
}

function hasUsableSessionToken(token = getToken()) {
  return Boolean(token) && !isTokenExpired(token);
}

function readInitialAuthState() {
  const token = getToken();
  return {
    loggedIn: hasUsableSessionToken(token),
    pushPromptOwner: getPushPromptOwner(),
    revision: getAuthRevision(),
  };
}

function WorkspaceLoadingFallback() {
  return (
    <main className="cc-workspace-loading" aria-busy="true">
      <span className="cc-workspace-loading-indicator" aria-hidden="true" />
      <span role="status">正在加载工作台…</span>
    </main>
  );
}

export function isWorkspaceChunkLoadError(error) {
  return WORKSPACE_CHUNK_ERROR_PATTERN.test(String(error?.message || error || ''));
}

export function WorkspaceLoadFailure({ onRetry = () => window.location.reload() }) {
  return (
    <main className="cc-workspace-loading cc-workspace-loading-error">
      <p role="alert">工作台加载失败，请检查网络后重试。</p>
      <button type="button" className="oc-auth-btn cc-workspace-loading-retry" onClick={onRetry}>
        重新加载
      </button>
    </main>
  );
}

export class RecoverableChunkErrorBoundary extends React.Component {
  state = { error: null };

  static getDerivedStateFromError(error) {
    return { error };
  }

  componentDidCatch(error) {
    if (isWorkspaceChunkLoadError(error)) this.props.onRecoverableError?.(error);
  }

  render() {
    const { error } = this.state;
    if (error) {
      if (!isWorkspaceChunkLoadError(error)) throw error;
      return this.props.fallback;
    }
    return this.props.children;
  }
}

export function WorkspaceLoadErrorBoundary({
  children,
  preservePreviousScreen = false,
  onRecoverableError,
}) {
  return (
    <RecoverableChunkErrorBoundary
      fallback={preservePreviousScreen ? null : <WorkspaceLoadFailure />}
      onRecoverableError={onRecoverableError}
    >
      {children}
    </RecoverableChunkErrorBoundary>
  );
}

export function PwaLoadErrorBoundary({ children }) {
  return <RecoverableChunkErrorBoundary fallback={null}>{children}</RecoverableChunkErrorBoundary>;
}

function WorkspaceEntry({ location, onReady }) {
  useLayoutEffect(() => {
    onReady();
  }, [onReady]);

  return <TinodeWeb location={location} />;
}

export function App() {
  const [browserLocation, setBrowserLocation] = useState(readBrowserLocation);
  const [auth, setAuth] = useState(readInitialAuthState);
  const [preserveAuthShell, setPreserveAuthShell] = useState(false);

  useEffect(() => {
    registerPwaServiceWorker();
  }, []);

  useEffect(() => {
    const handleAuthChanged = (event) => {
      const loggedIn = Boolean(event.detail?.loggedIn) && hasUsableSessionToken();
      const nextAuth = {
        loggedIn,
        pushPromptOwner: getPushPromptOwner(),
        revision: event.detail?.revision ?? getAuthRevision(),
      };
      if (loggedIn && !auth.loggedIn) setPreserveAuthShell(true);
      if (!loggedIn) setPreserveAuthShell(false);
      setAuth(nextAuth);
    };
    window.addEventListener('cc:auth-changed', handleAuthChanged);
    return () => window.removeEventListener('cc:auth-changed', handleAuthChanged);
  }, [auth.loggedIn]);

  useLayoutEffect(() => {
    const handleHistoryChange = () => setBrowserLocation(readBrowserLocation());
    window.addEventListener('popstate', handleHistoryChange);
    return () => window.removeEventListener('popstate', handleHistoryChange);
  }, []);

  useLayoutEffect(() => {
    const token = getToken();
    if (auth.loggedIn || (token && !isTokenExpired(token))) return;
    if (token) setToken(null);
    if (readStoredUserProfile()) clearStoredUserProfile();
  }, [auth.loggedIn, auth.revision]);

  const standaloneRoute = browserLocation.pathname.startsWith('/mobile-upload/')
    || new URLSearchParams(browserLocation.search).get('workflow_demo') === '1';
  const shouldLoadWorkspace = auth.loggedIn || standaloneRoute || developmentWorkspacePreview;
  const shouldMountPwaController = auth.loggedIn
    && shouldLoadWorkspace
    && shouldMountPwaForPathname(browserLocation.pathname);
  const showAuthGateway = !shouldLoadWorkspace || preserveAuthShell;
  const handleWorkspaceReady = useCallback(() => {
    setPreserveAuthShell(false);
  }, []);
  const handleWorkspaceLoadError = useCallback(() => {
    setPreserveAuthShell(false);
  }, []);

  return (
    <FeedbackProvider>
      <div className="cc-pwa-status" aria-live="polite" aria-label="应用状态">
        <PwaUpdateController />
        {shouldMountPwaController && (
          <PwaLoadErrorBoundary>
            <Suspense fallback={null}>
              <PwaController
                loggedIn={auth.loggedIn}
                pushPromptOwner={auth.pushPromptOwner}
                sessionRevision={auth.revision}
              />
            </Suspense>
          </PwaLoadErrorBoundary>
        )}
      </div>
      {showAuthGateway && <AuthGateway location={browserLocation} onAuthenticationIntent={preloadWorkspace} />}
      {shouldLoadWorkspace && (
        <WorkspaceLoadErrorBoundary
          preservePreviousScreen={preserveAuthShell}
          onRecoverableError={handleWorkspaceLoadError}
        >
          <Suspense fallback={showAuthGateway ? null : <WorkspaceLoadingFallback />}>
            <WorkspaceEntry location={browserLocation} onReady={handleWorkspaceReady} />
          </Suspense>
        </WorkspaceLoadErrorBoundary>
      )}
      {!auth.loggedIn && <PushCleanupController />}
    </FeedbackProvider>
  );
}

const root = ReactDOM.createRoot(document.getElementById('root'));
root.render(
  <React.StrictMode>
    <App />
  </React.StrictMode>
);
