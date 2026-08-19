import React, { lazy, Suspense, useEffect, useLayoutEffect, useState } from 'react';
import ReactDOM from 'react-dom/client';
import '@fontsource-variable/inter/wght.css';
import '@fontsource-variable/noto-sans-sc/wght.css';
import '@fontsource-variable/jetbrains-mono/wght.css';
import TinodeWeb from './views/tinode-web';
import PwaController from './components/pwa-controller';
import PushCleanupController from './components/push-cleanup-controller';
import { getAuthRevision, getPushPromptOwner, getToken } from './api';
import { FeedbackProvider } from './components/feedback-system';
import t from './i18n';
import { applyThemeAttributes, THEME_STORAGE_KEY } from './utils/theme-access';
import { shouldMountPwaForPathname } from './utils/auth-routes';
import './css/catsco-topbar.css';
import './css/catsco-secondary-headers.css';
import './css/catsco-settings-controls.css';
import './css/search-overlay.css';

const SharedConversationView = lazy(() => import('./views/shared-conversation-view'));

let storedTheme = '';
try {
  storedTheme = localStorage.getItem(THEME_STORAGE_KEY) || '';
} catch {
  // A blocked storage area should not prevent the public share route from
  // rendering with its default theme.
}
applyThemeAttributes(storedTheme);

function readBrowserLocation() {
  return {
    pathname: window.location.pathname,
    search: window.location.search,
    hash: window.location.hash,
  };
}

function decodeSharedConversationToken(value) {
  try {
    return decodeURIComponent(value);
  } catch {
    return '';
  }
}

export function App() {
  const [browserLocation, setBrowserLocation] = useState(readBrowserLocation);
  const [auth, setAuth] = useState(() => ({
    loggedIn: Boolean(getToken()),
    pushPromptOwner: getPushPromptOwner(),
    revision: getAuthRevision(),
  }));

  useEffect(() => {
    const handleAuthChanged = (event) => setAuth({
      loggedIn: Boolean(event.detail?.loggedIn),
      pushPromptOwner: getPushPromptOwner(),
      revision: event.detail?.revision ?? getAuthRevision(),
    });
    window.addEventListener('cc:auth-changed', handleAuthChanged);
    return () => window.removeEventListener('cc:auth-changed', handleAuthChanged);
  }, []);

  useLayoutEffect(() => {
    const handleHistoryChange = () => setBrowserLocation(readBrowserLocation());
    window.addEventListener('popstate', handleHistoryChange);
    return () => window.removeEventListener('popstate', handleHistoryChange);
  }, []);

  const mountPwa = shouldMountPwaForPathname(browserLocation.pathname);
  const sharedConversationMatch = browserLocation.pathname.match(/^\/share\/([^/]+)\/?$/);

  if (sharedConversationMatch) {
    return (
      <Suspense fallback={<main className="cc-shared-conversation-state" role="status">{t('conversation_share_loading')}</main>}>
        <SharedConversationView token={decodeSharedConversationToken(sharedConversationMatch[1])} />
      </Suspense>
    );
  }

  return (
    <FeedbackProvider>
      <TinodeWeb location={browserLocation} />
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
