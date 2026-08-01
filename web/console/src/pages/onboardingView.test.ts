import { describe, expect, it } from 'vitest'
import type { OwnerOnboardingInstance } from '../api/types'
import {
  onboardingNeedsUserAction,
  onboardingProgress,
  onboardingStateCopy,
} from './onboardingView'

function instance(overrides: Partial<OwnerOnboardingInstance>): OwnerOnboardingInstance {
  return {
    instance_id: 'inst-1',
    instance_name: 'Production',
    instance_kind: 'sub2api',
    connector_state: 'ready',
    service_state: 'provisioning',
    workflow_count: 1,
    ready_workflows: 0,
    delivered_keys: 1,
    verified_keys: 0,
    published_bindings: 0,
    active_bindings: 0,
    callable_bindings: 0,
    awaiting_verification_bindings: 0,
    verification_failed_bindings: 0,
    updated_at: '2026-07-21T00:00:00Z',
    ...overrides,
  }
}

describe('owner onboarding presentation', () => {
  it('keeps Connector mandatory and distinguishes user action from platform work', () => {
    expect(onboardingNeedsUserAction('awaiting_connector')).toBe(true)
    expect(onboardingNeedsUserAction('gateway_setup_required')).toBe(true)
    expect(onboardingNeedsUserAction('retrying')).toBe(false)
    expect(
      onboardingProgress(
        instance({ connector_state: 'missing', service_state: 'awaiting_connector' }),
      ),
    ).toBe(15)
  })

  it('reports real workflow progress and automatic retry without a manual retry promise', () => {
    expect(onboardingProgress(instance({ stage: 'delivering_bindings' }))).toBe(70)
    expect(onboardingProgress(instance({ service_state: 'active', ready_workflows: 1 }))).toBe(100)
    expect(onboardingStateCopy('retrying').detail).toContain('自动重试')
  })

  it('does not present deployment acknowledgement as an enabled service', () => {
    expect(
      onboardingProgress(
        instance({
          service_state: 'awaiting_verification',
          published_bindings: 1,
          active_bindings: 1,
          awaiting_verification_bindings: 1,
        }),
      ),
    ).toBe(95)
    expect(onboardingStateCopy('awaiting_verification').label).toContain('等待调用验证')
    expect(onboardingStateCopy('verification_failed').detail).toContain('当前不会作为可用线路')
  })
})
