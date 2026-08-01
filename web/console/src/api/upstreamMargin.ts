import { apiClient } from './client'
import type { IntelligenceWindow } from './upstreamIntelligence'

export type UpstreamMarginCostBucket =
  'exact' | 'estimated' | 'unknown' | 'unattributed' | 'expired'

export type UpstreamMarginBlockedReason =
  'no_cost_facts' | 'coverage_below_gate' | 'revenue_unavailable' | 'cross_currency_without_fx'

export interface UpstreamMarginMoney {
  currency: string
  amount: string
}

export interface UpstreamMarginCostColumn {
  fact_count: number
  amounts: UpstreamMarginMoney[]
  reasons?: Record<string, number>
}

// Browser-safe wire contract. Revenue, accounting claim, owner identity and
// every margin amount/rate are intentionally absent.
export interface UpstreamMarginCostReadResponse {
  window: IntelligenceWindow
  window_start: string
  window_end: string
  generated_at: string
  costs: Record<UpstreamMarginCostBucket, UpstreamMarginCostColumn> & {
    exact_fact_count: number
    derived_fact_count: number
  }
  total_cost_fact_count: number
  attributable_cost_fact_count: number
  uncovered_cost_fact_count: number
  attributable_coverage: string
  minimum_attributable_coverage: string
  coverage_gate_passed: boolean
  blocked_reasons: UpstreamMarginBlockedReason[]
}

export interface UpstreamMarginBrowserView {
  window: IntelligenceWindow
  windowStart: string
  windowEnd: string
  generatedAt: string
  costs: Array<{
    bucket: UpstreamMarginCostBucket
    factCount: number
    amounts: UpstreamMarginMoney[]
    reasons: Record<string, number>
  }>
  exactFactCount: number
  derivedFactCount: number
  totalCostFactCount: number
  attributableCostFactCount: number
  uncoveredCostFactCount: number
  attributableCoverage: string
  minimumAttributableCoverage: string
  coverageGatePassed: boolean
  blockedReasons: UpstreamMarginBlockedReason[]
}

export const upstreamMarginCostBucketOrder = [
  'exact',
  'estimated',
  'unknown',
  'unattributed',
  'expired',
] as const satisfies readonly UpstreamMarginCostBucket[]

export function toUpstreamMarginBrowserView(
  model: UpstreamMarginCostReadResponse,
): UpstreamMarginBrowserView {
  return {
    window: model.window,
    windowStart: model.window_start,
    windowEnd: model.window_end,
    generatedAt: model.generated_at,
    costs: upstreamMarginCostBucketOrder.map((bucket) => ({
      bucket,
      factCount: model.costs[bucket].fact_count,
      amounts: [...model.costs[bucket].amounts].sort((left, right) =>
        left.currency.localeCompare(right.currency),
      ),
      reasons: { ...(model.costs[bucket].reasons ?? {}) },
    })),
    exactFactCount: model.costs.exact_fact_count,
    derivedFactCount: model.costs.derived_fact_count,
    totalCostFactCount: model.total_cost_fact_count,
    attributableCostFactCount: model.attributable_cost_fact_count,
    uncoveredCostFactCount: model.uncovered_cost_fact_count,
    attributableCoverage: model.attributable_coverage,
    minimumAttributableCoverage: model.minimum_attributable_coverage,
    coverageGatePassed: model.coverage_gate_passed,
    blockedReasons: uniqueBlockedReasons(model.blocked_reasons),
  }
}

export const upstreamMarginApi = {
  summary: (userId: number, window: IntelligenceWindow) =>
    apiClient.request<UpstreamMarginCostReadResponse>('/upstream-intelligence/margin', {
      query: { user_id: userId, window },
    }),
}

function uniqueBlockedReasons(reasons: UpstreamMarginBlockedReason[]) {
  return [...new Set(reasons)]
}
