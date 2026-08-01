import { apiClient } from './client'
import type { AutoSwitchDecision, QualityCircuitState } from './types'

export interface OperatorRecoveryNote {
  note?: string
}

export interface QualityCircuitRuntime {
  plan_id: string
  channel_id: string
  state: QualityCircuitState
  opened_at?: string | null
  probe_after?: string | null
  half_open_since?: string | null
  last_probe_at?: string | null
  last_transition_at?: string | null
  open_count: number
  consecutive_probe_successes: number
  last_score: number
  last_reason?: { code?: string; text?: string }
  restore_pending?: boolean
  recovery_ready?: boolean
  recovery_stage?: number
  recovery_stage_started_at?: string | null
  recovery_observe_after?: string | null
  version: number
  created_at: string
  updated_at: string
}

const decisionPath = (decisionId: string, action: 'approve' | 'reject' | 'execute') =>
  `/auto-switch-decisions/${encodeURIComponent(decisionId)}/${action}`

export const recoveryActionEndpoints = {
  approve: (decisionId: string, input: OperatorRecoveryNote = {}) =>
    apiClient.request<AutoSwitchDecision>(decisionPath(decisionId, 'approve'), {
      method: 'POST',
      body: input,
    }),
  reject: (decisionId: string, input: OperatorRecoveryNote = {}) =>
    apiClient.request<AutoSwitchDecision>(decisionPath(decisionId, 'reject'), {
      method: 'POST',
      body: input,
    }),
  execute: (decisionId: string) =>
    apiClient.request<AutoSwitchDecision>(decisionPath(decisionId, 'execute'), {
      method: 'POST',
    }),
  manualRecover: (planId: string, channelId: string, input: OperatorRecoveryNote = {}) =>
    apiClient.request<QualityCircuitRuntime>(
      `/route-plans/${encodeURIComponent(planId)}/channels/${encodeURIComponent(channelId)}/recover`,
      { method: 'POST', body: input },
    ),
}
