import { useEffect, useMemo, useState } from 'react'
import { Link } from 'react-router'
import { PageContainer, ProCard } from '@ant-design/pro-components'
import {
  Alert,
  Button,
  Empty,
  Input,
  Progress,
  Select,
  Skeleton,
  Space,
  Statistic,
  Tag,
  Typography,
} from 'antd'
import {
  CheckCircleOutlined,
  ReloadOutlined,
  SearchOutlined,
  ThunderboltOutlined,
} from '@ant-design/icons'
import { useOwnerModelMarket } from '../api/ownerModelMarketHooks'
import type {
  OwnerModelMarketModel,
  OwnerModelMarketPrice,
  OwnerModelMarketStatus,
} from '../api/ownerModelMarket'
import type { IntelligencePriceDimension } from '../api/upstreamIntelligence'
import OwnerRoutingPreferenceCard from '../components/OwnerRoutingPreferenceCard'
import { friendlyErrorMessage } from '../api/errors'
import { getLocale, t } from '../i18n'
import { useLocaleVersion } from '../i18n/react'
import {
  modelMarketModels,
  modelMarketPriceCohort,
  priceKey,
  type ModelMarketSort,
} from './modelMarketView'

function statusColor(status: OwnerModelMarketStatus) {
  if (status === 'ready') return 'green'
  if (status === 'price_only') return 'blue'
  return 'orange'
}

function freshnessColor(value: string) {
  if (value === 'current') return 'green'
  if (value === 'stale') return 'orange'
  return 'red'
}

function number(value: string | null | undefined) {
  if (!value) return undefined
  const parsed = Number(value)
  return Number.isFinite(parsed) ? parsed : undefined
}

function percent(value: string | null | undefined) {
  const parsed = number(value)
  if (parsed === undefined) return t('modelMarket.common.unknown')
  return `${new Intl.NumberFormat(getLocale() === 'zh' ? 'zh-CN' : 'en-US', {
    maximumFractionDigits: 2,
  }).format(parsed * 100)}%`
}

function milliseconds(value: string | null | undefined) {
  const parsed = number(value)
  if (parsed === undefined) return t('modelMarket.common.unknown')
  if (parsed >= 1000) return `${(parsed / 1000).toFixed(parsed >= 10_000 ? 1 : 2)}s`
  return `${Math.round(parsed)}ms`
}

function compactDecimal(value: string | null) {
  const parsed = number(value)
  if (parsed === undefined) return t('modelMarket.common.unknown')
  return new Intl.NumberFormat(getLocale() === 'zh' ? 'zh-CN' : 'en-US', {
    maximumSignificantDigits: 6,
  }).format(parsed)
}

function priceText(price: OwnerModelMarketPrice) {
  const minimum = compactDecimal(price.minimum_cost)
  const maximum = compactDecimal(price.maximum_cost)
  const range = minimum === maximum ? minimum : `${minimum} – ${maximum}`
  const unit =
    price.dimension === 'request'
      ? t('modelMarket.price.perRequest')
      : t('modelMarket.price.perTokens', undefined, {
          count: new Intl.NumberFormat(getLocale() === 'zh' ? 'zh-CN' : 'en-US', {
            notation: 'compact',
          }).format(price.per_tokens),
        })
  return `${range} ${price.currency} ${unit}`
}

