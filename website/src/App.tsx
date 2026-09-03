import { lazy, Suspense, useEffect } from 'react'
import { LazyMotion } from 'framer-motion'
import { Capabilities } from './components/Capabilities'
import { Comparison } from './components/Comparison'
import { Footer } from './components/Footer'
import { Header } from './components/Header'
import { Hero } from './components/Hero'
import { ResourceConvergence } from './components/ResourceConvergence'
import { TaskDemoSection } from './components/TaskDemoSection'
import { Team } from './components/Team'
import { WorkEnvironment } from './components/WorkEnvironment'
import { WorkflowSection } from './components/WorkflowSection'
import { WorkflowStories } from './components/WorkflowStories'
import { applyRouteMetadata, resolveSiteRoute, type SitePage } from './site-routes'

// Non-home routes are split into separate chunks so the landing page ships
// the smallest possible initial bundle. Home sections stay synchronous to
// avoid layout shift and keep hash-anchor navigation reliable.
const AsyncPricingPage = lazy(() => import('./components/PricingPage').then((m) => ({ default: m.PricingPage })))
const AsyncDownloadPage = lazy(() => import('./components/DownloadPage').then((m) => ({ default: m.DownloadPage })))
const AsyncContactPage = lazy(() => import('./components/ContactPage').then((m) => ({ default: m.ContactPage })))
const AsyncLegalPage = lazy(() => import('./components/LegalPage').then((m) => ({ default: m.LegalPage })))
const AsyncLoginPage = lazy(() => import('./components/LoginPage').then((m) => ({ default: m.LoginPage })))
const AsyncNotFoundPage = lazy(() => import('./components/NotFoundPage').then((m) => ({ default: m.NotFoundPage })))

// framer-motion feature set is split into an async chunk; strict mode throws if
// any component still uses full `motion.*` elements instead of `m.*`.
const loadMotionFeatures = () => import('./motion-features').then((mod) => mod.default)

function HomePage() {
  return (
    <main id="main-content">
      <Hero />
      <WorkflowSection />
      <WorkflowStories />
      <ResourceConvergence />
      <TaskDemoSection />
      <WorkEnvironment />
      <Comparison />
      <Capabilities />
      <Team />
    </main>
  )
}

function renderPage(page: SitePage) {
  switch (page) {
    case 'home': return <HomePage />
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

  useEffect(() => {
    if (route?.page !== 'home' || !window.location.hash) return undefined

    const targetId = window.location.hash.slice(1)
    if (!targetId) return undefined

    let target: HTMLElement | null = null
    try {
      target = document.getElementById(decodeURIComponent(targetId))
    } catch {
      return undefined
    }

    if (!target) return undefined

    const frame = window.requestAnimationFrame(() => {
      const prefersReducedMotion = window.matchMedia('(prefers-reduced-motion: reduce)').matches
      target?.scrollIntoView({
        behavior: prefersReducedMotion ? 'auto' : 'smooth',
        block: 'start',
      })
    })

    return () => window.cancelAnimationFrame(frame)
  }, [route])

  if (route?.page === 'login') {
    return (
      <LazyMotion features={loadMotionFeatures} strict>
        <Suspense fallback={null}>
          <AsyncLoginPage />
        </Suspense>
      </LazyMotion>
    )
  }

  return (
    <LazyMotion features={loadMotionFeatures} strict>
      <div className="min-h-screen bg-canvas text-ink">
        <a className="skip-link" href="#main-content">跳到主要内容</a>
        <Header />
        <Suspense fallback={<div className="min-h-[calc(100vh-72px)]" aria-hidden="true" />}>
          {route ? renderPage(route.page) : <AsyncNotFoundPage />}
          <Footer />
        </Suspense>
      </div>
    </LazyMotion>
  )
}
