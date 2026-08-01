import { ProCard } from '@ant-design/pro-components'
import type { ProColumns } from '@ant-design/pro-components'
import { Alert, Button, Progress, Space, Statistic, Tag, Typography } from 'antd'
import { CheckCircleOutlined, ReloadOutlined, SafetyCertificateOutlined } from '@ant-design/icons'
import { useOwnerPoolHealth } from '../api/hooks'
import type { OwnerPoolIncident, OwnerPoolSwitchResult } from '../api/types'
import { friendlyErrorMessage } from '../api/errors'
import { EmptyTeach, RelativeTime } from '../components/common'
import { LocalizedProTable as ProTable } from '../components/LocalizedProTable'
import { t } from '../i18n'
import { useLocaleVersion } from '../i18n/react'
import {
  formatMilliseconds,
  formatSuccessRate,
  incidentTone,
  ownerPoolAvailability,
  ownerPoolServiceState,
  switchTone,
} from './ownerPoolHealthView'

function incidentStatus(status: OwnerPoolIncident['status']) {
  return <Tag color={incidentTone(status)}>{t(`poolHealth.owner.incident.${status}`, status)}</Tag>
}

function switchStatus(result: OwnerPoolSwitchResult['result']) {
  return <Tag color={switchTone(result)}>{t(`poolHealth.owner.switch.${result}`, result)}</Tag>
}