function ModelCard({ model }: { model: OwnerModelMarketModel }) {
  const quality = model.best_quality
  return (
    <ProCard bordered className="model-market-card">
      <Space direction="vertical" size="middle" style={{ width: '100%' }}>
        <div className="model-market-card-header">
          <div>
            <Typography.Title level={4} copyable={{ text: model.model_key }}>
              {model.model_key}
            </Typography.Title>
            <Space size={[4, 4]} wrap>
              <Tag color={statusColor(model.status)}>
                {t(`modelMarket.status.${model.status}`, model.status)}
              </Tag>
              {model.frontier_option_count > 0 ? (
                <Tag color="cyan" icon={<ThunderboltOutlined />}>
                  {t('modelMarket.card.frontier', undefined, {
                    count: model.frontier_option_count,
                  })}
                </Tag>
              ) : null}
              {model.freshest_evidence ? (
                <Tag color={freshnessColor(model.freshest_evidence)}>
                  {t(
                    `upstreamIntelligence.freshness.${model.freshest_evidence}`,
                    model.freshest_evidence,
                  )}
                </Tag>
              ) : null}
            </Space>
          </div>
          <Typography.Text type="secondary">
            {t('modelMarket.card.options', undefined, {
              trusted: model.comparable_offer_count,
              observed: model.observed_offer_count,
            })}
          </Typography.Text>
        </div>

        <div className="model-market-price-list">
          {model.prices.length ? (
            model.prices.map((price) => (
              <div key={priceKey(price)} className="model-market-price-row">
                <span>
                  {t(`upstreamIntelligence.priceDimensions.${price.dimension}`, price.dimension)}
                </span>
                <Typography.Text strong>{priceText(price)}</Typography.Text>
                <Typography.Text type="secondary">
                  {t('modelMarket.card.trustedOptions', undefined, {
                    count: price.trusted_option_count,
                  })}
                </Typography.Text>
              </div>
            ))
          ) : (
            <Typography.Text type="secondary">
              {t('modelMarket.card.noComparablePrice')}
            </Typography.Text>
          )}
        </div>

        {quality ? (
          <div className="model-market-quality-grid">
            <Statistic
              title={t('modelMarket.card.successRate')}
              value={percent(quality.success_rate)}
            />
            <Statistic
              title={t('modelMarket.card.ttftP95')}
              value={milliseconds(quality.ttft_p95_ms)}
            />
            <Statistic
              title={t('modelMarket.card.durationP95')}
              value={milliseconds(quality.duration_p95_ms)}
            />
            <div>
              <Typography.Text type="secondary">
                {t('modelMarket.card.qualityScore')}
              </Typography.Text>
              <Progress
                percent={number(quality.quality_score)}
                size="small"
                status={quality.health_state === 'healthy' ? 'normal' : 'exception'}
              />
            </div>
          </div>
        ) : (
          <Alert
            type="info"
            showIcon
            message={t('modelMarket.card.qualityPending')}
            description={t('modelMarket.card.qualityPendingDescription')}
          />
        )}
      </Space>
    </ProCard>
  )
}

