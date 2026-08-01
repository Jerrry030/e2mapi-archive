import type { OperationsIncident, OperationsOnboarding, OperationsTimelineItem } from '../api/types'

export interface OperationsCenterFilter {
  userId?: number
  instanceId?: string
  status?: 'attention' | 'active' | 'all'
}

function sameScope(
  item: { user_id?: number; instance_id?: string },
  filter: OperationsCenterFilter,
): boolean {
  if (filter.userId && item.user_id !== filter.userId) return false
  if (filter.instanceId && item.instance_id !== filter.instanceId) return false
  return true
}

export function filterOperationsOnboarding(
  rows: OperationsOnboarding[],
  filter: OperationsCenterFilter,
): OperationsOnboarding[] {
  return rows.filter((row) => {
    if (!sameScope(row, filter)) return false
    if (!filter.status || filter.status === 'all') return true
    if (filter.status === 'active') return row.status === 'active'
    return row.status === 'retryable' || row.status === 'pending' || row.status === 'running'
  })
}

export function filterOperationsIncidents(
  rows: OperationsIncident[],
  filter: OperationsCenterFilter,
): OperationsIncident[] {
  return rows.filter((row) => sameScope(row, filter))
}

export function filterOperationsTimeline(
  rows: OperationsTimelineItem[],
  filter: OperationsCenterFilter,
): OperationsTimelineItem[] {
  return rows.filter((row) => sameScope(row, filter))
}
