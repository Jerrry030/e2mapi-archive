import { Link } from 'react-router'
import { ProCard } from '@ant-design/pro-components'
import type { ProColumns } from '@ant-design/pro-components'
import { Alert, Badge, Button, Progress, Space, Statistic, Tag, Typography } from 'antd'
import { CloudServerOutlined, ReloadOutlined, SafetyCertificateOutlined } from '@ant-design/icons'
import { useCapabilities, useHealthSnapshots, useInstances } from '../api/hooks'
import { friendlyErrorMessage, friendlyInlineError } from '../api/errors'
import { EmptyTeach, RelativeTime } from '../components/common'
import { KindTag, ModeTag, RiskLevelTag } from '../components/tags'
import { LocalizedProTable as ProTable } from '../components/LocalizedProTable'
import type {
  AdapterCapability,
  AccountHealth,
  Instance,
  InstanceHealthSnapshot,
} from '../api/types'
import { capabilityDescriptionLabel, capabilityNameLabel, t } from '../i18n'
import { useLocaleVersion } from '../i18n/react'
import { healthPercent, healthSummaryMetrics } from './healthSummaryMetrics'

function healthBadge(healthy: boolean, failStreak?: number) {
  return healthy ? (
    <Badge status="success" text={t('poolHealth.healthy')} />
  ) : (
    <Badge status="error" text={t('poolHealth.unhealthy', undefined, { count: failStreak ?? 0 })} />
  )
}

function instanceName(instances: Instance[] | undefined, id?: string) {
  return instances?.find((item) => item.id === id)?.name ?? id ?? '-'
}

type AccountHealthRow = AccountHealth & {
  instance_id: string
  instance_name?: string
  checked_at: string
}

function accountStatusLabel(status?: string) {
  return t(`poolHealth.accountStatus.${status ?? 'unknown'}`, status ?? 'unknown')
}

