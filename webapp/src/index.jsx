import React, { useEffect, useState } from 'react';
import ReactDOM from 'react-dom/client';
import '@fontsource-variable/inter/wght.css';
import '@fontsource-variable/noto-sans-sc/wght.css';
import '@fontsource-variable/jetbrains-mono/wght.css';
import TinodeWeb from './views/tinode-web';
import PwaController from './components/pwa-controller';
import { getAuthRevision, getPushPromptOwner, getToken } from './api';
import { FeedbackProvider } from './components/feedback-system';
import { syncThemeColor, THEME_STORAGE_KEY } from './utils/theme-access';
import './css/catsco-topbar.css';
import './css/catsco-secondary-headers.css';
import './css/catsco-settings-controls.css';
import './css/search-overlay.css';

syncThemeColor(localStorage.getItem(THEME_STORAGE_KEY));

function App() {
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

  return (
    <FeedbackProvider>
      <TinodeWeb />
      <PwaController
        loggedIn={auth.loggedIn}
        pushPromptOwner={auth.pushPromptOwner}
        sessionRevision={auth.revision}
      />
    </FeedbackProvider>
  );
}

const root = ReactDOM.createRoot(document.getElementById('root'));
root.render(
  <React.StrictMode>
    <App />
  </React.StrictMode>
);
