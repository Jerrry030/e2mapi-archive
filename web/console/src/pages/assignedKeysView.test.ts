import { describe, expect, it } from 'vitest'
import { assignedKeyVersionLabel } from './assignedKeysView'

describe('assigned resource view', () => {
  it('formats valid delivery key versions without exposing key material', () => {
    expect(assignedKeyVersionLabel(1)).toBe('v1')
    expect(assignedKeyVersionLabel(9)).toBe('v9')
    expect(assignedKeyVersionLabel(0)).toBe('-')
    expect(assignedKeyVersionLabel(Number.NaN)).toBe('-')
  })
})
