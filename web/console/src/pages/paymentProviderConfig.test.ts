import { Form } from 'antd'
import { act, renderHook } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import {
  providerCreateInput,
  providerTypeDefaults,
  providerUpdateInput,
  type PaymentProviderFormValues,
} from './paymentProviderConfig'

describe('payment provider form conversion', () => {
  it('replaces stale nested branches in a real rc-field-form store', () => {
    const { result } = renderHook(() => Form.useForm<PaymentProviderFormValues>())
    const form = result.current[0]
    act(() => {
      form.setFieldsValue({
        provider_key: 'easypay',
        name: 'keep this name',
        enabled: true,
        supported_types: ['alipay'],
        config: { pid: '1001', apiBase: 'https://pay.example.com' },
        secrets: { pkey: 'secret' },
        limits: { alipay: { singleMin: 10 } },
        clear_secrets: ['pkey'],
      })
      form.resetFields(['config', 'secrets', 'limits', 'clear_secrets'])
      form.setFieldsValue(providerTypeDefaults('stripe'))
    })

    expect(form.getFieldsValue(true)).toEqual({
      provider_key: 'stripe',
      name: 'keep this name',
      enabled: true,
      supported_types: ['card', 'alipay', 'wxpay', 'link'],
      payment_mode: '',
      config: {},
      secrets: {},
      clear_secrets: [],
      limits: {},
    })
  })
  it('builds fresh provider-specific defaults without stale nested fields', () => {
    expect(providerTypeDefaults('stripe')).toEqual({
      provider_key: 'stripe',
      supported_types: ['card', 'alipay', 'wxpay', 'link'],
      payment_mode: '',
      config: {},
      secrets: {},
      clear_secrets: [],
      limits: {},
    })
  })
  it('trims fields and omits blank secrets on create', () => {
    expect(
      providerCreateInput({
        provider_key: 'stripe',
        name: ' Stripe main ',
        enabled: false,
        supported_types: ['card'],
        config: { publishableKey: ' pk_test ', currency: 'CNY', unused: '' },
        secrets: { secretKey: ' sk_test ', webhookSecret: '   ' },
        refund_enabled: false,
        allow_user_refund: true,
      }),
    ).toMatchObject({
      name: 'Stripe main',
      config: { publishableKey: 'pk_test', currency: 'CNY' },
      secrets: { secretKey: 'sk_test' },
      allow_user_refund: false,
    })
  })

  it('keeps explicit clear list and uses patch-safe secret semantics', () => {
    expect(
      providerUpdateInput({
        provider_key: 'easypay',
        name: 'main',
        enabled: true,
        supported_types: ['alipay'],
        config: { pid: '1001', apiBase: 'https://pay.example.com' },
        secrets: { pkey: '' },
        clear_secrets: ['pkey'],
      }),
    ).toMatchObject({ secrets: {}, clear_secrets: ['pkey'] })
  })
})
