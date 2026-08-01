import { describe, expect, it } from 'vitest'
import type { AutoSwitchChannelHealth, AutoSwitchDecision } from '../api/types'
import { setLocale } from '../i18n'
import {
  circuitReasonText,
  circuitStateView,
  decisionDetailText,
  decisionImpactText,
  failureScopeText,
  penaltyBreakdownText,
  recoveryProgressText,
  schedulingStateView,
} from './autoSwitchView'

function channel(overrides: Partial<AutoSwitchChannelHealth>): AutoSwitchChannelHealth {
  return {
    channel_id: 'channel-a',
    status: 'active',
    live: false,
    binding_state: 'disabled',
    sample_count: 20,
    success_rate: 0.5,
    upstream_error_rate: 0.5,
    ttft_p95: 5000,
    duration_p95: 20000,
    quality_score: 40,
    health_score: 40,
    eject_score: 60,
    quality_below_threshold: true,
    bad_windows: 2,
    cohort_percentage: 50,
    cohort_known: true,
    cohort_member: false,
    ejected: false,
    hard_failure: false,
    penalties: {
      error_rate: 0.5,
      error_penalty: 30,
      ttft_penalty: 20,
      duration_penalty: 10,
      total_penalty: 60,
    },
    health_state: 'unhealthy',
    consecutive_probe_successes: 0,
    ...overrides,
  }
}

describe('auto-switch channel status', () => {
  it('does not present a manual disable as a quality isolation', () => {
    const manual = channel({ quality_below_threshold: false })
    expect(schedulingStateView(manual)).toMatchObject({
      key: 'manual_disabled',
      label: '绑定已停用',
    })
  })

  it('keeps a low score advisory separate from a durable circuit', () => {
    expect(schedulingStateView(channel({ binding_state: 'active', live: true }))).toMatchObject({
      key: 'needs_ejection',
      label: '低分待摘除',
    })
  })

  it('shows half-open recovery evidence from the circuit runtime', () => {
    setLocale('zh')
    const recovering = channel({
      circuit_state: 'half_open',
      ejected: true,
      consecutive_probe_successes: 2,
      last_reason: { code: 'probe_passed', text: '主动探测通过' },
    })
    expect(schedulingStateView(recovering).key).toBe('recovering')
    expect(recoveryProgressText(recovering)).toBe('2/3')
    expect(circuitReasonText(recovering)).toBe('主动探测通过')
    expect(circuitStateView('half_open')).toMatchObject({ label: '主动探测' })
  })

  it('makes the 100-point deduction and failure scope explicit', () => {
    const soft = channel({ hard_failure: false })
    expect(penaltyBreakdownText(soft)).toBe(
      '总扣 60.0 / 100（错误 30.0/55，首字 20.0/25，总耗时 10.0/20）',
    )
    expect(failureScopeText(soft)).toBe('软故障：连续 2 个窗口 · 50% 稳定批次 · 当前下游未命中')
    expect(
      failureScopeText(channel({ bad_windows: 3, cohort_percentage: 75, cohort_member: true })),
    ).toContain('75% 稳定批次 · 当前下游已命中')
    expect(failureScopeText(channel({ hard_failure: true }))).toBe('硬故障：仅当前下游立即摘除')
  })

  it('surfaces the durable cohort outcome from a skipped decision', () => {
    const decision: AutoSwitchDecision = {
      id: 'decision-a',
      plan_id: 'plan-a',
      strategy: 'stability_first',
      trigger: 'auto',
      risk_level: 'L1',
      risk_reason:
        '质量分 40.0 已连续 2 个窗口低于摘除线，进入 50% 稳定灰度；本下游不在当前批次，保持原调度',
      status: 'skipped',
      auto_applied: false,
      dry_run_result: {
        instance_id: 'instance-a',
        plan_id: 'plan-a',
        dry_run: true,
        actions: [],
        created_at: '2026-07-14T00:00:00Z',
      },
      created_at: '2026-07-14T00:00:00Z',
      updated_at: '2026-07-14T00:00:00Z',
    }

    expect(decisionImpactText(decision)).toBe('软故障 · 50% 批次未命中')
    expect(
      decisionImpactText(
        decision,
        channel({ bad_windows: 3, cohort_percentage: 75, cohort_member: true }),
      ),
    ).toBe('软故障 · 连续 3 个窗口 · 75% 批次已命中')
    expect(decisionDetailText(decision)).toContain('保持原调度')
  })
})
