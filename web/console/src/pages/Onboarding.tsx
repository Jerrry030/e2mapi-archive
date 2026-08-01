import { Link } from 'react-router'
import { PageContainer, ProCard, StatisticCard } from '@ant-design/pro-components'
import { Alert, Button, Empty, Progress, Space, Tag, Typography } from 'antd'
import {
  ApiOutlined,
  BellOutlined,
  CheckCircleOutlined,
  CloudServerOutlined,
  ReloadOutlined,
  SafetyCertificateOutlined,
} from '@ant-design/icons'
import { useNotificationRoutes, useOwnerOnboarding } from '../api/hooks'
import { RelativeTime } from '../components/common'
import type { OwnerOnboardingInstance } from '../api/types'
import {
  onboardingNeedsUserAction,
  onboardingProgress,
  onboardingStateCopy,
  onboardingTone,
} from './onboardingView'

function instanceAction(item: OwnerOnboardingInstance) {
  switch (item.service_state) {
    case 'awaiting_connector':
    case 'connector_update_required':
      return { to: '/connectors', label: '查看安装与更新' }
    case 'connector_offline':
    case 'gateway_setup_required':
      return { to: '/connectors', label: '检查 Connector' }
    case 'active':
    case 'degraded':
    case 'awaiting_verification':
    case 'verification_failed':
      return { to: '/pool-health', label: '查看服务质量' }
    default:
      return { to: '/assigned-keys', label: '查看已交付资源' }
  }
}

function InstanceProgress({ item }: { item: OwnerOnboardingInstance }) {
  const copy = onboardingStateCopy(item.service_state)
  const action = instanceAction(item)
  return (
    <ProCard bordered style={{ marginBottom: 12 }}>
      <div style={{ display: 'flex', justifyContent: 'space-between', gap: 16, flexWrap: 'wrap' }}>
        <Space direction="vertical" size={4} style={{ minWidth: 240, flex: 1 }}>
          <Space wrap>
            <Typography.Title level={5} style={{ margin: 0 }}>
              {item.instance_name}
            </Typography.Title>
            <Tag>{item.instance_kind}</Tag>
            <Tag color={onboardingTone(item.service_state)}>{copy.label}</Tag>
          </Space>
          <Typography.Text type="secondary">{copy.detail}</Typography.Text>
          {item.next_attempt_at && item.service_state === 'retrying' ? (
            <Typography.Text type="secondary">
              下次自动重试 <RelativeTime value={item.next_attempt_at} />
              ；连续失败请联系平台管理员。
            </Typography.Text>
          ) : null}
          <Typography.Text type="secondary">
            Connector 最近在线：
            <RelativeTime value={item.connector_last_seen_at} />
          </Typography.Text>
        </Space>
        <Space direction="vertical" style={{ width: 260, maxWidth: '100%' }}>
          <Progress
            percent={onboardingProgress(item)}
            status={
              item.service_state === 'connector_offline'
                ? 'exception'
                : item.service_state === 'active'
                  ? 'success'
                  : 'active'
            }
          />
          <Typography.Text type="secondary">
            已验证 Key {item.verified_keys}/{item.delivered_keys} · 已验证可调用{' '}
            {item.callable_bindings}/{item.published_bindings}
          </Typography.Text>
          {item.awaiting_verification_bindings > 0 ? (
            <Typography.Text type="secondary">
              {item.awaiting_verification_bindings} 条线路已部署，等待首个真实请求或主动探测
            </Typography.Text>
          ) : null}
          {item.verification_failed_bindings > 0 ? (
            <Typography.Text type="danger">
              {item.verification_failed_bindings} 条线路调用验证失败
            </Typography.Text>
          ) : null}
          <Link to={action.to}>
            <Button
              type={onboardingNeedsUserAction(item.service_state) ? 'primary' : 'default'}
              block
            >
              {action.label}
            </Button>
          </Link>
        </Space>
      </div>
    </ProCard>
  )
}

export default function Onboarding() {
  const onboarding = useOwnerOnboarding()
  const notifications = useNotificationRoutes()
  const summary = onboarding.data?.summary
  const hasNotification = (notifications.data ?? []).some((route) => route.enabled)
  return (
    <PageContainer
      title="接入进度"
      subTitle="Connector 就绪后，平台会自动完成资源分配、交付、发布和验证"
    >
      {onboarding.error ? (
        <Alert
          type="error"
          showIcon
          style={{ marginBottom: 16 }}
          message="暂时无法读取接入进度"
          action={<Button onClick={() => onboarding.refetch()}>重试</Button>}
        />
      ) : null}
      <StatisticCard.Group style={{ marginBottom: 16 }}>
        <StatisticCard
          loading={onboarding.isLoading}
          statistic={{
            title: '托管实例',
            value: summary?.total_instances ?? 0,
            prefix: <CloudServerOutlined />,
          }}
        />
        <StatisticCard.Divider />
        <StatisticCard
          loading={onboarding.isLoading}
          statistic={{
            title: 'Connector 已就绪',
            value: summary?.connector_ready ?? 0,
            prefix: <ApiOutlined />,
          }}
        />
        <StatisticCard.Divider />
        <StatisticCard
          loading={onboarding.isLoading}
          statistic={{
            title: '服务已启用',
            value: summary?.active_instances ?? 0,
            prefix: <CheckCircleOutlined />,
          }}
        />
        <StatisticCard.Divider />
        <StatisticCard
          loading={onboarding.isLoading}
          statistic={{
            title: '需要你处理',
            value: summary?.action_required ?? 0,
            prefix: <SafetyCertificateOutlined />,
          }}
        />
      </StatisticCard.Group>
      {summary &&
      summary.total_instances > 0 &&
      summary.active_instances === summary.total_instances ? (
        <Alert
          type="success"
          showIcon
          style={{ marginBottom: 16 }}
          message="所有实例的托管服务均已启用"
          description="接下来可以在“服务质量与路由”中查看运行质量并选择智能路由偏好。"
          action={
            <Link to="/pool-health">
              <Button>查看服务质量</Button>
            </Link>
          }
        />
      ) : null}
      <ProCard
        title="实例接入状态"
        bordered
        loading={onboarding.isLoading}
        extra={
          <Button
            icon={<ReloadOutlined />}
            loading={onboarding.isFetching}
            onClick={() => onboarding.refetch()}
          >
            刷新
          </Button>
        }
        style={{ marginBottom: 16 }}
      >
        {(onboarding.data?.instances ?? []).length ? (
          onboarding.data!.instances.map((item) => (
            <InstanceProgress key={item.instance_id} item={item} />
          ))
        ) : !onboarding.isLoading && !onboarding.error ? (
          <Empty description="还没有托管实例">
            <Link to="/instances">
              <Button type="primary" icon={<CloudServerOutlined />}>
                接入第一个实例
              </Button>
            </Link>
          </Empty>
        ) : null}
      </ProCard>
      <ProCard title="可选：接收服务通知" bordered>
        <Space direction="vertical">
          <Typography.Text type="secondary">
            通知不是启用托管服务的前置条件。配置后可接收线路自动切换、资源交付异常和恢复结果。
          </Typography.Text>
          <Link to="/notifications">
            <Button icon={<BellOutlined />}>{hasNotification ? '管理通知' : '配置通知'}</Button>
          </Link>
        </Space>
      </ProCard>
    </PageContainer>
  )
}
