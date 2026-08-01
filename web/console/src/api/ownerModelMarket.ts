import { apiClient } from './client'
import type { IntelligenceFreshness, IntelligencePriceDimension } from './upstreamIntelligence'

export type OwnerModelMarketStatus = 'ready' | 'price_only' | 'insufficient_evidence'

export interface OwnerModelMarketPrice {
  dimension: IntelligencePriceDimension
  currency: string
  per_tokens: number
  minimum_cost: string | null
  maximum_cost: string | null
  trusted_option_count: number
}

export interface OwnerModelMarketQuality {
  quality_score: string | null
  success_rate: string | null
  ttft_p95_ms: string | null
  duration_p95_ms: string | null
  sample_count: number
  health_state: string
  freshness: IntelligenceFreshness
  on_frontier: boolean
  effective_cost: string | null
  currency: string
  per_tokens: number
  dimension: IntelligencePriceDimension
}

export interface OwnerModelMarketModel {
  model_key: string
  status: OwnerModelMarketStatus
  prices: OwnerModelMarketPrice[]
  observed_offer_count: number
  comparable_offer_count: number
  quality_option_count: number
  frontier_option_count: number
  freshest_evidence: IntelligenceFreshness | ''
  best_quality: OwnerModelMarketQuality | null
}

export interface OwnerModelMarket {
  fact_version: number
  generated_at: string
  metrics: {
    model_count: number
    ready_model_count: number
    price_only_model_count: number
    insufficient_evidence_model_count: number
    comparable_offer_count: number
    quality_covered_model_count: number
  }
  models: OwnerModelMarketModel[]
  returned_count: number
  truncated: boolean
}

export interface OwnerModelMarketFilter {
  q?: string
  price_dimension?: IntelligencePriceDimension
  limit?: number
}

export const ownerModelMarketApi = {
  get: (filter: OwnerModelMarketFilter = {}) =>
    apiClient.request<OwnerModelMarket>('/owner/model-market', {
      query: {
        q: filter.q,
        price_dimension: filter.price_dimension,
        limit: filter.limit,
      },
    }),
}
