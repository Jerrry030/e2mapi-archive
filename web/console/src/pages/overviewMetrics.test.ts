import { describe, expect, it } from 'vitest'
import { clientOverviewMetrics } from './overviewMetrics'
import type { OwnerOnboarding, OwnerPoolHealth } from '../api/types'

describe('client overview metrics', () => {
  it('uses service facts instead of the legacy instance status field', () => {
    const onboarding = { summary: { active_instances: 2, action_required: 1 } } as OwnerOnboarding
    const health = {
      capacity: { schedulable: 4 },
      incidents: [{ status: 'isolated' }],
    } as OwnerPoolHealth
    expect(clientOverviewMetrics(onboarding, health)).toEqual({
      activeInstances: 2,
      actionRequired: 1,
      schedulableRoutes: 4,
      openIncidents: 1,
    })
  })
})
