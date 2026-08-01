import type { RouteStrategyType } from './types'

export type OwnerRoutingPreference = 'smart_auto' | 'price_first' | 'speed_first' | 'success_first'

export interface OwnerRoutingPreferenceResult {
  preference: OwnerRoutingPreference
  effective_strategy: RouteStrategyType
}
