import { describe, expect, it } from 'vitest'
import { selectedPlanFromLocation, upstreamLocationFromSearch } from './upstreamLocation'

describe('upstreamLocationFromSearch', () => {
  it('reads a valid plan deep link', () => {
    expect(upstreamLocationFromSearch('?tab=plans&plan_id=plan-2')).toEqual({
      tab: 'plans',
      planId: 'plan-2',
    })
  })

  it('falls back from invalid tabs and ignores plan IDs outside the plans tab', () => {
    expect(upstreamLocationFromSearch('?tab=unknown&plan_id=plan-2')).toEqual({
      tab: 'plans',
      planId: 'plan-2',
    })
    expect(upstreamLocationFromSearch('?tab=channels&plan_id=plan-2')).toEqual({
      tab: 'channels',
      planId: undefined,
    })
  })
})

describe('selectedPlanFromLocation', () => {
  it('selects only a plan that is present in the loaded result', () => {
    expect(selectedPlanFromLocation('plan-2', ['plan-1', 'plan-2'])).toBe('plan-2')
    expect(selectedPlanFromLocation('missing', ['plan-1', 'plan-2'])).toBeUndefined()
  })
})
