import type {
  NotificationChannelStatus,
  NotificationChannel,
  NotificationPersonalChannel,
  NotificationRoute,
  NotificationTarget,
} from '../api/types'

export type NotificationDeliveryMethod =
  'platform_feishu' | 'personal_feishu' | 'platform_qq' | 'personal_qq' | 'webhook'

export function personalTargetRef(userId: number, channel: NotificationPersonalChannel) {
  return `credential_ref:user/${userId}/notification/personal-${channel}`
}

export function isPersonalTargetRef(
  targetRef: string,
  userId: number,
  channel: NotificationPersonalChannel,
) {
  return targetRef === personalTargetRef(userId, channel)
}

export function personalTargetForChannel(
  targets: NotificationTarget[] | null | undefined,
  channel: NotificationPersonalChannel,
) {
  return (targets ?? []).find((target) => target.channel === channel)
}

export function deliveryMethodForRoute(route: NotificationRoute): NotificationDeliveryMethod {
  if (route.channel === 'webhook') return 'webhook'
  if (isPersonalTargetRef(route.target_ref, route.user_id, route.channel)) {
    return route.channel === 'feishu' ? 'personal_feishu' : 'personal_qq'
  }
  return route.channel === 'feishu' ? 'platform_feishu' : 'platform_qq'
}

export function channelForDeliveryMethod(method: 'webhook'): 'webhook'
export function channelForDeliveryMethod(
  method: Exclude<NotificationDeliveryMethod, 'webhook'>,
): NotificationPersonalChannel
export function channelForDeliveryMethod(method: NotificationDeliveryMethod): NotificationChannel {
  if (method === 'webhook') return 'webhook'
  return method.endsWith('feishu') ? 'feishu' : 'qq'
}

export function targetRefForDeliveryMethod(
  method: NotificationDeliveryMethod,
  personalTarget?: NotificationTarget,
  fallbackRef?: string,
) {
  if (method === 'platform_feishu') return 'system:feishu'
  if (method === 'platform_qq') return 'system:qq'
  if (method === 'webhook') return fallbackRef ?? ''
  return personalTarget?.configured ? personalTarget.target_ref : (fallbackRef ?? '')
}

export function notificationDestination(route: NotificationRoute) {
  switch (deliveryMethodForRoute(route)) {
    case 'platform_feishu':
      return '平台飞书群'
    case 'personal_feishu':
      return '我的飞书机器人'
    case 'platform_qq':
      return '平台 QQ 群'
    case 'personal_qq':
      return '我的 QQ 群'
    case 'webhook':
      return '已保存的 Webhook 地址'
  }
}

export function canTestNotificationRoute(
  route: NotificationRoute,
  statuses: NotificationChannelStatus[] | undefined,
  targets: NotificationTarget[] | undefined,
) {
  if (route.channel === 'webhook') return true
  if (isPersonalTargetRef(route.target_ref, route.user_id, route.channel)) {
    const target = personalTargetForChannel(targets, route.channel)
    return target ? target.configured : true
  }
  return Boolean(statuses?.find((status) => status.channel === route.channel)?.configured)
}
