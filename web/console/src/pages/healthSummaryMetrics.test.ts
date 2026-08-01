import { describe, expect, it } from 'vitest'
import type { InstanceHealthSnapshot } from '../api/types'
import { healthPercent, healthSummaryMetrics } from './healthSummaryMetrics'

describe('health summary metrics', () => {
  it('keeps missing account data unknown instead of reporting 100 percent healthy', () => {
    expect(healthPercent(0, 0)).toBeUndefined()
    expect(healthSummaryMetrics([])).toEqual({
      healthPercent: undefined,
      scheduledAccounts: 0,
      totalAccounts: 0,
      unhealthyAccounts: 0,
    })
  })

  it('aggregates health and scheduling across snapshots', () => {
    const snapshots = [
      {
        instance_id: 'instance-1',
        checked_at: '2026-07-12T00:00:00Z',
        total_accounts: 2,
        healthy_count: 1,
        schedulable_count: 1,
        accounts: [
          { account_id: 'healthy', schedulable: true, healthy: true, fail_streak: 0 },
          { account_id: 'unhealthy', schedulable: false, healthy: false, fail_streak: 2 },
        ],
      },
    ] satisfies InstanceHealthSnapshot[]

    expect(healthSummaryMetrics(snapshots)).toEqual({
      healthPercent: 50,
      scheduledAccounts: 1,
      totalAccounts: 2,
      unhealthyAccounts: 1,
    })
  })
})
