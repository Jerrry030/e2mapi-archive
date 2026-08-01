import { apiClient } from './client'

export type IntelligenceAccuracy = 'exact' | 'derived' | 'estimated' | 'unknown' | 'unattributed'
export type IntelligenceCoverage = 'complete' | 'partial' | 'unavailable'
export type IntelligenceFreshness = 'current' | 'stale' | 'expired'
export type IntelligenceWindow = '24h' | '7d'
export type IntelligencePriceDimension = 'input' | 'output' | 'cached_input' | 'request'

export interface IntelligenceMetadata {
  fact_version: number
  generated_at: string
}

export interface IntelligenceSource {
  id: string
  mode: 'owned' | 'external'
  provider: string
  display_name: string
  currency?: string
  status: 'active' | 'paused' | 'disconnected'
  capabilities: { balance: boolean; groups: boolean; rates: boolean; prices: boolean }
  freshness: IntelligenceFreshness | null
  last_run_at: string | null
  last_success_at: string | null
  next_poll_at: string | null
  last_coverage?: IntelligenceCoverage
  last_error_code?: string
}

export interface IntelligenceEvidence {
  accuracy: IntelligenceAccuracy
  coverage: IntelligenceCoverage
  freshness: IntelligenceFreshness
  confidence: string | null
  observed_at: string
  effective_at?: string | null
  received_at: string
  fresh_until: string
  missing_fields: string[]
  reason_code?: string
}

export interface IntelligenceWallet {
  observation_id: string
  source: IntelligenceSource
  balance_amount: string | null
  unit_kind: 'fiat' | 'credit' | 'unknown'
  currency?: string
  evidence: IntelligenceEvidence
}

export interface IntelligenceRate {
  observation_id: string
  source: IntelligenceSource
  group_key: string
  model_key: string
  price_dimension: IntelligencePriceDimension
  settlement_currency?: string
  per_tokens: number
  group_multiplier: string | null
  recharge_yield: string | null
  published_unit_price: string | null
  effective_multiplier: string | null
  effective_unit_cost: string | null
  formula_version?: string
  evidence: IntelligenceEvidence
  comparable: boolean
  comparability_reason?: string
}

export interface IntelligenceChange {
  id: string
  source: IntelligenceSource
  event_type: string
  before_observation_id?: string
  after_observation_id?: string
  absolute_change: string | null
  percentage_change: string | null
  first_detected_at: string
  confirmed_at: string
  severity: 'info' | 'warning' | 'critical'
  impact_scope?: Record<string, string>
  group_key?: string
  model_key?: string
  price_dimension?: IntelligencePriceDimension
}

export interface IntelligenceFrontierPoint {
  rate: IntelligenceRate
  link_state: 'linked' | 'unlinked'
  channel_id?: string
  quality_score: string | null
  quality_evidence: {
    snapshot_id: string
    window: '1m' | '5m' | '30m' | '24h'
    quality_sample_count: number
    minimum_sample_count: number
    success_rate: string | null
    ttft_p95_ms: string | null
    duration_p95_ms: string | null
    health_state: string
    observed_at: string
    fresh_until: string
    freshness: IntelligenceFreshness
  } | null
  status: 'eligible' | 'blocked'
  blocked_reasons: string[]
  on_frontier: boolean
}

export type IntelligenceLinkScope = 'source_identity' | 'channel'
export type IntelligenceLinkStatus = 'active' | 'inactive'

export interface IntelligenceLink {
  id: string
  intelligence_source_id: string
  link_scope: IntelligenceLinkScope
  channel_id: string
  price_dimension: IntelligencePriceDimension
  status: IntelligenceLinkStatus
  verified_at: string | null
  created_at: string
  updated_at: string
}

export interface IntelligenceLinkInput {
  id?: string
  user_id: number
  intelligence_source_id?: string
  link_scope?: IntelligenceLinkScope
  upstream_source_identity?: string
  channel_id?: string
  price_dimension?: IntelligencePriceDimension
  status: IntelligenceLinkStatus
}

export interface IntelligenceOverview extends IntelligenceMetadata {
  metrics: {
    source_count: number
    active_source_count: number
    stale_source_count: number
    expired_source_count: number
    failed_source_count: number
    current_rate_count: number
    comparable_rate_count: number
    fresh_comparable_coverage: string | null
    balance_risk_source_count: number
    changes_24h: number
    changes_7d: number
    next_poll_at: string | null
  }
  wallets: IntelligenceWallet[]
  top_rates: IntelligenceRate[]
  recent_changes: IntelligenceChange[]
  frontier: IntelligenceFrontierPoint[]
}

export interface IntelligencePage<T> extends IntelligenceMetadata {
  items: T[]
  next_cursor?: string
}

export interface IntelligenceSourceFilter {
  status?: IntelligenceSource['status']
  provider?: string
  currency?: string
  accuracy?: IntelligenceAccuracy
  coverage?: IntelligenceCoverage
  freshness?: IntelligenceFreshness
  fact_version?: number
  cursor?: string
  limit?: number
}

