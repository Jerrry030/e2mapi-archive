import { describe, expect, it } from 'vitest'
import { consoleFeatureFlagsFromEnv } from './featureFlags'

describe('console feature flags', () => {
  it('keeps incomplete business modules hidden by default', () => {
    expect(consoleFeatureFlagsFromEnv({})).toEqual({
      billing: false,
      payments: false,
      supply: false,
      hybridSupply: false,
    })
  })

  it('only enables modules through explicit truthy values', () => {
    expect(
      consoleFeatureFlagsFromEnv({
        VITE_E2M_ENABLE_BILLING: 'true',
        VITE_E2M_ENABLE_PAYMENTS: '1',
        VITE_E2M_ENABLE_SUPPLY: 'ON',
        VITE_E2M_ENABLE_HYBRID_SUPPLY: 'yes',
      }),
    ).toEqual({ billing: true, payments: true, supply: true, hybridSupply: true })

    expect(
      consoleFeatureFlagsFromEnv({
        VITE_E2M_ENABLE_BILLING: 'false',
        VITE_E2M_ENABLE_PAYMENTS: '0',
        VITE_E2M_ENABLE_SUPPLY: 'enabled',
        VITE_E2M_ENABLE_HYBRID_SUPPLY: 'false',
      }),
    ).toEqual({ billing: false, payments: false, supply: false, hybridSupply: false })
  })
})
