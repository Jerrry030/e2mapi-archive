import { useMemo, useState } from 'react'
import {
  Alert,
  Button,
  Descriptions,
  Drawer,
  Empty,
  Modal,
  Select,
  Space,
  Steps,
  Table,
  Tag,
  Typography,
} from 'antd'
import { ReloadOutlined, SafetyCertificateOutlined } from '@ant-design/icons'
import type { ColumnsType } from 'antd/es/table'
import { friendlyErrorMessage } from '../../api/errors'
import type { UpstreamRecommendation } from '../../api/recommendationLab'
import { useRecommendations } from '../../api/recommendationLabHooks'
import type {
  RecommendationRollout,
  RecommendationRolloutGate,
  RecommendationRolloutOperation,
  RecommendationRolloutStage,
  RecommendationRolloutStatus,
} from '../../api/recommendationRollout'
import {
  useAdvanceRecommendationRollout,
  useRecommendationRollout,
  useRecommendationRollouts,
  useRollbackRecommendationRollout,
  useStartRecommendationRollout,
} from '../../api/recommendationRolloutHooks'
import { getLocale, t } from '../../i18n'
import { useLocaleVersion } from '../../i18n/react'

const stages: Exclude<RecommendationRolloutStage, 0>[] = [10, 25, 50, 100]

const statusColors: Record<RecommendationRolloutStatus, string> = {
  ready: 'blue',
  applying: 'processing',
  observing: 'cyan',
  rollback_required: 'error',
  completed: 'success',
  rolled_back: 'default',
  blocked: 'warning',
}

const operationStatusColors = {
  pending: 'default',
  running: 'processing',
  succeeded: 'success',
  failed: 'error',
  superseded: 'default',
} as const

