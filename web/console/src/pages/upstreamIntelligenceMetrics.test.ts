import { describe, expect, it } from 'vitest'
import { formatFreshComparableCoverage } from './upstreamIntelligenceMetrics'

describe('upstream intelligence metrics', () => {
  it('converts the 0..1 coverage ratio to an exact percentage string', () => {
    expect(formatFreshComparableCoverage('0')).toBe('0')
    expect(formatFreshComparableCoverage('0.25')).toBe('25')
    expect(formatFreshComparableCoverage('0.333333333333333333')).toBe('33.3333333333333333')
    expect(formatFreshComparableCoverage('1')).toBe('100')
    expect(formatFreshComparableCoverage(null)).toBeNull()
  })
})
