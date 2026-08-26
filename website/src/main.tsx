import React from 'react'
import ReactDOM from 'react-dom/client'
import App from './App'
import './styles/base.css'
import './styles/site-header.css'
import './styles/pages/home.css'
import './styles/site-footer.css'
import './styles/pages/pricing.css'
import './styles/pages/login.css'
import './styles/pages/content.css'
import './styles/pages/contact.css'
import './styles/legacy-responsive.css'

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
)
