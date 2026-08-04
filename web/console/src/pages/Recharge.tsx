import { useState } from 'react'
import { WalletOutlined } from '@ant-design/icons'
import { PageContainer, ProCard } from '@ant-design/pro-components'
import { Alert, App, Button, InputNumber, Radio, Space, Statistic, Typography } from 'antd'
import { friendlyErrorMessage } from '../api/errors'
import { useCreateRechargeOrder } from '../api/hooks'
import { usePlatformWallet } from '../api/platformDistributionHooks'
import { t } from '../i18n'
import { useLocaleVersion } from '../i18n/react'

const PRESET_AMOUNTS = [10, 50, 100, 500]

function formatMicros(micros: number): string {
  return (micros / 1_000_000).toFixed(2)
}

export default function Recharge() {
  useLocaleVersion()
  const { message } = App.useApp()
  const wallet = usePlatformWallet()
  const createOrder = useCreateRechargeOrder()
  const [amount, setAmount] = useState<number | null>(50)
  const [method, setMethod] = useState<'stripe' | 'alipay' | 'wxpay'>('stripe')

  const submit = () => {
    if (!amount || amount <= 0) {
      message.error(t('recharge.invalidAmount', '请输入有效的充值金额'))
      return
    }
    createOrder.mutate(
      { amount: amount.toFixed(2), currency: 'CNY', payment_type: method },
      {
        onSuccess: (response) => {
          // The provider-hosted checkout finishes the payment; the wallet is
          // credited by the verified webhook (or the expiry sweeper) only.
          window.location.assign(response.checkout_url)
        },
        onError: (error) => {
          message.error(friendlyErrorMessage(error))
        },
      },
    )
  }

  return (
    <PageContainer title={t('recharge.title', '余额充值')}>
      <Space direction="vertical" size="large" style={{ width: '100%' }}>
        <Alert
          type="info"
          showIcon
          message={t('recharge.intro.message', '充值到 E2M 平台钱包，用于平台分发 Key 的按量扣费')}
          description={t(
            'recharge.intro.description',
            '支付在服务商托管页面完成；到账以服务商回调验签为准，通常在支付完成后数秒内入账。',
          )}
        />
        <ProCard>
          <Statistic
            title={t('recharge.currentBalance', '当前余额（CNY）')}
            prefix={<WalletOutlined />}
            value={wallet.data ? formatMicros(wallet.data.available_micros) : '--'}
            loading={wallet.isLoading}
          />
        </ProCard>
        <ProCard title={t('recharge.amountTitle', '充值金额')}>
          <Space direction="vertical" size="middle" style={{ width: '100%' }}>
            <Radio.Group
              optionType="button"
              buttonStyle="solid"
              value={PRESET_AMOUNTS.includes(amount ?? 0) ? amount : undefined}
              onChange={(event) => setAmount(event.target.value as number)}
              options={PRESET_AMOUNTS.map((value) => ({ label: `¥${value}`, value }))}
            />
            <InputNumber
              min={0.01}
              max={1_000_000}
              precision={2}
              prefix="¥"
              style={{ width: 240 }}
              value={amount}
              onChange={(value) => setAmount(value)}
              placeholder={t('recharge.customAmount', '自定义金额')}
            />
            <Radio.Group
              value={method}
              onChange={(event) => setMethod(event.target.value as 'stripe' | 'alipay' | 'wxpay')}
              options={[
                { label: t('recharge.methodStripe', 'Stripe'), value: 'stripe' },
                { label: t('recharge.methodAlipay', '支付宝'), value: 'alipay' },
                { label: t('recharge.methodWxpay', '微信支付'), value: 'wxpay' },
              ]}
            />
            <Typography.Text type="secondary">
              {t('recharge.methodHint', '可用方式以平台已配置的收款渠道为准；支付宝/微信经易支付聚合渠道。')}
            </Typography.Text>
            <Button
              type="primary"
              size="large"
              loading={createOrder.isPending}
              onClick={submit}
              data-testid="recharge-submit"
            >
              {t('recharge.submit', '去支付')}
            </Button>
          </Space>
        </ProCard>
      </Space>
    </PageContainer>
  )
}