export default function OwnerHealthSummary() {
  useLocaleVersion()
  const health = useOwnerPoolHealth()
  const summary = health.data

  if (health.isLoading) return <ProCard loading />
  if (health.error) {
    return (
      <Alert
        type="error"
        showIcon
        message={t('poolHealth.owner.loadError')}
        description={friendlyErrorMessage(health.error)}
        action={<Button onClick={() => health.refetch()}>{t('poolHealth.summary.retry')}</Button>}
      />
    )
  }
  if (!summary) return null

  const availability = ownerPoolAvailability(summary)
  const serviceState = ownerPoolServiceState(summary)
  const hasPublished = serviceState !== 'empty'
  const incidentColumns: ProColumns<OwnerPoolIncident>[] = [
    {
      title: t('poolHealth.owner.columns.status'),
      dataIndex: 'status',
      width: 130,
      render: (_, record) => incidentStatus(record.status),
    },
    {
      title: t('poolHealth.owner.successRate'),
      dataIndex: 'success_rate',
      width: 110,
      render: (value) => formatSuccessRate(typeof value === 'number' ? value : null),
    },
    {
      title: t('poolHealth.owner.ttftP95'),
      dataIndex: 'ttft_p95_ms',
      width: 110,
      render: (value) => formatMilliseconds(typeof value === 'number' ? value : null),
    },
    {
      title: t('poolHealth.owner.durationP95'),
      dataIndex: 'duration_p95_ms',
      width: 120,
      render: (value) => formatMilliseconds(typeof value === 'number' ? value : null),
    },
    {
      title: t('poolHealth.owner.samples'),
      dataIndex: 'sample_count',
      width: 88,
    },
    {
      title: t('poolHealth.owner.columns.recovery'),
      width: 185,
      render: (_, record) => {
        if (!record.recovery) return '-'
        return (
          <Space direction="vertical" size={0}>
            <Typography.Text>
              {record.recovery.rollout_stage
                ? `${record.recovery.rollout_stage}% 回归观察`
                : t('poolHealth.owner.probeProgress', undefined, {
                    success: record.recovery.successful_probes,
                    required: record.recovery.required_probes,
                  })}
            </Typography.Text>
            {record.recovery.observe_after ? (
              <Typography.Text type="secondary">
                观察截止 <RelativeTime value={record.recovery.observe_after} />
              </Typography.Text>
            ) : null}
            {record.recovery.next_probe_at ? (
              <Typography.Text type="secondary">
                {t('poolHealth.owner.nextProbe')}{' '}
                <RelativeTime value={record.recovery.next_probe_at} />
              </Typography.Text>
            ) : null}
            {record.recovery.last_probe_at ? (
              <Typography.Text type="secondary">
                {t('poolHealth.owner.lastProbe')}{' '}
                <RelativeTime value={record.recovery.last_probe_at} />
              </Typography.Text>
            ) : null}
          </Space>
        )
      },
    },
    {
      title: t('poolHealth.owner.columns.updatedAt'),
      width: 125,
      render: (_, record) => <RelativeTime value={record.updated_at ?? record.detected_at} />,
    },
  ]
  const switchColumns: ProColumns<OwnerPoolSwitchResult>[] = [
    {
      title: t('poolHealth.owner.columns.result'),
      dataIndex: 'result',
      width: 150,
      render: (_, record) => switchStatus(record.result),
    },
    {
      title: t('poolHealth.owner.columns.startedAt'),
      dataIndex: 'started_at',
      width: 150,
      render: (_, record) => <RelativeTime value={record.started_at} />,
    },
    {
      title: t('poolHealth.owner.columns.finishedAt'),
      dataIndex: 'finished_at',
      width: 150,
      render: (_, record) => <RelativeTime value={record.finished_at} />,
    },
  ]

  return (
    <>
      <div style={{ display: 'flex', justifyContent: 'flex-end', marginBottom: 16 }}>
        <Button
          icon={<ReloadOutlined />}
          loading={health.isFetching}
          onClick={() => health.refetch()}
        >
          {t('poolHealth.summary.refresh')}
        </Button>
      </div>

      <div
        style={{
          display: 'grid',
          gridTemplateColumns: 'repeat(auto-fit, minmax(180px, 1fr))',
          gap: 16,
          marginBottom: 16,
        }}
      >
        <ProCard bordered>
          <Statistic
            title={t('poolHealth.owner.published')}
            value={summary.capacity.published}
            prefix={<SafetyCertificateOutlined />}
          />
        </ProCard>
        <ProCard bordered>
          <Statistic
            title={t('poolHealth.owner.schedulable')}
            value={summary.capacity.schedulable}
            valueStyle={{ color: summary.capacity.schedulable ? '#389e0d' : undefined }}
          />
        </ProCard>
        <ProCard bordered>
          <Statistic
            title={t('poolHealth.owner.isolated')}
            value={summary.capacity.isolated}
            valueStyle={{ color: summary.capacity.isolated ? '#cf1322' : undefined }}
          />
        </ProCard>
        <ProCard bordered>
          <Statistic
            title={t('poolHealth.owner.awaitingVerification')}
            value={summary.capacity.awaiting_verification}
            valueStyle={{ color: summary.capacity.awaiting_verification ? '#d48806' : undefined }}
          />
        </ProCard>
        <ProCard bordered>
          <Statistic
            title={t('poolHealth.owner.verificationFailed')}
            value={summary.capacity.verification_failed}
            valueStyle={{ color: summary.capacity.verification_failed ? '#cf1322' : undefined }}
          />
        </ProCard>
        <ProCard bordered>
          <Statistic
            title={t('poolHealth.owner.availability')}
            value={availability ?? '-'}
            suffix={availability === undefined ? undefined : '%'}
            prefix={<CheckCircleOutlined />}
          />
        </ProCard>
      </div>

      {serviceState === 'empty' ? (
        <Alert
          type="info"
          showIcon
          style={{ marginBottom: 16 }}
          message={t('poolHealth.owner.empty')}
        />
      ) : serviceState === 'awaiting_verification' ? (
        <Alert
          type="warning"
          showIcon
          style={{ marginBottom: 16 }}
          message={t('poolHealth.owner.awaitingVerificationMessage')}
        />
      ) : serviceState === 'verification_failed' ? (
        <Alert
          type="error"
          showIcon
          style={{ marginBottom: 16 }}
          message={t('poolHealth.owner.verificationFailedMessage')}
        />
      ) : serviceState === 'fail_closed' ? (
        <Alert
          type="error"
          showIcon
          style={{ marginBottom: 16 }}
          message={t('poolHealth.owner.failClosed')}
        />
      ) : serviceState === 'degraded' ? (
        <Alert
          type="warning"
          showIcon
          style={{ marginBottom: 16 }}
          message={t('poolHealth.owner.degraded')}
        />
      ) : serviceState === 'partially_unavailable' ? (
        <Alert
          type="warning"
          showIcon
          style={{ marginBottom: 16 }}
          message={t('poolHealth.owner.partiallyUnavailable')}
        />
      ) : (
        <Alert
          type="success"
          showIcon
          style={{ marginBottom: 16 }}
          message={t('poolHealth.owner.healthy')}
        />
      )}

      <ProCard title={t('poolHealth.owner.slaTitle')} bordered style={{ marginBottom: 16 }}>
        <div
          style={{
            display: 'grid',
            gridTemplateColumns: 'repeat(auto-fit, minmax(160px, 1fr))',
            gap: 16,
          }}
        >
          <div>
            <Statistic
              title={t('poolHealth.owner.successRate')}
              value={formatSuccessRate(summary.sla.success_rate)}
            />
          </div>
          <div>
            <Statistic
              title={t('poolHealth.owner.ttftP95')}
              value={formatMilliseconds(summary.sla.ttft_p95_ms)}
            />
          </div>
          <div>
            <Statistic
              title={t('poolHealth.owner.durationP95')}
              value={formatMilliseconds(summary.sla.duration_p95_ms)}
            />
          </div>
          <div>
            <Statistic title={t('poolHealth.owner.samples')} value={summary.sla.sample_count} />
          </div>
        </div>
        <Space style={{ marginTop: 12 }}>
          <Typography.Text type="secondary">{t('poolHealth.owner.window5m')}</Typography.Text>
          <Typography.Text type="secondary">
            {t('poolHealth.owner.updatedAt')} <RelativeTime value={summary.sla.updated_at} />
          </Typography.Text>
        </Space>
      </ProCard>

      <ProCard title={t('poolHealth.owner.incidentsTitle')} bordered style={{ marginBottom: 16 }}>
        <div>
          <Progress
            percent={availability}
            status={summary.capacity.schedulable === 0 && hasPublished ? 'exception' : 'normal'}
            style={{ maxWidth: 420, marginBottom: 12 }}
          />
        </div>
        <div>
          <ProColumnsTable
            rowKey={(_, index) => `incident-${index}`}
            columns={incidentColumns}
            dataSource={summary.incidents}
            empty={t('poolHealth.owner.noIncidents')}
          />
        </div>
      </ProCard>

      <ProCard title={t('poolHealth.owner.switchesTitle')} bordered>
        <ProColumnsTable
          rowKey={(_, index) => `switch-${index}`}
          columns={switchColumns}
          dataSource={summary.switches}
          empty={t('poolHealth.owner.noSwitches')}
        />
      </ProCard>
    </>
  )
}

function ProColumnsTable<T extends object>({
  rowKey,
  columns,
  dataSource,
  empty,
}: {
  rowKey: (record: T, index?: number) => string
  columns: ProColumns<T>[]
  dataSource: T[]
  empty: string
}) {
  return (
    <ProTable<T>
      rowKey={rowKey}
      search={false}
      options={false}
      pagination={dataSource.length > 8 ? { pageSize: 8 } : false}
      columns={columns}
      dataSource={dataSource}
      scroll={{ x: 'max-content' }}
      locale={{ emptyText: <EmptyTeach title={empty} /> }}
    />
  )
}