export function RecommendationRollouts({ userId }: { userId?: number }) {
  useLocaleVersion()
  const [selectedId, setSelectedId] = useState<string>()
  const [recommendationId, setRecommendationId] = useState<string>()
  const rollouts = useRecommendationRollouts(userId)
  const recommendations = useRecommendations(userId, 'dry_run_passed')
  const start = useStartRecommendationRollout()
  const advance = useAdvanceRecommendationRollout()
  const rollback = useRollbackRecommendationRollout()
  const selected = useRecommendationRollout(userId, selectedId)

  const activeRecommendationIDs = useMemo(
    () => new Set((rollouts.data ?? []).map((item) => item.recommendation_id)),
    [rollouts.data],
  )
  const startCandidates = (recommendations.data ?? []).filter(
    (item) => !activeRecommendationIDs.has(item.id),
  )

  const requestStart = () => {
    if (!userId || !recommendationId) return
    const recommendation = startCandidates.find((item) => item.id === recommendationId)
    Modal.confirm({
      title: t('upstreamIntelligence.rollouts.confirmStart'),
      content: t('upstreamIntelligence.rollouts.confirmStartDescription'),
      okText: t('upstreamIntelligence.rollouts.start'),
      onOk: async () => {
        const created = await start.mutateAsync({ userId, recommendationId })
        setRecommendationId(undefined)
        setSelectedId(created.id)
      },
      ...(recommendation
        ? {
            content: t('upstreamIntelligence.rollouts.confirmStartPlan', undefined, {
              plan: recommendationPlan(recommendation),
            }),
          }
        : {}),
    })
  }

  const requestAdvance = (rollout: RecommendationRollout) => {
    if (!userId) return
    Modal.confirm({
      title: t('upstreamIntelligence.rollouts.confirmAdvance'),
      content: t('upstreamIntelligence.rollouts.confirmAdvanceDescription', undefined, {
        stage: formatStage(rollout.stage),
      }),
      okText: t('upstreamIntelligence.rollouts.advanceConfirm'),
      onOk: () => advance.mutateAsync({ userId, rolloutId: rollout.id }),
    })
  }

  const requestRollback = (rollout: RecommendationRollout) => {
    if (!userId) return
    Modal.confirm({
      title: t('upstreamIntelligence.rollouts.confirmRollback'),
      content: t('upstreamIntelligence.rollouts.confirmRollbackDescription'),
      okText: t('upstreamIntelligence.rollouts.rollbackNow'),
      okButtonProps: { danger: true },
      onOk: () => rollback.mutateAsync({ userId, rolloutId: rollout.id }),
    })
  }

  const columns: ColumnsType<RecommendationRollout> = [
    {
      title: t('upstreamIntelligence.common.status'),
      width: 140,
      render: (_, item) => <RolloutStatus status={item.status} />,
    },
    {
      title: t('upstreamIntelligence.rollouts.planStage'),
      width: 190,
      render: (_, item) => (
        <Space direction="vertical" size={0}>
          <Typography.Text>
            {item.plan_id || t('upstreamIntelligence.rollouts.unknownPlan')}
          </Typography.Text>
          <Typography.Text type="secondary">
            {t('upstreamIntelligence.rollouts.currentPending', undefined, {
              current: formatStage(item.stage),
              pending: formatPendingStage(item.pending_stage),
            })}
          </Typography.Text>
        </Space>
      ),
    },
    {
      title: t('upstreamIntelligence.rollouts.gate'),
      width: 150,
      render: (_, item) => <GateStatus gate={item.gate} />,
    },
    {
      title: t('upstreamIntelligence.rollouts.baseline'),
      width: 180,
      render: (_, item) => (
        <Space direction="vertical" size={0}>
          <Tag color={item.baseline_verified ? 'green' : 'red'}>
            {item.baseline_verified
              ? t('upstreamIntelligence.rollouts.baselineVerified')
              : t('upstreamIntelligence.rollouts.baselineUnverified')}
          </Tag>
          <Typography.Text type="secondary">
            {t('upstreamIntelligence.common.accounts', undefined, {
              count: formatKnownCount(item.account_count),
            })}
          </Typography.Text>
        </Space>
      ),
    },
    {
      title: t('upstreamIntelligence.rollouts.observeUntil'),
      width: 190,
      render: (_, item) => formatDate(item.observe_until),
    },
    {
      title: t('upstreamIntelligence.rollouts.latestOperation'),
      width: 190,
      render: (_, item) => <OperationSummary operation={item.latest_operation} />,
    },
    {
      title: t('upstreamIntelligence.rollouts.stageVerification'),
      width: 140,
      render: (_, item) => (
        <Tag color={item.last_after_verified ? 'green' : 'default'}>
          {item.last_after_verified
            ? t('upstreamIntelligence.rollouts.observationVerified')
            : t('upstreamIntelligence.rollouts.noValidEvidence')}
        </Tag>
      ),
    },
    {
      title: t('upstreamIntelligence.common.actions'),
      key: 'actions',
      fixed: 'right',
      width: 250,
      render: (_, item) => (
        <Space wrap>
          <Button size="small" onClick={() => setSelectedId(item.id)}>
            {t('upstreamIntelligence.rollouts.details')}
          </Button>
          <Button
            size="small"
            type="primary"
            disabled={!observationDue(item)}
            loading={advance.isPending && advance.variables?.rolloutId === item.id}
            onClick={() => requestAdvance(item)}
          >
            {t('upstreamIntelligence.rollouts.advance')}
          </Button>
          <Button
            size="small"
            danger
            disabled={item.status === 'rolled_back'}
            loading={rollback.isPending && rollback.variables?.rolloutId === item.id}
            onClick={() => requestRollback(item)}
          >
            {t('upstreamIntelligence.rollouts.rollback')}
          </Button>
        </Space>
      ),
    },
  ]

  return (
    <Space direction="vertical" size="large" style={{ width: '100%' }}>
      <Alert
        type="warning"
        showIcon
        message={t('upstreamIntelligence.rollouts.warningTitle')}
        description={t('upstreamIntelligence.rollouts.warningDescription')}
      />

      <Space wrap>
        <Select
          aria-label={t('upstreamIntelligence.rollouts.selectRecommendationAria')}
          showSearch
          optionFilterProp="label"
          placeholder={
            userId
              ? t('upstreamIntelligence.rollouts.selectRecommendation')
              : t('upstreamIntelligence.common.selectCustomer')
          }
          className="intelligence-rollout-select"
          value={recommendationId}
          disabled={!userId}
          options={startCandidates.map((item) => ({
            value: item.id,
            label: `${recommendationPlan(item)} · ${shortFingerprint(item.fingerprint)}`,
          }))}
          onChange={setRecommendationId}
        />
        <Button
          type="primary"
          icon={<SafetyCertificateOutlined />}
          disabled={!userId || !recommendationId}
          loading={start.isPending}
          onClick={requestStart}
        >
          {t('upstreamIntelligence.rollouts.start')}
        </Button>
        <Button
          icon={<ReloadOutlined />}
          disabled={!userId}
          loading={rollouts.isFetching || recommendations.isFetching}
          onClick={() => {
            rollouts.refetch()
            recommendations.refetch()
          }}
        >
          {t('upstreamIntelligence.common.refresh')}
        </Button>
      </Space>

      {rollouts.error ? (
        <Alert
          type="error"
          showIcon
          message={t('upstreamIntelligence.rollouts.loadError')}
          description={`${friendlyErrorMessage(rollouts.error)} ${t('upstreamIntelligence.rollouts.unknownNotice')}`}
        />
      ) : null}

      <div
        className="intelligence-scroll-region"
        role="region"
        aria-label={t('upstreamIntelligence.rollouts.regionLabel')}
        tabIndex={0}
      >
        <Table<RecommendationRollout>
          rowKey="id"
          loading={rollouts.isLoading}
          dataSource={rollouts.data ?? []}
          columns={columns}
          pagination={{ pageSize: 20, hideOnSinglePage: true }}
          scroll={{ x: 1300 }}
          locale={{
            emptyText: userId
              ? t('upstreamIntelligence.rollouts.empty')
              : t('upstreamIntelligence.common.selectCustomer'),
          }}
        />
      </div>

      <Drawer
        title={t('upstreamIntelligence.rollouts.drawerTitle')}
        width={760}
        open={Boolean(selectedId)}
        destroyOnClose
        onClose={() => setSelectedId(undefined)}
      >
        {selected.isLoading ? (
          <Typography.Text>{t('upstreamIntelligence.common.loading')}</Typography.Text>
        ) : selected.error ? (
          <Alert type="error" showIcon message={friendlyErrorMessage(selected.error)} />
        ) : selected.data ? (
          <RolloutDetail rollout={selected.data} />
        ) : (
          <Empty />
        )}
      </Drawer>
    </Space>
  )
}

