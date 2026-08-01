import { describe, expect, it } from 'vitest'
import type { SecretRef } from '../api/types'
import {
  isManagedPersonalNotificationRef,
  notificationCredentialOptions,
  safeNotificationCredentialRef,
} from './notificationCredentials'

const secrets: SecretRef[] = [
  {
    ref: 'credential_ref:user/7/notification/ops',
    user_id: 7,
    kind: 'notification',
    name: '生产告警群',
    exists: true,
  },
  {
    ref: 'credential_ref:user/8/notification/other',
    user_id: 8,
    kind: 'notification',
    name: '其他账号',
    exists: true,
  },
  {
    ref: 'credential_ref:user/7/upstream/openai',
    user_id: 7,
    kind: 'upstream',
    name: '上游凭证',
    exists: true,
  },
  {
    ref: 'credential_ref:user/7/notification/deleted',
    user_id: 7,
    kind: 'notification',
    name: '已删除凭证',
    exists: false,
  },
]

describe('notification credential selection', () => {
  it('only exposes existing notification credentials owned by the selected user', () => {
    expect(notificationCredentialOptions(secrets, 7)).toEqual([
      {
        value: 'credential_ref:user/7/notification/ops',
        label: '生产告警群',
      },
    ])
  })

  it('keeps managed Feishu and QQ targets out of generic Webhook choices', () => {
    expect(
      isManagedPersonalNotificationRef('credential_ref:user/7/notification/personal-feishu'),
    ).toBe(true)
    expect(
      notificationCredentialOptions(
        [
          ...secrets,
          {
            ref: 'credential_ref:user/7/notification/personal-qq',
            user_id: 7,
            kind: 'notification',
            name: 'personal-qq',
            exists: true,
          },
        ],
        7,
      ).map((option) => option.value),
    ).not.toContain('credential_ref:user/7/notification/personal-qq')
  })

  it('never carries a plaintext or cross-account target into the webhook selector', () => {
    expect(safeNotificationCredentialRef('credential_ref:user/7/notification/ops', 7)).toBe(
      'credential_ref:user/7/notification/ops',
    )
    expect(safeNotificationCredentialRef('https://example.test/webhook/token', 7)).toBeUndefined()
    expect(
      safeNotificationCredentialRef('credential_ref:user/8/notification/other', 7),
    ).toBeUndefined()
    expect(
      safeNotificationCredentialRef('credential_ref:user/7/upstream/openai', 7),
    ).toBeUndefined()
    expect(
      safeNotificationCredentialRef('credential_ref:user/7/notification/ops/extra', 7),
    ).toBeUndefined()
  })
})
