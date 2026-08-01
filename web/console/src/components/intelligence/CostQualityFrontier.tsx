import { useId, useState } from 'react'
import { Alert, Empty, Select, Space, Tag, Typography } from 'antd'
import type { IntelligenceFrontierPoint } from '../../api/upstreamIntelligence'
import { getLocale, t } from '../../i18n'
import { useLocaleVersion } from '../../i18n/react'

const chartWidth = 720
const chartHeight = 360
const chartMargin = { top: 24, right: 30, bottom: 62, left: 76 }

interface FrontierChartDatum {
  point: IntelligenceFrontierPoint
  cost: number
  quality: number
}

interface FrontierChartCohort {
  key: string
  label: string
  points: IntelligenceFrontierPoint[]
}

function qualityEvidenceText(point: IntelligenceFrontierPoint) {
  const evidence = point.quality_evidence
  if (!evidence) return t('upstreamIntelligence.frontier.qualityUnknown')
  return t('upstreamIntelligence.frontier.qualityDetail', undefined, {
    samples: evidence.quality_sample_count,
    minimum: evidence.minimum_sample_count,
    freshness: t(`upstreamIntelligence.freshness.${evidence.freshness}`, evidence.freshness),
    time: new Date(evidence.observed_at).toLocaleString(getLocale() === 'zh' ? 'zh-CN' : 'en-US'),
  })
}

function effectiveCostText(point: IntelligenceFrontierPoint) {
  const cost = point.rate.effective_unit_cost
  if (cost === null) return t('upstreamIntelligence.common.unknown')
  const currency = point.rate.settlement_currency
  return `${cost}${currency ? ` ${currency}` : ''} / ${point.rate.per_tokens}`
}

function cohortKey(point: IntelligenceFrontierPoint) {
  return JSON.stringify([
    point.rate.model_key,
    point.rate.price_dimension,
    point.rate.settlement_currency ?? '',
    point.rate.per_tokens,
  ])
}

function cohortLabel(point: IntelligenceFrontierPoint) {
  const currency = point.rate.settlement_currency || t('upstreamIntelligence.common.unknown')
  const dimension = t(
    `upstreamIntelligence.priceDimensions.${point.rate.price_dimension}`,
    point.rate.price_dimension,
  )
  return `${point.rate.model_key} · ${dimension} · ${currency} / ${point.rate.per_tokens}`
}

function buildChartCohorts(points: IntelligenceFrontierPoint[]): FrontierChartCohort[] {
  const cohorts = new Map<string, FrontierChartCohort>()
  points.forEach((point) => {
    const key = cohortKey(point)
    const cohort = cohorts.get(key)
    if (cohort) {
      cohort.points.push(point)
      return
    }
    cohorts.set(key, { key, label: cohortLabel(point), points: [point] })
  })
  return Array.from(cohorts.values())
}

function chartNumber(value: string | null, minimum: number, maximum: number) {
  if (value === null || !/^-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?$/.test(value)) return null
  const parsed = Number(value)
  return Number.isFinite(parsed) && parsed >= minimum && parsed <= maximum ? parsed : null
}

function chartDatum(point: IntelligenceFrontierPoint): FrontierChartDatum | null {
  const cost = chartNumber(point.rate.effective_unit_cost, 0, Number.MAX_VALUE)
  const quality = chartNumber(point.quality_score, 0, 100)
  return cost === null || quality === null ? null : { point, cost, quality }
}

function formatAxisNumber(value: number) {
  return new Intl.NumberFormat(getLocale() === 'zh' ? 'zh-CN' : 'en-US', {
    maximumSignificantDigits: 4,
  }).format(value)
}

function chartStatus(point: IntelligenceFrontierPoint) {
  if (point.status !== 'eligible') return 'blocked'
  return point.on_frontier ? 'frontier' : 'comparable'
}

function pointStatusText(point: IntelligenceFrontierPoint) {
  switch (chartStatus(point)) {
    case 'frontier':
      return t('upstreamIntelligence.frontier.frontierCandidate')
    case 'comparable':
      return t('upstreamIntelligence.frontier.notFrontier')
    default:
      return t('upstreamIntelligence.frontier.blocked')
  }
}

