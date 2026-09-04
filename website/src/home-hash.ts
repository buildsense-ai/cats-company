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
