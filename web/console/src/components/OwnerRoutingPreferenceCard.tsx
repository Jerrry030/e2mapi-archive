import {
  CheckCircleFilled,
  DollarOutlined,
  RiseOutlined,
  ThunderboltOutlined,
} from '@ant-design/icons'
import { ProCard } from '@ant-design/pro-components'
import { Alert, Button, Skeleton, Space, Tag, Typography } from 'antd'
import { friendlyErrorMessage } from '../api/errors'
import {
  useOwnerRoutingPreference,
  useUpdateOwnerRoutingPreference,
} from '../api/routingPreferenceHooks'
import type { OwnerRoutingPreference } from '../api/routingPreferenceTypes'
import type { RouteStrategyType } from '../api/types'
import { getLocale } from '../i18n'
import { useLocaleVersion } from '../i18n/react'

interface PreferenceOption {
  value: OwnerRoutingPreference
  title: { zh: string; en: string }
  description: { zh: string; en: string }
  icon: React.ReactNode
}

const options: PreferenceOption[] = [
  {
    value: 'smart_auto',
    title: { zh: '智能自动', en: 'Smart auto' },
    description: {
      zh: '兼顾成功率、速度和成本，适合大多数场景。',
      en: 'Balance reliability, speed, and cost for most workloads.',
    },
    icon: <RiseOutlined />,
  },
  {
    value: 'price_first',
    title: { zh: '价格优先', en: 'Price first' },
    description: {
      zh: '在可用线路中，优先选择成本较低的线路。',
      en: 'Prefer lower-cost upstreams after the platform quality floor is met.',
    },
    icon: <DollarOutlined />,
  },
  {
    value: 'speed_first',
    title: { zh: '速度优先', en: 'Speed first' },
    description: {
      zh: '优先选择响应更快的线路，适合聊天等实时场景。',
      en: 'Emphasize first-token and total latency for interactive workloads.',
    },
    icon: <ThunderboltOutlined />,
  },
  {
    value: 'success_first',
    title: { zh: '成功率优先', en: 'Success first' },
    description: {
      zh: '优先选择更稳定、成功率更高的线路，适合重要业务。',
      en: 'Emphasize success rate and stability for critical production traffic.',
    },
    icon: <CheckCircleFilled />,
  },
]

const effectiveStrategyLabels: Record<RouteStrategyType, { zh: string; en: string }> = {
  balanced: { zh: '智能自动', en: 'Smart auto' },
  cost_first: { zh: '价格优先', en: 'Price first' },
  latency_first: { zh: '速度优先', en: 'Speed first' },
  stability_first: { zh: '成功率优先', en: 'Success first' },
}

function copy(value: { zh: string; en: string }) {
  return value[getLocale()]
}

function effectiveStrategyLabel(value: RouteStrategyType) {
  return copy(effectiveStrategyLabels[value] ?? { zh: '自定义', en: 'Custom' })
}

export default function OwnerRoutingPreferenceCard() {
  useLocaleVersion()
  const preference = useOwnerRoutingPreference()
  const update = useUpdateOwnerRoutingPreference()
  const english = getLocale() === 'en'
  const selected = preference.data?.preference

  return (
    <ProCard
      className="owner-routing-preference-card"
      title={english ? 'Routing preference' : '路由偏好'}
      subTitle={
        english
          ? 'Choose what matters most: price, speed, or success rate.'
          : '选择线路时，更看重价格、速度还是成功率。'
      }
      bordered
      style={{ marginBottom: 16 }}
      extra={
        preference.data ? (
          <Tag color="blue" data-testid="effective-strategy">
            {english
              ? `In use: ${effectiveStrategyLabel(preference.data.effective_strategy)}`
              : `正在使用：${effectiveStrategyLabel(preference.data.effective_strategy)}`}
          </Tag>
        ) : null
      }
    >
      <Alert
        type="info"
        showIcon
        style={{ marginBottom: 16 }}
        message={
          english
            ? 'Availability always comes first. Faulty routes are paused automatically and rejoin after recovery.'
            : '系统始终优先保障可用性；异常线路会自动停用，确认恢复后再重新启用。'
        }
      />

      {preference.isLoading ? (
        <Skeleton active paragraph={{ rows: 3 }} />
      ) : preference.error ? (
        <Alert
          type="warning"
          showIcon
          message={english ? 'Routing preference is temporarily unavailable' : '路由偏好暂不可用'}
          description={friendlyErrorMessage(preference.error)}
          action={
            <Button onClick={() => preference.refetch()}>{english ? 'Retry' : '重试'}</Button>
          }
        />
      ) : (
        <div
          role="radiogroup"
          aria-label={english ? 'Routing preference' : '路由偏好'}
          style={{
            display: 'grid',
            gridTemplateColumns: 'repeat(auto-fit, minmax(210px, 1fr))',
            gap: 12,
          }}
        >
          {options.map((option) => {
            const active = selected === option.value
            const saving = update.isPending && update.variables === option.value
            return (
              <Button
                key={option.value}
                role="radio"
                aria-checked={active}
                type={active ? 'primary' : 'default'}
                loading={saving}
                disabled={update.isPending}
                onClick={() => {
                  if (!active) update.mutate(option.value)
                }}
                style={{ height: 'auto', minHeight: 112, padding: 16, textAlign: 'left' }}
              >
                <Space align="start">
                  <span style={{ fontSize: 22, lineHeight: 1 }}>{option.icon}</span>
                  <Space direction="vertical" size={4}>
                    <Typography.Text strong style={{ color: 'inherit' }}>
                      {copy(option.title)}
                    </Typography.Text>
                    <Typography.Text
                      style={{
                        color: active ? 'rgba(255,255,255,.85)' : undefined,
                        whiteSpace: 'normal',
                      }}
                    >
                      {copy(option.description)}
                    </Typography.Text>
                  </Space>
                </Space>
              </Button>
            )
          })}
        </div>
      )}

      {update.error ? (
        <Alert
          type="error"
          showIcon
          style={{ marginTop: 12 }}
          message={english ? 'Failed to save routing preference' : '路由偏好保存失败'}
          description={friendlyErrorMessage(update.error)}
        />
      ) : null}
    </ProCard>
  )
}
