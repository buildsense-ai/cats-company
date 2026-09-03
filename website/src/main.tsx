import React from 'react'
import ReactDOM from 'react-dom/client'
import App from './App'
// Self-hosted variable fonts (no third-party font CDN; same-origin immutable cache).
import '@fontsource-variable/inter'
import '@fontsource-variable/noto-sans-sc'
// Global chrome + home styles live in the entry bundle: home is the sync-rendered
// landing page. Page-specific stylesheets are imported by their (lazy) page
// components so Vite splits them into per-route CSS chunks.
import './styles/base.css'
import './styles/site-header.css'
import './styles/pages/home.css'
import './styles/site-footer.css'
import './styles/legacy-responsive.css'

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
)
