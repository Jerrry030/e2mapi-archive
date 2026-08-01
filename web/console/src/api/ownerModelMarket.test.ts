import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ownerModelMarketApi } from './ownerModelMarket'

describe('owner model market API', () => {
  beforeEach(() => {
    localStorage.clear()
    vi.restoreAllMocks()
  })

  it('calls the owner-only aggregate endpoint without accepting an owner id', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response(
        JSON.stringify({
          fact_version: 1,
          generated_at: '2026-07-28T00:00:00Z',
          metrics: {},
          models: [],
          returned_count: 0,
          truncated: false,
        }),
        { status: 200, headers: { 'Content-Type': 'application/json' } },
      ),
    )

    await ownerModelMarketApi.get({ q: 'gpt', price_dimension: 'input', limit: 50 })

    const url = new URL(String(fetchMock.mock.calls[0]?.[0]), 'https://console.example')
    expect(url.pathname).toBe('/api/v1/owner/model-market')
    expect(url.searchParams.get('q')).toBe('gpt')
    expect(url.searchParams.get('price_dimension')).toBe('input')
    expect(url.searchParams.get('limit')).toBe('50')
    expect(url.searchParams.has('user_id')).toBe(false)
  })
})
