import { Link } from 'react-router'
import { PageContainer, ProCard, StatisticCard } from '@ant-design/pro-components'
import { Alert, Button, List, Space } from 'antd'
import { useActiveRoleUser, useAudits, useConnectors, useInstances } from '../api/hooks'
import { usePlatformUsage, usePlatformWallet } from '../api/platformDistributionHooks'
import { currentUserId, getActiveRole } from '../api/auth'
import { consoleFeatureFlags } from '../config/featureFlags'
import { EmptyTeach, RelativeTime } from '../components/common'
import { ActivityRiskTag } from '../components/tags'
import { effectiveEventLevel } from '../eventLevel'
import { auditActivityDescription } from '../i18n'
import { useLocaleVersion } from '../i18n/react'

export default function Overview() {
  useLocaleVersion()
  const user = useActiveRoleUser()
  const role = getActiveRole(user)
  const ownerRole = role === 'admin' || role === 'client'
  const scopedUserId = role === 'admin' ? undefined : currentUserId(user)
  const instances = useInstances(scopedUserId, ownerRole)
  const connectors = useConnectors(scopedUserId, undefined, ownerRole)
  const audits = useAudits(scopedUserId, ownerRole)
  const clientRole = role === 'client'
  const wallet = usePlatformWallet(undefined, clientRole)
  const usage = usePlatformUsage(undefined, clientRole)

  if (!ownerRole) {
    return (
      <PageContainer title="E2M Control">
        <Alert
          type="info"
          showIcon
          message="当前版本已移除供应商工作台"
          description="E2M 原生管理平台上游、下游 API Key、余额与请求转发；Connector 只负责站长自有号池。"
        />
      </PageContainer>
    )
  }

  const instanceList = instances.data ?? []
  const connectorList = connectors.data ?? []
  const online = connectorList.filter((item) => item.status === 'online').length
  const attention = connectorList.filter((item) => item.status !== 'online').length
  const instanceNames = new Map(instanceList.map((instance) => [instance.id, instance.name]))
  const recentAudits = (audits.data ?? []).slice(0, 8)
  const usageItems = usage.data?.items ?? []
  const todayStart = new Date()
  todayStart.setHours(0, 0, 0, 0)
  const todaySettledMicros = usageItems
    .filter((item) => item.status === 'settled' && new Date(item.created_at) >= todayStart)
    .reduce((sum, item) => sum + item.settled_micros, 0)

  return (
    <PageContainer
      title={role === 'admin' ? 'E2M Control 总览' : '自有号池总览'}
      subTitle="Connector 只负责托管站长自己的网关与账号池"
    >
      <StatisticCard.Group direction="row">
        <StatisticCard
          loading={instances.isLoading}
          statistic={{ title: '托管实例', value: instanceList.length }}
        />
        <StatisticCard.Divider />
        <StatisticCard
          loading={connectors.isLoading}
          statistic={{ title: '在线 Connector', value: online }}
        />
        <StatisticCard.Divider />
        <StatisticCard
          loading={connectors.isLoading}
          statistic={{ title: '需要处理', value: attention }}
        />
      </StatisticCard.Group>

      {clientRole ? (
        <ProCard title="平台消费" style={{ marginTop: 16 }}>
          <StatisticCard.Group direction="row">
            <StatisticCard
              loading={wallet.isLoading}
              statistic={{
                title: '钱包余额（CNY）',
                value: wallet.data ? (wallet.data.available_micros / 1_000_000).toFixed(2) : '--',
              }}
            />
            <StatisticCard.Divider />
            <StatisticCard
              loading={usage.isLoading}
              statistic={{
                title: '今日结算消费（CNY）',
                value: (todaySettledMicros / 1_000_000).toFixed(2),
              }}
            />
          </StatisticCard.Group>
          <Space wrap style={{ marginTop: 12 }}>
            <Link to="/model-market">
              <Button>查看模型市场</Button>
            </Link>
            <Link to="/platform-distribution">
              <Button>管理 Key 与用量</Button>
            </Link>
            {consoleFeatureFlags.payments ? (
              <>
                <Link to="/recharge">
                  <Button type="primary">余额充值</Button>
                </Link>
                <Link to="/redeem">
                  <Button>使用兑换码</Button>
                </Link>
              </>
            ) : null}
          </Space>
        </ProCard>
      ) : null}

      {attention > 0 ? (
        <Alert
          type="warning"
          showIcon
          style={{ marginTop: 16 }}
          message={`${attention} 个 Connector 未在线或已被停用`}
          action={
            <Link to="/connectors">
              <Button>检查接入状态</Button>
            </Link>
          }
        />
      ) : null}

      <ProCard title="常用操作" style={{ marginTop: 16 }}>
        <Space wrap>
          <Link to="/instances">
            <Button type="primary">管理托管实例</Button>
          </Link>
          <Link to="/connectors">
            <Button>Connector 接入</Button>
          </Link>
          <Link to="/pool-health">
            <Button>查看自有号池健康</Button>
          </Link>
          <Link to="/notifications">
            <Button>通知设置</Button>
          </Link>
        </Space>
      </ProCard>

      <ProCard title="最近活动" style={{ marginTop: 16 }} loading={audits.isLoading}>
        {recentAudits.length === 0 ? (
          <EmptyTeach title="暂无审计记录" />
        ) : (
          <List
            size="small"
            dataSource={recentAudits}
            renderItem={(audit) => (
              <List.Item>
                <Space>
                  <ActivityRiskTag
                    level={effectiveEventLevel(audit.event_level, audit.risk_level, audit.result)}
                  />
                  <span>
                    {auditActivityDescription(
                      audit.action,
                      audit.result,
                      audit.error_message,
                      instanceNames.get(audit.instance_id ?? '') ?? '',
                      audit.risk_level,
                      audit.details,
                    )}
                  </span>
                </Space>
                <RelativeTime value={audit.created_at} />
              </List.Item>
            )}
          />
        )}
      </ProCard>
    </PageContainer>
  )
}
