import { useState } from 'react'
import {
  Alert,
  Button,
  Descriptions,
  Drawer,
  Empty,
  Modal,
  Space,
  Table,
  Tag,
  Typography,
} from 'antd'
import { ExperimentOutlined, ReloadOutlined } from '@ant-design/icons'
import type { ColumnsType } from 'antd/es/table'
import type {
  DryRunResult,
  RecommendationGenerationDiagnostic,
  RecommendationStatus,
  ShadowResult,
  UpstreamRecommendation,
} from '../../api/recommendationLab'
import {
  useGenerateRecommendations,
  useRecommendation,
  useRecommendationExperiments,
  useRecommendations,
  useRunRecommendationDryRun,
  useRunRecommendationShadow,
} from '../../api/recommendationLabHooks'
import { friendlyErrorMessage } from '../../api/errors'
import { getLocale, t } from '../../i18n'
import { useLocaleVersion } from '../../i18n/react'

const statusColors: Record<RecommendationStatus, string> = {
  open: 'blue',
  shadowing: 'processing',
  ready_for_dry_run: 'cyan',
  dry_running: 'processing',
  dry_run_passed: 'green',
  dry_run_blocked: 'red',
  dismissed: 'default',
  expired: 'default',
}

export function RecommendationLab({ userId }: { userId?: number }) {
  useLocaleVersion()
  const [selectedId, setSelectedId] = useState<string>()
  const [diagnostics, setDiagnostics] = useState<RecommendationGenerationDiagnostic[]>([])
  const recommendations = useRecommendations(userId)
  const generate = useGenerateRecommendations()
  const shadow = useRunRecommendationShadow()
  const dryRun = useRunRecommendationDryRun()
  const selected = useRecommendation(userId, selectedId)
  const experiments = useRecommendationExperiments(userId, selectedId)

  const runShadow = (recommendation: UpstreamRecommendation) => {
    if (!userId) return
    Modal.confirm({
      title: t('upstreamIntelligence.recommendations.confirmShadow'),
      content: t('upstreamIntelligence.recommendations.confirmShadowDescription'),
      okText: t('upstreamIntelligence.recommendations.runShadow'),
      onOk: () => shadow.mutateAsync({ userId, recommendationId: recommendation.id }),
    })
  }

  const runDryRun = (recommendation: UpstreamRecommendation) => {
    if (!userId) return
    Modal.confirm({
      title: t('upstreamIntelligence.recommendations.confirmDryRun'),
      content: t('upstreamIntelligence.recommendations.confirmDryRunDescription'),
      okText: t('upstreamIntelligence.recommendations.runDryRun'),
      onOk: () => dryRun.mutateAsync({ userId, recommendationId: recommendation.id }),
    })
  }

  const columns: ColumnsType<UpstreamRecommendation> = [
    {
      title: t('upstreamIntelligence.common.status'),
      dataIndex: 'status',
      width: 130,
      render: (status: RecommendationStatus) => (
        <Tag color={statusColors[status]}>{recommendationStatusLabel(status)}</Tag>
      ),
    },
    {
      title: t('upstreamIntelligence.recommendations.modelDimension'),
      render: (_, item) => (
        <Space direction="vertical" size={0}>
          <Typography.Text>{item.model_key}</Typography.Text>
          <Typography.Text type="secondary">
            {t(
              `upstreamIntelligence.priceDimensions.${item.price_dimension}`,
              item.price_dimension,
            )}
          </Typography.Text>
        </Space>
      ),
    },
    {
      title: t('upstreamIntelligence.recommendations.candidateChange'),
      render: (_, item) => (
        <Space direction="vertical" size={0}>
          <Typography.Text>
            {safeIdentity(item.from_source_id, item.from_group_key)}
          </Typography.Text>
          <Typography.Text type="secondary">
            → {safeIdentity(item.to_source_id, item.to_group_key)}
          </Typography.Text>
        </Space>
      ),
    },
    {
      title: t('upstreamIntelligence.recommendations.expectedSavings'),
      render: (_, item) => (
        <Space direction="vertical" size={0}>
          <Typography.Text strong>
            {formatPercentRatio(item.savings.percent_expected)}
          </Typography.Text>
          <Typography.Text type="secondary">
            {formatMoney(item.savings.amount_expected, item.settlement_currency)}
          </Typography.Text>
        </Space>
      ),
    },
    {
      title: t('upstreamIntelligence.recommendations.constraints'),
      render: (_, item) => (
        <Space size={[4, 4]} wrap>
          {item.constraints.map((constraint) => (
            <Tag
              key={constraint.kind}
              color={
                constraint.status === 'passed'
                  ? 'green'
                  : constraint.status === 'blocked'
                    ? 'red'
                    : 'default'
              }
            >
              {t(
                `upstreamIntelligence.recommendations.constraintKinds.${constraint.kind}`,
                constraint.kind,
              )}
              :{' '}
              {t(
                `upstreamIntelligence.recommendations.constraintStatuses.${constraint.status}`,
                constraint.status,
              )}
            </Tag>
          ))}
        </Space>
      ),
    },
    {
      title: t('upstreamIntelligence.recommendations.expiresAt'),
      render: (_, item) => formatDate(item.expires_at),
    },
    {
      title: t('upstreamIntelligence.common.actions'),
      key: 'actions',
      fixed: 'right',
      width: 250,
      render: (_, item) => (
        <Space>
          <Button size="small" onClick={() => setSelectedId(item.id)}>
            {t('upstreamIntelligence.recommendations.details')}
          </Button>
          <Button
            size="small"
            disabled={item.status !== 'open'}
            loading={shadow.isPending && shadow.variables?.recommendationId === item.id}
            onClick={() => runShadow(item)}
          >
            {t('upstreamIntelligence.recommendations.shadowAction')}
          </Button>
          <Button
            size="small"
            type="primary"
            disabled={item.status !== 'ready_for_dry_run'}
            loading={dryRun.isPending && dryRun.variables?.recommendationId === item.id}
            onClick={() => runDryRun(item)}
          >
            {t('upstreamIntelligence.recommendations.dryRunAction')}
          </Button>
        </Space>
      ),
    },
  ]

  const loadError = recommendations.error

  return (
    <Space direction="vertical" size="middle" style={{ width: '100%' }}>
      <Alert
        type="info"
        showIcon
        message={t('upstreamIntelligence.recommendations.noticeTitle')}
        description={t('upstreamIntelligence.recommendations.noticeDescription')}
      />

      <Space wrap>
        <Button
          type="primary"
          icon={<ExperimentOutlined />}
          disabled={!userId}
          loading={generate.isPending}
          onClick={async () => {
            if (!userId) return
            const result = await generate.mutateAsync(userId)
            setDiagnostics(result.blocked)
          }}
        >
          {t('upstreamIntelligence.recommendations.generate')}
        </Button>
        <Button
          icon={<ReloadOutlined />}
          loading={recommendations.isFetching}
          disabled={!userId}
          onClick={() => recommendations.refetch()}
        >
          {t('upstreamIntelligence.common.refresh')}
        </Button>
        <Typography.Text type="secondary">
          {t('upstreamIntelligence.recommendations.idempotencyNotice')}
        </Typography.Text>
      </Space>

      {loadError ? (
        <Alert
          type="error"
          showIcon
          message={t('upstreamIntelligence.recommendations.loadError')}
          description={friendlyErrorMessage(loadError)}
        />
      ) : null}

      {diagnostics.length ? (
        <Alert
          type="warning"
          showIcon
          message={t('upstreamIntelligence.recommendations.blockedTitle')}
          description={
            <Space wrap>
              {diagnostics.map((item) => (
                <Tag key={item.reason} color="orange">
                  {t(
                    `upstreamIntelligence.recommendations.generationReasons.${item.reason}`,
                    item.reason,
                  )}{' '}
                  × {item.count}
                </Tag>
              ))}
            </Space>
          }
          closable
          onClose={() => setDiagnostics([])}
        />
      ) : null}

      <div
        className="intelligence-scroll-region"
        role="region"
        aria-label={t('upstreamIntelligence.recommendations.regionLabel')}
        tabIndex={0}
      >
        <Table<UpstreamRecommendation>
          rowKey="id"
          loading={recommendations.isLoading}
          dataSource={recommendations.data ?? []}
          columns={columns}
          pagination={{ pageSize: 20, hideOnSinglePage: true }}
          scroll={{ x: 1200 }}
          locale={{
            emptyText: userId
              ? t('upstreamIntelligence.recommendations.empty')
              : t('upstreamIntelligence.common.selectCustomer'),
          }}
        />
      </div>

      <Drawer
        title={t('upstreamIntelligence.recommendations.drawerTitle')}
        width={720}
        open={Boolean(selectedId)}
        onClose={() => setSelectedId(undefined)}
        destroyOnClose
      >
        {selected.isLoading ? (
          <Typography.Text>{t('upstreamIntelligence.common.loading')}</Typography.Text>
        ) : selected.error ? (
          <Alert type="error" showIcon message={friendlyErrorMessage(selected.error)} />
        ) : selected.data ? (
          <RecommendationDetail
            recommendation={selected.data}
            shadows={experiments.shadows.data ?? []}
            dryRuns={experiments.dryRuns.data ?? []}
          />
        ) : (
          <Empty />
        )}
      </Drawer>
    </Space>
  )
}

