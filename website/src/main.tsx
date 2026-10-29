import React from 'react'
import ReactDOM from 'react-dom/client'
import App from './App'
// CJK text intentionally uses the platform font stack (PingFang SC on macOS,
// Microsoft YaHei on Windows). Shipping the full Noto Sans SC unicode-range
// set adds many late network requests without improving the initial render.
import './styles/base.css'
import './styles/site-header.css'
import './styles/pages/home-critical.css'
import './styles/site-footer.css'
import './styles/legacy-responsive.css'

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
)
