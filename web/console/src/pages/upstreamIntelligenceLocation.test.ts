import { describe, expect, it } from 'vitest'
import { readIntelligenceLocation, writeIntelligenceLocation } from './upstreamIntelligenceLocation'

describe('upstream intelligence URL state', () => {
  it('round-trips shareable filters and evidence drawer state', () => {
    const encoded = writeIntelligenceLocation({
      tab: 'rates',
      userId: 42,
      source_id: 'src-1',
      model: 'gpt-5',
      group: 'premium',
      provider: 'sub2api',
      currency: 'USD',
      window: '7d',
      accuracy: 'derived',
      evidenceId: 'offer-1',
    })
    expect(readIntelligenceLocation(encoded)).toEqual({
      tab: 'rates',
      userId: 42,
      source_id: 'src-1',
      model: 'gpt-5',
      group: 'premium',
      provider: 'sub2api',
      currency: 'USD',
      window: '7d',
      accuracy: 'derived',
      evidenceId: 'offer-1',
    })
  })

  it('fails closed to safe defaults for unsupported enums and owner ids', () => {
    const location = readIntelligenceLocation(
      new URLSearchParams('tab=admin&window=forever&evidence=perfect&user_id=-1'),
    )
    expect(location.tab).toBe('overview')
    expect(location.window).toBe('24h')
    expect(location.accuracy).toBeUndefined()
    expect(location.userId).toBeUndefined()
  })

  it('round-trips the cost and margin guardrail tab', () => {
    const encoded = writeIntelligenceLocation({ tab: 'margin', userId: 42, window: '7d' })
    expect(readIntelligenceLocation(encoded).tab).toBe('margin')
    expect(readIntelligenceLocation(encoded).window).toBe('7d')
  })

  it.each(['recommendations', 'execution', 'rollouts'] as const)(
    'round-trips the %s closed-loop admin tab',
    (tab) => {
      const encoded = writeIntelligenceLocation({ tab, userId: 42, window: '24h' })
      expect(readIntelligenceLocation(encoded).tab).toBe(tab)
    },
  )
})
