import { useMemo, useState } from 'react'
import { Link } from 'react-router'
import { PageContainer, ProCard } from '@ant-design/pro-components'
import type { ProColumns } from '@ant-design/pro-components'
import { Alert, App, Button, Progress, Select, Space, Statistic, Tag, Typography } from 'antd'
import { ReloadOutlined } from '@ant-design/icons'
import { useInstances, useOperationsCenter, useUsers } from '../api/hooks'
import { useManualRecoverQualityCircuit } from '../api/recoveryActionHooks'
import type {
  OperationsIncident,
  OperationsOnboarding,
  OperationsSourceHealth,
  OperationsTimelineItem,
} from '../api/types'
import { friendlyErrorMessage, friendlyInlineError } from '../api/errors'
import { EmptyTeach, RelativeTime } from '../components/common'
import { LocalizedProTable as ProTable } from '../components/LocalizedProTable'
import {
  connectorErrorLabel,
  connectorTaskTypeLabel,
  onboardingErrorLabel,
  onboardingStageLabel,
  operationsStatusLabel,
  operationsTimelineKindLabel,
  operationsReasonLabel,
  reconcileRunLabel,
  t,
} from '../i18n'
import {
  filterOperationsIncidents,
  filterOperationsOnboarding,
  filterOperationsTimeline,
  type OperationsCenterFilter,
} from './operationsCenterFilters'

const incidentLabels: Record<OperationsIncident['status'], string> = {
  isolated: '已摘除',
  probing: '恢复探测',
  recovering: '灰度回归',
  needs_ejection: '待摘除',
  delivery_failed: '发布失败',
}

const incidentColors: Record<OperationsIncident['status'], string> = {
  isolated: 'error',
  probing: 'processing',
  recovering: 'processing',
  needs_ejection: 'warning',
  delivery_failed: 'error',
}

function quality(value?: number | null) {
  if (typeof value !== 'number') return <Tag>未知</Tag>
  return (
    <Progress
      percent={Math.round(value)}
      status={value <= 60 ? 'exception' : 'normal'}
      size="small"
      style={{ minWidth: 100 }}
    />
  )
}

function evidence(fresh: boolean, confidence: number, updatedAt?: string | null) {
  if (!updatedAt) return <Tag>无证据</Tag>
  return (
    <Space size={4}>
      <Tag color={fresh ? 'success' : 'warning'}>{fresh ? '新鲜' : '过期'}</Tag>
      <span>{Math.round(confidence * 100)}%</span>
      <RelativeTime value={updatedAt} />
    </Space>
  )
}

