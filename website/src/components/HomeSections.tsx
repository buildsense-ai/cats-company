import '../styles/pages/home.css'
import { useEffect } from 'react'
import { getDeferredHomeHashTargetId } from '../home-hash'
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
    const targetId = getDeferredHomeHashTargetId(window.location.hash)
    if (!targetId) return undefined

    const target = document.getElementById(targetId)
    if (!target) return undefined

    const frame = window.requestAnimationFrame(() => {
      const behavior = window.matchMedia('(prefers-reduced-motion: reduce)').matches ? 'auto' : 'smooth'
      target.scrollIntoView({ behavior, block: 'start' })
    })

    return () => window.cancelAnimationFrame(frame)
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
