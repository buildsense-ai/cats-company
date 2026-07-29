import React, { useEffect, useState } from 'react';
import ReactDOM from 'react-dom/client';
import '@fontsource-variable/inter/wght.css';
import '@fontsource-variable/noto-sans-sc/wght.css';
import '@fontsource-variable/jetbrains-mono/wght.css';
import TinodeWeb from './views/tinode-web';
import PwaController from './components/pwa-controller';
import { getAuthRevision, getToken } from './api';
import { FeedbackProvider } from './components/feedback-system';
import './css/catsco-topbar.css';
import './css/catsco-secondary-headers.css';
import './css/catsco-settings-controls.css';

function App() {
  const [auth, setAuth] = useState(() => ({
    loggedIn: Boolean(getToken()),
    revision: getAuthRevision(),
    pushCleanupHandled: false,
  }));

  useEffect(() => {
    const handleAuthChanged = (event) => setAuth({
      loggedIn: Boolean(event.detail?.loggedIn),
      revision: event.detail?.revision ?? getAuthRevision(),
      pushCleanupHandled: Boolean(event.detail?.pushCleanupHandled),
    });
    window.addEventListener('cc:auth-changed', handleAuthChanged);
    return () => window.removeEventListener('cc:auth-changed', handleAuthChanged);
  }, []);

  return (
    <FeedbackProvider>
      <TinodeWeb />
      <PwaController
        loggedIn={auth.loggedIn}
        sessionRevision={auth.revision}
        pushCleanupHandled={auth.pushCleanupHandled}
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
