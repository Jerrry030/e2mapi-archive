import { apiClient } from './client'
import type { ReconcileActionType } from './types'

export type RecommendationStatus =
  | 'open'
  | 'shadowing'
  | 'ready_for_dry_run'
  | 'dry_running'
  | 'dry_run_passed'
  | 'dry_run_blocked'
  | 'dismissed'
  | 'expired'

export type RecommendationConstraintKind = 'quality' | 'capacity' | 'balance'
export type RecommendationConstraintStatus = 'passed' | 'blocked' | 'unknown'

export interface RecommendationConstraint {
  kind: RecommendationConstraintKind
  status: RecommendationConstraintStatus
  reason_code?: string
  evidence_ids: string[]
  explanation?: string
}

export interface RecommendationCostRange {
  lower: string
  expected: string
  upper: string
}

export interface RecommendationSavingsRange {
  amount_lower: string
  amount_expected: string
  amount_upper: string
  percent_lower: string
  percent_expected: string
  percent_upper: string
}

// This is deliberately the browser projection returned by Core, not the
// persistence contract. Owner identity and affected downstream identities are
// absent so new server-only fields cannot accidentally become console data.
export interface UpstreamRecommendation {
  id: string
  status: RecommendationStatus
  intelligence_fact_version: number
  cost_ledger_fact_version: number
  link_fact_version: number
  plan_generation: number
  from_source_id: string
  from_channel_id: string
  from_group_key: string
  to_source_id: string
  to_channel_id: string
  to_group_key: string
  model_key: string
  price_dimension: 'input' | 'output' | 'cached_input' | 'request'
  settlement_currency: string
  per_tokens: number
  affected_plan_ids: string[]
  evidence_ids: string[]
  constraints: RecommendationConstraint[]
  from_cost: RecommendationCostRange
  to_cost: RecommendationCostRange
  savings: RecommendationSavingsRange
  formula_version: string
  strategy_version: string
  fingerprint: string
  dry_run_id?: string
  created_at: string
  expires_at: string
}

export type RecommendationGenerationReason =
  | 'no_current_facts'
  | 'no_published_plan'
  | 'no_callable_pair'
  | 'missing_verified_link'
  | 'missing_exact_cost'
  | 'incomparable_cost'
  | 'stale_price'
  | 'insufficient_quality'
  | 'insufficient_balance'
  | 'no_proven_savings'

export interface RecommendationGenerationDiagnostic {
  reason: RecommendationGenerationReason
  count: number
}

export interface RecommendationGenerationResult {
  recommendations: UpstreamRecommendation[]
  blocked: RecommendationGenerationDiagnostic[]
}

export interface ShadowCandidate {
  source_id: string
  channel_id: string
  group_key: string
  model_key: string
  price_dimension: UpstreamRecommendation['price_dimension']
  settlement_currency: string
  per_tokens: number
  cost: string
  quality_score: string
  constraints: RecommendationConstraint[]
  evidence_ids: string[]
}

export interface ShadowResult {
  id: string
  recommendation_id: string
  recommendation_fingerprint: string
  winner: ShadowCandidate
  ranking: ShadowCandidate[]
  evidence_ids: string[]
  evaluated_at: string
}

export interface DryRunSchedulingIntent {
  channel_id: string
  enabled: boolean
}

export interface DryRunAction {
  type: ReconcileActionType
  channel_id: string
}

export interface DryRunResult {
  id: string
  recommendation_id: string
  recommendation_fingerprint: string
  intelligence_fact_version: number
  cost_ledger_fact_version: number
  link_fact_version: number
  plan_generation: number
  plan_id: string
  from_channel_id: string
  to_channel_id: string
  desired_scheduling: DryRunSchedulingIntent[]
  actions: DryRunAction[]
  action_fingerprint: string
  created_at: string
}

export interface RecommendationExperimentResult<TExperiment> {
  recommendation: UpstreamRecommendation
  experiment: TExperiment
}

export type RecommendationExecutionScope = 'plan' | 'pool'

export interface RecommendationExecutionPolicy {
  id: string
  user_id: number
  scope: RecommendationExecutionScope
  plan_id?: string
  pool_id?: string
  enabled: boolean
  kill_switch: boolean
  daily_execution_cap: number
  cooldown_seconds: number
  minimum_savings: string
  version: number
  created_at: string
  updated_at: string
}

export interface RecommendationExecutionPolicyInput {
  user_id: number
  scope: RecommendationExecutionScope
  plan_id?: string
  pool_id?: string
  enabled: boolean
  kill_switch: boolean
  daily_execution_cap: number
  cooldown_seconds: number
  minimum_savings: string
  expected_version: number
}

function ownerQuery(userId: number) {
  return { user_id: userId }
}

function experimentQuery(userId: number, recommendationId?: string) {
  return { user_id: userId, recommendation_id: recommendationId, limit: 100 }
}

export const recommendationLabApi = {
  recommendations: (userId: number, status?: RecommendationStatus) =>
    apiClient.request<UpstreamRecommendation[]>('/upstream-intelligence/recommendations', {
      query: { ...ownerQuery(userId), status, limit: 100 },
    }),
  recommendation: (userId: number, recommendationId: string) =>
    apiClient.request<UpstreamRecommendation>(
      `/upstream-intelligence/recommendations/${encodeURIComponent(recommendationId)}`,
      { query: ownerQuery(userId) },
    ),
  generate: (userId: number) =>
    apiClient.request<RecommendationGenerationResult>(
      '/upstream-intelligence/recommendations/generate',
      { method: 'POST', query: ownerQuery(userId), body: {} },
    ),
  runShadow: (userId: number, recommendationId: string) =>
    apiClient.request<RecommendationExperimentResult<ShadowResult>>(
      `/upstream-intelligence/recommendations/${encodeURIComponent(recommendationId)}/shadow`,
      { method: 'POST', query: ownerQuery(userId), body: {} },
    ),
  runDryRun: (userId: number, recommendationId: string) =>
    apiClient.request<RecommendationExperimentResult<DryRunResult>>(
      `/upstream-intelligence/recommendations/${encodeURIComponent(recommendationId)}/dry-run`,
      { method: 'POST', query: ownerQuery(userId), body: {} },
    ),
  shadows: (userId: number, recommendationId?: string) =>
    apiClient.request<ShadowResult[]>('/upstream-intelligence/experiments/shadows', {
      query: experimentQuery(userId, recommendationId),
    }),
  dryRuns: (userId: number, recommendationId?: string) =>
    apiClient.request<DryRunResult[]>('/upstream-intelligence/experiments/dry-runs', {
      query: experimentQuery(userId, recommendationId),
    }),
  executionPolicies: (userId: number) =>
    apiClient.request<RecommendationExecutionPolicy[]>(
      '/upstream-intelligence/execution-policies',
      { query: ownerQuery(userId) },
    ),
  saveExecutionPolicy: (input: RecommendationExecutionPolicyInput) =>
    apiClient.request<RecommendationExecutionPolicy>('/upstream-intelligence/execution-policies', {
      method: 'PUT',
      body: input,
    }),
}