function RolloutDetail({ rollout }: { rollout: RecommendationRollout }) {
  return (
    <Space direction="vertical" size="large" style={{ width: '100%' }}>
      <Steps
        responsive
        current={currentStep(rollout)}
        status={
          rollout.status === 'rollback_required' || rollout.status === 'blocked'
            ? 'error'
            : 'process'
        }
        items={stages.map((stage) => ({
          title: `${stage}%`,
          description: stageDescription(rollout, stage),
        }))}
      />

      <Descriptions bordered size="small" column={1}>
        <Descriptions.Item label={t('upstreamIntelligence.rollouts.executionStatus')}>
          <RolloutStatus status={rollout.status} />
        </Descriptions.Item>
        <Descriptions.Item label={t('upstreamIntelligence.rollouts.currentPendingLabel')}>
          {formatStage(rollout.stage)} / {formatPendingStage(rollout.pending_stage)}
        </Descriptions.Item>
        <Descriptions.Item label={t('upstreamIntelligence.rollouts.gate')}>
          <GateStatus gate={rollout.gate} />
        </Descriptions.Item>
        <Descriptions.Item label={t('upstreamIntelligence.rollouts.baseline')}>
          <Tag color={rollout.baseline_verified ? 'green' : 'red'}>
            {rollout.baseline_verified
              ? t('upstreamIntelligence.rollouts.verified')
              : t('upstreamIntelligence.rollouts.unverified')}
          </Tag>{' '}
          {t('upstreamIntelligence.common.accounts', undefined, {
            count: formatKnownCount(rollout.account_count),
          })}
        </Descriptions.Item>
        <Descriptions.Item label={t('upstreamIntelligence.rollouts.rollbackReadback')}>
          <Tag color={rollout.rollback_verified ? 'green' : 'default'}>
            {rollout.rollback_verified
              ? t('upstreamIntelligence.rollouts.readbackMatches')
              : t('upstreamIntelligence.rollouts.unverified')}
          </Tag>
        </Descriptions.Item>
        <Descriptions.Item label={t('upstreamIntelligence.rollouts.stageEvidence')}>
          <Tag color={rollout.last_after_verified ? 'green' : 'default'}>
            {rollout.last_after_verified
              ? t('upstreamIntelligence.rollouts.verified')
              : t('upstreamIntelligence.rollouts.unverified')}
          </Tag>
        </Descriptions.Item>
        <Descriptions.Item label={t('upstreamIntelligence.rollouts.observeUntil')}>
          {formatDate(rollout.observe_until)}
        </Descriptions.Item>
        <Descriptions.Item label={t('upstreamIntelligence.rollouts.recommendationExpires')}>
          {formatDate(rollout.recommendation_expires_at)}
        </Descriptions.Item>
        <Descriptions.Item label={t('upstreamIntelligence.rollouts.plan')}>
          {rollout.plan_id || t('upstreamIntelligence.common.unknown')} ·{' '}
          {t('upstreamIntelligence.rollouts.generation')}{' '}
          {formatKnownCount(rollout.scheduling_generation)}
        </Descriptions.Item>
        <Descriptions.Item label={t('upstreamIntelligence.rollouts.factVersion')}>
          {formatKnownCount(rollout.fact_version)}
        </Descriptions.Item>
        <Descriptions.Item label={t('upstreamIntelligence.rollouts.evidenceCount')}>
          {Array.isArray(rollout.evidence_ids)
            ? rollout.evidence_ids.length
            : t('upstreamIntelligence.common.unknown')}
        </Descriptions.Item>
        <Descriptions.Item label={t('upstreamIntelligence.rollouts.recommendationFingerprint')}>
          <Typography.Text code copyable ellipsis style={{ maxWidth: 560 }}>
            {rollout.recommendation_fingerprint || t('upstreamIntelligence.common.unknown')}
          </Typography.Text>
        </Descriptions.Item>
        <Descriptions.Item label={t('upstreamIntelligence.rollouts.baselineFingerprint')}>
          <Typography.Text code copyable ellipsis style={{ maxWidth: 560 }}>
            {rollout.baseline_fingerprint || t('upstreamIntelligence.common.unknown')}
          </Typography.Text>
        </Descriptions.Item>
        <Descriptions.Item label={t('upstreamIntelligence.rollouts.startedAt')}>
          {formatDate(rollout.started_at)}
        </Descriptions.Item>
        <Descriptions.Item label={t('upstreamIntelligence.common.updatedAt')}>
          {formatDate(rollout.updated_at)}
        </Descriptions.Item>
      </Descriptions>

      <div>
        <Typography.Title level={5}>
          {t('upstreamIntelligence.rollouts.latestOperation')}
        </Typography.Title>
        {rollout.latest_operation ? (
          <OperationDetail operation={rollout.latest_operation} />
        ) : (
          <Empty
            image={Empty.PRESENTED_IMAGE_SIMPLE}
            description={t('upstreamIntelligence.rollouts.noOperation')}
          />
        )}
      </div>

      {Array.isArray(rollout.rollback_reasons) && rollout.rollback_reasons.length ? (
        <Alert
          type="warning"
          showIcon
          message={t('upstreamIntelligence.rollouts.reasonsTitle')}
          description={
            <Space wrap>
              {rollout.rollback_reasons.map((reason) => (
                <Tag key={reason} color="orange">
                  {rolloutReasonLabel(reason)}
                </Tag>
              ))}
            </Space>
          }
        />
      ) : null}
    </Space>
  )
}

