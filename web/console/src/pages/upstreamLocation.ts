const UPSTREAM_TABS = new Set(['plans', 'channels', 'pools', 'strategies'])

export interface UpstreamLocation {
  tab: string
  planId?: string
}

export function upstreamLocationFromSearch(search: string): UpstreamLocation {
  const params = new URLSearchParams(search)
  const requestedTab = params.get('tab')?.trim() ?? ''
  const tab = UPSTREAM_TABS.has(requestedTab) ? requestedTab : 'plans'
  const planId = params.get('plan_id')?.trim()
  return { tab, planId: tab === 'plans' && planId ? planId : undefined }
}

export function selectedPlanFromLocation(
  requestedPlanId: string | undefined,
  availablePlanIds: string[],
): string | undefined {
  if (!requestedPlanId) return undefined
  return availablePlanIds.includes(requestedPlanId) ? requestedPlanId : undefined
}