function FrontierLegend() {
  return (
    <div
      className="intelligence-frontier-chart-legend"
      role="list"
      aria-label={t('upstreamIntelligence.frontier.chartLegend')}
    >
      <span role="listitem">
        <span className="intelligence-frontier-legend-mark is-frontier" aria-hidden="true" />
        {t('upstreamIntelligence.frontier.legendFrontier')}
      </span>
      <span role="listitem">
        <span className="intelligence-frontier-legend-mark is-comparable" aria-hidden="true" />
        {t('upstreamIntelligence.frontier.legendComparable')}
      </span>
      <span role="listitem">
        <span className="intelligence-frontier-legend-mark is-blocked" aria-hidden="true" />
        {t('upstreamIntelligence.frontier.legendBlocked')}
      </span>
    </div>
  )
}

function FrontierChart({ cohort }: { cohort: FrontierChartCohort }) {
  const id = useId().replace(/:/g, '')
  const datums = cohort.points
    .map(chartDatum)
    .filter((value): value is FrontierChartDatum => Boolean(value))
  const excluded = cohort.points.length - datums.length
  const frontier = datums.filter((datum) => chartStatus(datum.point) === 'frontier')
  const comparable = datums.filter((datum) => chartStatus(datum.point) === 'comparable')
  const blocked = datums.filter((datum) => chartStatus(datum.point) === 'blocked')
  const description = t('upstreamIntelligence.frontier.chartDescription', undefined, {
    cohort: cohort.label,
    frontier: frontier.length,
    comparable: comparable.length,
    blocked: blocked.length,
    excluded,
  })

  if (!datums.length) {
    return (
      <Alert
        type="info"
        showIcon
        message={t('upstreamIntelligence.frontier.chartUnavailableTitle')}
        description={t('upstreamIntelligence.frontier.chartUnavailableDescription')}
      />
    )
  }

  const costs = datums.map((datum) => datum.cost)
  const minimumCost = Math.min(...costs)
  const maximumCost = Math.max(...costs)
  const costPadding =
    minimumCost === maximumCost
      ? Math.max(Math.abs(minimumCost) * 0.1, 1)
      : (maximumCost - minimumCost) * 0.06
  const domainMinimumCost = Math.max(0, minimumCost - costPadding)
  const domainMaximumCost = maximumCost + costPadding
  const plotWidth = chartWidth - chartMargin.left - chartMargin.right
  const plotHeight = chartHeight - chartMargin.top - chartMargin.bottom
  const x = (value: number) =>
    chartMargin.left +
    ((value - domainMinimumCost) / (domainMaximumCost - domainMinimumCost)) * plotWidth
  const y = (value: number) => chartMargin.top + ((100 - value) / 100) * plotHeight
  const xTicks = [domainMinimumCost, (domainMinimumCost + domainMaximumCost) / 2, domainMaximumCost]
  const yTicks = [0, 25, 50, 75, 100]
  const frontierPolyline = [...frontier]
    .sort((left, right) => left.cost - right.cost || left.quality - right.quality)
    .map((datum) => `${x(datum.cost)},${y(datum.quality)}`)
    .join(' ')

  return (
    <>
      <div
        className="intelligence-frontier-chart-region"
        role="region"
        aria-label={t('upstreamIntelligence.frontier.chartRegionLabel')}
        tabIndex={0}
      >
        <svg
          className="intelligence-frontier-chart"
          viewBox={`0 0 ${chartWidth} ${chartHeight}`}
          role="img"
          aria-label={t('upstreamIntelligence.frontier.chartTitle')}
          aria-describedby={`frontier-chart-description-${id}`}
        >
          <title id={`frontier-chart-title-${id}`}>
            {t('upstreamIntelligence.frontier.chartTitle')}
          </title>
          <desc id={`frontier-chart-description-${id}`}>{description}</desc>
          <rect
            className="intelligence-frontier-chart-background"
            x={chartMargin.left}
            y={chartMargin.top}
            width={plotWidth}
            height={plotHeight}
          />
          {yTicks.map((tick) => (
            <g key={`y-${tick}`}>
              <line
                className="intelligence-frontier-chart-grid"
                x1={chartMargin.left}
                x2={chartWidth - chartMargin.right}
                y1={y(tick)}
                y2={y(tick)}
              />
              <text
                className="intelligence-frontier-chart-tick"
                x={chartMargin.left - 12}
                y={y(tick) + 4}
                textAnchor="end"
              >
                {tick}
              </text>
            </g>
          ))}
          {xTicks.map((tick, index) => (
            <g key={`x-${index}`}>
              <line
                className="intelligence-frontier-chart-grid"
                x1={x(tick)}
                x2={x(tick)}
                y1={chartMargin.top}
                y2={chartHeight - chartMargin.bottom}
              />
              <text
                className="intelligence-frontier-chart-tick"
                x={x(tick)}
                y={chartHeight - chartMargin.bottom + 22}
                textAnchor="middle"
              >
                {formatAxisNumber(tick)}
              </text>
            </g>
          ))}
          <text
            className="intelligence-frontier-chart-axis-label"
            x={chartMargin.left + plotWidth / 2}
            y={chartHeight - 12}
            textAnchor="middle"
          >
            {t('upstreamIntelligence.frontier.axisCost')}
          </text>
          <text
            className="intelligence-frontier-chart-axis-label"
            transform={`translate(18 ${chartMargin.top + plotHeight / 2}) rotate(-90)`}
            textAnchor="middle"
          >
            {t('upstreamIntelligence.frontier.axisQuality')}
          </text>
          {frontier.length > 1 ? (
            <polyline
              className="intelligence-frontier-chart-line"
              points={frontierPolyline}
              fill="none"
              aria-hidden="true"
            />
          ) : null}
          {datums.map((datum) => {
            const status = chartStatus(datum.point)
            const label = t('upstreamIntelligence.frontier.chartPointLabel', undefined, {
              source: datum.point.rate.source.display_name,
              cost: effectiveCostText(datum.point),
              quality: datum.point.quality_score ?? t('upstreamIntelligence.common.unknown'),
              status: pointStatusText(datum.point),
            })
            const centerX = x(datum.cost)
            const centerY = y(datum.quality)
            return (
              <g
                key={`${datum.point.rate.observation_id}:${datum.point.channel_id ?? 'unlinked'}`}
                data-chart-status={status}
              >
                <title>{label}</title>
                {status === 'blocked' ? (
                  <>
                    <polygon
                      className="intelligence-frontier-chart-point is-blocked"
                      points={`${centerX},${centerY - 8} ${centerX + 8},${centerY} ${centerX},${centerY + 8} ${centerX - 8},${centerY}`}
                    />
                    <line
                      className="intelligence-frontier-chart-blocked-mark"
                      x1={centerX - 3}
                      x2={centerX + 3}
                      y1={centerY - 3}
                      y2={centerY + 3}
                    />
                    <line
                      className="intelligence-frontier-chart-blocked-mark"
                      x1={centerX + 3}
                      x2={centerX - 3}
                      y1={centerY - 3}
                      y2={centerY + 3}
                    />
                  </>
                ) : (
                  <circle
                    className={`intelligence-frontier-chart-point is-${status}`}
                    cx={centerX}
                    cy={centerY}
                    r={status === 'frontier' ? 7 : 6}
                  />
                )}
              </g>
            )
          })}
        </svg>
      </div>
      {excluded ? (
        <Typography.Text type="secondary">
          {t('upstreamIntelligence.frontier.chartExcluded', undefined, { count: excluded })}
        </Typography.Text>
      ) : null}
    </>
  )
}