export default function HealthSummary() {
  useLocaleVersion()
  const instances = useInstances()
  const capabilities = useCapabilities()
  const snapshots = useHealthSnapshots()

  const snaps = snapshots.data ?? []
  const {
    healthPercent: healthPct,
    scheduledAccounts: schedulableAccounts,
    unhealthyAccounts: unhealthyCount,
  } = healthSummaryMetrics(snaps)
  const loadError = instances.error ?? snapshots.error
  const healthCaps = (capabilities.data ?? []).filter((cap) =>
    ['get_health', 'list_accounts', 'get_instance_info'].includes(cap.name),
  )

  const snapshotColumns: ProColumns<InstanceHealthSnapshot>[] = [
    {
      title: t('poolHealth.summary.columns.instance'),
      dataIndex: 'instance_name',
      width: 220,
      ellipsis: true,
      render: (_, r) => (
        <Space direction="vertical" size={0}>
          <Link to={`/instances/${r.instance_id}/accounts`}>
            {r.instance_name || instanceName(instances.data, r.instance_id)}
          </Link>
          <Typography.Text type="secondary" style={{ fontSize: 12 }}>
            {r.instance_id}
          </Typography.Text>
        </Space>
      ),
    },
    {
      title: t('poolHealth.summary.columns.healthScore'),
      width: 160,
      render: (_, r) => {
        const pct = healthPercent(r.healthy_count, r.total_accounts)
        return pct === undefined ? (
          t('common.notAvailable')
        ) : (
          <Progress percent={pct} size="small" style={{ minWidth: 120 }} />
        )
      },
    },
    {
      title: t('poolHealth.summary.columns.accounts'),
      width: 96,
      render: (_, r) => `${r.healthy_count}/${r.total_accounts}`,
    },
    {
      title: t('poolHealth.scheduling'),
      dataIndex: 'schedulable_count',
      width: 96,
    },
    {
      title: t('poolHealth.summary.columns.lastCheck'),
      dataIndex: 'checked_at',
      width: 120,
      render: (_, r) => <RelativeTime value={r.checked_at} />,
    },
    {
      title: t('poolHealth.summary.columns.error'),
      dataIndex: 'last_error',
      width: 180,
      ellipsis: true,
      render: (_, record) => friendlyInlineError(record.last_error) || '-',
    },
    {
      title: t('poolHealth.summary.columns.actions'),
      valueType: 'option',
      width: 96,
      render: (_, r) => (
        <Link to={`/instances/${r.instance_id}/accounts`}>
          <Button size="small" icon={<CloudServerOutlined />}>
            {t('poolHealth.summary.accountAction')}
          </Button>
        </Link>
      ),
    },
  ]

  const accountRows = snaps.flatMap((snap) =>
    (snap.accounts ?? []).map((account) => ({
      ...account,
      instance_id: snap.instance_id,
      instance_name: snap.instance_name || instanceName(instances.data, snap.instance_id),
      checked_at: snap.checked_at,
    })),
  )
  const accountColumns: ProColumns<AccountHealthRow>[] = [
    {
      title: t('poolHealth.summary.columns.instance'),
      dataIndex: 'instance_name',
      width: 140,
      ellipsis: true,
    },
    {
      title: t('poolHealth.columns.account'),
      dataIndex: 'display_name',
      width: 160,
      ellipsis: true,
      render: (_, r) => r.display_name || r.account_id,
    },
    {
      title: t('poolHealth.columns.status'),
      dataIndex: 'status',
      width: 92,
      render: (_, record) => accountStatusLabel(record.status),
    },
    {
      title: t('poolHealth.columns.health'),
      dataIndex: 'healthy',
      width: 120,
      render: (_, r) => healthBadge(r.healthy, r.fail_streak),
    },
    {
      title: t('poolHealth.columns.scheduling'),
      dataIndex: 'schedulable',
      width: 112,
      render: (v) =>
        v ? (
          <Tag color="success">{t('poolHealth.scheduling')}</Tag>
        ) : (
          <Tag>{t('poolHealth.notScheduling')}</Tag>
        ),
    },
    {
      title: t('poolHealth.summary.columns.updatedAt'),
      dataIndex: 'checked_at',
      width: 120,
      render: (_, r) => <RelativeTime value={r.checked_at} />,
    },
  ]

  const capColumns: ProColumns<AdapterCapability>[] = [
    {
      title: t('poolHealth.summary.columns.system'),
      dataIndex: 'system',
      width: 100,
      render: (_, r) => <KindTag kind={r.system} />,
    },
    {
      title: t('poolHealth.summary.columns.capability'),
      dataIndex: 'name',
      width: 140,
      ellipsis: true,
      render: (_, record) => capabilityNameLabel(record.name),
    },
    {
      title: t('poolHealth.summary.columns.mode'),
      dataIndex: 'mode',
      width: 96,
      render: (_, r) => <ModeTag mode={r.mode} />,
    },
    {
      title: t('poolHealth.summary.columns.risk'),
      dataIndex: 'risk_level',
      width: 88,
      render: (_, r) => <RiskLevelTag level={r.risk_level} />,
    },
    {
      title: t('poolHealth.summary.columns.support'),
      dataIndex: 'supported',
      width: 96,
      render: (v) =>
        v ? (
          <Tag color="success">{t('poolHealth.summary.supported')}</Tag>
        ) : (
          <Tag>{t('poolHealth.summary.unsupported')}</Tag>
        ),
    },
    {
      title: t('poolHealth.summary.columns.description'),
      dataIndex: 'description',
      width: 180,
      ellipsis: true,
      render: (_, record) =>
        record.description ? capabilityDescriptionLabel(record.description) : '-',
    },
  ]

  if (instances.isLoading || snapshots.isLoading) return <ProCard loading />

  if (loadError) {
    return (
      <Alert
        type="error"
        showIcon
        message={t('poolHealth.loadError')}
        description={friendlyErrorMessage(loadError)}
        action={
          <Button
            onClick={() => {
              instances.refetch()
              snapshots.refetch()
              capabilities.refetch()
            }}
          >
            {t('poolHealth.summary.retry')}
          </Button>
        }
      />
    )
  }

  return (
    <>
      <div style={{ display: 'flex', justifyContent: 'flex-end', marginBottom: 16 }}>
        <Button
          icon={<ReloadOutlined />}
          loading={snapshots.isFetching || capabilities.isFetching}
          onClick={() => {
            snapshots.refetch()
            capabilities.refetch()
          }}
        >
          {t('poolHealth.summary.refresh')}
        </Button>
      </div>
      <ProCard gutter={16} style={{ marginBottom: 16 }}>
        <ProCard bordered>
          <Statistic
            title={t('poolHealth.summary.snapshotCount')}
            value={snaps.length}
            prefix={<SafetyCertificateOutlined />}
          />
        </ProCard>
        <ProCard bordered>
          <Statistic
            title={t('poolHealth.summary.accountHealth')}
            value={healthPct ?? '-'}
            suffix={healthPct === undefined ? undefined : '%'}
          />
        </ProCard>
        <ProCard bordered>
          <Statistic
            title={t('poolHealth.summary.scheduledAccounts')}
            value={schedulableAccounts}
          />
        </ProCard>
        <ProCard bordered>
          <Statistic
            title={t('poolHealth.summary.unhealthyAccounts')}
            value={unhealthyCount}
            valueStyle={{ color: unhealthyCount ? '#cf1322' : undefined }}
          />
        </ProCard>
      </ProCard>

      <Alert
        type={healthPct === undefined ? 'info' : unhealthyCount ? 'warning' : 'success'}
        showIcon
        style={{ marginBottom: 16 }}
        message={
          healthPct === undefined
            ? t('poolHealth.summary.noDataAlert')
            : unhealthyCount
              ? t('poolHealth.summary.unhealthyAlert', undefined, { count: unhealthyCount })
              : t('poolHealth.summary.healthyAlert')
        }
      />

      <ProTable<InstanceHealthSnapshot>
        rowKey="instance_id"
        loading={snapshots.isLoading || instances.isLoading}
        headerTitle={t('poolHealth.summary.instanceSnapshots')}
        search={false}
        options={{ reload: () => snapshots.refetch() }}
        columns={snapshotColumns}
        dataSource={snaps}
        scroll={{ x: 'max-content' }}
        expandable={{
          expandedRowRender: (snap) => (
            <ProTable<AccountHealth>
              rowKey="account_id"
              size="small"
              search={false}
              options={false}
              pagination={false}
              scroll={{ x: 'max-content' }}
              columns={[
                {
                  title: t('poolHealth.columns.account'),
                  dataIndex: 'display_name',
                  width: 180,
                  ellipsis: true,
                  render: (_, r) => r.display_name || r.account_id,
                },
                {
                  title: t('poolHealth.columns.status'),
                  dataIndex: 'status',
                  width: 96,
                  render: (_, record) => accountStatusLabel(record.status),
                },
                {
                  title: t('poolHealth.columns.health'),
                  dataIndex: 'healthy',
                  width: 140,
                  render: (_, r) => healthBadge(r.healthy, r.fail_streak),
                },
                {
                  title: t('poolHealth.columns.scheduling'),
                  dataIndex: 'schedulable',
                  width: 112,
                  render: (v) =>
                    v ? (
                      <Tag color="success">{t('poolHealth.scheduling')}</Tag>
                    ) : (
                      <Tag>{t('poolHealth.notScheduling')}</Tag>
                    ),
                },
              ]}
              dataSource={snap.accounts ?? []}
            />
          ),
        }}
        locale={{ emptyText: <EmptyTeach title={t('poolHealth.summary.emptySnapshots')} /> }}
      />

      <ProCard gutter={16} style={{ marginTop: 16 }}>
        <ProCard title={t('poolHealth.summary.unhealthyDetails')} bordered colSpan={16}>
          <ProTable<AccountHealthRow>
            rowKey={(r) => `${r.instance_id}-${r.account_id}`}
            size="small"
            search={false}
            options={false}
            pagination={{ pageSize: 6 }}
            columns={accountColumns}
            dataSource={accountRows.filter((item) => !item.healthy)}
            scroll={{ x: 'max-content' }}
            locale={{ emptyText: <EmptyTeach title={t('poolHealth.summary.emptyUnhealthy')} /> }}
          />
        </ProCard>
        <ProCard title={t('poolHealth.summary.healthCapabilities')} bordered colSpan={8}>
          {capabilities.isError ? (
            <Alert
              type="error"
              showIcon
              style={{ marginBottom: 12 }}
              message={friendlyErrorMessage(capabilities.error)}
            />
          ) : null}
          <ProTable<AdapterCapability>
            rowKey={(r) => `${r.system}-${r.name}`}
            size="small"
            search={false}
            options={{ reload: () => capabilities.refetch() }}
            pagination={false}
            columns={capColumns}
            dataSource={healthCaps}
            scroll={{ x: 'max-content' }}
            loading={capabilities.isLoading}
            locale={{
              emptyText: <EmptyTeach title={t('poolHealth.summary.emptyCapabilities')} />,
            }}
          />
        </ProCard>
      </ProCard>
    </>
  )
}
