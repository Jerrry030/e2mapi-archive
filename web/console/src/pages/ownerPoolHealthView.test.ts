import { describe, expect, it } from 'vitest'
import type { OwnerPoolHealth } from '../api/types'
import {
  formatMilliseconds,
  formatSuccessRate,
  incidentTone,
  ownerPoolAvailability,
  ownerPoolServiceState,
  switchTone,
} from './ownerPoolHealthView'

function summary(published: number, schedulable: number): OwnerPoolHealth {
  return {
    capacity: {
      published,
      schedulable,
      isolated: 0,
      awaiting_verification: 0,
      verification_failed: 0,
    },
    sla: {
      window: '5m',
      success_rate: null,
      ttft_p95_ms: null,
      duration_p95_ms: null,
      sample_count: 0,
      updated_at: null,
    },
    incidents: [],
    switches: [],
    generated_at: '2026-07-14T00:00:00Z',
  }
}

describe('owner pool health presentation', () => {
  it('derives availability from published and schedulable capacity', () => {
    expect(ownerPoolAvailability(summary(4, 3))).toBe(75)
    expect(ownerPoolAvailability(summary(0, 0))).toBeUndefined()
  })

  it('does not call manually disabled or failed capacity healthy', () => {
    expect(ownerPoolServiceState(summary(0, 0))).toBe('empty')
    expect(ownerPoolServiceState(summary(2, 0))).toBe('fail_closed')
    expect(ownerPoolServiceState(summary(2, 1))).toBe('partially_unavailable')
    expect(ownerPoolServiceState(summary(2, 2))).toBe('healthy')

    const isolated = summary(2, 1)
    isolated.capacity.isolated = 1
    expect(ownerPoolServiceState(isolated)).toBe('degraded')
  })

  it('distinguishes deployed-but-unverified routes from verified availability', () => {
    const awaiting = summary(2, 0)
    awaiting.capacity.awaiting_verification = 2
    expect(ownerPoolServiceState(awaiting)).toBe('awaiting_verification')

    const failed = summary(2, 0)
    failed.capacity.verification_failed = 1
    expect(ownerPoolServiceState(failed)).toBe('verification_failed')
  })

  it('formats factual SLA values without inventing missing data', () => {
    expect(formatSuccessRate(0.9987)).toBe('99.87%')
    expect(formatSuccessRate(null)).toBe('-')
    expect(formatMilliseconds(850)).toBe('850 ms')
    expect(formatMilliseconds(1250)).toBe('1.25 s')
    expect(formatMilliseconds(null)).toBe('-')
  })

  it('maps incidents and switch outcomes to stable semantic tones', () => {
    expect(incidentTone('isolated')).toBe('error')
    expect(incidentTone('recovering')).toBe('processing')
    expect(incidentTone('needs_ejection')).toBe('warning')
    expect(switchTone('succeeded')).toBe('success')
    expect(switchTone('rolled_back')).toBe('warning')
  })
})