export interface IntelligenceRun {
  id: string
  trigger: 'scheduled' | 'manual' | 'task'
  status: 'succeeded' | 'partial' | 'failed'
  coverage: IntelligenceCoverage
  started_at: string
  observed_at: string
  received_at: string
  completed_at: string | null
  fact_count: number
  page_count: number
  error_code?: string
  retryable: boolean
}

export interface IntelligenceEvidenceDetail extends IntelligenceMetadata {
  id: string
  kind: 'wallet' | 'offer' | 'change'
  source: IntelligenceSource
  run: IntelligenceRun | null
  wallet: IntelligenceWallet | null
  offer: IntelligenceRate | null
  change: IntelligenceChange | null
}

export interface IntelligenceOverviewFilter {
  source_id?: string
  model?: string
  group?: string
  provider?: string
  currency?: string
  window?: IntelligenceWindow
  accuracy?: IntelligenceAccuracy
  fact_version?: number
}

export interface IntelligenceRateFilter extends IntelligenceOverviewFilter {
  price_dimension?: IntelligencePriceDimension
  coverage?: IntelligenceCoverage
  freshness?: IntelligenceFreshness
  comparable?: boolean
  cursor?: string
  limit?: number
}

export interface IntelligenceChangeFilter {
  source_id?: string
  model?: string
  group?: string
  event_type?: string
  severity?: IntelligenceChange['severity']
  window?: IntelligenceWindow
  cursor?: string
  limit?: number
}

function overviewQuery(userId: number, filter: IntelligenceOverviewFilter = {}) {
  return {
    user_id: userId,
    source_id: filter.source_id,
    model_key: filter.model,
    group_key: filter.group,
    provider: filter.provider,
    currency: filter.currency,
    window: filter.window,
    accuracy: filter.accuracy,
    fact_version: filter.fact_version,
  }
}

function rateQuery(userId: number, filter: IntelligenceRateFilter = {}) {
  return {
    user_id: userId,
    source_id: filter.source_id,
    model_key: filter.model,
    group_key: filter.group,
    provider: filter.provider,
    currency: filter.currency,
    price_dimension: filter.price_dimension,
    accuracy: filter.accuracy,
    coverage: filter.coverage,
    freshness: filter.freshness,
    fact_version: filter.fact_version,
    comparable: filter.comparable,
    cursor: filter.cursor,
    limit: filter.limit,
  }
}

function frontierQuery(userId: number, filter: IntelligenceRateFilter = {}) {
  return {
    user_id: userId,
    source_id: filter.source_id,
    model_key: filter.model,
    group_key: filter.group,
    provider: filter.provider,
    currency: filter.currency,
    price_dimension: filter.price_dimension,
    freshness: filter.freshness,
    cursor: filter.cursor,
    limit: filter.limit,
  }
}

function changeQuery(userId: number, filter: IntelligenceChangeFilter = {}) {
  return {
    user_id: userId,
    source_id: filter.source_id,
    model_key: filter.model,
    group_key: filter.group,
    event_type: filter.event_type,
    severity: filter.severity,
    window: filter.window,
    cursor: filter.cursor,
    limit: filter.limit,
  }
}

function sourceQuery(userId: number, filter: IntelligenceSourceFilter = {}) {
  return { user_id: userId, ...filter }
}

export const upstreamIntelligenceApi = {
  sources: (userId: number, filter?: IntelligenceSourceFilter) =>
    apiClient.request<IntelligencePage<IntelligenceSource>>('/upstream-intelligence/sources', {
      query: sourceQuery(userId, filter),
    }),
  overview: (userId: number, filter?: IntelligenceOverviewFilter) =>
    apiClient.request<IntelligenceOverview>('/upstream-intelligence/overview', {
      query: overviewQuery(userId, filter),
    }),
  rates: (userId: number, filter?: IntelligenceRateFilter) =>
    apiClient.request<IntelligencePage<IntelligenceRate>>('/upstream-intelligence/rates', {
      query: rateQuery(userId, filter),
    }),
  changes: (userId: number, filter?: IntelligenceChangeFilter) =>
    apiClient.request<IntelligencePage<IntelligenceChange>>('/upstream-intelligence/changes', {
      query: changeQuery(userId, filter),
    }),
  frontier: (userId: number, filter?: IntelligenceRateFilter) =>
    apiClient.request<IntelligencePage<IntelligenceFrontierPoint>>(
      '/upstream-intelligence/frontier',
      { query: frontierQuery(userId, filter) },
    ),
  links: (userId: number) =>
    apiClient.request<IntelligencePage<IntelligenceLink>>('/upstream-intelligence/links', {
      query: { user_id: userId },
    }),
  createLink: (input: IntelligenceLinkInput) =>
    apiClient.request<IntelligenceLink>('/upstream-intelligence/links', {
      method: 'POST',
      body: input,
    }),
  updateLink: (id: string, input: IntelligenceLinkInput) =>
    apiClient.request<IntelligenceLink>(`/upstream-intelligence/links/${encodeURIComponent(id)}`, {
      method: 'PUT',
      body: input,
    }),
  evidence: (userId: number, id: string) =>
    apiClient.request<IntelligenceEvidenceDetail>(
      `/upstream-intelligence/evidence/${encodeURIComponent(id)}`,
      { query: { user_id: userId } },
    ),
}
