import { beforeEach, describe, expect, it, vi } from 'vitest'
import {
  upstreamMarginApi,
  toUpstreamMarginBrowserView,
  type UpstreamMarginCostColumn,
  type UpstreamMarginCostReadResponse,
} from './upstreamMargin'

function column(
  fact_count: number,
  amounts: UpstreamMarginCostColumn['amounts'] = [],
): UpstreamMarginCostColumn {
  return { fact_count, amounts }
}

function response(): UpstreamMarginCostReadResponse {
  return {
    window: '24h',
    window_start: '2026-07-25T12:00:00Z',
    window_end: '2026-07-26T12:00:00Z',
    generated_at: '2026-07-26T12:00:00Z',
    costs: {
      exact: column(2, [
        { currency: 'USD', amount: '2' },
        { currency: 'CNY', amount: '7' },
      ]),
      estimated: column(1, [{ currency: 'USD', amount: '0.5' }]),
      unknown: column(1),
      unattributed: column(0),
      expired: column(1),
      exact_fact_count: 1,
      derived_fact_count: 1,
    },
    total_cost_fact_count: 5,
    attributable_cost_fact_count: 3,
    uncovered_cost_fact_count: 2,
    attributable_coverage: '0.6',
    minimum_attributable_coverage: '0.9',
    coverage_gate_passed: false,
    blocked_reasons: ['coverage_below_gate', 'revenue_unavailable'],
  }
}

describe('upstream margin browser-safe mapping', () => {
  beforeEach(() => {
    localStorage.clear()
    vi.restoreAllMocks()
  })

  it('keeps five cost columns and window metadata without revenue or margin money', () => {
    const view = toUpstreamMarginBrowserView(response())
    expect(view.costs.map((item) => item.bucket)).toEqual([
      'exact',
      'estimated',
      'unknown',
      'unattributed',
      'expired',
    ])
    expect(view.costs[0].amounts).toEqual([
      { currency: 'CNY', amount: '7' },
      { currency: 'USD', amount: '2' },
    ])
    expect(view.window).toBe('24h')
    expect(view).not.toHaveProperty('revenue')
    expect(view).not.toHaveProperty('claim')
    expect(view).not.toHaveProperty('marginAmount')
    expect(JSON.stringify(view)).not.toContain('margin_rate')
  })

  it('calls the strict endpoint with owner and whitelist window only', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response(JSON.stringify(response()), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      }),
    )
    await upstreamMarginApi.summary(42, '7d')
    const url = new URL(String(fetchMock.mock.calls[0]?.[0]), 'https://console.example')
    expect(url.pathname).toBe('/api/v1/upstream-intelligence/margin')
    expect(url.searchParams.get('user_id')).toBe('42')
    expect(url.searchParams.get('window')).toBe('7d')
    expect([...url.searchParams.keys()].sort()).toEqual(['user_id', 'window'])
  })

  it('deduplicates blockers without mutating the server payload', () => {
    const input = response()
    input.blocked_reasons = ['revenue_unavailable', 'revenue_unavailable']
    const view = toUpstreamMarginBrowserView(input)
    expect(view.blockedReasons).toEqual(['revenue_unavailable'])
    expect(input.blocked_reasons).toHaveLength(2)
  })
})
