import { describe, expect, it } from 'vitest'
import type { Instance } from '../api/types'
import { instancesForLocation } from './instanceLocation'

const instances = [{ id: 'one' }, { id: 'two' }] as Instance[]

describe('instancesForLocation', () => {
  it('limits the table to the requested instance when it exists', () => {
    expect(instancesForLocation(instances, 'two')).toEqual({
      items: [instances[1]],
      requested: true,
      found: true,
    })
  })

  it('keeps the full list and reports an invalid location', () => {
    expect(instancesForLocation(instances, 'missing')).toEqual({
      items: instances,
      requested: true,
      found: false,
    })
    expect(instancesForLocation(instances)).toEqual({
      items: instances,
      requested: false,
      found: false,
    })
  })
})
