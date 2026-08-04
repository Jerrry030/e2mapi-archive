import { useState } from 'react'
import { GiftOutlined, WalletOutlined } from '@ant-design/icons'
import { PageContainer, ProCard } from '@ant-design/pro-components'
import { Alert, App, Button, Input, Space, Statistic, Typography } from 'antd'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { endpoints } from '../api/endpoints'
import { friendlyErrorMessage } from '../api/errors'
import { usePlatformWallet } from '../api/platformDistributionHooks'
import { t } from '../i18n'
import { useLocaleVersion } from '../i18n/react'

function formatMicros(micros: number): string {
  return (micros / 1_000_000).toFixed(2)
}

export default function Redeem() {
  useLocaleVersion()
  const { message } = App.useApp()
  const queryClient = useQueryClient()
  const wallet = usePlatformWallet()
  const [code, setCode] = useState('')

  const redeem = useMutation({
    mutationFn: (value: string) => endpoints.redeemCode(value),
    onSuccess: (result) => {
      queryClient.invalidateQueries({ queryKey: ['platform', 'wallet'] })
      message.success(
        t('redeem.success', '兑换成功，已入账 ¥{amount}', {
          amount: formatMicros(result.amount_micros),
        }),
      )
      setCode('')
    },
    onError: (error) => {
      message.error(friendlyErrorMessage(error))
    },
  })

  const submit = () => {
    const value = code.trim()
    if (!value) {
      message.error(t('redeem.codeRequired', '请输入兑换码'))
      return
    }
    redeem.mutate(value)
  }

  return (
    <PageContainer title={t('redeem.title', '兑换码')}>
      <Space direction="vertical" size="large" style={{ width: '100%' }}>
        <Alert
          type="info"
          showIcon
          message={t('redeem.intro.message', '输入兑换码，余额立即入账 E2M 平台钱包')}
          description={t(
            'redeem.intro.description',
            '兑换码由平台或合作发卡渠道发放，每张只能使用一次；兑换记录可在审计日志中追溯。',
          )}
        />
        <ProCard>
          <Statistic
            title={t('redeem.currentBalance', '当前余额（CNY）')}
            prefix={<WalletOutlined />}
            value={wallet.data ? formatMicros(wallet.data.available_micros) : '--'}
            loading={wallet.isLoading}
          />
        </ProCard>
        <ProCard title={t('redeem.inputTitle', '输入兑换码')}>
          <Space direction="vertical" size="middle" style={{ width: '100%' }}>
            <Input
              prefix={<GiftOutlined />}
              placeholder="XXXXXXXX-XXXXXXXX-XXXXXXXX-XXXXXXXX"
              style={{ maxWidth: 420 }}
              value={code}
              onChange={(event) => setCode(event.target.value)}
              onPressEnter={submit}
              data-testid="redeem-input"
            />
            <Typography.Text type="secondary">
              {t('redeem.hint', '连续多次输入错误会被暂时限制，请仔细核对后提交。')}
            </Typography.Text>
            <Button
              type="primary"
              loading={redeem.isPending}
              onClick={submit}
              data-testid="redeem-submit"
            >
              {t('redeem.submit', '立即兑换')}
            </Button>
          </Space>
        </ProCard>
      </Space>
    </PageContainer>
  )
}
