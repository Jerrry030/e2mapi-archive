import { useMemo, type KeyboardEvent } from 'react'
import { useSearchParams } from 'react-router'
import { PageContainer, ProCard } from '@ant-design/pro-components'
import type { ProColumns } from '@ant-design/pro-components'
import {
  Alert,
  Button,
  Input,
  Select,
  Skeleton,
  Space,
  Statistic,
  Tabs,
  Tag,
  Typography,
} from 'antd'
import { ReloadOutlined } from '@ant-design/icons'
import { useRoutePlans, useUpstreamChannels, useUpstreamPools, useUsers } from '../api/hooks'
import {
  useIntelligenceChanges,
  useIntelligenceFrontier,
  useIntelligenceLinks,
  useIntelligenceOverview,
  useIntelligenceRates,
  useSaveIntelligenceLink,
  useIntelligenceSources,
} from '../api/upstreamIntelligenceHooks'
import { useUpstreamMarginSummary } from '../api/upstreamMarginHooks'
import type {
  IntelligenceAccuracy,
  IntelligenceChange,
  IntelligenceOverviewFilter,
} from '../api/upstreamIntelligence'
import { DataQualityBanner } from '../components/intelligence/DataQualityBanner'
import { CostQualityFrontier } from '../components/intelligence/CostQualityFrontier'
import { EffectiveRateLeaderboard } from '../components/intelligence/EffectiveRateLeaderboard'
import { EvidenceDrawer } from '../components/intelligence/EvidenceDrawer'
import { SourceWalletPanel } from '../components/intelligence/SourceWalletPanel'
import { IntelligenceLinkManager } from '../components/intelligence/IntelligenceLinkManager'
import { UpstreamMarginSummary } from '../components/intelligence/UpstreamMarginSummary'
import { RecommendationLab } from '../components/intelligence/RecommendationLab'
import { RecommendationExecutionPolicies } from '../components/intelligence/RecommendationExecutionPolicies'
import { RecommendationRollouts } from '../components/intelligence/RecommendationRollouts'
import { LocalizedProTable as ProTable } from '../components/LocalizedProTable'
import { friendlyErrorMessage } from '../api/errors'
import { getLocale, t } from '../i18n'
import { useLocaleVersion } from '../i18n/react'
import {
  readIntelligenceLocation,
  writeIntelligenceLocation,
  type IntelligenceLocation,
  type IntelligenceTab,
} from './upstreamIntelligenceLocation'
import { formatFreshComparableCoverage } from './upstreamIntelligenceMetrics'

export function EvidenceButton({
  evidenceId,
  onOpen,
}: {
  evidenceId: string
  onOpen: (evidenceId: string) => void
}) {
  const open = () => onOpen(evidenceId)
  const activateFromKeyboard = (event: KeyboardEvent<HTMLButtonElement>) => {
    if (event.key !== 'Enter') return
    // Make Enter activation an explicit UI contract for browser/assistive
    // technology paths that do not synthesize the button's native click.
    event.preventDefault()
    open()
  }

  return (
    <Button type="link" size="small" onClick={open} onKeyDown={activateFromKeyboard}>
      {t('upstreamIntelligence.common.viewEvidence')}
    </Button>
  )
}