export function CostQualityFrontier({ points }: { points: IntelligenceFrontierPoint[] }) {
  useLocaleVersion()
  const cohorts = buildChartCohorts(points)
  const [requestedCohort, setRequestedCohort] = useState<string>()
  if (!points.length) return <Empty description={t('upstreamIntelligence.frontier.empty')} />
  const eligible = points.some((point) => point.status === 'eligible' && point.on_frontier)
  const selectedCohort = cohorts.find((cohort) => cohort.key === requestedCohort) ?? cohorts[0]
  return (
    <Space direction="vertical" style={{ width: '100%' }}>
      {!eligible ? (
        <Alert
          type="info"
          showIcon
          message={t('upstreamIntelligence.frontier.unavailableTitle')}
          description={t('upstreamIntelligence.frontier.unavailableDescription')}
        />
      ) : null}
      <section
        className="intelligence-frontier-visual"
        aria-label={t('upstreamIntelligence.frontier.chartSectionLabel')}
      >
        <div className="intelligence-frontier-chart-header">
          <div>
            <Typography.Title level={5} style={{ margin: 0 }}>
              {t('upstreamIntelligence.frontier.chartTitle')}
            </Typography.Title>
            <Typography.Text type="secondary">
              {t('upstreamIntelligence.frontier.chartHelp')}
            </Typography.Text>
          </div>
          {cohorts.length > 1 ? (
            <Select
              className="intelligence-frontier-cohort-select"
              aria-label={t('upstreamIntelligence.frontier.selectCohort')}
              value={selectedCohort.key}
              options={cohorts.map((cohort) => ({ value: cohort.key, label: cohort.label }))}
              onChange={setRequestedCohort}
            />
          ) : (
            <Typography.Text className="intelligence-frontier-cohort-label">
              {selectedCohort.label}
            </Typography.Text>
          )}
        </div>
        <FrontierLegend />
        <FrontierChart cohort={selectedCohort} />
      </section>
      <div
        className="intelligence-frontier-table-region"
        role="region"
        aria-label={t('upstreamIntelligence.frontier.regionLabel')}
        tabIndex={0}
      >
        <table className="intelligence-frontier-table">
          <caption>{t('upstreamIntelligence.frontier.caption')}</caption>
          <thead>
            <tr>
              <th scope="col">{t('upstreamIntelligence.frontier.sourceModel')}</th>
              <th scope="col">{t('upstreamIntelligence.frontier.effectiveCost')}</th>
              <th scope="col">{t('upstreamIntelligence.frontier.quality')}</th>
              <th scope="col">{t('upstreamIntelligence.frontier.qualityEvidence')}</th>
              <th scope="col">{t('upstreamIntelligence.frontier.mapping')}</th>
              <th scope="col">{t('upstreamIntelligence.frontier.comparability')}</th>
              <th scope="col">{t('upstreamIntelligence.frontier.paretoStatus')}</th>
              <th scope="col">{t('upstreamIntelligence.frontier.blockers')}</th>
            </tr>
          </thead>
          <tbody>
            {points.map((point) => (
              <tr key={`${point.rate.observation_id}:${point.channel_id ?? 'unlinked'}`}>
                <th scope="row">
                  <Typography.Text strong>{point.rate.source.display_name}</Typography.Text>
                  <span className="intelligence-frontier-row-detail">
                    {point.rate.model_key} · {point.rate.group_key}
                  </span>
                </th>
                <td>{effectiveCostText(point)}</td>
                <td>{point.quality_score ?? t('upstreamIntelligence.common.unknown')}</td>
                <td>{qualityEvidenceText(point)}</td>
                <td>
                  <Tag color={point.link_state === 'linked' ? 'green' : 'default'}>
                    {point.link_state === 'linked'
                      ? t('upstreamIntelligence.frontier.linked')
                      : t('upstreamIntelligence.frontier.unlinked')}
                  </Tag>
                </td>
                <td>
                  <Tag color={point.status === 'eligible' ? 'green' : 'orange'}>
                    {point.status === 'eligible'
                      ? t('upstreamIntelligence.frontier.comparable')
                      : t('upstreamIntelligence.frontier.blocked')}
                  </Tag>
                </td>
                <td>
                  {point.on_frontier ? (
                    <Typography.Text type="success">
                      {t('upstreamIntelligence.frontier.frontierCandidate')}
                    </Typography.Text>
                  ) : (
                    <Typography.Text type="secondary">
                      {t('upstreamIntelligence.frontier.notFrontier')}
                    </Typography.Text>
                  )}
                </td>
                <td>
                  {point.blocked_reasons.length ? (
                    <Space size={[0, 4]} wrap>
                      {point.blocked_reasons.map((reason) => (
                        <Tag key={reason}>
                          {t(`upstreamIntelligence.comparabilityReasons.${reason}`, reason)}
                        </Tag>
                      ))}
                    </Space>
                  ) : (
                    <Typography.Text type="secondary">
                      {t('upstreamIntelligence.common.none')}
                    </Typography.Text>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </Space>
  )
}
