import { describe, expect, it } from 'vitest'
import type { NotificationRoute, NotificationTarget } from '../api/types'
import {
  canTestNotificationRoute,
  deliveryMethodForRoute,
  isPersonalTargetRef,
  notificationDestination,
  targetRefForDeliveryMethod,
} from './notificationTargets'

const personalFeishu: NotificationTarget = {
  user_id: 7,
  channel: 'feishu',
  scope: 'personal',
  target_ref: 'credential_ref:user/7/notification/personal-feishu',
  configured: true,
  endpoint_host: 'open.feishu.cn',
}

function route(target_ref: string): NotificationRoute {
  return {
    id: 'route-1',
    user_id: 7,
    name: '异常提醒',
    channel: 'feishu',
    target_ref,
    min_risk_level: 'L0',
    enabled: true,
    created_at: '',
    updated_at: '',
  }
}

describe('personal notification targets', () => {
  it('only recognizes the deterministic owner and channel scoped ref', () => {
    expect(isPersonalTargetRef(personalFeishu.target_ref, 7, 'feishu')).toBe(true)
    expect(isPersonalTargetRef(personalFeishu.target_ref, 8, 'feishu')).toBe(false)
    expect(isPersonalTargetRef(personalFeishu.target_ref, 7, 'qq')).toBe(false)
  })

  it('distinguishes platform and personal destinations without changing channel', () => {
    expect(deliveryMethodForRoute(route('system:feishu'))).toBe('platform_feishu')
    expect(deliveryMethodForRoute(route(personalFeishu.target_ref))).toBe('personal_feishu')
    expect(notificationDestination(route(personalFeishu.target_ref))).toBe('我的飞书机器人')
  })

  it('uses a personal ref only after that target is configured', () => {
    expect(targetRefForDeliveryMethod('personal_feishu', personalFeishu)).toBe(
      personalFeishu.target_ref,
    )
    expect(
      targetRefForDeliveryMethod('personal_feishu', { ...personalFeishu, configured: false }),
    ).toBe('')
  })

  it('tests a personal target independently of the platform channel state', () => {
    const personalRoute = route(personalFeishu.target_ref)
    expect(
      canTestNotificationRoute(
        personalRoute,
        [{ channel: 'feishu', configured: false, state: 'unconfigured' }],
        [personalFeishu],
      ),
    ).toBe(true)
    expect(canTestNotificationRoute(personalRoute, [], [])).toBe(true)
    expect(canTestNotificationRoute(route('system:feishu'), [], [personalFeishu])).toBe(false)
  })
})