export default function ModelMarket() {
  useLocaleVersion()
  const [query, setQuery] = useState('')
  const [status, setStatus] = useState<OwnerModelMarketStatus>()
  const [dimension, setDimension] = useState<IntelligencePriceDimension>()
  const [sort, setSort] = useState<ModelMarketSort>('recommended')
  const [serverQuery, setServerQuery] = useState('')
  useEffect(() => {
    const timer = window.setTimeout(() => setServerQuery(query.trim()), 250)
    return () => window.clearTimeout(timer)
  }, [query])
  const market = useOwnerModelMarket({
    q: serverQuery || undefined,
    price_dimension: dimension,
    limit: 500,
  })
  const priceCohort = useMemo(
    () => modelMarketPriceCohort(market.data?.models ?? [], dimension),
    [dimension, market.data?.models],
  )
  const effectiveSort = !priceCohort && sort === 'lowest_price' ? 'recommended' : sort
  const models = useMemo(
    () =>
      modelMarketModels(market.data?.models ?? [], {
        query,
        status,
        sort: effectiveSort,
        priceCohort,
      }),
    [effectiveSort, market.data?.models, priceCohort, query, status],
  )

  return (
    <PageContainer title={t('modelMarket.page.title')} subTitle={t('modelMarket.page.subtitle')}>
      <Alert
        type="info"
        showIcon
        className="model-market-honesty-banner"
        message={t('modelMarket.page.honestyTitle')}
        description={t('modelMarket.page.honestyDescription')}
      />

      <OwnerRoutingPreferenceCard />

      {market.error ? (
        <Alert
          type="error"
          showIcon
          style={{ marginBottom: 16 }}
          message={t('modelMarket.page.loadError')}
          description={friendlyErrorMessage(market.error)}
          action={<Button onClick={() => market.refetch()}>{t('common.retry')}</Button>}
        />
      ) : null}

      {market.isLoading ? (
        <Skeleton active paragraph={{ rows: 10 }} />
      ) : market.data ? (
        <Space direction="vertical" size="large" style={{ width: '100%' }}>
          <div className="model-market-summary-grid">
            <ProCard bordered>
              <Statistic
                title={t('modelMarket.metrics.models')}
                value={market.data.metrics.model_count}
              />
            </ProCard>
            <ProCard bordered>
              <Statistic
                title={t('modelMarket.metrics.ready')}
                value={market.data.metrics.ready_model_count}
                prefix={<CheckCircleOutlined />}
                valueStyle={{
                  color: market.data.metrics.ready_model_count ? '#389e0d' : undefined,
                }}
              />
            </ProCard>
            <ProCard bordered>
              <Statistic
                title={t('modelMarket.metrics.comparableOffers')}
                value={market.data.metrics.comparable_offer_count}
              />
            </ProCard>
            <ProCard bordered>
              <Statistic
                title={t('modelMarket.metrics.qualityCovered')}
                value={market.data.metrics.quality_covered_model_count}
                suffix={`/ ${market.data.metrics.model_count}`}
              />
            </ProCard>
          </div>

          {market.data.truncated ? (
            <Alert type="warning" showIcon message={t('modelMarket.page.truncated')} />
          ) : null}

          <ProCard bordered>
            <div
              className="model-market-filter-grid"
              role="group"
              aria-label={t('modelMarket.filters.group')}
            >
              <Input
                allowClear
                prefix={<SearchOutlined />}
                aria-label={t('modelMarket.filters.search')}
                placeholder={t('modelMarket.filters.searchPlaceholder')}
                value={query}
                onChange={(event) => setQuery(event.target.value)}
              />
              <Select
                allowClear
                aria-label={t('modelMarket.filters.dimension')}
                placeholder={t('modelMarket.filters.allDimensions')}
                value={dimension}
                options={(['input', 'output', 'cached_input', 'request'] as const).map((value) => ({
                  value,
                  label: t(`upstreamIntelligence.priceDimensions.${value}`, value),
                }))}
                onChange={(value) => {
                  setDimension(value)
                  if (sort === 'lowest_price') setSort('recommended')
                }}
              />
              <Select
                allowClear
                aria-label={t('modelMarket.filters.status')}
                placeholder={t('modelMarket.filters.allStatuses')}
                value={status}
                options={(['ready', 'price_only', 'insufficient_evidence'] as const).map(
                  (value) => ({
                    value,
                    label: t(`modelMarket.status.${value}`, value),
                  }),
                )}
                onChange={setStatus}
              />
              <Select
                aria-label={t('modelMarket.filters.sort')}
                value={effectiveSort}
                options={(
                  ['recommended', 'lowest_price', 'quality', 'success', 'latency'] as const
                ).map((value) => ({
                  value,
                  disabled: value === 'lowest_price' && !priceCohort,
                  label: t(`modelMarket.sort.${value}`, value),
                }))}
                onChange={setSort}
              />
              <Button
                icon={<ReloadOutlined />}
                loading={market.isFetching}
                onClick={() => market.refetch()}
              >
                {t('modelMarket.common.refresh')}
              </Button>
            </div>
          </ProCard>

          <Typography.Text type="secondary">
            {t('modelMarket.page.resultCount', undefined, {
              visible: models.length,
              total: market.data.returned_count,
            })}
          </Typography.Text>

          {models.length ? (
            <div className="model-market-grid">
              {models.map((model) => (
                <ModelCard key={model.model_key} model={model} />
              ))}
            </div>
          ) : (
            <Empty description={t('modelMarket.page.empty')}>
              {market.data.metrics.model_count === 0 ? (
                <Link to="/onboarding">
                  <Button type="primary">{t('modelMarket.page.goToOnboarding')}</Button>
                </Link>
              ) : null}
            </Empty>
          )}

          <Typography.Text type="secondary">
            {t('modelMarket.page.snapshot', undefined, {
              version: `v${market.data.fact_version}`,
              time: new Date(market.data.generated_at).toLocaleString(
                getLocale() === 'zh' ? 'zh-CN' : 'en-US',
              ),
            })}
          </Typography.Text>
        </Space>
      ) : null}
    </PageContainer>
  )
}