export default function UpstreamIntelligence() {
  useLocaleVersion()
  const [search, setSearch] = useSearchParams()
  const location = useMemo(() => readIntelligenceLocation(search), [search])
  const users = useUsers()
  const owners = (users.data ?? []).filter((user) => user.enabled && user.roles.includes('client'))
  const userId = location.userId ?? owners[0]?.id
  const filter: IntelligenceOverviewFilter = {
    source_id: location.source_id,
    model: location.model,
    group: location.group,
    provider: location.provider,
    currency: location.currency,
    window: location.window,
    accuracy: location.accuracy,
  }
  const overview = useIntelligenceOverview(userId, filter)
  const sources = useIntelligenceSources(userId, { limit: 200 })
  const rates = useIntelligenceRates(userId, { ...filter, limit: 200 })
  const changes = useIntelligenceChanges(userId, {
    source_id: location.source_id,
    model: location.model,
    group: location.group,
    window: location.window,
    limit: 200,
  })
  const frontier = useIntelligenceFrontier(userId, { ...filter, limit: 200 })
  const links = useIntelligenceLinks(userId)
  const margin = useUpstreamMarginSummary(userId, location.window ?? '24h')
  const channels = useUpstreamChannels()
  const routePlans = useRoutePlans(userId)
  const pools = useUpstreamPools()
  const saveLink = useSaveIntelligenceLink()

  const update = (patch: Partial<IntelligenceLocation>) => {
    setSearch(writeIntelligenceLocation({ ...location, userId, ...patch }))
  }
  const refresh = () => {
    overview.refetch()
    rates.refetch()
    changes.refetch()
    frontier.refetch()
    links.refetch()
    margin.refetch()
    sources.refetch()
  }
  const error = overview.error ?? rates.error ?? changes.error ?? frontier.error
  const accuracyOptions: Array<{ value: IntelligenceAccuracy; label: string }> = [
    'exact',
    'derived',
    'estimated',
    'unknown',
    'unattributed',
  ].map((value) => ({
    value: value as IntelligenceAccuracy,
    label: t(`upstreamIntelligence.accuracy.${value}`, value),
  }))
  const formatDate = (value: string) =>
    new Date(value).toLocaleString(getLocale() === 'zh' ? 'zh-CN' : 'en-US')

  const changeColumns: ProColumns<IntelligenceChange>[] = [
    {
      title: t('upstreamIntelligence.evidence.event'),
      render: (_, item) => (
        <Tag
          color={
            item.severity === 'critical' ? 'red' : item.severity === 'warning' ? 'orange' : 'blue'
          }
        >
          {t(`upstreamIntelligence.events.${item.event_type}`, item.event_type)}
        </Tag>
      ),
    },
    {
      title: t('upstreamIntelligence.common.source'),
      render: (_, item) => item.source.display_name,
    },
    {
      title: t('upstreamIntelligence.common.model'),
      dataIndex: 'model_key',
      render: (value) => String(value || '—'),
    },
    {
      title: t('upstreamIntelligence.common.group'),
      dataIndex: 'group_key',
      render: (value) => String(value || '—'),
    },
    {
      title: t('upstreamIntelligence.evidence.changeAmount'),
      render: (_, item) =>
        item.percentage_change !== null
          ? `${item.percentage_change}%`
          : (item.absolute_change ?? t('upstreamIntelligence.common.unknown')),
    },
    {
      title: t('upstreamIntelligence.evidence.confirmedAt'),
      render: (_, item) => formatDate(item.confirmed_at),
    },
    {
      title: t('upstreamIntelligence.common.actions'),
      valueType: 'option',
      render: (_, item) => (
        <EvidenceButton evidenceId={item.id} onOpen={(evidenceId) => update({ evidenceId })} />
      ),
    },
  ]

  return (
    <PageContainer
      title={t('upstreamIntelligence.page.title')}
      subTitle={t('upstreamIntelligence.page.subtitle')}
      extra={[
        <Button
          key="refresh"
          icon={<ReloadOutlined />}
          loading={overview.isFetching}
          onClick={refresh}
        >
          {t('upstreamIntelligence.common.refresh')}
        </Button>,
      ]}
    >
      <Space direction="vertical" size="large" style={{ width: '100%' }}>
        <DataQualityBanner data={overview.data} />
        {error ? (
          <Alert
            type="error"
            showIcon
            message={t('upstreamIntelligence.page.loadError')}
            description={`${friendlyErrorMessage(error)} ${t('upstreamIntelligence.page.unknownDataNotice')}`}
          />
        ) : null}

        <ProCard bordered>
          <div
            className="intelligence-filter-grid"
            role="group"
            aria-label={t('upstreamIntelligence.page.filterGroup')}
          >
            <Select
              aria-label={t('upstreamIntelligence.page.selectCustomer')}
              showSearch
              optionFilterProp="label"
              placeholder={t('upstreamIntelligence.page.selectCustomer')}
              style={{ minWidth: 220 }}
              value={userId}
              options={owners.map((user) => ({
                value: user.id,
                label: user.display_name || user.email,
              }))}
              onChange={(next) =>
                update({ userId: next, source_id: undefined, evidenceId: undefined })
              }
            />
            <Select
              aria-label={t('upstreamIntelligence.page.filterSource')}
              allowClear
              placeholder={t('upstreamIntelligence.common.source')}
              style={{ minWidth: 190 }}
              value={location.source_id}
              options={(sources.data?.items ?? []).map((source) => ({
                value: source.id,
                label: source.display_name,
              }))}
              onChange={(source_id) => update({ source_id })}
            />
            <Input
              aria-label={t('upstreamIntelligence.page.filterModel')}
              allowClear
              placeholder={t('upstreamIntelligence.common.model')}
              style={{ width: 180 }}
              value={location.model}
              onChange={(event) => update({ model: event.target.value || undefined })}
            />
            <Input
              aria-label={t('upstreamIntelligence.page.filterGroupName')}
              allowClear
              placeholder={t('upstreamIntelligence.common.group')}
              style={{ width: 150 }}
              value={location.group}
              onChange={(event) => update({ group: event.target.value || undefined })}
            />
            <Input
              aria-label={t('upstreamIntelligence.page.filterProvider')}
              allowClear
              placeholder={t('upstreamIntelligence.page.providerPlaceholder')}
              style={{ width: 140 }}
              value={location.provider}
              onChange={(event) => update({ provider: event.target.value || undefined })}
            />
            <Input
              aria-label={t('upstreamIntelligence.page.filterCurrency')}
              allowClear
              placeholder={t('upstreamIntelligence.page.currencyPlaceholder')}
              style={{ width: 140 }}
              value={location.currency}
              onChange={(event) =>
                update({ currency: event.target.value.toUpperCase() || undefined })
              }
            />
            <Select
              aria-label={t('upstreamIntelligence.page.timeWindow')}
              value={location.window}
              style={{ width: 110 }}
              options={[
                { value: '24h', label: t('upstreamIntelligence.page.latest24h') },
                { value: '7d', label: t('upstreamIntelligence.page.latest7d') },
              ]}
              onChange={(window) => update({ window })}
            />
            <Select
              aria-label={t('upstreamIntelligence.page.filterAccuracy')}
              allowClear
              placeholder={t('upstreamIntelligence.page.accuracyPlaceholder')}
              style={{ width: 150 }}
              value={location.accuracy}
              options={accuracyOptions}
              onChange={(accuracy) => update({ accuracy })}
            />
          </div>
        </ProCard>

        {!userId && !users.isLoading ? (
          <Alert
            type="info"
            showIcon
            message={t('upstreamIntelligence.page.noCustomer')}
            description={t('upstreamIntelligence.page.noCustomerDescription')}
          />
        ) : null}

        {overview.isLoading ? (
          <Skeleton active />
        ) : overview.data ? (
          <div className="intelligence-summary-grid">
            <ProCard bordered>
              <Statistic
                title={t('upstreamIntelligence.page.metricSources')}
                value={overview.data.metrics.source_count}
              />
            </ProCard>
            <ProCard bordered>
              <Statistic
                title={t('upstreamIntelligence.page.metricFreshCoverage')}
                value={
                  formatFreshComparableCoverage(overview.data.metrics.fresh_comparable_coverage) ??
                  t('upstreamIntelligence.common.unknown')
                }
                suffix={overview.data.metrics.fresh_comparable_coverage !== null ? '%' : undefined}
              />
            </ProCard>
            <ProCard bordered>
              <Statistic
                title={t('upstreamIntelligence.page.metricBalanceRisk')}
                value={overview.data.metrics.balance_risk_source_count}
              />
            </ProCard>
            <ProCard bordered>
              <Statistic
                title={t('upstreamIntelligence.page.metricChanges24h')}
                value={overview.data.metrics.changes_24h}
              />
            </ProCard>
            <ProCard bordered>
              <Statistic
                title={t('upstreamIntelligence.page.metricComparableRates')}
                value={overview.data.metrics.comparable_rate_count}
                suffix={`/ ${overview.data.metrics.current_rate_count}`}
              />
            </ProCard>
          </div>
        ) : null}

        <Tabs
          activeKey={location.tab}
          onChange={(tab) => update({ tab: tab as IntelligenceTab })}
          items={[
            {
              key: 'overview',
              label: t('upstreamIntelligence.page.tabOverview'),
              children: (
                <Space direction="vertical" size="large" style={{ width: '100%' }}>
                  <ProCard title={t('upstreamIntelligence.page.walletCard')} bordered>
                    <SourceWalletPanel
                      wallets={overview.data?.wallets ?? []}
                      onEvidence={(evidenceId) => update({ evidenceId })}
                    />
                  </ProCard>
                  <ProCard title={t('upstreamIntelligence.page.ratesCard')} bordered>
                    <EffectiveRateLeaderboard
                      rates={overview.data?.top_rates ?? []}
                      loading={overview.isLoading}
                      onEvidence={(evidenceId) => update({ evidenceId })}
                    />
                  </ProCard>
                  <ProCard title={t('upstreamIntelligence.page.frontierCard')} bordered>
                    <CostQualityFrontier points={overview.data?.frontier ?? []} />
                  </ProCard>
                </Space>
              ),
            },
            {
              key: 'rates',
              label: t('upstreamIntelligence.page.tabRates'),
              children: (
                <EffectiveRateLeaderboard
                  rates={rates.data?.items ?? []}
                  loading={rates.isLoading}
                  onEvidence={(evidenceId) => update({ evidenceId })}
                />
              ),
            },
            {
              key: 'changes',
              label: t('upstreamIntelligence.page.tabChanges'),
              children: (
                <div
                  className="intelligence-scroll-region"
                  role="region"
                  aria-label={t('upstreamIntelligence.page.changesRegion')}
                  tabIndex={0}
                >
                  <ProTable<IntelligenceChange>
                    rowKey="id"
                    search={false}
                    options={false}
                    loading={changes.isLoading}
                    dataSource={changes.data?.items ?? []}
                    columns={changeColumns}
                    pagination={{ pageSize: 20 }}
                    scroll={{ x: 'max-content' }}
                  />
                </div>
              ),
            },
            {
              key: 'opportunities',
              label: t('upstreamIntelligence.page.tabOpportunities'),
              children: (
                <Space direction="vertical" style={{ width: '100%' }}>
                  <Alert
                    type="info"
                    showIcon
                    message={t('upstreamIntelligence.page.opportunityReadOnly')}
                    description={t('upstreamIntelligence.page.opportunityDescription')}
                  />
                  <CostQualityFrontier points={frontier.data?.items ?? []} />
                </Space>
              ),
            },
            {
              key: 'margin',
              label: t('upstreamIntelligence.page.tabMargin'),
              children: margin.isLoading ? (
                <Skeleton active />
              ) : margin.error ? (
                <Alert
                  type="error"
                  showIcon
                  message={t('upstreamIntelligence.page.marginLoadError')}
                  description={`${friendlyErrorMessage(margin.error)} ${t('upstreamIntelligence.page.unknownDataNotice')}`}
                />
              ) : margin.data ? (
                <UpstreamMarginSummary view={margin.data} />
              ) : null,
            },
            {
              key: 'links',
              label: t('upstreamIntelligence.page.tabLinks'),
              children: (
                <IntelligenceLinkManager
                  userId={userId}
                  sources={sources.data?.items ?? []}
                  channels={channels.data ?? []}
                  links={links.data?.items ?? []}
                  loading={links.isLoading || channels.isLoading}
                  saving={saveLink.isPending}
                  onSave={(input) => saveLink.mutateAsync(input)}
                />
              ),
            },
            {
              key: 'recommendations',
              label: t('upstreamIntelligence.page.tabRecommendations'),
              children: <RecommendationLab userId={userId} />,
            },
            {
              key: 'execution',
              label: t('upstreamIntelligence.page.tabExecution'),
              children: (
                <RecommendationExecutionPolicies
                  userId={userId}
                  plans={(routePlans.data ?? []).filter((plan) => plan.user_id === userId)}
                  pools={pools.data ?? []}
                />
              ),
            },
            {
              key: 'rollouts',
              label: t('upstreamIntelligence.page.tabRollouts'),
              children: <RecommendationRollouts userId={userId} />,
            },
          ]}
        />
        <Typography.Text type="secondary">
          {t('upstreamIntelligence.page.snapshot', undefined, {
            version: overview.data ? `v${overview.data.fact_version}` : '—',
            time: overview.data ? formatDate(overview.data.generated_at) : '—',
          })}
        </Typography.Text>
      </Space>

      <EvidenceDrawer
        userId={userId}
        evidenceId={location.evidenceId}
        onClose={() => update({ evidenceId: undefined })}
      />
    </PageContainer>
  )
}
