import { useEffect } from 'react'
import { Capabilities } from './components/Capabilities'
import { Comparison } from './components/Comparison'
import { ContactPage } from './components/ContactPage'
import { DownloadPage } from './components/DownloadPage'
import { Footer } from './components/Footer'
import { Header } from './components/Header'
import { Hero } from './components/Hero'
import { LegalPage } from './components/LegalPage'
import { LoginPage } from './components/LoginPage'
import { NotFoundPage } from './components/NotFoundPage'
import { PricingPage } from './components/PricingPage'
import { ResourceConvergence } from './components/ResourceConvergence'
import { TaskDemoSection } from './components/TaskDemoSection'
import { Team } from './components/Team'
import { WorkEnvironment } from './components/WorkEnvironment'
import { WorkflowSection } from './components/WorkflowSection'
import { WorkflowStories } from './components/WorkflowStories'
import { applyRouteMetadata, resolveSiteRoute, type SitePage } from './site-routes'

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
    case 'pricing': return <PricingPage />
    case 'download': return <DownloadPage />
    case 'contact': return <ContactPage />
    case 'privacy': return <LegalPage kind="privacy" />
    case 'terms': return <LegalPage kind="terms" />
    case 'login': return <LoginPage />
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

  if (route?.page === 'login') return <LoginPage />

  return (
    <div className="min-h-screen bg-canvas text-ink">
      <a className="skip-link" href="#main-content">跳到主要内容</a>
      <Header />
      {route ? renderPage(route.page) : <NotFoundPage />}
      <Footer />
    </div>
  )
}
