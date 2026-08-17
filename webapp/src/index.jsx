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
import { readStoredUserProfile, USER_PROFILE_STORAGE_KEY } from './utils/user-profile';
import './css/auth-critical.css';

const importWorkspace = () => import('./views/tinode-web');
const TinodeWeb = lazy(importWorkspace);
const preloadWorkspace = () => { void importWorkspace(); };
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
    try {
      localStorage.removeItem(USER_PROFILE_STORAGE_KEY);
    } catch {
      // The token has already been cleared; storage may be unavailable.
    }
  }, [auth.loggedIn]);

  const mountPwa = shouldMountPwaForPathname(browserLocation.pathname);
  const standaloneRoute = browserLocation.pathname.startsWith('/mobile-upload/')
    || new URLSearchParams(browserLocation.search).get('workflow_demo') === '1';
  const shouldLoadWorkspace = auth.loggedIn || standaloneRoute || developmentWorkspacePreview;

  return (
    <FeedbackProvider>
      {shouldLoadWorkspace ? (
        <Suspense fallback={<WorkspaceLoadingFallback />}>
          <TinodeWeb location={browserLocation} />
        </Suspense>
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
