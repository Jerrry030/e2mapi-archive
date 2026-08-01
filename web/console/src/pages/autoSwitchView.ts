import type { AutoSwitchChannelHealth, AutoSwitchDecision, QualityCircuitState } from '../api/types'
import { operationsReasonLabel } from '../i18n'

export const requiredRecoveryProbes = 3

export type SchedulingStateKey =
  | 'restore_pending'
  | 'recovering'
  | 'isolated'
  | 'needs_ejection'
  | 'manual_disabled'
  | 'delivery_failed'
  | 'schedulable'
  | 'standby'

export interface SchedulingStateView {
  key: SchedulingStateKey
  label: string
  color?: string
}

export interface StatusView {
  label: string
  color?: string
}

export function schedulingStateView(channel: AutoSwitchChannelHealth): SchedulingStateView {
  if (channel.restore_pending) {
    return { key: 'restore_pending', label: '恢复落盘中', color: 'processing' }
  }
  if (channel.circuit_state === 'half_open') {
    return { key: 'recovering', label: '恢复探测中', color: 'processing' }
  }
  if (channel.circuit_state === 'open') {
    return { key: 'isolated', label: '质量隔离', color: 'error' }
  }
  if (channel.quality_below_threshold) {
    return { key: 'needs_ejection', label: '低分待摘除', color: 'warning' }
  }
  if (channel.binding_state === 'failed') {
    return { key: 'delivery_failed', label: '发布失败', color: 'error' }
  }
  if (channel.binding_state === 'disabled') {
    return { key: 'manual_disabled', label: '绑定已停用' }
  }
  if (channel.binding_state === 'active' || channel.live) {
    return { key: 'schedulable', label: '调度中', color: 'success' }
  }
  return { key: 'standby', label: '未调度' }
}

export function recoveryProgressText(channel: AutoSwitchChannelHealth): string {
  if (channel.recovery_ready) {
    return channel.recovery_stage ? `${channel.recovery_stage}% 回归观察` : '探测已通过，等待灰度'
  }
  if (channel.circuit_state !== 'open' && channel.circuit_state !== 'half_open') return '-'
  return `${channel.consecutive_probe_successes ?? 0}/${requiredRecoveryProbes}`
}

export function circuitReasonText(channel: AutoSwitchChannelHealth): string {
  return operationsReasonLabel(channel.last_reason?.code ?? '', channel.last_reason?.text)
}

export function circuitStateView(state?: QualityCircuitState): StatusView {
  switch (state) {
    case 'closed':
      return { label: '正常', color: 'success' }
    case 'open':
      return { label: '已摘除', color: 'error' }
    case 'half_open':
      return { label: '主动探测', color: 'processing' }
    default:
      return { label: '未建立' }
  }
}

export function penaltyBreakdownText(channel: AutoSwitchChannelHealth): string {
  const penalties = channel.penalties
  return `总扣 ${(penalties?.total_penalty ?? 0).toFixed(1)} / 100（错误 ${(penalties?.error_penalty ?? 0).toFixed(1)}/55，首字 ${(penalties?.ttft_penalty ?? 0).toFixed(1)}/25，总耗时 ${(penalties?.duration_penalty ?? 0).toFixed(1)}/20）`
}

export function failureScopeText(channel: AutoSwitchChannelHealth): string {
  if (channel.hard_failure) return '硬故障：仅当前下游立即摘除'
  if (channel.quality_below_threshold && channel.cohort_percentage > 0) {
    if (!channel.cohort_known) {
      return `软故障：连续 ${channel.bad_windows} 个窗口 · ${channel.cohort_percentage}% 批次 · 全局成员待确认`
    }
    return `软故障：连续 ${channel.bad_windows} 个窗口 · ${channel.cohort_percentage}% 稳定批次 · 当前下游${channel.cohort_member ? '已命中' : '未命中'}`
  }
  return '-'
}

export function decisionImpactText(
  decision: AutoSwitchDecision,
  fromChannel?: AutoSwitchChannelHealth,
): string {
  const detail = [decision.risk_reason, decision.observation_note, decision.error]
    .filter(Boolean)
    .join('；')
  const cohort = detail.match(/(25|50|75)%\s*稳定灰度/)

  if (fromChannel?.hard_failure) return '硬故障 · 仅当前下游'
  if (fromChannel?.quality_below_threshold && fromChannel.cohort_percentage > 0) {
    if (!fromChannel.cohort_known) {
      return `软故障 · 连续 ${fromChannel.bad_windows} 个窗口 · ${fromChannel.cohort_percentage}% 批次待确认`
    }
    return `软故障 · 连续 ${fromChannel.bad_windows} 个窗口 · ${fromChannel.cohort_percentage}% 批次${fromChannel.cohort_member ? '已命中' : '未命中'}`
  }
  if (cohort) {
    return detail.includes('不在当前批次')
      ? `软故障 · ${cohort[1]}% 批次未命中`
      : `软故障 · ${cohort[1]}% 稳定批次`
  }
  return '当前下游'
}

export function decisionDetailText(decision: AutoSwitchDecision): string {
  return (
    decision.observation_note ||
    decision.error ||
    decision.risk_reason ||
    decision.trigger_reason ||
    '-'
  )
}
