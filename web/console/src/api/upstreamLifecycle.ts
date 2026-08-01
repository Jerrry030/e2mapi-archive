import { apiClient } from './client'

export type InventoryState = 'draft' | 'testing' | 'ready' | 'quarantined' | 'retired'
export type KeyRotationStatus = 'stable' | 'deploying' | 'rolling_back' | 'finalizing'

export interface InventoryPoolSummary {
  pool_id: string
  total: number
  ready: number
  allocated: number
  available: number
  draft: number
  testing: number
  quarantined: number
  retired: number
  proof_unverified: number
  proof_mismatch: number
  deployments_failed: number
  safety_stock_threshold: number
  below_safety_stock: boolean
}

export interface InventoryItem {
  channel: {
    id: string
    pool_id: string
    source_id?: string
    display_name: string
    provider?: string
    status: string
  }
  inventory_state: InventoryState
  allocated: boolean
  allocated_user_id?: number
  first_plan_id?: string
  allocated_at?: string
  delivery?: {
    masked_value: string
    key_version: number
    proof_status: string
  }
  target_instances: number
  proof_verified: number
  proof_mismatch: number
  deployments_deployed: number
  deployments_pending: number
  deployments_failed: number
}

export interface InventorySnapshot {
  pools: InventoryPoolSummary[]
  items: InventoryItem[]
  alerts: Array<{
    pool_id: string
    code: string
    message: string
    available: number
    threshold: number
  }>
  as_of: string
}

export interface InventoryImportEntry {
  source_id: string
  display_name: string
  provider?: string
  value: string
  credential_binding_id?: string
}

export interface KeyRotation {
  channel_id: string
  current_key_version: number
  current_masked_value: string
  previous_key_version?: number
  previous_masked_value?: string
  status: KeyRotationStatus
  target_instances: number
  confirmed_instances: number
  pending_instances?: string[]
  can_finalize: boolean
  can_rollback: boolean
  updated_at: string
}

export interface PoolRetirementJob {
  id: string
  pool_id: string
  status: 'pending' | 'running' | 'partial' | 'finalizing' | 'cleanup' | 'completed'
  total_plans: number
  completed_plans: number
  failed_plans: number
  cleanup_completed_plans: number
  cleanup_failed_plans: number
  last_error?: string
  updated_at: string
}

export const upstreamLifecycleApi = {
  inventory: (poolId?: string) =>
    apiClient.request<InventorySnapshot>('/upstream-inventory', { query: { pool_id: poolId } }),
  importInventory: (poolId: string, entries: InventoryImportEntry[]) =>
    apiClient.request<{ imported: number }>(`/upstream-pools/${poolId}/inventory/import`, {
      method: 'POST',
      body: { entries },
    }),
  setSafetyStock: (poolId: string, threshold: number) =>
    apiClient.request(`/upstream-pools/${poolId}/safety-stock`, {
      method: 'PUT',
      body: { threshold },
    }),
  setInventoryState: (channelId: string, state: InventoryState) =>
    apiClient.request(`/upstream-channels/${channelId}/inventory-state`, {
      method: 'PUT',
      body: { state },
    }),
  rotation: (channelId: string) =>
    apiClient.request<KeyRotation>(`/upstream-channels/${channelId}/key-rotation`),
  startRotation: (channelId: string, value: string) =>
    apiClient.request<KeyRotation>(`/upstream-channels/${channelId}/key-rotation`, {
      method: 'POST',
      body: { value },
    }),
  rollbackRotation: (channelId: string) =>
    apiClient.request<KeyRotation>(`/upstream-channels/${channelId}/key-rotation/rollback`, {
      method: 'POST',
    }),
  finalizeRotation: (channelId: string) =>
    apiClient.request<KeyRotation>(`/upstream-channels/${channelId}/key-rotation/finalize`, {
      method: 'POST',
    }),
  retirementJobs: (poolId?: string) =>
    apiClient.request<PoolRetirementJob[]>('/pool-retirement-jobs', { query: { pool_id: poolId } }),
  createRetirementJob: (poolId: string) =>
    apiClient.request<PoolRetirementJob>(`/upstream-pools/${poolId}/retirement-jobs`, {
      method: 'POST',
    }),
  runRetirementJob: (jobId: string) =>
    apiClient.request<PoolRetirementJob>(`/pool-retirement-jobs/${jobId}/run`, { method: 'POST' }),
}
