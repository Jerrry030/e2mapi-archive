import { describe, expect, it } from 'vitest'
import { channelInputFromForm } from './Upstream'
import { strategyFromForm, strategyValidationError } from './routeStrategyForm'

describe('Connector v2 upstream channel form', () => {
  it('keeps ownership and the configured active probe scope in the request', () => {
    expect(
      channelInputFromForm({
        pool_id: 'pool-1',
        source_id: ' upstream-a ',
        account_ownership: 'owner_provided',
        display_name: 'Owner key',
        status: 'active',
        probe_capability: 'text_stream',
        probe_endpoint_path: '/v1/messages',
      }),
    ).toMatchObject({
      pool_id: 'pool-1',
      source_id: 'upstream-a',
      account_ownership: 'owner_provided',
      probe_capability: 'text_stream',
      probe_endpoint_path: '/v1/messages',
    })
  })

  it('locks ownership during edits and clears a disabled probe scope', () => {
    expect(
      channelInputFromForm(
        {
          pool_id: 'pool-1',
          source_id: 'upstream-a',
          account_ownership: 'owner_provided',
          display_name: 'Managed key',
          status: 'active',
          probe_capability: 'disabled',
          probe_endpoint_path: '/v1/responses',
        },
        'platform_managed',
      ),
    ).toMatchObject({
      account_ownership: 'platform_managed',
      probe_capability: '',
      probe_endpoint_path: '',
    })
  })
})

describe('route strategy administration form', () => {
  it('keeps only the semantic owner for the selected scope', () => {
    const values = {
      name: 'Production default',
      user_id: 7,
      pool_id: 'pool-stale',
      plan_id: 'plan-stale',
      type: 'stability_first' as const,
      auto_apply: true,
      approval_required: false,
      cooldown_seconds: 600,
    }

    expect(strategyFromForm(values, 'user')).toMatchObject({
      scope: 'user',
      user_id: 7,
      pool_id: undefined,
      plan_id: undefined,
    })
    expect(strategyFromForm(values, 'pool')).toMatchObject({
      scope: 'pool',
      pool_id: 'pool-stale',
      user_id: undefined,
      plan_id: undefined,
    })
  })

  it('rejects incomplete weights and inverted success thresholds', () => {
    expect(strategyValidationError({ weight_success: 1 })).toContain('完整填写')
    expect(
      strategyValidationError({
        threshold_target_success_rate: 0.9,
        threshold_floor_success_rate: 0.95,
      }),
    ).toContain('不能高于')
    expect(
      strategyValidationError({
        weight_success: 0.3,
        weight_ttft: 0.2,
        weight_duration: 0.2,
        weight_stability: 0.2,
        weight_cost: 0.1,
      }),
    ).toBeUndefined()
    expect(strategyValidationError({ threshold_eject_score: 101 })).toContain('100')
  })

  it('serializes the quality ejection score', () => {
    expect(
      strategyFromForm(
        {
          type: 'stability_first',
          auto_apply: true,
          threshold_eject_score: 55,
        },
        'plan',
      ).thresholds,
    ).toMatchObject({ eject_score: 55 })
  })
})
