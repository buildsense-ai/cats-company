import '../styles/pages/home.css'
import { useEffect } from 'react'
import { getDeferredHomeHashTargetId, scrollToHomeHashTarget } from '../home-hash'
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
    return scrollToHomeHashTarget(getDeferredHomeHashTargetId(window.location.hash))
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
