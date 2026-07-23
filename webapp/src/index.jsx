import React from 'react';
import ReactDOM from 'react-dom/client';
import TinodeWeb from './views/tinode-web';
import { FeedbackProvider } from './components/feedback-system';
import './css/catsco-topbar.css';
import './css/catsco-secondary-headers.css';
import './css/catsco-settings-controls.css';

const root = ReactDOM.createRoot(document.getElementById('root'));
root.render(
  <React.StrictMode>
    <FeedbackProvider>
      <TinodeWeb />
    </FeedbackProvider>
  </React.StrictMode>
);
