import React from 'react'
import ReactDOM from 'react-dom/client'
import App from './App'
// Inter is needed immediately for the Latin brand/UI text. The CJK font is
// loaded after first paint below so 101 unicode-range files do not block FCP.
import '@fontsource-variable/inter'
// Global chrome + home styles live in the entry bundle: home is the sync-rendered
// landing page. Page-specific stylesheets are imported by their (lazy) page
// components so Vite splits them into per-route CSS chunks.
import './styles/base.css'
import './styles/site-header.css'
import './styles/pages/home.css'
import './styles/site-footer.css'
import './styles/legacy-responsive.css'

const loadCjkFont = () => {
  void import('@fontsource-variable/noto-sans-sc')
}

window.setTimeout(loadCjkFont, 1500)

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
)
