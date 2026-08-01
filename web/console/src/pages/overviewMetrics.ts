import type { OwnerOnboarding, OwnerPoolHealth } from '../api/types'

export interface ClientOverviewMetrics {
  activeInstances: number
  actionRequired: number
  schedulableRoutes: number
  openIncidents: number
}

export function clientOverviewMetrics(
  onboarding?: OwnerOnboarding,
  health?: OwnerPoolHealth,
): ClientOverviewMetrics {
  return {
    activeInstances: onboarding?.summary.active_instances ?? 0,
    actionRequired: onboarding?.summary.action_required ?? 0,
    schedulableRoutes: health?.capacity.schedulable ?? 0,
    openIncidents: health?.incidents.length ?? 0,
  }
}
