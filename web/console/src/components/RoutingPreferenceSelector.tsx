import {
  CheckCircleFilled,
  DollarOutlined,
  RiseOutlined,
  ThunderboltOutlined,
} from '@ant-design/icons'
import { Button, Space, Typography } from 'antd'
import type { PlatformRoutingPreference } from '../api/types'

// The copy states what the platform data plane actually does: smart_auto is
// the curated default order (not a magic blend), and the two telemetry-backed
// choices say out loud that they may pick a more expensive channel — the
// preference changes what a request costs, so the card must say so.
const options: Array<{
  value: PlatformRoutingPreference
  title: string
  description: string
  icon: React.ReactNode
}> = [
  {
    value: 'smart_auto',
    title: '智能自动',
    description: '使用平台维护的默认排序，适合大多数场景。',
    icon: <RiseOutlined />,
  },
  {
    value: 'price_first',
    title: '价格优先',
    description: '在通过健康与容量筛选的渠道中，优先综合单价更低的。',
    icon: <DollarOutlined />,
  },
  {
    value: 'speed_first',
    title: '速度优先',
    description: '按最近 30 分钟实测首字延迟优先，可能选择价格更高的渠道。',
    icon: <ThunderboltOutlined />,
  },
  {
    value: 'success_first',
    title: '成功率优先',
    description: '按最近 30 分钟实测成功率优先，可能选择价格更高的渠道。',
    icon: <CheckCircleFilled />,
  },
]

export const routingPreferenceLabels: Record<PlatformRoutingPreference, string> = {
  smart_auto: '智能自动',
  price_first: '价格优先',
  speed_first: '速度优先',
  success_first: '成功率优先',
}

interface RoutingPreferenceSelectorProps {
  /** Stored preference; absent behaves as smart_auto, so that card shows selected. */
  value?: PlatformRoutingPreference
  onSelect: (value: PlatformRoutingPreference) => void
  /** The choice currently being saved; shows a spinner on that card only. */
  savingValue?: PlatformRoutingPreference
  disabled?: boolean
}

export default function RoutingPreferenceSelector({
  value,
  onSelect,
  savingValue,
  disabled,
}: RoutingPreferenceSelectorProps) {
  const selected = value ?? 'smart_auto'
  return (
    <div
      role="radiogroup"
      aria-label="路由偏好"
      style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(210px, 1fr))', gap: 12 }}
    >
      {options.map((option) => {
        const active = selected === option.value
        return (
          <Button
            key={option.value}
            role="radio"
            aria-checked={active}
            type={active ? 'primary' : 'default'}
            loading={savingValue === option.value}
            disabled={disabled && savingValue !== option.value}
            onClick={() => {
              if (!active) onSelect(option.value)
            }}
            style={{ height: 'auto', minHeight: 104, padding: 16, textAlign: 'left' }}
          >
            <Space align="start">
              <span style={{ fontSize: 22, lineHeight: 1 }}>{option.icon}</span>
              <Space direction="vertical" size={4}>
                <Typography.Text strong style={{ color: 'inherit' }}>
                  {option.title}
                </Typography.Text>
                <Typography.Text
                  style={{ color: active ? 'rgba(255,255,255,.85)' : undefined, whiteSpace: 'normal' }}
                >
                  {option.description}
                </Typography.Text>
              </Space>
            </Space>
          </Button>
        )
      })}
    </div>
  )
}
