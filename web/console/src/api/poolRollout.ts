import { apiClient } from './client'

export type PoolRolloutScope = 'user' | 'instance'
export type PoolRolloutMode = 'immediate' | 'canary' | 'batched'

export interface PoolRolloutTarget {
  id: string
  pool_id: string
  scope: PoolRolloutScope
  user_id: number
  instance_id?: string
  enabled: boolean
  rollout: PoolRolloutMode
  rollout_batch_size?: number
  rollout_canary_count?: number
  note?: string
  created_at: string
  updated_at: string
}

export interface PoolRolloutResolution {
  pool_id: string
  user_id: number
  instance_id: string
  enabled: boolean
  source?: PoolRolloutScope
  target_id?: string
  rollout: PoolRolloutMode
  rollout_batch_size?: number
  rollout_canary_count?: number
}

export type PoolRolloutOperationAction = 'drain' | 'publish'
export type PoolRolloutOperationStatus =
  'pending' | 'running' | 'succeeded' | 'failed' | 'superseded'

export interface PoolRolloutOperation {
  id: string
  pool_id: string
  user_id: number
  instance_id: string
  plan_id?: string
  target_id?: string
  action: PoolRolloutOperationAction
  status: PoolRolloutOperationStatus
  desired_fingerprint: string
  attempts: number
  last_error?: string
  version: number
  lease_owner?: string
  lease_until?: string
  created_at: string
  updated_at: string
}

export interface PoolRolloutPreview {
  pool_id: string
  targets: PoolRolloutTarget[]
  instances: PoolRolloutResolution[]
  operations: PoolRolloutOperation[]
}

export interface PoolRolloutTargetInput {
  scope: PoolRolloutScope
  user_id: number
  instance_id?: string
  enabled: boolean
  rollout: PoolRolloutMode
  rollout_batch_size?: number
  rollout_canary_count?: number
  note?: string
}

export const poolRolloutEndpoints = {
  preview: (poolId: string) =>
    apiClient.request<PoolRolloutPreview>(
      `/upstream-pools/${encodeURIComponent(poolId)}/rollout-targets`,
    ),
  upsert: (poolId: string, input: PoolRolloutTargetInput) =>
    apiClient.request<PoolRolloutTarget>(
      `/upstream-pools/${encodeURIComponent(poolId)}/rollout-targets`,
      { method: 'PUT', body: input },
    ),
  remove: (
    poolId: string,
    input: Pick<PoolRolloutTargetInput, 'scope' | 'user_id' | 'instance_id'>,
  ) =>
    apiClient.request<void>(`/upstream-pools/${encodeURIComponent(poolId)}/rollout-targets`, {
      method: 'DELETE',
      query: { scope: input.scope, user_id: input.user_id, instance_id: input.instance_id },
    }),
}
