import { lazy, Suspense, useEffect, useRef, useState } from 'react'
import { Hero } from './Hero'
import { WorkflowSection } from './WorkflowSection'

const AsyncHomeSections = lazy(() => import('./HomeSections').then((m) => ({ default: m.HomeSections })))
const deferredHomeTargetIds = new Set(['workflows', 'task-demo', 'company-purpose', 'team'])

function targetsDeferredHomeSection(hash: string) {
  if (!hash) return false

  try {
    return deferredHomeTargetIds.has(decodeURIComponent(hash.slice(1)))
  } catch {
    return false
  }
}

function DeferredHomeSections() {
  const triggerRef = useRef<HTMLDivElement>(null)
  const [shouldLoad, setShouldLoad] = useState(() => targetsDeferredHomeSection(window.location.hash))

  useEffect(() => {
    const trigger = triggerRef.current
    if (!trigger || typeof IntersectionObserver === 'undefined') {
      setShouldLoad(true)
      return undefined
    }

    const observer = new IntersectionObserver(([entry]) => {
      if (!entry.isIntersecting) return
      setShouldLoad(true)
      observer.disconnect()
    })

    observer.observe(trigger)
    return () => observer.disconnect()
  }, [])

  useEffect(() => {
    const loadDeferredHashTarget = () => {
      if (targetsDeferredHomeSection(window.location.hash)) setShouldLoad(true)
    }

    window.addEventListener('hashchange', loadDeferredHashTarget)
    return () => window.removeEventListener('hashchange', loadDeferredHashTarget)
  }, [])

  return (
    <div ref={triggerRef} className="home-sections-deferred">
      {shouldLoad && (
        <Suspense fallback={null}>
          <AsyncHomeSections />
        </Suspense>
      )}
    </div>
  )
}

export function HomePage() {
  return (
    <main id="main-content">
      <Hero />
      <WorkflowSection />
      <DeferredHomeSections />
    </main>
  )
}
