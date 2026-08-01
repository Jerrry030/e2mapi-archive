import { apiClient } from './client'

export type RecommendationRolloutStage = 0 | 10 | 25 | 50 | 100

export type RecommendationRolloutStatus =
  'ready' | 'applying' | 'observing' | 'rollback_required' | 'completed' | 'rolled_back' | 'blocked'

export type RecommendationRolloutGateStatus = 'passed' | 'blocked' | 'unknown'

export interface RecommendationRolloutGate {
  status: RecommendationRolloutGateStatus
  reason_codes: string[]
}

export type RecommendationRolloutOperationAction = 'apply_stage' | 'rollback'
export type RecommendationRolloutOperationStatus =
  'pending' | 'running' | 'succeeded' | 'failed' | 'superseded'

export type RecommendationRolloutOperationErrorCode =
  | 'capability_unsupported'
  | 'ownership_lost'
  | 'plan_changed'
  | 'mapping_invalid'
  | 'weight_unknown'
  | 'baseline_changed'
  | 'revalidation_blocked'
  | 'gateway_unavailable'
  | 'write_failed'
  | 'readback_failed'
  | 'verification_failed'
  | 'internal_error'

export interface RecommendationRolloutOperation {
  id: string
  action: RecommendationRolloutOperationAction
  target_stage: RecommendationRolloutStage
  status: RecommendationRolloutOperationStatus
  attempts: number
  error_code?: RecommendationRolloutOperationErrorCode
  created_at: string
  updated_at: string
}

// Browser-safe projection only. Owner identity, gateway account identities,
// exact baseline weights, credentials and raw upstream material are omitted on
// purpose so they cannot become renderable through normal component props.
export interface RecommendationRollout {
  id: string
  recommendation_id: string
  recommendation_fingerprint: string
  plan_id: string
  fact_version: number
  evidence_ids: string[]
  account_count: number
  baseline_fingerprint: string
  baseline_verified: boolean
  scheduling_generation: number
  status: RecommendationRolloutStatus
  stage: RecommendationRolloutStage
  pending_stage: RecommendationRolloutStage
  observe_until?: string
  recommendation_expires_at: string
  rollback_reasons: string[]
  gate: RecommendationRolloutGate
  latest_operation?: RecommendationRolloutOperation
  last_after_verified: boolean
  rollback_verified: boolean
  started_at: string
  updated_at: string
}

export interface RecommendationRolloutFilter {
  status?: RecommendationRolloutStatus
  plan_id?: string
  limit?: number
}

function ownerQuery(userId: number) {
  return { user_id: userId }
}

export const recommendationRolloutApi = {
  list: (userId: number, filter: RecommendationRolloutFilter = {}) =>
    apiClient.request<RecommendationRollout[]>('/upstream-intelligence/rollouts', {
      query: { ...ownerQuery(userId), ...filter },
    }),
  get: (userId: number, rolloutId: string) =>
    apiClient.request<RecommendationRollout>(
      `/upstream-intelligence/rollouts/${encodeURIComponent(rolloutId)}`,
      { query: ownerQuery(userId) },
    ),
  start: (userId: number, recommendationId: string) =>
    apiClient.request<RecommendationRollout>(
      `/upstream-intelligence/recommendations/${encodeURIComponent(recommendationId)}/rollout`,
      { method: 'POST', query: ownerQuery(userId), body: {} },
    ),
  advance: (userId: number, rolloutId: string) =>
    apiClient.request<RecommendationRollout>(
      `/upstream-intelligence/rollouts/${encodeURIComponent(rolloutId)}/advance`,
      { method: 'POST', query: ownerQuery(userId), body: {} },
    ),
  rollback: (userId: number, rolloutId: string) =>
    apiClient.request<RecommendationRollout>(
      `/upstream-intelligence/rollouts/${encodeURIComponent(rolloutId)}/rollback`,
      { method: 'POST', query: ownerQuery(userId), body: {} },
    ),
}
