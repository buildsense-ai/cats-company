import { useEffect } from 'react'
import { LazyMotion } from 'framer-motion'
import { Capabilities } from './Capabilities'
import { Comparison } from './Comparison'
import { ResourceConvergence } from './ResourceConvergence'
import { TaskDemoSection } from './TaskDemoSection'
import { Team } from './Team'
import { WorkEnvironment } from './WorkEnvironment'
import { WorkflowStories } from './WorkflowStories'

const loadMotionFeatures = () => import('../motion-features').then((mod) => mod.default)

export function HomeSections() {
  useEffect(() => {
    if (!window.location.hash) return

    let target: HTMLElement | null = null
    try {
      target = document.getElementById(decodeURIComponent(window.location.hash.slice(1)))
    } catch {
      return
    }

    target?.scrollIntoView({
      behavior: window.matchMedia('(prefers-reduced-motion: reduce)').matches ? 'auto' : 'smooth',
      block: 'start',
    })
  }, [])

  return (
    <LazyMotion features={loadMotionFeatures} strict>
      <WorkflowStories />
      <ResourceConvergence />
      <TaskDemoSection />
      <WorkEnvironment />
      <Comparison />
      <Capabilities />
      <Team />
    </LazyMotion>
  )
}