function OperationDetail({ operation }: { operation: RecommendationRolloutOperation }) {
  const color = operationStatusColors[operation.status]
  return (
    <Descriptions bordered size="small" column={1}>
      <Descriptions.Item label={t('upstreamIntelligence.rollouts.action')}>
        {operation.action === 'rollback'
          ? t('upstreamIntelligence.rollouts.fullBaselineRollback')
          : t('upstreamIntelligence.rollouts.writeStage', undefined, {
              stage: formatStage(operation.target_stage),
            })}
      </Descriptions.Item>
      <Descriptions.Item label={t('upstreamIntelligence.common.status')}>
        {color ? (
          <Tag color={color}>{operationStatusLabel(operation.status)}</Tag>
        ) : (
          <Tag>{t('upstreamIntelligence.common.unknown')}</Tag>
        )}
      </Descriptions.Item>
      <Descriptions.Item label={t('upstreamIntelligence.rollouts.attempts')}>
        {formatKnownCount(operation.attempts)}
      </Descriptions.Item>
      <Descriptions.Item label={t('upstreamIntelligence.rollouts.errorCode')}>
        {operation.error_code
          ? t(
              `upstreamIntelligence.rollouts.operationErrors.${operation.error_code}`,
              operation.error_code,
            )
          : '—'}
      </Descriptions.Item>
      <Descriptions.Item label={t('upstreamIntelligence.common.updatedAt')}>
        {formatDate(operation.updated_at)}
      </Descriptions.Item>
    </Descriptions>
  )
}

function OperationSummary({ operation }: { operation?: RecommendationRolloutOperation }) {
  if (!operation)
    return (
      <Typography.Text type="secondary">{t('upstreamIntelligence.common.unknown')}</Typography.Text>
    )
  return (
    <Space direction="vertical" size={0}>
      <Typography.Text>
        {operation.action === 'rollback'
          ? t('upstreamIntelligence.rollouts.rollback')
          : t('upstreamIntelligence.rollouts.writeStage', undefined, {
              stage: formatStage(operation.target_stage),
            })}
      </Typography.Text>
      <Typography.Text type={operation.status === 'failed' ? 'danger' : 'secondary'}>
        {operationStatusLabel(operation.status)}
        {operation.error_code
          ? ` · ${t(
              `upstreamIntelligence.rollouts.operationErrors.${operation.error_code}`,
              operation.error_code,
            )}`
          : ''}
      </Typography.Text>
    </Space>
  )
}

