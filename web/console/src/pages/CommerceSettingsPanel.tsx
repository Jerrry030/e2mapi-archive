import { useEffect } from 'react'
import { ProCard, ProForm, ProFormText } from '@ant-design/pro-components'
import { Alert, App, Space, Typography } from 'antd'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { endpoints } from '../api/endpoints'
import { friendlyErrorMessage } from '../api/errors'
import type { UpdateCommerceSettingsInput } from '../api/types'
import { t } from '../i18n'

// CommerceSettingsPanel is the unified settings section for commerce runtime
// knobs. Values are stored in the settings module and hot-apply without a
// restart; environment variables only seed the very first boot.
export default function CommerceSettingsPanel() {
  const { message } = App.useApp()
  const queryClient = useQueryClient()
  const [form] = ProForm.useForm<UpdateCommerceSettingsInput>()
  const settings = useQuery({
    queryKey: ['admin', 'settings', 'commerce'],
    queryFn: () => endpoints.getCommerceSettings(),
  })

  useEffect(() => {
    if (!settings.data) return
    form.setFieldsValue({
      usd_to_cny_rate: settings.data.usd_to_cny_rate,
      balance_alert_threshold: settings.data.balance_alert_threshold,
    })
  }, [form, settings.data])

  const update = useMutation({
    mutationFn: (body: UpdateCommerceSettingsInput) => endpoints.updateCommerceSettings(body),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['admin', 'settings', 'commerce'] })
      message.success(t('commerceSettings.saved', '商务设置已保存并即时生效'))
    },
    onError: (error) => message.error(friendlyErrorMessage(error)),
  })

  return (
    <Space direction="vertical" size="large" style={{ width: '100%' }}>
      <Alert
        type="info"
        showIcon
        message={t('commerceSettings.intro.message', '商务运行参数集中在此管理，保存后即时生效，无需重启')}
        description={t(
          'commerceSettings.intro.description',
          '环境变量仅在首次启动时作为种子值写入；此后以这里保存的值为准。清空字段即关闭对应能力（保守失效）。',
        )}
      />
      <ProCard bordered>
        <ProForm<UpdateCommerceSettingsInput>
          form={form}
          layout="vertical"
          submitter={{
            searchConfig: { submitText: t('commerceSettings.save', '保存商务设置') },
            submitButtonProps: { loading: update.isPending },
            resetButtonProps: { onClick: () => settings.refetch() },
          }}
          onFinish={async (values) => {
            await update.mutateAsync({
              usd_to_cny_rate: values.usd_to_cny_rate?.trim() ?? '',
              balance_alert_threshold: values.balance_alert_threshold?.trim() ?? '',
            })
            return true
          }}
        >
          <ProFormText
            name="usd_to_cny_rate"
            label={t('commerceSettings.rate', '美元→人民币汇率')}
            placeholder="7.20"
            extra={t(
              'commerceSettings.rateExtra',
              '用于基准价目表定价（基准价 × 汇率 × 分组倍率）。留空则关闭基准价定价，仅显式上游价格生效。',
            )}
          />
          <ProFormText
            name="balance_alert_threshold"
            label={t('commerceSettings.threshold', '钱包低余额告警线（元）')}
            placeholder="5.00"
            extra={t(
              'commerceSettings.thresholdExtra',
              '客户平台钱包余额跌破该值时经通知路由告警（边沿触发）。留空关闭告警。',
            )}
          />
        </ProForm>
        {settings.data?.updated_at ? (
          <Typography.Text type="secondary">
            {t('commerceSettings.updatedAt', '最近更新：')}
            {new Date(settings.data.updated_at).toLocaleString()}
          </Typography.Text>
        ) : null}
      </ProCard>
    </Space>
  )
}
