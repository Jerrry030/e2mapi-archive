import type { Instance } from '../api/types'

export interface LocatedInstances {
  items: Instance[]
  requested: boolean
  found: boolean
}

export function instancesForLocation(
  instances: Instance[],
  requestedInstanceId?: string | null,
): LocatedInstances {
  const instanceId = requestedInstanceId?.trim()
  if (!instanceId) return { items: instances, requested: false, found: false }
  const located = instances.find((instance) => instance.id === instanceId)
  return {
    items: located ? [located] : instances,
    requested: true,
    found: Boolean(located),
  }
}
