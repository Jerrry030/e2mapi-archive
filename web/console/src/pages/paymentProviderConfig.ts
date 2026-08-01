import type {
  CreatePaymentProviderInput,
  PaymentProvider,
  PaymentProviderKey,
  UpdatePaymentProviderInput,
} from '../api/types'
import { t } from '../i18n'

export interface ProviderField {
  key: string
  label: string
  secret?: boolean
  optional?: boolean
  multiline?: boolean
  placeholder?: string
}

export interface ProviderDefinition {
  label: string
  description: string
  config: ProviderField[]
  secrets: ProviderField[]
  methods: { value: string; label: string }[]
  modes?: { value: string; label: string }[]
}

export const providerDefinitions: Record<PaymentProviderKey, ProviderDefinition> = {
  easypay: {
    label: 'EasyPay（易支付）',
    description: '兼容易支付协议的支付宝、微信聚合收款渠道',
    config: [
      { key: 'pid', label: '商户 ID（PID）' },
      { key: 'apiBase', label: 'API 地址', placeholder: 'https://pay.example.com' },
      { key: 'cidAlipay', label: '支付宝通道 ID', optional: true },
      { key: 'cidWxpay', label: '微信通道 ID', optional: true },
    ],
    secrets: [{ key: 'pkey', label: '商户密钥（PKey）', secret: true }],
    methods: [
      { value: 'alipay', label: '支付宝' },
      { value: 'wxpay', label: '微信支付' },
    ],
    modes: [
      { value: 'qrcode', label: '二维码' },
      { value: 'popup', label: '弹窗/跳转' },
    ],
  },
  alipay: {
    label: '支付宝官方',
    description: '支付宝开放平台直连',
    config: [{ key: 'appId', label: 'App ID' }],
    secrets: [
      { key: 'privateKey', label: '应用私钥', secret: true, multiline: true },
      { key: 'publicKey', label: '支付宝公钥', secret: true, multiline: true },
    ],
    methods: [{ value: 'alipay', label: '支付宝' }],
    modes: [
      { value: '', label: '桌面扫码优先' },
      { value: 'redirect', label: '直接跳转收银台' },
    ],
  },
  wxpay: {
    label: '微信支付官方',
    description: '微信支付 APIv3 直连',
    config: [
      { key: 'appId', label: 'App ID' },
      { key: 'mchId', label: '商户号（MchID）' },
      { key: 'certSerial', label: '商户证书序列号' },
      { key: 'publicKeyId', label: '微信支付公钥 ID' },
    ],
    secrets: [
      { key: 'privateKey', label: '商户 API 私钥', secret: true, multiline: true },
      { key: 'apiV3Key', label: 'APIv3 密钥', secret: true },
      { key: 'publicKey', label: '微信支付公钥', secret: true, multiline: true },
    ],
    methods: [{ value: 'wxpay', label: '微信支付' }],
  },
  stripe: {
    label: 'Stripe',
    description: '国际银行卡及 Stripe 本地支付方式',
    config: [
      { key: 'publishableKey', label: 'Publishable Key' },
      { key: 'currency', label: '结算币种', placeholder: 'CNY' },
    ],
    secrets: [
      { key: 'secretKey', label: 'Secret Key', secret: true },
      { key: 'webhookSecret', label: 'Webhook Secret', secret: true },
    ],
    methods: [
      { value: 'card', label: '银行卡' },
      { value: 'alipay', label: '支付宝' },
      { value: 'wxpay', label: '微信支付' },
      { value: 'link', label: 'Link' },
    ],
  },
  airwallex: {
    label: 'Airwallex（空中云汇）',
    description: '空中云汇国际收款',
    config: [
      { key: 'clientId', label: 'Client ID' },
      { key: 'apiBase', label: 'API 地址', placeholder: 'https://api.airwallex.com/api/v1' },
      { key: 'countryCode', label: '国家/地区代码', placeholder: 'CN' },
      { key: 'currency', label: '结算币种', placeholder: 'CNY' },
      { key: 'accountId', label: 'Account ID', optional: true },
    ],
    secrets: [
      { key: 'apiKey', label: 'API Key', secret: true },
      { key: 'webhookSecret', label: 'Webhook Secret', secret: true },
    ],
    methods: [{ value: 'airwallex', label: 'Airwallex' }],
  },
}

export const paymentProviderKeys = Object.keys(providerDefinitions) as PaymentProviderKey[]