export default function OperationsCenter() {
  const { modal } = App.useApp()
  const query = useOperationsCenter()
  const users = useUsers()
  const instances = useInstances()
  const data = query.data
  const [filter, setFilter] = useState<OperationsCenterFilter>({ status: 'all' })
  const manualRecover = useManualRecoverQualityCircuit()

  const confirmManualRecovery = (incident: OperationsIncident) => {
    modal.confirm({
      title: '确认人工恢复这条线路？',
      content:
        '仅在你已人工确认上游账号、凭证和接口均已恢复后执行。系统不会把这次操作伪装成主动探测证据；恢复后仍会继续观察真实请求质量。',
      okText: '确认恢复',
      cancelText: '取消',
      okButtonProps: { danger: true },
      onOk: () =>
        manualRecover.mutateAsync({
          planId: incident.plan_id,
          channelId: incident.channel_id,
          note: 'platform operator confirmed upstream recovery',
        }),
    })
  }

  const instanceOptions = useMemo(
    () =>
      (instances.data ?? [])
        .filter((item) => !filter.userId || item.user_id === filter.userId)
        .map((item) => ({ label: item.name, value: item.id })),
    [filter.userId, instances.data],
  )
  const onboarding = filterOperationsOnboarding(data?.onboarding ?? [], filter)
  const incidents = filterOperationsIncidents(data?.incidents ?? [], filter)
  const timeline = filterOperationsTimeline(data?.timeline ?? [], filter)

  const sourceColumns: ProColumns<OperationsSourceHealth>[] = [
    {
      title: '来源',
      dataIndex: 'display_name',
      width: 200,
      ellipsis: true,
      render: (_, record) => (
        <Space direction="vertical" size={0}>
          <Typography.Text strong>{record.display_name || record.source_id}</Typography.Text>
          <Typography.Text type="secondary" copyable={{ text: record.source_id }}>
            {record.source_id}
          </Typography.Text>
        </Space>
      ),
    },
    { title: '绑定', dataIndex: 'total_bindings', width: 72 },
    {
      title: '5 分钟请求',
      dataIndex: 'passive_requests_5m',
      width: 105,
      render: (value) => Number(value ?? 0).toLocaleString(),
    },
    { title: '调度中', dataIndex: 'schedulable', width: 76 },
    { title: '已摘除', dataIndex: 'isolated', width: 76 },
    { title: '恢复中', dataIndex: 'recovering', width: 76 },
    { title: '未知', dataIndex: 'unknown', width: 68 },
    {
      title: '最差质量分',
      dataIndex: 'worst_quality_score',
      width: 130,
      render: (value) => quality(typeof value === 'number' ? value : null),
    },
    {
      title: '证据',
      width: 190,
      render: (_, record) =>
        evidence(record.evidence_fresh, record.evidence_confidence, record.evidence_updated_at),
    },
  ]

  const incidentColumns: ProColumns<OperationsIncident>[] = [
    {
      title: '状态',
      dataIndex: 'status',
      width: 100,
      render: (_, record) => (
        <Tag color={incidentColors[record.status]}>{incidentLabels[record.status]}</Tag>
      ),
    },
    {
      title: '来源 / 下游',
      width: 220,
      ellipsis: true,
      render: (_, record) => `${record.display_name || record.source_id} · ${record.plan_id}`,
    },
    {
      title: '影响面',
      width: 145,
      render: (_, record) =>
        `${record.affected_downstreams} 个下游 · ${record.affected_requests_5m.toLocaleString()} 请求/5m`,
    },
    {
      title: '当前承载',
      width: 190,
      ellipsis: true,
      render: (_, record) =>
        record.current_routes?.length
          ? record.current_routes.map((route) => route.display_name || route.source_id).join(' / ')
          : '无健康备用',
    },
    {
      title: '质量分',
      dataIndex: 'quality_score',
      width: 120,
      render: (value) => quality(typeof value === 'number' ? value : null),
    },
    {
      title: '扣分证据',
      width: 245,
      render: (_, record) =>
        `错误 ${record.penalty?.error_penalty?.toFixed(1) ?? '0.0'}/55 · 首字 ${record.penalty?.ttft_penalty?.toFixed(1) ?? '0.0'}/25 · 总耗时 ${record.penalty?.duration_penalty?.toFixed(1) ?? '0.0'}/20`,
    },
    {
      title: '证据',
      width: 190,
      render: (_, record) =>
        evidence(record.evidence_fresh, record.evidence_confidence, record.evidence_updated_at),
    },
    {
      title: '恢复方式',
      dataIndex: 'connector_recovery_mode',
      width: 110,
      render: (_, record) =>
        record.connector_recovery_mode === 'automatic' ? (
          <Tag color="success">自动探测</Tag>
        ) : (
          <Tag color="warning">需人工恢复</Tag>
        ),
    },
    {
      title: '恢复进度',
      width: 150,
      render: (_, record) =>
        record.recovery_stage
          ? `${record.recovery_stage}% 回归观察`
          : `${record.successful_probes}/3 探测`,
    },
    {
      title: '下一动作',
      width: 140,
      render: (_, record) => (
        <RelativeTime value={record.recovery_observe_after ?? record.next_probe_at} />
      ),
    },
    {
      title: '原因',
      width: 260,
      ellipsis: true,
      render: (_, record) => operationsReasonLabel(record.reason?.code ?? '', record.reason?.text),
    },
    {
      title: '操作',
      valueType: 'option',
      width: 180,
      render: (_, record) => {
        const canRecover =
          record.connector_recovery_mode === 'manual' &&
          record.status === 'isolated' &&
          record.binding_state === 'disabled' &&
          (record.circuit_state === 'open' || record.circuit_state === 'half_open')
        return (
          <Space size={8}>
            <Link to={`/upstream?tab=plans&plan_id=${encodeURIComponent(record.plan_id)}`}>
              查看计划
            </Link>
            {canRecover ? (
              <Button
                size="small"
                danger
                loading={
                  manualRecover.isPending &&
                  manualRecover.variables?.planId === record.plan_id &&
                  manualRecover.variables?.channelId === record.channel_id
                }
                onClick={() => confirmManualRecovery(record)}
              >
                人工恢复
              </Button>
            ) : null}
          </Space>
        )
      },
    },
  ]

  const timelineColumns: ProColumns<OperationsTimelineItem>[] = [
    {
      title: '类型',
      dataIndex: 'kind',
      width: 110,
      render: (value) => operationsTimelineKindLabel(String(value)),
    },
    {
      title: '状态',
      dataIndex: 'status',
      width: 105,
      render: (value) => <Tag>{operationsStatusLabel(String(value))}</Tag>,
    },
    { title: '计划', dataIndex: 'plan_id', width: 150, ellipsis: true },
    {
      title: '事件',
      dataIndex: 'title',
      width: 260,
      ellipsis: true,
      render: (value, record) => {
        const title = String(value ?? '')
        if (record.kind === 'connector_task') return connectorTaskTypeLabel(title)
        if (record.kind === 'onboarding_workflow') return onboardingStageLabel(title)
        if (record.kind === 'gateway_receipt') {
          const [kind, trigger] = title.split(' · ')
          return reconcileRunLabel(kind, trigger)
        }
        return title || '-'
      },
    },
    {
      title: '结果',
      dataIndex: 'detail',
      width: 300,
      ellipsis: true,
      render: (value, record) => {
        const detail = String(value ?? '')
        if (!detail) return '-'
        if (record.kind === 'connector_task') {
          const scheduled = detail.match(/^scheduled for (.+)$/)
          if (scheduled) return t('operations.scheduledFor', undefined, { time: scheduled[1] })
          return connectorErrorLabel(detail)
        }
        if (record.kind === 'onboarding_workflow') return onboardingErrorLabel(detail)
        return friendlyInlineError(detail) || detail
      },
    },
    {
      title: '时间',
      dataIndex: 'at',
      width: 130,
      render: (value) => <RelativeTime value={String(value)} />,
    },
  ]

  const onboardingColumns: ProColumns<OperationsOnboarding>[] = [
    {
      title: '阶段',
      dataIndex: 'stage',
      width: 120,
      render: (_, record) => (
        <Tag
          color={
            record.status === 'active'
              ? 'success'
              : record.status === 'dormant'
                ? 'default'
                : record.status === 'retryable'
                  ? 'error'
                  : 'processing'
          }
        >
          {onboardingStageLabel(record.stage)}
        </Tag>
      ),
    },
    {
      title: '实例',
      dataIndex: 'instance_id',
      width: 190,
      ellipsis: true,
      render: (value) => (
        <Link to={`/instances?instance_id=${encodeURIComponent(String(value))}`}>
          {String(value)}
        </Link>
      ),
    },
    { title: '开放池', dataIndex: 'pool_id', width: 180, ellipsis: true },
    {
      title: '计划',
      dataIndex: 'plan_id',
      width: 180,
      ellipsis: true,
      render: (value) =>
        value ? (
          <Link to={`/upstream?tab=plans&plan_id=${encodeURIComponent(String(value))}`}>
            {String(value)}
          </Link>
        ) : (
          '-'
        ),
    },
    { title: '已交付 Key', dataIndex: 'delivered_keys', width: 100 },
    { title: '尝试', dataIndex: 'attempts', width: 72 },
    {
      title: '原因',
      dataIndex: 'last_error_code',
      width: 190,
      ellipsis: true,
      render: (value) => (value ? onboardingErrorLabel(String(value)) : '-'),
    },
    {
      title: '下次动作',
      dataIndex: 'next_attempt_at',
      width: 135,
      render: (value, record) =>
        record.status === 'dormant' ? (
          '-'
        ) : (
          <RelativeTime value={value ? String(value) : record.updated_at} />
        ),
    },
  ]

  return (
    <PageContainer
      title="运维中心"
      subTitle="来源质量、客户影响、自动接入与恢复证据"
      extra={[
        <Button
          key="refresh"
          icon={<ReloadOutlined />}
          loading={query.isFetching}
          onClick={() => query.refetch()}
        >
          刷新
        </Button>,
      ]}
    >
      {query.error ? (
        <Alert
          type="error"
          showIcon
          style={{ marginBottom: 16 }}
          message="运维事实加载失败"
          description={friendlyErrorMessage(query.error)}
        />
      ) : null}

      <ProCard bordered style={{ marginBottom: 16 }}>
        <Space wrap>
          <Select
            allowClear
            showSearch
            optionFilterProp="label"
            placeholder="全部客户"
            style={{ minWidth: 220 }}
            value={filter.userId}
            options={(users.data ?? [])
              .filter((item) => item.roles.includes('client'))
              .map((item) => ({ label: item.display_name || item.email, value: item.id }))}
            onChange={(userId) =>
              setFilter((current) => ({ ...current, userId, instanceId: undefined }))
            }
          />
          <Select
            allowClear
            showSearch
            optionFilterProp="label"
            placeholder="全部实例"
            style={{ minWidth: 220 }}
            value={filter.instanceId}
            options={instanceOptions}
            onChange={(instanceId) => setFilter((current) => ({ ...current, instanceId }))}
          />
          <Select
            value={filter.status}
            style={{ minWidth: 160 }}
            options={[
              { label: '全部接入状态', value: 'all' },
              { label: '仅需关注', value: 'attention' },
              { label: '仅已就绪', value: 'active' },
            ]}
            onChange={(status) => setFilter((current) => ({ ...current, status }))}
          />
          <Button onClick={() => setFilter({ status: 'all' })}>清空筛选</Button>
        </Space>
      </ProCard>

      <div className="operations-summary-grid">
        <ProCard bordered loading={query.isLoading}>
          <Statistic title="开放事故" value={data?.summary.open_incidents ?? 0} />
        </ProCard>
        <ProCard bordered loading={query.isLoading}>
          <Statistic title="已摘除 Binding" value={data?.summary.isolated_bindings ?? 0} />
        </ProCard>
        <ProCard bordered loading={query.isLoading}>
          <Statistic title="恢复中 Binding" value={data?.summary.recovering_bindings ?? 0} />
        </ProCard>
        <ProCard bordered loading={query.isLoading}>
          <Statistic
            title="观测覆盖率"
            value={data?.summary.fresh_evidence_percent ?? 0}
            suffix="%"
          />
        </ProCard>
        <ProCard bordered loading={query.isLoading}>
          <Statistic title="需人工恢复" value={data?.summary.manual_recovery ?? 0} />
        </ProCard>
        <ProCard bordered loading={query.isLoading}>
          <Statistic title="接入处理中" value={data?.summary.onboarding_pending ?? 0} />
        </ProCard>
        <ProCard bordered loading={query.isLoading}>
          <Statistic title="接入待重试" value={data?.summary.onboarding_retryable ?? 0} />
        </ProCard>
        <ProCard bordered loading={query.isLoading}>
          <Statistic title="服务已暂停" value={data?.summary.onboarding_dormant ?? 0} />
        </ProCard>
      </div>

      {data?.summary.unknown_bindings ? (
        <Alert
          type="warning"
          showIcon
          style={{ marginBottom: 16 }}
          message={`${data.summary.unknown_bindings} 个 Binding 缺少新鲜证据，状态按未知处理`}
        />
      ) : null}

      <ProCard title="来源健康矩阵" bordered style={{ marginBottom: 16 }}>
        <ProTable<OperationsSourceHealth>
          rowKey="source_id"
          size="small"
          search={false}
          options={false}
          pagination={false}
          scroll={{ x: 'max-content' }}
          columns={sourceColumns}
          dataSource={data?.sources ?? []}
          locale={{ emptyText: <EmptyTeach title="还没有托管来源" /> }}
        />
      </ProCard>
      <ProCard
        title={`自动接入进度 · ${onboarding.length} 项`}
        bordered
        style={{ marginBottom: 16 }}
      >
        <ProTable<OperationsOnboarding>
          rowKey="id"
          size="small"
          search={false}
          options={false}
          pagination={false}
          scroll={{ x: 'max-content' }}
          columns={onboardingColumns}
          dataSource={onboarding}
          locale={{ emptyText: <EmptyTeach title="当前筛选下没有自动接入任务" /> }}
        />
      </ProCard>
      <ProCard
        title={`当前事故与恢复 · ${incidents.length} 项`}
        bordered
        style={{ marginBottom: 16 }}
      >
        <ProTable<OperationsIncident>
          rowKey={(record) => `${record.plan_id}:${record.channel_id}`}
          size="small"
          search={false}
          options={false}
          pagination={false}
          scroll={{ x: 'max-content' }}
          columns={incidentColumns}
          dataSource={incidents}
          locale={{ emptyText: <EmptyTeach title="当前筛选下没有开放事故" /> }}
        />
      </ProCard>
      <ProCard title="动作时间线与网关回执" bordered>
        <ProTable<OperationsTimelineItem>
          rowKey={(record) => `${record.kind}:${record.id}`}
          size="small"
          search={false}
          options={false}
          pagination={{ pageSize: 20, showSizeChanger: false }}
          scroll={{ x: 'max-content' }}
          columns={timelineColumns}
          dataSource={timeline}
          locale={{ emptyText: <EmptyTeach title="当前筛选下没有调度动作" /> }}
        />
      </ProCard>
    </PageContainer>
  )
}
