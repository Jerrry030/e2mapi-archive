import { describe, expect, it } from 'vitest'
import { healthViewFromSearch, searchForHealthView } from './healthView'

describe('health view query state', () => {
  it('uses monitoring as the default and recognizes the overview deep link', () => {
    expect(healthViewFromSearch(new URLSearchParams())).toBe('operations')
    expect(healthViewFromSearch(new URLSearchParams('view=summary'))).toBe('summary')
    expect(healthViewFromSearch(new URLSearchParams('view=unknown'))).toBe('operations')
  })

  it('preserves unrelated query parameters when changing tabs', () => {
    const summary = searchForHealthView(new URLSearchParams('source=legacy'), 'summary')
    expect(summary.toString()).toBe('source=legacy&view=summary')

    const operations = searchForHealthView(summary, 'operations')
    expect(operations.toString()).toBe('source=legacy')
  })
})
