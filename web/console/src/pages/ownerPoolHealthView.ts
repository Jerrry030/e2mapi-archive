import type { OwnerPoolHealth, OwnerPoolIncidentStatus, OwnerPoolSwitchOutcome } from '../api/types'

export type OwnerPoolServiceState =
  | 'empty'
  | 'awaiting_verification'
  | 'verification_failed'
  | 'fail_closed'
  | 'degraded'
  | 'partially_unavailable'
  | 'healthy'

export function ownerPoolAvailability(summary: OwnerPoolHealth): number | undefined {
  if (summary.capacity.published <= 0) return undefined
  return Math.round((summary.capacity.schedulable / summary.capacity.published) * 100)
}

export function ownerPoolServiceState(summary: OwnerPoolHealth): OwnerPoolServiceState {
  if (summary.capacity.published <= 0) return 'empty'
  if (summary.capacity.schedulable <= 0 && summary.capacity.verification_failed > 0)
    return 'verification_failed'
  if (summary.capacity.schedulable <= 0 && summary.capacity.awaiting_verification > 0)
    return 'awaiting_verification'
  if (summary.capacity.schedulable <= 0) return 'fail_closed'
  if (summary.capacity.isolated > 0) return 'degraded'
  if (summary.capacity.schedulable < summary.capacity.published) return 'partially_unavailable'
  return 'healthy'
}

export function formatSuccessRate(value: number | null): string {
  if (value === null || !Number.isFinite(value)) return '-'
  return `${(value * 100).toFixed(2)}%`
}

export function formatMilliseconds(value: number | null): string {
  if (value === null || !Number.isFinite(value)) return '-'
  if (value >= 1000) return `${(value / 1000).toFixed(value >= 10_000 ? 1 : 2)} s`
  return `${Math.round(value)} ms`
}

export function incidentTone(
  status: OwnerPoolIncidentStatus,
): 'error' | 'warning' | 'processing' | 'default' {
  switch (status) {
    case 'isolated':
    case 'delivery_failed':
      return 'error'
    case 'needs_ejection':
      return 'warning'
    case 'recovering':
      return 'processing'
    default:
      return 'default'
  }
}

export function switchTone(
  result: OwnerPoolSwitchOutcome,
): 'success' | 'error' | 'warning' | 'processing' | 'default' {
  switch (result) {
    case 'succeeded':
      return 'success'
    case 'failed':
      return 'error'
    case 'rolled_back':
    case 'skipped':
      return 'warning'
    case 'pending':
    case 'in_progress':
      return 'processing'
    default:
      return 'default'
  }
}
