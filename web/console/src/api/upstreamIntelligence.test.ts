import { beforeEach, describe, expect, it, vi } from 'vitest'
import { upstreamIntelligenceApi } from './upstreamIntelligence'

describe('upstream intelligence API mapping', () => {
  beforeEach(() => {
    localStorage.clear()
    vi.restoreAllMocks()
  })

  it('maps friendly URL filters to the strict Core query vocabulary', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response(
        JSON.stringify({ fact_version: 1, generated_at: '2026-07-24T00:00:00Z', items: [] }),
        {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        },
      ),
    )
    await upstreamIntelligenceApi.rates(7, {
      model: 'model/a',
      group: 'premium',
      window: '24h',
      accuracy: 'exact',
      comparable: false,
      limit: 25,
    })
    const url = String(fetchMock.mock.calls[0]?.[0])
    expect(url).toContain('/api/v1/upstream-intelligence/rates?')
    const query = new URL(url, 'https://console.example').searchParams
    expect(query.get('user_id')).toBe('7')
    expect(query.get('model_key')).toBe('model/a')
    expect(query.get('group_key')).toBe('premium')
    expect(query.get('accuracy')).toBe('exact')
    expect(query.get('comparable')).toBe('false')
    expect(query.get('model')).toBeNull()
    expect(query.get('group')).toBeNull()
    expect(query.get('window')).toBeNull()
  })
})