function RecommendationDetail({
  recommendation,
  shadows,
  dryRuns,
}: {
  recommendation: UpstreamRecommendation
  shadows: ShadowResult[]
  dryRuns: DryRunResult[]
}) {
  const relatedShadows = shadows.filter(
    (experiment) => experiment.recommendation_id === recommendation.id,
  )
  const relatedDryRuns = dryRuns.filter(
    (experiment) => experiment.recommendation_id === recommendation.id,
  )
  return (
    <Space direction="vertical" size="large" style={{ width: '100%' }}>
      <Descriptions bordered size="small" column={1}>
        <Descriptions.Item label={t('upstreamIntelligence.common.status')}>
          <Tag color={statusColors[recommendation.status]}>
            {recommendationStatusLabel(recommendation.status)}
          </Tag>
        </Descriptions.Item>
        <Descriptions.Item label={t('upstreamIntelligence.common.model')}>
          {recommendation.model_key} ·{' '}
          {t(
            `upstreamIntelligence.priceDimensions.${recommendation.price_dimension}`,
            recommendation.price_dimension,
          )}
        </Descriptions.Item>
        <Descriptions.Item label={t('upstreamIntelligence.recommendations.cost')}>
          {formatMoney(recommendation.from_cost.expected, recommendation.settlement_currency)} →{' '}
          {formatMoney(recommendation.to_cost.expected, recommendation.settlement_currency)}
        </Descriptions.Item>
        <Descriptions.Item label={t('upstreamIntelligence.recommendations.savingsRange')}>
          {formatPercentRatio(recommendation.savings.percent_lower)} ～{' '}
          {formatPercentRatio(recommendation.savings.percent_upper)}
        </Descriptions.Item>
        <Descriptions.Item label={t('upstreamIntelligence.recommendations.affectedPlans')}>
          {recommendation.affected_plan_ids.map((id) => (
            <Tag key={id}>{id}</Tag>
          ))}
        </Descriptions.Item>
        <Descriptions.Item label={t('upstreamIntelligence.recommendations.factVersions')}>
          {t('upstreamIntelligence.recommendations.factVersionDetail', undefined, {
            intelligence: recommendation.intelligence_fact_version,
            cost: recommendation.cost_ledger_fact_version,
            link: recommendation.link_fact_version,
            plan: recommendation.plan_generation,
          })}
        </Descriptions.Item>
        <Descriptions.Item label={t('upstreamIntelligence.recommendations.evidenceCount')}>
          {recommendation.evidence_ids.length}
        </Descriptions.Item>
        <Descriptions.Item label={t('upstreamIntelligence.recommendations.fingerprint')}>
          <Typography.Text copyable code ellipsis style={{ maxWidth: 500 }}>
            {recommendation.fingerprint}
          </Typography.Text>
        </Descriptions.Item>
      </Descriptions>

      <div>
        <Typography.Title level={5}>
          {t('upstreamIntelligence.recommendations.shadowResults')}
        </Typography.Title>
        {relatedShadows.length ? (
          relatedShadows.map((result) => (
            <Alert
              key={result.id}
              type="success"
              showIcon
              message={t('upstreamIntelligence.recommendations.winner', undefined, {
                candidate: safeIdentity(result.winner.source_id, result.winner.group_key),
              })}
              description={t('upstreamIntelligence.recommendations.winnerDetail', undefined, {
                cost: result.winner.cost,
                quality: result.winner.quality_score,
                time: formatDate(result.evaluated_at),
              })}
              style={{ marginBottom: 8 }}
            />
          ))
        ) : (
          <Empty
            image={Empty.PRESENTED_IMAGE_SIMPLE}
            description={t('upstreamIntelligence.recommendations.noShadow')}
          />
        )}
      </div>

      <div>
        <Typography.Title level={5}>
          {t('upstreamIntelligence.recommendations.dryRunPlans')}
        </Typography.Title>
        {relatedDryRuns.length ? (
          relatedDryRuns.map((result) => (
            <Descriptions key={result.id} bordered size="small" column={1}>
              <Descriptions.Item label={t('upstreamIntelligence.recommendations.plan')}>
                {result.plan_id}
              </Descriptions.Item>
              <Descriptions.Item label={t('upstreamIntelligence.recommendations.schedulingDiff')}>
                {result.actions.map((action) => (
                  <Tag key={`${action.type}:${action.channel_id}`}>
                    {t(`operations.reconcileActions.${action.type}`, action.type)} ·{' '}
                    {action.channel_id}
                  </Tag>
                ))}
              </Descriptions.Item>
              <Descriptions.Item
                label={t('upstreamIntelligence.recommendations.actionFingerprint')}
              >
                <Typography.Text code>{result.action_fingerprint}</Typography.Text>
              </Descriptions.Item>
              <Descriptions.Item label={t('upstreamIntelligence.common.generatedAt')}>
                {formatDate(result.created_at)}
              </Descriptions.Item>
            </Descriptions>
          ))
        ) : (
          <Empty
            image={Empty.PRESENTED_IMAGE_SIMPLE}
            description={t('upstreamIntelligence.recommendations.noDryRun')}
          />
        )}
      </div>
    </Space>
  )
}

function formatPercentRatio(value: string) {
  const number = Number(value)
  return Number.isFinite(number)
    ? `${(number * 100).toFixed(2)}%`
    : t('upstreamIntelligence.common.unknown')
}

function formatMoney(value: string, currency: string) {
  return value ? `${value} ${currency}` : t('upstreamIntelligence.common.unknown')
}

function recommendationStatusLabel(status: RecommendationStatus) {
  return t(`upstreamIntelligence.recommendations.statuses.${status}`, status)
}

function formatDate(value: string) {
  return new Date(value).toLocaleString(getLocale() === 'zh' ? 'zh-CN' : 'en-US')
}

function safeIdentity(source: string, group: string) {
  return group ? `${source} / ${group}` : source
}
