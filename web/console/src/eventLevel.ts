import type { EventLevel, RiskLevel } from './api/types'

// Mirrors contracts.DefaultEventLevel for audit records written before the
// event_level field existed. Operation risk and outcome severity are separate:
// a successful sensitive change is a notice, not a warning.
export function effectiveEventLevel(
  eventLevel: EventLevel | undefined,
  riskLevel: RiskLevel,
  result: string,
): EventLevel {
  if (eventLevel && ['L0', 'L1', 'L2', 'L3'].includes(eventLevel)) return eventLevel
  switch (result) {
    case 'running':
      return 'L0'
    case 'retrying':
    case 'paused':
    case 'rejected':
      return 'L1'
    case 'failed':
      return 'L2'
    default:
      return riskLevel === 'L0' ? 'L0' : 'L1'
  }
}
