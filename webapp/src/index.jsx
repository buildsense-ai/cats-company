import React, { lazy, Suspense, useEffect, useLayoutEffect, useState, useTransition } from 'react';
import ReactDOM from 'react-dom/client';
import '@fontsource-variable/inter/wght.css';
import '@fontsource-variable/noto-sans-sc/wght.css';
import '@fontsource-variable/jetbrains-mono/wght.css';
import AuthGateway from './views/auth-gateway';
import PwaController from './components/pwa-controller';
import PushCleanupController from './components/push-cleanup-controller';
import { getAuthRevision, getPushPromptOwner, getToken, setToken } from './api';
import { FeedbackProvider } from './components/feedback-system';
import { applyDocumentTheme, THEME_STORAGE_KEY } from './utils/theme-access';
import { shouldMountPwaForPathname } from './utils/auth-routes';
import { clearStoredUserProfile, readStoredUserProfile } from './utils/user-profile';
import './css/auth-critical.css';

const importWorkspace = () => import('./views/tinode-web');
const TinodeWeb = lazy(importWorkspace);
const preloadWorkspace = () => { void importWorkspace().catch(() => undefined); };
const WORKSPACE_CHUNK_ERROR_PATTERN = /(?:chunkloaderror|loading chunk|failed to fetch dynamically imported module|importing a module script failed|dynamically imported module)/i;
const requestedThemePreview = import.meta.env.DEV
  ? new URLSearchParams(window.location.search).get('theme_preview')
  : '';
const developmentWorkspacePreview = import.meta.env.DEV && (
  import.meta.env.VITE_DEV_BYPASS_AUTH === 'true'
  || ['light', 'dark', 'liquid', 'liquid-green'].includes(requestedThemePreview)
);

applyDocumentTheme(localStorage.getItem(THEME_STORAGE_KEY));

function readBrowserLocation() {
  return {
    pathname: window.location.pathname,
    search: window.location.search,
    hash: window.location.hash,
  };
}

function isRestorableSession(token = getToken()) {
  return Boolean(token && readStoredUserProfile());
}

function readInitialAuthState() {
  const token = getToken();
  return {
    loggedIn: isRestorableSession(token),
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

export class WorkspaceLoadErrorBoundary extends React.Component {
  state = { error: null };

  static getDerivedStateFromError(error) {
    return { error };
  }

  render() {
    const { error } = this.state;
    if (error) {
      if (!isWorkspaceChunkLoadError(error)) throw error;
      return <WorkspaceLoadFailure />;
    }
    return this.props.children;
  }
}

export function App() {
  const [browserLocation, setBrowserLocation] = useState(readBrowserLocation);
  const [, startAuthTransition] = useTransition();
  const [auth, setAuth] = useState(readInitialAuthState);

  useEffect(() => {
    const handleAuthChanged = (event) => {
      const loggedIn = Boolean(event.detail?.loggedIn) && isRestorableSession();
      startAuthTransition(() => {
        setAuth({
          loggedIn,
          pushPromptOwner: getPushPromptOwner(),
          revision: event.detail?.revision ?? getAuthRevision(),
        });
      });
    };
    window.addEventListener('cc:auth-changed', handleAuthChanged);
    return () => window.removeEventListener('cc:auth-changed', handleAuthChanged);
  }, [startAuthTransition]);

  useLayoutEffect(() => {
    const handleHistoryChange = () => setBrowserLocation(readBrowserLocation());
    window.addEventListener('popstate', handleHistoryChange);
    return () => window.removeEventListener('popstate', handleHistoryChange);
  }, []);

  useLayoutEffect(() => {
    if (auth.loggedIn || !getToken() || readStoredUserProfile()) return;
    setToken(null);
    clearStoredUserProfile();
  }, [auth.loggedIn]);

  const mountPwa = shouldMountPwaForPathname(browserLocation.pathname);
  const standaloneRoute = browserLocation.pathname.startsWith('/mobile-upload/')
    || new URLSearchParams(browserLocation.search).get('workflow_demo') === '1';
  const shouldLoadWorkspace = auth.loggedIn || standaloneRoute || developmentWorkspacePreview;

  return (
    <FeedbackProvider>
      {shouldLoadWorkspace ? (
        <WorkspaceLoadErrorBoundary>
          <Suspense fallback={<WorkspaceLoadingFallback />}>
            <TinodeWeb location={browserLocation} />
          </Suspense>
        </WorkspaceLoadErrorBoundary>
      ) : <AuthGateway location={browserLocation} onAuthenticationIntent={preloadWorkspace} />}
      {!auth.loggedIn && <PushCleanupController />}
      {mountPwa && (
        <PwaController
          loggedIn={auth.loggedIn}
          pushPromptOwner={auth.pushPromptOwner}
          sessionRevision={auth.revision}
        />
      )}
    </FeedbackProvider>
  );
}

const root = ReactDOM.createRoot(document.getElementById('root'));
root.render(
  <React.StrictMode>
    <App />
  </React.StrictMode>
);