function localizedField(
  providerKey: PaymentProviderKey,
  kind: 'config' | 'secrets',
  field: ProviderField,
): ProviderField {
  return {
    ...field,
    label: t(`payment.providers.${providerKey}.${kind}.${field.key}`, field.label),
    placeholder: field.placeholder
      ? t(`payment.providers.${providerKey}.${kind}Placeholders.${field.key}`, field.placeholder)
      : undefined,
  }
}

/** Build display metadata in the active console locale without changing protocol values. */
export function localizedProviderDefinition(providerKey: PaymentProviderKey): ProviderDefinition {
  const definition = providerDefinitions[providerKey]
  return {
    ...definition,
    label: t(`payment.providers.${providerKey}.label`, definition.label),
    description: t(`payment.providers.${providerKey}.description`, definition.description),
    config: definition.config.map((field) => localizedField(providerKey, 'config', field)),
    secrets: definition.secrets.map((field) => localizedField(providerKey, 'secrets', field)),
    methods: definition.methods.map((method) => ({
      ...method,
      label: t(`payment.methods.${method.value}`, method.label),
    })),
    modes: definition.modes?.map((mode) => ({
      ...mode,
      label: t(`payment.modes.${mode.value || 'default'}`, mode.label),
    })),
  }
}
export interface PaymentProviderFormValues {
  provider_key: PaymentProviderKey
  name: string
  enabled: boolean
  supported_types: string[]
  payment_mode?: string
  sort_order?: number
  refund_enabled?: boolean
  allow_user_refund?: boolean
  config?: Record<string, string>
  secrets?: Record<string, string>
  clear_secrets?: string[]
  limits?: CreatePaymentProviderInput['limits']
}

export function providerTypeDefaults(
  providerKey: PaymentProviderKey,
): Pick<
  PaymentProviderFormValues,
  | 'provider_key'
  | 'supported_types'
  | 'payment_mode'
  | 'config'
  | 'secrets'
  | 'clear_secrets'
  | 'limits'
> {
  const definition = providerDefinitions[providerKey]
  return {
    provider_key: providerKey,
    supported_types: definition.methods.map((item) => item.value),
    payment_mode: definition.modes?.[0]?.value ?? '',
    config: {},
    secrets: {},
    clear_secrets: [],
    limits: {},
  }
}
function nonEmpty(values?: Record<string, string>): Record<string, string> {
  return Object.fromEntries(
    Object.entries(values ?? {})
      .map(([key, value]) => [key, value?.trim()] as const)
      .filter(([, value]) => Boolean(value)),
  )
}

export function providerCreateInput(values: PaymentProviderFormValues): CreatePaymentProviderInput {
  return {
    provider_key: values.provider_key,
    name: values.name.trim(),
    config: nonEmpty(values.config),
    secrets: nonEmpty(values.secrets),
    supported_types: values.supported_types ?? [],
    enabled: !!values.enabled,
    payment_mode: values.payment_mode ?? '',
    sort_order: values.sort_order ?? 0,
    limits: values.limits ?? {},
    refund_enabled: !!values.refund_enabled,
    allow_user_refund: !!values.refund_enabled && !!values.allow_user_refund,
  }
}

export function providerUpdateInput(values: PaymentProviderFormValues): UpdatePaymentProviderInput {
  const clearSecrets = new Set(values.clear_secrets ?? [])
  const secrets = Object.fromEntries(
    Object.entries(nonEmpty(values.secrets)).filter(([key]) => !clearSecrets.has(key)),
  )
  return {
    name: values.name.trim(),
    config: nonEmpty(values.config),
    secrets,
    clear_secrets: values.clear_secrets ?? [],
    supported_types: values.supported_types ?? [],
    enabled: !!values.enabled,
    payment_mode: values.payment_mode ?? '',
    sort_order: values.sort_order ?? 0,
    limits: values.limits ?? {},
    refund_enabled: !!values.refund_enabled,
    allow_user_refund: !!values.refund_enabled && !!values.allow_user_refund,
  }
}

export function providerToForm(provider: PaymentProvider): PaymentProviderFormValues {
  return {
    provider_key: provider.provider_key,
    name: provider.name,
    enabled: provider.enabled,
    supported_types: provider.supported_types,
    payment_mode: provider.payment_mode ?? '',
    sort_order: provider.sort_order,
    refund_enabled: provider.refund_enabled,
    allow_user_refund: provider.allow_user_refund,
    config: provider.config,
    secrets: {},
    clear_secrets: [],
    limits: provider.limits,
  }
}
