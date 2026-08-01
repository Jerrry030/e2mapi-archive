import { describe, expect, it } from 'vitest'
import { ApiError } from './client'
import { friendlyErrorMessage, friendlyInlineError } from './errors'
import { setLocale } from '../i18n'

describe('friendly error messages', () => {
  it('distinguishes invalid login credentials from an expired session', () => {
    setLocale('zh')

    expect(friendlyErrorMessage(new ApiError(401, 'invalid_credentials', '邮箱或密码错误'))).toBe(
      '邮箱或密码错误。',
    )
    expect(friendlyErrorMessage(new ApiError(401, 'unauthorized', 'auth: unauthorized'))).toBe(
      '登录已失效，请重新登录。',
    )
  })

  it('renders connector-required API errors in Chinese', () => {
    setLocale('zh')
    const msg = friendlyErrorMessage(
      new ApiError(
        400,
        'connector_not_configured',
        'adapters: instance inst-a has no connector; install a connector and configure the gateway credential locally',
      ),
    )

    expect(msg).toContain('连接器')
    expect(msg).not.toContain('install a connector')
    expect(msg).not.toContain('adapters:')
  })

  it('renders persisted health errors in Chinese', () => {
    setLocale('zh')
    const msg = friendlyInlineError(
      'adapters: instance inst-a has no connector; install a connector and configure the gateway credential locally',
    )

    expect(msg).toContain('连接器')
    expect(msg).not.toContain('install a connector')
  })

  it('localizes connector-prefixed missing connector errors', () => {
    setLocale('zh')

    expect(
      friendlyInlineError('connector gateway: instance inst-example has no connector_id'),
    ).toBe('该实例尚未绑定连接器。')
  })

  it('does not crash when persisted errors are structured values', () => {
    setLocale('zh')

    expect(friendlyInlineError({ message: 'instance not found' })).toBe('实例不存在。')
    expect(friendlyInlineError(['connector not found', 502])).toContain('连接器不存在')
    expect(friendlyInlineError(404)).toBe('404')
  })

  it('translates internal auto-switch details shown in operations timelines', () => {
    setLocale('zh')

    expect(friendlyInlineError('superseded by route-plan scheduling generation 12')).toBe(
      '已被发布计划调度版本 12 取代。',
    )
    expect(friendlyInlineError('replacement failed during scheduling apply')).toBe(
      '替代渠道调度应用失败。',
    )
  })
})
