import { useMemo, useState } from 'react'
import { Link } from 'react-router'
import { PageContainer, ProCard, StatisticCard } from '@ant-design/pro-components'
import { Alert, Badge, Button, Empty, Progress, Space, Table, Tag } from 'antd'
import type { ColumnsType } from 'antd/es/table'
import { SettingOutlined, SyncOutlined } from '@ant-design/icons'
import { useCheckInstanceHealthNow, useHealthSnapshots, useInstances } from '../api/hooks'
import { friendlyErrorMessage, friendlyInlineError } from '../api/errors'
import type { AccountHealth, Instance, InstanceHealthSnapshot } from '../api/types'
import { RelativeTime } from '../components/common'
import { InstanceMonitorPolicyDrawer } from '../components/InstanceMonitorPolicyDrawer'
import { t } from '../i18n'
import { useLocaleVersion } from '../i18n/react'
import { healthPercent, healthSummaryMetrics } from './healthSummaryMetrics'

function accountColumns(): ColumnsType<AccountHealth> {
  return [
    {
      title: t('poolHealth.columns.account'),
      dataIndex: 'display_name',
      render: (value, record) => value || record.account_id,
    },
    {
      title: t('poolHealth.columns.status'),
      dataIndex: 'status',
      width: 120,
      render: (value) => value || t('common.notAvailable'),
    },
    {
      title: t('poolHealth.columns.health'),
      dataIndex: 'healthy',
      width: 140,
      render: (_, record) =>
        record.healthy ? (
          <Badge status="success" text={t('poolHealth.healthy')} />
        ) : (
          <Badge
            status="error"
            text={t('poolHealth.unhealthy', undefined, { count: record.fail_streak })}
          />
        ),
    },
    {
      title: t('poolHealth.columns.scheduling'),
      dataIndex: 'schedulable',
      width: 130,
      render: (value) =>
        value ? (
          <Tag color="success">{t('poolHealth.scheduling')}</Tag>
        ) : (
          <Tag>{t('poolHealth.notScheduling')}</Tag>
        ),
    },
  ]
}

function InstanceHealthCard({
  instance,
  snapshot,
  onSettings,
}: {
  instance: Instance
  snapshot?: InstanceHealthSnapshot
  onSettings: () => void
}) {
  const checkNow = useCheckInstanceHealthNow(instance.id)
  const score = snapshot
    ? healthPercent(snapshot.healthy_count, snapshot.total_accounts)
    : undefined

  return (
    <ProCard
      bordered
      style={{ marginBottom: 16 }}
      title={<Link to={`/instances/${instance.id}/accounts`}>{instance.name}</Link>}
      extra={
        <Space wrap>
          {snapshot ? <RelativeTime value={snapshot.checked_at} /> : null}
          <Button icon={<SettingOutlined />} onClick={onSettings}>
            {t('poolHealth.settings')}
          </Button>
          <Button
            type="primary"
            icon={<SyncOutlined spin={checkNow.isPending} />}
            loading={checkNow.isPending}
            onClick={() => checkNow.mutate()}
          >
            {t('poolHealth.checkNow')}
          </Button>
        </Space>
      }
    >
      {!snapshot ? (
        <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t('poolHealth.noSnapshot')} />
      ) : (
        <>
          {snapshot.last_error ? (
            <Alert
              type="error"
              style={{ marginBottom: 12 }}
              message={friendlyInlineError(snapshot.last_error)}
            />
          ) : null}
          <Space size="large" wrap style={{ marginBottom: 12 }}>
            <Progress
              type="circle"
              size={56}
              percent={score ?? 0}
              format={() => (score === undefined ? '-' : `${Math.round(score)}%`)}
              status={score === undefined ? 'normal' : score === 100 ? 'success' : 'normal'}
            />
            <span>
              {t('poolHealth.accountSummary', undefined, {
                healthy: snapshot.healthy_count,
                total: snapshot.total_accounts,
                schedulable: snapshot.schedulable_count,
              })}
            </span>
          </Space>
          <Table
            rowKey="account_id"
            size="small"
            pagination={false}
            scroll={{ x: 'max-content' }}
            columns={accountColumns()}
            dataSource={snapshot.accounts ?? []}
          />
        </>
      )}
    </ProCard>
  )
}

export default function PoolHealth() {
  useLocaleVersion()
  const instances = useInstances()
  const snapshots = useHealthSnapshots()
  const [monitoring, setMonitoring] = useState<Instance | null>(null)
  const snapshotByInstance = useMemo(
    () => new Map((snapshots.data ?? []).map((snapshot) => [snapshot.instance_id, snapshot])),
    [snapshots.data],
  )
  const metrics = healthSummaryMetrics(snapshots.data ?? [])
  const loadError = instances.error ?? snapshots.error

  return (
    <PageContainer
      title="自有号池健康"
      subTitle="数据直接来自站长环境中的 Connector，不包含平台 Sub2API 号池"
    >
      <StatisticCard.Group direction="row" style={{ marginBottom: 16 }}>
        <StatisticCard
          loading={instances.isLoading}
          statistic={{ title: '托管实例', value: (instances.data ?? []).length }}
        />
        <StatisticCard.Divider />
        <StatisticCard
          loading={snapshots.isLoading}
          statistic={{ title: '可调度账号', value: metrics.scheduledAccounts }}
        />
        <StatisticCard.Divider />
        <StatisticCard
          loading={snapshots.isLoading}
          statistic={{ title: '异常账号', value: metrics.unhealthyAccounts }}
        />
      </StatisticCard.Group>

      {loadError ? (
        <Alert
          type="error"
          showIcon
          message="无法读取自有号池健康状态"
          description={friendlyErrorMessage(loadError)}
          action={
            <Button
              onClick={() => {
                instances.refetch()
                snapshots.refetch()
              }}
            >
              重试
            </Button>
          }
        />
      ) : (instances.data ?? []).length === 0 && !instances.isLoading ? (
        <Empty description="还没有托管实例">
          <Link to="/instances">
            <Button type="primary">接入第一个实例</Button>
          </Link>
        </Empty>
      ) : (
        (instances.data ?? []).map((instance) => (
          <InstanceHealthCard
            key={instance.id}
            instance={instance}
            snapshot={snapshotByInstance.get(instance.id)}
            onSettings={() => setMonitoring(instance)}
          />
        ))
      )}

      <InstanceMonitorPolicyDrawer instance={monitoring} onClose={() => setMonitoring(null)} />
    </PageContainer>
  )
}
