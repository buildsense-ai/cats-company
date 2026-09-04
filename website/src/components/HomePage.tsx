import { lazy, Suspense, useEffect, useRef, useState } from 'react'
import { getDeferredHomeHashTargetId, getHomeHashTargetId } from '../home-hash'
import { Hero } from './Hero'
import { WorkflowSection } from './WorkflowSection'

const AsyncHomeSections = lazy(() => import('./HomeSections').then((m) => ({ default: m.HomeSections })))

function scrollToHomeHashTarget() {
  const targetId = getHomeHashTargetId(window.location.hash)
  if (!targetId) return undefined

  const target = document.getElementById(targetId)
  if (!target) return undefined

  const frame = window.requestAnimationFrame(() => {
    const behavior = window.matchMedia('(prefers-reduced-motion: reduce)').matches ? 'auto' : 'smooth'
    target.scrollIntoView({ behavior, block: 'start' })
  })

  return () => window.cancelAnimationFrame(frame)
}

function DeferredHomeSections() {
  const triggerRef = useRef<HTMLDivElement>(null)
  const [shouldLoad, setShouldLoad] = useState(() => Boolean(getDeferredHomeHashTargetId(window.location.hash)))

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
      if (getDeferredHomeHashTargetId(window.location.hash)) setShouldLoad(true)
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
  useEffect(() => {
    let cancelScroll = scrollToHomeHashTarget()

    const scrollChangedHash = () => {
      cancelScroll?.()
      cancelScroll = scrollToHomeHashTarget()
    }

    window.addEventListener('hashchange', scrollChangedHash)
    return () => {
      cancelScroll?.()
      window.removeEventListener('hashchange', scrollChangedHash)
    }
  }, [])

  return (
    <main id="main-content">
      <Hero />
      <WorkflowSection />
      <DeferredHomeSections />
    </main>
  )
}
