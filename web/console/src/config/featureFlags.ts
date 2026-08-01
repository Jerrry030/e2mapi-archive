export interface ConsoleFeatureFlags {
  billing: boolean
  payments: boolean
  supply: boolean
  hybridSupply: boolean
}

const ENABLED_VALUES = new Set(['1', 'true', 'yes', 'on'])

function enabled(value: unknown): boolean {
  return typeof value === 'string' && ENABLED_VALUES.has(value.trim().toLowerCase())
}

export function consoleFeatureFlagsFromEnv(env: Record<string, unknown>): ConsoleFeatureFlags {
  return {
    billing: enabled(env.VITE_E2M_ENABLE_BILLING),
    payments: enabled(env.VITE_E2M_ENABLE_PAYMENTS),
    supply: enabled(env.VITE_E2M_ENABLE_SUPPLY),
    hybridSupply: enabled(env.VITE_E2M_ENABLE_HYBRID_SUPPLY),
  }
}

/**
 * These modules are intentionally opt-in while their business loops are incomplete.
 * Vite injects the values at build time; an omitted value always means disabled.
 */
export const consoleFeatureFlags = consoleFeatureFlagsFromEnv(import.meta.env)
