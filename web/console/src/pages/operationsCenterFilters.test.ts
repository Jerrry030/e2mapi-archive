import { describe, expect, it } from 'vitest'
import type { OperationsIncident, OperationsOnboarding, OperationsTimelineItem } from '../api/types'
import {
  filterOperationsIncidents,
  filterOperationsOnboarding,
  filterOperationsTimeline,
} from './operationsCenterFilters'

const onboarding = [
  { id: 'a', user_id: 1, instance_id: 'i1', status: 'retryable' },
  { id: 'b', user_id: 1, instance_id: 'i2', status: 'active' },
  { id: 'c', user_id: 2, instance_id: 'i3', status: 'running' },
] as OperationsOnboarding[]

describe('operations center filters', () => {
  it('scopes onboarding to an owner, instance, and attention state', () => {
    expect(filterOperationsOnboarding(onboarding, { userId: 1 }).map((row) => row.id)).toEqual([
      'a',
      'b',
    ])
    expect(
      filterOperationsOnboarding(onboarding, { userId: 1, status: 'attention' }).map(
        (row) => row.id,
      ),
    ).toEqual(['a'])
    expect(filterOperationsOnboarding(onboarding, { instanceId: 'i2' })[0]?.id).toBe('b')
  })

  it('uses the same owner and instance scope for incidents and timeline', () => {
    const incidents = [
      { plan_id: 'p1', channel_id: 'c1', user_id: 1, instance_id: 'i1' },
      { plan_id: 'p2', channel_id: 'c2', user_id: 2, instance_id: 'i2' },
    ] as OperationsIncident[]
    const timeline = [
      { id: 't1', user_id: 1, instance_id: 'i1' },
      { id: 't2', user_id: 2, instance_id: 'i2' },
    ] as OperationsTimelineItem[]
    expect(filterOperationsIncidents(incidents, { userId: 1 })).toHaveLength(1)
    expect(filterOperationsTimeline(timeline, { instanceId: 'i2' })[0]?.id).toBe('t2')
  })
})
