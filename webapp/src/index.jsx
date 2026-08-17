import React, { lazy, Suspense, useEffect, useLayoutEffect, useState, useTransition } from 'react';
import ReactDOM from 'react-dom/client';
import '@fontsource-variable/inter/wght.css';
import '@fontsource-variable/noto-sans-sc/wght.css';
import '@fontsource-variable/jetbrains-mono/wght.css';
import AuthGateway from './views/auth-gateway';
import PwaController from './components/pwa-controller';
import PushCleanupController from './components/push-cleanup-controller';
import { getAuthRevision, getPushPromptOwner, getToken } from './api';
import { FeedbackProvider } from './components/feedback-system';
import { applyDocumentTheme, THEME_STORAGE_KEY } from './utils/theme-access';
import { shouldMountPwaForPathname } from './utils/auth-routes';
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

export function App() {
  const [browserLocation, setBrowserLocation] = useState(readBrowserLocation);
  const [, startAuthTransition] = useTransition();
  const [auth, setAuth] = useState(() => ({
    loggedIn: Boolean(getToken()),
    pushPromptOwner: getPushPromptOwner(),
    revision: getAuthRevision(),
  }));

  useEffect(() => {
    const handleAuthChanged = (event) => {
      startAuthTransition(() => {
        setAuth({
          loggedIn: Boolean(event.detail?.loggedIn),
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

  const mountPwa = shouldMountPwaForPathname(browserLocation.pathname);
  const standaloneRoute = browserLocation.pathname.startsWith('/mobile-upload/')
    || new URLSearchParams(browserLocation.search).get('workflow_demo') === '1';
  const shouldLoadWorkspace = auth.loggedIn || standaloneRoute || developmentWorkspacePreview;

  return (
    <FeedbackProvider>
      {shouldLoadWorkspace ? (
        <Suspense fallback={null}>
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
