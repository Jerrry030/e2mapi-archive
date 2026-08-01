import type {
  NotificationChannel,
  NotificationChannelStatus,
  NotificationDelivery,
  NotificationDeliveryStatus,
  NotificationSystemChannel,
} from '../api/types'
import type { AuthUser } from '../api/auth'

export interface StatusView {
  label: string
  color: string
}

const deliveryStatusViews: Record<NotificationDeliveryStatus, StatusView> = {
  pending: { label: '等待发送', color: 'default' },
  processing: { label: '正在发送', color: 'processing' },
  retrying: { label: '正在重试', color: 'warning' },
  succeeded: { label: '已发送', color: 'success' },
  failed: { label: '发送失败', color: 'error' },
}

export function notificationDeliveryStatusView(status: NotificationDeliveryStatus): StatusView {
  return deliveryStatusViews[status] ?? { label: status, color: 'default' }
}

export function findSystemChannelStatus(
  statuses: NotificationChannelStatus[] | undefined,
  channel: NotificationSystemChannel,
): NotificationChannelStatus | undefined {
  return statuses?.find((item) => item.channel === channel)
}

export function notificationChannelStatusView(status?: NotificationChannelStatus): StatusView {
  if (!status) return { label: '状态未知', color: 'default' }
  if (!status.configured || status.state === 'unconfigured') {
    return { label: '未配置', color: 'warning' }
  }
  const views: Record<NotificationChannelStatus['state'], StatusView> = {
    unconfigured: { label: '未配置', color: 'warning' },
    unknown: { label: '待测试', color: 'default' },
    healthy: { label: '运行正常', color: 'success' },
    failing: { label: '最近发送失败', color: 'error' },
  }
  return views[status.state] ?? { label: status.state, color: 'default' }
}

export function canTestNotificationChannel(
  channel: NotificationChannel,
  statuses: NotificationChannelStatus[] | undefined,
): boolean {
  if (channel === 'webhook') return true
  return findSystemChannelStatus(statuses, channel)?.configured === true
}

export function latestDeliveriesByRoute(
  deliveries: NotificationDelivery[] | undefined,
): Map<string, NotificationDelivery> {
  const latest = new Map<string, NotificationDelivery>()
  for (const delivery of deliveries ?? []) {
    const current = latest.get(delivery.route_id)
    if (!current || Date.parse(delivery.updated_at) > Date.parse(current.updated_at)) {
      latest.set(delivery.route_id, delivery)
    }
  }
  return latest
}

export function notificationFormUserOptions(
  users: AuthUser[] | undefined,
  currentUser: AuthUser | null,
): Array<{ value: number; label: string }> {
  const byID = new Map<number, AuthUser>()
  for (const item of users ?? []) byID.set(item.id, item)
  if (currentUser) byID.set(currentUser.id, currentUser)

  return [...byID.values()]
    .filter(
      (item) =>
        item.enabled &&
        (item.roles.includes('client') || (currentUser && item.id === currentUser.id)),
    )
    .map((item) => ({
      value: item.id,
      label:
        currentUser && item.id === currentUser.id
          ? `${item.display_name || item.email}（平台运维）`
          : item.display_name || item.email,
    }))
}
