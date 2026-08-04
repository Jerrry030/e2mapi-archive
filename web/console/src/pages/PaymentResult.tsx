import { Button, Result, Space } from 'antd'
import { Link, useSearchParams } from 'react-router'
import { t } from '../i18n'
import { useLocaleVersion } from '../i18n/react'

/**
 * Landing pages for provider-hosted checkout redirects. Success here only
 * means the customer finished the checkout flow — the wallet is credited by
 * the verified webhook, so the page points back at the balance instead of
 * claiming a settled payment.
 */
export default function PaymentResult({ outcome }: { outcome: 'success' | 'cancelled' }) {
  useLocaleVersion()
  const [params] = useSearchParams()
  const orderId = params.get('order')

  if (outcome === 'cancelled') {
    return (
      <Result
        status="warning"
        title={t('paymentResult.cancelledTitle', '支付未完成')}
        subTitle={
          orderId
            ? t('paymentResult.cancelledWithOrder', '订单已保留，可重新发起支付或等待其自动过期。')
            : t('paymentResult.cancelledSubtitle', '你已取消本次支付。')
        }
        extra={
          <Space>
            <Link to="/recharge">
              <Button type="primary">{t('paymentResult.backToRecharge', '重新充值')}</Button>
            </Link>
            <Link to="/platform-distribution">
              <Button>{t('paymentResult.backToPlatform', '返回平台分发')}</Button>
            </Link>
          </Space>
        }
      />
    )
  }
  return (
    <Result
      status="success"
      title={t('paymentResult.successTitle', '支付流程已完成')}
      subTitle={t(
        'paymentResult.successSubtitle',
        '到账以支付服务商回调验签为准，通常数秒内入账；可在平台分发页查看余额与流水。',
      )}
      extra={
        <Space>
          <Link to="/platform-distribution">
            <Button type="primary">{t('paymentResult.viewBalance', '查看余额')}</Button>
          </Link>
          <Link to="/recharge">
            <Button>{t('paymentResult.rechargeAgain', '继续充值')}</Button>
          </Link>
        </Space>
      }
    />
  )
}
