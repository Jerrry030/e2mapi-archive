export type HybridResourceClass = 'owner' | 'economy' | 'stable'

export interface HybridAllocationRule {
  owner_percent: number
  economy_percent: number
  stable_percent: number
  owner_burst_max: number
  economy_burst_max: number
  stable_burst_max: number
}

export interface HybridModelAllocation {
  model: string
  rule: HybridAllocationRule
}

export interface HybridAllocation {
  user_id: number
  instance_id: string
  basis: 'requests'
  default_rule: HybridAllocationRule
  model_overrides: HybridModelAllocation[]
  daily_budget_micros: number
  max_unit_price_micros: number
  routing_generation: number
  version: number
  created_at: string
  updated_at: string
}

export interface HybridAllocationInput {
  basis: 'requests'
  default_rule: HybridAllocationRule
  model_overrides: HybridModelAllocation[]
  daily_budget_micros: number
  max_unit_price_micros: number
  expected_version: number
}

export interface HybridRoutingExecution {
  id: string
  user_id: number
  instance_id: string
  allocation_version: number
  generation: number
  model?: string
  status: 'pending' | 'applying' | 'succeeded' | 'failed'
  target: Record<HybridResourceClass, number>
  effective: Record<HybridResourceClass, number>
  actual: Record<HybridResourceClass, number>
  adjustment_codes: string[]
  error_code?: string
  attempts: number
  created_at: string
  updated_at: string
  completed_at?: string
}