function RolloutStatus({ status }: { status: RecommendationRolloutStatus }) {
  const color = statusColors[status]
  return color ? (
    <Tag color={color}>{rolloutStatusLabel(status)}</Tag>
  ) : (
    <Tag>{t('upstreamIntelligence.common.unknown')}</Tag>
  )
}

function GateStatus({ gate }: { gate?: RecommendationRolloutGate }) {
  const status = gate?.status
  const reasons = Array.isArray(gate?.reason_codes) ? gate.reason_codes : []
  const view =
    status === 'passed'
      ? { label: t('upstreamIntelligence.rollouts.gatesPassed'), color: 'green' }
      : status === 'blocked'
        ? { label: t('upstreamIntelligence.rollouts.gateBlocked'), color: 'red' }
        : { label: t('upstreamIntelligence.common.unknown'), color: 'default' }
  return (
    <Space direction="vertical" size={0}>
      <Tag color={view.color}>{view.label}</Tag>
      {reasons.length ? (
        <Typography.Text type="secondary" ellipsis style={{ maxWidth: 180 }}>
          {reasons.map(rolloutReasonLabel).join('、')}
        </Typography.Text>
      ) : null}
    </Space>
  )
}

function formatStage(stage: unknown) {
  if (stage === 0) return t('upstreamIntelligence.rollouts.baselineStage')
  return stages.includes(stage as Exclude<RecommendationRolloutStage, 0>)
    ? `${stage}%`
    : t('upstreamIntelligence.common.unknown')
}

function formatPendingStage(stage: unknown) {
  if (stage === 0) return t('upstreamIntelligence.common.none')
  return stages.includes(stage as Exclude<RecommendationRolloutStage, 0>)
    ? `${stage}%`
    : t('upstreamIntelligence.common.unknown')
}

function formatKnownCount(value: unknown) {
  return typeof value === 'number' && Number.isSafeInteger(value) && value >= 0
    ? String(value)
    : t('upstreamIntelligence.common.unknown')
}

function formatDate(value?: string) {
  if (!value) return t('upstreamIntelligence.common.unknown')
  const date = new Date(value)
  return Number.isNaN(date.getTime())
    ? t('upstreamIntelligence.common.unknown')
    : date.toLocaleString(getLocale() === 'zh' ? 'zh-CN' : 'en-US')
}

function observationDue(rollout: RecommendationRollout) {
  if (rollout.status !== 'observing' || !rollout.observe_until) return false
  const deadline = new Date(rollout.observe_until).getTime()
  return Number.isFinite(deadline) && deadline <= Date.now()
}

function currentStep(rollout: RecommendationRollout) {
  const pending = stages.indexOf(rollout.pending_stage as Exclude<RecommendationRolloutStage, 0>)
  if (pending >= 0) return pending
  const current = stages.indexOf(rollout.stage as Exclude<RecommendationRolloutStage, 0>)
  return current >= 0 ? current : 0
}

function stageDescription(
  rollout: RecommendationRollout,
  stage: Exclude<RecommendationRolloutStage, 0>,
) {
  if (rollout.pending_stage === stage) return t('upstreamIntelligence.rollouts.stageWriting')
  if (typeof rollout.stage === 'number' && rollout.stage >= stage)
    return t('upstreamIntelligence.rollouts.stageVerified')
  return t('upstreamIntelligence.rollouts.stagePending')
}

function shortFingerprint(value: string) {
  return value ? `${value.slice(0, 12)}…` : t('upstreamIntelligence.rollouts.unknownFingerprint')
}

function recommendationPlan(recommendation: UpstreamRecommendation) {
  return recommendation.affected_plan_ids?.[0] || t('upstreamIntelligence.rollouts.unknownPlan')
}

function rolloutStatusLabel(status: RecommendationRolloutStatus) {
  return t(`upstreamIntelligence.rollouts.statuses.${status}`, status)
}

function operationStatusLabel(status: RecommendationRolloutOperation['status']) {
  return t(`upstreamIntelligence.rollouts.operationStatuses.${status}`, status)
}

function rolloutReasonLabel(reason: string) {
  return t(`upstreamIntelligence.rollouts.blockReasons.${reason}`, reason)
}
