import { describe, expect, it } from 'vitest'
import type { AuthUser } from '../api/auth'
import type { NotificationChannelStatus, NotificationDelivery } from '../api/types'
import {
  canTestNotificationChannel,
  latestDeliveriesByRoute,
  notificationChannelStatusView,
  notificationDeliveryStatusView,
  notificationFormUserOptions,
} from './notificationDeliveryView'

const statuses: NotificationChannelStatus[] = [
  { channel: 'feishu', configured: false, state: 'unconfigured' },
  { channel: 'qq', configured: true, state: 'healthy' },
]

describe('notification delivery views', () => {
  it('uses plain-language channel and delivery states', () => {
    expect(notificationChannelStatusView(statuses[0])).toEqual({
      label: '未配置',
      color: 'warning',
    })
    expect(notificationChannelStatusView(statuses[1]).label).toBe('运行正常')
    expect(notificationDeliveryStatusView('retrying').label).toBe('正在重试')
    expect(notificationDeliveryStatusView('succeeded').label).toBe('已发送')
  })

  it('only enables tests for configured system channels and keeps webhook route-scoped', () => {
    expect(canTestNotificationChannel('feishu', statuses)).toBe(false)
    expect(canTestNotificationChannel('qq', statuses)).toBe(true)
    expect(canTestNotificationChannel('webhook', statuses)).toBe(true)
  })

  it('chooses the latest delivery for every route', () => {
    const base: NotificationDelivery = {
      id: 'delivery-old',
      user_id: 7,
      route_id: 'route-1',
      route_name: '生产异常提醒',
      kind: 'event',
      channel: 'feishu',
      status: 'failed',
      attempts: 3,
      max_attempts: 3,
      created_at: '2026-07-23T01:00:00Z',
      updated_at: '2026-07-23T01:01:00Z',
    }
    const latest = latestDeliveriesByRoute([
      { ...base, id: 'delivery-new', status: 'succeeded', updated_at: '2026-07-23T02:00:00Z' },
      base,
    ])

    expect(latest.get('route-1')?.id).toBe('delivery-new')
  })

  it('includes the current administrator as the platform operations recipient', () => {
    const admin: AuthUser = {
      id: 1,
      email: 'admin@example.com',
      display_name: 'Admin',
      roles: ['admin'],
      enabled: true,
    }
    const owner: AuthUser = {
      id: 7,
      email: 'owner@example.com',
      display_name: 'Owner',
      roles: ['client'],
      enabled: true,
    }

    expect(notificationFormUserOptions([owner], admin)).toEqual([
      { value: 7, label: 'Owner' },
      { value: 1, label: 'Admin（平台运维）' },
    ])
  })
})
