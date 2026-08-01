import { Tag, Tooltip } from 'antd'
import type {
  CapabilityMode,
  EventLevel,
  InstanceKind,
  InstanceStatus,
  NotificationChannel,
  RiskLevel,
  SupplyOfferStatus,
} from '../api/types'
import type { UserRole } from '../api/auth'
import { auditResultLabel, capabilityModeLabel, t } from '../i18n'
import { useLocaleVersion } from '../i18n/react'

// Single source of colour/label language for the whole console.

const instanceStatusMap: Record<InstanceStatus, { color: string; label: string }> = {
  active: { color: 'success', label: '活跃' },
  degraded: { color: 'warning', label: '降级' },
  offline: { color: 'error', label: '离线' },
  maintenance: { color: 'processing', label: '维护' },
  unknown: { color: 'default', label: '未知' },
}

const supplyStatusMap: Record<SupplyOfferStatus, { color: string; label: string }> = {
  pending: { color: 'gold', label: '待处理' },
  active: { color: 'success', label: '生效' },
  exhausted: { color: 'default', label: '已耗尽' },
  revoked: { color: 'error', label: '已吊销' },
}

const riskColors: Record<RiskLevel, string> = {
  L0: 'default',
  L1: 'blue',
  L2: 'orange',
  L3: 'red',
}

const kindMap: Record<InstanceKind, string> = {
  sub2api: 'geekblue',
  newapi: 'cyan',
  cpa: 'magenta',
}

const roleMap: Record<UserRole, { color: string; label: string }> = {
  admin: { color: 'gold', label: '管理员' },
  client: { color: 'blue', label: '托管能力' },
  supplier: { color: 'purple', label: '供给能力' },
}

const channelMap: Record<NotificationChannel, string> = {
  qq: 'gold',
  feishu: 'blue',
  webhook: 'default',
}

const channelLabels: Record<NotificationChannel, string> = {
  qq: 'QQ',
  feishu: '飞书',
  webhook: 'Webhook',
}

export function StatusTag({ status }: { status: InstanceStatus }) {
  const m = instanceStatusMap[status] ?? { color: 'default', label: status }
  return <Tag color={m.color}>{m.label}</Tag>
}

export function SupplyStatusTag({ status }: { status: SupplyOfferStatus }) {
  const m = supplyStatusMap[status] ?? { color: 'default', label: status }
  return <Tag color={m.color}>{m.label}</Tag>
}

export function RiskLevelTag({ level }: { level: RiskLevel }) {
  useLocaleVersion()
  const color = riskColors[level] ?? 'default'
  return (
    <Tooltip title={t(`capabilities.risks.${level}.hint`, '')}>
      <Tag color={color}>{t(`capabilities.risks.${level}.label`, level)}</Tag>
    </Tooltip>
  )
}

export function EventLevelTag({ level }: { level: EventLevel }) {
  useLocaleVersion()
  const color = riskColors[level] ?? 'default'
  return (
    <Tooltip title={t(`audit.eventLevels.${level}.hint`, '')}>
      <Tag color={color}>{t(`audit.eventLevels.${level}.label`, level)}</Tag>
    </Tooltip>
  )
}

// Backward-compatible alias for existing activity surfaces.
export const ActivityRiskTag = EventLevelTag

export function KindTag({ kind }: { kind: InstanceKind }) {
  return <Tag color={kindMap[kind] ?? 'default'}>{kind}</Tag>
}

export function RoleTag({ role }: { role: UserRole }) {
  const m = roleMap[role] ?? { color: 'default', label: role }
  return <Tag color={m.color}>{m.label}</Tag>
}

export function ChannelTag({ channel }: { channel: NotificationChannel }) {
  return <Tag color={channelMap[channel] ?? 'default'}>{channelLabels[channel] ?? channel}</Tag>
}

export function ModeTag({ mode }: { mode: CapabilityMode }) {
  useLocaleVersion()
  return mode === 'write' ? (
    <Tag color="orange">{capabilityModeLabel(mode)}</Tag>
  ) : (
    <Tag>{capabilityModeLabel(mode)}</Tag>
  )
}

export function EnabledTag({ enabled }: { enabled: boolean }) {
  return enabled ? <Tag color="success">已启用</Tag> : <Tag>已禁用</Tag>
}

export function ResultTag({ result }: { result: string }) {
  const map: Record<string, string> = {
    accepted: 'success',
    detected: 'warning',
    running: 'processing',
    retrying: 'warning',
    paused: 'default',
    verified: 'success',
    success: 'success',
    failed: 'error',
    rejected: 'default',
  }
  return <Tag color={map[result] ?? 'default'}>{auditResultLabel(result)}</Tag>
}
