import { lazy, Suspense, useEffect } from 'react'
import { Footer } from './components/Footer'
import { Header } from './components/Header'
import { applyRouteMetadata, resolveSiteRoute, type SitePage } from './site-routes'

// Route components and their page CSS are loaded together. This keeps the
// landing-page animation stylesheet out of pricing/legal/login initial loads.
const AsyncHomePage = lazy(() => import('./components/HomePage').then((m) => ({ default: m.HomePage })))
const AsyncPricingPage = lazy(() => import('./components/PricingPage').then((m) => ({ default: m.PricingPage })))
const AsyncDownloadPage = lazy(() => import('./components/DownloadPage').then((m) => ({ default: m.DownloadPage })))
const AsyncContactPage = lazy(() => import('./components/ContactPage').then((m) => ({ default: m.ContactPage })))
const AsyncLegalPage = lazy(() => import('./components/LegalPage').then((m) => ({ default: m.LegalPage })))
const AsyncLoginPage = lazy(() => import('./components/LoginPage').then((m) => ({ default: m.LoginPage })))
const AsyncNotFoundPage = lazy(() => import('./components/NotFoundPage').then((m) => ({ default: m.NotFoundPage })))

// Non-home pages never mount motion components; home mounts its animation
// provider inside the deferred lower-section chunk.

function renderPage(page: SitePage) {
  switch (page) {
    case 'home': return <AsyncHomePage />
    case 'pricing': return <AsyncPricingPage />
    case 'download': return <AsyncDownloadPage />
    case 'contact': return <AsyncContactPage />
    case 'privacy':
    case 'terms':
      return <AsyncLegalPage kind={page} />
    case 'login': return <AsyncLoginPage />
  }
}

export default function App() {
  const route = resolveSiteRoute(window.location.pathname)

  useEffect(() => applyRouteMetadata(route), [route])


  if (route?.page === 'login') {
    return <AsyncLoginPage />
  }

  return (
    <div className="min-h-screen bg-canvas text-ink">
      <a className="skip-link" href="#main-content">跳到主要内容</a>
      <Header />
      <Suspense fallback={<div className="min-h-[calc(100vh-72px)]" aria-hidden="true" />}>
        {route ? renderPage(route.page) : <AsyncNotFoundPage />}
        <Footer />
      </Suspense>
    </div>
  )
}
