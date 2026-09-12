export const deferredHomeTargetIds = new Set(['workflows', 'task-demo', 'company-purpose', 'team'])

export function getHomeHashTargetId(hash: string) {
  if (!hash) return null

  try {
    return decodeURIComponent(hash.slice(1)) || null
  } catch {
    return null
  }
}

export function getDeferredHomeHashTargetId(hash: string) {
  const targetId = getHomeHashTargetId(hash)
  return targetId && deferredHomeTargetIds.has(targetId) ? targetId : null
}

/** Scrolls to an existing home target on the next frame; returns a cancel function. */
export function scrollToHomeHashTarget(targetId: string | null) {
  if (!targetId) return undefined

  const target = document.getElementById(targetId)
  if (!target) return undefined

  const frame = window.requestAnimationFrame(() => {
    const behavior = window.matchMedia('(prefers-reduced-motion: reduce)').matches ? 'auto' : 'smooth'
    target.scrollIntoView({ behavior, block: 'start' })
  })

  return () => window.cancelAnimationFrame(frame)
}
