import { lazy, Suspense, useEffect, useRef, useState } from 'react'
import { Hero } from './Hero'
import { WorkflowSection } from './WorkflowSection'

const AsyncHomeSections = lazy(() => import('./HomeSections').then((m) => ({ default: m.HomeSections })))

function DeferredHomeSections() {
  const triggerRef = useRef<HTMLDivElement>(null)
  // Hash links (for example /#task-demo) must mount their target immediately.
  const [shouldLoad, setShouldLoad] = useState(() => Boolean(window.location.hash))

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
